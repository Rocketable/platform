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
	rows, err := s.db.QueryContext(context.Background(), `SELECT conversation_id, objective, check_script, max_turns, turns_used, status, note, slack_recipient_team_id, slack_recipient_user_id, created_at_unix_ns, updated_at_unix_ns FROM conversation_goals WHERE status = '' OR status = $1 ORDER BY conversation_id`, GoalStatusActive)
	if err != nil {
		return nil, fmt.Errorf("query active goals: %w", err)
	}
	defer func() { _ = rows.Close() }()

	goals := map[string]GoalState{}

	for rows.Next() {
		conversationID, goal, err := scanGoal(rows)
		if err != nil {
			return nil, err
		}

		goals[conversationID] = goal
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read active goals: %w", err)
	}

	if len(goals) == 0 {
		return nil, nil
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

	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query scheduled messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	messages := map[string]protocol.ScheduledMessageState{}

	for rows.Next() {
		id, message, err := scanScheduledMessage(rows)
		if err != nil {
			return nil, err
		}

		messages[id] = message
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read scheduled messages: %w", err)
	}

	if len(messages) == 0 {
		return nil, nil
	}

	return messages, nil
}

func (d stateDAO) clearParkAfter(ctx context.Context, scheduledID string) error {
	if _, err := d.db.ExecContext(ctx, `UPDATE thread_queue SET park_after = '' WHERE park_after = $1`, strings.TrimSpace(scheduledID)); err != nil {
		return fmt.Errorf("clear thread queue park: %w", err)
	}

	return nil
}

