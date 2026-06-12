package harnessbridge

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	harness "github.com/Rocketable/platform/internal/rocketcode"

	// Register the pure-Go SQLite database/sql driver used by this package.
	_ "modernc.org/sqlite"
)

const mainConversationID = "main"

const restartNotificationDeveloperMessage = "The rocketclaw server has been restarted."

var errStateStoreCorrupt = errors.New("rocketclaw state store is corrupt")

// State is the persisted rocketclaw session state.
type State struct {
	Threads                     map[string]ThreadState             `json:"threads,omitempty"`
	ResponseCheckpoints         map[string]ResponseCheckpointState `json:"response_checkpoints,omitempty"`
	ExternalMCPSessions         map[string]ExternalMCPSessionState `json:"external_mcp_sessions,omitempty"`
	ScheduledMessages           map[string]ScheduledMessageState   `json:"scheduled_messages,omitempty"`
	Goals                       map[string]GoalState               `json:"goals,omitempty"`
	PendingRestartNotifications map[string]bool                    `json:"pending_restart_notifications,omitempty"`
}

// GoalStatusActive and related constants are persisted goal-loop statuses.
const (
	GoalStatusActive          = "active"
	GoalStatusComplete        = "complete"
	GoalStatusBlocked         = "blocked"
	GoalStatusPaused          = "paused"
	GoalStatusStopped         = "stopped"
	GoalStatusBudgetExhausted = "budget_exhausted"
)

// ThreadState is the persisted state for one text-thread bridge.
type ThreadState struct {
	Agent              string `json:"agent,omitempty"`
	SeededFromResponse string `json:"seeded_from_response,omitempty"`
}

// ResponseCheckpointState records enough metadata to seed a response-rooted thread.
type ResponseCheckpointState struct {
	ConversationID string `json:"conversation_id,omitempty"`
	SessionEntryID int64  `json:"session_entry_id,omitempty"`
	ResponseID     string `json:"response_id,omitempty"`
	Model          string `json:"model,omitempty"`
	AssistantText  string `json:"assistant_text,omitempty"`
}

