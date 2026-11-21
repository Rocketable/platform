package harness

import (
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
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
	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

const (
	defaultWebFetchTimeout = 30 * time.Second
	maxWebFetchTimeout     = 120 * time.Second
	maxWebFetchRedirects   = 10
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
- Returns text only: images, PDFs and other non-text responses are refused
- The result also carries the final url, status, status_text, content_type, headers, redirects, request and timing_ms`

type webFetchPage struct {
	URL         string             `json:"url"`
	Status      int                `json:"status"`
	StatusText  string             `json:"status_text"`
	ContentType string             `json:"content_type"`
	Headers     map[string]string  `json:"headers"`
	Redirects   []webFetchRedirect `json:"redirects"`
	Request     webFetchRequest    `json:"request"`
	TimingMS    webFetchTiming     `json:"timing_ms"`
	Output      string             `json:"output"`
	Error       string             `json:"error,omitempty"`
}

type webFetchRedirect struct {
	URL      string `json:"url"`
	Status   int    `json:"status"`
	Location string `json:"location"`
}

type webFetchRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

type webFetchTiming struct {
	Total     int64 `json:"total"`
	FirstByte int64 `json:"first_byte"`
}

type webFetchRedirectReview struct {
	OriginalURL      string             `json:"original_url"`
	OriginalApproval string             `json:"original_url_approved_by"`
	Redirects        []webFetchRedirect `json:"redirects"`
	Hop              string             `json:"hop"`
	ChangesSite      bool               `json:"changes_site"`
	ScriptLine       int                `json:"script_line,omitempty"`
	ScriptCode       string             `json:"script_code,omitempty"`
}

type approvedRedirectContext struct{}

type publicHopContext struct{}

type webFetchResult struct {
	page webFetchPage
}

func (w *webFetchResult) String() string       { return w.page.Output }
func (w *webFetchResult) Text() string         { return w.page.Output }
func (w *webFetchResult) Type() string         { return "webfetch_result" }
func (w *webFetchResult) Freeze()              {}
func (w *webFetchResult) Truth() starlark.Bool { return w.page.Output != "" }
func (w *webFetchResult) Ok() bool             { return true }

func (w *webFetchResult) Len() int                   { return len(w.page.Output) }
func (w *webFetchResult) Index(i int) starlark.Value { return codemode.TextIndex(w, i) }
func (w *webFetchResult) Slice(start, end, step int) starlark.Value {
	return codemode.TextSlice(w, start, end, step)
}

func (w *webFetchResult) Binary(op syntax.Token, y starlark.Value, side starlark.Side) (starlark.Value, error) {
	z, err := codemode.TextBinary(w, op, y, side)
	if err != nil {
		return nil, fmt.Errorf("%w", err)
	}

	return z, nil
}

func (w *webFetchResult) Hash() (uint32, error) {
	hash, _ := starlark.String(w.page.Output).Hash()

	return hash, nil
}

func (w *webFetchResult) Failure() *codemode.Error {
	if w.page.Error == "" {
		return nil
	}

	details := starlark.StringDict{}
	for _, name := range webFetchFields() {
		details[name], _ = w.Attr(name)
	}

	return codemode.NewFailure("webfetch", w.page.Error, details)
}

func (w *webFetchResult) MarshalJSON() ([]byte, error) {
	encoded, err := jsonv2.Marshal(w.page, jsonv2.Deterministic(true))
	if err != nil {
		return nil, fmt.Errorf("encode webfetch_result: %w", err)
	}

	return encoded, nil
}

func webFetchFields() []string {
	return []string{"content_type", "headers", "output", "redirects", "request", "status", "status_text", "timing_ms", "url"}
}

func (w *webFetchResult) Attr(name string) (starlark.Value, error) {
	if v, ok := codemode.ResultAttr(w, nil, name); ok {
		return v, nil
	}

	switch name {
	case "url":
		return starlark.String(w.page.URL), nil
	case "status":
		return starlark.MakeInt(w.page.Status), nil
	case "status_text":
		return starlark.String(w.page.StatusText), nil
	case "content_type":
		return starlark.String(w.page.ContentType), nil
	case "output":
		return starlark.String(w.page.Output), nil
	case "headers":
		return webFetchValue(w.page.Headers)
	case "redirects":
		return webFetchValue(w.page.Redirects)
	case "request":
		return webFetchValue(w.page.Request)
	case "timing_ms":
		return webFetchValue(w.page.TimingMS)
	}

	method, _ := starlark.String(w.page.Output).Attr(name)

	return method, nil
}

func webFetchValue(field any) (starlark.Value, error) {
	encoded, err := jsonv2.Marshal(field, jsonv2.Deterministic(true))
	if err != nil {
		return nil, fmt.Errorf("encode webfetch_result field: %w", err)
	}

	value, err := starlark.Call(&starlark.Thread{Name: "webfetch"}, starjson.Module.Members["decode"], starlark.Tuple{starlark.String(encoded)}, nil)
	if err != nil {
		return nil, fmt.Errorf("decode webfetch_result field: %w", err)
	}

	return value, nil
}

func (w *webFetchResult) AttrNames() []string {
	return codemode.ResultAttrNames(append(webFetchFields(), starlark.String("").AttrNames()...)...)
}

var (
	_ codemode.Result    = (*webFetchResult)(nil)
	_ codemode.Text      = (*webFetchResult)(nil)
	_ starlark.HasAttrs  = (*webFetchResult)(nil)
	_ starlark.Sliceable = (*webFetchResult)(nil)
	_ starlark.HasBinary = (*webFetchResult)(nil)
)

func webFetch(ctx context.Context, client *http.Client, params webFetchToolParams) (ToolResult, error) {
	rawURL, _, _ := strings.Cut(params.URL, "#")
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return ToolResult{}, errors.New("URL must start with http:// or https://")
	}

	if u, err := url.Parse(rawURL); err == nil && u.User != nil {
		return ToolResult{}, errors.New("URL must not include credentials")
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

	noFollow := *client
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	start := time.Now()
	deadline := start.Add(min(timeout, maxWebFetchTimeout))

	var page webFetchPage

	finish := func(failure string) (ToolResult, error) {
		page.Error, page.TimingMS.Total = failure, time.Since(start).Milliseconds()
		result := &webFetchResult{page: page}

		return ToolResult{Output: cmp.Or(page.Error, page.Output), Data: result}, nil
	}

	target, hopCtx, honestUA := rawURL, ctx, false

	var (
		resp    *http.Response
		cancels []context.CancelFunc
	)

	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()

	for {
		requestCtx, cancel := context.WithDeadline(hopCtx, deadline)
		cancels = append(cancels, cancel)

		var err error

		resp, err = doWebFetchRequest(requestCtx, &noFollow, target, format, honestUA)
		if err != nil {
			if page.Status == 0 {
				return ToolResult{}, err
			}

			return finish(err.Error())
		}

		page.URL, page.Status, page.ContentType = resp.Request.URL.String(), resp.StatusCode, resp.Header.Get("Content-Type")
		page.StatusText = strings.TrimSpace(strings.TrimPrefix(resp.Status, strconv.Itoa(resp.StatusCode)))
		page.Headers = webFetchHeaders(resp.Header)
		page.Request = webFetchRequest{Method: resp.Request.Method, URL: page.URL, Headers: webFetchHeaders(resp.Request.Header)}
		page.TimingMS.FirstByte = time.Since(start).Milliseconds()

		if resp.StatusCode == http.StatusForbidden && resp.Header.Get("Cf-Mitigated") == "challenge" && !honestUA {
			_ = resp.Body.Close()
			honestUA = true

			continue
		}

		location, errLocation := resp.Location()
		if errLocation != nil || !slices.Contains([]int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect}, resp.StatusCode) {
			break
		}

		_ = resp.Body.Close()

		page.Redirects = append(page.Redirects, webFetchRedirect{URL: target, Status: resp.StatusCode, Location: resp.Header.Get("Location")})
		if len(page.Redirects) > maxWebFetchRedirects {
			return finish(fmt.Sprintf("too many redirects (more than %d)", maxWebFetchRedirects))
		}

		location.Fragment, location.RawFragment = "", ""

		checked := time.Now()

		if hopCtx, err = checkWebFetchRedirect(ctx, params, rawURL, location, page.Redirects); err != nil {
			return finish("redirect to " + location.Redacted() + ": " + err.Error())
		}

		deadline = deadline.Add(time.Since(checked))

		target = location.String()
	}

	defer func() { _ = resp.Body.Close() }()

	status := ""
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}

	if err := refuseNonTextWebFetch(normalizeMIME(page.ContentType)); err != nil {
		return finish(cmp.Or(status, err.Error()))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return finish(cmp.Or(status, "read body: "+err.Error()))
	}

	if err := refuseNonTextWebFetch(sniffAttachmentMIME(body, "")); err != nil {
		return finish(cmp.Or(status, err.Error()))
	}

	if page.Output, err = webFetchOutput(rawURL, format, page.ContentType, strings.ToValidUTF8(string(body), "\uFFFD")); err != nil {
		return finish(cmp.Or(status, err.Error()))
	}

	return finish(status)
}

func webFetchHeaders(header http.Header) map[string]string {
	headers := make(map[string]string, len(header))
	for name, values := range header {
		value := strings.Join(values, ", ")
		if slices.Contains([]string{"Authorization", "Cookie", "Set-Cookie"}, name) {
			value = "<redacted>"
		}

		headers[strings.ToLower(name)] = value
	}

	return headers
}

func checkWebFetchRedirect(ctx context.Context, call webFetchToolParams, original string, hop *url.URL, redirects []webFetchRedirect) (context.Context, error) {
	switch {
	case hop.Scheme != "http" && hop.Scheme != "https":
		return nil, errors.New("URL must start with http:// or https://")
	case hop.User != nil:
		return nil, errors.New("URL must not include credentials")
	}

	tc, _ := toolCallContextFrom(ctx)
	permissions := tc.looper.Permissions
	approval := "the reviewed script; listed in allowedNetworkTargets"

	var decision permissionDecision

	named := false

	if permissions.Easy {
		if targets := tc.looper.agent.NetworkTargets; targets == nil || networkTargetAllowed(targets, hop) {
			return ctx, nil
		}
	} else {
		decision = permissions.evaluate("webfetch", hop.String())
		named = decision.Matched && ruleNamesHost(decision.Rule.Pattern, hop)

		if decision.Matched && decision.Action == permissionDeny {
			return nil, permissionDeniedError(formatPermissionDenied(&decision))
		}
	}

	if !named {
		addrs, _ := net.DefaultResolver.LookupNetIP(ctx, "ip", hop.Hostname())
		for _, addr := range addrs {
			if publicAddressOnly("", netip.AddrPortFrom(addr, 0).String(), nil) != nil {
				return nil, fmt.Errorf("%s: private address, not named in this agent's rules", addr.Unmap())
			}
		}

		ctx = context.WithValue(ctx, publicHopContext{}, true)
	}

	if !permissions.Easy {
		if decision.Matched && decision.Action == permissionAllow {
			return ctx, nil
		}

		approval = "automatic review"
		if first := permissions.evaluate("webfetch", original); first.Action == permissionAllow {
			approval = fmt.Sprintf("rule %q => allow", first.Rule.Pattern)
		}
	}

	if tc.looper.InPermissionReview {
		return nil, permissionDeniedError(recursiveReviewDenied)
	}

	args, _ := jsonv2.Marshal(call)
	originalURL, _ := url.Parse(original)
	line, code := codemode.CallLine(ctx)
	review := &permissionReviewRequest{
		ActiveAgent:      tc.looper.agent.Name,
		ToolName:         "webfetch",
		Permission:       "webfetch",
		RawArguments:     canonicalToolArguments(args),
		Subjects:         []string{hop.String()},
		AutoSubjects:     []permissionReviewSubject{{Subject: hop.String(), RulePattern: decision.Rule.Pattern}},
		Reviewer:         decision.Rule.Reviewer,
		ReviewerEmbedded: decision.Rule.Reviewer == "",
		Redirect: &webFetchRedirectReview{
			OriginalURL: original, OriginalApproval: approval, Redirects: redirects, Hop: hop.String(),
			ChangesSite: !strings.EqualFold(originalURL.Hostname(), hop.Hostname()), ScriptLine: line, ScriptCode: code,
		},
	}

	if err := reviewNestedCall(ctx, &tc, review, ReviewTurn(ToolCallKey(ctx)+"/redirect/"+strconv.Itoa(len(redirects)))); err != nil {
		return nil, err
	}

	return context.WithValue(ctx, approvedRedirectContext{}, hop.String()), nil
}

func refuseNonTextWebFetch(mimeType string) error {
	switch {
	case isImageAttachmentMIME(mimeType):
		return errors.New("Unsupported fetched image content type: " + mimeType)
	case mimeType == "" || strings.HasPrefix(mimeType, "text/") || strings.HasSuffix(mimeType, "+json") || strings.HasSuffix(mimeType, "+xml") ||
		slices.Contains([]string{"application/json", "application/xml", "application/javascript", "application/x-javascript"}, mimeType):
		return nil
	default:
		return errors.New("Unsupported fetched file content type: " + mimeType)
	}
}

func webFetchOutput(rawURL, format, contentType, content string) (string, error) {
	if strings.Contains(normalizeMIME(contentType), "text/html") {
		switch format {
		case "markdown":
			opts := []converter.ConvertOptionFunc{}
			if u, err := url.Parse(rawURL); err == nil && u.Scheme != "" && u.Host != "" {
				opts = append(opts, converter.WithDomain(u.Scheme+"://"+u.Host))
			}

			markdown, err := htmltomarkdown.ConvertString(content, opts...)
			if err != nil {
				return "", fmt.Errorf("convert HTML to markdown: %w", err)
			}

			return markdown, nil
		case "text":
			return textFromHTMLString(content)
		}
	}

	if format == "markdown" && strings.Contains(content, "<html") {
		markdown, err := htmltomarkdown.ConvertString(content)
		if err == nil {
			return markdown, nil
		}
	}

	return content, nil
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

func easyWebFetchClient(targets []string) *http.Client {
	client := webFetchClient(targets == nil)
	client.Transport.(*http.Transport).Proxy = func(req *http.Request) (*url.URL, error) {
		if targets == nil || networkTargetAllowed(targets, req.URL) || req.Context().Value(approvedRedirectContext{}) == req.URL.String() {
			return nil, nil
		}

		return nil, fmt.Errorf("%s: not in allowedNetworkTargets", req.URL.Redacted())
	}

	return client
}

func webFetchClient(publicOnly bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		var dialer net.Dialer
		if publicOnly || ctx.Value(publicHopContext{}) != nil {
			dialer.Control = publicAddressOnly
		}

		return dialer.DialContext(ctx, network, address)
	}

	return &http.Client{Transport: transport}
}

func ruleNamesHost(pattern string, u *url.URL) bool {
	_, rest, ok := strings.Cut(pattern, "://")
	host, _, _ := strings.Cut(rest, "/")

	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}

	return ok && strings.EqualFold(strings.Trim(host, "[]"), u.Hostname())
}

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
