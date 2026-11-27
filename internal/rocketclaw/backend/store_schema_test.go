package backend

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	migrate "github.com/rubenv/sql-migrate"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestExternalRunReportsIndexMigration(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()

	_, err := store.db.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed)
SELECT 'chat-' || n, 'main', 'alice', 'web_chat', TRUE FROM generate_series(1, 2) n;
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, reports_to)
SELECT 'run-' || n, 'main', '', CASE WHEN n <= 8 THEN 'external_mcp_run' ELSE 'routine_run' END, FALSE, 'chat-' || (n % 2 + 1)
FROM generate_series(1, 8192) n;
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed)
VALUES ('unbound-run', 'main', '', 'external_mcp_run', FALSE);
ANALYZE conversations;`)
	require.NoError(t, err)

	const lookup = `SELECT DISTINCT reports_to FROM conversations WHERE kind = 'external_mcp_run' AND reports_to IS NOT NULL`
	for range 2 {
		destinations, err := queryStrings(ctx, store.db, lookup+` ORDER BY reports_to`, "external run destinations")
		require.NoError(t, err)
		require.Equal(t, []string{"chat-1", "chat-2"}, destinations)

		plan, err := queryStrings(ctx, store.db, `EXPLAIN (GENERIC_PLAN) `+lookup, "external run destinations plan")
		require.NoError(t, err)
		require.Contains(t, strings.Join(plan, "\n"), "conversations_external_reports")
		t.Log(strings.Join(plan, "\n"))

		_, err = (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 99)
		require.NoError(t, err)

		var exists bool
		require.NoError(t, store.db.QueryRowContext(ctx, `SELECT to_regclass('conversations_external_reports') IS NOT NULL`).Scan(&exists))
		require.False(t, exists)
		require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))
	}
}

func TestConversationSpaceIndexMigration(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()
	_, err := store.db.ExecContext(ctx, `INSERT INTO web_spaces (id, name) VALUES ('birth', 'Birth');
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, space_id)
SELECT 'chat-' || n, 'main', 'alice', 'web_chat', TRUE, CASE WHEN n <= 4 THEN 'birth' END
FROM generate_series(1, 2048) n;
ANALYZE conversations;
SET plan_cache_mode = force_generic_plan;
PREPARE birth_space_lookup(text) AS UPDATE ONLY conversations SET space_id = NULL WHERE space_id = $1;`)
	require.NoError(t, err)

	for range 2 {
		plan, err := queryStrings(ctx, store.db, `EXPLAIN EXECUTE birth_space_lookup('birth')`, "birth-space lookup plan")
		require.NoError(t, err)
		require.Contains(t, strings.Join(plan, "\n"), "conversations_space")
		t.Log(strings.Join(plan, "\n"))

		_, err = (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 76)
		require.NoError(t, err)

		var exists bool
		require.NoError(t, store.db.QueryRowContext(ctx, `SELECT to_regclass('conversations_space') IS NOT NULL`).Scan(&exists))
		require.False(t, exists)
		require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))
	}
}

func TestDelegationLookupStopsAtFirstChildEntry(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()
	recordTestConversations(t, store, "main", map[string]ConversationKind{"parent-history": KindWebChat})
	recordLegacyDelegations(t, store, "parent-history", "amber", "cobalt")
	_, err := store.db.ExecContext(ctx, `
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed)
SELECT 'unrelated-history-' || n, 'main', '', 'web_chat', TRUE FROM generate_series(1, 4096) n;
ALTER TABLE entries DISABLE TRIGGER entries_transcript_change, DISABLE TRIGGER shadow_entries_change;
INSERT INTO entries (conversation_id, entry_json, entry_timestamp) VALUES
    ('parent-history', '{"replay_input":[{"type":"function_call","call_id":"cobalt"},{"type":"function_call","call_id":"amber"},{"type":"function_call","call_id":"missing"}]}', '2000-01-01T00:00:00Z');
INSERT INTO history_links (conversation_id, entry_id) SELECT conversation_id, id FROM entries WHERE conversation_id = 'parent-history';
WITH children(conversation_id) AS (VALUES ('parent-history/amber'), ('parent-history/cobalt'))
INSERT INTO entries (conversation_id, entry_json, entry_timestamp)
SELECT conversation_id, '{}', '2000-01-01T00:00:00Z'
FROM children, generate_series(1, 4096);
INSERT INTO entries (conversation_id, entry_json, entry_timestamp)
SELECT 'unrelated-history-' || n, '{}', '2000-01-01T00:00:00Z' FROM generate_series(1, 4096) n;
ALTER TABLE entries ENABLE TRIGGER entries_transcript_change, ENABLE TRIGGER shadow_entries_change;
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

func saveEntryFacts(t *testing.T, store *SessionService, ids ...string) {
	t.Helper()

	for _, id := range ids {
		summary, err := loadSessionSummary(t.Context(), store.db, id)
		require.NoError(t, err)
		require.NoError(t, saveSessionSummary(t.Context(), store.db, summary))
	}
}

func TestChatListingReadsScaleWithReportingRuns(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()
	_, err := store.db.ExecContext(ctx, `
ALTER TABLE conversations SET (autovacuum_enabled = false);
ALTER TABLE entries SET (autovacuum_enabled = false);
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, last_updated)
SELECT 'chat-' || n, 'main', 'alice', 'web_chat', TRUE, now() FROM generate_series(1, 256) n;
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, reports_to)
SELECT 'run-' || c || '-' || n, 'job', '', 'routine_run', FALSE, 'chat-' || c FROM generate_series(1, 4) c, generate_series(1, 256) n;
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

	for _, err := range store.sidebarSessions(ctx, "", nil) {
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

	store := newColdTestSessionServiceAt(t, t.TempDir())
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()
	_, err := store.db.ExecContext(ctx, `
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed) VALUES
    ('with-metadata', 'main', '', 'web_chat', TRUE), ('without-metadata', 'main', '', 'web_chat', TRUE), ('other', 'main', '', 'web_chat', TRUE);
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed)
SELECT 'thread-' || n, 'main', '', 'web_chat', TRUE FROM generate_series(1, 4096) n;
ALTER TABLE entries DISABLE TRIGGER entries_transcript_change, DISABLE TRIGGER shadow_entries_change;
INSERT INTO entries (conversation_id, entry_json, entry_timestamp) VALUES
    ('with-metadata', '{"type":"mcp_external_metadata","version":1}', '2000-01-01T00:00:00Z'),
    ('with-metadata', '{"type":"mcp_external_metadata","version":2}', '2000-01-01T00:00:00Z'),
    ('other', '{"type":"mcp_external_metadata","version":3}', '2000-01-01T00:00:00Z');
WITH histories(conversation_id) AS (VALUES ('with-metadata'), ('without-metadata'))
INSERT INTO entries (conversation_id, entry_json, entry_timestamp)
SELECT conversation_id, '{"type":"turn"}', '2000-01-01T00:00:00Z'
FROM histories, generate_series(1, 4096);
INSERT INTO entries (conversation_id, entry_json, entry_timestamp)
SELECT 'thread-' || n, '{"type":"mcp_external_metadata","version":4}', '2000-01-01T00:00:00Z' FROM generate_series(1, 4096) n;
ALTER TABLE entries ENABLE TRIGGER entries_transcript_change, ENABLE TRIGGER shadow_entries_change;
ANALYZE conversations;
ANALYZE entries;
SET plan_cache_mode = force_generic_plan;`)
	require.NoError(t, err)

	for _, conversationID := range []string{"with-metadata", "without-metadata", "missing"} {
		entry, err := store.externalMCPMetadataEntry(ctx, conversationID)
		require.NoError(t, err)
		require.Equal(t, conversationID == "with-metadata", entry != nil)

		if entry != nil {
			require.Equal(t, 2, entry.Version)
			require.Equal(t, externalMCPMetadataEntryType, entry.Type)
		}
	}

	queries, err := queryStrings(ctx, store.db, `SELECT format('EXPLAIN EXECUTE %I(%s)', name,
    (SELECT string_agg('NULL', ',') FROM unnest(parameter_types)))
FROM pg_prepared_statements WHERE statement LIKE '%mcp_external_metadata%' AND statement NOT LIKE '%pg_prepared_statements%'`, "metadata lookup statement")
	require.NoError(t, err)
	require.Len(t, queries, 1)
	plan, err := queryStrings(ctx, store.db, queries[0], "metadata lookup plan")
	require.NoError(t, err)
	require.Contains(t, strings.Join(plan, "\n"), "session_entries_mcp_metadata")
	require.NotRegexp(t, `Sort Key: e(_\d+)?\.`, strings.Join(plan, "\n"), "the index orders the entries; only the few candidate conversation IDs may be sorted")

	for _, line := range plan {
		if strings.Contains(line, "Filter:") {
			require.NotContains(t, line, "entry_json", "the metadata index finds entries without filtering them")
		}
	}

	t.Log(strings.Join(plan, "\n"))
}

func TestShadowHistoryUsesProducerDestinationIndex(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()
	_, err := store.db.ExecContext(ctx, `
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed)
SELECT 'unrelated-' || n, 'sample-agent', '', 'web_chat', TRUE FROM generate_series(1, 4096) n;
INSERT INTO conversations (conversation_id, agent, created_by, producer_inbound_json, reports_to, kind, listed) VALUES
    ('destination', 'sample-agent', '', '{}', NULL, 'web_chat', TRUE),
    ('producer', 'sample-agent', '', '{"SyncDestination":"destination"}', 'destination', 'routine_run', FALSE),
    ('producer/comparison', 'sample-agent/comparison', '', '{}', NULL, 'shadow', FALSE);
