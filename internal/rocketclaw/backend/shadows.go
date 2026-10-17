package backend

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
)

var errHistoryBusy = errors.New("conversation history is busy")

type noHistoryWorkers struct{}

func (noHistoryWorkers) pauseHistory(context.Context, []string) ([]*Bridge, error) {
	return nil, nil
}

func (noHistoryWorkers) historyBusy([]string) bool {
	return false
}

// ownedHistoryIDs enumerates bindings before any cascade can erase ownership.
// Delegation paths remain scoped to these explicit roots, not shadow names. The
// histories under another top-level session are its own; subagent sessions belong to their root.
func ownedHistoryIDs(ctx context.Context, db stateStoreDB, roots []string) ([]string, error) {
	return queryStrings(ctx, db, `WITH shadow_parents AS (
    SELECT DISTINCT parent_conversation_id AS id FROM shadow_bindings
), owners AS (
    SELECT unnest($1::text[]) AS id
    UNION SELECT m.conversation_id FROM managed_conversations m JOIN shadow_parents p ON p.id = m.conversation_id
        WHERE m.producer_inbound_json->>'SyncDestination' = ANY($1)
    UNION SELECT e.private_conversation_id FROM external_mcp_sessions e JOIN shadow_parents p ON p.id IN (e.private_conversation_id, e.managed_conversation_id)
        WHERE e.managed_conversation_id = ANY($1)
), roots AS (
    SELECT id FROM owners UNION SELECT b.child_conversation_id FROM shadow_bindings b JOIN owners o ON o.id = b.parent_conversation_id
), histories AS (
    SELECT conversation_id FROM session_entries UNION SELECT conversation_id FROM active_turns
    UNION SELECT conversation_id FROM turn_steps UNION SELECT conversation_id FROM attachments
    UNION SELECT conversation_id FROM background_jobs UNION SELECT conversation_id FROM session_summaries
), nested AS (
    SELECT h.conversation_id, r.id AS root FROM histories h JOIN roots r ON starts_with(h.conversation_id, r.id || '/')
), claimed AS (
    SELECT DISTINCT n.conversation_id, n.root FROM nested n JOIN managed_conversations m ON m.conversation_id <> n.root AND m.parent_conversation_id = ''
        AND (m.conversation_id = n.conversation_id OR starts_with(n.conversation_id, m.conversation_id || '/'))
)
SELECT id FROM roots UNION SELECT n.conversation_id FROM nested n LEFT JOIN claimed c USING (conversation_id, root) WHERE c.conversation_id IS NULL
ORDER BY 1`, "owned history IDs", roots)
}

