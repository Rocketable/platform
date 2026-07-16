package harnessbridge

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const sessionDBSchemaVersion = 8

func initializeSessionDB(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	startedAt := time.Now()

	logger.Info("initializing rocketclaw state sqlite pragmas")

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
		statementStartedAt := time.Now()

		if err := execSessionDBStatement(ctx, db, statement); err != nil {
			return err
		}

		if elapsed := time.Since(statementStartedAt); elapsed > time.Second {
			logger.Warn("slow rocketclaw state sqlite pragma", "statement", statement, "elapsed", elapsed)
		}
	}

	logger.Info("initialized rocketclaw state sqlite pragmas", "elapsed", time.Since(startedAt))

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
		`CREATE TABLE IF NOT EXISTS managed_conversations (conversation_id TEXT PRIMARY KEY, agent TEXT NOT NULL, created_by TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS conversation_goals (conversation_id TEXT PRIMARY KEY, objective TEXT NOT NULL, check_script TEXT NOT NULL, max_turns INTEGER NOT NULL, turns_used INTEGER NOT NULL, status TEXT NOT NULL, note TEXT NOT NULL, created_at_unix_ns INTEGER NOT NULL, updated_at_unix_ns INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS external_mcp_sessions (external_conversation_id TEXT PRIMARY KEY, private_conversation_id TEXT UNIQUE CHECK (private_conversation_id IS NULL OR trim(private_conversation_id) <> ''), managed_conversation_id TEXT NOT NULL UNIQUE CHECK (trim(managed_conversation_id) <> ''), agent TEXT NOT NULL, slack_channel TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS scheduled_messages (scheduled_message_id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, agent TEXT NOT NULL, message TEXT NOT NULL, due_at_unix_ns INTEGER NOT NULL, recurring INTEGER NOT NULL, interval_ns INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS cron_schedules (schedule_id TEXT PRIMARY KEY, relative_path TEXT NOT NULL, next_due_unix_ns INTEGER NOT NULL, updated_at_unix_ns INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS cron_schedule_runs (relative_path TEXT PRIMARY KEY, running INTEGER NOT NULL, running_since_unix_ns INTEGER NOT NULL, updated_at_unix_ns INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS cron_schedules_next_due_id ON cron_schedules (next_due_unix_ns, schedule_id)`,
		`CREATE INDEX IF NOT EXISTS cron_schedules_relative_path ON cron_schedules (relative_path)`,
		`CREATE INDEX IF NOT EXISTS cron_schedule_runs_running_path ON cron_schedule_runs (running, relative_path)`,
		`CREATE TABLE IF NOT EXISTS pending_restart_notifications (conversation_id TEXT PRIMARY KEY)`,
		`CREATE TABLE IF NOT EXISTS active_turns (id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, agent TEXT NOT NULL, model TEXT NOT NULL, display_model TEXT NOT NULL, replay_input_json TEXT NOT NULL, output_trace_json TEXT NOT NULL, token_usage_json TEXT NOT NULL, response_id TEXT NOT NULL, open_function_calls_json TEXT NOT NULL, completed_function_outputs_json TEXT NOT NULL, restart_notice_json TEXT NOT NULL, source_metadata_json TEXT NOT NULL, created_at_unix_ns INTEGER NOT NULL, updated_at_unix_ns INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS active_turns_conversation_updated ON active_turns (conversation_id, updated_at_unix_ns)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize rocketcode session schema: %w", err)
		}
	}

	return nil
}
