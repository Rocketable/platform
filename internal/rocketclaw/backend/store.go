package backend

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"cirello.io/pglock"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/stdlib"
)

const (
	runLockName  = "rocketclaw-run"
	runLockTable = "rocketclaw_locks"
)

const (
	GoalStatusActive          = "active"
	GoalStatusProgress        = "progress"
	GoalStatusComplete        = "complete"
	GoalStatusBlocked         = "blocked"
	GoalStatusStopped         = "stopped"
	GoalStatusBudgetExhausted = "budget_exhausted"
)

type ThreadState struct {
	Agent     string        `json:"agent,omitempty"`
	CreatedBy ThreadCreator `json:"created_by,omitempty"`
	parent    string
	sticky    harness.AgentSticky
}

func (t ThreadState) Sticky() harness.AgentSticky {
	return t.sticky
}

type ThreadCreator string

const ThreadCreatedByCron ThreadCreator = "cron"

type ExternalMCPSessionState struct {
	Agent                 string            `json:"agent,omitempty"`
	PrivateConversationID string            `json:"private_conversation_id,omitempty"`
	ManagedConversationID string            `json:"managed_conversation_id,omitempty"`
	OriginPairs           map[string]string `json:"origin_pairs,omitempty"`
}

type CronScheduleState struct {
	ScheduleID   string
	RelativePath string
	NextDue      time.Time
}

type GoalState struct {
	Objective      string    `json:"objective,omitempty"`
	MaxTurns       int       `json:"max_turns,omitempty"`
	TurnsUsed      int       `json:"turns_used,omitempty"`
	Status         string    `json:"status,omitempty"`
	Note           string    `json:"note,omitempty"`
	CreatedAt      time.Time `json:"created_at,omitzero"`
	UpdatedAt      time.Time `json:"updated_at,omitzero"`
	PrincipalName  string    `json:"principal_name,omitempty"`
	PrincipalEmail string    `json:"principal_email,omitempty"`
	Origin         string    `json:"origin,omitempty"`
}

type SessionService struct {
	db             *sql.DB
	log            *slog.Logger
	attachments    attachmentStorage
	definitions    *config.LockedConfig
	jobs           conversationJobs
	historyWorkers interface {
		pauseHistory(context.Context, []string) ([]*Bridge, error)
		historyBusy([]string) bool
	}
	spillDir string
	shells   emulatedShells

	lastTurnIDMu sync.Mutex
	lastTurnID   int64

	scheduledMu sync.Mutex
	scheduled   []string
}

type ObservedSessionEntry struct {
	ID                   int64
	Entry                harness.SessionEntry
	Key                  string
	Revision             string
	SourceConversationID string
	Synced               bool
	Active               bool
	Terminal             protocol.Terminal
}

type PruneStateStats struct {
	Threads, ExternalMCPSessions int
	SessionRows                  int64
}

type stateStoreDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func queryScalar(ctx context.Context, db stateStoreDB, op, query string, dest any, args ...any) error {
	if err := db.QueryRowContext(ctx, query, args...).Scan(dest); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}

	return nil
}

func execState(ctx context.Context, db stateStoreDB, op, query string, args ...any) (sql.Result, error) {
	result, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}

	return result, nil
}

func execRows(ctx context.Context, db stateStoreDB, execOp, query string, args ...any) (int64, error) {
	result, err := execState(ctx, db, execOp, query, args...)
	if err != nil {
		return 0, err
	}

	n, _ := result.RowsAffected()

	return n, nil
}

func NewSessionServiceIn(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*SessionService, error) {
	db, err := openSessionDB(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}

	service, err := newSessionService(ctx, cfg, db, logger)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}

	return service, nil
}

func newSessionService(ctx context.Context, cfg *config.Config, db *sql.DB, logger *slog.Logger) (*SessionService, error) {
	driver, location, err := cfg.AttachmentLocation()
	if err != nil {
		return nil, fmt.Errorf("configure attachment storage: %w", err)
	}

	var attachments attachmentStorage = filesystemAttachments{path: location}

	if driver == config.S3Attachments {
		aws, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("configure attachment S3 storage: %w", err)
		}

		attachments = s3Attachments{client: s3.NewFromConfig(aws), bucket: location}
	}

	if err := initializeSessionDB(ctx, db, logger); err != nil {
		return nil, hideDSN(err, cfg.DatabaseURL, "initialize rocketclaw state store")
	}

	return &SessionService{db: db, log: logger, attachments: attachments, definitions: config.NewLockedConfig(cfg), jobs: noConversationJobs{}, historyWorkers: noHistoryWorkers{}, spillDir: rocketcodeSpillDir(cfg)}, nil
}

func (s *SessionService) UpsertThread(conversationID string, thread ThreadState) error {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return errors.New("thread conversation ID is required")
	}

	thread.Agent = strings.TrimSpace(thread.Agent)
	thread.CreatedBy = ThreadCreator(strings.TrimSpace(string(thread.CreatedBy)))

	ctx := context.Background()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin thread upsert: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, conversationID); err != nil {
		return err
	}

	summary, err := loadSessionSummary(ctx, tx, conversationID)
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed) VALUES ($1, $2, $3, $4, TRUE) ON CONFLICT(conversation_id) DO UPDATE SET agent = excluded.agent, created_by = CASE WHEN excluded.created_by = '' THEN conversations.created_by ELSE excluded.created_by END`, conversationID, thread.Agent, string(thread.CreatedBy), KindWebChat); err != nil {
		return fmt.Errorf("upsert managed conversation: %w", err)
	}

	if err := saveSessionSummary(ctx, tx, summary); err != nil {
		return err
	}

	if _, err := s.reconcileShadows(ctx, tx, conversationID, thread.Agent); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit thread upsert: %w", err)
	}

	return nil
}

func (s *SessionService) BeginGoal(conversationID string, goal *GoalState) error {
	conversationID = strings.TrimSpace(conversationID)
	goal.Objective = strings.TrimSpace(goal.Objective)

	if conversationID == "" {
		return errors.New("goal conversation ID is required")
	}

	if goal.Objective == "" {
		return errors.New("goal objective is required")
	}

	goal.MaxTurns = max(goal.MaxTurns, 0)

	return beginGoalDB(context.Background(), s.db, conversationID, goal)
}

func beginGoalDB(ctx context.Context, db stateStoreDB, conversationID string, goal *GoalState) error {
	now := time.Now().UTC()

	rows, err := execRows(ctx, db, "begin goal", `UPDATE conversations SET goal_objective = $2, goal_max_turns = $3, goal_turns_used = 0, goal_status = $4, goal_note = '', goal_created_at_unix_ns = $5, goal_updated_at_unix_ns = $5, goal_principal_name = $6, goal_principal_email = $7, goal_origin = $8 WHERE conversation_id = $1 AND COALESCE(goal_status NOT IN ('', $4), TRUE)`, conversationID, goal.Objective, goal.MaxTurns, GoalStatusActive, timeUnixNano(now), goal.PrincipalName, goal.PrincipalEmail, goal.Origin)
	if err != nil {
		return err
	}

	if rows <= 0 {
		return protocol.ErrGoalAlreadyActive
	}

	return nil
}

func (s *SessionService) Goal(conversationID string) (GoalState, bool, error) {
	return stateDAO{db: s.db}.goal(context.Background(), conversationID)
}

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

func (s *SessionService) StopGoal(conversationID string) error {
	_, err := s.setGoalStatus(conversationID, GoalStatusStopped, "stopped by human")
	return err
}

func (s *SessionService) RegisterExternalMCPConversation(externalConversationID, managedAgent string, session *ExternalMCPSessionState) error {
	externalConversationID = strings.TrimSpace(externalConversationID)
	managedAgent = strings.TrimSpace(managedAgent)
	session.Agent = strings.TrimSpace(session.Agent)
	session.PrivateConversationID = strings.TrimSpace(session.PrivateConversationID)
	session.ManagedConversationID = strings.TrimSpace(session.ManagedConversationID)

	ctx := context.Background()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin external MCP conversation registration: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := lockHistories(ctx, tx, []string{session.ManagedConversationID, session.PrivateConversationID}); err != nil {
		return err
	}

	managed, private := protocol.Conversation{ID: session.ManagedConversationID, Agent: managedAgent}, protocol.Conversation{ID: session.PrivateConversationID, Agent: session.Agent}

	created, err := (stateDAO{db: tx}).createConversation(ctx, managed, conversationFacts{kind: KindWebChat}, harness.AgentStickyOff)
	if err != nil {
		return err
	}

	if !created {
		return fmt.Errorf("register external MCP managed conversation %q: already recorded", managed.ID)
	}

	if _, err := (stateDAO{db: tx}).createConversation(ctx, private, conversationFacts{kind: KindExternalMCPRun}, harness.AgentStickyOff); err != nil {
		return err
	}

	for _, c := range []protocol.Conversation{managed, private} {
		if _, err := s.reconcileShadows(ctx, tx, c.ID, c.Agent); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET external_id = $1, external_run = NULLIF($2::text, ''), external_agent = $4, origin_pairs = $5 WHERE conversation_id = $3`, externalConversationID, session.PrivateConversationID, session.ManagedConversationID, session.Agent, originPairsJSON(session.OriginPairs)); err != nil {
		return fmt.Errorf("register external MCP binding: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit external MCP conversation registration: %w", err)
	}

	return nil
}

func (s *SessionService) RemoveExternalMCPConversation(externalConversationID string) error {
	ctx := context.Background()

	session, ok, err := (stateDAO{db: s.db}).externalMCPSession(ctx, externalConversationID)
	if err != nil {
		return err
	}

	if !ok {
		return nil
	}

	tx, ids, workers, err := s.beginHistoryMutation(ctx, []string{session.PrivateConversationID, session.ManagedConversationID}, false)
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()
	defer releaseHistory(workers)

	if _, err := deleteOwnedHistory(ctx, tx, ids); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit external MCP conversation cleanup: %w", err)
	}

	return s.removeHistoryFiles(ctx, ids)
}

