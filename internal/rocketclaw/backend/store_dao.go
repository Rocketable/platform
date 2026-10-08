package backend

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	harness "github.com/Rocketable/platform/internal/rocketcode"
)

type stateDAO struct {
	db stateStoreDB
}

// createConversation preserves selection and runs under the caller's history lock.
func (d stateDAO) createConversation(ctx context.Context, conversation protocol.Conversation) error {
	summary, err := loadSessionSummary(ctx, d.db, conversation.ID)
	if err != nil {
		return err
	}

	if err := markNewChatIndexed(ctx, d.db, conversation.ID); err != nil {
		return err
	}

	if _, err := d.db.ExecContext(ctx, `INSERT INTO managed_conversations (conversation_id, agent, created_by) VALUES ($1, $2, $3) ON CONFLICT (conversation_id) DO NOTHING`, conversation.ID, conversation.Agent, conversation.CreatedBy); err != nil {
		return fmt.Errorf("create conversation: %w", err)
	}

	return saveSessionSummary(ctx, d.db, summary)
}

func (d stateDAO) observedEntries(ctx context.Context, conversationID string) ([]ObservedSessionEntry, error) {
	return queryRows(ctx, d.db, `WITH `+sessionHistorySQL+`
SELECT id, entry_json, source_conversation_id, synced, revert_index FROM effective_entries
ORDER BY id`, "rocketcode session entries", scanObservedEntry, conversationID)
}

// scanObservedEntry reads id, entry_json, source_conversation_id, synced, and
// revert_index from effective_entries.
func scanObservedEntry(row rowScanner) (ObservedSessionEntry, error) {
	var (
		entry ObservedSessionEntry
		raw   string
		index int
	)
	if err := row.Scan(&entry.ID, &raw, &entry.SourceConversationID, &entry.Synced, &index); err != nil {
		return ObservedSessionEntry{}, fmt.Errorf("scan rocketcode session entry: %w", err)
	}

	if err := json.Unmarshal([]byte(raw), &entry.Entry); err != nil {
		return ObservedSessionEntry{}, fmt.Errorf("parse rocketcode session entry: %w", err)
	}

	clipRevertEntry(&entry.Entry, index)

	return entry, nil
}

// producer reads routing and effect progress under the caller's source history lock.
// A nil inbound means this conversation has no retained producer routing.
func (d stateDAO) producer(ctx context.Context, conversationID string) (*protocol.InboundMessage, int64, error) {
	var (
		data    []byte
		through int64
	)
	if err := d.db.QueryRowContext(ctx, `SELECT producer_inbound_json, producer_effects_through_id FROM managed_conversations WHERE conversation_id = $1`, conversationID).Scan(&data, &through); err != nil {
		return nil, 0, fmt.Errorf("load producer routing: %w", err)
	}

	var inbound *protocol.InboundMessage

	if data != nil {
		if err := json.Unmarshal(data, &inbound); err != nil {
			return nil, 0, fmt.Errorf("decode producer routing: %w", err)
		}
	}

	return inbound, through, nil
}

// bindProducer is called only after authorized destination resolution. The atomic
// update returns the established owner even when another binder already won.
func (d stateDAO) bindProducer(ctx context.Context, conversationID, destination string) (string, error) {
	var owner string
	if err := d.db.QueryRowContext(ctx, `UPDATE managed_conversations SET producer_inbound_json =
jsonb_set(producer_inbound_json::jsonb, '{SyncDestination}', to_jsonb(COALESCE(NULLIF(producer_inbound_json->>'SyncDestination', ''), $2)))::json
WHERE conversation_id = $1 RETURNING producer_inbound_json->>'SyncDestination'`, conversationID, destination).Scan(&owner); err != nil {
		return "", fmt.Errorf("bind producer destination: %w", err)
	}

	return owner, nil
}

// producerEffects loads only original effects in history order, after progress
// has been read under the same transaction's source history lock.
func (d stateDAO) producerEffects(ctx context.Context, conversationID string, through int64) ([]ObservedSessionEntry, error) {
	return queryRows(ctx, d.db, `SELECT id, entry_json FROM session_entries
WHERE conversation_id = $1 AND id > $2 AND entry_json->>'type' IN ($3, $4)
    AND NOT entry_json::jsonb ? 'sync_source_entry_id' ORDER BY id`, "pending producer effects", func(row rowScanner) (ObservedSessionEntry, error) {
		var (
			entry ObservedSessionEntry
			data  []byte
		)
		if err := row.Scan(&entry.ID, &data); err != nil {
			return entry, fmt.Errorf("scan producer effect: %w", err)
		}

		if err := json.Unmarshal(data, &entry.Entry); err != nil {
			return entry, fmt.Errorf("decode producer effect: %w", err)
		}

		return entry, nil
	}, conversationID, through, producerScheduleEntryType, producerResetEntryType)
}

// advanceProducerEffects commits progress in the caller's effect transaction.
func (d stateDAO) advanceProducerEffects(ctx context.Context, conversationID string, through int64) error {
	if _, err := d.db.ExecContext(ctx, `UPDATE managed_conversations SET producer_effects_through_id = $2 WHERE conversation_id = $1`, conversationID, through); err != nil {
		return fmt.Errorf("advance producer effects: %w", err)
	}

	return nil
}

// projectProducerEffects applies original effects only to their retained owner,
// under the caller's source and destination history locks. Progress commits with
// the effects; the returned surviving schedules are armed only after commit.
func (d stateDAO) projectProducerEffects(ctx context.Context, source, destination, agent string) (map[string]protocol.ScheduledMessageState, error) {
	inbound, through, err := d.producer(ctx, source)
	if err != nil {
		return nil, err
	}

	if inbound == nil || inbound.SyncDestination != destination {
		return nil, nil
	}

	var active bool
	if err := d.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM active_turns WHERE conversation_id = $1 AND phase <> $2)`, source, turnDone).Scan(&active); err != nil {
		return nil, fmt.Errorf("check producer projection completion: %w", err)
	}

	if active {
		return nil, nil
	}

	effects, err := d.producerEffects(ctx, source, through)
	if err != nil {
		return nil, err
	}

	// Legacy executable rows precede resumed effects. Rehome them before applying
	// schedule/reset entries, preserving their IDs, due times, and cadence.
	schedules, err := queryMap(ctx, d.db, `UPDATE scheduled_messages SET conversation_id = $2, agent = $3