// DeleteScheduledMessage deletes one scheduled message.
func (s *SessionService) DeleteScheduledMessage(id string) error {
	id = strings.TrimSpace(id)

	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin scheduled message delete: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := (stateDAO{db: tx}).clearParkAfter(context.Background(), id); err != nil {
		return err
	}

	if _, err := tx.ExecContext(context.Background(), `DELETE FROM scheduled_messages WHERE scheduled_message_id = $1`, id); err != nil {
		return fmt.Errorf("delete scheduled message: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit scheduled message delete: %w", err)
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

	_, err := s.db.ExecContext(context.Background(), `INSERT INTO thread_queue (queue_item_id, conversation_id, message, principal, stash_at_unix_ns, position, park_after, slack_channel, slack_ts, kind, content, source, slack_reply) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) ON CONFLICT(queue_item_id) DO UPDATE SET conversation_id = excluded.conversation_id, message = excluded.message, principal = excluded.principal, stash_at_unix_ns = excluded.stash_at_unix_ns, position = excluded.position, park_after = excluded.park_after, slack_channel = excluded.slack_channel, slack_ts = excluded.slack_ts, kind = excluded.kind, content = excluded.content, source = excluded.source, slack_reply = excluded.slack_reply`, strings.TrimSpace(id), strings.TrimSpace(item.ConversationID), item.Message, item.Principal, timeUnixNano(item.StashAt), item.Position, strings.TrimSpace(item.ParkAfter), item.SlackChannel, item.SlackTS, kind, content, item.Source, reply)
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
	rows, err := s.db.QueryContext(context.Background(), `SELECT queue_item_id, conversation_id, message, principal, stash_at_unix_ns, position, park_after, slack_channel, slack_ts, kind, content, source, slack_reply FROM thread_queue WHERE conversation_id = $1 ORDER BY position, stash_at_unix_ns`, strings.TrimSpace(conversationID))
	if err != nil {
		return nil, fmt.Errorf("query thread queue: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []protocol.ThreadQueueItem

	for rows.Next() {
		item, err := scanThreadQueueItem(rows)
		if err != nil {
			return nil, err
		}

		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read thread queue: %w", err)
	}

	return items, nil
}

// DeleteThreadQueueItem deletes one Enqueued Slack Message.
func (s *SessionService) DeleteThreadQueueItem(id string) error {
	if _, err := s.db.ExecContext(context.Background(), `DELETE FROM thread_queue WHERE queue_item_id = $1`, strings.TrimSpace(id)); err != nil {
		return fmt.Errorf("delete thread queue item: %w", err)
	}

	return nil
}

func (d stateDAO) claimThreadQueueItem(ctx context.Context, conversationID, id string) (protocol.ThreadQueueItem, bool, error) {
	item, err := scanThreadQueueItem(d.db.QueryRowContext(ctx, `DELETE FROM thread_queue WHERE conversation_id = $1 AND queue_item_id = $2 RETURNING queue_item_id, conversation_id, message, principal, stash_at_unix_ns, position, park_after, slack_channel, slack_ts, kind, content, source, slack_reply`, conversationID, id))
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
	)

	if err := scanner.Scan(&item.ID, &item.ConversationID, &item.Message, &item.Principal, &stashAt, &item.Position, &item.ParkAfter, &item.SlackChannel, &item.SlackTS, &item.Kind, &content, &item.Source, &reply); err != nil {
		return protocol.ThreadQueueItem{}, fmt.Errorf("scan thread queue item: %w", err)
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

// UpsertActiveTurn records a RocketCode active-turn restart handoff checkpoint with source metadata.
func (s *SessionService) UpsertActiveTurn(ctx context.Context, checkpoint *harness.ActiveTurnCheckpoint, sourceMetadata map[string]string) error {
	now := time.Now().UTC()

	if checkpoint == nil {
		return errors.New("active turn checkpoint is required")
	}

	checkpointState := *checkpoint
	checkpointState.TurnID = strings.TrimSpace(checkpointState.TurnID)
	checkpointState.ConversationKey = strings.TrimSpace(checkpointState.ConversationKey)
	checkpointState.Agent = strings.TrimSpace(checkpointState.Agent)
	checkpointState.Model = strings.TrimSpace(checkpointState.Model)
	checkpointState.DisplayModel = strings.TrimSpace(checkpointState.DisplayModel)
	checkpointState.ResponseID = strings.TrimSpace(checkpointState.ResponseID)

	if checkpointState.TurnID == "" {
		return errors.New("active turn ID is required")
	}

	if checkpointState.ConversationKey == "" {
		return errors.New("active turn conversation ID is required")
	}

	metadata, err := marshalActiveTurnJSON(sourceMetadata)
	if err != nil {
		return fmt.Errorf("marshal active turn source metadata: %w", err)
	}

	replayInput, err := marshalActiveTurnJSON(checkpointState.ReplayInput)
	if err != nil {
		return fmt.Errorf("marshal active turn replay input: %w", err)
	}

	outputTrace, err := marshalActiveTurnJSON(checkpointState.OutputTrace)
	if err != nil {
		return fmt.Errorf("marshal active turn output trace: %w", err)
	}

	tokenUsage, err := marshalActiveTurnJSON(checkpointState.TokenUsage)
	if err != nil {
		return fmt.Errorf("marshal active turn token usage: %w", err)
	}

	openCalls, err := marshalActiveTurnJSON(checkpointState.OpenFunctionCalls)
	if err != nil {
		return fmt.Errorf("marshal active turn open function calls: %w", err)
	}

	completedOutputs, err := marshalActiveTurnJSON(checkpointState.CompletedFunctionOutputs)
	if err != nil {
		return fmt.Errorf("marshal active turn completed function outputs: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `INSERT INTO active_turns (id, conversation_id, agent, model, display_model, replay_input_json, output_trace_json, token_usage_json, response_id, open_function_calls_json, completed_function_outputs_json, restart_notice_json, source_metadata_json, created_at_unix_ns, updated_at_unix_ns) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) ON CONFLICT(id) DO UPDATE SET conversation_id = excluded.conversation_id, agent = excluded.agent, model = excluded.model, display_model = excluded.display_model, replay_input_json = excluded.replay_input_json, output_trace_json = excluded.output_trace_json, token_usage_json = excluded.token_usage_json, response_id = excluded.response_id, open_function_calls_json = excluded.open_function_calls_json, completed_function_outputs_json = excluded.completed_function_outputs_json, restart_notice_json = excluded.restart_notice_json, source_metadata_json = excluded.source_metadata_json, updated_at_unix_ns = excluded.updated_at_unix_ns`, checkpointState.TurnID, checkpointState.ConversationKey, checkpointState.Agent, checkpointState.Model, checkpointState.DisplayModel, replayInput, outputTrace, tokenUsage, checkpointState.ResponseID, openCalls, completedOutputs, "", metadata, timeUnixNano(now), timeUnixNano(now))
	if err != nil {
		return fmt.Errorf("upsert active turn: %w", err)
	}

	return nil
}

// ClearActiveTurn removes an active root-turn checkpoint.
func (s *SessionService) ClearActiveTurn(ctx context.Context, turnID string) error {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return errors.New("active turn ID is required")
	}

	if _, err := s.db.ExecContext(ctx, `DELETE FROM active_turns WHERE id = $1`, turnID); err != nil {
		return fmt.Errorf("clear active turn: %w", err)
	}

	return nil
}

// RecoverableActiveTurns returns remaining active-turn handoff rows for startup recovery.
func (s *SessionService) RecoverableActiveTurns(ctx context.Context) ([]ActiveTurnState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, conversation_id, agent, model, display_model, replay_input_json, output_trace_json, token_usage_json, response_id, open_function_calls_json, completed_function_outputs_json, restart_notice_json, source_metadata_json, created_at_unix_ns, updated_at_unix_ns, pending_steers_json FROM active_turns ORDER BY conversation_id, updated_at_unix_ns DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("query recoverable active turns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var (
		turns    []ActiveTurnState
		corrupts []activeTurnCorruptError
	)

	for rows.Next() {
		turn, err := scanActiveTurn(rows)
		if err != nil {
			if errCorrupt, ok := errors.AsType[activeTurnCorruptError](err); ok {
				corrupts = append(corrupts, errCorrupt)

				continue
			}

			return nil, err
		}

		turns = append(turns, turn)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read recoverable active turns: %w", err)
	}

	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close recoverable active turns rows: %w", err)
	}

	for _, errCorrupt := range corrupts {
		_, err := s.db.ExecContext(ctx, `DELETE FROM active_turns WHERE id = $1`, errCorrupt.turnID)
		if err != nil {
			return nil, fmt.Errorf("delete corrupt active turn: %w", err)
		}
	}

	return turns, nil
}

func marshalActiveTurnJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal active turn JSON: %w", err)
	}

	return string(data), nil
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

type activeTurnCorruptError struct {
	turnID         string
	conversationID string
	field          string
	err            error
}

func (e activeTurnCorruptError) Error() string {
	return fmt.Sprintf("active turn %q conversation %q has corrupt %s: %v", e.turnID, e.conversationID, e.field, e.err)
}

func (e activeTurnCorruptError) Unwrap() error {
	return e.err
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

// SetPendingSteers copies uninjected Slack Steers onto the conversation's active-turn row.
func (s *SessionService) SetPendingSteers(conversationID string, steers []protocol.PendingSteer) error {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return errors.New("pending steers conversation ID is required")
	}

	if steers == nil {
		steers = []protocol.PendingSteer{}
	}

	payload, err := marshalActiveTurnJSON(steers)
	if err != nil {
		return fmt.Errorf("marshal pending steers: %w", err)
	}

	if _, err := s.db.ExecContext(context.Background(), `UPDATE active_turns SET pending_steers_json = $1 WHERE conversation_id = $2`, payload, conversationID); err != nil {
		return fmt.Errorf("persist pending steers: %w", err)
	}

	return nil
}

func scanActiveTurn(scanner rowScanner) (ActiveTurnState, error) {
	var (
		turn              ActiveTurnState
		replayInput       string
		outputTrace       string
		tokenUsage        string
		openCalls         string
		completedOutputs  string
		restartNotice     string
		sourceMetadata    string
		createdAtUnixNano int64
		updatedAtUnixNano int64
		pendingSteers     string
	)

	if err := scanner.Scan(&turn.Checkpoint.TurnID, &turn.Checkpoint.ConversationKey, &turn.Checkpoint.Agent, &turn.Checkpoint.Model, &turn.Checkpoint.DisplayModel, &replayInput, &outputTrace, &tokenUsage, &turn.Checkpoint.ResponseID, &openCalls, &completedOutputs, &restartNotice, &sourceMetadata, &createdAtUnixNano, &updatedAtUnixNano, &pendingSteers); err != nil {
		return ActiveTurnState{}, fmt.Errorf("scan active turn: %w", err)
	}

	turn.CreatedAt = timeFromUnixNano(createdAtUnixNano)
	turn.UpdatedAt = timeFromUnixNano(updatedAtUnixNano)

	sourceMetadata = cmp.Or(strings.TrimSpace(sourceMetadata), "{}")
	pendingSteers = cmp.Or(strings.TrimSpace(pendingSteers), "[]")

	for _, field := range []struct {
		raw  string
		dest any
		name string
	}{
		{replayInput, &turn.Checkpoint.ReplayInput, "replay input"},
		{outputTrace, &turn.Checkpoint.OutputTrace, "output trace"},
		{tokenUsage, &turn.Checkpoint.TokenUsage, "token usage"},
		{openCalls, &turn.Checkpoint.OpenFunctionCalls, "open function calls"},
		{completedOutputs, &turn.Checkpoint.CompletedFunctionOutputs, "completed function outputs"},
		{sourceMetadata, &turn.SourceMetadata, "source metadata"},
		{pendingSteers, &turn.PendingSteers, "pending steers"},
	} {
		if err := json.Unmarshal([]byte(field.raw), field.dest); err != nil {
			return ActiveTurnState{}, activeTurnCorruptError{turnID: turn.Checkpoint.TurnID, conversationID: turn.Checkpoint.ConversationKey, field: field.name, err: err}
		}
	}

	if turn.SourceMetadata == nil {
		turn.SourceMetadata = map[string]string{}
	}

	if strings.TrimSpace(restartNotice) != "" {
		turn.SourceMetadata["restart_notice_json"] = restartNotice
	}

	return turn, nil
}
