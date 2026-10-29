package harness

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWebFetchRequestAcceptHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Header.Get("Accept"))
	}))
	t.Cleanup(server.Close)

	for _, tt := range []struct {
		format string
		want   string
	}{
		{"markdown", "text/markdown;q=1.0, text/x-markdown;q=0.9, text/plain;q=0.8, text/html;q=0.7, */*;q=0.1"},
		{"text", "text/plain;q=1.0, text/markdown;q=0.9, text/html;q=0.8, */*;q=0.1"},
		{"html", "text/html;q=1.0, application/xhtml+xml;q=0.9, text/plain;q=0.8, text/markdown;q=0.7, */*;q=0.1"},
		{"", "*/*"},
	} {
		t.Run(tt.format, func(t *testing.T) {
			resp, err := doWebFetchRequest(t.Context(), server.URL, tt.format, false)
			require.NoError(t, err)

			defer func() { _ = resp.Body.Close() }()

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, tt.want, string(body))
		})
	}
}

func TestWebFetchFormatsHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if r.URL.Path == "/without-type" {
			w.Header().Set("Content-Type", "text/plain")
		}

		_, _ = w.Write([]byte(`<html><body><h1>Title</h1><script>hidden()</script><p>Hello <strong>world</strong>.</p><a href="guide">Guide</a></body></html>`))
	}))
	t.Cleanup(server.Close)

	markdown, err := webFetch(context.Background(), testWebFetchParams(server.URL+"/pages/index.html", "markdown"))
	require.NoError(t, err)
	require.Contains(t, markdown.Output, "# Title")
	require.Contains(t, markdown.Output, "**world**")
	require.Contains(t, markdown.Output, "[Guide]("+server.URL+"/guide)")

	text, err := webFetch(context.Background(), testWebFetchParams(server.URL, "text"))
	require.NoError(t, err)
	require.Contains(t, text.Output, "Title")
	require.Contains(t, text.Output, "Hello")
	require.NotContains(t, text.Output, "hidden")

	fallback, err := webFetch(t.Context(), testWebFetchParams(server.URL+"/without-type", "markdown"))
	require.NoError(t, err)
	require.Equal(t, "# Title\n\nHello **world**.\n\n[Guide](guide)", fallback.Output)
}

func TestWebFetchReturnsImageAndPDFAttachments(t *testing.T) {
	tests := []struct {
		name       string
		content    []byte
		contentTyp string
		wantOutput string
		wantMIME   string
	}{
		{name: "image", content: []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, contentTyp: "image/png", wantOutput: "Image fetched successfully", wantMIME: "image/png"},
		{name: "pdf", content: []byte("%PDF-1.7\n"), contentTyp: "application/pdf", wantOutput: "PDF fetched successfully", wantMIME: "application/pdf"},
		{name: "jpeg signature", content: []byte{0xff, 0xd8, 0xff}, contentTyp: "application/octet-stream", wantOutput: "Image fetched successfully", wantMIME: "image/jpeg"},
		{name: "gif signature", content: []byte("GIF8"), contentTyp: "application/octet-stream", wantOutput: "Image fetched successfully", wantMIME: "image/gif"},
		{name: "bmp signature", content: []byte("BM"), contentTyp: "application/octet-stream", wantOutput: "Image fetched successfully", wantMIME: "image/bmp"},
		{name: "webp signature", content: []byte("RIFF\x00\x00\x00\x00WEBP"), contentTyp: "application/octet-stream", wantOutput: "Image fetched successfully", wantMIME: "image/webp"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentTyp)
				_, _ = w.Write(tt.content)
			}))
			t.Cleanup(server.Close)

			got, err := webFetch(context.Background(), testWebFetchParams(server.URL, "markdown"))

			require.NoError(t, err)
			require.Equal(t, tt.wantOutput, got.Output)
			require.Len(t, got.Attachments, 1)
			require.Equal(t, tt.wantMIME, got.Attachments[0].MIME)
			require.Contains(t, got.Attachments[0].URL, "data:"+tt.wantMIME+";base64,")
		})
	}
}

func TestWebFetchRejectsInvalidURL(t *testing.T) {
	_, err := webFetch(context.Background(), testWebFetchParams("file:///tmp/image.png", ""))

	require.EqualError(t, err, "URL must start with http:// or https://")
}

func TestWebFetchChallengeRetry(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
	}{
		{name: "success", status: http.StatusOK},
		{name: "second challenge", status: http.StatusForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var agents []string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				agents = append(agents, r.UserAgent())
				if len(agents) == 1 {
					w.Header().Set("Cf-Mitigated", "challenge")
					w.WriteHeader(http.StatusForbidden)

					return
				}

				w.Header().Set("Cf-Mitigated", "challenge")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, "retried content")
			}))

			got, err := webFetch(t.Context(), testWebFetchParams(server.URL, "text"))
			server.Close()

			require.Len(t, agents, 2)
			require.Contains(t, agents[0], "Mozilla/5.0")
			require.Equal(t, "www.rocketable.com", agents[1])

			if tt.status == http.StatusForbidden {
				require.EqualError(t, err, "request failed with status 403")
				require.Equal(t, ToolResult{}, got)
			} else {
				require.NoError(t, err)
				require.Equal(t, TextToolResult("retried content"), got)
			}
		})
	}
}

func TestWebFetchBodyLimit(t *testing.T) {
	for _, size := range []int{maxAttachmentBytes, maxAttachmentBytes + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			body := strings.Repeat("x", size)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				w.(http.Flusher).Flush() // Exercise the body limit without Content-Length.
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(server.Close)

			got, err := webFetch(t.Context(), testWebFetchParams(server.URL, "text"))
			if size > maxAttachmentBytes {
				require.EqualError(t, err, "response too large (exceeds 5MB limit)")
				require.Empty(t, got.Output)
			} else {
				require.NoError(t, err)
				require.Equal(t, body, got.Output)
			}
		})
	}
}

func testWebFetchParams(url, format string) webFetchToolParams {
	return webFetchToolParams{URL: url, Format: format, TimeoutSecond: 0}
}