func (s *SessionService) UpdateConversationDetails(ctx context.Context, conversationID string, name *string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin conversation details update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if name != nil {
		var server, id string

		err := tx.QueryRowContext(ctx, `SELECT server, remote_session FROM conversations WHERE conversation_id = $1 AND kind = $2 FOR UPDATE`, conversationID, KindRemote).Scan(&server, &id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("read opencode session server: %w", err)
		}

		if err == nil {
			upstream, ok := s.definitions.Clone().OpenCode[server]
			if !ok {
				return false, fmt.Errorf("opencode server %q is not configured", server)
			}

			body := struct {
				Title string `json:"title"`
			}{Title: *name}

			if err := openCodeCall(ctx, server, upstream, http.MethodPatch, "/api/session/"+url.PathEscape(id), &body, nil); err != nil {
				return false, err
			}
		}
	}

	rows, err := execRows(ctx, tx, "update conversation details", `UPDATE conversations SET name = COALESCE($2, name),
    title = CASE WHEN kind = $3 THEN COALESCE($2, title) ELSE title END,
    synced_at = CASE WHEN kind = $3 AND $2 IS NOT NULL THEN clock_timestamp() ELSE synced_at END WHERE conversation_id = $1`, conversationID, name, KindRemote)
	if err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit conversation details update: %w", err)
	}

	return rows > 0, nil
}

func (s *SessionService) ExternalMCPSession(externalConversationID string) (ExternalMCPSessionState, bool, error) {
	return stateDAO{db: s.db}.externalMCPSession(context.Background(), externalConversationID)
}

func (s *SessionService) ScheduledMessagesForConversation(conversationID string) (map[string]protocol.ScheduledMessageState, error) {
	return stateDAO{db: s.db}.scheduledMessages(context.Background(), conversationID)
}

func (s *SessionService) PutScheduledMessage(id string, message *protocol.ScheduledMessageState) error {
	return stateDAO{db: s.db}.putScheduledMessage(context.Background(), id, message)
}

func (s *SessionService) ResetScheduledMessages(conversationID string) error {
	return stateDAO{db: s.db}.resetScheduledMessages(context.Background(), conversationID)
}

func (s *SessionService) ClaimScheduledMessage(id, conversationID string, dueAt, now time.Time) (message protocol.ScheduledMessageState, claimed bool, err error) {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return protocol.ScheduledMessageState{}, false, fmt.Errorf("begin scheduled message claim: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	id = strings.TrimSpace(id)
	row := tx.QueryRowContext(context.Background(), `WITH `+remindersSQL+` SELECT * FROM reminders WHERE id = $1`, id)

	_, message, err = (scheduledMessageScanner{}).scan(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return protocol.ScheduledMessageState{}, false, nil
		}

		return protocol.ScheduledMessageState{}, false, err
	}

	if message.ConversationID != strings.TrimSpace(conversationID) || !message.DueAt.Equal(dueAt) {
		return protocol.ScheduledMessageState{}, false, nil
	}

	if err := lockSessionHistory(context.Background(), tx, conversationID); err != nil {
		return protocol.ScheduledMessageState{}, false, err
	}

	var marker string
	if err := queryScalar(context.Background(), tx, "read scheduled cutoff", `SELECT COALESCE(max(revert_message_id), '') FROM conversations WHERE conversation_id = $1`, &marker, conversationID); err != nil {
		return protocol.ScheduledMessageState{}, false, err
	}

	if marker != "" {
		return protocol.ScheduledMessageState{}, false, nil
	}

	if message.Recurring {
		message.DueAt = now.UTC().Add(message.Interval)
		if _, err := execState(context.Background(), tx, "advance scheduled message", `WITH parked AS (UPDATE turns SET park_after = '' WHERE park_after = $1)
UPDATE schedules SET next_due_unix_ns = $2 WHERE id = $1`, id, timeUnixNano(message.DueAt)); err != nil {
			return protocol.ScheduledMessageState{}, false, err
		}
	}

	if err := tx.Commit(); err != nil {
		return protocol.ScheduledMessageState{}, false, fmt.Errorf("commit scheduled message claim: %w", err)
	}

	return message, true, nil
}

func (s *SessionService) SyncCronSchedules(schedules []CronScheduleState) error {
	ids, paths, due := make([]string, len(schedules)), make([]string, len(schedules)), make([]int64, len(schedules))
	for i, schedule := range schedules {
		ids[i], paths[i], due[i] = strings.TrimSpace(schedule.ScheduleID), strings.TrimSpace(schedule.RelativePath), timeUnixNano(schedule.NextDue)
	}

	_, err := execState(context.Background(), s.db, "sync cron schedules", `WITH observed AS (
    INSERT INTO schedules (id, cron_path, next_due_unix_ns) SELECT * FROM unnest($1::text[], $2::text[], $3::bigint[])
    ON CONFLICT (id) DO UPDATE SET cron_path = excluded.cron_path
)
DELETE FROM schedules WHERE cron_path IS NOT NULL AND id <> ALL($1)`, ids, paths, due)

	return err
}

func (s *SessionService) ScheduleAgents(agents []string) {
	s.scheduledMu.Lock()
	s.scheduled = agents
	s.scheduledMu.Unlock()
}

func (s *SessionService) ResetCronSchedules() error {
	_, err := execState(context.Background(), s.db, "reset cron schedules", `DELETE FROM schedules WHERE cron_path IS NOT NULL`)
	return err
}

func (s *SessionService) DueCronSchedules(now time.Time) ([]CronScheduleState, error) {
	return queryRows(context.Background(), s.db, `SELECT id, cron_path, next_due_unix_ns FROM schedules WHERE cron_path IS NOT NULL AND next_due_unix_ns <= $1 ORDER BY next_due_unix_ns, id`, "due cron schedules", scanCronSchedule, timeUnixNano(now))
}

func (s *SessionService) ClaimCronSchedule(due CronScheduleState, nextDue, now time.Time) (bool, error) {
	var claimed bool

	err := queryScalar(context.Background(), s.db, "claim cron schedule", `WITH advanced AS (
    UPDATE schedules SET next_due_unix_ns = $4 WHERE id = $1 AND cron_path = $2 AND next_due_unix_ns = $3 AND next_due_unix_ns <= $5 RETURNING cron_path
), running AS (
    SELECT t.id FROM advanced a JOIN conversations c ON c.cron_path = a.cron_path AND c.kind = $6
    JOIN turns t USING (conversation_id) WHERE t.takes_lane AND t.phase NOT IN ('waiting', 'done')
)
SELECT count(*) > 0 FROM advanced LEFT JOIN running r ON TRUE WHERE r.id IS NULL`, &claimed, strings.TrimSpace(due.ScheduleID), strings.TrimSpace(due.RelativePath),
		timeUnixNano(due.NextDue), timeUnixNano(nextDue), timeUnixNano(now), KindCronRun)

	return claimed, err
}

func (s *SessionService) ActiveGoalThreads() (map[string]ThreadState, error) {
	return queryMap(context.Background(), s.db, `SELECT conversation_id, agent, created_by FROM conversations
WHERE goal_status = '' OR goal_status = $1 ORDER BY conversation_id`, "active goal threads", activeGoalThreadScanner{}, GoalStatusActive)
}

type activeGoalThreadScanner struct{}

func (activeGoalThreadScanner) scan(row rowScanner) (string, ThreadState, error) {
	var (
		conversationID string
		thread         ThreadState
	)
	if err := row.Scan(&conversationID, &thread.Agent, &thread.CreatedBy); err != nil {
		return "", ThreadState{}, fmt.Errorf("scan active goal thread: %w", err)
	}

	return conversationID, thread, nil
}

type pruneKind int

const (
	pruneThread pruneKind = iota
	pruneGoal
	pruneExternalMCP
	pruneOrphan
	pruneSticky
)

type pruneCandidate struct {
	kind        pruneKind
	id, private string
	threads     int
}

func (c pruneCandidate) stale(ctx context.Context, db stateStoreDB, cutoff time.Time) (bool, error) {
	switch c.kind {
	case pruneSticky:
		return true, nil
	case pruneOrphan:
		orphans, err := stalePrivateConversationIDs(ctx, db, cutoff, c.id)
		return len(orphans) > 0, err
	case pruneGoal:
		var recorded bool
		if err := queryScalar(ctx, db, "read goal conversation", `SELECT count(*) > 0 FROM conversations WHERE conversation_id = $1 AND retention = 'record'`, &recorded, c.id); err != nil || recorded {
			return false, err
		}
	case pruneThread, pruneExternalMCP:
	}

	prune, err := shouldPruneThreadConversation(ctx, db, c.id, cutoff)
	if err != nil || !prune || c.private == "" {
		return prune, err
	}

	pending, err := conversationHasPendingWork(ctx, db, c.private)
	if err != nil || pending {
		return false, err
	}

	return sessionLatestBefore(ctx, db, c.private, time.Unix(0, 0).UTC(), cutoff)
}

