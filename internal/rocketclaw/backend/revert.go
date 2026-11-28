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

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

var errRevertConversation = errors.New("revert requires a Web chat, Slack thread, or shadow")

var (
	ErrInvalidRevertMessage = errors.New("invalid revert message ID")
	ErrRevertMessageGone    = errors.New("revert message is no longer in the session")
)

const popQueueItemSQL = `WITH tail AS (
    SELECT COALESCE(MAX(position), -1) + 1 AS position FROM turns WHERE conversation_id = $1 AND phase = 'waiting' AND park_after = ''
)
UPDATE turns t SET reason = $5, delivery = $3, park_after = '', inbound_json = jsonb_set(t.inbound_json::jsonb, '{Kind}', to_jsonb($3::text)), position = tail.position
FROM tail WHERE t.conversation_id = $1 AND t.input_id = $2 AND t.phase = 'waiting' AND t.delivery = $4`

const sessionHistorySQL = `RECURSIVE cutoff AS (
    SELECT COALESCE(max(revert_message_id), '') AS marker FROM conversations WHERE conversation_id = $1
), reporting_runs AS (
    SELECT conversation_id FROM conversations WHERE reports_to = $1
    UNION SELECT continues FROM conversations WHERE conversation_id = $1 AND continues IS NOT NULL
), viewed_rows AS (
    SELECT e.id, e.conversation_id, e.entry_json FROM history_links l JOIN entries e ON e.id = l.entry_id WHERE l.conversation_id = $1
), attributed_rows AS (
    SELECT id, entry_json, entry_json::jsonb AS entry, conversation_id <> $1 AS synced,
        CASE WHEN conversation_id <> $1 THEN conversation_id ELSE '' END AS source_conversation_id
    FROM viewed_rows
), physical_entries AS (
    SELECT id, entry_json, source_conversation_id, synced, CASE WHEN synced AND jsonb_typeof(entry->'replay_input') = 'array'
        THEN jsonb_set(entry, '{replay_input}', jsonb_path_query_array(entry->'replay_input', '$[*] ? (!(@.type == "compaction" || @.type == "compaction_summary"))'))
        ELSE entry END AS entry
    FROM attributed_rows
), marker_point AS (
    SELECT NULLIF(split_part(marker, ':', 1), '')::bigint AS entry_id, NULLIF(split_part(marker, ':', 2), '')::integer AS length FROM cutoff
), latest_link AS (
    SELECT entry_id FROM history_links WHERE conversation_id = $1 ORDER BY seq DESC LIMIT 1
), leaf AS (
    SELECT $1::text AS conversation_id, CASE WHEN m.length = 0 THEN l.parent_entry_id ELSE COALESCE(m.entry_id, n.entry_id) END AS entry_id,
        CASE WHEN m.length = 0 THEN l.parent_length ELSE m.length END AS length
    FROM marker_point m LEFT JOIN latest_link n ON TRUE LEFT JOIN history_links l ON l.conversation_id = $1 AND l.entry_id = m.entry_id
), current_path AS (
    SELECT conversation_id, entry_id, length FROM leaf
    UNION ALL SELECT p.conversation_id, l.parent_entry_id, l.parent_length FROM current_path p
    JOIN history_links l ON l.conversation_id = p.conversation_id AND l.entry_id = p.entry_id WHERE l.parent_entry_id IS NOT NULL
), effective_entries AS (
    SELECT e.*, CASE WHEN p.length < jsonb_array_length(CASE WHEN jsonb_typeof(e.entry->'replay_input') = 'array' THEN e.entry->'replay_input' END)
        THEN p.length ELSE -1 END AS revert_index
    FROM current_path p JOIN physical_entries e ON e.id = p.entry_id
), off_path_turns AS (
    SELECT a.id FROM turns a LEFT JOIN current_path p ON p.entry_id = a.history_anchor_id
    WHERE a.conversation_id = $1 AND a.terminal <> '' AND a.history_anchor_id <> 0 AND p.entry_id IS NULL
)`

func clipRevertEntry(entry *harness.SessionEntry, index int) {
	if index < 0 {
		return
	}

	entry.ReplayInput = entry.ReplayInput[:index]
	entry.ResponseID, entry.OutputTrace, entry.TokenUsage = "", nil, nil

	entry.ReplayAttribution = slices.DeleteFunc(entry.ReplayAttribution, func(a harness.ReplayAttribution) bool { return a.Start >= index })
	for i := range entry.ReplayAttribution {
		entry.ReplayAttribution[i].End = min(entry.ReplayAttribution[i].End, index)
	}
}

