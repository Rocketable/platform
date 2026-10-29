package backend

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
	"github.com/Rocketable/platform/internal/rocketcode"
)

// CreateConversation records an explicit ID, a Web chat or a cron run, without changing existing selection.
func (r *Runtime) CreateConversation(ctx context.Context, conversation protocol.Conversation) error {
	return r.Sessions.recordConversation(ctx, conversation, conversationFacts{kind: KindWebChat})
}

func (s *SessionService) recordConversation(ctx context.Context, conversation protocol.Conversation, facts conversationFacts) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin conversation creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, conversation.ID); err != nil {
		return err
	}

	created, err := (stateDAO{db: tx}).createConversation(ctx, conversation, facts)
	if err != nil {
		return err
	}

	if created {
		if _, err := s.reconcileShadows(ctx, tx, conversation.ID, conversation.Agent); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit conversation creation: %w", err)
	}

	return nil
}

// ContinueRun has a conversation show, and search find, a hidden run's history before its own,
// taking the run's preview when it first does, and adopts the run when it reports to no
// conversation. Nothing is copied, and continuing again changes nothing.
func (r *Runtime) ContinueRun(ctx context.Context, run, conversationID string) error {
	tx, err := r.Sessions.beginStateTx(ctx, "run continuation")
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockHistories(ctx, tx, []string{run, conversationID}); err != nil {
		return err
	}

	if _, err := execState(ctx, tx, "link continued run", `UPDATE conversations c SET continues = $2, cron_run = $2,
    preview = COALESCE(p.preview, c.preview), last_updated = COALESCE(p.last_updated, c.last_updated) FROM conversations p WHERE c.conversation_id = $1 AND c.continues IS NULL AND p.conversation_id = $2`, conversationID, run); err != nil {
		return err
	}

	if err := refreshPrincipals(ctx, tx, []string{conversationID}); err != nil {
		return err
	}

	owner, schedules, err := (stateDAO{db: tx}).bindRun(ctx, run, conversationID)
	if err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit run continuation: %w", err)
	}

	return r.threads.armSchedules(ctx, owner, schedules)
}

// SlackThread returns the Slack channel and thread conversationID was stored with: both for a
// Slack thread, empty for a conversation that is none.
func (s *SessionService) SlackThread(ctx context.Context, conversationID string) (channelID, threadTS string, err error) {
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(max(slack_channel), ''), COALESCE(max(slack_thread), '') FROM conversations
WHERE conversation_id = $1`, conversationID).Scan(&channelID, &threadTS); err != nil {
		return "", "", fmt.Errorf("read Slack thread: %w", err)
	}

	return channelID, threadTS, nil
}

// CronRun reports whether conversationID is a cron run, recorded or not, and the definition path
// of the cron run it is or, as a Web chat, continues; empty when that is unknown.
func (s *SessionService) CronRun(ctx context.Context, conversationID string) (path string, run bool, err error) {
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(max(COALESCE(c.cron_path, r.cron_path)), ''), COALESCE(bool_or(c.kind IN ($2, $3)), FALSE)
FROM conversations c LEFT JOIN conversations r ON r.conversation_id = c.cron_run WHERE c.conversation_id = $1`, conversationID, KindCronRun, KindOneOffCronRun).Scan(&path, &run); err != nil {
		return "", false, fmt.Errorf("read cron run: %w", err)
	}

	return path, run, nil
}

// ListConversations returns recorded conversations, never discovered pair IDs.
func (r *Runtime) ListConversations(ctx context.Context) (conversations []protocol.Conversation, err error) {
	rows, err := r.Sessions.db.QueryContext(ctx, `SELECT conversation_id, agent, created_by FROM conversations WHERE kind <> 'remote' ORDER BY conversation_id`)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var conversation protocol.Conversation
		if err := rows.Scan(&conversation.ID, &conversation.Agent, &conversation.CreatedBy); err != nil {
			return nil, fmt.Errorf("read conversation: %w", err)
		}

		conversations = append(conversations, conversation)
	}

	if err := rows.Err(); err != nil {
		return conversations, fmt.Errorf("list conversation rows: %w", err)
	}

	return conversations, nil
}

