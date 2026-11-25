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
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

var errHistoryBusy = errors.New("conversation history is busy")

type noHistoryWorkers struct{}

func (noHistoryWorkers) pauseHistory(context.Context, []string) ([]*Bridge, error) {
	return nil, nil
}

func (noHistoryWorkers) historyBusy([]string) bool {
	return false
}

func ownedHistoryIDs(ctx context.Context, db stateStoreDB, roots []string) ([]string, error) {
	return queryStrings(ctx, db, `WITH RECURSIVE shadow_parents AS (
    SELECT DISTINCT shadow_parent AS id FROM conversations WHERE shadow_parent IS NOT NULL
), owners AS (
    SELECT unnest($1::text[]) AS id
    UNION SELECT m.conversation_id FROM conversations m JOIN shadow_parents p ON p.id = m.conversation_id
        WHERE m.reports_to = ANY($1)
    UNION SELECT e.external_run FROM conversations e JOIN shadow_parents p ON p.id IN (e.external_run, e.conversation_id)
        WHERE e.conversation_id = ANY($1) AND e.external_id IS NOT NULL
), roots AS (
    SELECT id FROM owners UNION SELECT b.conversation_id FROM conversations b JOIN owners o ON o.id = b.shadow_parent
), `+descendantsSQL+`
SELECT id FROM roots UNION SELECT conversation_id FROM descendants ORDER BY 1`, "owned history IDs", roots)
}

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

			if open || s.historyWorkers.historyBusy(ids) {
				return tx, nil, workers, errHistoryBusy
			}

			return tx, ids, workers, nil
		}

		var pending []string

		pending, err = queryStrings(ctx, tx, `WITH shadowed AS (
    SELECT shadow_parent AS id FROM conversations WHERE shadow_parent IS NOT NULL
    UNION SELECT conversation_id FROM conversations WHERE shadow_parent IS NOT NULL
    UNION SELECT e.external_run FROM conversations e JOIN conversations b ON b.shadow_parent = e.conversation_id WHERE e.external_id IS NOT NULL
)
SELECT m.conversation_id FROM conversations m JOIN shadowed s ON s.id = m.conversation_id
WHERE m.conversation_id = ANY($1) AND NOT (m.conversation_id = ANY($2::text[])) ORDER BY 1`, "unsettled shadow family workers", ids, pausedIDs)
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

func (r *Runtime) settleShadowRevert(ctx context.Context, parent string) ([]*Bridge, error) {
	tx, err := r.Sessions.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin comparison cancellation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockShadowHistory(ctx, tx, parent); err != nil {
		return nil, err
	}

	children, err := queryStrings(ctx, tx, `WITH stopped AS (
    UPDATE shadow_turns SET terminal = $2, input_closed = TRUE WHERE parent_source_conversation_id = $1 AND terminal = '' RETURNING child_conversation_id, child_turn_id
)
SELECT DISTINCT s.child_conversation_id FROM stopped s
JOIN turns t ON t.conversation_id = s.child_conversation_id AND t.id = s.child_turn_id WHERE t.phase NOT IN ('waiting', 'done') ORDER BY 1`, "moved live children", parent, protocol.TerminalStopped)
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

	ids, err := queryStrings(ctx, db, `SELECT conversation_id FROM conversations WHERE shadow_parent = $1`, "shadow history locks", parent)
	if err != nil {
		return err
	}

	return lockHistories(ctx, db, ids)
}

func stageShadowRevertDB(ctx context.Context, db stateStoreDB, parent string) error {
	_, err := execState(ctx, db, "follow the parent's current point", `WITH `+sessionHistorySQL+`, path_turns AS (
    SELECT DISTINCT entry->>'turn_id' AS turn_id FROM effective_entries WHERE NOT synced AND revert_index < 0
), compared AS (
    SELECT s.child_conversation_id, e.id, p.turn_id IS NOT NULL AS kept,
        jsonb_array_length(CASE WHEN json_typeof(e.entry_json->'replay_input') = 'array' THEN e.entry_json::jsonb->'replay_input' ELSE '[]' END) AS length
    FROM shadow_turns s JOIN entries e ON e.conversation_id = s.child_conversation_id AND e.entry_json->>'turn_id' = s.child_turn_id
    LEFT JOIN path_turns p ON p.turn_id = s.parent_turn_id WHERE s.parent_source_conversation_id = $1 AND s.child_turn_id <> ''
), points AS (
    SELECT DISTINCT ON (child_conversation_id) child_conversation_id, id, kept, length FROM compared
    ORDER BY child_conversation_id, kept DESC, CASE WHEN kept THEN -id ELSE id END
), latest AS (
    SELECT DISTINCT ON (l.conversation_id) l.conversation_id, l.entry_id FROM history_links l JOIN points p ON p.child_conversation_id = l.conversation_id
    ORDER BY l.conversation_id, l.seq DESC
)
UPDATE conversations c SET revert_message_id = CASE WHEN NOT p.kept THEN p.id::text || ':0' WHEN p.id = l.entry_id THEN '' ELSE p.id::text || ':' || p.length END
FROM points p JOIN latest l ON l.conversation_id = p.child_conversation_id WHERE c.conversation_id = p.child_conversation_id`, parent)

	return err
}

