package rpc

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

type proxyUpstream struct {
	auth, origin, cookie, uri, body, bridge string
	calls                                   []string
	admitted                                int64
	refuse                                  bool
}

func (u *proxyUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.calls = append(u.calls, r.Method+" "+r.URL.RequestURI())
	body, _ := io.ReadAll(r.Body)

	switch r.Method + " " + r.URL.Path {
	case "GET /api/session/s1":
		_, _ = w.Write([]byte(`{"data":{"id":"s1","location":{"directory":"/srv/s1"}}}`))
		return
	case "PUT /api/experimental/mcp/rocketclaw":
		u.bridge = string(body)

		w.WriteHeader(http.StatusNoContent)

		return
	}

	u.origin, u.cookie, u.uri = r.Header.Get("Origin"), r.Header.Get("Cookie"), r.URL.RequestURI()
	u.body = string(body)

	if user, password, ok := r.BasicAuth(); ok && user == "opencode" {
		u.auth = password
	}

	w.Header().Set("Set-Cookie", "opencode_session=secret")
	w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))

	if prompt, _ := path.Match("/api/session/*/prompt", r.URL.Path); prompt && r.Method == http.MethodPost {
		if u.refuse {
			w.WriteHeader(http.StatusConflict)
			return
		}

		var out io.Writer = w

		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")

			compressed := gzip.NewWriter(w)
			defer func() { _ = compressed.Close() }()

			out = compressed
		}

		_, _ = fmt.Fprintf(out, `{"data":{"id":"msg_1","time":{"created":%d}}}`, u.admitted)

		return
	}

	switch r.URL.Path {
	case "/api/event":
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: hello\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	case "/api/denied":
		w.Header().Set("WWW-Authenticate", `Basic realm="opencode"`)
		w.WriteHeader(http.StatusUnauthorized)
	case "/api/pty/x/connect":
		connection, buffered, _ := http.NewResponseController(w).Hijack()
		defer func() { _ = connection.Close() }()

		_, _ = buffered.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		_ = buffered.Flush()
		_, _ = io.Copy(connection, buffered)
	default:
		_, _ = w.Write([]byte("opencode"))
	}
}

func startProxyTestRPC(t *testing.T, upstream string, channels ChannelAgentChoices) (http.Handler, *backend.SessionService, *sql.DB) {
	t.Helper()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	servers := map[string]config.OpenCodeConfig{"box": {URL: upstream, Password: "pw"}}
	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), OpenCode: servers, Permissions: map[string][]string{"bob": {"Ops"}}, Web: config.WebConfig{Tsnet: &config.TsnetConfig{Origin: "https://claw.example.ts.net"}}}
	sessions, err := backend.NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	listener, err := net.Listen("unix", testSocketPath(t))
	require.NoError(t, err)

	transport := grpc.NewServer()
	New(&mockBackend{SandboxErrorFunc: noSandboxError, SlackWarningFunc: noSlackWarning}, sessions, config.NewLockedConfig(cfg), channels, &mockRoutines{}).Register(transport)

	var serving errgroup.Group
	serving.Go(func() error {
		if err := transport.Serve(listener); err != nil {
			return fmt.Errorf("serve test RPC: %w", err)
		}

		return nil
	})
	t.Cleanup(func() {
		transport.Stop()
		require.NoError(t, serving.Wait())
	})

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	return NewHTTPHandler(connection, config.NewLockedConfig(cfg), ""), sessions, openTestTurns(t, dsn).db
}