WHERE conversation_id = $1
RETURNING scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring, interval_ns`, "rehome producer schedules", scanScheduledMessage, source, destination, agent)
	if err != nil {
		return nil, err
	}

	for i := range effects {
		observed := &effects[i]

		switch observed.Entry.Type {
		case producerScheduleEntryType:
			var scheduled protocol.ScheduledMessageState
			if err := json.Unmarshal(observed.Entry.OutputTrace[0], &scheduled); err != nil {
				return nil, fmt.Errorf("decode synced schedule: %w", err)
			}

			scheduled.ConversationID, scheduled.Agent = destination, agent

			id := rand.Text()
			if err := d.putScheduledMessage(ctx, id, &scheduled); err != nil {
				return nil, err
			}

			schedules[id] = scheduled
		case producerResetEntryType:
			if err := d.resetScheduledMessages(ctx, destination); err != nil {
				return nil, err
			}

			clear(schedules)
		}
	}

	if len(effects) > 0 {
		if err := d.advanceProducerEffects(ctx, source, effects[len(effects)-1].ID); err != nil {
			return nil, err
		}
	}

	return schedules, nil
}

// Thread returns the persisted managed conversation state.
func (s *SessionService) Thread(conversationID string) (ThreadState, bool, error) {
	var (
		thread    ThreadState
		createdBy string
	)

	err := s.db.QueryRowContext(context.Background(), `SELECT agent, created_by FROM managed_conversations WHERE conversation_id = $1`, strings.TrimSpace(conversationID)).Scan(&thread.Agent, &createdBy)
	if err == sql.ErrNoRows {
		return ThreadState{}, false, nil
	}

	if err != nil {
		return ThreadState{}, false, fmt.Errorf("read managed conversation: %w", err)
	}

	thread.CreatedBy = ThreadCreator(createdBy)

	return thread, true, nil
}

// SetThreadAgentIfExists updates a managed conversation agent without creating a thread.
func (s *SessionService) SetThreadAgentIfExists(conversationID, agent string) (bool, error) {
	rows, err := execRows(context.Background(), s.db, "update managed conversation agent", "count managed conversation agent update", `UPDATE managed_conversations SET agent = $1 WHERE conversation_id = $2`, strings.TrimSpace(agent), strings.TrimSpace(conversationID))
	return rows > 0, err
}

func (d stateDAO) externalMCPSession(ctx context.Context, externalConversationID string) (ExternalMCPSessionState, bool, error) {
	_, session, err := scanExternalMCPSession(d.db.QueryRowContext(ctx, `SELECT external_conversation_id, agent, private_conversation_id, managed_conversation_id, slack_channel, origin_pairs FROM external_mcp_sessions WHERE external_conversation_id = $1`, strings.TrimSpace(externalConversationID)))
	if errors.Is(err, sql.ErrNoRows) {
		return ExternalMCPSessionState{}, false, nil
	}

	if err != nil {
		return ExternalMCPSessionState{}, false, fmt.Errorf("read external MCP session: %w", err)
	}

	return session, true, nil
}

// ExternalMCPSessionByConversationID returns the public ID and binding for either session ID.
func (s *SessionService) ExternalMCPSessionByConversationID(conversationID string) (externalConversationID string, session ExternalMCPSessionState, ok bool, err error) {
	externalConversationID, session, err = scanExternalMCPSession(s.db.QueryRowContext(context.Background(), `SELECT external_conversation_id, agent, private_conversation_id, managed_conversation_id, slack_channel, origin_pairs FROM external_mcp_sessions WHERE private_conversation_id = $1 OR managed_conversation_id = $2`, strings.TrimSpace(conversationID), strings.TrimSpace(conversationID)))
	if errors.Is(err, sql.ErrNoRows) {
		return "", ExternalMCPSessionState{}, false, nil
	}

	if err != nil {
		return "", ExternalMCPSessionState{}, false, fmt.Errorf("read external MCP session by conversation ID: %w", err)
	}

	return externalConversationID, session, true, nil
}

func scanExternalMCPSession(scanner rowScanner) (string, ExternalMCPSessionState, error) {
	var (
		externalConversationID string
		session                ExternalMCPSessionState
		privateConversationID  sql.NullString
		originPairs            []byte
	)
	if err := scanner.Scan(&externalConversationID, &session.Agent, &privateConversationID, &session.ManagedConversationID, &session.SlackChannel, &originPairs); err != nil {
		return "", ExternalMCPSessionState{}, fmt.Errorf("scan external MCP session: %w", err)
	}

	if err := json.Unmarshal(originPairs, &session.OriginPairs); err != nil {
		return "", ExternalMCPSessionState{}, fmt.Errorf("decode external MCP origin pairs: %w", err)
	}

	session.PrivateConversationID = privateConversationID.String

	return externalConversationID, session, nil
}

// originPairsJSON stores caller details without runtime-injected keys.
func originPairsJSON(metadata map[string]string) []byte {
	pairs := maps.Clone(metadata)
	maps.DeleteFunc(pairs, func(key, _ string) bool {
		return key == "external_conversation_id" || strings.HasPrefix(key, "rocketclaw_")
	})
	raw, _ := json.Marshal(pairs) // Encoding a string map cannot fail.

	return raw
}

func (d stateDAO) goal(ctx context.Context, conversationID string) (GoalState, bool, error) {
	_, goal, err := scanGoal(d.db.QueryRowContext(ctx, `SELECT conversation_id, objective, check_script, max_turns, turns_used, status, note, slack_recipient_team_id, slack_recipient_user_id, created_at_unix_ns, updated_at_unix_ns FROM conversation_goals WHERE conversation_id = $1`, strings.TrimSpace(conversationID)))
	if errors.Is(err, sql.ErrNoRows) {
		return GoalState{}, false, nil
	}

	if err != nil {
		return GoalState{}, false, fmt.Errorf("read goal: %w", err)
	}

	return goal, true, nil
}

// ActiveGoals returns persisted active goals keyed by conversation ID.
func (s *SessionService) ActiveGoals() (map[string]GoalState, error) {
	goals, err := queryMap(context.Background(), s.db, `SELECT conversation_id, objective, check_script, max_turns, turns_used, status, note, slack_recipient_team_id, slack_recipient_user_id, created_at_unix_ns, updated_at_unix_ns FROM conversation_goals WHERE status = '' OR status = $1 ORDER BY conversation_id`, "active goals", scanGoal, GoalStatusActive)
	if err != nil || len(goals) == 0 {
		return nil, err
	}

	return goals, nil
}

func scanGoal(scanner rowScanner) (string, GoalState, error) {
	var (
		conversationID       string
		goal                 GoalState
		createdAt, updatedAt int64
	)
	if err := scanner.Scan(&conversationID, &goal.Objective, &goal.CheckScript, &goal.MaxTurns, &goal.TurnsUsed, &goal.Status, &goal.Note, &goal.SlackRecipientTeamID, &goal.SlackRecipientUserID, &createdAt, &updatedAt); err != nil {
		return "", GoalState{}, fmt.Errorf("scan goal: %w", err)
	}

	goal.CreatedAt = timeFromUnixNano(createdAt)

	goal.UpdatedAt = timeFromUnixNano(updatedAt)
	if strings.TrimSpace(goal.Status) == "" {
		goal.Status = GoalStatusActive
	}

	return conversationID, goal, nil
}

func (d stateDAO) putScheduledMessage(ctx context.Context, id string, message *protocol.ScheduledMessageState) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO scheduled_messages (scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring, interval_ns) VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT(scheduled_message_id) DO UPDATE SET conversation_id = excluded.conversation_id, agent = excluded.agent, message = excluded.message, due_at_unix_ns = excluded.due_at_unix_ns, recurring = excluded.recurring, interval_ns = excluded.interval_ns`, strings.TrimSpace(id), strings.TrimSpace(message.ConversationID), strings.TrimSpace(message.Agent), message.Message, timeUnixNano(message.DueAt), boolInt(message.Recurring), int64(message.Interval))
	if err != nil {
		return fmt.Errorf("put scheduled message: %w", err)
	}

	return nil
}

