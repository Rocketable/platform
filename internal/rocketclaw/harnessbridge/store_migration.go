package harnessbridge

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

func migrateSessionDB(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	startedAt := time.Now()

	logger.Info("checking rocketclaw state schema")

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin session db migration: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	logger.Info("ensuring rocketclaw normalized state tables")

	if err := createSessionSchema(ctx, tx); err != nil {
		return err
	}

	version, err := sessionDBUserVersion(ctx, tx)
	if err != nil {
		return err
	}

	if version > sessionDBSchemaVersion {
		return fmt.Errorf("unsupported rocketclaw state schema version %d", version)
	}

	logger.Info("checked rocketclaw state schema", "version", version, "target_version", sessionDBSchemaVersion)

	migrated := false

	if version == 1 {
		if err := migrateCronScheduleSpec(ctx, tx, logger); err != nil {
			return err
		}

		logger.Info("setting rocketclaw state schema version", "version", sessionDBSchemaVersion)

		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, sessionDBSchemaVersion)); err != nil {
			return fmt.Errorf("set rocketclaw state schema version: %w", err)
		}

		migrated = true
	}

	if version == 2 {
		logger.Info("adding rocketclaw active-turn restart handoff schema")

		if err := migrateActiveTurnSourceMetadata(ctx, tx, logger); err != nil {
			return err
		}

		if err := migrateActiveTurnRowExistence(ctx, tx, logger); err != nil {
			return err
		}

		logger.Info("setting rocketclaw state schema version", "version", sessionDBSchemaVersion)

		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, sessionDBSchemaVersion)); err != nil {
			return fmt.Errorf("set rocketclaw state schema version: %w", err)
		}

		migrated = true
	}

	if version == 3 {
		if err := migrateActiveTurnSourceMetadata(ctx, tx, logger); err != nil {
			return err
		}

		if err := migrateActiveTurnRowExistence(ctx, tx, logger); err != nil {
			return err
		}

		logger.Info("setting rocketclaw state schema version", "version", sessionDBSchemaVersion)

		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, sessionDBSchemaVersion)); err != nil {
			return fmt.Errorf("set rocketclaw state schema version: %w", err)
		}

		migrated = true
	}

	if version == 4 {
		if err := migrateActiveTurnRowExistence(ctx, tx, logger); err != nil {
			return err
		}

		logger.Info("setting rocketclaw state schema version", "version", sessionDBSchemaVersion)

		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, sessionDBSchemaVersion)); err != nil {
			return fmt.Errorf("set rocketclaw state schema version: %w", err)
		}

		migrated = true
	}

	if version == 0 {
		logger.Info("loading legacy rocketclaw state for normalized migration")

		state, err := loadRocketClawState(ctx, tx)
		if err != nil {
			return err
		}

		logger.Info(
			"loaded legacy rocketclaw state for normalized migration",
			"threads", len(state.Threads),
			"goals", len(state.Goals),
			"external_mcp_sessions", len(state.ExternalMCPSessions),
			"response_checkpoints", len(state.ResponseCheckpoints),
			"scheduled_messages", len(state.ScheduledMessages),
			"pending_restart_notifications", len(state.PendingRestartNotifications),
		)

		if err := importLegacyState(ctx, stateDAO{db: tx}, state, logger); err != nil {
			return err
		}

		logger.Info("setting rocketclaw state schema version", "version", sessionDBSchemaVersion)

		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, sessionDBSchemaVersion)); err != nil {
			return fmt.Errorf("set rocketclaw state schema version: %w", err)
		}

		migrated = true
	} else if !migrated {
		logger.Info("rocketclaw state schema already current", "version", version)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session db migration: %w", err)
	}

	if migrated {
		logger.Info("finished rocketclaw state schema migration", "version", sessionDBSchemaVersion, "duration", time.Since(startedAt))
	} else {
		logger.Info("finished rocketclaw state schema check", "version", sessionDBSchemaVersion, "duration", time.Since(startedAt))
	}

	return nil
}

func migrateCronScheduleSpec(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	logger.Info("removing persisted cron schedule specs")

	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(cron_schedules)`)
	if err != nil {
		return fmt.Errorf("inspect cron schedule schema: %w", err)
	}

	hasScheduleSpec := false

	for rows.Next() {
		var (
			cid          int
			name         string
			columnType   string
			notNull      int
			defaultValue sql.NullString
			primaryKey   int
		)

		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan cron schedule schema: %w", err)
		}

		if name == "schedule_spec" {
			hasScheduleSpec = true
		}
	}

	if err := rows.Close(); err != nil {
		return fmt.Errorf("close cron schedule schema rows: %w", err)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("read cron schedule schema: %w", err)
	}

	if hasScheduleSpec {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE cron_schedules DROP COLUMN schedule_spec`); err != nil {
			return fmt.Errorf("remove cron schedule spec column: %w", err)
		}
	}

	return nil
}

