package backend

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/openai/openai-go/v3/responses"
)

var errNoBranchPoint = errors.New("a branch can be deleted only where another branch continues from the same point")

const branchSQL = `, tree AS (
    SELECT l.entry_id AS id, l.parent_entry_id, l.parent_length, CASE WHEN jsonb_typeof(e.entry->'replay_input') = 'array' THEN e.entry->'replay_input' ELSE '[]' END AS items
    FROM history_links l LEFT JOIN physical_entries e ON e.id = l.entry_id WHERE l.conversation_id = $1
), nodes AS (
    SELECT t.id, t.items, jsonb_array_length(t.items) AS length, t.parent_entry_id, COALESCE(t.parent_length, jsonb_array_length(p.items)) AS parent_length
    FROM tree t LEFT JOIN tree p ON p.id = t.parent_entry_id
), first_node AS (
    SELECT n.*, $3::integer AS start FROM nodes n WHERE n.id = $2 AND $3::integer < n.length
), branch_point AS (
    SELECT id AS first_id, start, CASE WHEN start > 0 THEN id ELSE parent_entry_id END AS entry_id,
        CASE WHEN start > 0 THEN start ELSE parent_length END AS length FROM first_node
), alternatives AS (
    SELECT n.id FROM nodes n JOIN branch_point b ON n.parent_entry_id = b.entry_id AND n.parent_length = b.length AND n.id <> b.first_id
    UNION ALL SELECT n.id FROM nodes n JOIN branch_point b ON n.id = b.entry_id AND b.start = 0 AND b.length < n.length
), subtree AS (
    SELECT n.id FROM nodes n JOIN branch_point b ON b.start = 0 AND n.id = b.first_id OR b.start > 0 AND n.parent_entry_id = b.first_id AND n.parent_length > b.start
    UNION ALL SELECT n.id FROM subtree s JOIN nodes n ON n.parent_entry_id = s.id
), segment AS (
    SELECT n.id, n.length, 0 AS start FROM subtree JOIN nodes n USING (id)
    UNION ALL SELECT id, length, start FROM first_node WHERE start > 0
), items AS (
    SELECT i.item, i.ordinal > COALESCE(s.start, n.length) AS removed FROM nodes n LEFT JOIN segment s USING (id)
    CROSS JOIN LATERAL jsonb_array_elements(n.items) WITH ORDINALITY AS i(item, ordinal)
), removed_calls AS (
    SELECT item->>'call_id' AS call_id FROM items WHERE removed AND item->>'type' = 'function_call'
    EXCEPT SELECT item->>'call_id' FROM items WHERE NOT removed AND item->>'type' = 'function_call'
), roots AS (
    SELECT c.conversation_id AS id FROM conversations c JOIN removed_calls r USING (call_id) WHERE c.parent_conversation_id = $1
), ` + descendantsSQL + `, leaves AS (
    SELECT s.id FROM segment s LEFT JOIN nodes n ON n.parent_entry_id = s.id AND n.parent_length >= s.length WHERE n.id IS NULL
), inside AS (
    SELECT l.entry_id FROM leaf l JOIN segment s ON s.id = l.entry_id AND COALESCE(l.length, s.length) > s.start
    UNION ALL SELECT a.history_anchor_id FROM turns a JOIN subtree s ON s.id = a.history_anchor_id
    WHERE a.conversation_id = $1 AND a.takes_lane AND a.phase NOT IN ('waiting', 'done')
)
SELECT 'point', COALESCE(entry_id::text || ':' || (length - 1)::text, '') FROM branch_point
UNION ALL SELECT 'alternative', id::text FROM alternatives
UNION ALL SELECT 'entry', id::text FROM subtree
UNION ALL SELECT 'message', '' FROM items WHERE removed AND item->>'role' IN ('user', 'assistant')
UNION ALL SELECT 'leaf', '' FROM leaves
UNION ALL SELECT 'call', call_id FROM removed_calls
UNION ALL SELECT 'delegation', id FROM roots
UNION ALL SELECT 'delegation', conversation_id FROM descendants
UNION ALL SELECT 'job', j.input_id FROM turns j JOIN removed_calls r USING (call_id)
    WHERE j.conversation_id = $1 AND NOT j.takes_lane AND j.phase = 'running' AND j.subagent_key <> ''
UNION ALL SELECT 'inside', '' FROM inside
UNION ALL SELECT 'queued', '' FROM turns WHERE conversation_id = $1 AND phase = 'waiting' AND reason = $4`

type branchShape struct {
	counts                   protocol.BranchDeletion
	point                    string
	entries                  []int64
	calls, delegations, jobs []string
}