func (s *SessionService) PruneStateBefore(ctx context.Context, cutoff time.Time, pace time.Duration) (PruneStateStats, error) {
	stickyIDs, err := queryStrings(ctx, s.db, `SELECT conversation_id FROM conversations WHERE sticky <> 'off' ORDER BY conversation_id`, "sticky conversations")
	if err != nil {
		return PruneStateStats{}, err
	}

	threadIDs, err := queryStrings(ctx, s.db, `SELECT conversation_id FROM conversations WHERE retention = 'record' ORDER BY conversation_id`, "managed conversation IDs")
	if err != nil {
		return PruneStateStats{}, err
	}

	externalSessions, err := queryMap(ctx, s.db, externalMCPSessionSQL+` ORDER BY external_id`, "external MCP sessions", externalMCPSessionScanner{})
	if err != nil {
		return PruneStateStats{}, err
	}

	goalIDs, err := queryStrings(ctx, s.db, `SELECT conversation_id FROM conversations WHERE goal_objective IS NOT NULL ORDER BY conversation_id`, "goal conversation IDs")
	if err != nil {
		return PruneStateStats{}, err
	}

	orphans, err := stalePrivateConversationIDs(ctx, s.db, cutoff, "")
	if err != nil {
		return PruneStateStats{}, err
	}

	managed, external := map[string]bool{}, map[string]bool{}
	for _, id := range threadIDs {
		managed[id] = true
	}

	var candidates []pruneCandidate
	for _, id := range stickyIDs {
		candidates = append(candidates, pruneCandidate{kind: pruneSticky, id: id})
	}

	for _, key := range slices.Sorted(maps.Keys(externalSessions)) {
		session := externalSessions[key]
		c := pruneCandidate{kind: pruneExternalMCP, id: strings.TrimSpace(session.ManagedConversationID), private: strings.TrimSpace(session.PrivateConversationID)}
		external[c.id], external[c.private] = true, true

		for _, id := range []string{c.id, c.private} {
			if id != "" && managed[id] {
				c.threads++
			}
		}

		candidates = append(candidates, c)
	}

	for _, id := range threadIDs {
		if !external[id] && !slices.Contains(stickyIDs, id) {
			candidates = append(candidates, pruneCandidate{kind: pruneThread, id: id, threads: 1})
		}
	}

	for _, id := range goalIDs {
		if !managed[id] {
			candidates = append(candidates, pruneCandidate{kind: pruneGoal, id: id})
		}
	}

	for _, id := range orphans {
		candidates = append(candidates, pruneCandidate{kind: pruneOrphan, id: id})
	}

	var (
		stats       PruneStateStats
		errCombined error
	)

	for _, c := range candidates {
		stale, err := c.stale(ctx, s.db, cutoff)
		pruned := false

		if err == nil && stale {
			var rows int64

			rows, pruned, err = s.pruneCandidate(ctx, c, cutoff)
			if pruned {
				stats.Threads += c.threads
				stats.SessionRows += rows

				if c.kind == pruneExternalMCP {
					stats.ExternalMCPSessions++
				}
			}
		}

		errCombined = errors.Join(errCombined, err)

		wait := pace / 10
		if pruned {
			wait = pace
		}

		select {
		case <-ctx.Done():
			return stats, errors.Join(errCombined, ctx.Err())
		case <-time.After(wait):
		}
	}

	return stats, errors.Join(errCombined, s.removeHistoryFiles(ctx, nil))
}

func (s *SessionService) ObserveEntries(ctx context.Context, conversationID string) ([]ObservedSessionEntry, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, errors.New("conversation ID is required")
	}

	entries, err := (stateDAO{db: s.db}).observedEntries(ctx, conversationID)
	if err == nil && entries == nil {
		entries = []ObservedSessionEntry{}
	}

	return entries, err
}

func (s *SessionService) AppendEntryID(ctx context.Context, conversationID string, entry *harness.SessionEntry) (int64, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return 0, errors.New("conversation ID is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin session append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	id, err := appendSessionEntryDB(ctx, tx, conversationID, entry)
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit session append: %w", err)
	}

	return id, nil
}

func revertFloorSuffix(ctx context.Context, db stateStoreDB, conversationID string) (floor, first string, err error) {
	var eligible bool
	if _, eligible, _, floor, err = revertStateDB(ctx, db, conversationID); err != nil || floor == "" || !eligible {
		return "", "", err
	}

	err = queryScalar(ctx, db, "read first message after the revert floor", `WITH `+sessionHistorySQL+`, after AS (
    SELECT e.id, items.ordinal - 1 AS idx FROM physical_entries e CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry->'replay_input', 'null'::jsonb)) WITH ORDINALITY AS items(item, ordinal)
    WHERE item->>'role' = 'user' AND (e.id > split_part($2, ':', 1)::bigint OR e.id = split_part($2, ':', 1)::bigint AND items.ordinal - 1 > split_part($2, ':', 2)::integer)
    ORDER BY e.id, items.ordinal LIMIT 1
)
SELECT COALESCE(max(id::text || ':' || idx::text), '') FROM after`, &first, conversationID, floor)

	return floor, first, err
}

