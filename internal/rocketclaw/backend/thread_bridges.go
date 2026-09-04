package backend

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
)

type directBridge interface {
	Start(ctx context.Context) error
	Stop() error
	Submit(ctx context.Context, msg *protocol.InboundMessage) error
	RecoverActiveTurn(ctx context.Context, turn *ActiveTurnState) error
	InterruptActiveTurn() *protocol.InboundMessage
	SwitchAgent(agent string)
	PickLaterWork(ctx context.Context) error
}

type threadStart struct {
	conversationID, agent   string
	requireCreated          bool
	existingErr, persistErr string
}

type threadBridgeManager struct {
	log     *slog.Logger
	runtime *config.Config
	store   *SessionService
	factory func(Config) directBridge

	mu      sync.Mutex
	bridges map[string]directBridge
}

var _ protocol.PrimaryTextRouter = (*threadBridgeManager)(nil)

func newThreadBridgeManager(runtime *config.Config, store *SessionService, logger *slog.Logger, factory func(Config) directBridge) *threadBridgeManager {
	return &threadBridgeManager{
		log: logger.With("component", "thread_bridges"), runtime: runtime, store: store, factory: factory,
		mu:      sync.Mutex{},
		bridges: map[string]directBridge{},
	}
}

func (m *threadBridgeManager) Stop() error {
	m.mu.Lock()
	conversationIDs := slices.Sorted(maps.Keys(m.bridges))

	bridges := make([]directBridge, 0, len(conversationIDs))
	for _, conversationID := range conversationIDs {
		bridges = append(bridges, m.bridges[conversationID])
	}
	m.mu.Unlock()

	var errStop error
	for _, bridge := range bridges {
		errStop = errors.Join(errStop, bridge.Stop())
	}

	return errStop
}

func (m *threadBridgeManager) StartPendingScheduledMessages(recovering map[string]bool) error {
	scheduledMessages, err := m.store.ScheduledMessages()
	if err != nil {
		return fmt.Errorf("load pending scheduled message bridges: %w", err)
	}

	for _, message := range scheduledMessages {
		conversationID := strings.TrimSpace(message.ConversationID)
		if recovering[conversationID] {
			continue
		}

		if _, _, err := m.ensureThreadBridge(conversationID, ThreadState{Agent: message.Agent}, false); err != nil {
			return fmt.Errorf("start pending scheduled message bridge: %w", err)
		}
	}

	return nil
}

func (m *threadBridgeManager) StartActiveGoals(recovering map[string]bool) error {
	threads, err := m.store.ActiveGoalThreads()
	if err != nil {
		return fmt.Errorf("load active goal bridges: %w", err)
	}

	goals, err := m.store.ActiveGoals()
	if err != nil {
		return fmt.Errorf("load active goals: %w", err)
	}

	for conversationID, thread := range threads {
		if recovering[conversationID] {
			continue
		}

		managed, _, err := m.ensureThreadBridge(conversationID, thread, false)
		if err != nil {
			return fmt.Errorf("start active goal bridge: %w", err)
		}

		inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "goal_continuation", "Continue the active goal loop.", false)
		inbound.ConversationID = conversationID
		inbound.SlackReply = &protocol.SlackReplyTarget{RecipientTeamID: goals[conversationID].SlackRecipientTeamID, RecipientUserID: goals[conversationID].SlackRecipientUserID}

		if err := managed.Submit(context.Background(), inbound); err != nil {
			return fmt.Errorf("submit active goal continuation: %w", err)
		}
	}

	return nil
}

func (m *threadBridgeManager) SubmitThreadReply(ctx context.Context, target protocol.TextConversationTarget, inbound *protocol.InboundMessage) (bool, error) {
	conversationID := protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID)
	if conversationID == "" {
		return false, nil
	}

	thread, ok, err := m.store.Thread(conversationID)
	if err != nil {
		return false, fmt.Errorf("load persisted Slack thread state: %w", err)
	}

	if !ok {
		return false, nil
	}

	if thread.CreatedBy == ThreadCreatedByCron {
		disableStartNewThread(inbound)
	}

	if inbound.SlackReply != nil {
		inbound.SlackReply.ThreadTS = strings.TrimSpace(target.ThreadID)
	}

	managed, _, err := m.ensureThreadBridge(conversationID, thread, false)
	if err != nil {
		return false, err
	}

	inbound.ConversationID = conversationID

	if err := managed.Submit(ctx, inbound); err != nil {
		return true, fmt.Errorf("submit Slack thread reply: %w", err)
	}

	return true, nil
}