func migrateActiveTurnSourceMetadata(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	logger.Info("adding rocketclaw active-turn source metadata schema")

	hasMetadata, err := activeTurnsHasColumn(ctx, tx, "source_metadata_json")
	if err != nil {
		return err
	}

	if !hasMetadata {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE active_turns ADD COLUMN source_metadata_json TEXT NOT NULL DEFAULT '{}'`); err != nil {
			return fmt.Errorf("add active turn source metadata column: %w", err)
		}
	}

	return nil
}

func migrateActiveTurnRowExistence(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	logger.Info("removing rocketclaw active-turn status schema")

	for _, name := range []string{"active_turns_conversation_status", "active_turns_status_updated"} {
		if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS `+name); err != nil {
			return fmt.Errorf("drop active turn status index %s: %w", name, err)
		}
	}

	hasStatus, err := activeTurnsHasColumn(ctx, tx, "status")
	if err != nil {
		return err
	}

	if hasStatus {
		if _, err := tx.ExecContext(ctx, `DELETE FROM active_turns WHERE status IN ('completed', 'failed', 'canceled', 'cancelled', 'success', 'succeeded')`); err != nil {
			return fmt.Errorf("delete terminal active turns: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `ALTER TABLE active_turns DROP COLUMN status`); err != nil {
			return fmt.Errorf("remove active turn status column: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS active_turns_conversation_updated ON active_turns (conversation_id, updated_at_unix_ns)`); err != nil {
		return fmt.Errorf("create active turn conversation index: %w", err)
	}

	return nil
}

func activeTurnsHasColumn(ctx context.Context, db stateStoreDB, column string) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(active_turns)`)
	if err != nil {
		return false, fmt.Errorf("inspect active turn schema: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			cid          int
			name         string
			columnType   string
			notNull      int
			defaultValue sql.NullString
			primaryKey   int
		)

		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, fmt.Errorf("scan active turn schema: %w", err)
		}

		if name == column {
			return true, nil
		}
	}

	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("read active turn schema: %w", err)
	}

	return false, nil
}

func sessionDBUserVersion(ctx context.Context, db stateStoreDB) (int, error) {
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read rocketclaw state schema version: %w", err)
	}

	return version, nil
}

func importLegacyState(ctx context.Context, dao stateDAO, state State, logger *slog.Logger) error {
	logger.Info("importing legacy rocketclaw managed conversations", "count", len(state.Threads))

	for conversationID, thread := range state.Threads {
		if err := dao.upsertThread(ctx, conversationID, thread); err != nil {
			return err
		}
	}

	logger.Info("importing legacy rocketclaw goals", "count", len(state.Goals))

	for conversationID := range state.Goals {
		goal := state.Goals[conversationID]
		if err := dao.upsertGoal(ctx, conversationID, &goal); err != nil {
			return err
		}
	}

	logger.Info("importing legacy rocketclaw external MCP sessions", "count", len(state.ExternalMCPSessions))

	for externalConversationID, session := range state.ExternalMCPSessions {
		if err := dao.upsertExternalMCPSession(ctx, externalConversationID, session); err != nil {
			return err
		}
	}

	logger.Info("importing legacy rocketclaw response checkpoints", "count", len(state.ResponseCheckpoints))

	for key, checkpoint := range state.ResponseCheckpoints {
		if err := dao.upsertResponseCheckpoint(ctx, key, checkpoint); err != nil {
			return err
		}
	}

	logger.Info("importing legacy rocketclaw scheduled messages", "count", len(state.ScheduledMessages))

	for id := range state.ScheduledMessages {
		message := state.ScheduledMessages[id]
		if err := dao.putScheduledMessage(ctx, id, &message); err != nil {
			return err
		}
	}

	logger.Info("importing legacy rocketclaw pending restart notifications", "count", len(state.PendingRestartNotifications))

	for conversationID, pending := range state.PendingRestartNotifications {
		if pending {
			if err := dao.markRestartRequester(ctx, conversationID); err != nil {
				return err
			}
		}
	}

	logger.Info("imported legacy rocketclaw state into normalized tables")

	return nil
}