func (d stateDAO) scheduledMessages(ctx context.Context, conversationID string) (map[string]protocol.ScheduledMessageState, error) {
	query := `SELECT scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring, interval_ns FROM scheduled_messages ORDER BY scheduled_message_id`
	args := []any{}

	if strings.TrimSpace(conversationID) != "" {
		query = `SELECT scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring, interval_ns FROM scheduled_messages WHERE conversation_id = $1 ORDER BY scheduled_message_id`

		args = append(args, strings.TrimSpace(conversationID))
	}

	messages, err := queryMap(ctx, d.db, query, "scheduled messages", scanScheduledMessage, args...)
	if err != nil || len(messages) == 0 {
		return nil, err
	}

	return messages, nil
}

func (d stateDAO) clearParkAfter(ctx context.Context, scheduledID string) error {
	if _, err := d.db.ExecContext(ctx, `UPDATE thread_queue SET park_after = '' WHERE park_after = $1`, strings.TrimSpace(scheduledID)); err != nil {
		return fmt.Errorf("clear thread queue park: %w", err)
	}

	return nil
}

func (d stateDAO) deleteScheduledMessage(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if err := d.clearParkAfter(ctx, id); err != nil {
		return err
	}

	if _, err := d.db.ExecContext(ctx, `DELETE FROM scheduled_messages WHERE scheduled_message_id = $1`, id); err != nil {
		return fmt.Errorf("delete scheduled message: %w", err)
	}

	return nil
}

func (d stateDAO) resetScheduledMessages(ctx context.Context, conversationID string) error {
	conversationID = strings.TrimSpace(conversationID)
	if _, err := d.db.ExecContext(ctx, `UPDATE thread_queue SET park_after = '' WHERE conversation_id = $1`, conversationID); err != nil {
		return fmt.Errorf("clear thread queue parks: %w", err)
	}

	if _, err := d.db.ExecContext(ctx, `DELETE FROM scheduled_messages WHERE conversation_id = $1`, conversationID); err != nil {
		return fmt.Errorf("reset scheduled messages: %w", err)
	}

	return nil
}

// PutThreadQueueItem persists one Enqueued Slack Message.
func (s *SessionService) PutThreadQueueItem(id string, item *protocol.ThreadQueueItem) error {
	if err := putThreadQueueItem(context.Background(), s.db, id, item); err != nil {
		return fmt.Errorf("store waiting input: %w", err)
	}

	return nil
}

func putThreadQueueItem(ctx context.Context, db stateStoreDB, id string, item *protocol.ThreadQueueItem) error {
	kind := cmp.Or(item.Kind, protocol.InboundKindEnqueue)

	// Both payloads contain only strings, booleans and byte slices.
	content, _ := json.Marshal(item.Content)
	reply, _ := json.Marshal(item.SlackReply)

	var inbound *string

	if item.Inbound != nil {
		data, err := json.Marshal(item.Inbound)
		if err != nil {
			return fmt.Errorf("encode thread queue inbound: %w", err)
		}

		inbound = new(string(removeSessionEntryNUL(data)))
	}

	changed, err := execRows(ctx, db, "put thread queue item", "count stored queue items", `INSERT INTO thread_queue (queue_item_id, conversation_id, message, principal, stash_at_unix_ns, position, park_after, slack_channel, slack_ts, kind, content, source, slack_reply, inbound_json) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) ON CONFLICT(queue_item_id) DO UPDATE SET message = excluded.message, principal = excluded.principal, stash_at_unix_ns = excluded.stash_at_unix_ns, position = excluded.position, park_after = excluded.park_after, slack_channel = excluded.slack_channel, slack_ts = excluded.slack_ts, kind = excluded.kind, content = excluded.content, source = excluded.source, slack_reply = excluded.slack_reply, inbound_json = excluded.inbound_json WHERE thread_queue.conversation_id = excluded.conversation_id`, strings.TrimSpace(id), strings.TrimSpace(item.ConversationID), item.Message, item.Principal, timeUnixNano(item.StashAt), item.Position, strings.TrimSpace(item.ParkAfter), item.SlackChannel, item.SlackTS, kind, content, item.Source, reply, inbound)
	if err != nil {
		return fmt.Errorf("put thread queue item: %w", err)
	}

	if changed == 0 {
		return errors.New("queue item ID belongs to another conversation")
	}

	return nil
}

func (s *SessionService) queuedConversationIDs(ctx context.Context) ([]string, error) {
	return queryStrings(ctx, s.db, `SELECT DISTINCT conversation_id FROM thread_queue ORDER BY conversation_id`, "queued conversation IDs")
}

// ThreadQueueForConversation returns Enqueued Slack Messages in stack order.
func (s *SessionService) ThreadQueueForConversation(conversationID string) ([]protocol.ThreadQueueItem, error) {
	return queryRows(context.Background(), s.db, `SELECT queue_item_id, conversation_id, message, principal, stash_at_unix_ns, position, park_after, slack_channel, slack_ts, kind, content, source, slack_reply, inbound_json FROM thread_queue WHERE conversation_id = $1 ORDER BY position, stash_at_unix_ns`, "thread queue", scanThreadQueueItem, strings.TrimSpace(conversationID))
}

