package backend

import (
	"context"
	"database/sql"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	migrate "github.com/rubenv/sql-migrate"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

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

			_, err = barrier.ExecContext(t.Context(), `LOCK TABLE thread_queue IN ACCESS EXCLUSIVE MODE`)
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			var first errgroup.Group
			first.Go(func() error {
				store, err := NewSessionServiceIn(ctx, dsn, slog.New(slog.DiscardHandler))
				if err != nil {
					return err
				}

				return store.Stop()
			})
			// PostgreSQL proves the first startup reached DDL, not merely a goroutine.
			var pid int

			require.Eventually(t, func() bool {
				return db.QueryRowContext(t.Context(), `SELECT a.pid FROM pg_stat_activity a JOIN pg_locks l ON a.pid=l.pid WHERE l.relation='thread_queue'::regclass AND NOT l.granted AND a.query LIKE '%ADD COLUMN%'`).Scan(&pid) == nil
			}, 5*time.Second, time.Millisecond)

			ctxSecond, cancelSecond := context.WithCancel(t.Context())
			defer cancelSecond()

			var second errgroup.Group
			second.Go(func() error {
				store, err := NewSessionServiceIn(ctxSecond, dsn, slog.New(slog.DiscardHandler))
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

			require.NoError(t, barrier.Rollback())

			if outcome == "success" || outcome == "cancel waiting" {
				require.NoError(t, first.Wait())
			}

			if outcome != "cancel waiting" {
				require.NoError(t, second.Wait())
			}

			require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_migrations`).Scan(&n))
			require.Equal(t, 10, n)
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
					store, err := NewSessionServiceIn(t.Context(), dsn, slog.New(slog.DiscardHandler))
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
			require.Equal(t, 10, count)
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
	_, err := store.db.ExecContext(ctx, `DELETE FROM pg_migrations WHERE id IN ('009_session_summaries.sql', '010_slack_channel_facts.sql');
		DROP TABLE session_summaries, slack_channel_facts;
		CREATE FUNCTION reject_migration() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.id='010_slack_channel_facts.sql' THEN RAISE EXCEPTION 'ledger write failed'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER reject_migration BEFORE INSERT ON pg_migrations FOR EACH ROW EXECUTE FUNCTION reject_migration()`)
	require.NoError(t, err)
	// 009 commits; 010's DDL rolls back when its ledger write fails.
	require.Error(t, initializeSessionDB(ctx, store.db, slog.New(slog.DiscardHandler)))

	var n int
	require.NoError(t, store.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_migrations`).Scan(&n))
	require.Equal(t, 9, n)

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
