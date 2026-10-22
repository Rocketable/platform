package web

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedApplication(t *testing.T) {
	index, err := assets.ReadFile("dist/index.html")
	require.NoError(t, err)
	require.Contains(t, string(index), "<script")

	handler := Handler(config.SentryConfig{})

	for _, path := range []string{"/", "/s/conversation", "/oc/b3BlbmNvZGU6Ym94OnNlc19h", "/cron", "/agents", "/skills", "/skills/release", "/config", "/search", "/voice", "/pages", "/shortcuts", "/pages/Ops/main%2Freview/index.html"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, http.NoBody))
		require.Equal(t, http.StatusOK, response.Code, path)
		require.Equal(t, string(index), response.Body.String(), path)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/missing.js", http.NoBody))
	require.Equal(t, http.StatusNotFound, response.Code)

	files, err := fs.Glob(assets, "dist/assets/*.js")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	for _, path := range files {
		data, err := assets.ReadFile(path)
		require.NoError(t, err)

		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/"+strings.TrimPrefix(path, "dist/"), http.NoBody))
		require.Equal(t, http.StatusOK, response.Code)
		require.Equal(t, data, response.Body.Bytes())
	}
}

// Vite names the asset directory and some chunks with a leading underscore, which only an all: embed keeps.
func TestEmbeddedOCApplication(t *testing.T) {
	handler := Handler(config.SentryConfig{})
	files := os.DirFS("oc-dist")
	underscored := 0

	err := fs.WalkDir(files, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}

		if strings.Contains("/"+path, "/_") {
			underscored++
		}

		data, err := fs.ReadFile(files, path)
		require.NoError(t, err)

		target := "/oc-ui/" + path
		// Every built file is named for its content except the entry, which RocketClaw imports by name.
		cache := "public, max-age=31536000, immutable"
		if !strings.HasPrefix(path, "_assets/") || path == "_assets/index.js" {
			cache = "no-cache"
		}

		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, http.NoBody))
		require.Equal(t, http.StatusOK, response.Code, target)
		require.Equal(t, cache, response.Header().Get("Cache-Control"), target)
		require.Equal(t, string(data), response.Body.String(), target)

		return nil
	})
	require.NoError(t, err)
	require.Positive(t, underscored)
}

func TestEmbeddedFileCaching(t *testing.T) {
	handler := Handler(config.SentryConfig{})
	serve := func(path string, header http.Header) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, http.NoBody)
		maps.Copy(request.Header, header)

		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		return response
	}

	scripts, err := fs.Glob(assets, "dist/assets/main-*.js")
	require.NoError(t, err)
	require.Len(t, scripts, 1)
	data, err := assets.ReadFile(scripts[0])
	require.NoError(t, err)

	path := "/" + strings.TrimPrefix(scripts[0], "dist/")

	// A file named for its content never changes, so browsers keep it without asking again.
	plain := serve(path, nil)
	require.Equal(t, "public, max-age=31536000, immutable", plain.Header().Get("Cache-Control"))
	require.Empty(t, plain.Header().Get("Content-Encoding"))
	require.Equal(t, data, plain.Body.Bytes())

	compressed := serve(path, http.Header{"Accept-Encoding": {"gzip, br"}})
	require.Equal(t, "gzip", compressed.Header().Get("Content-Encoding"))
	require.Contains(t, compressed.Header().Get("Content-Type"), "javascript")
	require.Less(t, compressed.Body.Len(), len(data)/2)
	reader, err := gzip.NewReader(compressed.Body)
	require.NoError(t, err)
	unpacked, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, data, unpacked)

	// The page and files without a content hash are checked on each load, and an unchanged one is not sent again.
	for _, path := range []string{"/", "/s/conversation", "/assets/inter-OFL.txt"} {
		first := serve(path, nil)
		require.Equal(t, "no-cache", first.Header().Get("Cache-Control"), path)
		require.NotEmpty(t, first.Header().Get("ETag"), path)
		require.Equal(t, http.StatusNotModified, serve(path, http.Header{"If-None-Match": {first.Header().Get("ETag")}}).Code, path)
	}
}

func TestSentryBootstrap(t *testing.T) {
	sentry := config.SentryConfig{DSN: "https://public@o1.ingest.sentry.io/1", Environment: "</script><script>alert(1)</script>", TracesSampleRate: new(0.25)}
	handler := Handler(sentry)

	for _, path := range []string{"/", "/s/conversation", "/search"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, http.NoBody))
		require.Equal(t, http.StatusOK, response.Code)
		require.Contains(t, response.Body.String(), `<script id="sentry-config" type="application/json">`)
		require.Contains(t, response.Body.String(), `"dsn":"https://public@o1.ingest.sentry.io/1"`)
		require.Contains(t, response.Body.String(), `"traces_sample_rate":0.25`)
		require.NotContains(t, response.Body.String(), sentry.Environment)
		require.Contains(t, response.Body.String(), `\u003c/script\u003e`)
	}
}

func TestSentryRelease(t *testing.T) {
	for _, tt := range []struct {
		build *debug.BuildInfo
		want  string
	}{
		{build: nil, want: ""},
		{build: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, want: ""},
		{build: &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}, want: "v1.2.3"},
	} {
		require.Equal(t, tt.want, release(tt.build))
	}

	data, err := json.Marshal(sentryBootstrap{DSN: "https://public@o1.ingest.sentry.io/1", Release: "v1.2.3"})
	require.NoError(t, err)
	require.JSONEq(t, `{"dsn":"https://public@o1.ingest.sentry.io/1","release":"v1.2.3"}`, string(data))
}
