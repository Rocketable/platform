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

	version, migrated, err := prepareSessionSchemaMigration(ctx, tx, logger)
	if err != nil {
		return err
	}

	if version == 1 {
		if err := migrateCronScheduleSpec(ctx, tx, logger); err != nil {
			return err
		}

		logger.Info("setting rocketclaw state schema version", "version", sessionDBSchemaVersion)

		migrated = true
	}

	if version == 2 || version == 3 {
		logger.Info("adding rocketclaw active-turn restart handoff schema")

		if err := migrateActiveTurnSourceMetadata(ctx, tx, logger); err != nil {
			return err
		}
	}

	if version >= 2 && version <= 4 {
		if err := migrateActiveTurnRowExistence(ctx, tx, logger); err != nil {
			return err
		}

		logger.Info("setting rocketclaw state schema version", "version", sessionDBSchemaVersion)

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
			"scheduled_messages", len(state.ScheduledMessages),
			"pending_restart_notifications", len(state.PendingRestartNotifications),
		)

		if err := importLegacyState(ctx, stateDAO{db: tx}, state, logger); err != nil {
			return err
		}

		logger.Info("setting rocketclaw state schema version", "version", sessionDBSchemaVersion)

		migrated = true
	} else if !migrated {
		logger.Info("rocketclaw state schema already current", "version", version)
	}

	if version == 6 {
		migratedExternalMCP, err := migrateExternalMCPSessionState(ctx, tx, version)
		if err != nil {
			return err
		}

		migrated = migrated || migratedExternalMCP
	}

	if version <= 6 {
		if err := migrateChannelOnlyState(ctx, tx); err != nil {
			return err
		}

		migrated = true
	}

	migratedExternalMCP, err := migrateExternalMCPDualSessionsIfNeeded(ctx, tx, version)
	if err != nil {
		return err
	}

	migrated = migrated || migratedExternalMCP

	if version < sessionDBSchemaVersion {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, sessionDBSchemaVersion)); err != nil {
			return fmt.Errorf("set rocketclaw state schema version: %w", err)
		}

		migrated = true
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

func prepareSessionSchemaMigration(ctx context.Context, db stateStoreDB, logger *slog.Logger) (version int, migrated bool, err error) {
	logger.Info("ensuring rocketclaw normalized state tables")

	if err := createSessionSchema(ctx, db); err != nil {
		return 0, false, err
	}

	version, err = sessionDBUserVersion(ctx, db)
	if err != nil {
		return 0, false, err
	}

	if version > sessionDBSchemaVersion {
		return 0, false, fmt.Errorf("unsupported rocketclaw state schema version %d", version)
	}

	logger.Info("checked rocketclaw state schema", "version", version, "target_version", sessionDBSchemaVersion)

	migrated, err = migrateGoalRecipients(ctx, db)

	return version, migrated, err
}

func migrateGoalRecipients(ctx context.Context, db stateStoreDB) (bool, error) {
	changed := false

	for _, column := range []string{"slack_recipient_team_id", "slack_recipient_user_id"} {
		hasColumn, err := tableHasColumn(ctx, db, "conversation_goals", column, "conversation_goals", "iterate")
		if err != nil {
			return false, err
		}

		if hasColumn {
			continue
		}

		if _, err := db.ExecContext(ctx, `ALTER TABLE conversation_goals ADD COLUMN `+column+` TEXT NOT NULL DEFAULT ''`); err != nil {
			return false, fmt.Errorf("add conversation goal %s: %w", column, err)
		}

		changed = true
	}

	return changed, nil
}

func migrateExternalMCPDualSessionsIfNeeded(ctx context.Context, tx *sql.Tx, version int) (changed bool, err error) {
	if version > 7 {
		return false, nil
	}

	migrated, err := migrateExternalMCPDualSessionState(ctx, tx)
	if err != nil {
		return false, err
	}

	return migrated, nil
}

