package backend

import (
	"context"
	"crypto/rand"
	"database/sql"
	"log/slog"
	"net/url"
	"os"
	"slices"
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
	store := newTestSessionService(t)
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()
	_, err := store.db.ExecContext(ctx, `
INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES
    ('parent-history', '{"replay_input":[{"type":"function_call","call_id":"cobalt"},{"type":"function_call","call_id":"amber"},{"type":"function_call","call_id":"missing"}]}', '2000-01-01T00:00:00Z');
INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp)
SELECT conversation_id, '{}', '2000-01-01T00:00:00Z'
FROM (VALUES ('parent-history/amber'), ('parent-history/cobalt')) c(conversation_id), generate_series(1, 4096);
INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp)
SELECT 'unrelated-history-' || n, '{}', '2000-01-01T00:00:00Z' FROM generate_series(1, 4096) n;
ANALYZE session_entries;
SET plan_cache_mode = force_generic_plan;
SELECT pg_stat_force_next_flush();`)
	require.NoError(t, err)

	const historyReads = `SELECT pg_stat_get_tuples_returned('session_entries'::regclass)
    + COALESCE(SUM(pg_stat_get_tuples_returned(indexrelid)), 0)
FROM pg_index WHERE indrelid = 'session_entries'::regclass`

	var before, after int64
	require.NoError(t, store.db.QueryRowContext(ctx, historyReads).Scan(&before))
	ids, err := store.Delegations(ctx, "parent-history", "", 0, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"parent-history/amber", "parent-history/cobalt"}, ids)

	_, err = store.db.ExecContext(ctx, `SELECT pg_stat_force_next_flush()`)
	require.NoError(t, err)
	require.NoError(t, store.db.QueryRowContext(ctx, historyReads).Scan(&after))
	require.Equal(t, int64(3), after-before, "read the parent and only one indexed entry per child history")
}

func TestChatOriginFactsSkipsKnownSourceLookup(t *testing.T) {
	store := newTestSessionService(t)
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()
	_, err := store.db.ExecContext(ctx, `
INSERT INTO session_entries (id, conversation_id, entry_json, entry_timestamp)
VALUES (1000, 'source', '{}', '');
INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp)
SELECT 'unrelated-' || n, '{}', '' FROM generate_series(1, 512) n;
INSERT INTO managed_conversations (conversation_id, agent, created_by)
SELECT 'web:origin-' || n, 'sample-agent', 'alice' FROM generate_series(1, 128) n;
INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp)
SELECT conversation_id, '{"sync_source_conversation_id":"source","sync_source_entry_id":1000}', ''
FROM managed_conversations;
ANALYZE session_entries;
SET plan_cache_mode = force_generic_plan;
SELECT pg_stat_force_next_flush();`)
	require.NoError(t, err)

	const sourceReads = `SELECT pg_stat_get_tuples_returned('session_entries_pkey'::regclass)`

	var before, after int64
	require.NoError(t, store.db.QueryRowContext(ctx, sourceReads).Scan(&before))

	seen := 0

	for facts, err := range store.ChatOriginFacts(ctx, "") {
		require.NoError(t, err)
		require.Equal(t, "source", facts.CreatingSource)

		seen++
	}

	require.Equal(t, 128, seen)

	_, err = store.db.ExecContext(ctx, `SELECT pg_stat_force_next_flush()`)
	require.NoError(t, err)
	require.NoError(t, store.db.QueryRowContext(ctx, sourceReads).Scan(&after))
	require.Equal(t, before, after, "an explicit source needs no source-entry lookup")
}

