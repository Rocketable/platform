package backend

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	harness "github.com/Rocketable/platform/internal/rocketcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	migrate "github.com/rubenv/sql-migrate"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestDelegationLookupStopsAtFirstChildEntry(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()
	recordTestConversations(t, store, "main", map[string]ConversationKind{"parent-history": KindWebChat})
	recordLegacyDelegations(t, store, "parent-history", "amber", "cobalt")
	_, err := store.db.ExecContext(ctx, `
INSERT INTO entries (conversation_id, entry_json, entry_timestamp) VALUES
    ('parent-history', '{"replay_input":[{"type":"function_call","call_id":"cobalt"},{"type":"function_call","call_id":"amber"},{"type":"function_call","call_id":"missing"}]}', '2000-01-01T00:00:00Z');
INSERT INTO entries (conversation_id, entry_json, entry_timestamp)
SELECT conversation_id, '{}', '2000-01-01T00:00:00Z'
FROM (VALUES ('parent-history/amber'), ('parent-history/cobalt')) c(conversation_id), generate_series(1, 4096);
INSERT INTO entries (conversation_id, entry_json, entry_timestamp)
SELECT 'unrelated-history-' || n, '{}', '2000-01-01T00:00:00Z' FROM generate_series(1, 4096) n;
ANALYZE entries;
SET plan_cache_mode = force_generic_plan;
SELECT pg_stat_force_next_flush();`)
	require.NoError(t, err)

	const historyReads = `SELECT pg_stat_get_tuples_returned('entries'::regclass)
    + COALESCE(SUM(pg_stat_get_tuples_returned(indexrelid)), 0)
FROM pg_index WHERE indrelid = 'entries'::regclass`

	var before, after int64
	require.NoError(t, store.db.QueryRowContext(ctx, historyReads).Scan(&before))
	ids, err := store.Delegations(ctx, "parent-history", "", 0, 0)
	require.NoError(t, err)
	require.Equal(t, []Delegation{{ID: "parent-history/amber", Parent: "parent-history", CallID: "amber", Legacy: true, Kind: KindSubagent}, {ID: "parent-history/cobalt", Parent: "parent-history", CallID: "cobalt", Legacy: true, Kind: KindSubagent}}, ids)

	_, err = store.db.ExecContext(ctx, `SELECT pg_stat_force_next_flush()`)
	require.NoError(t, err)
	require.NoError(t, store.db.QueryRowContext(ctx, historyReads).Scan(&after))
	require.Equal(t, int64(3), after-before, "read the parent and only one indexed entry per child history")
}

func TestChatOriginFactsSkipsKnownSourceLookup(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()
	_, err := store.db.ExecContext(ctx, `
INSERT INTO entries (id, conversation_id, entry_json, entry_timestamp)
VALUES (1000, 'source', '{}', '');
INSERT INTO entries (conversation_id, entry_json, entry_timestamp)
SELECT 'unrelated-' || n, '{}', '' FROM generate_series(1, 512) n;
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed)
SELECT 'web:origin-' || n, 'sample-agent', 'alice', 'web_chat', TRUE FROM generate_series(1, 128) n;
INSERT INTO entries (conversation_id, entry_json, entry_timestamp)
SELECT conversation_id, '{"sync_source_conversation_id":"source","sync_source_entry_id":1000}', ''
FROM conversations;
ANALYZE entries;
SET plan_cache_mode = force_generic_plan;
SELECT pg_stat_force_next_flush();`)
	require.NoError(t, err)

	const sourceReads = `SELECT pg_stat_get_tuples_returned('session_entries_pkey'::regclass)`

	var before, after int64
	require.NoError(t, store.db.QueryRowContext(ctx, sourceReads).Scan(&before))

	chats, err := queryStrings(ctx, store.db, `SELECT conversation_id FROM conversations`, "chats")
	require.NoError(t, err)
	saveEntryFacts(t, store, chats...)

	_, err = store.db.ExecContext(ctx, `SELECT pg_stat_force_next_flush()`)
	require.NoError(t, err)
	require.NoError(t, store.db.QueryRowContext(ctx, sourceReads).Scan(&after))
	require.Equal(t, before, after, "an explicit source needs no source-entry lookup")

	seen := 0

	for facts, err := range store.ChatOriginFacts(ctx, "") {
		require.NoError(t, err)
		require.Equal(t, "source", facts.CreatingSource)

		seen++
	}

	require.Equal(t, 128, seen)
}

// saveEntryFacts saves each conversation's summary again, which reads its entry facts as a write does.
func saveEntryFacts(t *testing.T, store *SessionService, ids ...string) {
	t.Helper()

	for _, id := range ids {
		summary, err := loadSessionSummary(t.Context(), store.db, id)
		require.NoError(t, err)
		require.NoError(t, saveSessionSummary(t.Context(), store.db, summary))
	}
}

// A few chats receiving many cron reports must not make the listing read every entry or
// conversation once per chat: the planner bet a walk in ID order would soon meet a report.
func TestChatListingReadsScaleWithReportingRuns(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()
	_, err := store.db.ExecContext(ctx, `
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, last_updated)
SELECT 'chat-' || n, 'main', 'alice', 'web_chat', TRUE, now() FROM generate_series(1, 256) n;
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, reports_to)
SELECT 'run-' || c || '-' || n, 'job', '', 'cron_run', FALSE, 'chat-' || c FROM generate_series(1, 4) c, generate_series(1, 256) n;
-- Runs 1-1 and 2-1 report before their chats' own entries, chat-3's runs after them; chat-4 has none.
INSERT INTO entries (conversation_id, entry_json, entry_timestamp)
SELECT conversation_id, '{}', '' FROM conversations, generate_series(1, 8) WHERE conversation_id <> 'chat-4'
ORDER BY conversation_id NOT IN ('run-1-1', 'run-2-1'), conversation_id LIKE 'run-%', conversation_id;
ANALYZE conversations;
ANALYZE entries;
SELECT pg_stat_force_next_flush();`)
	require.NoError(t, err)

	creators := map[string]string{"chat-1": "run-1-1", "chat-2": "run-2-1", "chat-4": "run-4-1"}
	chats, err := queryStrings(ctx, store.db, `SELECT conversation_id FROM conversations WHERE listed`, "chats")
	require.NoError(t, err)
	saveEntryFacts(t, store, chats...)
	_, err = store.db.ExecContext(ctx, `SELECT pg_stat_force_next_flush()`)
	require.NoError(t, err)

	const reads = `SELECT sum(pg_stat_get_tuples_returned(c.oid)) FROM pg_class c
LEFT JOIN pg_index i ON i.indexrelid = c.oid WHERE c.oid IN ('entries'::regclass, 'conversations'::regclass)
    OR i.indrelid IN ('entries'::regclass, 'conversations'::regclass)`

	var before, after int64
	require.NoError(t, store.db.QueryRowContext(ctx, reads).Scan(&before))

	listed := 0

	for _, err := range store.sidebarSessions(ctx, nil) {
		require.NoError(t, err)

		listed++
	}

	for facts, err := range store.ChatOriginFacts(ctx, "") {
		require.NoError(t, err)
		require.Equal(t, creators[facts.ConversationID], facts.CreatingSource, facts.ConversationID)
	}

	_, err = store.db.ExecContext(ctx, `SELECT pg_stat_force_next_flush()`)
	require.NoError(t, err)
	require.NoError(t, store.db.QueryRowContext(ctx, reads).Scan(&after))
	require.Equal(t, 256, listed)
	require.Less(t, after-before, int64(4096), "reads grow with chats and runs, never with entries")

	for id, creator := range creators {
		var sources []string

		for facts, err := range store.ChatOriginFacts(ctx, id) {
			require.NoError(t, err)

			sources = append(sources, facts.CreatingSource)
		}

		require.Equal(t, []string{creator}, sources, id)
	}
}

func TestExternalMCPMetadataLookupUsesIndex(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()
	_, err := store.db.ExecContext(ctx, `
INSERT INTO entries (conversation_id, entry_json, entry_timestamp) VALUES
    ('with-metadata', '{"type":"mcp_external_metadata","version":1}', '2000-01-01T00:00:00Z'),
    ('with-metadata', '{"type":"mcp_external_metadata","version":2}', '2000-01-01T00:00:00Z'),
    ('other', '{"type":"mcp_external_metadata","version":3}', '2000-01-01T00:00:00Z');
INSERT INTO entries (conversation_id, entry_json, entry_timestamp)
SELECT conversation_id, '{"type":"turn"}', '2000-01-01T00:00:00Z'
FROM (VALUES ('with-metadata'), ('without-metadata')) c(conversation_id), generate_series(1, 4096);
ANALYZE entries;
SET plan_cache_mode = force_generic_plan;`)
	require.NoError(t, err)

	for _, conversationID := range []string{"with-metadata", "without-metadata", "missing"} {
		entry, found, err := store.externalMCPMetadataEntry(ctx, conversationID)
		require.NoError(t, err)
		require.Equal(t, conversationID == "with-metadata", found)

		if found {
			require.Equal(t, 2, entry.Entry.Version)
			require.Equal(t, externalMCPMetadataEntryType, entry.Entry.Type)
		}
	}

	// Explain the actual prepared lookup, not a separately maintained query.
	queries, err := queryStrings(ctx, store.db, `SELECT format('EXPLAIN EXECUTE %I(%s)', name,
    (SELECT string_agg('NULL', ',') FROM unnest(parameter_types)))
FROM pg_prepared_statements WHERE statement LIKE 'SELECT id, entry_json FROM entries%'`, "metadata lookup statement")
	require.NoError(t, err)
	require.Len(t, queries, 1)
	plan, err := queryStrings(ctx, store.db, queries[0], "metadata lookup plan")
	require.NoError(t, err)
	require.Contains(t, strings.Join(plan, "\n"), "session_entries_mcp_metadata")
	require.NotContains(t, strings.Join(plan, "\n"), "Filter:")
	require.NotContains(t, strings.Join(plan, "\n"), "Sort")
	t.Log(strings.Join(plan, "\n"))
}

func TestHistoryVisibilityUsesRevertedParentIndex(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()
	_, err := store.db.ExecContext(ctx, `
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed)
SELECT 'unrelated-' || n, 'main', '', 'web_chat', TRUE FROM generate_series(1, 4096) n;
INSERT INTO conversations (conversation_id, agent, created_by, revert_message_id, kind, listed)
VALUES ('parent', 'main', '', '10:1', 'web_chat', TRUE);
INSERT INTO entries (id, conversation_id, entry_json, entry_timestamp) VALUES
    (10, 'parent', '{"replay_input":[{"type":"function_call","call_id":"kept"},{"type":"function_call","call_id":"discarded"}]}', ''),
    (20, 'parent/kept', '{}', ''), (30, 'parent/discarded', '{}', ''), (40, 'ordinary', '{}', '');
ANALYZE conversations;
ANALYZE entries;
SET plan_cache_mode = force_generic_plan;`)
	require.NoError(t, err)
	recordLegacyDelegations(t, store, "parent", "kept", "discarded")

	for _, tc := range []struct {
		conversationID string
		id             int64
	}{
		{"parent/kept", 20}, {"parent/discarded", 0}, {"ordinary", 40}, {"missing", 0},
	} {
		start, oldest, err := store.TranscriptPage(ctx, tc.conversationID, 0, 1)
		require.NoError(t, err)
		require.Equal(t, tc.id, start)
		require.Equal(t, tc.id, oldest)
	}

	// Explain the actual prepared history query, including its shared visibility check.
	queries, err := queryStrings(ctx, store.db, `SELECT format('EXPLAIN EXECUTE %I(%s)', name,
    (SELECT string_agg('NULL', ',') FROM unnest(parameter_types)))
FROM pg_prepared_statements WHERE statement = $1`, "history statement", transcriptPageSQL)
	require.NoError(t, err)
	require.Len(t, queries, 1)
	plan, err := queryStrings(ctx, store.db, queries[0], "history visibility plan")
	require.NoError(t, err)
	require.Contains(t, strings.Join(plan, "\n"), "managed_conversations_reverted")
	require.NotContains(t, strings.Join(plan, "\n"), "Seq Scan on conversations parent")
	t.Log(strings.Join(plan, "\n"))
}

func TestTranscriptChangeUsesSyncSourceIndex(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	_, err := store.db.ExecContext(t.Context(), `
INSERT INTO entries (conversation_id, entry_json, entry_timestamp)
SELECT 'history-' || n, json_build_object('sync_source_entry_id', n), '' FROM generate_series(1, 4096) n;
ANALYZE entries;`)
	require.NoError(t, err)

	plan, err := queryStrings(t.Context(), store.db, `EXPLAIN SELECT DISTINCT conversation_id FROM entries
WHERE entry_json::jsonb->>'sync_source_entry_id' = $1`, "transcript change plan", "2048")
	require.NoError(t, err)
	require.Contains(t, strings.Join(plan, "\n"), "session_entries_sync_source_id")
	require.NotContains(t, strings.Join(plan, "\n"), "Seq Scan")
}

func TestShadowHistoryUsesProducerDestinationIndex(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()
	_, err := store.db.ExecContext(ctx, `
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed)
SELECT 'unrelated-' || n, 'sample-agent', '', 'web_chat', TRUE FROM generate_series(1, 4096) n;
INSERT INTO conversations (conversation_id, agent, created_by, producer_inbound_json, reports_to, kind, listed) VALUES
    ('destination', 'sample-agent', '', '{}', NULL, 'web_chat', TRUE),
    ('producer', 'sample-agent', '', '{"SyncDestination":"destination"}', 'destination', 'cron_run', FALSE),
    ('producer/comparison', 'sample-agent/comparison', '', '{}', NULL, 'shadow', FALSE);
UPDATE conversations SET shadow_parent = 'producer', parent_agent = 'sample-agent', shadow_agent = 'sample-agent/comparison' WHERE conversation_id = 'producer/comparison';
ANALYZE conversations;
SET plan_cache_mode = force_generic_plan;`)
	require.NoError(t, err)

	view, err := store.ObserveHistory(ctx, "destination", "", 0, 0, 0, &HistoryInventory{})
	require.NoError(t, err)
	require.Equal(t, []protocol.ShadowBinding{{ParentConversationID: "producer", ParentAgent: "sample-agent", ShadowAgent: "sample-agent/comparison", ChildConversationID: "producer/comparison"}}, view.Shadows)

	// Both prepared queries use the shared producer lookup, not a copied test query.
	queries, err := queryStrings(ctx, store.db, `SELECT format('EXPLAIN EXECUTE %I(%s)', name,
    array_to_string(array_fill('NULL'::text, ARRAY[cardinality(parameter_types)]), ','))
FROM pg_prepared_statements WHERE starts_with(statement, $1) ORDER BY name`, "shadow history statements", shadowHistorySQL)
	require.NoError(t, err)
	require.Len(t, queries, 2)

	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	set := migrate.MigrationSet{TableName: "pg_migrations"}

	for range 2 {
		for _, query := range queries {
			plan, err := queryStrings(ctx, store.db, query, "shadow history plan")
			require.NoError(t, err)
			require.Contains(t, strings.Join(plan, "\n"), "managed_conversations_reports_to")
			t.Log(strings.Join(plan, "\n"))
		}

		down, err := set.ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 47)
		require.NoError(t, err)

		var index sql.NullString

		require.NoError(t, store.db.QueryRowContext(ctx, `SELECT to_regclass('managed_conversations_reports_to')::text`).Scan(&index))
		require.False(t, index.Valid)

		previous := backfillPreviousRelease(t, store.db, "pg_migrations")
		n, err := set.ExecContext(ctx, store.db, "postgres", source, migrate.Up)
		require.NoError(t, err)
		require.Equal(t, down, previous+n)

		// Re-running the migrations rebuilds conversations; refresh its statistics as the first pass did.
		_, err = store.db.ExecContext(ctx, `ANALYZE conversations`)
		require.NoError(t, err)
	}
}

