package rpc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"os"
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
	t.Parallel()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	cfg := &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}
	sessions, err := backend.NewSessionServiceIn(t.Context(), cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	require.NoError(t, sessions.RegisterExternalMCPConversation("external", "main", &backend.ExternalMCPSessionState{PrivateConversationID: "private-X", ManagedConversationID: "a", Agent: "main"}))

	for _, id := range []string{"b", "c"} {
		require.NoError(t, sessions.UpsertThread(id, backend.ThreadState{Agent: "main"}))
	}

	db := openTestTurns(t, dsn).db
	server := &Server{sessions: sessions, cfg: new(config.LockedConfig)}
	call := func(login string, request *ChangeTabsRequest) (*TabsResponse, error) {
		return server.tabs(metadata.NewIncomingContext(t.Context(), tailnetUser(login)), request)
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

	saved := &TabState{
		Tabs: []*Tab{{Path: "/", Space: "main"}, {Path: "/s/YQ", Group: "g", Space: "main"}, {Path: "/s/Yg", Space: "ops"},
			{Path: "/search/a", Space: "ops", Search: &SearchPage{Name: "Triage", Searches: []*SavedSearch{{Id: "1", Query: "is:unread"}, {Id: "2", Name: "Mine", Query: "principal:alice"}}, Active: "2"}}},
		Groups: []*TabGroup{{Id: "g", Name: "Work", Color: "Blue", Collapsed: true}},
		Space:  "main",
		Last:   map[string]string{"main": "/s/YQ", "ops": "/search"},
	}
	response, err := call("alice", &ChangeTabsRequest{State: saved})
	require.NoError(t, err)
	assert.False(t, response.Stale)

	response, err = call("alice", &ChangeTabsRequest{})
	require.NoError(t, err)
	assert.True(t, proto.Equal(saved, response.State), "a read returns the saved state")
	assert.Equal(t, int64(1), response.Revision)
	assert.Equal(t, []string{"Main"}, names(response))

	response, err = call("bob", &ChangeTabsRequest{})
	require.NoError(t, err)
	assert.True(t, proto.Equal(&TabState{}, response.State), "bob has his own state")
	assert.Equal(t, int64(0), response.Revision)

	response, err = call("alice", &ChangeTabsRequest{State: &TabState{Space: "main"}, Revision: 0})
	require.NoError(t, err)
	assert.True(t, response.Stale)
	assert.True(t, proto.Equal(saved, response.State))
	assert.Equal(t, int64(1), response.Revision)

	_, err = db.ExecContext(t.Context(), `INSERT INTO web_tab_attention (username, path, conversation_id, unread_at) VALUES ('alice', '/s/Yg', 'b', now()), ('alice', '/s/Yw', 'c', now())`)
	require.NoError(t, err)

	next := &TabState{Tabs: []*Tab{{Path: "/s/a", Space: "main"}, {Path: "/s/bm9uZQ", Space: "main"}, {Path: "/s/_w", Space: "main"}}, Space: "main", Last: map[string]string{"main": "/s/a"}}
	response, err = call("alice", &ChangeTabsRequest{State: next, Revision: 1})
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

	response, err = call("alice", &ChangeTabsRequest{})
	require.NoError(t, err)
	assert.True(t, proto.Equal(&TabState{Tabs: []*Tab{{Path: "/s/YQ", Space: "main"}}, Space: "main", Last: map[string]string{"main": "/s/YQ"}}, response.State))

	_, err = db.ExecContext(t.Context(), `DELETE FROM conversations WHERE conversation_id = 'b'`)
	require.NoError(t, err)
	assert.Equal(t, []string{"/s/YQ 0", "/s/Yw f f"}, aliceTabs())

	response, err = call("bob", &ChangeTabsRequest{State: &TabState{Space: "main"}, Revision: 0})
	require.NoError(t, err)
	assert.False(t, response.Stale)

	response, err = call("alice", &ChangeTabsRequest{Share: &SharedTab{Space: "main", Path: "/s/YQ"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"main /s/YQ"}, shared(response))
	response, err = call("bob", &ChangeTabsRequest{Share: &SharedTab{Space: "main", Path: "/search"}})
	require.NoError(t, err)
	assert.True(t, proto.Equal(&TabState{Space: "main"}, response.State))
	assert.Equal(t, []string{"main /s/YQ", "main /search"}, shared(response))

	for path, code := range map[string]codes.Code{"/": codes.InvalidArgument, "/s/cHJpdmF0ZS1Y": codes.PermissionDenied, "/s/bm9uZQ": codes.NotFound, "/pages/x/%": codes.InvalidArgument, "/pages/x/%FF": codes.InvalidArgument} {
		_, err = call("bob", &ChangeTabsRequest{Share: &SharedTab{Space: "main", Path: path}})
		assert.Equal(t, code, status.Code(err), path)
	}

	_, err = call("bob", &ChangeTabsRequest{Unshare: &SharedTab{Space: "main", Path: "/s/YQ"}})
	require.NoError(t, err)
	response, err = call("alice", &ChangeTabsRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"main /search"}, shared(response))
	assert.Equal(t, []*Tab{{Path: "/s/YQ", Space: "main"}}, response.State.Tabs)

	response, err = call("bob", &ChangeTabsRequest{State: &TabState{Space: "main"}, Revision: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"main /search"}, shared(response), "a save's reply carries the Shared tabs too")

	response, err = call("bob", &ChangeTabsRequest{CreateSpace: "Ops"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Main", "Ops"}, names(response), "spaces are shared, oldest first")

	_, err = call("alice", &ChangeTabsRequest{CreateSpace: "OPS"})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = call("alice", &ChangeTabsRequest{RenameSpace: &Space{Id: "main", Name: "ops"}})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	response, err = call("alice", &ChangeTabsRequest{RenameSpace: &Space{Id: response.Spaces[1].Id, Name: "Operations"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"Main", "Operations"}, names(response))
	response, err = call("alice", &ChangeTabsRequest{DeleteSpace: "main"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Operations"}, names(response))
	assert.Equal(t, []string{response.Spaces[0].Id + " /search"}, shared(response), "a deleted space's Shared tabs move to the first space")
	_, err = call("alice", &ChangeTabsRequest{DeleteSpace: response.Spaces[0].Id})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "the last space stays")

	space := response.Spaces[0].Id
	_, err = call("alice", &ChangeTabsRequest{Share: &SharedTab{Space: space, Path: "/cron"}})
	require.NoError(t, err)
	_, err = call("bob", &ChangeTabsRequest{State: &TabState{Space: space, SharedOrder: []string{"/cron", "/search"}}, Revision: 2})
	require.NoError(t, err)
	response, err = call("bob", &ChangeTabsRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"/cron", "/search"}, response.State.SharedOrder)
	assert.Equal(t, []string{space + " /search", space + " /cron"}, shared(response))
	response, err = call("alice", &ChangeTabsRequest{})
	require.NoError(t, err)
	assert.Empty(t, response.State.SharedOrder, "a person's Shared order is their own")

	triage := &SearchPage{Name: "Triage", Searches: []*SavedSearch{{Id: "1", Query: "is:unread"}}, Active: "1"}
	_, err = call("alice", &ChangeTabsRequest{Share: &SharedTab{Space: space, Path: "/search/a", Search: triage}})
	require.NoError(t, err)
	response, err = call("bob", &ChangeTabsRequest{})
	require.NoError(t, err)
	assert.True(t, proto.Equal(&SharedTab{Space: space, Path: "/search/a", Search: triage}, response.Shared[2]))
	assert.Nil(t, response.Shared[0].Search, "other Shared tabs have no page")

	edited := &SearchPage{Name: "Triage", Searches: []*SavedSearch{{Id: "1", Query: "is:unread"}, {Id: "2", Query: "tag:bug"}}, Active: "1"}
	response, err = call("bob", &ChangeTabsRequest{EditShared: &SharedTab{Space: space, Path: "/search/a", Base: edited, Search: edited}})
	require.NoError(t, err)
	assert.True(t, response.Stale)
	assert.True(t, proto.Equal(triage, response.Shared[2].Search), "a refused edit answers with the page as saved")

	response, err = call("bob", &ChangeTabsRequest{EditShared: &SharedTab{Space: space, Path: "/search/a", Base: triage, Search: edited}})
	require.NoError(t, err)
	assert.False(t, response.Stale)
	assert.Equal(t, []string{space + " /search", space + " /cron", space + " /search/a"}, shared(response), "an edit keeps its place")
	assert.True(t, proto.Equal(edited, response.Shared[2].Search))

	response, err = call("alice", &ChangeTabsRequest{Unshare: &SharedTab{Space: space, Path: "/search/a"}})
	require.NoError(t, err)
	assert.True(t, proto.Equal(edited, response.Unshared))
	response, err = call("bob", &ChangeTabsRequest{EditShared: &SharedTab{Space: space, Path: "/search/a", Base: edited, Search: edited}})
	require.NoError(t, err)
	assert.True(t, response.Stale)
	assert.Equal(t, []string{space + " /search", space + " /cron"}, shared(response), "an edit after unsharing does not share again")

	later := time.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05.000Z07:00")
	_, err = call("alice", &ChangeTabsRequest{Attention: &AttentionChange{Paths: []string{"/s/a", "/cron"}, Action: AttentionAction_PARK, Until: later}})
	require.NoError(t, err)
	_, err = call("bob", &ChangeTabsRequest{Attention: &AttentionChange{Paths: []string{"/s/Yw"}, Action: AttentionAction_PARK, Until: later}})
	require.NoError(t, err)
	assert.Equal(t, []string{"/cron t t", "/s/YQ 0", "/s/YQ t t", "/s/Yw f f"}, aliceTabs())

	for path, code := range map[string]codes.Code{"/": codes.InvalidArgument, "group:g": codes.InvalidArgument, "/s/cHJpdmF0ZS1Y": codes.PermissionDenied, "/s/bm9uZQ": codes.NotFound} {
		_, err = call("alice", &ChangeTabsRequest{Attention: &AttentionChange{Paths: []string{"/s/Yw", path}, Action: AttentionAction_MARK_UNREAD}})
		assert.Equal(t, code, status.Code(err), path)
	}

	_, err = call("alice", &ChangeTabsRequest{Attention: &AttentionChange{Paths: []string{"/s/Yw"}, Action: AttentionAction_PARK, Until: "tomorrow"}})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Equal(t, []string{"/cron t t", "/s/YQ 0", "/s/YQ t t", "/s/Yw f f"}, aliceTabs())

	_, err = call("alice", &ChangeTabsRequest{Attention: &AttentionChange{Paths: []string{"/s/YQ", "/cron"}, Action: AttentionAction_UNPARK}})
	require.NoError(t, err)
	assert.Equal(t, []string{"/s/YQ 0", "/s/Yw f f"}, aliceTabs())

	_, err = call("alice", &ChangeTabsRequest{Attention: &AttentionChange{Paths: []string{"/s/YQ", "/cron"}, Action: AttentionAction_PARK_UNTIL_DONE}})
	require.NoError(t, err)
	assert.Equal(t, []string{"/s/YQ 0", "/s/YQ t t", "/s/Yw f f"}, aliceTabs())

	_, err = call("alice", &ChangeTabsRequest{Attention: &AttentionChange{Paths: []string{"/s/YQ"}, Action: AttentionAction_UNPARK}})
	require.NoError(t, err)
	assert.Equal(t, []string{"/s/YQ 0", "/s/Yw f f"}, aliceTabs())

	sent := func(paths ...string) {
		t.Helper()

		_, err := call("alice", &ChangeTabsRequest{Attention: &AttentionChange{Paths: paths, Action: AttentionAction_SENT}})
		require.NoError(t, err)
	}
	response, err = call("alice", &ChangeTabsRequest{})
	require.NoError(t, err)
	assert.True(t, response.AutoPark)

	response, err = call("alice", &ChangeTabsRequest{AutoPark: new(false)})
	require.NoError(t, err)
	assert.False(t, response.AutoPark)
	sent("/s/YQ")
	assert.Equal(t, []string{"/s/YQ 0", "/s/Yw f f"}, aliceTabs(), "nothing is auto-parked with it off")

	response, err = call("bob", &ChangeTabsRequest{})
	require.NoError(t, err)
	assert.True(t, response.AutoPark, "auto-park is each person's own")

	_, err = call("alice", &ChangeTabsRequest{Attention: &AttentionChange{Paths: []string{"/s/YQ", "/cron"}, Action: AttentionAction_AUTO_PARK}})
	require.NoError(t, err)
	sent("/s/YQ", "/s/Yw")
	assert.Equal(t, []string{"/s/YQ 0", "/s/YQ t t", "/s/Yw f f"}, aliceTabs())

	_, err = call("alice", &ChangeTabsRequest{Attention: &AttentionChange{Paths: []string{"/s/YQ"}, Action: AttentionAction_UNPARK}})
	require.NoError(t, err)
	assert.Equal(t, []string{"/s/YQ 0", "/s/YQ f f", "/s/Yw f f"}, aliceTabs())

	response, err = call("alice", &ChangeTabsRequest{AutoPark: new(true)})
	require.NoError(t, err)
	assert.True(t, response.AutoPark)

	_, err = call("alice", &ChangeTabsRequest{Attention: &AttentionChange{Paths: []string{"/s/YQ"}, Action: AttentionAction_STOP_AUTO_PARK}})
	require.NoError(t, err)
	sent("/s/YQ", "/s/Yw")
	assert.Equal(t, []string{"/s/YQ 0", "/s/YQ f f", "/s/Yw t t"}, aliceTabs(), "her choice for a session wins over her choice for every session")

	_, err = db.ExecContext(t.Context(), `DELETE FROM web_tab_attention WHERE username = 'alice' AND path <> '/s/Yw'; UPDATE web_tab_attention SET parked = FALSE, until = NULL WHERE username = 'alice'`)
	require.NoError(t, err)

	response, err = call("alice", &ChangeTabsRequest{})
	require.NoError(t, err)

	assert.True(t, proto.Equal(&StripLayout{}, response.Layout), "nothing laid out yet: %v", response.Layout)

	layout := &StripLayout{Folded: map[string]bool{"@work": true, "work/main": false}, Order: []string{"work/lab", "#shared", "/main"}}
	_, err = call("alice", &ChangeTabsRequest{Layout: layout})
	require.NoError(t, err)
	response, err = call("alice", &ChangeTabsRequest{})
	require.NoError(t, err)
	assert.True(t, proto.Equal(layout, response.Layout), "a read returns the saved layout: %v", response.Layout)
	response, err = call("bob", &ChangeTabsRequest{})
	require.NoError(t, err)
	assert.True(t, proto.Equal(&StripLayout{}, response.Layout), "another person's layout is not theirs: %v", response.Layout)

	listed := func(response *TabsResponse) []string {
		out := make([]string, 0, len(response.Lists))
		for _, tab := range response.Lists {
			out = append(out, tab.List+" "+tab.Server+" "+tab.Path+" "+tab.Space)
		}

		return out
	}
	_, err = call("alice", &ChangeTabsRequest{ListAdd: &ListTab{List: "recent", Path: "/s/YQ", Space: "main"}})
	require.NoError(t, err)
	response, err = call("alice", &ChangeTabsRequest{ListAdd: &ListTab{List: "recent", Server: "lab", Path: "/s/bm9uZQ", Space: "ops"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"recent lab /s/bm9uZQ ops", "recent  /s/YQ main"}, listed(response), "an add answers with the lists, a new tab on top, and another server's path is its own")
	assert.Nil(t, response.State, "an add answers with the lists alone")

	_, err = call("alice", &ChangeTabsRequest{ListAdd: &ListTab{List: "parked", Path: "/s/YQ", Space: "main"}})
	require.NoError(t, err)
	response, err = call("alice", &ChangeTabsRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"parked  /s/YQ main", "recent lab /s/bm9uZQ ops", "recent  /s/YQ main"}, listed(response), "every read carries the lists to the person's other devices")
	response, err = call("alice", &ChangeTabsRequest{ListOrder: []*ListTab{{List: "recent", Path: "/s/YQ"}, {List: "recent", Server: "lab", Path: "/s/bm9uZQ"}}})
	require.NoError(t, err)
	assert.Equal(t, []string{"parked  /s/YQ main", "recent  /s/YQ main", "recent lab /s/bm9uZQ ops"}, listed(response), "an order answers with the list in that order")
	response, err = call("alice", &ChangeTabsRequest{ListForget: &ListTab{List: "parked", Path: "/s/YQ"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"recent  /s/YQ main", "recent lab /s/bm9uZQ ops"}, listed(response), "a forgotten tab leaves its list alone")
	assert.Nil(t, response.State, "a forget answers with the lists alone")

	for path, code := range map[string]codes.Code{"/": codes.InvalidArgument, "group:g": codes.InvalidArgument, "/pages/x/%": codes.InvalidArgument, "/s/cHJpdmF0ZS1Y": codes.PermissionDenied, "/s/bm9uZQ": codes.NotFound} {
		_, err = call("alice", &ChangeTabsRequest{ListAdd: &ListTab{List: "parked", Path: path, Space: "main"}})
		assert.Equal(t, code, status.Code(err), path)
	}

	for _, request := range []*ChangeTabsRequest{
		{ListAdd: &ListTab{List: "recent", Server: "lab", Path: "/pages/x/%FF", Space: "ops"}},
		{ListAdd: &ListTab{List: "closed", Path: "/s/YQ", Space: "main"}},
		{ListForget: &ListTab{List: "closed", Path: "/s/YQ"}},
		{ListOrder: []*ListTab{{List: "recent", Path: "/s/YQ"}, {List: "parked", Path: "/s/YQ"}}},
	} {
		_, err = call("alice", request)
		assert.Equal(t, codes.InvalidArgument, status.Code(err), "%v", request)
	}

	response, err = call("bob", &ChangeTabsRequest{})
	require.NoError(t, err)
	assert.Empty(t, listed(response), "another person's lists are not theirs")
}

func TestPromptAutoParks(t *testing.T) {
	h := startWebHarness(t)
	alice := metadata.NewIncomingContext(t.Context(), tailnetUser("alice"))
	attention := func() backend.Attention {
		t.Helper()

		for chat, err := range h.rt.Sessions.SidebarChats(t.Context(), "alice", h.server.channels, []string{"worker"}) {
			require.NoError(t, err)
			return chat.Session.Attention
		}

		return backend.Attention{}
	}

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

	h.replies <- modelReplyText(rand.Text(), "Done.")

	require.NoError(t, turn.Wait())

	ctx, cancel := context.WithCancel(t.Context())

	var waking errgroup.Group
	waking.Go(func() error { h.rt.Sessions.WakeParks(ctx); return nil })

	defer func() { cancel(); require.NoError(t, waking.Wait()) }()

	require.Eventually(t, func() bool { return !attention().Parked }, 10*time.Second, 50*time.Millisecond, "the server ends the park once the session is idle; the held message is not work")
}

func TestTabsSpaceLimit(t *testing.T) {
	t.Parallel()

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
	alice := metadata.NewIncomingContext(t.Context(), tailnetUser("alice"))
	bob := metadata.NewIncomingContext(t.Context(), tailnetUser("bob"))
	read := func(ctx context.Context) (spaces, shared []string) {
		t.Helper()

		response, err := server.tabs(ctx, &ChangeTabsRequest{})
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

	_, err = server.tabs(bob, &ChangeTabsRequest{Share: &SharedTab{Space: mainID, Path: path("main-chat")}})
	require.NoError(t, err)
	_, err = server.tabs(alice, &ChangeTabsRequest{Share: &SharedTab{Space: opsID, Path: path("ops-chat")}})
	require.NoError(t, err)

	for _, request := range []*ChangeTabsRequest{
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

	for _, tab := range []*ListTab{{List: "recent", Path: "/cron", Space: mainID}, {List: "parked", Path: path("main-chat"), Space: opsID}} {
		_, err := server.tabs(alice, &ChangeTabsRequest{ListAdd: tab})
		require.Equal(t, codes.PermissionDenied, status.Code(err), "%v", tab)
	}

	require.NoError(t, sessions.AddToList(t.Context(), "alice", backend.ListTab{List: backend.RecentList, Path: "/cron", Space: mainID}))
	response, err := server.tabs(alice, &ChangeTabsRequest{ListAdd: &ListTab{List: "recent", Path: path("ops-chat"), Space: opsID}})
	require.NoError(t, err)
	require.Len(t, response.Lists, 1, "a listed tab outside her spaces is left out")
	require.Equal(t, path("ops-chat"), response.Lists[0].Path)
	response, err = server.tabs(alice, &ChangeTabsRequest{})
	require.NoError(t, err)
	require.Len(t, response.Lists, 1, "a read leaves out a listed tab outside her spaces too")
	require.Equal(t, path("ops-chat"), response.Lists[0].Path)

	spaces, shared = read(bob)
	require.Equal(t, []string{"Main", "Ops"}, spaces)
	require.Equal(t, []string{path("main-chat"), path("ops-chat")}, shared)
}
