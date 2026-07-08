package harnessbridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	harness "github.com/Rocketable/platform/internal/rocketcode"
)

type stateDAO struct {
	db stateStoreDB
}

func (d stateDAO) upsertThread(ctx context.Context, conversationID string, thread ThreadState) error {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return errors.New("thread conversation ID is required")
	}

	_, err := d.db.ExecContext(ctx, `INSERT INTO managed_conversations (conversation_id, agent, seeded_from_response, created_by) VALUES (?, ?, ?, ?) ON CONFLICT(conversation_id) DO UPDATE SET agent = excluded.agent, seeded_from_response = excluded.seeded_from_response, created_by = excluded.created_by`, conversationID, strings.TrimSpace(thread.Agent), strings.TrimSpace(thread.SeededFromResponse), strings.TrimSpace(string(thread.CreatedBy)))
	if err != nil {
		return fmt.Errorf("upsert managed conversation: %w", err)
	}

	return nil
}

func (d stateDAO) upsertThreadAgent(ctx context.Context, conversationID, agent string) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO managed_conversations (conversation_id, agent, seeded_from_response, created_by) VALUES (?, ?, '', '') ON CONFLICT(conversation_id) DO UPDATE SET agent = excluded.agent`, strings.TrimSpace(conversationID), strings.TrimSpace(agent))
	if err != nil {
		return fmt.Errorf("upsert managed conversation agent: %w", err)
	}

	return nil
}

func (d stateDAO) markThreadCreatedBy(ctx context.Context, conversationID string, createdBy ThreadCreator) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO managed_conversations (conversation_id, agent, seeded_from_response, created_by) VALUES (?, '', '', ?) ON CONFLICT(conversation_id) DO UPDATE SET created_by = excluded.created_by`, strings.TrimSpace(conversationID), strings.TrimSpace(string(createdBy)))
	if err != nil {
		return fmt.Errorf("mark managed conversation creator: %w", err)
	}

	return nil
}

func (d stateDAO) markThreadSeeded(ctx context.Context, conversationID, seedKey string) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO managed_conversations (conversation_id, agent, seeded_from_response, created_by) VALUES (?, ?, ?, '') ON CONFLICT(conversation_id) DO UPDATE SET seeded_from_response = excluded.seeded_from_response, agent = CASE WHEN trim(managed_conversations.agent) = '' THEN excluded.agent ELSE managed_conversations.agent END`, strings.TrimSpace(conversationID), mainConversationID, strings.TrimSpace(seedKey))
	if err != nil {
		return fmt.Errorf("mark managed conversation seeded: %w", err)
	}

	return nil
}

func (d stateDAO) thread(ctx context.Context, conversationID string) (ThreadState, bool, error) {
	var (
		thread    ThreadState
		createdBy string
	)

	err := d.db.QueryRowContext(ctx, `SELECT agent, seeded_from_response, created_by FROM managed_conversations WHERE conversation_id = ?`, strings.TrimSpace(conversationID)).Scan(&thread.Agent, &thread.SeededFromResponse, &createdBy)
	if err == sql.ErrNoRows {
		return ThreadState{}, false, nil
	}

	if err != nil {
		return ThreadState{}, false, fmt.Errorf("read managed conversation: %w", err)
	}

	thread.CreatedBy = ThreadCreator(createdBy)

	return thread, true, nil
}

func (d stateDAO) setThreadAgent(ctx context.Context, conversationID, agent string) (bool, error) {
	result, err := d.db.ExecContext(ctx, `UPDATE managed_conversations SET agent = ? WHERE conversation_id = ?`, strings.TrimSpace(agent), strings.TrimSpace(conversationID))
	if err != nil {
		return false, fmt.Errorf("update managed conversation agent: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count managed conversation agent update: %w", err)
	}

	return rows > 0, nil
}

func (d stateDAO) threadForSeed(ctx context.Context, seedConversationID string) (conversationID string, thread ThreadState, ok bool, err error) {
	var createdBy string

	err = d.db.QueryRowContext(ctx, `SELECT conversation_id, agent, seeded_from_response, created_by FROM managed_conversations WHERE seeded_from_response = ? ORDER BY conversation_id LIMIT 1`, strings.TrimSpace(seedConversationID)).Scan(&conversationID, &thread.Agent, &thread.SeededFromResponse, &createdBy)
	if err == sql.ErrNoRows {
		return "", ThreadState{}, false, nil
	}

	if err != nil {
		return "", ThreadState{}, false, fmt.Errorf("read managed conversation by seed: %w", err)
	}

	thread.CreatedBy = ThreadCreator(createdBy)

	return conversationID, thread, true, nil
}

func (d stateDAO) upsertResponseCheckpoint(ctx context.Context, key string, checkpoint ResponseCheckpointState) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO response_checkpoints (checkpoint_key, source_conversation_id, session_entry_id, response_id, model, assistant_text) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(checkpoint_key) DO UPDATE SET source_conversation_id = excluded.source_conversation_id, session_entry_id = excluded.session_entry_id, response_id = excluded.response_id, model = excluded.model, assistant_text = excluded.assistant_text`, strings.TrimSpace(key), strings.TrimSpace(checkpoint.ConversationID), checkpoint.SessionEntryID, strings.TrimSpace(checkpoint.ResponseID), strings.TrimSpace(checkpoint.Model), checkpoint.AssistantText)
	if err != nil {
		return fmt.Errorf("upsert response checkpoint: %w", err)
	}

	return nil
}

