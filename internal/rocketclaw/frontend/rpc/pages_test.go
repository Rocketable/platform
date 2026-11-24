package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	cronfrontend "github.com/Rocketable/platform/internal/rocketclaw/frontend/cron"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
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
	t.Parallel()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}
	sessions, err := backend.NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	large := bytes.Repeat([]byte("x"), 4<<20+1)
	for space, pages := range map[string][]backend.Page{
		"Billing": {{Kind: backend.PageFile, Name: "index.html", ContentType: "text/html", Body: []byte("<p>billing</p>")}, {Kind: backend.PageData, Name: "rows", ContentType: "application/json", Body: []byte("[1]")}},
		"Ops": {
			{Kind: backend.PageFile, Name: "x.html", ContentType: "text/html", Body: []byte("<p>x</p>")},
			{Kind: backend.PageFile, Name: "docs/index.html", ContentType: "text/html", Body: []byte("<p>docs</p>")},
			{Kind: backend.PageFile, Name: "large.js", ContentType: "text/javascript", Body: large},
			{Kind: backend.PageData, Name: "rows", ContentType: "application/json", Body: []byte(`{"total":3}`)},
		},
		"Ops Team": {{Kind: backend.PageFile, Name: "a b/c.html", ContentType: "text/html", Body: []byte("<p>c</p>")}},
		"Atlas": {
			{Kind: backend.PageFile, Name: "opencode:box:ses_a/index.html", ContentType: "text/html", Body: []byte("<p>a</p>")},
			{Kind: backend.PageFile, Name: "main/review/a b/index.html", ContentType: "text/html", Body: []byte("<p>review</p>")},
		},
		"Gone": {{Kind: backend.PageFile, Name: "index.html", ContentType: "text/html", Body: []byte("<p>gone</p>")}},
	} {
		for _, page := range pages {
			require.NoError(t, sessions.PutPage(t.Context(), &page, "", space))
		}
	}

	spaces, err := sessions.Spaces(t.Context())
	require.NoError(t, err)
	require.NoError(t, sessions.DeleteSpace(t.Context(), spaces[slices.IndexFunc(spaces, func(space backend.Space) bool { return space.Name == "Gone" })].ID))

	listener, err := net.Listen("unix", testSocketPath(t))
	require.NoError(t, err)

	transport := grpc.NewServer()
	New(&mockBackend{SandboxErrorFunc: noSandboxError, SlackWarningFunc: noSlackWarning}, sessions, config.NewLockedConfig(cfg), &mockChannels{}, &mockCronJobs{}).Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() {
		transport.Stop()
		require.NoError(t, serving.Wait())
	})

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	denied := NewHTTPHandler(connection, new(config.LockedConfig), "")
	admitted := signedIn("alice", denied)
	app := httptest.NewRecorder()
	admitted.ServeHTTP(app, httptest.NewRequest(http.MethodGet, "/", http.NoBody))

	for _, tc := range []struct {
		path        string
		handler     http.Handler
		code        int
		body        string
		contentType string
	}{
		{path: "/pages-raw/Ops/x.html", handler: admitted, code: http.StatusOK, body: "<p>x</p>", contentType: "text/html"},
		{path: "/pages-raw/ops/docs/", handler: admitted, code: http.StatusOK, body: "<p>docs</p>", contentType: "text/html"},
		{path: "/pages-raw/Ops/large.js", handler: admitted, code: http.StatusOK, body: string(large), contentType: "text/javascript"},
		{path: "/pages-raw/Atlas/main%2Freview/a%20b/index.html", handler: admitted, code: http.StatusOK, body: "<p>review</p>", contentType: "text/html"},
		{path: "/pages-raw/Atlas/opencode:box:ses_a/index.html", handler: admitted, code: http.StatusOK, body: "<p>a</p>", contentType: "text/html"},
		{path: "/pages-raw/Atlas/opencode%3Abox%3Ases_a/", handler: admitted, code: http.StatusOK, body: "<p>a</p>", contentType: "text/html"},
		{path: "/pages-raw/Gone/index.html", handler: admitted, code: http.StatusOK, body: "<p>gone</p>", contentType: "text/html"},
		{path: "/pages-raw/nobody/x.html", handler: admitted, code: http.StatusNotFound},
		{path: "/pages-raw/Billing/x.html", handler: admitted, code: http.StatusNotFound},
		{path: "/pages-raw/Ops/x.html", handler: denied, code: http.StatusUnauthorized},
		{path: "/pages/OPS/x.html", handler: admitted, code: http.StatusOK, body: app.Body.String()},
		{path: "/pages/Ops%20Team/a%20b/c.html?v=1", handler: admitted, code: http.StatusOK, body: app.Body.String()},
		{path: "/pages/Atlas/opencode%3Abox%3Ases_a/index.html", handler: admitted, code: http.StatusOK, body: app.Body.String()},
		{path: "/pages/Ops/missing.html", handler: admitted, code: http.StatusNotFound},
		{path: "/pages/Ops/x.html", handler: denied, code: http.StatusUnauthorized},
	} {
		request := httptest.NewRequest(http.MethodGet, tc.path, http.NoBody)
		response := httptest.NewRecorder()
		tc.handler.ServeHTTP(response, request)
		require.Equal(t, tc.code, response.Code, tc.path)

		if tc.code != http.StatusOK {
			continue
		}

		require.Equal(t, tc.body, response.Body.String(), tc.path)

		if tc.contentType != "" {
			require.Equal(t, http.Header{
				"Content-Type":                 {tc.contentType},
				"Content-Security-Policy":      {"frame-ancestors 'self'", "sandbox allow-scripts allow-popups allow-forms allow-modals allow-downloads"},
				"Cross-Origin-Resource-Policy": {"cross-origin"},
				"X-Content-Type-Options":       {"nosniff"},
				"Cache-Control":                {"no-store"},
			}, response.Header(), tc.path)
		}
	}

	ctx := metadata.NewOutgoingContext(t.Context(), tailnetUser("alice"))
	for space, want := range map[string]*Page{"ops": {Space: "Ops", ContentType: "application/json", Body: []byte(`{"total":3}`)}, "Billing": {Space: "Billing", ContentType: "application/json", Body: []byte("[1]")}} {
		data, err := invoke[Page](ctx, connection, "GetPage", &PageRequest{Space: space, Path: "rows", Data: true})
		require.NoError(t, err)
		require.True(t, proto.Equal(want, data), "a page reads its own space's Page Data: %v", data)
	}

	listed, err := invoke[ListPagesResponse](ctx, connection, "ListPages", &ListPagesRequest{})
	require.NoError(t, err)
	require.True(t, proto.Equal(&ListPagesResponse{Pages: []*PageEntry{
		{Space: "Atlas", Path: "main/review/a b/index.html"},
		{Space: "Atlas", Path: "opencode:box:ses_a/index.html"},
		{Space: "Billing", Path: "index.html"},
		{Space: "Gone", Path: "index.html"},
		{Space: "Ops", Path: "docs/index.html"},
		{Space: "Ops", Path: "x.html"},
		{Space: "Ops Team", Path: "a b/c.html"},
	}}, listed), "unexpected pages: %v", listed)

	version := func() string {
		response := httptest.NewRecorder()
		admitted.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/ListPages", strings.NewReader(`{"space":"ops","path":"x.html"}`)))
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())

		var found struct {
			Pages []struct {
				Space, Path, UpdatedAt string
			}
		}
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &found))
		require.Len(t, found.Pages, 1)
		page := found.Pages[0]
		require.Equal(t, "Ops", page.Space)
		require.Equal(t, "x.html", page.Path)
		require.NotEmpty(t, page.UpdatedAt)

		return page.UpdatedAt
	}
	before := version()
	require.Equal(t, before, version())
	require.NoError(t, sessions.PutPage(t.Context(), &backend.Page{Kind: backend.PageFile, Name: "x.html", ContentType: "text/html", Body: []byte("<p>updated</p>")}, "", "Ops"))
	require.NotEqual(t, before, version())

	_, err = invoke[Page](t.Context(), connection, "GetPage", &PageRequest{Space: "Ops", Path: "x.html"})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	_, err = invoke[ListPagesResponse](t.Context(), connection, "ListPages", &ListPagesRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	request := httptest.NewRequest(http.MethodPost, "/api/Identity", strings.NewReader(`{}`))
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	request.Header.Set("Origin", "null")

	response := httptest.NewRecorder()
	admitted.ServeHTTP(response, request)
	require.Equal(t, http.StatusForbidden, response.Code)
}

