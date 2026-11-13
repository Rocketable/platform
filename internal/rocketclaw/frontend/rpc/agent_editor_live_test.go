package rpc

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	cronfrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TestAgentEditorRealBackendBrowser runs the agent editor's browser test against a real backend: alice's
// browser at one address, and bob's, limited to Ops until /test/unlimit, at another.
func TestAgentEditorRealBackendBrowser(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), Permissions: map[string][]string{"bob": {"Ops"}}}
	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.Mkdir("agents", 0o700))
	require.NoError(t, root.WriteFile("agents/support-bot.md", []byte("---\ndescription: Answers support questions\nmodel: gpt-6.1-sol\npermission: {}\n---\nYou answer support questions.\n"), 0o600))

	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan *backend.Runtime)
	assembly := &mockFrontendAssembler{AssembleFunc: func(rt *backend.Runtime) (<-chan struct{}, []func(context.Context) error, error) {
		ready <- rt
		return rt.RunCtx.Done(), nil, nil
	}, ValidateAssetsFunc: func(*config.Config, string) error { return nil }}

	var running errgroup.Group
	running.Go(func() error {
		return backend.Run(ctx, cfg, "", slog.New(slog.DiscardHandler), assembly, sandbox.None{})
	})
	t.Cleanup(func() { cancel(); require.NoError(t, running.Wait()) })

	rt := <-ready
	listener, err := net.Listen("unix", testSocketPath(t))
	require.NoError(t, err)

	transport := grpc.NewServer()
	New(rt, rt.Sessions, rt.Cfg, &mockChannels{}, &mockCronJobs{JobsFunc: func() ([]cronfrontend.Job, error) { return nil, nil }}).Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() { transport.Stop(); require.NoError(t, serving.Wait()) })

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	handler := NewHTTPHandler(connection, cfg.Web.Sentry, cfg.PostHog, cfg.OpenCode, "", nil)
	alice := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/test/unlimit" {
			handler.ServeHTTP(w, r)
			return
		}

		unlimited := rt.Cfg.Clone()
		unlimited.Permissions = nil
		rt.Cfg.Store(unlimited)
	}))
	t.Cleanup(alice.Close)

	// The test tailscale names 192.0.2.2 bob.
	bob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = "192.0.2.2:1234"
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(bob.Close)

	runWebTest(t, "src/agent-editor-transport.test.ts", "ROCKETCLAW_EDITOR_ALICE_URL="+alice.URL, "ROCKETCLAW_EDITOR_BOB_URL="+bob.URL)
}
