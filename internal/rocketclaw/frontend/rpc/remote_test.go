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
	routinefrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/routine"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

func TestRemoteRocketClawRealBrowser(t *testing.T) {
	t.Parallel()

	const suffix = "tail1234.ts.net"

	serve := func(reply string, agents ...string) (*grpc.ClientConn, *config.Config) {
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if !assert.NoError(t, err) {
				return
			}
			defer func() { _ = conn.Close() }()

			for {
				if _, message, err := conn.ReadMessage(); err != nil || answerWebsocketTitle(t, conn, message) {
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

		cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), OpenAI: config.OpenAIConfig{APIKey: "remote-test", APIBaseURL: strings.Replace(provider.URL, "http://", "ws://", 1)}, Slack: liveSlack()}
		root, err := os.OpenRoot(cfg.Workspace)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, root.Close()) })
		require.NoError(t, root.MkdirAll("agents", 0o700))

		for _, agent := range agents {
			require.NoError(t, root.WriteFile(filepath.Join("agents", agent+".md"), []byte("---\ndescription: Remote browser fixture\nmodel: gpt-6.1-sol\npermission: {}\n---\nReply plainly.\n"), 0o600))
		}

		ctx, cancel := context.WithCancel(t.Context())
		ready := make(chan *backend.Runtime)
		assembly := &mockFrontendAssembler{GateFunc: passGate, AssembleFunc: func(rt *backend.Runtime) (<-chan struct{}, []func(context.Context) error, error) {
			ready <- rt
			return rt.RunCtx.Done(), nil, nil
		}}

		running, ctxRunning := errgroup.WithContext(ctx)
		running.Go(func() error {
			return backend.Run(ctx, cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)).With("test", t.Name(), "fixture_reply", reply), new(slog.LevelVar), assembly, sandbox.None{})
		})
		t.Cleanup(func() { cancel(); require.NoError(t, running.Wait()) })

		var rt *backend.Runtime
		select {
		case rt = <-ready:
		case <-ctxRunning.Done():
			t.Fatalf("start %q fixture before readiness: %v", reply, running.Wait())
		}

		for _, agent := range agents {
			require.NoError(t, rt.Sessions.PutPage(ctx, &backend.Page{Kind: backend.PageFile, Name: "index.html", ContentType: "text/html", Body: []byte("<p>" + reply + " page</p>")}, "", agent))
		}

		var waking errgroup.Group
		waking.Go(func() error { rt.Sessions.WakeParks(ctx); return nil })
		t.Cleanup(func() { cancel(); require.NoError(t, waking.Wait()) })

		listener, err := net.Listen("unix", testSocketPath(t))
		require.NoError(t, err)

		transport := grpc.NewServer()
		New(rt, rt.Sessions, rt.Cfg, &mockChannels{SlackTagsMatchingFunc: func(context.Context, string) []string { return nil }}, &mockRoutines{JobsFunc: func() ([]routinefrontend.Job, error) { return nil, nil }}).Register(transport)

		var serving errgroup.Group
		serving.Go(func() error { return transport.Serve(listener) })
		t.Cleanup(func() { transport.Stop(); require.NoError(t, serving.Wait()) })

		connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, connection.Close()) })

		return connection, cfg
	}

	named := func(name string, server *httptest.Server) string {
		return "https://" + name + "." + suffix + ":" + server.URL[strings.LastIndex(server.URL, ":")+1:]
	}

	whois := &mockTailnetWhoIs{WhoIsFunc: func(context.Context, string) (*apitype.WhoIsResponse, error) {
		return &apitype.WhoIsResponse{Node: &tailcfg.Node{}, UserProfile: &tailcfg.UserProfile{LoginName: "alice"}}, nil
	}}

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

	remoteRPC, remoteConfig := serve("Remote reply", "scout")
	remoteDB := openTestTurns(t, remoteConfig.DatabaseURL).db
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		off, handler := down, tailnet
		mu.Unlock()

		if off {
			panic(http.ErrAbortHandler)
		}

		if r.URL.Path == "/api/DownloadAttachment" {
			for name := range r.Header {
				if strings.HasPrefix(name, "Sec-Fetch-") {
					r.Header.Del(name)
				}
			}
		}

		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(remote.Close)

	remotes := map[string]config.RemoteRocketClawConfig{"work": {URL: named("work", remote)}, "gone": {URL: "https://gone." + suffix + ":1"}}
	localRPC, _ := serve("Local reply")
	web := versioned("local", signedIn("alice", NewHTTPHandler(localRPC, config.NewLockedConfig(&config.Config{RocketClaws: remotes}), "")))
	local := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/test/other-site" {
			_, _ = w.Write([]byte("<!doctype html><title>Another site</title>"))
			return
		}

		if r.URL.Path == "/test/view-expired" {
			var stale bool

			err := remoteDB.QueryRowContext(r.Context(), `UPDATE web_tab_states SET viewed_at = now() - interval '11 seconds'
WHERE username = 'alice' RETURNING viewed_at < now() - interval '10 seconds'`).Scan(&stale)
			assert.NoError(t, err)
			assert.True(t, stale, "the remote chat's last view must have expired")

			return
		}

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

	mu.Lock()
	tailnet = NewTailnetHandler(whois, []string{named("local", local)}, versioned("work", NewHTTPHandler(remoteRPC, new(config.LockedConfig), "")))
	mu.Unlock()

	runWebTest(t, "src/remote-transport.test.ts", "ROCKETCLAW_REMOTE_TEST_LOCAL="+named("local", local), "ROCKETCLAW_REMOTE_TEST_REMOTE="+named("work", remote), "ROCKETCLAW_REMOTE_TEST_WORKSPACE="+remoteConfig.Workspace, "ROCKETCLAW_REMOTE_TEST_SUFFIX="+suffix)
}
