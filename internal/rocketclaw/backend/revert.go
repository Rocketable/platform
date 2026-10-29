package backend

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

var errRevertConversation = errors.New("revert requires a Web chat, Slack thread, or shadow")

var (
	// ErrInvalidRevertMessage is a saved message ID undo cannot read.
	ErrInvalidRevertMessage = errors.New("invalid revert message ID")
	// ErrRevertMessageGone is an undo target the conversation does not hold.
	ErrRevertMessageGone = errors.New("revert message is no longer in the session")
)

// popQueueItemSQL moves a held waiting turn ($4) to the tail of the ready queue as delivery $3 and
// reason $5, in its stored request too, so it runs as that kind.
const popQueueItemSQL = `WITH tail AS (
    SELECT COALESCE(MAX(position), -1) + 1 AS position FROM turns WHERE conversation_id = $1 AND phase = 'waiting' AND park_after = ''
)
UPDATE turns t SET reason = $5, delivery = $3, park_after = '', inbound_json = jsonb_set(t.inbound_json::jsonb, '{Kind}', to_jsonb($3::text)), position = tail.position
FROM tail WHERE t.conversation_id = $1 AND t.input_id = $2 AND t.phase = 'waiting' AND t.delivery = $4`

// Physical saved turns resolve checkpoint supersession before the exclusive
// boundary is applied. All history readers share this inventory: a conversation's
// own entries, and those of the hidden runs reporting to it or it continues, with their compaction
// items left out of the replay as the visible view shows them (visibleEntry).
// A hidden run's own view leaves out the copies its chat once held, even once that chat is gone;
// any other view, sandbox entries. A delegation is readable while each undone conversation it nests
// under keeps the call that started the delegation on the way down. It begins the query's
// WITH RECURSIVE list.
const sessionHistorySQL = `RECURSIVE starts AS (
    SELECT $1::text AS id
), ` + ancestorsSQL + `, reverted_parents AS (
    SELECT p.conversation_id, l.call_id, split_part(p.revert_message_id, ':', 1)::bigint AS entry_id, split_part(p.revert_message_id, ':', 2)::integer AS replay_index
    FROM ancestors a JOIN conversations l ON l.conversation_id = a.conversation_id JOIN conversations p ON p.conversation_id = l.parent_conversation_id
    WHERE p.revert_message_id <> ''
), kept_calls AS (
    SELECT DISTINCT p.conversation_id FROM reverted_parents p JOIN entries e ON e.conversation_id = p.conversation_id
    CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry_json::jsonb->'replay_input', 'null'::jsonb)) WITH ORDINALITY AS items(item, ordinal)
    WHERE item->>'type' = 'function_call' AND item->>'call_id' = p.call_id
        AND (e.id < p.entry_id OR e.id = p.entry_id AND ordinal <= p.replay_index)
), visibility AS (
    SELECT count(*) FILTER (WHERE k.conversation_id IS NULL) = 0 AS readable FROM reverted_parents p LEFT JOIN kept_calls k USING (conversation_id)
), viewer AS (
    SELECT v.readable, CASE WHEN c.producer_inbound_json IS NULL THEN 'sandbox' ELSE 'visible' END AS hidden_audience, COALESCE(c.revert_message_id, '') AS marker
    FROM visibility v LEFT JOIN conversations c ON c.conversation_id = $1
), reporting_runs AS (
    SELECT conversation_id FROM conversations WHERE reports_to = $1
    UNION SELECT continues FROM conversations WHERE conversation_id = $1 AND continues IS NOT NULL
), viewed_rows AS (
    SELECT e.id, e.conversation_id, e.entry_json, FALSE AS reported FROM entries e CROSS JOIN viewer v
    WHERE e.conversation_id = $1 AND e.audience <> v.hidden_audience AND v.readable
    UNION ALL
    SELECT e.id, e.conversation_id, e.entry_json, TRUE FROM reporting_runs r JOIN entries e USING (conversation_id) CROSS JOIN viewer v
    WHERE e.audience <> 'sandbox' AND v.readable
), attributed_rows AS (
    SELECT r.id, r.entry_json::text AS entry_json, r.entry_json::jsonb AS entry, r.reported OR r.entry_json::jsonb ? 'sync_source_entry_id' AS synced,
        CASE WHEN r.reported THEN r.conversation_id ELSE COALESCE(r.entry_json->>'sync_source_conversation_id', source.conversation_id, '') END AS source_conversation_id
    FROM viewed_rows r LEFT JOIN entries source ON NOT r.reported AND source.id = (r.entry_json->>'sync_source_entry_id')::bigint
), physical_entries AS (
    SELECT id, entry_json, source_conversation_id, synced, CASE WHEN synced AND jsonb_typeof(entry->'replay_input') = 'array'
        THEN jsonb_set(entry, '{replay_input}', jsonb_path_query_array(entry->'replay_input', '$[*] ? (!(@.type == "compaction" || @.type == "compaction_summary"))'))
        ELSE entry END AS entry
    FROM attributed_rows
), cutoff AS (
    SELECT marker FROM viewer
), effective_entries AS (
    SELECT e.*, CASE WHEN e.id = NULLIF(split_part(c.marker, ':', 1), '')::bigint
        THEN split_part(c.marker, ':', 2)::integer ELSE -1 END AS revert_index, c.marker
    FROM physical_entries e CROSS JOIN cutoff c
    WHERE c.marker = '' OR e.id < split_part(c.marker, ':', 1)::bigint
        OR e.id = split_part(c.marker, ':', 1)::bigint AND split_part(c.marker, ':', 2)::integer > 0
)`

