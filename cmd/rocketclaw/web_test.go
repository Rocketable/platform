package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend/rpc"
	"github.com/stretchr/testify/assert"
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

func TestWebRPCTailnetNode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("TS_CONTROL_URL", "http://127.0.0.1:1") // Tests never reach Tailscale's control plane.

	configDir, err := os.UserConfigDir()
	require.NoError(t, err)

	cfg := &config.Config{Web: config.WebConfig{ListenAddress: "127.0.0.1:0"}}
	rt := &backend.Runtime{Cfg: config.NewLockedConfig(cfg), Log: slog.New(slog.DiscardHandler)}
	stop, err := startWebRPC(rt, &mockWebChannels{}, &mockWebCron{})
	require.NoError(t, err)
	require.NoError(t, stop(t.Context()))
	require.NoDirExists(t, filepath.Join(configDir, "rocketclaw"), "no node starts without web.tsnet")

	// A logged-out node without a key never holds up startup or prints a login link:
	// the server starts, logs one notice, and serves only its plain listener.
	var logs bytes.Buffer

	rt.Log = slog.New(slog.NewTextHandler(&logs, nil))
	cfg.Web.Tsnet = &config.TsnetConfig{Hostname: "rocketclaw-test"}
	rt.Cfg.Store(cfg)
	stop, err = startWebRPC(rt, &mockWebChannels{}, &mockWebCron{})
	require.NoError(t, err)
	require.NoError(t, stop(t.Context()))
	assert.Contains(t, logs.String(), "Tailscale authentication unavailable")
	assert.NotContains(t, logs.String(), "login.tailscale.com")

	// With a key the node logs itself in; it never does here, so stop must cancel the wait and close it.
	logs.Reset()
	cfg.Web.Tsnet.AuthKey = "tskey-auth-test"
	rt.Cfg.Store(cfg)
	stop, err = startWebRPC(rt, &mockWebChannels{}, &mockWebCron{})
	require.NoError(t, err)
	require.NoError(t, stop(t.Context()))
	assert.NotContains(t, logs.String(), "Tailscale authentication unavailable")

	info, err := os.Stat(filepath.Join(configDir, "rocketclaw", "tsnet-rocketclaw-test"))
	require.NoError(t, err)
	require.Equal(t, os.ModeDir|0o700, info.Mode())
	require.FileExists(t, filepath.Join(configDir, "rocketclaw", "tsnet-rocketclaw-test", "tailscaled.state"))
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

func TestWebRPCRelaysAndSyncsOpenCode(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, password, ok := r.BasicAuth(); !ok || user != "opencode" || password != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		switch r.URL.Path {
		case "/api/session":
			_, _ = w.Write([]byte(`{"data":[{"id":"ses_a","title":"Alpha","time":{"updated":1000},"location":{"directory":"/srv"}}]}`))
		case "/api/session/active":
			_, _ = w.Write([]byte(`{"data":{}}`))
		default:
			_, _ = w.Write([]byte("opencode web"))
		}
	}))
	t.Cleanup(upstream.Close)

	// A port that was free a moment ago becomes the relay's.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := probe.Addr().(*net.TCPAddr).Port
	require.NoError(t, probe.Close())

	servers := map[string]config.OpenCodeConfig{"box": {URL: upstream.URL, Password: "pw", Port: port}}
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	sessions, err := backend.NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), OpenCode: servers}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	cfg := &config.Config{Workspace: t.TempDir(), Web: config.WebConfig{ListenAddress: "127.0.0.1:0"}, WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}, OpenCode: servers}
	rt := &backend.Runtime{Cfg: config.NewLockedConfig(cfg), Log: slog.New(slog.DiscardHandler), Sessions: sessions}
	stop, err := startWebRPC(rt, &mockWebChannels{}, &mockWebCron{})
	require.NoError(t, err)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", port), http.NoBody)
	require.NoError(t, err)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, errors.Join(err, response.Body.Close()))
	assert.Equal(t, "opencode web", string(body), "the relay serves OpenCode with its password")

	// The sync's first poll runs when the Web starts.
	require.Eventually(t, func() bool {
		for row, err := range sessions.SidebarSessions(t.Context()) {
			if err == nil && row.Conversation.ID == "opencode:box:ses_a" {
				return true
			}
		}

		return false
	}, 10*time.Second, 50*time.Millisecond)

	require.NoError(t, stop(t.Context()))
}

func TestWebRPCFailsWhenARelayPortIsTaken(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, taken.Close()) })

	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()

	servers := map[string]config.OpenCodeConfig{"box": {URL: gone.URL, Password: "pw", Port: taken.Addr().(*net.TCPAddr).Port}}
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	sessions, err := backend.NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), OpenCode: servers}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	cfg := &config.Config{Workspace: t.TempDir(), Web: config.WebConfig{ListenAddress: "127.0.0.1:0"}, OpenCode: servers}
	_, err = startWebRPC(&backend.Runtime{Cfg: config.NewLockedConfig(cfg), Log: slog.New(slog.DiscardHandler), Sessions: sessions}, &mockWebChannels{}, &mockWebCron{})
	require.ErrorContains(t, err, "start OpenCode relay")
}