func (m *threadBridgeManager) StashThreadQueueItem(ctx context.Context, target protocol.TextConversationTarget, item *protocol.ThreadQueueItem) error {
	return m.stashQueueItem(ctx, protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID), item)
}

func (m *threadBridgeManager) ThreadQueueItems(target protocol.TextConversationTarget) ([]protocol.ThreadQueueItem, error) {
	return m.queueItems(protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID))
}

func (m *threadBridgeManager) DeleteThreadQueueItem(ctx context.Context, target protocol.TextConversationTarget, id string) (bool, error) {
	return m.deleteQueueItem(ctx, protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID), id)
}

func (m *threadBridgeManager) PromoteThreadQueueItem(ctx context.Context, target protocol.TextConversationTarget, id string) (bool, error) {
	return m.promoteQueueItem(ctx, protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID), id, strings.TrimSpace(target.ThreadID))
}

func (m *threadBridgeManager) ScheduledMessages(target protocol.TextConversationTarget) (map[string]protocol.ScheduledMessageState, error) {
	messages, err := m.store.ScheduledMessagesForConversation(protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID))
	if err != nil {
		return nil, fmt.Errorf("list scheduled messages: %w", err)
	}

	return messages, nil
}

func (m *threadBridgeManager) SwitchThreadAgent(target protocol.TextConversationTarget, agent string) (bool, error) {
	conversationID := protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID)
	if conversationID == "" {
		return false, nil
	}

	return m.switchConversationAgent(conversationID, agent)
}

// SwitchConversationAgent persists selection and updates the existing live bridge.
// Frontends validate their current human/producer policy before calling it.
func (r *Runtime) SwitchConversationAgent(conversationID, agent string) (bool, error) {
	switched, err := r.threads.switchConversationAgent(conversationID, agent)
	if err != nil {
		return false, fmt.Errorf("switch conversation agent: %w", err)
	}

	return switched, nil
}

func (m *threadBridgeManager) ThreadAgent(target protocol.TextConversationTarget) (agent string, handled bool, err error) {
	conversationID := protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID)
	if conversationID == "" {
		return "", false, nil
	}

	thread, ok, err := m.store.Thread(conversationID)
	if err != nil {
		return "", false, fmt.Errorf("load persisted Slack thread state: %w", err)
	}

	if !ok {
		return "", false, nil
	}

	agent = strings.TrimSpace(thread.Agent)

	return agent, true, nil
}

func (m *threadBridgeManager) ReserveWorkflowTurn(target protocol.TextConversationTarget) (release func(), reserved bool, err error) {
	conversationID := protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID)

	release, reserved, err = m.store.ReserveWorkflowTurn(conversationID)
	if err != nil {
		err = fmt.Errorf("reserve workflow turn: %w", err)
	}

	return release, reserved, err
}

func (m *threadBridgeManager) StartThread(ctx context.Context, agent string, target protocol.TextConversationTarget, inbound *protocol.InboundMessage) error {
	conversationID := protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID)
	if conversationID == "" {
		return errors.New("slack thread target is required")
	}

	managed, err := m.ensureStartedThread(&threadStart{conversationID: conversationID, agent: agent, persistErr: "persist Slack thread bridge"})
	if err != nil {
		return err
	}

	inbound.ConversationID = conversationID

	return m.submitInbound(ctx, managed, inbound, "Slack thread start")
}

