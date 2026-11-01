package rpc

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

// proxyUpstream records what reached the fake OpenCode server.
type proxyUpstream struct {
	hits                            int
	auth, origin, cookie, uri, body string
}

func (u *proxyUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.hits++
	u.origin, u.cookie, u.uri = r.Header.Get("Origin"), r.Header.Get("Cookie"), r.URL.RequestURI()
	body, _ := io.ReadAll(r.Body)
	u.body = string(body)

	if user, password, ok := r.BasicAuth(); ok && user == "opencode" {
		u.auth = password
	}

	w.Header().Set("Set-Cookie", "opencode_session=secret")
	w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin")) // OpenCode answers its own CORS.

	switch r.URL.Path {
	case "/api/event":
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: hello\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done() // The stream stays open, as OpenCode's does.
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

// startProxyTestRPC serves a Web that admits 192.0.2.10 and 127.0.0.1 and relays box to upstream.
func startProxyTestRPC(t *testing.T, upstream string) http.Handler {
	t.Helper()

	cfg := &config.Config{}
	listener, err := net.Listen("unix", testSocketPath(t))
	require.NoError(t, err)

	transport := grpc.NewServer()
	New(&mockBackend{SandboxErrorFunc: noSandboxError}, nil, config.NewLockedConfig(cfg), &mockChannels{}, &mockCronJobs{}).Register(transport)

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

	return NewHTTPHandler(connection, config.SentryConfig{}, config.PostHogConfig{}, map[string]config.OpenCodeConfig{"box": {URL: upstream, Password: "pw"}}, "", nil)
}

func TestOpenCodeProxy(t *testing.T) {
	fake := &proxyUpstream{}
	upstream := httptest.NewServer(fake)
	t.Cleanup(upstream.Close)

	handler := startProxyTestRPC(t, upstream.URL)

	serve := func(method, target, remote string, header http.Header) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), method, "http://rocketclaw.example"+target, nil)

		request.RemoteAddr = remote
		maps.Copy(request.Header, header)

		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		return response
	}

	response := serve(http.MethodGet, "/oc/box/api/session?parentID=null", "192.0.2.10:5000", http.Header{"Cookie": {"rocketclaw=1"}})
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "opencode", response.Body.String())
	assert.Equal(t, "/api/session?parentID=null", fake.uri, "the prefix is stripped and the query kept")
	assert.Equal(t, "pw", fake.auth, "the proxy adds the password")
	assert.Empty(t, fake.cookie, "RocketClaw's cookies never reach OpenCode")
	assert.Empty(t, response.Header().Values("Set-Cookie"), "OpenCode never sets cookies on RocketClaw's origin")
	assert.Equal(t, "sandbox", response.Header().Get("Content-Security-Policy"))
	assert.Equal(t, "nosniff", response.Header().Get("X-Content-Type-Options"))

	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		assert.Equal(t, http.StatusOK, serve(method, "/oc/box/api/session/ses_a", "192.0.2.10:5000", nil).Code, method)
		assert.Equal(t, "/api/session/ses_a", fake.uri, method)
	}

	response = serve(http.MethodPost, "/oc/box/api/denied", "192.0.2.10:5000", nil)
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	assert.Empty(t, response.Header().Get("WWW-Authenticate"), "the browser never prompts for OpenCode's password")
	assert.Equal(t, "sandbox", response.Header().Get("Content-Security-Policy"))

	before := fake.hits

	assert.Equal(t, http.StatusUnauthorized, serve(http.MethodGet, "/oc/box/api/session", "192.0.2.99:5000", nil).Code, "a caller the Web does not admit")
	assert.Equal(t, http.StatusNotFound, serve(http.MethodGet, "/oc/nope/api/session", "192.0.2.10:5000", nil).Code, "a server that is not configured")
	assert.NotEqual(t, "opencode", serve(http.MethodGet, "/oc/box/other", "192.0.2.10:5000", nil).Body.String(), "only the API is proxied")

	for _, header := range []http.Header{
		{"Origin": {"https://evil.example"}},
		{"Origin": {"null"}},
		{"Origin": {"https://evil.example"}, "Connection": {"Upgrade"}, "Upgrade": {"websocket"}},
	} {
		assert.Equal(t, http.StatusForbidden, serve(http.MethodGet, "/oc/box/api/session", "192.0.2.10:5000", header).Code, header)
	}

	assert.Equal(t, before, fake.hits, "refused requests never reach OpenCode")

	assert.Equal(t, http.StatusOK, serve(http.MethodGet, "/oc/box/api/session", "192.0.2.10:5000", http.Header{"Origin": {"http://rocketclaw.example"}}).Code)
	assert.Equal(t, upstream.URL, fake.origin, "OpenCode sees its own origin")

	// Prompts carry the admitted sender; everything else reaches OpenCode byte-for-byte.
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
		request.RemoteAddr = "192.0.2.10:5000"

		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, test.body)

		if test.want == test.body {
			assert.Equal(t, test.want, fake.body, "%s %s passes unchanged", test.method, test.path)
		} else {
			assert.JSONEq(t, test.want, fake.body, test.body)
		}
	}
}

