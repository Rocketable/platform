package backend

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
	"github.com/Rocketable/platform/internal/rocketcode"
)

// CreateConversation records an explicit ID without changing existing selection.
func (r *Runtime) CreateConversation(ctx context.Context, conversation protocol.Conversation) error {
	tx, err := r.Sessions.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin conversation creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, conversation.ID); err != nil {
		return err
	}

	if err := (stateDAO{db: tx}).createConversation(ctx, conversation); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit conversation creation: %w", err)
	}

	return nil
}

// SyncConversation exposes surviving source work in an existing destination.
// The producer's own occupancy is not a competing destination turn.
func (r *Runtime) SyncConversation(ctx context.Context, source, destination string) error {
	bridges := make([]*Bridge, 0, 2)

	for _, id := range []string{source, destination} {
		managed, err := r.threads.recordedBridge(id)
		if err != nil {
			return err
		}

		bridges = append(bridges, managed)
	}

	r.Sessions.turnGatesMu.Lock()
	gate := r.Sessions.turnGates[destination]
	owned := gate != nil && gate.reservedFor == source
	r.Sessions.turnGatesMu.Unlock()

	if owned {
		return bridges[1].syncConversation(ctx, bridges[0])
	}

	completion := &turnCompletion{done: make(chan struct{})}
	if err := bridges[1].enqueue(ctx, &bridgeRequest{syncSource: source, producer: bridges[0], completion: completion}, "sync conversation"); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return fmt.Errorf("wait for conversation sync: %w", ctx.Err())
	case <-bridges[1].stopCh:
		return fmt.Errorf("wait for conversation sync: %w", protocol.ErrBridgeStopped)
	case <-completion.done:
		return completion.err
	}
}

func (b *Bridge) syncConversation(ctx context.Context, source *Bridge) error {
	store := b.config.SessionService

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin conversation sync: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	ids := []string{source.config.ConversationID, b.config.ConversationID}
	slices.Sort(ids)

	for _, id := range ids {
		if err := lockSessionHistory(ctx, tx, id); err != nil {
			return err
		}
	}

	dao := stateDAO{db: tx}

	entries, err := dao.observedEntries(ctx, source.config.ConversationID)
	if err != nil {
		return err
	}

	summary, err := loadSessionSummary(ctx, tx, b.config.ConversationID)
	if err != nil {
		return err
	}

	changed := false

	for i := range entries {
		observed := &entries[i]

		producer := observed.SourceConversationID
		if !observed.Synced {
			producer = source.config.ConversationID
		}

		entry, err := externalMCPManagedEntry(&observed.Entry, nil)
		if err != nil {
			return err
		}

		data, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("encode synced entry: %w", err)
		}

		data = removeSessionEntryNUL(data)

		added, err := execRows(ctx, tx, "insert synced entry", "count synced entries", `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp)
SELECT $1, ($2::jsonb || jsonb_build_object('sync_source_entry_id', $3::bigint, 'sync_source_conversation_id', $5::text))::json, $4
WHERE NOT EXISTS (SELECT 1 FROM session_entries WHERE conversation_id = $1 AND entry_json::jsonb->>'sync_source_entry_id' = $3::text)`, b.config.ConversationID, string(data), observed.ID, entry.Timestamp.UTC().Format(time.RFC3339Nano), producer)
		if err != nil {
			return err
		}

		if added == 0 {
			continue
		}

		changed = true

		if err := projectSessionSummary(&summary, &entry, entry.Timestamp.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}

	schedules, err := dao.projectProducerEffects(ctx, source.config.ConversationID, b.config.ConversationID, b.agentSnapshot())
	if err != nil {
		return err
	}

	if changed {
		if err := saveSessionSummary(ctx, tx, summary); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit conversation sync: %w", err)
	}

	for id, scheduled := range schedules {
		b.armScheduledMessage(id, &scheduled)
	}

	source.mu.Lock()
	output := source.pendingOutput
	source.mu.Unlock()

	if output != nil {
		message := protocol.CloneOutboundMessage(output)

		message.ConversationID = b.config.ConversationID
		if err := b.bus.PublishOutbound(ctx, message); err != nil {
			return fmt.Errorf("publish synced output: %w", err)
		}

		source.mu.Lock()
		source.pendingOutput = nil
		source.mu.Unlock()
	}

	store.completeTurnPairReservation(b.config.ConversationID, source.config.ConversationID)

	return nil
}

