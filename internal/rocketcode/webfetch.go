package rocketcode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
)

const (
	defaultWebFetchTimeout = 30 * time.Second
	maxWebFetchTimeout     = 120 * time.Second
)

func webFetchDescription() string {
	return strings.Join([]string{
		"- Fetches content from a specified URL",
		"- Takes a URL and optional format as input",
		"- Fetches the URL content, converts to requested format (markdown by default)",
		"- Returns the content in the specified format",
		"- Use this tool when you need to retrieve and analyze web content",
		"",
		"Usage notes:",
		"- The URL must be a fully-formed valid URL",
		"- Format options: markdown (default), text, or html",
		"- This tool is read-only and does not modify any files",
		"- Image and PDF responses are returned as model-visible attachments",
	}, "\n")
}

func webFetch(ctx context.Context, params webFetchToolParams) (ToolResult, error) {
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
	if params.Timeout > 0 {
		timeout = time.Duration(params.Timeout) * time.Second
	}

	if timeout > maxWebFetchTimeout {
		timeout = maxWebFetchTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := doWebFetchRequest(ctx, params.URL, format, false)
	if err != nil {
		return ToolResult{}, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("Cf-Mitigated") == "challenge" {
		_ = resp.Body.Close()

		resp, err = doWebFetchRequest(ctx, params.URL, format, true)
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

	body, err := readLimited(resp.Body, maxAttachmentBytes)
	if err != nil {
		return ToolResult{}, err
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
			if origin := originForURL(rawURL); origin != "" {
				opts = append(opts, converter.WithDomain(origin))
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

func doWebFetchRequest(ctx context.Context, rawURL, format string, honestUA bool) (*http.Response, error) {
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
	req.Header.Set("Accept", webFetchAcceptHeader(format))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	return resp, nil
}

func webFetchAcceptHeader(format string) string {
	switch format {
	case "markdown":
		return "text/markdown;q=1.0, text/x-markdown;q=0.9, text/plain;q=0.8, text/html;q=0.7, */*;q=0.1"
	case "text":
		return "text/plain;q=1.0, text/markdown;q=0.9, text/html;q=0.8, */*;q=0.1"
	case "html":
		return "text/html;q=1.0, application/xhtml+xml;q=0.9, text/plain;q=0.8, text/markdown;q=0.7, */*;q=0.1"
	default:
		return "*/*"
	}
}
