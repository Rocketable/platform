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

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func lockSessionHistory(ctx context.Context, db stateStoreDB, conversationID string) error {
	_, err := execState(ctx, db, "lock session history", `SELECT 1 FROM conversations WHERE conversation_id = $1 FOR NO KEY UPDATE`, conversationID)
	return err
}

func lockHistories(ctx context.Context, db stateStoreDB, ids []string) error {
	if len(ids) == 1 {
		return lockSessionHistory(ctx, db, ids[0])
	}

	shadows, err := queryStrings(ctx, db, `WITH RECURSIVE starts AS (
    SELECT unnest($1::text[]) AS id
), `+ancestorsSQL+`
SELECT DISTINCT a.id FROM ancestors a JOIN conversations b ON b.conversation_id = a.conversation_id WHERE b.shadow_parent IS NOT NULL`, "shadow history IDs", ids)
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

func loadSessionSummary(ctx context.Context, db stateStoreDB, conversationID string) (protocol.SessionSummary, error) {
	summary := protocol.SessionSummary{ConversationID: conversationID}

	var preview []byte

	err := db.QueryRowContext(ctx, `SELECT preview, last_updated FROM conversations WHERE conversation_id = $1 AND last_updated IS NOT NULL`, conversationID).Scan(&preview, &summary.LastUpdated)
	if err == nil {
		summary.LastMessage = string(preview)
		return summary, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return summary, fmt.Errorf("read session summary: %w", err)
	}

	err = readRows(ctx, db, `SELECT entry_json, entry_timestamp FROM entries WHERE conversation_id = $1 ORDER BY id`, "session summary history", func(row rowScanner) error {
		var (
			raw       sql.RawBytes
			timestamp string
		)
		if err := row.Scan(&raw, &timestamp); err != nil {
			return fmt.Errorf("scan session summary history: %w", err)
		}

		var entry harness.SessionEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
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

const entryFactsSQL = `WITH own AS (
    SELECT id FROM entries WHERE conversation_id = $1 ORDER BY id LIMIT 1
), reported AS (
    SELECT e.id, e.conversation_id AS source FROM conversations m JOIN entries e USING (conversation_id)
    WHERE m.reports_to = $1 ORDER BY e.id LIMIT 1
), firsts AS (
    SELECT id, NULL::text AS source, TRUE AS own FROM own
    UNION ALL SELECT id, source, FALSE FROM reported
), facts AS (
    SELECT COALESCE(bool_or(own), FALSE) AS has_entries, (array_agg(source ORDER BY id))[1] AS first_source FROM firsts
)`

func saveSessionSummary(ctx context.Context, db stateStoreDB, summary protocol.SessionSummary) error {
	_, err := execState(ctx, db, "write session summary", entryFactsSQL+`
UPDATE conversations SET preview = $2, last_updated = $3, has_entries = f.has_entries, first_source = f.first_source FROM facts f
WHERE conversation_id = $1`, summary.ConversationID, []byte(summary.LastMessage), summary.LastUpdated)

	return err
}