UPDATE conversations SET shadow_parent = 'producer', parent_agent = 'sample-agent', shadow_agent = 'sample-agent/comparison' WHERE conversation_id = 'producer/comparison';
ANALYZE conversations;
SET plan_cache_mode = force_generic_plan;`)
	require.NoError(t, err)

	view, err := store.ObserveHistory(ctx, "destination", "", 0, 0, 0, &HistoryInventory{})
	require.NoError(t, err)
	require.Equal(t, []protocol.ShadowBinding{{ParentConversationID: "producer", ParentAgent: "sample-agent", ShadowAgent: "sample-agent/comparison", ChildConversationID: "producer/comparison"}}, view.Shadows)

	queries, err := queryStrings(ctx, store.db, `SELECT format('EXPLAIN EXECUTE %I(%s)', name,
    array_to_string(array_fill('NULL'::text, ARRAY[cardinality(parameter_types)]), ','))
FROM pg_prepared_statements WHERE starts_with(statement, $1) ORDER BY name`, "shadow history statements", shadowHistorySQL)
	require.NoError(t, err)
	require.Len(t, queries, 2)

	for _, query := range queries {
		plan, err := queryStrings(ctx, store.db, query, "shadow history plan")
		require.NoError(t, err)
		require.Contains(t, strings.Join(plan, "\n"), "managed_conversations_reports_to")
		t.Log(strings.Join(plan, "\n"))
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

	base, err := url.Parse(os.Getenv("ROCKETCLAW_TEST_DATABASE_URL"))
	require.NoError(t, err)
	adminConfig, err := pgx.ParseConfig(base.String())
	require.NoError(t, err)

	admin := stdlib.OpenDB(*adminConfig)
	defer func() { require.NoError(t, admin.Close()) }()

	database := "t_" + strings.ToLower(rand.Text())
	_, err = admin.ExecContext(t.Context(), `CREATE DATABASE `+database)

	require.NoError(t, err)
	defer func() {
		_, err := admin.ExecContext(context.Background(), `DROP DATABASE `+database+` WITH (FORCE)`)
		require.NoError(t, err)
	}()

	var databaseOID uint32
	require.NoError(t, admin.QueryRowContext(t.Context(), `SELECT oid FROM pg_database WHERE datname = $1`, database).Scan(&databaseOID))

	for _, outcome := range []string{"success", "cancel", "cancel waiting", "connection loss"} {
		t.Run(outcome, func(t *testing.T) {
			schema := "t_" + strings.ToLower(rand.Text())
			location := *base
			location.Path = "/" + database
			query := location.Query()
			query.Set("options", "-csearch_path="+schema)
			location.RawQuery = query.Encode()
			dsn := location.String()
			cfg, err := pgx.ParseConfig(dsn)
			require.NoError(t, err)

			db := stdlib.OpenDB(*cfg)
			defer func() { require.NoError(t, db.Close()) }()

			_, err = db.ExecContext(t.Context(), `CREATE SCHEMA `+schema)

			require.NoError(t, err)
			defer func() {
				_, err := db.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE; DROP EXTENSION IF EXISTS pg_trgm`)
				require.NoError(t, err)
			}()

			_, err = db.ExecContext(t.Context(), `CREATE TABLE pg_migrations (id text PRIMARY KEY, applied_at timestamptz)`)
			require.NoError(t, err)
			barrier, err := db.BeginTx(t.Context(), nil)
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(t.Context())
			ctxSecond, cancelSecond := context.WithCancel(t.Context())

			var first, second errgroup.Group

			defer func() {
				cancelSecond()
				cancel()

				_ = barrier.Rollback()
				_ = first.Wait()
				_ = second.Wait()
			}()

			_, err = barrier.ExecContext(t.Context(), `LOCK TABLE pg_migrations IN SHARE MODE`)
			require.NoError(t, err)

			first.Go(func() error {
				store, err := NewSessionServiceIn(ctx, &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
				if err != nil {
					return err
				}

				return store.Stop()
			})

			var pid int

			require.Eventually(t, func() bool {
				return db.QueryRowContext(t.Context(), `SELECT a.pid FROM pg_stat_activity a JOIN pg_locks l ON a.pid = l.pid
WHERE l.database = $1 AND l.relation = 'pg_migrations'::regclass AND NOT l.granted AND a.query LIKE 'LOCK TABLE pg_migrations%'`, databaseOID).Scan(&pid) == nil
			}, 5*time.Second, time.Millisecond)

			second.Go(func() error {
				store, err := NewSessionServiceIn(ctxSecond, &config.Config{DatabaseURL: dsn, Workspace: t.TempDir()}, slog.New(slog.DiscardHandler))
				if err != nil {
					return err
				}

				return store.Stop()
			})
			require.Eventually(t, func() bool {
				var waiting int

				err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_locks WHERE database = $1 AND relation = 'pg_migrations'::regclass AND NOT granted`, databaseOID).Scan(&waiting)

				return err == nil && waiting == 2
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

			var n int

			if outcome == "cancel" || outcome == "connection loss" {
				var conversations sql.NullString
				require.NoError(t, db.QueryRowContext(t.Context(), `SELECT to_regclass('conversations')::text`).Scan(&conversations))
				require.False(t, conversations.Valid, "the baseline commits with its ledger row or not at all")
				require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_migrations`).Scan(&n))
				require.Zero(t, n)
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
			require.Eventually(t, func() bool {
				var locks int

				err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_locks WHERE database = $1 AND relation = 'pg_migrations'::regclass`, databaseOID).Scan(&locks)

				return err == nil && locks == 0
			}, 5*time.Second, time.Millisecond)
		})
	}
}

func TestSessionMigrationsCancelPlanning(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	store := newColdTestSessionServiceAt(t, workspace)
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
		return Run(ctx, &config.Config{Workspace: workspace, DatabaseURL: testStoreDSN(workspace)}, slog.New(slog.DiscardHandler), new(slog.LevelVar), &frontendAssemblerMock{GateFunc: passGate}, sandbox.None{})
	})
	require.Eventually(t, func() bool {
		var n int

		err := barrier.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_locks WHERE relation='pg_migrations'::regclass AND NOT granted`).Scan(&n)

		return err == nil && n == 1
	}, 5*time.Second, time.Millisecond)
	cancel()
	require.Error(t, work.Wait())
	require.NoError(t, barrier.Rollback())
	store.db.SetMaxOpenConns(1)
	require.NoError(t, initializeSessionDB(t.Context(), store.db, slog.New(slog.DiscardHandler)))
	require.NoError(t, store.db.PingContext(t.Context()))
}

func TestSessionMigrationRollsBackWithItsLedgerWrite(t *testing.T) {
	t.Parallel()

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	cfg, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)

	db := stdlib.OpenDB(*cfg)
	defer func() { require.NoError(t, db.Close()) }()

	ctx := t.Context()
	_, err = db.ExecContext(ctx, `CREATE TABLE pg_migrations (id text PRIMARY KEY, applied_at timestamptz);
		CREATE FUNCTION reject_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'ledger write failed'; END $$;
		CREATE TRIGGER reject_migration BEFORE INSERT ON pg_migrations FOR EACH ROW EXECUTE FUNCTION reject_migration()`)
	require.NoError(t, err)
	require.ErrorContains(t, initializeSessionDB(ctx, db, slog.New(slog.DiscardHandler)), "ledger write failed")

	var conversations sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, `SELECT to_regclass('conversations')::text`).Scan(&conversations))
	require.False(t, conversations.Valid)

	_, err = db.ExecContext(ctx, `DROP TRIGGER reject_migration ON pg_migrations; DROP FUNCTION reject_migration()`)
	require.NoError(t, err)
	require.NoError(t, initializeSessionDB(ctx, db, slog.New(slog.DiscardHandler)))

	_, err = db.ExecContext(ctx, `INSERT INTO pg_migrations VALUES ('unknown.sql', now())`)
	require.NoError(t, err)
	require.ErrorContains(t, initializeSessionDB(ctx, db, slog.New(slog.DiscardHandler)), "unknown migration")
}