// ExternalMCPSessionState maps an external MCP conversation ID to its private session.
type ExternalMCPSessionState struct {
	Agent          string `json:"agent,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
}

// ScheduledMessageState records one pending delayed system prompt.
type ScheduledMessageState struct {
	ConversationID string        `json:"conversation_id,omitempty"`
	Agent          string        `json:"agent,omitempty"`
	Message        string        `json:"message,omitempty"`
	DueAt          time.Time     `json:"due_at,omitzero"`
	Recurring      bool          `json:"recurring,omitempty"`
	Interval       time.Duration `json:"interval,omitempty"`
}

// GoalState records one active or terminal managed-thread goal loop.
type GoalState struct {
	Objective   string    `json:"objective,omitempty"`
	CheckScript string    `json:"check_script,omitempty"`
	MaxTurns    int       `json:"max_turns,omitempty"`
	TurnsUsed   int       `json:"turns_used,omitempty"`
	Status      string    `json:"status,omitempty"`
	Note        string    `json:"note,omitempty"`
	CreatedAt   time.Time `json:"created_at,omitzero"`
	UpdatedAt   time.Time `json:"updated_at,omitzero"`
}

type sqliteSessionStore struct {
	conversationID string
	service        *SessionService
}

// SessionService owns runtime SQLite session and state access inside one rocketclaw process.
type SessionService struct {
	db *sql.DB
}

// SessionSummary is the compact observable state of one rocketcode session.
type SessionSummary struct {
	ConversationID, LastUserMessage, LastAssistantMessage string
	Turns                                                 int
	LastUpdated                                           time.Time
}

// SessionListOptions bounds read-only session summary inspection.
type SessionListOptions struct {
	Since time.Time
	Until time.Time
	Limit int
}

// ObservedSessionEntry is one stored rocketcode entry with its SQLite row ID.
type ObservedSessionEntry struct {
	ID    int64
	Entry harness.SessionEntry
}

// VacuumStats reports SQLite page counts before and after vacuuming.
type VacuumStats struct {
	DBExists                         bool
	BeforePageCount, BeforeFreePages int64
	AfterPageCount, AfterFreePages   int64
}

// WALCheckpointStats reports the outcome of a SQLite WAL checkpoint.
type WALCheckpointStats struct {
	Busy, LogFrames, CheckpointedFrames int64
}

// PruneStateStats reports how much stale persisted state was removed.
type PruneStateStats struct {
	Threads, ResponseCheckpoints, ExternalMCPSessions int
	SessionRows                                       int64
}

type stateStoreDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func newSessionStore(conversationID string, service *SessionService) sqliteSessionStore {
	if conversationID = strings.TrimSpace(conversationID); conversationID == "" {
		conversationID = mainConversationID
	}

	return sqliteSessionStore{conversationID: conversationID, service: service}
}

// NewSessionServiceIn starts a runtime-owned SQLite session service in workDir.
func NewSessionServiceIn(workspace, workDir string) (*SessionService, error) {
	db, err := openWorkspaceSessionDB(context.Background(), workspace, workDir)
	if err != nil {
		return nil, err
	}

	return &SessionService{db: db}, nil
}

// Load returns the current persisted session state.
func (s *SessionService) Load() (State, error) {
	return loadRocketClawState(context.Background(), s.db)
}

// UpsertThread records or updates a text-thread bridge entry.
func (s *SessionService) UpsertThread(conversationID, agent string) error {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return errors.New("thread conversation ID is required")
	}

	return s.updateState(func(state *State) {
		if state.Threads == nil {
			state.Threads = map[string]ThreadState{}
		}

		thread := state.Threads[conversationID]
		thread.Agent = strings.TrimSpace(agent)
		state.Threads[conversationID] = thread
	})
}

// BeginGoal records a new active goal for a managed conversation.
func (s *SessionService) BeginGoal(conversationID, objective, checkScript string, maxTurns int) error {
	conversationID = strings.TrimSpace(conversationID)
	objective = strings.TrimSpace(objective)
	checkScript = strings.TrimSpace(checkScript)

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

	return s.updateState(func(state *State) {
		if state.Goals == nil {
			state.Goals = map[string]GoalState{}
		}

		state.Goals[conversationID] = GoalState{Objective: objective, CheckScript: checkScript, MaxTurns: maxTurns, Status: GoalStatusActive, CreatedAt: now, UpdatedAt: now}
	})
}

// Goal returns the persisted goal state for a conversation.
func (s *SessionService) Goal(conversationID string) (GoalState, bool, error) {
	state, err := s.Load()
	if err != nil {
		return GoalState{}, false, err
	}

	goal, ok := state.Goals[strings.TrimSpace(conversationID)]

	goal.Status = strings.TrimSpace(goal.Status)
	if goal.Status == "" {
		goal.Status = GoalStatusActive
	}

	return goal, ok, nil
}

// ActiveGoals returns persisted active goals keyed by conversation ID.
func (s *SessionService) ActiveGoals() (map[string]GoalState, error) {
	state, err := s.Load()
	if err != nil {
		return nil, err
	}

	active := map[string]GoalState{}

	for conversationID := range state.Goals {
		goal := state.Goals[conversationID]
		if strings.TrimSpace(goal.Status) == GoalStatusActive {
			active[conversationID] = goal
		}
	}

	if len(active) == 0 {
		return nil, nil
	}

	return active, nil
}

// AccountGoalTurn increments one active goal turn and applies budget exhaustion.
func (s *SessionService) AccountGoalTurn(conversationID string) (GoalState, bool, error) {
	conversationID = strings.TrimSpace(conversationID)

	var (
		goal GoalState
		ok   bool
	)

	err := s.updateState(func(state *State) {
		goal, ok = state.Goals[conversationID]
		if !ok {
			return
		}

		status := strings.TrimSpace(goal.Status)
		if status == "" {
			status = GoalStatusActive
		}

		if status == GoalStatusStopped || status == GoalStatusBudgetExhausted {
			return
		}

		goal.TurnsUsed++

		goal.UpdatedAt = time.Now().UTC()
		if status == GoalStatusActive && goal.MaxTurns > 0 && goal.TurnsUsed >= goal.MaxTurns {
			goal.Status = GoalStatusBudgetExhausted
		}

		state.Goals[conversationID] = goal
	})
	if err != nil {
		return GoalState{}, false, err
	}

	return goal, ok, nil
}

// UpdateGoalStatus sets a model-controlled terminal goal status.
func (s *SessionService) UpdateGoalStatus(conversationID, status, note string) (GoalState, error) {
	status = strings.TrimSpace(status)
	switch status {
	case GoalStatusComplete, GoalStatusBlocked, GoalStatusPaused:
	default:
		return GoalState{}, fmt.Errorf("unsupported goal status %q", status)
	}

	return s.setGoalStatus(conversationID, status, note)
}

// StopGoal marks an active goal stopped.
func (s *SessionService) StopGoal(conversationID string) (GoalState, bool, error) {
	goal, err := s.setGoalStatus(conversationID, GoalStatusStopped, "stopped by human")
	if err != nil {
		return GoalState{}, false, err
	}

	return goal, strings.TrimSpace(goal.Status) == GoalStatusStopped, nil
}

// MarkThreadSeeded records the response checkpoint used to seed a text thread.
func (s *SessionService) MarkThreadSeeded(conversationID, seedKey string) error {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return errors.New("thread conversation ID is required")
	}

	return s.updateState(func(state *State) {
		if state.Threads == nil {
			state.Threads = map[string]ThreadState{}
		}

		thread := state.Threads[conversationID]

		thread.Agent = strings.TrimSpace(thread.Agent)
		if thread.Agent == "" {
			thread.Agent = mainConversationID
		}

		thread.SeededFromResponse = strings.TrimSpace(seedKey)
		state.Threads[conversationID] = thread
	})
}

// UpsertResponseCheckpoint records a response checkpoint.
func (s *SessionService) UpsertResponseCheckpoint(key string, checkpoint ResponseCheckpointState) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("response checkpoint key is required")
	}

	return s.updateState(func(state *State) {
		if state.ResponseCheckpoints == nil {
			state.ResponseCheckpoints = map[string]ResponseCheckpointState{}
		}

		checkpoint.ConversationID = strings.TrimSpace(checkpoint.ConversationID)
		checkpoint.ResponseID = strings.TrimSpace(checkpoint.ResponseID)
		checkpoint.Model = strings.TrimSpace(checkpoint.Model)
		state.ResponseCheckpoints[key] = checkpoint
	})
}

// UpsertExternalMCPSession records an external MCP conversation ID mapping.
func (s *SessionService) UpsertExternalMCPSession(externalConversationID string, session ExternalMCPSessionState) error {
	externalConversationID = strings.TrimSpace(externalConversationID)
	if externalConversationID == "" {
		return errors.New("external MCP conversation ID is required")
	}

	return s.updateState(func(state *State) {
		if state.ExternalMCPSessions == nil {
			state.ExternalMCPSessions = map[string]ExternalMCPSessionState{}
		}

		session.Agent = strings.TrimSpace(session.Agent)
		session.ConversationID = strings.TrimSpace(session.ConversationID)
		state.ExternalMCPSessions[externalConversationID] = session
	})
}

// MarkRestartRequester records that conversationID should see the post-restart notice.
func (s *SessionService) MarkRestartRequester(ctx context.Context, conversationID string) error {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return errors.New("restart requester conversation ID is required")
	}

	return s.updateStateContext(ctx, func(state *State) {
		if state.PendingRestartNotifications == nil {
			state.PendingRestartNotifications = map[string]bool{}
		}

		state.PendingRestartNotifications[conversationID] = true
	})
}

// ApplyPendingRestartNotifications appends one developer notice to pending requester sessions.
func (s *SessionService) ApplyPendingRestartNotifications(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin restart notification update: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	state, err := loadRocketClawState(ctx, tx)
	if err != nil {
		return err
	}

	conversationIDs := make([]string, 0, len(state.PendingRestartNotifications))
	for conversationID := range state.PendingRestartNotifications {
		conversationIDs = append(conversationIDs, conversationID)
	}

	slices.Sort(conversationIDs)

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

	state.PendingRestartNotifications = nil
	if err := saveRocketClawState(ctx, tx, state); err != nil {
		return err
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

	state, err := loadRocketClawState(ctx, tx)
	if err != nil {
		return PruneStateStats{}, err
	}

	var stats PruneStateStats

	deleteConversations := map[string]struct{}{}

	for conversationID := range state.Threads {
		prune, err := shouldPruneThreadConversation(ctx, tx, conversationID, cutoff)
		if err != nil {
			return PruneStateStats{}, err
		}

		if prune {
			delete(state.Threads, conversationID)
			delete(state.Goals, conversationID)
			deleteConversations[conversationID] = struct{}{}
			stats.Threads++
		}
	}

	for conversationID := range state.Goals {
		if _, ok := state.Threads[conversationID]; ok {
			continue
		}

		prune, err := shouldPruneThreadConversation(ctx, tx, conversationID, cutoff)
		if err != nil {
			return PruneStateStats{}, err
		}

		if prune {
			delete(state.Goals, conversationID)
		}
	}

	for key := range state.ResponseCheckpoints {
		if ts, ok := responseCheckpointTime(key); ok && ts.Before(cutoff) {
			delete(state.ResponseCheckpoints, key)
			stats.ResponseCheckpoints++
		}
	}

	for externalConversationID, session := range state.ExternalMCPSessions {
		conversationID := strings.TrimSpace(session.ConversationID)
		if conversationID == "" {
			delete(state.ExternalMCPSessions, externalConversationID)
			stats.ExternalMCPSessions++

			continue
		}

		prune, err := sessionLatestBefore(ctx, tx, conversationID, time.Unix(0, 0).UTC(), cutoff)
		if err != nil {
			return PruneStateStats{}, err
		}

		if prune {
			delete(state.ExternalMCPSessions, externalConversationID)

			deleteConversations[conversationID] = struct{}{}
			stats.ExternalMCPSessions++
		}
	}

	orphans, err := stalePrivateConversationIDs(ctx, tx, cutoff, state)
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
		delete(state.PendingRestartNotifications, conversationID)
		delete(state.Goals, conversationID)
	}

	stats.SessionRows = rows

	if err := saveRocketClawState(ctx, tx, state); err != nil {
		return PruneStateStats{}, err
	}

	if err := tx.Commit(); err != nil {
		return PruneStateStats{}, fmt.Errorf("commit state prune: %w", err)
	}

	return stats, nil
}

// ObserveEntries loads observed session entries through the runtime service.
func (s *SessionService) ObserveEntries(ctx context.Context, conversationID string, lastID int64) ([]ObservedSessionEntry, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		conversationID = mainConversationID
	}

	return observeSessionEntriesDB(ctx, s.db, conversationID, lastID)
}

// AppendEntryID appends one entry through the runtime service and returns its row ID.
func (s *SessionService) AppendEntryID(ctx context.Context, conversationID string, entry *harness.SessionEntry) (int64, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		conversationID = mainConversationID
	}

	return appendSessionEntryDB(ctx, s.db, conversationID, entry)
}

// Stop closes the runtime service and its SQLite handle.
func (s *SessionService) Stop(context.Context) error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close rocketcode session db: %w", err)
	}

	return nil
}

// Vacuum runs incremental SQLite vacuum through the runtime service handle.
func (s *SessionService) Vacuum(ctx context.Context) (VacuumStats, error) {
	beforePages, err := queryPragmaInt(ctx, s.db, "page_count")
	if err != nil {
		return VacuumStats{}, err
	}

	beforeFree, err := queryPragmaInt(ctx, s.db, "freelist_count")
	if err != nil {
		return VacuumStats{}, err
	}

	if _, err := s.db.ExecContext(ctx, `PRAGMA incremental_vacuum`); err != nil {
		return VacuumStats{}, fmt.Errorf("incremental vacuum rocketcode session db: %w", err)
	}

	afterPages, err := queryPragmaInt(ctx, s.db, "page_count")
	if err != nil {
		return VacuumStats{}, err
	}

	afterFree, err := queryPragmaInt(ctx, s.db, "freelist_count")
	if err != nil {
		return VacuumStats{}, err
	}

	return VacuumStats{DBExists: true, BeforePageCount: beforePages, BeforeFreePages: beforeFree, AfterPageCount: afterPages, AfterFreePages: afterFree}, nil
}

// CheckpointWAL checkpoints and truncates the SQLite WAL through the runtime service handle.
func (s *SessionService) CheckpointWAL(ctx context.Context) (WALCheckpointStats, error) {
	return checkpointWALDB(ctx, s.db)
}

func (s *SessionService) setGoalStatus(conversationID, status, note string) (GoalState, error) {
	conversationID = strings.TrimSpace(conversationID)

	var goal GoalState

	err := s.updateState(func(state *State) {
		current, ok := state.Goals[conversationID]
		if !ok || strings.TrimSpace(current.Status) != GoalStatusActive {
			goal = current
			return
		}

		current.Status = status
		current.Note = strings.TrimSpace(note)
		current.UpdatedAt = time.Now().UTC()
		state.Goals[conversationID] = current
		goal = current
	})
	if err != nil {
		return GoalState{}, err
	}

	return goal, nil
}

func (s *SessionService) updateState(mutate func(*State)) error {
	return s.updateStateContext(context.Background(), mutate)
}

func (s *SessionService) updateStateContext(ctx context.Context, mutate func(*State)) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin state update: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	state, err := loadRocketClawState(ctx, tx)
	if err != nil {
		return err
	}

	mutate(&state)

	if err := saveRocketClawState(ctx, tx, state); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit state update: %w", err)
	}

	return nil
}

func sessionDBPathIn(workspace, workDir string) string {
	return filepath.Join(workspace, workDir, "state.sqlite3")
}

func prepareSessionDBPathIn(workspace, workDir string) error {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return fmt.Errorf("open workspace root: %w", err)
	}

	defer func() { _ = root.Close() }()

	if err := root.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("create rocketcode session db dir: %w", err)
	}

	_, err = rootPathExistsNoSymlink(root, filepath.ToSlash(filepath.Join(workDir, "state.sqlite3")), "rocketcode session db")

	return err
}

func (s sqliteSessionStore) in() iter.Seq2[harness.SessionEntry, error] {
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
func (s sqliteSessionStore) outID(entry harness.SessionEntry) (int64, error) {
	return s.service.AppendEntryID(context.Background(), s.conversationID, &entry)
}

// ObserveSessionEntries returns replay entries and their row IDs after lastID.
func ObserveSessionEntries(ctx context.Context, dbPath, conversationID string, lastID int64) ([]ObservedSessionEntry, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		conversationID = mainConversationID
	}

	workspace := filepath.Dir(filepath.Dir(dbPath))
	workDir := filepath.Base(filepath.Dir(dbPath))

	db, ok, err := openExistingSessionDBReadOnly(ctx, workspace, workDir)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, nil
	}

	defer func() { _ = db.Close() }()

	return observeSessionEntriesDB(ctx, db, conversationID, lastID)
}

func observeSessionEntriesDB(ctx context.Context, db *sql.DB, conversationID string, lastID int64) ([]ObservedSessionEntry, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, entry_json FROM session_entries WHERE conversation_id = ? AND id > ? ORDER BY id`, conversationID, lastID)
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