// RunTurn waits for the submitted work's processing and terminal handling.
func (r *Runtime) RunTurn(ctx context.Context, inbound *protocol.InboundMessage) error {
	if err := rejectShadowMutation(ctx, r.Sessions.db, inbound.ConversationID); err != nil {
		return err
	}

	bridge, err := r.threads.recordedBridge(ctx, inbound.ConversationID)
	if err != nil {
		return err
	}

	conversationID := inbound.ConversationID
	if inbound.Kind == protocol.InboundKindCancel {
		bridge = bridge.stopTarget()
		conversationID = bridge.config.ConversationID

		if err := r.Sessions.StopGoal(conversationID); err != nil {
			return err
		}

		bridge.mu.Lock()
		completion := bridge.activeCompletion
		bridge.mu.Unlock()
		bridge.InterruptActiveTurn()

		if completion != nil {
			select {
			case <-ctx.Done():
				return fmt.Errorf("wait for interrupted turn: %w", ctx.Err())
			case <-completion.done:
				return completion.err
			}
		}

		message := protocol.NewOutboundMessage(conversationID, "")
		message.Complete, message.SlackReply = true, inbound.SlackReply

		return r.PublishOutbound(ctx, message)
	}

	// A message sent here runs after the input a stopped parent held here (awaitSubagent), released ahead of it.
	if err := bridge.releaseHeld(ctx); err != nil {
		return err
	}

	completion := &turnCompletion{done: make(chan struct{})}

	request := bridgeRequest{inbound: inbound, completion: completion}
	if inbound.Source == protocol.SourceWeb && inbound.Human && inbound.SyncDestination == "" && inbound.SlackReply == nil {
		_, eligible, _, err := r.Sessions.RevertState(ctx, conversationID)
		if err != nil {
			return err
		}

		if eligible {
			inbound.Workflow = inboundWorkflow(inbound)

			fresh, err := r.admitWeb(ctx, bridge, &request, protocol.GoalRequest{})
			if err != nil || !fresh {
				return err
			}

			request.queueItemID = inbound.Metadata["web_message_id"]
		}
	}

	if inbound.SyncDestination != "" {
		destination, err := r.threads.recordedBridge(ctx, inbound.SyncDestination)
		if err != nil {
			return err
		}

		request.producer = bridge
		bridge = destination
	}

	if err := bridge.enqueue(ctx, &request, "run turn"); err != nil {
		return errors.Join(err, request.closeWorkflow())
	}

	select {
	case <-bridge.stopCh:
		return protocol.ErrBridgeStopped
	case <-completion.done:
		return completion.err
	}
}

// StartGoal records an active goal on a recorded conversation and submits its
// first turn without waiting for the goal loop.
func (r *Runtime) StartGoal(ctx context.Context, inbound *protocol.InboundMessage, goal protocol.GoalRequest) error {
	if err := rejectShadowMutation(ctx, r.Sessions.db, inbound.ConversationID); err != nil {
		return err
	}

	bridge, err := r.threads.recordedBridge(ctx, inbound.ConversationID)
	if err != nil {
		return err
	}

	// The goal's work runs after the input a stopped parent held here, released ahead of it.
	if err := bridge.releaseHeld(ctx); err != nil {
		return err
	}

	if inbound.Source == protocol.SourceWeb {
		_, eligible, _, err := r.Sessions.RevertState(ctx, inbound.ConversationID)
		if err != nil {
			return err
		}

		if eligible {
			inbound.GoalAction = protocol.GoalActionKickoff

			data, _ := json.Marshal(goal) // Only strings and an integer.

			inbound.Metadata["web_goal"] = string(data)

			request := bridgeRequest{inbound: inbound}

			fresh, err := r.admitWeb(ctx, bridge, &request, goal)
			if err != nil || !fresh {
				return err
			}

			return bridge.enqueue(ctx, &bridgeRequest{inbound: inbound, queueItemID: inbound.Metadata["web_message_id"]}, "start Web goal")
		}
	}

	if err := r.Sessions.BeginGoal(inbound.ConversationID, &GoalState{Objective: goal.Objective, MaxTurns: goal.MaxTurns, PrincipalName: inbound.Metadata[protocol.InboundPrincipalNameMetadataKey], PrincipalEmail: inbound.Metadata[protocol.InboundPrincipalEmailMetadataKey]}); err != nil {
		return fmt.Errorf("persist goal: %w", err)
	}

	inbound.GoalAction = protocol.GoalActionKickoff

	return bridge.Submit(ctx, inbound)
}

