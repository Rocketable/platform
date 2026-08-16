package harnessbridge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge/harnessbridgetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestOpenDoesNotCopySQLite(t *testing.T) {
	workspace := t.TempDir()
	writeSQLite(t, workspace, 9, true)

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	service, err := NewSessionServiceIn(workspace, config.DefaultRuntimeDir, dsn, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	var n int
	require.NoError(t, service.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM session_entries`).Scan(&n))
	assert.Equal(t, 0, n)
	require.NoError(t, service.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM managed_conversations`).Scan(&n))
	assert.Equal(t, 0, n)
}

func TestOpenIgnoresNonV9SQLite(t *testing.T) {
	workspace := t.TempDir()
	writeSQLite(t, workspace, 8, false)

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	service, err := NewSessionServiceIn(workspace, config.DefaultRuntimeDir, dsn, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	var n int
	require.NoError(t, service.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM session_entries`).Scan(&n))
	assert.Equal(t, 0, n)
}

func TestMigrateSQLiteCopiesV9IDs(t *testing.T) {
	workspace := t.TempDir()
	writeSQLite(t, workspace, 9, true)

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	logger := slog.New(slog.DiscardHandler)

	inserted, err := MigrateSQLite(t.Context(), workspace, config.DefaultRuntimeDir, dsn, logger, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, int64(2), inserted)

	service, err := NewSessionServiceIn(workspace, config.DefaultRuntimeDir, dsn, logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	var id int64
	require.NoError(t, service.db.QueryRowContext(t.Context(), `SELECT id FROM session_entries WHERE conversation_id = 'main'`).Scan(&id))
	assert.Equal(t, int64(7), id)

	next, err := service.AppendEntryID(t.Context(), "main", testSessionEntry("more", "later"))
	require.NoError(t, err)
	assert.Greater(t, next, int64(7))
}

func TestMigrateSQLiteResumesWithoutDeleting(t *testing.T) {
	workspace := t.TempDir()
	writeSQLite(t, workspace, 9, true)

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	logger := slog.New(slog.DiscardHandler)

	inserted, err := MigrateSQLite(t.Context(), workspace, config.DefaultRuntimeDir, dsn, logger, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, int64(2), inserted)

	src, err := sql.Open("sqlite", sessionDBPathIn(workspace, config.DefaultRuntimeDir))
	require.NoError(t, err)
	_, err = src.Exec(`INSERT INTO session_entries (id, conversation_id, entry_json, entry_timestamp) VALUES (8, 'main', '{"version":1}', ?)`, time.Unix(2, 0).UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
	require.NoError(t, src.Close())

	inserted, err = MigrateSQLite(t.Context(), workspace, config.DefaultRuntimeDir, dsn, logger, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, int64(1), inserted)

	service, err := NewSessionServiceIn(workspace, config.DefaultRuntimeDir, dsn, logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	var n int
	require.NoError(t, service.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM session_entries`).Scan(&n))
	assert.Equal(t, 2, n)
	require.NoError(t, service.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM managed_conversations`).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestMigrateSQLiteRejectsNonV9(t *testing.T) {
	workspace := t.TempDir()
	writeSQLite(t, workspace, 8, true)

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	logger := slog.New(slog.DiscardHandler)

	_, err = MigrateSQLite(t.Context(), workspace, config.DefaultRuntimeDir, dsn, logger, io.Discard)
	got, ok := errors.AsType[sqliteVersionError](err)
	require.True(t, ok)
	assert.Equal(t, sqliteVersionError{got: 8, want: sessionDBSchemaVersion}, got)
	assert.Equal(t, "sqlite store has user_version 8, want 9", err.Error())

	service, err := NewSessionServiceIn(workspace, config.DefaultRuntimeDir, dsn, logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	var n int
	require.NoError(t, service.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM session_entries`).Scan(&n))
	assert.Equal(t, 0, n)
}

func TestMigrateSQLiteRejectsDirectory(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(sessionDBPathIn(workspace, config.DefaultRuntimeDir), 0o755))

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	_, err = MigrateSQLite(t.Context(), workspace, config.DefaultRuntimeDir, dsn, slog.New(slog.DiscardHandler), io.Discard)
	require.ErrorIs(t, err, errReadSQLiteVersion)
}

func TestMigrateSQLiteReportsProgressWriteError(t *testing.T) {
	workspace := t.TempDir()
	writeSQLite(t, workspace, 9, true)

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	_, err = MigrateSQLite(t.Context(), workspace, config.DefaultRuntimeDir, dsn, slog.New(slog.DiscardHandler), errWriter{})
	require.ErrorIs(t, err, io.ErrClosedPipe)
}

