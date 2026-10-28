package justbash

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Rocketable/platform/internal/justbash/justbashpb"
)

// Network defaults follow just-bash's NetworkConfig (upstream src/network/fetch.ts).
const (
	defaultMaxRedirects    = 20
	defaultFetchTimeout    = 30 * time.Second
	defaultMaxResponseSize = 10 << 20
)

var errNetworkDenied = errors.New("network access denied")

// target is one allowedNetworkTargets entry: a host, an address, a range, or a URL prefix.
type target struct {
	// host is a lowercase hostname, optionally starting with "*." to match any subdomain. URL prefix
	// entries also set it to their URL's host.
	host   string
	addr   netip.Addr
	prefix netip.Prefix
	// port restricts host and addr entries; empty allows any port.
	port string
	// origin and path form a URL prefix entry.
	origin  string
	path    string
	methods []string
}

// networkRules is an agent's allowedNetworkTargets.
type networkRules struct {
	auto    bool
	targets []target
}

func parseTarget(entry string, methods []string) (target, error) {
	if strings.Contains(entry, "://") {
		u, err := url.Parse(entry)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return target{}, fmt.Errorf("%q: URL prefixes must be http(s) URLs with a host and no query, fragment, or user", entry)
		}

		path := cmp.Or(u.EscapedPath(), "/")
		if path != "/" && ambiguousPath(path) {
			return target{}, fmt.Errorf("%q: ambiguous path", entry)
		}

		return target{host: strings.ToLower(u.Hostname()), origin: origin(u), path: path, methods: methods}, nil
	}

	if prefix, err := netip.ParsePrefix(entry); err == nil {
		return target{prefix: prefix.Masked(), methods: methods}, nil
	}

	host, port := entry, ""
	if h, p, err := net.SplitHostPort(entry); err == nil {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return target{}, fmt.Errorf("%q: invalid port", entry)
		}

		host, port = h, p
	}

	if addr, err := netip.ParseAddr(host); err == nil {
		return target{addr: addr.Unmap(), port: port, methods: methods}, nil
	}

	host = strings.ToLower(host)
	if !validHostname(strings.TrimPrefix(host, "*.")) {
		return target{}, fmt.Errorf("%q: not a host, address, CIDR range, or URL prefix", entry)
	}

	return target{host: host, port: port, methods: methods}, nil
}

func validHostname(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}

	for label := range strings.SplitSeq(host, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}

		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}

	return true
}

// match reports the entry that allows a request. Under auto, the zero target allows it.
func (r networkRules) match(u *url.URL, method string) (*target, bool) {
	if r.auto {
		return &target{}, true
	}

	for i := range r.targets {
		if r.targets[i].matches(u, method) {
			return &r.targets[i], true
		}
	}

	return nil, false
}

func (t *target) matches(u *url.URL, method string) bool {
	if len(t.methods) > 0 && !slices.Contains(t.methods, method) {
		return false
	}

	host := strings.ToLower(u.Hostname())

	switch {
	case t.origin != "":
		// URL prefixes match like upstream src/network/allow-list.ts matchesAllowListEntry.
		path := cmp.Or(u.EscapedPath(), "/")
		if origin(u) != t.origin || (t.path != "/" && ambiguousPath(path)) {
			return false
		}

		return t.path == "/" || path == t.path || strings.HasPrefix(path, strings.TrimSuffix(t.path, "/")+"/")
	case t.prefix.IsValid():
		addr, err := netip.ParseAddr(host)

		return err == nil && t.prefix.Contains(addr.Unmap())
	case t.port != "" && t.port != urlPort(u):
		return false
	case t.addr.IsValid():
		addr, err := netip.ParseAddr(host)

		return err == nil && addr.Unmap() == t.addr
	case strings.HasPrefix(t.host, "*."):
		return strings.HasSuffix(host, t.host[1:])
	default:
		return host == t.host
	}
}

// names reports whether the entry lifts the block on a non-public address: it names the request's
// host exactly, or names an address or range containing addr. Wildcards and auto never do.
func (t *target) names(host string, addr netip.Addr) bool {
	switch {
	case t.addr.IsValid():
		return t.addr == addr
	case t.prefix.IsValid():
		return t.prefix.Contains(addr)
	default:
		return t.host != "" && !strings.HasPrefix(t.host, "*.") && strings.EqualFold(t.host, host)
	}
}

func origin(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if p := u.Port(); p != "" && p != defaultPort(u.Scheme) {
		host = net.JoinHostPort(host, p)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}

	return strings.ToLower(u.Scheme) + "://" + host
}

func urlPort(u *url.URL) string {
	return cmp.Or(u.Port(), defaultPort(u.Scheme))
}

func defaultPort(scheme string) string {
	if strings.EqualFold(scheme, "https") {
		return "443"
	}

	return "80"
}

// ambiguousPath rejects path forms whose meaning can change after proxy or server canonicalization,
// like upstream src/network/allow-list.ts hasAmbiguousPathSyntax.
func ambiguousPath(path string) bool {
	candidate := path
	for range 3 {
		if strings.ContainsAny(candidate, `\;`) || badEscape(candidate) {
			return true
		}

		lower := strings.ToLower(candidate)
		for _, s := range []string{"%2f", "%2e", "%5c", "%3b"} {
			if strings.Contains(lower, s) {
				return true
			}
		}

		decoded, err := url.PathUnescape(candidate)
		if err != nil {
			return true
		}

		if decoded == candidate {
			return false
		}

		candidate = decoded
	}

	return strings.Contains(candidate, "%")
}