func migrateExternalMCPSessionState(ctx context.Context, tx *sql.Tx, version int) (bool, error) {
	if version != 6 {
		return false, nil
	}

	for _, statement := range []string{
		`CREATE TABLE external_mcp_sessions_v7 (external_conversation_id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL UNIQUE, agent TEXT NOT NULL, slack_channel TEXT NOT NULL)`,
		`INSERT INTO external_mcp_sessions_v7 SELECT external_conversation_id, conversation_id, agent, slack_channel FROM external_mcp_sessions WHERE trim(slack_channel) <> '' AND conversation_id LIKE 'slack-thread:%' AND instr(substr(conversation_id, 14), ':') > 1 AND trim(substr(substr(conversation_id, 14), instr(substr(conversation_id, 14), ':') + 1)) <> ''`,
		`DROP TABLE external_mcp_sessions`,
		`ALTER TABLE external_mcp_sessions_v7 RENAME TO external_mcp_sessions`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return false, fmt.Errorf("migrate external MCP session state: %w", err)
		}
	}

	return true, nil
}

func migrateExternalMCPDualSessionState(ctx context.Context, db stateStoreDB) (bool, error) {
	hasPrivateConversationID, err := tableHasColumn(ctx, db, "external_mcp_sessions", "private_conversation_id", "external_mcp_sessions", "iterate")
	if err != nil {
		return false, err
	}

	if hasPrivateConversationID {
		return false, nil
	}

	for _, statement := range []string{
		`CREATE TABLE external_mcp_sessions_v8 (external_conversation_id TEXT PRIMARY KEY, private_conversation_id TEXT UNIQUE CHECK (private_conversation_id IS NULL OR trim(private_conversation_id) <> ''), managed_conversation_id TEXT NOT NULL UNIQUE CHECK (trim(managed_conversation_id) <> ''), agent TEXT NOT NULL, slack_channel TEXT NOT NULL)`,
		`INSERT INTO external_mcp_sessions_v8 (external_conversation_id, private_conversation_id, managed_conversation_id, agent, slack_channel) SELECT external_conversation_id, NULL, conversation_id, agent, slack_channel FROM external_mcp_sessions`,
		`DROP TABLE external_mcp_sessions`,
		`ALTER TABLE external_mcp_sessions_v8 RENAME TO external_mcp_sessions`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return false, fmt.Errorf("install v8 external MCP sessions: %w", err)
		}
	}

	return true, nil
}