func (s *SessionService) DeleteSession(ctx context.Context, conversationID string) (int64, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return 0, errors.New("conversation ID is required")
	}

	if err := rejectShadowMutation(ctx, s.db, conversationID); err != nil {
		return 0, err
	}

	kind, err := s.ConversationKind(ctx, conversationID)
	if err != nil {
		return 0, err
	}

	if kind == KindRemote {
		return 0, ErrRemoteSession
	}

	if floor, first, err := revertFloorSuffix(ctx, s.db, conversationID); err != nil || floor != "" && first == "" {
		return 0, err
	}

	tx, ids, workers, err := s.beginHistoryMutation(ctx, []string{conversationID}, false)
	if err != nil {
		return 0, err
	}
	defer releaseHistory(workers)

	defer func() { _ = tx.Rollback() }()

	if floor, first, err := revertFloorSuffix(ctx, tx, conversationID); err != nil || floor != "" {
		var rows int64
		if err == nil && first != "" {
			err = queryScalar(ctx, tx, "count entries after the revert floor", `SELECT count(*) FROM entries WHERE conversation_id = $1 AND id >= split_part($2, ':', 1)::bigint`, &rows, conversationID, first)
			if err == nil {
				err = commitRevertDB(ctx, tx, conversationID, first)
			}

			if err == nil {
				err = refreshPrincipals(ctx, tx, []string{conversationID})
			}
		}

		if err != nil {
			return 0, err
		}

		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("commit history wipe: %w", err)
		}

		return rows, nil
	}

	rows, err := execRows(ctx, tx, "delete rocketcode session", `DELETE FROM entries WHERE conversation_id = ANY($1)`, ids)
	if err != nil {
		return 0, err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET continues = NULL WHERE conversation_id = ANY($1) AND continues IS NOT NULL`, ids); err != nil {
		return 0, fmt.Errorf("unlink continued run: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET preview = NULL, last_updated = NULL WHERE conversation_id = ANY($1)`, ids); err != nil {
		return 0, fmt.Errorf("delete rocketcode session summaries: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `WITH done AS (DELETE FROM turns WHERE conversation_id = $1 AND (phase = 'done' OR NOT takes_lane) RETURNING takes_lane, CASE WHEN takes_lane THEN id ELSE input_id END AS key)
DELETE FROM steps s USING done d WHERE s.conversation_id = $1 AND (d.takes_lane AND s.key = d.key OR starts_with(s.key, d.key || '/'))`, conversationID); err != nil {
		return 0, fmt.Errorf("delete terminal transcript turns: %w", err)
	}

	for _, table := range []string{"turns", "steps"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE conversation_id = ANY($1) AND conversation_id <> $2`, ids, conversationID); err != nil {
			return 0, fmt.Errorf("clear owned %s: %w", table, err)
		}
	}

	if err := recordRunning(ctx, tx, ids); err != nil {
		return 0, err
	}

	if err := refreshPrincipals(ctx, tx, ids); err != nil {
		return 0, err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM conversations WHERE conversation_id = ANY($1) AND parent_conversation_id IS NOT NULL`, ids); err != nil {
		return 0, fmt.Errorf("clear subagent sessions: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM shadow_turns WHERE child_conversation_id = ANY($1)`, ids); err != nil {
		return 0, fmt.Errorf("clear comparisons: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `WITH retained AS (
SELECT concat(message, content::text, inbound_json::text, outbound_json::text) AS payload FROM turns WHERE conversation_id = ANY($1) AND phase <> 'done' AND takes_lane
UNION ALL SELECT s.value::text FROM steps s JOIN turns a ON a.conversation_id = s.conversation_id
    AND (s.key = a.id OR starts_with(s.key, a.id || '/')) WHERE a.conversation_id = ANY($1) AND a.phase NOT IN ('waiting', 'done')
UNION ALL SELECT f.id FROM attachments f JOIN turns a ON a.id = f.turn_id WHERE f.conversation_id = ANY($1) AND a.phase NOT IN ('waiting', 'done')
), cleared AS (
    SELECT f.id FROM attachments f LEFT JOIN retained r ON strpos(r.payload, f.id) > 0 WHERE f.conversation_id = ANY($1) AND r.payload IS NULL
), removed AS (DELETE FROM attachments f USING cleared c WHERE f.id = c.id RETURNING f.id)
INSERT INTO pending_attachment_deletions (id) SELECT id FROM removed`, ids); err != nil {
		return 0, fmt.Errorf("enqueue cleared history attachments: %w", err)
	}

	if err := saveSessionSummary(ctx, tx, protocol.SessionSummary{ConversationID: conversationID}); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit session deletion: %w", err)
	}

	return rows, s.removeHistoryFiles(ctx, ids)
}

var (
	ErrNotChat     = errors.New("only Web chats and Slack threads can be deleted")
	ErrSessionBusy = errors.New("this session has a running turn, queued or stashed work, a Background Job, or a scheduled message; stop or clear it first")
)

func (s *SessionService) DeleteChat(ctx context.Context, conversationID string) error {
	var (
		kind    ConversationKind
		private string
	)
	if err := s.db.QueryRowContext(ctx, `SELECT kind, COALESCE(external_run, '') FROM conversations WHERE conversation_id = $1`, conversationID).Scan(&kind, &private); err != nil {
		return fmt.Errorf("read deleted chat: %w", err)
	}

	if kind != KindWebChat && kind != KindSlackThread {
		return ErrNotChat
	}

	active, err := s.HasActiveTurn(ctx, conversationID)
	if err != nil {
		return err
	}

	if active {
		return ErrSessionBusy
	}

	_, pruned, err := s.pruneCandidate(ctx, pruneCandidate{kind: pruneThread, id: conversationID, private: private}, time.Now())
	if err == nil && !pruned {
		return ErrSessionBusy
	}

	return err
}

func (s *SessionService) Delegations(ctx context.Context, conversationID, sourceConversationID string, from, before int64) ([]Delegation, error) {
	children, err := queryRows(ctx, s.db, delegationsSQL, "delegation histories", scanDelegation, conversationID, sourceConversationID, from, before)
	if err != nil {
		return nil, fmt.Errorf("observe delegations: %w", err)
	}

	return children, nil
}

type Delegation struct {
	ID, Parent, CallID string
	Legacy             bool
	Kind               ConversationKind
}

func scanDelegation(row rowScanner) (Delegation, error) {
	var delegation Delegation
	if err := row.Scan(&delegation.ID, &delegation.Parent, &delegation.CallID, &delegation.Legacy, &delegation.Kind); err != nil {
		return Delegation{}, fmt.Errorf("scan delegation: %w", err)
	}

	return delegation, nil
}

const delegationsSQL = `WITH ` + sessionHistorySQL + `, ` + turnReplaySQL + `, saved_turns AS (
    SELECT DISTINCT entry->>'turn_id' AS turn_id FROM physical_entries WHERE NOT synced
), calls AS (
    SELECT COALESCE(NULLIF(source_conversation_id, ''), $1) AS producer, entry->>'turn_id' AS turn_id, item->>'call_id' AS call_id
    FROM effective_entries e CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry->'replay_input', 'null'::jsonb)) WITH ORDINALITY AS items(item, ordinal)
    WHERE e.id >= $3 AND ($4::bigint = 0 OR e.id < $4)
        AND item->>'type' = 'function_call' AND (e.revert_index < 0 OR ordinal <= e.revert_index)
    UNION ALL
    SELECT $1, r.turn_id, r.item->>'call_id' FROM turn_replay r CROSS JOIN viewer v LEFT JOIN saved_turns saved USING (turn_id)
    WHERE $4::bigint = 0 AND v.readable AND v.marker = '' AND saved.turn_id IS NULL AND r.item->>'type' = 'function_call'
), children AS (
    SELECT DISTINCT c.conversation_id AS id, p.producer, c.call_id, c.call_turn IS NULL AS legacy, c.kind FROM calls p
    JOIN conversations c ON c.parent_conversation_id = p.producer AND c.call_id = p.call_id
    WHERE (c.call_turn IS NULL OR c.call_turn = p.turn_id) AND ($2 = '' OR p.producer = $2)
)
SELECT c.* FROM children c
-- Exception to the no-subquery rule: stop at a child history's first entry.
CROSS JOIN LATERAL (SELECT 1 FROM entries e WHERE e.conversation_id = c.id LIMIT 1) h ORDER BY c.id`

type SidebarSession struct {
	Conversation  protocol.Conversation
	Kind          ConversationKind
	RemoteSession string
	Summary       *protocol.SessionSummary
	Running       bool
	Background    bool
	Asking        bool
	Paused        bool
	Name          string
	ForkedFrom    string
	Tags          []string
	Server        string
	Directory     string
	Offline       bool
	SpaceID       string
	SlackChannel  string
	CronPath      string
	Reply         string
	Model         string
	Attention     Attention
	AutoPark      bool
	Origin        ChatOriginFacts
	StateSince    time.Time
	Queued        bool
	External      bool
	Pausable      bool
	ArrivedAt     time.Time
}

type ChatOriginFacts struct {
	ConversationID         string
	CreatedBy              ThreadCreator
	ExternalConversationID string
	Binding                ExternalMCPSessionState
	CreatingSource         string
	ProducerAgent          string
	Run                    *protocol.CronRun
}

const chatOriginSQL = `, creating AS (
    SELECT *, COALESCE(cron_run, CASE WHEN kind <> 'slack_thread' OR created_by = $2 THEN first_source END, '') AS creating_source FROM visible_chats
), origins AS (
    SELECT c.*, COALESCE(c.external_id, '') AS external, COALESCE(c.external_agent, '') AS binding_agent,
        CASE WHEN c.external_id IS NOT NULL THEN c.conversation_id ELSE '' END AS managed, COALESCE(c.origin_pairs, 'null') AS pairs,
        COALESCE(p.agent, '') AS producer_agent, COALESCE(p.cron_path, '') AS run_path, p.cron_run_at_unix_ns AS run_at, p.kind IS NOT DISTINCT FROM $3 AS one_off
    FROM creating c LEFT JOIN conversations p ON p.conversation_id = c.creating_source AND p.cron_run_at_unix_ns IS NOT NULL
)`

const shownChatsSQL = `, reporting AS (
    SELECT reports_to AS conversation_id FROM conversations WHERE reports_to IS NOT NULL
    UNION SELECT c.conversation_id FROM conversations c JOIN conversations r ON r.conversation_id = c.continues WHERE c.continues IS NOT NULL
), shown AS (
    -- A chat whose history is its hidden runs' has a summary time once their first entry lands.
    SELECT c.*, c.has_entries OR p.conversation_id IS NOT NULL AND c.last_updated > '0001-01-01' IS TRUE AS found, p.conversation_id IS NOT NULL AS reported
    FROM origins c LEFT JOIN reporting p USING (conversation_id)
)`

const originColumns = `c.external, c.binding_agent, c.managed, c.pairs, c.creating_source, c.producer_agent, c.run_path, c.run_at, c.one_off`

type chatOriginScan struct {
	pairs sql.RawBytes
	run   protocol.CronRun
	at    sql.NullInt64
}

func (scan *chatOriginScan) read(row rowScanner, facts *ChatOriginFacts, dest ...any) error {
	if err := row.Scan(append(dest, &facts.ExternalConversationID, &facts.Binding.Agent, &facts.Binding.ManagedConversationID, &scan.pairs, &facts.CreatingSource, &facts.ProducerAgent, &scan.run.Path, &scan.at, &scan.run.OneOff)...); err != nil {
		return fmt.Errorf("with its origin: %w", err)
	}

	if err := json.Unmarshal(scan.pairs, &facts.Binding.OriginPairs); err != nil {
		return fmt.Errorf("decode external MCP origin pairs: %w", err)
	}

	if scan.at.Valid {
		run := scan.run
		run.At = time.Unix(0, scan.at.Int64).UTC()
		facts.Run = &run
	}

	return nil
}

func (s *SessionService) ChatOriginFacts(ctx context.Context, id string) iter.Seq2[ChatOriginFacts, error] {
	return func(yield func(ChatOriginFacts, error) bool) {
		rows, err := s.db.QueryContext(ctx, `WITH `+visibleChatsSQL+chatOriginSQL+` SELECT c.conversation_id, c.created_by, `+originColumns+` FROM origins c`, id, ThreadCreatedByCron, KindOneOffCronRun)
		if err != nil {
			yield(ChatOriginFacts{}, fmt.Errorf("query chat origin facts: %w", err))
			return
		}
		defer func() { _ = rows.Close() }()

		dest := make([]any, 0, 11)

		var origin chatOriginScan

		for rows.Next() {
			var facts ChatOriginFacts

			dest = append(dest[:0], &facts.ConversationID, &facts.CreatedBy)
			if err := origin.read(rows, &facts, dest...); err != nil {
				yield(ChatOriginFacts{}, fmt.Errorf("scan chat origin facts: %w", err))
				return
			}

			if !yield(facts, nil) {
				return
			}
		}

		if err := errors.Join(rows.Err(), ctx.Err()); err != nil {
			yield(ChatOriginFacts{}, fmt.Errorf("read chat origin facts: %w", err))
		}
	}
}

type CronRunSource struct {
	ConversationID       string
	SourceConversationID string
	Run                  protocol.CronRun
}

const cronRunSourcesSQL = `WITH located AS (
    SELECT conversation_id, reports_to, cron_path, cron_run_at_unix_ns FROM conversations
    WHERE kind IN ('cron_run', 'one_off_cron_run') AND cron_path IS NOT NULL
), firsts AS (
    SELECT e.conversation_id, min(e.id) AS id FROM located r JOIN entries e USING (conversation_id) GROUP BY e.conversation_id
), deliveries AS (
    SELECT id, conversation_id, conversation_id AS source FROM firsts
    UNION ALL SELECT e.id, r.reports_to, r.conversation_id FROM firsts e JOIN located r USING (conversation_id)
    WHERE r.reports_to IS NOT NULL
    UNION ALL SELECT e.id, c.conversation_id, c.continues FROM firsts e JOIN conversations c ON c.continues = e.conversation_id
)
SELECT d.conversation_id, r.conversation_id, r.cron_path, r.cron_run_at_unix_ns FROM deliveries d
JOIN conversations c ON c.conversation_id = d.conversation_id JOIN located r ON r.conversation_id = d.source
WHERE c.retention = 'record' AND (c.revert_message_id = '' OR d.id < split_part(c.revert_message_id, ':', 1)::bigint
        OR d.id = split_part(c.revert_message_id, ':', 1)::bigint AND split_part(c.revert_message_id, ':', 2)::integer > 0)
    AND (d.conversation_id = d.source OR c.kind NOT IN ('cron_run', 'one_off_cron_run', 'external_mcp_run'))
