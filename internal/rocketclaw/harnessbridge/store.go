package harnessbridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
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

// State is the persisted rocketclaw session state.
type State struct {
	Threads                     map[string]ThreadState             `json:"threads,omitempty"`
	ResponseCheckpoints         map[string]ResponseCheckpointState `json:"response_checkpoints,omitempty"`
	ExternalMCPSessions         map[string]ExternalMCPSessionState `json:"external_mcp_sessions,omitempty"`
	ScheduledMessages           map[string]ScheduledMessageState   `json:"scheduled_messages,omitempty"`
	PendingRestartNotifications map[string]bool                    `json:"pending_restart_notifications,omitempty"`
}

// ThreadState is the persisted state for one Slack thread bridge.
type ThreadState struct {
	Agent              string `json:"agent,omitempty"`
	SeededFromResponse string `json:"seeded_from_response,omitempty"`
}

// ResponseCheckpointState records enough metadata to seed a Slack response-rooted thread.
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

// NewSessionService starts a runtime-owned SQLite session service.
func NewSessionService(workspace string) (*SessionService, error) {
	return NewSessionServiceIn(workspace, config.DefaultWorkDir)
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

// UpsertThread records or updates a Slack thread bridge entry.
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

// MarkThreadSeeded records the response checkpoint used to seed a Slack thread.
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

// UpsertResponseCheckpoint records a Slack response checkpoint.
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
		prune, err := shouldPruneSlackConversation(ctx, tx, conversationID, cutoff)
		if err != nil {
			return PruneStateStats{}, err
		}

		if prune {
			delete(state.Threads, conversationID)
			deleteConversations[conversationID] = struct{}{}
			stats.Threads++
		}
	}

	for key := range state.ResponseCheckpoints {
		if ts, ok := slackStateKeyTime(key, "slack-response:"); ok && ts.Before(cutoff) {
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

func workDirName(workDir string) string {
	if strings.TrimSpace(workDir) != "" {
		return workDir
	}

	return config.DefaultWorkDir
}

func sessionDBPath(workspace string) string {
	return sessionDBPathIn(workspace, config.DefaultWorkDir)
}

func sessionDBPathIn(workspace, workDir string) string {
	return filepath.Join(workspace, workDirName(workDir), "state.sqlite3")
}

func prepareSessionDBPath(workspace string) error {
	return prepareSessionDBPathIn(workspace, config.DefaultWorkDir)
}

func prepareSessionDBPathIn(workspace, workDir string) error {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return fmt.Errorf("open workspace root: %w", err)
	}

	defer func() { _ = root.Close() }()

	if err := root.MkdirAll(workDirName(workDir), 0o755); err != nil {
		return fmt.Errorf("create rocketcode session db dir: %w", err)
	}

	_, err = rootPathExistsNoSymlink(root, filepath.ToSlash(filepath.Join(workDirName(workDir), "state.sqlite3")), "rocketcode session db")

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
	if err := prepareSessionDBPathIn(workspace, workDir); err != nil {
		return nil, err
	}

	db, err := openSessionDB(ctx, dbPath)
	if err != nil {
		return nil, err
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

// DeleteSession removes all entries for one conversation ID and returns deleted rows.
func DeleteSession(ctx context.Context, workspace, conversationID string) (int64, error) {
	return DeleteSessionIn(ctx, workspace, config.DefaultWorkDir, conversationID)
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

// VacuumSessions runs explicit SQLite maintenance for the rocketcode session DB.
func VacuumSessions(ctx context.Context, workspace string) (VacuumStats, error) {
	return VacuumSessionsIn(ctx, workspace, config.DefaultWorkDir)
}

// VacuumSessionsIn runs explicit SQLite maintenance for the rocketcode session DB in workDir.
func VacuumSessionsIn(ctx context.Context, workspace, workDir string) (VacuumStats, error) {
	db, ok, err := openExistingSessionDB(ctx, workspace, workDir)
	if err != nil {
		return VacuumStats{}, err
	}

	if !ok {
		return VacuumStats{}, nil
	}

	defer func() { _ = db.Close() }()

	beforePages, err := queryPragmaInt(ctx, db, "page_count")
	if err != nil {
		return VacuumStats{}, err
	}

	beforeFree, err := queryPragmaInt(ctx, db, "freelist_count")
	if err != nil {
		return VacuumStats{}, err
	}

	if _, err := db.ExecContext(ctx, `PRAGMA optimize`); err != nil {
		return VacuumStats{}, fmt.Errorf("optimize rocketcode session db: %w", err)
	}

	if _, err := db.ExecContext(ctx, `VACUUM`); err != nil {
		return VacuumStats{}, fmt.Errorf("vacuum rocketcode session db: %w", err)
	}

	afterPages, err := queryPragmaInt(ctx, db, "page_count")
	if err != nil {
		return VacuumStats{}, err
	}

	afterFree, err := queryPragmaInt(ctx, db, "freelist_count")
	if err != nil {
		return VacuumStats{}, err
	}

	return VacuumStats{DBExists: true, BeforePageCount: beforePages, BeforeFreePages: beforeFree, AfterPageCount: afterPages, AfterFreePages: afterFree}, nil
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

	ok, err := rootPathExistsNoSymlink(root, filepath.ToSlash(filepath.Join(workDirName(workDir), "state.sqlite3")), "rocketcode session db")
	if err != nil || !ok {
		return nil, false, err
	}

	db, err := openSessionDB(ctx, sessionDBPathIn(workspace, workDir))

	return db, err == nil, err
}

func queryPragmaInt(ctx context.Context, db *sql.DB, name string) (int64, error) {
	var value int64
	if err := db.QueryRowContext(ctx, "PRAGMA "+name).Scan(&value); err != nil {
		return 0, fmt.Errorf("query sqlite pragma %s: %w", name, err)
	}

	return value, nil
}

// ListSessions returns summaries for all stored rocketcode sessions.
func ListSessions(ctx context.Context, workspace string) ([]SessionSummary, error) {
	return ListSessionsIn(ctx, workspace, config.DefaultWorkDir)
}

// ListSessionsIn returns summaries for all stored rocketcode sessions in workDir.
func ListSessionsIn(ctx context.Context, workspace, workDir string) ([]SessionSummary, error) {
	if err := prepareSessionDBPathIn(workspace, workDir); err != nil {
		return nil, err
	}

	dbPath := sessionDBPathIn(workspace, workDir)

	db, err := openSessionDB(ctx, dbPath)
	if err != nil {
		return nil, err
	}

	defer func() { _ = db.Close() }()

	rows, err := db.QueryContext(ctx, `SELECT conversation_id, entry_json, entry_timestamp FROM session_entries ORDER BY conversation_id, id`)
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
func SessionDBPath(workspace string) string { return sessionDBPath(workspace) }

// SessionDBPathIn returns the SQLite database path for rocketcode session inspection in workDir.
func SessionDBPathIn(workspace, workDir string) string { return sessionDBPathIn(workspace, workDir) }

// SlackThreadConversationID returns the stable conversation ID for a Slack thread.
func SlackThreadConversationID(channelID, threadTS string) string {
	return slackPairKey("slack-thread:", channelID, threadTS)
}

// SlackResponseCheckpointKey returns the stable key for one posted Slack AI response message.
func SlackResponseCheckpointKey(channelID, messageTS string) string {
	return slackPairKey("slack-response:", channelID, messageTS)
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

func shouldPruneSlackConversation(ctx context.Context, db stateStoreDB, conversationID string, cutoff time.Time) (bool, error) {
	created, ok := slackStateKeyTime(conversationID, "slack-thread:")
	if !ok {
		return false, nil
	}

	return sessionLatestBefore(ctx, db, conversationID, created, cutoff)
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
	rows, err := db.QueryContext(ctx, `SELECT conversation_id FROM session_entries WHERE conversation_id LIKE 'slack-thread:%' OR conversation_id LIKE 'external_mcp:%' OR conversation_id LIKE 'cron:%' OR conversation_id LIKE 'one-off-cron:%' GROUP BY conversation_id HAVING MAX(julianday(entry_timestamp)) < julianday(?)`, cutoff.Format(time.RFC3339Nano))
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

	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout = 5000; CREATE TABLE IF NOT EXISTS session_entries (id INTEGER PRIMARY KEY AUTOINCREMENT, conversation_id TEXT NOT NULL, entry_json TEXT NOT NULL, entry_timestamp TEXT NOT NULL); CREATE TABLE IF NOT EXISTS session_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL); CREATE INDEX IF NOT EXISTS session_entries_conversation_id_id ON session_entries (conversation_id, id);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize rocketcode session db: %w", err)
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