// beginHistoryMutation returns locked ownership only after live family work settles.
// First admission shares the root lock. A parent already past that lock can still
// add children before its interruption settles, so enumerate again afterward.
// No SQL lock or mutex crosses worker settlement. An idle mutation never pauses or
// interrupts: it fails with errHistoryBusy while any family worker or comparison
// is live, and otherwise returns the locked family at once.
func (s *SessionService) beginHistoryMutation(ctx context.Context, roots []string, idle bool) (tx *sql.Tx, ids []string, workers []*Bridge, err error) {
	pausedIDs := []string{}
	settled := false

	defer func() {
		if err != nil {
			if tx != nil {
				_ = tx.Rollback()
			}

			releaseHistory(workers)
		}
	}()

	for {
		tx, err = s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, nil, workers, fmt.Errorf("begin history mutation: %w", err)
		}

		// Lock the whole family in the shared order, then enumerate it again
		// under those locks. If it grew, rolling back releases every lock.
		var locked []string

		locked, err = ownedHistoryIDs(ctx, tx, roots)
		if err != nil {
			return tx, nil, workers, err
		}

		if err := lockHistories(ctx, tx, locked); err != nil {
			return tx, nil, workers, err
		}

		ids, err = ownedHistoryIDs(ctx, tx, roots)
		if err != nil {
			return tx, nil, workers, err
		}

		if slices.ContainsFunc(ids, func(id string) bool { return !slices.Contains(locked, id) }) {
			_ = tx.Rollback()
			continue
		}

		if idle {
			var open bool
			if err := queryScalar(ctx, tx, "read open comparisons", `SELECT count(*) > 0 FROM shadow_turns WHERE child_conversation_id = ANY($1) AND terminal = ''`, &open, ids); err != nil {
				return tx, nil, workers, err
			}

			// Locks are held, so work that starts after this check waits for the commit.
			if open || s.historyWorkers.historyBusy(ids) {
				return tx, nil, workers, errHistoryBusy
			}

			return tx, ids, workers, nil
		}

		var pending []string

		pending, err = queryStrings(ctx, tx, `SELECT m.conversation_id FROM managed_conversations m
WHERE m.conversation_id = ANY($1) AND NOT (m.conversation_id = ANY($2::text[])) AND (
    EXISTS (SELECT 1 FROM shadow_bindings b WHERE b.parent_conversation_id = m.conversation_id OR b.child_conversation_id = m.conversation_id)
    OR EXISTS (SELECT 1 FROM external_mcp_sessions e JOIN shadow_bindings b ON b.parent_conversation_id = e.managed_conversation_id
        WHERE e.private_conversation_id = m.conversation_id)) ORDER BY 1`, "unsettled shadow family workers", ids, pausedIDs)
		if err != nil {
			return tx, nil, workers, err
		}

		if len(pending) == 0 && settled {
			return tx, ids, workers, nil
		}

		if len(pending) > 0 {
			if err = tx.Commit(); err != nil {
				return tx, nil, workers, fmt.Errorf("commit history ownership: %w", err)
			}

			var paused []*Bridge

			paused, err = s.historyWorkers.pauseHistory(ctx, pending)

			workers = append(workers, paused...)
			if err != nil {
				return tx, nil, workers, err
			}

			pausedIDs = append(pausedIDs, pending...)
			settled = false

			continue
		}
		// Flags precede this lock acquisition, so even admission already past its
		// in-memory check has committed before cancellation is written.
		if _, err = tx.ExecContext(ctx, `UPDATE shadow_turns SET terminal = $2, input_closed = TRUE
WHERE child_conversation_id = ANY($1) AND terminal = ''`, ids, protocol.TerminalStopped); err != nil {
			return tx, nil, workers, fmt.Errorf("cancel owned comparisons: %w", err)
		}

		if err = tx.Commit(); err != nil {
			return tx, nil, workers, fmt.Errorf("commit comparison cancellation: %w", err)
		}

		for _, id := range ids {
			s.jobs.abandon(id)
		}

		if err := settleHistoryWorkers(ctx, workers); err != nil {
			return tx, nil, workers, err
		}

		settled = true
	}
}

// deleteOwnedConversations shares full removal between retention and failed MCP creation.
func deleteOwnedConversations(ctx context.Context, db stateStoreDB, ids []string) error {
	for _, id := range ids {
		for _, table := range []string{"active_turns", "turn_steps", "background_jobs", "conversation_goals", "thread_queue", "scheduled_messages", "managed_conversations"} {
			if _, err := db.ExecContext(ctx, `DELETE FROM `+table+` WHERE conversation_id = $1`, id); err != nil {
				return fmt.Errorf("delete owned %s: %w", table, err)
			}
		}
	}

	return nil
}

// releaseHistory returns settled workers to admission after owned state is erased.
func releaseHistory(workers []*Bridge) {
	for _, b := range workers {
		b.mu.Lock()
		b.historyMutation = false
		b.mu.Unlock()
	}
}

func (m *threadBridgeManager) pauseHistory(ctx context.Context, ids []string) (workers []*Bridge, err error) {
	for _, id := range ids {
		b, err := m.recordedBridge(ctx, id)
		if err != nil {
			return nil, err
		}

		workers = append(workers, b)
	}

	for i, b := range workers {
		b.mu.Lock()
		if b.historyMutation {
			b.mu.Unlock()
			releaseHistory(workers[:i])

			return nil, errHistoryBusy
		}

		b.historyMutation, b.inputOpen = true, false
		b.mu.Unlock()
	}

	return workers, nil
}

// historyBusy reports whether a live worker of ids runs, holds, or awaits work.
// It never starts a worker: a conversation without one has nothing in flight.
func (m *threadBridgeManager) historyBusy(ids []string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	return slices.ContainsFunc(ids, func(id string) bool {
		b, ok := m.bridges[id].(*Bridge)
		if !ok {
			return false
		}

		b.mu.Lock()
		defer b.mu.Unlock()

		return b.handling || b.historyMutation || len(b.requestCh) > 0 || len(b.waiting) > 0
	})
}