func (m *threadBridgeManager) StartNewThread(ctx context.Context, req *protocol.StartNewThreadRequest, createRoot func(context.Context, *protocol.StartNewThreadRequest) (protocol.StartNewThreadRootResult, error)) (protocol.StartNewThreadResult, error) {
	targetAgent := cmp.Or(strings.TrimSpace(req.Agent), strings.TrimSpace(req.CurrentAgent), "main")
	if len(req.AllowedAgents) > 0 && !slices.Contains(req.AllowedAgents, targetAgent) {
		return protocol.StartNewThreadResult{}, fmt.Errorf("agent %q is not allowed on this source surface", targetAgent)
	}

	agents, err := ExternalMCPAgentsIn(m.runtime, m.runtime.RuntimeDirName())
	if err != nil {
		return protocol.StartNewThreadResult{}, fmt.Errorf("load configured agents: %w", err)
	}

	if !slices.Contains(agents, targetAgent) {
		return protocol.StartNewThreadResult{}, fmt.Errorf("agent %q is not configured", targetAgent)
	}

	var (
		conversationID, url string
		rootTarget          protocol.TextConversationTarget
	)

	switch req.Source {
	case protocol.SourceSystem:
		if req.SlackReply == nil || strings.TrimSpace(req.SlackReply.ChannelID) == "" {
			return protocol.StartNewThreadResult{}, fmt.Errorf("rocketclaw_start_new_thread is not available for %s turns", req.Source)
		}

		fallthrough
	case protocol.SourceSlack:
		root, err := createRoot(ctx, req)
		if err != nil {
			return protocol.StartNewThreadResult{}, err
		}

		rootTarget = root.Target
		conversationID, url = protocol.SlackThreadConversationID(rootTarget.ChannelID, rootTarget.ThreadID), root.URL
	case protocol.SourceExternalMCP, protocol.SourceWeb:
		return protocol.StartNewThreadResult{}, fmt.Errorf("rocketclaw_start_new_thread is not available for %s turns", req.Source)
	}

	managed, err := m.ensureStartedThread(&threadStart{conversationID: conversationID, agent: targetAgent, requireCreated: true, existingErr: "Slack new thread conversation already exists", persistErr: "persist Slack new thread bridge"})
	if err != nil {
		return protocol.StartNewThreadResult{}, err
	}

	inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "rocketclaw_start_new_thread", req.Prompt, false)
	inbound.ConversationID = conversationID
	inbound.SlackReply = &protocol.SlackReplyTarget{ChannelID: strings.TrimSpace(rootTarget.ChannelID), MessageTS: strings.TrimSpace(rootTarget.MessageID), ThreadTS: strings.TrimSpace(rootTarget.ThreadID)}

	inbound.Metadata = map[string]string{protocol.InboundOriginMetadataKey: "System", protocol.InboundMediaMetadataKey: "Text"}
	if err := managed.Submit(ctx, inbound); err != nil {
		return protocol.StartNewThreadResult{}, fmt.Errorf("submit Slack new thread first prompt: %w", err)
	}

	return protocol.StartNewThreadResult{ConversationID: conversationID, URL: url}, nil
}

func (m *threadBridgeManager) StartGoalInThread(ctx context.Context, agent, objective, checkScript string, maxTurns int, target protocol.TextConversationTarget, inbound *protocol.InboundMessage) error {
	conversationID := protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID)
	if conversationID == "" {
		return errors.New("slack thread target is required")
	}

	thread, _, err := m.store.Thread(conversationID)
	if err != nil {
		return fmt.Errorf("load goal thread state: %w", err)
	}

	cronCreated := thread.CreatedBy == ThreadCreatedByCron

	if cronCreated {
		disableStartNewThread(inbound)
	}

	if storedAgent := strings.TrimSpace(thread.Agent); storedAgent != "" {
		agent = storedAgent
	}

	if strings.TrimSpace(checkScript) != "" {
		if err := ValidateGoalCheckScriptStart(m.runtime, agent, checkScript); err != nil {
			return fmt.Errorf("validate goal check script: %w", err)
		}
	}

	managed, err := m.ensureStartedThread(&threadStart{conversationID: conversationID, agent: agent, persistErr: "persist goal thread bridge"})
	if err != nil {
		return err
	}

	if err := m.store.BeginGoal(conversationID, objective, checkScript, maxTurns, inbound.SlackReply.RecipientTeamID, inbound.SlackReply.RecipientUserID); err != nil {
		return fmt.Errorf("persist goal: %w", err)
	}

	inbound.Label = "goal"
	inbound.ConversationID = conversationID

	return m.submitInbound(ctx, managed, inbound, "goal thread start")
}

func (m *threadBridgeManager) WorkflowDescriptions() ([]protocol.WorkflowDescription, error) {
	definitions, err := m.loadWorkflowDefinitions()
	if err != nil {
		return nil, err
	}

	return workflow.Descriptions(definitions), nil
}