UNION
SELECT t.conversation_id, r.conversation_id, r.cron_path, r.cron_run_at_unix_ns FROM turns t JOIN located r USING (conversation_id) JOIN conversations c USING (conversation_id)
WHERE c.retention = 'record' AND t.takes_lane AND (t.phase NOT IN ('waiting', 'done') OR t.terminal <> '')`

func (s *SessionService) CronRunSources(ctx context.Context) ([]CronRunSource, error) {
	return queryRows(ctx, s.db, cronRunSourcesSQL, "cron run sources", func(row rowScanner) (CronRunSource, error) {
		var (
			source CronRunSource
			at     int64
		)
		if err := row.Scan(&source.ConversationID, &source.SourceConversationID, &source.Run.Path, &at); err != nil {
			return CronRunSource{}, fmt.Errorf("scan cron run source: %w", err)
		}

		source.Run.At = time.Unix(0, at).UTC()

		return source, nil
	})
}

func (s *SessionService) ChannelFact(ctx context.Context, workspaceID, channelID string) (name string, found bool, err error) {
	err = queryScalar(ctx, s.db, "read Slack channel fact", `SELECT name FROM slack_channel_facts WHERE workspace_id = $1 AND channel_id = $2`, &name, workspaceID, channelID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}

	if err != nil {
		return "", false, err
	}

	return name, true, nil
}

func (s *SessionService) RecordChannelFact(ctx context.Context, workspaceID, channelID, name string, observedAt time.Time) error {
	_, err := execState(ctx, s.db, "record Slack channel fact", `INSERT INTO slack_channel_facts (workspace_id, channel_id, name, observed_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (workspace_id, channel_id) DO UPDATE SET name = EXCLUDED.name, observed_at = EXCLUDED.observed_at
WHERE slack_channel_facts.observed_at < EXCLUDED.observed_at`, workspaceID, channelID, name, observedAt.UnixNano())

	return err
}

func (s *SessionService) SlackChannelIDs(ctx context.Context) ([]string, error) {
	return queryStrings(ctx, s.db, `SELECT DISTINCT slack_channel COLLATE "C" FROM conversations WHERE slack_channel IS NOT NULL ORDER BY 1`, "Slack channel IDs")
}

func (s *SessionService) Stop() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close rocketcode session db: %w", err)
	}

	return nil
}

func (s *SessionService) scheduledAgents() []string {
	s.scheduledMu.Lock()
	defer s.scheduledMu.Unlock()

	return s.scheduled
}

func (s *SessionService) sidebarSessions(ctx context.Context, username string, chats []string) iter.Seq2[SidebarSession, error] {
	return func(yield func(SidebarSession, error) bool) {
		limit, err := s.SpaceLimitFor(ctx, s.definitions.Clone().PermittedSpaces(username))
		if err != nil {
			yield(SidebarSession{}, err)
			return
		}

		rows, err := s.db.QueryContext(ctx, `WITH `+visibleChatsSQL+chatOriginSQL+shownChatsSQL+`,
running AS (
    SELECT DISTINCT ON (conversation_id) conversation_id, inbound_json->>'Text' AS prompt, to_timestamp(created_at_unix_ns / 1e9) AS started_at
    FROM turns WHERE phase NOT IN ('waiting', 'done') AND takes_lane
    ORDER BY conversation_id, created_at_unix_ns
), live AS (
    -- Running lane turns and Background Jobs, then waiting turns other than scheduled reminders.
    SELECT conversation_id, created_at_unix_ns AS since, takes_lane, FALSE AS queued FROM turns WHERE phase = 'running' OR phase = 'delivering' AND takes_lane
    UNION ALL SELECT conversation_id, created_at_unix_ns, TRUE, TRUE FROM turns WHERE phase = 'waiting' AND reason <> 'scheduled'
), credited AS (
    SELECT conversation_id, since, takes_lane, queued, TRUE AS own FROM live
    UNION ALL SELECT c.reports_to, l.since, l.takes_lane, l.queued, FALSE FROM live l JOIN conversations c USING (conversation_id) WHERE c.reports_to IS NOT NULL
), work AS (
    -- Background work is a chat's Background Jobs and everything running of the runs reporting to it.
    SELECT conversation_id, bool_or(NOT queued AND NOT (own AND takes_lane)) AS background, min(since) FILTER (WHERE NOT queued) AS running_since,
        min(since) FILTER (WHERE queued) AS queued_since, max(since) AS arrived
    FROM credited GROUP BY conversation_id
), asking AS (
    SELECT DISTINCT q.conversation_id FROM running r
    JOIN steps q ON q.conversation_id = r.conversation_id AND q.key LIKE '%`+webQuestionStep+`'
    LEFT JOIN steps a ON a.conversation_id = q.conversation_id AND a.key = left(q.key, -length('`+webQuestionStep+`')) || '/answer'
    WHERE a.key IS NULL
), external_runs AS (
    SELECT DISTINCT reports_to AS conversation_id FROM conversations WHERE kind = 'external_mcp_run' AND reports_to IS NOT NULL
), everyone AS (
    SELECT auto_park FROM web_tab_states WHERE username = $6
), reads AS (
    SELECT conversation_id, unread_at, quiet, parked, unfollowed, NULLIF(until, 'infinity') AS until, until = 'infinity' AS until_done, note, auto_park
    FROM web_tab_attention WHERE username = $6 AND conversation_id IS NOT NULL
), local AS (
SELECT
    c.conversation_id,
    c.agent,
    c.created_by,
    CASE WHEN c.found THEN c.preview ELSE convert_to(r.prompt, 'UTF8') END AS preview,
    CASE WHEN c.found THEN c.last_updated ELSE r.started_at END AS updated,
    c.running,
    COALESCE(w.background, FALSE) AS background,
    k.conversation_id IS NOT NULL AS asking,
    c.paused,
    c.kind = 'web_chat' AND c.created_by NOT IN ('', $2) AND c.cron_run IS NULL AND NOT c.reported AND NOT (c.sticky = 'global' AND COALESCE(c.agent = ANY($7), FALSE)) AS pausable,
    c.name,
    c.forked_from,
    COALESCE(c.tags, '[]'::jsonb) AS tags,
    '' AS server,
    '' AS directory,
    FALSE AS offline,
    COALESCE(c.space_id, '') AS space_id,
    '' AS reply,
    COALESCE(c.slack_channel, '') AS slack_channel,
    COALESCE(run.cron_path, '') AS cron_path,
    '' AS model,
    c.kind,
    '' AS remote_session,
    u.unread_at,
    COALESCE(u.quiet, FALSE) AS quiet,
    COALESCE(u.parked, FALSE) AS parked,
    COALESCE(u.unfollowed, FALSE) AS unfollowed,
    u.until,
    COALESCE(u.until_done, FALSE) AS until_done,
    COALESCE(u.note, '') AS note,
    COALESCE(u.auto_park, e.auto_park, TRUE) AS auto_park,
    COALESCE(w.running_since, w.queued_since, 0) AS state_since,
    w.queued_since IS NOT NULL AS queued,
    c.external <> '' OR x.conversation_id IS NOT NULL AS external_mcp,
    COALESCE(w.arrived, 0) AS arrived,
    `+originColumns+`
FROM shown c
LEFT JOIN reads u ON u.conversation_id = c.conversation_id
LEFT JOIN everyone e ON TRUE
LEFT JOIN conversations run ON run.conversation_id = c.cron_run
LEFT JOIN running r ON r.conversation_id = c.conversation_id
LEFT JOIN work w ON w.conversation_id = c.conversation_id
LEFT JOIN asking k ON k.conversation_id = c.conversation_id
LEFT JOIN external_runs x ON x.conversation_id = c.conversation_id
WHERE (c.found OR c.running) AND ($5::text[] IS NULL OR c.conversation_id = ANY($5))
), remote AS (
SELECT o.conversation_id, o.agent, '', convert_to(o.title, 'UTF8'), o.last_updated, o.running, o.background, o.asking, FALSE AS paused, FALSE AS pausable, o.name, o.forked_from, COALESCE(o.tags, '[]'::jsonb),
    o.server, o.directory, o.synced_at < now() - interval '15 seconds', COALESCE(o.space_id, ''), COALESCE(o.reply, ''), '', '', COALESCE(o.model, ''), o.kind, o.remote_session, u.unread_at, COALESCE(u.quiet, FALSE),
    COALESCE(u.parked, FALSE), COALESCE(u.unfollowed, FALSE), u.until, COALESCE(u.until_done, FALSE), COALESCE(u.note, ''), COALESCE(u.auto_park, e.auto_park, TRUE),
    0, FALSE AS queued, FALSE AS external_mcp, 0,
    '', '', '', 'null'::jsonb, '', '', '', NULL::bigint, FALSE
FROM conversations o
LEFT JOIN reads u ON u.conversation_id = o.conversation_id
LEFT JOIN everyone e ON TRUE
-- Rows of servers no longer configured stay stored, hidden, and return with their server.
WHERE o.kind = 'remote' AND o.server = ANY($4) AND ($5::text[] IS NULL OR o.conversation_id = ANY($5))
), chats AS (
SELECT * FROM local UNION ALL SELECT * FROM remote
)
SELECT * FROM chats`, pgx.QueryExecModeCacheDescribe, "", ThreadCreatedByCron, KindOneOffCronRun,
			slices.Collect(maps.Keys(s.definitions.Clone().OpenCode)), chats, username, s.scheduledAgents())
		if err != nil {
			yield(SidebarSession{}, fmt.Errorf("query sidebar sessions: %w", err))
			return
		}
		defer func() { _ = rows.Close() }()

		var (
			sessions       []*SidebarSession
			preview        pgtype.DriverBytes
			tags           pgtype.DriverBytes
			updated        sql.NullTime
			unread, until  sql.NullTime
			since, arrived int64
			origin         chatOriginScan
		)

		dest := make([]any, 0, 45)

		for rows.Next() {
			var row SidebarSession

			dest = append(dest[:0], &row.Conversation.ID, &row.Conversation.Agent, &row.Conversation.CreatedBy, &preview, &updated, &row.Running, &row.Background, &row.Asking, &row.Paused, &row.Pausable, &row.Name, &row.ForkedFrom, &tags, &row.Server, &row.Directory, &row.Offline, &row.SpaceID, &row.Reply, &row.SlackChannel, &row.CronPath, &row.Model, &row.Kind, &row.RemoteSession, &unread, &row.Attention.Quiet,
				&row.Attention.Parked, &row.Attention.Unfollowed, &until, &row.Attention.UntilDone, &row.Attention.Note, &row.AutoPark, &since, &row.Queued, &row.External, &arrived)
			if err := origin.read(rows, &row.Origin, dest...); err != nil {
				yield(SidebarSession{}, fmt.Errorf("scan sidebar session: %w", err))
				return
			}

			if !limit.AllowsSession(&row) {
				continue
			}

			row.Origin.ConversationID, row.Origin.CreatedBy = row.Conversation.ID, ThreadCreator(row.Conversation.CreatedBy)
			row.Attention.UnreadAt, row.Attention.Until = unread.Time.UTC(), until.Time.UTC()
			row.StateSince, row.ArrivedAt = cmp.Or(timeFromUnixNano(since), updated.Time.UTC()), cmp.Or(timeFromUnixNano(arrived), updated.Time.UTC())

			if err := json.Unmarshal(tags, &row.Tags); err != nil {
				yield(SidebarSession{}, fmt.Errorf("decode sidebar tags: %w", err))
				return
			}

			if updated.Valid {
				row.Summary = &protocol.SessionSummary{ConversationID: row.Conversation.ID, LastMessage: string(preview), LastUpdated: updated.Time.UTC()}
			}

			sessions = append(sessions, &row)
		}

		if err := errors.Join(rows.Err(), ctx.Err()); err != nil {
			yield(SidebarSession{}, fmt.Errorf("read sidebar sessions: %w", err))
			return
		}

		slices.SortFunc(sessions, func(a, b *SidebarSession) int {
			var updated [2]time.Time

			for i, row := range [2]*SidebarSession{a, b} {
				if row.Summary != nil {
					updated[i] = row.Summary.LastUpdated
				}
			}

			return cmp.Or(updated[1].Compare(updated[0]), strings.Compare(a.Conversation.ID, b.Conversation.ID))
		})

		for _, row := range sessions {
			if !yield(*row, nil) {
				return
			}
		}
	}
}

func (s *SessionService) pruneCandidate(ctx context.Context, c pruneCandidate, cutoff time.Time) (rows int64, pruned bool, err error) {
	if c.kind == pruneGoal {
		tx, err := s.beginStateTx(ctx, "goal prune")
		if err != nil {
			return 0, false, err
		}

		defer func() { _ = tx.Rollback() }()

		if err := lockSessionHistory(ctx, tx, c.id); err != nil {
			return 0, false, err
		}

		if stale, err := c.stale(ctx, tx, cutoff); err != nil || !stale {
			return 0, false, err
		}

		if _, err := tx.ExecContext(ctx, `UPDATE conversations SET goal_objective = NULL, goal_max_turns = NULL, goal_turns_used = NULL,
    goal_status = NULL, goal_note = NULL, goal_created_at_unix_ns = NULL, goal_updated_at_unix_ns = NULL,
    goal_principal_name = NULL, goal_principal_email = NULL, goal_origin = '' WHERE conversation_id = $1`, c.id); err != nil {
			return 0, false, fmt.Errorf("delete stale goal: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return 0, false, fmt.Errorf("commit goal prune: %w", err)
		}

		return 0, true, nil
	}

	roots := []string{c.id}
	if c.private != "" {
		roots = append(roots, c.private)
	}

	if c.kind != pruneOrphan {
		runs, err := queryStrings(ctx, s.db, `SELECT conversation_id FROM conversations WHERE reports_to = $1
UNION SELECT continues FROM conversations WHERE conversation_id = $1 AND continues IS NOT NULL ORDER BY 1`, "chat runs", c.id)
		if err != nil {
			return 0, false, err
		}

		roots = append(roots, runs...)
	}

	tx, ids, _, err := s.beginHistoryMutation(ctx, roots, true)
	if errors.Is(err, errHistoryBusy) {
		return 0, false, nil
	}

	if err != nil {
		return 0, false, err
	}

	defer func() { _ = tx.Rollback() }()

	if stale, err := c.stale(ctx, tx, cutoff); err != nil || !stale {
		return 0, false, err
	}

	if c.kind == pruneSticky {
		if _, err := execState(ctx, tx, "enqueue expired sticky attachments", `WITH payloads AS NOT MATERIALIZED (
    SELECT entry_json::text AS payload, NULL AS turn, entry_timestamp < $2 AS expired FROM entries WHERE conversation_id = ANY($1)
    UNION ALL SELECT concat(message, content::text, inbound_json::text, outbound_json::text), id, phase = 'done' AND note IS DISTINCT FROM $3 AND created_at_unix_ns < $4 FROM turns WHERE conversation_id = ANY($1)
    UNION ALL SELECT message, NULL, FALSE FROM schedules WHERE conversation_id = ANY($1)
), expiring AS (
    SELECT DISTINCT f.id, f.turn_id FROM attachments f JOIN payloads p ON p.expired AND (strpos(p.payload, f.id) > 0 OR p.turn = f.turn_id) WHERE f.conversation_id = ANY($1)
), kept AS MATERIALIZED (
    SELECT DISTINCT e.id FROM expiring e JOIN payloads p ON NOT p.expired AND (strpos(p.payload, e.id) > 0 OR p.turn = e.turn_id)
), removed AS (DELETE FROM attachments f USING expiring e LEFT JOIN kept k USING (id) WHERE f.id = e.id AND k.id IS NULL RETURNING f.id)
INSERT INTO pending_attachment_deletions (id) SELECT id FROM removed`, ids, cutoff.UTC().Format(time.RFC3339Nano), notePending, cutoff.UnixNano()); err != nil {
			return 0, false, err
		}

		rows, err = execRows(ctx, tx, "prune old sticky messages", `DELETE FROM entries WHERE conversation_id = ANY($1) AND entry_timestamp < $2`, ids, cutoff.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return 0, false, err
		}

		if _, err := execState(ctx, tx, "prune old sticky turns", `DELETE FROM turns WHERE conversation_id = ANY($1) AND phase = 'done' AND note IS DISTINCT FROM $2 AND created_at_unix_ns < $3`, ids, notePending, cutoff.UnixNano()); err != nil {
			return 0, false, err
		}

		if err := refreshPrincipals(ctx, tx, ids); err != nil {
			return 0, false, err
		}

		if err := tx.Commit(); err != nil {
			return 0, false, fmt.Errorf("commit sticky message prune: %w", err)
		}

		return rows, rows > 0, nil
	}

	rows, err = deleteOwnedHistory(ctx, tx, ids)
	if err != nil {
		return 0, false, err
	}

	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("commit state prune: %w", err)
	}

	return rows, true, s.removeHistoryFiles(ctx, ids)
}

func sessionTags(ctx context.Context, db stateStoreDB, conversationID string) ([]string, error) {
	var data []byte
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(min(tags::text), '[]') FROM conversations WHERE conversation_id = $1`, conversationID).Scan(&data); err != nil {
		return nil, fmt.Errorf("read session tags: %w", err)
	}

	var tags []string
	if err := json.Unmarshal(data, &tags); err != nil {
		return nil, fmt.Errorf("decode session tags: %w", err)
	}

	return tags, nil
}

