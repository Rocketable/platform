package backend

import (
	"context"
	"testing"

	migrate "github.com/rubenv/sql-migrate"
	"github.com/stretchr/testify/require"
)

func TestExternalMCPOriginDetailsStayOutsideHistory(t *testing.T) {
	store := newTestSessionService(t)
	binding := ExternalMCPSessionState{PrivateConversationID: "private", ManagedConversationID: "managed", Agent: "helper", SlackChannel: "#triage", OriginPairs: map[string]string{"topic": "billing"}}
	require.NoError(t, store.RegisterExternalMCPConversation("external", "helper", &binding))

	for _, id := range []string{"private", "managed"} {
		_, got, found, err := store.ExternalMCPSessionByConversationID(id)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, binding, got)
		entries, err := store.ObserveEntries(t.Context(), id)
		require.NoError(t, err)
		require.Empty(t, entries)
	}
}

func TestExternalMCPOriginPairsMigration(t *testing.T) {
	store := newTestSessionService(t)
	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	set := migrate.MigrationSet{TableName: "pg_migrations"}
	n, err := set.ExecMaxContext(t.Context(), store.db, "postgres", source, migrate.Down, 2)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	_, err = store.db.ExecContext(t.Context(), `INSERT INTO external_mcp_sessions
(external_conversation_id, private_conversation_id, managed_conversation_id, agent, slack_channel) VALUES
('with-details', 'private', 'managed', 'helper', '#triage'),
('without-details', 'empty-private', 'empty-managed', 'helper', '#triage');
INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES
('private', '{"type":"mcp_external_origin_pairs","output_trace":[{"pairs":{"topic":"first","note":"Unicode Ω"}}]}', ''),
('private', '{"type":"mcp_external_origin_pairs","output_trace":[{"pairs":{"topic":"later"}}]}', ''),
('managed', '{"type":"mcp_external_origin_pairs","output_trace":[{"pairs":{"topic":"copy"}}]}', ''),
('private', '{"type":"turn","output_trace":[{"text":"answer"}]}', ''),
('unbound', '{"type":"mcp_external_origin_pairs","output_trace":[{"pairs":{"topic":"archived"}}]}', '');`)
	require.NoError(t, err)

	for range 2 {
		n, err = set.ExecMaxContext(t.Context(), store.db, "postgres", source, migrate.Up, 1)
		require.NoError(t, err)
		require.Equal(t, 1, n)

		binding, found, err := store.ExternalMCPSession("with-details")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, map[string]string{"topic": "first", "note": "Unicode Ω"}, binding.OriginPairs)
		binding, found, err = store.ExternalMCPSession("without-details")
		require.NoError(t, err)
		require.True(t, found)
		require.Nil(t, binding.OriginPairs)
		entries, err := store.ObserveEntries(t.Context(), "private")
		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.Equal(t, "turn", entries[0].Entry.Type)
		entries, err = store.ObserveEntries(t.Context(), "managed")
		require.NoError(t, err)
		require.Empty(t, entries)
		entries, err = store.ObserveEntries(t.Context(), "unbound")
		require.NoError(t, err)
		require.Len(t, entries, 1, "unbound archived details must not be discarded")
		n, err = set.ExecMaxContext(t.Context(), store.db, "postgres", source, migrate.Down, 1)
		require.NoError(t, err)
		require.Equal(t, 1, n)
	}
}

func TestChatOriginFactsReportsFailures(t *testing.T) {
	store := newTestSessionService(t)
	ctx := t.Context()

	for _, id := range []string{"first", "second"} {
		require.NoError(t, store.UpsertThread(id, ThreadState{Agent: "main"}))
	}

	seen := 0

	for _, err := range store.ChatOriginFacts(ctx) {
		require.NoError(t, err)

		seen++

		break
	}

	require.Equal(t, 1, seen, "stopping early ends the scan")

	_, err := store.db.ExecContext(ctx, `INSERT INTO external_mcp_sessions (external_conversation_id, private_conversation_id, managed_conversation_id, agent, slack_channel, origin_pairs)
VALUES ('external', 'private', 'first', 'helper', '#triage', '"not an object"')`)
	require.NoError(t, err)

	var errs []error

	for _, err := range store.ChatOriginFacts(ctx) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	require.Len(t, errs, 1)
	require.ErrorContains(t, errs[0], "decode external MCP origin pairs")

	canceled, cancel := context.WithCancel(ctx)
	cancel()

	errs = nil

	for _, err := range store.ChatOriginFacts(canceled) {
		errs = append(errs, err)
	}

	require.Len(t, errs, 1)
	require.ErrorIs(t, errs[0], context.Canceled)
}
