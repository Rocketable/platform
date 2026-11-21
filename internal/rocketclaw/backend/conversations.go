package backend

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func (r *Runtime) CreateConversation(ctx context.Context, conversation protocol.Conversation) error {
	_, err := r.Sessions.recordConversation(ctx, &conversation, conversationFacts{kind: KindWebChat}, harness.AgentStickyOff)
	return err
}

func (s *SessionService) CreateStickyConversation(ctx context.Context, conversation protocol.Conversation, sticky harness.AgentSticky) (id string, resumed bool, err error) {
	created, err := s.recordConversation(ctx, &conversation, conversationFacts{kind: KindWebChat}, sticky)

	return conversation.ID, !created, err
}

func (s *SessionService) recordConversation(ctx context.Context, conversation *protocol.Conversation, facts conversationFacts, sticky harness.AgentSticky) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin conversation creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	dao := stateDAO{db: tx}

	if sticky != harness.AgentStickyOff {
		id, err := dao.stickyConversationID(ctx, conversation.Agent, conversation.CreatedBy, sticky)
		if err != nil {
			return false, err
		}

		if id != "" {
			conversation.ID = id
			return false, nil
		}
	}

	if err := lockSessionHistory(ctx, tx, conversation.ID); err != nil {
		return false, err
	}

	created, err := dao.createConversation(ctx, *conversation, facts, sticky)
	if err != nil {
		return false, err
	}

	if !created && sticky != harness.AgentStickyOff {
		if conversation.ID, err = dao.stickyConversationID(ctx, conversation.Agent, conversation.CreatedBy, sticky); err != nil {
			return false, err
		}
	}

	if created {
		if _, err := s.reconcileShadows(ctx, tx, conversation.ID, conversation.Agent); err != nil {
			return false, err
		}
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit conversation creation: %w", err)
	}

	return created, nil
}

func (r *Runtime) ContinueRun(ctx context.Context, run, conversationID string, shared bool) (string, error) {
	tx, err := r.Sessions.beginStateTx(ctx, "run continuation")
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockHistories(ctx, tx, []string{run, conversationID}); err != nil {
		return "", err
	}

	if !shared {
		if _, err := execState(ctx, tx, "link continued run", `UPDATE conversations c SET continues = $2, cron_run = $2,
    preview = COALESCE(p.preview, c.preview), last_updated = COALESCE(p.last_updated, c.last_updated) FROM conversations p WHERE c.conversation_id = $1 AND c.continues IS NULL AND p.conversation_id = $2`, conversationID, run); err != nil {
			return "", err
		}

		if err := refreshPrincipals(ctx, tx, []string{conversationID}); err != nil {
			return "", err
		}
	}

	owner, schedules, err := (stateDAO{db: tx}).bindRun(ctx, run, conversationID)
	if err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit run continuation: %w", err)
	}

	return owner, r.threads.armSchedules(ctx, owner, schedules)
}

func (s *SessionService) SlackThread(ctx context.Context, conversationID string) (channelID, threadTS string, channelSession bool, err error) {
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(max(slack_channel), ''), COALESCE(max(slack_thread), ''), COALESCE(bool_or(kind = 'slack_thread' AND slack_thread IS NULL), FALSE)
FROM conversations WHERE conversation_id = $1`, conversationID).Scan(&channelID, &threadTS, &channelSession); err != nil {
		return "", "", false, fmt.Errorf("read Slack thread: %w", err)
	}

	return channelID, threadTS, channelSession, nil
}

func (s *SessionService) CronRun(ctx context.Context, conversationID string) (path string, run bool, err error) {
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(max(COALESCE(c.cron_path, r.cron_path)), ''), COALESCE(bool_or(c.kind IN ($2, $3)), FALSE)
FROM conversations c LEFT JOIN conversations r ON r.conversation_id = c.cron_run WHERE c.conversation_id = $1`, conversationID, KindCronRun, KindOneOffCronRun).Scan(&path, &run); err != nil {
		return "", false, fmt.Errorf("read cron run: %w", err)
	}

	return path, run, nil
}

func CronStem(path string) string {
	if agent, schedule, ok := strings.Cut(strings.TrimPrefix(path, "agents/"), ".md#"); ok {
		return agent + " · " + schedule
	}

	return strings.TrimSuffix(strings.TrimPrefix(path, "cron/"), ".md")
}

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
			fresh, err := r.admitWeb(ctx, bridge, inbound, protocol.GoalRequest{})
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
		return err
	}

	select {
	case <-bridge.stopCh:
		return protocol.ErrBridgeStopped
	case <-completion.done:
		return completion.err
	}
}

