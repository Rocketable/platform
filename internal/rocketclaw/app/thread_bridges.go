package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
)

type directBridge interface {
	Start(ctx context.Context) error
	Stop() error
	Submit(ctx context.Context, msg *events.InboundMessage) error
	SeedThreadFromConversation(ctx context.Context, sourceConversationID string) error
	SeedThreadFromCron(ctx context.Context, seedText string) error
	SeedResponseThread(ctx context.Context, checkpoint events.ResponseCheckpoint) error
	Summarize(ctx context.Context, prompt string) (string, error)
	WaitIdle(ctx context.Context) error
	InterruptActiveTurn() *events.InboundMessage
	SwitchAgent(agent string)
}

type bridgeConfig struct {
	ConversationID, Agent string
	OutputTargets         []events.OutputTarget
}

type bridgeFactory func(bridgeConfig) directBridge

type managedThreadBridge struct {
	bridge        directBridge
	summarizing   bool
	queuedReplies []*events.InboundMessage
}

type threadBridgeSnapshot struct {
	conversationID string
	bridge         directBridge
	summarizing    bool
	queuedReplies  int
}

type threadStart struct {
	conversationID, agent, seedConversationID, seedCronText string
	outputTargets                                           []events.OutputTarget
	requireCreated                                          bool
	existingErr, seedErr, persistErr, makerErr              string
	createdBy                                               harnessbridge.ThreadCreator
}

const textThreadSummaryPrompt = "Summarize the current state of this managed text thread for handoff to the main session. Keep it concise. Include the user's goal, the important facts, decisions already made, open questions, and the next useful follow-up. Return only the summary text."

type primaryTextBinding struct {
	label         string
	outputTargets []events.OutputTarget
}

type threadBridgeManager struct {
	log     *slog.Logger
	runtime *config.Config
	store   *harnessbridge.SessionService
	bus     *events.Bus
	factory bridgeFactory
	targets []events.OutputTarget
	text    primaryTextBinding

	mu       sync.Mutex
	draining bool
	bridges  map[string]*managedThreadBridge
}

func newThreadBridgeManager(bus *events.Bus, runtime *config.Config, store *harnessbridge.SessionService, logger *slog.Logger, factory bridgeFactory) *threadBridgeManager {
	return &threadBridgeManager{log: logger.With("component", "thread_bridges"), runtime: runtime, store: store, bus: bus, factory: factory, targets: events.MainOutputTargets(), text: primaryTextBindingFor(), mu: sync.Mutex{}, bridges: map[string]*managedThreadBridge{}}
}

func primaryTextBindingFor() primaryTextBinding {
	return primaryTextBinding{label: "Slack", outputTargets: []events.OutputTarget{events.OutputTargetSlackMain}}
}

func (b primaryTextBinding) conversationID(target events.TextConversationTarget) string {
	return harnessbridge.SlackThreadConversationID(strings.TrimSpace(target.ChannelID), strings.TrimSpace(target.ThreadID))
}

func (b primaryTextBinding) checkpointKey(target events.TextConversationTarget) string {
	return harnessbridge.SlackResponseCheckpointKey(target.ChannelID, target.MessageID)
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

func (b primaryTextBinding) publishSummary(ctx context.Context, bus *events.Bus, log *slog.Logger, target events.TextConversationTarget, summary string) error {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return errors.New("thread summary is empty")
	}

	metadata := "slack_thread_summary channel=" + strings.TrimSpace(target.ChannelID) + " thread=" + strings.TrimSpace(target.ThreadID)
	body := "Slack thread summary from channel " + strings.TrimSpace(target.ChannelID) + " thread " + strings.TrimSpace(target.ThreadID) + ":\n\n" + summary

	inbound := events.NewMainInboundMessage(events.SourceSystem, events.InboundKindInternalize, metadata, body, false)
	if err := bus.PublishInbound(ctx, inbound); err != nil {
		return fmt.Errorf("publish %s thread summary: %w", b.label, err)
	}

	log.Info("enqueued text thread summary in main inbound queue", "connector", b.label, "channel", strings.TrimSpace(target.ChannelID), "thread_id", strings.TrimSpace(target.ThreadID), "text_len", len(summary))

	return nil
}