// AppendSessionEntryID appends one replayable turn and returns its SQLite row ID.
func AppendSessionEntryID(ctx context.Context, dbPath, conversationID string, entry *harness.SessionEntry) (int64, error) {
	if entry == nil {
		return 0, errors.New("rocketcode session entry is required")
	}

	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		conversationID = mainConversationID
	}

	workspace := filepath.Dir(filepath.Dir(dbPath))

	workDir := filepath.Base(filepath.Dir(dbPath))
	if err := prepareSessionDBPathIn(workspace, workDir); err != nil {
		return 0, err
	}

	db, err := openSessionDB(ctx, dbPath)
	if err != nil {
		return 0, err
	}

	defer func() { _ = db.Close() }()

	return appendSessionEntryDB(ctx, db, conversationID, entry)
}

func appendSessionEntryDB(ctx context.Context, db stateStoreDB, conversationID string, entry *harness.SessionEntry) (int64, error) {
	data, err := json.Marshal(entry)
	if err != nil {
		return 0, fmt.Errorf("marshal rocketcode session entry: %w", err)
	}

	result, err := db.ExecContext(ctx, `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp) VALUES (?, ?, ?)`, conversationID, string(data), entry.Timestamp.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("append rocketcode session entry: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read appended rocketcode session entry id: %w", err)
	}

	return id, nil
}

