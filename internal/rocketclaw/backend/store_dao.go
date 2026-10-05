package backend

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	harness "github.com/Rocketable/platform/internal/rocketcode"
)

type stateDAO struct {
	db stateStoreDB
}

// Thread returns the persisted managed conversation state.
func (s *SessionService) Thread(conversationID string) (ThreadState, bool, error) {
	var (
		thread    ThreadState
		createdBy string
	)

	err := s.db.QueryRowContext(context.Background(), `SELECT agent, created_by, settled FROM managed_conversations WHERE conversation_id = $1`, strings.TrimSpace(conversationID)).Scan(&thread.Agent, &createdBy, &thread.Settled)
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
	_, session, err := scanExternalMCPSession(d.db.QueryRowContext(ctx, `SELECT external_conversation_id, agent, private_conversation_id, managed_conversation_id, slack_channel FROM external_mcp_sessions WHERE external_conversation_id = $1`, strings.TrimSpace(externalConversationID)))
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
	externalConversationID, session, err = scanExternalMCPSession(s.db.QueryRowContext(context.Background(), `SELECT external_conversation_id, agent, private_conversation_id, managed_conversation_id, slack_channel FROM external_mcp_sessions WHERE private_conversation_id = $1 OR managed_conversation_id = $2`, strings.TrimSpace(conversationID), strings.TrimSpace(conversationID)))
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
	)
	if err := scanner.Scan(&externalConversationID, &session.Agent, &privateConversationID, &session.ManagedConversationID, &session.SlackChannel); err != nil {
		return "", ExternalMCPSessionState{}, fmt.Errorf("scan external MCP session: %w", err)
	}

	session.PrivateConversationID = privateConversationID.String

	return externalConversationID, session, nil
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

	_, err := s.db.ExecContext(context.Background(), `INSERT INTO thread_queue (queue_item_id, conversation_id, message, principal, stash_at_unix_ns, position, park_after, slack_channel, slack_ts, kind, content, source, slack_reply, inbound_json) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) ON CONFLICT(queue_item_id) DO UPDATE SET conversation_id = excluded.conversation_id, message = excluded.message, principal = excluded.principal, stash_at_unix_ns = excluded.stash_at_unix_ns, position = excluded.position, park_after = excluded.park_after, slack_channel = excluded.slack_channel, slack_ts = excluded.slack_ts, kind = excluded.kind, content = excluded.content, source = excluded.source, slack_reply = excluded.slack_reply, inbound_json = excluded.inbound_json`, strings.TrimSpace(id), strings.TrimSpace(item.ConversationID), item.Message, item.Principal, timeUnixNano(item.StashAt), item.Position, strings.TrimSpace(item.ParkAfter), item.SlackChannel, item.SlackTS, kind, content, item.Source, reply, inbound)
	if err != nil {
		return fmt.Errorf("put thread queue item: %w", err)
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
}

// conversationJournal stores one conversation's RocketCode turn steps.
type conversationJournal struct {
	store          *SessionService
	conversationID string
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
func (j conversationJournal) SaveTrace(ctx context.Context, turnID string, trace []json.RawMessage) error {
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
// history, goal accounting, and journal cleanup, and returns the outbound to
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

	moved, err := execRows(ctx, tx, "finish active turn", "count finished active turn", `UPDATE active_turns SET phase = $2, terminal = $4, updated_at_unix_ns = $5,
outbound_json = ($3::jsonb || COALESCE((SELECT jsonb_build_object('ReplyState', value::jsonb) FROM turn_steps WHERE conversation_id = $7 AND key = $8), '{}'::jsonb))::json
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

		return &stored, nil
	}

	for i := range finish.entries {
		if _, err := finish.store.appendDB(ctx, tx, &finish.entries[i]); err != nil {
			return nil, err
		}
	}

	if finish.accountGoal {
		if _, err := tx.ExecContext(ctx, `UPDATE conversation_goals SET turns_used = turns_used + 1, status = CASE WHEN max_turns > 0 AND turns_used + 1 >= max_turns THEN $1 ELSE $2 END, updated_at_unix_ns = $3 WHERE conversation_id = $4 AND (status = '' OR status = $2)`, GoalStatusBudgetExhausted, GoalStatusActive, timeUnixNano(time.Now()), finish.store.conversationID); err != nil {
			return nil, fmt.Errorf("account goal turn: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM turn_steps WHERE conversation_id = $1 AND (starts_with(key, $2 || '/') OR key = $2 AND $3 = '')`, finish.store.conversationID, turnID, finish.terminal); err != nil {
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

// closeTurn ends a delivered row; stopped and failed rows stay as transcript history.
// Steps recorded while delivering (a posted cron root) go with it.
func (s *SessionService) closeTurn(ctx context.Context, turnID string) error {
	if _, err := s.db.ExecContext(ctx, `WITH delivered AS (DELETE FROM active_turns WHERE id = $1 AND terminal = '' RETURNING id),
delivery_steps AS (DELETE FROM turn_steps WHERE starts_with(key, $1 || '/'))
UPDATE active_turns SET phase = $2 WHERE id = $1 AND terminal <> ''`, turnID, turnDone); err != nil {
		return fmt.Errorf("close active turn: %w", err)
	}

	return nil
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
