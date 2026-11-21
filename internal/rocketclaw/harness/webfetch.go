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
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
	"go.starlark.net/starlark"
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
- Returns text only: images, PDFs and other non-text responses are refused`

type webFetchPage struct {
	ContentType string `json:"content_type"`
	Output      string `json:"output"`
	URL         string `json:"url"`
}

type webFetchResult struct {
	page webFetchPage
}

func (w *webFetchResult) String() string       { return w.page.Output }
func (w *webFetchResult) Text() string         { return w.page.Output }
func (w *webFetchResult) Type() string         { return "webfetch_result" }
func (w *webFetchResult) Freeze()              {}
func (w *webFetchResult) Truth() starlark.Bool { return w.page.Output != "" }
func (w *webFetchResult) Ok() bool             { return true }

func (w *webFetchResult) Hash() (uint32, error) {
	hash, _ := starlark.String(w.page.Output).Hash()

	return hash, nil
}

func (w *webFetchResult) MarshalJSON() ([]byte, error) {
	encoded, err := jsonv2.Marshal(struct {
		ContentType string `json:"content_type"`
		Output      string `json:"output"`
	}{w.page.ContentType, w.page.Output})
	if err != nil {
		return nil, fmt.Errorf("encode webfetch_result: %w", err)
	}

	return encoded, nil
}

func (w *webFetchResult) Attr(name string) (starlark.Value, error) {
	if v, ok := codemode.ResultAttr(w, nil, name); ok {
		return v, nil
	}

	switch name {
	case "content_type":
		return starlark.String(w.page.ContentType), nil
	case "output":
		return starlark.String(w.page.Output), nil
	case "url":
		return starlark.String(w.page.URL), nil
	}

	method, _ := starlark.String(w.page.Output).Attr(name)

	return method, nil
}

func (w *webFetchResult) AttrNames() []string {
	return codemode.ResultAttrNames(append([]string{"content_type", "output", "url"}, starlark.String("").AttrNames()...)...)
}

var (
	_ codemode.Result   = (*webFetchResult)(nil)
	_ codemode.Text     = (*webFetchResult)(nil)
	_ starlark.HasAttrs = (*webFetchResult)(nil)
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

	timeout = min(timeout, maxWebFetchTimeout)

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tc, _ := toolCallContextFrom(ctx)
	checked := *client
	checked.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > maxWebFetchRedirects {
			return fmt.Errorf("too many redirects (more than %d)", maxWebFetchRedirects)
		}

		if req.URL.User != nil {
			return errors.New("URL must not include credentials")
		}

		req.URL.Fragment, req.URL.RawFragment = "", ""

		decision := tc.looper.Permissions.evaluate("webfetch", req.URL.String())
		if decision.Matched && decision.Action == permissionDeny {
			return permissionDeniedError(formatPermissionDenied(&decision))
		}

		return nil
	}

	resp, err := doWebFetchRequest(ctx, &checked, rawURL, format, false)
	if err != nil {
		return ToolResult{}, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("Cf-Mitigated") == "challenge" {
		_ = resp.Body.Close()

		resp, err = doWebFetchRequest(ctx, &checked, rawURL, format, true)
		if err != nil {
			return ToolResult{}, err
		}

		defer func() { _ = resp.Body.Close() }()
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ToolResult{}, fmt.Errorf("request failed with status %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if err := refuseNonTextWebFetch(normalizeMIME(contentType)); err != nil {
		return ToolResult{}, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ToolResult{}, fmt.Errorf("read body: %w", err)
	}

	if err := refuseNonTextWebFetch(sniffAttachmentMIME(body, "")); err != nil {
		return ToolResult{}, err
	}

	output, err := webFetchOutput(rawURL, format, contentType, strings.ToValidUTF8(string(body), "\uFFFD"))
	if err != nil {
		return ToolResult{}, err
	}

	return ToolResult{Output: output, Data: &webFetchResult{page: webFetchPage{ContentType: contentType, Output: output, URL: resp.Request.URL.String()}}}, nil
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
	transport := http.DefaultTransport.(*http.Transport).Clone()
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