func (d stateDAO) responseCheckpoint(ctx context.Context, key string) (ResponseCheckpointState, bool, error) {
	var checkpoint ResponseCheckpointState

	err := d.db.QueryRowContext(ctx, `SELECT source_conversation_id, session_entry_id, response_id, model, assistant_text FROM response_checkpoints WHERE checkpoint_key = ?`, strings.TrimSpace(key)).Scan(&checkpoint.ConversationID, &checkpoint.SessionEntryID, &checkpoint.ResponseID, &checkpoint.Model, &checkpoint.AssistantText)
	if err == sql.ErrNoRows {
		return ResponseCheckpointState{}, false, nil
	}

	if err != nil {
		return ResponseCheckpointState{}, false, fmt.Errorf("read response checkpoint: %w", err)
	}

	return checkpoint, true, nil
}

func (d stateDAO) upsertExternalMCPSession(ctx context.Context, externalConversationID string, session ExternalMCPSessionState) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO external_mcp_sessions (external_conversation_id, conversation_id, agent) VALUES (?, ?, ?) ON CONFLICT(external_conversation_id) DO UPDATE SET conversation_id = excluded.conversation_id, agent = excluded.agent`, strings.TrimSpace(externalConversationID), strings.TrimSpace(session.ConversationID), strings.TrimSpace(session.Agent))
	if err != nil {
		return fmt.Errorf("upsert external MCP session: %w", err)
	}

	return nil
}

func (d stateDAO) externalMCPSession(ctx context.Context, externalConversationID string) (ExternalMCPSessionState, bool, error) {
	var session ExternalMCPSessionState

	err := d.db.QueryRowContext(ctx, `SELECT agent, conversation_id FROM external_mcp_sessions WHERE external_conversation_id = ?`, strings.TrimSpace(externalConversationID)).Scan(&session.Agent, &session.ConversationID)
	if err == sql.ErrNoRows {
		return ExternalMCPSessionState{}, false, nil
	}

	if err != nil {
		return ExternalMCPSessionState{}, false, fmt.Errorf("read external MCP session: %w", err)
	}

	return session, true, nil
}

func (d stateDAO) upsertGoal(ctx context.Context, conversationID string, goal *GoalState) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO conversation_goals (conversation_id, objective, check_script, max_turns, turns_used, status, note, created_at_unix_ns, updated_at_unix_ns) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(conversation_id) DO UPDATE SET objective = excluded.objective, check_script = excluded.check_script, max_turns = excluded.max_turns, turns_used = excluded.turns_used, status = excluded.status, note = excluded.note, created_at_unix_ns = excluded.created_at_unix_ns, updated_at_unix_ns = excluded.updated_at_unix_ns`, strings.TrimSpace(conversationID), strings.TrimSpace(goal.Objective), strings.TrimSpace(goal.CheckScript), goal.MaxTurns, goal.TurnsUsed, strings.TrimSpace(goal.Status), strings.TrimSpace(goal.Note), timeUnixNano(goal.CreatedAt), timeUnixNano(goal.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert goal: %w", err)
	}

	return nil
}

