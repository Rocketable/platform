package rpc

import (
	"bufio"
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
)

// relayUpstream records what reached the fake OpenCode server.
type relayUpstream struct {
	hits   int
	auth   string
	origin string
}

func (u *relayUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.hits++
	u.origin = r.Header.Get("Origin")

	if user, password, ok := r.BasicAuth(); ok && user == "opencode" {
		u.auth = password
	}

	if r.URL.Path == "/api/event" {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: hello\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done() // The stream stays open, as OpenCode's does.

		return
	}

	_, _ = w.Write([]byte("opencode"))
}

func TestOpenCodeRelay(t *testing.T) {
	fake := &relayUpstream{}
	upstream := httptest.NewServer(fake)
	t.Cleanup(upstream.Close)

	web := New(&mockBackend{}, nil, config.NewLockedConfig(&config.Config{WebUsers: map[netip.Addr]string{netip.MustParseAddr("192.0.2.10"): "alice"}}), &mockChannels{}, &mockCronJobs{})
	relay := web.AdmitHTTP(NewOpenCodeRelay(config.OpenCodeConfig{URL: upstream.URL, Password: "pw", Port: 4100}))

	serve := func(remote string, header http.Header) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://rocketclaw.example:4100/", nil)

		request.RemoteAddr = remote
		maps.Copy(request.Header, header)

		response := httptest.NewRecorder()
		relay.ServeHTTP(response, request)

		return response
	}

	response := serve("192.0.2.10:5000", nil)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "opencode", response.Body.String())
	assert.Equal(t, "pw", fake.auth, "the relay adds the password")
	assert.Equal(t, "frame-ancestors http://rocketclaw.example:* https://rocketclaw.example:*", response.Header().Get("Content-Security-Policy"))

	before := fake.hits

	assert.Equal(t, http.StatusUnauthorized, serve("192.0.2.99:5000", nil).Code, "a caller the Web does not admit")
	assert.Equal(t, http.StatusUnauthorized, serve("not-an-address", nil).Code, "an unparsable caller")

	for _, header := range []http.Header{
		{"Origin": {"https://evil.example"}},
		{"Origin": {"https://evil.example"}, "Connection": {"Upgrade"}, "Upgrade": {"websocket"}},
	} {
		assert.Equal(t, http.StatusForbidden, serve("192.0.2.10:5000", header).Code)
	}

	assert.Equal(t, before, fake.hits, "refused requests never reach OpenCode")

	assert.Equal(t, http.StatusOK, serve("192.0.2.10:5000", http.Header{"Origin": {"http://rocketclaw.example:4100"}}).Code)
	assert.Equal(t, upstream.URL, fake.origin, "OpenCode sees its own origin")
}

func TestOpenCodeRelayOnTheTailnetStreams(t *testing.T) {
	upstream := httptest.NewServer(&relayUpstream{})
	t.Cleanup(upstream.Close)

	// The tailnet listener admits like the Web there: a Tailscale user, whatever the IP.
	whois := &mockTailnetWhoIs{WhoIsFunc: func(context.Context, string) (*apitype.WhoIsResponse, error) {
		return &apitype.WhoIsResponse{Node: &tailcfg.Node{}, UserProfile: &tailcfg.UserProfile{LoginName: "zoe@example.com"}}, nil
	}}
	relay := httptest.NewTLSServer(NewTailnetHandler(whois, NewOpenCodeRelay(config.OpenCodeConfig{URL: upstream.URL, Password: "pw", Port: 4100})))
	t.Cleanup(relay.Close)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, relay.URL+"/api/event", http.NoBody)
	require.NoError(t, err)

	response, err := relay.Client().Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })

	line, err := bufio.NewReader(response.Body).ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "data: hello\n", line, "events arrive while the stream stays open")
}
