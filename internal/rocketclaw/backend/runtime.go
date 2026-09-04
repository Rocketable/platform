package backend

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	harness "github.com/Rocketable/platform/internal/rocketcode"
)

// Runtime is the backend after construction, before frontends.
type Runtime struct {
	Cfg            *config.Config
	ConfigPath     string
	Log            *slog.Logger
	RunCtx         context.Context
	Channels       protocol.Channels
	Sessions       *SessionService
	OverlayMu      *sync.Mutex
	Reload         func(context.Context, string) (string, error)
	Restart        func(context.Context, string) (string, error)
	RecoveredTurns []ActiveTurnState
	CannotResume   []cannotResumeItem

	threads *threadBridgeManager

	mu   sync.Mutex
	live []chan protocol.ConversationEvent
}

type livePublisher struct {
	ch chan<- protocol.Broadcast
	rt *Runtime
}

func (p *livePublisher) PublishOutbound(ctx context.Context, message *protocol.OutboundMessage) error {
	if p.rt != nil {
		p.rt.HandleBroadcast(ctx, &protocol.Broadcast{Message: message, Delivery: message})
	}

	if err := protocol.BroadcastPublisher(p.ch).PublishOutbound(ctx, message); err != nil {
		return fmt.Errorf("publish live outbound: %w", err)
	}

	return nil
}

type unknownConversationError struct {
	id string
}

func (e unknownConversationError) Error() string {
	return protocol.ErrUnknownConversation.Error() + " " + e.id
}

func (e unknownConversationError) Unwrap() error {
	return protocol.ErrUnknownConversation
}

// RuntimeFor builds a Runtime for tests.
func RuntimeFor() *Runtime {
	manager := newThreadBridgeManager(new(config.Config), nil, slog.New(slog.DiscardHandler), func(Config) directBridge {
		return nil
	})

	channels := protocol.NewChannels()
	go discardBroadcasts(channels.Broadcasts)

	return &Runtime{Cfg: new(config.Config), threads: manager, Channels: channels}
}

// Subscribe returns a process-wide live event stream. Missed events are not replayed.
func (r *Runtime) Subscribe(context.Context) <-chan protocol.ConversationEvent {
	ch := make(chan protocol.ConversationEvent, 16)

	r.mu.Lock()
	r.live = append(r.live, ch)
	r.mu.Unlock()

	return ch
}

// CreateConversation records id, agents, and interpret-only tags.
func (r *Runtime) CreateConversation(id string, agents []string, tags []protocol.ConversationTag) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("conversation id is required")
	}

	if len(agents) == 0 || strings.TrimSpace(agents[0]) == "" {
		return errors.New("agents are required")
	}

	thread := ThreadState{Agent: strings.TrimSpace(agents[0])}
	if slices.Contains(tags, protocol.ConversationCron) {
		thread.CreatedBy = ThreadCreatedByCron
	} else if slices.Contains(tags, protocol.ConversationUserFacing) {
		thread.CreatedBy = ThreadCreatedByUser
	}

	return r.Sessions.UpsertThread(id, thread)
}

// ListConversations returns recorded conversations.
func (r *Runtime) ListConversations() ([]protocol.ConversationRecord, error) {
	ctx := context.Background()

	ids, err := r.Sessions.ManagedConversationIDs(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]protocol.ConversationRecord, 0, len(ids))
	for _, id := range ids {
		thread, ok, err := r.Sessions.Thread(id)
		if err != nil {
			return nil, err
		}

		if !ok {
			continue
		}

		record := protocol.ConversationRecord{ID: id, Agents: []string{thread.Agent}}
		switch thread.CreatedBy {
		case ThreadCreatedByCron:
			record.Tags = []protocol.ConversationTag{protocol.ConversationCron, protocol.ConversationUserFacing}
		case ThreadCreatedByUser:
			record.Tags = []protocol.ConversationTag{protocol.ConversationUserFacing}
		}

		out = append(out, record)
	}

	return out, nil
}