func sessionMigrationCount(t *testing.T) int {
	t.Helper()

	migrations, err := (migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}).FindMigrations()
	require.NoError(t, err)

	return len(migrations)
}

func TestSessionMigrationsSerializeStartup(t *testing.T) {
	t.Parallel()

	for _, outcome := range []string{"success", "cancel", "cancel waiting", "connection loss"} {
		t.Run(outcome, func(t *testing.T) {
			dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
			require.NoError(t, err)
			cfg, err := pgx.ParseConfig(dsn)
			require.NoError(t, err)

			db := stdlib.OpenDB(*cfg)
			defer func() { require.NoError(t, db.Close()) }()

			source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
			n, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecMaxContext(t.Context(), db, "postgres", source, migrate.Up, 5)
			require.NoError(t, err)
			require.Equal(t, 5, n)
			barrier, err := db.BeginTx(t.Context(), nil)
			require.NoError(t, err)

			defer func() { _ = barrier.Rollback() }()

			_, err = barrier.ExecContext(t.Context(), `LOCK TABLE pg_migrations IN SHARE MODE`)
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			var first errgroup.Group
			first.Go(func() error {
				store, err := NewSessionServiceIn(ctx, &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
				if err != nil {
					return err
				}

				return store.Stop()
			})

			var pid int

			require.Eventually(t, func() bool {
				return db.QueryRowContext(t.Context(), `SELECT a.pid FROM pg_stat_activity a JOIN pg_locks l ON a.pid=l.pid WHERE l.relation='pg_migrations'::regclass AND NOT l.granted AND a.query LIKE 'INSERT INTO pg_migrations%'`).Scan(&pid) == nil
			}, 5*time.Second, time.Millisecond)

			ctxSecond, cancelSecond := context.WithCancel(t.Context())
			defer cancelSecond()

			var second errgroup.Group
			second.Go(func() error {
				store, err := NewSessionServiceIn(ctxSecond, &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
				if err != nil {
					return err
				}

				return store.Stop()
			})
			require.Eventually(t, func() bool {
				var waiting int

				err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted AND classid=hashtext('rocketclaw schema migrations')::oid AND objid=hashtext(current_schema())::oid`).Scan(&waiting)

				return err == nil && waiting == 1
			}, 5*time.Second, time.Millisecond)

			switch outcome {
			case "cancel waiting":
				cancelSecond()
				require.Error(t, second.Wait())
			case "cancel":
				cancel()
				require.Error(t, first.Wait())
			case "connection loss":
				_, err = db.ExecContext(t.Context(), `SELECT pg_terminate_backend($1)`, pid)
				require.NoError(t, err)
				require.Error(t, first.Wait())
			}

			if outcome == "cancel" || outcome == "connection loss" {
				var columns int
				require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_attribute WHERE attrelid='thread_queue'::regclass AND attname='kind' AND NOT attisdropped`).Scan(&columns))
				require.Zero(t, columns)
				require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_migrations`).Scan(&n))
				require.Equal(t, 5, n)
			}

			require.NoError(t, barrier.Rollback())

			if outcome == "success" || outcome == "cancel waiting" {
				require.NoError(t, first.Wait())
			}

			if outcome != "cancel waiting" {
				require.NoError(t, second.Wait())
			}

			require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_migrations`).Scan(&n))
			require.Equal(t, sessionMigrationCount(t), n)
			// No migration lock may survive startup and poison later pool users.
			require.Eventually(t, func() bool {
				var locks int

				err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND classid=hashtext('rocketclaw schema migrations')::oid AND objid=hashtext(current_schema())::oid`).Scan(&locks)

				return err == nil && locks == 0
			}, 5*time.Second, time.Millisecond)
		})
	}
}

func TestSessionMigrationsSerializeLedgerCreation(t *testing.T) {
	t.Parallel()

	for _, legacy := range []bool{false, true} {
		t.Run(strconv.FormatBool(legacy), func(t *testing.T) {
			dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
			require.NoError(t, err)
			cfg, err := pgx.ParseConfig(dsn)
			require.NoError(t, err)

			db := stdlib.OpenDB(*cfg)
			defer func() { require.NoError(t, db.Close()) }()

			if legacy {
				_, err = (migrate.MigrationSet{TableName: "gorp_migrations"}).ExecMaxContext(t.Context(), db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Up, 5)
				require.NoError(t, err)
			}

			barrier, err := db.BeginTx(t.Context(), nil)
			require.NoError(t, err)

			defer func() { _ = barrier.Rollback() }()

			_, err = barrier.ExecContext(t.Context(), `SELECT pg_advisory_xact_lock(hashtext('rocketclaw schema migrations'), hashtext(current_schema()))`)
			require.NoError(t, err)

			var callers errgroup.Group
			for range 2 {
				callers.Go(func() error {
					store, err := NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
					if err != nil {
						return err
					}

					return store.Stop()
				})
			}

			require.Eventually(t, func() bool {
				var waiting int

				err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted AND classid=hashtext('rocketclaw schema migrations')::oid AND objid=hashtext(current_schema())::oid`).Scan(&waiting)

				return err == nil && waiting == 2
			}, 5*time.Second, time.Millisecond)

			var exists bool
			require.NoError(t, db.QueryRowContext(t.Context(), `SELECT to_regclass('pg_migrations') IS NOT NULL`).Scan(&exists))
			require.False(t, exists, "both callers must wait before creating or renaming the ledger")
			require.NoError(t, barrier.Rollback())
			require.NoError(t, callers.Wait())

			var count int
			require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_migrations`).Scan(&count))
			require.Equal(t, sessionMigrationCount(t), count)
		})
	}
}

func TestSessionMigrationsCancelPlanning(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	store := newTestSessionServiceAt(t, workspace)
	store.db.SetMaxOpenConns(2)
	barrier, err := store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)

	defer func() { _ = barrier.Rollback() }()

	_, err = barrier.ExecContext(t.Context(), `LOCK TABLE pg_migrations IN ACCESS EXCLUSIVE MODE`)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var work errgroup.Group
	work.Go(func() error {
		return Run(ctx, &config.Config{Workspace: workspace, DatabaseURL: testStoreDSN(workspace)}, "", slog.New(slog.DiscardHandler), &frontendAssemblerMock{})
	})
	require.Eventually(t, func() bool {
		var n int

		err := barrier.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_locks WHERE relation='pg_migrations'::regclass AND NOT granted`).Scan(&n)

		return err == nil && n == 1
	}, 5*time.Second, time.Millisecond)
	cancel()
	require.Error(t, work.Wait())
	require.NoError(t, barrier.Rollback())
	// A single connection must suffice on the next call, even after cancellation.
	store.db.SetMaxOpenConns(1)
	require.NoError(t, initializeSessionDB(t.Context(), store.db, slog.New(slog.DiscardHandler)))
	require.NoError(t, store.db.PingContext(t.Context()))
}

func TestSessionMigrationRollbackAndCatchup(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	_, err := store.db.ExecContext(ctx, `DELETE FROM pg_migrations WHERE id IN ('009_session_summaries.sql', '010_slack_channel_facts.sql', '011_last_message_summaries.sql');
		DROP TABLE legacy_session_summaries, slack_channel_facts;
		CREATE FUNCTION reject_migration() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.id='010_slack_channel_facts.sql' THEN RAISE EXCEPTION 'ledger write failed'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER reject_migration BEFORE INSERT ON pg_migrations FOR EACH ROW EXECUTE FUNCTION reject_migration()`)
	require.NoError(t, err)
	// 009 commits; 010's DDL rolls back when its ledger write fails.
	require.Error(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	var n int
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_migrations`).Scan(&n))
	require.Equal(t, sessionMigrationCount(t)-2, n)

	var missing sql.NullString
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT to_regclass('slack_channel_facts')::text`).Scan(&missing))
	require.False(t, missing.Valid)

	_, err = store.db.ExecContext(ctx, `DROP TRIGGER reject_migration ON pg_migrations; DROP FUNCTION reject_migration()`)
	require.NoError(t, err)
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	var applied time.Time
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT applied_at FROM pg_migrations WHERE id='010_slack_channel_facts.sql'`).Scan(&applied))
	_, err = store.db.ExecContext(ctx, `DELETE FROM pg_migrations WHERE id='009_session_summaries.sql'; DROP TABLE session_summaries`)
	require.NoError(t, err)
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	var unchanged time.Time
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT applied_at FROM pg_migrations WHERE id='010_slack_channel_facts.sql'`).Scan(&unchanged))
	require.Equal(t, applied, unchanged)

	_, err = store.db.ExecContext(ctx, `INSERT INTO pg_migrations VALUES ('unknown.sql', now())`)
	require.NoError(t, err)
	require.ErrorContains(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)), "unknown migration")
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT to_regclass('session_summaries')::text`).Scan(&missing))
	require.True(t, missing.Valid)
}

func TestProducerSessionTagsMigration(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	// Back to before 059 gave the tables 025 writes their legacy_ names.
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 58)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `DELETE FROM pg_migrations WHERE id='025_copy_producer_session_tags.sql';
INSERT INTO external_mcp_sessions (external_conversation_id, private_conversation_id, managed_conversation_id, agent, slack_channel) VALUES
    ('e1', 'private-missing', 'visible-missing', 'main', ''),
    ('e2', 'private-tagged', 'visible-tagged', 'main', ''),
    ('e3', 'private-empty', 'visible-empty', 'main', ''),
    ('e4', 'private-untagged', 'visible-untagged', 'main', '');
INSERT INTO session_tags (conversation_id, tags) VALUES
    ('private-missing', '["customer"]'),
    ('private-tagged', '["private"]'), ('visible-tagged', '["kept"]'),
    ('private-empty', '["filled"]'), ('visible-empty', '[]'),
    ('private-untagged', '[]'),
    ('unbound', '["alone"]')`)
	require.NoError(t, err)
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	tags, err := queryStrings(ctx, store.db, `SELECT conversation_id || '=' || tags::text FROM legacy_session_tags ORDER BY conversation_id`, "session tags")
	require.NoError(t, err)
	require.Equal(t, []string{
		`private-empty=["filled"]`,
		`private-missing=["customer"]`,
		`private-tagged=["private"]`,
		`private-untagged=[]`,
		`unbound=["alone"]`,
		`visible-empty=["filled"]`,
		`visible-missing=["customer"]`,
		`visible-tagged=["kept"]`,
	}, tags)
}

func TestDropScriptCallsMigration(t *testing.T) {
	store := newTestSessionService(t)
	ctx := t.Context()
	// Back to before 032_drop_script_calls and the migrations that follow it.
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 32)
	require.NoError(t, err)

	progress := `{"type":"rocketcode_public_progress","progress":{"id":"script","kind":"tool","state":"completed"}}`
	trace := func(files string) string {
		return `[{"type":"function_call","call_id":"script/host/1","parent_call_id":"script","name":"read","arguments":"{}"},` +
			`{"type":"function_call_output","call_id":"script/host/1","parent_call_id":"script","output":"file contents"},` +
			`{"type":"function_call","call_id":"script/host/2","parent_call_id":"script","name":"rocketclaw_attach_files_to_response","arguments":"{}"},` +
			`{"type":"function_call_output","call_id":"script/host/2","parent_call_id":"script","output":"queued attachments for final response: ` + files + `"},` +
			progress + `,false]`
	}
	_, err = store.db.ExecContext(ctx, `INSERT INTO session_entries (id, conversation_id, entry_json, entry_timestamp) VALUES
    (1, 'chat', json_build_object('type', 'turn', 'turn_id', 'saved', 'output_trace', $1::json), ''),
    (2, 'chat', '{"type":"turn","turn_id":"plain","output_trace":[]}', '')`, trace("first second"))
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `INSERT INTO active_turns (id, conversation_id, inbound_json, output_trace_json, history_anchor_id, created_at_unix_ns, updated_at_unix_ns) VALUES
    ('running', 'chat', '{}', $1, 2, 1, 1)`, trace("third"))
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `INSERT INTO attachments (id, conversation_id, name, mime_type, size, original_unverified, upload) VALUES
    ('first', 'chat', 'a', '', 0, false, false), ('second', 'chat', 'b', '', 0, false, false), ('third', 'chat', 'c', '', 0, false, false), ('upload', 'chat', 'd', '', 0, false, true)`)
	require.NoError(t, err)

	var before, after string

	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT entry_json::text FROM session_entries WHERE id = 2`).Scan(&before))
	// 049_hidden_run_entries refuses the running turn, so this stops at 047_opencode_session_agent_model.
	_, err = (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Up, 47)
	require.NoError(t, err)

	for _, query := range []string{`SELECT (entry_json->'output_trace')::text FROM session_entries WHERE id = 1`, `SELECT output_trace_json FROM active_turns WHERE id = 'running'`} {
		var raw string
		require.NoError(t, store.db.QueryRowContext(ctx, query).Scan(&raw))
		require.JSONEq(t, `[`+progress+`,false]`, raw, "calls made inside a script are dropped, other trace records stay in order")
	}

	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT entry_json::text FROM session_entries WHERE id = 2`).Scan(&after))
	require.Equal(t, before, after, "an entry without script calls stays byte-identical")

	for turn, want := range map[string][]string{"saved": {"first", "second"}, "running": {"third"}, "": {"upload"}} {
		ids, err := queryStrings(ctx, store.db, `SELECT id FROM attachments WHERE conversation_id = 'chat' AND turn_id = $1 ORDER BY seq`, "turn attachments", turn)
		require.NoError(t, err)
		require.Equal(t, want, ids, "files attached inside a script belong to that turn's final reply, in order")
	}
}

// 038 gives a subagent that owns Background Jobs its session row, as task creates one, under the
// session it nests in, so its pending Completion Note wakes it at startup. Its turns read only the task
// turns of its history, as before it was a session; a hidden cron run, with no session row, keeps its history.
func TestAgentSessionControlMigrationCreatesSubagentSessions(t *testing.T) {
	t.Parallel()

	nt := newNoteTest(t, answerNoteTest)
	set, source := migrate.MigrationSet{TableName: "pg_migrations"}, migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	down, err := set.ExecVersionContext(t.Context(), nt.service.db, "postgres", source, migrate.Down, 38)
	require.NoError(t, err)

	_, err = nt.service.db.ExecContext(t.Context(), `INSERT INTO background_jobs (conversation_id, job_id, child_key, kind, status, agent, label, call_id, subagent_key, origin_json, sync_destination, runner_id, note_state, wake, claimed_turn_id, created_at_unix_ns, finished_at_unix_ns, result)
