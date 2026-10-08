package backend

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
)

// Physical saved turns resolve checkpoint supersession before the exclusive
// boundary is applied. All history readers share this inventory.
const sessionHistorySQL = `visibility AS (
    SELECT NOT EXISTS (SELECT 1 FROM managed_conversations parent WHERE parent.revert_message_id <> ''
            AND starts_with($1, parent.conversation_id || '/') AND NOT EXISTS (
                SELECT 1 FROM session_entries p
                CROSS JOIN LATERAL jsonb_array_elements(NULLIF(p.entry_json::jsonb->'replay_input', 'null'::jsonb)) WITH ORDINALITY AS items(item, ordinal)
                WHERE p.conversation_id = parent.conversation_id AND item->>'type' = 'function_call'
                    AND (starts_with($1 || '/', parent.conversation_id || '/' || (item->>'call_id') || '/')
                        OR starts_with($1, parent.conversation_id || '/' || (item->>'call_id') || '-'))
                    AND (p.id < split_part(parent.revert_message_id, ':', 1)::bigint OR
                        p.id = split_part(parent.revert_message_id, ':', 1)::bigint AND ordinal <= split_part(parent.revert_message_id, ':', 2)::integer))) AS readable
), physical_entries AS (
    SELECT e.id, e.entry_json::text AS entry_json, e.entry_json::jsonb AS entry,
        COALESCE(e.entry_json->>'sync_source_conversation_id', source.conversation_id, '') AS source_conversation_id,
        e.entry_json::jsonb ? 'sync_source_entry_id' AS synced
    FROM session_entries e
    LEFT JOIN session_entries source ON source.id = (e.entry_json->>'sync_source_entry_id')::bigint
    WHERE e.conversation_id = $1 AND (SELECT readable FROM visibility)
), cutoff AS (
    SELECT COALESCE((SELECT revert_message_id FROM managed_conversations WHERE conversation_id = $1), '') AS marker
), effective_entries AS (
    SELECT e.*, CASE WHEN e.id = NULLIF(split_part(c.marker, ':', 1), '')::bigint
        THEN split_part(c.marker, ':', 2)::integer ELSE -1 END AS revert_index, c.marker
    FROM physical_entries e CROSS JOIN cutoff c
    WHERE c.marker = '' OR e.id < split_part(c.marker, ':', 1)::bigint
        OR e.id = split_part(c.marker, ':', 1)::bigint AND split_part(c.marker, ':', 2)::integer > 0
)`

// clipRevertEntry retains a partial turn without its abandoned output or usage.
// OpenCode V2: packages/core/src/session/projector.ts:717-773.
func clipRevertEntry(entry *rocketcode.SessionEntry, index int) {
	if index < 0 {
		return
	}

	entry.ReplayInput = entry.ReplayInput[:index]
	entry.ResponseID, entry.OutputTrace, entry.TokenUsage = "", nil, nil

	entry.ReplayAttribution = slices.DeleteFunc(entry.ReplayAttribution, func(a rocketcode.ReplayAttribution) bool { return a.Start >= index })
	for i := range entry.ReplayAttribution {
		entry.ReplayAttribution[i].End = min(entry.ReplayAttribution[i].End, index)
	}
}

