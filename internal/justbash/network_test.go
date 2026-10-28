package justbash

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/justbash/justbashpb"
)

func rulesOf(t *testing.T, entries ...any) networkRules {
	t.Helper()

	rules, err := parseNetworkRules(entries)
	require.NoError(t, err)

	return rules
}

func TestNetworkRulesMatch(t *testing.T) {
	t.Parallel()

	rules := rulesOf(t,
		"api.stripe.com",
		"*.googleapis.com",
		"127.0.0.1:8080",
		"10.0.0.0/8",
		map[string]any{"target": "https://api.example.com/v1", "methods": []any{"GET"}},
	)

	cases := []struct {
		method, url string
		want        bool
	}{
		{"POST", "https://api.stripe.com/anything", true},
		{"GET", "https://API.STRIPE.COM:8443/x", true},
		{"GET", "https://evil.com/api.stripe.com", false},
		{"GET", "https://storage.googleapis.com/b", true},
		{"GET", "https://googleapis.com/b", false},
		{"GET", "http://127.0.0.1:8080/", true},
		{"GET", "http://127.0.0.1:8081/", false},
		{"GET", "http://10.1.2.3/", true},
		{"GET", "https://api.example.com/v1", true},
		{"GET", "https://api.example.com/v1/charges", true},
		{"POST", "https://api.example.com/v1/charges", false},
		{"GET", "https://api.example.com/v1evil", false},
		{"GET", "https://api.example.com.evil.com/v1/x", false},
		{"GET", "http://api.example.com/v1/x", false},
		{"GET", "https://api.example.com/v1/%2e%2e/x", false},
		{"GET", "https://api.example.com/v1/a%2fb", false},
	}
	for _, tc := range cases {
		u, err := url.Parse(tc.url)
		require.NoError(t, err)

		_, got := rules.match(u, tc.method)
		require.Equal(t, tc.want, got, "%s %s", tc.method, tc.url)
	}

	_, got := networkRules{auto: true}.match(&url.URL{Scheme: "http", Host: "anything"}, "DELETE")
	require.True(t, got)
}

func TestNonPublic(t *testing.T) {
	t.Parallel()

	for _, s := range []string{"127.0.0.1", "169.254.169.254", "10.1.2.3", "172.16.0.1", "192.168.1.1", "100.64.0.1", "0.0.0.0", "::1", "fc00::1", "fe80::1", "::ffff:127.0.0.1", "64:ff9b::a01:203", "2002:a00:1::"} {
		require.True(t, nonPublic(netip.MustParseAddr(s)), s)
	}

	for _, s := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		require.False(t, nonPublic(netip.MustParseAddr(s)), s)
	}
}

func TestTargetNames(t *testing.T) {
	t.Parallel()

	loopback := netip.MustParseAddr("127.0.0.1")
	rules := rulesOf(t, "localhost", "*.internal.example", "127.0.0.1:9", "127.0.0.0/8", "http://metadata.internal/")
	require.True(t, rules.targets[0].names("localhost", loopback))
	require.False(t, rules.targets[1].names("db.internal.example", loopback), "wildcards never lift the block")
	require.True(t, rules.targets[2].names("127.0.0.1", loopback))
	require.True(t, rules.targets[3].names("127.0.0.1", loopback))
	require.True(t, rules.targets[4].names("metadata.internal", netip.MustParseAddr("169.254.169.254")))
	require.False(t, (&target{}).names("localhost", loopback), "auto never lifts the block")
}

func TestFetch(t *testing.T) {
	t.Parallel()

	otherSaw := make(chan string, 1)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case otherSaw <- r.Header.Get("Authorization") + "|" + r.Header.Get("Cookie") + "|" + r.Header.Get("X-Keep"):
		default:
		}

		_, _ = w.Write([]byte("other"))
	}))
	t.Cleanup(other.Close)

	big := strings.Repeat("x", 6<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, other.URL+"/", http.StatusFound)
		case "/big":
			_, _ = w.Write([]byte(big))
		default:
			w.Header().Set("X-Seen", r.Method+" "+r.Header.Get("X-Test"))
			_, _ = w.Write([]byte("hello"))
		}
	}))
	t.Cleanup(srv.Close)

	host := strings.TrimPrefix(srv.URL, "http://")
	listed := rulesOf(t, host)

	t.Run("listed loopback", func(t *testing.T) {
		t.Parallel()

		resp, err := fetch(t.Context(), listed, &justbashpb.FetchRequest{Url: srv.URL + "/", Method: "POST", Headers: map[string]string{"X-Test": "1"}})
		require.NoError(t, err)
		require.Equal(t, int32(200), resp.GetStatus())
		require.Equal(t, "hello", string(resp.GetBody()))
		require.Equal(t, "POST 1", resp.GetHeaders()["x-seen"])
	})

	t.Run("large body", func(t *testing.T) {
		t.Parallel()

		resp, err := fetch(t.Context(), listed, &justbashpb.FetchRequest{Url: srv.URL + "/big"})
		require.NoError(t, err)
		require.Len(t, resp.GetBody(), len(big))
	})

	t.Run("unlisted port", func(t *testing.T) {
		t.Parallel()

		_, err := fetch(t.Context(), listed, &justbashpb.FetchRequest{Url: other.URL + "/"})
		require.ErrorIs(t, err, errNetworkDenied)
	})

	t.Run("auto refuses loopback at dial", func(t *testing.T) {
		t.Parallel()

		_, err := fetch(t.Context(), networkRules{auto: true}, &justbashpb.FetchRequest{Url: srv.URL + "/"})
		require.ErrorIs(t, err, errNetworkDenied)
		require.ErrorContains(t, err, "non-public address 127.0.0.1")
	})

	t.Run("redirect re-checked per hop", func(t *testing.T) {
		t.Parallel()

		_, err := fetch(t.Context(), listed, &justbashpb.FetchRequest{Url: srv.URL + "/redirect", FollowRedirects: true, MaxRedirects: 20})
		require.ErrorIs(t, err, errNetworkDenied)

		resp, err := fetch(t.Context(), listed, &justbashpb.FetchRequest{Url: srv.URL + "/redirect"})
		require.NoError(t, err)
		require.Equal(t, int32(http.StatusFound), resp.GetStatus())
	})

	t.Run("redirect followed when both hops listed", func(t *testing.T) {
		t.Parallel()

		both := rulesOf(t, host, strings.TrimPrefix(other.URL, "http://"))
		resp, err := fetch(t.Context(), both, &justbashpb.FetchRequest{Url: srv.URL + "/redirect", FollowRedirects: true, MaxRedirects: 20, Headers: map[string]string{"authorization": "Bearer s3cret", "cookie": "a=b", "x-keep": "1"}})
		require.NoError(t, err)
		require.Equal(t, "other", string(resp.GetBody()))
		require.Len(t, resp.GetRedirectChain(), 1)
		require.Equal(t, "||1", <-otherSaw, "credentials never follow a redirect to another origin")

		_, err = fetch(t.Context(), both, &justbashpb.FetchRequest{Url: srv.URL + "/redirect", FollowRedirects: true, MaxRedirects: 0})
		require.ErrorContains(t, err, "too many redirects (0)", "--max-redirs 0 follows no redirect")
	})
}
