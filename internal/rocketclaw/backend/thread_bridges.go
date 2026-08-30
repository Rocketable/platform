package backend

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
)

type directBridge interface {
	Start(ctx context.Context) error
	Stop() error
	Submit(ctx context.Context, msg *protocol.InboundMessage) error
	SubmitWhenActive(ctx context.Context, msg *protocol.InboundMessage, activation protocol.ActivationHook) error
	RecoverActiveTurn(ctx context.Context, turn *ActiveTurnState) error
	InterruptActiveTurn() *protocol.InboundMessage
	SwitchAgent(agent string)
	PickLaterWork(ctx context.Context) error
}

type managedThreadBridge struct {
	bridge directBridge
}

type threadStart struct {
	conversationID, agent   string
	outputTargets           []protocol.OutputTarget
	requireCreated          bool
	existingErr, persistErr string
	createdBy               ThreadCreator
}

type primaryTextBinding struct {
	label         string
	outputTargets []protocol.OutputTarget
}

type threadBridgeManager struct {
	log     *slog.Logger
	runtime *config.Config
	store   *SessionService
	factory func(Config) directBridge
	text    primaryTextBinding
	output  func(context.Context, *protocol.OutboundMessage) error
	abort   func(*protocol.OutboundMessage)
	root    func(context.Context, *protocol.StartNewThreadRequest) (protocol.StartNewThreadRootResult, error)

	mu      sync.Mutex
	bridges map[string]*managedThreadBridge
}

var _ protocol.PrimaryTextRouter = (*threadBridgeManager)(nil)

func newThreadBridgeManager(runtime *config.Config, store *SessionService, logger *slog.Logger, factory func(Config) directBridge) *threadBridgeManager {
	return &threadBridgeManager{
		log: logger.With("component", "thread_bridges"), runtime: runtime, store: store, factory: factory,
		text:   primaryTextBinding{label: "Slack", outputTargets: []protocol.OutputTarget{protocol.OutputTargetSlack}},
		output: func(context.Context, *protocol.OutboundMessage) error { return nil },
		abort:  func(*protocol.OutboundMessage) {},
		root: func(context.Context, *protocol.StartNewThreadRequest) (protocol.StartNewThreadRootResult, error) {
			return protocol.StartNewThreadRootResult{}, errors.New("thread bridge is not ready")
		},
		mu:      sync.Mutex{},
		bridges: map[string]*managedThreadBridge{},
	}
}

func (b primaryTextBinding) conversationID(target protocol.TextConversationTarget) string {
	return protocol.SlackThreadConversationID(strings.TrimSpace(target.ChannelID), strings.TrimSpace(target.ThreadID))
}

func (b primaryTextBinding) targetForConversationID(conversationID string) (protocol.TextConversationTarget, bool) {
	channelID, threadTS, ok := protocol.SlackThreadTarget(conversationID)

	return protocol.TextConversationTarget{ChannelID: channelID, MessageID: threadTS, ThreadID: threadTS}, ok
}