func TestInitializeSessionDBAdoptsReleasedStore(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	ctx := t.Context()
	recordTestConversations(t, store, "main", map[string]ConversationKind{"chat": KindWebChat})

	release := func(ids ...string) {
		t.Helper()

		_, err := store.db.ExecContext(ctx, `DELETE FROM pg_migrations;
CREATE TABLE IF NOT EXISTS legacy_thread_queue (queue_item_id TEXT PRIMARY KEY);
INSERT INTO legacy_thread_queue VALUES ('q') ON CONFLICT DO NOTHING`)
		require.NoError(t, err)

		for _, id := range ids {
			_, err = store.db.ExecContext(ctx, `INSERT INTO pg_migrations VALUES ($1, '2026-10-22Z')`, id)
			require.NoError(t, err)
		}
	}

	release("001_init.sql", "058_schedules.sql")
	require.ErrorContains(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)), "predates 059_legacy_handoff.sql")

	var kept int
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) FROM legacy_thread_queue`).Scan(&kept))
	require.Equal(t, 1, kept)

	release("001_init.sql", "059_legacy_handoff.sql", "060_web_tab_rows.sql", "061_drop_web_tab_state_blob.sql", "062_drop_web_tab_placement.sql", "063_drop_web_tab_pin.sql", "064_web_shared_tabs.sql", "065_web_tab_mutes.sql", "066_hosted_page_conversation.sql", "067_behavior_settings.sql", "068_web_tab_search.sql", "069_external_mcp_web_chats.sql", "070_web_shared_order.sql", "071_drop_goal_check_script.sql", "072_remote_asking.sql", "073_drop_spawned_by.sql", "074_session_reply_jobs.sql", "075_drop_guardrail_entries.sql", "076_conversation_space_index.sql", "077_review_origin.sql", "078_subagent_model.sql", "079_web_top_shelves.sql", "080_external_mcp_metadata.sql", "081_drop_web_tab_preview.sql", "082_web_closed_tabs.sql", "083_web_tab_marked_unread.sql", "084_history_requires_conversation.sql", "085_drop_voice_conversation.sql", "086_conversation_sticky.sql", "087_web_search_pages.sql", "088_web_top_shelf_rows.sql", "089_web_tab_attention.sql", "090_web_auto_park.sql", "091_web_auto_park_default.sql", "092_conversation_paused.sql", "093_agent_drafts.sql", "094_agent_draft_messages.sql", "095_drop_retired_conversation_columns.sql", "096_hidden_run_copies.sql", "097_drop_entry_audience.sql", "098_drop_dashboards.sql", "099_external_run_reports_index.sql", "100_web_tab_folds.sql", "101_drop_top_shelf.sql", "102_web_card_order.sql", "103_drop_conversation_pinned.sql", "104_web_park_until_idle.sql", "105_web_tab_unfollowed.sql", "106_background_result_files.sql", "107_web_navigation.sql", "108_history_links.sql", "109_space_owned_pages.sql", "110_remote_automation.sql", "111_web_park_after_idle.sql", "112_routines_tab_paths.sql", "113_generated_name.sql", "114_web_tab_lists.sql", "115_drop_voice.sql", "116_routine_names.sql", "117_drop_attachment_original_unverified.sql", "118_goal_check_force.sql")
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	ledger, err := queryStrings(ctx, store.db, `SELECT id FROM pg_migrations ORDER BY id`, "ledger")
	require.NoError(t, err)
	require.Equal(t, []string{"001_baseline.sql", "060_web_tab_rows.sql", "061_drop_web_tab_state_blob.sql", "062_drop_web_tab_placement.sql", "063_drop_web_tab_pin.sql", "064_web_shared_tabs.sql", "065_web_tab_mutes.sql", "066_hosted_page_conversation.sql", "067_behavior_settings.sql", "068_web_tab_search.sql", "069_external_mcp_web_chats.sql", "070_web_shared_order.sql", "071_drop_goal_check_script.sql", "072_remote_asking.sql", "073_drop_spawned_by.sql", "074_session_reply_jobs.sql", "075_drop_guardrail_entries.sql", "076_conversation_space_index.sql", "077_review_origin.sql", "078_subagent_model.sql", "079_web_top_shelves.sql", "080_external_mcp_metadata.sql", "081_drop_web_tab_preview.sql", "082_web_closed_tabs.sql", "083_web_tab_marked_unread.sql", "084_history_requires_conversation.sql", "085_drop_voice_conversation.sql", "086_conversation_sticky.sql", "087_web_search_pages.sql", "088_web_top_shelf_rows.sql", "089_web_tab_attention.sql", "090_web_auto_park.sql", "091_web_auto_park_default.sql", "092_conversation_paused.sql", "093_agent_drafts.sql", "094_agent_draft_messages.sql", "095_drop_retired_conversation_columns.sql", "096_hidden_run_copies.sql", "097_drop_entry_audience.sql", "098_drop_dashboards.sql", "099_external_run_reports_index.sql", "100_web_tab_folds.sql", "101_drop_top_shelf.sql", "102_web_card_order.sql", "103_drop_conversation_pinned.sql", "104_web_park_until_idle.sql", "105_web_tab_unfollowed.sql", "106_background_result_files.sql", "107_web_navigation.sql", "108_history_links.sql", "109_space_owned_pages.sql", "110_remote_automation.sql", "111_web_park_after_idle.sql", "112_routines_tab_paths.sql", "113_generated_name.sql", "114_web_tab_lists.sql", "115_drop_voice.sql", "116_routine_names.sql", "117_drop_attachment_original_unverified.sql", "118_goal_check_force.sql"}, ledger)

	legacy, err := queryStrings(ctx, store.db, `SELECT tablename FROM pg_tables WHERE schemaname = current_schema() AND starts_with(tablename, 'legacy_')`, "legacy tables")
	require.NoError(t, err)
	require.Empty(t, legacy)

	chats, err := queryStrings(ctx, store.db, `SELECT conversation_id FROM conversations`, "conversations")
	require.NoError(t, err)
	require.Equal(t, []string{"chat"}, chats)
}

func TestHistoryRequiresConversationMigration(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	ctx := t.Context()
	recordTestConversations(t, store, "main", map[string]ConversationKind{"kept": KindWebChat})
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 84)
	require.NoError(t, err)

	for _, id := range []string{"kept", "gone"} {
		_, err = store.db.ExecContext(ctx, `INSERT INTO entries (conversation_id, entry_json, entry_timestamp) VALUES ($1, '{}', '')`, id)
		require.NoError(t, err)
		_, err = store.db.ExecContext(ctx, `INSERT INTO steps (conversation_id, key, value) VALUES ($1, 'step', '{}')`, id)
		require.NoError(t, err)
		_, err = store.db.ExecContext(ctx, `INSERT INTO turns (id, conversation_id, inbound_json, output_trace_json, created_at_unix_ns, updated_at_unix_ns) VALUES ($1 || '-turn', $1, '{}', '[]', 0, 0)`, id)
		require.NoError(t, err)
	}

	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	for _, table := range []string{"entries", "steps", "turns"} {
		owners, err := queryStrings(ctx, store.db, `SELECT conversation_id FROM `+table+` ORDER BY 1`, table)
		require.NoError(t, err)
		require.Equal(t, []string{"kept"}, owners, table)
	}

	_, err = store.db.ExecContext(ctx, `INSERT INTO entries (conversation_id, entry_json, entry_timestamp) VALUES ('gone', '{}', '')`)
	require.ErrorContains(t, err, "entries_conversation_fkey")
}

func TestHistoryLinksMigration(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	ctx := t.Context()
	recordTestConversations(t, store, "main", map[string]ConversationKind{"linear": KindWebChat, "staged": KindWebChat, "run": KindRoutineRun, "continuing": KindWebChat,
		"reported": KindWebChat, "reporter": KindRoutineRun, "steered": KindWebChat, "gapped": KindWebChat, "elsewhere": KindWebChat})
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 107)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `UPDATE conversations SET reports_to = 'linear' WHERE conversation_id = 'run';
UPDATE conversations SET continues = 'run' WHERE conversation_id = 'continuing';
UPDATE conversations SET reports_to = 'reported' WHERE conversation_id = 'reporter'`)
	require.NoError(t, err)

	insert := func(conversationID string, texts ...string) int64 {
		items := make([]string, 0, len(texts))
		for i, text := range texts {
			items = append(items, fmt.Sprintf(`{"type":"message","role":%q,"content":%q}`, []string{"user", "assistant"}[i%2], text))
		}

		var id int64
		require.NoError(t, store.db.QueryRowContext(ctx, `INSERT INTO entries (conversation_id, entry_json, entry_timestamp) VALUES ($1, $2, '') RETURNING id`,
			conversationID, `{"type":"turn","replay_input":[`+strings.Join(items, ",")+`]}`).Scan(&id))

		return id
	}

	first, report, second := insert("linear", "a", "b"), insert("run", "report", "done"), insert("linear", "c", "d")
	kept, hidden := insert("staged", "u1", "a1", "u2", "a2"), insert("staged", "later", "reply")
	_, err = store.db.ExecContext(ctx, `UPDATE conversations SET revert_message_id = $1 WHERE conversation_id = 'staged'`, fmt.Sprintf("%d:2", kept))
	require.NoError(t, err)

	asked := insert("reported", "q1", "a1", "q2", "a2")
	insert("reporter", "cron", "news")

	var steered int64
	require.NoError(t, store.db.QueryRowContext(ctx, `INSERT INTO entries (conversation_id, entry_json, entry_timestamp) VALUES ('steered', $1, '') RETURNING id`,
		`{"type":"turn","replay_input":[{"type":"message","role":"user","content":"w1"},{"type":"message","role":"assistant","content":"w2"},`+
			`{"type":"message","role":"user","content":"from slack","prompt_header":"[Slack principal=bob]"},{"type":"message","role":"assistant","content":"w3"}]}`).Scan(&steered))

	g1 := insert("gapped", "g1", "r1")
	gap := insert("elsewhere", "other", "reply")
	insert("gapped", "g2", "r2")

	for chat, marker := range map[string]string{"reported": fmt.Sprintf("%d:2", asked), "steered": fmt.Sprintf("%d:2", steered), "gapped": fmt.Sprintf("%d:0", gap)} {
		_, err = store.db.ExecContext(ctx, `UPDATE conversations SET revert_message_id = $2 WHERE conversation_id = $1`, chat, marker)
		require.NoError(t, err)
	}

	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	for id, want := range map[string][]int64{"linear": {first, report, second}, "run": {report}, "continuing": {report}, "staged": {kept}} {
		view, err := store.ObserveHistory(ctx, id, "", 0, 0, 0, &HistoryInventory{})
		require.NoError(t, err)

		ids := make([]int64, 0, len(view.Entries))
		for _, entry := range view.Entries {
			ids = append(ids, entry.ID)
			require.Equal(t, entry.ID == report && id != "run", entry.Synced, id)
		}

		require.Equal(t, want, ids, id)
	}

	require.Equal(t, []string{"message:a", "message:b", "message:report", "message:done", "message:c", "message:d"}, observedEntryTexts(t, store, "linear"))
	require.Equal(t, []string{"message:u1", "message:a1"}, observedEntryTexts(t, store, "staged"), "a staged marker stays the current point")

	marker, _, _, err := store.RevertState(ctx, "staged")
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%d:2", kept), marker)

	var stored bool
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) = 1 FROM entries WHERE id = $1`, hidden).Scan(&stored))
	require.True(t, stored, "the hidden suffix stays stored")

	require.Equal(t, []string{"message:q1", "message:a1", "message:q2", "message:a2", "message:cron", "message:news"}, observedEntryTexts(t, store, "reported"), "a report after the marker cancels it, as the old settle did")
	require.Equal(t, []string{"message:w1", "message:w2", "message:from slack", "message:w3"}, observedEntryTexts(t, store, "steered"), "an outside message after the marker cancels it")
	require.Equal(t, []string{"message:g1", "message:r1"}, observedEntryTexts(t, store, "gapped"), "a marker on an entry the chat does not show keeps the entries before it")

	for chat, want := range map[string]string{"reported": "", "steered": "", "gapped": fmt.Sprintf("%d:2", g1)} {
		marker, _, _, err := store.RevertState(ctx, chat)
		require.NoError(t, err)
		require.Equal(t, want, marker, chat)
	}
}

