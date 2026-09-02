package backend

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"cirello.io/pglock"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	harness "github.com/Rocketable/platform/internal/rocketcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const (
	restartNotificationDeveloperMessage = "The rocketclaw server has been restarted."
	runLockName                         = "rocketclaw-run"
	runLockTable                        = "rocketclaw_locks"
)

var errRunLocked = errors.New("rocketclaw is already running against this database")

// GoalStatusActive and related constants are persisted goal-loop statuses.
const (
	GoalStatusActive          = "active"
	GoalStatusProgress        = "progress"
	GoalStatusComplete        = "complete"
	GoalStatusBlocked         = "blocked"
	GoalStatusStopped         = "stopped"
	GoalStatusBudgetExhausted = "budget_exhausted"
)

// ThreadState is the persisted state for one text-thread bridge.
type ThreadState struct {
	Agent     string        `json:"agent,omitempty"`
	CreatedBy ThreadCreator `json:"created_by,omitempty"`
}

// ThreadCreator records which subsystem created a managed text conversation.
type ThreadCreator string

// ThreadCreatedByCron marks managed conversations created for cron output.
const ThreadCreatedByCron ThreadCreator = "cron"

// ExternalMCPSessionState binds an external MCP conversation ID to private and managed sessions.
type ExternalMCPSessionState struct {
	Agent                 string `json:"agent,omitempty"`
	PrivateConversationID string `json:"private_conversation_id,omitempty"`
	ManagedConversationID string `json:"managed_conversation_id,omitempty"`
	SlackChannel          string `json:"slack_channel,omitempty"`
}

// ActiveTurnState records one durable RocketCode active-turn checkpoint.
type ActiveTurnState struct {
	Checkpoint     harness.ActiveTurnCheckpoint `json:"checkpoint"`
	SourceMetadata map[string]string            `json:"source_metadata,omitempty"`
	PendingSteers  []protocol.PendingSteer      `json:"pending_steers,omitempty"`
	CreatedAt      time.Time                    `json:"created_at,omitzero"`
	UpdatedAt      time.Time                    `json:"updated_at,omitzero"`
}

// CronScheduleState records one observed scheduled cron trigger.
type CronScheduleState struct {
	ScheduleID   string
	RelativePath string
	NextDue      time.Time
}

// CronScheduleRun is a claimed scheduled cron run.
type CronScheduleRun struct {
	ScheduleID   string
	RelativePath string
	DueAt        time.Time
}

// GoalState records one active or terminal managed-thread goal loop.
type GoalState struct {
	Objective            string    `json:"objective,omitempty"`
	CheckScript          string    `json:"check_script,omitempty"`
	MaxTurns             int       `json:"max_turns,omitempty"`
	TurnsUsed            int       `json:"turns_used,omitempty"`
	Status               string    `json:"status,omitempty"`
	Note                 string    `json:"note,omitempty"`
	SlackRecipientTeamID string    `json:"slack_recipient_team_id,omitempty"`
	SlackRecipientUserID string    `json:"slack_recipient_user_id,omitempty"`
	CreatedAt            time.Time `json:"created_at,omitzero"`
	UpdatedAt            time.Time `json:"updated_at,omitzero"`
}

type sessionStore struct {
	conversationID, managedConversationID string
	managedReplayPrefix                   []json.RawMessage
	service                               *SessionService
}

// SessionService owns runtime PostgreSQL session and state access inside one rocketclaw process.
type SessionService struct {
	db *sql.DB

	turnGatesMu sync.Mutex
	turnGates   map[string]*sessionTurnGate
	waitersMu   sync.Mutex
	waiters     map[string]*protocol.InboundMessage
}

type sessionTurnGate struct {
	token       chan struct{}
	reserved    chan struct{}
	reservedFor string
	refs        int
}

// SessionListOptions bounds read-only session summary inspection.
type SessionListOptions struct {
	Since, Until time.Time
	Limit        int
}

// ObservedSessionEntry is one stored rocketcode entry with its row ID.
type ObservedSessionEntry struct {
	ID    int64
	Entry harness.SessionEntry
}

// PruneStateStats reports how much stale persisted state was removed.
type PruneStateStats struct {
	Threads, ExternalMCPSessions int
	SessionRows                  int64
}

type stateStoreDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func newSessionStore(conversationID string, service *SessionService) sessionStore {
	return sessionStore{conversationID: strings.TrimSpace(conversationID), service: service}
}

// NewSessionServiceIn starts a runtime-owned PostgreSQL session service.
func NewSessionServiceIn(databaseURL string, logger *slog.Logger) (*SessionService, error) {
	db, err := openSessionDB(context.Background(), databaseURL, logger)
	if err != nil {
		return nil, err
	}

	return &SessionService{db: db, turnGates: map[string]*sessionTurnGate{}, waiters: map[string]*protocol.InboundMessage{}}, nil
}

// UpsertThread records or updates a text-thread bridge entry.
func (s *SessionService) UpsertThread(conversationID string, thread ThreadState) error {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return errors.New("thread conversation ID is required")
	}

	thread.Agent = strings.TrimSpace(thread.Agent)
	thread.CreatedBy = ThreadCreator(strings.TrimSpace(string(thread.CreatedBy)))

	return stateDAO{db: s.db}.upsertThread(context.Background(), conversationID, thread)
}