func parseMessageID(messageID string) (id int64, index int, err error) {
	idText, indexText, ok := strings.Cut(messageID, ":")
	id, errID := strconv.ParseInt(idText, 10, 64)

	index, errIndex := strconv.Atoi(indexText)
	if !ok || errID != nil || errIndex != nil || id <= 0 || index < 0 || strconv.FormatInt(id, 10) != idText || strconv.Itoa(index) != indexText {
		return 0, 0, ErrInvalidRevertMessage
	}

	return id, index, nil
}

func revertBoundary(ctx context.Context, db stateStoreDB, conversationID, messageID string, boundary protocol.RevertBoundary) (ObservedSessionEntry, int, error) {
	id, index, err := parseMessageID(messageID)
	if err != nil {
		return ObservedSessionEntry{}, 0, err
	}

	entry := ObservedSessionEntry{ID: id}

	var raw string

	err = db.QueryRowContext(ctx, `WITH `+sessionHistorySQL+`
SELECT entry_json::text, synced FROM physical_entries WHERE id = $2`, conversationID, id).Scan(&raw, &entry.Synced)
	if errors.Is(err, sql.ErrNoRows) {
		return entry, 0, ErrRevertMessageGone
	}

	if err != nil {
		return entry, 0, fmt.Errorf("read revert boundary: %w", err)
	}

	if err := json.Unmarshal([]byte(raw), &entry.Entry); err != nil {
		return entry, 0, fmt.Errorf("decode revert boundary: %w", err)
	}

	if err := visibleEntry(&entry); err != nil {
		return entry, 0, err
	}

	if index >= len(entry.Entry.ReplayInput) {
		return entry, 0, ErrRevertMessageGone
	}

	end := index + 1
	if boundary == protocol.RevertThrough {
		end = min(index+2, len(entry.Entry.ReplayInput))
	}

	items, err := harness.ReplayInputToParams(entry.Entry.ReplayInput[index:end])
	if err != nil {
		return entry, 0, fmt.Errorf("decode revert boundary: %w", err)
	}

	roles := []string{"", "user"}
	for i := range items {
		if roles[i], _, _, err = ReplayInputMessageRoleText(&items[i], entry.Entry.ReplayInput[index+i]); err != nil {
			return entry, 0, err
		}
	}

	if roles[0] != "user" && (boundary != protocol.RevertThrough || roles[0] != "assistant" && roles[1] != "user") {
		return entry, 0, errors.New("revert boundary must be a user message, or an agent reply to move through")
	}

	return entry, index, nil
}

func (s *SessionService) RevertState(ctx context.Context, conversationID string) (marker string, eligible bool, predecessor string, err error) {
	marker, eligible, predecessor, _, err = revertStateDB(ctx, s.db, conversationID)
	if err != nil {
		return "", false, "", fmt.Errorf("observe session revert: %w", err)
	}

	return marker, eligible, predecessor, nil
}

func revertStateDB(ctx context.Context, db stateStoreDB, conversationID string) (marker string, eligible bool, predecessor, floor string, err error) {
	err = db.QueryRowContext(ctx, `WITH `+sessionHistorySQL+`, chat AS (
    SELECT kind FROM conversations WHERE conversation_id = $1
), floor AS (
    SELECT e.id, items.ordinal - 1 AS idx FROM effective_entries e LEFT JOIN chat m ON TRUE
    CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry->'replay_input', 'null'::jsonb)) WITH ORDINALITY AS items(item, ordinal)
    WHERE (e.revert_index < 0 OR items.ordinal <= e.revert_index)
        AND `+outsideItemSQL+`
    ORDER BY e.id DESC, items.ordinal DESC LIMIT 1
), previous AS (
    SELECT e.id::text || ':' || (ordinal - 1)::text AS id FROM effective_entries e LEFT JOIN floor f ON TRUE
    CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry->'replay_input', 'null'::jsonb)) WITH ORDINALITY AS items(item, ordinal)
    WHERE item->>'role' = 'user' AND (e.revert_index < 0 OR ordinal <= e.revert_index) AND (f.id IS NULL OR e.id > f.id OR e.id = f.id AND ordinal - 1 > f.idx)
    ORDER BY e.id DESC, ordinal DESC LIMIT 1
)
SELECT c.marker, COALESCE(m.kind IN ('web_chat', 'slack_thread', 'shadow'), FALSE), COALESCE(p.id, ''), COALESCE(f.id::text || ':' || f.idx::text, '')
FROM cutoff c LEFT JOIN chat m ON TRUE LEFT JOIN previous p ON TRUE LEFT JOIN floor f ON TRUE`, conversationID).Scan(&marker, &eligible, &predecessor, &floor)
	if err != nil {
		return "", false, "", "", fmt.Errorf("read revert state: %w", err)
	}

	return marker, eligible, predecessor, floor, nil
}