func TestMigrateSQLiteReportsMidTableProgressWriteError(t *testing.T) {
	workspace := t.TempDir()
	writeSQLite(t, workspace, 9, true)
	src, err := sql.Open("sqlite", sessionDBPathIn(workspace, config.DefaultRuntimeDir))
	require.NoError(t, err)

	for id := 8; id <= 1006; id++ {
		_, err = src.Exec(`INSERT INTO session_entries (id, conversation_id, entry_json, entry_timestamp) VALUES (?, 'main', '{"version":1}', ?)`, id, time.Unix(int64(id), 0).UTC().Format(time.RFC3339Nano))
		require.NoError(t, err)
	}

	require.NoError(t, src.Close())

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	_, err = MigrateSQLite(t.Context(), workspace, config.DefaultRuntimeDir, dsn, slog.New(slog.DiscardHandler), &failAfterWriter{ok: 8})
	require.ErrorIs(t, err, io.ErrClosedPipe)

	db, err := openSessionDB(t.Context(), dsn, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	var n int
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM session_entries`).Scan(&n))
	assert.Equal(t, 1000, n)
}

type failAfterWriter struct{ ok int }

func (w *failAfterWriter) Write(p []byte) (int, error) {
	if w.ok == 0 {
		return 0, io.ErrClosedPipe
	}

	w.ok--

	return len(p), nil
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

func TestMigrateSQLiteReportsProgressEveryThousand(t *testing.T) {
	workspace := t.TempDir()
	writeSQLite(t, workspace, 9, true)
	src, err := sql.Open("sqlite", sessionDBPathIn(workspace, config.DefaultRuntimeDir))
	require.NoError(t, err)

	for id := 8; id <= 1006; id++ {
		_, err = src.Exec(`INSERT INTO session_entries (id, conversation_id, entry_json, entry_timestamp) VALUES (?, 'main', '{"version":1}', ?)`, id, time.Unix(int64(id), 0).UTC().Format(time.RFC3339Nano))
		require.NoError(t, err)
	}

	require.NoError(t, src.Close())

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	var out strings.Builder

	inserted, err := MigrateSQLite(t.Context(), workspace, config.DefaultRuntimeDir, dsn, slog.New(slog.DiscardHandler), &out)
	require.NoError(t, err)
	assert.Equal(t, int64(1001), inserted)
	assert.Contains(t, out.String(), "session_entries: 1000\n")
}

func TestMigrateSQLiteReportsOpenError(t *testing.T) {
	workspace := t.TempDir()
	writeSQLite(t, workspace, 9, true)
	_, err := MigrateSQLite(t.Context(), workspace, config.DefaultRuntimeDir, "not-a-dsn", slog.New(slog.DiscardHandler), io.Discard)
	require.Error(t, err)
}

func TestMigrateSQLiteRequiresFile(t *testing.T) {
	workspace := t.TempDir()
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	logger := slog.New(slog.DiscardHandler)

	_, err = MigrateSQLite(t.Context(), workspace, config.DefaultRuntimeDir, dsn, logger, io.Discard)
	require.ErrorIs(t, err, os.ErrNotExist)

	service, err := NewSessionServiceIn(workspace, config.DefaultRuntimeDir, dsn, logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	var n int
	require.NoError(t, service.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM session_entries`).Scan(&n))
	assert.Equal(t, 0, n)
}

func TestMigrateSQLiteRecopiesAfterEmptyDSN(t *testing.T) {
	workspace := t.TempDir()
	writeSQLite(t, workspace, 9, true)

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	logger := slog.New(slog.DiscardHandler)

	inserted, err := MigrateSQLite(t.Context(), workspace, config.DefaultRuntimeDir, dsn, logger, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, int64(2), inserted)

	service, err := NewSessionServiceIn(workspace, config.DefaultRuntimeDir, dsn, logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })
	_, err = service.db.ExecContext(t.Context(), `DELETE FROM session_entries`)
	require.NoError(t, err)
	_, err = service.db.ExecContext(t.Context(), `DELETE FROM managed_conversations`)
	require.NoError(t, err)

	inserted, err = MigrateSQLite(t.Context(), workspace, config.DefaultRuntimeDir, dsn, logger, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, int64(2), inserted)
}

func writeSQLite(t *testing.T, workspace string, version int, withRows bool) {
	t.Helper()

	sqlitePath := sessionDBPathIn(workspace, config.DefaultRuntimeDir)
	require.NoError(t, os.MkdirAll(filepath.Dir(sqlitePath), 0o755))
	src, err := sql.Open("sqlite", sqlitePath)
	require.NoError(t, err)
	_, err = src.Exec(`CREATE TABLE session_entries (id INTEGER PRIMARY KEY AUTOINCREMENT, conversation_id TEXT NOT NULL, entry_json TEXT NOT NULL, entry_timestamp TEXT NOT NULL)`)
	require.NoError(t, err)
	_, err = src.Exec(`CREATE TABLE managed_conversations (conversation_id TEXT PRIMARY KEY, agent TEXT NOT NULL, created_by TEXT NOT NULL)`)
	require.NoError(t, err)

	if withRows {
		_, err = src.Exec(`INSERT INTO managed_conversations VALUES ('main', 'planner', 'human')`)
		require.NoError(t, err)
		_, err = src.Exec(`INSERT INTO session_entries (id, conversation_id, entry_json, entry_timestamp) VALUES (7, 'main', '{"version":1}', ?)`, time.Unix(1, 0).UTC().Format(time.RFC3339Nano))
		require.NoError(t, err)
	}

	_, err = src.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version))
	require.NoError(t, err)
	require.NoError(t, src.Close())
}
