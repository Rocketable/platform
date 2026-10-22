package rpc

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func TestSessionReads(t *testing.T) {
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	sessions, err := backend.NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	cfg := &config.Config{Workspace: t.TempDir(), WebUsers: map[netip.Addr]string{netip.MustParseAddr("127.0.0.1"): "alice", netip.MustParseAddr("192.0.2.2"): "bob"}}
	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(filepath.Join(cfg.RuntimeDirName(), "agents"), 0o700))
	require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "agents", "main.md"), []byte("---\nmodel: gpt-6.1-sol\n---\nHelp."), 0o600))

	for _, id := range []string{"x", "y"} {
		require.NoError(t, sessions.UpsertThread(id, backend.ThreadState{Agent: "main"}))
		_, err := sessions.AppendEntryID(t.Context(), id, &rocketcode.SessionEntry{})
		require.NoError(t, err)
	}

	db := openTestTurns(t, dsn).db
	mark := func() {
		_, err := db.ExecContext(t.Context(), `WITH viewed AS (
    INSERT INTO web_tab_states (username, revision, viewing, viewed_at) VALUES ('alice', 0, 'y', now())
    ON CONFLICT (username) DO UPDATE SET viewing = excluded.viewing, viewed_at = excluded.viewed_at
)
INSERT INTO web_tabs (username, path, conversation_id, unread_at) VALUES ('alice', '/s/eA', 'x', '2026-10-22T10:00:00.5Z'), ('alice', '/s/eQ', 'y', NULL)
ON CONFLICT (username, path) DO UPDATE SET unread_at = excluded.unread_at`)
		require.NoError(t, err)
	}
	marked := []string{"alice viewing=y", "x unread=t", "y unread=f"}
	reads := func() []string {
		rows, err := db.QueryContext(t.Context(), `SELECT format('%s viewing=%s', username, viewing) FROM web_tab_states
UNION ALL SELECT format('%s unread=%s', conversation_id, unread_at IS NOT NULL) FROM web_tabs ORDER BY 1`)
		require.NoError(t, err)

		defer func() { require.NoError(t, rows.Close()) }()

		var out []string

		for rows.Next() {
			var row string
			require.NoError(t, rows.Scan(&row))
			out = append(out, row)
		}

		require.NoError(t, rows.Err())

		return out
	}

	listener, err := net.Listen("unix", testSocketPath(t))
	require.NoError(t, err)

	transport := grpc.NewServer()
	New(&mockBackend{}, sessions, config.NewLockedConfig(cfg), &mockChannels{}, &mockCronJobs{}).Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() { transport.Stop(); require.NoError(t, serving.Wait()) })

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	unread := func(ip, viewing string) map[string]string {
		listed, err := invoke[ListSessionsResponse](metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", ip)), connection, "ListSessions", &ListSessionsRequest{Viewing: viewing})
		require.NoError(t, err)

		out := map[string]string{}
		for _, session := range listed.Sessions {
			out[session.Id] = session.UnreadAt
		}

		return out
	}

	mark()
	assert.Equal(t, map[string]string{"x": "2026-10-22T10:00:00.5Z", "y": ""}, unread("127.0.0.1", ""), "an empty viewing lists alice's mark")
	assert.Equal(t, marked, reads(), "and leaves her view of y and her mark on x unchanged")
	assert.Equal(t, map[string]string{"x": "", "y": ""}, unread("192.0.2.2", ""), "bob has no mark")
	assert.Equal(t, map[string]string{"x": "2026-10-22T10:00:00.5Z", "y": ""}, unread("127.0.0.1", "nope"))
	assert.Equal(t, marked, reads(), "a viewing that names no conversation writes nothing")

	assert.Equal(t, map[string]string{"x": "", "y": ""}, unread("127.0.0.1", "x"), "viewing x clears its mark in the same response")
	assert.Equal(t, []string{"alice viewing=x", "x unread=f", "y unread=f"}, reads(), "and ends alice's view of y")

	httpServer := startHTTPTestServer(t, connection)

	for _, tc := range []struct {
		header, value string
		want          []string
	}{
		{"Sec-Fetch-Site", "cross-site", marked},
		{"Origin", "https://elsewhere.example", marked},
		{"Sec-Fetch-Site", "same-origin", []string{"alice viewing=x", "x unread=f", "y unread=f"}},
	} {
		mark()

		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, httpServer.URL+"/api/ListSessions?viewing=x", http.NoBody)
		require.NoError(t, err)
		request.Header.Set(tc.header, tc.value)

		response, err := httpServer.Client().Do(request)
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, http.StatusOK, response.StatusCode)
		assert.Equal(t, tc.want, reads(), "%s: %s", tc.header, tc.value)
	}
}