func stageRevertDB(ctx context.Context, db stateStoreDB, conversationID, target string, boundary protocol.RevertBoundary) (marker, prompt string, err error) {
	marker, eligible, predecessor, floor, err := revertStateDB(ctx, db, conversationID)
	if err != nil {
		return "", "", err
	}

	if !eligible {
		return "", "", errRevertConversation
	}

	if target == "" {
		target, boundary = predecessor, protocol.RevertBefore
		if target == "" {
			return marker, "", nil
		}
	}

	entry, index, err := revertBoundary(ctx, db, conversationID, target, boundary)
	if err != nil {
		return "", "", err
	}

	length := index
	if boundary == protocol.RevertThrough {
		length++
	}

	if floor != "" {
		var kept bool
		if err := queryScalar(ctx, db, "read revert floor ancestry", `WITH RECURSIVE target AS (
    SELECT $2::bigint AS entry_id, $3::integer AS length
    UNION ALL SELECT l.parent_entry_id, l.parent_length FROM target t JOIN history_links l ON l.conversation_id = $1 AND l.entry_id = t.entry_id
    WHERE l.parent_entry_id IS NOT NULL
)
SELECT count(*) > 0 FROM target WHERE entry_id = split_part($4, ':', 1)::bigint AND (length IS NULL OR length > split_part($4, ':', 2)::integer)`, &kept, conversationID, entry.ID, length, floor); err != nil {
			return "", "", err
		}

		if !kept {
			return "", "", errors.New("revert cannot go back past a message from a routine run, External MCP, or Slack")
		}
	}

	if boundary != protocol.RevertThrough {
		items, _ := harness.ReplayInputToParams(entry.Entry.ReplayInput[index : index+1])
		if _, prompt, _, err = ReplayInputMessageRoleText(&items[0], entry.Entry.ReplayInput[index]); err != nil {
			return "", "", err
		}

		var goalPrompt string
		if err := db.QueryRowContext(ctx, `WITH goal AS (SELECT inbound_json->'Metadata'->>'web_goal_prompt' AS prompt FROM turns
    WHERE conversation_id = $1 AND phase <> 'waiting' AND takes_lane AND inbound_json->'Metadata'->>'web_message_id' = $2 LIMIT 1)
SELECT COALESCE(max(prompt), '') FROM goal`, conversationID, items[0].OfMessage.ExtraFields()["input_id"]).Scan(&goalPrompt); err != nil {
			return "", "", fmt.Errorf("read reverted goal prompt: %w", err)
		}

		prompt = cmp.Or(goalPrompt, prompt)
	}

	if err := queryScalar(ctx, db, "stage revert", `WITH latest AS (
    SELECT entry_id FROM history_links WHERE conversation_id = $1 ORDER BY seq DESC LIMIT 1
)
UPDATE conversations c SET revert_message_id = CASE WHEN l.entry_id = $3 AND $4 THEN '' ELSE $2 END
FROM latest l WHERE c.conversation_id = $1 RETURNING c.revert_message_id`, &marker, conversationID, fmt.Sprintf("%d:%d", entry.ID, length), entry.ID, length == len(entry.Entry.ReplayInput)); err != nil {
		return "", "", err
	}

	if err := stageShadowRevertDB(ctx, db, conversationID); err != nil {
		return "", "", err
	}

	return marker, prompt, refreshPathSummary(ctx, db, conversationID)
}

func refreshPathSummary(ctx context.Context, db stateStoreDB, conversationID string) error {
	if _, err := execState(ctx, db, "reset moved summary", `UPDATE conversations SET preview = NULL, last_updated = NULL WHERE conversation_id = $1`, conversationID); err != nil {
		return err
	}

	summary, err := loadSessionSummary(ctx, db, conversationID)
	if err != nil {
		return err
	}

	return saveSessionSummary(ctx, db, summary)
}

