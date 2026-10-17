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

	return observeTranscriptDB(ctx, s.db, conversationID, from, before, revisions)
}

func observeTranscriptDB(ctx context.Context, db stateStoreDB, conversationID string, from, before int64, revisions map[string]string) ([]ObservedSessionEntry, error) {
	manifest, _ := json.Marshal(revisions)

	return queryRows(ctx, db, `WITH `+sessionHistorySQL+`, saved_turns AS (
    SELECT DISTINCT CASE WHEN synced THEN source_conversation_id ELSE $1 END AS conversation_id, entry->>'turn_id' AS turn_id FROM physical_entries
), readable_turns AS (
    -- A running hidden run that syncs here shows live, after this chat's history.
    SELECT a.*, c.marker, CASE WHEN a.conversation_id = $1 THEN a.history_anchor_id ELSE 9223372036854775807 END AS anchor
    FROM active_turns a CROSS JOIN cutoff c CROSS JOIN visibility v
    LEFT JOIN saved_turns saved ON saved.conversation_id = a.conversation_id AND saved.turn_id = a.id
    WHERE v.readable AND saved.turn_id IS NULL AND (a.conversation_id = $1
            AND (c.marker = '' OR a.history_anchor_id < split_part(c.marker, ':', 1)::bigint)
        OR a.inbound_json->>'SyncDestination' = $1 AND a.terminal = '' AND c.marker = '')
), transcript AS (
    SELECT e.id, e.entry_json, e.source_conversation_id, e.synced,
        FALSE AS active, COALESCE(a.terminal, '') AS terminal, 0::bigint AS checkpoint_timestamp, '' AS turn_id, 0 AS checkpoint, e.id AS position, e.revert_index, e.marker
    FROM effective_entries e LEFT JOIN shadow_turns t
        ON t.child_conversation_id = CASE WHEN e.synced THEN e.source_conversation_id ELSE $1 END AND t.child_turn_id = e.entry->>'turn_id'
    LEFT JOIN active_turns a
        ON a.conversation_id = CASE WHEN e.synced THEN e.source_conversation_id ELSE $1 END AND a.id = e.entry->>'turn_id'
        AND a.terminal = 'stopped' AND COALESCE(t.inbound_json, a.inbound_json)->>'Human' = 'true'
        AND e.entry->>'type' = 'turn' AND COALESCE(e.entry->>'response_id', '') = ''
    WHERE e.id >= $3 AND ($4::bigint = 0 OR e.id < $4)
    UNION ALL
    SELECT 0, (COALESCE(s.value::jsonb->'record', '{}'::jsonb) || jsonb_build_object(
        'version', 1, 'type', 'turn', 'turn_id', a.id, 'output_trace', turn_trace(a.id, a.output_trace_json)
    ))::text, CASE WHEN a.conversation_id = $1 THEN '' ELSE a.conversation_id END, a.conversation_id <> $1,
        a.terminal = '', a.terminal, a.created_at_unix_ns, a.id, 1, a.anchor, -1, a.marker
    FROM readable_turns a LEFT JOIN turn_steps s ON s.conversation_id = a.conversation_id AND s.key = a.id
    WHERE (a.phase <> 'done' OR a.terminal <> '')
        AND CASE WHEN $4::bigint = 0 THEN a.terminal = '' OR a.anchor >= $3
            ELSE a.terminal <> '' AND a.anchor >= $3 AND a.anchor < $4 END
), fingerprinted AS (
    SELECT *, CASE WHEN COALESCE(entry_json::jsonb->>'turn_id', '') <> ''
        THEN 'turn:' || CASE WHEN synced THEN source_conversation_id ELSE $1 END || ':' || (entry_json::jsonb->>'turn_id')
        ELSE id::text END AS key,
        md5(entry_json || terminal || checkpoint_timestamp::text || CASE WHEN revert_index >= 0 THEN marker ELSE '' END) AS revision
    FROM transcript
)
SELECT id, CASE WHEN $2::jsonb->>key IS DISTINCT FROM revision THEN entry_json END,
    source_conversation_id, synced, active, terminal, checkpoint_timestamp, key, revision, revert_index
FROM fingerprinted ORDER BY position, checkpoint, checkpoint_timestamp, turn_id`, "transcript", func(row rowScanner) (ObservedSessionEntry, error) {
		var (
			entry               ObservedSessionEntry
			raw                 sql.NullString
			checkpointTimestamp int64
			index               int
		)
		if err := row.Scan(&entry.ID, &raw, &entry.SourceConversationID, &entry.Synced, &entry.Active, &entry.Terminal, &checkpointTimestamp, &entry.Key, &entry.Revision, &index); err != nil {
			return ObservedSessionEntry{}, fmt.Errorf("scan transcript entry: %w", err)
		}

		if raw.Valid {
			if err := json.Unmarshal([]byte(raw.String), &entry.Entry); err != nil {
				return ObservedSessionEntry{}, fmt.Errorf("decode transcript entry: %w", err)
			}

			clipRevertEntry(&entry.Entry, index)

			if entry.ID == 0 {
				entry.Entry.Timestamp = timeFromUnixNano(checkpointTimestamp)
			}
		}

		return entry, nil
	}, conversationID, string(manifest), from, before)
}

// TranscriptPage returns the saved-entry ID that starts the newest limit
// entries before (zero for no bound), or zero when fewer exist, and the
// conversation's oldest saved-entry ID, which changes only when history is cleared.
func (s *SessionService) TranscriptPage(ctx context.Context, conversationID string, before int64, limit int) (start, oldest int64, err error) {
	err = s.db.QueryRowContext(ctx, transcriptPageSQL, conversationID, before, limit-1).Scan(&start, &oldest)
	if err != nil {
		return 0, 0, fmt.Errorf("read transcript page: %w", err)
	}

	return start, oldest, nil
}

const transcriptPageSQL = `WITH ` + sessionHistorySQL + ` SELECT
    COALESCE((SELECT id FROM effective_entries WHERE ($2::bigint = 0 OR id < $2) ORDER BY id DESC OFFSET $3 LIMIT 1), 0),
    COALESCE((SELECT MIN(id) FROM effective_entries), 0)`
