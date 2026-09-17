package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmbeddedApplication(t *testing.T) {
	index, err := assets.ReadFile("dist/index.html")
	require.NoError(t, err)
	require.Contains(t, string(index), "<script")

	handler := Handler()

	for _, path := range []string{"/", "/s/conversation", "/cron", "/agents", "/skills", "/config", "/settled"} {
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