VALUES ($1, 'turn-0/call/a', '/a', 'execute', 'completed', 'researcher', 'tests', 'call', '', '{}', '', 'runner-1', 'pending', TRUE, '', 1, 1, 'completed'),
    ($1, 'turn-0/call/b', '/a/b', 'execute', 'completed', 'writer', 'tests', 'call', '', '{}', '', 'runner-1', 'consumed', FALSE, '', 2, 2, 'completed')`, nt.conversationID)
	require.NoError(t, err)

	cron := "cron:cron/HEARTBEAT.md:20261015T000000Z:ABC"
	_, err = nt.service.db.ExecContext(t.Context(), `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES
    ($3, '{"version":1,"type":"turn","turn_id":"turn-0","replay_input":[{"type":"function_call","call_id":"a","name":"task","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"done"}]}', ''),
    ($1, '{"version":1,"type":"turn","turn_id":"turn-0/call/a/task","replay_input":[{"type":"message","role":"user","content":"task turn"},{"type":"function_call","call_id":"b","name":"task","arguments":"{}"},{"type":"function_call_output","call_id":"b","output":"done"}]}', ''),
    ($1, '{"version":1,"type":"turn","turn_id":"7","replay_input":[{"type":"message","role":"user","content":"guardrail run"}]}', ''),
    ($1, '{"version":1,"type":"turn","replay_input":[{"type":"message","role":"user","content":"untagged entry"}]}', ''),
    ($1, '{"version":1,"type":"turn","turn_id":"turn-0/call/a-review","replay_input":[{"type":"message","role":"user","content":"review run"}]}', ''),
    ($2, '{"version":1,"type":"turn","turn_id":"turn-123"}', '')`, nt.conversationID+"/a", cron, nt.conversationID)
	require.NoError(t, err)

	previous := backfillPreviousRelease(t, nt.service.db, "pg_migrations")
	n, err := set.ExecContext(t.Context(), nt.service.db, "postgres", source, migrate.Up)
	require.NoError(t, err)
	require.Equal(t, down, previous+n)

	sessions, err := queryStrings(t.Context(), nt.service.db, `SELECT conversation_id || ' ' || agent || ' ' || created_by || ' ' || COALESCE(parent_conversation_id, '') FROM conversations
WHERE kind <> 'cron_run' ORDER BY conversation_id`, "sessions")
	require.NoError(t, err)
	require.Equal(t, []string{nt.conversationID + " main  ", nt.conversationID + "/a researcher  " + nt.conversationID, nt.conversationID + "/a/b writer  " + nt.conversationID + "/a"}, sessions, "a nested subagent's parent is the subagent it nests in")

	entries, err := queryStrings(t.Context(), nt.service.db, `SELECT conversation_id || ' ' || COALESCE(entry_json->>'turn_id', '-') || ' ' || (entry_json->>'type') FROM entries ORDER BY id`, "entries")
	require.NoError(t, err)

	sub := nt.conversationID + "/a "
	require.Equal(t, []string{nt.conversationID + " turn-0 turn", sub + "turn-0/call/a/task turn", sub + "7 guardrail", sub + "- guardrail", sub + "turn-0/call/a-review guardrail", cron + " turn-123 turn"}, entries)

	manager, _ := nt.restart(t)
	body := nt.request(t)
	require.Equal(t, systemPromptHeader()+"\n\n"+testExecuteNote("turn-0/call/a"), lastRequestMessage(t, body))
	require.Contains(t, body, "task turn")

	for _, skipped := range []string{"guardrail run", "untagged entry", "review run"} {
		require.NotContains(t, body, skipped)
	}

	nt.waitIdle(t, manager, nt.conversationID+"/a")
	require.Equal(t, []string{"turn-0/call/a consumed true", "turn-0/call/b consumed false"}, noteStates(t, nt.service))
}

func TestProducerHandoffMigration(t *testing.T) {
	t.Parallel()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	cfg, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)

	db := stdlib.OpenDB(*cfg)
	defer func() { require.NoError(t, db.Close()) }()

	ctx := t.Context()
	_, err = (migrate.MigrationSet{TableName: "pg_migrations"}).ExecMaxContext(ctx, db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Up, 28)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
INSERT INTO managed_conversations (conversation_id, agent, created_by) VALUES
    ('recoverable', 'main', ''), ('projected', 'main', ''), ('silent', 'main', 'cron'),
    ('delivered', 'main', 'cron'), ('legacy', 'main', ''), ('onward', 'main', ''), ('ordinary', 'main', '');
INSERT INTO session_entries (id, conversation_id, entry_json, entry_timestamp) VALUES
    (10, 'recoverable', '{"type":"producer_reset_schedules"}', ''),
    (11, 'recoverable', '{"type":"producer_schedule"}', ''),
    (12, 'recoverable', '{"type":"producer_reset_schedules"}', ''),
    (13, 'recoverable', '{"type":"producer_schedule"}', ''),
    (20, 'projected', '{"type":"producer_schedule"}', ''),
    (30, 'silent', '{"type":"producer_schedule"}', ''),
    (40, 'legacy', '{"type":"producer_reset_schedules"}', ''),
    (50, 'delivered', '{"type":"producer_schedule"}', ''),
    (101, 'owner', '{"type":"producer_schedule","sync_source_entry_id":11,"sync_source_conversation_id":"recoverable"}', ''),
    (102, 'alternate', '{"type":"producer_reset_schedules","sync_source_entry_id":12,"sync_source_conversation_id":"recoverable"}', ''),
    (103, 'owner', '{"type":"producer_schedule","sync_source_entry_id":20}', ''),
    (104, 'onward', '{"type":"producer_reset_schedules","sync_source_entry_id":40}', ''),
    (105, 'slack-thread:C1:1.2', '{"type":"producer_schedule","sync_source_entry_id":50}', '');
SELECT setval(pg_get_serial_sequence('session_entries', 'id'), 105);
INSERT INTO active_turns (id, conversation_id, inbound_json, output_trace_json, history_anchor_id, created_at_unix_ns, updated_at_unix_ns, phase) VALUES
    ('active', 'recoverable', '{"ConversationID":"recoverable","SyncDestination":"owner"}', '[]', 10, 1, 1, 'running'),
    ('already', 'projected', '{"ConversationID":"projected","SyncDestination":"owner"}', '[]', 19, 1, 1, 'delivering'),
    ('cron', 'silent', '{"ConversationID":"silent","RequireOutputDecision":true}', '[]', 29, 1, 1, 'running'),
    ('report', 'delivered', '{"ConversationID":"delivered","RequireOutputDecision":true}', '[]', 49, 1, 1, 'delivering');
INSERT INTO turn_steps (conversation_id, key, value) VALUES
    ('recoverable', 'active/tool/schedule', '{"output":"scheduled"}'),
    ('delivered', 'report/cron-root', '{"ChannelID":"C1","MessageID":"1.2"}');
INSERT INTO scheduled_messages (scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring, interval_ns) VALUES
    ('kept', 'owner', 'selected', 'existing schedule', 123, 1, 456);`)
	require.NoError(t, err)
	// 049_hidden_run_entries refuses running turns, so this inspects the schema 047_opencode_session_agent_model leaves.
	_, err = (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Up, 47)
	require.NoError(t, err)

	for _, tc := range []struct {
		id, owner string
		through   int64
		pending   []int64
	}{
		{"recoverable", "owner", 11, []int64{12, 13}},
		{"projected", "owner", 20, nil},
		{"silent", "", 0, []int64{30}},
		{"delivered", "slack-thread:C1:1.2", 50, nil},
		{"legacy", "", 40, nil},
		{"onward", "", 0, nil},
		{"ordinary", "", 0, nil},
	} {
		t.Run(tc.id, func(t *testing.T) {
			var (
				routing *string
				through int64
			)
			require.NoError(t, db.QueryRowContext(t.Context(), `SELECT producer_inbound_json->>'SyncDestination', producer_effects_through_id FROM managed_conversations WHERE conversation_id = $1`, tc.id).Scan(&routing, &through))
			require.Equal(t, tc.through, through)

			if tc.id == "legacy" || tc.id == "onward" || tc.id == "ordinary" {
				require.Nil(t, routing)
			} else {
				require.NotNil(t, routing)
				require.Equal(t, tc.owner, *routing)
			}

			ids, err := queryStrings(t.Context(), db, `SELECT id::text FROM session_entries WHERE conversation_id = $1 AND id > $2
    AND entry_json->>'type' IN ('producer_schedule', 'producer_reset_schedules') AND NOT entry_json::jsonb ? 'sync_source_entry_id' ORDER BY id`, "pending effects", tc.id, through)
			require.NoError(t, err)

			pending := make([]string, 0, len(tc.pending))
			for _, id := range tc.pending {
				pending = append(pending, strconv.FormatInt(id, 10))
			}

			require.Equal(t, pending, append([]string{}, ids...))
		})
	}

	var step string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT value FROM turn_steps WHERE conversation_id = 'recoverable' AND key = 'active/tool/schedule'`).Scan(&step))
	require.JSONEq(t, `{"output":"scheduled"}`, step, "the scheduling result remains available to journal replay")

	messages, err := queryMap(ctx, db, `SELECT scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring <> 0, interval_ns FROM scheduled_messages`, "scheduled messages", scheduledMessageScanner{})
	require.NoError(t, err)
	require.Equal(t, map[string]protocol.ScheduledMessageState{"kept": {ConversationID: "owner", Agent: "selected", Message: "existing schedule", DueAt: time.Unix(0, 123).UTC(), Recurring: true, Interval: 456}}, messages)
}

func TestBackgroundJobsMigration(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	set := migrate.MigrationSet{TableName: "pg_migrations"}
	schema := func() (bool, string) {
		var (
			table sql.NullString
			body  string
		)
		require.NoError(t, store.db.QueryRowContext(ctx, `SELECT to_regclass('background_jobs')::text, prosrc FROM pg_proc WHERE proname = 'notify_transcript_change' AND pronamespace = current_schema()::regnamespace`).Scan(&table, &body))

		return table.Valid, body
	}

	for range 2 {
		down, err := set.ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 28)
		require.NoError(t, err)

		exists, body := schema()
		require.False(t, exists)
		require.NotContains(t, body, "background_jobs")

		_, err = store.db.ExecContext(ctx, `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('web-session:rolled-back', '{}', '')`)
		require.NoError(t, err, "the restored transcript trigger still runs")

		n, err := set.ExecContext(ctx, store.db, "postgres", source, migrate.Up)
		require.NoError(t, err)
		require.Equal(t, down, n)

		// 059 retired the table and its branch; Background Jobs are turns.
		exists, body = schema()
		require.False(t, exists)
		require.NotContains(t, body, "sync_destination")
	}
}

func TestSessionSummaryRecencyIndexMigration(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	set := migrate.MigrationSet{TableName: "pg_migrations"}

	var index sql.NullString
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT to_regclass('session_summaries_updated')::text`).Scan(&index))
	require.False(t, index.Valid)

	for range 2 {
		down, err := set.ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 31)
		require.NoError(t, err)

		var definition string
		require.NoError(t, store.db.QueryRowContext(ctx, `SELECT pg_get_indexdef('session_summaries_updated'::regclass)`).Scan(&definition))
		require.Contains(t, definition, `(last_updated DESC, conversation_id COLLATE "C")`)

		n, err := set.ExecContext(ctx, store.db, "postgres", source, migrate.Up)
		require.NoError(t, err)
		require.Equal(t, down, n)
		require.NoError(t, store.db.QueryRowContext(ctx, `SELECT to_regclass('session_summaries_updated')::text`).Scan(&index))
		require.False(t, index.Valid)
	}
}

func TestMessageSearchMigrationSharesExtension(t *testing.T) {
	base, err := url.Parse(os.Getenv("ROCKETCLAW_TEST_DATABASE_URL"))
	require.NoError(t, err)
	cfg, err := pgx.ParseConfig(base.String())
	require.NoError(t, err)

	admin := stdlib.OpenDB(*cfg)

	t.Cleanup(func() { require.NoError(t, admin.Close()) })

	// A fresh database has no pg_trgm yet, so concurrent schemas really race to create it.
	name := "t_" + strings.ToLower(rand.Text())
	_, err = admin.ExecContext(t.Context(), `CREATE DATABASE `+name)
	require.NoError(t, err)

	// Cleanups run in reverse, so this drop follows every schema connection's close.
	t.Cleanup(func() {
		_, err := admin.ExecContext(context.Background(), `DROP DATABASE `+name+` WITH (FORCE)`)
		require.NoError(t, err)
	})

	base.Path = "/" + name
	t.Setenv("ROCKETCLAW_TEST_DATABASE_URL", base.String())

	newStore := func(dsn string) error {
		store, err := NewSessionServiceIn(t.Context(), &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
		if err != nil {
			return err
		}

		return store.Stop()
	}

	var callers errgroup.Group

	barriers := make([]*sql.Tx, 0, 2)

	for range 2 {
		dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
		require.NoError(t, err)
		cfg, err := pgx.ParseConfig(dsn)
		require.NoError(t, err)

		db := stdlib.OpenDB(*cfg)

		t.Cleanup(func() { require.NoError(t, db.Close()) })

		// Stop before 030, then hold the first caller after it creates the extension.
		_, err = (migrate.MigrationSet{TableName: "pg_migrations"}).ExecMaxContext(t.Context(), db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Up, 31)
		require.NoError(t, err)
		barrier, err := db.BeginTx(t.Context(), nil)
		require.NoError(t, err)

		t.Cleanup(func() { _ = barrier.Rollback() })

		_, err = barrier.ExecContext(t.Context(), `LOCK TABLE session_entries IN ACCESS EXCLUSIVE MODE`)
		require.NoError(t, err)

		barriers = append(barriers, barrier)

		callers.Go(func() error { return newStore(dsn) })
	}

	require.Eventually(t, func() bool {
		var waiting int

		err := admin.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid WHERE a.datname = $1 AND NOT l.granted`, name).Scan(&waiting)

		return err == nil && waiting == 2
	}, 5*time.Second, time.Millisecond)

	for _, barrier := range barriers {
		require.NoError(t, barrier.Rollback())
	}

	require.NoError(t, callers.Wait())

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	require.NoError(t, newStore(dsn))

	cfg, err = pgx.ParseConfig(dsn)
	require.NoError(t, err)

	db := stdlib.OpenDB(*cfg)

	t.Cleanup(func() { require.NoError(t, db.Close()) })

	schemas, err := queryStrings(t.Context(), db, `SELECT extnamespace::regnamespace::text FROM pg_extension WHERE extname = 'pg_trgm'`, "pg_trgm schema")
	require.NoError(t, err)
	require.Equal(t, []string{"public"}, schemas)
}

// The trigram indexes serve a search for a three-character term over saved entries and ended turns.
func TestMessageSearchSchema(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()
	_, err := store.db.ExecContext(ctx, `
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed) VALUES ('chat', 'main', '', 'web_chat', TRUE);
INSERT INTO entries (conversation_id, entry_json, entry_timestamp, search_text)
SELECT 'chat', '{}', '', '0 0 ["user","",' || to_json(md5(n::text)) || ',' || to_json(md5(n::text)) || ']' FROM generate_series(1, 4096) n;
INSERT INTO turns (id, conversation_id, inbound_json, output_trace_json, history_anchor_id, created_at_unix_ns, updated_at_unix_ns, phase, terminal, search_text)
SELECT 'turn-' || n, 'chat', '{}', '[]', n, 1, 1, 'done', 'stopped', '0 0 ["user","",' || to_json(md5(n::text)) || ',' || to_json(md5(n::text)) || ']' FROM generate_series(1, 4096) n;
ANALYZE entries;
ANALYZE turns;
-- Rows inserted since the migration wait in the indexes' pending lists, which a vacuum would merge.
SELECT gin_clean_pending_list('entries_search_text'::regclass), gin_clean_pending_list('turns_search_text'::regclass);
-- Tables this small are cheaper to scan; turning sequential scans off proves the trigram indexes serve the search.
SET enable_seqscan = off;`)
	require.NoError(t, err)

	plan, err := queryStrings(ctx, store.db, `EXPLAIN `+searchMessagesSQL, "message search plan", pgx.QueryExecModeExec, []string{"%abc%"})
	require.NoError(t, err)
	require.Contains(t, strings.Join(plan, "\n"), "entries_search_text")
	require.Contains(t, strings.Join(plan, "\n"), "turns_search_text")
}

func TestSessionMigrationUnlockFailureDiscardsConnection(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()

	var pid int
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
	// Shadow only this schema's unlock call to fail after successful migrations.
	_, err := store.db.ExecContext(ctx, `CREATE FUNCTION pg_advisory_unlock(integer, integer) RETURNS boolean LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'unlock failed'; END $$;
		SELECT set_config('search_path', current_schema() || ',pg_catalog', false)`)
	require.NoError(t, err)
	require.ErrorContains(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)), "unlock failed")

	var nextPID int
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&nextPID))
	require.NotEqual(t, pid, nextPID)
	require.Eventually(t, func() bool {
		var locks int

		err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_locks WHERE pid=$1`, pid).Scan(&locks)

		return err == nil && locks == 0
	}, 5*time.Second, time.Millisecond)

	_, err = store.db.ExecContext(ctx, `SELECT set_config('search_path', current_schema() || ',pg_catalog', false); DROP FUNCTION pg_advisory_unlock(integer, integer)`)
	require.NoError(t, err)
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))
}

