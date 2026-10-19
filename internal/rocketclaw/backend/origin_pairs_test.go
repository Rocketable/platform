package backend

import (
	"context"
	"testing"

	migrate "github.com/rubenv/sql-migrate"
	"github.com/stretchr/testify/require"
)

func TestExternalMCPOriginDetailsStayOutsideHistory(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	store := newTestSessionService(t)
	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	set := migrate.MigrationSet{TableName: "pg_migrations"}
	_, err := set.ExecVersionContext(t.Context(), store.db, "postgres", source, migrate.Down, 24)
	require.NoError(t, err)
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
		n, err := set.ExecMaxContext(t.Context(), store.db, "postgres", source, migrate.Up, 1)
		require.NoError(t, err)
		require.Equal(t, 1, n)

		pairs, err := queryStrings(t.Context(), store.db, `SELECT external_conversation_id || ':' || origin_pairs::text FROM external_mcp_sessions ORDER BY 1`, "migrated origin pairs")
		require.NoError(t, err)
		require.Equal(t, []string{`with-details:{"note": "Unicode Ω", "topic": "first"}`, "without-details:null"}, pairs)
		// This deliberately tests physical migration output at schema 024,
		// before the current application's history projection columns exist.
		entries, err := queryStrings(t.Context(), store.db, `SELECT conversation_id || ':' || (entry_json->>'type') FROM session_entries ORDER BY conversation_id`, "migrated origin entries")
		require.NoError(t, err)
		require.Equal(t, []string{"private:turn", "unbound:mcp_external_origin_pairs"}, entries)
		n, err = set.ExecMaxContext(t.Context(), store.db, "postgres", source, migrate.Down, 1)
		require.NoError(t, err)
		require.Equal(t, 1, n)
	}
}

func TestChatOriginFactsReportsFailures(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()

	for _, id := range []string{"first", "second"} {
		require.NoError(t, store.UpsertThread(id, ThreadState{Agent: "main"}))
	}

	seen := 0

	for _, err := range store.ChatOriginFacts(ctx, "") {
		require.NoError(t, err)

		seen++

		break
	}

	require.Equal(t, 1, seen, "stopping early ends the scan")

	_, err := store.db.ExecContext(ctx, `UPDATE conversations SET external_id = 'external', external_run = 'private', external_agent = 'helper', external_channel = '#triage',
    origin_pairs = '"not an object"' WHERE conversation_id = 'first'`)
	require.NoError(t, err)

	var errs []error

	for _, err := range store.ChatOriginFacts(ctx, "") {
		if err != nil {
			errs = append(errs, err)
		}
	}

	require.Len(t, errs, 1)
	require.ErrorContains(t, errs[0], "decode external MCP origin pairs")
	// A single-session origin read must neither visit nor decode unrelated bindings.
	seen = 0

	for facts, err := range store.ChatOriginFacts(ctx, "second") {
		require.NoError(t, err)
		require.Equal(t, "second", facts.ConversationID)

		seen++
	}

	require.Equal(t, 1, seen)

	for _, err := range store.ChatOriginFacts(ctx, "missing") {
		require.NoError(t, err)
		t.Fatal("missing origin unexpectedly returned a row")
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()

	errs = nil

	for _, err := range store.ChatOriginFacts(canceled, "") {
		errs = append(errs, err)
	}

	require.Len(t, errs, 1)
	require.ErrorIs(t, errs[0], context.Canceled)
}
