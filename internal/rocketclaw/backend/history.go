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
	Delegations []Delegation
	// Lineage is the conversations it nests under, from its top-level conversation down, then itself.
	Lineage []SidebarSession
	// Reviews are the Delegations that are permission or guardrail reviews.
	Reviews     []string
	Shadows     []protocol.ShadowBinding
	Eligible    bool
	Predecessor string
	// Floor is the newest message undo must keep (revertStateDB), or "".
	Floor string
	Reset bool
}

// ancestorsSQL pairs each conversation id of starts with itself and every conversation it nests
// under, by parent link, depth levels up. A query using it starts WITH RECURSIVE.
const ancestorsSQL = `ancestors AS (
    SELECT id, id AS conversation_id, 0 AS depth FROM starts
    UNION SELECT a.id, c.parent_conversation_id, a.depth + 1 FROM ancestors a JOIN conversations c ON c.conversation_id = a.conversation_id
    WHERE c.parent_conversation_id IS NOT NULL
)`

// descendantsSQL lists every conversation nested under a conversation id of roots, by parent
// link. A query using it starts WITH RECURSIVE.
const descendantsSQL = `descendants AS (
    SELECT c.conversation_id FROM roots r JOIN conversations c ON c.parent_conversation_id = r.id
    UNION SELECT c.conversation_id FROM descendants d JOIN conversations c ON c.parent_conversation_id = d.conversation_id
)`

// ShadowParent resolves comparison history (including delegations) through its
// explicit binding and a private producer's recorded visible destination.
func (s *SessionService) ShadowParent(ctx context.Context, id string) (string, error) {
	var parent string

	err := queryScalar(ctx, s.db, "resolve shadow parent", `WITH RECURSIVE starts AS (
    SELECT $1::text AS id
), `+ancestorsSQL+`
SELECT COALESCE(m.reports_to, b.shadow_parent) FROM ancestors a JOIN conversations b ON b.conversation_id = a.conversation_id
JOIN conversations m ON m.conversation_id = b.shadow_parent WHERE b.shadow_parent IS NOT NULL`, &parent, id)
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
	if err := db.QueryRowContext(ctx, `WITH RECURSIVE starts AS (
    SELECT $1::text AS id
), `+ancestorsSQL+`
SELECT count(*) > 0 FROM ancestors a JOIN conversations b ON b.conversation_id = a.conversation_id WHERE b.shadow_parent IS NOT NULL`, id).Scan(&shadow); err != nil {
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

	marker, eligible, predecessor, floor, err := revertStateDB(ctx, tx, id)
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

	view = HistorySnapshot{Inventory: HistoryInventory{ConversationID: id, Source: source, From: from, Oldest: oldest, Marker: marker}, Eligible: eligible, Predecessor: predecessor, Floor: floor, Reset: previous.Entries == nil}

	view.Entries, err = observeTranscriptDB(ctx, tx, id, from, before, previous.Entries)
	if err != nil {
		return view, err
	}

	view.Delegations, err = queryRows(ctx, tx, delegationsSQL, "history delegations", scanDelegation, id, source, from, before)
	if err != nil {
		return view, err
	}

	view.Lineage, err = queryRows(ctx, tx, `WITH RECURSIVE starts AS (
    SELECT $1::text AS id
), `+ancestorsSQL+`
SELECT c.conversation_id, c.kind, c.agent FROM ancestors a JOIN conversations c ON c.conversation_id = a.conversation_id ORDER BY a.depth DESC`,
		"history lineage", func(row rowScanner) (SidebarSession, error) {
			var level SidebarSession
			if err := row.Scan(&level.Conversation.ID, &level.Kind, &level.Conversation.Agent); err != nil {
				return SidebarSession{}, fmt.Errorf("scan history lineage: %w", err)
			}

			return level, nil
		}, id)
	if err != nil {
		return view, err
	}

	ids := make([]string, len(view.Delegations))
	for i := range view.Delegations {
		ids[i] = view.Delegations[i].ID
	}

	view.Reviews, err = queryStrings(ctx, tx, `SELECT conversation_id FROM conversations WHERE conversation_id = ANY($1) AND kind = $2`, "history reviews", ids, KindReview)
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
// transcript. Live runs reporting here appear before they save.
const shadowHistorySQL = `WITH ` + sessionHistorySQL + `, owners AS (
    SELECT $1::text AS id FROM viewer WHERE readable AND ($2 = '' OR $2 = $1)
    UNION
    SELECT source_conversation_id FROM effective_entries
    WHERE synced AND source_conversation_id <> '' AND ($2 = '' OR source_conversation_id = $2)
        AND id >= $3 AND ($4::bigint = 0 OR id < $4)
    UNION
    SELECT r.conversation_id FROM reporting_runs r CROSS JOIN viewer v
    WHERE $4::bigint = 0 AND v.readable AND v.marker = '' AND ($2 = '' OR r.conversation_id = $2)
), bindings AS (
    SELECT b.shadow_parent AS parent_conversation_id, b.parent_agent, b.shadow_agent, b.conversation_id AS child_conversation_id
    FROM conversations b JOIN owners o ON o.id = b.shadow_parent
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

	err = readRows(ctx, db, shadowHistorySQL+`, child_turns AS (
    SELECT t.* FROM shadow_turns t JOIN bindings b ON b.child_conversation_id = t.child_conversation_id WHERE $2 = '' OR t.parent_source_conversation_id = $2
), child_entries AS (
    SELECT t.id, string_agg(e.entry_json::text, '' ORDER BY e.id) AS saved FROM child_turns t
    JOIN entries e ON e.conversation_id = t.child_conversation_id AND e.entry_json->>'turn_id' = t.child_turn_id GROUP BY t.id
), shown_parents AS (
    SELECT DISTINCT entry->>'turn_id' AS turn_id, CASE WHEN synced THEN source_conversation_id ELSE $1 END AS source FROM effective_entries
    WHERE revert_index < 0 AND id >= $3 AND ($4::bigint = 0 OR id < $4)
), saved_parents AS (
    SELECT DISTINCT entry->>'turn_id' AS turn_id, CASE WHEN synced THEN source_conversation_id ELSE $1 END AS source FROM physical_entries
)
SELECT t.child_conversation_id, t.parent_source_conversation_id, t.parent_turn_id, t.child_turn_id,
    t.id, COALESCE(a.phase = 'running', FALSE) AND t.terminal = '',
    COALESCE(NULLIF(t.terminal, ''), NULLIF(a.terminal, ''),
        CASE WHEN t.child_turn_id <> '' AND (a.id IS NULL OR a.phase <> 'running') THEN 'complete' ELSE '' END),
    md5(COALESCE(c.saved, '') || COALESCE(turn_trace(a.id, a.output_trace_json)::text, '') || COALESCE(s.value::text, a.record::text, ''))
FROM child_turns t CROSS JOIN cutoff k LEFT JOIN child_entries c ON c.id = t.id
LEFT JOIN turns a ON a.conversation_id = t.child_conversation_id AND a.id = t.child_turn_id
LEFT JOIN steps s ON s.conversation_id = t.child_conversation_id AND s.key = t.child_turn_id
LEFT JOIN shown_parents p ON p.turn_id = t.parent_turn_id AND p.source = t.parent_source_conversation_id
LEFT JOIN saved_parents v ON v.turn_id = t.parent_turn_id AND v.source = t.parent_source_conversation_id
WHERE p.turn_id IS NOT NULL OR $4::bigint = 0 AND k.marker = '' AND v.turn_id IS NULL
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