func (m *threadBridgeManager) settleHistory(ctx context.Context, ids []string) ([]*Bridge, error) {
	workers, err := m.pauseHistory(ctx, ids)
	if err != nil {
		return nil, err
	}

	if err := settleHistoryWorkers(ctx, workers); err != nil {
		releaseHistory(workers)
		return nil, err
	}

	return workers, nil
}

func settleHistoryWorkers(ctx context.Context, workers []*Bridge) error {
	// A destination can own its producer's settlement. Interrupt all sides before
	// waiting for any one of them, regardless of conversation-ID ordering.
	for _, b := range workers {
		b.InterruptActiveTurn()
	}

	for _, b := range workers {
		b.mu.Lock()
		settlement := b.settlement
		b.mu.Unlock()

		if settlement != nil {
			select {
			case <-ctx.Done():
				return fmt.Errorf("wait for history settlement: %w", ctx.Err())
			case <-settlement.done:
				if settlement.err != nil {
					return settlement.err
				}
			}
		}
	}

	return nil
}

// A mid-turn message selects its owning turn, including all physical checkpoints.
// Unmaterialized future manifests belong to the same discarded branch.
const shadowRevertSQL = `WITH affected AS (
SELECT t.* FROM shadow_turns t WHERE t.parent_source_conversation_id = $1 AND t.id >= (
    SELECT COALESCE(min(s.id) FILTER (WHERE s.parent_turn_id = $3), min(s.id) FILTER (WHERE (
        EXISTS (SELECT 1 FROM session_entries e WHERE e.conversation_id = $1
            AND e.id >= $2 AND e.entry_json->>'turn_id' = s.parent_turn_id)
        OR NOT EXISTS (SELECT 1 FROM session_entries e WHERE e.conversation_id = $1 AND e.entry_json->>'turn_id' = s.parent_turn_id)
            AND s.id > COALESCE((SELECT min(k.id) FROM shadow_turns k WHERE k.parent_source_conversation_id = $1 AND k.parent_turn_id = $3),
                (SELECT max(k.id) FROM shadow_turns k WHERE k.parent_source_conversation_id = $1 AND EXISTS (
                    SELECT 1 FROM session_entries e WHERE e.conversation_id = $1 AND e.id < $2 AND e.entry_json->>'turn_id' = k.parent_turn_id)), 0))))
    FROM shadow_turns s WHERE s.parent_source_conversation_id = $1 AND s.child_conversation_id = t.child_conversation_id
)) `

// settleShadowRevert commits cancellation before interrupting either live side.
// A crash after the commit cannot restart effects from the discarded branch.
func (r *Runtime) settleShadowRevert(ctx context.Context, parent string, boundary *ObservedSessionEntry) ([]*Bridge, error) {
	tx, err := r.Sessions.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin comparison cancellation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockShadowHistory(ctx, tx, parent); err != nil {
		return nil, err
	}

	if err := stageShadowRevertDB(ctx, tx, parent, boundary); err != nil {
		return nil, err
	}

	children, err := queryStrings(ctx, tx, shadowRevertSQL+`SELECT DISTINCT a.child_conversation_id FROM affected a
JOIN active_turns t ON t.conversation_id = a.child_conversation_id AND t.id = a.child_turn_id WHERE t.phase <> 'done' ORDER BY 1`, "reverted live children", parent, boundary.ID, boundary.Entry.TurnID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit comparison cancellation: %w", err)
	}

	return r.threads.settleHistory(ctx, children)
}

func lockShadowHistory(ctx context.Context, db stateStoreDB, parent string) error {
	if err := lockSessionHistory(ctx, db, parent); err != nil {
		return err
	}

	ids, err := queryStrings(ctx, db, `SELECT child_conversation_id FROM shadow_bindings WHERE parent_conversation_id = $1`, "shadow history locks", parent)
	if err != nil {
		return err
	}

	return lockHistories(ctx, db, ids)
}

