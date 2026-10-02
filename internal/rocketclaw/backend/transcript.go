package backend

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Changes registers before yielding an initial refresh, then waits for committed
// changes to this conversation. Breaking iteration or cancelling ctx discards
// the listening connection rather than returning LISTEN state to the pool.
func (s *SessionService) Changes(ctx context.Context, conversationID string) iter.Seq2[protocol.ConversationChange, error] {
	return func(yield func(protocol.ConversationChange, error) bool) {
		conn, err := s.db.Conn(ctx)
		if err != nil {
			if ctx.Err() == nil {
				yield(protocol.ConversationChange{}, fmt.Errorf("connect transcript listener: %w", err))
			}

			return
		}
		defer func() {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			_ = conn.Close()
		}()

		var channel string

		err = conn.QueryRowContext(ctx, `SELECT 'rocketclaw_transcript_' || md5(current_schema())`).Scan(&channel)
		if err == nil {
			_, err = conn.ExecContext(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize())
		}

		if err != nil {
			if ctx.Err() == nil {
				yield(protocol.ConversationChange{}, fmt.Errorf("listen for transcript changes: %w", err))
			}

			return
		}

		if !yield(protocol.ConversationChange{ConversationID: conversationID}, nil) {
			return
		}

		for {
			var change protocol.ConversationChange

			err := conn.Raw(func(raw any) error {
				notification, err := raw.(*stdlib.Conn).Conn().WaitForNotification(ctx)
				if err != nil {
					return fmt.Errorf("wait for transcript change: %w", err)
				}

				if err := json.Unmarshal([]byte(notification.Payload), &change); err != nil {
					return fmt.Errorf("decode transcript change: %w", err)
				}

				return nil
			})
			if err != nil {
				if ctx.Err() == nil {
					yield(protocol.ConversationChange{}, err)
				}

				return
			}

			if change.ConversationID == conversationID && !yield(change, nil) {
				return
			}
		}
	}
}

// ObserveTranscript returns the ordered inventory from one database snapshot,
// with payloads only for keys whose fingerprints differ from revisions.
// Nil revisions requests all payloads. A saved logical turn supersedes its checkpoint.
// Turn keys are producer-scoped and stable across checkpoint completion.
// Checkpoints stay after the saved row visible when first persisted, preserving ID order.
// Rows are limited to positions in [from, before); zero before means no upper
// bound and also includes every running checkpoint. Positive before reads a
// settled page, so running checkpoints are excluded.
// Source attribution matches ObserveEntries; authorization belongs to the caller.
// Fingerprints describe readable content, not the transaction IDs yielded by Changes.
func (s *SessionService) ObserveTranscript(ctx context.Context, conversationID string, from, before int64, revisions map[string]string) ([]ObservedSessionEntry, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, errors.New("conversation ID is required")
	}

	manifest, _ := json.Marshal(revisions)

	entries, err := queryRows(ctx, s.db, `WITH saved AS (
    SELECT id, entry_json::text AS entry_json, entry_json::jsonb AS entry FROM session_entries
    WHERE conversation_id = $1 AND id >= $3 AND ($4::bigint = 0 OR id < $4)
), attributed AS (
    SELECT destination.id, destination.entry_json, destination.entry,
        COALESCE(destination.entry->>'sync_source_conversation_id', source.conversation_id, '') AS source_conversation_id,
        destination.entry ? 'sync_source_entry_id' AS synced
    FROM saved destination
    LEFT JOIN session_entries source ON source.id = (destination.entry->>'sync_source_entry_id')::bigint
), transcript AS (
    SELECT id, entry_json, source_conversation_id, synced,
        FALSE AS active, '' AS terminal, 0::bigint AS checkpoint_timestamp, '' AS turn_id, 0 AS checkpoint, id AS position
    FROM attributed
    UNION ALL
    SELECT 0, jsonb_build_object(
        'version', 1, 'type', 'turn', 'turn_id', a.id,
        'agent', a.agent, 'model', a.display_model, 'reasoning_effort', a.reasoning_effort_json::jsonb,
        'replay_input', a.replay_input_json::jsonb, 'replay_attribution', a.replay_attribution_json::jsonb,
        'output_trace', a.output_trace_json::jsonb, 'token_usage', a.token_usage_json::jsonb,
        'response_id', a.response_id
    )::text, '', FALSE, a.terminal = '', a.terminal, a.created_at_unix_ns, a.id, 1, a.history_anchor_id
    FROM active_turns a
    WHERE a.conversation_id = $1
        AND CASE WHEN $4::bigint = 0 THEN a.terminal = '' OR a.history_anchor_id >= $3
            ELSE a.terminal <> '' AND a.history_anchor_id >= $3 AND a.history_anchor_id < $4 END
        AND NOT EXISTS (
        SELECT 1 FROM attributed WHERE entry->>'turn_id' = a.id AND (NOT synced OR source_conversation_id = $1)
    )
), fingerprinted AS (
    SELECT *, CASE WHEN COALESCE(entry_json::jsonb->>'turn_id', '') <> ''
        THEN 'turn:' || CASE WHEN synced THEN source_conversation_id ELSE $1 END || ':' || (entry_json::jsonb->>'turn_id')
        ELSE id::text END AS key,
        md5(entry_json || terminal || checkpoint_timestamp::text) AS revision
    FROM transcript
)
SELECT id, CASE WHEN $2::jsonb->>key IS DISTINCT FROM revision THEN entry_json END,
    source_conversation_id, synced, active, terminal, checkpoint_timestamp, key, revision
FROM fingerprinted ORDER BY position, checkpoint, checkpoint_timestamp, turn_id`, "transcript", func(row rowScanner) (ObservedSessionEntry, error) {
		var (
			entry               ObservedSessionEntry
			raw                 sql.NullString
			checkpointTimestamp int64
		)
		if err := row.Scan(&entry.ID, &raw, &entry.SourceConversationID, &entry.Synced, &entry.Active, &entry.Terminal, &checkpointTimestamp, &entry.Key, &entry.Revision); err != nil {
			return ObservedSessionEntry{}, fmt.Errorf("scan transcript entry: %w", err)
		}

		if raw.Valid {
			if err := json.Unmarshal([]byte(raw.String), &entry.Entry); err != nil {
				return ObservedSessionEntry{}, fmt.Errorf("decode transcript entry: %w", err)
			}

			if entry.ID == 0 {
				entry.Entry.Timestamp = timeFromUnixNano(checkpointTimestamp)
			}
		}

		return entry, nil
	}, conversationID, string(manifest), from, before)
	if err != nil {
		return nil, err
	}

	return entries, nil
}

