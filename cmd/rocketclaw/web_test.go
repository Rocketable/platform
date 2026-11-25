package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/frontend/rpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestPublicWebDoesNotExposeDiagnostics(t *testing.T) {
	connection, err := grpc.NewClient("passthrough:///unused", grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	handler := rpc.NewHTTPHandler(connection, new(config.LockedConfig), "")

	for _, prefix := range []string{"/debug/pprof/", "/s/debug/pprof/"} {
		for _, path := range []string{"", "heap", "allocs", "goroutine", "profile?seconds=1", "trace?seconds=0.01", "cmdline", "symbol", "block", "mutex", "unknown"} {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, prefix+path, http.NoBody)
			handler.ServeHTTP(response, request)
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

func webSessions(t *testing.T) *backend.SessionService {
	t.Helper()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	sessions, err := backend.NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	return sessions
}

func TestWebRPCSyncsOpenCode(t *testing.T) {
	node := startTestTailnet(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, password, ok := r.BasicAuth(); !ok || user != "opencode" || password != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		switch r.URL.Path {
		case "/api/session":
			_, _ = w.Write([]byte(`{"data":[{"id":"ses_a","title":"Alpha","time":{"updated":1000},"location":{"directory":"/srv"}}]}`))
		default:
			_, _ = w.Write([]byte(`{"data":{}}`))
		}
	}))
	t.Cleanup(upstream.Close)

	servers := map[string]config.OpenCodeConfig{"box": {URL: upstream.URL, Password: "pw"}}
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	sessions, err := backend.NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), OpenCode: servers}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	cfg := &config.Config{Workspace: t.TempDir(), Web: config.WebConfig{Tsnet: &config.TsnetConfig{Hostname: "rocketclaw-test"}}, OpenCode: servers}
	rt := &backend.Runtime{Cfg: config.NewLockedConfig(cfg), Log: slog.New(slog.DiscardHandler), Sessions: sessions}
	stop := startWebRPC(rt, node, &mockWebChannels{}, &mockWebRoutines{})

	require.Eventually(t, func() bool {
		for chat, err := range sessions.SidebarChats(t.Context(), "", &mockWebChannels{}, nil) {
			if err == nil && chat.Session.Conversation.ID == "opencode:box:ses_a" {
				return true
			}
		}

		return false
	}, 10*time.Second, 50*time.Millisecond)

	require.NoError(t, stop(t.Context()))
}
