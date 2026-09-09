package backend

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	harness "github.com/Rocketable/platform/internal/rocketcode"
)

// Background backfill shares this transaction lock with history mutation. It
// also covers missing summaries and private histories without managed rows.
func lockSessionHistory(ctx context.Context, db stateStoreDB, conversationID string) error {
	_, err := db.ExecContext(ctx, `SELECT pg_advisory_xact_lock(87901, hashtext($1))`, conversationID)
	if err != nil {
		return fmt.Errorf("lock session history: %w", err)
	}

	return nil
}

// loadSessionSummary decodes history only when its durable projection is missing.
// The caller holds the history lock until the projection is committed.
func loadSessionSummary(ctx context.Context, db stateStoreDB, conversationID string) (protocol.SessionSummary, error) {
	summary := protocol.SessionSummary{ConversationID: conversationID}

	var preview []byte

	err := db.QueryRowContext(ctx, `SELECT preview, last_updated FROM session_summaries WHERE conversation_id = $1`, conversationID).Scan(&preview, &summary.LastUpdated)
	if err == nil {
		summary.LastUserMessage = string(preview)
		return summary, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return summary, fmt.Errorf("read session summary: %w", err)
	}

	rows, err := db.QueryContext(ctx, `SELECT entry_json, entry_timestamp FROM session_entries WHERE conversation_id = $1 ORDER BY id`, conversationID)
	if err != nil {
		return summary, fmt.Errorf("query session summary history: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var raw, timestamp string
		if err := rows.Scan(&raw, &timestamp); err != nil {
			return summary, fmt.Errorf("scan session summary history: %w", err)
		}

		var entry harness.SessionEntry
		if err := json.Unmarshal([]byte(raw), &entry); err != nil {
			return summary, fmt.Errorf("parse session summary history: %w", err)
		}

		if err := projectSessionSummary(&summary, &entry, timestamp); err != nil {
			return summary, err
		}
	}

	if err := rows.Err(); err != nil {
		return summary, fmt.Errorf("read session summary history: %w", err)
	}

	return summary, nil
}

func projectSessionSummary(summary *protocol.SessionSummary, entry *harness.SessionEntry, timestamp string) error {
	messages, err := replayInputMessages(entry.ReplayInput)
	if err != nil {
		return fmt.Errorf("decode session summary replay input: %w", err)
	}

	for _, message := range messages {
		if message.role == "user" {
			summary.LastUserMessage = message.text
		}
	}

	if updated, errParse := time.Parse(time.RFC3339Nano, timestamp); errParse == nil {
		summary.LastUpdated = updated.UTC().Truncate(time.Microsecond)
	}

	return nil
}

func saveSessionSummary(ctx context.Context, db stateStoreDB, summary protocol.SessionSummary) error {
	_, err := db.ExecContext(ctx, `INSERT INTO session_summaries (conversation_id, preview, last_updated) VALUES ($1, $2, $3)
ON CONFLICT (conversation_id) DO UPDATE SET preview = EXCLUDED.preview, last_updated = EXCLUDED.last_updated`, summary.ConversationID, []byte(summary.LastUserMessage), summary.LastUpdated)
	if err != nil {
		return fmt.Errorf("write session summary: %w", err)
	}

	return nil
}

func (s *SessionService) sessionSummariesComplete(ctx context.Context) (bool, error) {
	var complete bool

	err := s.db.QueryRowContext(ctx, `SELECT NOT EXISTS (SELECT 1 FROM
(SELECT conversation_id FROM managed_conversations UNION SELECT conversation_id FROM session_entries) c
WHERE NOT EXISTS (SELECT 1 FROM session_summaries s WHERE s.conversation_id = c.conversation_id))`).Scan(&complete)
	if err != nil {
		return false, fmt.Errorf("read session summary completeness: %w", err)
	}

	return complete, nil
}

func (s *SessionService) backfillSessionSummaries(ctx context.Context) error {
	for {
		var conversationID string

		err := s.db.QueryRowContext(ctx, `SELECT conversation_id FROM
(SELECT conversation_id FROM managed_conversations UNION SELECT conversation_id FROM session_entries) c
WHERE NOT EXISTS (SELECT 1 FROM session_summaries s WHERE s.conversation_id = c.conversation_id)
ORDER BY conversation_id COLLATE "C" LIMIT 1`).Scan(&conversationID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("find missing session summary: %w", err)
		}

		if err := s.backfillSessionSummary(ctx, conversationID); err != nil {
			return err
		}
	}
}

func (s *SessionService) backfillSessionSummary(ctx context.Context, conversationID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin session summary backfill: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, conversationID); err != nil {
		return err
	}

	var missing bool
	if err := tx.QueryRowContext(ctx, `SELECT (EXISTS (SELECT 1 FROM managed_conversations WHERE conversation_id = $1)
OR EXISTS (SELECT 1 FROM session_entries WHERE conversation_id = $1))
AND NOT EXISTS (SELECT 1 FROM session_summaries WHERE conversation_id = $1)`, conversationID).Scan(&missing); err != nil {
		return fmt.Errorf("check session summary backfill: %w", err)
	}

	if missing {
		summary, err := loadSessionSummary(ctx, tx, conversationID)
		if err != nil {
			return err
		}

		if err := saveSessionSummary(ctx, tx, summary); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session summary backfill: %w", err)
	}

	return nil
}