// BeginGoal records a new active goal for a managed conversation.
func (s *SessionService) BeginGoal(conversationID, objective, checkScript string, maxTurns int, recipientTeamID, recipientUserID string) error {
	conversationID = strings.TrimSpace(conversationID)
	objective = strings.TrimSpace(objective)
	checkScript = strings.TrimSpace(checkScript)
	recipientTeamID = strings.TrimSpace(recipientTeamID)
	recipientUserID = strings.TrimSpace(recipientUserID)

	if conversationID == "" {
		return errors.New("goal conversation ID is required")
	}

	if objective == "" {
		return errors.New("goal objective is required")
	}

	if maxTurns < 0 {
		maxTurns = 0
	}

	now := time.Now().UTC()

	ok, err := stateDAO{db: s.db}.beginGoal(context.Background(), conversationID, &GoalState{Objective: objective, CheckScript: checkScript, MaxTurns: maxTurns, Status: GoalStatusActive, SlackRecipientTeamID: recipientTeamID, SlackRecipientUserID: recipientUserID, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return err
	}

	if !ok {
		return protocol.ErrGoalAlreadyActive
	}

	return nil
}

// Goal returns the persisted goal state for a conversation.
func (s *SessionService) Goal(conversationID string) (GoalState, bool, error) {
	return stateDAO{db: s.db}.goal(context.Background(), conversationID)
}

// ActiveGoals returns persisted active goals keyed by conversation ID.
func (s *SessionService) ActiveGoals() (map[string]GoalState, error) {
	return stateDAO{db: s.db}.activeGoals(context.Background())
}

// AccountGoalTurn increments one active goal turn and applies budget exhaustion.
func (s *SessionService) AccountGoalTurn(conversationID string) (GoalState, bool, error) {
	conversationID = strings.TrimSpace(conversationID)
	ctx := context.Background()

	tx, err := s.beginStateTx(ctx, "goal turn accounting")
	if err != nil {
		return GoalState{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	dao := stateDAO{db: tx}
	if _, err := dao.accountGoalTurn(ctx, conversationID, time.Now().UTC()); err != nil {
		return GoalState{}, false, err
	}

	goal, ok, err := dao.goal(ctx, conversationID)
	if err != nil {
		return GoalState{}, false, err
	}

	if err := tx.Commit(); err != nil {
		return GoalState{}, false, fmt.Errorf("commit goal turn accounting: %w", err)
	}

	return goal, ok, nil
}

// UpdateGoalStatus records a model-controlled goal status update.
func (s *SessionService) UpdateGoalStatus(conversationID, status, note string) (GoalState, error) {
	status = strings.TrimSpace(status)
	switch status {
	case GoalStatusProgress:
		return s.setGoalStatus(conversationID, GoalStatusActive, note)
	case GoalStatusComplete, GoalStatusBlocked:
	default:
		return GoalState{}, fmt.Errorf("unsupported goal status %q", status)
	}

	return s.setGoalStatus(conversationID, status, note)
}

// StopGoal marks an active goal stopped.
func (s *SessionService) StopGoal(conversationID string) error {
	_, err := s.setGoalStatus(conversationID, GoalStatusStopped, "stopped by human")
	return err
}

// UpsertExternalMCPSession records an external MCP conversation ID mapping.
func (s *SessionService) UpsertExternalMCPSession(externalConversationID string, session *ExternalMCPSessionState) error {
	externalConversationID = strings.TrimSpace(externalConversationID)
	if externalConversationID == "" {
		return errors.New("external MCP conversation ID is required")
	}

	session.Agent = strings.TrimSpace(session.Agent)
	session.PrivateConversationID = strings.TrimSpace(session.PrivateConversationID)
	session.ManagedConversationID = strings.TrimSpace(session.ManagedConversationID)
	session.SlackChannel = strings.TrimSpace(session.SlackChannel)

	return stateDAO{db: s.db}.upsertExternalMCPSession(context.Background(), externalConversationID, session)
}

// RegisterExternalMCPConversation atomically persists a managed conversation and its public binding.
func (s *SessionService) RegisterExternalMCPConversation(externalConversationID, managedAgent string, session *ExternalMCPSessionState) error {
	externalConversationID = strings.TrimSpace(externalConversationID)
	managedAgent = strings.TrimSpace(managedAgent)
	session.Agent = strings.TrimSpace(session.Agent)
	session.PrivateConversationID = strings.TrimSpace(session.PrivateConversationID)
	session.ManagedConversationID = strings.TrimSpace(session.ManagedConversationID)
	session.SlackChannel = strings.TrimSpace(session.SlackChannel)
	s.reserveTurnPair(session.ManagedConversationID, session.PrivateConversationID)

	ctx := context.Background()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.completeTurnPairReservation(session.ManagedConversationID, session.PrivateConversationID)
		return fmt.Errorf("begin external MCP conversation registration: %w", err)
	}

	committed := false

	defer func() {
		_ = tx.Rollback()

		if !committed {
			s.completeTurnPairReservation(session.ManagedConversationID, session.PrivateConversationID)
		}
	}()

	if _, err := tx.ExecContext(ctx, `INSERT INTO managed_conversations (conversation_id, agent, created_by) VALUES ($1, $2, '')`, session.ManagedConversationID, managedAgent); err != nil {
		return fmt.Errorf("register external MCP managed conversation: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO external_mcp_sessions (external_conversation_id, private_conversation_id, managed_conversation_id, agent, slack_channel) VALUES ($1, $2, $3, $4, $5)`, externalConversationID, session.PrivateConversationID, session.ManagedConversationID, session.Agent, session.SlackChannel); err != nil {
		return fmt.Errorf("register external MCP binding: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit external MCP conversation registration: %w", err)
	}

	committed = true

	return nil
}

// RemoveExternalMCPConversation removes a failed newly-created conversation and all of its durable state.
func (s *SessionService) RemoveExternalMCPConversation(externalConversationID string) error {
	ctx := context.Background()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin external MCP conversation cleanup: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	session, ok, err := (stateDAO{db: tx}).externalMCPSession(ctx, externalConversationID)
	if err != nil {
		return err
	}

	if !ok {
		return nil
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM external_mcp_sessions WHERE external_conversation_id = $1 AND managed_conversation_id = $2`, strings.TrimSpace(externalConversationID), session.ManagedConversationID); err != nil {
		return fmt.Errorf("clean failed external MCP binding: %w", err)
	}

	for _, conversationID := range []string{session.PrivateConversationID, session.ManagedConversationID} {
		if conversationID == "" {
			continue
		}

		for _, statement := range []string{
			`DELETE FROM session_entries WHERE conversation_id = $1`,
			`DELETE FROM active_turns WHERE conversation_id = $1`,
			`DELETE FROM scheduled_messages WHERE conversation_id = $1`,
			`DELETE FROM thread_queue WHERE conversation_id = $1`,
			`DELETE FROM pending_restart_notifications WHERE conversation_id = $1`,
			`DELETE FROM conversation_goals WHERE conversation_id = $1`,
		} {
			if _, err := tx.ExecContext(ctx, statement, conversationID); err != nil {
				return fmt.Errorf("clean failed external MCP conversation: %w", err)
			}
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM managed_conversations WHERE conversation_id = $1`, session.ManagedConversationID); err != nil {
		return fmt.Errorf("clean failed external MCP managed conversation: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit external MCP conversation cleanup: %w", err)
	}

	s.completeTurnPairReservation(session.ManagedConversationID, session.PrivateConversationID)

	return nil
}

// MarkRestartRequester records that conversationID should see the post-restart notice.
func (s *SessionService) MarkRestartRequester(ctx context.Context, conversationID string) error {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return errors.New("restart requester conversation ID is required")
	}

	return stateDAO{db: s.db}.markRestartRequester(ctx, conversationID)
}

// StartActiveTurn upserts a durable active root-turn checkpoint.
func (s *SessionService) StartActiveTurn(ctx context.Context, checkpoint *harness.ActiveTurnCheckpoint) error {
	return stateDAO{db: s.db}.upsertActiveTurn(ctx, checkpoint, time.Now().UTC())
}

// UpsertActiveTurn records a RocketCode active-turn restart handoff checkpoint with source metadata.
func (s *SessionService) UpsertActiveTurn(ctx context.Context, checkpoint *harness.ActiveTurnCheckpoint, sourceMetadata map[string]string) error {
	return stateDAO{db: s.db}.upsertActiveTurnWithSourceMetadata(ctx, checkpoint, sourceMetadata, time.Now().UTC())
}

// ClearActiveTurn removes an active root-turn checkpoint.
func (s *SessionService) ClearActiveTurn(ctx context.Context, turnID string) error {
	return stateDAO{db: s.db}.clearActiveTurn(ctx, turnID)
}

// SetPendingSteers copies uninjected Slack Steers onto the conversation's active-turn row.
func (s *SessionService) SetPendingSteers(conversationID string, steers []protocol.PendingSteer) error {
	return stateDAO{db: s.db}.setPendingSteers(context.Background(), conversationID, steers)
}

// RecoverableActiveTurns returns remaining active-turn handoff rows for startup recovery.
func (s *SessionService) RecoverableActiveTurns(ctx context.Context) ([]ActiveTurnState, error) {
	return stateDAO{db: s.db}.recoverableActiveTurns(ctx)
}

// ActiveTurn returns a durable active-turn checkpoint.
func (s *SessionService) ActiveTurn(ctx context.Context, turnID string) (ActiveTurnState, bool, error) {
	return stateDAO{db: s.db}.activeTurn(ctx, turnID)
}

// Thread returns the persisted managed conversation state.
func (s *SessionService) Thread(conversationID string) (ThreadState, bool, error) {
	return stateDAO{db: s.db}.thread(context.Background(), conversationID)
}

// SetThreadAgentIfExists updates a managed conversation agent without creating a thread.
func (s *SessionService) SetThreadAgentIfExists(conversationID, agent string) (bool, error) {
	return stateDAO{db: s.db}.setThreadAgent(context.Background(), conversationID, agent)
}

// ExternalMCPSession returns a persisted external MCP session mapping.
func (s *SessionService) ExternalMCPSession(externalConversationID string) (ExternalMCPSessionState, bool, error) {
	return stateDAO{db: s.db}.externalMCPSession(context.Background(), externalConversationID)
}

// ExternalMCPSessionByConversationID returns the public ID and binding for either session ID.
func (s *SessionService) ExternalMCPSessionByConversationID(conversationID string) (externalConversationID string, session ExternalMCPSessionState, ok bool, err error) {
	return stateDAO{db: s.db}.externalMCPSessionByConversationID(context.Background(), conversationID)
}

// ReserveExternalMCPRecovery makes paired work wait for the recovering owner.
func (s *SessionService) ReserveExternalMCPRecovery(conversationID string) error {
	_, session, ok, err := s.ExternalMCPSessionByConversationID(conversationID)
	if err != nil || !ok {
		return err
	}

	if session.PrivateConversationID != "" {
		s.reserveTurnPair(session.ManagedConversationID, conversationID)
	}

	return nil
}

// ReleaseExternalMCPRecovery releases paired work after recovery is abandoned.
func (s *SessionService) ReleaseExternalMCPRecovery(conversationID string) error {
	_, session, ok, err := s.ExternalMCPSessionByConversationID(conversationID)
	if err != nil || !ok || session.PrivateConversationID == "" {
		return err
	}

	s.completeTurnPairReservation(session.ManagedConversationID, conversationID)

	return nil
}

// ScheduledMessages returns all persisted scheduled messages.
func (s *SessionService) ScheduledMessages() (map[string]protocol.ScheduledMessageState, error) {
	return stateDAO{db: s.db}.scheduledMessages(context.Background(), "")
}

// ScheduledMessagesForConversation returns persisted scheduled messages for one conversation.
func (s *SessionService) ScheduledMessagesForConversation(conversationID string) (map[string]protocol.ScheduledMessageState, error) {
	return stateDAO{db: s.db}.scheduledMessages(context.Background(), conversationID)
}

// PutScheduledMessage persists one scheduled message.
func (s *SessionService) PutScheduledMessage(id string, message *protocol.ScheduledMessageState) error {
	return stateDAO{db: s.db}.putScheduledMessage(context.Background(), id, message)
}

// DeleteScheduledMessage deletes one scheduled message.
func (s *SessionService) DeleteScheduledMessage(id string) error {
	return stateDAO{db: s.db}.deleteScheduledMessage(context.Background(), id)
}

// ResetScheduledMessages deletes pending scheduled messages for one conversation.
func (s *SessionService) ResetScheduledMessages(conversationID string) error {
	return stateDAO{db: s.db}.resetScheduledMessages(context.Background(), conversationID)
}

// PutThreadQueueItem persists one Enqueued Slack Message.
func (s *SessionService) PutThreadQueueItem(id string, item *protocol.ThreadQueueItem) error {
	return stateDAO{db: s.db}.putThreadQueueItem(context.Background(), id, item)
}

// ThreadQueueForConversation returns Enqueued Slack Messages in stack order.
func (s *SessionService) ThreadQueueForConversation(conversationID string) ([]protocol.ThreadQueueItem, error) {
	return stateDAO{db: s.db}.threadQueueForConversation(context.Background(), conversationID)
}

// DeleteThreadQueueItem deletes one Enqueued Slack Message.
func (s *SessionService) DeleteThreadQueueItem(id string) error {
	return stateDAO{db: s.db}.deleteThreadQueueItem(context.Background(), id)
}

// ClaimScheduledMessage verifies one due scheduled message and advances recurring messages atomically.
func (s *SessionService) ClaimScheduledMessage(id, conversationID string, dueAt, now time.Time) (message protocol.ScheduledMessageState, claimed bool, err error) {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return protocol.ScheduledMessageState{}, false, fmt.Errorf("begin scheduled message claim: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(context.Background(), `SELECT scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring, interval_ns FROM scheduled_messages WHERE scheduled_message_id = $1`, strings.TrimSpace(id))

	_, message, err = scanScheduledMessage(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return protocol.ScheduledMessageState{}, false, nil
		}

		return protocol.ScheduledMessageState{}, false, err
	}

	if message.ConversationID != strings.TrimSpace(conversationID) || !message.DueAt.Equal(dueAt) {
		return protocol.ScheduledMessageState{}, false, nil
	}

	if message.Recurring {
		if err := (stateDAO{db: tx}).clearParkAfter(context.Background(), id); err != nil {
			return protocol.ScheduledMessageState{}, false, err
		}

		message.DueAt = now.UTC().Add(message.Interval)
		if err := (stateDAO{db: tx}).putScheduledMessage(context.Background(), id, &message); err != nil {
			return protocol.ScheduledMessageState{}, false, err
		}
	}

	if err := tx.Commit(); err != nil {
		return protocol.ScheduledMessageState{}, false, fmt.Errorf("commit scheduled message claim: %w", err)
	}

	return message, true, nil
}

// SyncCronSchedules replaces observed scheduled cron definitions.
func (s *SessionService) SyncCronSchedules(schedules []CronScheduleState, now time.Time) error {
	ctx := context.Background()

	tx, err := s.beginStateTx(ctx, "cron schedule sync")
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	seen := map[string]struct{}{}

	for _, schedule := range schedules {
		schedule.ScheduleID = strings.TrimSpace(schedule.ScheduleID)
		schedule.RelativePath = strings.TrimSpace(schedule.RelativePath)
		seen[schedule.ScheduleID] = struct{}{}

		_, err := tx.ExecContext(ctx, `INSERT INTO cron_schedules (schedule_id, relative_path, next_due_unix_ns, updated_at_unix_ns) VALUES ($1, $2, $3, $4) ON CONFLICT(schedule_id) DO UPDATE SET relative_path = excluded.relative_path, updated_at_unix_ns = excluded.updated_at_unix_ns`, schedule.ScheduleID, schedule.RelativePath, timeUnixNano(schedule.NextDue), timeUnixNano(now))
		if err != nil {
			return fmt.Errorf("upsert cron schedule: %w", err)
		}

		_, err = tx.ExecContext(ctx, `INSERT INTO cron_schedule_runs (relative_path, running, running_since_unix_ns, updated_at_unix_ns) VALUES ($1, 0, 0, $2) ON CONFLICT(relative_path) DO NOTHING`, schedule.RelativePath, timeUnixNano(now))
		if err != nil {
			return fmt.Errorf("ensure cron run state: %w", err)
		}
	}

	rows, err := tx.QueryContext(ctx, `SELECT schedule_id FROM cron_schedules ORDER BY schedule_id`)
	if err != nil {
		return fmt.Errorf("query cron schedules for sync: %w", err)
	}

	var stale []string

	for rows.Next() {
		var scheduleID string
		if err := rows.Scan(&scheduleID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan cron schedule for sync: %w", err)
		}

		if _, ok := seen[scheduleID]; !ok {
			stale = append(stale, scheduleID)
		}
	}

	if err := rows.Close(); err != nil {
		return fmt.Errorf("close cron schedule sync rows: %w", err)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("read cron schedules for sync: %w", err)
	}

	for _, scheduleID := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM cron_schedules WHERE schedule_id = $1`, scheduleID); err != nil {
			return fmt.Errorf("delete stale cron schedule: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM cron_schedule_runs WHERE running = 0 AND relative_path NOT IN (SELECT relative_path FROM cron_schedules)`); err != nil {
		return fmt.Errorf("delete stale cron run state: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit cron schedule sync: %w", err)
	}

	return nil
}

// ResetCronSchedules clears scheduled cron state at daemon observation start.
func (s *SessionService) ResetCronSchedules() error {
	ctx := context.Background()

	tx, err := s.beginStateTx(ctx, "cron schedule reset")
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM cron_schedules`); err != nil {
		return fmt.Errorf("delete cron schedules: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM cron_schedule_runs`); err != nil {
		return fmt.Errorf("delete cron run state: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit cron schedule reset: %w", err)
	}

	return nil
}