func pruneRevertDelegations(ctx context.Context, db stateStoreDB, conversationID string) error {
	children, err := queryStrings(ctx, db, `WITH `+sessionHistorySQL+`, kept_calls_here AS (
    SELECT DISTINCT item->>'call_id' AS call_id FROM effective_entries e
    CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry->'replay_input', 'null'::jsonb)) WITH ORDINALITY AS items(item, ordinal)
    WHERE item->>'type' = 'function_call' AND (e.revert_index < 0 OR ordinal <= e.revert_index)
), running_subagents AS (
    SELECT $1 || subagent_key AS conversation_id FROM turns WHERE conversation_id = $1 AND NOT takes_lane AND phase = 'running' AND subagent_key <> ''
), children AS (
    SELECT c.conversation_id, k.call_id IS NOT NULL OR r.conversation_id IS NOT NULL AS kept FROM conversations c
    LEFT JOIN kept_calls_here k ON k.call_id = c.call_id LEFT JOIN running_subagents r USING (conversation_id) WHERE c.parent_conversation_id = $1
    UNION ALL SELECT c.conversation_id, p.kept OR r.conversation_id IS NOT NULL FROM children p JOIN conversations c ON c.parent_conversation_id = p.conversation_id
    LEFT JOIN running_subagents r ON r.conversation_id = c.conversation_id
)
SELECT conversation_id FROM children WHERE NOT kept`, "discarded revert delegations", conversationID)
	if err != nil {
		return err
	}

	return deleteDelegationsDB(ctx, db, children)
}

func deleteDelegationsDB(ctx context.Context, db stateStoreDB, children []string) error {
	for _, table := range []string{"entries", "turns", "steps", "conversations"} {
		if _, err := db.ExecContext(ctx, `DELETE FROM `+table+` WHERE conversation_id = ANY($1)`, children); err != nil {
			return fmt.Errorf("prune revert delegation: %w", err)
		}
	}

	return nil
}

func revertInputDB(ctx context.Context, db stateStoreDB, conversationID, inputID string) (messageID, turnID string, err error) {
	err = db.QueryRowContext(ctx, `WITH `+turnReplaySQL+`, saved AS (
    SELECT e.id::text || ':' || (items.ordinal - 1)::text AS message_id FROM entries e
    CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry_json::jsonb->'replay_input', 'null'::jsonb)) WITH ORDINALITY AS items(item, ordinal)
    WHERE e.conversation_id = $1 AND items.item->>'input_id' = $2 ORDER BY e.id DESC, items.ordinal DESC LIMIT 1
), recorded AS (
    SELECT turn_id FROM turn_replay WHERE item->>'input_id' = $2 LIMIT 1
)
SELECT COALESCE(saved.message_id, ''), COALESCE(recorded.turn_id, '') FROM saved FULL JOIN recorded ON TRUE`, conversationID, inputID).Scan(&messageID, &turnID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrRevertMessageGone
	}

	if err != nil {
		return "", "", fmt.Errorf("read reverted input: %w", err)
	}

	return messageID, turnID, nil
}

func revertTargetDB(ctx context.Context, db stateStoreDB, conversationID, target string, boundary protocol.RevertBoundary) (turnID string, err error) {
	messageID := target
	if !strings.Contains(target, ":") {
		if messageID, turnID, err = revertInputDB(ctx, db, conversationID, target); err != nil || messageID == "" {
			return turnID, err
		}
	}

	_, _, err = revertBoundary(ctx, db, conversationID, messageID, boundary)

	return turnID, err
}