// DeleteThreadQueueItem deletes one Enqueued Slack Message.
func (s *SessionService) DeleteThreadQueueItem(id string) error {
	if _, err := s.db.ExecContext(context.Background(), `DELETE FROM thread_queue WHERE queue_item_id = $1`, strings.TrimSpace(id)); err != nil {
		return fmt.Errorf("delete thread queue item: %w", err)
	}

	return nil
}

func (d stateDAO) claimThreadQueueItem(ctx context.Context, conversationID, id string) (protocol.ThreadQueueItem, bool, error) {
	item, err := scanThreadQueueItem(d.db.QueryRowContext(ctx, `DELETE FROM thread_queue WHERE conversation_id = $1 AND queue_item_id = $2 AND kind <> $3 RETURNING queue_item_id, conversation_id, message, principal, stash_at_unix_ns, position, park_after, slack_channel, slack_ts, kind, content, source, slack_reply, inbound_json`, conversationID, id, protocol.InboundKindHeld))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return item, false, fmt.Errorf("claim thread queue item: %w", err)
	}

	return item, err == nil, nil
}

func scanThreadQueueItem(scanner rowScanner) (protocol.ThreadQueueItem, error) {
	var (
		item           protocol.ThreadQueueItem
		stashAt        int64
		content, reply []byte
		inbound        []byte
	)

	if err := scanner.Scan(&item.ID, &item.ConversationID, &item.Message, &item.Principal, &stashAt, &item.Position, &item.ParkAfter, &item.SlackChannel, &item.SlackTS, &item.Kind, &content, &item.Source, &reply, &inbound); err != nil {
		return protocol.ThreadQueueItem{}, fmt.Errorf("scan thread queue item: %w", err)
	}

	if inbound != nil {
		if err := json.Unmarshal(inbound, &item.Inbound); err != nil {
			return item, fmt.Errorf("decode thread queue inbound: %w", err)
		}
	}

	if err := json.Unmarshal(content, &item.Content); err != nil {
		return item, fmt.Errorf("decode thread queue content: %w", err)
	}

	if err := json.Unmarshal(reply, &item.SlackReply); err != nil {
		return item, fmt.Errorf("decode thread queue reply: %w", err)
	}

	item.StashAt = timeFromUnixNano(stashAt)

	return item, nil
}

// turnPhase is an active-turn row's lifecycle phase; only done rows are terminal.
type turnPhase string

const (
	turnRunning    turnPhase = "running"
	turnDelivering turnPhase = "delivering"
	turnDone       turnPhase = "done"
)

// activeTurn is a conversation's durable queue head: a taken request and, once
// finished, the final outbound delivered from it.
type activeTurn struct {
	id, conversationID string
	phase              turnPhase
	inbound            *protocol.InboundMessage
	outbound           *protocol.OutboundMessage
}

// turnFinish is everything a bridge request writes when it moves to delivering.
type turnFinish struct {
	store       sessionStore
	entries     []harness.SessionEntry
	accountGoal bool
	outbound    *protocol.OutboundMessage
	terminal    protocol.Terminal
	hidden      bool // The turn is a hidden run's (hiddenRun).
	// retry is set by finishTurn: the fewest failed wakes among the notes the hidden run's turn
	// released for a retry, or 0 when it released none.
	retry int
}

// conversationJournal stores one conversation's RocketCode turn steps.
type conversationJournal struct {
	store          *SessionService
	conversationID string
	log            *slog.Logger
}

// LoadTurnStep returns one recorded step of a conversation's turns.
func (s *SessionService) LoadTurnStep(ctx context.Context, conversationID, key string) (value json.RawMessage, found bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT value FROM turn_steps WHERE conversation_id = $1 AND key = $2`, conversationID, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, fmt.Errorf("load turn step: %w", err)
	}

	return value, true, nil
}

// SaveTurnStep records one step of a conversation's turns; the turn's finish removes it.
func (s *SessionService) SaveTurnStep(ctx context.Context, conversationID, key string, value json.RawMessage) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO turn_steps (conversation_id, key, value) VALUES ($1, $2, $3) ON CONFLICT (conversation_id, key) DO UPDATE SET value = excluded.value`, conversationID, key, string(removeSessionEntryNUL(value))); err != nil {
		return fmt.Errorf("save turn step: %w", err)
	}

	return nil
}

func (j conversationJournal) Load(ctx context.Context, key string) (value json.RawMessage, found bool, err error) {
	return j.store.LoadTurnStep(ctx, j.conversationID, key)
}

func (j conversationJournal) Save(ctx context.Context, key string, value json.RawMessage) error {
	return j.store.SaveTurnStep(ctx, j.conversationID, key, value)
}

// SaveTrace records live progress on the request's row; retry turn IDs share it,
// and late writes cannot alter a stopped or failed turn.
func (j conversationJournal) SaveTrace(ctx context.Context, turnID string, trace []json.RawMessage) (err error) {
	startedAt := time.Now()

	defer func() {
		// These are repeated snapshots, not tool starts/finishes or execution durations.
		// Ponytail: full snapshots repeat prior operations; a delta API needs separate approval.
		observed := harness.PublicProgressFromTrace(trace)
		for i := range observed {
			progress := &observed[i]
			if progress.Kind != harness.PublicProgressText {
				j.log.Info("operation observed", "event", "operation_snapshot", "conversation_id", j.conversationID, "turn_id", turnID, "operation_id", progress.ID, "parent_id", progress.ParentID, "kind", progress.Kind, "state", progress.State, "boundary", "journal_observation", "observation_elapsed_ms", time.Since(startedAt).Milliseconds(), "error_type", fmt.Sprintf("%T", err))
			}
		}
	}()

	rowID, _, _ := strings.Cut(turnID, "/")

	data, err := json.Marshal(trace)
	if err != nil {
		return fmt.Errorf("encode turn trace: %w", err)
	}

	if _, err := j.store.db.ExecContext(ctx, `UPDATE active_turns SET output_trace_json = $2, updated_at_unix_ns = $3 WHERE id = $1 AND terminal = '' AND output_trace_json::jsonb IS DISTINCT FROM $2::jsonb`, rowID, string(removeSessionEntryNUL(data)), timeUnixNano(time.Now())); err != nil {
		return fmt.Errorf("save turn trace: %w", err)
	}

	return nil
}

