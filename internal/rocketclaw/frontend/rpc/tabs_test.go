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

	saved := &TabState{
		Tabs:      []*Tab{{Path: "/", Space: "main"}, {Path: "/s/YQ", Pinned: true, Group: "g", Space: "main"}},
		Groups:    []*TabGroup{{Id: "g", Name: "Work", Color: "Blue", Collapsed: true}},
		Space:     "main",
		Last:      map[string]string{"main": "/s/YQ"},
		Placement: TabPlacement_LEFT,
	}
	response, err := call("127.0.0.1", &TabsRequest{State: saved})
	require.NoError(t, err)
	assert.True(t, proto.Equal(saved, response.State), "the save answers with the saved state")
	assert.Equal(t, int64(1), response.Revision)
	assert.False(t, response.Stale)

	// A save made from an older revision is refused and answered with the newer saved state.
	response, err = call("127.0.0.1", &TabsRequest{State: &TabState{Space: "main"}})
	require.NoError(t, err)
	assert.True(t, response.Stale)
	assert.True(t, proto.Equal(saved, response.State))
	assert.Equal(t, int64(1), response.Revision)

	response, err = call("127.0.0.1", &TabsRequest{})
	require.NoError(t, err)
	assert.True(t, proto.Equal(saved, response.State), "a read returns it unchanged")
	assert.Equal(t, int64(1), response.Revision)
	assert.Equal(t, []string{"Main"}, names(response))

	response, err = call("192.0.2.2", &TabsRequest{CreateSpace: "Ops"})
	require.NoError(t, err)
	assert.True(t, proto.Equal(&TabState{}, response.State), "bob has his own, empty state")

	// Saved tabs that cannot be read back read as none, and the next save replaces them.
	unreadable, err := sessions.SaveTabState(t.Context(), "bob", []byte{0xff}, 0)
	require.NoError(t, err)
	require.True(t, unreadable)

	response, err = call("192.0.2.2", &TabsRequest{})
	require.NoError(t, err)
	assert.True(t, proto.Equal(&TabState{}, response.State))
	response, err = call("192.0.2.2", &TabsRequest{State: &TabState{Space: "main"}, Revision: response.Revision})
	require.NoError(t, err)
	assert.False(t, response.Stale)
	response, err = call("127.0.0.1", &TabsRequest{})
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