func stageShadowRevertDB(ctx context.Context, db stateStoreDB, parent string, entry *ObservedSessionEntry) error {
	if _, err := db.ExecContext(ctx, shadowRevertSQL+`UPDATE shadow_turns t SET terminal = $4, input_closed = TRUE
FROM affected a WHERE t.id = a.id AND t.terminal = ''`, parent, entry.ID, entry.Entry.TurnID, protocol.TerminalStopped); err != nil {
		return fmt.Errorf("cancel reverted comparisons: %w", err)
	}

	_, err := execState(ctx, db, "stage comparison boundaries", shadowRevertSQL+`, boundaries AS (
SELECT a.child_conversation_id, min(cut.id) AS id FROM affected a CROSS JOIN LATERAL (
    SELECT e.id FROM session_entries e WHERE e.conversation_id = a.child_conversation_id AND e.entry_json->>'turn_id' = a.child_turn_id
    UNION ALL SELECT t.history_anchor_id + 1 FROM active_turns t WHERE t.conversation_id = a.child_conversation_id AND t.id = a.child_turn_id
) cut GROUP BY a.child_conversation_id)
UPDATE managed_conversations m SET revert_message_id = b.id::text || ':0' FROM boundaries b WHERE m.conversation_id = b.child_conversation_id`, parent, entry.ID, entry.Entry.TurnID)

	return err
}

func commitShadowRevertDB(ctx context.Context, db stateStoreDB, parent string, entry *ObservedSessionEntry) error {
	children, err := queryStrings(ctx, db, shadowRevertSQL+`SELECT DISTINCT child_conversation_id FROM affected ORDER BY 1`, "discarded shadow histories", parent, entry.ID, entry.Entry.TurnID)
	if err != nil {
		return err
	}

	for _, child := range children {
		if err := pruneRevertDelegations(ctx, db, child); err != nil {
			return err
		}

		if _, err := db.ExecContext(ctx, `DELETE FROM session_entries e USING managed_conversations m WHERE e.conversation_id = $1 AND m.conversation_id = $1
    AND m.revert_message_id <> '' AND e.id >= split_part(m.revert_message_id, ':', 1)::bigint`, child); err != nil {
			return fmt.Errorf("rewind comparison history: %w", err)
		}

		if _, err := db.ExecContext(ctx, `UPDATE managed_conversations SET revert_message_id = '' WHERE conversation_id = $1`, child); err != nil {
			return fmt.Errorf("clear comparison cutoff: %w", err)
		}

		if _, err := db.ExecContext(ctx, `DELETE FROM session_summaries WHERE conversation_id = $1`, child); err != nil {
			return fmt.Errorf("clear comparison summary: %w", err)
		}

		summary, err := loadSessionSummary(ctx, db, child)
		if err != nil {
			return err
		}

		if err := saveSessionSummary(ctx, db, summary); err != nil {
			return err
		}
	}

	_, errExec := execState(ctx, db, "discard comparison branch", shadowRevertSQL+`, journals AS (
DELETE FROM turn_steps s USING affected a WHERE s.conversation_id = a.child_conversation_id
    AND (s.key = a.child_turn_id OR starts_with(s.key, a.child_turn_id || '/'))
), turns AS (DELETE FROM active_turns t USING affected a WHERE t.conversation_id = a.child_conversation_id AND t.id = a.child_turn_id)
DELETE FROM shadow_turns t USING affected a WHERE t.id = a.id`, parent, entry.ID, entry.Entry.TurnID)

	return errExec
}

// turnInputSnapshot is the consumed stream, not an admission queue or a cursor.
// Applied identities advance only in RocketCode's own journal checkpoint.
type turnInputSnapshot struct {
	Agent          string                      `json:"agent"`
	Inputs         []rocketcode.PromptInput    `json:"inputs"`
	ContextEntries []rocketcode.SessionEntry   `json:"context_entries"`
	ChildContext   []rocketcode.SessionEntry   `json:"child_context"`
	Environment    map[string]string           `json:"environment"`
	Closed         bool                        `json:"closed"`
	GoalTurn       bool                        `json:"goal_turn"`
	Workflow       protocol.WorkflowInvocation `json:"workflow"`
	WorkflowSource string                      `json:"workflow_source"`
	WorkflowPath   string                      `json:"workflow_path"`
}

// emptyTurnInputsError reports a turn whose saved input holds no prompt to run.
type emptyTurnInputsError struct{ TurnID string }

func (e emptyTurnInputsError) Error() string {
	return "turn " + e.TurnID + " has saved input but no prompt to run"
}

type inputPersistenceError struct{ cause error }

func (e inputPersistenceError) Error() string { return "persist consumed input: " + e.cause.Error() }
func (e inputPersistenceError) Unwrap() error { return e.cause }

