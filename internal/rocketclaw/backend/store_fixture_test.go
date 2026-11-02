package backend

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	code := 0

	err := func() (errResult error) {
		base := strings.TrimSpace(os.Getenv("ROCKETCLAW_TEST_DATABASE_URL"))
		if base == "" || os.Getenv("ROCKETCLAW_TEST_MIGRATED_DATABASE_URL") != "" {
			// Unit tests need no PostgreSQL; children reuse their parent's template.
			code = m.Run()
			return nil
		}

		cfg, err := pgx.ParseConfig(base)
		if err != nil {
			return fmt.Errorf("parse fixture database URL: %w", err)
		}

		location, err := url.Parse(base)
		if err != nil {
			return fmt.Errorf("parse fixture database location: %w", err)
		}

		admin := stdlib.OpenDB(*cfg)
		defer func() { errResult = errors.Join(errResult, admin.Close()) }()

		name := "t_" + strings.ToLower(rand.Text())
		if _, err := admin.Exec("CREATE DATABASE " + name + " TEMPLATE template0"); err != nil {
			return fmt.Errorf("create fixture template: %w", err)
		}

		defer func() {
			_, err := admin.Exec("DROP DATABASE " + name)
			errResult = errors.Join(errResult, err)
		}()

		location.Path = "/" + name
		q := location.Query()
		q.Set("options", "-csearch_path=fixture")
		location.RawQuery = q.Encode()

		cfg, err = pgx.ParseConfig(location.String())
		if err != nil {
			return fmt.Errorf("parse fixture template URL: %w", err)
		}

		db := stdlib.OpenDB(*cfg)

		_, err = db.Exec("CREATE SCHEMA fixture")
		if err == nil {
			err = initializeSessionDB(context.Background(), db, slog.New(slog.DiscardHandler))
		}

		if err := errors.Join(err, db.Close()); err != nil {
			return fmt.Errorf("migrate fixture template: %w", err)
		}

		if err := os.Setenv("ROCKETCLAW_TEST_MIGRATED_DATABASE_URL", location.String()); err != nil {
			return fmt.Errorf("remember fixture template: %w", err)
		}
		// Only shadow and search helpers clone it; cold migration tests still
		// use their original constructors. Every test cleanup precedes its drop.
		code = m.Run()

		return nil
	}()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		code = 1
	}

	os.Exit(code)
}

func newMigratedTestSessionServiceAt(t *testing.T, workspace string) *SessionService {
	t.Helper()

	template, err := url.Parse(os.Getenv("ROCKETCLAW_TEST_MIGRATED_DATABASE_URL"))
	require.NoError(t, err)
	cfg, err := pgx.ParseConfig(os.Getenv("ROCKETCLAW_TEST_DATABASE_URL"))
	require.NoError(t, err)

	admin := stdlib.OpenDB(*cfg)
	// Each fixture owns its handle, but no idle admin connection outlives a query.
	admin.SetMaxIdleConns(0)
	t.Cleanup(func() { require.NoError(t, admin.Close()) })

	name := "t_" + strings.ToLower(rand.Text())
	_, err = admin.ExecContext(t.Context(), "CREATE DATABASE "+name+" TEMPLATE "+strings.TrimPrefix(template.Path, "/"))
	require.NoError(t, err)
	// Register before the existing constructor: its Stop runs before this drop.
	t.Cleanup(func() {
		_, err := admin.Exec("DROP DATABASE " + name)
		require.NoError(t, err)
	})

	location := *template
	location.Path = "/" + name
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	require.NoError(t, root.WriteFile(".test-database-url", []byte(location.String()), 0o600))
	require.NoError(t, root.Close())

	return newTestSessionServiceAt(t, workspace)
}