func (d stateDAO) beginGoal(ctx context.Context, conversationID string, goal *GoalState) (bool, error) {
	result, err := d.db.ExecContext(ctx, `INSERT INTO conversation_goals (conversation_id, objective, check_script, max_turns, turns_used, status, note, created_at_unix_ns, updated_at_unix_ns) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(conversation_id) DO UPDATE SET objective = excluded.objective, check_script = excluded.check_script, max_turns = excluded.max_turns, turns_used = excluded.turns_used, status = excluded.status, note = excluded.note, created_at_unix_ns = excluded.created_at_unix_ns, updated_at_unix_ns = excluded.updated_at_unix_ns WHERE conversation_goals.status NOT IN ('', ?)`, strings.TrimSpace(conversationID), strings.TrimSpace(goal.Objective), strings.TrimSpace(goal.CheckScript), goal.MaxTurns, goal.TurnsUsed, strings.TrimSpace(goal.Status), strings.TrimSpace(goal.Note), timeUnixNano(goal.CreatedAt), timeUnixNano(goal.UpdatedAt), GoalStatusActive)
	if err != nil {
		return false, fmt.Errorf("begin goal: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count goal start: %w", err)
	}

	return rows > 0, nil
}

func (d stateDAO) accountGoalTurn(ctx context.Context, conversationID string, now time.Time) (bool, error) {
	result, err := d.db.ExecContext(ctx, `UPDATE conversation_goals SET turns_used = turns_used + 1, status = CASE WHEN max_turns > 0 AND turns_used + 1 >= max_turns THEN ? ELSE ? END, updated_at_unix_ns = ? WHERE conversation_id = ? AND (status = '' OR status = ?)`, GoalStatusBudgetExhausted, GoalStatusActive, timeUnixNano(now), strings.TrimSpace(conversationID), GoalStatusActive)
	if err != nil {
		return false, fmt.Errorf("account goal turn: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count goal turn accounting: %w", err)
	}

	return rows > 0, nil
}

func (d stateDAO) setActiveGoalStatus(ctx context.Context, conversationID, status, note string, now time.Time) (bool, error) {
	result, err := d.db.ExecContext(ctx, `UPDATE conversation_goals SET status = ?, note = ?, updated_at_unix_ns = ? WHERE conversation_id = ? AND (status = '' OR status = ?)`, strings.TrimSpace(status), strings.TrimSpace(note), timeUnixNano(now), strings.TrimSpace(conversationID), GoalStatusActive)
	if err != nil {
		return false, fmt.Errorf("set active goal status: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count active goal status update: %w", err)
	}

	return rows > 0, nil
}

func (d stateDAO) goal(ctx context.Context, conversationID string) (GoalState, bool, error) {
	var (
		goal                 GoalState
		createdAt, updatedAt int64
	)

	err := d.db.QueryRowContext(ctx, `SELECT objective, check_script, max_turns, turns_used, status, note, created_at_unix_ns, updated_at_unix_ns FROM conversation_goals WHERE conversation_id = ?`, strings.TrimSpace(conversationID)).Scan(&goal.Objective, &goal.CheckScript, &goal.MaxTurns, &goal.TurnsUsed, &goal.Status, &goal.Note, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return GoalState{}, false, nil
	}

	if err != nil {
		return GoalState{}, false, fmt.Errorf("read goal: %w", err)
	}

	goal.CreatedAt = timeFromUnixNano(createdAt)

	goal.UpdatedAt = timeFromUnixNano(updatedAt)
	if strings.TrimSpace(goal.Status) == "" {
		goal.Status = GoalStatusActive
	}

	return goal, true, nil
}