// WorkflowDescriptions lists the saved workflows a human can start with $workflow.
func (r *Runtime) WorkflowDescriptions() (descriptions []protocol.WorkflowDescription, err error) {
	runtimeCfg := r.Cfg.Clone()

	root, err := os.OpenRoot(runtimeCfg.Workspace)
	if err != nil {
		return nil, fmt.Errorf("open workflow root: %w", err)
	}

	defer func() { err = errors.Join(err, root.Close()) }()

	definitions, err := workflow.Load(root, runtimeCfg.RuntimeDirName())
	if err != nil {
		return nil, fmt.Errorf("load workflow definitions: %w", err)
	}

	return workflow.Descriptions(definitions), nil
}

// QueueItems returns persisted waiting work plus uninjected active steers.
func (r *Runtime) QueueItems(conversationID string) ([]protocol.ThreadQueueItem, error) {
	return r.threads.queueItems(conversationID)
}

// PopQueueItem releases held work at the end of the ordinary queue.
func (r *Runtime) PopQueueItem(ctx context.Context, conversationID, id string) (bool, error) {
	if err := rejectShadowMutation(ctx, r.Sessions.db, conversationID); err != nil {
		return false, err
	}

	_, eligible, _, err := r.Sessions.RevertState(ctx, conversationID)
	if err != nil {
		return false, err
	}

	if eligible {
		return r.mutateWebQueue(ctx, conversationID, id, protocol.InboundKindEnqueue)
	}

	changed, err := execRows(ctx, r.Sessions.db, "pop queue item", "count popped queue items", popQueueItemSQL, conversationID, id, protocol.InboundKindEnqueue, protocol.InboundKindHeld, waitQueued)
	if err != nil || changed == 0 {
		return false, err
	}

	return true, r.threads.PickLaterWork(ctx, conversationID)
}

// PromoteQueueItem claims one persisted enqueue and submits it as a steer, keeping its principal.
func (r *Runtime) PromoteQueueItem(ctx context.Context, conversationID, id string) (bool, error) {
	if err := rejectShadowMutation(ctx, r.Sessions.db, conversationID); err != nil {
		return false, err
	}

	_, eligible, _, err := r.Sessions.RevertState(ctx, conversationID)
	if err != nil {
		return false, err
	}

	if eligible {
		return r.mutateWebQueue(ctx, conversationID, id, protocol.InboundKindSteer)
	}

	return r.threads.promoteQueueItem(ctx, conversationID, id)
}

