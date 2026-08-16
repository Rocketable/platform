package harnessbridge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	_ "modernc.org/sqlite" // state.sqlite3 reader for operator migrate
)

var errReadSQLiteVersion = errors.New("read sqlite schema version")

type sqliteVersionError struct {
	got, want int
}

func (e sqliteVersionError) Error() string {
	return fmt.Sprintf("sqlite store has user_version %d, want %d", e.got, e.want)
}

// MigrateSQLite copies missing v9 sqlite rows into the PostgreSQL store.
func MigrateSQLite(ctx context.Context, workspace, runtimeDir, databaseURL string, logger *slog.Logger, out io.Writer) (int64, error) {
	sqlitePath := sessionDBPathIn(workspace, runtimeDir)
	if _, err := os.Stat(sqlitePath); err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("sqlite store not found: %w", err)
		}

		return 0, fmt.Errorf("stat sqlite store: %w", err)
	}

	db, err := openSessionDB(ctx, databaseURL, logger)
	if err != nil {
		return 0, err
	}
	defer func() { _ = db.Close() }()

	return copySQLiteStore(ctx, db, sqlitePath, logger, out)
}

func copySQLiteStore(ctx context.Context, dest *sql.DB, sqlitePath string, logger *slog.Logger, out io.Writer) (int64, error) {
	src, err := sql.Open("sqlite", "file:"+sqlitePath+"?mode=ro")
	if err != nil {
		return 0, fmt.Errorf("open sqlite store: %w", err)
	}
	defer func() { _ = src.Close() }()

	var version int
	if err := src.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("%w: %w", errReadSQLiteVersion, err)
	}

	if version != sessionDBSchemaVersion {
		return 0, sqliteVersionError{got: version, want: sessionDBSchemaVersion}
	}

	copies := []struct {
		name string
		src  string
		dest string
	}{
		{"managed_conversations", `SELECT conversation_id, agent, created_by FROM managed_conversations`, `INSERT INTO managed_conversations (conversation_id, agent, created_by) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`},
		{"conversation_goals", `SELECT conversation_id, objective, check_script, max_turns, turns_used, status, note, slack_recipient_team_id, slack_recipient_user_id, created_at_unix_ns, updated_at_unix_ns FROM conversation_goals`, `INSERT INTO conversation_goals (conversation_id, objective, check_script, max_turns, turns_used, status, note, slack_recipient_team_id, slack_recipient_user_id, created_at_unix_ns, updated_at_unix_ns) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT DO NOTHING`},
		{"external_mcp_sessions", `SELECT external_conversation_id, private_conversation_id, managed_conversation_id, agent, slack_channel FROM external_mcp_sessions`, `INSERT INTO external_mcp_sessions (external_conversation_id, private_conversation_id, managed_conversation_id, agent, slack_channel) VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`},
		{"scheduled_messages", `SELECT scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring, interval_ns FROM scheduled_messages`, `INSERT INTO scheduled_messages (scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring, interval_ns) VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT DO NOTHING`},
		{"cron_schedules", `SELECT schedule_id, relative_path, next_due_unix_ns, updated_at_unix_ns FROM cron_schedules`, `INSERT INTO cron_schedules (schedule_id, relative_path, next_due_unix_ns, updated_at_unix_ns) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`},
		{"cron_schedule_runs", `SELECT relative_path, running, running_since_unix_ns, updated_at_unix_ns FROM cron_schedule_runs`, `INSERT INTO cron_schedule_runs (relative_path, running, running_since_unix_ns, updated_at_unix_ns) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`},
		{"pending_restart_notifications", `SELECT conversation_id FROM pending_restart_notifications`, `INSERT INTO pending_restart_notifications (conversation_id) VALUES ($1) ON CONFLICT DO NOTHING`},
		{"active_turns", `SELECT id, conversation_id, agent, model, display_model, replay_input_json, output_trace_json, token_usage_json, response_id, open_function_calls_json, completed_function_outputs_json, restart_notice_json, source_metadata_json, created_at_unix_ns, updated_at_unix_ns FROM active_turns`, `INSERT INTO active_turns (id, conversation_id, agent, model, display_model, replay_input_json, output_trace_json, token_usage_json, response_id, open_function_calls_json, completed_function_outputs_json, restart_notice_json, source_metadata_json, created_at_unix_ns, updated_at_unix_ns) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) ON CONFLICT DO NOTHING`},
		{"session_entries", `SELECT id, conversation_id, entry_json, entry_timestamp FROM session_entries`, `INSERT INTO session_entries (id, conversation_id, entry_json, entry_timestamp) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`},
	}

	logger.Info("copying sqlite store", "path", sqlitePath)

	var inserted int64

	for _, table := range copies {
		n, err := copyTable(ctx, dest, src, table.name, table.src, table.dest, out)
		if err != nil {
			return 0, fmt.Errorf("copy %s: %w", table.name, err)
		}

		inserted += n
	}

	var maxID sql.NullInt64
	if err := dest.QueryRowContext(ctx, `SELECT MAX(id) FROM session_entries`).Scan(&maxID); err != nil {
		return 0, fmt.Errorf("read copied session entry id: %w", err)
	}

	if maxID.Valid {
		if _, err := dest.ExecContext(ctx, `SELECT setval(pg_get_serial_sequence('session_entries', 'id'), $1, true)`, maxID.Int64); err != nil {
			return 0, fmt.Errorf("reset session entry identity: %w", err)
		}
	}

	return inserted, nil
}

func copyTable(ctx context.Context, dest, src *sql.DB, name, selectSQL, insertSQL string, out io.Writer) (int64, error) {
	rows, err := src.QueryContext(ctx, selectSQL)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") || strings.Contains(err.Error(), "does not exist") {
			return 0, writeMigrateProgress(out, name, 0)
		}

		return 0, fmt.Errorf("select sqlite rows: %w", err)
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return 0, fmt.Errorf("read sqlite columns: %w", err)
	}

	values := make([]any, len(cols))

	ptrs := make([]any, len(cols))
	for i := range values {
		ptrs[i] = &values[i]
	}

	var touched, inserted int64

	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return 0, fmt.Errorf("scan sqlite row: %w", err)
		}

		result, err := dest.ExecContext(ctx, insertSQL, values...)
		if err != nil {
			return 0, fmt.Errorf("insert sqlite row: %w", err)
		}

		n, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("count sqlite insert: %w", err)
		}

		inserted += n

		touched++
		if touched%1000 == 0 {
			if err := writeMigrateProgress(out, name, touched); err != nil {
				return 0, err
			}
		}
	}

	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read sqlite rows: %w", err)
	}

	return inserted, writeMigrateProgress(out, name, touched)
}

func writeMigrateProgress(out io.Writer, name string, touched int64) error {
	_, err := fmt.Fprintf(out, "%s: %d\n", name, touched)
	if err != nil {
		return fmt.Errorf("write sqlite progress: %w", err)
	}

	return nil
}
