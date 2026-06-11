package rocketcode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWebFetchFormatsHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><h1>Title</h1><script>hidden()</script><p>Hello <strong>world</strong>.</p></body></html>`))
	}))
	t.Cleanup(server.Close)

	markdown, err := webFetch(context.Background(), testWebFetchParams(server.URL, "markdown"))
	require.NoError(t, err)
	require.Contains(t, markdown.Output, "# Title")
	require.Contains(t, markdown.Output, "**world**")

	text, err := webFetch(context.Background(), testWebFetchParams(server.URL, "text"))
	require.NoError(t, err)
	require.Contains(t, text.Output, "Title")
	require.Contains(t, text.Output, "Hello")
	require.NotContains(t, text.Output, "hidden")
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

func testWebFetchParams(url, format string) webFetchToolParams {
	return webFetchToolParams{URL: url, Format: format, Timeout: 0}
}
