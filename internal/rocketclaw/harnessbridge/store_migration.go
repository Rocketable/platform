package harnessbridge

import (
	"context"
	"database/sql"
	"fmt"
)

func migrateSessionDB(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin session db migration: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

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

	if version == 0 {
		state, err := loadRocketClawState(ctx, tx)
		if err != nil {
			return err
		}

		if err := importLegacyState(ctx, stateDAO{db: tx}, state); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, sessionDBSchemaVersion)); err != nil {
			return fmt.Errorf("set rocketclaw state schema version: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session db migration: %w", err)
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

func importLegacyState(ctx context.Context, dao stateDAO, state State) error {
	for conversationID, thread := range state.Threads {
		if err := dao.upsertThread(ctx, conversationID, thread); err != nil {
			return err
		}
	}

	for conversationID := range state.Goals {
		goal := state.Goals[conversationID]
		if err := dao.upsertGoal(ctx, conversationID, &goal); err != nil {
			return err
		}
	}

	for externalConversationID, session := range state.ExternalMCPSessions {
		if err := dao.upsertExternalMCPSession(ctx, externalConversationID, session); err != nil {
			return err
		}
	}

	for key, checkpoint := range state.ResponseCheckpoints {
		if err := dao.upsertResponseCheckpoint(ctx, key, checkpoint); err != nil {
			return err
		}
	}

	for id := range state.ScheduledMessages {
		message := state.ScheduledMessages[id]
		if err := dao.putScheduledMessage(ctx, id, &message); err != nil {
			return err
		}
	}

	for conversationID, pending := range state.PendingRestartNotifications {
		if pending {
			if err := dao.markRestartRequester(ctx, conversationID); err != nil {
				return err
			}
		}
	}

	return nil
}
