package harness

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
)

const (
	defaultWebFetchTimeout = 30 * time.Second
	maxWebFetchTimeout     = 120 * time.Second
)

const webFetchDescription = `- Fetches content from a specified URL
- Takes a URL and optional format as input
- Fetches the URL content, converts to requested format (markdown by default)
- Returns the content in the specified format
- Use this tool when you need to retrieve and analyze web content

Usage notes:
- The URL must be a fully-formed valid URL
- Format options: markdown (default), text, or html
- This tool is read-only and does not modify any files
- Image and PDF responses are returned as model-visible attachments`

func webFetch(ctx context.Context, client *http.Client, params webFetchToolParams) (ToolResult, error) {
	if !strings.HasPrefix(params.URL, "http://") && !strings.HasPrefix(params.URL, "https://") {
		return ToolResult{}, errors.New("URL must start with http:// or https://")
	}

	format := params.Format
	if format == "" {
		format = "markdown"
	}

	if format != "markdown" && format != "text" && format != "html" {
		return ToolResult{}, fmt.Errorf("unsupported format %q", params.Format)
	}

	timeout := defaultWebFetchTimeout
	if params.TimeoutSecond > 0 {
		timeout = time.Duration(params.TimeoutSecond) * time.Second
	}

	timeout = min(timeout, maxWebFetchTimeout)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := doWebFetchRequest(ctx, client, params.URL, format, false)
	if err != nil {
		return ToolResult{}, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("Cf-Mitigated") == "challenge" {
		_ = resp.Body.Close()

		resp, err = doWebFetchRequest(ctx, client, params.URL, format, true)
		if err != nil {
			return ToolResult{}, err
		}

		defer func() { _ = resp.Body.Close() }()
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ToolResult{}, fmt.Errorf("request failed with status %d", resp.StatusCode)
	}

	if length := resp.Header.Get("Content-Length"); length != "" {
		value, err := strconv.ParseInt(length, 10, 64)
		if err == nil && value > maxAttachmentBytes {
			return ToolResult{}, errors.New("response too large (exceeds 5MB limit)")
		}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachmentBytes+1))
	if err != nil {
		return ToolResult{}, fmt.Errorf("read limited body: %w", err)
	}

	if len(body) > maxAttachmentBytes {
		return ToolResult{}, errors.New("response too large (exceeds 5MB limit)")
	}

	return webFetchToolResult(params.URL, format, resp.Header.Get("Content-Type"), body)
}

func webFetchToolResult(rawURL, format, contentType string, body []byte) (ToolResult, error) {
	mimeType := sniffAttachmentMIME(body, contentType)
	if isSupportedAttachmentMIME(mimeType) {
		filename := "webfetch"
		if mimeType == "application/pdf" {
			filename += ".pdf"
		}

		attachment, err := attachmentFromBytes(filename, mimeType, body)
		if err != nil {
			return ToolResult{}, err
		}

		message := "Image fetched successfully"
		if mimeType == "application/pdf" {
			message = "PDF fetched successfully"
		}

		return ToolResult{Output: message, Attachments: []Attachment{attachment}}, nil
	}

	content := string(body)

	if strings.Contains(normalizeMIME(contentType), "text/html") {
		switch format {
		case "markdown":
			opts := []converter.ConvertOptionFunc{}
			if u, err := url.Parse(rawURL); err == nil && u.Scheme != "" && u.Host != "" {
				opts = append(opts, converter.WithDomain(u.Scheme+"://"+u.Host))
			}

			markdown, err := htmltomarkdown.ConvertString(content, opts...)
			if err != nil {
				return ToolResult{}, fmt.Errorf("convert HTML to markdown: %w", err)
			}

			return TextToolResult(markdown), nil
		case "text":
			text, err := textFromHTMLString(content)
			if err != nil {
				return ToolResult{}, err
			}

			return TextToolResult(text), nil
		}
	}

	if format == "markdown" && bytes.Contains(body, []byte("<html")) {
		markdown, err := htmltomarkdown.ConvertString(content)
		if err == nil {
			return TextToolResult(markdown), nil
		}
	}

	return TextToolResult(content), nil
}