// 049 keeps every hidden-run row: originals stay the run's context, the copy in the chat a run
// reports to moves to the run as visible-only, other copies stay their chats', and the buckets of
// originals and copies reconcile. Every conversation shows the same entries in the same order.
func TestHiddenRunEntriesMigration(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 47)
	require.NoError(t, err)

	_, err = store.db.ExecContext(ctx, `
INSERT INTO managed_conversations (conversation_id, agent, created_by, producer_inbound_json) VALUES
    ('chat-a', 'main', '', NULL), ('chat-b', 'main', '', NULL), ('web:cron:two', 'main', '', NULL),
    ('cron:one', 'job', 'cron', '{"SyncDestination":"chat-a"}'),
    ('cron:two', 'job', 'cron', '{"SyncDestination":"chat-a"}'),
    ('cron:none', 'job', 'cron', '{}');
INSERT INTO session_entries (id, conversation_id, entry_json, entry_timestamp) VALUES
    (1, 'cron:one', '{"type":"turn","replay_input":[{"type":"message","role":"user","content":"one"}]}', ''),
    (2, 'chat-a', '{"type":"turn","replay_input":[{"type":"message","role":"user","content":"hello"}]}', ''),
    (3, 'chat-a', '{"type":"turn","sync_source_entry_id":1,"sync_source_conversation_id":"cron:one","replay_input":[{"type":"message","role":"user","content":"one"}]}', ''),
    (4, 'cron:two', '{"type":"turn","replay_input":[{"type":"message","role":"user","content":"two"}]}', ''),
    (5, 'chat-a', '{"type":"turn","sync_source_entry_id":4,"replay_input":[{"type":"message","role":"user","content":"two"}]}', ''),
    (6, 'web:cron:two', '{"type":"turn","sync_source_entry_id":4,"sync_source_conversation_id":"cron:two","replay_input":[{"type":"message","role":"user","content":"two"}]}', ''),
    (7, 'cron:none', '{"type":"turn","replay_input":[{"type":"message","role":"user","content":"none"}]}', ''),
    (8, 'chat-b', '{"type":"turn","sync_source_entry_id":999,"replay_input":[{"type":"message","role":"user","content":"orphan"}]}', '');
SELECT setval(pg_get_serial_sequence('session_entries', 'id'), 8);`)
	require.NoError(t, err)

	var before string
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT string_agg(md5(entry_json::text), ',' ORDER BY id) FROM session_entries`).Scan(&before))
	backfillPreviousRelease(t, store.db, "pg_migrations")
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	var after string
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT string_agg(md5(entry_json::text), ',' ORDER BY id) FROM entries`).Scan(&after))
	require.Equal(t, before, after, "no entry is rewritten or dropped")

	rows, err := queryStrings(ctx, store.db, `SELECT id || ' ' || conversation_id || ' ' || audience FROM entries ORDER BY id`, "migrated entries")
	require.NoError(t, err)
	require.Equal(t, []string{
		"1 cron:one sandbox", "2 chat-a both", "3 cron:one visible", "4 cron:two sandbox",
		"5 cron:two visible", "6 web:cron:two both", "7 cron:none both", "8 chat-b visible",
	}, rows)

	buckets, err := queryStrings(ctx, store.db, `SELECT bucket || ' ' || originals || ' ' || copies FROM legacy_hidden_run_buckets ORDER BY bucket`, "buckets")
	require.NoError(t, err)
	require.Equal(t, []string{"copy only 0 1", "original only 1 0", "original with one copy 1 1", "original with several copies 1 2"}, buckets)

	for id, want := range map[string][]int64{"chat-a": {2, 3, 5}, "chat-b": {8}, "web:cron:two": {6}, "cron:one": {1}, "cron:two": {4}, "cron:none": {7}} {
		entries, err := store.ObserveEntries(ctx, id)
		require.NoError(t, err)

		ids := make([]int64, 0, len(entries))
		for i := range entries {
			ids = append(ids, entries[i].ID)
		}

		require.Equal(t, want, ids, id)
	}
}

// 049 turns hidden runs' pending schedule effects into live reminders of the chat each run reports
// to, or of the run itself: only effects after a run's last reset and after the ones already
// applied, with the reset clearing that chat's reminders, and a run's own reminders moving to its chat.
func TestHiddenRunPendingRemindersMigration(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 47)
	require.NoError(t, err)

	_, err = store.db.ExecContext(ctx, `
INSERT INTO managed_conversations (conversation_id, agent, created_by, producer_inbound_json, producer_effects_through_id) VALUES
    ('chat-a', 'main', '', NULL, 0), ('chat-b', 'helper', '', NULL, 0),
    ('cron:reset', 'job', 'cron', '{"SyncDestination":"chat-a"}', 0),
    ('cron:moved', 'job', 'cron', '{"SyncDestination":"chat-b"}', 0),
    ('cron:own', 'job', 'cron', '{}', 10);
INSERT INTO scheduled_messages (scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring, interval_ns) VALUES
    ('chat-old', 'chat-a', 'main', 'cleared by the reset', 1893456000000000000, 0, 0),
    ('run-owned', 'cron:moved', 'job', 'moved', 1893456000000000000, 0, 0);
INSERT INTO session_entries (id, conversation_id, entry_json, entry_timestamp) VALUES
    (9, 'cron:own', '{"type":"producer_schedule","output_trace":[{"message":"already applied","due_at":"2030-01-01T00:00:00Z"}]}', ''),
    (11, 'cron:reset', '{"type":"producer_schedule","output_trace":[{"message":"before the reset","due_at":"2030-01-01T00:00:00Z"}]}', ''),
    (12, 'cron:reset', '{"type":"producer_reset_schedules","output_trace":[{}]}', ''),
    (13, 'cron:reset', '{"type":"producer_schedule","output_trace":[{"message":"after the reset","due_at":"2030-01-01T00:00:00Z"}]}', ''),
    (14, 'cron:reset', '{"type":"producer_schedule","output_trace":[{"message":"hourly","due_at":"2030-01-01T01:00:00Z","recurring":true,"interval":3600000000000}]}', ''),
    (15, 'cron:own', '{"type":"producer_schedule","output_trace":[{"message":"own","due_at":"2030-01-01T00:00:00Z"}]}', ''),
    (21, 'chat-a', '{"type":"producer_schedule","sync_source_entry_id":11}', ''),
    (22, 'chat-a', '{"type":"producer_reset_schedules","sync_source_entry_id":12}', ''),
    (23, 'chat-a', '{"type":"producer_schedule","sync_source_entry_id":13}', ''),
    (24, 'chat-a', '{"type":"producer_schedule","sync_source_entry_id":14}', '');
SELECT setval(pg_get_serial_sequence('session_entries', 'id'), 24);`)
	require.NoError(t, err)
	backfillPreviousRelease(t, store.db, "pg_migrations")
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	reminders, err := store.ScheduledMessages()
	require.NoError(t, err)

	due := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	require.Equal(t, map[string]protocol.ScheduledMessageState{
		"producer-13": {ConversationID: "chat-a", Agent: "main", Message: "after the reset", DueAt: due},
		"producer-14": {ConversationID: "chat-a", Agent: "main", Message: "hourly", DueAt: due.Add(time.Hour), Recurring: true, Interval: time.Hour},
		"producer-15": {ConversationID: "cron:own", Agent: "job", Message: "own", DueAt: due},
		"run-owned":   {ConversationID: "chat-b", Agent: "helper", Message: "moved", DueAt: due},
	}, reminders)
}

// 049 refuses to run while a turn runs, a visible chat the previous release has not finished
// indexing, or an entry of a hidden run lacks its copy in the chat the run reports to, and the
// refusal commits nothing.
func TestHiddenRunEntriesMigrationRefuses(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, seed, want string }{
		{"running turn", `INSERT INTO active_turns (id, conversation_id, inbound_json, output_trace_json, created_at_unix_ns, updated_at_unix_ns) VALUES ('running', 'chat', '{}', '[]', 1, 1)`,
			"049_hidden_run_entries: 1 turns, Background Jobs, or shadow turns are unfinished"},
		{"unindexed chat", `INSERT INTO managed_conversations (conversation_id, agent, created_by, parent_conversation_id) VALUES ('unindexed', 'main', '', ''), ('cron:hidden', 'main', '', '')`,
			"049_hidden_run_entries: 1 visible chats are not fully in message_search"},
		{"run entry missing from its chat", `INSERT INTO managed_conversations (conversation_id, agent, created_by, parent_conversation_id, producer_inbound_json) VALUES ('chat', 'main', '', '', NULL), ('cron:run', 'job', 'cron', '', '{"SyncDestination":"chat"}');
INSERT INTO message_search_indexed (conversation_id) VALUES ('chat');
INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('cron:run', '{"type":"turn","replay_input":[]}', '')`,
			"049_hidden_run_entries: 1 entries of hidden runs have no copy in the chat they report to"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := newTestSessionService(t)
			ctx := t.Context()
			source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
			_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 47)
			require.NoError(t, err)
			_, err = store.db.ExecContext(ctx, tc.seed)
			require.NoError(t, err)

			require.ErrorContains(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)), tc.want)

			var last string
			require.NoError(t, store.db.QueryRowContext(ctx, `SELECT max(id) FROM pg_migrations`).Scan(&last))
			require.Equal(t, "048_shadow_step_limit.sql", last)

			var manifest sql.NullString
			require.NoError(t, store.db.QueryRowContext(ctx, `SELECT to_regclass('legacy_history')::text`).Scan(&manifest))
			require.False(t, manifest.Valid)
		})
	}
}

// 051 classifies every conversation once: recorded ones by today's ID shapes and anti-joins,
// with Web listing exactly the chats visibleChatsSQL did, and each history without a record as a
// hidden conversation. A finished cron run without a record becomes a hidden cron run whose chat
// shows the same messages in the same order (AE6).
func TestConversationsMigrationClassifiesConversations(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 47)
	require.NoError(t, err)

	const (
		run    = "cron:cron/daily.md:20261019T120000.123456789Z:RUN"
		oneOff = "one-off-cron:cron/once.md:20261019T130000.000000001Z:ONE"
		report = "slack-thread:C1:1700000000.000100"
		mcp    = "slack-thread:C2:1700000000.000200"
		resume = "web:" + run
	)

	_, err = store.db.ExecContext(ctx, strings.NewReplacer("$1", report, "$2", mcp, "$3", resume, "$4", run, "$5", oneOff).Replace(`
INSERT INTO managed_conversations (conversation_id, agent, created_by, parent_conversation_id) VALUES
    ('chat', 'main', 'alice', ''), ('$1', 'main', 'cron', ''), ('$2', 'main', '', ''), ('$3', 'main', 'alice', ''),
    ('chat/sub', 'helper', '', 'chat'), ('gone/sub', 'helper', '', 'gone'), ('SHADOW', 'critic', '', '');
INSERT INTO shadow_bindings (child_conversation_id, parent_conversation_id, parent_agent, shadow_agent) VALUES ('SHADOW', 'chat', 'main', 'critic');
INSERT INTO session_tags (conversation_id, tags) VALUES ('cron:tagged', '["kept"]'), ('web-tagged', '["kept"]');
INSERT INTO external_mcp_sessions (external_conversation_id, private_conversation_id, managed_conversation_id, agent, slack_channel) VALUES
    ('public', 'external_mcp:main:PRIVATE', '$2', 'main', '#ops');
INSERT INTO session_entries (id, conversation_id, entry_json, entry_timestamp) VALUES
    (1, 'chat', '{"type":"turn","turn_id":"turn-1","replay_input":[{"type":"message","role":"user","content":"hello"},{"type":"function_call","call_id":"sub"},{"type":"function_call","call_id":"call_1"},{"type":"function_call","call_id":"call_2"}]}', ''),
    (2, '$4', '{"type":"turn","replay_input":[{"type":"message","role":"user","content":"report"}]}', ''),
    (3, '$1', '{"type":"turn","sync_source_entry_id":2,"sync_source_conversation_id":"$4","replay_input":[{"type":"message","role":"user","content":"report"}]}', ''),
    (4, '$3', '{"type":"turn","sync_source_entry_id":2,"sync_source_conversation_id":"$4","replay_input":[{"type":"message","role":"user","content":"report"}]}', ''),
    (5, 'chat/call_1-review-999678fc', '{"type":"guardrail"}', ''),
    (6, 'chat/call_2', '{"type":"guardrail"}', ''),
    (7, '$5', '{"type":"turn"}', ''),
    (8, 'gone/sub', '{"type":"turn"}', ''),
    (9, 'external_mcp:main:PRIVATE', '{"type":"turn"}', '');
SELECT setval(pg_get_serial_sequence('session_entries', 'id'), 9);`))
	require.NoError(t, err)
	backfillPreviousRelease(t, store.db, "pg_migrations")
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	rows, err := queryStrings(ctx, store.db, `SELECT concat_ws(' ', conversation_id, kind, CASE WHEN listed THEN 'listed' ELSE 'hidden' END, retention, parent_conversation_id, cron_path, cron_run_at_unix_ns, slack_channel, slack_thread, cron_run, call_turn, call_id)
FROM conversations ORDER BY conversation_id COLLATE "C"`, "conversations")
	require.NoError(t, err)
	require.Equal(t, []string{
		"SHADOW shadow hidden record",
		"chat web_chat listed record",
		"chat/call_1-review-999678fc review hidden nested chat turn-1 call_1",
		"chat/call_2 subagent hidden nested chat call_2",
		"chat/sub subagent hidden record chat sub",
		run + " cron_run hidden private cron/daily.md 1792411200123456789",
		"cron:tagged cron_run hidden private",
		"external_mcp:main:PRIVATE external_mcp_run hidden private",
		"gone/sub subagent listed record",
		oneOff + " one_off_cron_run hidden private cron/once.md 1792414800000000001",
		report + " slack_thread listed record C1 1700000000.000100",
		mcp + " slack_thread listed record C2 1700000000.000200",
		resume + " web_chat listed record " + run,
	}, rows)

	var listed []string

	for row, err := range store.sidebarSessions(ctx, nil) {
		require.NoError(t, err)

		listed = append(listed, row.Conversation.ID)
	}

	require.ElementsMatch(t, []string{"chat", "gone/sub", report, resume}, listed)

	entries, err := store.ObserveEntries(ctx, report)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, int64(3), entries[0].ID)
	require.Equal(t, run, entries[0].SourceConversationID)

	for facts, err := range store.ChatOriginFacts(ctx, report) {
		require.NoError(t, err)

		_, text := DecideOrigin(&facts)
		require.Equal(t, strings.ToLower("Cron Source: cron/daily.md Stem: daily Run kind: scheduled Run ID: "+run+" Agent:  Ran at: 2026-10-19T12:00:00.123456789Z"), text)
	}

	// The cron list keeps what Web listed when it parsed recorded IDs: the chats holding 051's
	// unrecorded runs, not those runs, and a later run with its recorded subagent sessions, each
	// located at the run, without the review, which has no record.
	later, at := "cron:cron/loop.md:20261020T010000.000000002Z:LATER", time.Date(2026, 10, 20, 1, 0, 0, 2, time.UTC)
	require.NoError(t, (&Runtime{Sessions: store}).CreateConversation(ctx, protocol.Conversation{ID: later, Agent: "job", CreatedBy: "cron", Run: &protocol.CronRun{Path: "cron/loop.md", At: at}}))
	require.NoError(t, store.startSubagent(ctx, later, harness.ChildKey{Path: "/call-1", Turn: "turn-1", Call: "call-1"}, "helper", true))
	require.NoError(t, store.startSubagent(ctx, later+"/call-1", harness.ChildKey{Path: "/call-2", Turn: "turn-2", Call: "call-2"}, "helper", true))
	require.NoError(t, (&Bridge{config: Config{ConversationID: later + "/call-1", SessionService: store}}).AppendChildEntry(ctx, harness.ChildKey{Path: "/call-3-review-0a1b2c3d", Turn: "turn-3", Call: "call-3"}, testSessionEntry("review", "allowed")))

	for _, id := range []string{later, later + "/call-1", later + "/call-1/call-2"} {
		_, err := store.AppendEntryID(ctx, id, testSessionEntry("work", "done"))
		require.NoError(t, err)
	}

	sources, err := store.CronRunSources(ctx)
	require.NoError(t, err)

	daily, loop := protocol.CronRun{Path: "cron/daily.md", At: time.Date(2026, 10, 19, 12, 0, 0, 123456789, time.UTC)}, protocol.CronRun{Path: "cron/loop.md", At: at}
	require.ElementsMatch(t, []CronRunSource{
		{ConversationID: report, SourceConversationID: run, Run: daily}, {ConversationID: resume, SourceConversationID: run, Run: daily},
		{ConversationID: later, SourceConversationID: later, Run: loop}, {ConversationID: later + "/call-1", SourceConversationID: later + "/call-1", Run: loop},
		{ConversationID: later + "/call-1/call-2", SourceConversationID: later + "/call-1/call-2", Run: loop},
	}, sources)
}