func readBranchDB(ctx context.Context, db stateStoreDB, conversationID string, first int64, start int) (branchShape, error) {
	var (
		shape        branchShape
		found        bool
		alternatives int
	)

	err := readRows(ctx, db, `WITH `+sessionHistorySQL+branchSQL, "branch", func(row rowScanner) error {
		var kind, value string
		if err := row.Scan(&kind, &value); err != nil {
			return fmt.Errorf("scan branch: %w", err)
		}

		switch kind {
		case "point":
			found, shape.point = true, value
		case "alternative":
			alternatives++
		case "entry":
			id, _ := strconv.ParseInt(value, 10, 64)
			shape.entries = append(shape.entries, id)
		case "message":
			shape.counts.Messages++
		case "leaf":
			shape.counts.NestedBranches++
		case "call":
			shape.calls = append(shape.calls, value)
		case "delegation":
			shape.delegations = append(shape.delegations, value)
		case "job":
			shape.jobs = append(shape.jobs, value)
		case "inside":
			shape.counts.Current = true
		case "queued":
			shape.counts.QueuedMessages++
		}

		return nil
	}, conversationID, first, start, waitQueued)

	switch {
	case err != nil:
		return shape, err
	case !found:
		return shape, ErrRevertMessageGone
	case shape.point == "" || alternatives == 0:
		return shape, errNoBranchPoint
	}

	shape.counts.NestedBranches--
	shape.counts.SubagentSessions = len(shape.delegations)

	if !shape.counts.Current {
		shape.counts.QueuedMessages = 0
	}

	return shape, nil
}

func (r *Runtime) DeleteBranch(ctx context.Context, conversationID, messageID string, dryRun bool) (protocol.BranchDeletion, error) {
	first, start, err := parseMessageID(messageID)
	if err != nil {
		return protocol.BranchDeletion{}, err
	}

	if err := rejectShadowMutation(ctx, r.Sessions.db, conversationID); err != nil {
		return protocol.BranchDeletion{}, err
	}

	_, eligible, _, err := r.Sessions.RevertState(ctx, conversationID)
	if err != nil {
		return protocol.BranchDeletion{}, err
	}

	if !eligible {
		return protocol.BranchDeletion{}, errRevertConversation
	}

	branch, err := readBranchDB(ctx, r.Sessions.db, conversationID, first, start)
	if err != nil || dryRun {
		return branch.counts, err
	}

	b, err := r.holdHistory(ctx, conversationID)
	if err != nil {
		return protocol.BranchDeletion{}, err
	}

	defer func() {
		releaseHistory([]*Bridge{b})

		_ = b.pickLaterWork(context.WithoutCancel(ctx), false)
	}()

	if branch.counts.Current {
		if _, _, err := r.moveCurrentPoint(ctx, b, conversationID, branch.point, "", protocol.RevertThrough); err != nil {
			return protocol.BranchDeletion{}, err
		}
	}

	for _, job := range branch.jobs {
		if _, err := r.background.stop(ctx, conversationID, job, errStoppedWithOwner); err != nil {
			return protocol.BranchDeletion{}, err
		}
	}

	tx, err := r.Sessions.beginStateTx(ctx, "branch deletion")
	if err != nil {
		return protocol.BranchDeletion{}, err
	}

	defer func() { _ = tx.Rollback() }()

	if err := lockShadowHistory(ctx, tx, conversationID); err != nil {
		return protocol.BranchDeletion{}, err
	}

	doomed, err := readBranchDB(ctx, tx, conversationID, first, start)
	if err != nil {
		return protocol.BranchDeletion{}, err
	}

	if doomed.counts.Current {
		return protocol.BranchDeletion{}, errHistoryBusy
	}

	if _, err := execState(ctx, tx, "delete branch turns", `WITH doomed_entries AS (
    SELECT id, entry_json->>'turn_id' AS turn_id FROM entries WHERE id = ANY($2::bigint[])
), doomed_turns AS (
    SELECT DISTINCT a.id, a.takes_lane, CASE WHEN a.takes_lane THEN a.id ELSE a.input_id END AS step_key FROM turns a
    LEFT JOIN doomed_entries d ON d.turn_id = a.id OR d.id = a.history_anchor_id
    WHERE a.conversation_id = $1 AND a.phase = 'done' AND (a.takes_lane AND d.id IS NOT NULL OR NOT a.takes_lane AND a.call_id = ANY($3::text[]))
), compared_turns AS (
    SELECT id FROM doomed_turns UNION SELECT turn_id FROM doomed_entries
), removed_steps AS (
    DELETE FROM steps s USING doomed_turns d WHERE s.conversation_id = $1 AND (d.takes_lane AND s.key = d.step_key OR starts_with(s.key, d.step_key || '/'))
), removed_shadow_turns AS (
    DELETE FROM shadow_turns s USING compared_turns c WHERE s.parent_source_conversation_id = $1 AND s.parent_turn_id = c.id RETURNING s.child_conversation_id, s.child_turn_id
), removed_comparisons AS (
    DELETE FROM entries e USING removed_shadow_turns s WHERE e.conversation_id = s.child_conversation_id AND e.entry_json->>'turn_id' = s.child_turn_id RETURNING e.id, e.conversation_id
), removed_comparison_links AS (
    DELETE FROM history_links l USING removed_comparisons c WHERE l.conversation_id = c.conversation_id AND l.entry_id = c.id
)
DELETE FROM turns a USING doomed_turns d WHERE a.conversation_id = $1 AND a.id = d.id`, conversationID, doomed.entries, doomed.calls); err != nil {
		return protocol.BranchDeletion{}, err
	}

	if _, err := execState(ctx, tx, "delete branch entries", `DELETE FROM entries WHERE conversation_id = $1 AND id = ANY($2)`, conversationID, doomed.entries); err != nil {
		return protocol.BranchDeletion{}, err
	}

	if _, err := execState(ctx, tx, "delete branch links", `DELETE FROM history_links WHERE conversation_id = $1 AND entry_id = ANY($2)`, conversationID, doomed.entries); err != nil {
		return protocol.BranchDeletion{}, err
	}

	if err := deleteDelegationsDB(ctx, tx, doomed.delegations); err != nil {
		return protocol.BranchDeletion{}, err
	}

	if start > 0 {
		if err := clipEntryDB(ctx, tx, first, start); err != nil {
			return protocol.BranchDeletion{}, err
		}
	}

	if err := refreshPrincipals(ctx, tx, []string{conversationID}); err != nil {
		return protocol.BranchDeletion{}, err
	}

	if err := refreshPathSummary(ctx, tx, conversationID); err != nil {
		return protocol.BranchDeletion{}, err
	}

	if err := tx.Commit(); err != nil {
		return protocol.BranchDeletion{}, fmt.Errorf("commit branch deletion: %w", err)
	}

	return branch.counts, nil
}