func badEscape(s string) bool {
	for i := range len(s) {
		if s[i] == '%' && (i+2 >= len(s) || !isHex(s[i+1]) || !isHex(s[i+2])) {
			return true
		}
	}

	return false
}

func isHex(c byte) bool {
	return ('0' <= c && c <= '9') || ('a' <= c && c <= 'f') || ('A' <= c && c <= 'F')
}

// nonPublic reports loopback, private, link-local and other addresses that are not on the public
// internet, following upstream src/network/allow-list.ts isPrivateIp.
func nonPublic(addr netip.Addr) bool {
	addr = addr.Unmap()
	if addr.Is6() {
		b := addr.As16()
		switch {
		case netip.MustParsePrefix("64:ff9b::/96").Contains(addr):
			return nonPublic(netip.AddrFrom4([4]byte(b[12:16])))
		case netip.MustParsePrefix("2002::/16").Contains(addr):
			return nonPublic(netip.AddrFrom4([4]byte(b[2:6])))
		}
	}

	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() || addr.IsInterfaceLocalMulticast() {
		return true
	}

	for _, p := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "64:ff9b:1::/48"} {
		if netip.MustParsePrefix(p).Contains(addr) {
			return true
		}
	}

	return false
}

// fetch makes req for an agent with rules, following redirects itself so every hop is checked.
func fetch(ctx context.Context, rules networkRules, req *justbashpb.FetchRequest) (*justbashpb.FetchResponse, error) {
	timeout := defaultFetchTimeout
	if req.GetTimeoutMs() > 0 {
		timeout = min(timeout, time.Duration(req.GetTimeoutMs())*time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	maxRedirects := defaultMaxRedirects
	if req.GetMaxRedirects() > 0 {
		maxRedirects = min(maxRedirects, int(req.GetMaxRedirects()))
	}

	method, body, rawURL := cmp.Or(strings.ToUpper(req.GetMethod()), http.MethodGet), req.GetBody(), req.GetUrl()

	var chain []*justbashpb.Redirect

	for hop := 0; ; hop++ {
		resp, err := fetchHop(ctx, rules, method, rawURL, req.GetHeaders(), body)
		if err != nil {
			return nil, err
		}

		location := resp.Header.Get("Location")
		if !req.GetFollowRedirects() || location == "" || resp.StatusCode < 300 || resp.StatusCode > 399 {
			return readResponse(resp, rawURL, chain)
		}

		_ = resp.Body.Close()

		if hop >= maxRedirects {
			return nil, fmt.Errorf("too many redirects (%d)", maxRedirects)
		}

		chain = append(chain, &justbashpb.Redirect{Status: int32(resp.StatusCode), StatusText: http.StatusText(resp.StatusCode), Headers: flatHeaders(resp.Header)})

		base, _ := url.Parse(rawURL) // fetchHop already parsed rawURL.

		next, err := base.Parse(location)
		if err != nil {
			return nil, fmt.Errorf("redirect location %q: %w", location, err)
		}

		rawURL = next.String()

		if resp.StatusCode == http.StatusSeeOther || ((resp.StatusCode == http.StatusMovedPermanently || resp.StatusCode == http.StatusFound) && method == http.MethodPost) {
			method, body = http.MethodGet, nil
		}
	}
}

func fetchHop(ctx context.Context, rules networkRules, method, rawURL string, headers map[string]string, body []byte) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, fmt.Errorf("%w: %s: only http and https URLs with a host are allowed", errNetworkDenied, rawURL)
	}

	matched, ok := rules.match(u, method)
	if !ok {
		return nil, fmt.Errorf("%w: %s %s is not in allowedNetworkTargets", errNetworkDenied, method, rawURL)
	}

	host := u.Hostname()
	dialer := &net.Dialer{Control: func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("%w: %s: %w", errNetworkDenied, address, err)
		}

		if addr := ap.Addr().Unmap(); nonPublic(addr) && !matched.names(host, addr) {
			return fmt.Errorf("%w: %s resolves to non-public address %s", errNetworkDenied, host, addr)
		}

		return nil
	}}

	transport := &http.Transport{DialContext: dialer.DialContext, DisableKeepAlives: true, ForceAttemptHTTP2: true}
	defer transport.CloseIdleConnections()

	httpReq, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, rawURL, err)
	}

	return resp, nil
}

func readResponse(resp *http.Response, rawURL string, chain []*justbashpb.Redirect) (*justbashpb.FetchResponse, error) {
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, defaultMaxResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if len(body) > defaultMaxResponseSize {
		return nil, fmt.Errorf("response larger than %d bytes", defaultMaxResponseSize)
	}

	return &justbashpb.FetchResponse{Status: int32(resp.StatusCode), StatusText: http.StatusText(resp.StatusCode), Headers: flatHeaders(resp.Header), Body: body, Url: rawURL, RedirectChain: chain}, nil
}

func flatHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for _, k := range slices.Sorted(maps.Keys(h)) {
		out[strings.ToLower(k)] = strings.Join(h[k], ", ")
	}

	return out
}