// 051 refuses a history no rule classifies, or a delegation no call of its parent started, and the
// refusal commits nothing.
func TestConversationsMigrationRefusesUnclassifiedHistory(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ histories, refusal string }{
		{`('mystery id', '{}', '')`, "051_conversations: 1 histories match no conversation kind, such as mystery id"},
		{`('web:chat', '{"turn_id":"turn-1","replay_input":[{"type":"function_call","call_id":"call_1"}]}', ''), ('web:chat/call_2', '{}', '')`,
			"051_conversations: 1 delegations match no call of their parent, such as web:chat/call_2"},
	} {
		store := newTestSessionService(t)
		ctx := t.Context()
		source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
		_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 50)
		require.NoError(t, err)
		_, err = store.db.ExecContext(ctx, `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES `+tc.histories)
		require.NoError(t, err)

		require.ErrorContains(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)), tc.refusal)

		var last string
		require.NoError(t, store.db.QueryRowContext(ctx, `SELECT max(id) FROM pg_migrations`).Scan(&last))
		require.Equal(t, "050_conversation_continues.sql", last)
	}
}

// An External MCP call for an existing external ID finds its binding, with the Slack thread it is
// on, in one row through the external ID's unique index.
func TestExternalMCPBindingLookupUsesOneIndexedRow(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()
	thread := protocol.SlackReplyTarget{ChannelID: "C1", ThreadTS: "1.1"}
	require.NoError(t, store.RegisterExternalMCPConversation("ticket", "main", &ExternalMCPSessionState{Thread: thread, Agent: "job", PrivateConversationID: "private", ManagedConversationID: protocol.SlackThreadConversationID("C1", "1.1"), SlackChannel: "#ops"}))
	_, err := store.db.ExecContext(ctx, `
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed)
SELECT 'chat-' || n, 'main', '', 'web_chat', TRUE FROM generate_series(1, 4096) n;
ANALYZE conversations;
SET plan_cache_mode = force_generic_plan;`)
	require.NoError(t, err)

	session, found, err := store.ExternalMCPSession("ticket")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, ExternalMCPSessionState{Agent: "job", PrivateConversationID: "private", ManagedConversationID: protocol.SlackThreadConversationID("C1", "1.1"), SlackChannel: "#ops", Thread: thread}, session)

	// Explain the actual prepared lookup, not a separately maintained query.
	queries, err := queryStrings(ctx, store.db, `SELECT format('EXPLAIN EXECUTE %I(%s)', name,
    array_to_string(array_fill('NULL'::text, ARRAY[cardinality(parameter_types)]), ','))
FROM pg_prepared_statements WHERE statement = $1`, "binding lookup statement", externalMCPSessionSQL+` AND external_id = $1`)
	require.NoError(t, err)
	require.Len(t, queries, 1)
	plan, err := queryStrings(ctx, store.db, queries[0], "binding lookup plan")
	require.NoError(t, err)
	require.Contains(t, strings.Join(plan, "\n"), "conversations_external_id_key")
	require.NotContains(t, strings.Join(plan, "\n"), "Seq Scan")
	t.Log(strings.Join(plan, "\n"))
}

// 052 moves each side table's rows onto the conversations they describe and leaves the side
// tables empty; facts of an ID without a conversation stay as they were. Remote OpenCode
// sessions become conversations of their own kind, and shadow turns stay with their shadows.
func TestConversationFactsMigration(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 52)
	require.NoError(t, err)

	_, err = store.db.ExecContext(ctx, `
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, slack_channel, slack_thread) VALUES
    ('chat', 'main', 'alice', 'web_chat', TRUE, NULL, NULL), ('slack-thread:C1:1.1', 'main', '', 'slack_thread', TRUE, 'C1', '1.1'),
    ('external_mcp:job:RUN', 'job', '', 'external_mcp_run', FALSE, NULL, NULL), ('SHADOW', 'critic', '', 'shadow', FALSE, NULL, NULL);
INSERT INTO session_summaries (conversation_id, preview, last_updated) VALUES ('chat', 'hello', '2026-10-19Z'), ('ghost', 'gone', '2026-10-18Z');
INSERT INTO session_tags (conversation_id, tags) VALUES ('chat', '["a"]'), ('ghost', '["b"]');
INSERT INTO conversation_goals (conversation_id, objective, check_script, max_turns, turns_used, status, note, created_at_unix_ns, updated_at_unix_ns, principal_name, principal_email) VALUES
    ('chat', 'ship', 'check.sh', 5, 2, 'active', 'going', 1, 2, 'Alice', 'alice@example.com');
INSERT INTO external_mcp_sessions (external_conversation_id, private_conversation_id, managed_conversation_id, agent, slack_channel, origin_pairs) VALUES
    ('ticket', 'external_mcp:job:RUN', 'slack-thread:C1:1.1', 'job', '#ops', '{"Ticket":"T-1"}');
INSERT INTO shadow_bindings (child_conversation_id, parent_conversation_id, parent_agent, shadow_agent) VALUES ('SHADOW', 'chat', 'main', 'critic');
INSERT INTO shadow_turns (child_conversation_id, parent_source_conversation_id, parent_turn_id, inbound_json, terminal) VALUES ('SHADOW', 'chat', 'T', '{}', 'complete');
INSERT INTO opencode_sessions (conversation_id, server, title, directory, updated_at, running, synced_at, pinned, name, reply) VALUES
    ('opencode:box:ses_a', 'box', 'Title', '/srv', '2026-10-17Z', TRUE, '2026-10-19Z', TRUE, 'Named', 'done');`)
	require.NoError(t, err)
	migrateBeforeHandoff(t, store.db)

	for _, table := range []string{"session_summaries", "session_tags", "conversation_goals", "external_mcp_sessions", "shadow_bindings", "opencode_sessions"} {
		var rows int
		require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&rows))
		require.Zero(t, rows, table)
	}

	facts, err := queryStrings(ctx, store.db, `SELECT concat_ws('|', conversation_id, kind, retention, convert_from(preview, 'UTF8'), to_char(last_updated AT TIME ZONE 'UTC', 'YYYY-MM-DD'),
    tags::text, goal_objective, goal_check_script, goal_max_turns, goal_turns_used, goal_status, goal_note, goal_created_at_unix_ns, goal_updated_at_unix_ns,
    goal_principal_name, goal_principal_email, external_id, external_run, external_agent, external_channel, origin_pairs::text, shadow_parent, parent_agent, shadow_agent,
    server, directory, title, running, to_char(synced_at AT TIME ZONE 'UTC', 'YYYY-MM-DD'), pinned, name, reply)
FROM conversations ORDER BY conversation_id COLLATE "C"`, "conversation facts")
	require.NoError(t, err)
	require.Equal(t, []string{
		"SHADOW|shadow|record|chat|main|critic|f|f|",
		`chat|web_chat|record|hello|2026-10-19|["a"]|ship|check.sh|5|2|active|going|1|2|Alice|alice@example.com|f|f|`,
		"external_mcp:job:RUN|external_mcp_run|record|f|f|",
		"opencode:box:ses_a|remote|remote|2026-10-17|box|/srv|Title|t|2026-10-19|t|Named|done",
		`slack-thread:C1:1.1|slack_thread|record|ticket|external_mcp:job:RUN|job|#ops|{"Ticket": "T-1"}|f|f|`,
	}, facts)

	unmoved, err := queryStrings(ctx, store.db, `SELECT source || ' ' || conversation_id FROM legacy_unmoved_facts ORDER BY 1`, "unmoved facts")
	require.NoError(t, err)
	require.Equal(t, []string{"session_summaries ghost", "session_tags ghost"}, unmoved)

	parent, err := store.ShadowParent(ctx, "SHADOW")
	require.NoError(t, err)
	require.Equal(t, "chat", parent)

	var turns int
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) FROM shadow_turns WHERE child_conversation_id = 'SHADOW'`).Scan(&turns))
	require.Equal(t, 1, turns)

	binding, found, err := store.ExternalMCPSession("ticket")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, ExternalMCPSessionState{Agent: "job", PrivateConversationID: "external_mcp:job:RUN", ManagedConversationID: "slack-thread:C1:1.1", SlackChannel: "#ops",
		OriginPairs: map[string]string{"Ticket": "T-1"}, Thread: protocol.SlackReplyTarget{ChannelID: "C1", ThreadTS: "1.1"}}, binding)
}

// 052 aborts when a moved value differs from its source, and the abort commits nothing.
func TestConversationFactsMigrationRefusesMismatch(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 52)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed) VALUES ('chat', 'main', '', 'web_chat', TRUE);
INSERT INTO session_tags (conversation_id, tags) VALUES ('chat', '["a"]');
-- The body compiles at its first call, once 052 has added the tags column.
CREATE FUNCTION tamper_tags() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.tags IS NOT NULL THEN NEW.tags := '["tampered"]'; END IF; RETURN NEW; END $$;
CREATE TRIGGER tamper_tags BEFORE UPDATE ON conversations FOR EACH ROW EXECUTE FUNCTION tamper_tags();`)
	require.NoError(t, err)

	require.ErrorContains(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)), "052_conversation_facts: 2 rows differ from their source tables")

	var last string
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT max(id) FROM pg_migrations`).Scan(&last))
	require.Equal(t, "051_conversations.sql", last)

	var tags int
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) FROM session_tags`).Scan(&tags))
	require.Equal(t, 1, tags)
}

// legacyTurnStepKeys lists conversationID's turn steps in key order before 054 made them steps.
func legacyTurnStepKeys(t *testing.T, store *SessionService, conversationID string) []string {
	t.Helper()

	keys, err := queryStrings(t.Context(), store.db, `SELECT key FROM turn_steps WHERE conversation_id = $1 ORDER BY key`, "turn step keys", conversationID)
	require.NoError(t, err)

	return keys
}

// turnItemsMigrationStep is a stopped turn's step as RocketCode saved it whole before 053.
const turnItemsMigrationStep = `{"applied_input_ids":["in-1"],"record":{"version":1,"type":"turn","turn_id":"stopped","response_id":"resp-1",` +
	`"replay_input":[{"type":"message","role":"user","input_id":"in-1","content":"question \u003cb\u003e"},{"type":"message","role":"assistant","content":"partial"}],` +
	`"output_trace":[{"type":"web_search_call"}]},"trace":[{"type":"rocketcode_public_progress"}],"response":{"id":"resp-1"}}`

// seedTurnItemsMigration migrates store down to before 053 and saves a stopped turn's whole step there.
func seedTurnItemsMigration(t *testing.T, store *SessionService) {
	t.Helper()

	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(t.Context(), store.db, "postgres", source, migrate.Down, 53)
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed) VALUES ('chat', 'main', '', 'web_chat', TRUE);
INSERT INTO active_turns (id, conversation_id, inbound_json, output_trace_json, created_at_unix_ns, updated_at_unix_ns, history_anchor_id, phase, terminal)
VALUES ('stopped', 'chat', '{}', '[]', 1, 1, 0, 'done', 'stopped')`)
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `INSERT INTO turn_steps (conversation_id, key, value) VALUES ('chat', 'stopped', $1), ('chat', 'stopped/call/c1', '{"output":[]}')`, turnItemsMigrationStep)
	require.NoError(t, err)
}

// 053 makes each item of a saved turn a step of its own with its bytes, its step keeps the rest, History
// shows the stopped turn as before, and Down puts the whole step back.
func TestTurnItemsMigration(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	seedTurnItemsMigration(t, store)

	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", source, migrate.Up, 53)
	require.NoError(t, err)

	require.Equal(t, []string{"stopped", "stopped/call/c1", "stopped/output_trace/0000000000", "stopped/replay_input/0000000000", "stopped/replay_input/0000000001", "stopped/trace/0000000000"}, legacyTurnStepKeys(t, store, "chat"))

	var item, head string
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT value::text FROM turn_steps WHERE key = 'stopped/replay_input/0000000000'`).Scan(&item))
	require.JSONEq(t, `{"type":"message","role":"user","input_id":"in-1","content":"question <b>"}`, item)
	require.Contains(t, item, `"question \u003cb\u003e"`, "the item keeps its bytes")
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT value::text FROM turn_steps WHERE key = 'stopped'`).Scan(&head))
	require.JSONEq(t, `{"applied_input_ids":["in-1"],"record":{"version":1,"type":"turn","turn_id":"stopped","response_id":"resp-1"},"response":{"id":"resp-1"}}`, head)

	migrateBeforeHandoff(t, store.db)
	entries, err := store.ObserveTranscript(ctx, "chat", 0, 0, nil)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, protocol.TerminalStopped, entries[0].Terminal)
	require.Equal(t, []string{`{"type":"message","role":"user","input_id":"in-1","content":"question \u003cb\u003e"}`, `{"type":"message","role":"assistant","content":"partial"}`},
		[]string{string(entries[0].Entry.ReplayInput[0]), string(entries[0].Entry.ReplayInput[1])})

	_, err = (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 53)
	require.NoError(t, err)
	require.Equal(t, []string{"stopped", "stopped/call/c1"}, legacyTurnStepKeys(t, store, "chat"))
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT value::text FROM turn_steps WHERE key = 'stopped'`).Scan(&head))
	require.JSONEq(t, turnItemsMigrationStep, head)
}