func (s *SessionService) toggleSessionTag(ctx context.Context, conversationID, tag string, group []string) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin session tag toggle: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, conversationID); err != nil {
		return nil, err
	}

	tags, err := sessionTags(ctx, tx, conversationID)
	if err != nil {
		return nil, err
	}

	active := slices.Contains(tags, tag)

	tags = slices.DeleteFunc(tags, func(name string) bool { return slices.Contains(group, name) })
	if !active {
		tags = append(tags, tag)
	}

	slices.Sort(tags)

	data, _ := json.Marshal(tags)

	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET tags = $2 WHERE conversation_id = $1`, conversationID, data); err != nil {
		return nil, fmt.Errorf("write session tags: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit session tag toggle: %w", err)
	}

	return tags, nil
}

func (s *SessionService) newTurnID() string {
	s.lastTurnIDMu.Lock()
	defer s.lastTurnIDMu.Unlock()

	s.lastTurnID = max(s.lastTurnID+1, time.Now().UnixNano())

	return fmt.Sprintf("turn-%d", s.lastTurnID)
}

func (s *SessionService) externalMCPMetadataEntry(ctx context.Context, conversationID string) (*harness.SessionEntry, error) {
	var raw string

	err := s.db.QueryRowContext(ctx, `WITH shown AS (
    SELECT $1::text AS conversation_id UNION SELECT conversation_id FROM conversations WHERE reports_to = $1
    UNION SELECT continues FROM conversations WHERE conversation_id = $1 AND continues IS NOT NULL
), latest AS (
    SELECT max(e.id) AS id FROM shown s JOIN entries e USING (conversation_id) WHERE e.entry_json::jsonb->>'type' = 'mcp_external_metadata'
)
SELECT e.entry_json FROM latest l JOIN entries e USING (id)`, strings.TrimSpace(conversationID)).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read external MCP metadata entry: %w", err)
	}

	var entry harness.SessionEntry
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		return nil, fmt.Errorf("parse external MCP metadata entry: %w", err)
	}

	return &entry, nil
}

func (s *SessionService) setGoalStatus(conversationID, status, note string) (GoalState, error) {
	conversationID = strings.TrimSpace(conversationID)
	ctx := context.Background()

	tx, err := s.beginStateTx(ctx, "goal status update")
	if err != nil {
		return GoalState{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := execRows(ctx, tx, "set active goal status", `UPDATE conversations SET goal_status = $1, goal_note = $2, goal_updated_at_unix_ns = $3 WHERE conversation_id = $4 AND (goal_status = '' OR goal_status = $5)`, strings.TrimSpace(status), strings.TrimSpace(note), timeUnixNano(time.Now().UTC()), conversationID, GoalStatusActive); err != nil {
		return GoalState{}, err
	}

	goal, _, err := (stateDAO{db: tx}).goal(ctx, conversationID)
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

func (s *SessionService) openSpill() (*os.Root, error) {
	root, err := os.OpenRoot(s.spillDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("open execute spill dir: %w", err)
	}

	return root, nil
}

func (s *SessionService) sweepRetainedResults() error {
	root, err := s.openSpill()
	if root == nil {
		return err
	}

	harness.SweepRetainedResults(root, "retained")

	if err := root.Close(); err != nil {
		return fmt.Errorf("close execute spill dir: %w", err)
	}

	return nil
}

func (s *SessionService) removeRetainedResults(conversationIDs ...string) error {
	root, err := s.openSpill()
	if root == nil {
		return err
	}

	for _, conversationID := range conversationIDs {
		err = errors.Join(err, root.RemoveAll(rocketcodeRetainedDir(conversationID)))
	}

	if err = errors.Join(err, root.Close()); err != nil {
		return fmt.Errorf("remove retained execute results: %w", err)
	}

	return nil
}

func (s *SessionService) removeHistoryFiles(ctx context.Context, ids []string) error {
	blobs, err := queryStrings(ctx, s.db, `SELECT id FROM pending_attachment_deletions ORDER BY id`, "pending attachment deletions")
	if err != nil {
		return err
	}

	var errCombined error

	for _, id := range blobs {
		if err := s.attachments.Delete(ctx, id); err != nil {
			errCombined = errors.Join(errCombined, err)
			continue
		}

		_, err := s.db.ExecContext(ctx, `DELETE FROM pending_attachment_deletions WHERE id = $1`, id)
		errCombined = errors.Join(errCombined, err)
	}

	return errors.Join(errCombined, s.removeRetainedResults(ids...))
}

func removeSessionEntryNUL(data []byte) []byte {
	if !bytes.Contains(data, []byte(`\u0000`)) {
		return data
	}

	output := make([]byte, 0, len(data))
	for part := range bytes.SplitAfterSeq(data, []byte(`\\`)) {
		for text := range bytes.SplitSeq(part, []byte(`\u0000`)) {
			output = append(output, text...)
		}
	}

	return output
}

func appendSessionEntryDB(ctx context.Context, db stateStoreDB, conversationID string, entry *harness.SessionEntry) (int64, error) {
	data, err := json.Marshal(entry)
	if err != nil {
		return 0, fmt.Errorf("marshal rocketcode session entry: %w", err)
	}

	data = removeSessionEntryNUL(data)

	reportsTo, err := lockRunHistories(ctx, db, conversationID)
	if err != nil {
		return 0, err
	}

	shown := ObservedSessionEntry{Entry: *entry, Synced: reportsTo != ""}
	if err := visibleEntry(&shown); err != nil {
		return 0, err
	}

	var searched bool
	if err := queryScalar(ctx, db, "read entry search", `SELECT COALESCE(bool_and(listed), TRUE) FROM conversations WHERE conversation_id = $1`, &searched, conversationID); err != nil {
		return 0, err
	}

	var (
		text       string
		principals []string
	)

	if searched || reportsTo != "" {
		text, principals, err = messageSearchText(ctx, db, conversationID, shown.Entry.ReplayInput)
		if err != nil {
			return 0, err
		}
	}

	var id int64
	if err := db.QueryRowContext(ctx, `INSERT INTO entries (conversation_id, entry_json, entry_timestamp, search_text) VALUES ($1, $2, $3, NULLIF($4, '')) RETURNING id`, conversationID, string(data), entry.Timestamp.UTC().Format(time.RFC3339Nano), text).Scan(&id); err != nil {
		return 0, fmt.Errorf("append rocketcode session entry: %w", err)
	}

	if len(principals) > 0 {
		if _, err := execState(ctx, db, "add message principals", `WITH current AS (
    SELECT conversation_id, principals FROM conversations WHERE (conversation_id = $1 OR continues = $3) AND revert_message_id = '' AND NOT principals @> $2::text[]
), merged AS (
    SELECT c.conversation_id, array_agg(DISTINCT p.principal) AS principals FROM current c CROSS JOIN LATERAL unnest(c.principals || $2::text[]) AS p(principal)
    GROUP BY c.conversation_id
)
UPDATE conversations c SET principals = m.principals FROM merged m WHERE c.conversation_id = m.conversation_id`, cmp.Or(reportsTo, conversationID), principals, conversationID); err != nil {
			return 0, err
		}
	}

	if entry.TurnID != "" {
		hidden, err := execRows(ctx, db, "clear hidden turn message search", `UPDATE turns SET search_text = NULL