func TestOpenCodeProxy(t *testing.T) {
	t.Parallel()

	fake := &proxyUpstream{}
	upstream := httptest.NewServer(fake)
	t.Cleanup(upstream.Close)

	channels := &mockChannels{SendSessionMentionFunc: func(_ context.Context, id, link string) error {
		assert.Equal(t, "https://claw.example.ts.net/oc/b3BlbmNvZGU6Ym94OnMy", link)

		if id == "U4" {
			return errors.New("delivery refused")
		}

		return nil
	}}
	channels.SlackMentionEmailFunc = func(_ context.Context, id string) string {
		assert.Len(t, channels.SendSessionMentionCalls(), 4)

		switch id {
		case "U1":
			return "riley@example.com"
		case "U2":
			return "bob"
		case "U3":
			return "sam@example.com"
		default:
			return ""
		}
	}
	handler, sessions, db := startProxyTestRPC(t, upstream.URL, channels)

	serve := func(method, target, login string, header http.Header) *httptest.ResponseRecorder {
		var body io.Reader
		if method == http.MethodPost {
			body = strings.NewReader(`{"parts":[]}`)
		}

		request := httptest.NewRequestWithContext(t.Context(), method, "http://rocketclaw.example"+target, body)

		maps.Copy(request.Header, header)

		caller := handler
		if login != "" {
			caller = signedIn(login, handler)
		}

		response := httptest.NewRecorder()
		caller.ServeHTTP(response, request)

		return response
	}

	response := serve(http.MethodGet, "/oc/box/api/session?parentID=null", "alice", http.Header{"Cookie": {"rocketclaw=1"}})
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "opencode", response.Body.String())
	assert.Equal(t, "/api/session?parentID=null", fake.uri, "the prefix is stripped and the query kept")
	assert.Equal(t, "pw", fake.auth, "the proxy adds the password")
	assert.Empty(t, fake.cookie, "RocketClaw's cookies never reach OpenCode")
	assert.Empty(t, response.Header().Values("Set-Cookie"), "OpenCode never sets cookies on RocketClaw's origin")
	assert.Contains(t, response.Header().Values("Content-Security-Policy"), "sandbox")
	assert.Equal(t, "nosniff", response.Header().Get("X-Content-Type-Options"))

	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		assert.Equal(t, http.StatusOK, serve(method, "/oc/box/api/session/ses_a", "alice", nil).Code, method)
		assert.Equal(t, "/api/session/ses_a", fake.uri, method)
	}

	response = serve(http.MethodPost, "/oc/box/api/denied", "alice", nil)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	assert.Empty(t, response.Header().Get("WWW-Authenticate"), "the browser never prompts for OpenCode's password")
	assert.Contains(t, response.Header().Values("Content-Security-Policy"), "sandbox")

	before := len(fake.calls)

	assert.Equal(t, http.StatusUnauthorized, serve(http.MethodGet, "/oc/box/api/session", "", nil).Code, "a caller the Web does not admit")
	assert.Equal(t, http.StatusForbidden, serve(http.MethodGet, "/oc/box/api/session", "bob", nil).Code, "a person limited to spaces")
	assert.Equal(t, http.StatusNotFound, serve(http.MethodGet, "/oc/nope/api/session", "alice", nil).Code, "a server that is not configured")
	assert.NotEqual(t, "opencode", serve(http.MethodGet, "/oc/box/other", "alice", nil).Body.String(), "only the API is proxied")

	for _, header := range []http.Header{
		{"Origin": {"https://evil.example"}},
		{"Origin": {"null"}},
		{"Origin": {"https://evil.example"}, "Connection": {"Upgrade"}, "Upgrade": {"websocket"}},
	} {
		assert.Equal(t, http.StatusForbidden, serve(http.MethodGet, "/oc/box/api/session", "alice", header).Code, header)
	}

	assert.Len(t, fake.calls, before, "refused requests never reach OpenCode")

	assert.Equal(t, http.StatusOK, serve(http.MethodGet, "/oc/box/api/session", "alice", http.Header{"Origin": {"http://rocketclaw.example"}}).Code)
	assert.Equal(t, upstream.URL, fake.origin, "OpenCode sees its own origin")

	for _, test := range []struct{ method, path, body, want string }{
		{http.MethodPost, "/api/session/s1/prompt", `{"parts":[],"metadata":{"displayText":"hi","agent":"build"}}`, `{"parts":[],"metadata":{"displayText":"hi","agent":"build","rocketclaw_username":"alice"}}`},
		{http.MethodPost, "/api/session/s1/prompt", `{"parts":[]}`, `{"parts":[],"metadata":{"rocketclaw_username":"alice"}}`},
		{http.MethodPost, "/api/session/s1/prompt", `{"metadata":{"rocketclaw_username":"mallory"}}`, `{"metadata":{"rocketclaw_username":"alice"}}`},
		{http.MethodPost, "/api/session/s1/prompt", `not json`, `not json`},
		{http.MethodPost, "/api/session/s1/prompt", ``, ``},
		{http.MethodPost, "/api/session/s1/command", `{"metadata":{"rocketclaw_username":"mallory"}}`, `{"metadata":{"rocketclaw_username":"mallory"}}`},
		{http.MethodPost, "/api/session/s1/shell", `{ "command": "ls" }`, `{ "command": "ls" }`},
		{http.MethodGet, "/api/session/s1/prompt", `{ "parts": [] }`, `{ "parts": [] }`},
	} {
		request := httptest.NewRequestWithContext(t.Context(), test.method, "http://rocketclaw.example/oc/box"+test.path, strings.NewReader(test.body))
		response := httptest.NewRecorder()
		signedIn("alice", handler).ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, test.body)

		if test.want == test.body {
			assert.Equal(t, test.want, fake.body, "%s %s passes unchanged", test.method, test.path)
		} else {
			assert.JSONEq(t, test.want, fake.body, test.body)
		}
	}

	fake.calls = nil

	require.Equal(t, http.StatusOK, serve(http.MethodPost, "/oc/box/api/session/s1/prompt", "alice", nil).Code)
	assert.Equal(t, []string{"GET /api/session/s1", "PUT /api/experimental/mcp/rocketclaw?location%5Bdirectory%5D=%2Fsrv%2Fs1", "POST /api/session/s1/prompt"}, fake.calls, "the bridge reaches the session's folder before its prompt does")
	assert.JSONEq(t, `{"config":{"type":"remote","url":"https://claw.example.ts.net/opencode/box/mcp","oauth":false,"headers":{"Authorization":"Basic b3BlbmNvZGU6cHc="}}}`, fake.bridge)

	_, err := db.ExecContext(t.Context(), `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, retention, server, remote_session, title, name, directory, last_updated, synced_at)
VALUES ('opencode:box:s2', '', '', 'remote', FALSE, 'remote', 'box', 's2', '', '', '/srv', now(), now())`)
	require.NoError(t, err)

	parked := func() (bool, sql.NullInt64) {
		t.Helper()

		var (
			parked    bool
			afterIdle sql.NullInt64
		)

		err := db.QueryRowContext(t.Context(), `SELECT parked AND until = 'infinity', after_idle FROM web_tab_attention WHERE username = 'alice' AND conversation_id = 'opencode:box:s2'`).Scan(&parked, &afterIdle)
		if errors.Is(err, sql.ErrNoRows) {
			return false, sql.NullInt64{}
		}

		require.NoError(t, err)

		return parked, afterIdle
	}

	fake.refuse = true

	require.Equal(t, http.StatusConflict, serve(http.MethodPost, "/oc/box/api/session/s2/prompt", "alice", nil).Code)

	on, _ := parked()
	assert.False(t, on, "a prompt OpenCode refused parks nothing")

	fake.refuse, fake.admitted = false, 1_795_551_291_768
	response = serve(http.MethodPost, "/oc/box/api/session/s2/prompt", "alice", http.Header{"Accept-Encoding": {"gzip, deflate, br, zstd"}})
	require.Equal(t, http.StatusOK, response.Code)
	assert.JSONEq(t, `{"data":{"id":"msg_1","time":{"created":1795551291768}}}`, response.Body.String(), "the browser gets OpenCode's answer")

	on, afterIdle := parked()
	assert.True(t, on, "an admitted prompt parks its tab until the session is done")
	assert.Equal(t, sql.NullInt64{Int64: 1_795_551_291_768, Valid: true}, afterIdle, "the park waits for an idle marker after OpenCode admitted the prompt, even when OpenCode compressed its answer")

	_, err = db.ExecContext(t.Context(), `INSERT INTO web_tab_states (username, revision) VALUES ('Riley@example.com', 0), ('bob', 0), ('sam@example.com', 0)`)
	require.NoError(t, err)
	require.NoError(t, sessions.ChangeAttention(t.Context(), "sam@example.com", []string{"/oc/b3BlbmNvZGU6Ym94OnMy"}, backend.AttentionPark, nil, 0))

	post := func(tags string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/oc/box/api/session/s2/prompt", strings.NewReader(`{"text":"Hi @Riley","metadata":{"rocketclaw_mention_ids":`+tags+`}}`))
		response := httptest.NewRecorder()
		signedIn("alice", handler).ServeHTTP(response, request)

		return response
	}

	fake.refuse = true

	require.Equal(t, http.StatusConflict, post(`["U1"]`).Code)

	fake.refuse = false

	for _, test := range []struct{ tags, warning string }{
		{`["U1","U2","U3","U4","U5","U6"]`, "Teammate notifications could not be sent."},
		{`{"U1":true}`, "Invalid teammate tags."},
	} {
		response = post(test.tags)
		require.Equal(t, http.StatusOK, response.Code)
		require.Equal(t, test.warning, response.Header().Get(mentionWarningHeader))
		require.JSONEq(t, `{"text":"Hi @Riley","metadata":{"rocketclaw_username":"alice"}}`, fake.body)
		require.Empty(t, channels.SendSessionMentionCalls())
	}

	response = post(`["U1","U1","U2","U3","U4"]`)
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "One or more teammate notifications could not be sent.", response.Header().Get(mentionWarningHeader))
	require.JSONEq(t, `{"text":"Hi @Riley","metadata":{"rocketclaw_username":"alice"}}`, fake.body)
	require.Len(t, channels.SendSessionMentionCalls(), 4)

	var marked string
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT coalesce(string_agg(username, ',' ORDER BY username), '') FROM web_tab_attention
WHERE conversation_id = 'opencode:box:s2' AND unread_at IS NOT NULL`).Scan(&marked))
	require.Equal(t, "Riley@example.com", marked)
}

func TestOpenCodeProxyStreamsAndUpgrades(t *testing.T) {
	t.Parallel()

	fake := &proxyUpstream{}
	upstream := httptest.NewServer(fake)
	t.Cleanup(upstream.Close)

	proxy, _, _ := startProxyTestRPC(t, upstream.URL, &mockChannels{})
	web := httptest.NewServer(signedIn("alice", proxy))
	t.Cleanup(web.Close)

	tagged := false
	whois := &mockTailnetWhoIs{WhoIsFunc: func(context.Context, string) (*apitype.WhoIsResponse, error) {
		node := &tailcfg.Node{}
		if tagged {
			node.Tags = []string{"tag:server"}
		}

		return &apitype.WhoIsResponse{Node: node, UserProfile: &tailcfg.UserProfile{LoginName: "zoe@example.com"}}, nil
	}}
	proxy, _, _ = startProxyTestRPC(t, upstream.URL, &mockChannels{})
	tailnet := httptest.NewTLSServer(NewTailnetHandler(whois, []string{"https://local.tail1234.ts.net"}, io.Discard, proxy))
	t.Cleanup(tailnet.Close)

	for _, listener := range []*httptest.Server{web, tailnet} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, listener.URL+"/oc/box/api/event", http.NoBody)
		require.NoError(t, err)

		response, err := listener.Client().Do(request)
		require.NoError(t, err)
		t.Cleanup(func() { _ = response.Body.Close() })

		line, err := bufio.NewReader(response.Body).ReadString('\n')
		require.NoError(t, err)
		assert.Equal(t, "data: hello\n", line, "events arrive while the stream stays open")
	}

	prompt, err := http.NewRequestWithContext(t.Context(), http.MethodPost, tailnet.URL+"/oc/box/api/session/s1/prompt", strings.NewReader(`{"parts":[]}`))
	require.NoError(t, err)

	sent, err := tailnet.Client().Do(prompt)
	require.NoError(t, err)
	require.NoError(t, sent.Body.Close())
	require.Equal(t, http.StatusOK, sent.StatusCode)
	assert.JSONEq(t, `{"parts":[],"metadata":{"rocketclaw_username":"zoe@example.com"}}`, fake.body, "a tailnet prompt carries the tailnet login")

	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, tailnet.URL+"/oc/box/api/session", http.NoBody)
	require.NoError(t, err)
	request.Header.Set("Origin", tailnet.URL)

	response, err := tailnet.Client().Do(request)
	require.NoError(t, err)

	_ = response.Body.Close()

	assert.Equal(t, http.StatusOK, response.StatusCode, "a same-origin request over TLS")

	request, err = http.NewRequestWithContext(t.Context(), http.MethodDelete, tailnet.URL+"/oc/box/api/session/ses_a", http.NoBody)
	require.NoError(t, err)
	request.Header.Set("Origin", "https://local.tail1234.ts.net")

	response, err = tailnet.Client().Do(request)
	require.NoError(t, err)

	_ = response.Body.Close()

	assert.Equal(t, http.StatusOK, response.StatusCode, "a trusted page passes the relay's origin check")
	assert.Equal(t, []string{"https://local.tail1234.ts.net"}, response.Header.Values("Access-Control-Allow-Origin"))
	assert.Equal(t, mentionWarningHeader, response.Header.Get("Access-Control-Expose-Headers"))

	tagged = true
	request, err = http.NewRequestWithContext(t.Context(), http.MethodGet, tailnet.URL+"/oc/box/api/session", http.NoBody)
	require.NoError(t, err)

	response, err = tailnet.Client().Do(request)
	require.NoError(t, err)

	_ = response.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, response.StatusCode, "a tagged node is refused")

	connection, err := net.Dial("tcp", web.Listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })

	_, err = fmt.Fprintf(connection, "GET /oc/box/api/pty/x/connect?ticket=t HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n", web.Listener.Addr(), web.URL)
	require.NoError(t, err)

	reader := bufio.NewReader(connection)
	upgraded, err := http.ReadResponse(reader, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = upgraded.Body.Close() })
	assert.Equal(t, http.StatusSwitchingProtocols, upgraded.StatusCode)
	assert.Equal(t, upstream.URL, fake.origin)

	_, err = connection.Write([]byte("ping"))
	require.NoError(t, err)

	echo := make([]byte, 4)
	_, err = io.ReadFull(reader, echo)
	require.NoError(t, err)
	assert.Equal(t, "ping", string(echo))
}

func TestOpenCodeProxyLogsOnlyFailuresTheBrowserWaitsFor(t *testing.T) {
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()

	var logged bytes.Buffer

	previous := log.Writer()

	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })

	proxy := openCodeProxy(config.OpenCodeConfig{URL: gone.URL})

	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/event", http.NoBody))
	assert.Equal(t, http.StatusBadGateway, response.Code)
	assert.Contains(t, logged.String(), "http: proxy error: ")

	logged.Reset()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	proxy.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/event", http.NoBody))
	assert.Empty(t, logged.String())
}

func TestWatchListing(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(&proxyUpstream{})
	t.Cleanup(upstream.Close)

	handler, sessions, _ := startProxyTestRPC(t, upstream.URL, &mockChannels{})
	web := httptest.NewServer(signedIn("alice", handler))
	t.Cleanup(web.Close)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, web.URL+"/listing", http.NoBody)
	require.NoError(t, err)

	response, err := web.Client().Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })
	require.Equal(t, "text/event-stream", response.Header.Get("Content-Type"))

	events := bufio.NewReader(response.Body)
	next := func() string {
		t.Helper()

		for {
			line, err := events.ReadString('\n')
			require.NoError(t, err)

			if data, ok := strings.CutPrefix(line, "data: "); ok {
				return strings.TrimSpace(data)
			}
		}
	}

	assert.Equal(t, "{}", next(), "a watch opens with an event, so a window that reconnects fetches what it missed")

	require.NoError(t, sessions.ChangeAttention(t.Context(), "alice", []string{"/cron"}, backend.AttentionUnread, nil, 0))
	assert.Equal(t, "{}", next(), "a change to her tabs reaches her window at once")

	whois := &mockTailnetWhoIs{WhoIsFunc: func(context.Context, string) (*apitype.WhoIsResponse, error) {
		return &apitype.WhoIsResponse{Node: &tailcfg.Node{}, UserProfile: &tailcfg.UserProfile{LoginName: "zoe@example.com"}}, nil
	}}
	tailnet := httptest.NewTLSServer(NewTailnetHandler(whois, []string{"https://other.tail1234.ts.net"}, io.Discard, handler))
	t.Cleanup(tailnet.Close)

	request, err = http.NewRequestWithContext(t.Context(), http.MethodGet, tailnet.URL+"/listing", http.NoBody)
	require.NoError(t, err)
	request.Header.Set("Origin", "https://other.tail1234.ts.net")

	remote, err := tailnet.Client().Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = remote.Body.Close() })
	assert.Equal(t, http.StatusOK, remote.StatusCode)
	assert.Equal(t, []string{"https://other.tail1234.ts.net"}, remote.Header.Values("Access-Control-Allow-Origin"), "another server's page can watch this server's listing")
}