// 053 aborts when an item differs from its source step, and the abort commits nothing.
func TestTurnItemsMigrationRefusesMismatch(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	seedTurnItemsMigration(t, store)
	_, err := store.db.ExecContext(ctx, `CREATE FUNCTION tamper_item() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.value := '{}'; RETURN NEW; END $$;
CREATE TRIGGER tamper_item BEFORE INSERT ON turn_steps FOR EACH ROW WHEN (NEW.key = 'stopped/replay_input/0000000001') EXECUTE FUNCTION tamper_item();`)
	require.NoError(t, err)

	require.ErrorContains(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)), "053_turn_items: 2 turn step values differ from their source steps")

	var last string
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT max(id) FROM pg_migrations`).Scan(&last))
	require.Equal(t, "052_conversation_facts.sql", last)
	require.Equal(t, []string{"stopped", "stopped/call/c1"}, legacyTurnStepKeys(t, store, "chat"))
}

// seedEntriesStepsMigration migrates store down to before 054 and saves there a chat's entries with,
// between them, a stopped and a failed turn known only by their steps, and a human-stopped turn whose
// prompt was saved, and a running turn with a Code Mode record in another chat.
func seedEntriesStepsMigration(t *testing.T, store *SessionService) {
	t.Helper()

	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(t.Context(), store.db, "postgres", source, migrate.Down, 54)
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed) VALUES ('chat', 'main', '', 'web_chat', TRUE), ('live', 'main', '', 'web_chat', TRUE);
INSERT INTO session_entries (id, conversation_id, entry_json, entry_timestamp) VALUES
    (101, 'chat', '{"version":1,"type":"turn","turn_id":"first","timestamp":"2026-10-19T00:00:01Z","replay_input":[{"type":"message","role":"user","content":"first"}]}', '2026-10-19T00:00:01Z'),
    (102, 'chat', '{"version":1,"type":"turn","turn_id":"second","timestamp":"2026-10-19T00:00:02Z","replay_input":[{"type":"message","role":"user","content":"second"}]}', '2026-10-19T00:00:02Z'),
    (103, 'chat', '{"version":1,"type":"turn","turn_id":"kept","timestamp":"2026-10-19T00:00:03Z","replay_input":[{"type":"message","role":"user","input_id":"k1","content":"kept prompt"}]}', '2026-10-19T00:00:03Z');
INSERT INTO active_turns (id, conversation_id, inbound_json, output_trace_json, created_at_unix_ns, updated_at_unix_ns, history_anchor_id, phase, terminal) VALUES
    ('stopped', 'chat', '{}', '[{"type":"web_search_call"}]', 1, 1, 101, 'done', 'stopped'),
    ('failed', 'chat', '{}', '[]', 2, 2, 101, 'done', 'failed'),
    ('kept', 'chat', '{"Human":true}', '[]', 3, 3, 102, 'done', 'stopped'),
    ('live', 'live', '{}', '[]', 4, 4, 0, 'running', '');
INSERT INTO turn_steps (conversation_id, key, value) VALUES
    ('chat', 'stopped', '{"applied_input_ids":["s1"],"record":{"version":1,"type":"turn","turn_id":"stopped","response_id":"resp-1"},"response":{"id":"resp-1"}}'),
    ('chat', 'stopped/replay_input/0000000000', '{"type":"message","role":"user","input_id":"s1","content":"stopped question \u003cb\u003e"}'),
    ('chat', 'stopped/replay_input/0000000001', '{"type":"message","role":"assistant","content":"partial"}'),
    ('chat', 'failed', '{"applied_input_ids":["q1"],"record":{"version":1,"type":"turn","turn_id":"failed"}}'),
    ('chat', 'failed/replay_input/0000000000', '{"type":"message","role":"user","input_id":"q1","content":"failed question"}'),
    ('chat', 'kept', '{"applied_input_ids":["k1"],"record":{"version":1,"type":"turn","turn_id":"kept"}}'),
    ('chat', 'kept/replay_input/0000000000', '{"type":"message","role":"user","input_id":"k1","content":"kept prompt"}'),
    ('live', 'live', '{"record":{"version":1,"type":"turn","turn_id":"live"}}'),
    ('live', 'live/replay_input/0000000000', '{"type":"message","role":"user","content":"live question"}');
INSERT INTO code_mode_calls (turn_id, key, record) VALUES
    ('stopped', 'stopped/call/exec/host/0', '{"type":"rocketcode_code_mode_call","parent_call_id":"exec","key":"stopped/call/exec/host/0","name":"bash","status":"ok"}'),
    ('live', 'live/call/exec/host/0', '{"type":"rocketcode_code_mode_call","parent_call_id":"exec","key":"live/call/exec/host/0","name":"bash","status":"running"}');`)
	require.NoError(t, err)
}

// 054 moves each stopped or failed turn's record and applied inputs onto its turn row and leaves no
// step of an ended turn: History shows each ended turn in its place and a saved prompt once, its
// consumed input is not queued to run again, rocketclaw_sessions_get pages as before, and Down puts
// the steps back.
func TestEntriesStepsMigration(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	seedEntriesStepsMigration(t, store)
	migrateBeforeHandoff(t, store.db)

	require.Empty(t, testTurnStepKeys(t, store, "chat"), "no step of an ended turn survives")
	require.Equal(t, []string{"live", "live/code_mode/live/call/exec/host/0", "live/replay_input/0000000000"}, testTurnStepKeys(t, store, "live"))
	calls, err := conversationJournal{store: store, conversationID: "live", log: slog.New(slog.DiscardHandler)}.CodeModeCalls(ctx, "live")
	require.NoError(t, err)
	require.Equal(t, []string{`{"type":"rocketcode_code_mode_call","parent_call_id":"exec","key":"live/call/exec/host/0","name":"bash","status":"running"}`}, []string{string(calls[0])})

	transcript, err := store.ObserveTranscript(ctx, "chat", 0, 0, nil)
	require.NoError(t, err)

	keys := make([]string, 0, len(transcript))
	for i := range transcript {
		keys = append(keys, transcript[i].Key)
	}

	require.Equal(t, []string{"turn:chat:first", "turn:chat:stopped", "turn:chat:failed", "turn:chat:second", "turn:chat:kept"}, keys, "each ended turn keeps its place, and a saved prompt shows once")

	stopped := transcript[1]
	require.Zero(t, stopped.ID)
	require.Equal(t, protocol.TerminalStopped, stopped.Terminal)
	require.Equal(t, "resp-1", stopped.Entry.ResponseID)
	require.Equal(t, []string{`{"type":"message","role":"user","input_id":"s1","content":"stopped question \u003cb\u003e"}`, `{"type":"message","role":"assistant","content":"partial"}`},
		[]string{string(stopped.Entry.ReplayInput[0]), string(stopped.Entry.ReplayInput[1])}, "items keep their bytes")
	require.Len(t, stopped.Entry.OutputTrace, 2)
	require.Equal(t, "stopped/call/exec/host/0", harness.CodeModeCallsFromTrace(stopped.Entry.OutputTrace)[0].Key, "the trace keeps its Code Mode record")
	require.Equal(t, protocol.TerminalFailed, transcript[2].Terminal)
	require.Zero(t, transcript[2].ID)
	require.Equal(t, protocol.TerminalStopped, transcript[4].Terminal, "the saved prompt shows as stopped")

	var applied string
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT applied_input_ids::text FROM turns WHERE id = 'failed'`).Scan(&applied))
	require.JSONEq(t, `["q1"]`, applied)

	// The failed turn's consumed input stays recorded, so Undo does not queue it to run again.
	b := &Bridge{config: Config{ConversationID: "chat", SessionService: store}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	request := bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "failed question", true), queueItemID: "q1"}

	b.mu.Lock()
	err = b.preserveRevertRequestLocked(ctx, &request)
	b.mu.Unlock()
	require.NoError(t, err)
	require.Empty(t, b.waiting)

	queue, err := store.ThreadQueueForConversation("chat")
	require.NoError(t, err)
	require.Empty(t, queue)

	get := getSessionTool(store)
	for before, want := range map[int64]string{
		0:   "timestamp\trole\tcontent\n2026-10-19T00:00:02Z\tuser\tsecond\n2026-10-19T00:00:03Z\tuser\tkept prompt\n[next_before_entry_id=102]\n",
		102: "timestamp\trole\tcontent\n2026-10-19T00:00:01Z\tuser\tfirst\n",
	} {
		page, err := get.Call(ctx, json.RawMessage(`{"conversation_id":"chat","limit":2,"before_entry_id":`+strconv.FormatInt(before, 10)+`}`), nil)
		require.NoError(t, err)
		require.Equal(t, want, page.Output, before)
	}

	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	_, err = (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 54)
	require.NoError(t, err)
	require.Equal(t, []string{"failed", "failed/replay_input/0000000000", "kept", "kept/replay_input/0000000000", "stopped", "stopped/replay_input/0000000000", "stopped/replay_input/0000000001"}, legacyTurnStepKeys(t, store, "chat"))

	var head string
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT value::text FROM turn_steps WHERE key = 'stopped'`).Scan(&head))
	require.JSONEq(t, `{"applied_input_ids":["s1"],"record":{"version":1,"type":"turn","turn_id":"stopped","response_id":"resp-1"},"response":{"id":"resp-1"}}`, head)
	require.Equal(t, []string{"live", "live/replay_input/0000000000"}, legacyTurnStepKeys(t, store, "live"))

	records, err := queryStrings(ctx, store.db, `SELECT key FROM code_mode_calls ORDER BY id`, "code mode calls")
	require.NoError(t, err)
	require.Equal(t, []string{"stopped/call/exec/host/0", "live/call/exec/host/0"}, records)
}

// 054 aborts when a moved record differs from its source steps, and the abort commits nothing.
func TestEntriesStepsMigrationRefusesMismatch(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	seedEntriesStepsMigration(t, store)
	_, err := store.db.ExecContext(ctx, `CREATE FUNCTION tamper_record() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.record := '{"replay_input":[]}'; RETURN NEW; END $$;
CREATE TRIGGER tamper_record BEFORE UPDATE ON active_turns FOR EACH ROW WHEN (NEW.id = 'failed') EXECUTE FUNCTION tamper_record();`)
	require.NoError(t, err)

	require.ErrorContains(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)), "054_entries_steps: 4 moved records or steps differ from their source")

	var last string
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT max(id) FROM pg_migrations`).Scan(&last))
	require.Equal(t, "053_turn_items.sql", last)
	require.Equal(t, []string{"failed", "failed/replay_input/0000000000", "kept", "kept/replay_input/0000000000", "stopped", "stopped/replay_input/0000000000", "stopped/replay_input/0000000001"}, legacyTurnStepKeys(t, store, "chat"))
}

// backfillPreviousRelease migrates db up to 047 and marks every conversation's message search complete
// there, as the previous release's startup backfill does before 049 can run, and returns how many
// migrations it applied.
func backfillPreviousRelease(t *testing.T, db *sql.DB, ledger string) int {
	t.Helper()

	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	applied, err := (migrate.MigrationSet{TableName: ledger}).ExecVersionContext(t.Context(), db, "postgres", source, migrate.Up, 47)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO message_search_indexed (conversation_id) SELECT conversation_id FROM managed_conversations ON CONFLICT DO NOTHING`)
	require.NoError(t, err)

	return applied
}

// migrateBeforeHandoff migrates db up to 058. A test that seeds rows after 049 wrote its manifest
// stops there: 059 checks the manifest again and refuses rows it never recorded.
func migrateBeforeHandoff(t *testing.T, db *sql.DB) {
	t.Helper()

	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(t.Context(), db, "postgres", source, migrate.Up, 58)
	require.NoError(t, err)
}

// entrySearchUserItem is a Web prompt whose text holds what JSON and lines escape: quotes, a
// backslash, HTML, a line and paragraph separator, a newline, and a tab.
const entrySearchUserItem = `{"type":"message","role":"user","prompt_header":"[Web principal=\"Ann <a&b>\"]","content":"[Web principal=\"Ann <a&b>\"]\n\nSay \"hi\" \\ <b>&amp; \u2028\u2029 x\nnext\tTAB ÀB"}`

const entrySearchUserText = "Say \"hi\" \\ <b>&amp; \u2028\u2029 x\nnext\tTAB ÀB"

// seedEntrySearchMigration migrates store down to before 055 and saves there what the previous
// release indexed: a chat's entry, a compacted entry of the run reporting to it, and a stopped turn,
// an entry the run saved in its own view before it reported to the chat, and a review's entry, which
// search never shows.
func seedEntrySearchMigration(t *testing.T, store *SessionService) {
	t.Helper()

	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(t.Context(), store.db, "postgres", source, migrate.Down, 55)
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed) VALUES ('chat', 'main', '', 'web_chat', TRUE);
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, reports_to) VALUES ('run', 'job', '', 'cron_run', FALSE, 'chat'), ('review', 'main', '', 'review', FALSE, NULL);
INSERT INTO entries (id, conversation_id, entry_json, entry_timestamp) VALUES
    (301, 'chat', '{"replay_input":[`+entrySearchUserItem+`,{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"First"},{"type":"output_text","text":" "},{"type":"output_text","text":"Third"}]}]}', ''),
    (302, 'run', '{"replay_input":[{"type":"compaction","id":"cmp_1","encrypted_content":"sealed"},{"type":"message","role":"user","content":"run report"}]}', ''),
    (303, 'run', '{"replay_input":[{"type":"compaction","id":"cmp_2","encrypted_content":"sealed"},{"type":"message","role":"user","content":"early run text"}]}', ''),
    (304, 'review', '{"replay_input":[{"type":"message","role":"user","content":"review text"}]}', '');