// startTurnDB records a taken request as its conversation's running queue head.
func startTurnDB(ctx context.Context, db stateStoreDB, turnID, conversationID string, inbound *protocol.InboundMessage) error {
	data, err := json.Marshal(inbound)
	if err != nil {
		return fmt.Errorf("encode turn inbound: %w", err)
	}

	now := timeUnixNano(time.Now())
	if _, err := db.ExecContext(ctx, `INSERT INTO active_turns (id, conversation_id, inbound_json, output_trace_json, created_at_unix_ns, updated_at_unix_ns, history_anchor_id)
VALUES ($1, $2, $3, '[]', $4, $4, COALESCE((SELECT MAX(id) FROM session_entries WHERE conversation_id = $2), 0))`, turnID, conversationID, string(removeSessionEntryNUL(data)), now); err != nil {
		return fmt.Errorf("start active turn: %w", err)
	}

	if inbound.SyncDestination != "" || inbound.RequireOutputDecision {
		if _, err := db.ExecContext(ctx, `UPDATE managed_conversations SET producer_inbound_json =
($2::jsonb || jsonb_build_object('SyncDestination', COALESCE(NULLIF(producer_inbound_json->>'SyncDestination', ''), $2::jsonb->>'SyncDestination')))::json
WHERE conversation_id = $1`, conversationID, string(removeSessionEntryNUL(data))); err != nil {
			return fmt.Errorf("retain producer routing: %w", err)
		}
	}

	return nil
}

// headTurn returns the non-terminal row that runs on workerID: its own, or a
// private producer's whose sync destination it is.
func (s *SessionService) headTurn(ctx context.Context, workerID string) (activeTurn, bool, error) {
	var (
		turn              activeTurn
		inbound, outbound []byte
	)

	err := s.db.QueryRowContext(ctx, `SELECT id, conversation_id, phase, inbound_json, outbound_json FROM active_turns
WHERE phase <> $2 AND COALESCE(NULLIF(inbound_json->>'SyncDestination', ''), conversation_id) = $1 ORDER BY created_at_unix_ns LIMIT 1`, workerID, turnDone).Scan(&turn.id, &turn.conversationID, &turn.phase, &inbound, &outbound)
	if errors.Is(err, sql.ErrNoRows) {
		return activeTurn{}, false, nil
	}

	if err != nil {
		return activeTurn{}, false, fmt.Errorf("load active turn: %w", err)
	}

	if err := json.Unmarshal(inbound, &turn.inbound); err != nil {
		return activeTurn{}, false, fmt.Errorf("decode active turn inbound: %w", err)
	}

	if outbound != nil {
		if err := json.Unmarshal(outbound, &turn.outbound); err != nil {
			return activeTurn{}, false, fmt.Errorf("decode active turn outbound: %w", err)
		}

		if err := s.loadAttachmentData(ctx, turn.outbound.Attachments); err != nil {
			return activeTurn{}, false, err
		}
	}

	return turn, true, nil
}

func (s *SessionService) activeTurnWorkers(ctx context.Context) ([]string, error) {
	return queryStrings(ctx, s.db, `SELECT DISTINCT COALESCE(NULLIF(inbound_json->>'SyncDestination', ''), conversation_id) FROM active_turns WHERE phase <> $1 ORDER BY 1`, "active turn workers", turnDone)
}

// HasActiveTurn reports whether the conversation has a running or undelivered turn.
func (s *SessionService) HasActiveTurn(ctx context.Context, conversationID string) (bool, error) {
	var active bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM active_turns WHERE conversation_id = $1 AND phase <> $2)`, conversationID, turnDone).Scan(&active); err != nil {
		return false, fmt.Errorf("check active turn: %w", err)
	}

	return active, nil
}

// finishTurn moves a running row to delivering in one transaction with its
// history, goal accounting, Completion Note claims, and journal cleanup that
// keeps Background Jobs' steps, and returns the outbound to
// deliver. When another path, such as $stop, finished the row first, that
// path's stored outbound wins and nothing else is written.
func (s *SessionService) finishTurn(ctx context.Context, turnID string, finish *turnFinish) (*protocol.OutboundMessage, error) {
	outbound, err := json.Marshal(finish.outbound)
	if err != nil {
		return nil, fmt.Errorf("encode final outbound: %w", err)
	}

	tx, err := s.beginStateTx(ctx, "turn finish")
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// commitRevertDB also takes the history lock before the turn row.
	if err := lockSessionHistory(ctx, tx, finish.store.conversationID); err != nil {
		return nil, err
	}

	moved, err := execRows(ctx, tx, "finish active turn", "count finished active turn", `UPDATE active_turns SET phase = $2, terminal = $4, updated_at_unix_ns = $5,
outbound_json = (SELECT json_object_agg(key, COALESCE(reply.value, payload.value))
    FROM json_each($3::json) payload
    FULL JOIN (SELECT 'ReplyState' AS key, value FROM turn_steps WHERE conversation_id = $7 AND key = $8) reply USING (key))
