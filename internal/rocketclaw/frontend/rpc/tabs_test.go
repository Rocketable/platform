package rpc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
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
	server := &Server{sessions: sessions, cfg: new(config.LockedConfig)}
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

		result, err := db.QueryContext(t.Context(), `WITH rows AS (
    SELECT format('%s %s', path, position) AS row FROM web_tabs WHERE username = 'alice'
    UNION ALL SELECT format('%s %s %s', path, parked, until IS NOT NULL) FROM web_tab_attention WHERE username = 'alice'
)
SELECT row FROM rows ORDER BY row COLLATE "C"`)
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
		Tabs: []*Tab{{Path: "/", Space: "main"}, {Path: "/s/YQ", Group: "g", Space: "main"}, {Path: "/s/Yg", Space: "ops"},
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

	// Alice has b's open tab and c, which has no tab, unread. A save closes b's tab and keeps both marks, writes a's
	// tab saved in the old URI spelling under its canonical path, and drops tabs of sessions that do not exist, even
	// ones whose path decodes to bytes that are not UTF-8.
	_, err = db.ExecContext(t.Context(), `INSERT INTO web_tab_attention (username, path, conversation_id, unread_at) VALUES ('alice', '/s/Yg', 'b', now()), ('alice', '/s/Yw', 'c', now())`)
	require.NoError(t, err)

	next := &TabState{Tabs: []*Tab{{Path: "/s/a", Space: "main"}, {Path: "/s/bm9uZQ", Space: "main"}, {Path: "/s/_w", Space: "main"}}, Space: "main", Last: map[string]string{"main": "/s/a"}}
	response, err = call("127.0.0.1", &TabsRequest{State: next, Revision: 1})
	require.NoError(t, err)
	assert.False(t, response.Stale)
	assert.True(t, proto.Equal(next, response.State), "the save answers with the saved state")
	assert.Equal(t, int64(2), response.Revision)
	assert.Equal(t, []string{"/s/YQ 0", "/s/Yg f f", "/s/Yw f f"}, aliceTabs())

	closed := map[string]*Tab{}
	for _, tab := range response.Closed {
		closed[tab.Path] = tab
	}

	assert.Len(t, closed, 2)
	assert.True(t, proto.Equal(saved.Tabs[2], closed["/s/Yg"]))
	assert.True(t, proto.Equal(saved.Tabs[3], closed["/search/a"]))

	response, err = call("127.0.0.1", &TabsRequest{})
	require.NoError(t, err)
	assert.True(t, proto.Equal(&TabState{Tabs: []*Tab{{Path: "/s/YQ", Space: "main"}}, Space: "main", Last: map[string]string{"main": "/s/YQ"}}, response.State))

	// Deleting a session deletes its tabs and attention.
	_, err = db.ExecContext(t.Context(), `DELETE FROM conversations WHERE conversation_id = 'b'`)
	require.NoError(t, err)
	assert.Equal(t, []string{"/s/YQ 0", "/s/Yw f f"}, aliceTabs())

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

	// Alice parks a, under its legacy spelling, and the Cron tab until a time, and Bob parks c: each
	// person's attention is their own, and a session's is kept under its canonical path.
	later := time.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05.000Z07:00")
	_, err = call("127.0.0.1", &TabsRequest{Attention: &AttentionChange{Paths: []string{"/s/a", "/cron"}, Action: AttentionAction_PARK, Until: later}})
	require.NoError(t, err)
	_, err = call("192.0.2.2", &TabsRequest{Attention: &AttentionChange{Paths: []string{"/s/Yw"}, Action: AttentionAction_PARK, Until: later}})
	require.NoError(t, err)
	assert.Equal(t, []string{"/cron t t", "/s/YQ 0", "/s/YQ t t", "/s/Yw f f"}, aliceTabs())

	// New session, a kept group's placeholder, a session nobody may open, one that does not exist, and a malformed
	// time are refused, and refuse the locations changed with them.
	for path, code := range map[string]codes.Code{"/": codes.InvalidArgument, "group:g": codes.InvalidArgument, "/s/cHJpdmF0ZS1Y": codes.PermissionDenied, "/s/bm9uZQ": codes.NotFound} {
		_, err = call("127.0.0.1", &TabsRequest{Attention: &AttentionChange{Paths: []string{"/s/Yw", path}, Action: AttentionAction_MARK_UNREAD}})
		assert.Equal(t, code, status.Code(err), path)
	}

	_, err = call("127.0.0.1", &TabsRequest{Attention: &AttentionChange{Paths: []string{"/s/Yw"}, Action: AttentionAction_PARK, Until: "tomorrow"}})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Equal(t, []string{"/cron t t", "/s/YQ 0", "/s/YQ t t", "/s/Yw f f"}, aliceTabs())

	// Unparking both leaves a read row with no timer, which goes.
	_, err = call("127.0.0.1", &TabsRequest{Attention: &AttentionChange{Paths: []string{"/s/YQ", "/cron"}, Action: AttentionAction_UNPARK}})
	require.NoError(t, err)
	assert.Equal(t, []string{"/s/YQ 0", "/s/Yw f f"}, aliceTabs())

	// Parking until it is done takes the session and skips the Cron tab; unparking it lets the row go again.
	_, err = call("127.0.0.1", &TabsRequest{Attention: &AttentionChange{Paths: []string{"/s/YQ", "/cron"}, Action: AttentionAction_PARK_UNTIL_DONE}})
	require.NoError(t, err)
	assert.Equal(t, []string{"/s/YQ 0", "/s/YQ t t", "/s/Yw f f"}, aliceTabs())

	_, err = call("127.0.0.1", &TabsRequest{Attention: &AttentionChange{Paths: []string{"/s/YQ"}, Action: AttentionAction_UNPARK}})
	require.NoError(t, err)
	assert.Equal(t, []string{"/s/YQ 0", "/s/Yw f f"}, aliceTabs())

	// A prompt Alice sent parks until it is done only a session she auto-parks: by her choice for it, which keeps its
	// row through an unpark, else by her choice for every session, which every reply of hers carries.
	sent := func(paths ...string) {
		t.Helper()

		_, err := call("127.0.0.1", &TabsRequest{Attention: &AttentionChange{Paths: paths, Action: AttentionAction_SENT}})
		require.NoError(t, err)
	}
	sent("/s/YQ")
	assert.Equal(t, []string{"/s/YQ 0", "/s/Yw f f"}, aliceTabs(), "nothing is auto-parked yet")

	_, err = call("127.0.0.1", &TabsRequest{Attention: &AttentionChange{Paths: []string{"/s/YQ", "/cron"}, Action: AttentionAction_AUTO_PARK}})
	require.NoError(t, err)
	sent("/s/YQ", "/s/Yw")
	assert.Equal(t, []string{"/s/YQ 0", "/s/YQ t t", "/s/Yw f f"}, aliceTabs())

	_, err = call("127.0.0.1", &TabsRequest{Attention: &AttentionChange{Paths: []string{"/s/YQ"}, Action: AttentionAction_UNPARK}})
	require.NoError(t, err)
	assert.Equal(t, []string{"/s/YQ 0", "/s/YQ f f", "/s/Yw f f"}, aliceTabs())

	response, err = call("127.0.0.1", &TabsRequest{AutoPark: new(true)})
	require.NoError(t, err)
	assert.True(t, response.AutoPark)

	_, err = call("127.0.0.1", &TabsRequest{Attention: &AttentionChange{Paths: []string{"/s/YQ"}, Action: AttentionAction_STOP_AUTO_PARK}})
	require.NoError(t, err)
	sent("/s/YQ", "/s/Yw")
	assert.Equal(t, []string{"/s/YQ 0", "/s/YQ f f", "/s/Yw t t"}, aliceTabs(), "her choice for a session wins over her choice for every session")

	response, err = call("192.0.2.2", &TabsRequest{})
	require.NoError(t, err)
	assert.False(t, response.AutoPark, "auto-park is each person's own")

	_, err = db.ExecContext(t.Context(), `DELETE FROM web_tab_attention WHERE username = 'alice' AND path <> '/s/Yw'; UPDATE web_tab_attention SET parked = FALSE, until = NULL WHERE username = 'alice'`)
	require.NoError(t, err)

	response, err = call("127.0.0.1", &TabsRequest{})
	require.NoError(t, err)

	// A Top Shelf is the caller's own: each save replaces it whole, and every reply carries it.
	assert.True(t, proto.Equal(&TopShelf{}, response.TopShelf), "nothing shelved yet: %v", response.TopShelf)

	shelf := &TopShelf{Tabs: []*ShelfTab{{Server: "work", Space: "main", Path: "/s/eA"}, {Path: "/s/YQ", Space: "main"}, {Path: "/s/Yw", Space: "main"},
		{Path: "/search/1", Space: "main", Search: &SearchPage{Name: "x", Searches: []*SavedSearch{{Id: "1", Query: "q", Notify: true}}, Active: "1"}}, {Server: "work", Path: "/search/1", Space: "main"}}, Collapsed: true}
	_, err = call("127.0.0.1", &TabsRequest{TopShelf: shelf})
	require.NoError(t, err)
	response, err = call("127.0.0.1", &TabsRequest{})
	require.NoError(t, err)
	assert.True(t, proto.Equal(shelf, response.TopShelf), "a read returns the saved shelf, one path on two servers twice: %v", response.TopShelf)

	_, err = db.ExecContext(t.Context(), `DELETE FROM conversations WHERE conversation_id = 'c'`)
	require.NoError(t, err)
	response, err = call("127.0.0.1", &TabsRequest{})
	require.NoError(t, err)

	shelf.Tabs = slices.Delete(shelf.Tabs, 2, 3)
	assert.True(t, proto.Equal(shelf, response.TopShelf), "c's deleted session leaves the shelf: %v", response.TopShelf)
	response, err = call("192.0.2.2", &TabsRequest{})
	require.NoError(t, err)
	assert.True(t, proto.Equal(&TopShelf{}, response.TopShelf), "another person's shelf is not theirs: %v", response.TopShelf)
	_, err = call("127.0.0.1", &TabsRequest{TopShelf: &TopShelf{}})
	require.NoError(t, err)
	response, err = call("127.0.0.1", &TabsRequest{})
	require.NoError(t, err)
	assert.True(t, proto.Equal(&TopShelf{}, response.TopShelf), "an emptied shelf stays empty: %v", response.TopShelf)
}

