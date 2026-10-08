package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend/rpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestPublicWebDoesNotExposeDiagnostics(t *testing.T) {
	// No RPC calls are needed for SPA routes; use a real, unconnected client.
	connection, err := grpc.NewClient("passthrough:///unused", grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	handler := rpc.NewHTTPHandler(connection, config.SentryConfig{})

	for _, prefix := range []string{"/debug/pprof/", "/s/debug/pprof/"} {
		for _, path := range []string{"", "heap", "allocs", "goroutine", "profile?seconds=1", "trace?seconds=0.01", "cmdline", "symbol", "block", "mutex", "unknown"} {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, prefix+path, http.NoBody)
			request.RemoteAddr = "127.0.0.1:12345"
			handler.ServeHTTP(response, request)
			// SPA success is not evidence of a profiling endpoint: inspect the content.
			if strings.HasPrefix(prefix, "/s/") {
				require.Equal(t, http.StatusOK, response.Code, prefix+path)
				require.Contains(t, response.Body.String(), "/assets/main-", prefix+path)
			} else {
				require.Equal(t, http.StatusNotFound, response.Code, prefix+path)
			}

			require.NotContains(t, response.Body.String(), "Types of profiles available", path)
			require.NotContains(t, response.Body.String(), "goroutine profile:", path)
			require.NotEqual(t, "application/octet-stream", response.Header().Get("Content-Type"), path)
			require.Empty(t, response.Header().Get("Content-Disposition"), path)
		}
	}
}

func TestWebRPC(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	logger := slog.New(slog.DiscardHandler)
	sessions, err := backend.NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	rt := &backend.Runtime{Sessions: sessions, Cfg: config.NewLockedConfig(&config.Config{Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}), Log: logger}

	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = httpListener.Close() })
	cfg := rt.Cfg.Clone()
	require.NoError(t, json.Unmarshal([]byte(`{"web":{"listen_address":"`+httpListener.Addr().String()+`","sentry":{"dsn":"https://public@o1.ingest.sentry.io/1"}}}`), cfg))
	rt.Cfg.Store(cfg)

	_, err = startWebRPC(rt, &mockWebChannels{}, &mockWebCron{})
	require.ErrorContains(t, err, "start Web HTTP")
	require.NoError(t, httpListener.Close())

	stop, err := startWebRPC(rt, &mockWebChannels{}, &mockWebCron{})
	require.NoError(t, err)
	defer func() { require.NoError(t, stop(t.Context())) }()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+cfg.Web.ListenAddress+"/api/Identity", strings.NewReader("{}"))
	require.NoError(t, err)
	responseHTTP, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	var identity struct {
		Username string `json:"username"`
	}
	require.NoError(t, json.NewDecoder(responseHTTP.Body).Decode(&identity))
	require.NoError(t, responseHTTP.Body.Close())
	require.Equal(t, http.StatusOK, responseHTTP.StatusCode)
	require.Equal(t, "alice", identity.Username)
	request, err = http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+cfg.Web.ListenAddress+"/", nil)
	require.NoError(t, err)
	responseHTTP, err = http.DefaultClient.Do(request)
	require.NoError(t, err)
	body, err := io.ReadAll(responseHTTP.Body)
	require.NoError(t, err)
	require.NoError(t, responseHTTP.Body.Close())
	require.Equal(t, http.StatusOK, responseHTTP.StatusCode)
	require.Contains(t, string(body), "/assets/main-")
	require.Contains(t, string(body), `"dsn":"https://public@o1.ingest.sentry.io/1"`)

	require.NoError(t, stop(t.Context()))
	// Immediate shutdown is valid even before Serve's goroutine is scheduled.
	stop, err = startWebRPC(rt, &mockWebChannels{}, &mockWebCron{})
	require.NoError(t, err)
}