func saveRevertTurnDB(ctx context.Context, db stateStoreDB, conversationID, inputID, turnID string) (string, error) {
	messageID, _, err := revertInputDB(ctx, db, conversationID, inputID)
	if err != nil || messageID != "" {
		return messageID, err
	}

	appended, err := appendQueriedEntries(ctx, db, conversationID, "reverted turn", `WITH `+sessionHistorySQL+`, turn AS (
    SELECT id, history_anchor_id, created_at_unix_ns, replay, record FROM turns
    WHERE conversation_id = $1 AND id = $2 AND record IS NOT NULL
), later AS (
    SELECT t.id FROM turn t JOIN effective_entries e ON NOT e.synced AND e.id > t.history_anchor_id
    UNION SELECT t.id FROM turn t JOIN turns l ON l.conversation_id = $1 AND l.phase <> 'waiting' AND l.takes_lane AND l.created_at_unix_ns > t.created_at_unix_ns
)
SELECT '{"replay_input":' || COALESCE(t.replay::text, 'null') || ',' || substr((t.record::jsonb - 'response_id')::text, 2)
FROM turn t LEFT JOIN later USING (id) WHERE later.id IS NULL`, conversationID, turnID)
	if err != nil {
		return "", err
	}

	if appended == 0 {
		return "", errors.New("revert message is not in the latest turn")
	}

	messageID, _, err = revertInputDB(ctx, db, conversationID, inputID)

	return messageID, err
}

func (r *Runtime) StageRevert(ctx context.Context, conversationID, target string, boundary protocol.RevertBoundary) (marker, prompt string, err error) {
	if err := rejectShadowMutation(ctx, r.Sessions.db, conversationID); err != nil {
		return "", "", err
	}

	current, eligible, predecessor, err := r.Sessions.RevertState(ctx, conversationID)
	if err != nil {
		return "", "", err
	}

	if !eligible {
		return "", "", errRevertConversation
	}

	if target == "" && predecessor == "" {
		return current, "", nil
	}

	turnID, err := revertTargetDB(ctx, r.Sessions.db, conversationID, cmp.Or(target, predecessor), boundary)
	if err != nil {
		return "", "", err
	}

	b, err := r.holdHistory(ctx, conversationID)
	if err != nil {
		return "", "", err
	}

	defer func() {
		releaseHistory([]*Bridge{b})

		_ = b.pickLaterWork(context.WithoutCancel(ctx), false)
	}()

	return r.moveCurrentPoint(ctx, b, conversationID, target, turnID, boundary)
}