// A prompt the agent acts on parks the sender's tab until it is done when they auto-park the session, before its turn
// starts, and the turn's end makes the park due; a stash parks nothing.
func TestPromptAutoParks(t *testing.T) {
	h := startVoiceHarness(t)
	alice := metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))
	attention := func() backend.Attention {
		t.Helper()

		for chat, err := range h.rt.Sessions.SidebarChats(t.Context(), "alice", h.server.channels, []string{"worker"}) {
			require.NoError(t, err)
			return chat.Session.Attention
		}

		return backend.Attention{}
	}

	require.NoError(t, h.rt.Sessions.SetAutoParkAll(t.Context(), "alice", true))
	_, err := h.server.prompt(alice, &PromptRequest{Id: "worker", Text: "held", Delivery: PromptDelivery_STASH})
	require.NoError(t, err)
	require.False(t, attention().Parked, "a stash parks nothing")

	var turn errgroup.Group

	turn.Go(func() error {
		_, err := h.server.prompt(alice, &PromptRequest{Id: "worker", Text: "work", Delivery: PromptDelivery_STEER})
		return err
	})

	<-h.requests

	parked := attention()
	require.True(t, parked.Parked && parked.UntilDone, "parked until it is done while the turn runs: %+v", parked)

	h.replies <- voiceModelText(rand.Text(), "Done.")

	require.NoError(t, turn.Wait())

	parked = attention()
	require.True(t, parked.Parked && !parked.UntilDone && !parked.Until.IsZero(), "the turn's end makes the park due: %+v", parked)
}

