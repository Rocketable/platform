package rpc

import (
	"log/slog"
	"net/netip"
	"testing"

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

	for _, id := range []string{"a", "b", "c"} {
		require.NoError(t, sessions.UpsertThread(id, backend.ThreadState{Agent: "main"}))
	}

	db := openTestTurns(t, dsn).db
	server := &Server{sessions: sessions, usernames: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice", netip.MustParseAddr("192.0.2.2"): "bob"}}
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

	// /search is the ops space's last tab, though closed.
	saved := &TabState{
		Tabs:      []*Tab{{Path: "/", Space: "main"}, {Path: "/s/YQ", Pinned: true, Group: "g", Space: "main"}, {Path: "/s/Yg", Preview: true, Space: "ops"}},
		Groups:    []*TabGroup{{Id: "g", Name: "Work", Color: "Blue", Collapsed: true}},
		Space:     "main",
		Last:      map[string]string{"main": "/s/YQ", "ops": "/search"},
		Placement: TabPlacement_LEFT,
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
	// path decodes to bytes that are not UTF-8, and deletes closed tabs that are neither unread nor some
	// space's last.
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
	_, err = call("127.0.0.1", &TabsRequest{DeleteSpace: response.Spaces[0].Id})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "the last space stays")
}