func TestMigratedTestDatabaseMatchesColdAndIsolatesClones(t *testing.T) {
	t.Parallel()

	first := newMigratedTestSessionServiceAt(t, t.TempDir())
	second := newMigratedTestSessionServiceAt(t, t.TempDir())
	adminConfig, err := pgx.ParseConfig(os.Getenv("ROCKETCLAW_TEST_DATABASE_URL"))
	require.NoError(t, err)

	admin := stdlib.OpenDB(*adminConfig)

	t.Cleanup(func() { require.NoError(t, admin.Close()) })

	name := "t_" + strings.ToLower(rand.Text())
	_, err = admin.ExecContext(t.Context(), "CREATE DATABASE "+name+" TEMPLATE template0")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := admin.Exec("DROP DATABASE " + name)
		require.NoError(t, err)
	})

	location, err := url.Parse(os.Getenv("ROCKETCLAW_TEST_MIGRATED_DATABASE_URL"))
	require.NoError(t, err)

	coldLocation := *location
	coldLocation.Path = "/" + name
	cfg, err := pgx.ParseConfig(coldLocation.String())
	require.NoError(t, err)

	db := stdlib.OpenDB(*cfg)
	_, err = db.ExecContext(t.Context(), "CREATE SCHEMA fixture")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	require.NoError(t, root.WriteFile(".test-database-url", []byte(coldLocation.String()), 0o600))
	require.NoError(t, root.Close())
	cold := newTestSessionServiceAt(t, workspace)

	// Every definition is compared with a genuine cold startup, including the
	// functions' captured search_path. No schema is renamed after migration.
	const catalog = `WITH relations AS (
    SELECT c.oid, c.relname, c.relkind, c.relpersistence, c.reloptions, c.relrowsecurity
    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema()
), definitions AS (
    SELECT 'relation:' || relname || ':' || relkind::text || ':' || relpersistence::text || ':' || COALESCE(reloptions::text, '') || ':' || relrowsecurity::text AS definition FROM relations
    UNION ALL
    SELECT 'column:' || r.relname || ':' || a.attnum || ':' || a.attname || ':' || format_type(a.atttypid, a.atttypmod) || ':' || a.attnotnull::text || ':' || a.attidentity::text || ':' || a.attgenerated::text || ':' || COALESCE(pg_get_expr(d.adbin, d.adrelid), '')
    FROM relations r JOIN pg_attribute a ON a.attrelid = r.oid LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum WHERE a.attnum > 0 AND NOT a.attisdropped
    UNION ALL
    SELECT 'constraint:' || r.relname || ':' || c.conname || ':' || pg_get_constraintdef(c.oid, true) FROM relations r JOIN pg_constraint c ON c.conrelid = r.oid
    UNION ALL
    SELECT 'index:' || r.relname || ':' || pg_get_indexdef(i.indexrelid) FROM relations r JOIN pg_index i ON i.indrelid = r.oid
    UNION ALL
    SELECT 'trigger:' || r.relname || ':' || pg_get_triggerdef(t.oid, true) FROM relations r JOIN pg_trigger t ON t.tgrelid = r.oid WHERE NOT t.tgisinternal
    UNION ALL
    SELECT 'function:' || p.proname || ':' || pg_get_functiondef(p.oid) || ':' || COALESCE(p.proconfig::text, '') FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = current_schema()
    UNION ALL
    SELECT 'sequence:' || r.relname || ':' || s.seqstart || ':' || s.seqincrement || ':' || s.seqmax || ':' || s.seqmin || ':' || s.seqcache || ':' || s.seqcycle::text FROM relations r JOIN pg_sequence s ON s.seqrelid = r.oid
)
SELECT definition FROM definitions ORDER BY definition`

	want, err := queryStrings(t.Context(), cold.db, catalog, "cold fixture catalog")
	require.NoError(t, err)
	require.NotEmpty(t, want)
	ledger, err := queryStrings(t.Context(), cold.db, "SELECT id FROM pg_migrations ORDER BY id", "cold fixture ledger")
	require.NoError(t, err)
	require.Len(t, ledger, sessionMigrationCount(t))

	for _, store := range []*SessionService{first, second} {
		got, err := queryStrings(t.Context(), store.db, catalog, "cloned fixture catalog")
		require.NoError(t, err)
		require.Equal(t, want, got)
		got, err = queryStrings(t.Context(), store.db, "SELECT id FROM pg_migrations ORDER BY id", "cloned fixture ledger")
		require.NoError(t, err)
		require.Equal(t, ledger, got)

		var schema string
		require.NoError(t, store.db.QueryRowContext(t.Context(), "SELECT current_schema()").Scan(&schema))
		require.Equal(t, "fixture", schema)
	}

	for i, store := range []*SessionService{first, second} {
		agent := []string{"alpha", "beta"}[i]
		require.NoError(t, store.UpsertThread("same", ThreadState{Agent: agent}))
	}

	for i, store := range []*SessionService{first, second} {
		thread, found, err := store.Thread("same")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, []string{"alpha", "beta"}[i], thread.Agent)
	}

	_, found, err := cold.Thread("same")
	require.NoError(t, err)
	require.False(t, found)

	listeners := make([]*sql.Conn, 0, 2)

	t.Cleanup(func() {
		for _, conn := range listeners {
			require.NoError(t, conn.Close())
		}
	})

	var channel string

	for _, store := range []*SessionService{first, second} {
		conn, err := store.db.Conn(t.Context())

		require.NoError(t, err)

		require.NoError(t, conn.QueryRowContext(t.Context(), "SELECT 'rocketclaw_transcript_' || md5(current_schema())").Scan(&channel))
		_, err = conn.ExecContext(t.Context(), "LISTEN "+pgx.Identifier{channel}.Sanitize())
		require.NoError(t, err)

		listeners = append(listeners, conn)
	}

	_, err = first.AppendEntryID(t.Context(), "same", testSessionEntry("SECRET", "answer"))
	require.NoError(t, err)
	_, err = first.db.ExecContext(t.Context(), "SELECT pg_notify($1, 'first clone barrier')", channel)
	require.NoError(t, err)
	_, err = second.db.ExecContext(t.Context(), "SELECT pg_notify($1, 'second clone barrier')", channel)
	require.NoError(t, err)

	var notifications [2]*pgconn.Notification

	for i, conn := range listeners {
		require.NoError(t, conn.Raw(func(raw any) error {
			var err error

			notifications[i], err = raw.(*stdlib.Conn).Conn().WaitForNotification(t.Context())
			if err != nil {
				return fmt.Errorf("wait for fixture notification: %w", err)
			}

			return nil
		}))
	}

	require.Equal(t, "second clone barrier", notifications[1].Payload, "the first clone's earlier write cannot notify the second")
	require.Equal(t, channel, notifications[0].Channel)
	require.NotEqual(t, "first clone barrier", notifications[0].Payload, "the entry write must notify before its barrier")

	var change protocol.ConversationChange
	require.NoError(t, json.Unmarshal([]byte(notifications[0].Payload), &change))
	require.Equal(t, "same", change.ConversationID)
	require.NotEmpty(t, change.Revision)
	require.NotContains(t, notifications[0].Payload, "SECRET")
	entries, err := second.ObserveEntries(t.Context(), "same")
	require.NoError(t, err)
	require.Empty(t, entries)
}