// DeleteSessionIn removes all entries for one conversation ID in workDir and returns deleted rows.
func DeleteSessionIn(ctx context.Context, workspace, workDir, conversationID string) (int64, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return 0, errors.New("conversation ID is required")
	}

	db, ok, err := openExistingSessionDB(ctx, workspace, workDir)
	if err != nil {
		return 0, err
	}

	if !ok {
		return 0, nil
	}

	defer func() { _ = db.Close() }()

	result, err := db.ExecContext(ctx, `DELETE FROM session_entries WHERE conversation_id = ?`, conversationID)
	if err != nil {
		return 0, fmt.Errorf("delete rocketcode session: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count deleted rocketcode session rows: %w", err)
	}

	return rows, nil
}

func checkpointWALDB(ctx context.Context, db *sql.DB) (WALCheckpointStats, error) {
	var stats WALCheckpointStats
	if err := db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&stats.Busy, &stats.LogFrames, &stats.CheckpointedFrames); err != nil {
		return WALCheckpointStats{}, fmt.Errorf("checkpoint rocketcode session db WAL: %w", err)
	}

	return stats, nil
}

func openExistingSessionDB(ctx context.Context, workspace, workDir string) (*sql.DB, bool, error) {
	if err := prepareSessionDBPathIn(workspace, workDir); err != nil {
		return nil, false, err
	}

	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, false, fmt.Errorf("open workspace root: %w", err)
	}

	defer func() { _ = root.Close() }()

	ok, err := rootPathExistsNoSymlink(root, filepath.ToSlash(filepath.Join(workDir, "state.sqlite3")), "rocketcode session db")
	if err != nil || !ok {
		return nil, false, err
	}

	db, err := openSessionDB(ctx, sessionDBPathIn(workspace, workDir))

	return db, err == nil, err
}