func (d stateDAO) activeGoals(ctx context.Context) (map[string]GoalState, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT conversation_id, objective, check_script, max_turns, turns_used, status, note, created_at_unix_ns, updated_at_unix_ns FROM conversation_goals WHERE status = '' OR status = ? ORDER BY conversation_id`, GoalStatusActive)
	if err != nil {
		return nil, fmt.Errorf("query active goals: %w", err)
	}
	defer func() { _ = rows.Close() }()

	goals := map[string]GoalState{}

	for rows.Next() {
		var (
			conversationID       string
			goal                 GoalState
			createdAt, updatedAt int64
		)
		if err := rows.Scan(&conversationID, &goal.Objective, &goal.CheckScript, &goal.MaxTurns, &goal.TurnsUsed, &goal.Status, &goal.Note, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan active goal: %w", err)
		}

		goal.CreatedAt = timeFromUnixNano(createdAt)

		goal.UpdatedAt = timeFromUnixNano(updatedAt)
		if strings.TrimSpace(goal.Status) == "" {
			goal.Status = GoalStatusActive
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

func (d stateDAO) putScheduledMessage(ctx context.Context, id string, message *ScheduledMessageState) error {
	_, err := d.db.ExecContext(ctx, `INSERT INTO scheduled_messages (scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring, interval_ns) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(scheduled_message_id) DO UPDATE SET conversation_id = excluded.conversation_id, agent = excluded.agent, message = excluded.message, due_at_unix_ns = excluded.due_at_unix_ns, recurring = excluded.recurring, interval_ns = excluded.interval_ns`, strings.TrimSpace(id), strings.TrimSpace(message.ConversationID), strings.TrimSpace(message.Agent), message.Message, timeUnixNano(message.DueAt), boolInt(message.Recurring), int64(message.Interval))
	if err != nil {
		return fmt.Errorf("put scheduled message: %w", err)
	}

	return nil
}

func (d stateDAO) scheduledMessages(ctx context.Context, conversationID string) (map[string]ScheduledMessageState, error) {
	query := `SELECT scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring, interval_ns FROM scheduled_messages ORDER BY scheduled_message_id`
	args := []any{}

	if strings.TrimSpace(conversationID) != "" {
		query = `SELECT scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring, interval_ns FROM scheduled_messages WHERE conversation_id = ? ORDER BY scheduled_message_id`

		args = append(args, strings.TrimSpace(conversationID))
	}

	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query scheduled messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	messages := map[string]ScheduledMessageState{}

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

func (d stateDAO) deleteScheduledMessage(ctx context.Context, id string) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM scheduled_messages WHERE scheduled_message_id = ?`, strings.TrimSpace(id)); err != nil {
		return fmt.Errorf("delete scheduled message: %w", err)
	}

	return nil
}