func TestHistoryReadFollowsLinkIndexes(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()
	recordTestConversations(t, store, "main", map[string]ConversationKind{"long": KindWebChat})
	_, err := store.db.ExecContext(ctx, `
ALTER TABLE conversations SET (autovacuum_enabled = false);
ALTER TABLE entries SET (autovacuum_enabled = false);
ALTER TABLE history_links SET (autovacuum_enabled = false);
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed)
SELECT 'other-' || n, 'main', '', 'web_chat', TRUE FROM generate_series(1, 4096) n;
ALTER TABLE entries DISABLE TRIGGER entries_transcript_change, DISABLE TRIGGER shadow_entries_change;
INSERT INTO entries (conversation_id, entry_json, entry_timestamp)
SELECT CASE WHEN n % 2 = 0 THEN 'long' ELSE 'other-' || (n + 1) / 2 END, '{"type":"turn","replay_input":[]}', '' FROM generate_series(1, 8192) n;
ALTER TABLE entries ENABLE TRIGGER entries_transcript_change, ENABLE TRIGGER shadow_entries_change;
INSERT INTO history_links (conversation_id, entry_id, parent_entry_id)
SELECT conversation_id, id, lag(id) OVER (PARTITION BY conversation_id ORDER BY id) FROM entries ORDER BY id;
ANALYZE conversations;
ANALYZE entries;
ANALYZE history_links;
SET plan_cache_mode = force_generic_plan;`)
	require.NoError(t, err)

	entries, err := store.ObserveEntries(ctx, "long")
	require.NoError(t, err)
	require.Len(t, entries, 4096)

	queries, err := queryStrings(ctx, store.db, `SELECT format('EXPLAIN EXECUTE %I(%s)', name,
    array_to_string(array_fill('NULL'::text, ARRAY[cardinality(parameter_types)]), ','))
FROM pg_prepared_statements WHERE statement LIKE '%revert_index FROM effective_entries' || chr(10) || 'ORDER BY id'`, "history read statement")
	require.NoError(t, err)
	require.Len(t, queries, 1)
	plan, err := queryStrings(ctx, store.db, queries[0], "history read plan")
	require.NoError(t, err)
	t.Log(strings.Join(plan, "\n"))
	require.Contains(t, strings.Join(plan, "\n"), "Index Scan Backward using history_links_leaf", "the current point is the newest link")
	require.Regexp(t, `WorkTable Scan on current_path[^\n]*\n\s*->  Index Scan using history_links_pkey`, strings.Join(plan, "\n"), "each step of the walk is one indexed link")
	require.NotContains(t, strings.Join(plan, "\n"), "Seq Scan on entries")
	require.NotContains(t, strings.Join(plan, "\n"), "Seq Scan on history_links")
}

func TestAgentDraftsMigrationDown(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	ctx := t.Context()
	recordTestConversations(t, store, "bot", map[string]ConversationKind{"draft-chat": KindAgentDraft, "kept": KindWebChat})
	recordLegacyDelegations(t, store, "draft-chat", "call-1")

	for _, id := range []string{"draft-chat", "draft-chat/call-1", "kept"} {
		_, err := store.db.ExecContext(ctx, `INSERT INTO entries (conversation_id, entry_json, entry_timestamp) VALUES ($1, '{}', '')`, id)
		require.NoError(t, err)
		_, err = store.db.ExecContext(ctx, `INSERT INTO steps (conversation_id, key, value) VALUES ($1, 'step', '{}')`, id)
		require.NoError(t, err)
		require.NoError(t, startTurnDB(ctx, store.db, id+"-turn", id, protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "hello", true)))
	}

	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 91)
	require.NoError(t, err)

	for _, table := range []string{"conversations", "entries", "steps", "turns"} {
		owners, err := queryStrings(ctx, store.db, `SELECT conversation_id FROM `+table+` ORDER BY 1`, table)
		require.NoError(t, err)
		require.Equal(t, []string{"kept"}, owners, table)
	}
}

func TestHiddenRunCopiesMigration(t *testing.T) {
	t.Parallel()

	downTo95 := func(store *SessionService) {
		_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(t.Context(), store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 95)
		require.NoError(t, err)
	}

	store := newColdTestSessionServiceAt(t, t.TempDir())
	ctx := t.Context()

	recordTestConversations(t, store, "main", map[string]ConversationKind{"chat": KindWebChat, "run": KindRoutineRun, "cron-chat": KindWebChat, "old-run": KindRoutineRun, "mcp-chat": KindWebChat, "mcp-run": KindExternalMCPRun})
	downTo95(store)
	_, err := store.db.ExecContext(ctx, `UPDATE conversations SET reports_to = 'chat', producer_inbound_json = '{}' WHERE conversation_id = 'run';
UPDATE conversations SET first_source = 'old-run', last_updated = now() WHERE conversation_id = 'cron-chat';
UPDATE conversations SET external_id = 'ext-1', external_run = 'mcp-run', last_updated = now() WHERE conversation_id = 'mcp-chat'`)
	require.NoError(t, err)

	const (
		original = `{"type":"turn","turn_id":"t1","replay_input":[{"type":"compaction","id":"cmp_1","encrypted_content":"sealed"},{"type":"message","role":"user","content":"report"}]}`
		shown    = `{"type":"turn","turn_id":"t1","replay_input":[{"type":"message","role":"user","content":"report"}]}`
	)

	insert := func(conversation, audience, entry, searchText string) int64 {
		var id int64
		require.NoError(t, store.db.QueryRowContext(ctx, `INSERT INTO entries (conversation_id, entry_json, entry_timestamp, audience, search_text) VALUES ($1, $2, '2026-10-01T00:00:00Z', $3, NULLIF($4, '')) RETURNING id`,
			conversation, entry, audience, searchText).Scan(&id))

		return id
	}
	copied := func(source int64, run string) string {
		return fmt.Sprintf(`{"type":"turn","turn_id":"t1","replay_input":[{"type":"message","role":"user","content":"report"}],"sync_source_entry_id":%d,"sync_source_conversation_id":%q}`, source, run)
	}

	pair := insert("run", "sandbox", original, "")
	merged := insert("run", "visible", copied(pair, "run"), "copy text")
	oldRun := insert("old-run", "both", shown, "")
	insert("cron-chat", "both", copied(oldRun, "old-run"), "chat text")
	mcpRun := insert("mcp-run", "both", shown, "")
	insert("mcp-chat", "both", shown, "")
	insert("mcp-chat", "both", copied(mcpRun, "mcp-run"), "")

	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	rows, err := queryStrings(ctx, store.db, `SELECT conversation_id || ' ' || id || ' ' || md5(entry_json::text) || ' ' || COALESCE(search_text, '') FROM entries ORDER BY id`, "merged entries")
	require.NoError(t, err)
	require.Equal(t, []string{
		fmt.Sprintf("run %d %x copy text", merged, md5.Sum([]byte(original))),
		fmt.Sprintf("old-run %d %x chat text", oldRun, md5.Sum([]byte(shown))),
		fmt.Sprintf("mcp-run %d %x ", mcpRun, md5.Sum([]byte(shown))),
	}, rows)

	facts, err := queryStrings(ctx, store.db, `SELECT conversation_id || ' ' || COALESCE(reports_to, '') || ' ' || COALESCE(first_source, '') FROM conversations ORDER BY 1`, "conversation facts")
	require.NoError(t, err)
	require.Equal(t, []string{"chat  ", "cron-chat  old-run", "mcp-chat  mcp-run", "mcp-run mcp-chat ", "old-run cron-chat ", "run chat "}, facts)

	for conversation, items := range map[string]int{"chat": 1, "run": 2} {
		observed, err := store.ObserveEntries(ctx, conversation)
		require.NoError(t, err)
		require.Len(t, observed, 1, conversation)
		require.Equal(t, merged, observed[0].ID, conversation)
		require.Len(t, observed[0].Entry.ReplayInput, items, "the chat leaves out the run's compaction items")
	}

	refused := newColdTestSessionServiceAt(t, t.TempDir())
	recordTestConversations(t, refused, "main", map[string]ConversationKind{"run": KindRoutineRun})
	downTo95(refused)
	_, err = refused.db.ExecContext(ctx, `INSERT INTO entries (conversation_id, entry_json, entry_timestamp, audience) VALUES ('run', '{}', '', 'visible')`)
	require.NoError(t, err)
	require.ErrorContains(t, initializeSessionDB(ctx, refused.db, slog.New(slog.DiscardHandler)), "cannot be merged")

	ledger, err := queryStrings(ctx, refused.db, `SELECT id FROM pg_migrations WHERE id >= '096' ORDER BY id`, "ledger")
	require.NoError(t, err)
	require.Empty(t, ledger, "a refusal commits nothing")
}