func (m *threadBridgeManager) StartWorkflowInThread(ctx context.Context, agent, name, args string, target protocol.TextConversationTarget, inbound *protocol.InboundMessage) error {
	definitions, err := m.loadWorkflowDefinitions()
	if err != nil {
		return err
	}

	definition := definitions[name]
	if definition == nil {
		return fmt.Errorf("workflow %q is not configured", name)
	}

	conversationID := protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID)

	thread, _, err := m.store.Thread(conversationID)
	if err != nil {
		return fmt.Errorf("load workflow thread state: %w", err)
	}

	if storedAgent := strings.TrimSpace(thread.Agent); storedAgent != "" {
		agent = storedAgent
	}

	managed, err := m.ensureStartedThread(&threadStart{conversationID: conversationID, agent: agent, persistErr: "persist workflow thread bridge"})
	if err != nil {
		return err
	}

	inbound.Label, inbound.ConversationID = "workflow", conversationID
	inbound.Text = strings.TrimSpace("$workflow " + name + " " + args)

	inbound.Workflow = &protocol.WorkflowInvocation{Name: name, Args: args}

	return m.submitInbound(ctx, managed, inbound, "workflow thread start")
}

func (m *threadBridgeManager) InterruptThread(target protocol.TextConversationTarget) (*protocol.InboundMessage, error) {
	conversationID := protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID)
	if conversationID == "" {
		return nil, nil
	}

	if err := m.store.StopGoal(conversationID); err != nil {
		return nil, fmt.Errorf("stop goal thread: %w", err)
	}

	return m.InterruptConversation(conversationID), nil
}

func (m *threadBridgeManager) PickLaterWork(ctx context.Context, conversationID string) error {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil
	}

	thread, ok, err := m.store.Thread(conversationID)
	if err != nil {
		return fmt.Errorf("load later-work thread: %w", err)
	}

	if !ok {
		return nil
	}

	managed, _, err := m.ensureThreadBridge(conversationID, thread, false)
	if err != nil {
		return err
	}

	if err := managed.PickLaterWork(ctx); err != nil {
		return fmt.Errorf("pick later work: %w", err)
	}

	return nil
}

func (m *threadBridgeManager) InterruptConversation(conversationID string) *protocol.InboundMessage {
	m.store.turnGatesMu.Lock()
	if gate := m.store.turnGates[conversationID]; gate != nil && gate.reservedFor != "" {
		conversationID = gate.reservedFor
	}
	m.store.turnGatesMu.Unlock()

	m.mu.Lock()
	managed := m.bridges[conversationID]
	m.mu.Unlock()

	if managed == nil {
		return nil
	}

	return managed.InterruptActiveTurn()
}

func (m *threadBridgeManager) ThreadBusy(target protocol.TextConversationTarget) bool {
	return m.store.PairBusyFor(protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID))
}

func (m *threadBridgeManager) RegisterThread(target protocol.TextConversationTarget, agent string) (bool, error) {
	conversationID := protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID)
	if conversationID == "" {
		return false, errors.New("text thread target is required")
	}

	if _, ok, err := m.store.Thread(conversationID); err != nil {
		return false, fmt.Errorf("load text thread bridge: %w", err)
	} else if ok {
		return false, nil
	}

	_, err := m.ensureStartedThread(&threadStart{conversationID: conversationID, agent: agent, persistErr: "persist text thread bridge"})

	return err == nil, err
}

func (m *threadBridgeManager) RecoverActiveTurn(ctx context.Context, turn *ActiveTurnState) error {
	checkpoint := turn.Checkpoint

	conversationID := strings.TrimSpace(checkpoint.ConversationKey)

	managed, _, err := m.ensureThreadBridge(conversationID, ThreadState{Agent: checkpoint.Agent}, true)
	if err != nil {
		return err
	}

	if err := managed.RecoverActiveTurn(ctx, turn); err != nil {
		return fmt.Errorf("submit recovered active turn: %w", err)
	}

	return nil
}

func (m *threadBridgeManager) queueItems(conversationID string) ([]protocol.ThreadQueueItem, error) {
	items, err := m.store.ThreadQueueForConversation(conversationID)
	if err != nil {
		return nil, fmt.Errorf("list thread queue: %w", err)
	}

	m.mu.Lock()
	managed := m.bridges[conversationID]
	m.mu.Unlock()

	if managed != nil {
		bridge := managed.(*Bridge)
		bridge.mu.Lock()
		for _, request := range bridge.steers[bridge.steersRead:] {
			inbound := request.inbound

			item := protocol.ThreadQueueItem{ID: request.queueItemID, ConversationID: conversationID, Kind: protocol.InboundKindSteer, Message: inbound.Text, Principal: inbound.Metadata[protocol.InboundPrincipalMetadataKey]}
			if inbound.SlackReply != nil {
				item.SlackChannel, item.SlackTS = inbound.SlackReply.ChannelID, inbound.SlackReply.MessageTS
			}

			items = append(items, item)
		}
		bridge.mu.Unlock()
	}

	return items, nil
}