func migrateChannelOnlyState(ctx context.Context, tx stateStoreDB) error {
	hasPrivateConversationID, err := tableHasColumn(ctx, tx, "external_mcp_sessions", "private_conversation_id", "external_mcp_sessions", "iterate")
	if err != nil {
		return err
	}

	hasSeeded, err := tableHasColumn(ctx, tx, "managed_conversations", "seeded_from_response", "managed_conversations", "iterate")
	if err != nil {
		return err
	}

	if !hasSeeded {
		hasSlackChannel, err := tableHasColumn(ctx, tx, "external_mcp_sessions", "slack_channel", "external_mcp_sessions", "iterate")
		if err != nil {
			return err
		}

		statements := []string{}
		if !hasSlackChannel {
			statements = append(statements,
				`DROP TABLE external_mcp_sessions`,
				`CREATE TABLE external_mcp_sessions (external_conversation_id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL UNIQUE, agent TEXT NOT NULL, slack_channel TEXT NOT NULL)`,
			)
		}

		externalCleanup := `DELETE FROM external_mcp_sessions WHERE trim(slack_channel) = '' OR conversation_id NOT LIKE 'slack-thread:%' OR conversation_id LIKE 'slack-thread:D%'`
		if hasPrivateConversationID {
			externalCleanup = `DELETE FROM external_mcp_sessions WHERE trim(slack_channel) = '' OR managed_conversation_id NOT LIKE 'slack-thread:%' OR managed_conversation_id LIKE 'slack-thread:D%'`
		}

		statements = append(statements,
			externalCleanup,
			`DELETE FROM session_entries WHERE conversation_id = 'main' OR conversation_id LIKE 'slack-thread:D%' OR json_extract(entry_json, '$.type') IN ('main_thread_seed', 'conversation_thread_seed', 'response_thread_seed', 'cron_thread_seed')`,
			`DELETE FROM conversation_goals WHERE conversation_id = 'main' OR conversation_id LIKE 'slack-thread:D%'`,
			`DELETE FROM scheduled_messages WHERE conversation_id = 'main' OR conversation_id LIKE 'slack-thread:D%'`,
			`DELETE FROM pending_restart_notifications WHERE conversation_id = 'main' OR conversation_id LIKE 'slack-thread:D%'`,
			`DELETE FROM active_turns WHERE conversation_id = 'main' OR conversation_id LIKE 'slack-thread:D%' OR conversation_id NOT IN (SELECT conversation_id FROM managed_conversations)`,
			`DELETE FROM managed_conversations WHERE conversation_id = 'main' OR conversation_id LIKE 'slack-thread:D%'`,
			`DROP TABLE IF EXISTS response_checkpoints`,
		)
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate channel-only state: %w", err)
			}
		}

		return nil
	}

	statements := []string{
		`CREATE TEMP TABLE mcp_alias_migrations (old_conversation_id TEXT PRIMARY KEY, new_conversation_id TEXT NOT NULL UNIQUE, external_conversation_id TEXT NOT NULL, agent TEXT NOT NULL, slack_channel TEXT NOT NULL)`,
		`INSERT INTO mcp_alias_migrations SELECT e.conversation_id, m.conversation_id, e.external_conversation_id, m.agent, substr(m.conversation_id, 14, instr(substr(m.conversation_id, 14), ':') - 1) FROM external_mcp_sessions e JOIN managed_conversations m ON m.seeded_from_response = e.conversation_id WHERE m.conversation_id LIKE 'slack-thread:%' AND m.conversation_id NOT LIKE 'slack-thread:D%' AND instr(substr(m.conversation_id, 14), ':') > 1 AND trim(substr(substr(m.conversation_id, 14), instr(substr(m.conversation_id, 14), ':') + 1)) <> ''`,
		`CREATE TEMP TABLE invalid_seeded_conversations AS SELECT conversation_id FROM managed_conversations WHERE seeded_from_response <> '' AND conversation_id NOT IN (SELECT new_conversation_id FROM mcp_alias_migrations)`,
		`CREATE TEMP TABLE invalid_mcp_conversations AS SELECT conversation_id FROM external_mcp_sessions WHERE conversation_id NOT IN (SELECT old_conversation_id FROM mcp_alias_migrations)`,
		`CREATE TABLE external_mcp_sessions_v7 (external_conversation_id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL UNIQUE, agent TEXT NOT NULL, slack_channel TEXT NOT NULL)`,
		`INSERT INTO external_mcp_sessions_v7 SELECT external_conversation_id, new_conversation_id, agent, slack_channel FROM mcp_alias_migrations`,
		`DROP TABLE external_mcp_sessions`,
		`ALTER TABLE external_mcp_sessions_v7 RENAME TO external_mcp_sessions`,
		`DELETE FROM session_entries WHERE conversation_id IN (SELECT new_conversation_id FROM mcp_alias_migrations) OR conversation_id IN (SELECT conversation_id FROM invalid_seeded_conversations) OR conversation_id IN (SELECT conversation_id FROM invalid_mcp_conversations)`,
		`UPDATE session_entries SET conversation_id = (SELECT new_conversation_id FROM mcp_alias_migrations WHERE old_conversation_id = session_entries.conversation_id) WHERE conversation_id IN (SELECT old_conversation_id FROM mcp_alias_migrations)`,
		`DELETE FROM active_turns WHERE conversation_id IN (SELECT conversation_id FROM invalid_seeded_conversations) OR conversation_id IN (SELECT conversation_id FROM invalid_mcp_conversations)`,
		`UPDATE active_turns SET conversation_id = (SELECT new_conversation_id FROM mcp_alias_migrations WHERE old_conversation_id = active_turns.conversation_id) WHERE conversation_id IN (SELECT old_conversation_id FROM mcp_alias_migrations)`,
		`DELETE FROM scheduled_messages WHERE conversation_id IN (SELECT conversation_id FROM invalid_seeded_conversations) OR conversation_id IN (SELECT conversation_id FROM invalid_mcp_conversations)`,
		`UPDATE scheduled_messages SET conversation_id = (SELECT new_conversation_id FROM mcp_alias_migrations WHERE old_conversation_id = scheduled_messages.conversation_id) WHERE conversation_id IN (SELECT old_conversation_id FROM mcp_alias_migrations)`,
		`DELETE FROM pending_restart_notifications WHERE conversation_id IN (SELECT conversation_id FROM invalid_seeded_conversations) OR conversation_id IN (SELECT conversation_id FROM invalid_mcp_conversations)`,
		`INSERT OR IGNORE INTO pending_restart_notifications SELECT new_conversation_id FROM mcp_alias_migrations JOIN pending_restart_notifications ON pending_restart_notifications.conversation_id = old_conversation_id`,
		`DELETE FROM pending_restart_notifications WHERE conversation_id IN (SELECT old_conversation_id FROM mcp_alias_migrations)`,
		`DELETE FROM conversation_goals WHERE conversation_id IN (SELECT conversation_id FROM invalid_seeded_conversations) OR conversation_id IN (SELECT conversation_id FROM invalid_mcp_conversations)`,
		`DELETE FROM conversation_goals WHERE conversation_id IN (SELECT new_conversation_id FROM mcp_alias_migrations) AND conversation_id IN (SELECT conversation_id FROM conversation_goals) AND EXISTS (SELECT 1 FROM conversation_goals old_goal JOIN mcp_alias_migrations ON old_goal.conversation_id = old_conversation_id WHERE new_conversation_id = conversation_goals.conversation_id)`,
		`UPDATE conversation_goals SET conversation_id = (SELECT new_conversation_id FROM mcp_alias_migrations WHERE old_conversation_id = conversation_goals.conversation_id) WHERE conversation_id IN (SELECT old_conversation_id FROM mcp_alias_migrations)`,
		`DELETE FROM session_entries WHERE conversation_id = 'main' OR conversation_id LIKE 'slack-thread:D%' OR conversation_id IN (SELECT conversation_id FROM invalid_seeded_conversations) OR json_extract(entry_json, '$.type') IN ('main_thread_seed', 'conversation_thread_seed', 'response_thread_seed', 'cron_thread_seed')`,
		`DELETE FROM conversation_goals WHERE conversation_id = 'main' OR conversation_id LIKE 'slack-thread:D%'`,
		`DELETE FROM scheduled_messages WHERE conversation_id = 'main' OR conversation_id LIKE 'slack-thread:D%'`,
		`DELETE FROM pending_restart_notifications WHERE conversation_id = 'main' OR conversation_id LIKE 'slack-thread:D%'`,
		`DELETE FROM active_turns WHERE conversation_id = 'main' OR conversation_id LIKE 'slack-thread:D%'`,
		`CREATE TABLE managed_conversations_v6 (conversation_id TEXT PRIMARY KEY, agent TEXT NOT NULL, created_by TEXT NOT NULL)`,
		`INSERT INTO managed_conversations_v6 SELECT conversation_id, agent, created_by FROM managed_conversations WHERE conversation_id <> 'main' AND conversation_id NOT LIKE 'slack-thread:D%' AND conversation_id NOT IN (SELECT conversation_id FROM invalid_seeded_conversations)`,
		`DROP TABLE managed_conversations`,
		`ALTER TABLE managed_conversations_v6 RENAME TO managed_conversations`,
		`DELETE FROM active_turns WHERE conversation_id NOT IN (SELECT conversation_id FROM managed_conversations)`,
		`DROP TABLE IF EXISTS response_checkpoints`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate channel-only state: %w", err)
		}
	}

	return nil
}