func loadTurnInputs(ctx context.Context, db stateStoreDB, conversation, turn string) (turnInputSnapshot, bool, error) {
	var snapshot turnInputSnapshot

	var data []byte

	err := queryScalar(ctx, db, "load canonical input", `SELECT value FROM turn_steps WHERE conversation_id = $1 AND key = $2`, &data, conversation, turn+"/canonical")
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, false, nil
	}

	if err != nil {
		return snapshot, false, err
	}

	if err := json.Unmarshal(data, &snapshot); err != nil {
		return snapshot, false, fmt.Errorf("decode canonical input: %w", err)
	}

	return snapshot, len(snapshot.Inputs) > 0 || snapshot.WorkflowSource != "", nil
}

func saveTurnInputs(ctx context.Context, db stateStoreDB, conversation, turn string, snapshot *turnInputSnapshot) error {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode canonical input: %w", err)
	}
	// Both sides see the payload only after this transaction commits. Parent
	// cleanup removes its journal copy, but not the child's unconsumed manifest.
	if _, err := db.ExecContext(ctx, `WITH journaled AS (
INSERT INTO turn_steps (conversation_id, key, value) VALUES ($1, $2 || '/canonical', $3)
ON CONFLICT (conversation_id, key) DO UPDATE SET value = excluded.value)
UPDATE shadow_turns SET inputs = $3::jsonb->'inputs', context_entries = $3::jsonb - 'inputs' - 'environment' - 'closed',
environment = $3::jsonb->'environment', input_closed = ($3::jsonb->>'closed')::boolean
WHERE parent_source_conversation_id = $1 AND parent_turn_id = $2`, conversation, turn, string(removeSessionEntryNUL(data))); err != nil {
		return fmt.Errorf("save canonical input and fan-out: %w", err)
	}

	if snapshot.Closed {
		return releaseShadowPayloads(ctx, db, conversation, turn)
	}

	return nil
}

// releaseShadowPayloads needs both durable closure and successful child saving.
// A running child may already have checkpoints but still needs its final drain.
func releaseShadowPayloads(ctx context.Context, db stateStoreDB, conversation, turn string) error {
	_, err := execState(ctx, db, "release completed comparison payloads", `UPDATE shadow_turns t SET inbound_json = '{}', inputs = '[]', context_entries = '{}', environment = '{}'
WHERE ((parent_source_conversation_id = $1 AND parent_turn_id = $2) OR (child_conversation_id = $1 AND child_turn_id = $2))
    AND input_closed AND terminal = '' AND child_turn_id <> ''
    AND NOT EXISTS (SELECT 1 FROM active_turns a WHERE a.conversation_id = t.child_conversation_id AND a.id = t.child_turn_id
        AND (a.phase = 'running' OR a.terminal <> ''))
    AND EXISTS (SELECT 1 FROM session_entries e WHERE e.conversation_id = t.child_conversation_id AND e.entry_json->>'turn_id' = t.child_turn_id)`, conversation, turn)

	return err
}

func (s *SessionService) shadowInputs(ctx context.Context, child, turn string) (turnInputSnapshot, bool, error) {
	var (
		snapshot                     turnInputSnapshot
		inputs, entries, environment []byte
	)

	err := s.db.QueryRowContext(ctx, `SELECT inputs, context_entries, environment, input_closed FROM shadow_turns WHERE child_conversation_id = $1 AND child_turn_id = $2`, child, turn).Scan(&inputs, &entries, &environment, &snapshot.Closed)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, false, nil
	}

	if err != nil {
		return snapshot, false, fmt.Errorf("load mirrored input: %w", err)
	}

	for _, field := range []struct {
		data   []byte
		target any
	}{{entries, &snapshot}, {inputs, &snapshot.Inputs}, {environment, &snapshot.Environment}} {
		if err := json.Unmarshal(field.data, field.target); err != nil {
			return snapshot, false, fmt.Errorf("decode mirrored input: %w", err)
		}
	}

	return snapshot, true, nil
}

func (s *SessionService) captureTurnInputs(ctx context.Context, conversation, turn string, snapshot *turnInputSnapshot) error {
	tx, err := s.beginStateTx(ctx, "prepared input capture")
	if err != nil {
		return inputPersistenceError{cause: err}
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, conversation); err != nil {
		return inputPersistenceError{cause: err}
	}

	if err := saveTurnInputs(ctx, tx, conversation, turn, snapshot); err != nil {
		return inputPersistenceError{cause: err}
	}

	if err := tx.Commit(); err != nil {
		return inputPersistenceError{cause: fmt.Errorf("commit prepared input: %w", err)}
	}

	return nil
}

