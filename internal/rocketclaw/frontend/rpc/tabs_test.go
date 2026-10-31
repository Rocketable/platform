package rpc

import (
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestTabs(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}
	sessions, err := backend.NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	for _, id := range []string{"a", "b", "c", "private-X"} {
		require.NoError(t, sessions.UpsertThread(id, backend.ThreadState{Agent: "main"}))
	}

	require.NoError(t, sessions.UpsertExternalMCPSession("external", &backend.ExternalMCPSessionState{PrivateConversationID: "private-X", ManagedConversationID: "a", Agent: "main"}))

	db := openTestTurns(t, dsn).db
	server := &Server{sessions: sessions, cfg: config.NewLockedConfig(&config.Config{WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice", netip.MustParseAddr("192.0.2.2"): "bob"}})}
	call := func(ip string, request *TabsRequest) (*TabsResponse, error) {
		return server.tabs(metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", ip)), request)
	}
	names := func(response *TabsResponse) []string {
		out := make([]string, 0, len(response.Spaces))
		for _, space := range response.Spaces {
			out = append(out, space.Name)
		}

		return out
	}
	shared := func(response *TabsResponse) []string {
		out := make([]string, 0, len(response.Shared))
		for _, tab := range response.Shared {
			out = append(out, tab.Space+" "+tab.Path)
		}

		return out
	}
	aliceTabs := func() []string {
		t.Helper()

		var out []string

		result, err := db.QueryContext(t.Context(), `SELECT format('%s %s %s', path, position, unread_at IS NOT NULL) FROM web_tabs WHERE username = 'alice' ORDER BY path COLLATE "C"`)
		require.NoError(t, err)

		defer func() { require.NoError(t, result.Close()) }()

		for result.Next() {
			var row string
			require.NoError(t, result.Scan(&row))
			out = append(out, row)
		}

		require.NoError(t, result.Err())

		return out
	}

	// /search is the ops space's last tab, though closed. A Search page keeps its name, board style, and searches with
	// their notify marks.
	saved := &TabState{
		Tabs: []*Tab{{Path: "/", Space: "main"}, {Path: "/s/YQ", Group: "g", Space: "main"}, {Path: "/s/Yg", Preview: true, Space: "ops"},
			{Path: "/search/a", Space: "ops", Search: &SearchPage{Name: "Triage", Board: "kanban", Searches: []*SavedSearch{{Id: "1", Query: "is:unread"}, {Id: "2", Name: "Mine", Query: "principal:alice", Notify: true}}, Active: "2"}}},
		Groups: []*TabGroup{{Id: "g", Name: "Work", Color: "Blue", Collapsed: true}},
		Space:  "main",
		Last:   map[string]string{"main": "/s/YQ", "ops": "/search"},
	}
	response, err := call("127.0.0.1", &TabsRequest{State: saved})
	require.NoError(t, err)
	assert.False(t, response.Stale)

	response, err = call("127.0.0.1", &TabsRequest{})
	require.NoError(t, err)
	assert.True(t, proto.Equal(saved, response.State), "a read returns the saved state")
	assert.Equal(t, int64(1), response.Revision)
	assert.Equal(t, []string{"Main"}, names(response))

	response, err = call("192.0.2.2", &TabsRequest{})
	require.NoError(t, err)
	assert.True(t, proto.Equal(&TabState{}, response.State), "bob has his own state")
	assert.Equal(t, int64(0), response.Revision)

	// A save made from an older revision is refused and answered with the newer saved state.
	response, err = call("127.0.0.1", &TabsRequest{State: &TabState{Space: "main"}, Revision: 0})
	require.NoError(t, err)
	assert.True(t, response.Stale)
	assert.True(t, proto.Equal(saved, response.State))
	assert.Equal(t, int64(1), response.Revision)

	// Alice has b's open tab and c's closed one unread. A save keeps both marks, writes a's tab saved in the
	// old URI spelling under its canonical path, drops tabs of sessions that do not exist, even ones whose
	// path decodes to bytes that are not UTF-8, and deletes closed tabs that are not unread.
	_, err = db.ExecContext(t.Context(), `UPDATE web_tabs SET unread_at = now() WHERE conversation_id = 'b'; INSERT INTO web_tabs (username, path, conversation_id, unread_at) VALUES ('alice', '/s/Yw', 'c', now())`)
	require.NoError(t, err)

	next := &TabState{Tabs: []*Tab{{Path: "/s/a", Space: "main"}, {Path: "/s/bm9uZQ", Space: "main"}, {Path: "/s/_w", Space: "main"}}, Space: "main", Last: map[string]string{"main": "/s/a"}}
	response, err = call("127.0.0.1", &TabsRequest{State: next, Revision: 1})
	require.NoError(t, err)
	assert.False(t, response.Stale)
	assert.True(t, proto.Equal(next, response.State), "the save answers with the saved state")
	assert.Equal(t, int64(2), response.Revision)
	assert.Equal(t, []string{"/s/YQ 0 f", "/s/Yg  t", "/s/Yw  t"}, aliceTabs())

	response, err = call("127.0.0.1", &TabsRequest{})
	require.NoError(t, err)
	assert.True(t, proto.Equal(&TabState{Tabs: []*Tab{{Path: "/s/YQ", Space: "main"}}, Space: "main", Last: map[string]string{"main": "/s/YQ"}}, response.State))

	// Deleting a session deletes its tabs.
	_, err = db.ExecContext(t.Context(), `DELETE FROM conversations WHERE conversation_id = 'b'`)
	require.NoError(t, err)
	assert.Equal(t, []string{"/s/YQ 0 f", "/s/Yw  t"}, aliceTabs())

	response, err = call("192.0.2.2", &TabsRequest{State: &TabState{Space: "main"}, Revision: 0})
	require.NoError(t, err)
	assert.False(t, response.Stale)

	// Alice shares her open tab, then Bob a page: everyone sees both, oldest first, and Bob's own tabs stay his.
	response, err = call("127.0.0.1", &TabsRequest{Share: &SharedTab{Space: "main", Path: "/s/YQ"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"main /s/YQ"}, shared(response))
	response, err = call("192.0.2.2", &TabsRequest{Share: &SharedTab{Space: "main", Path: "/search"}})
	require.NoError(t, err)
	assert.True(t, proto.Equal(&TabState{Space: "main"}, response.State))
	assert.Equal(t, []string{"main /s/YQ", "main /search"}, shared(response))

	// New session, a session nobody may open, one that does not exist, and a path no Web can decode cannot be shared.
	for path, code := range map[string]codes.Code{"/": codes.InvalidArgument, "/s/cHJpdmF0ZS1Y": codes.PermissionDenied, "/s/bm9uZQ": codes.NotFound, "/pages/x/%": codes.InvalidArgument, "/pages/x/%FF": codes.InvalidArgument} {
		_, err = call("192.0.2.2", &TabsRequest{Share: &SharedTab{Space: "main", Path: path}})
		assert.Equal(t, code, status.Code(err), path)
	}

	// Bob takes Alice's tab out for everyone; it stays among her own tabs.
	_, err = call("192.0.2.2", &TabsRequest{Unshare: &SharedTab{Space: "main", Path: "/s/YQ"}})
	require.NoError(t, err)
	response, err = call("127.0.0.1", &TabsRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"main /search"}, shared(response))
	assert.Equal(t, []*Tab{{Path: "/s/YQ", Space: "main"}}, response.State.Tabs)

	response, err = call("192.0.2.2", &TabsRequest{State: &TabState{Space: "main"}, Revision: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"main /search"}, shared(response), "a save's reply carries the Shared tabs too")

	response, err = call("192.0.2.2", &TabsRequest{CreateSpace: "Ops"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Main", "Ops"}, names(response), "spaces are shared, oldest first")

	_, err = call("127.0.0.1", &TabsRequest{CreateSpace: "OPS"})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = call("127.0.0.1", &TabsRequest{RenameSpace: &Space{Id: "main", Name: "ops"}})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	response, err = call("127.0.0.1", &TabsRequest{RenameSpace: &Space{Id: response.Spaces[1].Id, Name: "Operations"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"Main", "Operations"}, names(response))
	response, err = call("127.0.0.1", &TabsRequest{DeleteSpace: "main"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Operations"}, names(response))
	assert.Equal(t, []string{response.Spaces[0].Id + " /search"}, shared(response), "a deleted space's Shared tabs move to the first space")
	_, err = call("127.0.0.1", &TabsRequest{DeleteSpace: response.Spaces[0].Id})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "the last space stays")

	// Bob moves a newer Shared tab above an older one for himself alone.
	space := response.Spaces[0].Id
	_, err = call("127.0.0.1", &TabsRequest{Share: &SharedTab{Space: space, Path: "/cron"}})
	require.NoError(t, err)
	_, err = call("192.0.2.2", &TabsRequest{State: &TabState{Space: space, SharedOrder: []string{"/cron", "/search"}}, Revision: 2})
	require.NoError(t, err)
	response, err = call("192.0.2.2", &TabsRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"/cron", "/search"}, response.State.SharedOrder)
	assert.Equal(t, []string{space + " /search", space + " /cron"}, shared(response))
	response, err = call("127.0.0.1", &TabsRequest{})
	require.NoError(t, err)
	assert.Empty(t, response.State.SharedOrder, "a person's Shared order is their own")

	// A Shared Search page carries its one page to everyone, and an edit replaces the page for everyone.
	triage := &SearchPage{Name: "Triage", Searches: []*SavedSearch{{Id: "1", Query: "is:unread"}}, Active: "1"}
	_, err = call("127.0.0.1", &TabsRequest{Share: &SharedTab{Space: space, Path: "/search/a", Search: triage}})
	require.NoError(t, err)
	response, err = call("192.0.2.2", &TabsRequest{})
	require.NoError(t, err)
	assert.True(t, proto.Equal(&SharedTab{Space: space, Path: "/search/a", Search: triage}, response.Shared[2]))
	assert.Nil(t, response.Shared[0].Search, "other Shared tabs have no page")

	// An edit made from the page as saved replaces it, its board style and notify marks included; one made from a page
	// that has since changed is refused as stale.
	edited := &SearchPage{Name: "Triage", Board: "kanban", Searches: []*SavedSearch{{Id: "1", Query: "is:unread", Notify: true}, {Id: "2", Query: "tag:bug"}}, Active: "1"}
	response, err = call("192.0.2.2", &TabsRequest{EditShared: &SharedTab{Space: space, Path: "/search/a", Base: edited, Search: edited}})
	require.NoError(t, err)
	assert.True(t, response.Stale)
	assert.True(t, proto.Equal(triage, response.Shared[2].Search), "a refused edit answers with the page as saved")

	response, err = call("192.0.2.2", &TabsRequest{EditShared: &SharedTab{Space: space, Path: "/search/a", Base: triage, Search: edited}})
	require.NoError(t, err)
	assert.False(t, response.Stale)
	assert.Equal(t, []string{space + " /search", space + " /cron", space + " /search/a"}, shared(response), "an edit keeps its place")
	assert.True(t, proto.Equal(edited, response.Shared[2].Search))

	// Taking it out of Shared answers with the page it held, and a later edit does not share it again.
	response, err = call("127.0.0.1", &TabsRequest{Unshare: &SharedTab{Space: space, Path: "/search/a"}})
	require.NoError(t, err)
	assert.True(t, proto.Equal(edited, response.Unshared))
	response, err = call("192.0.2.2", &TabsRequest{EditShared: &SharedTab{Space: space, Path: "/search/a", Base: edited, Search: edited}})
	require.NoError(t, err)
	assert.True(t, response.Stale)
	assert.Equal(t, []string{space + " /search", space + " /cron"}, shared(response), "an edit after unsharing does not share again")

	// A saved page with a field a later Web dropped still reads, and one saved before board styles is an ordinary page.
	_, err = searchPage([]byte(`{"name":"x","gone":1}`))
	require.NoError(t, err)
	legacy, err := searchPage([]byte(`{"name":"x","searches":[{"id":"1","query":"q"}],"active":"1"}`))
	require.NoError(t, err)
	assert.True(t, proto.Equal(&SearchPage{Name: "x", Searches: []*SavedSearch{{Id: "1", Query: "q"}}, Active: "1"}, legacy))

	// Alice mutes a and snoozes c, and Bob mutes c: every reply carries only the caller's live mutes.
	mutes := func(response *TabsResponse) []string {
		out := make([]string, 0, len(response.Mutes))
		for _, mute := range response.Mutes {
			out = append(out, mute.Path+" "+mute.Until)
		}

		return out
	}
	// The browser writes times with milliseconds; replies carry RFC 3339 times in UTC.
	later := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	until := later.Format(time.RFC3339)
	_, err = call("127.0.0.1", &TabsRequest{Mute: &TabMute{Path: "/s/YQ"}})
	require.NoError(t, err)
	response, err = call("127.0.0.1", &TabsRequest{Mute: &TabMute{Path: "/s/Yw", Until: later.Format("2006-01-02T15:04:05.000Z07:00")}})
	require.NoError(t, err)
	assert.Equal(t, []string{"/s/YQ ", "/s/Yw " + until}, mutes(response))
	response, err = call("192.0.2.2", &TabsRequest{Mute: &TabMute{Path: "/s/Yw"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"/s/Yw "}, mutes(response))
	response, err = call("127.0.0.1", &TabsRequest{Unmute: "/s/YQ"})
	require.NoError(t, err)
	assert.Equal(t, []string{"/s/Yw " + until}, mutes(response))
	response, err = call("127.0.0.1", &TabsRequest{Mute: &TabMute{Path: "/s/YQ", Until: time.Now().Add(-time.Hour).Format(time.RFC3339)}})
	require.NoError(t, err)
	assert.Equal(t, []string{"/s/Yw " + until}, mutes(response), "a past snooze is not carried")

	// A malformed time, a session nobody may open, one that does not exist, and a page cannot be muted.
	for mute, code := range map[*TabMute]codes.Code{{Path: "/s/YQ", Until: "tomorrow"}: codes.InvalidArgument, {Path: "/s/cHJpdmF0ZS1Y"}: codes.PermissionDenied, {Path: "/s/bm9uZQ"}: codes.NotFound, {Path: "/search"}: codes.InvalidArgument} {
		_, err = call("127.0.0.1", &TabsRequest{Mute: mute})
		assert.Equal(t, code, status.Code(err), mute.Path)
	}

	response, err = call("127.0.0.1", &TabsRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"/s/Yw " + until}, mutes(response), "a refused mute stores nothing")
}
