package app

import (
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
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
)

type directBridge interface {
	Start(ctx context.Context) error
	Stop() error
	Submit(ctx context.Context, msg *events.InboundMessage) error
	SubmitWhenActive(ctx context.Context, msg *events.InboundMessage, activation harnessbridge.ActivationHook) error
	RecoverActiveTurn(ctx context.Context, turn *harnessbridge.ActiveTurnState) error
	InterruptActiveTurn() *events.InboundMessage
	TurnPhase() harnessbridge.ThreadTurnPhase
	SwitchAgent(agent string)
	PickLaterWork(ctx context.Context) error
}

type managedThreadBridge struct {
	bridge directBridge
}

type threadStart struct {
	conversationID, agent   string
	outputTargets           []events.OutputTarget
	requireCreated          bool
	existingErr, persistErr string
	createdBy               harnessbridge.ThreadCreator
}

type primaryTextBinding struct {
	label         string
	outputTargets []events.OutputTarget
}

type threadBridgeManager struct {
	log     *slog.Logger
	runtime *config.Config
	store   *harnessbridge.SessionService
	factory func(harnessbridge.Config) directBridge
	text    primaryTextBinding

	mu      sync.Mutex
	bridges map[string]*managedThreadBridge
}

func newThreadBridgeManager(runtime *config.Config, store *harnessbridge.SessionService, logger *slog.Logger, factory func(harnessbridge.Config) directBridge) *threadBridgeManager {
	return &threadBridgeManager{log: logger.With("component", "thread_bridges"), runtime: runtime, store: store, factory: factory, text: primaryTextBinding{label: "Slack", outputTargets: []events.OutputTarget{events.OutputTargetSlack}}, mu: sync.Mutex{}, bridges: map[string]*managedThreadBridge{}}
}

func (b primaryTextBinding) conversationID(target events.TextConversationTarget) string {
	return harnessbridge.SlackThreadConversationID(strings.TrimSpace(target.ChannelID), strings.TrimSpace(target.ThreadID))
}

func (b primaryTextBinding) targetForConversationID(conversationID string) (events.TextConversationTarget, bool) {
	channelID, threadTS, ok := harnessbridge.SlackThreadTarget(conversationID)

	return events.TextConversationTarget{ChannelID: channelID, MessageID: threadTS, ThreadID: threadTS}, ok
}

func (b primaryTextBinding) setReplyThread(inbound *events.InboundMessage, target events.TextConversationTarget) {
	if inbound.SlackReply != nil {
		inbound.SlackReply.ThreadTS = strings.TrimSpace(target.ThreadID)
	}
}

func (b primaryTextBinding) setContinuationReply(inbound *events.InboundMessage, target events.TextConversationTarget) {
	inbound.SlackReply = &events.SlackReplyTarget{ChannelID: target.ChannelID, MessageTS: target.MessageID, ThreadTS: target.ThreadID}
}

