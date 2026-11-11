package rpc

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func TestSessionReads(t *testing.T) {
	t.Parallel()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	sessions, err := backend.NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Stop()) })

	cfg := &config.Config{Workspace: t.TempDir()}
	root, err := os.OpenRoot(cfg.Workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(filepath.Join(cfg.RuntimeDirName(), "agents"), 0o700))
	require.NoError(t, root.WriteFile(filepath.Join(cfg.RuntimeDirName(), "agents", "main.md"), []byte("---\nmodel: gpt-6.1-sol\n---\nHelp."), 0o600))

	for _, id := range []string{"x", "y"} {
		require.NoError(t, sessions.UpsertThread(id, backend.ThreadState{Agent: "main"}))
		_, err := sessions.AppendEntryID(t.Context(), id, &harness.SessionEntry{})
		require.NoError(t, err)
	}

	db := openTestTurns(t, dsn).db
	mark := func() {
		_, err := db.ExecContext(t.Context(), `WITH viewed AS (
    INSERT INTO web_tab_states (username, revision, viewing, viewed_at) VALUES ('alice', 0, '/s/eQ', now())
    ON CONFLICT (username) DO UPDATE SET viewing = excluded.viewing, viewed_at = excluded.viewed_at
)
INSERT INTO web_tab_attention (username, path, conversation_id, unread_at, quiet) VALUES ('alice', '/s/eA', 'x', '2026-10-22T10:00:00.5Z', TRUE), ('alice', '/cron', NULL, '2026-10-22T10:00:00.5Z', FALSE)
ON CONFLICT (username, path) DO UPDATE SET unread_at = excluded.unread_at, quiet = excluded.quiet`)
		require.NoError(t, err)
	}
	marked := []string{"alice viewing=/s/eQ", "tab /cron unread=t", "tab /s/eA unread=t"}
	reads := func() []string {
		rows, err := db.QueryContext(t.Context(), `SELECT format('%s viewing=%s', username, viewing) FROM web_tab_states
UNION ALL SELECT format('tab %s unread=%s', path, unread_at IS NOT NULL) FROM web_tab_attention ORDER BY 1`)
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
	New(&mockBackend{SandboxErrorFunc: noSandboxError}, sessions, config.NewLockedConfig(cfg), &mockChannels{}, &mockCronJobs{}).Register(transport)

	var serving errgroup.Group
	serving.Go(func() error { return transport.Serve(listener) })
	t.Cleanup(func() { transport.Stop(); require.NoError(t, serving.Wait()) })

	connection, err := grpc.NewClient("unix:"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })

	unread := func(ip, viewing string) map[string]string {
		listed, err := invoke[ListSessionsResponse](metadata.NewOutgoingContext(t.Context(), metadata.Pairs("rocketclaw-principal", ip)), connection, "ListSessions", &ListSessionsRequest{Viewing: viewing, SameOrigin: true})
		require.NoError(t, err)

		out := map[string]string{}
		for _, session := range listed.Sessions {
			out[session.Id] = session.UnreadAt
			if session.MarkedUnread {
				out[session.Id] += " marked"
			}
		}

		for _, tab := range listed.Tabs {
			out[tab.Path] = tab.UnreadAt
		}

		return out
	}

	// Alice set her mark on x herself and has her Cron tab marked, and the listing says so.
	const at = "2026-10-22T10:00:00.5Z"

	mark()
	assert.Equal(t, map[string]string{"x": at + " marked", "y": "", "/cron": at}, unread("127.0.0.1", ""), "an empty viewing lists alice's marks")
	assert.Equal(t, marked, reads(), "and leaves her view of y and her marks unchanged")
	assert.Equal(t, map[string]string{"x": "", "y": ""}, unread("192.0.2.2", ""), "bob has no mark")
	assert.Equal(t, map[string]string{"x": at + " marked", "y": "", "/cron": at}, unread("127.0.0.1", "/s/bm9uZQ"))
	assert.Equal(t, []string{"alice viewing=/s/bm9uZQ", "tab /cron unread=t", "tab /s/eA unread=t"}, reads(), "a viewing that names no session clears no mark")

	assert.Equal(t, map[string]string{"x": "", "y": "", "/cron": at}, unread("127.0.0.1", "/s/x"), "viewing x, in its legacy spelling, clears its mark in the same response")
	assert.Equal(t, []string{"alice viewing=/s/eA", "tab /cron unread=t", "tab /s/eA unread=f"}, reads(), "and ends alice's view of y")
	assert.Equal(t, map[string]string{"x": "", "y": "", "/cron": ""}, unread("127.0.0.1", "/cron"), "viewing the Cron tab clears its mark")

	httpServer := startHTTPTestServer(t, connection)

	// A same-origin poll records the view and ends Alice's due park of her Agents tab; a cross-site one does neither.
	viewed, woken := []string{"alice viewing=/s/eA", "tab /agents unread=t", "tab /cron unread=t", "tab /s/eA unread=f"}, "tab /agents unread=t"
	for _, tc := range []struct {
		header, value, query string
		want                 []string
	}{
		{"Sec-Fetch-Site", "cross-site", "viewing=/s/eA", append([]string{"tab /agents unread=f"}, marked...)},
		{"Origin", "https://elsewhere.example", "viewing=/s/eA", append([]string{"tab /agents unread=f"}, marked...)},
		{"Sec-Fetch-Site", "same-origin", "viewing=/s/eA", viewed},
		{"Sec-Fetch-Site", "same-origin", "viewing=/s/eA&hold", []string{"alice viewing=/s/eA", woken, "tab /cron unread=t", "tab /s/eA unread=t"}},
	} {
		mark()

		_, err := db.ExecContext(t.Context(), `INSERT INTO web_tab_attention (username, path, parked, until) VALUES ('alice', '/agents', TRUE, now() - interval '1 second')
ON CONFLICT (username, path) DO UPDATE SET unread_at = NULL, parked = TRUE, until = excluded.until`)
		require.NoError(t, err)

		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, httpServer.URL+"/api/ListSessions?"+tc.query, http.NoBody)
		require.NoError(t, err)
		request.Header.Set(tc.header, tc.value)

		response, err := httpServer.Client().Do(request)
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, http.StatusOK, response.StatusCode)
		assert.ElementsMatch(t, tc.want, reads(), "%s: %s, %s", tc.header, tc.value, tc.query)
	}
}