// EditQueueItem changes only the text of a queued or held input that has not been claimed.
func (r *Runtime) EditQueueItem(ctx context.Context, conversationID, id, text string) (bool, error) {
	if err := rejectShadowMutation(ctx, r.Sessions.db, conversationID); err != nil {
		return false, err
	}

	b, err := r.threads.recordedBridge(ctx, conversationID)
	if err != nil {
		return false, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.historyMutation {
		return false, errors.New("conversation history is busy")
	}

	tx, err := r.Sessions.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin queue edit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, conversationID); err != nil {
		return false, err
	}

	var marker string
	if err := queryScalar(ctx, tx, "read queue edit cutoff", `SELECT COALESCE(revert_message_id, '') FROM conversations WHERE conversation_id = $1`, &marker, conversationID); err != nil {
		return false, err
	}

	if marker != "" {
		return false, errors.New("conversation history is reverted")
	}

	item, err := scanThreadQueueItem(tx.QueryRowContext(ctx, `SELECT `+waitingColumns+` FROM turns
WHERE conversation_id = $1 AND input_id = $2 AND phase = $3 AND reason <> $4 AND delivery IN ($5, $6) FOR UPDATE`, conversationID, id, turnWaiting, waitScheduled, protocol.InboundKindEnqueue, protocol.InboundKindHeld))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	item.Message, item.Content.Text = text, text

	var inbound *string

	updated := item.Content
	if item.Inbound != nil {
		if raw, ok := item.Inbound.Metadata[protocol.InboundRawTextMetadataKey]; ok && (len(updated.TextAttachments) > 0 || item.Inbound.Text != raw) {
			suffix := item.Inbound.Text
			if strings.TrimSpace(raw) != "" {
				suffix = strings.TrimPrefix(strings.TrimPrefix(suffix, raw), "\n\n")
			}

			updated.TextAttachments = nil
			if suffix != "" {
				updated.TextAttachments = []string{suffix}
			}
		}
	}

	updatedText := protocol.NewInboundMessageFromContent(item.Source, item.Kind, &updated, true).Text
	if item.Inbound != nil {
		item.Inbound.Text = updatedText
		if item.Inbound.Metadata == nil {
			item.Inbound.Metadata = make(map[string]string)
		}

		item.Inbound.Metadata[protocol.InboundRawTextMetadataKey] = text
		item.Inbound.Workflow = inboundWorkflow(item.Inbound)
		data, _ := json.Marshal(item.Inbound)
		inbound = new(string(removeSessionEntryNUL(data)))
	}

	content, _ := json.Marshal(item.Content)
	if _, err := tx.ExecContext(ctx, `UPDATE turns SET message = $3, content = $4, inbound_json = $5 WHERE conversation_id = $1 AND input_id = $2 AND phase = 'waiting'`, conversationID, id, text, content, inbound); err != nil {
		return false, fmt.Errorf("edit queue text: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit queue edit: %w", err)
	}

	for i := range b.waiting {
		request := &b.waiting[i]
		if request.queueItemID != id {
			continue
		}

		if request.inbound.Text != updatedText {
			if err := request.closeWorkflow(); err != nil {
				return false, err
			}
		}

		request.inbound.Text = updatedText
		if request.inbound.Metadata == nil {
			request.inbound.Metadata = make(map[string]string)
		}

		request.inbound.Metadata[protocol.InboundRawTextMetadataKey] = text
		request.inbound.Workflow = inboundWorkflow(request.inbound)
	}

	return true, nil
}

// DeleteQueueItem drops one waiting steer or enqueue so it never runs.
func (r *Runtime) DeleteQueueItem(ctx context.Context, conversationID, id string) (bool, error) {
	if err := rejectShadowMutation(ctx, r.threads.store.db, conversationID); err != nil {
		return false, err
	}

	return r.threads.deleteQueueItem(ctx, conversationID, id)
}

// ReorderQueueItems writes persisted enqueue positions in the given ID order.
func (r *Runtime) ReorderQueueItems(conversationID string, ids []string) error {
	if err := rejectShadowMutation(context.Background(), r.Sessions.db, conversationID); err != nil {
		return err
	}

	for i, id := range ids {
		if _, err := r.Sessions.db.ExecContext(context.Background(), `UPDATE turns SET position = $1 WHERE conversation_id = $2 AND input_id = $3 AND phase = 'waiting'`, i, conversationID, id); err != nil {
			return fmt.Errorf("reorder thread queue: %w", err)
		}
	}

	return nil
}