func (b *Bridge) drainShadowInputs(ctx context.Context, phase rocketcode.TurnPhase, applied []string) ([]rocketcode.PromptInput, error) {
	// Register before rereading durable state, so a commit between registration
	// and the query cannot strand a final-answer wait. No lock crosses the wait.
	b.mu.Lock()
	wake := make(chan struct{}, 1)
	b.shadowInputWake = wake
	turn := b.activeTurnID

	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.shadowInputWake = nil
		b.mu.Unlock()
	}()

	for {
		snapshot, _, err := b.config.SessionService.shadowInputs(ctx, b.config.ConversationID, turn)
		if err != nil {
			return nil, inputPersistenceError{cause: err}
		}

		inputs := slices.DeleteFunc(snapshot.Inputs, func(input rocketcode.PromptInput) bool { return slices.Contains(applied, input.ID) })
		if len(inputs) > 0 || snapshot.Closed || phase != rocketcode.TurnPhaseFinalAnswer {
			return inputs, nil
		}

		select {
		case <-wake:
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for mirrored input: %w", context.Cause(ctx))
		}
	}
}

func (b *Bridge) notifyShadowInputs(ctx context.Context, turn string) error {
	children, err := queryStrings(ctx, b.config.SessionService.db, `SELECT child_conversation_id FROM shadow_turns WHERE parent_source_conversation_id = $1 AND parent_turn_id = $2`, "mirrored input waiters", b.config.ConversationID, turn)
	if err != nil {
		return err
	}

	for _, id := range children {
		b.threads.mu.Lock()
		child, found := b.threads.bridges[id].(*Bridge)
		b.threads.mu.Unlock()

		if found {
			var (
				stopped   bool
				childTurn string
				cancel    context.CancelFunc
			)
			if err := b.config.SessionService.db.QueryRowContext(ctx, `SELECT terminal = $3, child_turn_id FROM shadow_turns WHERE child_conversation_id = $1 AND parent_turn_id = $2 AND parent_source_conversation_id = $4`, id, turn, protocol.TerminalStopped, b.config.ConversationID).Scan(&stopped, &childTurn); err != nil {
				return fmt.Errorf("read shadow stop: %w", err)
			}

			child.mu.Lock()

			if stopped && child.activeTurnID == childTurn {
				child.activeTurnInterrupted = true

				cancel = child.activeTurnCancel
				select {
				case child.activeTurnInterrupts <- os.Interrupt:
				default:
				}
			}

			select {
			case child.shadowInputWake <- struct{}{}:
			default:
			}
			child.mu.Unlock()

			if cancel != nil {
				cancel()
			}

			if err := child.PickLaterWork(ctx); err != nil {
				return err
			}
		}
	}

	return nil
}

func (b *Bridge) finishStoppedShadow(ctx context.Context, request *bridgeRequest) (bool, error) {
	if !b.shadow {
		return false, nil
	}

	var stopped bool
	if err := b.config.SessionService.db.QueryRowContext(ctx, `SELECT terminal = $3 FROM shadow_turns WHERE child_conversation_id = $1 AND child_turn_id = $2`, b.config.ConversationID, request.turnID, protocol.TerminalStopped).Scan(&stopped); err != nil {
		return false, inputPersistenceError{cause: fmt.Errorf("read recovered shadow stop: %w", err)}
	}

	if !stopped {
		return false, nil
	}

	return true, b.finish(ctx, request, &turnFinish{store: newSessionStore(b.config.ConversationID, b.config.SessionService)}, &runResult{turnID: request.turnID}, protocol.TerminalStopped)
}

func (b *Bridge) startShadowWorkers(ctx context.Context, turn string) error {
	children, err := queryStrings(ctx, b.config.SessionService.db, `SELECT child_conversation_id FROM shadow_turns WHERE parent_source_conversation_id = $1 AND parent_turn_id = $2`, "ready shadow workers", b.config.ConversationID, turn)
	if err != nil {
		return err
	}

	for _, id := range children {
		if err := b.threads.PickLaterWork(ctx, id); err != nil {
			return fmt.Errorf("wake shadow worker: %w", err)
		}
	}

	return nil
}