func (m *threadBridgeManager) promoteQueueItem(ctx context.Context, conversationID, id, threadTS string) (bool, error) {
	thread, recorded, err := m.store.Thread(conversationID)
	if err != nil || !recorded {
		return false, err
	}

	managed, _, err := m.ensureThreadBridge(conversationID, thread, false)
	if err != nil {
		return false, err
	}

	item, claimed, err := (stateDAO{db: m.store.db}).claimThreadQueueItem(ctx, conversationID, id)
	if err != nil || !claimed {
		return false, err
	}

	inbound := m.store.TakeMCPWaiter(id)
	if inbound == nil {
		content := item.Content
		content.Text = item.Message
		inbound = protocol.NewInboundMessageFromContent(item.Source, cmp.Or(item.Kind, protocol.InboundKindEnqueue), item.Principal, &content, true)
		inbound.Metadata = map[string]string{protocol.InboundPrincipalMetadataKey: item.Principal}

		inbound.SlackReply = &protocol.SlackReplyTarget{ChannelID: item.SlackChannel, MessageTS: item.SlackTS, ThreadTS: cmp.Or(threadTS, item.SlackTS)}
		if item.SlackReply != nil {
			reply := *item.SlackReply
			inbound.SlackReply = &reply
		}
	}

	kind, human := inbound.Kind, inbound.Human

	inbound.Kind, inbound.Human = protocol.InboundKindSteer, true
	if err := managed.Submit(ctx, inbound); err != nil {
		inbound.Kind, inbound.Human = kind, human
		m.store.PutMCPWaiter(id, inbound)

		return false, errors.Join(err, m.store.PutThreadQueueItem(id, &item))
	}

	return true, nil
}

func (m *threadBridgeManager) deleteQueueItem(ctx context.Context, conversationID, id string) (bool, error) {
	m.mu.Lock()
	managed := m.bridges[conversationID]
	m.mu.Unlock()

	if managed != nil {
		bridge := managed.(*Bridge)
		bridge.mu.Lock()
		for i := bridge.steersRead; i < len(bridge.steers); i++ {
			request := bridge.steers[i]
			if request.queueItemID == id {
				bridge.steers = slices.Delete(bridge.steers, i, i+1)
				request.completion.err = context.Canceled
				close(request.completion.done)
				bridge.mu.Unlock()
				request.inbound.CompleteResponseWithAttachments("", nil, context.Canceled)

				return true, nil
			}
		}
		bridge.mu.Unlock()
	}

	_, removed, err := (stateDAO{db: m.store.db}).claimThreadQueueItem(ctx, conversationID, id)
	if err != nil || !removed {
		return false, err
	}

	if waiter := m.store.TakeMCPWaiter(id); waiter != nil {
		waiter.CompleteResponseWithAttachments("", nil, errors.New("queue row removed"))
	}

	return true, m.PickLaterWork(ctx, conversationID)
}

func (m *threadBridgeManager) reorderQueueItems(conversationID string, ids []string) error {
	items, err := m.store.ThreadQueueForConversation(conversationID)
	if err != nil {
		return err
	}

	byID := make(map[string]*protocol.ThreadQueueItem, len(items))
	for i := range items {
		byID[items[i].ID] = &items[i]
	}

	for i, id := range ids {
		item := byID[id]
		if item == nil {
			continue
		}

		item.Position = i
		if err := m.store.PutThreadQueueItem(id, item); err != nil {
			return err
		}
	}

	return nil
}

func (m *threadBridgeManager) stashQueueItem(ctx context.Context, conversationID string, item *protocol.ThreadQueueItem) error {
	item.ConversationID = conversationID

	existing, err := m.store.ThreadQueueForConversation(conversationID)
	if err != nil {
		return fmt.Errorf("list thread queue: %w", err)
	}

	empty := 0

	for i := range existing {
		if strings.TrimSpace(existing[i].ParkAfter) == "" {
			empty++
		}
	}

	item.Position = empty
	item.ParkAfter = ""

	if err := m.store.PutThreadQueueItem(item.ID, item); err != nil {
		return fmt.Errorf("stash thread queue item: %w", err)
	}

	thread, recorded, err := m.store.Thread(conversationID)
	if err != nil || !recorded {
		return err
	}

	managed, _, err := m.ensureThreadBridge(conversationID, thread, false)
	if err != nil {
		return err
	}

	return managed.(*Bridge).submitEnqueuedItem(ctx, item)
}