// StashQueueItem persists waiting work and offers it to the conversation bridge.
func (r *Runtime) StashQueueItem(ctx context.Context, conversationID string, item *protocol.ThreadQueueItem) error {
	if err := rejectShadowMutation(ctx, r.Sessions.db, conversationID); err != nil {
		return err
	}

	if item.Source == protocol.SourceWeb {
		_, eligible, _, err := r.Sessions.RevertState(ctx, conversationID)
		if err != nil {
			return err
		}

		if eligible {
			bridge, err := r.threads.recordedBridge(ctx, conversationID)
			if err != nil {
				return err
			}

			inbound := item.Inbound
			if inbound == nil {
				content := item.Content
				content.Text = item.Message
				inbound = protocol.NewInboundMessageFromContent(item.Source, item.Kind, &content, true)
				inbound.ConversationID = conversationID
				inbound.Metadata["web_message_id"], inbound.Metadata[protocol.InboundPrincipalMetadataKey] = item.ID, item.Principal
			}

			inbound.Workflow = inboundWorkflow(inbound)

			request := bridgeRequest{inbound: inbound}

			fresh, err := r.admitWeb(ctx, bridge, &request, protocol.GoalRequest{})
			if err != nil || !fresh || item.Kind == protocol.InboundKindHeld {
				return errors.Join(err, request.closeWorkflow())
			}

			request.queueItemID = item.ID

			err = bridge.enqueue(ctx, &request, "enqueue Web prompt")
			if err != nil {
				err = errors.Join(err, request.closeWorkflow())
			}

			return err
		}
	}

	return r.threads.stashQueueItem(ctx, conversationID, item)
}

// MoveToBackground moves every running execute and task call of the conversation's allowed agents
// into the background and reports whether any moved. Its running turn learns at its next step
// which of its own calls moved, and a moved script's pending question is withdrawn.
func (r *Runtime) MoveToBackground(ctx context.Context, conversationID string) (bool, error) {
	if err := rejectShadowMutation(ctx, r.Sessions.db, conversationID); err != nil {
		return false, err
	}

	bridge, err := r.threads.recordedBridge(ctx, conversationID)
	if err != nil {
		return false, err
	}

	// Holding b.mu until the note is queued makes the step after the moved calls drain it.
	bridge.mu.Lock()
	defer bridge.mu.Unlock()

	moved := r.background.move(conversationID)

	var listed strings.Builder

	for _, run := range moved {
		fmt.Fprintf(&listed, "- %s: %s (job ID: %s)\n", run.row.kind.tool(), cmp.Or(run.row.label, run.row.jobID), run.row.jobID)
	}

	if listed.Len() > 0 {
		bridge.movedNotes = append(bridge.movedNotes, systemPromptInput(rand.Text(), fmt.Sprintf(movedNote, listed.String())))
	}

	return len(moved) > 0, nil
}

// StopBackgroundJob stops, for the user, a running Background Job listed at conversationID.
func (r *Runtime) StopBackgroundJob(ctx context.Context, conversationID, jobID string) (bool, error) {
	if err := rejectShadowMutation(ctx, r.Sessions.db, conversationID); err != nil {
		return false, err
	}

	return r.background.stop(ctx, conversationID, jobID, errStoppedByUser)
}

// BackgroundJobs lists the Background Jobs shown at conversationID and reports whether
// MoveToBackground has a running call to move there.
func (r *Runtime) BackgroundJobs(ctx context.Context, conversationID string) ([]protocol.BackgroundJob, bool, error) {
	jobs, err := r.Sessions.backgroundJobs(ctx, conversationID)
	if err != nil {
		return nil, false, err
	}

	return listedBackgroundJobs(jobs, conversationID), r.background.movable(conversationID), nil
}

// CompletionNotes returns, in finish order, the jobs whose Completion Notes a system input saved
// at history delivers, by the job IDs it stored, with each note's text.
func (r *Runtime) CompletionNotes(ctx context.Context, history string, jobIDs []string) ([]protocol.BackgroundJob, error) {
	jobs, err := r.Sessions.notedBackgroundJobs(ctx, history, jobIDs)
	if err != nil {
		return nil, err
	}

	notes := listedBackgroundJobs(jobs, history)
	for i := range notes {
		notes[i].Note = backgroundNote(&jobs[i])
	}

	return notes, nil
}