func (d stateDAO) resetScheduledMessages(ctx context.Context, conversationID string) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM scheduled_messages WHERE conversation_id = ?`, strings.TrimSpace(conversationID)); err != nil {
		return fmt.Errorf("reset scheduled messages: %w", err)
	}

	return nil
}

func (d stateDAO) markRestartRequester(ctx context.Context, conversationID string) error {
	if _, err := d.db.ExecContext(ctx, `INSERT INTO pending_restart_notifications (conversation_id) VALUES (?) ON CONFLICT(conversation_id) DO NOTHING`, strings.TrimSpace(conversationID)); err != nil {
		return fmt.Errorf("mark restart requester: %w", err)
	}

	return nil
}

func (d stateDAO) upsertActiveTurn(ctx context.Context, checkpoint *harness.ActiveTurnCheckpoint, now time.Time) error {
	turn, err := activeTurnStateFromCheckpoint(checkpoint, now)
	if err != nil {
		return err
	}

	return d.upsertActiveTurnState(ctx, &turn)
}

func (d stateDAO) upsertActiveTurnWithSourceMetadata(ctx context.Context, checkpoint *harness.ActiveTurnCheckpoint, sourceMetadata map[string]string, now time.Time) error {
	turn, err := activeTurnStateFromCheckpoint(checkpoint, now)
	if err != nil {
		return err
	}

	turn.SourceMetadata = sourceMetadata

	return d.upsertActiveTurnState(ctx, &turn)
}

func (d stateDAO) upsertActiveTurnState(ctx context.Context, turn *ActiveTurnState) error {
	checkpointState := turn.Checkpoint

	metadata, err := marshalActiveTurnJSON(turn.SourceMetadata)
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

	_, err = d.db.ExecContext(ctx, `INSERT INTO active_turns (id, conversation_id, agent, model, display_model, replay_input_json, output_trace_json, token_usage_json, response_id, open_function_calls_json, completed_function_outputs_json, restart_notice_json, source_metadata_json, created_at_unix_ns, updated_at_unix_ns) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET conversation_id = excluded.conversation_id, agent = excluded.agent, model = excluded.model, display_model = excluded.display_model, replay_input_json = excluded.replay_input_json, output_trace_json = excluded.output_trace_json, token_usage_json = excluded.token_usage_json, response_id = excluded.response_id, open_function_calls_json = excluded.open_function_calls_json, completed_function_outputs_json = excluded.completed_function_outputs_json, restart_notice_json = excluded.restart_notice_json, source_metadata_json = excluded.source_metadata_json, updated_at_unix_ns = excluded.updated_at_unix_ns`, checkpointState.TurnID, checkpointState.ConversationKey, checkpointState.Agent, checkpointState.Model, checkpointState.DisplayModel, replayInput, outputTrace, tokenUsage, checkpointState.ResponseID, openCalls, completedOutputs, "", metadata, timeUnixNano(turn.CreatedAt), timeUnixNano(turn.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert active turn: %w", err)
	}

	return nil
}

func (d stateDAO) clearActiveTurn(ctx context.Context, turnID string) error {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return errors.New("active turn ID is required")
	}

	if _, err := d.db.ExecContext(ctx, `DELETE FROM active_turns WHERE id = ?`, turnID); err != nil {
		return fmt.Errorf("clear active turn: %w", err)
	}

	return nil
}

func (d stateDAO) recoverableActiveTurns(ctx context.Context) ([]ActiveTurnState, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, conversation_id, agent, model, display_model, replay_input_json, output_trace_json, token_usage_json, response_id, open_function_calls_json, completed_function_outputs_json, restart_notice_json, source_metadata_json, created_at_unix_ns, updated_at_unix_ns FROM active_turns ORDER BY conversation_id, updated_at_unix_ns DESC, id`)
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
		_, err := d.db.ExecContext(ctx, `DELETE FROM active_turns WHERE id = ?`, errCorrupt.turnID)
		if err != nil {
			return nil, fmt.Errorf("delete corrupt active turn: %w", err)
		}
	}

	return turns, nil
}

func (d stateDAO) activeTurn(ctx context.Context, turnID string) (ActiveTurnState, bool, error) {
	row := d.db.QueryRowContext(ctx, `SELECT id, conversation_id, agent, model, display_model, replay_input_json, output_trace_json, token_usage_json, response_id, open_function_calls_json, completed_function_outputs_json, restart_notice_json, source_metadata_json, created_at_unix_ns, updated_at_unix_ns FROM active_turns WHERE id = ?`, strings.TrimSpace(turnID))

	turn, err := scanActiveTurn(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ActiveTurnState{}, false, nil
	}

	if err != nil {
		return ActiveTurnState{}, false, fmt.Errorf("read active turn: %w", err)
	}

	return turn, true, nil
}