// ListConversations returns recorded conversations, never discovered pair IDs.
func (r *Runtime) ListConversations(ctx context.Context) (conversations []protocol.Conversation, err error) {
	rows, err := r.Sessions.db.QueryContext(ctx, `SELECT conversation_id, agent, created_by FROM managed_conversations ORDER BY conversation_id`)
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
	conversationID := inbound.ConversationID
	if inbound.Kind == protocol.InboundKindCancel {
		r.Sessions.turnGatesMu.Lock()
		if gate := r.Sessions.turnGates[conversationID]; gate != nil && gate.reservedFor != "" {
			conversationID = gate.reservedFor
		}
		r.Sessions.turnGatesMu.Unlock()
	}

	bridge, err := r.threads.recordedBridge(conversationID)
	if err != nil {
		return err
	}

	if inbound.Kind == protocol.InboundKindCancel {
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
		destination, err := r.threads.recordedBridge(inbound.SyncDestination)
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
	bridge, err := r.threads.recordedBridge(inbound.ConversationID)
	if err != nil {
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

	if strings.TrimSpace(goal.CheckScript) != "" {
		if err := ValidateGoalCheckScriptStart(r.Cfg, bridge.agentSnapshot(), goal.CheckScript); err != nil {
			return fmt.Errorf("validate goal check script: %w", err)
		}
	}

	if err := r.Sessions.BeginGoal(inbound.ConversationID, goal.Objective, goal.CheckScript, goal.MaxTurns, "", ""); err != nil {
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
	_, eligible, _, err := r.Sessions.RevertState(ctx, conversationID)
	if err != nil {
		return false, err
	}

	if eligible {
		return r.mutateWebQueue(ctx, conversationID, id, protocol.InboundKindEnqueue)
	}

	changed, err := execRows(ctx, r.Sessions.db, "pop queue item", "count popped queue items", `UPDATE thread_queue SET kind = $3, park_after = '', position = (SELECT COALESCE(MAX(position), -1) + 1 FROM thread_queue WHERE conversation_id = $1 AND park_after = '') WHERE conversation_id = $1 AND queue_item_id = $2 AND kind = $4`, conversationID, id, protocol.InboundKindEnqueue, protocol.InboundKindHeld)
	if err != nil || changed == 0 {
		return false, err
	}

	return true, r.threads.PickLaterWork(ctx, conversationID)
}

// PromoteQueueItem claims one persisted enqueue and submits it as a steer, keeping its principal.
func (r *Runtime) PromoteQueueItem(ctx context.Context, conversationID, id string) (bool, error) {
	_, eligible, _, err := r.Sessions.RevertState(ctx, conversationID)
	if err != nil {
		return false, err
	}

	if eligible {
		return r.mutateWebQueue(ctx, conversationID, id, protocol.InboundKindSteer)
	}

	return r.threads.promoteQueueItem(ctx, conversationID, id, "")
}

// DeleteQueueItem drops one waiting steer or enqueue so it never runs.
func (r *Runtime) DeleteQueueItem(ctx context.Context, conversationID, id string) (bool, error) {
	return r.threads.deleteQueueItem(ctx, conversationID, id)
}

// ReorderQueueItems writes persisted enqueue positions in the given ID order.
func (r *Runtime) ReorderQueueItems(conversationID string, ids []string) error {
	for i, id := range ids {
		if _, err := r.Sessions.db.ExecContext(context.Background(), `UPDATE thread_queue SET position=$1 WHERE conversation_id=$2 AND queue_item_id=$3`, i, conversationID, id); err != nil {
			return fmt.Errorf("reorder thread queue: %w", err)
		}
	}

	return nil
}

// StashQueueItem persists waiting work and offers it to the conversation bridge.
func (r *Runtime) StashQueueItem(ctx context.Context, conversationID string, item *protocol.ThreadQueueItem) error {
	if item.Source == protocol.SourceWeb {
		_, eligible, _, err := r.Sessions.RevertState(ctx, conversationID)
		if err != nil {
			return err
		}

		if eligible {
			bridge, err := r.threads.recordedBridge(conversationID)
			if err != nil {
				return err
			}

			content := item.Content
			content.Text = item.Message
			inbound := protocol.NewInboundMessageFromContent(item.Source, item.Kind, &content, true)
			inbound.ConversationID = conversationID
			inbound.Metadata["web_message_id"], inbound.Metadata[protocol.InboundPrincipalMetadataKey] = item.ID, item.Principal
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
func (r *Runtime) MoveToBackground(conversationID string) (bool, error) {
	bridge, err := r.threads.recordedBridge(conversationID)
	if err != nil {
		return false, err
	}

	// Holding b.mu until the note is queued makes the step after the moved calls drain it.
	bridge.mu.Lock()
	defer bridge.mu.Unlock()

	moved := r.background.move(conversationID)

	var listed strings.Builder

	for _, run := range moved {
		if run.row.childKey == "" { // A subagent's moved calls belong to its own turn.
			fmt.Fprintf(&listed, "- %s: %s (job ID: %s)\n", run.row.kind, cmp.Or(run.row.label, run.row.jobID), run.row.jobID)
		}
	}

	if listed.Len() > 0 {
		bridge.movedNotes = append(bridge.movedNotes, systemPromptInput("", fmt.Sprintf(movedNote, listed.String())))
	}

	return len(moved) > 0, nil
}

// StopBackgroundJob stops, for the user, a running Background Job listed at conversationID.
func (r *Runtime) StopBackgroundJob(ctx context.Context, conversationID, jobID string) (bool, error) {
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
// at history delivers, by the job IDs it stored, with each note's text. A Delegation History's
// notes are rows of its conversation, owned by its subagent.
func (r *Runtime) CompletionNotes(ctx context.Context, history string, jobIDs []string) ([]protocol.BackgroundJob, error) {
	// Ponytail: a Delegation History's conversation ID is not stored, so each ancestor ID is
	// tried in turn, one query per nesting level; store it with the history if this gets costly.
	for conversationID := history; ; {
		jobs, err := r.Sessions.notedBackgroundJobs(ctx, conversationID, jobIDs)
		if err != nil {
			return nil, err
		}

		jobs = slices.DeleteFunc(jobs, func(job backgroundJob) bool { return job.conversationID+job.childKey != history })
		if len(jobs) > 0 {
			notes := listedBackgroundJobs(jobs, conversationID)
			for i := range notes {
				notes[i].Note = backgroundNote(&jobs[i])
			}

			return notes, nil
		}

		index := strings.LastIndexByte(conversationID, '/')
		if index < 0 {
			return nil, nil
		}

		conversationID = conversationID[:index]
	}
}

// listedBackgroundJobs shows jobs at conversationID, hiding those of the runs reporting there.
func listedBackgroundJobs(jobs []backgroundJob, conversationID string) []protocol.BackgroundJob {
	listed := make([]protocol.BackgroundJob, len(jobs))

	for i := range jobs {
		job := &jobs[i]

		listed[i] = protocol.BackgroundJob{ID: job.jobID, Kind: string(job.kind), State: string(job.status), Label: job.label, ToolCallID: job.callID, SubagentKey: job.subagentKey, Hidden: job.conversationID != conversationID}
		if job.status == backgroundStopped {
			listed[i].StoppedBy = strings.TrimPrefix(job.result, "stopped by ")
		}
	}

	return listed
}

func (b *Bridge) drainSteers(ctx context.Context, phase rocketcode.TurnPhase) []rocketcode.PromptInput {
	b.mu.Lock()
	if b.historyMutation {
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()
	inputs := b.config.SteerDrain.Drain(ctx, phase)
	b.mu.Lock()
	if b.historyMutation {
		b.mu.Unlock()
		return inputs
	}

	// Completion Notes are claimed under b.mu, so a note this last step misses finds notesOpen false
	// and wakes the conversation. A resumed turn is offered the notes it held once more.
	notes, err := claimBackgroundNotes(context.WithoutCancel(ctx), b.config.SessionService.db, b.config.ConversationID, "", b.activeTurnID, true)
	if err != nil {
		b.log.Error("claim background notes", "conversation_id", b.config.ConversationID, "turn_id", b.activeTurnID, "error", err)
	}

	for i := range notes {
		if !slices.Contains(b.notesOffered, notes[i].jobID) {
			b.notesOffered = append(b.notesOffered, notes[i].jobID)
			inputs = append(inputs, systemPromptInput(notes[i].jobID, backgroundNote(&notes[i])))
		}
	}

	inputs = append(inputs, b.movedNotes...)
	b.movedNotes = nil

	pending := slices.Clone(b.steers[b.steersRead:])
	if len(pending) == 0 && len(inputs) == 0 && phase == rocketcode.TurnPhaseFinalAnswer {
		b.inputOpen, b.notesOpen = false, false
	}

	b.steersRead = len(b.steers)
	b.mu.Unlock()

	for i := range pending {
		request := &pending[i]
		prompt := buildPrompt(request.inbound, nil)
		header, _, _ := strings.Cut(prompt, "\n\n")
		b.publishConsumed(ctx, request.inbound, header)
		directSkill := inboundDirectSkill(request.inbound)
		inputs = append(inputs, rocketcode.PromptInput{ID: request.queueItemID, Text: prompt, Header: header, Attachments: attachmentsFromInbound(request.inbound.Attachments), DirectSkill: directSkill})
		b.log.Info("steer injected", "event", "steer_injected", "conversation_id", b.config.ConversationID, "queue_item_id", request.queueItemID, "phase", phase)
	}

	return inputs
}

// publishConsumed posts a web input's consume card once per turn, even across a restart.
func (b *Bridge) publishConsumed(ctx context.Context, inbound *protocol.InboundMessage, header string) {
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