func (m *threadBridgeManager) submitInbound(ctx context.Context, managed directBridge, inbound *protocol.InboundMessage, wrap string) error {
	if err := managed.Submit(ctx, inbound); err != nil {
		return fmt.Errorf("submit %s: %w", wrap, err)
	}

	return nil
}

func (m *threadBridgeManager) loadWorkflowDefinitions() (definitions map[string]*workflow.Definition, err error) {
	root, err := os.OpenRoot(m.runtime.Workspace)
	if err != nil {
		return nil, fmt.Errorf("open workflow root: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()

	definitions, err = workflow.Load(root, m.runtime.RuntimeDirName())
	if err != nil {
		return nil, fmt.Errorf("load workflow definitions: %w", err)
	}

	return definitions, nil
}

func (m *threadBridgeManager) ensureStartedThread(start *threadStart) (directBridge, error) {
	thread, recorded, err := m.store.Thread(start.conversationID)
	if err != nil {
		return nil, err
	}

	if start.requireCreated && recorded {
		return nil, errors.New(start.existingErr)
	}

	if !recorded {
		thread = ThreadState{Agent: start.agent}
	}

	managed, created, err := m.ensureThreadBridge(start.conversationID, thread, false)
	if err != nil {
		return nil, err
	}

	if start.requireCreated && !created {
		return nil, errors.New(start.existingErr)
	}

	if !recorded {
		if err := m.store.UpsertThread(start.conversationID, thread); err != nil {
			m.mu.Lock()
			delete(m.bridges, start.conversationID)
			m.mu.Unlock()

			_ = managed.Stop()

			return nil, fmt.Errorf("%s: %w", start.persistErr, err)
		}
	}

	return managed, nil
}

func disableStartNewThread(inbound *protocol.InboundMessage) {
	if inbound.Metadata == nil {
		inbound.Metadata = map[string]string{}
	}

	inbound.Metadata[protocol.InboundStartNewThreadDisabledMetadataKey] = "true"
}

func (m *threadBridgeManager) switchConversationAgent(conversationID, agent string) (bool, error) {
	ok, err := m.store.SetThreadAgentIfExists(conversationID, agent)
	if err != nil {
		return false, fmt.Errorf("persist Slack thread agent switch: %w", err)
	}

	if !ok {
		return false, nil
	}

	m.mu.Lock()
	managed := m.bridges[conversationID]
	m.mu.Unlock()

	if managed != nil {
		managed.SwitchAgent(agent)
	}

	return true, nil
}

func (m *threadBridgeManager) ensureThreadBridge(conversationID string, thread ThreadState, recoveringActiveTurn bool) (directBridge, bool, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, false, errors.New("text thread conversation ID is required")
	}

	m.mu.Lock()
	existing := m.bridges[conversationID]
	m.mu.Unlock()

	if existing != nil {
		return existing, false, nil
	}

	bridgeCfg := Config{
		ConversationID:       conversationID,
		Agent:                strings.TrimSpace(thread.Agent),
		RecoveringActiveTurn: recoveringActiveTurn,
		UserQuestionAsker:    protocol.NoUserQuestionAsker(),
	}
	if recoveringActiveTurn {
		recorded, ok, err := m.store.Thread(conversationID)
		if err != nil {
			return nil, false, fmt.Errorf("read recovered conversation selection: %w", err)
		}

		if ok && recorded.Agent != thread.Agent {
			bridgeCfg.AgentAfterRecovery = recorded.Agent
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if managed := m.bridges[conversationID]; managed != nil {
		return managed, false, nil
	}

	if bridgeCfg.Agent == "" {
		return nil, false, errors.New("text thread agent is required")
	}

	managed := m.factory(bridgeCfg)
	if err := managed.Start(context.Background()); err != nil {
		return nil, false, fmt.Errorf("start text thread bridge: %w", err)
	}

	m.bridges[conversationID] = managed

	return managed, true, nil
}