func (r *Runtime) StartGoal(ctx context.Context, inbound *protocol.InboundMessage, goal protocol.GoalRequest) error {
	if err := rejectShadowMutation(ctx, r.Sessions.db, inbound.ConversationID); err != nil {
		return err
	}

	bridge, err := r.threads.recordedBridge(ctx, inbound.ConversationID)
	if err != nil {
		return err
	}

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

			data, _ := json.Marshal(goal)

			inbound.Metadata["web_goal"] = string(data)

			fresh, err := r.admitWeb(ctx, bridge, inbound, goal)
			if err != nil || !fresh {
				return err
			}

			return bridge.enqueue(ctx, &bridgeRequest{inbound: inbound, queueItemID: inbound.Metadata["web_message_id"]}, "start Web goal")
		}
	}

	if err := r.Sessions.BeginGoal(inbound.ConversationID, &GoalState{Objective: goal.Objective, MaxTurns: goal.MaxTurns, PrincipalName: inbound.Metadata[protocol.InboundPrincipalNameMetadataKey], PrincipalEmail: inbound.Metadata[protocol.InboundPrincipalEmailMetadataKey], Origin: reviewOriginHeader(inbound)}); err != nil {
		return fmt.Errorf("persist goal: %w", err)
	}

	inbound.GoalAction = protocol.GoalActionKickoff

	return bridge.Submit(ctx, inbound)
}

func (r *Runtime) QueueItems(conversationID string) ([]protocol.ThreadQueueItem, error) {
	return r.threads.queueItems(conversationID)
}

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

	changed, err := execRows(ctx, r.Sessions.db, "pop queue item", popQueueItemSQL, conversationID, id, protocol.InboundKindEnqueue, protocol.InboundKindHeld, waitQueued)
	if err != nil || changed == 0 {
		return false, err
	}

	return true, r.threads.PickLaterWork(ctx, conversationID)
}

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

func (r *Runtime) EditQueueItem(ctx context.Context, conversationID, id, text, principal, username string) (bool, error) {
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

	item.Message, item.Content.Text, item.Source, item.Principal = text, text, protocol.SourceWeb, principal
	editor := protocol.InboundMessage{Source: protocol.SourceWeb, Human: true, Metadata: map[string]string{protocol.InboundPrincipalMetadataKey: principal, protocol.InboundPrincipalNameMetadataKey: principal, protocol.InboundPrincipalEmailMetadataKey: username}}

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
		copyProvenance(item.Inbound, &editor)
		data, _ := json.Marshal(item.Inbound)
		inbound = new(string(removeSessionEntryNUL(data)))
	}

	content, _ := json.Marshal(item.Content)
	if _, err := tx.ExecContext(ctx, `UPDATE turns SET message = $3, content = $4, inbound_json = $5, source = $6, principal = $7 WHERE conversation_id = $1 AND input_id = $2 AND phase = 'waiting'`, conversationID, id, text, content, inbound, item.Source, item.Principal); err != nil {
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

		request.inbound.Text = updatedText
		if request.inbound.Metadata == nil {
			request.inbound.Metadata = make(map[string]string)
		}

		request.inbound.Metadata[protocol.InboundRawTextMetadataKey] = text
		copyProvenance(request.inbound, &editor)
	}

	return true, nil
}

func (r *Runtime) DeleteQueueItem(ctx context.Context, conversationID, id string) (bool, error) {
	if err := rejectShadowMutation(ctx, r.threads.store.db, conversationID); err != nil {
		return false, err
	}

	return r.threads.deleteQueueItem(ctx, conversationID, id)
}

func (r *Runtime) ReorderQueueItems(ctx context.Context, conversationID string, ids []string) error {
	if err := rejectShadowMutation(ctx, r.Sessions.db, conversationID); err != nil {
		return err
	}

	tx, err := r.Sessions.beginStateTx(ctx, "thread queue reorder")
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, conversationID); err != nil {
		return err
	}

	if _, err := execState(ctx, tx, "reorder thread queue", `WITH queue AS (
    SELECT input_id, row_number() OVER (ORDER BY position, created_at_unix_ns, input_id COLLATE "C") AS slot
    FROM turns WHERE conversation_id = $1 AND phase = 'waiting' AND reason <> $3
), requested AS (
    SELECT input_id, min(n) AS n FROM unnest($2::text[]) WITH ORDINALITY AS r(input_id, n) GROUP BY input_id
), listed AS (
    SELECT q.input_id, q.slot, row_number() OVER (ORDER BY r.n) AS wanted, row_number() OVER (ORDER BY q.slot) AS held
    FROM queue q JOIN requested r USING (input_id)
), slots AS (
    SELECT q.input_id, COALESCE(h.slot, q.slot) AS slot FROM queue q LEFT JOIN listed w USING (input_id) LEFT JOIN listed h ON h.held = w.wanted
)
UPDATE turns t SET position = s.slot - 1 FROM slots s WHERE t.conversation_id = $1 AND t.phase = 'waiting' AND t.input_id = s.input_id`, conversationID, ids, waitScheduled); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit thread queue reorder: %w", err)
	}

	return nil
}

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

			fresh, err := r.admitWeb(ctx, bridge, inbound, protocol.GoalRequest{})
			if err != nil || !fresh || item.Kind == protocol.InboundKindHeld {
				return err
			}

			return bridge.enqueue(ctx, &bridgeRequest{inbound: inbound, queueItemID: item.ID}, "enqueue Web prompt")
		}
	}

	return r.threads.stashQueueItem(ctx, conversationID, item)
}