func TestTabsSpaceLimit(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir(), Permissions: map[string][]string{"alice": {"Ops"}}}
	sessions, err := backend.NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(cfg.RuntimeDirName()+"/agents", 0o700))
	require.NoError(t, root.WriteFile(cfg.RuntimeDirName()+"/agents/ops-agent.md", []byte("---\nmodel: gpt-6.1-sol\nspace: Ops\n---\nHelp."), 0o600))

	server := &Server{sessions: sessions, cfg: config.NewLockedConfig(cfg)}
	alice := metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "127.0.0.1"))
	bob := metadata.NewIncomingContext(t.Context(), metadata.Pairs("rocketclaw-principal", "192.0.2.2"))
	read := func(ctx context.Context) (spaces, shared []string) {
		t.Helper()

		response, err := server.tabs(ctx, &TabsRequest{})
		require.NoError(t, err)

		for _, space := range response.Spaces {
			spaces = append(spaces, space.Name)
		}

		for _, tab := range response.Shared {
			shared = append(shared, tab.Path)
		}

		return spaces, shared
	}

	before, err := sessions.Spaces(t.Context())
	require.NoError(t, err)
	require.Len(t, before, 1)

	spaces, _ := read(alice)
	require.Equal(t, []string{"Ops"}, spaces, "Ops is created for her")

	after, err := sessions.Spaces(t.Context())
	require.NoError(t, err)
	require.Len(t, after, 2)

	mainID, opsID := after[0].ID, after[1].ID
	path := func(id string) string { return "/s/" + base64.RawURLEncoding.EncodeToString([]byte(id)) }

	rt := &backend.Runtime{Sessions: sessions}
	require.NoError(t, rt.CreateConversation(t.Context(), protocol.Conversation{ID: "ops-chat", Agent: "ops-agent", Space: "Ops"}))
	require.NoError(t, rt.CreateConversation(t.Context(), protocol.Conversation{ID: "main-chat", Agent: "ops-agent", Space: "Main"}))

	_, err = server.tabs(bob, &TabsRequest{Share: &SharedTab{Space: mainID, Path: path("main-chat")}})
	require.NoError(t, err)
	_, err = server.tabs(alice, &TabsRequest{Share: &SharedTab{Space: opsID, Path: path("ops-chat")}})
	require.NoError(t, err)

	for _, request := range []*TabsRequest{
		{CreateSpace: "Support"},
		{RenameSpace: &Space{Id: opsID, Name: "Ops 2"}},
		{DeleteSpace: opsID},
		{Share: &SharedTab{Space: mainID, Path: "/search/1"}},
		{EditShared: &SharedTab{Space: mainID, Path: path("main-chat")}},
		{Unshare: &SharedTab{Space: mainID, Path: path("main-chat")}},
	} {
		_, err := server.tabs(alice, request)
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", request)
	}

	spaces, shared := read(alice)
	require.Equal(t, []string{"Ops"}, spaces)
	require.Equal(t, []string{path("ops-chat")}, shared)

	spaces, shared = read(bob)
	require.Equal(t, []string{"Main", "Ops"}, spaces)
	require.Equal(t, []string{path("main-chat"), path("ops-chat")}, shared)
}