// RecoverSessionDBIfCorrupt recovers a corrupt existing state DB before daemon startup proceeds.
func RecoverSessionDBIfCorrupt(ctx context.Context, workspace, workDir string) (bool, error) {
	db, ok, err := openExistingSessionDBReadOnly(ctx, workspace, workDir)
	switch {
	case err != nil:
		if !isSQLiteCorruptionError(err) {
			return false, err
		}
	case !ok:
		return false, nil
	default:
		err = quickCheckSessionDB(ctx, db)
		_ = db.Close()

		if err == nil {
			return false, nil
		}

		if !errors.Is(err, errStateStoreCorrupt) {
			return false, err
		}
	}

	if _, err := exec.LookPath("sqlite3"); err != nil {
		return false, fmt.Errorf("recover corrupt rocketcode session db: sqlite3 command not found: %w", err)
	}

	recoveryRel, err := snapshotSessionDBForRecovery(workspace, workDir)
	if err != nil {
		return false, err
	}

	if err := recoverSessionDBSnapshot(ctx, workspace, recoveryRel); err != nil {
		return false, err
	}

	if err := swapRecoveredSessionDB(workspace, workDir, recoveryRel); err != nil {
		return false, err
	}

	return true, nil
}

func quickCheckSessionDB(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA quick_check`)
	if err != nil {
		if isSQLiteCorruptionError(err) {
			return fmt.Errorf("%w: %w", errStateStoreCorrupt, err)
		}

		return fmt.Errorf("quick-check rocketcode session db: %w", err)
	}

	defer func() { _ = rows.Close() }()

	messages := []string{}

	for rows.Next() {
		var message string
		if err := rows.Scan(&message); err != nil {
			if isSQLiteCorruptionError(err) {
				return fmt.Errorf("%w: %w", errStateStoreCorrupt, err)
			}

			return fmt.Errorf("scan rocketcode session db quick-check: %w", err)
		}

		if strings.TrimSpace(message) != "ok" {
			messages = append(messages, message)
		}
	}

	if err := rows.Err(); err != nil {
		if isSQLiteCorruptionError(err) {
			return fmt.Errorf("%w: %w", errStateStoreCorrupt, err)
		}

		return fmt.Errorf("read rocketcode session db quick-check: %w", err)
	}

	if len(messages) > 0 {
		return fmt.Errorf("%w: quick_check failed: %s", errStateStoreCorrupt, strings.Join(messages, "; "))
	}

	return nil
}

func isSQLiteCorruptionError(err error) bool {
	text := strings.ToLower(err.Error())

	return strings.Contains(text, "database disk image is malformed") || strings.Contains(text, "sqlite_corrupt") || strings.Contains(text, "file is not a database") || strings.Contains(text, "malformed")
}

func snapshotSessionDBForRecovery(workspace, workDir string) (string, error) {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return "", fmt.Errorf("open workspace root: %w", err)
	}

	defer func() { _ = root.Close() }()

	recoveryRel := filepath.ToSlash(filepath.Join(workDir, "tmp", "sqlite-recovery-"+strconv.FormatInt(time.Now().UTC().UnixNano(), 10)))
	if err := root.MkdirAll(filepath.ToSlash(filepath.Join(workDir, "tmp")), 0o755); err != nil {
		return "", fmt.Errorf("create rocketcode session db recovery parent: %w", err)
	}

	if err := root.Mkdir(recoveryRel, 0o700); err != nil {
		return "", fmt.Errorf("create rocketcode session db recovery dir: %w", err)
	}

	for _, name := range []string{"state.sqlite3", "state.sqlite3-wal", "state.sqlite3-shm"} {
		required := name == "state.sqlite3"
		if err := copyRecoveryFile(root, filepath.ToSlash(filepath.Join(workDir, name)), filepath.ToSlash(filepath.Join(recoveryRel, name)), required); err != nil {
			return "", err
		}
	}

	return recoveryRel, nil
}

func copyRecoveryFile(root *os.Root, src, dst string, required bool) error {
	ok, err := rootPathExistsNoSymlink(root, src, "rocketcode session db recovery source")
	if err != nil || !ok {
		if !required && !ok {
			return nil
		}

		return err
	}

	in, err := root.Open(src)
	if err != nil {
		return fmt.Errorf("open rocketcode session db recovery source: %w", err)
	}

	defer func() { _ = in.Close() }()

	out, err := root.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create rocketcode session db recovery copy: %w", err)
	}

	_, errCopy := io.Copy(out, in)
	errClose := out.Close()

	if errCopy != nil {
		return fmt.Errorf("copy rocketcode session db recovery source: %w", errCopy)
	}

	if errClose != nil {
		return fmt.Errorf("close rocketcode session db recovery copy: %w", errClose)
	}

	return nil
}

func recoverSessionDBSnapshot(ctx context.Context, workspace, recoveryRel string) error {
	snapshotPath := filepath.Join(workspace, filepath.FromSlash(recoveryRel), "state.sqlite3")
	recoveredPath := filepath.Join(workspace, filepath.FromSlash(recoveryRel), "state.recovered.sqlite3")
	sqlPath := filepath.Join(workspace, filepath.FromSlash(recoveryRel), "recover.sql")

	sqlFile, err := os.OpenFile(sqlPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create rocketcode session db recovery sql: %w", err)
	}

	var stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, "sqlite3", snapshotPath, ".recover")
	cmd.Stdout = sqlFile
	cmd.Stderr = &stderr
	errRun := cmd.Run()
	errClose := sqlFile.Close()

	if errRun != nil {
		return fmt.Errorf("recover corrupt rocketcode session db with sqlite3: %w: %s", errRun, strings.TrimSpace(stderr.String()))
	}

	if errClose != nil {
		return fmt.Errorf("close rocketcode session db recovery sql: %w", errClose)
	}

	sqlInput, err := os.Open(sqlPath)
	if err != nil {
		return fmt.Errorf("open rocketcode session db recovery sql: %w", err)
	}

	stderr.Reset()

	cmd = exec.CommandContext(ctx, "sqlite3", recoveredPath)
	cmd.Stdin = sqlInput
	cmd.Stderr = &stderr
	errRun = cmd.Run()
	errClose = sqlInput.Close()

	if errRun != nil {
		return fmt.Errorf("build recovered rocketcode session db with sqlite3: %w: %s", errRun, strings.TrimSpace(stderr.String()))
	}

	if errClose != nil {
		return fmt.Errorf("close rocketcode session db recovery sql input: %w", errClose)
	}

	if err := validateRecoveredSessionDB(ctx, recoveredPath); err != nil {
		return err
	}

	db, err := openSessionDB(ctx, recoveredPath)
	if err != nil {
		return err
	}

	if err := quickCheckSessionDB(ctx, db); err != nil {
		_ = db.Close()
		return err
	}

	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = db.Close()
		return fmt.Errorf("checkpoint recovered rocketcode session db: %w", err)
	}

	if err := db.Close(); err != nil {
		return fmt.Errorf("close recovered rocketcode session db: %w", err)
	}

	return nil
}

func validateRecoveredSessionDB(ctx context.Context, recoveredPath string) error {
	db, err := openSessionDBReadOnly(ctx, recoveredPath)
	if err != nil {
		return err
	}

	defer func() { _ = db.Close() }()

	if err := quickCheckSessionDB(ctx, db); err != nil {
		return err
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = 'session_entries'`).Scan(&count); err != nil {
		return fmt.Errorf("check recovered rocketcode session entries table: %w", err)
	}

	if count == 0 {
		return errors.New("recovered rocketcode session db is missing session_entries")
	}

	rows, err := db.QueryContext(ctx, `SELECT id, conversation_id, entry_json, entry_timestamp FROM session_entries LIMIT 1`)
	if err != nil {
		return fmt.Errorf("validate recovered rocketcode session entries schema: %w", err)
	}

	defer func() { _ = rows.Close() }()

	if err := rows.Err(); err != nil {
		return fmt.Errorf("read recovered rocketcode session entries schema check: %w", err)
	}

	return nil
}