// TranscriptPage returns the saved-entry ID that starts the newest limit
// entries before (zero for no bound), or zero when fewer exist, and the
// conversation's oldest saved-entry ID, which changes only when history is cleared.
func (s *SessionService) TranscriptPage(ctx context.Context, conversationID string, before int64, limit int) (start, oldest int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT
    COALESCE((SELECT id FROM session_entries WHERE conversation_id = $1 AND ($2::bigint = 0 OR id < $2) ORDER BY id DESC OFFSET $3 LIMIT 1), 0),
    COALESCE((SELECT MIN(id) FROM session_entries WHERE conversation_id = $1), 0)`, conversationID, before, limit-1).Scan(&start, &oldest)
	if err != nil {
		return 0, 0, fmt.Errorf("read transcript page: %w", err)
	}

	return start, oldest, nil
}

// SetActiveTurnTerminal retains a failed or stopped checkpoint for observation
// while preventing startup recovery from resuming it.
func (s *SessionService) SetActiveTurnTerminal(ctx context.Context, turnID string, terminal protocol.Terminal) error {
	_, err := s.db.ExecContext(ctx, `UPDATE active_turns SET terminal = $2, updated_at_unix_ns = $3 WHERE id = $1`, turnID, terminal, timeUnixNano(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("set active turn terminal: %w", err)
	}

	return nil
}

// OriginPairs reads only the initial caller metadata, never the producer transcript.
func (s *SessionService) OriginPairs(ctx context.Context, conversationID string) (map[string]string, error) {
	var raw string

	err := s.db.QueryRowContext(ctx, `SELECT entry_json FROM session_entries
WHERE conversation_id = $1 AND entry_json::jsonb->>'type' = $2 ORDER BY id LIMIT 1`, conversationID, externalMCPOriginPairsEntryType).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read origin metadata: %w", err)
	}

	var entry ObservedSessionEntry
	if err := json.Unmarshal([]byte(raw), &entry.Entry); err != nil {
		return nil, fmt.Errorf("decode origin metadata: %w", err)
	}

	pairs, _ := OriginPairsFromEntry(&entry.Entry)

	return pairs, nil
}