func (b primaryTextBinding) setContinuationReply(inbound *protocol.InboundMessage, target protocol.TextConversationTarget) {
	inbound.SlackReply = &protocol.SlackReplyTarget{ChannelID: target.ChannelID, MessageTS: target.MessageID, ThreadTS: target.ThreadID}
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

		if _, _, err := m.ensureThreadBridge(conversationID, ThreadState{Agent: message.Agent}, m.text.outputTargets, false); err != nil {
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

		inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "goal_continuation", "Continue the active goal loop.", false)
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

func (m *threadBridgeManager) SubmitThreadReply(ctx context.Context, target protocol.TextConversationTarget, inbound *protocol.InboundMessage) (bool, error) {
	return m.SubmitWhenActive(ctx, target, inbound, NoopActivationHook)
}

func (m *threadBridgeManager) SubmitWhenActive(ctx context.Context, target protocol.TextConversationTarget, inbound *protocol.InboundMessage, activation protocol.ActivationHook) (bool, error) {
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

	if thread.CreatedBy == ThreadCreatedByCron {
		disableStartNewThread(inbound)
	}

	if inbound.SlackReply != nil {
		inbound.SlackReply.ThreadTS = strings.TrimSpace(target.ThreadID)
	}

	managed, _, err := m.ensureThreadBridge(conversationID, thread, m.text.outputTargets, false)
	if err != nil {
		return false, err
	}

	inbound.ConversationID = conversationID
	m.prepareOriginator(ctx, inbound)

	if err := managed.bridge.SubmitWhenActive(ctx, inbound, activation); err != nil {
		return true, fmt.Errorf("submit %s thread reply: %w", m.text.label, err)
	}

	return true, nil
}

func (m *threadBridgeManager) StashThreadQueueItem(_ context.Context, target protocol.TextConversationTarget, item *protocol.ThreadQueueItem) error {
	conversationID := m.text.conversationID(target)
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

	return nil
}

func (m *threadBridgeManager) ThreadQueueItems(_ context.Context, target protocol.TextConversationTarget) ([]protocol.ThreadQueueItem, error) {
	items, err := m.store.ThreadQueueForConversation(m.text.conversationID(target))
	if err != nil {
		return nil, fmt.Errorf("list thread queue: %w", err)
	}

	return items, nil
}

func (m *threadBridgeManager) DeleteThreadQueueItem(ctx context.Context, target protocol.TextConversationTarget, id string) error {
	conversationID := m.text.conversationID(target)

	items, err := m.store.ThreadQueueForConversation(conversationID)
	if err != nil {
		return fmt.Errorf("list thread queue: %w", err)
	}

	for i := range items {
		if items[i].ID == id {
			if waiter := m.store.TakeMCPWaiter(id); waiter != nil {
				waiter.CompleteResponse("", errors.New("queue row removed"))
			}

			if err := m.store.DeleteThreadQueueItem(id); err != nil {
				return fmt.Errorf("delete thread queue item: %w", err)
			}

			return m.PickLaterWork(ctx, conversationID)
		}
	}

	return nil
}

func (m *threadBridgeManager) ScheduledMessages(_ context.Context, target protocol.TextConversationTarget) (map[string]protocol.ScheduledMessageState, error) {
	messages, err := m.store.ScheduledMessagesForConversation(m.text.conversationID(target))
	if err != nil {
		return nil, fmt.Errorf("list scheduled messages: %w", err)
	}

	return messages, nil
}

func (m *threadBridgeManager) SwitchThreadAgent(target protocol.TextConversationTarget, agent string) (bool, error) {
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

func (m *threadBridgeManager) ThreadAgent(target protocol.TextConversationTarget) (agent string, handled bool, err error) {
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

func (m *threadBridgeManager) ReserveWorkflowTurn(target protocol.TextConversationTarget) (release func(), reserved bool, err error) {
	conversationID := m.text.conversationID(target)

	release, reserved, err = m.store.ReserveWorkflowTurn(conversationID)
	if err != nil {
		err = fmt.Errorf("reserve workflow turn: %w", err)
	}

	return release, reserved, err
}

func (m *threadBridgeManager) StartThread(ctx context.Context, agent string, target protocol.TextConversationTarget, inbound *protocol.InboundMessage) error {
	conversationID := m.text.conversationID(target)
	if conversationID == "" {
		return fmt.Errorf("%s thread target is required", strings.ToLower(m.text.label))
	}

	managed, err := m.ensureStartedThread(&threadStart{conversationID: conversationID, agent: agent, outputTargets: m.text.outputTargets, persistErr: "persist " + m.text.label + " thread bridge"})
	if err != nil {
		return err
	}

	inbound.ConversationID = conversationID

	return m.submitInbound(ctx, managed.bridge, inbound, m.text.label+" thread start")
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
		conversationID, url, label string
		outputTargets              []protocol.OutputTarget
		rootTarget                 protocol.TextConversationTarget
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
		conversationID, url = m.text.conversationID(rootTarget), root.URL
		outputTargets, label = m.text.outputTargets, m.text.label
	case protocol.SourceExternalMCP:
		return protocol.StartNewThreadResult{}, fmt.Errorf("rocketclaw_start_new_thread is not available for %s turns", req.Source)
	}

	managed, err := m.ensureStartedThread(&threadStart{conversationID: conversationID, agent: targetAgent, outputTargets: outputTargets, requireCreated: true, existingErr: label + " new thread conversation already exists", persistErr: "persist " + label + " new thread bridge"})
	if err != nil {
		return protocol.StartNewThreadResult{}, err
	}

	inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "rocketclaw_start_new_thread", req.Prompt, false)
	inbound.ConversationID = conversationID
	inbound.Response = req.Response
	m.text.setContinuationReply(inbound, protocol.TextConversationTarget{ChannelID: strings.TrimSpace(rootTarget.ChannelID), MessageID: strings.TrimSpace(rootTarget.MessageID), ThreadID: strings.TrimSpace(rootTarget.ThreadID)})

	inbound.Metadata = map[string]string{protocol.InboundOriginMetadataKey: "System", protocol.InboundMediaMetadataKey: "Text"}
	if err := managed.bridge.Submit(ctx, inbound); err != nil {
		return protocol.StartNewThreadResult{}, fmt.Errorf("submit %s new thread first prompt: %w", label, err)
	}

	return protocol.StartNewThreadResult{ConversationID: conversationID, URL: url}, nil
}

func (m *threadBridgeManager) StartGoalInThread(ctx context.Context, agent, objective, checkScript string, maxTurns int, target protocol.TextConversationTarget, inbound *protocol.InboundMessage) error {
	conversationID := m.text.conversationID(target)
	if conversationID == "" {
		return fmt.Errorf("%s thread target is required", strings.ToLower(m.text.label))
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

	managed, err := m.ensureStartedThread(&threadStart{conversationID: conversationID, agent: agent, outputTargets: m.text.outputTargets, persistErr: "persist goal thread bridge"})
	if err != nil {
		return err
	}

	if err := m.store.BeginGoal(conversationID, objective, checkScript, maxTurns, inbound.SlackReply.RecipientTeamID, inbound.SlackReply.RecipientUserID); err != nil {
		return fmt.Errorf("persist goal: %w", err)
	}

	inbound.Label = "goal"
	inbound.ConversationID = conversationID

	return m.submitInbound(ctx, managed.bridge, inbound, "goal thread start")
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

	inbound.Workflow = &protocol.WorkflowInvocation{Name: name, Args: args}

	return m.submitInbound(ctx, managed.bridge, inbound, "workflow thread start")
}

func (m *threadBridgeManager) InterruptThread(target protocol.TextConversationTarget) (*protocol.InboundMessage, error) {
	conversationID := m.text.conversationID(target)
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

	managed, _, err := m.ensureThreadBridge(conversationID, thread, m.text.outputTargets, false)
	if err != nil {
		return err
	}

	if err := managed.bridge.PickLaterWork(ctx); err != nil {
		return fmt.Errorf("pick later work: %w", err)
	}

	return nil
}

func (m *threadBridgeManager) InterruptConversation(conversationID string) *protocol.InboundMessage {
	m.mu.Lock()
	managed := m.bridges[conversationID]
	m.mu.Unlock()

	if managed == nil {
		return nil
	}

	return managed.bridge.InterruptActiveTurn()
}

func (m *threadBridgeManager) ThreadBusy(target protocol.TextConversationTarget) bool {
	return m.store.PairBusy(m.text.conversationID(target))
}

func (m *threadBridgeManager) RegisterCronThread(_ context.Context, target protocol.TextConversationTarget, agent string) error {
	conversationID := m.text.conversationID(target)
	if conversationID == "" {
		return errors.New("text thread target is required")
	}

	_, err := m.ensureStartedThread(&threadStart{conversationID: conversationID, agent: agent, outputTargets: m.text.outputTargets, persistErr: "persist text cron thread bridge", createdBy: ThreadCreatedByCron})

	return err
}

func (m *threadBridgeManager) RegisterThread(target protocol.TextConversationTarget, agent string) (bool, error) {
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

func (m *threadBridgeManager) SubmitExternalMCP(ctx context.Context, agent, conversationID string, inbound *protocol.InboundMessage, activation protocol.ActivationHook) error {
	managedID := strings.TrimSpace(conversationID)

	_, session, ok, errSession := m.store.ExternalMCPSessionByConversationID(conversationID)
	if errSession != nil {
		return fmt.Errorf("load external MCP session: %w", errSession)
	}

	if ok && strings.TrimSpace(session.ManagedConversationID) != "" {
		managedID = session.ManagedConversationID
	}

	if m.store.PairBusyFor(managedID, conversationID) {
		return m.stashBusyExternalMCP(ctx, inbound, managedID)
	}

	managed, _, err := m.ensureThreadBridge(conversationID, ThreadState{Agent: agent}, m.text.outputTargets, false)
	if err != nil {
		return err
	}

	managed.bridge.SwitchAgent(agent)

	inbound.Bridge = protocol.BridgeExternalMCP

	if err := managed.bridge.SubmitWhenActive(ctx, inbound, activation); err != nil {
		return fmt.Errorf("submit external MCP agent prompt: %w", err)
	}

	return nil
}

func (m *threadBridgeManager) RecoverActiveTurn(ctx context.Context, turn *ActiveTurnState) error {
	checkpoint := turn.Checkpoint

	conversationID := strings.TrimSpace(checkpoint.ConversationKey)

	managed, _, err := m.ensureThreadBridge(conversationID, ThreadState{Agent: checkpoint.Agent}, m.text.outputTargets, true)
	if err != nil {
		return err
	}

	if err := managed.bridge.RecoverActiveTurn(ctx, turn); err != nil {
		return fmt.Errorf("submit recovered active turn: %w", err)
	}

	return nil
}

func (m *threadBridgeManager) stashBusyExternalMCP(ctx context.Context, inbound *protocol.InboundMessage, managedID string) error {
	target, ok := m.text.targetForConversationID(managedID)
	if !ok {
		return fmt.Errorf("managed conversation %q is not a Slack thread", managedID)
	}

	principal := ""
	if inbound.Metadata != nil {
		principal = inbound.Metadata[protocol.InboundPrincipalMetadataKey]
	}

	item := &protocol.ThreadQueueItem{ID: rand.Text(), Message: inbound.Text, Principal: principal, StashAt: time.Now().UTC()}
	if err := m.StashThreadQueueItem(ctx, target, item); err != nil {
		return err
	}

	m.store.PutMCPWaiter(item.ID, inbound)

	return nil
}

func (m *threadBridgeManager) prepareOriginator(ctx context.Context, inbound *protocol.InboundMessage) {
	if inbound.Bridge == "" {
		switch inbound.Source {
		case protocol.SourceSlack:
			inbound.Bridge = protocol.BridgeSlack
		case protocol.SourceExternalMCP:
			inbound.Bridge = protocol.BridgeExternalMCP
		case protocol.SourceSystem:
			// System turns keep any caller-set bridge.
		}
	}

	if inbound.Bridge != protocol.BridgeSlack || inbound.Response != nil {
		return
	}

	inbound.Response = make(chan protocol.Response, 8)
	go m.consumeOutput(ctx, inbound.Response)
}

func (m *threadBridgeManager) consumeOutput(ctx context.Context, responses <-chan protocol.Response) {
	for {
		select {
		case result := <-responses:
			handled, err := m.handleInteraction(ctx, result.Payload)
			if err != nil {
				return
			}

			if handled {
				continue
			}

			payload, ok := result.Payload.(*protocol.TextResponse)
			if !ok || payload.Message == nil {
				return
			}

			if err := m.output(ctx, payload.Message); err != nil {
				if payload.Message.Complete {
					m.abort(payload.Message)
					payload.Message.MarkDelivered(err)

					return
				}

				m.abort(payload.Message)

				continue
			}

			if payload.Message.Complete {
				payload.Message.MarkDelivered(nil)

				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func (m *threadBridgeManager) handleInteraction(ctx context.Context, payload protocol.ResponsePayload) (bool, error) {
	switch interaction := payload.(type) {
	case protocol.StartNewThreadResponse:
		root, err := m.root(ctx, interaction.Request)
		if err != nil {
			interaction.Err <- err

			return true, err
		}

		interaction.Root <- root

		return true, nil
	default:
		return false, nil
	}
}

func (m *threadBridgeManager) submitInbound(ctx context.Context, managed directBridge, inbound *protocol.InboundMessage, wrap string) error {
	m.prepareOriginator(ctx, inbound)

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

func (m *threadBridgeManager) ensureStartedThread(start *threadStart) (*managedThreadBridge, error) {
	managed, created, err := m.ensureThreadBridge(start.conversationID, ThreadState{Agent: start.agent}, start.outputTargets, false)
	if err != nil {
		return nil, err
	}

	if start.requireCreated && !created {
		return nil, errors.New(start.existingErr)
	}

	if created {
		if err := m.store.UpsertThread(start.conversationID, ThreadState{Agent: start.agent, CreatedBy: start.createdBy}); err != nil {
			m.mu.Lock()
			delete(m.bridges, start.conversationID)
			m.mu.Unlock()

			_ = managed.bridge.Stop()

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

func (m *threadBridgeManager) ensureThreadBridge(conversationID string, thread ThreadState, outputTargets []protocol.OutputTarget, recoveringActiveTurn bool) (*managedThreadBridge, bool, error) {
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
		OutputTargets:        outputTargets,
		RecoveringActiveTurn: recoveringActiveTurn,
		UserQuestionAsker:    protocol.NoUserQuestionAsker(),
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
