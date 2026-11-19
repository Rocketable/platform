package main

import (
	"context"
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
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
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
	stop := startWebRPC(rt, node, &mockWebChannels{}, &mockWebCron{})

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

func TestTraceWebRPC(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()

	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	handle := func(fail bool) grpc.UnaryHandler {
		return func(ctx context.Context, _ any) (any, error) {
			require.False(t, trace.SpanContextFromContext(ctx).IsValid())

			if fail {
				return nil, status.Error(codes.NotFound, "missing")
			}

			return "reply", nil
		}
	}

	for _, rate := range []float64{0, 1} {
		reply, err := traceWebRPC(rate)(t.Context(), nil, &grpc.UnaryServerInfo{FullMethod: "/rpc.Web/History"}, handle(false))
		require.NoError(t, err)
		require.Equal(t, "reply", reply)

		_, err = traceWebRPC(rate)(t.Context(), nil, &grpc.UnaryServerInfo{FullMethod: "/rpc.Web/Prompt"}, handle(true))
		require.Equal(t, codes.NotFound, status.Code(err))
	}

	spans := recorder.Ended()
	require.Len(t, spans, 2)

	for i, want := range []struct {
		name   string
		code   codes.Code
		status otelcodes.Code
	}{{"rpc.Web/History", codes.OK, otelcodes.Unset}, {"rpc.Web/Prompt", codes.NotFound, otelcodes.Error}} {
		require.Equal(t, want.name, spans[i].Name())
		require.Equal(t, trace.SpanKindServer, spans[i].SpanKind())
		require.Equal(t, want.status, spans[i].Status().Code)
		require.Contains(t, spans[i].Attributes(), attribute.Int("rpc.grpc.status_code", int(want.code)))
	}
}