// ConversationAgent returns the stored agent for id.
func (r *Runtime) ConversationAgent(id string) (string, error) {
	thread, ok, err := r.Sessions.Thread(strings.TrimSpace(id))
	if err != nil {
		return "", err
	}

	if !ok {
		return "", unknownConversationError{id: id}
	}

	return thread.Agent, nil
}

// SwitchAgent persists the agent for id.
func (r *Runtime) SwitchAgent(id, agent string) error {
	ok, err := r.threads.switchConversationAgent(strings.TrimSpace(id), strings.TrimSpace(agent))
	if err != nil {
		return err
	}

	if !ok {
		return unknownConversationError{id: id}
	}

	return nil
}

// RunTurn blocks until that request is fully handled.
func (r *Runtime) RunTurn(ctx context.Context, req *protocol.TurnRequest) error {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return errors.New("conversation id is required")
	}

	if req.Kind == protocol.TurnCancel {
		r.threads.InterruptConversation(id)

		return nil
	}

	if _, ok, err := r.Sessions.Thread(id); err != nil {
		return err
	} else if !ok {
		return unknownConversationError{id: id}
	}

	if req.Kind == protocol.TurnSteer {
		if r.threads.conversationBusy(id) {
			r.PushSteer(id, req.Text)
		}

		return nil
	}

	if req.Kind == protocol.TurnEnqueue {
		inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "enqueued_message", req.Text, true)
		wait := inbound.EnableResponseWait()
		item := &protocol.ThreadQueueItem{ID: rand.Text(), ConversationID: id, Message: req.Text, StashAt: time.Now().UTC()}

		existing, errQueue := r.Sessions.ThreadQueueForConversation(id)
		if errQueue != nil {
			return errQueue
		}

		empty := 0

		for i := range existing {
			if strings.TrimSpace(existing[i].ParkAfter) == "" {
				empty++
			}
		}

		item.Position = empty
		if errPut := r.Sessions.PutThreadQueueItem(item.ID, item); errPut != nil {
			return errPut
		}

		r.Sessions.PutMCPWaiter(item.ID, inbound)

		if !r.threads.conversationBusy(id) {
			if errPick := r.threads.PickLaterWork(ctx, id); errPick != nil {
				return errPick
			}
		}

		return waitTurn(ctx, wait)
	}

	unlock, err := r.Sessions.lockTurnPair(ctx, id, id)
	if err != nil {
		return err
	}
	defer unlock()

	inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "", req.Text, true)
	switch req.Kind {
	case protocol.TurnGoal:
		inbound.Label = goalKickoffLabel
	case protocol.TurnWorkflow:
		inbound.Label = "workflow"
	case protocol.TurnPrompt, protocol.TurnSteer, protocol.TurnEnqueue, protocol.TurnCancel:
	}

	externalID, session, paired, errPair := r.Sessions.ExternalMCPSessionByConversationID(id)
	if errPair != nil {
		return errPair
	}
	if paired {
		inbound.Source = protocol.SourceExternalMCP
		inbound.Bridge = protocol.BridgeExternalMCP
		inbound.Metadata = map[string]string{"external_conversation_id": externalID}
		if channelID, threadTS, ok := protocol.SlackThreadTarget(session.ManagedConversationID); ok {
			inbound.SlackReply = &protocol.SlackReplyTarget{ChannelID: channelID, MessageTS: threadTS, ThreadTS: threadTS}
		}
	}

	wait := inbound.EnableResponseWait()
	if err := r.threads.submitConversation(ctx, id, inbound, req.UserFacingID); err != nil {
		return err
	}

	return waitTurn(ctx, wait)
}

// ListLaterWork returns queued later-work rows for a conversation.
func (r *Runtime) ListLaterWork(_ context.Context, id string) ([]protocol.ThreadQueueItem, error) {
	return r.Sessions.ThreadQueueForConversation(strings.TrimSpace(id))
}