func TestExternalMCPMetadataLookupUsesIndex(t *testing.T) {
	store := newTestSessionService(t)
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()
	_, err := store.db.ExecContext(ctx, `
INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES
    ('with-metadata', '{"type":"mcp_external_metadata","version":1}', '2000-01-01T00:00:00Z'),
    ('with-metadata', '{"type":"mcp_external_metadata","version":2}', '2000-01-01T00:00:00Z'),
    ('other', '{"type":"mcp_external_metadata","version":3}', '2000-01-01T00:00:00Z');
INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp)
SELECT conversation_id, '{"type":"turn"}', '2000-01-01T00:00:00Z'
FROM (VALUES ('with-metadata'), ('without-metadata')) c(conversation_id), generate_series(1, 4096);
ANALYZE session_entries;
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
FROM pg_prepared_statements WHERE statement LIKE 'SELECT id, entry_json FROM session_entries%'`, "metadata lookup statement")
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
	store := newTestSessionService(t)
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()
	_, err := store.db.ExecContext(ctx, `
INSERT INTO managed_conversations (conversation_id, agent, created_by)
SELECT 'unrelated-' || n, 'main', '' FROM generate_series(1, 4096) n;
INSERT INTO managed_conversations (conversation_id, agent, created_by, revert_message_id)
VALUES ('parent', 'main', '', '10:1');
INSERT INTO session_entries (id, conversation_id, entry_json, entry_timestamp) VALUES
    (10, 'parent', '{"replay_input":[{"type":"function_call","call_id":"kept"},{"type":"function_call","call_id":"discarded"}]}', ''),
    (20, 'parent/kept', '{}', ''), (30, 'parent/discarded', '{}', ''), (40, 'ordinary', '{}', '');
ANALYZE managed_conversations;
ANALYZE session_entries;
SET plan_cache_mode = force_generic_plan;`)
	require.NoError(t, err)

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
	require.NotContains(t, strings.Join(plan, "\n"), "Seq Scan on managed_conversations parent")
	t.Log(strings.Join(plan, "\n"))
}

func TestHistoryDeletionUsesConversationRangeIndexes(t *testing.T) {
	store := newTestSessionService(t)
	_, err := store.db.ExecContext(t.Context(), `
INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp)
SELECT 'history-' || n, '{}', '2000-01-01T00:00:00Z' FROM generate_series(1, 4096) n;
INSERT INTO session_summaries (conversation_id, preview, last_updated)
SELECT conversation_id, ''::bytea, '2000-01-01Z'::timestamptz + id * interval '1 second' FROM session_entries;
ANALYZE session_entries;
ANALYZE session_summaries;`)
	require.NoError(t, err)

	for _, table := range []string{"session_entries", "session_summaries"} {
		t.Run(table, func(t *testing.T) {
			plan, err := queryStrings(t.Context(), store.db, `EXPLAIN DELETE FROM `+table+` WHERE `+historyWithDelegations, "history deletion plan", "history-2048")
			require.NoError(t, err)
			require.Contains(t, strings.Join(plan, "\n"), table+"_conversation_id_c")
			require.NotContains(t, strings.Join(plan, "\n"), "Seq Scan")
		})
	}
}

func TestTranscriptChangeUsesSyncSourceIndex(t *testing.T) {
	store := newTestSessionService(t)
	_, err := store.db.ExecContext(t.Context(), `
INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp)
SELECT 'history-' || n, json_build_object('sync_source_entry_id', n), '' FROM generate_series(1, 4096) n;
ANALYZE session_entries;`)
	require.NoError(t, err)

	plan, err := queryStrings(t.Context(), store.db, `EXPLAIN SELECT DISTINCT conversation_id FROM session_entries
WHERE entry_json::jsonb->>'sync_source_entry_id' = $1`, "transcript change plan", "2048")
	require.NoError(t, err)
	require.Contains(t, strings.Join(plan, "\n"), "session_entries_sync_source_id")
	require.NotContains(t, strings.Join(plan, "\n"), "Seq Scan")
}

func TestSessionMigrationsSerializeStartup(t *testing.T) {
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
			require.Equal(t, 33, n)
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
			require.Equal(t, 33, count)
		})
	}
}

