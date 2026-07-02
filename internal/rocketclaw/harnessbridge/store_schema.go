package harnessbridge

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const sessionDBSchemaVersion = 1

func initializeSessionDB(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	for _, statement := range []string{
		`PRAGMA busy_timeout = 30000`,
		`PRAGMA page_size = 4096`,
		`PRAGMA auto_vacuum = INCREMENTAL`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA cache_size = -64000`,
		`PRAGMA mmap_size = 268435456`,
		`PRAGMA temp_store = MEMORY`,
	} {
		if err := execSessionDBStatement(ctx, db, statement); err != nil {
			return err
		}
	}

	if err := migrateSessionDB(ctx, db, logger); err != nil {
		return fmt.Errorf("migrate rocketcode session db: %w", err)
	}

	return nil
}

func execSessionDBStatement(ctx context.Context, db *sql.DB, statement string) error {
	deadline := time.Now().Add(30 * time.Second)

	for {
		_, err := db.ExecContext(ctx, statement)
		if err == nil {
			return nil
		}

		if !strings.Contains(err.Error(), "database is locked") || time.Now().After(deadline) {
			return fmt.Errorf("initialize rocketcode session db: %w", err)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("initialize rocketcode session db: %w", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func createSessionSchema(ctx context.Context, db stateStoreDB) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS session_entries (id INTEGER PRIMARY KEY AUTOINCREMENT, conversation_id TEXT NOT NULL, entry_json TEXT NOT NULL, entry_timestamp TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS session_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS session_entries_conversation_id_id ON session_entries (conversation_id, id)`,
		`CREATE INDEX IF NOT EXISTS session_entries_conversation_id_timestamp_jd ON session_entries (conversation_id, julianday(entry_timestamp))`,
		`CREATE TABLE IF NOT EXISTS managed_conversations (conversation_id TEXT PRIMARY KEY, agent TEXT NOT NULL, seeded_from_response TEXT NOT NULL, created_by TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS conversation_goals (conversation_id TEXT PRIMARY KEY, objective TEXT NOT NULL, check_script TEXT NOT NULL, max_turns INTEGER NOT NULL, turns_used INTEGER NOT NULL, status TEXT NOT NULL, note TEXT NOT NULL, created_at_unix_ns INTEGER NOT NULL, updated_at_unix_ns INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS external_mcp_sessions (external_conversation_id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, agent TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS response_checkpoints (checkpoint_key TEXT PRIMARY KEY, source_conversation_id TEXT NOT NULL, session_entry_id INTEGER NOT NULL, response_id TEXT NOT NULL, model TEXT NOT NULL, assistant_text TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS scheduled_messages (scheduled_message_id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, agent TEXT NOT NULL, message TEXT NOT NULL, due_at_unix_ns INTEGER NOT NULL, recurring INTEGER NOT NULL, interval_ns INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS pending_restart_notifications (conversation_id TEXT PRIMARY KEY)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize rocketcode session schema: %w", err)
		}
	}

	return nil
}