// revertBoundary resolves an opaque saved message ID against physical replay.
// Caller holds the history lock for any mutation.
func revertBoundary(ctx context.Context, db stateStoreDB, conversationID, messageID string) (ObservedSessionEntry, int, error) {
	idText, indexText, ok := strings.Cut(messageID, ":")
	id, errID := strconv.ParseInt(idText, 10, 64)

	index, errIndex := strconv.Atoi(indexText)
	if !ok || errID != nil || errIndex != nil || id <= 0 || index < 0 || strconv.FormatInt(id, 10) != idText || strconv.Itoa(index) != indexText {
		return ObservedSessionEntry{}, 0, errors.New("invalid revert message ID")
	}

	entry := ObservedSessionEntry{ID: id}

	var raw string
	if err := db.QueryRowContext(ctx, `SELECT entry_json FROM session_entries WHERE conversation_id = $1 AND id = $2`, conversationID, id).Scan(&raw); err != nil {
		return entry, 0, fmt.Errorf("read revert boundary: %w", err)
	}

	if err := json.Unmarshal([]byte(raw), &entry.Entry); err != nil {
		return entry, 0, fmt.Errorf("decode revert boundary: %w", err)
	}

	if index >= len(entry.Entry.ReplayInput) {
		return entry, 0, errors.New("revert message is no longer in the session")
	}

	items, err := rocketcode.ReplayInputToParams(entry.Entry.ReplayInput[index : index+1])
	if err != nil {
		return entry, 0, fmt.Errorf("decode revert boundary: %w", err)
	}

	role, _, found, err := ReplayInputMessageRoleText(&items[0], entry.Entry.ReplayInput[index])
	if err != nil {
		return entry, 0, err
	}

	if !found || role != "user" {
		return entry, 0, errors.New("revert boundary must be a user message")
	}

	return entry, index, nil
}

// RevertState reports the durable cutoff and capability from physical origin
// facts. Empty effective history must not erase a conversation's origin.
func (s *SessionService) RevertState(ctx context.Context, conversationID string) (marker string, eligible bool, predecessor string, err error) {
	marker, eligible, predecessor, err = revertStateDB(ctx, s.db, conversationID)
	if err != nil {
		return "", false, "", fmt.Errorf("observe session revert: %w", err)
	}

	return marker, eligible, predecessor, nil
}

func revertStateDB(ctx context.Context, db stateStoreDB, conversationID string) (marker string, eligible bool, predecessor string, err error) {
	err = db.QueryRowContext(ctx, `WITH `+sessionHistorySQL+` SELECT c.marker,
    EXISTS (SELECT 1 FROM managed_conversations m WHERE m.conversation_id = $1
        AND m.created_by <> $2 AND strpos(m.conversation_id, '/') = 0
        AND m.conversation_id NOT LIKE 'slack-thread:%'
        AND m.conversation_id NOT LIKE 'cron:%' AND m.conversation_id NOT LIKE 'one-off-cron:%'
        AND m.conversation_id NOT LIKE 'web:cron:%' AND m.conversation_id NOT LIKE 'web:one-off-cron:%'
        AND NOT EXISTS (SELECT 1 FROM external_mcp_sessions x WHERE x.managed_conversation_id = $1 OR x.private_conversation_id = $1)
        AND COALESCE((SELECT source_conversation_id FROM physical_entries ORDER BY id LIMIT 1), '') NOT LIKE 'cron:%'
        AND COALESCE((SELECT source_conversation_id FROM physical_entries ORDER BY id LIMIT 1), '') NOT LIKE 'one-off-cron:%'
        AND COALESCE((SELECT source_conversation_id FROM physical_entries ORDER BY id LIMIT 1), '') NOT LIKE 'slack-thread:%'
        AND NOT EXISTS (SELECT 1 FROM external_mcp_sessions x WHERE
            (SELECT source_conversation_id FROM physical_entries ORDER BY id LIMIT 1) IN (x.private_conversation_id, x.managed_conversation_id))),
    COALESCE((SELECT e.id::text || ':' || (ordinal - 1)::text FROM effective_entries e
        CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry->'replay_input', 'null'::jsonb)) WITH ORDINALITY AS items(item, ordinal)
        WHERE item->>'role' = 'user' AND (e.revert_index < 0 OR ordinal <= e.revert_index)
        ORDER BY e.id DESC, ordinal DESC LIMIT 1), '') FROM cutoff c`, conversationID, ThreadCreatedByCron).Scan(&marker, &eligible, &predecessor)
	if err != nil {
		return "", false, "", fmt.Errorf("read revert state: %w", err)
	}

	return marker, eligible, predecessor, nil
}