const branchPathSQL = `, branch_nodes AS (
    SELECT entry_id AS id, parent_entry_id, parent_length, seq FROM history_links WHERE conversation_id = $1
), branch AS (
    SELECT id, seq FROM branch_nodes WHERE id = $2::bigint
    UNION ALL SELECT n.id, n.seq FROM branch b JOIN branch_nodes n ON n.parent_entry_id = b.id AND (b.id <> $2 OR COALESCE(n.parent_length > $3::integer, TRUE))
), newest_leaf AS (
    SELECT id FROM branch ORDER BY seq DESC LIMIT 1
), branch_path AS (
    SELECT id AS entry_id, -1 AS length, 0 AS depth FROM newest_leaf
    UNION ALL SELECT n.parent_entry_id, COALESCE(n.parent_length, -1), p.depth + 1 FROM branch_path p JOIN branch_nodes n ON n.id = p.entry_id WHERE p.entry_id <> $2
)
SELECT e.id, e.entry_json, e.source_conversation_id, e.synced, p.length FROM branch_path p JOIN physical_entries e ON e.id = p.entry_id ORDER BY p.depth DESC`

func (s *SessionService) BranchEntries(ctx context.Context, conversationID, branchID string) ([]ObservedSessionEntry, error) {
	first, start, err := parseMessageID(branchID)
	if err != nil {
		return nil, err
	}

	active, err := s.HasActiveTurn(ctx, conversationID)
	if err != nil {
		return nil, err
	}

	if active {
		return nil, errHistoryBusy
	}

	entries, err := queryRows(ctx, s.db, `WITH `+sessionHistorySQL+branchPathSQL, "branch path", scanObservedEntry, conversationID, first, start)
	if err != nil {
		return nil, err
	}

	if len(entries) == 0 || start >= len(entries[0].Entry.ReplayInput) {
		return nil, ErrRevertMessageGone
	}

	entries[0].Entry.ReplayInput = entries[0].Entry.ReplayInput[start:]

	return entries, nil
}

func (s *SessionService) AppendBranchSummary(ctx context.Context, conversationID, branchID, summary string) (bool, error) {
	first, start, err := parseMessageID(branchID)
	if err != nil {
		return false, err
	}

	tx, err := s.beginStateTx(ctx, "branch summary")
	if err != nil {
		return false, err
	}

	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, conversationID); err != nil {
		return false, err
	}

	active, err := s.HasActiveTurn(ctx, conversationID)
	if err != nil {
		return false, err
	}

	if active {
		return false, errHistoryBusy
	}

	var onPath bool
	if err := queryScalar(ctx, tx, "read summarized branch", `WITH `+sessionHistorySQL+`
SELECT count(*) > 0 FROM effective_entries WHERE id = $2::bigint AND (revert_index < 0 OR revert_index > $3::integer)`, &onPath, conversationID, first, start); err != nil || onPath {
		return false, err
	}

	replay, _ := harness.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{harness.PromptInputMessage(&harness.PromptInput{
		Texts: []string{"[System]\n\nSummary of another branch of this chat:\n\n" + summary}, Header: "[System]",
	})})
	if _, err := appendSessionEntryDB(ctx, tx, conversationID, &harness.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Now().UTC(), ReplayInput: replay}); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit branch summary: %w", err)
	}

	return true, nil
}
