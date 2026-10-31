package rpc

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	cronfrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

// A local page works a chat on a remote RocketClaw behind its tailnet handler, lists and searches it beside
// local chats, is notified of its replies, and greys it while the remote is off; a declared remote that
// never answers shows why. Both servers sit under one tailnet suffix, which the browser maps to this machine.
func TestRemoteRocketClawRealBrowser(t *testing.T) {
	const suffix = "tail1234.ts.net"

	// serve starts a RocketClaw whose model always says reply, with agents beside its built-in ones, and returns its Web RPC
	// and workspace.
	serve := func(reply string, agents ...string) (*grpc.ClientConn, string) {
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if !assert.NoError(t, err) {
				return
			}
			defer func() { _ = conn.Close() }()

			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}

				for _, event := range []string{`{"type":"response.created","response":{"id":"resp"}}`, `{"type":"response.completed","response":{"id":"resp","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[{"id":"msg","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"` + reply + `","annotations":[]}]}]}}`} {
					if conn.WriteMessage(websocket.TextMessage, []byte(event)) != nil {
						return
					}
				}
			}
		}))
		t.Cleanup(provider.Close)

		dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
		require.NoError(t, err)

		cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), OpenAI: config.OpenAIConfig{APIKey: "remote-test", APIBaseURL: strings.Replace(provider.URL, "http://", "ws://", 1)}}
		root, err := os.OpenRoot(cfg.Workspace)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, root.Close()) })
		// Startup builds the runtime agents from the workspace's agents directory.
		require.NoError(t, root.MkdirAll("agents", 0o700))

		for _, agent := range agents {
			require.NoError(t, root.WriteFile(filepath.Join("agents", agent+".md"), []byte("---\ndescription: Remote browser fixture\nmodel: gpt-6.1-sol\npermission: {}\n---\nReply plainly.\n"), 0o600))
		}

		ctx, cancel := context.WithCancel(t.Context())
		ready := make(chan *backend.Runtime)
		assembly := &mockFrontendAssembler{AssembleFunc: func(rt *backend.Runtime) (<-chan struct{}, []func(context.Context) error, error) {
			ready <- rt
			return rt.RunCtx.Done(), nil, nil
		}, ValidateAssetsFunc: func(*config.Config, string) error { return nil }}

		running, ctxRunning := errgroup.WithContext(ctx)
		running.Go(func() error { return backend.Run(ctx, cfg, "", slog.New(slog.DiscardHandler), assembly) })
		t.Cleanup(func() { cancel(); require.NoError(t, running.Wait()) })

		var rt *backend.Runtime
		select {
		case rt = <-ready:
		case <-ctxRunning.Done():
			t.Fatalf("start %q fixture before readiness: %v", reply, running.Wait())
		}

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

		return connection, cfg.Workspace
	}

	// The tailnet name a test server answers at.
	named := func(name string, server *httptest.Server) string {
		return "https://" + name + "." + suffix + ":" + server.URL[strings.LastIndex(server.URL, ":")+1:]
	}

	whois := &mockTailnetWhoIs{WhoIsFunc: func(context.Context, string) (*apitype.WhoIsResponse, error) {
		return &apitype.WhoIsResponse{Node: &tailcfg.Node{}, UserProfile: &tailcfg.UserProfile{LoginName: "alice"}}, nil
	}}
	// The local page switches the remote off and on; while off, the remote drops every connection unanswered.
	// It also moves a server to another protocol version.
	var (
		mu       sync.Mutex
		down     bool
		versions = map[string]bool{}
		tailnet  http.Handler
	)

	versioned := func(name string, next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			other := versions[name]
			mu.Unlock()

			if other && r.URL.Path == "/api/Protocol" {
				_, _ = w.Write([]byte(`{"protoSha256":"other"}`))
				return
			}

			next.ServeHTTP(w, r)
		})
	}

	remoteRPC, remoteWorkspace := serve("Remote reply", "scout")
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		off, handler := down, tailnet
		mu.Unlock()

		if off {
			panic(http.ErrAbortHandler)
		}

		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(remote.Close)

	remotes := map[string]config.RemoteRocketClawConfig{"work": {URL: named("work", remote)}, "gone": {URL: "https://gone." + suffix + ":1"}}
	localRPC, _ := serve("Local reply")
	web := versioned("local", NewHTTPHandler(localRPC, config.SentryConfig{}, config.PostHogConfig{}, nil, "", remotes))
	local := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if state, ok := strings.CutPrefix(r.URL.Path, "/test/remote/"); ok {
			mu.Lock()
			down = state == "down"
			mu.Unlock()

			return
		}

		if name, ok := strings.CutPrefix(r.URL.Path, "/test/version/"); ok {
			mu.Lock()
			versions[name] = true
			mu.Unlock()

			return
		}

		web.ServeHTTP(w, r)
	}))
	t.Cleanup(local.Close)

	// The remote trusts the local page's origin, which exists only now, and no other page.
	mu.Lock()
	tailnet = NewTailnetHandler(whois, []string{named("local", local)}, versioned("work", NewHTTPHandler(remoteRPC, config.SentryConfig{}, config.PostHogConfig{}, nil, "", nil)))
	mu.Unlock()

	runWebTest(t, "src/remote-transport.test.ts", "ROCKETCLAW_REMOTE_TEST_LOCAL="+named("local", local), "ROCKETCLAW_REMOTE_TEST_REMOTE="+named("work", remote), "ROCKETCLAW_REMOTE_TEST_WORKSPACE="+remoteWorkspace, "ROCKETCLAW_REMOTE_TEST_SUFFIX="+suffix)
}