// stageRevertDB changes metadata only. Runtime owns interruption and settlement.
func stageRevertDB(ctx context.Context, db stateStoreDB, conversationID, before string) (cutoff, prompt string, err error) {
	marker, eligible, predecessor, err := revertStateDB(ctx, db, conversationID)
	if err != nil {
		return "", "", err
	}

	if !eligible {
		return "", "", errors.New("revert requires a top-level pure Web conversation")
	}

	if before == "" {
		before = predecessor
		if before == "" {
			return marker, "", nil
		}
	}

	entry, index, err := revertBoundary(ctx, db, conversationID, before)
	if err != nil {
		return "", "", err
	}

	if marker != "" {
		idText, indexText, _ := strings.Cut(marker, ":")
		id, _ := strconv.ParseInt(idText, 10, 64)

		boundary, _ := strconv.Atoi(indexText)
		if entry.ID > id || entry.ID == id && index >= boundary {
			return "", "", errors.New("revert message is not visible")
		}
	}

	items, err := rocketcode.ReplayInputToParams(entry.Entry.ReplayInput[index : index+1])
	if err != nil {
		return "", "", fmt.Errorf("decode reverted request: %w", err)
	}

	_, text, _, err := ReplayInputMessageRoleText(&items[0], entry.Entry.ReplayInput[index])
	if err != nil {
		return "", "", err
	}
	// Goal replay includes generated steering instructions. Its existing durable
	// owner keeps the editable command and the exact admission identity.
	var goalPrompt string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE((SELECT inbound_json->'Metadata'->>'web_goal_prompt' FROM active_turns
        WHERE conversation_id = $1 AND inbound_json->'Metadata'->>'web_message_id' = $2 LIMIT 1), '')`, conversationID, items[0].OfMessage.ExtraFields()["input_id"]).Scan(&goalPrompt); err != nil {
		return "", "", fmt.Errorf("read reverted goal prompt: %w", err)
	}

	if goalPrompt != "" {
		text = goalPrompt
	}

	if _, err := db.ExecContext(ctx, `UPDATE managed_conversations SET revert_message_id = $2 WHERE conversation_id = $1`, conversationID, before); err != nil {
		return "", "", fmt.Errorf("stage revert: %w", err)
	}

	return before, text, nil
}

// commitRevertDB is called only inside prepared durable admission's transaction.
func commitRevertDB(ctx context.Context, db stateStoreDB, conversationID, marker string) error {
	entry, index, err := revertBoundary(ctx, db, conversationID, marker)
	if err != nil {
		return err
	}

	discarded, err := queryStrings(ctx, db, `SELECT id FROM active_turns WHERE conversation_id = $1 AND
        (history_anchor_id >= $2 OR id = $3 OR id IN (SELECT entry_json->>'turn_id' FROM session_entries WHERE conversation_id = $1 AND id >= $2))`, "discarded revert turns", conversationID, entry.ID, entry.Entry.TurnID)
	if err != nil {
		return err
	}
	// Children are conversation records, never handles for undoing tool effects. A task
	// subagent's or permission review's key adds a hash to its call ID, and a running
	// Background Job's subagent keeps its history to finish, as $stop leaves background work running.
	children, err := queryStrings(ctx, db, `WITH `+sessionHistorySQL+`
    SELECT DISTINCT child.conversation_id FROM (
        SELECT conversation_id FROM session_entries UNION SELECT conversation_id FROM active_turns
    ) child
    WHERE starts_with(child.conversation_id, $1 || '/') AND NOT EXISTS (
        SELECT 1 FROM effective_entries e CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry->'replay_input', 'null'::jsonb)) WITH ORDINALITY AS items(item, ordinal)
        WHERE item->>'type' = 'function_call' AND (e.revert_index < 0 OR ordinal <= e.revert_index)
            AND (starts_with(child.conversation_id || '/', $1 || '/' || (item->>'call_id') || '/') OR starts_with(child.conversation_id, $1 || '/' || (item->>'call_id') || '-')))
    AND NOT EXISTS (SELECT 1 FROM background_jobs j WHERE j.conversation_id = $1 AND j.status = 'running' AND j.subagent_key <> ''
        AND starts_with(child.conversation_id || '/', $1 || j.subagent_key || '/'))`, "discarded revert delegations", conversationID)
	if err != nil {
		return err
	}

	for _, child := range children {
		for _, query := range []string{`DELETE FROM session_entries WHERE conversation_id = $1`, `DELETE FROM active_turns WHERE conversation_id = $1`, `DELETE FROM turn_steps WHERE conversation_id = $1`} {
			if _, err := db.ExecContext(ctx, query, child); err != nil {
				return fmt.Errorf("prune revert delegation: %w", err)
			}
		}
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM turn_steps s WHERE conversation_id = $1 AND EXISTS (SELECT 1 FROM unnest($2::text[]) id WHERE key = id OR starts_with(key, id || '/'))
    AND NOT EXISTS (SELECT 1 FROM background_jobs j WHERE j.conversation_id = s.conversation_id AND j.status = 'running' AND starts_with(s.key, j.job_id || '/'))`, conversationID, discarded); err != nil {
		return fmt.Errorf("prune revert journals: %w", err)
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM active_turns WHERE conversation_id = $1 AND id = ANY($2)`, conversationID, discarded); err != nil {
		return fmt.Errorf("prune revert turns: %w", err)
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM session_entries WHERE conversation_id = $1 AND (id > $2 OR id = $2 AND $3 = 0)`, conversationID, entry.ID, index); err != nil {
		return fmt.Errorf("prune revert suffix: %w", err)
	}

	if index > 0 {
		clipRevertEntry(&entry.Entry, index)

		data, err := json.Marshal(entry.Entry)
		if err != nil {
			return fmt.Errorf("encode revert prefix: %w", err)
		}

		if _, err := db.ExecContext(ctx, `WITH unindexed AS (DELETE FROM message_search WHERE entry_id = $1 AND replay_index >= $3)
UPDATE session_entries SET entry_json = $2 WHERE id = $1`, entry.ID, string(data), index); err != nil {
			return fmt.Errorf("save revert prefix: %w", err)
		}
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM thread_queue WHERE conversation_id = $1 AND source = $2`, conversationID, protocol.SourceWeb); err != nil {
		return fmt.Errorf("prune reverted human queue: %w", err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE managed_conversations SET revert_message_id = '' WHERE conversation_id = $1`, conversationID); err != nil {
		return fmt.Errorf("commit revert marker: %w", err)
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM session_summaries WHERE conversation_id = $1`, conversationID); err != nil {
		return fmt.Errorf("reset reverted summary: %w", err)
	}

	summary, err := loadSessionSummary(ctx, db, conversationID)
	if err != nil {
		return err
	}

	return saveSessionSummary(ctx, db, summary)
}

// StageRevert settles the recorded bridge before changing its cutoff. Like
// packages/app/src/session/revert.ts, interruption is not effect rollback.
func (r *Runtime) StageRevert(ctx context.Context, conversationID, before string) (marker, prompt string, err error) {
	_, eligible, predecessor, err := r.Sessions.RevertState(ctx, conversationID)
	if err != nil {
		return "", "", err
	}

	if !eligible {
		return "", "", errors.New("revert requires a top-level pure Web conversation")
	}

	if before == "" && predecessor == "" {
		marker, _, _, err := r.Sessions.RevertState(ctx, conversationID)
		return marker, "", err
	}

	if _, _, err := revertBoundary(ctx, r.Sessions.db, conversationID, cmp.Or(before, predecessor)); err != nil {
		return "", "", err
	}

	b, err := r.threads.recordedBridge(conversationID)
	if err != nil {
		return "", "", err
	}

	b.mu.Lock()
	if b.historyMutation {
		b.mu.Unlock()
		return "", "", errors.New("conversation history is busy")
	}

	b.historyMutation, b.inputOpen = true, false
	settlement := b.settlement

	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.historyMutation = false
		b.mu.Unlock()

		if err != nil {
			_ = b.pickLaterWork(ctx, false)
		}
	}()

	if err := r.Sessions.StopGoal(conversationID); err != nil {
		return "", "", err
	}

	b.InterruptActiveTurn()

	if settlement != nil {
		select {
		case <-ctx.Done():
			return "", "", fmt.Errorf("wait for revert settlement: %w", ctx.Err())
		case <-settlement.done:
			if settlement.err != nil {
				return "", "", settlement.err
			}
		}
	}

	b.mu.Lock()
	for draining := true; draining; {
		select {
		case request := <-b.requestCh:
			err = b.preserveRevertRequestLocked(ctx, &request)
			draining = err == nil
		default:
			draining = false
		}
	}
	b.mu.Unlock()

	if err != nil {
		return "", "", err
	}

	tx, err := r.Sessions.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", fmt.Errorf("begin revert staging: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, conversationID); err != nil {
		return "", "", err
	}

	if active, err := r.Sessions.HasActiveTurn(ctx, conversationID); err != nil || active {
		return "", "", errors.Join(err, errors.New("revert requires settled delivery"))
	}

	marker, prompt, err = stageRevertDB(ctx, tx, conversationID, before)
	if err != nil {
		return "", "", err
	}

	if err := tx.Commit(); err != nil {
		return "", "", fmt.Errorf("commit staged revert: %w", err)
	}

	return marker, prompt, nil
}

// ClearRevert restores the whole suffix without changing any viewer's draft.
func (r *Runtime) ClearRevert(ctx context.Context, conversationID string) error {
	b, err := r.threads.recordedBridge(conversationID)
	if err != nil {
		return err
	}

	b.mu.Lock()
	if b.historyMutation {
		b.mu.Unlock()
		return errors.New("conversation history is busy")
	}

	tx, err := r.Sessions.db.BeginTx(ctx, nil)
	if err == nil {
		defer func() { _ = tx.Rollback() }()

		err = lockSessionHistory(ctx, tx, conversationID)
	}

	if err == nil {
		_, eligible, _, errState := revertStateDB(ctx, tx, conversationID)

		err = errState
		if err == nil && !eligible {
			err = errors.New("revert requires a top-level pure Web conversation")
		}
	}

	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE managed_conversations SET revert_message_id = '' WHERE conversation_id = $1`, conversationID)
	}

	if err == nil {
		err = tx.Commit()
	}
	b.mu.Unlock()

	if err != nil {
		return fmt.Errorf("clear revert: %w", err)
	}

	return b.pickLaterWork(ctx, false)
}

