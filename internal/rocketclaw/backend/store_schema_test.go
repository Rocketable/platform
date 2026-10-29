package backend

import (
	"context"
	"crypto/rand"
	"database/sql"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
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
	// The planner reads only the statistics set here. Autovacuum, woken by these writes, would
	// change them mid-test, and the read count with them, though never with entries.
	_, err := store.db.ExecContext(ctx, `
ALTER TABLE conversations SET (autovacuum_enabled = false);
ALTER TABLE entries SET (autovacuum_enabled = false);
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
INSERT INTO entries (conversation_id, entry_json, entry_timestamp)
SELECT 'thread-' || n, '{"type":"mcp_external_metadata","version":4}', '2000-01-01T00:00:00Z' FROM generate_series(1, 4096) n;
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
FROM pg_prepared_statements WHERE statement LIKE '%mcp_external_metadata%' AND statement NOT LIKE '%pg_prepared_statements%'`, "metadata lookup statement")
	require.NoError(t, err)
	require.Len(t, queries, 1)
	plan, err := queryStrings(ctx, store.db, queries[0], "metadata lookup plan")
	require.NoError(t, err)
	require.Contains(t, strings.Join(plan, "\n"), "session_entries_mcp_metadata")
	require.NotContains(t, strings.Join(plan, "\n"), "Sort")

	for _, line := range plan {
		if strings.Contains(line, "Filter:") {
			require.NotContains(t, line, "entry_json", "the metadata index finds entries without filtering them")
		}
	}

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

	for _, outcome := range []string{"success", "cancel", "cancel waiting", "connection loss"} {
		t.Run(outcome, func(t *testing.T) {
			// The baseline's extension lock spans schemas, so unrelated startups need a different database.
			name := "t_" + strings.ToLower(rand.Text())
			_, err := admin.ExecContext(t.Context(), `CREATE DATABASE `+name)

			require.NoError(t, err)
			defer func() {
				_, err := admin.ExecContext(context.Background(), `DROP DATABASE `+name+` WITH (FORCE)`)
				require.NoError(t, err)
			}()

			location := *base
			location.Path = "/" + name
			query := location.Query()
			query.Set("options", "-csearch_path="+name)
			location.RawQuery = query.Encode()
			dsn := location.String()
			cfg, err := pgx.ParseConfig(dsn)
			require.NoError(t, err)

			db := stdlib.OpenDB(*cfg)
			defer func() { require.NoError(t, db.Close()) }()

			_, err = db.ExecContext(t.Context(), `CREATE SCHEMA `+name)
			require.NoError(t, err)

			// The ledger exists, so the barrier can hold the first caller at its baseline's ledger write.
			_, err = db.ExecContext(t.Context(), `CREATE TABLE pg_migrations (id text PRIMARY KEY, applied_at timestamptz)`)
			require.NoError(t, err)
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

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	cfg, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)

	db := stdlib.OpenDB(*cfg)
	defer func() { require.NoError(t, db.Close()) }()

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
	require.False(t, exists, "both callers must wait before creating the ledger")
	require.NoError(t, barrier.Rollback())
	require.NoError(t, callers.Wait())

	var count int
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_migrations`).Scan(&count))
	require.Equal(t, sessionMigrationCount(t), count)
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

// A migration commits with its ledger row or not at all, and a ledger row this binary does not know stops startup.
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

// A store the migrations through 059_legacy_handoff.sql built adopts the baseline in place, keeping
// its rows and the later migrations it ran and dropping the legacy_ tables; a store that stopped
// before 059 is refused unchanged.
func TestInitializeSessionDBAdoptsReleasedStore(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
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

	// The later migrations already ran here; running them again would fail on what they created.
	release("001_init.sql", "059_legacy_handoff.sql", "060_web_tab_rows.sql", "061_drop_web_tab_state_blob.sql", "062_drop_web_tab_placement.sql", "063_drop_web_tab_pin.sql", "064_web_shared_tabs.sql", "065_web_tab_mutes.sql", "066_hosted_page_conversation.sql", "068_web_tab_search.sql", "069_external_mcp_web_chats.sql", "070_web_shared_order.sql", "071_drop_goal_check_script.sql", "072_remote_asking.sql", "073_drop_spawned_by.sql", "074_session_reply_jobs.sql")
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	ledger, err := queryStrings(ctx, store.db, `SELECT id FROM pg_migrations ORDER BY id`, "ledger")
	require.NoError(t, err)
	require.Equal(t, []string{"001_baseline.sql", "060_web_tab_rows.sql", "061_drop_web_tab_state_blob.sql", "062_drop_web_tab_placement.sql", "063_drop_web_tab_pin.sql", "064_web_shared_tabs.sql", "065_web_tab_mutes.sql", "066_hosted_page_conversation.sql", "068_web_tab_search.sql", "069_external_mcp_web_chats.sql", "070_web_shared_order.sql", "071_drop_goal_check_script.sql", "072_remote_asking.sql", "073_drop_spawned_by.sql", "074_session_reply_jobs.sql"}, ledger)

	legacy, err := queryStrings(ctx, store.db, `SELECT tablename FROM pg_tables WHERE schemaname = current_schema() AND starts_with(tablename, 'legacy_')`, "legacy tables")
	require.NoError(t, err)
	require.Empty(t, legacy)

	chats, err := queryStrings(ctx, store.db, `SELECT conversation_id FROM conversations`, "conversations")
	require.NoError(t, err)
	require.Equal(t, []string{"chat"}, chats)
}

func TestDropWebTabStateBlobMigration(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	// Back to before 061, with alice's tabs still the blob 060's release unpacks at startup.
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

	store := newTestSessionService(t)
	ctx := t.Context()
	// Back to before 063, with alice's first tab pinned.
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 63)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `INSERT INTO web_tab_states (username, space, revision) VALUES ('alice', 'main', 1);
INSERT INTO web_tabs (username, path, position, pinned, group_id, space) VALUES ('alice', '/search', 0, TRUE, 'g', 'main'), ('alice', '/cron', 1, FALSE, '', 'main')`)
	require.NoError(t, err)
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	state, _, err := store.TabState(ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, []Tab{{Path: "/search", Group: "g", Space: "main"}, {Path: "/cron", Space: "main"}}, state.Tabs, "a pinned tab stays open in place as an ordinary tab")
}

func TestDropSpawnedByMigration(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	// Back to before 073, with a session another session created.
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

func TestSlackThreadsMigration(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	ctx := t.Context()
	// Back to before 069, with an External MCP session, a cron report, and a mention each on a Slack thread.
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 69)
	require.NoError(t, err)
	require.NoError(t, store.RegisterExternalMCPConversation("ticket", "main", &ExternalMCPSessionState{Agent: "main", PrivateConversationID: "private", ManagedConversationID: "mcp"}))
	require.NoError(t, store.UpsertThread("cron", ThreadState{Agent: "main", CreatedBy: ThreadCreatedByCron}))
	require.NoError(t, store.UpsertThread("mention", ThreadState{Agent: "main"}))
	_, err = store.db.ExecContext(ctx, `UPDATE conversations SET kind = 'slack_thread', slack_channel = 'C1', slack_thread = '1.' || length(conversation_id), external_channel = '#ops' WHERE conversation_id IN ('mcp', 'cron', 'mention')`)
	require.NoError(t, err)
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	for id, want := range map[string]ConversationKind{"mcp": KindWebChat, "cron": KindWebChat, "mention": KindSlackThread} {
		kind, ok, err := store.ConversationKind(ctx, id)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, want, kind, id)

		_, thread, err := store.SlackThread(ctx, id)
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

	store := newTestSessionService(t)
	ctx := t.Context()
	// Back to before 064: main's last tab is open, ops's last tab is closed.
	_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecVersionContext(ctx, store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 64)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `INSERT INTO web_tab_states (username, space, revision) VALUES ('alice', 'main', 1);
INSERT INTO web_tabs (username, path, position, space, last) VALUES ('alice', '/search', 0, 'main', TRUE), ('alice', '/cron', NULL, 'ops', TRUE), ('alice', '/agents', NULL, 'ops', FALSE)`)
	require.NoError(t, err)
	require.NoError(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	state, _, err := store.TabState(ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, TabState{Tabs: []Tab{{Path: "/search", Space: "main"}}, Space: "main", Last: map[string]string{"main": "/search", "ops": "/cron"}, SharedOrder: []string{}}, state)

	rows, err := queryStrings(ctx, store.db, `SELECT path FROM web_tabs ORDER BY path`, "tab rows")
	require.NoError(t, err)
	require.Equal(t, []string{"/search"}, rows, "closed tabs kept only as a space's last tab are dropped")
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

		// Hold the first caller at its baseline's ledger write, after it creates the extension; the
		// second then waits for the extension.
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

// An External MCP call for an existing external ID finds its binding in one row through the
// external ID's unique index.
func TestExternalMCPBindingLookupUsesOneIndexedRow(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
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