INSERT INTO active_turns (id, conversation_id, inbound_json, output_trace_json, history_anchor_id, created_at_unix_ns, updated_at_unix_ns, phase, terminal, replay, record)
VALUES ('stopped', 'chat', '{}', '[]', 302, 1, 1, 'done', 'stopped', '[{"type":"message","role":"user","content":"stopped ask"}]', '{}');
INSERT INTO message_search_indexed (conversation_id) VALUES ('chat');`)
	require.NoError(t, err)
	_, err = store.db.ExecContext(t.Context(), `INSERT INTO message_search (conversation_id, entry_id, turn_id, replay_index, part_index, role, text, text_lower, principal) VALUES
    ('chat', 301, NULL, 0, 0, 'user', $1, $2, 'Ann <a&b>'),
    ('chat', 301, NULL, 1, 0, 'assistant', 'First', 'first', ''),
    ('chat', 301, NULL, 1, 2, 'assistant', 'Third', 'third', ''),
    ('chat', 302, NULL, 0, 0, 'user', 'run report', 'run report', ''),
    ('chat', NULL, 'stopped', 0, 0, 'user', 'stopped ask', 'stopped ask', ''),
    ('run', 303, NULL, 1, 0, 'user', 'early run text', 'early run text', 'Early'),
    ('review', 304, NULL, 0, 0, 'user', 'review text', 'review text', 'Reviewer')`, entrySearchUserText, strings.ToLower(entrySearchUserText))
	require.NoError(t, err)
}

// 055 moves each message_search row into its entry's or turn's search text, as Go writes it for a new
// entry, so search finds old messages with their message IDs and principals; Down hands search back
// to the previous release's backfill.
func TestEntrySearchMigration(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	seedEntrySearchMigration(t, store)
	migrateBeforeHandoff(t, store.db)

	for needle, want := range map[string][]MessageSearchHit{
		"\"hi\" \\ <b>&amp; \u2028\u2029 x\nnext\ttab àb": {{ConversationID: "chat", Role: "user", Text: entrySearchUserText, MessageID: "301:0"}},
		"ir":      {{ConversationID: "chat", Role: "assistant", Text: "First", MessageID: "301:1"}, {ConversationID: "chat", Role: "assistant", Text: "Third", MessageID: "301:1"}},
		"report":  {{ConversationID: "chat", Role: "user", Text: "run report", MessageID: "302:0"}},
		"stopped": {{ConversationID: "chat", Role: "user", Text: "stopped ask"}},
		"user":    nil,
		"early":   nil,
	} {
		hits, err := store.SearchMessages(ctx, needle, nil)
		require.NoError(t, err)
		require.Equal(t, want, hits, needle)
	}

	principals, _, err := store.messagePrincipals(ctx)
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"chat": {"Ann <a&b>"}}, principals)
	require.Equal(t, principals, storedPrincipals(t, store), "the chat's row holds them")

	var reviewTexts int
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(search_text) FROM entries WHERE conversation_id = 'review'`).Scan(&reviewTexts))
	require.Zero(t, reviewTexts, "a conversation search never shows gets no text")

	// A new entry with the same replay gets the same text from Go.
	var (
		raw   string
		entry harness.SessionEntry
	)
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT entry_json FROM entries WHERE id = 301`).Scan(&raw))
	require.NoError(t, json.Unmarshal([]byte(raw), &entry))
	id, err := store.AppendEntryID(ctx, "chat", &entry)
	require.NoError(t, err)

	var migrated, written string
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT a.search_text, b.search_text FROM entries a, entries b WHERE a.id = 301 AND b.id = $1`, id).Scan(&migrated, &written))
	require.Equal(t, written, migrated)

	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	_, err = (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 55)
	require.NoError(t, err)

	var columns, rows int
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND column_name = 'search_text'`).Scan(&columns))
	require.Zero(t, columns)
	require.NoError(t, store.db.QueryRowContext(ctx, `WITH search_rows AS (SELECT count(*) AS n FROM message_search), markers AS (SELECT count(*) AS n FROM message_search_indexed)
SELECT s.n + m.n FROM search_rows s CROSS JOIN markers m`).Scan(&rows))
	require.Zero(t, rows, "the previous release's backfill indexes every chat again")
}

// 055 refuses a backfilled line or chat principals that differ from message_search; the abort
// commits nothing.
func TestEntrySearchMigrationRefuses(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, seed, want string }{
		{"mismatch", `CREATE FUNCTION tamper_search() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.search_text := replace(NEW.search_text, 'First', 'Fist'); RETURN NEW; END $$;
CREATE TRIGGER tamper_search BEFORE UPDATE ON entries FOR EACH ROW EXECUTE FUNCTION tamper_search();`, "055_entry_search: 2 search text lines differ from message_search"},
		{"principals", `CREATE FUNCTION tamper_principals() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.principals := '{}'; RETURN NEW; END $$;
CREATE TRIGGER tamper_principals BEFORE UPDATE ON conversations FOR EACH ROW EXECUTE FUNCTION tamper_principals();`, "055_entry_search: 1 chat principals differ from message_search"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := newTestSessionService(t)
			ctx := t.Context()
			seedEntrySearchMigration(t, store)
			_, err := store.db.ExecContext(ctx, tc.seed)
			require.NoError(t, err)

			require.ErrorContains(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)), tc.want)

			var last string
			require.NoError(t, store.db.QueryRowContext(ctx, `SELECT max(id) FROM pg_migrations`).Scan(&last))
			require.Equal(t, "054_entries_steps.sql", last)
		})
	}
}

// turnsMigrationItem is a saved user message as the replay input of an entry.
type turnsMigrationItem struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	InputID string `json:"input_id"`
	Header  string `json:"prompt_header"`
	Content string `json:"content"`
}

// 056 moves every lane's waiting inputs into turns with their IDs, deliveries, and order; a stash stays
// stashed and a stopped parent's held input stays held. A Slack mention and a subagent task answered or
// waiting before the migration are refused when delivered again after it.
func TestTurnsMigration(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 47)
	require.NoError(t, err)

	const (
		thread   = "slack-thread:D123:111.222"
		subagent = "web:parent/call_1"
		answered = "web:parent/turn-1/call/answered"
		held     = "web:parent/turn-1/call/held"
	)

	write := func(id, text string) *protocol.InboundMessage {
		inbound := agentWrite(text)
		inbound.ConversationID, inbound.Metadata["web_message_id"], inbound.Kind = subagent, id, protocol.InboundKindEnqueue

		return inbound
	}
	header, _, _ := strings.Cut(buildPrompt(write(answered, "answered prompt"), nil, false), "\n\n")
	saved, err := json.Marshal([]turnsMigrationItem{{Type: "message", Role: "user", InputID: answered, Header: header, Content: buildPrompt(write(answered, "answered prompt"), nil, false)}})
	require.NoError(t, err)
	heldInbound, err := json.Marshal(write(held, "held prompt"))
	require.NoError(t, err)

	mention := newThreadInboundMessage("second", "222.333", "111.222")
	mention.Kind = protocol.InboundKindEnqueue
	mentionInbound, err := json.Marshal(mention)
	require.NoError(t, err)

	_, err = store.db.ExecContext(ctx, `
INSERT INTO managed_conversations (conversation_id, agent, created_by, parent_conversation_id) VALUES
    ('chat', 'main', 'alice', ''), ('`+thread+`', 'main', '', ''), ('web:parent', 'main', 'alice', ''), ('`+subagent+`', 'helper', '', 'web:parent');
INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES
    ('`+thread+`', '{"type":"turn","replay_input":[{"type":"message","role":"user","input_id":"slack:D123:111.222","content":"first"}]}', ''),
    ('web:parent', '{"type":"turn","turn_id":"turn-1","replay_input":[{"type":"function_call","call_id":"call_1","name":"task","arguments":"{}"}]}', '');
INSERT INTO thread_queue (queue_item_id, conversation_id, message, principal, stash_at_unix_ns, position, park_after, slack_channel, slack_ts, kind, source, inbound_json) VALUES
    ('q-parked', 'chat', 'parked', 'alice', 1, 0, 's1', '', '', 'enqueue', 'web', NULL),
    ('q-first', 'chat', 'first', 'alice', 2, 0, '', '', '', 'enqueue', 'web', NULL),
    ('q-steer', 'chat', 'steer', 'alice', 3, 1, '', '', '', 'steer', 'web', '{"Kind":"steer","Text":"steer"}'),
    ('q-stash', 'chat', 'stash', 'alice', 4, 2, '', '', '', 'held', 'web', NULL),
    ('q-goal', 'chat', 'goal', 'alice', 5, 3, '', '', '', 'prompt', 'web', '{"Kind":"prompt","Text":"goal"}')`)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ($1, json_build_object('type', 'turn', 'replay_input', $2::json), '')`, subagent, string(saved))
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `INSERT INTO thread_queue (queue_item_id, conversation_id, message, principal, stash_at_unix_ns, position, park_after, slack_channel, slack_ts, kind, source, inbound_json) VALUES
    ('slack:D123:222.333', $1, 'second', 'U1', 6, 0, '', 'D123', '222.333', 'enqueue', 'slack', $2), ($3, $4, 'held prompt', '', 7, 0, '', '', '', 'held', 'system', $5)`, thread, string(mentionInbound), held, subagent, string(heldInbound))
	require.NoError(t, err)
	backfillPreviousRelease(t, store.db, "pg_migrations")
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	var queued int
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) FROM legacy_thread_queue`).Scan(&queued))
	require.Zero(t, queued, "thread_queue stays, empty, under its legacy name")

	waiting, err := queryStrings(ctx, store.db, `SELECT concat_ws('|', input_id, reason, delivery) FROM turns WHERE phase = 'waiting' ORDER BY 1`, "waiting turns")
	require.NoError(t, err)
	require.Equal(t, []string{"q-first|queued|enqueue", "q-goal|queued|prompt", "q-parked|queued|enqueue", "q-stash|stashed|held", "q-steer|queued|steer",
		"slack:D123:222.333|queued|enqueue", held + "|held|held"}, waiting)

	items, err := store.ThreadQueueForConversation("chat")
	require.NoError(t, err)

	lane := make([]string, 0, len(items))
	for i := range items {
		lane = append(lane, strings.Join([]string{items[i].ID, string(items[i].Kind), items[i].Message, items[i].ParkAfter, strconv.Itoa(items[i].Position)}, "|"))
	}

	require.Equal(t, []string{"q-parked|enqueue|parked|s1|0", "q-first|enqueue|first||0", "q-steer|steer|steer||1", "q-stash|held|stash||2", "q-goal|prompt|goal||3"}, lane)
	require.Equal(t, []string{"q-parked", "q-first", "q-steer", "q-goal"}, queueIDs(t, store, "chat"), "the stash waits until popped")

	stopped := &Bridge{log: store.log, config: Config{ConversationID: thread, SessionService: store}, stopped: true}
	manager := newThreadBridgeManager(nil, store, store.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return stopped }})

	for _, ts := range []string{"111.222", "222.333"} {
		inbound := newThreadInboundMessage("again", ts, "111.222")
		protocol.SetInboundAllowedAgents(inbound, []string{"main"})
		accepted, err := manager.SubmitMention(ctx, "main", slackTarget("D123", "111.222"), inbound)
		require.NoError(t, err)
		require.False(t, accepted, "an answered or waiting mention is refused")
	}

	runningTestBridge(t, store, manager, subagent)

	for id, text := range map[string]string{answered: "answered prompt", held: "held prompt"} {
		_, _, err := manager.writeSession(ctx, subagent, id, sessionWriteEnqueue, write(id, text))
		require.ErrorIs(t, err, errWriteDelivered, id)
	}

	reason, err := queryStrings(ctx, store.db, `SELECT reason FROM turns WHERE input_id = $1`, "held reason", held)
	require.NoError(t, err)
	require.Equal(t, []string{"held"}, reason, "a refused write releases nothing")
}

// 056 aborts when a lane's waiting inputs differ from 049's manifest or a moved value differs from its
// queue row, and the abort commits nothing.
func TestTurnsMigrationRefusesMismatch(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, seed, want string }{
		{"unrecorded input", ``, "056_turns: 1 waiting inputs differ"},
		{"tampered value", `INSERT INTO legacy_lane_work (conversation_id, position, queue_item_id, kind) VALUES ('chat', 1, 'q', 'enqueue');
-- The body compiles at its first call, once 056 has added the message column.
CREATE FUNCTION tamper_message() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.message := 'tampered'; RETURN NEW; END $$;
CREATE TRIGGER tamper_message BEFORE INSERT ON active_turns FOR EACH ROW EXECUTE FUNCTION tamper_message();`, "056_turns: 2 waiting inputs differ"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := newTestSessionService(t)
			ctx := t.Context()
			source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
			_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 55)
			require.NoError(t, err)
			_, err = store.db.ExecContext(ctx, `INSERT INTO thread_queue (queue_item_id, conversation_id, message, principal, stash_at_unix_ns, position) VALUES ('q', 'chat', 'queued', 'alice', 1, 0);`+tc.seed)
			require.NoError(t, err)

			require.ErrorContains(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)), tc.want)

			var last string
			require.NoError(t, store.db.QueryRowContext(ctx, `SELECT max(id) FROM pg_migrations`).Scan(&last))
			require.Equal(t, "055_entry_search.sql", last)

			var queued int
			require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) FROM thread_queue`).Scan(&queued))
			require.Equal(t, 1, queued)
		})
	}
}

// 057 moves every Background Job into turns with its fields and Completion Note and empties
// background_jobs; the note of a hidden run's job lists and wakes toward the chat the run reports to.
func TestBackgroundTurnsMigration(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 47)
	require.NoError(t, err)

	_, err = store.db.ExecContext(ctx, `
INSERT INTO managed_conversations (conversation_id, agent, created_by, parent_conversation_id, producer_inbound_json) VALUES
    ('chat', 'main', 'alice', '', NULL), ('cron:daily', 'main', '', '', '{"SyncDestination":"chat"}');
INSERT INTO background_jobs (conversation_id, job_id, kind, status, agent, label, call_id, subagent_key, origin_json, sync_destination, runner_id, note_state, wake,
    wake_attempts, wake_after_unix_ns, claimed_turn_id, created_at_unix_ns, finished_at_unix_ns, result, input) VALUES
    ('chat', 'turn-1/call/a', 'execute', 'killed', 'main', 'tests', 'a', '', '{"Source":"web"}', '', 'runner-1', 'pending', FALSE, 0, 0, '', 1, 2, '', ''),
    ('chat', 'turn-1/call/b', 'task', 'completed', 'helper', 'review', 'b', '/b', '{"Source":"web"}', '', 'runner-1', 'consumed', FALSE, 0, 0, 'turn-2', 3, 4, 'fine', 'review it'),
    ('cron:daily', 'turn-3/call/c', 'execute', 'failed', 'main', 'report', 'c', '', '{"SyncDestination":"chat"}', 'chat', 'runner-1', 'pending', TRUE, 2, 600, '', 5, 6, 'exit 1', '')`)
	require.NoError(t, err)
	backfillPreviousRelease(t, store.db, "pg_migrations")
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	var left int
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) FROM legacy_background_jobs`).Scan(&left))
	require.Zero(t, left, "background_jobs stays, empty, under its legacy name")

	require.Equal(t, []string{"turn-1/call/a", "turn-3/call/c"}, backgroundJobIDs(t, store, "chat"), "the chat lists its pending note and that of the run reporting to it")

	web, routed := &protocol.InboundMessage{Source: protocol.SourceWeb}, &protocol.InboundMessage{SyncDestination: "chat"}
	noted, err := store.notedBackgroundJobs(ctx, "chat", []string{"turn-1/call/a", "turn-1/call/b"})
	require.NoError(t, err)
	require.Equal(t, []backgroundJob{
		{conversationID: "chat", jobID: "turn-1/call/a", kind: turnScript, status: backgroundKilled, agent: "main", label: "tests", callID: "a", origin: web, runnerID: "runner-1", noteState: notePending,
			createdAt: time.Unix(0, 1).UTC(), finishedAt: time.Unix(0, 2).UTC()},
		{conversationID: "chat", jobID: "turn-1/call/b", kind: turnNote, status: backgroundCompleted, agent: "helper", label: "review", callID: "b", subagentKey: "/b", origin: web, runnerID: "runner-1",
			noteState: noteConsumed, claimedTurnID: "turn-2", createdAt: time.Unix(0, 3).UTC(), finishedAt: time.Unix(0, 4).UTC(), result: "fine", input: "review it"},
	}, noted)

	wake, found, err := store.backgroundWake(ctx, "cron:daily")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, backgroundJob{conversationID: "cron:daily", jobID: "turn-3/call/c", kind: turnScript, status: backgroundFailed, agent: "main", label: "report", callID: "c", origin: routed,
		runnerID: "runner-1", noteState: notePending, wake: true, wakeAfter: time.Unix(0, 600).UTC(), createdAt: time.Unix(0, 5).UTC(), finishedAt: time.Unix(0, 6).UTC(), result: "exit 1"}, wake)

	attempts, err := queryStrings(ctx, store.db, `SELECT input_id || ' ' || wake_attempts FROM turns WHERE NOT takes_lane ORDER BY 1`, "wake attempts")
	require.NoError(t, err)
	require.Equal(t, []string{"turn-1/call/a 0", "turn-1/call/b 0", "turn-3/call/c 2"}, attempts)
}

// 057 refuses a running Background Job, and aborts when the jobs differ from 049's manifest, a moved
// value differs from its job, or a pending note would report elsewhere than its job did; neither commits.
func TestBackgroundTurnsMigrationRefuses(t *testing.T) {
	t.Parallel()

	const job = `INSERT INTO background_jobs (conversation_id, job_id, kind, status, agent, label, call_id, subagent_key, origin_json, sync_destination, runner_id, note_state, wake,
    claimed_turn_id, created_at_unix_ns, finished_at_unix_ns, result) VALUES ('chat', 'turn-1/call/a', 'execute', '%s', 'main', 'tests', 'a', '', '{}', '%s', 'runner-1', '%s', FALSE, '', 1, 2, 'out');`

	for _, tc := range []struct{ name, seed, want string }{
		{"running job", fmt.Sprintf(job, "running", "", "none"), "057_background_turns: 1 Background Jobs are running"},
		{"unrecorded job", fmt.Sprintf(job, "completed", "", "consumed"), "057_background_turns: 1 Background Jobs differ"},
		{"tampered value", fmt.Sprintf(job, "completed", "", "consumed") + `INSERT INTO legacy_row_counts VALUES ('background_jobs', 'completed/consumed', 1);