func (m *threadBridgeManager) Stop() error {
	var errStop error

	bridges := m.bridgesSnapshot()
	for i := range bridges {
		errStop = errors.Join(errStop, bridges[i].bridge.Stop())
	}

	return errStop
}

func (m *threadBridgeManager) StopAccepting() {
	m.mu.Lock()
	m.draining = true
	m.mu.Unlock()
}

func (m *threadBridgeManager) WaitIdle(ctx context.Context) error {
	var errWait error

	bridges := m.bridgesSnapshot()
	m.mu.Lock()
	draining := m.draining
	m.mu.Unlock()

	m.log.Info("thread bridge manager idle wait state", "bridge_count", len(bridges), "draining", draining)

	for i := range bridges {
		m.log.Info("thread bridge idle wait state", "conversation_id", bridges[i].conversationID, "summarizing", bridges[i].summarizing, "queued_replies", bridges[i].queuedReplies)
		errWait = errors.Join(errWait, bridges[i].bridge.WaitIdle(ctx))
	}

	return errWait
}

func (m *threadBridgeManager) StartPendingScheduledMessages() error {
	scheduledMessages, err := m.store.ScheduledMessages()
	if err != nil {
		return fmt.Errorf("load pending scheduled message bridges: %w", err)
	}

	for _, message := range scheduledMessages {
		conversationID := strings.TrimSpace(message.ConversationID)
		if conversationID == events.MainConversationID() {
			continue
		}

		outputTargets := []events.OutputTarget{events.OutputTargetSlackMain}
		if strings.HasPrefix(conversationID, "external_mcp:") {
			outputTargets = m.targets
		}

		if _, _, err := m.ensureThreadBridge(conversationID, harnessbridge.ThreadState{Agent: message.Agent}, outputTargets); err != nil {
			return fmt.Errorf("start pending scheduled message bridge: %w", err)
		}
	}

	return nil
}

func (m *threadBridgeManager) StartActiveGoals() error {
	threads, err := m.store.ActiveGoalThreads()
	if err != nil {
		return fmt.Errorf("load active goal bridges: %w", err)
	}

	for conversationID, thread := range threads {
		target, ok := m.text.targetForConversationID(conversationID)
		if !ok {
			continue
		}

		managed, _, err := m.ensureThreadBridge(conversationID, thread, m.text.outputTargets)
		if err != nil {
			return fmt.Errorf("start active goal bridge: %w", err)
		}

		inbound := events.NewMainInboundMessage(events.SourceSystem, events.InboundKindPrompt, "goal_continuation", "Continue the active goal loop.", false)
		inbound.ConversationID = conversationID
		m.text.setContinuationReply(inbound, target)

		if err := managed.bridge.Submit(context.Background(), inbound); err != nil {
			return fmt.Errorf("submit active goal continuation: %w", err)
		}
	}

	return nil
}