// clipRevertEntry retains a partial turn without its abandoned output or usage.
// OpenCode V2: packages/core/src/session/projector.ts:717-773.
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

// revertBoundary resolves an opaque saved message ID against physical replay.
// Caller holds the history lock for any mutation.
func revertBoundary(ctx context.Context, db stateStoreDB, conversationID, messageID string) (ObservedSessionEntry, int, error) {
	idText, indexText, ok := strings.Cut(messageID, ":")
	id, errID := strconv.ParseInt(idText, 10, 64)

	index, errIndex := strconv.Atoi(indexText)
	if !ok || errID != nil || errIndex != nil || id <= 0 || index < 0 || strconv.FormatInt(id, 10) != idText || strconv.Itoa(index) != indexText {
		return ObservedSessionEntry{}, 0, ErrInvalidRevertMessage
	}

	entry := ObservedSessionEntry{ID: id}

	var raw string

	err := db.QueryRowContext(ctx, `WITH `+sessionHistorySQL+`
SELECT entry_json, synced FROM physical_entries WHERE id = $2`, conversationID, id).Scan(&raw, &entry.Synced)
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

	items, err := harness.ReplayInputToParams(entry.Entry.ReplayInput[index : index+1])
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
	marker, eligible, predecessor, _, err = revertStateDB(ctx, s.db, conversationID)
	if err != nil {
		return "", false, "", fmt.Errorf("observe session revert: %w", err)
	}

	return marker, eligible, predecessor, nil
}