WHERE conversation_id = $1 AND id = $2 AND search_text IS NOT NULL`, conversationID, entry.TurnID)
		if err != nil {
			return 0, err
		}

		if hidden > 0 {
			if err := refreshPrincipals(ctx, db, []string{conversationID}); err != nil {
				return 0, err
			}
		}
	}

	if err := projectEntrySummary(ctx, db, conversationID, entry); err != nil || reportsTo == "" {
		return id, err
	}

	return id, projectEntrySummary(ctx, db, reportsTo, entry)
}

func projectEntrySummary(ctx context.Context, db stateStoreDB, conversationID string, entry *harness.SessionEntry) error {
	summary, err := loadSessionSummary(ctx, db, conversationID)
	if err != nil {
		return err
	}

	if err := projectSessionSummary(&summary, entry, entry.Timestamp.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}

	return saveSessionSummary(ctx, db, summary)
}

func lockRunHistories(ctx context.Context, db stateStoreDB, conversationID string) (string, error) {
	var reportsTo string
	if err := queryScalar(ctx, db, "read run destination", `SELECT COALESCE(max(reports_to), '') FROM conversations WHERE conversation_id = $1`, &reportsTo, conversationID); err != nil {
		return "", err
	}

	return reportsTo, lockHistories(ctx, db, slices.DeleteFunc([]string{conversationID, reportsTo}, func(id string) bool { return id == "" }))
}

func visibleEntry(observed *ObservedSessionEntry) error {
	entry := &observed.Entry
	if !observed.Synced {
		return nil
	}

	replay := make([]json.RawMessage, 0, len(entry.ReplayInput))
	boundaries := make([]int, len(entry.ReplayInput)+1)

	for i, raw := range entry.ReplayInput {
		boundaries[i] = len(replay)

		var item struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return fmt.Errorf("read visible replay item type: %w", err)
		}

		if item.Type != "compaction" && item.Type != "compaction_summary" {
			replay = append(replay, raw)
		}
	}

	boundaries[len(entry.ReplayInput)] = len(replay)
	entry.ReplayInput, entry.ReplayAttribution = replay, slices.Clone(entry.ReplayAttribution)
	harness.RemapReplayAttribution(entry.ReplayAttribution, boundaries)

	return nil
}

func shouldPruneThreadConversation(ctx context.Context, db stateStoreDB, conversationID string, cutoff time.Time) (bool, error) {
	var bound bool
	if err := queryScalar(ctx, db, "read retention ownership", `SELECT count(*) > 0 FROM conversations WHERE conversation_id = $1 AND shadow_parent IS NOT NULL`, &bound, conversationID); err != nil || bound {
		return false, err
	}

	pending, err := conversationHasPendingWork(ctx, db, conversationID)
	if err != nil || pending {
		return false, err
	}

	var created time.Time

	err = queryScalar(ctx, db, "read chat time", `WITH continued AS (
    SELECT DISTINCT continues AS conversation_id FROM conversations WHERE continues = $1
)
SELECT CASE WHEN c.slack_thread ~ '^[0-9]+(\.[0-9]+)?$' THEN to_timestamp(c.slack_thread::numeric) ELSE c.created_at END
FROM conversations c LEFT JOIN continued k USING (conversation_id)
WHERE c.conversation_id = $1 AND (c.retention = 'record' AND (c.listed OR c.kind IN ('cron_run', 'one_off_cron_run') AND c.reports_to IS NULL AND k.conversation_id IS NULL)
    OR c.slack_thread ~ '^[0-9]+(\.[0-9]+)?$')`, &created, conversationID)
	if !errors.Is(err, sql.ErrNoRows) {
		if err != nil {
			return false, err
		}

		return sessionLatestBefore(ctx, db, conversationID, created, cutoff)
	}

	var emptyManaged bool
	if err := queryScalar(ctx, db, "read empty managed conversation", `WITH shown AS (
    SELECT $1::text AS conversation_id UNION SELECT conversation_id FROM conversations WHERE reports_to = $1
    UNION SELECT continues FROM conversations WHERE conversation_id = $1 AND continues IS NOT NULL
), entry AS (
    SELECT $1::text AS conversation_id FROM shown s JOIN entries e USING (conversation_id) LIMIT 1
), turn AS (
    SELECT conversation_id FROM turns WHERE conversation_id = $1 AND takes_lane LIMIT 1
)
SELECT count(*) > 0 FROM conversations m
LEFT JOIN entry e USING (conversation_id)
LEFT JOIN turn t USING (conversation_id)
WHERE m.conversation_id = $1 AND m.retention = 'record' AND m.created_at < $2 AND e.conversation_id IS NULL AND t.conversation_id IS NULL`, &emptyManaged, conversationID, cutoff); err != nil {
		return false, err
	}

	return emptyManaged, nil
}

func sessionLatestBefore(ctx context.Context, db stateStoreDB, conversationID string, fallback, cutoff time.Time) (bool, error) {
	var before bool
	if err := queryScalar(ctx, db, "read latest session entry timestamp", `WITH runs AS (
    SELECT conversation_id FROM conversations WHERE reports_to = $3
    UNION SELECT continues FROM conversations WHERE conversation_id = $3 AND continues IS NOT NULL
), shown AS (
    SELECT entry_timestamp FROM entries WHERE conversation_id = $3
    UNION ALL SELECT e.entry_timestamp FROM runs r JOIN entries e USING (conversation_id)
)
SELECT COALESCE(MAX(entry_timestamp), $1) < $2 FROM shown`, &before, fallback.UTC().Format(time.RFC3339Nano), cutoff.UTC().Format(time.RFC3339Nano), conversationID); err != nil {
		return false, err
	}

	return before, nil
}

func readRows(ctx context.Context, db stateStoreDB, query, label string, scan func(rowScanner) error, args ...any) error {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("query %s: %w", label, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("read %s: %w", label, err)
	}

	return nil
}

func (s *SessionService) readParallelRows(ctx context.Context, query, label string, scan func(rowScanner) error, args ...any) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin %s: %w", label, err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `SET LOCAL parallel_setup_cost = 0`); err != nil {
		return fmt.Errorf("plan %s: %w", label, err)
	}

	return readRows(ctx, tx, query, label, scan, args...)
}

func queryRows[T any](ctx context.Context, db stateStoreDB, query, label string, scan func(rowScanner) (T, error), args ...any) ([]T, error) {
	var out []T

	err := readRows(ctx, db, query, label, func(row rowScanner) error {
		item, err := scan(row)
		if err != nil {
			return err
		}

		out = append(out, item)

		return nil
	}, args...)
	if err != nil {
		return nil, err
	}

	return out, nil
}

type mapRowScanner[K comparable, V any] interface {
	scan(rowScanner) (K, V, error)
}

func queryMap[K comparable, V any](ctx context.Context, db stateStoreDB, query, label string, scan mapRowScanner[K, V], args ...any) (map[K]V, error) {
	out := map[K]V{}

	err := readRows(ctx, db, query, label, func(row rowScanner) error {
		key, value, err := scan.scan(row)
		if err != nil {
			return err
		}

		out[key] = value

		return nil
	}, args...)
	if err != nil || len(out) == 0 {
		return nil, err
	}

	return out, nil
}

func queryStrings(ctx context.Context, db stateStoreDB, query, label string, args ...any) ([]string, error) {
	return queryRows(ctx, db, query, label, func(row rowScanner) (string, error) {
		var value string
		if err := row.Scan(&value); err != nil {
			return "", fmt.Errorf("scan %s: %w", label, err)
		}

		return value, nil
	}, args...)
}

func conversationHasPendingWork(ctx context.Context, db stateStoreDB, conversationID string) (bool, error) {
	var pending bool
	if err := queryScalar(ctx, db, "check conversation pending work", `WITH `+remindersSQL+`, owners AS (
    SELECT $1::text AS conversation_id
    UNION
    SELECT conversation_id FROM conversations WHERE reports_to = $1
), related_conversations AS (
    SELECT conversation_id FROM owners
    UNION
    SELECT b.conversation_id FROM conversations b JOIN owners o ON b.shadow_parent = o.conversation_id
), pending_work AS (
    SELECT 1 FROM turns WHERE conversation_id = $1 AND phase = 'waiting'
    UNION ALL
    SELECT 1 FROM reminders JOIN related_conversations USING (conversation_id)
    UNION ALL
    SELECT 1 FROM turns JOIN owners USING (conversation_id) WHERE NOT takes_lane AND (phase = 'running' OR note = $2 AND wake)
    UNION ALL
    SELECT 1 FROM shadow_turns t JOIN conversations b ON b.conversation_id = t.child_conversation_id
    LEFT JOIN turns a ON a.conversation_id = t.child_conversation_id AND a.id = t.child_turn_id AND a.phase NOT IN ('waiting', 'done')
    WHERE b.shadow_parent IS NOT NULL AND (b.shadow_parent = $1 OR b.conversation_id = $1 OR t.parent_source_conversation_id = $1)
        AND (t.terminal = '' AND t.child_turn_id = '' OR a.id IS NOT NULL)
)
SELECT count(*) > 0 FROM pending_work`, &pending, conversationID, notePending); err != nil {
		return false, err
	}

	return pending, nil
}

func stalePrivateConversationIDs(ctx context.Context, db stateStoreDB, cutoff time.Time, conversationID string) ([]string, error) {
	return queryStrings(ctx, db, `WITH private_entries AS (
    SELECT e.conversation_id, e.entry_timestamp FROM conversations c JOIN entries e USING (conversation_id) WHERE c.retention = 'private'
    UNION ALL
    SELECT c.conversation_id, '1970-01-01T00:00:00Z' FROM conversations c LEFT JOIN entries e USING (conversation_id)
    WHERE c.retention = 'private' AND c.tags IS NOT NULL AND e.conversation_id IS NULL
),
referenced_conversations AS (
    SELECT external_run AS conversation_id FROM conversations WHERE external_id IS NOT NULL
    UNION ALL
    SELECT conversation_id FROM turns WHERE phase = 'waiting'
    UNION ALL
    SELECT conversation_id FROM schedules
    UNION ALL
    SELECT unnest(ARRAY[j.conversation_id, c.reports_to]) FROM turns j LEFT JOIN conversations c USING (conversation_id) WHERE NOT j.takes_lane AND (j.phase = 'running' OR j.note = $2 AND j.wake)
    UNION ALL
    SELECT t.parent_source_conversation_id FROM shadow_turns t
    LEFT JOIN turns a ON a.conversation_id = t.child_conversation_id AND a.id = t.child_turn_id AND a.phase NOT IN ('waiting', 'done')
    WHERE t.terminal = '' AND (t.child_turn_id = '' OR a.id IS NOT NULL)
)
SELECT h.conversation_id FROM private_entries h
LEFT JOIN referenced_conversations r ON r.conversation_id = h.conversation_id
WHERE r.conversation_id IS NULL AND ($3 = '' OR h.conversation_id = $3)
GROUP BY h.conversation_id HAVING MAX(h.entry_timestamp) < $1`, "stale private session conversations", cutoff.UTC().Format(time.RFC3339Nano), notePending, conversationID)
}

func deleteOwnedHistory(ctx context.Context, db stateStoreDB, conversationIDs []string) (int64, error) {
	var deleted int64

	for _, conversationID := range slices.Sorted(slices.Values(conversationIDs)) {
		if err := lockSessionHistory(ctx, db, conversationID); err != nil {
			return 0, err
		}

		rows, err := execRows(ctx, db, "delete stale session entries", `DELETE FROM entries WHERE conversation_id = $1`, conversationID)
		if err != nil {
			return 0, err
		}

		deleted += rows
	}

	if err := refreshPrincipals(ctx, db, conversationIDs); err != nil {
		return deleted, err
	}

	for _, table := range []string{"turns", "steps", "schedules", "conversations"} {
		if _, err := db.ExecContext(ctx, `DELETE FROM `+table+` WHERE conversation_id = ANY($1)`, conversationIDs); err != nil {
			return 0, fmt.Errorf("delete owned %s: %w", table, err)
		}
	}

	_, err := execState(ctx, db, "enqueue deleted attachments", `WITH removed AS (DELETE FROM attachments WHERE conversation_id = ANY($1) RETURNING id)
