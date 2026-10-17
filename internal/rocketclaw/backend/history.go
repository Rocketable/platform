package backend

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

// HistoryInventory follows a stateless saved-entry view, not an event cursor.
type HistoryInventory struct {
	ConversationID string            `json:"conversation_id"`
	Source         string            `json:"source"`
	Entries        map[string]string `json:"entries"`
	From           int64             `json:"from,omitempty"`
	Oldest         int64             `json:"oldest,omitempty"`
	Marker         string            `json:"revert_message_id,omitempty"`
	Shadows        string            `json:"shadows,omitempty"`
}

// HistorySnapshot keeps cutoff, page bounds, payloads and delegation links coherent.
type HistorySnapshot struct {
	Inventory   HistoryInventory
	Entries     []ObservedSessionEntry
	Delegations []string
	Shadows     []protocol.ShadowBinding
	Eligible    bool
	Predecessor string
	Reset       bool
}

// ShadowParent resolves comparison history (including delegations) through its
// explicit binding and a private producer's recorded visible destination.
func (s *SessionService) ShadowParent(ctx context.Context, id string) (string, error) {
	var parent string

	err := queryScalar(ctx, s.db, "resolve shadow parent", `SELECT COALESCE(NULLIF(m.producer_inbound_json->>'SyncDestination', ''), b.parent_conversation_id)
FROM shadow_bindings b JOIN managed_conversations m ON m.conversation_id = b.parent_conversation_id
WHERE b.child_conversation_id = $1 OR starts_with($1, b.child_conversation_id || '/')`, &parent, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}

	if err != nil {
		return "", err
	}

	return parent, nil
}

// Public mutations cannot admit work outside a shadow's parent-driven stream.
// Internal persistence (model output, tool effects, and history seeds) stays local.
func rejectShadowMutation(ctx context.Context, db stateStoreDB, id string) error {
	var shadow bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM shadow_bindings WHERE child_conversation_id = $1 OR starts_with($1, child_conversation_id || '/'))`, id).Scan(&shadow); err != nil {
		return fmt.Errorf("read comparison ownership: %w", err)
	}

	if shadow {
		return errors.New("shadow sessions are read-only")
	}

	return nil
}

// ObserveHistory reads every part of a followed view from one database snapshot.
func (s *SessionService) ObserveHistory(ctx context.Context, id, source string, from, before int64, limit int, prior *HistoryInventory) (view HistorySnapshot, err error) {
	previous := *prior

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return view, fmt.Errorf("begin history snapshot: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	marker, eligible, predecessor, err := revertStateDB(ctx, tx, id)
	if err != nil {
		return view, err
	}

	if previous.ConversationID != id || previous.Source != source || previous.Marker != marker {
		previous = HistoryInventory{}
	}

	var oldest int64

	if limit > 0 || before > 0 {
		var start int64
		if err := tx.QueryRowContext(ctx, transcriptPageSQL, id, before, max(limit, 1)-1).Scan(&start, &oldest); err != nil {
			return view, fmt.Errorf("read history page: %w", err)
		}

		switch {
		case limit == 0:
		case before == 0 && previous.Entries != nil && previous.Oldest == oldest:
			from = previous.From
		default:
			from = start
		}
	}

	if before > 0 || previous.Oldest != oldest {
		previous = HistoryInventory{}
	}

	view = HistorySnapshot{Inventory: HistoryInventory{ConversationID: id, Source: source, From: from, Oldest: oldest, Marker: marker}, Eligible: eligible, Predecessor: predecessor, Reset: previous.Entries == nil}

	view.Entries, err = observeTranscriptDB(ctx, tx, id, from, before, previous.Entries)
	if err != nil {
		return view, err
	}

	view.Delegations, err = queryStrings(ctx, tx, delegationsSQL, "history delegations", id, source, from, before)
	if err != nil {
		return view, err
	}

	view.Shadows, err = shadowHistoryDB(ctx, tx, id, source, from, before)
	if err != nil {
		return view, err
	}

	if len(view.Shadows) > 0 {
		data, _ := json.Marshal(view.Shadows)

		view.Inventory.Shadows = fmt.Sprintf("%x", sha256.Sum256(data))
	}

	if err := tx.Commit(); err != nil {
		return view, fmt.Errorf("commit history snapshot: %w", err)
	}

	return view, nil
}

// Shadow metadata uses the same effective parent prefix and page bounds as its
// transcript. Live producers appear before sync; saved copies retain their source
// identity after the producer's entries are pruned.
const shadowHistorySQL = `WITH ` + sessionHistorySQL + `, owners AS (
    SELECT $1::text AS id WHERE (SELECT readable FROM visibility) AND ($2 = '' OR $2 = $1)
    UNION
    SELECT source_conversation_id FROM effective_entries
    WHERE synced AND source_conversation_id <> '' AND ($2 = '' OR source_conversation_id = $2)
        AND id >= $3 AND ($4::bigint = 0 OR id < $4)
    UNION
    SELECT conversation_id FROM managed_conversations
    WHERE producer_inbound_json->>'SyncDestination' = $1 AND $4::bigint = 0
        AND (SELECT readable FROM visibility) AND (SELECT marker FROM cutoff) = ''
        AND ($2 = '' OR conversation_id = $2)
), bindings AS (
    SELECT b.* FROM shadow_bindings b JOIN owners o ON o.id = b.parent_conversation_id
) `

func shadowHistoryDB(ctx context.Context, db stateStoreDB, id, source string, from, before int64) ([]protocol.ShadowBinding, error) {
	bindings, err := queryRows(ctx, db, shadowHistorySQL+`