func activeTurnStateFromCheckpoint(checkpoint *harness.ActiveTurnCheckpoint, now time.Time) (ActiveTurnState, error) {
	if checkpoint == nil {
		return ActiveTurnState{}, errors.New("active turn checkpoint is required")
	}

	checkpointCopy := *checkpoint
	checkpointCopy.TurnID = strings.TrimSpace(checkpointCopy.TurnID)
	checkpointCopy.ConversationKey = strings.TrimSpace(checkpointCopy.ConversationKey)
	checkpointCopy.Agent = strings.TrimSpace(checkpointCopy.Agent)
	checkpointCopy.Model = strings.TrimSpace(checkpointCopy.Model)
	checkpointCopy.DisplayModel = strings.TrimSpace(checkpointCopy.DisplayModel)
	checkpointCopy.ResponseID = strings.TrimSpace(checkpointCopy.ResponseID)

	turn := ActiveTurnState{Checkpoint: checkpointCopy, SourceMetadata: map[string]string{}, CreatedAt: now, UpdatedAt: now}

	if turn.Checkpoint.TurnID == "" {
		return ActiveTurnState{}, errors.New("active turn ID is required")
	}

	if turn.Checkpoint.ConversationKey == "" {
		return ActiveTurnState{}, errors.New("active turn conversation ID is required")
	}

	return turn, nil
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

func scanScheduledMessage(scanner rowScanner) (string, ScheduledMessageState, error) {
	var (
		id              string
		message         ScheduledMessageState
		dueAt, interval int64
		recurring       int
	)
	if err := scanner.Scan(&id, &message.ConversationID, &message.Agent, &message.Message, &dueAt, &recurring, &interval); err != nil {
		return "", ScheduledMessageState{}, fmt.Errorf("scan scheduled message: %w", err)
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
	)

	if err := scanner.Scan(&turn.Checkpoint.TurnID, &turn.Checkpoint.ConversationKey, &turn.Checkpoint.Agent, &turn.Checkpoint.Model, &turn.Checkpoint.DisplayModel, &replayInput, &outputTrace, &tokenUsage, &turn.Checkpoint.ResponseID, &openCalls, &completedOutputs, &restartNotice, &sourceMetadata, &createdAtUnixNano, &updatedAtUnixNano); err != nil {
		return ActiveTurnState{}, fmt.Errorf("scan active turn: %w", err)
	}

	turn.CreatedAt = timeFromUnixNano(createdAtUnixNano)
	turn.UpdatedAt = timeFromUnixNano(updatedAtUnixNano)

	if err := json.Unmarshal([]byte(replayInput), &turn.Checkpoint.ReplayInput); err != nil {
		return ActiveTurnState{}, activeTurnCorruptError{turnID: turn.Checkpoint.TurnID, conversationID: turn.Checkpoint.ConversationKey, field: "replay input", err: err}
	}

	if err := json.Unmarshal([]byte(outputTrace), &turn.Checkpoint.OutputTrace); err != nil {
		return ActiveTurnState{}, activeTurnCorruptError{turnID: turn.Checkpoint.TurnID, conversationID: turn.Checkpoint.ConversationKey, field: "output trace", err: err}
	}

	if err := json.Unmarshal([]byte(tokenUsage), &turn.Checkpoint.TokenUsage); err != nil {
		return ActiveTurnState{}, activeTurnCorruptError{turnID: turn.Checkpoint.TurnID, conversationID: turn.Checkpoint.ConversationKey, field: "token usage", err: err}
	}

	if err := json.Unmarshal([]byte(openCalls), &turn.Checkpoint.OpenFunctionCalls); err != nil {
		return ActiveTurnState{}, activeTurnCorruptError{turnID: turn.Checkpoint.TurnID, conversationID: turn.Checkpoint.ConversationKey, field: "open function calls", err: err}
	}

	if err := json.Unmarshal([]byte(completedOutputs), &turn.Checkpoint.CompletedFunctionOutputs); err != nil {
		return ActiveTurnState{}, activeTurnCorruptError{turnID: turn.Checkpoint.TurnID, conversationID: turn.Checkpoint.ConversationKey, field: "completed function outputs", err: err}
	}

	if strings.TrimSpace(sourceMetadata) == "" {
		sourceMetadata = "{}"
	}

	if err := json.Unmarshal([]byte(sourceMetadata), &turn.SourceMetadata); err != nil {
		return ActiveTurnState{}, activeTurnCorruptError{turnID: turn.Checkpoint.TurnID, conversationID: turn.Checkpoint.ConversationKey, field: "source metadata", err: err}
	}

	if turn.SourceMetadata == nil {
		turn.SourceMetadata = map[string]string{}
	}

	if strings.TrimSpace(restartNotice) != "" {
		turn.SourceMetadata["restart_notice_json"] = restartNotice
	}

	return turn, nil
}