// DueCronSchedules returns observed scheduled cron definitions due at now.
func (s *SessionService) DueCronSchedules(now time.Time, limit int) ([]CronScheduleState, error) {
	query := `SELECT schedule_id, relative_path, next_due_unix_ns FROM cron_schedules WHERE next_due_unix_ns <= $1 ORDER BY next_due_unix_ns, schedule_id`
	args := []any{timeUnixNano(now)}

	if limit > 0 {
		query += ` LIMIT $2`

		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, fmt.Errorf("query due cron schedules: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var schedules []CronScheduleState

	for rows.Next() {
		schedule, err := scanCronSchedule(rows)
		if err != nil {
			return nil, err
		}

		schedules = append(schedules, schedule)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read due cron schedules: %w", err)
	}

	return schedules, nil
}

// ClaimCronSchedule verifies one due scheduled cron trigger and records per-file running state.
func (s *SessionService) ClaimCronSchedule(due CronScheduleState, nextDue, now time.Time) (CronScheduleRun, bool, error) {
	ctx := context.Background()

	tx, err := s.beginStateTx(ctx, "cron schedule claim")
	if err != nil {
		return CronScheduleRun{}, false, err
	}

	defer func() { _ = tx.Rollback() }()

	schedule, err := scanCronSchedule(tx.QueryRowContext(ctx, `SELECT schedule_id, relative_path, next_due_unix_ns FROM cron_schedules WHERE schedule_id = $1`, strings.TrimSpace(due.ScheduleID)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CronScheduleRun{}, false, nil
		}

		return CronScheduleRun{}, false, err
	}

	if schedule.RelativePath != strings.TrimSpace(due.RelativePath) || !schedule.NextDue.Equal(due.NextDue) || schedule.NextDue.After(now) {
		return CronScheduleRun{}, false, nil
	}

	var running bool

	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM cron_schedule_runs WHERE relative_path = $1 AND running != 0)`, schedule.RelativePath).Scan(&running)
	if err != nil {
		return CronScheduleRun{}, false, fmt.Errorf("check cron run state: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE cron_schedules SET next_due_unix_ns = $1, updated_at_unix_ns = $2 WHERE schedule_id = $3`, timeUnixNano(nextDue), timeUnixNano(now), schedule.ScheduleID); err != nil {
		return CronScheduleRun{}, false, fmt.Errorf("advance cron schedule: %w", err)
	}

	if running {
		if err := tx.Commit(); err != nil {
			return CronScheduleRun{}, false, fmt.Errorf("commit overlapped cron schedule claim: %w", err)
		}

		return CronScheduleRun{}, false, nil
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO cron_schedule_runs (relative_path, running, running_since_unix_ns, updated_at_unix_ns) VALUES ($1, 1, $2, $3) ON CONFLICT(relative_path) DO UPDATE SET running = 1, running_since_unix_ns = excluded.running_since_unix_ns, updated_at_unix_ns = excluded.updated_at_unix_ns`, schedule.RelativePath, timeUnixNano(now), timeUnixNano(now))
	if err != nil {
		return CronScheduleRun{}, false, fmt.Errorf("claim cron run state: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return CronScheduleRun{}, false, fmt.Errorf("commit cron schedule claim: %w", err)
	}

	return CronScheduleRun{ScheduleID: schedule.ScheduleID, RelativePath: schedule.RelativePath, DueAt: schedule.NextDue}, true, nil
}

// CompleteCronRun clears per-file scheduled cron running state.
func (s *SessionService) CompleteCronRun(relativePath string, now time.Time) error {
	_, err := s.db.ExecContext(context.Background(), `UPDATE cron_schedule_runs SET running = 0, running_since_unix_ns = 0, updated_at_unix_ns = $1 WHERE relative_path = $2`, timeUnixNano(now), strings.TrimSpace(relativePath))
	if err != nil {
		return fmt.Errorf("complete cron run: %w", err)
	}

	return nil
}

// ActiveGoalThreads returns managed thread state for conversations with active goals.
func (s *SessionService) ActiveGoalThreads() (map[string]ThreadState, error) {
	rows, err := s.db.QueryContext(context.Background(), `SELECT g.conversation_id, m.agent, m.created_by FROM conversation_goals g JOIN managed_conversations m ON m.conversation_id = g.conversation_id WHERE g.status = '' OR g.status = $1 ORDER BY g.conversation_id`, GoalStatusActive)
	if err != nil {
		return nil, fmt.Errorf("query active goal threads: %w", err)
	}
	defer func() { _ = rows.Close() }()

	threads := map[string]ThreadState{}

	for rows.Next() {
		var (
			conversationID, createdBy string
			thread                    ThreadState
		)
		if err := rows.Scan(&conversationID, &thread.Agent, &createdBy); err != nil {
			return nil, fmt.Errorf("scan active goal thread: %w", err)
		}

		thread.CreatedBy = ThreadCreator(createdBy)
		threads[conversationID] = thread
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read active goal threads: %w", err)
	}

	if len(threads) == 0 {
		return nil, nil
	}

	return threads, nil
}

// ApplyPendingRestartNotifications appends one developer notice to pending requester sessions.
func (s *SessionService) ApplyPendingRestartNotifications(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin restart notification update: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `SELECT conversation_id FROM pending_restart_notifications ORDER BY conversation_id`)
	if err != nil {
		return fmt.Errorf("query restart notification requesters: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var conversationIDs []string

	for rows.Next() {
		var conversationID string
		if err := rows.Scan(&conversationID); err != nil {
			return fmt.Errorf("scan restart notification requester: %w", err)
		}

		conversationIDs = append(conversationIDs, conversationID)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("read restart notification requesters: %w", err)
	}

	for _, conversationID := range conversationIDs {
		replayInput, err := replayInputForMessage("developer", restartNotificationDeveloperMessage)
		if err != nil {
			return fmt.Errorf("encode restart notification replay input: %w", err)
		}

		_, err = appendSessionEntryDB(ctx, tx, conversationID, &harness.SessionEntry{Version: 1, Type: "restart_notification", Timestamp: time.Now().UTC(), ReplayInput: replayInput})
		if err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM pending_restart_notifications`); err != nil {
		return fmt.Errorf("clear restart notification requesters: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit restart notification update: %w", err)
	}

	return nil
}

