package backend

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	harness "github.com/Rocketable/platform/internal/rocketcode"
)

// Background backfill shares this transaction lock with history mutation. It
// also covers missing summaries and private histories without managed rows.
func lockSessionHistory(ctx context.Context, db stateStoreDB, conversationID string) error {
	_, err := execState(ctx, db, "lock session history", `SELECT pg_advisory_xact_lock(87901, hashtext($1))`, conversationID)
	return err
}

// lockHistories takes several history locks in the one order every transaction
// shares: ordinary histories, then shadow comparison histories, each by bytes.
// Shadow histories go last because lockShadowHistory reaches them through their
// parent's lock. Taking two locks any other way can deadlock against it.
func lockHistories(ctx context.Context, db stateStoreDB, ids []string) error {
	shadows, err := queryStrings(ctx, db, `WITH candidates AS (
    SELECT unnest($1::text[]) AS id
)
SELECT DISTINCT c.id FROM candidates c
JOIN shadow_bindings b ON c.id = b.child_conversation_id OR starts_with(c.id, b.child_conversation_id || '/')`, "shadow history IDs", ids)
	if err != nil {
		return err
	}

	ordered := slices.SortedFunc(slices.Values(ids), func(a, b string) int {
		if shadowA, shadowB := slices.Contains(shadows, a), slices.Contains(shadows, b); shadowA != shadowB {
			if shadowA {
				return 1
			}

			return -1
		}

		return strings.Compare(a, b)
	})

	for _, id := range ordered {
		if err := lockSessionHistory(ctx, db, id); err != nil {
			return err
		}
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

	err = readRows(ctx, db, `SELECT entry_json, entry_timestamp FROM session_entries WHERE conversation_id = $1 ORDER BY id`, "session summary history", func(row rowScanner) error {
		var raw, timestamp string
		if err := row.Scan(&raw, &timestamp); err != nil {
			return fmt.Errorf("scan session summary history: %w", err)
		}

		var entry harness.SessionEntry
		if err := json.Unmarshal([]byte(raw), &entry); err != nil {
			return fmt.Errorf("parse session summary history: %w", err)
		}

		return projectSessionSummary(&summary, &entry, timestamp)
	}, conversationID)

	return summary, err
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

		text = strings.ReplaceAll(text, "\x00", "")

		if (role == "user" || role == "assistant") && strings.TrimSpace(text) != "" {
			summary.LastMessage = text
		}
	}

	if updated, errParse := time.Parse(time.RFC3339Nano, timestamp); errParse == nil {
		summary.LastUpdated = updated.UTC().Truncate(time.Microsecond)
	}

	return nil
}

func saveSessionSummary(ctx context.Context, db stateStoreDB, summary protocol.SessionSummary) error {
	_, err := execState(ctx, db, "write session summary", `INSERT INTO session_summaries (conversation_id, preview, last_updated) VALUES ($1, $2, $3)
ON CONFLICT (conversation_id) DO UPDATE SET preview = EXCLUDED.preview, last_updated = EXCLUDED.last_updated`, summary.ConversationID, []byte(summary.LastMessage), summary.LastUpdated)

	return err
}

func (s *SessionService) backfillSessionSummaries(ctx context.Context) error {
	for {
		conversationIDs, err := queryStrings(ctx, s.db, `SELECT conversation_id FROM
(SELECT conversation_id FROM managed_conversations UNION SELECT conversation_id FROM session_entries) c
WHERE NOT EXISTS (SELECT 1 FROM session_summaries s WHERE s.conversation_id = c.conversation_id)
ORDER BY conversation_id COLLATE "C"`, "missing session summaries")
		if err != nil {
			return err
		}

		if len(conversationIDs) == 0 {
			return nil
		}

		for _, conversationID := range conversationIDs {
			if err := s.backfillSessionSummary(ctx, conversationID); err != nil {
				return err
			}
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
