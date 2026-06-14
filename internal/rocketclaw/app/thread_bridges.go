package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	SeedThreadFromMain(ctx context.Context) error
	SeedThreadFromCron(ctx context.Context, seedText string) error
	SeedResponseThread(ctx context.Context, checkpoint events.ResponseCheckpoint, checkpointKey string) error
	Summarize(ctx context.Context, prompt string) (string, error)
	WaitIdle(ctx context.Context) error
	InterruptActiveTurn() *events.InboundMessage
}

type bridgeConfig struct {
	ConversationID string
	Agent          string
	OutputTargets  []events.OutputTarget
}

type bridgeFactory func(bridgeConfig) directBridge

type managedThreadBridge struct {
	bridge        directBridge
	summarizing   bool
	queuedReplies []*events.InboundMessage
}

const textThreadSummaryPrompt = "Summarize the current state of this managed text thread for handoff to the main session. Keep it concise. Include the user's goal, the important facts, decisions already made, open questions, and the next useful follow-up. Return only the summary text."

type primaryTextBinding struct {
	label         string
	outputTargets []events.OutputTarget
	discord       bool
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
	return &threadBridgeManager{log: logger.With("component", "thread_bridges"), runtime: runtime, store: store, bus: bus, factory: factory, targets: events.MainOutputTargets(), text: primaryTextBindingFor(runtime), mu: sync.Mutex{}, bridges: map[string]*managedThreadBridge{}}
}

func primaryTextBindingFor(runtime *config.Config) primaryTextBinding {
	if runtime != nil && runtime.DiscordText.Enabled && !runtime.Slack.Enabled {
		return primaryTextBinding{label: "Discord", outputTargets: []events.OutputTarget{events.OutputTargetDiscordText}, discord: true}
	}

	return primaryTextBinding{label: "Slack", outputTargets: []events.OutputTarget{events.OutputTargetSlackMain}}
}

func (b primaryTextBinding) conversationID(target events.TextConversationTarget) string {
	if b.discord {
		return harnessbridge.DiscordThreadConversationID(strings.TrimSpace(target.ThreadID))
	}

	return harnessbridge.SlackThreadConversationID(strings.TrimSpace(target.ChannelID), strings.TrimSpace(target.ThreadID))
}

func (b primaryTextBinding) checkpointKey(target events.TextConversationTarget) string {
	if b.discord {
		return harnessbridge.DiscordResponseCheckpointKey(target.ChannelID, target.MessageID)
	}

	return harnessbridge.SlackResponseCheckpointKey(target.ChannelID, target.MessageID)
}

func (b primaryTextBinding) targetForConversationID(conversationID string) (events.TextConversationTarget, bool) {
	if b.discord {
		threadID, ok := harnessbridge.DiscordThreadTarget(conversationID)
		return events.TextConversationTarget{ChannelID: threadID, ThreadID: threadID}, ok
	}

	channelID, threadTS, ok := harnessbridge.SlackThreadTarget(conversationID)

	return events.TextConversationTarget{ChannelID: channelID, MessageID: threadTS, ThreadID: threadTS}, ok
}

func (b primaryTextBinding) setReplyThread(inbound *events.InboundMessage, target events.TextConversationTarget) {
	if b.discord && inbound.DiscordReply != nil {
		inbound.DiscordReply.ThreadID = strings.TrimSpace(target.ThreadID)
	} else if !b.discord && inbound.SlackReply != nil {
		inbound.SlackReply.ThreadTS = strings.TrimSpace(target.ThreadID)
	}
}

func (b primaryTextBinding) setContinuationReply(inbound *events.InboundMessage, target events.TextConversationTarget) {
	if b.discord {
		inbound.DiscordReply = &events.DiscordReplyTarget{ChannelID: target.ThreadID, ThreadID: target.ThreadID}
	} else {
		inbound.SlackReply = &events.SlackReplyTarget{ChannelID: target.ChannelID, MessageTS: target.MessageID, ThreadTS: target.ThreadID}
	}
}

func (b primaryTextBinding) markerReply(marker *events.InboundMessage) *events.InboundMessage {
	if marker == nil || b.discord && marker.DiscordReply == nil || !b.discord && marker.SlackReply == nil {
		return nil
	}

	return marker
}