func doWebFetchRequest(ctx context.Context, client *http.Client, rawURL, format string, honestUA bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"
	if honestUA {
		ua = "www.rocketable.com"
	}

	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	switch format {
	case "markdown":
		req.Header.Set("Accept", "text/markdown;q=1.0, text/x-markdown;q=0.9, text/plain;q=0.8, text/html;q=0.7, */*;q=0.1")
	case "text":
		req.Header.Set("Accept", "text/plain;q=1.0, text/markdown;q=0.9, text/html;q=0.8, */*;q=0.1")
	case "html":
		req.Header.Set("Accept", "text/html;q=1.0, application/xhtml+xml;q=0.9, text/plain;q=0.8, text/markdown;q=0.7, */*;q=0.1")
	default:
		req.Header.Set("Accept", "*/*")
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	return resp, nil
}

// easyWebFetchClient is the client of an easy agent's webfetch and of its emulated shell's curl, so both reach
// the same (R34): with targets nil any public address, else only the URLs targets lists, redirects included.
// It uses no proxy.
func easyWebFetchClient(targets []string) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Every request, each redirect's too, asks Proxy first, and an error refuses it.
	transport.Proxy = func(req *http.Request) (*url.URL, error) {
		if targets == nil || networkTargetAllowed(targets, req.URL) {
			return nil, nil
		}

		return nil, fmt.Errorf("%s: not in allowedNetworkTargets", req.URL.Redacted())
	}

	if targets == nil {
		dialer := &net.Dialer{Control: publicAddressOnly}
		transport.DialContext = dialer.DialContext
	}

	return &http.Client{Transport: transport}
}

// publicAddressOnly, a dialer's Control, refuses address, the one a name resolved to as it is dialed, when it
// is loopback, private, link-local (cloud metadata included), shared, documentation or reserved space, an IPv4
// address in IPv4-mapped, NAT64 or 6to4 form counting as itself.
func publicAddressOnly(_, address string, _ syscall.RawConn) error {
	addrPort, err := netip.ParseAddrPort(address)
	addr := addrPort.Addr().WithZone("").Unmap()
	ip := addr.As16()

	switch {
	case netip.MustParsePrefix("64:ff9b::/96").Contains(addr):
		addr = netip.AddrFrom4([4]byte(ip[12:]))
	case netip.MustParsePrefix("2002::/16").Contains(addr):
		addr = netip.AddrFrom4([4]byte(ip[2:6]))
	}

	if err != nil || slices.ContainsFunc([]string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
		"192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"::/128", "::1/128", "fe80::/10", "fc00::/7", "2001:db8::/32", "64:ff9b:1::/48",
	}, func(prefix string) bool { return netip.MustParsePrefix(prefix).Contains(addr) }) {
		return fmt.Errorf("%s: private address, not in allowedNetworkTargets", address)
	}

	return nil
}

// networkTargetAllowed reports whether targets, an agent's allowedNetworkTargets list, allows u: a host or
// host:port is its https and http origins, a URL is an origin and a path prefix. A prefix ending in "/"
// allows the paths under it; any other allows itself and the paths below it, never "/v1x" for "/v1".
func networkTargetAllowed(targets []string, u *url.URL) bool {
	origin := func(u *url.URL) string {
		port := u.Port()
		if port == map[string]string{"http": "80", "https": "443"}[u.Scheme] {
			port = ""
		}

		return u.Scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port
	}

	path := cmp.Or(u.EscapedPath(), "/")

	for _, target := range targets {
		entries := []string{target}
		if !strings.Contains(target, "://") {
			entries = []string{"https://" + target, "http://" + target}
		}

		for _, entry := range entries {
			allowed, err := url.Parse(entry)
			if err != nil || origin(allowed) != origin(u) {
				continue
			}

			prefix := allowed.EscapedPath()
			if prefix == "" || prefix == "/" || !ambiguousPath(path) && (strings.HasSuffix(prefix, "/") && strings.HasPrefix(path, prefix) || path == prefix || strings.HasPrefix(path, prefix+"/")) {
				return true
			}
		}
	}

	return false
}

// ambiguousPath reports whether escaped path, a URL's, stays out of every path prefix because decoding could
// change its separators. A dot segment counts too: a server may resolve it, but Go sends it as written.
func ambiguousPath(path string) bool {
	if slices.ContainsFunc(strings.Split(path, "/"), func(segment string) bool { return segment == "." || segment == ".." }) {
		return true
	}

	for range 3 {
		lower := strings.ToLower(path)

		decoded, err := url.PathUnescape(path)
		if err != nil || !utf8.ValidString(decoded) || strings.ContainsAny(path, `\;`) ||
			slices.ContainsFunc([]string{"%2f", "%2e", "%5c", "%3b"}, func(escape string) bool { return strings.Contains(lower, escape) }) {
			return true
		}

		if decoded == path {
			return false
		}

		path = decoded
	}

	return strings.Contains(path, "%")
}