SELECT parent_conversation_id, parent_agent, shadow_agent, child_conversation_id
FROM bindings ORDER BY parent_conversation_id COLLATE "C", parent_agent COLLATE "C", shadow_agent COLLATE "C"`, "shadow history bindings", func(row rowScanner) (protocol.ShadowBinding, error) {
		var binding protocol.ShadowBinding
		if err := row.Scan(&binding.ParentConversationID, &binding.ParentAgent, &binding.ShadowAgent, &binding.ChildConversationID); err != nil {
			return binding, fmt.Errorf("scan shadow binding: %w", err)
		}

		return binding, nil
	}, id, source, from, before)
	if err != nil {
		return nil, err
	}

	err = readRows(ctx, db, shadowHistorySQL+`
SELECT t.child_conversation_id, t.parent_source_conversation_id, t.parent_turn_id, t.child_turn_id,
    t.id, COALESCE(a.phase = 'running', FALSE) AND t.terminal = '',
    COALESCE(NULLIF(t.terminal, ''), NULLIF(a.terminal, ''),
        CASE WHEN t.child_turn_id <> '' AND (a.id IS NULL OR a.phase <> 'running') THEN 'complete' ELSE '' END),
    md5(COALESCE((SELECT string_agg(e.entry_json::text, '' ORDER BY e.id) FROM session_entries e
        WHERE e.conversation_id = t.child_conversation_id AND e.entry_json->>'turn_id' = t.child_turn_id), '')
        || COALESCE(turn_trace(a.id, a.output_trace_json)::text, '') || COALESCE(s.value::text, ''))
FROM shadow_turns t JOIN bindings b ON b.child_conversation_id = t.child_conversation_id
LEFT JOIN active_turns a ON a.conversation_id = t.child_conversation_id AND a.id = t.child_turn_id
LEFT JOIN turn_steps s ON s.conversation_id = t.child_conversation_id AND s.key = t.child_turn_id
WHERE ($2 = '' OR t.parent_source_conversation_id = $2) AND (
    EXISTS (SELECT 1 FROM effective_entries e WHERE e.entry->>'turn_id' = t.parent_turn_id
        AND CASE WHEN e.synced THEN e.source_conversation_id ELSE $1 END = t.parent_source_conversation_id
        AND e.revert_index < 0 AND e.id >= $3 AND ($4::bigint = 0 OR e.id < $4))
    OR $4::bigint = 0 AND (SELECT marker FROM cutoff) = ''
        AND NOT EXISTS (SELECT 1 FROM physical_entries e WHERE e.entry->>'turn_id' = t.parent_turn_id
            AND CASE WHEN e.synced THEN e.source_conversation_id ELSE $1 END = t.parent_source_conversation_id)
)
ORDER BY t.id`, "shadow turns", func(row rowScanner) error {
		var (
			child string
			turn  protocol.ShadowTurn
		)
		if err := row.Scan(&child, &turn.ParentSourceConversationID, &turn.ParentTurnID, &turn.ChildTurnID, &turn.Order, &turn.Running, &turn.Terminal, &turn.Revision); err != nil {
			return fmt.Errorf("scan shadow turn: %w", err)
		}

		i := slices.IndexFunc(bindings, func(b protocol.ShadowBinding) bool { return b.ChildConversationID == child })
		bindings[i].Turns = append(bindings[i].Turns, turn)

		return nil
	}, id, source, from, before)
	if err != nil {
		return nil, err
	}

	return bindings, nil
}