func swapRecoveredSessionDB(workspace, workDir, recoveryRel string) error {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return fmt.Errorf("open workspace root: %w", err)
	}

	defer func() { _ = root.Close() }()

	for _, name := range []string{"state.sqlite3", "state.sqlite3-wal", "state.sqlite3-shm"} {
		src := filepath.ToSlash(filepath.Join(workDir, name))

		ok, err := rootPathExistsNoSymlink(root, src, "rocketcode session db")
		if err != nil {
			return err
		}

		if !ok {
			continue
		}

		if err := root.Rename(src, filepath.ToSlash(filepath.Join(recoveryRel, "corrupt-"+name))); err != nil {
			return fmt.Errorf("move corrupt rocketcode session db aside: %w", err)
		}
	}

	if err := root.Rename(filepath.ToSlash(filepath.Join(recoveryRel, "state.recovered.sqlite3")), filepath.ToSlash(filepath.Join(workDir, "state.sqlite3"))); err != nil {
		return fmt.Errorf("install recovered rocketcode session db: %w", err)
	}

	return nil
}

func openExistingSessionDBReadOnly(ctx context.Context, workspace, workDir string) (*sql.DB, bool, error) {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, false, fmt.Errorf("open workspace root: %w", err)
	}

	defer func() { _ = root.Close() }()

	ok, err := rootPathExistsNoSymlink(root, filepath.ToSlash(filepath.Join(workDir, "state.sqlite3")), "rocketcode session db")
	if err != nil || !ok {
		return nil, false, err
	}

	db, err := openSessionDBReadOnly(ctx, sessionDBPathIn(workspace, workDir))

	return db, err == nil, err
}

func queryPragmaInt(ctx context.Context, db *sql.DB, name string) (int64, error) {
	var value int64
	if err := db.QueryRowContext(ctx, "PRAGMA "+name).Scan(&value); err != nil {
		return 0, fmt.Errorf("query sqlite pragma %s: %w", name, err)
	}

	return value, nil
}