// preserveRevertRequestLocked transfers an uninjected input's queue and completion
// ownership together. Recorded input IDs, not the drain cursor, decide injection.
func (b *Bridge) preserveRevertRequestLocked(ctx context.Context, request *bridgeRequest) (err error) {
	defer func() {
		if err != nil && request.completion != nil {
			request.completion.err = err
			close(request.completion.done)
			request.completion = nil // Failed transfer releases the caller, not its durable input.
		}
	}()
	// A new stage abandons prepared execution, but not its durable input. Redo
	// prepares it again just like restart recovery does.
	if err := request.closeWorkflow(); err != nil {
		return err
	}

	if request.inbound == nil || !request.inbound.Human && !request.scheduledMessageRecurring {
		return nil
	}

	request.queueItemID = cmp.Or(request.queueItemID, request.inbound.Metadata["web_message_id"], rand.Text())

	var recorded bool
	if err := b.config.SessionService.db.QueryRowContext(ctx, `SELECT EXISTS (
    SELECT 1 FROM session_entries e CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry_json::jsonb->'replay_input', 'null'::jsonb)) item
    WHERE e.conversation_id = $1 AND item->>'input_id' = $2
    UNION ALL SELECT 1 FROM turn_steps s CROSS JOIN LATERAL jsonb_array_elements(NULLIF(s.value::jsonb->'record'->'replay_input', 'null'::jsonb)) item
    WHERE s.conversation_id = $1 AND item->>'input_id' = $2)`, b.config.ConversationID, request.queueItemID).Scan(&recorded); err != nil {
		return fmt.Errorf("classify waiting reverted input: %w", err)
	}

	if recorded {
		if request.completion != nil {
			close(request.completion.done)
		}

		return nil
	}

	if slices.ContainsFunc(b.waiting, func(waiting bridgeRequest) bool { return waiting.queueItemID == request.queueItemID }) {
		return nil
	}

	tx, err := b.config.SessionService.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin waiting input preservation: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, b.config.ConversationID); err != nil {
		return err
	}

	var position int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT position FROM thread_queue WHERE conversation_id = $1 AND queue_item_id = $2), -1)`, b.config.ConversationID, request.queueItemID).Scan(&position); err != nil {
		return fmt.Errorf("read waiting input position: %w", err)
	}

	if position < 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE thread_queue SET position = position + 1 WHERE conversation_id = $1 AND position >= $2`, b.config.ConversationID, len(b.waiting)); err != nil {
			return fmt.Errorf("make room for waiting input: %w", err)
		}

		item := protocol.ThreadQueueItem{ID: request.queueItemID, ConversationID: b.config.ConversationID, Message: request.inbound.Text,
			Source: request.inbound.Source, Principal: request.inbound.Metadata[protocol.InboundPrincipalMetadataKey], Kind: protocol.InboundKindEnqueue,
			Inbound: request.inbound, StashAt: time.Now().UTC(), Position: len(b.waiting)}
		if err := putThreadQueueItem(ctx, tx, item.ID, &item); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE thread_queue SET position = position + 1 WHERE conversation_id = $1 AND queue_item_id <> $2 AND position >= $3 AND position < $4`, b.config.ConversationID, request.queueItemID, len(b.waiting), position); err != nil {
			return fmt.Errorf("reorder waiting input: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `UPDATE thread_queue SET position = $3 WHERE conversation_id = $1 AND queue_item_id = $2`, b.config.ConversationID, request.queueItemID, len(b.waiting)); err != nil {
			return fmt.Errorf("move waiting input: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit waiting input preservation: %w", err)
	}

	b.waiting = append(b.waiting, *request)

	return nil
}

// mutateWebQueue serializes explicit release with staging and branch admission.
func (r *Runtime) mutateWebQueue(ctx context.Context, conversationID, id string, kind protocol.InboundKind) (bool, error) {
	b, err := r.threads.recordedBridge(conversationID)
	if err != nil {
		return false, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.historyMutation {
		return false, errors.New("conversation history is busy")
	}

	tx, err := r.Sessions.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin Web queue mutation: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, conversationID); err != nil {
		return false, err
	}

	var marker string

	err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT revert_message_id FROM managed_conversations WHERE conversation_id = $1), '')`, conversationID).Scan(&marker)
	if err != nil {
		return false, fmt.Errorf("read queue mutation cutoff: %w", err)
	}

	if marker != "" {
		return false, errors.New("conversation history is reverted")
	}

	if kind == protocol.InboundKindEnqueue {
		changed, err := execRows(ctx, tx, "pop Web queue item", "count popped queue items", `UPDATE thread_queue SET kind = $3, park_after = '',
            inbound_json = jsonb_set(inbound_json::jsonb, '{Kind}', to_jsonb($3::text)),
            position = (SELECT COALESCE(MAX(position), -1) + 1 FROM thread_queue WHERE conversation_id = $1 AND park_after = '')
            WHERE conversation_id = $1 AND queue_item_id = $2 AND kind = $4`, conversationID, id, kind, protocol.InboundKindHeld)
		if err != nil || changed == 0 {
			return false, err
		}

		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit popped Web input: %w", err)
		}
		b.mu.Unlock()
		err = b.pickLaterWork(ctx, false)
		b.mu.Lock()

		return true, err
	}

	item, claimed, err := (stateDAO{db: tx}).claimThreadQueueItem(ctx, conversationID, id)
	if err != nil || !claimed {
		return false, err
	}

	inbound := item.Inbound
	if inbound == nil {
		content := item.Content
		content.Text = item.Message
		inbound = protocol.NewInboundMessageFromContent(item.Source, kind, &content, true)
		inbound.ConversationID = conversationID
		inbound.Metadata["web_message_id"], inbound.Metadata[protocol.InboundPrincipalMetadataKey] = id, item.Principal
	}

	inbound.Kind, item.Inbound, item.Kind, item.ParkAfter = kind, inbound, kind, ""
	if err := putThreadQueueItem(ctx, tx, id, &item); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit promoted Web input: %w", err)
	}
	b.mu.Unlock()
	err = b.enqueue(ctx, &bridgeRequest{inbound: inbound, queueItemID: id}, "promote Web input")
	b.mu.Lock()

	return true, err
}

