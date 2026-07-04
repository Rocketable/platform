package harnessbridge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
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