func TestSessionMigrationsCancelPlanning(t *testing.T) {
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
	store := newTestSessionService(t)
	ctx := t.Context()
	_, err := store.db.ExecContext(ctx, `DELETE FROM pg_migrations WHERE id IN ('009_session_summaries.sql', '010_slack_channel_facts.sql', '011_last_message_summaries.sql');
		DROP TABLE session_summaries, slack_channel_facts;
		CREATE FUNCTION reject_migration() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.id='010_slack_channel_facts.sql' THEN RAISE EXCEPTION 'ledger write failed'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER reject_migration BEFORE INSERT ON pg_migrations FOR EACH ROW EXECUTE FUNCTION reject_migration()`)
	require.NoError(t, err)
	// 009 commits; 010's DDL rolls back when its ledger write fails.
	require.Error(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	var n int
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_migrations`).Scan(&n))
	require.Equal(t, 31, n)

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
	store := newTestSessionService(t)
	ctx := t.Context()
	_, err := store.db.ExecContext(ctx, `DELETE FROM pg_migrations WHERE id='025_copy_producer_session_tags.sql';
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

	tags, err := queryStrings(ctx, store.db, `SELECT conversation_id || '=' || tags::text FROM session_tags ORDER BY conversation_id`, "session tags")
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

func TestProducerHandoffMigration(t *testing.T) {
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
	require.NoError(t, initializeSessionDB(ctx, db, slog.New(slog.DiscardHandler)))
	dao := stateDAO{db: db}

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
			routing, through, err := dao.producer(t.Context(), tc.id)
			require.NoError(t, err)
			require.Equal(t, tc.through, through)

			if tc.id == "legacy" || tc.id == "onward" || tc.id == "ordinary" {
				require.Nil(t, routing)
			} else {
				require.NotNil(t, routing)
				require.Equal(t, tc.owner, routing.SyncDestination)
			}

			effects, err := dao.producerEffects(t.Context(), tc.id, through)
			require.NoError(t, err)

			ids := make([]int64, 0, len(effects))
			for i := range effects {
				ids = append(ids, effects[i].ID)
			}

			require.True(t, slices.Equal(tc.pending, ids), "pending effect IDs = %v, want %v", ids, tc.pending)
		})
	}

	store := &SessionService{db: db}
	step, found, err := store.LoadTurnStep(ctx, "recoverable", "active/tool/schedule")
	require.NoError(t, err)
	require.True(t, found)
	require.JSONEq(t, `{"output":"scheduled"}`, string(step), "the scheduling result remains available to journal replay")

	_, err = store.finishTurn(ctx, "active", &turnFinish{store: newSessionStore("recoverable", store), outbound: protocol.NewOutboundMessage("recoverable", "")})
	require.NoError(t, err)
	require.NoError(t, store.closeTurn(ctx, "active"))
	ids, err := store.pendingProducerIDs(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"recoverable"}, ids)

	newID, err := store.AppendEntryID(ctx, "recoverable", &harness.SessionEntry{Version: 1, Type: producerResetEntryType})
	require.NoError(t, err)
	require.Greater(t, newID, int64(13))

	effects, err := dao.producerEffects(ctx, "recoverable", 11)
	require.NoError(t, err)
	require.Len(t, effects, 3)

	messages, err := dao.scheduledMessages(ctx, "")
	require.NoError(t, err)
	require.Equal(t, map[string]protocol.ScheduledMessageState{"kept": {ConversationID: "owner", Agent: "selected", Message: "existing schedule", DueAt: time.Unix(0, 123).UTC(), Recurring: true, Interval: 456}}, messages)
}

func TestBackgroundJobsMigration(t *testing.T) {
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
		n, err := set.ExecMaxContext(ctx, store.db, "postgres", source, migrate.Down, 4)
		require.NoError(t, err)
		require.Equal(t, 4, n)

		exists, body := schema()
		require.False(t, exists)
		require.NotContains(t, body, "background_jobs")

		_, err = store.db.ExecContext(ctx, `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ('rolled-back', '{}', '')`)
		require.NoError(t, err, "the restored transcript trigger still runs")

		n, err = set.ExecMaxContext(ctx, store.db, "postgres", source, migrate.Up, 4)
		require.NoError(t, err)
		require.Equal(t, 4, n)

		exists, body = schema()
		require.True(t, exists)
		require.Contains(t, body, "sync_destination")
	}
}

