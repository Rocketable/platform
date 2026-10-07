package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
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

	for _, path := range []string{"/", "/s/conversation", "/cron", "/agents", "/skills", "/config", "/search"} {
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