// PruneStateBefore removes expired thread and external-session state.
func (s *SessionService) PruneStateBefore(ctx context.Context, cutoff time.Time) (PruneStateStats, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PruneStateStats{}, fmt.Errorf("begin state prune: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	threadIDs, err := managedConversationIDs(ctx, tx)
	if err != nil {
		return PruneStateStats{}, err
	}

	externalSessions, err := externalMCPSessions(ctx, tx)
	if err != nil {
		return PruneStateStats{}, err
	}

	externalManagedIDs := map[string]struct{}{}
	for _, session := range externalSessions {
		externalManagedIDs[session.ManagedConversationID] = struct{}{}
	}

	var stats PruneStateStats

	deleteConversations := map[string]struct{}{}

	for _, conversationID := range threadIDs {
		if _, external := externalManagedIDs[conversationID]; external {
			continue
		}

		prune, err := shouldPruneThreadConversation(ctx, tx, conversationID, cutoff)
		if err != nil {
			return PruneStateStats{}, err
		}

		if prune {
			deleteConversations[conversationID] = struct{}{}
			stats.Threads++
		}
	}

	goalIDs, err := goalConversationIDs(ctx, tx)
	if err != nil {
		return PruneStateStats{}, err
	}

	for _, conversationID := range goalIDs {
		if slices.Contains(threadIDs, conversationID) {
			continue
		}

		prune, err := shouldPruneThreadConversation(ctx, tx, conversationID, cutoff)
		if err != nil {
			return PruneStateStats{}, err
		}

		if prune {
			if _, err := tx.ExecContext(ctx, `DELETE FROM conversation_goals WHERE conversation_id = $1`, conversationID); err != nil {
				return PruneStateStats{}, fmt.Errorf("delete stale goal: %w", err)
			}
		}
	}

	externalStats, err := pruneExternalMCPSessions(ctx, tx, cutoff, threadIDs, externalSessions, deleteConversations)
	if err != nil {
		return PruneStateStats{}, err
	}

	stats.Threads += externalStats.Threads
	stats.ExternalMCPSessions = externalStats.ExternalMCPSessions

	orphans, err := stalePrivateConversationIDs(ctx, tx, cutoff)
	if err != nil {
		return PruneStateStats{}, err
	}

	for _, conversationID := range orphans {
		deleteConversations[conversationID] = struct{}{}
	}

	rows, err := deleteSessionEntries(ctx, tx, deleteConversations)
	if err != nil {
		return PruneStateStats{}, err
	}

	for conversationID := range deleteConversations {
		if _, err := tx.ExecContext(ctx, `DELETE FROM active_turns WHERE conversation_id = $1`, conversationID); err != nil {
			return PruneStateStats{}, fmt.Errorf("delete stale active turn: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM pending_restart_notifications WHERE conversation_id = $1`, conversationID); err != nil {
			return PruneStateStats{}, fmt.Errorf("delete stale pending restart notification: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM conversation_goals WHERE conversation_id = $1`, conversationID); err != nil {
			return PruneStateStats{}, fmt.Errorf("delete stale conversation goal: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM thread_queue WHERE conversation_id = $1`, conversationID); err != nil {
			return PruneStateStats{}, fmt.Errorf("delete stale thread queue: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM managed_conversations WHERE conversation_id = $1`, conversationID); err != nil {
			return PruneStateStats{}, fmt.Errorf("delete stale managed conversation: %w", err)
		}
	}

	stats.SessionRows = rows

	if err := tx.Commit(); err != nil {
		return PruneStateStats{}, fmt.Errorf("commit state prune: %w", err)
	}

	return stats, nil
}

// ObserveEntries loads observed session entries through the runtime service.
func (s *SessionService) ObserveEntries(ctx context.Context, conversationID string, lastID int64) ([]ObservedSessionEntry, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, errors.New("conversation ID is required")
	}

	return observeSessionEntriesDB(ctx, s.db, conversationID, lastID)
}

// AppendEntryID appends one entry through the runtime service and returns its row ID.
func (s *SessionService) AppendEntryID(ctx context.Context, conversationID string, entry *harness.SessionEntry) (int64, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return 0, errors.New("conversation ID is required")
	}

	return appendSessionEntryDB(ctx, s.db, conversationID, entry)
}

// DeleteSession removes all entries for one conversation ID and returns deleted rows.
func (s *SessionService) DeleteSession(ctx context.Context, conversationID string) (int64, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return 0, errors.New("conversation ID is required")
	}

	result, err := s.db.ExecContext(ctx, `DELETE FROM session_entries WHERE conversation_id = $1`, conversationID)
	if err != nil {
		return 0, fmt.Errorf("delete rocketcode session: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count deleted rocketcode session rows: %w", err)
	}

	return rows, nil
}

// ListSessions returns summaries for stored rocketcode sessions.
func (s *SessionService) ListSessions(ctx context.Context, options SessionListOptions) ([]protocol.SessionSummary, error) {
	return listSessionsDB(ctx, s.db, options)
}

// Stop closes the runtime service and its database handle.
func (s *SessionService) Stop(context.Context) error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close rocketcode session db: %w", err)
	}

	return nil
}