func TestSessionSummaryRecencyIndexMigration(t *testing.T) {
	store := newTestSessionService(t)
	ctx := t.Context()
	source := migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}
	set := migrate.MigrationSet{TableName: "pg_migrations"}

	var index sql.NullString
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT to_regclass('session_summaries_updated')::text`).Scan(&index))
	require.False(t, index.Valid)

	for range 2 {
		n, err := set.ExecMaxContext(ctx, store.db, "postgres", source, migrate.Down, 1)
		require.NoError(t, err)
		require.Equal(t, 1, n)

		var definition string
		require.NoError(t, store.db.QueryRowContext(ctx, `SELECT pg_get_indexdef('session_summaries_updated'::regclass)`).Scan(&definition))
		require.Contains(t, definition, `(last_updated DESC, conversation_id COLLATE "C")`)

		n, err = set.ExecMaxContext(ctx, store.db, "postgres", source, migrate.Up, 1)
		require.NoError(t, err)
		require.Equal(t, 1, n)
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

func TestMessageSearchSchema(t *testing.T) {
	store := newTestSessionService(t)
	store.db.SetMaxOpenConns(1)

	ctx := t.Context()
	_, err := store.db.ExecContext(ctx, `
INSERT INTO session_entries (id, conversation_id, entry_json, entry_timestamp) VALUES (1, 'chat', '{}', ''), (2, 'chat', '{}', '');
INSERT INTO active_turns (id, conversation_id, inbound_json, output_trace_json, history_anchor_id, created_at_unix_ns, updated_at_unix_ns) VALUES ('turn', 'chat', '{}', '[]', 2, 1, 1);
INSERT INTO message_search (conversation_id, entry_id, turn_id, replay_index, part_index, role, text, text_lower) VALUES
    ('chat', 1, NULL, 0, 0, 'user', 'Kept', 'kept'),
    ('chat', 2, NULL, 0, 0, 'user', 'Deleted entry', 'deleted entry'),
    ('chat', NULL, 'turn', 0, 0, 'assistant', 'Deleted turn', 'deleted turn');
DELETE FROM session_entries WHERE id = 2;
DELETE FROM active_turns WHERE id = 'turn';`)
	require.NoError(t, err)

	texts, err := queryStrings(ctx, store.db, `SELECT text FROM message_search`, "message search text")
	require.NoError(t, err)
	require.Equal(t, []string{"Kept"}, texts)

	_, err = store.db.ExecContext(ctx, `INSERT INTO message_search (conversation_id, entry_id, replay_index, part_index, role, text, text_lower) VALUES ('chat', 1, 0, 0, 'user', 'Again', 'again')`)
	require.ErrorContains(t, err, `duplicate key value violates unique constraint "message_search_entry"`)

	_, err = store.db.ExecContext(ctx, `
INSERT INTO session_entries (id, conversation_id, entry_json, entry_timestamp) SELECT n, 'chat', '{}', '' FROM generate_series(3, 4096) n;
INSERT INTO message_search (conversation_id, entry_id, replay_index, part_index, role, text, text_lower)
SELECT 'chat', n, 0, 0, 'user', md5(n::text), md5(n::text) FROM generate_series(3, 4096) n;
ANALYZE message_search;
-- A table this small is cheaper to scan; prove the trigram index serves the LIKE.
SET enable_seqscan = off;`)
	require.NoError(t, err)

	plan, err := queryStrings(ctx, store.db, `EXPLAIN SELECT entry_id FROM message_search WHERE text_lower LIKE '%abc%'`, "message search plan")
	require.NoError(t, err)
	require.Contains(t, strings.Join(plan, "\n"), "message_search_text")
}

func TestSessionMigrationUnlockFailureDiscardsConnection(t *testing.T) {
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