-- The body compiles at its first call, once 057 has added the result column.
CREATE FUNCTION tamper_result() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.result := 'tampered'; RETURN NEW; END $$;
CREATE TRIGGER tamper_result BEFORE INSERT ON turns FOR EACH ROW EXECUTE FUNCTION tamper_result();`, "057_background_turns: 2 Background Jobs differ"},
		{"moved destination", fmt.Sprintf(job, "completed", "elsewhere", "pending") + `INSERT INTO legacy_row_counts VALUES ('background_jobs', 'completed/pending', 1);`,
			"057_background_turns: 1 Background Jobs differ"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := newTestSessionService(t)
			ctx := t.Context()
			source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
			_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 57)
			require.NoError(t, err)
			_, err = store.db.ExecContext(ctx, tc.seed)
			require.NoError(t, err)

			require.ErrorContains(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)), tc.want)

			var last string
			require.NoError(t, store.db.QueryRowContext(ctx, `SELECT max(id) FROM pg_migrations`).Scan(&last))
			require.Equal(t, "056_turns.sql", last)

			var jobs int
			require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) FROM background_jobs`).Scan(&jobs))
			require.Equal(t, 1, jobs)
		})
	}
}

// 058 moves recurring reminders into schedules and one-time ones into waiting turns, each with every
// field, and drops cron state: no cron run missed during the downtime fires until startup rebuilds it.
func TestSchedulesMigration(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 57)
	require.NoError(t, err)

	_, err = store.db.ExecContext(ctx, `
INSERT INTO scheduled_messages (scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring, interval_ns) VALUES
    ('once', 'chat', 'main', 'call back', 100, 0, 0), ('every', 'chat', 'helper', 'check in', 200, 1, 7200000000000);
INSERT INTO cron_schedules (schedule_id, relative_path, next_due_unix_ns, updated_at_unix_ns) VALUES ('cron/daily.md#0#0 4 * * *', 'cron/daily.md', 1, 1);
INSERT INTO cron_schedule_runs (relative_path, running, running_since_unix_ns, updated_at_unix_ns) VALUES ('cron/daily.md', 1, 1, 1)`)
	require.NoError(t, err)
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	messages, err := store.ScheduledMessages()
	require.NoError(t, err)
	require.Equal(t, map[string]protocol.ScheduledMessageState{
		"once":  {ConversationID: "chat", Agent: "main", Message: "call back", DueAt: time.Unix(0, 100).UTC()},
		"every": {ConversationID: "chat", Agent: "helper", Message: "check in", DueAt: time.Unix(0, 200).UTC(), Recurring: true, Interval: 2 * time.Hour},
	}, messages)

	reasons, err := queryStrings(ctx, store.db, `SELECT input_id || ' ' || reason FROM turns WHERE phase = 'waiting'`, "waiting reminders")
	require.NoError(t, err)
	require.Equal(t, []string{"once scheduled"}, reasons, "a one-time reminder waits as a turn of its chat")

	due, err := store.DueCronSchedules(time.Now())
	require.NoError(t, err)
	require.Empty(t, due, "no cron run missed during the downtime fires")

	var left int
	require.NoError(t, store.db.QueryRowContext(ctx, `WITH sources AS (
    SELECT 1 FROM legacy_scheduled_messages UNION ALL SELECT 1 FROM legacy_cron_schedules UNION ALL SELECT 1 FROM legacy_cron_schedule_runs
)
SELECT count(*) FROM sources`).Scan(&left))
	require.Zero(t, left, "the source tables stay, empty, under their legacy names")
}

// 058 aborts when a moved reminder differs from its source; it commits nothing.
func TestSchedulesMigrationRefusesMismatch(t *testing.T) {
	t.Parallel()

	const reminder = `INSERT INTO scheduled_messages (scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring, interval_ns) VALUES ('once', 'chat', 'main', 'call back', 100, 0, %d);`

	for _, tc := range []struct{ name, seed string }{
		{"one-time interval", fmt.Sprintf(reminder, 5)},
		{"tampered value", fmt.Sprintf(reminder, 0) + `
CREATE FUNCTION tamper_message() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.message := 'tampered'; RETURN NEW; END $$;
CREATE TRIGGER tamper_message BEFORE INSERT ON turns FOR EACH ROW EXECUTE FUNCTION tamper_message();`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := newTestSessionService(t)
			ctx := t.Context()
			source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
			_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 57)
			require.NoError(t, err)
			_, err = store.db.ExecContext(ctx, tc.seed)
			require.NoError(t, err)

			require.ErrorContains(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)), "058_schedules: 2 reminders differ")

			var last string
			require.NoError(t, store.db.QueryRowContext(ctx, `SELECT max(id) FROM pg_migrations`).Scan(&last))
			require.Equal(t, "057_background_turns.sql", last)

			var reminders int
			require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) FROM scheduled_messages`).Scan(&reminders))
			require.Equal(t, 1, reminders)
		})
	}
}

// legacyHandoffSeed holds a row in every table the release retires, as the previous release left them,
// with a finished cron run that has no conversation record and whose report was copied into a chat.
const legacyHandoffSeed = `
INSERT INTO managed_conversations (conversation_id, agent, created_by, parent_conversation_id) VALUES
    ('chat', 'main', 'alice', ''), ('SHADOW', 'critic', '', ''), ('slack-thread:C1:1.1', 'main', '', '');
INSERT INTO session_entries (id, conversation_id, entry_json, entry_timestamp) VALUES
    (1, '$run', '{"type":"turn","replay_input":[{"type":"message","role":"user","content":"report"}]}', ''),
    (2, 'chat', '{"type":"turn","replay_input":[{"type":"message","role":"user","content":"hello"}]}', ''),
    (3, 'chat', '{"type":"turn","sync_source_entry_id":1,"sync_source_conversation_id":"$run","replay_input":[{"type":"message","role":"user","content":"report"}]}', ''),
    (4, 'chat', '{"type":"turn","replay_input":[{"type":"message","role":"user","content":"later"}]}', '');
SELECT setval(pg_get_serial_sequence('session_entries', 'id'), 4);
INSERT INTO active_turns (id, conversation_id, inbound_json, output_trace_json, history_anchor_id, created_at_unix_ns, updated_at_unix_ns, phase, terminal) VALUES
    ('stopped', 'slack-thread:C1:1.1', '{}', '[]', 0, 1, 1, 'done', 'stopped');
INSERT INTO turn_steps (conversation_id, key, value) VALUES ('slack-thread:C1:1.1', 'stopped',
    '{"applied_input_ids":[],"record":{"version":1,"type":"turn","turn_id":"stopped","replay_input":[{"type":"message","role":"user","content":"stopped ask"}]}}');
INSERT INTO code_mode_calls (turn_id, key, record) VALUES ('stopped', 'stopped/call/exec/host/0', '{"type":"rocketcode_code_mode_call","key":"stopped/call/exec/host/0","name":"bash"}');
INSERT INTO message_search (conversation_id, entry_id, turn_id, replay_index, part_index, role, text, text_lower, principal) VALUES
    ('chat', 2, NULL, 0, 0, 'user', 'hello', 'hello', 'alice'), ('slack-thread:C1:1.1', NULL, 'stopped', 0, 0, 'user', 'stopped ask', 'stopped ask', '');
INSERT INTO message_search_indexed (conversation_id) VALUES ('chat');
INSERT INTO thread_queue (queue_item_id, conversation_id, message, principal, stash_at_unix_ns, position) VALUES ('q', 'chat', 'queued', 'alice', 1, 0);
INSERT INTO background_jobs (conversation_id, job_id, kind, status, agent, label, call_id, subagent_key, origin_json, sync_destination, runner_id, note_state, wake,
    claimed_turn_id, created_at_unix_ns, finished_at_unix_ns, result) VALUES ('chat', 'turn-1/call/a', 'execute', 'completed', 'main', 'tests', 'a', '', '{}', '', 'runner-1', 'consumed', FALSE, '', 1, 2, 'out');
INSERT INTO scheduled_messages (scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring, interval_ns) VALUES ('once', 'chat', 'main', 'call back', 100, 0, 0);
INSERT INTO cron_schedules (schedule_id, relative_path, next_due_unix_ns, updated_at_unix_ns) VALUES ('cron/daily.md#0#0 4 * * *', 'cron/daily.md', 1, 1);
INSERT INTO cron_schedule_runs (relative_path, running, running_since_unix_ns, updated_at_unix_ns) VALUES ('cron/daily.md', 0, 0, 1);
INSERT INTO session_summaries (conversation_id, preview, last_updated) VALUES ('chat', 'hello', '2026-10-19Z');
INSERT INTO session_tags (conversation_id, tags) VALUES ('chat', '["a"]');
INSERT INTO conversation_goals (conversation_id, objective, check_script, max_turns, turns_used, status, note, created_at_unix_ns, updated_at_unix_ns) VALUES
    ('chat', 'ship', 'check.sh', 5, 2, 'active', 'going', 1, 2);
INSERT INTO external_mcp_sessions (external_conversation_id, private_conversation_id, managed_conversation_id, agent, slack_channel) VALUES
    ('ticket', 'external_mcp:job:RUN', 'slack-thread:C1:1.1', 'job', '#ops');
INSERT INTO shadow_bindings (child_conversation_id, parent_conversation_id, parent_agent, shadow_agent) VALUES ('SHADOW', 'chat', 'main', 'critic');
INSERT INTO shadow_turns (child_conversation_id, parent_source_conversation_id, parent_turn_id, inbound_json, terminal) VALUES ('SHADOW', 'chat', 'T', '{}', 'complete');
INSERT INTO opencode_sessions (conversation_id, server, title, directory, updated_at, running, synced_at) VALUES ('opencode:box:ses_a', 'box', 'Title', '/srv', '2026-10-17Z', FALSE, '2026-10-19Z');`

// The release migrates a store with a row in every retired table to the head, where 059 finds 049's
// manifest unchanged and keeps the rows nothing moved under legacy_ names; Down restores the tables
// with their foreign keys, dropping what those would have cascaded, and the head is reached again.
// Covers AE6: the cron run becomes a hidden cron run, and a link to the chat it reported into opens
// it with the same messages in the same order.
func TestLegacyHandoffMigration(t *testing.T) {
	t.Parallel()

	const run = "cron:cron/daily.md:20261019T120000.000000000Z:RUN"

	store := newTestSessionService(t)
	ctx := t.Context()
	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	set := migrate.MigrationSet{TableName: "pg_migrations"}
	_, err := set.ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 47)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, strings.ReplaceAll(legacyHandoffSeed, "$run", run))
	require.NoError(t, err)

	// What the previous release's History showed in the chat.
	before, err := queryStrings(ctx, store.db, `SELECT id || ' ' || (entry_json->'replay_input'->0)::text FROM session_entries WHERE conversation_id = 'chat' ORDER BY id`, "chat messages before")
	require.NoError(t, err)
	backfillPreviousRelease(t, store.db, "pg_migrations")
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	kinds, err := queryStrings(ctx, store.db, `SELECT concat_ws(' ', conversation_id, kind, listed) FROM conversations WHERE conversation_id IN ('chat', $1) ORDER BY 1`, "conversation kinds", run)
	require.NoError(t, err)
	require.Equal(t, []string{"chat web_chat t", run + " cron_run f"}, kinds)

	view, err := store.ObserveHistory(ctx, "chat", "", 0, 0, 0, &HistoryInventory{})
	require.NoError(t, err)

	after := make([]string, 0, len(view.Entries))
	for _, entry := range view.Entries {
		after = append(after, strconv.FormatInt(entry.ID, 10)+" "+string(entry.Entry.ReplayInput[0]))
	}

	require.Equal(t, []string{`2 {"type":"message","role":"user","content":"hello"}`, `3 {"type":"message","role":"user","content":"report"}`, `4 {"type":"message","role":"user","content":"later"}`}, before)
	require.Equal(t, before, after)

	kept, err := queryStrings(ctx, store.db, `SELECT 'search ' || count(*) FROM legacy_message_search UNION ALL SELECT 'code mode ' || count(*) FROM legacy_code_mode_calls`, "kept rows")
	require.NoError(t, err)
	require.Equal(t, []string{"search 2", "code mode 1"}, kept)

	// Pruning the stopped turn leaves its search row and Code Mode record behind, unbound.
	_, err = store.db.ExecContext(ctx, `DELETE FROM turns WHERE id = 'stopped'`)
	require.NoError(t, err)
	_, err = set.ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 58)
	require.NoError(t, err)

	restored, err := queryStrings(ctx, store.db, `SELECT 'search ' || count(*) FROM message_search UNION ALL SELECT 'code mode ' || count(*) FROM code_mode_calls
UNION ALL SELECT 'keys ' || count(*) FROM pg_constraint WHERE contype = 'f' AND conrelid IN ('message_search'::regclass, 'code_mode_calls'::regclass, 'shadow_bindings'::regclass)`, "restored rows")
	require.NoError(t, err)
	require.Equal(t, []string{"search 1", "code mode 0", "keys 5"}, restored)
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))
}

// 059 aborts when the end of the release differs from 049's manifest, and the abort renames nothing.
func TestLegacyHandoffMigrationRefusesMismatch(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, tamper string }{
		{"entry bytes", `INSERT INTO legacy_entry_md5 VALUES (1, 'tampered')`},
		{"history", `INSERT INTO legacy_history VALUES ('chat', 1, 1)`},
		{"listed", `INSERT INTO legacy_listed VALUES ('chat')`},
		{"shown", `INSERT INTO legacy_shown VALUES ('chat')`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := newTestSessionService(t)
			ctx := t.Context()
			source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
			_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", source, migrate.Down, 58)
			require.NoError(t, err)
			_, err = store.db.ExecContext(ctx, tc.tamper)
			require.NoError(t, err)

			require.ErrorContains(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)), "059_legacy_handoff: 1 rows differ from the baseline manifest")

			var last string
			require.NoError(t, store.db.QueryRowContext(ctx, `SELECT max(id) FROM pg_migrations`).Scan(&last))
			require.Equal(t, "058_schedules.sql", last)

			var queue sql.NullString
			require.NoError(t, store.db.QueryRowContext(ctx, `SELECT to_regclass('thread_queue')::text`).Scan(&queue))
			require.True(t, queue.Valid)
		})
	}
}