WHERE id = $1 AND phase = $6`, turnID, turnDelivering, string(removeSessionEntryNUL(outbound)), finish.terminal, timeUnixNano(time.Now()), turnRunning, finish.store.conversationID, protocol.ReplyStepKey(turnID))
	if err != nil {
		return nil, err
	}

	if moved == 0 {
		var stored protocol.OutboundMessage

		if err := tx.QueryRowContext(ctx, `SELECT outbound_json FROM active_turns WHERE id = $1`, turnID).Scan(&outbound); err != nil {
			return nil, fmt.Errorf("load finished turn outbound: %w", err)
		}

		if err := json.Unmarshal(outbound, &stored); err != nil {
			return nil, fmt.Errorf("decode finished turn outbound: %w", err)
		}

		// A runner arriving after its turn ended may have rewritten the record.
		if err := s.indexTurnMessages(ctx, tx, finish.store.conversationID, turnID); err != nil {
			return nil, err
		}

		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit late turn finish: %w", err)
		}

		return &stored, s.loadAttachmentData(ctx, stored.Attachments)
	}

	for i := range finish.entries {
		if _, err := finish.store.appendDB(ctx, tx, &finish.entries[i]); err != nil {
			return nil, err
		}
	}

	if finish.terminal != "" {
		if err := s.indexTurnMessages(ctx, tx, finish.store.conversationID, turnID); err != nil {
			return nil, err
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM thread_queue q WHERE q.conversation_id = $1 AND EXISTS (
    SELECT 1 FROM session_entries e CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry_json::jsonb->'replay_input', 'null'::jsonb)) item
    WHERE e.conversation_id = q.conversation_id AND item->>'input_id' = q.queue_item_id)`, finish.store.conversationID); err != nil {
		return nil, fmt.Errorf("release recorded input ownership: %w", err)
	}

	if finish.accountGoal {
		if _, err := tx.ExecContext(ctx, `UPDATE conversation_goals SET turns_used = turns_used + 1, status = CASE WHEN max_turns > 0 AND turns_used + 1 >= max_turns THEN $1 ELSE $2 END, updated_at_unix_ns = $3 WHERE conversation_id = $4 AND (status = '' OR status = $2)`, GoalStatusBudgetExhausted, GoalStatusActive, timeUnixNano(time.Now()), finish.store.conversationID); err != nil {
			return nil, fmt.Errorf("account goal turn: %w", err)
		}
	}

	// A turn that saved entries delivered its claimed notes; one that saved none, such as a
	// failed wake, releases them to the next turn without waking for them again, as OpenCode's
	// next turn still sees a failed turn's promoted input (packages/core/src/session/input.ts).
	// A hidden run may have no next turn, so its main agent's notes wake it again 1 minute after
	// the first such turn, 5 minutes after the second, and every 30 minutes after that.
	var errSettle error

	switch {
	case len(finish.entries) > 0:
		_, errSettle = tx.ExecContext(ctx, `UPDATE background_jobs SET note_state = $3 WHERE conversation_id = $1 AND claimed_turn_id = $2 AND note_state = 'pending'`, finish.store.conversationID, turnID, noteConsumed)
	case finish.hidden:
		errSettle = tx.QueryRowContext(ctx, `WITH released AS (UPDATE background_jobs SET claimed_turn_id = '', wake = (child_key = ''), wake_attempts = wake_attempts + 1,
wake_after_unix_ns = $4 + ($5::bigint[])[LEAST(wake_attempts + 1, 3)] WHERE conversation_id = $1 AND claimed_turn_id = $2 AND note_state = $3 RETURNING wake_attempts, child_key)
SELECT COALESCE(MIN(wake_attempts) FILTER (WHERE child_key = ''), 0) FROM released`, finish.store.conversationID, turnID, notePending, timeUnixNano(time.Now()), []int64{int64(time.Minute), int64(5 * time.Minute), int64(30 * time.Minute)}).Scan(&finish.retry)
	default:
		_, errSettle = tx.ExecContext(ctx, `UPDATE background_jobs SET claimed_turn_id = '', wake = FALSE WHERE conversation_id = $1 AND claimed_turn_id = $2 AND note_state = $3`, finish.store.conversationID, turnID, notePending)
	}

	if errSettle != nil {
		return nil, fmt.Errorf("settle background note claims: %w", errSettle)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM turn_steps s WHERE conversation_id = $1 AND (starts_with(key, $2 || '/') OR key = $2 AND $3 = '') AND `+keepRunningJobSteps, finish.store.conversationID, turnID, finish.terminal); err != nil {
		return nil, fmt.Errorf("clear turn steps: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit turn finish: %w", err)
	}

	if finish.store.managedConversationID != "" && len(finish.entries) > 0 {
		s.completeTurnPairReservation(finish.store.managedConversationID, finish.store.conversationID)
	}

	return finish.outbound, nil
}

// keepRunningJobSteps keeps, in a delete of turn_steps s under the key prefix $2, the steps
// of the running Background Jobs nested under that prefix, which own them.
const keepRunningJobSteps = `NOT EXISTS (SELECT 1 FROM background_jobs j WHERE j.conversation_id = s.conversation_id AND j.status = 'running'
    AND starts_with(j.job_id, $2 || '/') AND starts_with(s.key, j.job_id || '/'))`

// closeTurn ends a delivered row; stopped and failed rows stay as transcript history.
// Steps recorded while delivering (a posted cron root) go with it.
// Web owners without replay identity, and goal kickoffs with generated prompt
// framing, stay done so retries and editable commands retain their original input.
func (s *SessionService) closeTurn(ctx context.Context, turnID string) error {
	if _, err := s.db.ExecContext(ctx, `WITH delivered AS (DELETE FROM active_turns a WHERE id = $2 AND terminal = '' AND
    NOT (inbound_json->>'Source' = 'web' AND COALESCE(inbound_json->'Metadata'->>'web_message_id', '') <> '' AND
        (inbound_json->>'GoalAction' = 'goal' OR NOT EXISTS (
        SELECT 1 FROM session_entries e CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry_json::jsonb->'replay_input', 'null'::jsonb)) item
        WHERE e.conversation_id = a.conversation_id AND item->>'input_id' = a.inbound_json->'Metadata'->>'web_message_id'))) RETURNING id),