// ReserveWorkflowTurn reserves paired turn ownership for a managed workflow.
func (s *SessionService) ReserveWorkflowTurn(conversationID string) (release func(), reserved bool, err error) {
	conversationID = strings.TrimSpace(conversationID)

	_, session, paired, err := s.ExternalMCPSessionByConversationID(conversationID)
	if err != nil {
		return inertTurnRelease, false, err
	}

	if !paired || conversationID != session.ManagedConversationID {
		return inertTurnRelease, true, nil
	}

	s.turnGatesMu.Lock()
	defer s.turnGatesMu.Unlock()

	gate := s.turnGates[conversationID]
	if gate != nil && (gate.reservedFor != "" || gate.refs > 0 || len(gate.token) == 0) {
		return inertTurnRelease, false, nil
	}

	if gate == nil {
		gate = &sessionTurnGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}

		s.turnGates[conversationID] = gate
	}

	gate.reservedFor, gate.reserved = conversationID, make(chan struct{})

	return func() { s.completeTurnPairReservation(conversationID, conversationID) }, true, nil
}

func inertTurnRelease() {}

// PutMCPWaiter records an MCP turn waiting on a later-work queue row.
func (s *SessionService) PutMCPWaiter(id string, inbound *protocol.InboundMessage) {
	s.waitersMu.Lock()
	if s.waiters == nil {
		s.waiters = map[string]*protocol.InboundMessage{}
	}

	s.waiters[id] = inbound
	s.waitersMu.Unlock()
}

// TakeMCPWaiter removes and returns the MCP waiter for a queue row.
func (s *SessionService) TakeMCPWaiter(id string) *protocol.InboundMessage {
	s.waitersMu.Lock()
	defer s.waitersMu.Unlock()

	if s.waiters == nil {
		return nil
	}

	inbound := s.waiters[id]
	delete(s.waiters, id)

	return inbound
}

// PairBusyFor reports whether pairID is busy for a caller other than conversationID.
func (s *SessionService) PairBusyFor(pairID, conversationID string) bool {
	s.turnGatesMu.Lock()
	defer s.turnGatesMu.Unlock()

	gate := s.turnGates[strings.TrimSpace(pairID)]
	if gate == nil {
		return false
	}

	if gate.reservedFor != "" && gate.reservedFor != strings.TrimSpace(conversationID) {
		return true
	}

	return len(gate.token) == 0
}

