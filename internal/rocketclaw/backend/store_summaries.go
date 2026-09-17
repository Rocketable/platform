package backend

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	harness "github.com/Rocketable/platform/internal/rocketcode"
	"github.com/openai/openai-go/v3/responses"
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
		summary.LastMessage = string(preview)
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
	items, err := harness.ReplayInputToParams(entry.ReplayInput)
	if err != nil {
		return fmt.Errorf("decode session summary replay input: %w", err)
	}

	for i := range items {
		role, text, _, err := ReplayInputMessageRoleText(&items[i], entry.ReplayInput[i])
		if err != nil {
			return err
		}

		if (role == "user" || role == "assistant") && strings.TrimSpace(text) != "" {
			summary.LastMessage = text
		}
	}

	delivery, err := ReplayDeliveryText(items)
	if err != nil {
		return err
	}

	if strings.TrimSpace(delivery) != "" {
		summary.LastMessage = delivery
	}

	if updated, errParse := time.Parse(time.RFC3339Nano, timestamp); errParse == nil {
		summary.LastUpdated = updated.UTC().Truncate(time.Microsecond)
	}

	return nil
}

// ReplayDeliveryText returns the last successful human-facing delivery in a turn.
func ReplayDeliveryText(items []responses.ResponseInputItemUnionParam) (string, error) {
	calls := make(map[string]string)
	text := ""

	for i := range items {
		item := &items[i]
		if call := item.OfFunctionCall; call != nil && call.Name == "rocketclaw_i_want_human_partner_to_see_this" {
			calls[call.CallID] = call.Arguments
		}

		output := item.OfFunctionCallOutput
		if output == nil || output.Output.OfString.Value != "queued for verbatim delivery" {
			continue
		}

		arguments, ok := calls[output.CallID.Value]
		if !ok {
			continue
		}

		var delivery struct {
			Payload string `json:"payload"`
		}
		if err := json.Unmarshal([]byte(arguments), &delivery); err != nil {
			return "", fmt.Errorf("decode delivery report: %w", err)
		}

		text = delivery.Payload
	}

	return text, nil
}

func saveSessionSummary(ctx context.Context, db stateStoreDB, summary protocol.SessionSummary) error {
	_, err := db.ExecContext(ctx, `INSERT INTO session_summaries (conversation_id, preview, last_updated) VALUES ($1, $2, $3)
ON CONFLICT (conversation_id) DO UPDATE SET preview = EXCLUDED.preview, last_updated = EXCLUDED.last_updated`, summary.ConversationID, []byte(summary.LastMessage), summary.LastUpdated)
	if err != nil {
		return fmt.Errorf("write session summary: %w", err)
	}

	return nil
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