delivery_steps AS (DELETE FROM turn_steps s WHERE starts_with(key, $2 || '/') AND `+keepRunningJobSteps+`)
UPDATE active_turns SET phase = $1 WHERE id = $2 AND id NOT IN (SELECT id FROM delivered)`, turnDone, turnID); err != nil {
		return fmt.Errorf("close active turn: %w", err)
	}

	return nil
}

// backgroundJobKind is the work a Background Job runs; task and subagent wake
// rows run a subagent.
type backgroundJobKind string

const (
	backgroundExecute      backgroundJobKind = "execute"
	backgroundTask         backgroundJobKind = "task"
	backgroundSubagentWake backgroundJobKind = "subagent_wake"
)

// backgroundJobStatus is a Background Job's lifecycle state; only running is not terminal.
type backgroundJobStatus string

const (
	backgroundRunning   backgroundJobStatus = "running"
	backgroundCompleted backgroundJobStatus = "completed"
	backgroundFailed    backgroundJobStatus = "failed"
	backgroundStopped   backgroundJobStatus = "stopped"
	backgroundKilled    backgroundJobStatus = "killed"
)

// noteState tracks a Background Job's Completion Note.
type noteState string

const (
	noteNone     noteState = "none"
	notePending  noteState = "pending"
	noteConsumed noteState = "consumed"
)

// backgroundJob is one durable Background Job row, keyed by its root
// conversation and its tool call key. The job owns the journal steps under
// jobID + "/"; childKey names the owning subagent, empty for the main agent.
type backgroundJob struct {
	conversationID, jobID, childKey string
	kind                            backgroundJobKind
	status                          backgroundJobStatus
	agent, label, callID            string
	subagentKey                     string
	// origin is the originating turn's frozen routing; its SyncDestination
	// also lists the job at that conversation.
	origin    *protocol.InboundMessage
	runnerID  string
	noteState noteState
	wake      bool
	// wakeAfter is when a note a hidden run released may wake it again (finishTurn).
	wakeAfter     time.Time
	claimedTurnID string
	createdAt     time.Time
	finishedAt    time.Time
	// result is the job's output, or its error when it failed.
	result string
}

const backgroundJobColumns = `conversation_id, job_id, child_key, kind, status, agent, label, call_id, subagent_key, origin_json, runner_id, note_state, wake, wake_after_unix_ns, claimed_turn_id, created_at_unix_ns, finished_at_unix_ns, result`

func scanBackgroundJob(scanner rowScanner) (backgroundJob, error) {
	var (
		job                              backgroundJob
		origin                           []byte
		wakeAfter, createdAt, finishedAt int64
	)

	if err := scanner.Scan(&job.conversationID, &job.jobID, &job.childKey, &job.kind, &job.status, &job.agent, &job.label, &job.callID, &job.subagentKey, &origin, &job.runnerID, &job.noteState, &job.wake, &wakeAfter, &job.claimedTurnID, &createdAt, &finishedAt, &job.result); err != nil {
		return backgroundJob{}, fmt.Errorf("scan background job: %w", err)
	}

	if err := json.Unmarshal(origin, &job.origin); err != nil {
		return backgroundJob{}, fmt.Errorf("decode background job origin: %w", err)
	}

	job.wakeAfter, job.createdAt, job.finishedAt = timeFromUnixNano(wakeAfter), timeFromUnixNano(createdAt), timeFromUnixNano(finishedAt)

	return job, nil
}

// createBackgroundJob records a running job once per (conversation, job ID)
// and returns the stored row; created is false when the row already existed.
func createBackgroundJob(ctx context.Context, db stateStoreDB, job *backgroundJob) (stored backgroundJob, created bool, err error) {
	origin, err := json.Marshal(job.origin)
	if err != nil {
		return backgroundJob{}, false, fmt.Errorf("encode background job origin: %w", err)
	}

	inserted, err := execRows(ctx, db, "create background job", "count created background job", `INSERT INTO background_jobs (conversation_id, job_id, child_key, kind, status, agent, label, call_id, subagent_key, origin_json, sync_destination, runner_id, note_state, wake, claimed_turn_id, created_at_unix_ns, finished_at_unix_ns, result)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, FALSE, '', $14, 0, '') ON CONFLICT (conversation_id, job_id) DO NOTHING`, job.conversationID, job.jobID, job.childKey, job.kind, backgroundRunning, job.agent, job.label, job.callID, job.subagentKey, string(removeSessionEntryNUL(origin)), job.origin.SyncDestination, job.runnerID, noteNone, timeUnixNano(time.Now()))
	if err != nil {
		return backgroundJob{}, false, err
	}

	stored, err = scanBackgroundJob(db.QueryRowContext(ctx, `SELECT `+backgroundJobColumns+` FROM background_jobs WHERE conversation_id = $1 AND job_id = $2`, job.conversationID, job.jobID))

	return stored, inserted > 0, err
}

// finishBackgroundJob ends a running job held by job.runnerID and, in the same transaction,
// settles the notes its subagent claimed and deletes its journal steps except those of the
// running jobs it started. It reports false when another transition or runner won.
func (s *SessionService) finishBackgroundJob(ctx context.Context, job *backgroundJob) (bool, error) {
	if job.kind == backgroundSubagentWake { // A subagent wake leaves no note of its own.
		job.noteState, job.wake = noteNone, false
	}

	tx, err := s.beginStateTx(ctx, "background job finish")
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	finished, err := execRows(ctx, tx, "finish background job", "count finished background job", `UPDATE background_jobs SET status = $4, note_state = $5, wake = $6, result = $7, finished_at_unix_ns = $8
WHERE conversation_id = $1 AND job_id = $2 AND runner_id = $3 AND status = $9`, job.conversationID, job.jobID, job.runnerID, job.status, job.noteState, job.wake, strings.ReplaceAll(job.result, "\x00", ""), timeUnixNano(time.Now()), backgroundRunning)
	if err != nil || finished == 0 {
		return false, err
	}

	// A subagent saves its turn only when its job completes; otherwise the notes it claimed go back to
	// its next turn, like a failed turn's.
	claims, settled := `UPDATE background_jobs SET note_state = $3 WHERE conversation_id = $1 AND claimed_turn_id = $2 AND note_state = 'pending'`, noteConsumed
	if job.status != backgroundCompleted {
		claims, settled = `UPDATE background_jobs SET claimed_turn_id = '', wake = FALSE WHERE conversation_id = $1 AND claimed_turn_id = $2 AND note_state = $3`, notePending
	}

	if _, err := tx.ExecContext(ctx, claims, job.conversationID, job.jobID, settled); err != nil {
		return false, fmt.Errorf("settle background job note claims: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM turn_steps s WHERE conversation_id = $1 AND starts_with(key, $2 || '/') AND `+keepRunningJobSteps, job.conversationID, job.jobID); err != nil {
		return false, fmt.Errorf("clear background job steps: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit background job finish: %w", err)
	}

	return true, nil
}