// reconcileShadows attaches new definitions once under the parent's history lock.
// Existing bindings survive removal and switching; the next activation fixes its roster.
func (s *SessionService) reconcileShadows(ctx context.Context, db stateStoreDB, parent, agent string) ([]string, error) {
	var child bool
	if err := queryScalar(ctx, db, "read shadow ownership", `SELECT EXISTS (SELECT 1 FROM shadow_bindings WHERE child_conversation_id = $1)`, &child, parent); err != nil || child {
		return nil, err
	}

	cfg := s.definitions.Clone()

	root, err := os.OpenRoot(cfg.Workspace)
	if err != nil {
		return nil, fmt.Errorf("open shadow definitions: %w", err)
	}

	defer func() { _ = root.Close() }()

	// No matching directory is the ordinary, unshadowed case. Do not load unrelated
	// definitions or require a skills directory just to create a conversation.
	info, err := root.Stat(filepath.Join(cfg.RuntimeDirName(), "agents", agent))
	if errors.Is(err, fs.ErrNotExist) || err == nil && !info.IsDir() {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read shadow directory: %w", err)
	}

	agentsFS, err := fs.Sub(root.FS(), filepath.ToSlash(filepath.Join(cfg.RuntimeDirName(), "agents")))
	if err != nil {
		return nil, fmt.Errorf("open shadow agents directory: %w", err)
	}

	definitions, err := loadAgentDefinitions(agentsFS, cfg)
	if err != nil {
		return nil, err
	}

	names := definitions.ShadowNames(agent)
	if len(names) > 0 {
		if _, err := (stateDAO{db: db}).createConversation(ctx, protocol.Conversation{ID: parent, Agent: agent}); err != nil {
			return nil, err
		}
	}

	for _, name := range names {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM shadow_bindings WHERE parent_conversation_id = $1 AND parent_agent = $2 AND shadow_agent = $3)`, parent, agent, name).Scan(&exists); err != nil {
			return nil, fmt.Errorf("read shadow binding: %w", err)
		}

		if exists {
			continue
		}

		id := rand.Text()
		if _, err := (stateDAO{db: db}).createConversation(ctx, protocol.Conversation{ID: id, Agent: name}); err != nil {
			return nil, err
		}

		if _, err := db.ExecContext(ctx, `INSERT INTO shadow_bindings (child_conversation_id, parent_conversation_id, parent_agent, shadow_agent) VALUES ($1, $2, $3, $4)`, id, parent, agent, name); err != nil {
			return nil, fmt.Errorf("attach shadow binding: %w", err)
		}

		observed, err := (stateDAO{db: db}).observedEntries(ctx, parent)
		if err != nil {
			return nil, err
		}

		var entries []rocketcode.SessionEntry

		producers := []string{parent}

		for i := range observed {
			entry := &observed[i]
			if entry.Synced && entry.SourceConversationID == "" {
				continue
			}

			if entry.SourceConversationID != "" {
				producers = append(producers, entry.SourceConversationID)
			}

			if len(entry.Entry.ReplayInput) > 0 {
				entries = append(entries, entry.Entry)
			}
		}

		if _, err := s.copySessionPrefix(ctx, db, id, entries, producers, ""); err != nil {
			return nil, err
		}
	}

	return names, nil
}

// admitShadowInputs fixes identity and roster in the parent's admission transaction.
func (b *Bridge) admitShadowInputs(ctx context.Context, db stateStoreDB, request *bridgeRequest, agent, turn string) error {
	if err := b.config.SessionService.startShadowManifests(ctx, db, b.config.ConversationID, agent, turn, request.inbound); err != nil {
		return err
	}
	// Prepared workflows fix their agent before activation. Other requests fix it
	// here, atomically with the roster, for execution and restart.
	snapshot := turnInputSnapshot{Agent: agent, Inputs: []rocketcode.PromptInput{}}

	if definition := request.workflow.definition; definition != nil {
		var shadowed bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM shadow_turns WHERE parent_source_conversation_id = $1 AND parent_turn_id = $2)`, b.config.ConversationID, turn).Scan(&shadowed); err != nil {
			return fmt.Errorf("read admitted workflow roster: %w", err)
		}

		if shadowed {
			snapshot.Workflow, snapshot.WorkflowSource, snapshot.WorkflowPath = request.inbound.Workflow, definition.Source, definition.Path
		}
	}

	return saveTurnInputs(ctx, db, b.config.ConversationID, turn, &snapshot)
}