func TestPagesRealBrowser(t *testing.T) {
	t.Parallel()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), Slack: liveSlack()}

	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan *backend.Runtime)
	assembly := &mockFrontendAssembler{GateFunc: passGate, AssembleFunc: func(rt *backend.Runtime) (<-chan struct{}, []func(context.Context) error, error) {
		ready <- rt
		return rt.RunCtx.Done(), nil, nil
	}, ValidateAssetsFunc: func(*config.Config, string) error { return nil }}

	var running errgroup.Group
	running.Go(func() error {
		return backend.Run(ctx, cfg, slog.New(slog.DiscardHandler), new(slog.LevelVar), assembly, sandbox.None{})
	})
	t.Cleanup(func() { cancel(); require.NoError(t, running.Wait()) })

	rt := <-ready
	probe := `<script src="probe.js"></script><script>(async () => {
		await fetch("/api/Identity", { method: "POST", mode: "no-cors", body: "{}" });
		window.result = await fetch("/api/ListSessions").then((response) => response.text(), (error) => error.name);
	})();</script>`
	require.NoError(t, rt.Sessions.PutPage(ctx, &backend.Page{Kind: backend.PageFile, Name: "probe.html", ContentType: "text/html", Body: []byte(probe)}, "", "Main"))
	require.NoError(t, rt.Sessions.PutPage(ctx, &backend.Page{Kind: backend.PageFile, Name: "probe.js", ContentType: "text/javascript", Body: []byte(`window.asset = "loaded";`)}, "", "Main"))

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

	handler := signedIn("alice", NewHTTPHandler(connection, config.NewLockedConfig(cfg), ""))

	var (
		mu      sync.Mutex
		answers []int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.Header.Get("Origin") != "null" {
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

	require.Equal(t, []int{http.StatusForbidden, http.StatusForbidden, http.StatusForbidden, http.StatusForbidden}, answers)
}