func TestDropWebTabStateBlobMigration(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	ctx := t.Context()
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 61)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `INSERT INTO web_tab_states (username, state, revision) VALUES ('alice', '\x0a00', 1), ('bob', NULL, 1)`)
	require.NoError(t, err)
	require.ErrorContains(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)), "not yet unpacked")

	_, err = store.db.ExecContext(ctx, `UPDATE web_tab_states SET state = NULL`)
	require.NoError(t, err)
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	columns, err := queryStrings(ctx, store.db, `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'web_tab_states' AND column_name = 'state'`, "blob column")
	require.NoError(t, err)
	require.Empty(t, columns)
}

func TestDropWebTabPinMigration(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	ctx := t.Context()
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 63)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `INSERT INTO web_tab_states (username, space, revision) VALUES ('alice', 'main', 1);
INSERT INTO web_tabs (username, path, position, pinned, preview, group_id, space) VALUES ('alice', '/search', 0, TRUE, FALSE, 'g', 'main'), ('alice', '/cron', 1, FALSE, TRUE, '', 'main')`)
	require.NoError(t, err)
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	state, _, err := store.TabState(ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, []Tab{{Path: "/search", Group: "g", Space: "main"}, {Path: "/routines", Space: "main"}}, state.Tabs, "pinned and preview tabs stay open in place as ordinary tabs")
}

func TestDropSpawnedByMigration(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	ctx := t.Context()
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 73)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, created_by, spawned_by, kind, listed) VALUES ('child', 'main', 'Alice', 'parent', $1, TRUE)`, KindWebChat)
	require.NoError(t, err)
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	columns, err := queryStrings(ctx, store.db, `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'conversations' AND column_name = 'spawned_by'`, "creator column")
	require.NoError(t, err)
	require.Empty(t, columns)

	thread, recorded, err := store.Thread("child")
	require.NoError(t, err)
	require.True(t, recorded)
	require.Equal(t, ThreadState{Agent: "main", CreatedBy: "Alice"}, thread, "the session stays")
}

func TestDropGuardrailEntriesMigration(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	ctx := t.Context()
	recordTestConversations(t, store, "main", map[string]ConversationKind{"sub": KindSubagent})
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 75)
	require.NoError(t, err)

	_, err = store.db.ExecContext(ctx, `INSERT INTO entries (conversation_id, entry_json, entry_timestamp) VALUES ('sub', '{"type":"guardrail"}', ''), ('sub', '{"type":"turn"}', '')`)
	require.NoError(t, err)

	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	kinds, err := queryStrings(ctx, store.db, `SELECT entry_json::jsonb->>'type' FROM entries WHERE conversation_id = 'sub' ORDER BY id`, "entry types")
	require.NoError(t, err)
	require.Equal(t, []string{"turn"}, kinds)
}

func TestSlackThreadsMigration(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	ctx := t.Context()
	require.NoError(t, store.RegisterExternalMCPConversation("ticket", "main", &ExternalMCPSessionState{Agent: "main", PrivateConversationID: "private", ManagedConversationID: "mcp"}))
	createWebChat(t, store, protocol.Conversation{ID: "cron", Agent: "main", CreatedBy: string(ThreadCreatedByRoutine)})
	createWebChat(t, store, protocol.Conversation{ID: "mention", Agent: "main"})
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 69)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `UPDATE conversations SET kind = 'slack_thread', slack_channel = 'C1', slack_thread = '1.' || length(conversation_id), external_channel = '#ops' WHERE conversation_id IN ('mcp', 'cron', 'mention')`)
	require.NoError(t, err)
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	for id, want := range map[string]ConversationKind{"mcp": KindWebChat, "cron": KindWebChat, "mention": KindSlackThread} {
		kind, err := store.ConversationKind(ctx, id)
		require.NoError(t, err)
		require.Equal(t, want, kind, id)

		_, thread, _, err := store.SlackThread(ctx, id)
		require.NoError(t, err)
		require.Equal(t, want == KindSlackThread, thread != "", "only a mention thread still posts to Slack: %s", id)
	}

	session, ok, err := store.ExternalMCPSession("ticket")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ExternalMCPSessionState{Agent: "main", PrivateConversationID: "private", ManagedConversationID: "mcp"}, session, "the session keeps its private run and canonical chat")
}

func TestSpaceLastTabMigration(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	ctx := t.Context()
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 64)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `INSERT INTO web_tab_states (username, space, revision) VALUES ('alice', 'main', 1);
INSERT INTO web_tabs (username, path, position, space, last) VALUES ('alice', '/search', 0, 'main', TRUE), ('alice', '/cron', NULL, 'ops', TRUE), ('alice', '/agents', NULL, 'ops', FALSE)`)
	require.NoError(t, err)
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	state, _, err := store.TabState(ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, TabState{Tabs: []Tab{{Path: "/search", Space: "main"}}, Space: "main", Last: map[string]string{"main": "/search", "ops": "/routines"}, SharedOrder: []string{}}, state)

	rows, err := queryStrings(ctx, store.db, `SELECT path FROM web_tabs ORDER BY path`, "tab rows")
	require.NoError(t, err)
	require.Equal(t, []string{"/search"}, rows, "closed tabs kept only as a space's last tab are dropped")
}

func TestWebSearchPagesMigration(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	ctx := t.Context()
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 87)
	require.NoError(t, err)

	shape, err := queryStrings(ctx, store.db, `SELECT table_name || ' ' || column_name FROM information_schema.columns
WHERE table_schema = current_schema() AND (column_name IN ('search', 'search_page') OR starts_with(table_name, 'web_search'))
UNION ALL SELECT 'function' WHERE to_regprocedure('delete_search_page()') IS NOT NULL ORDER BY 1`, "search page shape")
	require.NoError(t, err)
	require.Equal(t, []string{"web_closed_tabs search", "web_shared_tabs search", "web_tabs search"}, shape)

	_, err = store.db.ExecContext(ctx, `INSERT INTO web_tab_states (username, space, revision) VALUES ('alice', 'main', 1);
INSERT INTO web_tabs (username, path, position, space, search) VALUES
    ('alice', '/search/a', 0, 'main', '{"name":"Triage","searches":[{"id":"1","query":"is:unread"},{"id":"2","name":"Mine","query":"principal:alice"}],"active":"2"}'),
    ('alice', '/search/b', 1, 'main', '{"searches":[{"id":"1","query":"a"},{"id":"1","query":"b"}],"active":"9"}'),
    ('alice', '/search/c', 2, 'main', '{"name":"Empty","active":"1"}'),
    ('alice', '/cron', 3, 'main', NULL);
INSERT INTO web_shared_tabs (space, path, search) VALUES ('main', '/search/d', '{"name":"Deploys","searches":[],"active":""}'), ('main', '/cron', NULL);
INSERT INTO web_closed_tabs (username, path, space, search, closed_at) VALUES ('alice', '/search/e', 'main', '{}', now())`)
	require.NoError(t, err)
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	state, _, err := store.TabState(ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, []Tab{
		{Path: "/search/a", Space: "main", Search: &SearchPage{Name: "Triage", Searches: []SavedSearch{{ID: "1", Query: "is:unread"}, {ID: "2", Name: "Mine", Query: "principal:alice"}}, Active: "2"}},
		{Path: "/search/b", Space: "main", Search: &SearchPage{Searches: []SavedSearch{{ID: "1", Query: "a"}, {ID: "1", Query: "b"}}, Active: "9"}},
		{Path: "/search/c", Space: "main", Search: &SearchPage{Name: "Empty", Active: "1"}},
		{Path: "/routines", Space: "main"},
	}, state.Tabs)

	shared, err := store.SharedTabs(ctx)
	require.NoError(t, err)
	require.Equal(t, []Tab{{Space: "main", Path: "/search/d", Search: &SearchPage{Name: "Deploys"}}, {Space: "main", Path: "/routines"}}, shared)

	closed, err := store.ClosedTabs(ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, []Tab{{Path: "/search/e", Space: "main", Search: &SearchPage{}}}, closed)
}

func TestRoutinesTabPathsMigration(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	ctx := t.Context()
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 111)
	require.NoError(t, err)

	_, err = store.db.ExecContext(ctx, `INSERT INTO web_tab_states (username, space, revision, last, shared_order, card_order, navigation) VALUES
    ('alice', 'main', 1, '{"main": "/cron", "ops": "/cron/ZGFpbHk", "dev": "/cronjobs"}', '["/cron/ZGFpbHk", "/agents"]', E'["main", "parked\\n/cron", "parked\\n/cronjobs"]', '/cron/ZGFpbHk'),
    ('bob', 'main', 1, '{}', '[]', '[]', '');