// prepareWorkflowShadows captures deferred preparation before waking children.
func (b *Bridge) prepareWorkflowShadows(ctx context.Context, request *bridgeRequest) error {
	if b.shadow {
		return nil
	}

	snapshot, _, err := loadTurnInputs(ctx, b.config.SessionService.db, b.config.ConversationID, request.turnID)
	if err != nil {
		return inputPersistenceError{cause: err}
	}

	var shadowed bool
	if err := b.config.SessionService.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM shadow_turns WHERE parent_source_conversation_id = $1 AND parent_turn_id = $2)`, b.config.ConversationID, request.turnID).Scan(&shadowed); err != nil {
		return inputPersistenceError{cause: err}
	}

	if shadowed && snapshot.WorkflowSource == "" {
		snapshot.Workflow, snapshot.WorkflowSource, snapshot.WorkflowPath = request.inbound.Workflow, request.workflow.definition.Source, request.workflow.definition.Path
		if err := b.config.SessionService.captureTurnInputs(ctx, b.config.ConversationID, request.turnID, &snapshot); err != nil {
			return err
		}
	}

	if err := b.startShadowWorkers(ctx, request.turnID); err != nil {
		return inputPersistenceError{cause: err}
	}

	return nil
}

// startShadowManifests fixes the roster in the same transaction as parent admission.
// The manifest owns its payload independently of active-turn and journal cleanup.
func (s *SessionService) startShadowManifests(ctx context.Context, db stateStoreDB, parent, agent, turnID string, inbound *protocol.InboundMessage) error {
	names, err := s.reconcileShadows(ctx, db, parent, agent)
	if err != nil || len(names) == 0 {
		return err
	}

	data, _ := json.Marshal(inbound)

	_, err = execState(ctx, db, "start shadow manifests", `INSERT INTO shadow_turns (child_conversation_id, parent_source_conversation_id, parent_turn_id, inbound_json)
SELECT child_conversation_id, $1, $3, $4 FROM shadow_bindings
WHERE parent_conversation_id = $1 AND parent_agent = $2 AND shadow_agent = ANY($5)
ORDER BY shadow_agent`, parent, agent, turnID, string(removeSessionEntryNUL(data)), names)

	return err
}

// claimShadowTurn takes only the oldest pending manifest, after earlier child work
// settles. The history anchor and claim commit together under the child history lock.
func (s *SessionService) claimShadowTurn(ctx context.Context, child string) (activeTurn, bool, error) {
	tx, err := s.beginStateTx(ctx, "shadow turn claim")
	if err != nil {
		return activeTurn{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, child); err != nil {
		return activeTurn{}, false, err
	}

	var (
		id   int64
		data []byte
	)

	err = tx.QueryRowContext(ctx, `WITH head AS (
SELECT id, inbound_json, inputs, context_entries FROM shadow_turns WHERE child_conversation_id = $1 AND child_turn_id = '' AND terminal = '' ORDER BY id LIMIT 1)
SELECT id, inbound_json FROM head WHERE (jsonb_array_length(inputs) > 0 OR COALESCE(context_entries->>'workflow_source', '') <> '')
AND NOT EXISTS (SELECT 1 FROM active_turns WHERE conversation_id = $1 AND phase <> $2)`, child, turnDone).Scan(&id, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return activeTurn{}, false, nil
	}

	if err != nil {
		return activeTurn{}, false, fmt.Errorf("read shadow queue head: %w", err)
	}

	var parent protocol.InboundMessage
	if err := json.Unmarshal(data, &parent); err != nil {
		return activeTurn{}, false, fmt.Errorf("decode shadow admission: %w", err)
	}
	// Build a fresh request: connector reply, completion, goal, and sync ownership
	// belong to the parent, never to its comparison child.
	inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, parent.Text, false)
	inbound.ConversationID = child
	inbound.PreserveWhitespace, inbound.Workflow = parent.PreserveWhitespace, parent.Workflow

	turnID := fmt.Sprintf("shadow-turn-%d", id)
	if err := startTurnDB(ctx, tx, turnID, child, inbound); err != nil {
		return activeTurn{}, false, err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE shadow_turns SET child_turn_id = $2 WHERE id = $1`, id, turnID); err != nil {
		return activeTurn{}, false, fmt.Errorf("claim shadow turn: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return activeTurn{}, false, fmt.Errorf("commit shadow turn claim: %w", err)
	}

	return activeTurn{id: turnID, conversationID: child, phase: turnRunning, inbound: inbound}, true, nil
}