func (s *SessionService) appendExternalMCPEntry(ctx context.Context, privateConversationID, managedConversationID string, entry *harness.SessionEntry, managedReplayPrefix []json.RawMessage) (int64, error) {
	managedEntry, err := externalMCPManagedEntry(entry, managedReplayPrefix)
	if err != nil {
		return 0, err
	}

	tx, err := s.beginStateTx(ctx, "external MCP session entry append")
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	privateID, err := appendSessionEntryDB(ctx, tx, strings.TrimSpace(privateConversationID), entry)
	if err != nil {
		return 0, err
	}

	if _, err := appendSessionEntryDB(ctx, tx, strings.TrimSpace(managedConversationID), &managedEntry); err != nil {
		return 0, fmt.Errorf("append managed external MCP session entry: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit external MCP session entry append: %w", err)
	}

	if entry.Type != externalMCPMetadataEntryType {
		s.completeTurnPairReservation(managedConversationID, privateConversationID)
	}

	return privateID, nil
}

func (s *SessionService) lockTurnPair(ctx context.Context, pairID, conversationID string) (func(), error) {
	pairID = strings.TrimSpace(pairID)
	conversationID = strings.TrimSpace(conversationID)

	s.turnGatesMu.Lock()

	gate := s.turnGates[pairID]
	if gate == nil {
		gate = &sessionTurnGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}

		s.turnGates[pairID] = gate
	}

	gate.refs++
	reserved := gate.reserved
	waitForReservation := gate.reservedFor != "" && gate.reservedFor != conversationID
	s.turnGatesMu.Unlock()

	if waitForReservation {
		select {
		case <-ctx.Done():
			s.releaseTurnGateReference(pairID, gate)
			return nil, fmt.Errorf("wait for paired turn reservation: %w", ctx.Err())
		case <-reserved:
		}
	}

	select {
	case <-ctx.Done():
		s.releaseTurnGateReference(pairID, gate)
		return nil, fmt.Errorf("wait for paired turn: %w", ctx.Err())
	case <-gate.token:
	}

	return func() {
		gate.token <- struct{}{}

		s.releaseTurnGateReference(pairID, gate)
	}, nil
}

func (s *SessionService) reserveTurnPair(pairID, conversationID string) {
	pairID = strings.TrimSpace(pairID)
	conversationID = strings.TrimSpace(conversationID)

	s.turnGatesMu.Lock()
	defer s.turnGatesMu.Unlock()

	gate := s.turnGates[pairID]
	if gate == nil {
		gate = &sessionTurnGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}

		s.turnGates[pairID] = gate
	}

	if gate.reservedFor == "" {
		gate.reservedFor = conversationID
		gate.reserved = make(chan struct{})
	}
}

func (s *SessionService) completeTurnPairReservation(pairID, conversationID string) {
	s.turnGatesMu.Lock()
	defer s.turnGatesMu.Unlock()

	gate := s.turnGates[strings.TrimSpace(pairID)]
	if gate == nil || gate.reservedFor != strings.TrimSpace(conversationID) {
		return
	}

	close(gate.reserved)
	gate.reservedFor = ""

	gate.reserved = nil
	if gate.refs == 0 {
		delete(s.turnGates, strings.TrimSpace(pairID))
	}
}

func (s *SessionService) releaseTurnGateReference(pairID string, gate *sessionTurnGate) {
	s.turnGatesMu.Lock()
	defer s.turnGatesMu.Unlock()

	gate.refs--
	if gate.refs == 0 && gate.reservedFor == "" {
		delete(s.turnGates, pairID)
	}
}

func (s *SessionService) externalMCPMetadataEntry(ctx context.Context, conversationID string) (ObservedSessionEntry, bool, error) {
	var (
		entry ObservedSessionEntry
		raw   string
	)

	err := s.db.QueryRowContext(ctx, `SELECT id, entry_json FROM session_entries WHERE conversation_id = $1 AND entry_json::jsonb->>'type' = $2 ORDER BY id DESC LIMIT 1`, strings.TrimSpace(conversationID), externalMCPMetadataEntryType).Scan(&entry.ID, &raw)
	if err == sql.ErrNoRows {
		return ObservedSessionEntry{}, false, nil
	}

	if err != nil {
		return ObservedSessionEntry{}, false, fmt.Errorf("read external MCP metadata entry: %w", err)
	}

	if err := json.Unmarshal([]byte(raw), &entry.Entry); err != nil {
		return ObservedSessionEntry{}, false, fmt.Errorf("parse external MCP metadata entry: %w", err)
	}

	return entry, true, nil
}

func (s *SessionService) setGoalStatus(conversationID, status, note string) (GoalState, error) {
	conversationID = strings.TrimSpace(conversationID)
	ctx := context.Background()

	tx, err := s.beginStateTx(ctx, "goal status update")
	if err != nil {
		return GoalState{}, err
	}
	defer func() { _ = tx.Rollback() }()

	dao := stateDAO{db: tx}
	if _, err := dao.setActiveGoalStatus(ctx, conversationID, status, note, time.Now().UTC()); err != nil {
		return GoalState{}, err
	}

	goal, _, err := dao.goal(ctx, conversationID)
	if err != nil {
		return GoalState{}, err
	}

	if err := tx.Commit(); err != nil {
		return GoalState{}, fmt.Errorf("commit goal status update: %w", err)
	}

	return goal, nil
}

func (s *SessionService) beginStateTx(ctx context.Context, label string) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin %s: %w", label, err)
	}

	return tx, nil
}

func (s sessionStore) in() iter.Seq2[harness.SessionEntry, error] {
	return func(yield func(harness.SessionEntry, error) bool) {
		var (
			observed []ObservedSessionEntry
			err      error
		)

		observed, err = s.service.ObserveEntries(context.Background(), s.conversationID, 0)
		if err != nil {
			var entry harness.SessionEntry
			yield(entry, err)

			return
		}

		for i := range observed {
			if !yield(observed[i].Entry, nil) {
				return
			}
		}
	}
}

//nolint:gocritic // rocketcode requires value-shaped session entries at this boundary.
func (s sessionStore) outID(entry harness.SessionEntry) (int64, error) {
	if s.managedConversationID != "" {
		return s.service.appendExternalMCPEntry(context.Background(), s.conversationID, s.managedConversationID, &entry, s.managedReplayPrefix)
	}

	return s.service.AppendEntryID(context.Background(), s.conversationID, &entry)
}

// ObserveSessionEntries returns replay entries and their row IDs after lastID.
func ObserveSessionEntries(ctx context.Context, databaseURL, conversationID string, lastID int64) ([]ObservedSessionEntry, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, errors.New("conversation ID is required")
	}

	service, err := NewSessionServiceIn(databaseURL, slog.New(slog.DiscardHandler))
	if err != nil {
		return nil, err
	}
	defer func() { _ = service.Stop(ctx) }()

	return service.ObserveEntries(ctx, conversationID, lastID)
}