INSERT INTO web_tabs (username, path, position, space) VALUES ('alice', '/cron', 0, 'main'), ('alice', '/cron/ZGFpbHk', 1, 'main'), ('alice', '/cronjobs', 2, 'main');
INSERT INTO web_shared_tabs (space, path) VALUES ('main', '/cron/ZGFpbHk'), ('main', '/agents');
INSERT INTO web_closed_tabs (username, path, space, closed_at) VALUES ('alice', '/cron', 'main', now());
INSERT INTO web_tab_attention (username, path) VALUES ('alice', '/cron/ZGFpbHk')`)
	require.NoError(t, err)
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	paths, err := queryStrings(ctx, store.db, `SELECT 'tab ' || path FROM web_tabs UNION ALL SELECT 'shared ' || path FROM web_shared_tabs
UNION ALL SELECT 'closed ' || path FROM web_closed_tabs UNION ALL SELECT 'attention ' || path FROM web_tab_attention
UNION ALL SELECT username || ' ' || last::text || ' ' || shared_order::text || ' ' || card_order::text || ' ' || navigation FROM web_tab_states ORDER BY 1`, "tab paths")
	require.NoError(t, err)
	require.Equal(t, []string{
		`alice {"dev": "/cronjobs", "ops": "/routines/ZGFpbHk", "main": "/routines"} ["/routines/ZGFpbHk", "/agents"] ["main", "parked\n/routines", "parked\n/cronjobs"] /routines/ZGFpbHk`,
		"attention /routines/ZGFpbHk",
		`bob {} [] [] `,
		"closed /routines",
		"shared /agents",
		"shared /routines/ZGFpbHk",
		"tab /cronjobs",
		"tab /routines",
		"tab /routines/ZGFpbHk",
	}, paths)
}

func TestDropVoiceMigration(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	ctx := t.Context()
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 115)
	require.NoError(t, err)

	_, err = store.db.ExecContext(ctx, `INSERT INTO voice_settings (username, agent, voice, transcript) VALUES ('alice', 'main', 'cedar', TRUE);
INSERT INTO web_tab_states (username, space, revision, last, shared_order, card_order, navigation) VALUES
    ('alice', 'main', 1, '{"main": "/voice"}', '["/voice"]', E'["local\\n/voice"]', '/voice'),
    ('bob', 'main', 1, '{"main": "/voice", "ops": "/agents", "dev": "/voices"}', '["/agents", "/voice", "/voice/x"]', E'["main", "local\\n/voice", "remote\\n/voice", "local\\n/voices", "/voice"]', '/agents');
INSERT INTO web_tabs (username, path, position, space) VALUES ('bob', '/voice', 0, 'main'), ('bob', '/voice/x', 1, 'main'), ('bob', '/voices', 2, 'main');
INSERT INTO web_shared_tabs (space, path) VALUES ('main', '/voice'), ('main', '/agents');
INSERT INTO web_closed_tabs (username, path, space, closed_at) VALUES ('bob', '/voice', 'main', now()), ('bob', '/agents', 'main', now());
INSERT INTO web_tab_attention (username, path) VALUES ('bob', '/voice'), ('bob', '/agents');
INSERT INTO web_tab_lists (username, list, server, path, space, position) VALUES ('bob', 'recent', 'local', '/voice', 'main', 0), ('bob', 'recent', 'remote', '/voice', 'main', 1), ('bob', 'recent', 'local', '/agents', 'main', 2);
INSERT INTO behavior_settings (document) VALUES ('{"embedded_agents": {"main": {"disabled": true, "model": ""}, "voice": {"disabled": false, "model": "gpt-x"}}, "title_model": "gpt-y"}')`)
	require.NoError(t, err)
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	tables, err := queryStrings(ctx, store.db, `SELECT tablename FROM pg_tables WHERE schemaname = current_schema() AND tablename = 'voice_settings'`, "voice tables")
	require.NoError(t, err)
	require.Empty(t, tables)

	paths, err := queryStrings(ctx, store.db, `WITH entries AS (
    SELECT 'tab ' || path AS entry FROM web_tabs UNION ALL SELECT 'shared ' || path FROM web_shared_tabs
    UNION ALL SELECT 'closed ' || path FROM web_closed_tabs UNION ALL SELECT 'attention ' || path FROM web_tab_attention
    UNION ALL SELECT 'list ' || server || ' ' || path FROM web_tab_lists
    UNION ALL SELECT username || ' ' || last::text || ' ' || shared_order::text || ' ' || card_order::text || ' ' || navigation FROM web_tab_states
    UNION ALL SELECT document::text FROM behavior_settings
)
SELECT entry FROM entries ORDER BY entry COLLATE "C"`, "voice paths")
	require.NoError(t, err)
	require.Equal(t, []string{
		`alice {} [] [] `,
		"attention /agents",
		`bob {"dev": "/voices", "ops": "/agents"} ["/agents", "/voice/x"] ["main", "local\n/voices"] /agents`,
		"closed /agents",
		"list local /agents",
		"shared /agents",
		"tab /voice/x",
		"tab /voices",
		`{"title_model": "gpt-y", "embedded_agents": {"main": {"model": "", "disabled": true}}}`,
	}, paths)
}

func TestRoutineNamesMigration(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	ctx := t.Context()
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 115)
	require.NoError(t, err)

	const (
		run, oneOff, report = "cron:agents/a.md#1h:20260101T000000Z:x", "one-off-cron:agents/a.md#1h:20260102T000000Z:y", "web:cron:agents/a.md#1h:20260101T000000Z:x"
		routine             = `{"RelativePath":"agents/a.md#1h","Agent":"a","RanAt":"2026-01-01T00:00:00Z","Space":"Ops"}`
	)

	inbound := `{"Source":"system","Text":"run","Cronjob":` + routine + `}`
	for _, seed := range []struct {
		statement string
		args      []any
	}{
		{`INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, cron_path, cron_run_at_unix_ns, producer_inbound_json) VALUES
    ($1, 'a', 'cron', 'cron_run', FALSE, 'agents/a.md#1h', 1767225600000000000, $3::json),
    ($2, 'a', 'cron', 'one_off_cron_run', FALSE, 'agents/a.md#1h', 1767312000000000000, '{"Source":"system"}')`, []any{run, oneOff, inbound}},
		{`INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, cron_run) VALUES ($1, 'a', 'cron', 'web_chat', TRUE, $2)`, []any{report, run}},
		{`INSERT INTO schedules (id, cron_path, next_due_unix_ns, origin) VALUES ('agents/a.md#1h', 'agents/a.md#1h', 1, '[ScheduledMessage source=Cron principal="Ann"]')`, nil},
		{`UPDATE conversations SET goal_origin = '[Cron]' WHERE conversation_id = $1`, []any{report}},
		{`INSERT INTO entries (conversation_id, entry_json, entry_timestamp) VALUES ($1, '{"type":"message","role":"user","content":"run"}', '2026-01-01T00:00:00Z')`, []any{run}},
		{`INSERT INTO shadow_turns (child_conversation_id, parent_source_conversation_id, parent_turn_id, inbound_json) VALUES ($1, $2, 'turn', $3::json)`, []any{report, run, inbound}},
	} {
		_, err = store.db.ExecContext(ctx, seed.statement, seed.args...)
		require.NoError(t, err)
	}

	require.NoError(t, store.PutThreadQueueItem("follow-up", &protocol.ThreadQueueItem{ConversationID: run, Message: "again"}))
	_, err = store.db.ExecContext(ctx, `UPDATE turns SET inbound_json = $1::json, outbound_json = $2::json, origin = '[ScheduledMessage source=Cron]' WHERE input_id = 'follow-up'`,
		`{"Source":"system","Text":"again","Kind":"enqueue","Cronjob":`+routine+`}`, `{"Text":"done","Cronjob":`+routine+`}`)
	require.NoError(t, err)

	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	rows, err := queryStrings(ctx, store.db, `SELECT concat_ws(' ', conversation_id, kind, created_by, routine_path, routine_run_at_unix_ns, routine_run, NULLIF(goal_origin, '')) FROM conversations
UNION ALL SELECT concat_ws(' ', 'schedule', routine_path, origin) FROM schedules
UNION ALL SELECT concat_ws(' ', 'turn', inbound_json->'Routine'->>'Agent', inbound_json->'Cronjob', outbound_json->'Routine'->>'Space', outbound_json->'Cronjob', origin) FROM turns
UNION ALL SELECT concat_ws(' ', 'shadow', inbound_json->'Routine'->>'Agent', inbound_json->'Cronjob') FROM shadow_turns
UNION ALL SELECT concat_ws(' ', 'producer', producer_inbound_json->'Routine'->>'Agent', producer_inbound_json->'Cronjob') FROM conversations WHERE producer_inbound_json IS NOT NULL
UNION ALL SELECT 'index ' || to_regclass('conversations_routine_runs')::text ORDER BY 1`, "renamed routine facts")
	require.NoError(t, err)
	require.Equal(t, []string{
		"cron:agents/a.md#1h:20260101T000000Z:x routine_run routine agents/a.md#1h 1767225600000000000",
		"index conversations_routine_runs",
		"one-off-cron:agents/a.md#1h:20260102T000000Z:y one_off_routine_run routine agents/a.md#1h 1767312000000000000",
		"producer",
		"producer a",
		`schedule agents/a.md#1h [ScheduledMessage source=Routine principal="Ann"]`,
		"shadow a",
		"turn a Ops [ScheduledMessage source=Routine]",
		"web:cron:agents/a.md#1h:20260101T000000Z:x web_chat routine cron:agents/a.md#1h:20260101T000000Z:x [Routine]",
	}, rows)

	queue, err := store.ThreadQueueForConversation(run)
	require.NoError(t, err)
	require.Len(t, queue, 1)
	require.Equal(t, &protocol.RoutineMessage{RelativePath: "agents/a.md#1h", Agent: "a", RanAt: "2026-01-01T00:00:00Z", Space: "Ops"}, queue[0].Inbound.Routine, "a waiting follow-up of an old run still carries its routine")

	sources, err := store.RoutineRunSources(ctx)
	require.NoError(t, err)
	require.Equal(t, []RoutineRunSource{{ConversationID: run, SourceConversationID: run, Run: protocol.RoutineRun{Path: "agents/a.md#1h", At: time.Unix(0, 1767225600000000000).UTC()}}}, sources, "an old run still lists under its own ID")

	for _, id := range []string{run, report} {
		path, isRun, err := store.RoutineRun(ctx, id)
		require.NoError(t, err)
		require.Equal(t, "agents/a.md#1h", path, id)
		require.Equal(t, id == run, isRun, id)
	}

	_, err = (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 115)
	require.NoError(t, err)

	restored, err := queryStrings(ctx, store.db, `SELECT concat_ws(' ', conversation_id, kind, created_by, cron_path, cron_run, NULLIF(goal_origin, '')) FROM conversations
UNION ALL SELECT concat_ws(' ', 'schedule', cron_path, origin) FROM schedules
UNION ALL SELECT concat_ws(' ', 'turn', inbound_json->'Cronjob'->>'Agent', inbound_json->'Routine', outbound_json->'Cronjob'->>'Space', origin) FROM turns
UNION ALL SELECT concat_ws(' ', 'shadow', inbound_json->'Cronjob'->>'Agent') FROM shadow_turns
UNION ALL SELECT concat_ws(' ', 'producer', producer_inbound_json->'Cronjob'->>'Agent') FROM conversations WHERE producer_inbound_json IS NOT NULL
UNION ALL SELECT 'index ' || to_regclass('conversations_cron_runs')::text ORDER BY 1`, "restored cron facts")
	require.NoError(t, err)
	require.Equal(t, []string{
		"cron:agents/a.md#1h:20260101T000000Z:x cron_run cron agents/a.md#1h",
		"index conversations_cron_runs",
		"one-off-cron:agents/a.md#1h:20260102T000000Z:y one_off_cron_run cron agents/a.md#1h",
		"producer",
		"producer a",
		`schedule agents/a.md#1h [ScheduledMessage source=Cron principal="Ann"]`,
		"shadow a",
		"turn a Ops [ScheduledMessage source=Cron]",
		"web:cron:agents/a.md#1h:20260101T000000Z:x web_chat cron cron:agents/a.md#1h:20260101T000000Z:x [Cron]",
	}, restored)
}

