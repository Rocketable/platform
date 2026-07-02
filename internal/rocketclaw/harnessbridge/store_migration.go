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
	} else {
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