func observeSessionEntriesDB(ctx context.Context, db *sql.DB, conversationID string, lastID int64) ([]ObservedSessionEntry, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, entry_json FROM session_entries WHERE conversation_id = $1 AND id > $2 ORDER BY id`, conversationID, lastID)
	if err != nil {
		return nil, fmt.Errorf("query rocketcode session entries: %w", err)
	}

	defer func() { _ = rows.Close() }()

	entries := []ObservedSessionEntry{}

	for rows.Next() {
		var (
			id  int64
			raw string
		)

		if err := rows.Scan(&id, &raw); err != nil {
			return nil, fmt.Errorf("scan rocketcode session entry: %w", err)
		}

		var entry harness.SessionEntry
		if err := json.Unmarshal([]byte(raw), &entry); err != nil {
			return nil, fmt.Errorf("parse rocketcode session entry: %w", err)
		}

		entries = append(entries, ObservedSessionEntry{ID: id, Entry: entry})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read rocketcode session entries: %w", err)
	}

	return entries, nil
}

func appendSessionEntryDB(ctx context.Context, db stateStoreDB, conversationID string, entry *harness.SessionEntry) (int64, error) {
	data, err := json.Marshal(entry)
	if err != nil {
		return 0, fmt.Errorf("marshal rocketcode session entry: %w", err)
	}

	var id int64
	if err := db.QueryRowContext(ctx, `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES ($1, $2, $3) RETURNING id`, conversationID, string(data), entry.Timestamp.UTC().Format(time.RFC3339Nano)).Scan(&id); err != nil {
		return 0, fmt.Errorf("append rocketcode session entry: %w", err)
	}

	return id, nil
}

func externalMCPManagedEntry(entry *harness.SessionEntry, replayPrefix []json.RawMessage) (harness.SessionEntry, error) {
	managed := *entry
	managed.ReplayInput = append(make([]json.RawMessage, 0, len(replayPrefix)+len(entry.ReplayInput)), replayPrefix...)

	for _, raw := range entry.ReplayInput {
		var item struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return harness.SessionEntry{}, fmt.Errorf("read external MCP replay item type: %w", err)
		}

		if item.Type != "compaction" && item.Type != "compaction_summary" {
			managed.ReplayInput = append(managed.ReplayInput, raw)
		}
	}

	return managed, nil
}

// DeleteSessionIn removes all entries for one conversation ID and returns deleted rows.
func DeleteSessionIn(ctx context.Context, databaseURL, conversationID string) (int64, error) {
	service, err := NewSessionServiceIn(databaseURL, slog.New(slog.DiscardHandler))
	if err != nil {
		return 0, err
	}
	defer func() { _ = service.Stop(ctx) }()

	return service.DeleteSession(ctx, conversationID)
}

func listSessionsDB(ctx context.Context, db *sql.DB, options SessionListOptions) ([]protocol.SessionSummary, error) {
	query := `SELECT conversation_id, entry_json, entry_timestamp FROM session_entries ORDER BY conversation_id, id`

	var args []any

	if !options.Since.IsZero() || !options.Until.IsZero() || options.Limit > 0 {
		var since any
		if !options.Since.IsZero() {
			since = options.Since.UTC()
		}

		var until any
		if !options.Until.IsZero() {
			until = options.Until.UTC()
		}

		query = `WITH candidates AS (
	SELECT conversation_id, MAX(entry_timestamp::timestamptz) AS last_updated
	FROM session_entries
	GROUP BY conversation_id
	HAVING ($1::timestamptz IS NULL OR MAX(entry_timestamp::timestamptz) >= $2::timestamptz)
		AND ($3::timestamptz IS NULL OR MAX(entry_timestamp::timestamptz) < $4::timestamptz)
	ORDER BY last_updated DESC, conversation_id`
		args = []any{since, since, until, until}

		if options.Limit > 0 {
			query += `
	LIMIT $5`

			args = append(args, options.Limit)
		}

		query += `
)
SELECT se.conversation_id, se.entry_json, se.entry_timestamp
FROM session_entries se
JOIN candidates c ON c.conversation_id = se.conversation_id
ORDER BY c.last_updated DESC, c.conversation_id, se.id`
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query rocketcode session summaries: %w", err)
	}

	defer func() { _ = rows.Close() }()

	summaryByID := map[string]*protocol.SessionSummary{}
	order := []string{}

	for rows.Next() {
		var conversationID, raw, timestamp string
		if err := rows.Scan(&conversationID, &raw, &timestamp); err != nil {
			return nil, fmt.Errorf("scan rocketcode session summary: %w", err)
		}

		summary := summaryByID[conversationID]
		if summary == nil {
			summary = &protocol.SessionSummary{ConversationID: conversationID}
			summaryByID[conversationID] = summary
			order = append(order, conversationID)
		}

		var entry harness.SessionEntry
		if err := json.Unmarshal([]byte(raw), &entry); err != nil {
			return nil, fmt.Errorf("parse rocketcode session summary entry: %w", err)
		}

		summary.Turns++
		if updated, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
			summary.LastUpdated = updated
		}

		messages, err := replayInputMessages(entry.ReplayInput)
		if err != nil {
			return nil, fmt.Errorf("decode rocketcode session summary replay input: %w", err)
		}

		for i := range messages {
			switch messages[i].role {
			case "user":
				summary.LastUserMessage = messages[i].text
			case "assistant":
				summary.LastAssistantMessage = messages[i].text
			}
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read rocketcode session summaries: %w", err)
	}

	summaries := make([]protocol.SessionSummary, 0, len(order))
	for _, conversationID := range order {
		summaries = append(summaries, *summaryByID[conversationID])
	}

	return summaries, nil
}

// ListSessionsInOptions returns summaries for stored rocketcode sessions.
func ListSessionsInOptions(ctx context.Context, databaseURL string, options SessionListOptions) ([]protocol.SessionSummary, error) {
	service, err := NewSessionServiceIn(databaseURL, slog.New(slog.DiscardHandler))
	if err != nil {
		return nil, err
	}
	defer func() { _ = service.Stop(ctx) }()

	return service.ListSessions(ctx, options)
}

func slackStateKeyTime(key, prefix string) (time.Time, bool) {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, prefix) {
		return time.Time{}, false
	}

	i := strings.LastIndexByte(key, ':')
	if i < len(prefix) || i == len(key)-1 {
		return time.Time{}, false
	}

	secondsText, fractionText, _ := strings.Cut(key[i+1:], ".")

	seconds, err := strconv.ParseInt(secondsText, 10, 64)
	if err != nil {
		return time.Time{}, false
	}

	nanos := int64(0)

	if fractionText != "" {
		if len(fractionText) > 9 {
			fractionText = fractionText[:9]
		}

		nanos, err = strconv.ParseInt((fractionText + "000000000")[:9], 10, 64)
		if err != nil {
			return time.Time{}, false
		}
	}

	return time.Unix(seconds, nanos).UTC(), true
}

func shouldPruneThreadConversation(ctx context.Context, db stateStoreDB, conversationID string, cutoff time.Time) (bool, error) {
	created, ok := slackStateKeyTime(conversationID, "slack-thread:")
	if !ok {
		return false, nil
	}

	return sessionLatestBefore(ctx, db, conversationID, created, cutoff)
}

func sessionLatestBefore(ctx context.Context, db stateStoreDB, conversationID string, fallback, cutoff time.Time) (bool, error) {
	var before bool

	err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(entry_timestamp), $1) < $2 FROM session_entries WHERE conversation_id = $3`, fallback.UTC().Format(time.RFC3339Nano), cutoff.UTC().Format(time.RFC3339Nano), conversationID).Scan(&before)
	if err != nil {
		return false, fmt.Errorf("read latest session entry timestamp: %w", err)
	}

	return before, nil
}

func managedConversationIDs(ctx context.Context, db stateStoreDB) ([]string, error) {
	return queryStrings(ctx, db, `SELECT conversation_id FROM managed_conversations ORDER BY conversation_id`, "managed conversation IDs")
}