// DeleteLaterWork drops one later-work row and picks the next head if idle.
func (r *Runtime) DeleteLaterWork(ctx context.Context, id, itemID string) error {
	id, itemID = strings.TrimSpace(id), strings.TrimSpace(itemID)

	items, err := r.Sessions.ThreadQueueForConversation(id)
	if err != nil {
		return err
	}

	for i := range items {
		if items[i].ID != itemID {
			continue
		}

		if waiter := r.Sessions.TakeMCPWaiter(itemID); waiter != nil {
			waiter.CompleteResponse("", errors.New("queue row removed"))
		}

		if errDelete := r.Sessions.DeleteThreadQueueItem(itemID); errDelete != nil {
			return errDelete
		}

		return r.threads.PickLaterWork(ctx, id)
	}

	return nil
}

// ReorderLaterWork writes later-work row order for a conversation.
func (r *Runtime) ReorderLaterWork(_ context.Context, id string, itemIDs []string) error {
	items, err := r.Sessions.ThreadQueueForConversation(strings.TrimSpace(id))
	if err != nil {
		return err
	}

	if len(itemIDs) != len(items) {
		return errors.New("reorder later work")
	}

	byID := make(map[string]protocol.ThreadQueueItem, len(items))
	for i := range items {
		byID[items[i].ID] = items[i]
	}

	for i, itemID := range itemIDs {
		item, ok := byID[itemID]
		if !ok {
			return errors.New("reorder later work")
		}

		item.Position = i
		if errPut := r.Sessions.PutThreadQueueItem(item.ID, &item); errPut != nil {
			return errPut
		}
	}

	return nil
}

// ConversationBusy reports whether a conversation has an in-flight turn.
func (r *Runtime) ConversationBusy(id string) bool {
	return r.threads.conversationBusy(strings.TrimSpace(id))
}

// ScheduledMessages returns persisted scheduled prompts for a conversation.
func (r *Runtime) ScheduledMessages(id string) (map[string]protocol.ScheduledMessageState, error) {
	messages, err := r.Sessions.ScheduledMessagesForConversation(strings.TrimSpace(id))
	if err != nil {
		return nil, fmt.Errorf("list scheduled messages: %w", err)
	}

	return messages, nil
}

// WorkflowDescriptions returns configured workflow names and summaries.
func (r *Runtime) WorkflowDescriptions() ([]protocol.WorkflowDescription, error) {
	descriptions, err := r.threads.WorkflowDescriptions()
	if err != nil {
		return nil, fmt.Errorf("list workflows: %w", err)
	}

	return descriptions, nil
}

// SyncConversation waits until dst is idle, then copies src session_entries into dst.
func (r *Runtime) SyncConversation(ctx context.Context, src, dst string) error {
	src, dst = strings.TrimSpace(src), strings.TrimSpace(dst)
	if src == "" || dst == "" {
		return errors.New("sync conversation ids are required")
	}

	if _, ok, err := r.Sessions.Thread(dst); err != nil {
		return err
	} else if !ok {
		return unknownConversationError{id: dst}
	}

	if _, ok, err := r.Sessions.Thread(src); err != nil {
		return err
	} else if !ok {
		return unknownConversationError{id: src}
	}

	unlock, err := r.Sessions.lockTurnPair(ctx, dst, dst)
	if err != nil {
		return err
	}
	defer unlock()

	srcEntries, err := r.Sessions.ObserveEntries(ctx, src, 0)
	if err != nil {
		return err
	}

	dstEntries, err := r.Sessions.ObserveEntries(ctx, dst, 0)
	if err != nil {
		return err
	}

	seen := make(map[string]struct{}, len(dstEntries))
	for i := range dstEntries {
		seen[sessionEntryKey(&dstEntries[i].Entry)] = struct{}{}
	}

	copied := ""

	for i := range srcEntries {
		entry := srcEntries[i].Entry
		if _, ok := seen[sessionEntryKey(&entry)]; ok {
			continue
		}

		if _, err := r.Sessions.AppendEntryID(ctx, dst, &entry); err != nil {
			return err
		}

		messages, errReplay := replayInputMessages(entry.ReplayInput)
		if errReplay != nil {
			return errReplay
		}

		for _, message := range messages {
			if message.role == "assistant" && strings.TrimSpace(message.text) != "" {
				copied = message.text
			}
		}
	}

	r.fanout(protocol.ConversationEvent{ConversationID: dst, Text: copied, Role: "assistant", Complete: true})

	return nil
}