func (b primaryTextBinding) publishSummary(ctx context.Context, bus *events.Bus, log *slog.Logger, target events.TextConversationTarget, summary string) error {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return errors.New("thread summary is empty")
	}

	label := strings.ToLower(b.label)
	metadata := label + "_thread_summary thread=" + strings.TrimSpace(target.ThreadID)

	body := b.label + " thread summary from thread " + strings.TrimSpace(target.ThreadID) + ":\n\n" + summary
	if !b.discord {
		metadata = "slack_thread_summary channel=" + strings.TrimSpace(target.ChannelID) + " thread=" + strings.TrimSpace(target.ThreadID)
		body = "Slack thread summary from channel " + strings.TrimSpace(target.ChannelID) + " thread " + strings.TrimSpace(target.ThreadID) + ":\n\n" + summary
	}

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
		errStop = errors.Join(errStop, bridges[i].Stop())
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
	for i := range bridges {
		errWait = errors.Join(errWait, bridges[i].WaitIdle(ctx))
	}

	return errWait
}

func (m *threadBridgeManager) StartPendingScheduledMessages() error {
	state, err := m.store.Load()
	if err != nil {
		return fmt.Errorf("load pending scheduled message bridges: %w", err)
	}

	for _, message := range state.ScheduledMessages {
		conversationID := strings.TrimSpace(message.ConversationID)
		if conversationID == events.MainConversationID() {
			continue
		}

		outputTargets := []events.OutputTarget{events.OutputTargetSlackMain}
		if strings.HasPrefix(conversationID, "discord-thread:") {
			outputTargets = []events.OutputTarget{events.OutputTargetDiscordText}
		}

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
	state, err := m.store.Load()
	if err != nil {
		return fmt.Errorf("load active goal bridges: %w", err)
	}

	for conversationID := range state.Goals {
		goal := state.Goals[conversationID]
		if strings.TrimSpace(goal.Status) != harnessbridge.GoalStatusActive {
			continue
		}

		thread := state.Threads[conversationID]

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

	state, err := m.store.Load()
	if err != nil {
		return false, fmt.Errorf("load persisted %s thread state: %w", m.text.label, err)
	}

	thread, ok := state.Threads[conversationID]
	if !ok {
		return false, nil
	}

	if strings.HasPrefix(thread.SeededFromResponse, "external_mcp:") {
		m.text.setReplyThread(inbound, target)

		if err := m.SubmitExternalMCP(ctx, thread.Agent, thread.SeededFromResponse, inbound); err != nil {
			return true, fmt.Errorf("submit external MCP %s thread reply: %w", m.text.label, err)
		}

		return true, nil
	}

	m.text.setReplyThread(inbound, target)

	return m.submitManagedThreadReply(ctx, conversationID, thread, inbound, m.text.outputTargets, m.text.label)
}

func (m *threadBridgeManager) RecordResponseCheckpoint(target events.TextConversationTarget, checkpoint events.ResponseCheckpoint) error {
	return m.recordResponseCheckpoint(m.text.checkpointKey(target), checkpoint, m.text.label)
}

func (m *threadBridgeManager) PrepareResponseThreadReply(target events.TextConversationTarget) (bool, error) {
	return m.prepareResponseThreadReply(m.text.checkpointKey(target), m.text.label)
}

//nolint:funcorder // Kept near the public checkpoint methods it supports.
func (m *threadBridgeManager) recordResponseCheckpoint(key string, checkpoint events.ResponseCheckpoint, label string) error {
	if key == "" {
		return nil
	}

	if err := m.store.UpsertResponseCheckpoint(key, harnessbridge.ResponseCheckpointState{ConversationID: checkpoint.ConversationID, SessionEntryID: checkpoint.SessionEntryID, ResponseID: checkpoint.ResponseID, Model: checkpoint.Model, AssistantText: checkpoint.AssistantText}); err != nil {
		return fmt.Errorf("persist %s response checkpoint: %w", label, err)
	}

	return nil
}

//nolint:funcorder // Kept near the public checkpoint methods it supports.
func (m *threadBridgeManager) prepareResponseThreadReply(key, label string) (bool, error) {
	if key == "" {
		return false, nil
	}

	state, err := m.store.Load()
	if err != nil {
		return false, fmt.Errorf("load persisted %s response checkpoint: %w", label, err)
	}

	_, ok := state.ResponseCheckpoints[key]

	return ok, nil
}

func (m *threadBridgeManager) SubmitResponseThreadReply(ctx context.Context, target events.TextConversationTarget, inbound *events.InboundMessage) (bool, error) {
	conversationID := m.text.conversationID(target)
	m.text.setReplyThread(inbound, target)

	return m.submitResponseThreadReply(ctx, conversationID, m.text.checkpointKey(target), inbound, m.text.outputTargets, m.text.label)
}

func (m *threadBridgeManager) SummarizeThread(ctx context.Context, target events.TextConversationTarget) (bool, error) {
	conversationID := m.text.conversationID(target)
	if conversationID == "" {
		return false, nil
	}

	return m.summarizeThread(ctx, conversationID, m.text.outputTargets, m.text.label, func(summary string) error {
		return m.text.publishSummary(ctx, m.bus, m.log, target, summary)
	})
}

//nolint:funcorder // Kept near the public summary method it supports.
func (m *threadBridgeManager) summarizeThread(ctx context.Context, conversationID string, outputTargets []events.OutputTarget, label string, publish func(string) error) (bool, error) {
	m.mu.Lock()
	managed := m.bridges[conversationID]
	m.mu.Unlock()

	if managed == nil {
		state, err := m.store.Load()
		if err != nil {
			return false, fmt.Errorf("load persisted %s thread state: %w", label, err)
		}

		thread, ok := state.Threads[conversationID]
		if !ok {
			return false, nil
		}

		managed, _, err = m.ensureThreadBridge(conversationID, thread, outputTargets)
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
		errPublish = publish(summary)
	}

	errDrain := m.finishSummarizeThread(conversationID, managed)

	return true, errors.Join(errSummarize, errPublish, errDrain)
}

func (m *threadBridgeManager) StartThread(ctx context.Context, agent string, preSeed bool, target events.TextConversationTarget, inbound *events.InboundMessage) error {
	conversationID := m.text.conversationID(target)
	if conversationID == "" {
		return fmt.Errorf("%s thread target is required", strings.ToLower(m.text.label))
	}

	return m.startManagedThread(ctx, conversationID, agent, preSeed, inbound, m.text.outputTargets, m.text.label)
}

//nolint:funcorder // Kept near the public thread-start method it supports.
func (m *threadBridgeManager) startManagedThread(ctx context.Context, conversationID, agent string, preSeed bool, inbound *events.InboundMessage, outputTargets []events.OutputTarget, label string) error {
	managed, created, err := m.ensureThreadBridge(conversationID, harnessbridge.ThreadState{Agent: strings.TrimSpace(agent)}, outputTargets)
	if err != nil {
		return err
	}

	if created && preSeed {
		if err := managed.bridge.SeedThreadFromMain(ctx); err != nil {
			m.dropCreatedBridge(conversationID, managed)
			return fmt.Errorf("seed %s thread from main session: %w", label, err)
		}
	}

	if created {
		if err := m.store.UpsertThread(conversationID, agent); err != nil {
			m.dropCreatedBridge(conversationID, managed)
			return fmt.Errorf("persist %s thread bridge: %w", label, err)
		}
	}

	inbound.ConversationID = conversationID
	if err := managed.bridge.Submit(ctx, inbound); err != nil {
		return fmt.Errorf("submit %s thread start: %w", label, err)
	}

	return nil
}

//nolint:funcorder // Kept near start helpers that need rollback.
func (m *threadBridgeManager) dropCreatedBridge(conversationID string, managed *managedThreadBridge) {
	m.mu.Lock()
	delete(m.bridges, conversationID)
	m.mu.Unlock()

	_ = managed.bridge.Stop()
}

func (m *threadBridgeManager) StartGoalInThread(ctx context.Context, agent, objective, checkScript string, maxTurns int, target events.TextConversationTarget, inbound *events.InboundMessage) error {
	conversationID := m.text.conversationID(target)
	if conversationID == "" {
		return fmt.Errorf("%s thread target is required", strings.ToLower(m.text.label))
	}

	return m.startGoalInConversation(ctx, conversationID, agent, objective, checkScript, maxTurns, inbound, m.text.outputTargets)
}

//nolint:funcorder // Kept near the public goal-start method it supports.
func (m *threadBridgeManager) startGoalInConversation(ctx context.Context, conversationID, agent, objective, checkScript string, maxTurns int, inbound *events.InboundMessage, outputTargets []events.OutputTarget) error {
	state, err := m.store.Load()
	if err != nil {
		return fmt.Errorf("load goal thread state: %w", err)
	}

	if storedAgent := strings.TrimSpace(state.Threads[conversationID].Agent); storedAgent != "" {
		agent = storedAgent
	}

	if strings.TrimSpace(checkScript) != "" {
		if err := harnessbridge.ValidateGoalCheckScriptStart(m.runtime, agent, checkScript); err != nil {
			return fmt.Errorf("validate goal check script: %w", err)
		}
	}

	managed, created, err := m.ensureThreadBridge(conversationID, harnessbridge.ThreadState{Agent: strings.TrimSpace(agent), SeededFromResponse: ""}, outputTargets)
	if err != nil {
		return err
	}

	if created {
		if err := m.store.UpsertThread(conversationID, agent); err != nil {
			m.dropCreatedBridge(conversationID, managed)
			return fmt.Errorf("persist goal thread bridge: %w", err)
		}
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

	marker, err := m.interruptConversation(conversationID)
	if err != nil {
		return nil, err
	}

	return m.text.markerReply(marker), nil
}

//nolint:funcorder // Kept near the public interrupt method it supports.
func (m *threadBridgeManager) interruptConversation(conversationID string) (*events.InboundMessage, error) {
	if conversationID == "" {
		return nil, nil
	}

	if err := m.store.StopGoal(conversationID); err != nil {
		return nil, fmt.Errorf("stop goal thread: %w", err)
	}

	m.mu.Lock()

	managed := m.bridges[conversationID]
	if managed != nil {
		managed.queuedReplies = nil
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

	managed, created, err := m.ensureThreadBridge(conversationID, harnessbridge.ThreadState{Agent: strings.TrimSpace(agent)}, m.text.outputTargets)
	if err != nil {
		return err
	}

	if created {
		if err := managed.bridge.SeedThreadFromCron(ctx, seedText); err != nil {
			m.dropCreatedBridge(conversationID, managed)
			return fmt.Errorf("seed text cron thread: %w", err)
		}

		if err := m.store.UpsertThread(conversationID, agent); err != nil {
			m.dropCreatedBridge(conversationID, managed)
			return fmt.Errorf("persist text cron thread bridge: %w", err)
		}
	}

	return nil
}

//nolint:funcorder // Kept near the public reply method it supports.
func (m *threadBridgeManager) submitManagedThreadReply(ctx context.Context, conversationID string, thread harnessbridge.ThreadState, inbound *events.InboundMessage, outputTargets []events.OutputTarget, label string) (bool, error) {
	managed, _, err := m.ensureThreadBridge(conversationID, thread, outputTargets)
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
		return true, fmt.Errorf("submit %s thread reply: %w", label, err)
	}

	return true, nil
}

//nolint:funcorder // Kept near the public response-thread reply method it supports.
func (m *threadBridgeManager) submitResponseThreadReply(ctx context.Context, conversationID, checkpointKey string, inbound *events.InboundMessage, outputTargets []events.OutputTarget, label string) (bool, error) {
	if conversationID == "" || checkpointKey == "" {
		return false, nil
	}

	state, err := m.store.Load()
	if err != nil {
		return false, fmt.Errorf("load persisted %s response checkpoint: %w", label, err)
	}

	checkpoint, ok := state.ResponseCheckpoints[checkpointKey]
	if !ok {
		return false, nil
	}

	managed, _, err := m.ensureThreadBridge(conversationID, harnessbridge.ThreadState{Agent: "main", SeededFromResponse: strings.TrimSpace(state.Threads[conversationID].SeededFromResponse)}, outputTargets)
	if err != nil {
		return true, err
	}

	seededFrom := strings.TrimSpace(state.Threads[conversationID].SeededFromResponse)
	if seededFrom != checkpointKey {
		if seededFrom != "" {
			return true, fmt.Errorf("%s thread already seeded from %s", strings.ToLower(label), seededFrom)
		}

		if err := managed.bridge.SeedResponseThread(ctx, events.ResponseCheckpoint{ConversationID: checkpoint.ConversationID, SessionEntryID: checkpoint.SessionEntryID, ResponseID: checkpoint.ResponseID, Model: checkpoint.Model, AssistantText: checkpoint.AssistantText}, checkpointKey); err != nil {
			return true, fmt.Errorf("seed %s response-rooted thread: %w", label, err)
		}

		if err := m.store.MarkThreadSeeded(conversationID, checkpointKey); err != nil {
			return true, fmt.Errorf("persist %s response-rooted thread seed: %w", label, err)
		}
	}

	inbound.ConversationID = conversationID

	if err := managed.bridge.Submit(ctx, inbound); err != nil {
		return true, fmt.Errorf("submit %s response-rooted thread reply: %w", label, err)
	}

	return true, nil
}

func (m *threadBridgeManager) PrepareThreadReply(target events.TextConversationTarget) (bool, error) {
	conversationID := m.text.conversationID(target)
	if conversationID == "" {
		return false, nil
	}

	state, err := m.store.Load()
	if err != nil {
		return false, fmt.Errorf("load persisted %s thread state: %w", m.text.label, err)
	}

	return strings.TrimSpace(state.Threads[conversationID].Agent) != "", nil
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

func (m *threadBridgeManager) bridgesSnapshot() []directBridge {
	m.mu.Lock()
	defer m.mu.Unlock()

	bridges := make([]directBridge, 0, len(m.bridges))
	for _, managed := range m.bridges {
		bridges = append(bridges, managed.bridge)
	}

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