// ListSessionsInOptions returns summaries for stored rocketcode sessions in workDir.
func ListSessionsInOptions(ctx context.Context, workspace, workDir string, options SessionListOptions) ([]SessionSummary, error) {
	db, ok, err := openExistingSessionDBReadOnly(ctx, workspace, workDir)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, nil
	}

	defer func() { _ = db.Close() }()

	query := `SELECT conversation_id, entry_json, entry_timestamp FROM session_entries ORDER BY conversation_id, id`

	var args []any

	if !options.Since.IsZero() || !options.Until.IsZero() || options.Limit > 0 {
		since := ""
		if !options.Since.IsZero() {
			since = options.Since.UTC().Format(time.RFC3339Nano)
		}

		until := ""
		if !options.Until.IsZero() {
			until = options.Until.UTC().Format(time.RFC3339Nano)
		}

		query = `WITH candidates AS (
	SELECT conversation_id, MAX(julianday(entry_timestamp)) AS last_updated
	FROM session_entries
	GROUP BY conversation_id
	HAVING (? = '' OR MAX(julianday(entry_timestamp)) >= julianday(?))
		AND (? = '' OR MAX(julianday(entry_timestamp)) < julianday(?))
	ORDER BY last_updated DESC, conversation_id
	LIMIT CASE WHEN ? > 0 THEN ? ELSE -1 END
)
SELECT se.conversation_id, se.entry_json, se.entry_timestamp
FROM session_entries se
JOIN candidates c ON c.conversation_id = se.conversation_id
ORDER BY c.last_updated DESC, c.conversation_id, se.id`
		args = []any{since, since, until, until, options.Limit, options.Limit}
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query rocketcode session summaries: %w", err)
	}

	defer func() { _ = rows.Close() }()

	summaryByID := map[string]*SessionSummary{}
	order := []string{}

	for rows.Next() {
		var conversationID, raw, timestamp string
		if err := rows.Scan(&conversationID, &raw, &timestamp); err != nil {
			return nil, fmt.Errorf("scan rocketcode session summary: %w", err)
		}

		summary := summaryByID[conversationID]
		if summary == nil {
			summary = &SessionSummary{ConversationID: conversationID}
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

	summaries := make([]SessionSummary, 0, len(order))
	for _, conversationID := range order {
		summaries = append(summaries, *summaryByID[conversationID])
	}

	return summaries, nil
}

// SessionDBPath returns the SQLite database path for rocketcode session inspection.
func SessionDBPath(workspace string) string { return SessionDBPathIn(workspace, config.DefaultWorkDir) }

// SessionDBPathIn returns the SQLite database path for rocketcode session inspection in workDir.
func SessionDBPathIn(workspace, workDir string) string { return sessionDBPathIn(workspace, workDir) }

// SlackThreadConversationID returns the stable conversation ID for a Slack thread.
func SlackThreadConversationID(channelID, threadTS string) string {
	return slackPairKey("slack-thread:", channelID, threadTS)
}

// SlackThreadTarget returns the Slack channel and thread timestamp for a Slack thread conversation ID.
func SlackThreadTarget(conversationID string) (channelID, threadTS string, ok bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(conversationID), "slack-thread:")
	if !ok {
		return "", "", false
	}

	channelID, threadTS, ok = strings.Cut(rest, ":")
	channelID, threadTS = strings.TrimSpace(channelID), strings.TrimSpace(threadTS)

	return channelID, threadTS, ok && channelID != "" && threadTS != ""
}

// SlackResponseCheckpointKey returns the stable key for one posted Slack AI response message.
func SlackResponseCheckpointKey(channelID, messageTS string) string {
	return slackPairKey("slack-response:", channelID, messageTS)
}

// DiscordThreadConversationID returns the stable conversation ID for a Discord thread.
func DiscordThreadConversationID(threadID string) string {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return ""
	}

	return "discord-thread:" + threadID
}

// DiscordResponseCheckpointKey returns the stable key for one posted Discord AI response message.
func DiscordResponseCheckpointKey(channelID, messageID string) string {
	channelID, messageID = strings.TrimSpace(channelID), strings.TrimSpace(messageID)
	if channelID == "" || messageID == "" {
		return ""
	}

	return "discord-response:" + channelID + ":" + messageID
}

func slackPairKey(prefix, channelID, ts string) string {
	channelID, ts = strings.TrimSpace(channelID), strings.TrimSpace(ts)
	if channelID == "" || ts == "" {
		return ""
	}

	return prefix + channelID + ":" + ts
}

func rootPathExistsNoSymlink(root *os.Root, path, label string) (bool, error) {
	info, err := root.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("stat %s: %w", label, err)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%s must not be a symlink", label)
	}

	return true, nil
}

func loadRocketClawState(ctx context.Context, db stateStoreDB) (State, error) {
	var raw string

	err := db.QueryRowContext(ctx, `SELECT value FROM session_meta WHERE key = ?`, "rocketclaw_state").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, nil
	}

	if err != nil {
		return State{}, fmt.Errorf("read persisted state: %w", err)
	}

	if strings.TrimSpace(raw) == "" {
		return State{}, nil
	}

	var state State
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return State{}, fmt.Errorf("parse persisted state: %w", err)
	}

	normalizeState(&state)

	return state, nil
}