INSERT INTO pending_attachment_deletions (id) SELECT id FROM removed`, conversationIDs)

	return deleted, err
}

type runLockWork interface {
	Run(context.Context) error
}

func holdRunLock(ctx context.Context, db *sql.DB, work runLockWork) error {
	client, _ := pglock.UnsafeNew(db, pglock.WithCustomTable(runLockTable))
	if err := client.TryCreateTable(); err != nil {
		return fmt.Errorf("acquire rocketclaw run lock: %w", err)
	}

	var owner string

	err := client.Do(ctx, runLockName, func(lockCtx context.Context, lock *pglock.Lock) error {
		owner = lock.Owner()
		return work.Run(lockCtx)
	})

	_, errRelease := db.ExecContext(context.WithoutCancel(ctx), `DELETE FROM `+runLockTable+` WHERE name = $1 AND owner = $2`, runLockName, owner)
	if err := errors.Join(err, errRelease); err != nil {
		return fmt.Errorf("%w", err)
	}

	return nil
}

func openSessionDB(ctx context.Context, databaseURL string) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, hideDSN(err, databaseURL, "open rocketclaw state store")
	}

	db := stdlib.OpenDB(*cfg)
	db.SetMaxIdleConns(2 * summaryReaders)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, hideDSN(err, databaseURL, "ping rocketclaw state store")
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

func (m *memoryStore) Append(_ context.Context, entry *harness.SessionEntry) error {
	m.entries = append(m.entries, *entry)
	return nil
}

func (m *memoryStore) in() iter.Seq2[harness.SessionEntry, error] {
	return func(yield func(harness.SessionEntry, error) bool) {
		for i := range m.entries {
			if !yield(m.entries[i], nil) {
				return
			}
		}
	}
}