type turnInputSnapshot struct {
	Agent          string                 `json:"agent"`
	Inputs         []harness.PromptInput  `json:"inputs"`
	ContextEntries []harness.SessionEntry `json:"context_entries"`
	ChildContext   []harness.SessionEntry `json:"child_context"`
	Environment    map[string]string      `json:"environment"`
	Closed         bool                   `json:"closed"`
	GoalTurn       bool                   `json:"goal_turn"`
	Resets         []string               `json:"resets"`
}

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

	found, err := scanFound("load canonical input", db.QueryRowContext(ctx, `SELECT value FROM steps WHERE conversation_id = $1 AND key = $2`, conversation, turn+"/canonical").Scan(&data))
	if err != nil || !found {
		return snapshot, false, err
	}

	if err := json.Unmarshal(data, &snapshot); err != nil {
		return snapshot, false, fmt.Errorf("decode canonical input: %w", err)
	}

	return snapshot, len(snapshot.Inputs) > 0, nil
}

func saveTurnInputs(ctx context.Context, db stateStoreDB, conversation, turn string, snapshot *turnInputSnapshot) error {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode canonical input: %w", err)
	}

	if _, err := db.ExecContext(ctx, `WITH journaled AS (
INSERT INTO steps (conversation_id, key, value) VALUES ($1, $2 || '/canonical', $3)
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

func releaseShadowPayloads(ctx context.Context, db stateStoreDB, conversation, turn string) error {
	_, err := execState(ctx, db, "release completed comparison payloads", `WITH settled AS (
    SELECT t.id, t.child_conversation_id, t.child_turn_id FROM shadow_turns t
    LEFT JOIN turns a ON a.conversation_id = t.child_conversation_id AND a.id = t.child_turn_id AND (a.phase = 'running' OR a.terminal <> '')
    WHERE (t.parent_source_conversation_id = $1 AND t.parent_turn_id = $2 OR t.child_conversation_id = $1 AND t.child_turn_id = $2)
        AND t.input_closed AND t.terminal = '' AND t.child_turn_id <> '' AND a.id IS NULL
), saved AS (
    SELECT DISTINCT s.id FROM settled s JOIN entries e ON e.conversation_id = s.child_conversation_id AND e.entry_json->>'turn_id' = s.child_turn_id
)
UPDATE shadow_turns t SET inbound_json = '{}', inputs = '[]', context_entries = '{}', environment = '{}' FROM saved s WHERE t.id = s.id`, conversation, turn)

	return err
}

func (s *SessionService) shadowInputs(ctx context.Context, child, turn string) (turnInputSnapshot, bool, error) {
	var (
		snapshot                     turnInputSnapshot
		inputs, entries, environment []byte
	)

	found, err := scanFound("load mirrored input", s.db.QueryRowContext(ctx, `SELECT inputs, context_entries, environment, input_closed FROM shadow_turns WHERE child_conversation_id = $1 AND child_turn_id = $2`, child, turn).Scan(&inputs, &entries, &environment, &snapshot.Closed))
	if err != nil || !found {
		return snapshot, false, err
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

func shadowStepSegment(applied, resets []string) int {
	return len(slices.DeleteFunc(slices.Clone(applied), func(id string) bool { return !slices.Contains(resets, id) }))
}

func (b *Bridge) drainShadowInputs(ctx context.Context, responseID string, phase harness.TurnPhase, applied []string) ([]harness.PromptInput, error) {
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

	db := b.config.SessionService.db

	for {
		snapshot, _, err := b.config.SessionService.shadowInputs(ctx, b.config.ConversationID, turn)
		if err != nil {
			return nil, inputPersistenceError{cause: err}
		}

		segment := shadowStepSegment(applied, snapshot.Resets)
		if _, err := db.ExecContext(ctx, `INSERT INTO shadow_steps (shadow_turn_id, child, response_id, segment)
SELECT id, TRUE, $3, $4 FROM shadow_turns WHERE child_conversation_id = $1 AND child_turn_id = $2 ON CONFLICT DO NOTHING`, b.config.ConversationID, turn, responseID, segment); err != nil {
			return nil, inputPersistenceError{cause: fmt.Errorf("record shadow step: %w", err)}
		}

		var steps, parentSteps int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE s.child), count(*) FILTER (WHERE NOT s.child)
FROM shadow_turns t JOIN shadow_steps s ON s.shadow_turn_id = t.id
WHERE t.child_conversation_id = $1 AND t.child_turn_id = $2 AND s.segment = $3`, b.config.ConversationID, turn, segment).Scan(&steps, &parentSteps); err != nil {
			return nil, inputPersistenceError{cause: fmt.Errorf("read shadow step budget: %w", err)}
		}

		capped := steps >= parentSteps*3/2
		inputs := slices.DeleteFunc(snapshot.Inputs, func(input harness.PromptInput) bool { return slices.Contains(applied, input.ID) })

		switch {
		case !capped && (len(inputs) > 0 || snapshot.Closed || phase != harness.TurnPhaseFinalAnswer):
			return inputs, nil
		case slices.ContainsFunc(inputs, func(input harness.PromptInput) bool { return slices.Contains(snapshot.Resets, input.ID) }):
			return inputs, nil
		case snapshot.Closed && phase == harness.TurnPhaseFinalAnswer:
			return nil, nil
		case snapshot.Closed:
			note := fmt.Sprintf("Hit the step limit: %d vs. parent's %d.", steps, parentSteps)
			if _, err := db.ExecContext(ctx, `UPDATE shadow_turns SET terminal = $3, limit_note = $4 WHERE child_conversation_id = $1 AND child_turn_id = $2 AND terminal = ''`, b.config.ConversationID, turn, protocol.TerminalStopped, note); err != nil {
				return nil, inputPersistenceError{cause: fmt.Errorf("stop shadow at step limit: %w", err)}
			}

			b.InterruptActiveTurn()

			return nil, nil
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

	return true, b.finish(ctx, request, &turnFinish{conversationID: strings.TrimSpace(b.config.ConversationID)}, &runResult{turnID: request.turnID}, protocol.TerminalStopped)
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

func (s *SessionService) reconcileShadows(ctx context.Context, db stateStoreDB, parent, agent string) ([]string, error) {
	var unshadowed bool
	if err := queryScalar(ctx, db, "read shadow ownership", `SELECT count(*) > 0 FROM conversations WHERE conversation_id = $1 AND (shadow_parent IS NOT NULL OR cron_path LIKE 'agents/%' OR kind = $2)`, &unshadowed, parent, KindAgentDraft); err != nil || unshadowed {
		return nil, err
	}

	cfg := s.definitions.Clone()

	root, err := os.OpenRoot(cfg.Workspace)
	if err != nil {
		return nil, fmt.Errorf("open shadow definitions: %w", err)
	}

	defer func() { _ = root.Close() }()

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

	skillsFS, err := fs.Sub(root.FS(), filepath.ToSlash(filepath.Join(cfg.RuntimeDirName(), "skills")))
	if err != nil {
		return nil, fmt.Errorf("open shadow skills directory: %w", err)
	}

	definitions, err := loadAgentDefinitions(agentsFS, skillsFS, cfg)
	if err != nil {
		return nil, err
	}

	names := definitions.ShadowNames(agent)

	return names, s.bindShadows(ctx, db, parent, agent, names)
}

func (s *SessionService) bindShadows(ctx context.Context, db stateStoreDB, parent, agent string, names []string) error {
	if len(names) > 0 {
		if _, err := (stateDAO{db: db}).createConversation(ctx, protocol.Conversation{ID: parent, Agent: agent}, conversationFacts{kind: KindWebChat}, harness.AgentStickyOff); err != nil {
			return err
		}
	}

	for _, name := range names {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT count(*) > 0 FROM conversations WHERE shadow_parent = $1 AND parent_agent = $2 AND shadow_agent = $3`, parent, agent, name).Scan(&exists); err != nil {
			return fmt.Errorf("read shadow binding: %w", err)
		}

		if exists {
			continue
		}

		id := rand.Text()
		if _, err := (stateDAO{db: db}).createConversation(ctx, protocol.Conversation{ID: id, Agent: name}, conversationFacts{kind: KindShadow}, harness.AgentStickyOff); err != nil {
			return err
		}

		if _, err := db.ExecContext(ctx, `UPDATE conversations SET shadow_parent = $2, parent_agent = $3, shadow_agent = $4 WHERE conversation_id = $1`, id, parent, agent, name); err != nil {
			return fmt.Errorf("attach shadow binding: %w", err)
		}

		observed, err := (stateDAO{db: db}).observedEntries(ctx, parent)
		if err != nil {
			return err
		}

		var entries []harness.SessionEntry

		producers := []string{parent}

		for i := range observed {
			entry := &observed[i]
			if entry.SourceConversationID != "" {
				producers = append(producers, entry.SourceConversationID)
			}

			if len(entry.Entry.ReplayInput) > 0 {
				entries = append(entries, entry.Entry)
			}
		}

		if _, err := s.copySessionPrefix(ctx, db, id, entries, producers, ""); err != nil {
			return err
		}
	}

	return nil
}

func (s *SessionService) startShadowManifests(ctx context.Context, db stateStoreDB, parent, agent, turnID string, inbound *protocol.InboundMessage) error {
	files, err := agentDraftFiles(inbound)
	if err != nil {
		return err
	}

	var names []string

	if files == nil {
		names, err = s.reconcileShadows(ctx, db, parent, agent)
	} else {
		for file := range files {
			if variant, ok := strings.CutPrefix(file, agent+"/"); ok {
				names = append(names, agent+"/"+strings.TrimSuffix(variant, ".md"))
			}
		}

		if _, ok := inbound.Metadata[agentDraftLiveMetadataKey]; ok {
			names = append(names, agent)
		}

		err = s.bindShadows(ctx, db, parent, agent, names)
	}

	if err != nil || len(names) == 0 {
		return err
	}

	data, _ := json.Marshal(inbound)

	_, err = execState(ctx, db, "start shadow manifests", `INSERT INTO shadow_turns (child_conversation_id, parent_source_conversation_id, parent_turn_id, inbound_json)
SELECT conversation_id, $1, $3, $4 FROM conversations
WHERE shadow_parent = $1 AND parent_agent = $2 AND shadow_agent = ANY($5)
ORDER BY shadow_agent`, parent, agent, turnID, string(removeSessionEntryNUL(data)), names)

	return err
}

func agentDraftFiles(msg *protocol.InboundMessage) (map[string]string, error) {
	data, ok := msg.Metadata[agentDraftFilesMetadataKey]
	if !ok {
		return nil, nil
	}

	var files map[string]string
	if err := json.Unmarshal([]byte(data), &files); err != nil {
		return nil, fmt.Errorf("decode agent draft files: %w", err)
	}

	return files, nil
}

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
		id                       int64
		data                     []byte
		shadowAgent, parentAgent string
	)

	found, err := scanFound("read shadow queue head", tx.QueryRowContext(ctx, `WITH head AS (
SELECT id, inbound_json, inputs FROM shadow_turns WHERE child_conversation_id = $1 AND child_turn_id = '' AND terminal = '' ORDER BY id LIMIT 1),
busy AS (SELECT count(*) AS n FROM turns WHERE conversation_id = $1 AND phase NOT IN ('waiting', 'done'))
SELECT h.id, h.inbound_json, c.shadow_agent, c.parent_agent FROM head h CROSS JOIN busy b JOIN conversations c ON c.conversation_id = $1
WHERE jsonb_array_length(h.inputs) > 0 AND b.n = 0`, child).Scan(&id, &data, &shadowAgent, &parentAgent))
	if err != nil || !found {
		return activeTurn{}, false, err
	}

	var parent protocol.InboundMessage
	if err := json.Unmarshal(data, &parent); err != nil {
		return activeTurn{}, false, fmt.Errorf("decode shadow admission: %w", err)
	}

	inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, parent.Text, false)
	inbound.ConversationID, inbound.OriginHeader = child, reviewOriginHeader(&parent)

	inbound.PreserveWhitespace = parent.PreserveWhitespace
	if name := parent.Metadata[protocol.InboundPrincipalNameMetadataKey]; name != "" {
		inbound.Metadata = map[string]string{protocol.InboundPrincipalNameMetadataKey: name, protocol.InboundPrincipalEmailMetadataKey: parent.Metadata[protocol.InboundPrincipalEmailMetadataKey]}
	}

	if files, ok := parent.Metadata[agentDraftFilesMetadataKey]; ok {
		if shadowAgent == parentAgent {
			live, _ := json.Marshal(map[string]string{shadowAgent + ".md": parent.Metadata[agentDraftLiveMetadataKey]})
			files = string(live)
		}

		if inbound.Metadata == nil {
			inbound.Metadata = map[string]string{}
		}

		inbound.Metadata[agentDraftFilesMetadataKey] = files
	}

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