func saveRocketClawState(ctx context.Context, db stateStoreDB, state State) error {
	normalizeState(&state)

	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal persisted state: %w", err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO session_meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, "rocketclaw_state", string(data)); err != nil {
		return fmt.Errorf("write persisted state: %w", err)
	}

	return nil
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
		created, ok = discordStateKeyTime(conversationID, "discord-thread:")
	}

	if !ok {
		return false, nil
	}

	return sessionLatestBefore(ctx, db, conversationID, created, cutoff)
}

func responseCheckpointTime(key string) (time.Time, bool) {
	if ts, ok := slackStateKeyTime(key, "slack-response:"); ok {
		return ts, true
	}

	return discordStateKeyTime(key, "discord-response:")
}

func discordStateKeyTime(key, prefix string) (time.Time, bool) {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, prefix) {
		return time.Time{}, false
	}

	id := key[len(prefix):]
	if i := strings.LastIndexByte(id, ':'); i >= 0 {
		id = id[i+1:]
	}

	snowflake, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return time.Time{}, false
	}

	return time.UnixMilli(int64((snowflake >> 22) + 1420070400000)).UTC(), true
}

func sessionLatestBefore(ctx context.Context, db stateStoreDB, conversationID string, fallback, cutoff time.Time) (bool, error) {
	var before bool

	err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(julianday(entry_timestamp)), julianday(?)) < julianday(?) FROM session_entries WHERE conversation_id = ?`, fallback.Format(time.RFC3339Nano), cutoff.Format(time.RFC3339Nano), conversationID).Scan(&before)
	if err != nil {
		return false, fmt.Errorf("read latest session entry timestamp: %w", err)
	}

	return before, nil
}

func stalePrivateConversationIDs(ctx context.Context, db *sql.Tx, cutoff time.Time, state State) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT conversation_id FROM session_entries WHERE conversation_id LIKE 'slack-thread:%' OR conversation_id LIKE 'discord-thread:%' OR conversation_id LIKE 'external_mcp:%' OR conversation_id LIKE 'cron:%' OR conversation_id LIKE 'one-off-cron:%' GROUP BY conversation_id HAVING MAX(julianday(entry_timestamp)) < julianday(?)`, cutoff.Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("query stale private session conversations: %w", err)
	}

	defer func() { _ = rows.Close() }()

	stale := []string{}

rowsLoop:
	for rows.Next() {
		var conversationID string
		if err := rows.Scan(&conversationID); err != nil {
			return nil, fmt.Errorf("scan stale private session conversation: %w", err)
		}

		if _, ok := state.Threads[conversationID]; ok {
			continue
		}

		for _, session := range state.ExternalMCPSessions {
			if strings.TrimSpace(session.ConversationID) == conversationID {
				continue rowsLoop
			}
		}

		stale = append(stale, conversationID)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read stale private session conversations: %w", err)
	}

	return stale, nil
}

func deleteSessionEntries(ctx context.Context, db stateStoreDB, conversationIDs map[string]struct{}) (int64, error) {
	var deleted int64

	for conversationID := range conversationIDs {
		result, err := db.ExecContext(ctx, `DELETE FROM session_entries WHERE conversation_id = ?`, conversationID)
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

func normalizeState(state *State) {
	if len(state.Threads) == 0 {
		state.Threads = nil
	}

	if len(state.ResponseCheckpoints) == 0 {
		state.ResponseCheckpoints = nil
	}

	if len(state.ExternalMCPSessions) == 0 {
		state.ExternalMCPSessions = nil
	}

	if len(state.ScheduledMessages) == 0 {
		state.ScheduledMessages = nil
	}

	if len(state.Goals) == 0 {
		state.Goals = nil
	}

	if len(state.PendingRestartNotifications) == 0 {
		state.PendingRestartNotifications = nil
	}
}

func openSessionDB(ctx context.Context, dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open rocketcode session db: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// page_size and auto_vacuum must precede schema creation for new databases.
	for _, statement := range []string{
		`PRAGMA busy_timeout = 30000`,
		`PRAGMA page_size = 4096`,
		`PRAGMA auto_vacuum = INCREMENTAL`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA cache_size = -64000`,
		`PRAGMA mmap_size = 268435456`,
		`PRAGMA temp_store = MEMORY`,
		`CREATE TABLE IF NOT EXISTS session_entries (id INTEGER PRIMARY KEY AUTOINCREMENT, conversation_id TEXT NOT NULL, entry_json TEXT NOT NULL, entry_timestamp TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS session_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS session_entries_conversation_id_id ON session_entries (conversation_id, id)`,
	} {
		deadline := time.Now().Add(30 * time.Second)

		for {
			_, err := db.ExecContext(ctx, statement)
			if err == nil {
				break
			}

			if !strings.Contains(err.Error(), "database is locked") || time.Now().After(deadline) {
				_ = db.Close()
				return nil, fmt.Errorf("initialize rocketcode session db: %w", err)
			}

			select {
			case <-ctx.Done():
				_ = db.Close()
				return nil, fmt.Errorf("initialize rocketcode session db: %w", ctx.Err())
			case <-time.After(50 * time.Millisecond):
			}
		}
	}

	return db, nil
}

func openSessionDBReadOnly(ctx context.Context, dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro"}).String())
	if err != nil {
		return nil, fmt.Errorf("open rocketcode session db read-only: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout = 30000`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize rocketcode session db read-only: %w", err)
	}

	return db, nil
}

func openWorkspaceSessionDB(ctx context.Context, workspace, workDir string) (*sql.DB, error) {
	if err := prepareSessionDBPathIn(workspace, workDir); err != nil {
		return nil, err
	}

	return openSessionDB(ctx, sessionDBPathIn(workspace, workDir))
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