func (r *Runtime) holdHistory(ctx context.Context, conversationID string) (*Bridge, error) {
	b, err := r.threads.recordedBridge(ctx, conversationID)
	if err != nil {
		return nil, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.historyMutation {
		return nil, errHistoryBusy
	}

	b.historyMutation = true

	return b, nil
}

func (r *Runtime) moveCurrentPoint(ctx context.Context, b *Bridge, conversationID, target, turnID string, boundary protocol.RevertBoundary) (marker, prompt string, err error) {
	b.mu.Lock()
	settlement := b.settlement
	b.mu.Unlock()

	if err := r.Sessions.StopGoal(conversationID); err != nil {
		return "", "", err
	}

	workers, err := r.settleShadowRevert(ctx, conversationID)
	if err != nil {
		return "", "", err
	}
	defer releaseHistory(workers)

	b.mu.Lock()
	b.inputOpen = false
	b.mu.Unlock()

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

	if err := lockShadowHistory(ctx, tx, conversationID); err != nil {
		return "", "", err
	}

	if active, err := r.Sessions.HasActiveTurn(ctx, conversationID); err != nil || active {
		return "", "", errors.Join(err, errors.New("revert requires settled delivery"))
	}

	if target != "" && !strings.Contains(target, ":") {
		if target, err = saveRevertTurnDB(ctx, tx, conversationID, target, turnID); err != nil {
			return "", "", err
		}
	}

	marker, prompt, err = stageRevertDB(ctx, tx, conversationID, target, boundary)
	if err != nil {
		return "", "", err
	}

	return marker, prompt, commitState(tx, "staged revert")
}

func (b *Bridge) preserveRevertRequestLocked(ctx context.Context, request *bridgeRequest) (err error) {
	defer func() {
		if err != nil && request.completion != nil {
			request.completion.err = err
			close(request.completion.done)
			request.completion = nil
		}
	}()

	if request.inbound == nil || !request.inbound.Human && !request.scheduledMessageRecurring {
		return nil
	}

	request.queueItemID = cmp.Or(request.queueItemID, request.inbound.Metadata["web_message_id"], rand.Text())

	var recorded bool
	if err := b.config.SessionService.db.QueryRowContext(ctx, `WITH `+turnReplaySQL+`, recorded AS (
    SELECT 1 FROM entries e CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry_json::jsonb->'replay_input', 'null'::jsonb)) item
    WHERE e.conversation_id = $1 AND item->>'input_id' = $2
    UNION ALL SELECT 1 FROM turn_replay WHERE item->>'input_id' = $2
)
SELECT count(*) > 0 FROM recorded`, b.config.ConversationID, request.queueItemID).Scan(&recorded); err != nil {
		return fmt.Errorf("classify waiting reverted input: %w", err)
	}

	if recorded {
		if request.completion != nil {
			close(request.completion.done)
		}

		return nil
	}

	if i := slices.IndexFunc(b.waiting, func(waiting bridgeRequest) bool { return waiting.queueItemID == request.queueItemID }); i >= 0 {
		if b.waiting[i].completion == nil {
			b.waiting[i].completion, request.completion = request.completion, nil
		}

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
	if err := tx.QueryRowContext(ctx, `WITH waiting AS (SELECT position FROM turns WHERE conversation_id = $1 AND input_id = $2 AND phase = 'waiting')
SELECT COALESCE(max(position), -1) FROM waiting`, b.config.ConversationID, request.queueItemID).Scan(&position); err != nil {
		return fmt.Errorf("read waiting input position: %w", err)
	}

	if position < 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE turns SET position = position + 1 WHERE conversation_id = $1 AND phase = 'waiting' AND position >= $2`, b.config.ConversationID, len(b.waiting)); err != nil {
			return fmt.Errorf("make room for waiting input: %w", err)
		}

		item := protocol.ThreadQueueItem{ID: request.queueItemID, ConversationID: b.config.ConversationID, Kind: protocol.InboundKindEnqueue,
			Inbound: request.inbound, StashAt: time.Now().UTC(), Position: len(b.waiting)}
		if err := putThreadQueueItem(ctx, tx, item.ID, &item); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE turns SET position = position + 1 WHERE conversation_id = $1 AND phase = 'waiting' AND input_id <> $2 AND position >= $3 AND position < $4`, b.config.ConversationID, request.queueItemID, len(b.waiting), position); err != nil {
			return fmt.Errorf("reorder waiting input: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `UPDATE turns SET position = $3 WHERE conversation_id = $1 AND input_id = $2 AND phase = 'waiting'`, b.config.ConversationID, request.queueItemID, len(b.waiting)); err != nil {
			return fmt.Errorf("move waiting input: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit waiting input preservation: %w", err)
	}

	b.waiting = append(b.waiting, *request)

	return nil
}

func (r *Runtime) mutateWebQueue(ctx context.Context, conversationID, id string, kind protocol.InboundKind) (bool, error) {
	b, err := r.threads.recordedBridge(ctx, conversationID)
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

	var paused bool
	if err := queryScalar(ctx, tx, "read queue mutation pause", `SELECT COALESCE(bool_or(paused), FALSE) FROM conversations WHERE conversation_id = $1`, &paused, conversationID); err != nil {
		return false, err
	}

	if paused && kind == protocol.InboundKindSteer {
		return false, AgentPausedError{}
	}

	if kind == protocol.InboundKindEnqueue {
		changed, err := execRows(ctx, tx, "pop Web queue item", popQueueItemSQL, conversationID, id, kind, protocol.InboundKindHeld, waitQueued)
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

	item.Inbound.Kind, item.Kind, item.ParkAfter = kind, kind, ""
	if err := putThreadQueueItem(ctx, tx, id, &item); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit promoted Web input: %w", err)
	}
	b.mu.Unlock()
	err = b.enqueue(ctx, &bridgeRequest{inbound: item.Inbound, queueItemID: id}, "promote Web input")
	b.mu.Lock()

	return true, err
}

func (r *Runtime) admitWeb(ctx context.Context, b *Bridge, inbound *protocol.InboundMessage, goal protocol.GoalRequest) (fresh bool, err error) {
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

	if err := lockShadowHistory(ctx, tx, inbound.ConversationID); err != nil {
		return false, err
	}

	accepted, err = reconcileWebDB(ctx, tx, inbound)
	if err != nil || accepted {
		return false, err
	}

	if goal.Objective != "" {
		if err := beginGoalDB(ctx, tx, inbound.ConversationID, goalStateFor(inbound, goal)); err != nil {
			return false, err
		}
	}

	item := protocol.ThreadQueueItem{ID: inbound.Metadata["web_message_id"], ConversationID: inbound.ConversationID, Kind: inbound.Kind, Inbound: inbound, StashAt: time.Now().UTC()}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), -1) + 1 FROM turns WHERE conversation_id = $1 AND phase = 'waiting'`, inbound.ConversationID).Scan(&item.Position); err != nil {
		return false, fmt.Errorf("read Web admission position: %w", err)
	}

	if err := putThreadQueueItem(ctx, tx, item.ID, &item); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit Web admission: %w", err)
	}

	return true, nil
}

func reconcileWebDB(ctx context.Context, db stateStoreDB, inbound *protocol.InboundMessage) (bool, error) {
	id := inbound.Metadata["web_message_id"]

	var raw []byte

	err := queryScalar(ctx, db, "read admitted Web input", `SELECT inbound_json FROM turns
WHERE conversation_id = $1 AND takes_lane AND (input_id = $2 OR inbound_json->'Metadata'->>'web_message_id' = $2) LIMIT 1`, &raw, inbound.ConversationID, id)
	if err == nil && raw != nil {
		var stored protocol.InboundMessage
		if err := json.Unmarshal(raw, &stored); err != nil {
			return false, fmt.Errorf("decode admitted Web input: %w", err)
		}

		attachmentsMatch := slices.Equal(attachmentsFromInbound(stored.Attachments), attachmentsFromInbound(inbound.Attachments))
		if stored.Text != inbound.Text || stored.Source != inbound.Source || stored.Metadata[protocol.InboundPrincipalMetadataKey] != inbound.Metadata[protocol.InboundPrincipalMetadataKey] || stored.Metadata["web_goal"] != inbound.Metadata["web_goal"] || !attachmentsMatch || stored.AttachmentPresence != inbound.AttachmentPresence || !slices.Equal(stored.AttachmentWarnings, inbound.AttachmentWarnings) || stored.GoalAction != inbound.GoalAction {
			return false, errors.New("message ID was already accepted with different content")
		}

		return true, nil
	}

	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}

	found, err := scanFound("read saved Web input", db.QueryRowContext(ctx, `SELECT item::text FROM entries e
    CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry_json::jsonb->'replay_input', 'null'::jsonb)) item
    WHERE e.conversation_id = $1 AND item->>'input_id' = $2 LIMIT 1`, inbound.ConversationID, id).Scan(&raw))
	if err != nil || !found {
		return false, err
	}

	items, err := harness.ReplayInputToParams([]json.RawMessage{raw})
	if err != nil {
		return false, fmt.Errorf("decode saved Web replay: %w", err)
	}

	_, text, _, err := ReplayInputMessageRoleText(&items[0], raw)
	if err != nil {
		return false, err
	}

	var message struct {
		Content json.RawMessage
		Header  string `json:"prompt_header"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return false, fmt.Errorf("decode saved Web message: %w", err)
	}

	var parts []struct {
		Type     string `json:"type"`
		ImageURL string `json:"image_url"`
		FileData string `json:"file_data"`
		FileURL  string `json:"file_url"`
		Filename string `json:"filename"`
	}
	if strings.HasPrefix(string(message.Content), "[") {
		if err := json.Unmarshal(message.Content, &parts); err != nil {
			return false, fmt.Errorf("decode saved Web content: %w", err)
		}
	}

	var attachments []harness.Attachment

	for _, part := range parts {
		if part.Type == "input_image" || part.Type == "input_file" {
			url := cmp.Or(part.ImageURL, part.FileData, part.FileURL)
			mime, _, _ := strings.Cut(strings.TrimPrefix(url, "data:"), ";")

			if part.Type == "input_image" {
				part.Filename = ""
			}

			attachments = append(attachments, harness.Attachment{MIME: mime, Filename: part.Filename, URL: url})
		}
	}

	expectedAttachments := attachmentsFromInbound(inbound.Attachments)
	for i := range expectedAttachments {
		if strings.HasPrefix(expectedAttachments[i].MIME, "image/") {
			expectedAttachments[i].Filename = ""
		}
	}

	header, expected, _ := strings.Cut(buildPrompt(inbound, nil, false), "\n\n")
	expectedIdentity, _, _ := strings.Cut(header, " additional_instructions=")

	identity, _, _ := strings.Cut(message.Header, " additional_instructions=")
	if text != expected || strings.TrimSuffix(identity, "]") != strings.TrimSuffix(expectedIdentity, "]") || !slices.Equal(attachments, expectedAttachments) {
		return false, errors.New("message ID was already accepted with different content")
	}

	return true, nil
}