func (m *threadBridgeManager) SubmitThreadReply(ctx context.Context, target events.TextConversationTarget, inbound *events.InboundMessage) (bool, error) {
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

	cronCreated, err := m.cronCreatedThread(ctx, conversationID, thread)
	if err != nil {
		return false, err
	}

	if strings.HasPrefix(thread.SeededFromResponse, "external_mcp:") {
		disableStartNewThread(inbound)
		m.text.setReplyThread(inbound, target)

		if err := m.SubmitExternalMCP(ctx, thread.Agent, thread.SeededFromResponse, inbound); err != nil {
			return true, fmt.Errorf("submit external MCP %s thread reply: %w", m.text.label, err)
		}

		return true, nil
	}

	if cronCreated {
		disableStartNewThread(inbound)
	}

	m.text.setReplyThread(inbound, target)

	managed, _, err := m.ensureThreadBridge(conversationID, thread, m.text.outputTargets)
	if err != nil {
		return false, err
	}

	inbound.ConversationID = conversationID

	m.mu.Lock()
	if managed.summarizing {
		managed.queuedReplies = append(managed.queuedReplies, inbound)
		m.mu.Unlock()

		return true, nil
	}

	bridge := managed.bridge
	m.mu.Unlock()

	if err := bridge.Submit(ctx, inbound); err != nil {
		return true, fmt.Errorf("submit %s thread reply: %w", m.text.label, err)
	}

	return true, nil
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

func (m *threadBridgeManager) RecordResponseCheckpoint(target events.TextConversationTarget, checkpoint events.ResponseCheckpoint) error {
	checkpointKey := m.text.checkpointKey(target)
	if checkpointKey == "" {
		return nil
	}

	if err := m.store.UpsertResponseCheckpoint(checkpointKey, harnessbridge.ResponseCheckpointState{ConversationID: checkpoint.ConversationID, SessionEntryID: checkpoint.SessionEntryID, ResponseID: checkpoint.ResponseID, Model: checkpoint.Model, AssistantText: checkpoint.AssistantText}); err != nil {
		return fmt.Errorf("persist %s response checkpoint: %w", m.text.label, err)
	}

	return nil
}

func (m *threadBridgeManager) PrepareResponseThreadReply(target events.TextConversationTarget) (bool, error) {
	checkpointKey := m.text.checkpointKey(target)
	if checkpointKey == "" {
		return false, nil
	}

	_, ok, err := m.store.ResponseCheckpoint(checkpointKey)
	if err != nil {
		return false, fmt.Errorf("load persisted %s response checkpoint: %w", m.text.label, err)
	}

	return ok, nil
}

func (m *threadBridgeManager) SubmitResponseThreadReply(ctx context.Context, target events.TextConversationTarget, inbound *events.InboundMessage) (bool, error) {
	conversationID := m.text.conversationID(target)

	checkpointKey := m.text.checkpointKey(target)
	if conversationID == "" || checkpointKey == "" {
		return false, nil
	}

	m.text.setReplyThread(inbound, target)

	checkpoint, ok, err := m.store.ResponseCheckpoint(checkpointKey)
	if err != nil {
		return false, fmt.Errorf("load persisted %s response checkpoint: %w", m.text.label, err)
	}

	if !ok {
		return false, nil
	}

	thread, _, err := m.store.Thread(conversationID)
	if err != nil {
		return false, fmt.Errorf("load persisted %s thread state: %w", m.text.label, err)
	}

	managed, _, err := m.ensureThreadBridge(conversationID, harnessbridge.ThreadState{Agent: "main", SeededFromResponse: strings.TrimSpace(thread.SeededFromResponse)}, m.text.outputTargets)
	if err != nil {
		return true, err
	}

	seededFrom := strings.TrimSpace(thread.SeededFromResponse)
	if seededFrom != checkpointKey {
		if seededFrom != "" {
			return true, fmt.Errorf("%s thread already seeded from %s", strings.ToLower(m.text.label), seededFrom)
		}

		if err := managed.bridge.SeedResponseThread(ctx, events.ResponseCheckpoint{ConversationID: checkpoint.ConversationID, SessionEntryID: checkpoint.SessionEntryID, ResponseID: checkpoint.ResponseID, Model: checkpoint.Model, AssistantText: checkpoint.AssistantText}); err != nil {
			return true, fmt.Errorf("seed %s response-rooted thread: %w", m.text.label, err)
		}

		if err := m.store.MarkThreadSeeded(conversationID, checkpointKey); err != nil {
			return true, fmt.Errorf("persist %s response-rooted thread seed: %w", m.text.label, err)
		}
	}

	inbound.ConversationID = conversationID

	if err := managed.bridge.Submit(ctx, inbound); err != nil {
		return true, fmt.Errorf("submit %s response-rooted thread reply: %w", m.text.label, err)
	}

	return true, nil
}

func (m *threadBridgeManager) SummarizeThread(ctx context.Context, target events.TextConversationTarget) (bool, error) {
	conversationID := m.text.conversationID(target)
	if conversationID == "" {
		return false, nil
	}

	m.mu.Lock()
	managed := m.bridges[conversationID]
	m.mu.Unlock()

	if managed == nil {
		thread, ok, err := m.store.Thread(conversationID)
		if err != nil {
			return false, fmt.Errorf("load persisted %s thread state: %w", m.text.label, err)
		}

		if !ok {
			return false, nil
		}

		managed, _, err = m.ensureThreadBridge(conversationID, thread, m.text.outputTargets)
		if err != nil {
			return true, err
		}
	}

	m.mu.Lock()
	if managed.summarizing {
		m.mu.Unlock()
		return true, nil
	}

	managed.summarizing = true
	bridge := managed.bridge
	m.mu.Unlock()

	summary, errSummarize := bridge.Summarize(ctx, textThreadSummaryPrompt)

	var errPublish error
	if errSummarize == nil {
		errPublish = m.text.publishSummary(ctx, m.bus, m.log, target, summary)
	}

	errDrain := m.finishSummarizeThread(conversationID, managed)

	return true, errors.Join(errSummarize, errPublish, errDrain)
}

func (m *threadBridgeManager) StartThread(ctx context.Context, agent string, preSeed bool, target events.TextConversationTarget, inbound *events.InboundMessage) error {
	conversationID := m.text.conversationID(target)
	if conversationID == "" {
		return fmt.Errorf("%s thread target is required", strings.ToLower(m.text.label))
	}

	seedConversationID := ""
	if preSeed {
		seedConversationID = events.MainConversationID()
	}

	managed, err := m.ensureStartedThread(ctx, &threadStart{conversationID: conversationID, agent: agent, outputTargets: m.text.outputTargets, seedConversationID: seedConversationID, seedErr: "seed " + m.text.label + " thread from main session", persistErr: "persist " + m.text.label + " thread bridge"})
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

	agents, err := harnessbridge.ExternalMCPAgentsIn(m.runtime.Workspace, m.runtime.WorkDirName())
	if err != nil {
		return events.StartNewThreadResult{}, fmt.Errorf("load configured agents: %w", err)
	}

	if !slices.Contains(agents, targetAgent) {
		return events.StartNewThreadResult{}, fmt.Errorf("agent %q is not configured", targetAgent)
	}

	var (
		conversationID, url, label string
		outputTargets              []events.OutputTarget
	)

	switch req.Source {
	case events.SourceSlack:
		root, err := createRoot(ctx, req)
		if err != nil {
			return events.StartNewThreadResult{}, err
		}

		conversationID, url = m.text.conversationID(root.Target), root.URL
		outputTargets, label = m.text.outputTargets, m.text.label
	case events.SourceExternalMCP, events.SourceSystem:
		return events.StartNewThreadResult{}, fmt.Errorf("rocketclaw_start_new_thread is not available for %s turns", req.Source)
	}

	managed, err := m.ensureStartedThread(ctx, &threadStart{conversationID: conversationID, agent: targetAgent, outputTargets: outputTargets, requireCreated: true, existingErr: label + " new thread conversation already exists", seedConversationID: req.SourceConversationID, seedErr: "seed " + label + " new thread from source conversation", persistErr: "persist " + label + " new thread bridge"})
	if err != nil {
		return events.StartNewThreadResult{}, err
	}

	inbound := events.NewMainInboundMessage(events.SourceSystem, events.InboundKindPrompt, "rocketclaw_start_new_thread", events.StartNewThreadFirstPrompt(req, targetAgent), false)
	inbound.ConversationID = conversationID

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

	cronCreated, err := m.cronCreatedThread(ctx, conversationID, thread)
	if err != nil {
		return err
	}

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

	managed, err := m.ensureStartedThread(ctx, &threadStart{conversationID: conversationID, agent: agent, outputTargets: m.text.outputTargets, persistErr: "persist goal thread bridge"})
	if err != nil {
		return err
	}

	if err := m.store.BeginGoal(conversationID, objective, checkScript, maxTurns); err != nil {
		return fmt.Errorf("persist goal: %w", err)
	}

	inbound.Label = "goal"

	inbound.ConversationID = conversationID
	if err := managed.bridge.Submit(ctx, inbound); err != nil {
		return fmt.Errorf("submit goal thread start: %w", err)
	}

	return nil
}

func (m *threadBridgeManager) InterruptThread(target events.TextConversationTarget) (*events.InboundMessage, error) {
	conversationID := m.text.conversationID(target)
	if conversationID == "" {
		return nil, nil
	}

	bridgeConversationID := conversationID

	thread, ok, err := m.store.Thread(conversationID)
	if err != nil {
		return nil, fmt.Errorf("load persisted %s thread state: %w", m.text.label, err)
	}

	if ok && strings.HasPrefix(thread.SeededFromResponse, "external_mcp:") {
		bridgeConversationID = thread.SeededFromResponse
	}

	if err := m.store.StopGoal(conversationID); err != nil {
		return nil, fmt.Errorf("stop goal thread: %w", err)
	}

	m.mu.Lock()

	managed := m.bridges[bridgeConversationID]
	if managed != nil {
		managed.queuedReplies = nil
	}

	if bridgeConversationID != conversationID {
		if visible := m.bridges[conversationID]; visible != nil {
			visible.queuedReplies = nil
		}
	}
	m.mu.Unlock()

	if managed == nil {
		return nil, nil
	}

	return managed.bridge.InterruptActiveTurn(), nil
}

func (m *threadBridgeManager) RegisterCronThread(ctx context.Context, target events.TextConversationTarget, agent, seedText string) error {
	conversationID := m.text.conversationID(target)
	if conversationID == "" {
		return errors.New("text thread target is required")
	}

	_, err := m.ensureStartedThread(ctx, &threadStart{conversationID: conversationID, agent: agent, outputTargets: m.text.outputTargets, seedCronText: seedText, seedErr: "seed text cron thread", persistErr: "persist text cron thread bridge", createdBy: harnessbridge.ThreadCreatedByCron, makerErr: "persist text cron thread origin"})

	return err
}

func (m *threadBridgeManager) SubmitExternalMCP(ctx context.Context, agent, conversationID string, inbound *events.InboundMessage) error {
	managed, _, err := m.ensureThreadBridge(conversationID, harnessbridge.ThreadState{Agent: agent}, m.targets)
	if err != nil {
		return err
	}

	if err := managed.bridge.Submit(ctx, inbound); err != nil {
		return fmt.Errorf("submit external MCP agent prompt: %w", err)
	}

	return nil
}

func (m *threadBridgeManager) ensureStartedThread(ctx context.Context, start *threadStart) (*managedThreadBridge, error) {
	managed, created, err := m.ensureThreadBridge(start.conversationID, harnessbridge.ThreadState{Agent: start.agent}, start.outputTargets)
	if err != nil {
		return nil, err
	}

	if start.requireCreated && !created {
		return nil, errors.New(start.existingErr)
	}

	if created {
		if start.seedConversationID != "" {
			if err := managed.bridge.SeedThreadFromConversation(ctx, start.seedConversationID); err != nil {
				m.dropCreatedBridge(start.conversationID, managed)
				return nil, fmt.Errorf("%s: seed thread from source conversation: %w", start.seedErr, err)
			}
		}

		if start.seedCronText != "" {
			if err := managed.bridge.SeedThreadFromCron(ctx, start.seedCronText); err != nil {
				m.dropCreatedBridge(start.conversationID, managed)
				return nil, fmt.Errorf("%s: seed thread from cron: %w", start.seedErr, err)
			}
		}

		if err := m.store.UpsertThread(start.conversationID, start.agent); err != nil {
			m.dropCreatedBridge(start.conversationID, managed)
			return nil, fmt.Errorf("%s: %w", start.persistErr, err)
		}

		if start.createdBy != "" {
			if err := m.store.MarkThreadCreatedBy(start.conversationID, start.createdBy); err != nil {
				m.dropCreatedBridge(start.conversationID, managed)
				return nil, fmt.Errorf("%s: %w", start.makerErr, err)
			}
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

func (m *threadBridgeManager) dropCreatedBridge(conversationID string, managed *managedThreadBridge) {
	m.mu.Lock()
	delete(m.bridges, conversationID)
	m.mu.Unlock()

	_ = managed.bridge.Stop()
}

func (m *threadBridgeManager) cronCreatedThread(ctx context.Context, conversationID string, thread harnessbridge.ThreadState) (bool, error) {
	if thread.CreatedBy == harnessbridge.ThreadCreatedByCron {
		return true, nil
	}

	entries, err := m.store.ObserveEntries(ctx, conversationID, 0)
	if err != nil {
		return false, fmt.Errorf("load thread seed entries: %w", err)
	}

	for i := range entries {
		if entries[i].Entry.Type == "cron_thread_seed" {
			if err := m.store.MarkThreadCreatedBy(conversationID, harnessbridge.ThreadCreatedByCron); err != nil {
				return false, fmt.Errorf("persist legacy cron thread origin: %w", err)
			}

			return true, nil
		}
	}

	return false, nil
}

func (m *threadBridgeManager) bridgesSnapshot() []threadBridgeSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	bridges := make([]threadBridgeSnapshot, 0, len(m.bridges))
	for conversationID, managed := range m.bridges {
		bridges = append(bridges, threadBridgeSnapshot{conversationID: conversationID, bridge: managed.bridge, summarizing: managed.summarizing, queuedReplies: len(managed.queuedReplies)})
	}

	slices.SortFunc(bridges, func(a, b threadBridgeSnapshot) int { return strings.Compare(a.conversationID, b.conversationID) })

	return bridges
}

func (m *threadBridgeManager) ensureThreadBridge(conversationID string, thread harnessbridge.ThreadState, outputTargets []events.OutputTarget) (*managedThreadBridge, bool, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, false, errors.New("text thread conversation ID is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.draining {
		return nil, false, errors.New("thread bridges are draining")
	}

	if managed := m.bridges[conversationID]; managed != nil {
		return managed, false, nil
	}

	thread.Agent = strings.TrimSpace(thread.Agent)
	if thread.Agent == "" {
		thread.Agent = "main"
	}

	managed := &managedThreadBridge{
		bridge: m.factory(bridgeConfig{
			ConversationID: conversationID,
			Agent:          thread.Agent,
			OutputTargets:  outputTargets,
		}),
	}
	if err := managed.bridge.Start(context.Background()); err != nil {
		return nil, false, fmt.Errorf("start text thread bridge: %w", err)
	}

	m.bridges[conversationID] = managed

	return managed, true, nil
}

func (m *threadBridgeManager) finishSummarizeThread(conversationID string, managed *managedThreadBridge) error {
	var errDrain error

	for {
		m.mu.Lock()
		queuedReplies := managed.queuedReplies

		managed.queuedReplies = nil
		if len(queuedReplies) == 0 {
			managed.summarizing = false
			m.mu.Unlock()

			return errDrain
		}
		m.mu.Unlock()

		for i := range queuedReplies {
			queuedReplies[i].ConversationID = conversationID
			errDrain = errors.Join(errDrain, managed.bridge.Submit(context.Background(), queuedReplies[i]))
		}
	}
}
