package rpc

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	cronfrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestPages(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}
	sessions, err := backend.NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	agents := cfg.RuntimeDirName() + "/agents/"
	require.NoError(t, root.MkdirAll(agents+"main", 0o700))
	require.NoError(t, root.MkdirAll(agents+"reporter", 0o700))

	for name, frontmatter := range map[string]string{"main": "", "main/review": "", "reporter": "space: Ops\n", "reporter/review": "space: Ops Team\n"} {
		require.NoError(t, root.WriteFile(agents+name+".md", []byte("---\nmodel: example-model\n"+frontmatter+"---\nHelp."), 0o600))
	}

	// Two OpenCode sessions own pages: ses_a in Billing, ses_b without a space.
	_, err = openTestTurns(t, dsn).db.ExecContext(t.Context(), `WITH space AS (INSERT INTO web_spaces (name) VALUES ('Billing') RETURNING id)
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, retention, server, title, directory, last_updated, running, synced_at, remote_session, space_id)
SELECT 'opencode:box:ses_' || n, '', '', 'remote', FALSE, 'remote', 'box', '', '/srv', now(), FALSE, now(), 'ses_' || n, CASE WHEN n = 'a' THEN space.id END
FROM space, unnest(ARRAY['a', 'b']) n`)
	require.NoError(t, err)

	large := bytes.Repeat([]byte("x"), 4<<20+1)
	for _, page := range []backend.Page{
		{Agent: "opencode:box:ses_a", Kind: backend.PageFile, Name: "index.html", ContentType: "text/html", Body: []byte("<p>a</p>"), Conversation: "opencode:box:ses_a"},
		{Agent: "opencode:box:ses_a", Kind: backend.PageData, Name: "rows", ContentType: "application/json", Body: []byte("[1]"), Conversation: "opencode:box:ses_a"},
		{Agent: "opencode:box:ses_b", Kind: backend.PageFile, Name: "index.html", ContentType: "text/html", Body: []byte("<p>b</p>"), Conversation: "opencode:box:ses_b"},
		{Agent: "reporter", Kind: backend.PageFile, Name: "x.html", ContentType: "text/html", Body: []byte("<p>x</p>")},
		{Agent: "reporter", Kind: backend.PageFile, Name: "docs/index.html", ContentType: "text/html", Body: []byte("<p>docs</p>")},
		{Agent: "reporter", Kind: backend.PageFile, Name: "large.js", ContentType: "text/javascript", Body: large},
		{Agent: "reporter", Kind: backend.PageData, Name: "revenue", ContentType: "application/json", Body: []byte(`{"total":3}`)},
		{Agent: "reporter/review", Kind: backend.PageFile, Name: "a b/c.html", ContentType: "text/html", Body: []byte("<p>c</p>")},
		{Agent: "main/review", Kind: backend.PageFile, Name: "a.html", ContentType: "text/html", Body: []byte("<p>review</p>")},
		{Agent: "gone", Kind: backend.PageFile, Name: "index.html", ContentType: "text/html", Body: []byte("<p>gone</p>")},
	} {
		require.NoError(t, sessions.PutPage(t.Context(), &page))
	}

	listener, err := net.Listen("unix", testSocketPath(t))
	require.NoError(t, err)

	transport := grpc.NewServer()
	New(&mockBackend{}, sessions, config.NewLockedConfig(cfg), &mockChannels{}, &mockCronJobs{}).Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() {
		transport.Stop()
		require.NoError(t, serving.Wait())
	})

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	handler := NewHTTPHandler(connection, config.SentryConfig{}, nil, "")
	app := httptest.NewRecorder()
	handler.ServeHTTP(app, httptest.NewRequest(http.MethodGet, "/", http.NoBody))

	const admitted, denied = "127.0.0.1:1234", "192.0.2.1:1234"
	for _, tc := range []struct {
		path, address string
		code          int
		body          string
		contentType   string
		location      string
	}{
		{path: "/pages-raw/reporter/x.html", address: admitted, code: http.StatusOK, body: "<p>x</p>", contentType: "text/html"},
		{path: "/pages-raw/reporter/docs/", address: admitted, code: http.StatusOK, body: "<p>docs</p>", contentType: "text/html"},
		{path: "/pages-raw/reporter/large.js", address: admitted, code: http.StatusOK, body: string(large), contentType: "text/javascript"},
		{path: "/pages-raw/main%2Freview/a.html", address: admitted, code: http.StatusOK, body: "<p>review</p>", contentType: "text/html"},
		{path: "/pages-raw/nobody/x.html", address: admitted, code: http.StatusNotFound},
		{path: "/pages-raw/gone/index.html", address: admitted, code: http.StatusNotFound},
		{path: "/pages-raw/reporter/missing.html", address: admitted, code: http.StatusNotFound},
		{path: "/pages-raw/reporter/x.html", address: denied, code: http.StatusUnauthorized},
		{path: "/pages/billing/reporter/x.html", address: admitted, code: http.StatusPermanentRedirect, location: "/pages/Ops/reporter/x.html"},
		{path: "/pages/Ops/reporter%2Freview/a%20b/c.html?v=1", address: admitted, code: http.StatusPermanentRedirect, location: "/pages/Ops%20Team/reporter%2Freview/a%20b/c.html?v=1"},
		{path: "/pages/OPS/reporter/x.html", address: admitted, code: http.StatusOK, body: app.Body.String()},
		{path: "/pages/billing/main%2Freview/a.html", address: admitted, code: http.StatusOK, body: app.Body.String()},
		{path: "/pages/-/main%2Freview/a.html", address: admitted, code: http.StatusOK, body: app.Body.String()},
		{path: "/pages/Ops/nobody/x.html", address: admitted, code: http.StatusNotFound},
		{path: "/pages-raw/opencode:box:ses_a/index.html", address: admitted, code: http.StatusOK, body: "<p>a</p>", contentType: "text/html"},
		{path: "/pages-raw/opencode%3Abox%3Ases_b/", address: admitted, code: http.StatusOK, body: "<p>b</p>", contentType: "text/html"},
		{path: "/pages/Ops/opencode%3Abox%3Ases_a/index.html", address: admitted, code: http.StatusPermanentRedirect, location: "/pages/Billing/opencode%3Abox%3Ases_a/index.html"},
		{path: "/pages/billing/opencode:box:ses_a/index.html", address: admitted, code: http.StatusOK, body: app.Body.String()},
		{path: "/pages/Ops/reporter/missing.html", address: admitted, code: http.StatusNotFound},
		{path: "/pages/Ops/reporter/x.html", address: denied, code: http.StatusUnauthorized},
	} {
		request := httptest.NewRequest(http.MethodGet, tc.path, http.NoBody)
		request.RemoteAddr = tc.address
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		require.Equal(t, tc.code, response.Code, tc.path)
		require.Equal(t, tc.location, response.Header().Get("Location"), tc.path)

		if tc.code != http.StatusOK {
			continue
		}

		require.Equal(t, tc.body, response.Body.String(), tc.path)

		if tc.contentType != "" {
			require.Equal(t, http.Header{
				"Content-Type":            {tc.contentType},
				"Content-Security-Policy": {"sandbox allow-scripts allow-popups allow-forms allow-modals allow-downloads"},
				"X-Content-Type-Options":  {"nosniff"},
				"Cache-Control":           {"no-store"},
			}, response.Header(), tc.path)
		}
	}

	ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))
	data, err := invoke[Page](ctx, connection, "GetPage", &PageRequest{Agent: "reporter", Path: "revenue", Data: true})
	require.NoError(t, err)
	require.True(t, proto.Equal(&Page{Space: "Ops", ContentType: "application/json", Body: []byte(`{"total":3}`)}, data), "unexpected page data: %v", data)

	data, err = invoke[Page](ctx, connection, "GetPage", &PageRequest{Agent: "opencode:box:ses_a", Path: "rows", Data: true})
	require.NoError(t, err)
	require.True(t, proto.Equal(&Page{Space: "Billing", ContentType: "application/json", Body: []byte("[1]")}, data), "unexpected session page data: %v", data)

	// A session's HTML pages list under its space; one with no space lists nowhere.
	listed, err := invoke[ListPagesResponse](ctx, connection, "ListPages", &ListPagesRequest{})
	require.NoError(t, err)
	require.True(t, proto.Equal(&ListPagesResponse{Pages: []*PageEntry{
		{Agent: "main/review", Path: "a.html"},
		{Agent: "opencode:box:ses_a", Space: "Billing", Path: "index.html"},
		{Agent: "reporter", Space: "Ops", Path: "docs/index.html"},
		{Agent: "reporter", Space: "Ops", Path: "x.html"},
		{Agent: "reporter/review", Space: "Ops Team", Path: "a b/c.html"},
	}}, listed), "unexpected pages: %v", listed)

	deniedCtx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "192.0.2.1"))
	_, err = invoke[Page](deniedCtx, connection, "GetPage", &PageRequest{Agent: "reporter", Path: "x.html"})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	_, err = invoke[ListPagesResponse](deniedCtx, connection, "ListPages", &ListPagesRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	request := httptest.NewRequest(http.MethodPost, "/api/Identity", strings.NewReader(`{}`))
	request.RemoteAddr = admitted
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	request.Header.Set("Origin", "null")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusForbidden, response.Code)
}

