package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/require"
)

func TestWebRPC(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	logger := slog.New(slog.DiscardHandler)
	sessions, err := backend.NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	rt := &backend.Runtime{Sessions: sessions, Cfg: &config.Config{Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}, Log: logger}

	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = httpListener.Close() })
	require.NoError(t, json.Unmarshal([]byte(`{"web":{"listen_address":"`+httpListener.Addr().String()+`"}}`), rt.Cfg))

	_, err = startWebRPC(rt, &mockWebChannels{}, &mockWebCron{})
	require.ErrorContains(t, err, "start Web HTTP")
	require.NoError(t, httpListener.Close())

	stop, err := startWebRPC(rt, &mockWebChannels{}, &mockWebCron{})
	require.NoError(t, err)
	defer func() { require.NoError(t, stop(t.Context())) }()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+rt.Cfg.Web.ListenAddress+"/api/Identity", strings.NewReader("{}"))
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
	request, err = http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+rt.Cfg.Web.ListenAddress+"/", nil)
	require.NoError(t, err)
	responseHTTP, err = http.DefaultClient.Do(request)
	require.NoError(t, err)
	body, err := io.ReadAll(responseHTTP.Body)
	require.NoError(t, err)
	require.NoError(t, responseHTTP.Body.Close())
	require.Equal(t, http.StatusOK, responseHTTP.StatusCode)
	require.Contains(t, string(body), "/assets/main-")

	require.NoError(t, stop(t.Context()))
	// Immediate shutdown is valid even before Serve's goroutine is scheduled.
	stop, err = startWebRPC(rt, &mockWebChannels{}, &mockWebCron{})
	require.NoError(t, err)
}