// admitWeb gives a prepared prompt a durable queue owner before waking the
// bridge. Pruning and acceptance share the same history transaction.
func (r *Runtime) admitWeb(ctx context.Context, b *Bridge, request *bridgeRequest, goal protocol.GoalRequest) (fresh bool, err error) {
	inbound := request.inbound
	if inbound.Metadata["web_message_id"] == "" {
		if inbound.Metadata == nil {
			inbound.Metadata = make(map[string]string)
		}

		inbound.Metadata["web_message_id"] = rand.Text()
	}

	accepted, err := reconcileWebDB(ctx, r.Sessions.db, inbound)
	if err != nil || accepted {
		return false, err
	}

	if strings.TrimSpace(goal.CheckScript) != "" {
		if err := ValidateGoalCheckScriptStart(r.Cfg, b.agentSnapshot(), goal.CheckScript); err != nil {
			return false, fmt.Errorf("validate goal check script: %w", err)
		}
	}

	if inbound.Workflow.Name != "" {
		if err := b.prepareWorkflow(request); err != nil {
			return false, err
		}

		defer func() {
			if !fresh {
				err = errors.Join(err, request.closeWorkflow())
			}
		}()
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.historyMutation {
		return false, errors.New("conversation history is busy")
	}

	tx, err := r.Sessions.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin Web admission: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, inbound.ConversationID); err != nil {
		return false, err
	}

	accepted, err = reconcileWebDB(ctx, tx, inbound)
	if err != nil || accepted {
		return false, err
	}

	var marker string

	err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT revert_message_id FROM managed_conversations WHERE conversation_id = $1), '')`, inbound.ConversationID).Scan(&marker)
	if err != nil {
		return false, fmt.Errorf("read admission cutoff: %w", err)
	}

	if marker != "" {
		if err := commitRevertDB(ctx, tx, inbound.ConversationID, marker); err != nil {
			return false, err
		}
	}

	if goal.Objective != "" {
		if err := beginGoalDB(ctx, tx, inbound.ConversationID, &GoalState{Objective: goal.Objective, CheckScript: goal.CheckScript, MaxTurns: goal.MaxTurns}); err != nil {
			return false, err
		}
	}

	item := protocol.ThreadQueueItem{ID: inbound.Metadata["web_message_id"], ConversationID: inbound.ConversationID, Message: inbound.Text,
		Principal: inbound.Metadata[protocol.InboundPrincipalMetadataKey], Kind: inbound.Kind, Source: inbound.Source, Inbound: inbound, StashAt: time.Now().UTC(),
		Content: protocol.InboundContent{Attachments: inbound.Attachments, AttachmentPresence: inbound.AttachmentPresence, AttachmentWarnings: inbound.AttachmentWarnings}}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), -1) + 1 FROM thread_queue WHERE conversation_id = $1`, inbound.ConversationID).Scan(&item.Position); err != nil {
		return false, fmt.Errorf("read Web admission position: %w", err)
	}

	if err := putThreadQueueItem(ctx, tx, item.ID, &item); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit Web admission: %w", err)
	}

	if marker != "" {
		for i := range b.waiting {
			waiting := &b.waiting[i]
			if waiting.completion != nil {
				waiting.completion.err = context.Canceled
				close(waiting.completion.done)
			}
		}

		b.waiting = nil
	}

	return true, nil
}