// AE4: a page's script cannot use the API as the viewer, opened directly or inside its tab.
func TestPagesRealBrowser(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice"}}
	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.Mkdir("agents", 0o700))
	require.NoError(t, root.WriteFile("agents/main.md", []byte("---\nmodel: test-model\n---\nHelp.\n"), 0o600))

	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan *backend.Runtime)
	assembly := &mockFrontendAssembler{AssembleFunc: func(rt *backend.Runtime) (backend.SlackFrontend, <-chan struct{}, []func(context.Context) error, error) {
		ready <- rt
		return nil, rt.RunCtx.Done(), nil, nil // Public assembler permits absent Slack.
	}, ValidateAssetsFunc: func(*config.Config, string, []string) error { return nil }}

	var running errgroup.Group
	running.Go(func() error { return backend.Run(ctx, cfg, "", slog.New(slog.DiscardHandler), assembly) })
	t.Cleanup(func() { cancel(); require.NoError(t, running.Wait()) })

	rt := <-ready
	// The probe acts as the viewer with a POST that needs no response, then reads a GET.
	probe := `<script>(async () => {
		await fetch("/api/Identity", { method: "POST", mode: "no-cors", body: "{}" });
		window.result = await fetch("/api/ListSessions").then((response) => response.text(), (error) => error.name);
	})();</script>`
	require.NoError(t, rt.Sessions.PutPage(ctx, &backend.Page{Agent: "main", Kind: backend.PageFile, Name: "probe.html", ContentType: "text/html", Body: []byte(probe)}))

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

	// Pages have an opaque origin, so their requests carry Origin: null; their POSTs' answers are recorded.
	handler := NewHTTPHandler(connection, cfg.Web.Sentry, cfg.OpenCode, "")

	var (
		mu      sync.Mutex
		answers []int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Origin") != "null" {
			handler.ServeHTTP(w, r)
			return
		}

		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		mu.Lock()

		answers = append(answers, recorder.Code)
		mu.Unlock()
		w.WriteHeader(recorder.Code)
	}))
	t.Cleanup(server.Close)
	runWebTest(t, "src/pages-transport.test.ts", "ROCKETCLAW_PAGES_TEST_URL="+server.URL)

	mu.Lock()
	defer mu.Unlock()

	require.Equal(t, []int{http.StatusForbidden, http.StatusForbidden}, answers)
}