func goalConversationIDs(ctx context.Context, db stateStoreDB) ([]string, error) {
	return queryStrings(ctx, db, `SELECT conversation_id FROM conversation_goals ORDER BY conversation_id`, "goal conversation IDs")
}

func externalMCPSessions(ctx context.Context, db stateStoreDB) (map[string]ExternalMCPSessionState, error) {
	rows, err := db.QueryContext(ctx, `SELECT external_conversation_id, agent, private_conversation_id, managed_conversation_id, slack_channel FROM external_mcp_sessions ORDER BY external_conversation_id`)
	if err != nil {
		return nil, fmt.Errorf("query external MCP sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	sessions := map[string]ExternalMCPSessionState{}

	for rows.Next() {
		externalConversationID, session, err := scanExternalMCPSession(rows)
		if err != nil {
			return nil, err
		}

		sessions[externalConversationID] = session
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read external MCP sessions: %w", err)
	}

	return sessions, nil
}

func queryStrings(ctx context.Context, db stateStoreDB, query, label string) ([]string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", label, err)
	}
	defer func() { _ = rows.Close() }()

	var values []string

	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan %s: %w", label, err)
		}

		values = append(values, value)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}

	return values, nil
}

func conversationExists(ctx context.Context, db stateStoreDB, table, column, conversationID string) (bool, error) {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+table+` WHERE `+column+` = $1)`, conversationID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check conversation reference in %s: %w", table, err)
	}

	return exists, nil
}

func pruneExternalMCPSessions(ctx context.Context, tx *sql.Tx, cutoff time.Time, threadIDs []string, sessions map[string]ExternalMCPSessionState, deleteConversations map[string]struct{}) (PruneStateStats, error) {
	var stats PruneStateStats

	for externalConversationID, session := range sessions {
		privateConversationID := strings.TrimSpace(session.PrivateConversationID)
		managedConversationID := strings.TrimSpace(session.ManagedConversationID)

		pruneManaged, err := shouldPruneThreadConversation(ctx, tx, managedConversationID, cutoff)
		if err != nil {
			return PruneStateStats{}, err
		}

		if !pruneManaged {
			continue
		}

		if privateConversationID != "" {
			prunePrivate, err := sessionLatestBefore(ctx, tx, privateConversationID, time.Unix(0, 0).UTC(), cutoff)
			if err != nil {
				return PruneStateStats{}, err
			}

			if !prunePrivate {
				continue
			}

			deleteConversations[privateConversationID] = struct{}{}
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM external_mcp_sessions WHERE external_conversation_id = $1`, externalConversationID); err != nil {
			return PruneStateStats{}, fmt.Errorf("delete stale external MCP session: %w", err)
		}

		deleteConversations[managedConversationID] = struct{}{}
		if slices.Contains(threadIDs, managedConversationID) {
			stats.Threads++
		}

		stats.ExternalMCPSessions++
	}

	return stats, nil
}

func stalePrivateConversationIDs(ctx context.Context, db *sql.Tx, cutoff time.Time) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT conversation_id FROM session_entries WHERE conversation_id LIKE 'slack-thread:%' OR conversation_id LIKE 'external_mcp:%' OR conversation_id LIKE 'cron:%' OR conversation_id LIKE 'one-off-cron:%' GROUP BY conversation_id HAVING MAX(entry_timestamp) < $1`, cutoff.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("query stale private session conversations: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var candidates []string

	for rows.Next() {
		var conversationID string
		if err := rows.Scan(&conversationID); err != nil {
			return nil, fmt.Errorf("scan stale private session conversation: %w", err)
		}

		candidates = append(candidates, conversationID)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read stale private session conversations: %w", err)
	}

	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close stale private session conversations: %w", err)
	}

	var stale []string

	for _, conversationID := range candidates {
		if ok, err := conversationExists(ctx, db, `managed_conversations`, `conversation_id`, conversationID); err != nil {
			return nil, err
		} else if ok {
			continue
		}

		if ok, err := conversationExists(ctx, db, `external_mcp_sessions`, `private_conversation_id`, conversationID); err != nil {
			return nil, err
		} else if ok {
			continue
		}

		stale = append(stale, conversationID)
	}

	return stale, nil
}

func deleteSessionEntries(ctx context.Context, db stateStoreDB, conversationIDs map[string]struct{}) (int64, error) {
	var deleted int64

	for conversationID := range conversationIDs {
		result, err := db.ExecContext(ctx, `DELETE FROM session_entries WHERE conversation_id = $1`, conversationID)
		if err != nil {
			return 0, fmt.Errorf("delete stale session entries: %w", err)
		}

		rows, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("count stale session entries: %w", err)
		}

		deleted += rows
	}

	return deleted, nil
}

type runLockWork interface {
	Run(context.Context) error
}

func newRunLockClient(db *sql.DB) (*pglock.Client, error) {
	client, err := pglock.UnsafeNew(
		db,
		pglock.WithCustomTable(runLockTable),
		pglock.WithLeaseDuration(pglock.DefaultLeaseDuration),
		pglock.WithHeartbeatFrequency(pglock.DefaultHeartbeatFrequency),
	)
	if err != nil {
		return nil, fmt.Errorf("acquire rocketclaw run lock: %w", err)
	}

	if err := client.TryCreateTable(); err != nil {
		return nil, fmt.Errorf("acquire rocketclaw run lock: %w", err)
	}

	return client, nil
}

func holdRunLock(ctx context.Context, db *sql.DB, work runLockWork) error {
	client, err := newRunLockClient(db)
	if err != nil {
		return err
	}

	err = client.Do(ctx, runLockName, func(lockCtx context.Context, _ *pglock.Lock) error {
		return work.Run(lockCtx)
	}, pglock.FailIfLocked())
	if errors.Is(err, pglock.ErrNotAcquired) {
		return errRunLocked
	}

	if err != nil {
		return fmt.Errorf("%w", err)
	}

	return nil
}

func openSessionDB(ctx context.Context, databaseURL string, logger *slog.Logger) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, hideDSN(err, databaseURL, "open rocketclaw state store")
	}

	db := stdlib.OpenDB(*cfg)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, hideDSN(err, databaseURL, "ping rocketclaw state store")
	}

	if err := initializeSessionDB(ctx, db, logger); err != nil {
		_ = db.Close()
		return nil, hideDSN(err, databaseURL, "initialize rocketclaw state store")
	}

	return db, nil
}

func hideDSN(err error, databaseURL, op string) error {
	msg := err.Error()
	if databaseURL != "" {
		msg = strings.ReplaceAll(msg, databaseURL, "postgres")
	}

	if u, errParse := url.Parse(databaseURL); errParse == nil && u.User != nil {
		if password, ok := u.User.Password(); ok && password != "" {
			msg = strings.ReplaceAll(msg, password, "redacted")
		}
	}

	return fmt.Errorf("%s: %s", op, msg)
}

type memoryStore struct{ entries []harness.SessionEntry }

func (m *memoryStore) in() iter.Seq2[harness.SessionEntry, error] {
	return func(yield func(harness.SessionEntry, error) bool) {
		for i := range m.entries {
			if !yield(m.entries[i], nil) {
				return
			}
		}
	}
}

//nolint:gocritic // rocketcode requires this callback shape.
func (m *memoryStore) out(entry harness.SessionEntry) error {
	m.entries = append(m.entries, entry)
	return nil
}
