package backend

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
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

	summary, err := loadSessionSummary(ctx, tx, conversation.ID)
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO managed_conversations (conversation_id, agent, created_by) VALUES ($1, $2, $3) ON CONFLICT (conversation_id) DO NOTHING`, conversation.ID, conversation.Agent, conversation.CreatedBy); err != nil {
		return fmt.Errorf("create conversation: %w", err)
	}

	if err := saveSessionSummary(ctx, tx, summary); err != nil {
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
		managed, err := r.recordedBridge(id)
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
	case <-completion.done:
		return completion.err
	}
}

func (b *Bridge) syncConversation(ctx context.Context, source *Bridge) error {
	store := b.config.SessionService

	entries, err := store.ObserveEntries(ctx, source.config.ConversationID)
	if err != nil {
		return err
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin conversation sync: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, b.config.ConversationID); err != nil {
		return err
	}

	summary, err := loadSessionSummary(ctx, tx, b.config.ConversationID)
	if err != nil {
		return err
	}

	changed := false

	schedules := map[string]protocol.ScheduledMessageState{}

	for i := range entries {
		observed := &entries[i]

		entry, err := externalMCPManagedEntry(&observed.Entry, nil)
		if err != nil {
			return err
		}

		data, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("encode synced entry: %w", err)
		}

		added, err := execRows(ctx, tx, "insert synced entry", "count synced entries", `INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp)
SELECT $1, ($2::jsonb || jsonb_build_object('sync_source_entry_id', $3::bigint))::text, $4
WHERE NOT EXISTS (SELECT 1 FROM session_entries WHERE conversation_id = $1 AND entry_json::jsonb->>'sync_source_entry_id' = $3::text)`, b.config.ConversationID, string(data), observed.ID, entry.Timestamp.UTC().Format(time.RFC3339Nano))
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

		switch entry.Type {
		case producerScheduleEntryType:
			var scheduled protocol.ScheduledMessageState
			if err := json.Unmarshal(entry.OutputTrace[0], &scheduled); err != nil {
				return fmt.Errorf("decode synced schedule: %w", err)
			}

			scheduled.ConversationID, scheduled.Agent = b.config.ConversationID, b.agentSnapshot()

			id := rand.Text()
			if err := (stateDAO{db: tx}).putScheduledMessage(ctx, id, &scheduled); err != nil {
				return err
			}

			schedules[id] = scheduled
		case producerResetEntryType:
			if err := (stateDAO{db: tx}).resetScheduledMessages(ctx, b.config.ConversationID); err != nil {
				return err
			}

			clear(schedules)
		}
	}

	if changed {
		if err := saveSessionSummary(ctx, tx, summary); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, `UPDATE managed_conversations SET settled = FALSE WHERE conversation_id = $1 AND settled = TRUE`, b.config.ConversationID); err != nil {
			return fmt.Errorf("reopen synced conversation: %w", err)
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
	rows, err := r.Sessions.db.QueryContext(ctx, `SELECT conversation_id, agent, created_by, settled FROM managed_conversations ORDER BY conversation_id`)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var conversation protocol.Conversation
		if err := rows.Scan(&conversation.ID, &conversation.Agent, &conversation.CreatedBy, &conversation.Settled); err != nil {
			return nil, fmt.Errorf("read conversation: %w", err)
		}

		conversations = append(conversations, conversation)
	}

	if err := rows.Err(); err != nil {
		return conversations, fmt.Errorf("list conversation rows: %w", err)
	}

	return conversations, nil
}

func (r *Runtime) recordedBridge(conversationID string) (*Bridge, error) {
	thread, recorded, err := r.Sessions.Thread(conversationID)
	if err != nil {
		return nil, err
	}

	if !recorded {
		return nil, fmt.Errorf("conversation %q is not recorded", conversationID)
	}

	managed, _, err := r.threads.ensureThreadBridge(conversationID, thread, false)
	if err != nil {
		return nil, err
	}

	return managed.(*Bridge), nil
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

	bridge, err := r.recordedBridge(conversationID)
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
	if inbound.SyncDestination != "" {
		destination, err := r.recordedBridge(inbound.SyncDestination)
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
		return errBridgeStopped
	case <-completion.done:
		return completion.err
	}
}

// QueueItems returns persisted waiting work plus uninjected active steers.
func (r *Runtime) QueueItems(conversationID string) ([]protocol.ThreadQueueItem, error) {
	return r.threads.queueItems(conversationID)
}

// PopQueueItem releases held work at the end of the ordinary queue.
func (r *Runtime) PopQueueItem(ctx context.Context, conversationID, id string) (bool, error) {
	changed, err := execRows(ctx, r.Sessions.db, "pop queue item", "count popped queue items", `UPDATE thread_queue SET kind = $3, park_after = '', position = (SELECT COALESCE(MAX(position), -1) + 1 FROM thread_queue WHERE conversation_id = $1 AND park_after = '') WHERE conversation_id = $1 AND queue_item_id = $2 AND kind = $4`, conversationID, id, protocol.InboundKindEnqueue, protocol.InboundKindHeld)
	if err != nil || changed == 0 {
		return false, err
	}

	return true, r.threads.PickLaterWork(ctx, conversationID)
}

// PromoteQueueItem claims one persisted enqueue and submits it as a steer, keeping its principal.
func (r *Runtime) PromoteQueueItem(ctx context.Context, conversationID, id string) (bool, error) {
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
	return r.threads.stashQueueItem(ctx, conversationID, item)
}

func (b *Bridge) drainSteers(ctx context.Context, phase rocketcode.TurnPhase) []rocketcode.PromptInput {
	inputs := b.config.SteerDrain.Drain(ctx, phase)
	b.mu.Lock()

	pending := slices.Clone(b.steers[b.steersRead:])
	if len(pending) == 0 && len(inputs) == 0 && phase == rocketcode.TurnPhaseFinalAnswer {
		b.inputOpen = false
	}

	b.steersRead = len(b.steers)
	b.mu.Unlock()

	for _, request := range pending {
		b.publishConsumed(ctx, request.inbound)
		directSkill := inboundDirectSkill(request.inbound)
		inputs = append(inputs, rocketcode.PromptInput{Text: buildPrompt(request.inbound, nil), Attachments: attachmentsFromInbound(request.inbound.Attachments), DirectSkill: directSkill})
	}

	return inputs
}

func (b *Bridge) publishConsumed(ctx context.Context, inbound *protocol.InboundMessage) {
	if id := inbound.Metadata["web_message_id"]; id != "" {
		message := protocol.NewOutboundMessage(b.config.ConversationID, "")

		message.ConsumedID, message.ConsumedText = id, inbound.Text
		if err := b.bus.PublishOutbound(ctx, message); err != nil {
			b.log.Error("publish consumed web input", "error", err)
		}
	}
}
