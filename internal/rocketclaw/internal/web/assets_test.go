package web

import (
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedApplication(t *testing.T) {
	index := unpack("dist/index.html.gz")
	require.Contains(t, string(index), "<script")

	handler := Handler(new(config.LockedConfig), "")

	for _, path := range []string{"/", "/s/conversation", "/oc/b3BlbmNvZGU6Ym94OnNlc19h", "/cron", "/cron/SEVBUlRCRUFU", "/agents", "/agents/new", "/agents/drafts/abc", "/skills", "/skills/release", "/config", "/search", "/search/3-1-4", "/voice", "/pages", "/shortcuts", "/closed", "/pages/Ops/main%2Freview/index.html", "/r/work", "/r/work/s/abc", "/r/work/agents"} {
		request := httptest.NewRequest(http.MethodGet, path, http.NoBody)
		request.Header.Set("Accept-Encoding", "gzip")

		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, path)
		require.Equal(t, "text/html; charset=utf-8", response.Header().Get("Content-Type"), path)
		require.Empty(t, response.Header().Get("Content-Encoding"), path)
		require.Equal(t, string(index), response.Body.String(), path)
	}

	for _, path := range []string{"/missing.js", "/assets/missing.js", "/oc-ui/missing.js", "/assets/../index.html"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, http.NoBody))
		require.Equal(t, http.StatusNotFound, response.Code, path)
	}

	files, err := fs.Glob(assets, "dist/assets/*.gz")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	for _, path := range files {
		target := "/" + strings.TrimSuffix(strings.TrimPrefix(path, "dist/"), ".gz")

		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, http.NoBody))
		require.Equal(t, http.StatusOK, response.Code, target)
		require.NotEmpty(t, response.Header().Get("Content-Type"), target)
		require.Empty(t, response.Header().Get("Content-Encoding"), target)
		require.Equal(t, unpack(path), response.Body.Bytes(), target)
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

		path, ok := strings.CutSuffix(path, ".gz")
		require.True(t, ok, path)

		if strings.Contains("/"+path, "/_") {
			underscored++
		}

		data, err := fs.ReadFile(files, path+".gz")
		require.NoError(t, err)

		target := "/oc-ui/" + path

		cache := "public, max-age=31536000, immutable"
		if !strings.HasPrefix(path, "_assets/") || path == "_assets/index.js" {
			cache = "no-cache"
		}

		request := httptest.NewRequest(http.MethodGet, target, http.NoBody)
		request.Header.Set("Accept-Encoding", "gzip")

		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, target)
		require.Equal(t, cache, response.Header().Get("Cache-Control"), target)
		require.NotEmpty(t, response.Header().Get("Content-Type"), target)
		require.Equal(t, "gzip", response.Header().Get("Content-Encoding"), target)
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

	scripts, err := fs.Glob(assets, "dist/assets/main-*.js.gz")
	require.NoError(t, err)
	require.Len(t, scripts, 1)
	stored, err := assets.ReadFile(scripts[0])
	require.NoError(t, err)

	path := "/" + strings.TrimSuffix(strings.TrimPrefix(scripts[0], "dist/"), ".gz")

	plain := serve(path, nil)
	require.Equal(t, "public, max-age=31536000, immutable", plain.Header().Get("Cache-Control"))
	require.Equal(t, "Accept-Encoding", plain.Header().Get("Vary"))
	require.Equal(t, "text/javascript; charset=utf-8", plain.Header().Get("Content-Type"))
	require.Empty(t, plain.Header().Get("Content-Encoding"))
	require.Equal(t, unpack(scripts[0]), plain.Body.Bytes())

	compressed := serve(path, http.Header{"Accept-Encoding": {"gzip, br"}})
	require.Equal(t, "Accept-Encoding", compressed.Header().Get("Vary"))
	require.Equal(t, "gzip", compressed.Header().Get("Content-Encoding"))
	require.Equal(t, "text/javascript; charset=utf-8", compressed.Header().Get("Content-Type"))
	require.Equal(t, strings.TrimSuffix(plain.Header().Get("ETag"), `"`)+`-gzip"`, compressed.Header().Get("ETag"))
	require.Equal(t, stored, compressed.Body.Bytes())
	require.Equal(t, http.StatusNotModified, serve(path, http.Header{"Accept-Encoding": {"gzip"}, "If-None-Match": {compressed.Header().Get("ETag")}}).Code)

	for _, path := range []string{"/", "/s/conversation", "/assets/inter-OFL.txt"} {
		first := serve(path, nil)
		require.Equal(t, "no-cache", first.Header().Get("Cache-Control"), path)
		require.NotEmpty(t, first.Header().Get("ETag"), path)
		require.Equal(t, http.StatusNotModified, serve(path, http.Header{"If-None-Match": {first.Header().Get("ETag")}}).Code, path)
	}
}

func TestServerNameTitle(t *testing.T) {
	handler := Handler(new(config.LockedConfig), "lab <1>")

	for _, path := range []string{"/", "/s/conversation"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, http.NoBody))
		require.Equal(t, http.StatusOK, response.Code)
		require.Contains(t, response.Body.String(), "<title>lab &lt;1&gt; - RocketClaw</title>")
		require.NotEmpty(t, response.Header().Get("ETag"))
	}
}

func TestBrowserBootstrap(t *testing.T) {
	remotes := map[string]config.RemoteRocketClawConfig{"work": {URL: "https://rocketclaw.tail1234.ts.net"}, "lab": {URL: "https://lab.tail1234.ts.net"}}
	cfg := config.NewLockedConfig(&config.Config{PostHog: config.PostHogConfig{ProjectToken: "phc_test", Host: "https://us.i.posthog.com", TimingSampleRate: 0.25}, RocketClaws: remotes})
	handler := Handler(cfg, "")

	for _, path := range []string{"/", "/s/conversation", "/search"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, http.NoBody))
		require.Equal(t, http.StatusOK, response.Code)
		require.Contains(t, response.Body.String(), `<script id="posthog-config" type="application/json">{"project_token":"phc_test","host":"https://us.i.posthog.com","timing_sample_rate":0.25}</script>`)
		require.Contains(t, response.Body.String(), `<script id="rocketclaws" type="application/json">[{"name":"lab","url":"https://lab.tail1234.ts.net"},{"name":"work","url":"https://rocketclaw.tail1234.ts.net"}]</script>`)
	}

	cfg.Store(&config.Config{})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	require.NotContains(t, response.Body.String(), `id="posthog-config"`)
	require.NotContains(t, response.Body.String(), `id="rocketclaws"`)
}