func TestOpenCodeProxyStreamsAndUpgrades(t *testing.T) {
	fake := &proxyUpstream{}
	upstream := httptest.NewServer(fake)
	t.Cleanup(upstream.Close)

	web := httptest.NewServer(startProxyTestRPC(t, upstream.URL))
	t.Cleanup(web.Close)

	// The tailnet listener admits like the Web there: a Tailscale user, whatever the IP.
	tagged := false
	whois := &mockTailnetWhoIs{WhoIsFunc: func(context.Context, string) (*apitype.WhoIsResponse, error) {
		node := &tailcfg.Node{}
		if tagged {
			node.Tags = []string{"tag:server"}
		}

		return &apitype.WhoIsResponse{Node: node, UserProfile: &tailcfg.UserProfile{LoginName: "zoe@example.com"}}, nil
	}}
	tailnet := httptest.NewTLSServer(NewTailnetHandler(whois, []string{"https://local.tail1234.ts.net"}, startProxyTestRPC(t, upstream.URL)))
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

	// A browser on the tailnet sends its HTTPS origin, which is the proxy's own.
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, tailnet.URL+"/oc/box/api/session", http.NoBody)
	require.NoError(t, err)
	request.Header.Set("Origin", tailnet.URL)

	response, err := tailnet.Client().Do(request)
	require.NoError(t, err)

	_ = response.Body.Close()

	assert.Equal(t, http.StatusOK, response.StatusCode, "a same-origin request over TLS")

	// A RocketClaw page this server trusts reaches OpenCode, with only RocketClaw's CORS answer.
	request, err = http.NewRequestWithContext(t.Context(), http.MethodDelete, tailnet.URL+"/oc/box/api/session/ses_a", http.NoBody)
	require.NoError(t, err)
	request.Header.Set("Origin", "https://local.tail1234.ts.net")

	response, err = tailnet.Client().Do(request)
	require.NoError(t, err)

	_ = response.Body.Close()

	assert.Equal(t, http.StatusOK, response.StatusCode, "a trusted page passes the relay's origin check")
	assert.Equal(t, []string{"https://local.tail1234.ts.net"}, response.Header.Values("Access-Control-Allow-Origin"))

	tagged = true
	request, err = http.NewRequestWithContext(t.Context(), http.MethodGet, tailnet.URL+"/oc/box/api/session", http.NoBody)
	require.NoError(t, err)

	response, err = tailnet.Client().Do(request)
	require.NoError(t, err)

	_ = response.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, response.StatusCode, "a tagged node is refused")

	// A same-origin WebSocket upgrade reaches the terminal with OpenCode's own origin, both ways.
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