func sessionEntryKey(entry *harness.SessionEntry) string {
	raw, err := json.Marshal(entry)
	if err != nil {
		return entry.Type + entry.Timestamp.UTC().Format(time.RFC3339Nano)
	}

	return string(raw)
}

func waitTurn(ctx context.Context, wait <-chan protocol.InboundResponse) error {
	select {
	case resp := <-wait:
		return resp.Err
	case <-ctx.Done():
		return fmt.Errorf("wait for turn: %w", ctx.Err())
	}
}

func discardBroadcasts(ch <-chan protocol.Broadcast) {
	for b := range ch {
		switch req := b.Interaction.(type) {
		case protocol.PostWebUserRequest:
			req.Done <- nil
		case protocol.ChannelNameRequest:
			req.Name <- ""
		case protocol.DrainSteersRequest:
			req.Steers <- nil
		case protocol.ActivateEnqueueRequest:
			req.Done <- nil
		case protocol.StartNewThreadResponse:
			req.Err <- errors.New("text root is not available")
		case protocol.AskUserQuestionResponse:
			req.Err <- errors.New("ask_user_question is not available")
		}
	}
}

func broadcastInteraction(ctx context.Context, broadcasts chan<- protocol.Broadcast, payload protocol.ResponsePayload) error {
	select {
	case broadcasts <- protocol.Broadcast{Interaction: payload}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("broadcast interaction: %w", ctx.Err())
	}
}

// PushSteer buffers web steer text for the next drain.
func (r *Runtime) PushSteer(conversationID, text string) {
	r.threads.pushSteer(conversationID, text)
}

// TakeSteers returns and clears buffered web steers for conversationID.
func (r *Runtime) TakeSteers(conversationID string) []string {
	return r.threads.drainSteers(context.Background(), conversationID)
}

// HandleBroadcast fans Subscribe events out to Frontends. Slack delivery stays on Slack.
func (r *Runtime) HandleBroadcast(_ context.Context, broadcast *protocol.Broadcast) protocol.BroadcastAcknowledgement {
	if broadcast.Relay != nil || broadcast.RelayCleanup != nil || broadcast.Message == nil {
		return protocol.BroadcastAcknowledgement{Status: protocol.BroadcastDropped}
	}

	text := strings.TrimSpace(broadcast.Message.Text)

	role := "assistant"
	if broadcast.Message.Originator {
		role = "user"
	} else if text == "" {
		text = strings.TrimSpace(broadcast.Message.ProgressText)
		role = "thinking"
	}

	if text != "" {
		r.fanout(protocol.ConversationEvent{Text: text, Role: role, Complete: broadcast.Message.Complete, ConversationID: broadcast.Message.ConversationID})
	}

	if !slices.Contains(broadcast.Message.Targets, protocol.OutputTargetWeb) {
		return protocol.BroadcastAcknowledgement{Status: protocol.BroadcastDropped}
	}

	if broadcast.Delivery != nil && broadcast.Message.Complete {
		broadcast.Delivery.MarkDelivered(nil)
	}

	return protocol.BroadcastAcknowledgement{Status: protocol.BroadcastHandled}
}

// SubmitInbound runs activation then submits one inbound to conversationID.
func (r *Runtime) SubmitInbound(ctx context.Context, agent, conversationID string, inbound *protocol.InboundMessage, activation protocol.ActivationHook) error {
	if err := activation(ctx, inbound); err != nil {
		return err
	}

	return r.threads.SubmitExternalMCP(ctx, agent, conversationID, inbound, NoopActivationHook)
}

func (r *Runtime) fanout(event protocol.ConversationEvent) {
	r.mu.Lock()
	live := slices.Clone(r.live)
	r.mu.Unlock()

	for _, ch := range live {
		select {
		case ch <- event:
		default:
		}
	}
}