func reconcileWebDB(ctx context.Context, db stateStoreDB, inbound *protocol.InboundMessage) (bool, error) {
	id := inbound.Metadata["web_message_id"]

	var raw []byte

	err := db.QueryRowContext(ctx, `SELECT inbound_json FROM active_turns WHERE conversation_id = $1 AND inbound_json->'Metadata'->>'web_message_id' = $2
    UNION ALL SELECT inbound_json FROM thread_queue WHERE conversation_id = $1 AND queue_item_id = $2 LIMIT 1`, inbound.ConversationID, id).Scan(&raw)
	if err == nil && raw != nil {
		var stored protocol.InboundMessage
		if err := json.Unmarshal(raw, &stored); err != nil {
			return false, fmt.Errorf("decode admitted Web input: %w", err)
		}

		attachmentsMatch := slices.Equal(attachmentsFromInbound(stored.Attachments), attachmentsFromInbound(inbound.Attachments))
		if stored.Text != inbound.Text || stored.Source != inbound.Source || stored.Metadata[protocol.InboundPrincipalMetadataKey] != inbound.Metadata[protocol.InboundPrincipalMetadataKey] || stored.Metadata["web_goal"] != inbound.Metadata["web_goal"] || !attachmentsMatch || stored.AttachmentPresence != inbound.AttachmentPresence || !slices.Equal(stored.AttachmentWarnings, inbound.AttachmentWarnings) || stored.Workflow != inbound.Workflow || stored.GoalAction != inbound.GoalAction {
			return false, errors.New("message ID was already accepted with different content")
		}

		return true, nil
	}

	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("read admitted Web input: %w", err)
	}

	err = db.QueryRowContext(ctx, `SELECT item::text FROM session_entries e
    CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry_json::jsonb->'replay_input', 'null'::jsonb)) item
    WHERE e.conversation_id = $1 AND item->>'input_id' = $2 LIMIT 1`, inbound.ConversationID, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("read saved Web input: %w", err)
	}

	items, err := rocketcode.ReplayInputToParams([]json.RawMessage{raw})
	if err != nil {
		return false, fmt.Errorf("decode saved Web replay: %w", err)
	}

	_, text, _, err := ReplayInputMessageRoleText(&items[0], raw)
	if err != nil {
		return false, err
	}

	var message replayMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return false, fmt.Errorf("decode saved Web message: %w", err)
	}

	var parts []portableContent
	if strings.HasPrefix(string(message.Content), "[") {
		if err := json.Unmarshal(message.Content, &parts); err != nil {
			return false, fmt.Errorf("decode saved Web content: %w", err)
		}
	}

	var attachments []rocketcode.Attachment

	for _, part := range parts {
		if part.Type == "input_image" || part.Type == "input_file" {
			url := cmp.Or(part.ImageURL, part.FileData, part.FileURL)
			mime, _, _ := strings.Cut(strings.TrimPrefix(url, "data:"), ";")
			attachments = append(attachments, rocketcode.Attachment{MIME: mime, Filename: part.Filename, URL: url})
		}
	}

	expectedAttachments := attachmentsFromInbound(inbound.Attachments)
	for i := range expectedAttachments {
		if strings.HasPrefix(expectedAttachments[i].MIME, "image/") {
			expectedAttachments[i].Filename = "" // Image replay carries bytes, not a filename.
		}
	}

	header, expected, _ := strings.Cut(buildPrompt(inbound, nil), "\n\n")
	expectedIdentity, _, _ := strings.Cut(header, " additional_instructions=")

	identity, _, _ := strings.Cut(message.Header, " additional_instructions=")
	if text != expected || strings.TrimSuffix(identity, "]") != strings.TrimSuffix(expectedIdentity, "]") || !slices.Equal(attachments, expectedAttachments) {
		return false, errors.New("message ID was already accepted with different content")
	}

	return true, nil
}