func (r *Runtime) MoveToBackground(ctx context.Context, conversationID string) (bool, error) {
	if err := rejectShadowMutation(ctx, r.Sessions.db, conversationID); err != nil {
		return false, err
	}

	bridge, err := r.threads.recordedBridge(ctx, conversationID)
	if err != nil {
		return false, err
	}

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

func (r *Runtime) StopBackgroundJob(ctx context.Context, conversationID, jobID string) (bool, error) {
	if err := rejectShadowMutation(ctx, r.Sessions.db, conversationID); err != nil {
		return false, err
	}

	return r.background.stop(ctx, conversationID, jobID, errStoppedByUser)
}

func (r *Runtime) BackgroundJobs(ctx context.Context, conversationID string) ([]protocol.BackgroundJob, bool, error) {
	jobs, err := r.Sessions.backgroundJobs(ctx, conversationID)
	if err != nil {
		return nil, false, err
	}

	return listedBackgroundJobs(jobs, conversationID), r.background.movable(conversationID), nil
}

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

func listedBackgroundJobs(jobs []backgroundJob, conversationID string) []protocol.BackgroundJob {
	listed := make([]protocol.BackgroundJob, len(jobs))

	for i := range jobs {
		job := &jobs[i]

		listed[i] = protocol.BackgroundJob{ID: job.jobID, Kind: string(job.kind.tool()), State: string(job.status), Label: job.label, ToolCallID: job.callID, SubagentKey: job.subagentKey, Hidden: job.conversationID != conversationID}
		if job.status == backgroundStopped && slices.Contains([]backgroundStopError{errStoppedByUser, errStoppedByAgent}, backgroundStopError(job.result)) {
			listed[i].StoppedBy = strings.TrimPrefix(job.result, "stopped by ")
		}
	}

	return listed
}

func (b *Bridge) Drain(ctx context.Context, responseID string, phase harness.TurnPhase, applied []string) ([]harness.PromptInput, error) {
	if b.shadow {
		return b.drainShadowInputs(ctx, responseID, phase, applied)
	}

	b.mu.Lock()
	turnID, origin := b.activeTurnID, b.activeReply
	b.mu.Unlock()

	thread, err := b.moveRatcheted(ctx, turnID, origin, true)
	if err != nil {
		return nil, err
	}

	if thread != "" {
		b.mu.Lock()
		b.activeTurnCancel()
		b.mu.Unlock()

		return nil, turnMovedError{thread: thread}
	}

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

		return slices.DeleteFunc(snapshot.Inputs, func(input harness.PromptInput) bool { return slices.Contains(applied, input.ID) }), nil
	}

	inputs := snapshot.Inputs
	segment := shadowStepSegment(applied, snapshot.Resets)

	notes, err := claimBackgroundNotes(context.WithoutCancel(ctx), tx, b.config.ConversationID, b.activeTurnID, true)
	if err != nil {
		b.mu.Unlock()
		return nil, inputPersistenceError{cause: err}
	}

	for i := range notes {
		if !slices.Contains(b.notesOffered, notes[i].jobID) {
			note, attachments := b.config.SessionService.backgroundNoteInput(&notes[i])
			input := systemPromptInput(notes[i].jobID, note)
			input.Attachments = attachmentsFromInbound(attachments)
			inputs = append(inputs, input)
		}
	}

	inputs = append(inputs, b.movedNotes...)

	pending := slices.Clone(b.steers[b.steersRead:])

	for i := range pending {
		request := &pending[i]
		prompt := buildPrompt(request.inbound, nil, true)
		header, _, _ := strings.Cut(prompt, "\n\n")
		directSkill := inboundDirectSkill(request.inbound)
		inputs = append(inputs, harness.PromptInput{ID: request.queueItemID, Texts: []string{prompt}, Header: header, Attachments: attachmentsFromInbound(request.inbound.Attachments), DirectSkill: directSkill, Agent: request.inbound.Metadata[inboundAgentMetadataKey]})

		if request.inbound.Human && !slices.Contains(snapshot.Resets, request.queueItemID) {
			snapshot.Resets = append(snapshot.Resets, request.queueItemID)
		}

		b.log.Info("steer injected", "event", "steer_injected", "conversation_id", b.config.ConversationID, "queue_item_id", request.queueItemID, "phase", phase)
	}

	snapshot.Inputs = []harness.PromptInput{}

	var offered []harness.PromptInput

	for i := range inputs {
		input := &inputs[i]
		if !slices.ContainsFunc(snapshot.Inputs, func(saved harness.PromptInput) bool { return saved.ID == input.ID }) {
			snapshot.Inputs = append(snapshot.Inputs, *input)
			if !slices.Contains(applied, input.ID) {
				offered = append(offered, *input)
			}
		}
	}

	if len(offered) == 0 && phase == harness.TurnPhaseFinalAnswer {
		snapshot.Closed = true
	}

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