func (m *threadBridgeManager) Stop() error {
	m.mu.Lock()
	conversationIDs := slices.Sorted(maps.Keys(m.bridges))

	bridges := make([]directBridge, 0, len(conversationIDs))
	for _, conversationID := range conversationIDs {
		bridges = append(bridges, m.bridges[conversationID].bridge)
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

		if _, _, err := m.ensureThreadBridge(conversationID, harnessbridge.ThreadState{Agent: message.Agent}, m.text.outputTargets, false); err != nil {
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

		target, ok := m.text.targetForConversationID(conversationID)
		if !ok {
			continue
		}

		managed, _, err := m.ensureThreadBridge(conversationID, thread, m.text.outputTargets, false)
		if err != nil {
			return fmt.Errorf("start active goal bridge: %w", err)
		}

		inbound := events.NewInboundMessage(events.SourceSystem, events.InboundKindPrompt, "goal_continuation", "Continue the active goal loop.", false)
		inbound.ConversationID = conversationID
		m.text.setContinuationReply(inbound, target)
		inbound.SlackReply.RecipientTeamID = goals[conversationID].SlackRecipientTeamID
		inbound.SlackReply.RecipientUserID = goals[conversationID].SlackRecipientUserID

		if err := managed.bridge.Submit(context.Background(), inbound); err != nil {
			return fmt.Errorf("submit active goal continuation: %w", err)
		}
	}

	return nil
}

func (m *threadBridgeManager) SubmitThreadReply(ctx context.Context, target events.TextConversationTarget, inbound *events.InboundMessage) (bool, error) {
	return m.SubmitWhenActive(ctx, target, inbound, harnessbridge.NoopActivationHook)
}

func (m *threadBridgeManager) SubmitWhenActive(ctx context.Context, target events.TextConversationTarget, inbound *events.InboundMessage, activation harnessbridge.ActivationHook) (bool, error) {
	conversationID := m.text.conversationID(target)
	if conversationID == "" {
		return false, nil
	}

	thread, ok, err := m.store.Thread(conversationID)
	if err != nil {
		return false, fmt.Errorf("load persisted %s thread state: %w", m.text.label, err)
	}

	if !ok {
		return false, nil
	}

	if thread.CreatedBy == harnessbridge.ThreadCreatedByCron {
		disableStartNewThread(inbound)
	}

	m.text.setReplyThread(inbound, target)

	managed, _, err := m.ensureThreadBridge(conversationID, thread, m.text.outputTargets, false)
	if err != nil {
		return false, err
	}

	inbound.ConversationID = conversationID

	if err := managed.bridge.SubmitWhenActive(ctx, inbound, activation); err != nil {
		return true, fmt.Errorf("submit %s thread reply: %w", m.text.label, err)
	}

	return true, nil
}

func (m *threadBridgeManager) StashThreadQueueItem(_ context.Context, target events.TextConversationTarget, item *harnessbridge.ThreadQueueItem) error {
	conversationID := m.text.conversationID(target)
	item.ConversationID = conversationID

	existing, err := m.store.ThreadQueueForConversation(conversationID)
	if err != nil {
		return fmt.Errorf("list thread queue: %w", err)
	}

	item.Position = len(existing)

	if err := m.store.PutThreadQueueItem(item.ID, item); err != nil {
		return fmt.Errorf("stash thread queue item: %w", err)
	}

	return nil
}

func (m *threadBridgeManager) ThreadQueueItems(_ context.Context, target events.TextConversationTarget) ([]harnessbridge.ThreadQueueItem, error) {
	items, err := m.store.ThreadQueueForConversation(m.text.conversationID(target))
	if err != nil {
		return nil, fmt.Errorf("list thread queue: %w", err)
	}

	return items, nil
}

func (m *threadBridgeManager) ReorderThreadQueue(_ context.Context, target events.TextConversationTarget, ids []string) error {
	if err := m.store.ReorderThreadQueue(m.text.conversationID(target), ids); err != nil {
		return fmt.Errorf("reorder thread queue: %w", err)
	}

	return nil
}

func (m *threadBridgeManager) DeleteThreadQueueItem(_ context.Context, target events.TextConversationTarget, id string) error {
	conversationID := m.text.conversationID(target)

	items, err := m.store.ThreadQueueForConversation(conversationID)
	if err != nil {
		return fmt.Errorf("list thread queue: %w", err)
	}

	for i := range items {
		if items[i].ID == id {
			if err := m.store.DeleteThreadQueueItem(id); err != nil {
				return fmt.Errorf("delete thread queue item: %w", err)
			}

			return nil
		}
	}

	return nil
}

func (m *threadBridgeManager) ScheduledMessages(_ context.Context, target events.TextConversationTarget) (map[string]harnessbridge.ScheduledMessageState, error) {
	messages, err := m.store.ScheduledMessagesForConversation(m.text.conversationID(target))
	if err != nil {
		return nil, fmt.Errorf("list scheduled messages: %w", err)
	}

	return messages, nil
}

func (m *threadBridgeManager) DeleteScheduledMessage(_ context.Context, target events.TextConversationTarget, id string) error {
	conversationID := m.text.conversationID(target)

	messages, err := m.store.ScheduledMessagesForConversation(conversationID)
	if err != nil {
		return fmt.Errorf("list scheduled messages: %w", err)
	}

	if _, ok := messages[id]; !ok {
		return nil
	}

	if err := m.store.DeleteScheduledMessage(id); err != nil {
		return fmt.Errorf("delete scheduled message: %w", err)
	}

	return nil
}

func (m *threadBridgeManager) ResetScheduledMessages(_ context.Context, target events.TextConversationTarget) error {
	if err := m.store.ResetScheduledMessages(m.text.conversationID(target)); err != nil {
		return fmt.Errorf("reset scheduled messages: %w", err)
	}

	return nil
}

func (m *threadBridgeManager) SwitchThreadAgent(target events.TextConversationTarget, agent string) (bool, error) {
	conversationID := m.text.conversationID(target)
	if conversationID == "" {
		return false, nil
	}

	ok, err := m.store.SetThreadAgentIfExists(conversationID, agent)
	if err != nil {
		return false, fmt.Errorf("persist %s thread agent switch: %w", m.text.label, err)
	}

	if !ok {
		return false, nil
	}

	m.mu.Lock()
	managed := m.bridges[conversationID]
	m.mu.Unlock()

	if managed != nil {
		managed.bridge.SwitchAgent(agent)
	}

	return true, nil
}

func (m *threadBridgeManager) ThreadAgent(target events.TextConversationTarget) (agent string, handled bool, err error) {
	conversationID := m.text.conversationID(target)
	if conversationID == "" {
		return "", false, nil
	}

	thread, ok, err := m.store.Thread(conversationID)
	if err != nil {
		return "", false, fmt.Errorf("load persisted %s thread state: %w", m.text.label, err)
	}

	if !ok {
		return "", false, nil
	}

	agent = strings.TrimSpace(thread.Agent)

	return agent, true, nil
}

func (m *threadBridgeManager) ReserveWorkflowTurn(target events.TextConversationTarget) (release func(), reserved bool, err error) {
	conversationID := m.text.conversationID(target)

	release, reserved, err = m.store.ReserveWorkflowTurn(conversationID)
	if err != nil {
		err = fmt.Errorf("reserve workflow turn: %w", err)
	}

	return release, reserved, err
}

func (m *threadBridgeManager) StartThread(ctx context.Context, agent string, target events.TextConversationTarget, inbound *events.InboundMessage) error {
	conversationID := m.text.conversationID(target)
	if conversationID == "" {
		return fmt.Errorf("%s thread target is required", strings.ToLower(m.text.label))
	}

	managed, err := m.ensureStartedThread(&threadStart{conversationID: conversationID, agent: agent, outputTargets: m.text.outputTargets, persistErr: "persist " + m.text.label + " thread bridge"})
	if err != nil {
		return err
	}

	inbound.ConversationID = conversationID
	if err := managed.bridge.Submit(ctx, inbound); err != nil {
		return fmt.Errorf("submit %s thread start: %w", m.text.label, err)
	}

	return nil
}

func (m *threadBridgeManager) StartNewThread(ctx context.Context, req *events.StartNewThreadRequest, createRoot func(context.Context, *events.StartNewThreadRequest) (events.StartNewThreadRootResult, error)) (events.StartNewThreadResult, error) {
	targetAgent := startNewThreadTargetAgent(req)
	if len(req.AllowedAgents) > 0 && !slices.Contains(req.AllowedAgents, targetAgent) {
		return events.StartNewThreadResult{}, fmt.Errorf("agent %q is not allowed on this source surface", targetAgent)
	}

	agents, err := harnessbridge.ExternalMCPAgentsIn(m.runtime, m.runtime.RuntimeDirName())
	if err != nil {
		return events.StartNewThreadResult{}, fmt.Errorf("load configured agents: %w", err)
	}

	if !slices.Contains(agents, targetAgent) {
		return events.StartNewThreadResult{}, fmt.Errorf("agent %q is not configured", targetAgent)
	}

	var (
		conversationID, url, label string
		outputTargets              []events.OutputTarget
		rootTarget                 events.TextConversationTarget
	)

	switch req.Source {
	case events.SourceSlack:
		root, err := createRoot(ctx, req)
		if err != nil {
			return events.StartNewThreadResult{}, err
		}

		rootTarget = root.Target
		conversationID, url = m.text.conversationID(rootTarget), root.URL
		outputTargets, label = m.text.outputTargets, m.text.label
	case events.SourceExternalMCP, events.SourceSystem:
		return events.StartNewThreadResult{}, fmt.Errorf("rocketclaw_start_new_thread is not available for %s turns", req.Source)
	}

	managed, err := m.ensureStartedThread(&threadStart{conversationID: conversationID, agent: targetAgent, outputTargets: outputTargets, requireCreated: true, existingErr: label + " new thread conversation already exists", persistErr: "persist " + label + " new thread bridge"})
	if err != nil {
		return events.StartNewThreadResult{}, err
	}

	inbound := events.NewInboundMessage(events.SourceSystem, events.InboundKindPrompt, "rocketclaw_start_new_thread", req.Prompt, false)
	inbound.ConversationID = conversationID
	inbound.Response = req.Response
	m.text.setContinuationReply(inbound, events.TextConversationTarget{ChannelID: strings.TrimSpace(rootTarget.ChannelID), MessageID: strings.TrimSpace(rootTarget.MessageID), ThreadID: strings.TrimSpace(rootTarget.ThreadID)})

	inbound.Metadata = map[string]string{events.InboundOriginMetadataKey: "System", events.InboundMediaMetadataKey: "Text"}
	if err := managed.bridge.Submit(ctx, inbound); err != nil {
		return events.StartNewThreadResult{}, fmt.Errorf("submit %s new thread first prompt: %w", label, err)
	}

	return events.StartNewThreadResult{ConversationID: conversationID, URL: url}, nil
}

func startNewThreadTargetAgent(req *events.StartNewThreadRequest) string {
	targetAgent := strings.TrimSpace(req.Agent)
	if targetAgent == "" {
		targetAgent = strings.TrimSpace(req.CurrentAgent)
	}

	if targetAgent == "" {
		return "main"
	}

	return targetAgent
}

func (m *threadBridgeManager) StartGoalInThread(ctx context.Context, agent, objective, checkScript string, maxTurns int, target events.TextConversationTarget, inbound *events.InboundMessage) error {
	conversationID := m.text.conversationID(target)
	if conversationID == "" {
		return fmt.Errorf("%s thread target is required", strings.ToLower(m.text.label))
	}

	thread, _, err := m.store.Thread(conversationID)
	if err != nil {
		return fmt.Errorf("load goal thread state: %w", err)
	}

	cronCreated := thread.CreatedBy == harnessbridge.ThreadCreatedByCron

	if cronCreated {
		disableStartNewThread(inbound)
	}

	if storedAgent := strings.TrimSpace(thread.Agent); storedAgent != "" {
		agent = storedAgent
	}

	if strings.TrimSpace(checkScript) != "" {
		if err := harnessbridge.ValidateGoalCheckScriptStart(m.runtime, agent, checkScript); err != nil {
			return fmt.Errorf("validate goal check script: %w", err)
		}
	}

	managed, err := m.ensureStartedThread(&threadStart{conversationID: conversationID, agent: agent, outputTargets: m.text.outputTargets, persistErr: "persist goal thread bridge"})
	if err != nil {
		return err
	}

	if err := m.store.BeginGoal(conversationID, objective, checkScript, maxTurns, inbound.SlackReply.RecipientTeamID, inbound.SlackReply.RecipientUserID); err != nil {
		return fmt.Errorf("persist goal: %w", err)
	}

	inbound.Label = "goal"

	inbound.ConversationID = conversationID
	if err := managed.bridge.Submit(ctx, inbound); err != nil {
		return fmt.Errorf("submit goal thread start: %w", err)
	}

	return nil
}

func (m *threadBridgeManager) WorkflowDescriptions() ([]workflow.Description, error) {
	definitions, err := m.loadWorkflowDefinitions()
	if err != nil {
		return nil, err
	}

	return workflow.Descriptions(definitions), nil
}

func (m *threadBridgeManager) StartWorkflowInThread(ctx context.Context, agent, name, args string, target events.TextConversationTarget, inbound *events.InboundMessage) error {
	definitions, err := m.loadWorkflowDefinitions()
	if err != nil {
		return err
	}

	definition := definitions[name]
	if definition == nil {
		return fmt.Errorf("workflow %q is not configured", name)
	}

	conversationID := m.text.conversationID(target)

	thread, _, err := m.store.Thread(conversationID)
	if err != nil {
		return fmt.Errorf("load workflow thread state: %w", err)
	}

	if storedAgent := strings.TrimSpace(thread.Agent); storedAgent != "" {
		agent = storedAgent
	}

	managed, err := m.ensureStartedThread(&threadStart{conversationID: conversationID, agent: agent, outputTargets: m.text.outputTargets, persistErr: "persist workflow thread bridge"})
	if err != nil {
		return err
	}

	inbound.Label, inbound.ConversationID = "workflow", conversationID
	inbound.Text = strings.TrimSpace("$workflow " + name + " " + args)

	inbound.Workflow = &workflow.RunRequest{Args: args, Definition: definition}
	if err := managed.bridge.Submit(ctx, inbound); err != nil {
		return fmt.Errorf("submit workflow thread start: %w", err)
	}

	return nil
}

func (m *threadBridgeManager) InterruptThread(target events.TextConversationTarget) (*events.InboundMessage, error) {
	conversationID := m.text.conversationID(target)
	if conversationID == "" {
		return nil, nil
	}

	if err := m.store.StopGoal(conversationID); err != nil {
		return nil, fmt.Errorf("stop goal thread: %w", err)
	}

	m.mu.Lock()
	managed := m.bridges[conversationID]
	m.mu.Unlock()

	if managed == nil {
		return nil, nil
	}

	return managed.bridge.InterruptActiveTurn(), nil
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

	managed, _, err := m.ensureThreadBridge(conversationID, thread, m.text.outputTargets, false)
	if err != nil {
		return err
	}

	if err := managed.bridge.PickLaterWork(ctx); err != nil {
		return fmt.Errorf("pick later work: %w", err)
	}

	return nil
}

func (m *threadBridgeManager) InterruptConversation(conversationID string) *events.InboundMessage {
	m.mu.Lock()
	managed := m.bridges[conversationID]
	m.mu.Unlock()

	if managed == nil {
		return nil
	}

	return managed.bridge.InterruptActiveTurn()
}

func (m *threadBridgeManager) TurnPhase(target events.TextConversationTarget) harnessbridge.ThreadTurnPhase {
	conversationID := m.text.conversationID(target)
	m.mu.Lock()
	managed := m.bridges[conversationID]
	m.mu.Unlock()

	if managed == nil {
		return harnessbridge.ThreadTurnUnclassified
	}

	return managed.bridge.TurnPhase()
}

func (m *threadBridgeManager) RegisterCronThread(_ context.Context, target events.TextConversationTarget, agent string) error {
	conversationID := m.text.conversationID(target)
	if conversationID == "" {
		return errors.New("text thread target is required")
	}

	_, err := m.ensureStartedThread(&threadStart{conversationID: conversationID, agent: agent, outputTargets: m.text.outputTargets, persistErr: "persist text cron thread bridge", createdBy: harnessbridge.ThreadCreatedByCron})

	return err
}

func (m *threadBridgeManager) RegisterThread(target events.TextConversationTarget, agent string) (bool, error) {
	conversationID := m.text.conversationID(target)
	if conversationID == "" {
		return false, errors.New("text thread target is required")
	}

	if _, ok, err := m.store.Thread(conversationID); err != nil {
		return false, fmt.Errorf("load text thread bridge: %w", err)
	} else if ok {
		return false, nil
	}

	_, err := m.ensureStartedThread(&threadStart{conversationID: conversationID, agent: agent, outputTargets: m.text.outputTargets, persistErr: "persist text thread bridge"})

	return err == nil, err
}

func (m *threadBridgeManager) SubmitExternalMCP(ctx context.Context, agent, conversationID string, inbound *events.InboundMessage, activation harnessbridge.ActivationHook) error {
	managed, _, err := m.ensureThreadBridge(conversationID, harnessbridge.ThreadState{Agent: agent}, m.text.outputTargets, false)
	if err != nil {
		return err
	}

	managed.bridge.SwitchAgent(agent)

	if err := managed.bridge.SubmitWhenActive(ctx, inbound, activation); err != nil {
		return fmt.Errorf("submit external MCP agent prompt: %w", err)
	}

	return nil
}

func (m *threadBridgeManager) RecoverActiveTurn(ctx context.Context, turn *harnessbridge.ActiveTurnState) error {
	checkpoint := turn.Checkpoint

	conversationID := strings.TrimSpace(checkpoint.ConversationKey)

	managed, _, err := m.ensureThreadBridge(conversationID, harnessbridge.ThreadState{Agent: checkpoint.Agent}, m.text.outputTargets, true)
	if err != nil {
		return err
	}

	if err := managed.bridge.RecoverActiveTurn(ctx, turn); err != nil {
		return fmt.Errorf("submit recovered active turn: %w", err)
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

func (m *threadBridgeManager) ensureStartedThread(start *threadStart) (*managedThreadBridge, error) {
	managed, created, err := m.ensureThreadBridge(start.conversationID, harnessbridge.ThreadState{Agent: start.agent}, start.outputTargets, false)
	if err != nil {
		return nil, err
	}

	if start.requireCreated && !created {
		return nil, errors.New(start.existingErr)
	}

	if created {
		if err := m.store.UpsertThread(start.conversationID, harnessbridge.ThreadState{Agent: start.agent, CreatedBy: start.createdBy}); err != nil {
			m.mu.Lock()
			delete(m.bridges, start.conversationID)
			m.mu.Unlock()

			_ = managed.bridge.Stop()

			return nil, fmt.Errorf("%s: %w", start.persistErr, err)
		}
	}

	return managed, nil
}

func disableStartNewThread(inbound *events.InboundMessage) {
	if inbound.Metadata == nil {
		inbound.Metadata = map[string]string{}
	}

	inbound.Metadata[events.InboundStartNewThreadDisabledMetadataKey] = "true"
}

func (m *threadBridgeManager) ensureThreadBridge(conversationID string, thread harnessbridge.ThreadState, outputTargets []events.OutputTarget, recoveringActiveTurn bool) (*managedThreadBridge, bool, error) {
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

	bridgeCfg := harnessbridge.Config{
		ConversationID:       conversationID,
		Agent:                strings.TrimSpace(thread.Agent),
		OutputTargets:        outputTargets,
		RecoveringActiveTurn: recoveringActiveTurn,
		UserQuestionAsker:    events.NoUserQuestionAsker(),
	}

	externalConversationID, externalSession, external, err := m.store.ExternalMCPSessionByConversationID(conversationID)
	if err != nil {
		return nil, false, fmt.Errorf("load external MCP paired conversation: %w", err)
	}

	if external {
		bridgeCfg.ManagedConversationID = externalSession.ManagedConversationID
		if conversationID == externalSession.PrivateConversationID {
			bridgeCfg.Agent = externalSession.Agent
			bridgeCfg.ExternalConversationID = externalConversationID
		} else {
			managedThread, ok, err := m.store.Thread(externalSession.ManagedConversationID)
			if err != nil {
				return nil, false, fmt.Errorf("load managed external MCP conversation: %w", err)
			}

			if ok {
				if recoveringActiveTurn {
					bridgeCfg.AgentAfterRecovery = strings.TrimSpace(managedThread.Agent)
				} else {
					bridgeCfg.Agent = strings.TrimSpace(managedThread.Agent)
				}
			}
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

	managed := &managedThreadBridge{bridge: m.factory(bridgeCfg)}
	if err := managed.bridge.Start(context.Background()); err != nil {
		return nil, false, fmt.Errorf("start text thread bridge: %w", err)
	}

	m.bridges[conversationID] = managed

	return managed, true, nil
}