// hasBackgroundJob reports whether the call keyed jobID already became a job.
func (s *SessionService) hasBackgroundJob(ctx context.Context, conversationID, jobID string) (bool, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM background_jobs WHERE conversation_id = $1 AND job_id = $2)`, conversationID, jobID).Scan(&exists); err != nil {
		return false, fmt.Errorf("look up background job: %w", err)
	}

	return exists, nil
}

// restampBackgroundJob hands a running job held by job.runnerID to runnerID.
func (s *SessionService) restampBackgroundJob(ctx context.Context, job *backgroundJob, runnerID string) (bool, error) {
	moved, err := execRows(ctx, s.db, "restamp background job", "count restamped background job", `UPDATE background_jobs SET runner_id = $4 WHERE conversation_id = $1 AND job_id = $2 AND runner_id = $3 AND status = $5`, job.conversationID, job.jobID, job.runnerID, runnerID, backgroundRunning)
	return moved > 0, err
}

// claimBackgroundNotes claims the owner's unclaimed pending notes for turnID,
// skipping notes another transaction is claiming, in finish order. With held it
// also returns the pending notes turnID already holds, for a resumed turn.
func claimBackgroundNotes(ctx context.Context, db stateStoreDB, conversationID, childKey, turnID string, held bool) ([]backgroundJob, error) {
	return queryRows(ctx, db, `WITH claimed AS (UPDATE background_jobs SET claimed_turn_id = $3 WHERE (conversation_id, job_id) IN (
    SELECT conversation_id, job_id FROM background_jobs WHERE conversation_id = $1 AND child_key = $2 AND note_state = $4 AND claimed_turn_id = '' FOR UPDATE SKIP LOCKED
) RETURNING `+backgroundJobColumns+`)
SELECT * FROM (SELECT * FROM claimed UNION ALL SELECT `+backgroundJobColumns+` FROM background_jobs WHERE $5 AND conversation_id = $1 AND child_key = $2 AND note_state = $4 AND claimed_turn_id = $3) notes ORDER BY finished_at_unix_ns, job_id`, "background note claims", scanBackgroundJob, conversationID, childKey, turnID, notePending, held)
}

// redirectBackgroundJobs makes a hidden run's jobs without a destination report to destinationID.
func (d stateDAO) redirectBackgroundJobs(ctx context.Context, conversationID, destinationID string) error {
	if _, err := d.db.ExecContext(ctx, `UPDATE background_jobs SET sync_destination = $2, origin_json = (origin_json::jsonb || jsonb_build_object('SyncDestination', $2::text))::json WHERE conversation_id = $1 AND sync_destination = ''`, conversationID, destinationID); err != nil {
		return fmt.Errorf("redirect background jobs: %w", err)
	}

	return nil
}

// backgroundWake returns the owner's unclaimed Completion Note that may wake it first, if any:
// the earliest finished of those that may wake it now, or else the one whose wakeAfter comes first.
func (s *SessionService) backgroundWake(ctx context.Context, conversationID, childKey string) (backgroundJob, bool, error) {
	job, err := scanBackgroundJob(s.db.QueryRowContext(ctx, `SELECT `+backgroundJobColumns+` FROM background_jobs WHERE conversation_id = $1 AND child_key = $2 AND note_state = $3 AND wake AND claimed_turn_id = '' ORDER BY GREATEST(wake_after_unix_ns, $4), finished_at_unix_ns, job_id LIMIT 1`, conversationID, childKey, notePending, timeUnixNano(time.Now())))
	if errors.Is(err, sql.ErrNoRows) {
		return backgroundJob{}, false, nil
	}

	return job, err == nil, err
}

// backgroundWakeOwners returns, for startup, one unclaimed Completion Note
// that may wake its owner, per owner.
func (s *SessionService) backgroundWakeOwners(ctx context.Context) ([]backgroundJob, error) {
	return queryRows(ctx, s.db, `SELECT DISTINCT ON (conversation_id, child_key) `+backgroundJobColumns+` FROM background_jobs WHERE note_state = $1 AND wake AND claimed_turn_id = '' ORDER BY conversation_id, child_key`, "background wake owners", scanBackgroundJob, notePending)
}

// notedBackgroundJobs returns the conversation's jobs listed in jobIDs, in finish order.
func (s *SessionService) notedBackgroundJobs(ctx context.Context, conversationID string, jobIDs []string) ([]backgroundJob, error) {
	return queryRows(ctx, s.db, `SELECT `+backgroundJobColumns+` FROM background_jobs WHERE conversation_id = $1 AND job_id = ANY($2) ORDER BY finished_at_unix_ns, job_id`, "noted background jobs", scanBackgroundJob, conversationID, jobIDs)
}

// startSubagentWake claims the pending notes of the subagent a wake job runs
// and records the job, running as that subagent's agent, in one transaction.
// It returns no notes, and records nothing, unless one of them may wake it.
func (s *SessionService) startSubagentWake(ctx context.Context, job *backgroundJob) ([]backgroundJob, error) {
	tx, err := s.beginStateTx(ctx, "subagent wake start")
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	notes, err := claimBackgroundNotes(ctx, tx, job.conversationID, job.subagentKey, job.jobID, false)
	if err != nil || !slices.ContainsFunc(notes, func(note backgroundJob) bool { return note.wake }) {
		return nil, err
	}

	job.agent, job.label, job.origin = notes[0].agent, notes[0].label, notes[0].origin
	if _, _, err := createBackgroundJob(ctx, tx, job); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit subagent wake start: %w", err)
	}

	return notes, nil
}

// releaseStaleBackgroundClaims frees pending notes claimed by turns that are no longer running.
func (s *SessionService) releaseStaleBackgroundClaims(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE background_jobs j SET claimed_turn_id = '' WHERE note_state = $1 AND claimed_turn_id <> ''
AND NOT EXISTS (SELECT 1 FROM active_turns a WHERE a.id = j.claimed_turn_id AND a.phase = $2)`, notePending, turnRunning); err != nil {
		return fmt.Errorf("release stale background note claims: %w", err)
	}

	return nil
}

// backgroundJobs lists a conversation's running jobs and undelivered notes,
// including those of hidden runs whose sync destination it is.
func (s *SessionService) backgroundJobs(ctx context.Context, conversationID string) ([]backgroundJob, error) {
	return queryRows(ctx, s.db, `SELECT `+backgroundJobColumns+` FROM background_jobs WHERE (conversation_id = $1 OR sync_destination = $1) AND (status = $2 OR note_state = $3) ORDER BY created_at_unix_ns, job_id`, "background jobs", scanBackgroundJob, conversationID, backgroundRunning, notePending)
}

// runningBackgroundJobs returns every running job, for startup recovery.
func (s *SessionService) runningBackgroundJobs(ctx context.Context) ([]backgroundJob, error) {
	return queryRows(ctx, s.db, `SELECT `+backgroundJobColumns+` FROM background_jobs WHERE status = $1 ORDER BY created_at_unix_ns, conversation_id, job_id`, "running background jobs", scanBackgroundJob, backgroundRunning)
}

func boolInt(value bool) int {
	if value {
		return 1
	}

	return 0
}

func timeUnixNano(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}

	return value.UTC().UnixNano()
}

func timeFromUnixNano(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}

	return time.Unix(0, value).UTC()
}

type rowScanner interface {
	Scan(...any) error
}

func scanScheduledMessage(scanner rowScanner) (id string, message protocol.ScheduledMessageState, err error) {
	var (
		dueAt, interval int64
		recurring       int
	)
	if err := scanner.Scan(&id, &message.ConversationID, &message.Agent, &message.Message, &dueAt, &recurring, &interval); err != nil {
		return "", protocol.ScheduledMessageState{}, fmt.Errorf("scan scheduled message: %w", err)
	}

	message.DueAt = timeFromUnixNano(dueAt)
	message.Recurring = recurring != 0
	message.Interval = time.Duration(interval)

	return id, message, nil
}

func scanCronSchedule(scanner rowScanner) (CronScheduleState, error) {
	var (
		schedule CronScheduleState
		nextDue  int64
	)

	if err := scanner.Scan(&schedule.ScheduleID, &schedule.RelativePath, &nextDue); err != nil {
		return CronScheduleState{}, fmt.Errorf("scan cron schedule: %w", err)
	}

	schedule.NextDue = timeFromUnixNano(nextDue)

	return schedule, nil
}