func tableHasColumn(ctx context.Context, db stateStoreDB, table, column, schemaDisplay, readDisplay string) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, fmt.Errorf("inspect %s schema: %w", schemaDisplay, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			cid, notNull, primaryKey int
			name, columnType         string
			defaultValue             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, fmt.Errorf("scan %s schema: %w", schemaDisplay, err)
		}

		if name == column {
			return true, nil
		}
	}

	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("%s %s schema: %w", readDisplay, schemaDisplay, err)
	}

	return false, nil
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

	hasMetadata, err := tableHasColumn(ctx, tx, "active_turns", "source_metadata_json", "active turn", "read")
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

	hasStatus, err := tableHasColumn(ctx, tx, "active_turns", "status", "active turn", "read")
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

func sessionDBUserVersion(ctx context.Context, db stateStoreDB) (int, error) {
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read rocketclaw state schema version: %w", err)
	}

	return version, nil
}

func importLegacyState(ctx context.Context, dao stateDAO, state State, logger *slog.Logger) error {
	if err := migrateChannelOnlyState(ctx, dao.db); err != nil {
		return err
	}

	if _, err := migrateExternalMCPDualSessionState(ctx, dao.db); err != nil {
		return err
	}

	if err := createSessionSchema(ctx, dao.db); err != nil {
		return err
	}

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
		if _, ok, err := dao.externalMCPSession(ctx, externalConversationID); err != nil {
			return err
		} else if ok {
			continue
		}

		if session.ManagedConversationID == "" {
			continue
		}

		if err := dao.upsertExternalMCPSession(ctx, externalConversationID, &session); err != nil {
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