// listedBackgroundJobs shows jobs at conversationID, hiding those of the runs reporting there.
func listedBackgroundJobs(jobs []backgroundJob, conversationID string) []protocol.BackgroundJob {
	listed := make([]protocol.BackgroundJob, len(jobs))

	for i := range jobs {
		job := &jobs[i]

		listed[i] = protocol.BackgroundJob{ID: job.jobID, Kind: string(job.kind.tool()), State: string(job.status), Label: job.label, ToolCallID: job.callID, SubagentKey: job.subagentKey, Hidden: job.conversationID != conversationID}
		if job.status == backgroundStopped {
			listed[i].StoppedBy = strings.TrimPrefix(job.result, "stopped by ")
		}
	}

	return listed
}

// Drain commits the offered input before advancing any consumed state. A bound
// child instead reads its parent's stream and waits at the final-answer boundary.
func (b *Bridge) Drain(ctx context.Context, responseID string, phase rocketcode.TurnPhase, applied []string) ([]rocketcode.PromptInput, error) {
	if b.shadow {
		return b.drainShadowInputs(ctx, responseID, phase, applied)
	}

	// Slow persistence delays this bridge's admissions. Separating transactional
	// admission ownership is the upgrade path; a partial unlock loses late inputs.
	b.mu.Lock()
	if b.historyMutation {
		b.mu.Unlock()
		return nil, nil
	}

	tx, err := b.config.SessionService.beginStateTx(context.WithoutCancel(ctx), "consumed input")
	if err != nil {
		b.mu.Unlock()
		return nil, inputPersistenceError{cause: err}
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(context.WithoutCancel(ctx), tx, b.config.ConversationID); err != nil {
		b.mu.Unlock()
		return nil, inputPersistenceError{cause: err}
	}

	snapshot, _, err := loadTurnInputs(ctx, tx, b.config.ConversationID, b.activeTurnID)
	if err != nil {
		b.mu.Unlock()
		return nil, inputPersistenceError{cause: err}
	}

	if snapshot.Closed {
		b.inputOpen, b.notesOpen = false, false
		b.mu.Unlock()

		return slices.DeleteFunc(snapshot.Inputs, func(input rocketcode.PromptInput) bool { return slices.Contains(applied, input.ID) }), nil
	}

	inputs := slices.Clone(snapshot.Inputs)
	segment := shadowStepSegment(applied, snapshot.Resets)

	// Completion Notes are claimed under b.mu, so a note this last step misses finds notesOpen false
	// and wakes the conversation. A resumed turn is offered the notes it held once more.
	notes, err := claimBackgroundNotes(context.WithoutCancel(ctx), tx, b.config.ConversationID, b.activeTurnID, true)
	if err != nil {
		b.mu.Unlock()
		return nil, inputPersistenceError{cause: err}
	}

	for i := range notes {
		if !slices.Contains(b.notesOffered, notes[i].jobID) {
			inputs = append(inputs, systemPromptInput(notes[i].jobID, backgroundNote(&notes[i])))
		}
	}

	inputs = append(inputs, b.movedNotes...)

	pending := slices.Clone(b.steers[b.steersRead:])

	for i := range pending {
		request := &pending[i]
		prompt := buildPrompt(request.inbound, nil, true)
		header, _, _ := strings.Cut(prompt, "\n\n")
		directSkill := inboundDirectSkill(request.inbound)
		inputs = append(inputs, rocketcode.PromptInput{ID: request.queueItemID, Texts: []string{prompt}, Header: header, Attachments: attachmentsFromInbound(request.inbound.Attachments), DirectSkill: directSkill, Agent: request.inbound.Metadata[inboundAgentMetadataKey]})

		if request.inbound.Human && !slices.Contains(snapshot.Resets, request.queueItemID) {
			snapshot.Resets = append(snapshot.Resets, request.queueItemID)
		}

		b.log.Info("steer injected", "event", "steer_injected", "conversation_id", b.config.ConversationID, "queue_item_id", request.queueItemID, "phase", phase)
	}
	// A restart reoffers accepted steers and held notes, while the canonical
	// stream already owns their earlier payload and ordering.
	snapshot.Inputs = []rocketcode.PromptInput{}

	var offered []rocketcode.PromptInput

	for i := range inputs {
		input := &inputs[i]
		if !slices.ContainsFunc(snapshot.Inputs, func(saved rocketcode.PromptInput) bool { return saved.ID == input.ID }) {
			snapshot.Inputs = append(snapshot.Inputs, *input)
			if !slices.Contains(applied, input.ID) {
				offered = append(offered, *input)
			}
		}
	}

	if len(offered) == 0 && phase == rocketcode.TurnPhaseFinalAnswer {
		snapshot.Closed = true
	}

	// The response this drain follows is one step of budget for this turn's shadows, recorded
	// before the turn's next response and once even when a restart drains it again.
	if _, err := tx.ExecContext(ctx, `INSERT INTO shadow_steps (shadow_turn_id, child, response_id, segment)
SELECT id, FALSE, $3, $4 FROM shadow_turns WHERE parent_source_conversation_id = $1 AND parent_turn_id = $2 ON CONFLICT DO NOTHING`, b.config.ConversationID, b.activeTurnID, responseID, segment); err != nil {
		b.mu.Unlock()
		return nil, inputPersistenceError{cause: fmt.Errorf("record parent step: %w", err)}
	}

	if err := saveTurnInputs(ctx, tx, b.config.ConversationID, b.activeTurnID, &snapshot); err != nil {
		b.mu.Unlock()
		return nil, inputPersistenceError{cause: err}
	}

	if err := tx.Commit(); err != nil {
		b.mu.Unlock()
		return nil, inputPersistenceError{cause: fmt.Errorf("commit consumed input: %w", err)}
	}

	turn := b.activeTurnID

	b.steersRead, b.movedNotes = len(b.steers), nil
	for i := range notes {
		if !slices.Contains(b.notesOffered, notes[i].jobID) {
			b.notesOffered = append(b.notesOffered, notes[i].jobID)
		}
	}

	if snapshot.Closed {
		b.inputOpen, b.notesOpen = false, false
	}
	b.mu.Unlock()

	for i := range pending {
		b.publishConsumed(ctx, pending[i].inbound, inputs[len(inputs)-len(pending)+i].Header)
	}

	if err := b.notifyShadowInputs(ctx, turn); err != nil {
		return nil, inputPersistenceError{cause: err}
	}

	return offered, nil
}

// publishConsumed posts a web input's consume card once per turn, even across a restart.
func (b *Bridge) publishConsumed(ctx context.Context, inbound *protocol.InboundMessage, header string) {
	if b.shadow {
		return
	}

	if id := inbound.Metadata["web_message_id"]; id != "" {
		b.mu.Lock()
		key := b.activeTurnID + "/consumed/" + id
		b.mu.Unlock()

		if _, posted, err := b.config.SessionService.LoadTurnStep(ctx, b.config.ConversationID, key); err != nil || posted {
			if err != nil {
				b.log.Error("load consumed web input", "error", err)
			}

			return
		}

		message := protocol.NewOutboundMessage(b.config.ConversationID, "")

		message.ConsumedID, message.ConsumedText, message.ConsumedSource = id, inbound.Text, inbound.Source
		message.ConsumedRawText = inbound.Metadata[protocol.InboundRawTextMetadataKey]
		message.ConsumedHeader = header
		message.SourceConversationID = b.config.ConversationID
		b.mu.Lock()
		message.Agent, message.Model, message.ReasoningEffort = b.activeAttribution.Agent, b.activeAttribution.Model, b.activeAttribution.ReasoningEffort
		b.mu.Unlock()

		if err := b.bus.PublishOutbound(ctx, message); err != nil {
			b.log.Error("publish consumed web input", "error", err)
			return
		}

		if err := b.config.SessionService.SaveTurnStep(context.WithoutCancel(ctx), b.config.ConversationID, key, json.RawMessage("true")); err != nil {
			b.log.Error("record consumed web input", "error", err)
		}
	}
}