func TestDropTopShelfMigration(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	ctx := t.Context()
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 101)
	require.NoError(t, err)

	_, err = store.db.ExecContext(ctx, `INSERT INTO web_tab_states (username, space, revision, shelf_collapsed, folds) VALUES ('alice', 'main', 1, TRUE, '{"@work": true}');
INSERT INTO web_search_pages (id, name, active) VALUES ('00000000-0000-0000-0000-000000000001', 'Shelved', ''), ('00000000-0000-0000-0000-000000000002', 'Kept', '');
INSERT INTO web_searches (page, position, id, name, query) VALUES ('00000000-0000-0000-0000-000000000001', 1, '1', '', 'q');
INSERT INTO web_top_shelves (username, position, server, space, path, search_page) VALUES ('alice', 0, '', 'main', '/search/1', '00000000-0000-0000-0000-000000000001');
INSERT INTO web_tabs (username, path, position, space, search_page) VALUES ('alice', '/search/2', 0, 'main', '00000000-0000-0000-0000-000000000002')`)
	require.NoError(t, err)
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	left, err := queryStrings(ctx, store.db, `SELECT table_name || ' ' || column_name FROM information_schema.columns
WHERE table_schema = current_schema() AND (table_name = 'web_top_shelves' OR column_name = 'shelf_collapsed')
UNION ALL SELECT 'page ' || name FROM web_search_pages UNION ALL SELECT 'search ' || query FROM web_searches`, "left over")
	require.NoError(t, err)
	require.Equal(t, []string{"page Kept"}, left)

	layout, err := store.StripLayout(ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"@work": true}, layout.Folded)
}

func TestWebTabAttentionMigration(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	ctx := t.Context()
	recordTestConversations(t, store, "main", map[string]ConversationKind{"a": KindWebChat, "b": KindWebChat})
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 89)
	require.NoError(t, err)

	shape, err := queryStrings(ctx, store.db, `SELECT table_name || ' ' || column_name || ' ' || is_nullable FROM information_schema.columns
WHERE table_schema = current_schema() AND (table_name IN ('web_tab_mutes', 'web_tab_attention') OR table_name IN ('web_tabs', 'web_closed_tabs') AND column_name IN ('unread_at', 'marked_unread', 'position', 'group_id'))
ORDER BY table_name COLLATE "C", column_name`, "attention shape")
	require.NoError(t, err)
	require.Equal(t, []string{"web_tab_mutes conversation_id NO", "web_tab_mutes until YES", "web_tab_mutes username NO", "web_tabs group_id NO", "web_tabs marked_unread NO", "web_tabs position YES", "web_tabs unread_at YES"}, shape)

	_, err = store.db.ExecContext(ctx, `INSERT INTO web_tab_states (username, revision, space, viewing, viewed_at) VALUES ('alice', 2, 'main', 'a', now());
INSERT INTO web_tab_mutes (username, conversation_id, until) VALUES ('alice', 'a', NULL), ('alice', 'b', now() - interval '1 day');
INSERT INTO web_tabs (username, path, conversation_id, position, space, unread_at, marked_unread) VALUES
    ('alice', '/s/YQ', 'a', 0, 'main', now(), FALSE), ('alice', '/cron', NULL, 1, 'main', NULL, FALSE), ('alice', '/s/Yg', 'b', NULL, '', now(), TRUE)`)
	require.NoError(t, err)
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	state, revision, err := store.TabState(ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, []Tab{{Path: "/s/YQ", Space: "main"}, {Path: "/routines", Space: "main"}}, state.Tabs)
	require.Equal(t, int64(2), revision)

	left, err := queryStrings(ctx, store.db, `SELECT 'attention ' || path FROM web_tab_attention
UNION ALL SELECT 'tab ' || path FROM web_tabs WHERE position IS NULL
UNION ALL SELECT 'view ' || viewing FROM web_tab_states WHERE viewing <> '' OR viewed_at IS NOT NULL`, "left over")
	require.NoError(t, err)
	require.Empty(t, left, "no mark, mute, ghost tab or view of a session is left")
}