// revertStateDB also returns floor, the message ID of the newest message undo must keep, or "".
// A Web chat, Slack thread, or shadow can undo its people's Web messages, never back past a
// message of a cron run or External MCP call it shows, or one sent from Slack or by a cron or
// External MCP prompt; a Slack thread's messages from before prompts were labeled count as sent
// from Slack.
func revertStateDB(ctx context.Context, db stateStoreDB, conversationID string) (marker string, eligible bool, predecessor, floor string, err error) {
	err = db.QueryRowContext(ctx, `WITH `+sessionHistorySQL+`, chat AS (
    SELECT kind FROM conversations WHERE conversation_id = $1
), floor AS (
    SELECT e.id, items.ordinal - 1 AS idx FROM physical_entries e LEFT JOIN chat m ON TRUE
    CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry->'replay_input', 'null'::jsonb)) WITH ORDINALITY AS items(item, ordinal)
    WHERE e.synced OR item->>'role' = 'user' AND (item->>'prompt_header' !~ '^\[(Web|System)[ \]]' OR item->>'prompt_header' IS NULL AND m.kind = 'slack_thread')
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

// afterRevertFloor reports whether message ID id comes after floor (see revertStateDB).
func afterRevertFloor(id, floor string) bool {
	var entry, index, floorEntry, floorIndex int64

	_, _ = fmt.Sscanf(id, "%d:%d", &entry, &index)
	_, _ = fmt.Sscanf(floor, "%d:%d", &floorEntry, &floorIndex)

	return entry > floorEntry || entry == floorEntry && index > floorIndex
}

// stageRevertDB changes metadata only. Runtime owns interruption and settlement.
func stageRevertDB(ctx context.Context, db stateStoreDB, conversationID, before string) (cutoff, prompt string, err error) {
	marker, eligible, predecessor, floor, err := revertStateDB(ctx, db, conversationID)
	if err != nil {
		return "", "", err
	}

	if !eligible {
		return "", "", errRevertConversation
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

	if !afterRevertFloor(before, floor) {
		return "", "", errors.New("revert cannot go back past a message from a cron run, External MCP, or Slack")
	}

	if marker != "" {
		idText, indexText, _ := strings.Cut(marker, ":")
		id, _ := strconv.ParseInt(idText, 10, 64)

		boundary, _ := strconv.Atoi(indexText)
		if entry.ID > id || entry.ID == id && index >= boundary {
			return "", "", errors.New("revert message is not visible")
		}
	}

	// revertBoundary already decoded and projected this same, unchanged user item.
	items, _ := harness.ReplayInputToParams(entry.Entry.ReplayInput[index : index+1])
	_, text, _, err := ReplayInputMessageRoleText(&items[0], entry.Entry.ReplayInput[index])
	// Goal replay includes generated steering instructions. Its existing durable
	// owner keeps the editable command and the exact admission identity.
	var goalPrompt string
	if err := db.QueryRowContext(ctx, `WITH goal AS (SELECT inbound_json->'Metadata'->>'web_goal_prompt' AS prompt FROM turns
    WHERE conversation_id = $1 AND phase <> 'waiting' AND takes_lane AND inbound_json->'Metadata'->>'web_message_id' = $2 LIMIT 1)
SELECT COALESCE(max(prompt), '') FROM goal`, conversationID, items[0].OfMessage.ExtraFields()["input_id"]).Scan(&goalPrompt); err != nil {
		return "", "", fmt.Errorf("read reverted goal prompt: %w", err)
	}

	if goalPrompt != "" {
		text = goalPrompt
	}

	if _, err := db.ExecContext(ctx, `UPDATE conversations SET revert_message_id = $2 WHERE conversation_id = $1`, conversationID, before); err != nil {
		return "", "", fmt.Errorf("stage revert: %w", err)
	}

	if err := stageShadowRevertDB(ctx, db, conversationID, &entry); err != nil {
		return "", "", err
	}

	if err := refreshPrincipals(ctx, db, []string{conversationID}); err != nil {
		return "", "", err
	}

	return before, text, err
}

// commitRevertDB is called only inside prepared durable admission's transaction.
func commitRevertDB(ctx context.Context, db stateStoreDB, conversationID, marker string) error {
	if err := lockShadowHistory(ctx, db, conversationID); err != nil {
		return err
	}

	entry, index, err := revertBoundary(ctx, db, conversationID, marker)
	if err != nil {
		return err
	}

	if err := commitShadowRevertDB(ctx, db, conversationID, &entry); err != nil {
		return err
	}

	discarded, err := queryStrings(ctx, db, `WITH cut_turns AS (
    SELECT DISTINCT entry_json->>'turn_id' AS id FROM entries WHERE conversation_id = $1 AND id >= $2
)
SELECT a.id FROM turns a LEFT JOIN cut_turns c USING (id)
WHERE a.conversation_id = $1 AND (a.history_anchor_id >= $2 OR a.id = $3 OR c.id IS NOT NULL)`, "discarded revert turns", conversationID, entry.ID, entry.Entry.TurnID)
	if err != nil {
		return err
	}

	if err := pruneRevertDelegations(ctx, db, conversationID); err != nil {
		return err
	}

	if _, err := db.ExecContext(ctx, `WITH owned AS (
    SELECT input_id AS job_id FROM turns WHERE conversation_id = $1 AND NOT takes_lane AND phase = 'running'
), discarded AS (
    SELECT DISTINCT s.key FROM steps s JOIN unnest($2::text[]) d(id) ON s.key = d.id OR starts_with(s.key, d.id || '/')
    LEFT JOIN owned j ON starts_with(s.key, j.job_id || '/') WHERE s.conversation_id = $1 AND j.job_id IS NULL
)
DELETE FROM steps s USING discarded d WHERE s.conversation_id = $1 AND s.key = d.key`, conversationID, discarded); err != nil {
		return fmt.Errorf("prune revert journals: %w", err)
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM turns WHERE conversation_id = $1 AND id = ANY($2)`, conversationID, discarded); err != nil {
		return fmt.Errorf("prune revert turns: %w", err)
	}

	if err := recordRunning(ctx, db, []string{conversationID}); err != nil {
		return err
	}

	// The cut never reaches a hidden run's entries: they are at or before the revert floor.
	if _, err := db.ExecContext(ctx, `DELETE FROM entries WHERE conversation_id = $1 AND (id > $2 OR id = $2 AND $3 = 0)`, conversationID, entry.ID, index); err != nil {
		return fmt.Errorf("prune revert suffix: %w", err)
	}

	if index > 0 {
		clipRevertEntry(&entry.Entry, index)

		data, err := json.Marshal(entry.Entry)
		if err != nil {
			return fmt.Errorf("encode revert prefix: %w", err)
		}

		// The clipped entry keeps the search text of the messages before the cut.
		if _, err := db.ExecContext(ctx, `WITH kept AS (
    SELECT string_agg(l.line, E'\n' ORDER BY l.n) AS search_text FROM entries e
    CROSS JOIN LATERAL string_to_table(e.search_text, E'\n') WITH ORDINALITY AS l(line, n) WHERE e.id = $1 AND split_part(l.line, ' ', 1)::integer < $3
)
UPDATE entries e SET entry_json = $2, search_text = k.search_text FROM kept k WHERE e.id = $1`, entry.ID, string(data), index); err != nil {
			return fmt.Errorf("save revert prefix: %w", err)
		}
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM turns WHERE conversation_id = $1 AND phase = 'waiting' AND source = $2`, conversationID, protocol.SourceWeb); err != nil {
		return fmt.Errorf("prune reverted human queue: %w", err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE conversations SET revert_message_id = '' WHERE conversation_id = $1`, conversationID); err != nil {
		return fmt.Errorf("commit revert marker: %w", err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE conversations SET preview = NULL, last_updated = NULL WHERE conversation_id = $1`, conversationID); err != nil {
		return fmt.Errorf("reset reverted summary: %w", err)
	}

	summary, err := loadSessionSummary(ctx, db, conversationID)
	if err != nil {
		return err
	}

	return saveSessionSummary(ctx, db, summary)
}

// Delegations are histories, never handles for undoing tool effects. Running parent
// Background Jobs keep theirs; shadow lineages cannot start such jobs. A discarded
// subagent session goes with its history.
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

	for _, table := range []string{"entries", "turns", "steps", "conversations"} {
		if _, err := db.ExecContext(ctx, `DELETE FROM `+table+` WHERE conversation_id = ANY($1)`, children); err != nil {
			return fmt.Errorf("prune revert delegation: %w", err)
		}
	}

	return nil
}

// revertInputDB finds the user message a Web input ID names: its saved message
// ID, or else the turn whose unsaved record holds it.
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

// revertTargetDB resolves a saved message ID, or a Web input ID whose turn may
// not have saved it yet. That boundary has only the turn ID selecting its comparisons.
func revertTargetDB(ctx context.Context, db stateStoreDB, conversationID, target string) (ObservedSessionEntry, error) {
	messageID, turnID := target, ""
	if !strings.Contains(target, ":") {
		var err error
		if messageID, turnID, err = revertInputDB(ctx, db, conversationID, target); err != nil {
			return ObservedSessionEntry{}, err
		}
	}

	if messageID == "" {
		return ObservedSessionEntry{ID: math.MaxInt64, Entry: harness.SessionEntry{TurnID: turnID}}, nil
	}

	boundary, _, err := revertBoundary(ctx, db, conversationID, messageID)

	return boundary, err
}

// saveRevertTurnDB returns the saved message ID of a settled turn's input. When
// that turn saved nothing, it saves the latest turn's record as preserveStoppedInput
// does, so Redo restores it like OpenCode keeps an interrupted turn's messages.
// Records hold only finished items (rocketcode saveTurn).
func saveRevertTurnDB(ctx context.Context, db stateStoreDB, conversationID, inputID, turnID string) (string, error) {
	messageID, _, err := revertInputDB(ctx, db, conversationID, inputID)
	if err != nil || messageID != "" {
		return messageID, err
	}

	appended, err := appendQueriedEntries(ctx, db, conversationID, "reverted turn", `WITH turn AS (
    SELECT id, history_anchor_id, created_at_unix_ns, replay, record FROM turns
    WHERE conversation_id = $1 AND id = $2 AND record IS NOT NULL
), later AS (
    SELECT t.id FROM turn t JOIN entries e ON e.conversation_id = $1 AND e.id > t.history_anchor_id
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

// StageRevert settles the recorded bridge before changing its cutoff. Like
// packages/app/src/session/revert.ts, interruption is not effect rollback.
// before is a saved message ID, or the Web input ID of a message whose turn has
// not saved it, as when that turn is running.
func (r *Runtime) StageRevert(ctx context.Context, conversationID, before string) (marker, prompt string, err error) {
	if err := rejectShadowMutation(ctx, r.Sessions.db, conversationID); err != nil {
		return "", "", err
	}

	_, eligible, predecessor, err := r.Sessions.RevertState(ctx, conversationID)
	if err != nil {
		return "", "", err
	}

	if !eligible {
		return "", "", errRevertConversation
	}

	if before == "" && predecessor == "" {
		marker, _, _, err := r.Sessions.RevertState(ctx, conversationID)
		return marker, "", err
	}

	target := cmp.Or(before, predecessor)

	boundary, err := revertTargetDB(ctx, r.Sessions.db, conversationID, target)
	if err != nil {
		return "", "", err
	}

	b, err := r.threads.recordedBridge(ctx, conversationID)
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
		releaseHistory([]*Bridge{b})

		if err != nil {
			_ = b.pickLaterWork(ctx, false)
		}
	}()

	if err := r.Sessions.StopGoal(conversationID); err != nil {
		return "", "", err
	}

	workers, err := r.settleShadowRevert(ctx, conversationID, &boundary)
	if err != nil {
		return "", "", err
	}
	defer releaseHistory(workers)

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

	if !strings.Contains(target, ":") {
		if before, err = saveRevertTurnDB(ctx, tx, conversationID, target, boundary.Entry.TurnID); err != nil {
			return "", "", err
		}
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
	if err := rejectShadowMutation(ctx, r.Sessions.db, conversationID); err != nil {
		return err
	}

	b, err := r.threads.recordedBridge(ctx, conversationID)
	if err != nil {
		return err
	}

	b.mu.Lock()
	if b.historyMutation {
		b.mu.Unlock()
		return errors.New("conversation history is busy")
	}

	b.historyMutation = true
	b.mu.Unlock()

	tx, err := r.Sessions.db.BeginTx(ctx, nil)
	if err == nil {
		defer func() { _ = tx.Rollback() }()

		err = lockShadowHistory(ctx, tx, conversationID)
	}

	if err == nil {
		_, eligible, _, _, errState := revertStateDB(ctx, tx, conversationID)

		err = errState
		if err == nil && !eligible {
			err = errRevertConversation
		}
	}

	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE conversations SET revert_message_id = '' WHERE conversation_id = $1 OR shadow_parent = $1`, conversationID)
	}

	if err == nil {
		err = refreshPrincipals(ctx, tx, []string{conversationID})
	}

	if err == nil {
		err = tx.Commit()
	}

	releaseHistory([]*Bridge{b})

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
	if err := tx.QueryRowContext(ctx, `WITH waiting AS (SELECT position FROM turns WHERE conversation_id = $1 AND input_id = $2 AND phase = 'waiting')
SELECT COALESCE(max(position), -1) FROM waiting`, b.config.ConversationID, request.queueItemID).Scan(&position); err != nil {
		return fmt.Errorf("read waiting input position: %w", err)
	}

	if position < 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE turns SET position = position + 1 WHERE conversation_id = $1 AND phase = 'waiting' AND position >= $2`, b.config.ConversationID, len(b.waiting)); err != nil {
			return fmt.Errorf("make room for waiting input: %w", err)
		}

		item := protocol.ThreadQueueItem{ID: request.queueItemID, ConversationID: b.config.ConversationID, Message: request.inbound.Text,
			Source: request.inbound.Source, Principal: request.inbound.Metadata[protocol.InboundPrincipalMetadataKey], Kind: protocol.InboundKindEnqueue,
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

// mutateWebQueue serializes explicit release with staging and branch admission.
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

	var marker string
	if err := queryScalar(ctx, tx, "read queue mutation cutoff", `SELECT COALESCE(max(revert_message_id), '') FROM conversations WHERE conversation_id = $1`, &marker, conversationID); err != nil {
		return false, err
	}

	if marker != "" {
		return false, errors.New("conversation history is reverted")
	}

	if kind == protocol.InboundKindEnqueue {
		changed, err := execRows(ctx, tx, "pop Web queue item", "count popped queue items", popQueueItemSQL, conversationID, id, kind, protocol.InboundKindHeld, waitQueued)
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

	if err := lockShadowHistory(ctx, tx, inbound.ConversationID); err != nil {
		return false, err
	}

	accepted, err = reconcileWebDB(ctx, tx, inbound)
	if err != nil || accepted {
		return false, err
	}

	var marker string
	if err := queryScalar(ctx, tx, "read admission cutoff", `SELECT COALESCE(max(revert_message_id), '') FROM conversations WHERE conversation_id = $1`, &marker, inbound.ConversationID); err != nil {
		return false, err
	}

	if marker != "" {
		_, _, _, floor, err := revertStateDB(ctx, tx, inbound.ConversationID)
		if err != nil {
			return false, err
		}

		// A message from a cron run, External MCP, or Slack that arrived after the staged undo cancels it.
		if !afterRevertFloor(marker, floor) {
			if _, err := tx.ExecContext(ctx, `UPDATE conversations SET revert_message_id = '' WHERE conversation_id = $1 OR shadow_parent = $1`, inbound.ConversationID); err != nil {
				return false, fmt.Errorf("cancel staged revert: %w", err)
			}

			if err := refreshPrincipals(ctx, tx, []string{inbound.ConversationID}); err != nil {
				return false, err
			}

			marker = ""
		} else if err := commitRevertDB(ctx, tx, inbound.ConversationID, marker); err != nil {
			return false, err
		}
	}

	if goal.Objective != "" {
		if err := beginGoalDB(ctx, tx, inbound.ConversationID, &GoalState{Objective: goal.Objective, MaxTurns: goal.MaxTurns, PrincipalName: inbound.Metadata[protocol.InboundPrincipalNameMetadataKey], PrincipalEmail: inbound.Metadata[protocol.InboundPrincipalEmailMetadataKey]}); err != nil {
			return false, err
		}
	}

	item := protocol.ThreadQueueItem{ID: inbound.Metadata["web_message_id"], ConversationID: inbound.ConversationID, Message: inbound.Text,
		Principal: inbound.Metadata[protocol.InboundPrincipalMetadataKey], Kind: inbound.Kind, Source: inbound.Source, Inbound: inbound, StashAt: time.Now().UTC(),
		Content: protocol.InboundContent{Attachments: inbound.Attachments, AttachmentPresence: inbound.AttachmentPresence, AttachmentWarnings: inbound.AttachmentWarnings}}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), -1) + 1 FROM turns WHERE conversation_id = $1 AND phase = 'waiting'`, inbound.ConversationID).Scan(&item.Position); err != nil {
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

	err := queryScalar(ctx, db, "read admitted Web input", `SELECT inbound_json FROM turns
WHERE conversation_id = $1 AND takes_lane AND (input_id = $2 OR inbound_json->'Metadata'->>'web_message_id' = $2) LIMIT 1`, &raw, inbound.ConversationID, id)
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
		return false, err
	}

	err = db.QueryRowContext(ctx, `SELECT item::text FROM entries e
    CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry_json::jsonb->'replay_input', 'null'::jsonb)) item
    WHERE e.conversation_id = $1 AND item->>'input_id' = $2 LIMIT 1`, inbound.ConversationID, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("read saved Web input: %w", err)
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
			attachments = append(attachments, harness.Attachment{MIME: mime, Filename: part.Filename, URL: url})
		}
	}

	expectedAttachments := attachmentsFromInbound(inbound.Attachments)
	for i := range expectedAttachments {
		if strings.HasPrefix(expectedAttachments[i].MIME, "image/") {
			expectedAttachments[i].Filename = "" // Image replay carries bytes, not a filename.
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
