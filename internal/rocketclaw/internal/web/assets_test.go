package web

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedApplication(t *testing.T) {
	index, err := assets.ReadFile("dist/index.html")
	require.NoError(t, err)
	require.Contains(t, string(index), "<script")

	handler := Handler(new(config.LockedConfig), "")

	for _, path := range []string{"/", "/s/conversation", "/oc/b3BlbmNvZGU6Ym94OnNlc19h", "/cron", "/cron/SEVBUlRCRUFU", "/agents", "/agents/new", "/agents/drafts/abc", "/skills", "/skills/release", "/config", "/search", "/search/3-1-4", "/voice", "/pages", "/shortcuts", "/closed", "/pages/Ops/main%2Freview/index.html", "/r/work", "/r/work/s/abc", "/r/work/agents"} {
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

func TestEmbeddedOCApplication(t *testing.T) {
	handler := Handler(new(config.LockedConfig), "")
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
	handler := Handler(new(config.LockedConfig), "")
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

	for _, path := range []string{"/", "/s/conversation", "/assets/inter-OFL.txt"} {
		first := serve(path, nil)
		require.Equal(t, "no-cache", first.Header().Get("Cache-Control"), path)
		require.NotEmpty(t, first.Header().Get("ETag"), path)
		require.Equal(t, http.StatusNotModified, serve(path, http.Header{"If-None-Match": {first.Header().Get("ETag")}}).Code, path)
	}
}

func TestServerNameTitle(t *testing.T) {
	cfg := new(config.LockedConfig)
	handler := Handler(cfg, "lab <1>")

	for _, path := range []string{"/", "/s/conversation"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, http.NoBody))
		require.Equal(t, http.StatusOK, response.Code)
		require.Contains(t, response.Body.String(), "<title>lab &lt;1&gt; - RocketClaw</title>")
		require.NotEmpty(t, response.Header().Get("ETag"))
	}

	cfg.Store(&config.Config{ServerDisplayName: "live"})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	require.Contains(t, response.Body.String(), "<title>live - RocketClaw</title>")
}

func TestCompressedFileAllocations(t *testing.T) {
	result := testing.Benchmark(BenchmarkServeCompressedFile)
	require.Less(t, result.AllocedBytesPerOp(), int64(512<<10))
}

func TestCompressedFileResponses(t *testing.T) {
	var writers sync.Pool

	for _, text := range []string{"", "alpha", "beta"} {
		t.Run(text, func(t *testing.T) {
			t.Parallel()

			data := bytes.Repeat([]byte(text), 256)
			request := httptest.NewRequest(http.MethodGet, "/example.js", http.NoBody)
			request.Header.Set("Accept-Encoding", "gzip")

			for range 2 {
				response := httptest.NewRecorder()
				serveFile(response, request, "example.js", data, "assets", &writers)
				reader, err := gzip.NewReader(response.Body)
				require.NoError(t, err)
				unpacked, err := io.ReadAll(reader)
				require.NoError(t, err)
				require.NoError(t, reader.Close())
				require.Equal(t, data, unpacked)
			}
		})
	}
}

func BenchmarkServeCompressedFile(b *testing.B) {
	var writers sync.Pool

	data := bytes.Repeat([]byte("const greeting = 'hello';\n"), 256)
	request := httptest.NewRequest(http.MethodGet, "/example.js", http.NoBody)
	request.Header.Set("Accept-Encoding", "gzip")
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()

	for b.Loop() {
		serveFile(httptest.NewRecorder(), request, "example.js", data, "assets", &writers)
	}
}

func TestBrowserBootstrap(t *testing.T) {
	remotes := map[string]config.RemoteRocketClawConfig{"work": {URL: "https://rocketclaw.tail1234.ts.net"}, "lab": {URL: "https://lab.tail1234.ts.net"}}
	cfg := config.NewLockedConfig(&config.Config{PostHog: config.PostHogConfig{ProjectToken: "phc_test", Host: "https://us.i.posthog.com"}, RocketClaws: remotes})
	handler := Handler(cfg, "")

	for _, path := range []string{"/", "/s/conversation", "/search"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, http.NoBody))
		require.Equal(t, http.StatusOK, response.Code)
		require.Contains(t, response.Body.String(), `<script id="posthog-config" type="application/json">{"project_token":"phc_test","host":"https://us.i.posthog.com"}</script>`)
		require.Contains(t, response.Body.String(), `<script id="rocketclaws" type="application/json">[{"name":"lab","url":"https://lab.tail1234.ts.net"},{"name":"work","url":"https://rocketclaw.tail1234.ts.net"}]</script>`)
	}

	cfg.Store(&config.Config{})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	require.NotContains(t, response.Body.String(), `id="posthog-config"`)
	require.NotContains(t, response.Body.String(), `id="rocketclaws"`)
}