func TestMessageSearchMigrationSharesExtension(t *testing.T) {
	base, err := url.Parse(os.Getenv("ROCKETCLAW_TEST_DATABASE_URL"))
	require.NoError(t, err)
	cfg, err := pgx.ParseConfig(base.String())
	require.NoError(t, err)

	admin := stdlib.OpenDB(*cfg)

	t.Cleanup(func() { require.NoError(t, admin.Close()) })

	name := "t_" + strings.ToLower(rand.Text())
	_, err = admin.ExecContext(t.Context(), `CREATE DATABASE `+name)
	require.NoError(t, err)

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

		_, err = db.ExecContext(t.Context(), `CREATE TABLE pg_migrations (id text PRIMARY KEY, applied_at timestamptz)`)
		require.NoError(t, err)
		barrier, err := db.BeginTx(t.Context(), nil)
		require.NoError(t, err)

		t.Cleanup(func() { _ = barrier.Rollback() })

		_, err = barrier.ExecContext(t.Context(), `LOCK TABLE pg_migrations IN SHARE MODE`)
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

func TestMessageSearchSchema(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()
	_, err := store.db.ExecContext(ctx, `
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed) VALUES ('chat', 'main', '', 'web_chat', TRUE);
INSERT INTO entries (conversation_id, entry_json, entry_timestamp, search_text)
SELECT 'chat', '{}', '', '0 0 ["user","",' || to_json(md5(n::text)) || ',' || to_json(md5(n::text)) || ']' FROM generate_series(1, 4096) n;
INSERT INTO turns (id, conversation_id, inbound_json, output_trace_json, history_anchor_id, created_at_unix_ns, updated_at_unix_ns, phase, terminal, search_text)
SELECT 'turn-' || n, 'chat', '{}', '[]', n, 1, 1, 'done', 'stopped', '0 0 ["user","",' || to_json(md5(n::text)) || ',' || to_json(md5(n::text)) || ']' FROM generate_series(1, 4096) n;
INSERT INTO history_links (conversation_id, entry_id, parent_entry_id) SELECT conversation_id, id, lag(id) OVER (ORDER BY id) FROM entries ORDER BY id;
ANALYZE entries;
ANALYZE turns;
ANALYZE history_links;
SELECT gin_clean_pending_list('entries_search_text'::regclass), gin_clean_pending_list('turns_search_text'::regclass);
SET enable_seqscan = off;`)
	require.NoError(t, err)

	plan, err := queryStrings(ctx, store.db, `EXPLAIN `+searchMessagesSQL, "message search plan", pgx.QueryExecModeExec, []string{"%abc%"})
	require.NoError(t, err)
	require.Contains(t, strings.Join(plan, "\n"), "entries_search_text")
	require.Contains(t, strings.Join(plan, "\n"), "turns_search_text")
}

func TestExternalMCPBindingLookupUsesOneIndexedRow(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()
	require.NoError(t, store.RegisterExternalMCPConversation("ticket", "main", &ExternalMCPSessionState{Agent: "job", PrivateConversationID: "private", ManagedConversationID: "managed"}))
	_, err := store.db.ExecContext(ctx, `
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed)
SELECT 'chat-' || n, 'main', '', 'web_chat', TRUE FROM generate_series(1, 4096) n;
ANALYZE conversations;
SET plan_cache_mode = force_generic_plan;`)
	require.NoError(t, err)

	session, found, err := store.ExternalMCPSession("ticket")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, ExternalMCPSessionState{Agent: "job", PrivateConversationID: "private", ManagedConversationID: "managed"}, session)

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

func TestExternalMCPMetadataMigration(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	ctx := t.Context()
	migrations := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}

	recordTestConversations(t, store, "main", map[string]ConversationKind{"mcp": KindWebChat, "shadow": KindWebChat})
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrations, migrate.Down, 79)
	require.NoError(t, err)

	const (
		planted = `{"Source":"external_mcp","Text":"hi","Metadata":{"ticket":"T-1","external_conversation_id":"ext-1","rocketclaw_agent":"intruder","rocketclaw_principal":"alice","source":"api","rocketclaw_raw_text":"planted"}}`
		bare    = `{"Source":"external_mcp","Text":"hi","Metadata":{"ticket":"T-2"}}`
		kept    = `{"Source":"external_mcp","Text":"hi","Metadata":{"rocketclaw_principal":"bob","source":"api"}}`
		web     = `{"Source":"web","Text":"hi","Metadata":{"web_message_id":"w-1","rocketclaw_raw_text":"hi"}}`
	)

	seed := func(query string, args ...any) {
		t.Helper()

		_, err := store.db.ExecContext(ctx, query, args...)
		require.NoError(t, err)
	}
	seed(`UPDATE conversations SET producer_inbound_json = $1 WHERE conversation_id = 'mcp'`, planted)
	seed(`INSERT INTO turns (id, conversation_id, created_at_unix_ns, updated_at_unix_ns, inbound_json) VALUES ('planted', 'mcp', 1, 1, $1), ('bare', 'mcp', 2, 2, $2), ('kept', 'mcp', 3, 3, $3), ('web', 'mcp', 4, 4, $4)`, planted, bare, kept, web)
	seed(`INSERT INTO shadow_turns (child_conversation_id, parent_source_conversation_id, parent_turn_id, inbound_json) VALUES ('shadow', 'mcp', 'planted', $1)`, planted)
	seed(`INSERT INTO steps (conversation_id, key, value) VALUES ('mcp', 'planted/steers', json_build_array(json_build_object('id', 's1', 'inbound', $1::json), json_build_object('id', 's2', 'inbound', $2::json)))`, planted, web)
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	read := func(query string) string {
		t.Helper()

		var raw string
		require.NoError(t, store.db.QueryRowContext(ctx, query).Scan(&raw))

		return raw
	}
	decode := func(raw string) *protocol.InboundMessage {
		t.Helper()

		inbound := new(protocol.InboundMessage)
		require.NoError(t, json.Unmarshal([]byte(raw), inbound))

		return inbound
	}

	var steers []journaledSteer
	require.NoError(t, json.Unmarshal([]byte(read(`SELECT value FROM steps WHERE key = 'planted/steers'`)), &steers))
	require.Len(t, steers, 2)
	require.Equal(t, []string{"s1", "s2"}, []string{steers[0].ID, steers[1].ID})

	stored := map[string]*protocol.InboundMessage{
		"turns":     decode(read(`SELECT inbound_json FROM turns WHERE id = 'planted'`)),
		"shadow":    decode(read(`SELECT inbound_json FROM shadow_turns WHERE child_conversation_id = 'shadow'`)),
		"producer":  decode(read(`SELECT producer_inbound_json FROM conversations WHERE conversation_id = 'mcp'`)),
		"steer":     steers[0].Inbound,
		"web steer": steers[1].Inbound,
	}
	for place, inbound := range stored {
		if place == "web steer" {
			require.Equal(t, map[string]string{"web_message_id": "w-1", protocol.InboundRawTextMetadataKey: "hi"}, inbound.Metadata, place)
			require.Nil(t, inbound.ExternalMCPMetadata, place)

			continue
		}

		require.Equal(t, map[string]string{protocol.InboundPrincipalMetadataKey: "alice", "source": "api"}, inbound.Metadata, place)
		require.Equal(t, map[string]string{rocketclawConversationIDEnv: "mcp", rocketclawMetadataEnvPrefix + "TICKET": "T-1", rocketclawMetadataEnvPrefix + "EXTERNAL_CONVERSATION_ID": "ext-1", rocketclawMetadataEnvPrefix + "ROCKETCLAW_AGENT": "intruder"}, externalMCPMetadataEnv("mcp", inbound.ExternalMCPMetadata), "%s keeps its variables, apart from the planted raw text", place)
	}

	moved := decode(read(`SELECT inbound_json FROM turns WHERE id = 'bare'`))
	require.Equal(t, map[string]string{}, moved.Metadata, "no principal leaves an empty map, not null")
	require.Equal(t, map[string]string{"ticket": "T-2"}, moved.ExternalMCPMetadata)
	require.Equal(t, []string{kept, web}, []string{read(`SELECT inbound_json FROM turns WHERE id = 'kept'`), read(`SELECT inbound_json FROM turns WHERE id = 'web'`)}, "rows with nothing to move are untouched")

	seed(`INSERT INTO turns (id, conversation_id, created_at_unix_ns, updated_at_unix_ns, inbound_json) VALUES
('newer', 'mcp', 5, 5, '{"Source":"external_mcp","Metadata":{"rocketclaw_principal":"alice","rocketclaw_raw_text":"hi","ticket":"own"},"ExternalMCPMetadata":{"ticket":"T-3","note":"n","rocketclaw_principal_name":"Mallory","rocketclaw_sender_agent":"main"}}'),
('system', 'mcp', 6, 6, '{"Source":"system","Metadata":null,"ExternalMCPMetadata":null}')`)

	_, err = (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrations, migrate.Down, 79)
	require.NoError(t, err)

	original := map[string]string{"ticket": "T-1", "external_conversation_id": "ext-1", "rocketclaw_agent": "intruder", protocol.InboundPrincipalMetadataKey: "alice", "source": "api"}

	require.NoError(t, json.Unmarshal([]byte(read(`SELECT value FROM steps WHERE key = 'planted/steers'`)), &steers))

	for place, raw := range map[string]string{
		"turns":    read(`SELECT inbound_json FROM turns WHERE id = 'planted'`),
		"shadow":   read(`SELECT inbound_json FROM shadow_turns WHERE child_conversation_id = 'shadow'`),
		"producer": read(`SELECT producer_inbound_json FROM conversations WHERE conversation_id = 'mcp'`),
	} {
		require.Equal(t, original, decode(raw).Metadata, place)
		require.NotContains(t, raw, "ExternalMCPMetadata", place)
	}

	require.Equal(t, original, steers[0].Inbound.Metadata)
	require.Equal(t, map[string]string{"ticket": "T-2"}, decode(read(`SELECT inbound_json FROM turns WHERE id = 'bare'`)).Metadata)
	require.Equal(t, map[string]string{protocol.InboundPrincipalMetadataKey: "alice", protocol.InboundRawTextMetadataKey: "hi", "ticket": "own", "note": "n"}, decode(read(`SELECT inbound_json FROM turns WHERE id = 'newer'`)).Metadata)
	require.JSONEq(t, `{"Source":"system","Metadata":null}`, read(`SELECT inbound_json FROM turns WHERE id = 'system'`))
}

func TestParkAfterIdleMigration(t *testing.T) {
	t.Parallel()

	store := newColdTestSessionServiceAt(t, t.TempDir())
	ctx := t.Context()
	recordTestConversations(t, store, "main", map[string]ConversationKind{"native": KindWebChat})
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 110)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, kind, created_by, listed, retention, server, remote_session, title, directory, name, last_updated, read_through, synced_at)
VALUES ('opencode:box:waiting', 'main', 'remote', '', FALSE, 'remote', 'box', 'waiting', '', '/srv', '', now(), 5000, now()),
    ('opencode:box:background', 'main', 'remote', '', FALSE, 'remote', 'box', 'background', '', '/srv', '', now(), 6000, now()),
    ('opencode:box:asking', 'main', 'remote', '', FALSE, 'remote', 'box', 'asking', '', '/srv', '', now(), 7000, now());
INSERT INTO web_tab_attention (username, path, conversation_id, parked, until, worked, resume_until, resumes) VALUES
    ('alice', '/oc/waiting', 'opencode:box:waiting', TRUE, 'infinity', FALSE, NULL, FALSE),
    ('alice', '/oc/background', 'opencode:box:background', TRUE, 'infinity', TRUE, NULL, FALSE),
    ('alice', '/oc/asking', 'opencode:box:asking', FALSE, NULL, FALSE, 'infinity', TRUE),
    ('alice', '/s/native', 'native', TRUE, 'infinity', FALSE, NULL, FALSE)`)
	require.NoError(t, err)

	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	thresholds := map[string]*int64{}

	require.NoError(t, readRows(ctx, store.db, `SELECT path, after_idle FROM web_tab_attention`, "thresholds", func(row rowScanner) error {
		var (
			path      string
			afterIdle sql.NullInt64
		)
		if err := row.Scan(&path, &afterIdle); err != nil {
			return fmt.Errorf("scan threshold: %w", err)
		}

		thresholds[path] = nil
		if afterIdle.Valid {
			thresholds[path] = &afterIdle.Int64
		}

		return nil
	}))
	require.Equal(t, map[string]*int64{"/oc/waiting": new(int64(5000)), "/oc/background": new(int64(5999)), "/oc/asking": new(int64(7000)), "/s/native": nil}, thresholds,
		"a park still waiting for its turn waits for the next idle marker, also while a question pauses it; one that only waits on background work ends when that stops")

	require.NoError(t, store.tickParks(ctx))
	require.Contains(t, attentionOf(t, store, "alice", "/oc/waiting"), "parked until=done")
	require.Contains(t, attentionOf(t, store, "alice", "/oc/background"), "until=due", "the park-ending step finds it done")
	require.Contains(t, attentionOf(t, store, "alice", "/oc/asking"), "parked until=done", "a park a question paused comes back once nothing is asked")
}
