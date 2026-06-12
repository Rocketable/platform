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
	Start(context.Context) error
	Stop() error
	Submit(context.Context, *events.InboundMessage) error
	SeedThreadFromMain(context.Context) error
	SeedThreadFromCron(context.Context, string) error
	SeedResponseThread(context.Context, events.ResponseCheckpoint, string) error
	Summarize(context.Context, string) (string, error)
	WaitIdle(context.Context) error
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

const slackThreadSummaryPrompt = "Summarize the current state of this managed Slack thread for handoff to the main session. Keep it concise. Include the user's goal, the important facts, decisions already made, open questions, and the next useful follow-up. Return only the summary text."

type threadBridgeManager struct {
	log     *slog.Logger
	runtime *config.Config
	store   *harnessbridge.SessionService
	bus     *events.Bus
	factory bridgeFactory
	targets []events.OutputTarget

	mu       sync.Mutex
	draining bool
	bridges  map[string]*managedThreadBridge
}

func newThreadBridgeManager(bus *events.Bus, runtime *config.Config, store *harnessbridge.SessionService, logger *slog.Logger, factory bridgeFactory) *threadBridgeManager {
	return &threadBridgeManager{log: logger.With("component", "thread_bridges"), runtime: runtime, store: store, bus: bus, factory: factory, targets: events.MainOutputTargets(), mu: sync.Mutex{}, bridges: map[string]*managedThreadBridge{}}
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

		channelID, threadTS, ok := harnessbridge.SlackThreadTarget(conversationID)
		if !ok {
			continue
		}

		thread := state.Threads[conversationID]

		managed, _, err := m.ensureThreadBridge(conversationID, thread, []events.OutputTarget{events.OutputTargetSlackMain})
		if err != nil {
			return fmt.Errorf("start active goal bridge: %w", err)
		}

		inbound := events.NewMainInboundMessage(events.SourceSystem, events.InboundKindPrompt, "goal_continuation", "Continue the active goal loop.", false)
		inbound.ConversationID = conversationID

		inbound.SlackReply = &events.SlackReplyTarget{ChannelID: channelID, MessageTS: threadTS, ThreadTS: threadTS}
		if err := managed.bridge.Submit(context.Background(), inbound); err != nil {
			return fmt.Errorf("submit active goal continuation: %w", err)
		}
	}

	return nil
}

func (m *threadBridgeManager) StartDiscordThread(ctx context.Context, agent string, preSeed bool, inbound *events.InboundMessage) error {
	if inbound == nil || inbound.DiscordReply == nil {
		return errors.New("discord reply target is required")
	}

	conversationID := harnessbridge.DiscordThreadConversationID(inbound.DiscordReply.ThreadID)
	if conversationID == "" {
		return errors.New("discord thread target is required")
	}

	managed, created, err := m.ensureThreadBridge(conversationID, harnessbridge.ThreadState{Agent: strings.TrimSpace(agent)}, []events.OutputTarget{events.OutputTargetDiscordText})
	if err != nil {
		return err
	}

	if created && preSeed {
		if err := managed.bridge.SeedThreadFromMain(ctx); err != nil {
			m.mu.Lock()
			delete(m.bridges, conversationID)
			m.mu.Unlock()

			_ = managed.bridge.Stop()

			return fmt.Errorf("seed Discord thread from main session: %w", err)
		}
	}

	if created {
		if err := m.store.UpsertThread(conversationID, agent); err != nil {
			m.mu.Lock()
			delete(m.bridges, conversationID)
			m.mu.Unlock()

			_ = managed.bridge.Stop()

			return fmt.Errorf("persist Discord thread bridge: %w", err)
		}
	}

	inbound.ConversationID = conversationID
	if err := managed.bridge.Submit(ctx, inbound); err != nil {
		return fmt.Errorf("submit Discord thread start: %w", err)
	}

	return nil
}

func (m *threadBridgeManager) PrepareDiscordThreadReply(_ context.Context, threadID string) (bool, error) {
	conversationID := harnessbridge.DiscordThreadConversationID(threadID)
	if conversationID == "" {
		return false, nil
	}

	state, err := m.store.Load()
	if err != nil {
		return false, fmt.Errorf("load persisted Discord thread state: %w", err)
	}

	return strings.TrimSpace(state.Threads[conversationID].Agent) != "", nil
}

func (m *threadBridgeManager) SubmitDiscordThreadReply(ctx context.Context, threadID string, inbound *events.InboundMessage) (bool, error) {
	conversationID := harnessbridge.DiscordThreadConversationID(threadID)
	if conversationID == "" {
		return false, nil
	}

	state, err := m.store.Load()
	if err != nil {
		return false, fmt.Errorf("load persisted Discord thread state: %w", err)
	}

	thread, ok := state.Threads[conversationID]
	if !ok {
		return false, nil
	}

	managed, _, err := m.ensureThreadBridge(conversationID, thread, []events.OutputTarget{events.OutputTargetDiscordText})
	if err != nil {
		return false, err
	}

	inbound.ConversationID = conversationID
	if inbound.DiscordReply != nil {
		inbound.DiscordReply.ThreadID = strings.TrimSpace(threadID)
	}

	m.mu.Lock()
	if managed.summarizing {
		managed.queuedReplies = append(managed.queuedReplies, inbound)
		m.mu.Unlock()

		return true, nil
	}

	bridge := managed.bridge
	m.mu.Unlock()

	if err := bridge.Submit(ctx, inbound); err != nil {
		return true, fmt.Errorf("submit Discord thread reply: %w", err)
	}

	return true, nil
}

func (m *threadBridgeManager) RecordDiscordResponseCheckpoint(_ context.Context, channelID, messageID string, checkpoint events.ResponseCheckpoint) error {
	key := harnessbridge.DiscordResponseCheckpointKey(channelID, messageID)
	if key == "" {
		return nil
	}

	if err := m.store.UpsertResponseCheckpoint(key, harnessbridge.ResponseCheckpointState{ConversationID: checkpoint.ConversationID, SessionEntryID: checkpoint.SessionEntryID, ResponseID: checkpoint.ResponseID, Model: checkpoint.Model, AssistantText: checkpoint.AssistantText}); err != nil {
		return fmt.Errorf("persist Discord response checkpoint: %w", err)
	}

	return nil
}

func (m *threadBridgeManager) PrepareDiscordResponseThreadReply(_ context.Context, channelID, messageID string) (bool, error) {
	key := harnessbridge.DiscordResponseCheckpointKey(channelID, messageID)
	if key == "" {
		return false, nil
	}

	state, err := m.store.Load()
	if err != nil {
		return false, fmt.Errorf("load persisted Discord response checkpoint: %w", err)
	}

	_, ok := state.ResponseCheckpoints[key]

	return ok, nil
}

func (m *threadBridgeManager) SubmitDiscordResponseThreadReply(ctx context.Context, channelID, messageID, threadID string, inbound *events.InboundMessage) (bool, error) {
	conversationID := harnessbridge.DiscordThreadConversationID(threadID)

	checkpointKey := harnessbridge.DiscordResponseCheckpointKey(channelID, messageID)
	if conversationID == "" || checkpointKey == "" {
		return false, nil
	}

	state, err := m.store.Load()
	if err != nil {
		return false, fmt.Errorf("load persisted Discord response checkpoint: %w", err)
	}

	checkpoint, ok := state.ResponseCheckpoints[checkpointKey]
	if !ok {
		return false, nil
	}

	managed, _, err := m.ensureThreadBridge(conversationID, harnessbridge.ThreadState{Agent: "main", SeededFromResponse: strings.TrimSpace(state.Threads[conversationID].SeededFromResponse)}, []events.OutputTarget{events.OutputTargetDiscordText})
	if err != nil {
		return true, err
	}

	seededFrom := strings.TrimSpace(state.Threads[conversationID].SeededFromResponse)
	if seededFrom != checkpointKey {
		if seededFrom != "" {
			return true, fmt.Errorf("discord thread already seeded from %s", seededFrom)
		}

		if err := managed.bridge.SeedResponseThread(ctx, events.ResponseCheckpoint{ConversationID: checkpoint.ConversationID, SessionEntryID: checkpoint.SessionEntryID, ResponseID: checkpoint.ResponseID, Model: checkpoint.Model, AssistantText: checkpoint.AssistantText}, checkpointKey); err != nil {
			return true, fmt.Errorf("seed Discord response-rooted thread: %w", err)
		}

		if err := m.store.MarkThreadSeeded(conversationID, checkpointKey); err != nil {
			return true, fmt.Errorf("persist Discord response-rooted thread seed: %w", err)
		}
	}

	inbound.ConversationID = conversationID
	if inbound.DiscordReply != nil {
		inbound.DiscordReply.ThreadID = strings.TrimSpace(threadID)
	}

	if err := managed.bridge.Submit(ctx, inbound); err != nil {
		return true, fmt.Errorf("submit Discord response-rooted thread reply: %w", err)
	}

	return true, nil
}

func (m *threadBridgeManager) SummarizeDiscordThread(ctx context.Context, threadID string) (bool, error) {
	conversationID := harnessbridge.DiscordThreadConversationID(threadID)
	if conversationID == "" {
		return false, nil
	}

	m.mu.Lock()
	managed := m.bridges[conversationID]
	m.mu.Unlock()

	if managed == nil {
		state, err := m.store.Load()
		if err != nil {
			return false, fmt.Errorf("load persisted Discord thread state: %w", err)
		}

		thread, ok := state.Threads[conversationID]
		if !ok {
			return false, nil
		}

		managed, _, err = m.ensureThreadBridge(conversationID, thread, []events.OutputTarget{events.OutputTargetDiscordText})
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

	summary, errSummarize := bridge.Summarize(ctx, slackThreadSummaryPrompt)

	var errPublish error
	if errSummarize == nil {
		errPublish = m.publishDiscordThreadSummary(ctx, threadID, summary)
	}

	errDrain := m.finishSummarizeThread(conversationID, managed)

	return true, errors.Join(errSummarize, errPublish, errDrain)
}

func (m *threadBridgeManager) StartThread(ctx context.Context, agent string, preSeed bool, inbound *events.InboundMessage) error {
	if inbound == nil || inbound.SlackReply == nil {
		return errors.New("slack reply target is required")
	}

	conversationID := harnessbridge.SlackThreadConversationID(strings.TrimSpace(inbound.SlackReply.ChannelID), strings.TrimSpace(inbound.SlackReply.ThreadTS))
	if conversationID == "" {
		return errors.New("slack thread target is required")
	}

	managed, created, err := m.ensureThreadBridge(conversationID, harnessbridge.ThreadState{Agent: strings.TrimSpace(agent), SeededFromResponse: ""}, []events.OutputTarget{events.OutputTargetSlackMain})
	if err != nil {
		return err
	}

	if created && preSeed {
		if err := managed.bridge.SeedThreadFromMain(ctx); err != nil {
			m.mu.Lock()
			delete(m.bridges, conversationID)
			m.mu.Unlock()

			_ = managed.bridge.Stop()

			return fmt.Errorf("seed Slack thread from main session: %w", err)
		}
	}

	if created {
		if err := m.store.UpsertThread(conversationID, agent); err != nil {
			m.mu.Lock()
			delete(m.bridges, conversationID)
			m.mu.Unlock()

			_ = managed.bridge.Stop()

			return fmt.Errorf("persist Slack thread bridge: %w", err)
		}
	}

	inbound.ConversationID = conversationID
	if err := managed.bridge.Submit(ctx, inbound); err != nil {
		return fmt.Errorf("submit Slack thread start: %w", err)
	}

	return nil
}

func (m *threadBridgeManager) StartGoalThread(ctx context.Context, agent, objective, checkScript string, maxTurns int, inbound *events.InboundMessage) error {
	if inbound == nil || inbound.SlackReply == nil {
		return errors.New("slack reply target is required")
	}

	conversationID := harnessbridge.SlackThreadConversationID(strings.TrimSpace(inbound.SlackReply.ChannelID), strings.TrimSpace(inbound.SlackReply.ThreadTS))
	if conversationID == "" {
		return errors.New("slack thread target is required")
	}

	if strings.TrimSpace(checkScript) != "" {
		if err := harnessbridge.ValidateGoalCheckScriptStart(m.runtime, agent, checkScript); err != nil {
			return fmt.Errorf("validate Slack goal check script: %w", err)
		}
	}

	managed, created, err := m.ensureThreadBridge(conversationID, harnessbridge.ThreadState{Agent: strings.TrimSpace(agent), SeededFromResponse: ""}, []events.OutputTarget{events.OutputTargetSlackMain})
	if err != nil {
		return err
	}

	if created {
		if err := m.store.UpsertThread(conversationID, agent); err != nil {
			m.mu.Lock()
			delete(m.bridges, conversationID)
			m.mu.Unlock()

			_ = managed.bridge.Stop()

			return fmt.Errorf("persist Slack goal thread bridge: %w", err)
		}
	}

	if err := m.store.BeginGoal(conversationID, objective, checkScript, maxTurns); err != nil {
		return fmt.Errorf("persist Slack goal: %w", err)
	}

	inbound.Label = "goal"

	inbound.ConversationID = conversationID
	if err := managed.bridge.Submit(ctx, inbound); err != nil {
		return fmt.Errorf("submit Slack goal thread start: %w", err)
	}

	return nil
}

func (m *threadBridgeManager) StopGoalThread(_ context.Context, channelID, threadTS string) (bool, error) {
	conversationID := harnessbridge.SlackThreadConversationID(channelID, threadTS)
	if conversationID == "" {
		return false, nil
	}

	goal, stopped, err := m.store.StopGoal(conversationID)
	if err != nil {
		return false, fmt.Errorf("stop Slack goal thread: %w", err)
	}

	return stopped && strings.TrimSpace(goal.Status) == harnessbridge.GoalStatusStopped, nil
}

func (m *threadBridgeManager) RegisterCronThread(ctx context.Context, channelID, threadTS, agent, seedText string) error {
	conversationID := harnessbridge.SlackThreadConversationID(channelID, threadTS)
	if conversationID == "" {
		return errors.New("slack thread target is required")
	}

	managed, created, err := m.ensureThreadBridge(conversationID, harnessbridge.ThreadState{Agent: strings.TrimSpace(agent)}, []events.OutputTarget{events.OutputTargetSlackMain})
	if err != nil {
		return err
	}

	if created {
		if err := managed.bridge.SeedThreadFromCron(ctx, seedText); err != nil {
			m.mu.Lock()
			delete(m.bridges, conversationID)
			m.mu.Unlock()

			_ = managed.bridge.Stop()

			return fmt.Errorf("seed Slack cron thread: %w", err)
		}

		if err := m.store.UpsertThread(conversationID, agent); err != nil {
			m.mu.Lock()
			delete(m.bridges, conversationID)
			m.mu.Unlock()

			_ = managed.bridge.Stop()

			return fmt.Errorf("persist Slack cron thread bridge: %w", err)
		}
	}

	return nil
}

func (m *threadBridgeManager) SubmitThreadReply(ctx context.Context, channelID, threadTS string, inbound *events.InboundMessage) (bool, error) {
	conversationID := harnessbridge.SlackThreadConversationID(channelID, threadTS)
	if conversationID == "" {
		return false, nil
	}

	state, err := m.store.Load()
	if err != nil {
		return false, fmt.Errorf("load persisted Slack thread state: %w", err)
	}

	thread, ok := state.Threads[conversationID]
	if !ok {
		return false, nil
	}

	if strings.HasPrefix(thread.SeededFromResponse, "external_mcp:") {
		if inbound.SlackReply != nil {
			inbound.SlackReply.ThreadTS = strings.TrimSpace(threadTS)
		}

		if err := m.SubmitExternalMCP(ctx, thread.Agent, thread.SeededFromResponse, inbound); err != nil {
			return true, fmt.Errorf("submit external MCP Slack thread reply: %w", err)
		}

		return true, nil
	}

	managed, _, err := m.ensureThreadBridge(conversationID, thread, []events.OutputTarget{events.OutputTargetSlackMain})
	if err != nil {
		return false, err
	}

	inbound.ConversationID = conversationID
	if inbound.SlackReply != nil {
		inbound.SlackReply.ThreadTS = strings.TrimSpace(threadTS)
	}

	m.mu.Lock()
	if managed.summarizing {
		managed.queuedReplies = append(managed.queuedReplies, inbound)
		m.mu.Unlock()

		return true, nil
	}

	bridge := managed.bridge
	m.mu.Unlock()

	if err := bridge.Submit(ctx, inbound); err != nil {
		return true, fmt.Errorf("submit Slack thread reply: %w", err)
	}

	messageTS := ""
	if inbound.SlackReply != nil {
		messageTS = inbound.SlackReply.MessageTS
	}

	m.log.Info("submitted Slack thread reply to bridge", "conversation_id", conversationID, "channel", strings.TrimSpace(channelID), "thread_ts", strings.TrimSpace(threadTS), "message_ts", strings.TrimSpace(messageTS), "text_len", len([]rune(inbound.Text)), "seeded_from_response", strings.TrimSpace(thread.SeededFromResponse), "agent", strings.TrimSpace(thread.Agent))

	return true, nil
}

func (m *threadBridgeManager) PrepareResponseThreadReply(_ context.Context, channelID, threadTS string) (bool, error) {
	key := harnessbridge.SlackResponseCheckpointKey(channelID, threadTS)
	if key == "" {
		return false, nil
	}

	state, err := m.store.Load()
	if err != nil {
		return false, fmt.Errorf("load persisted Slack response checkpoint: %w", err)
	}

	_, ok := state.ResponseCheckpoints[key]

	return ok, nil
}

func (m *threadBridgeManager) SubmitResponseThreadReply(ctx context.Context, channelID, threadTS string, inbound *events.InboundMessage) (bool, error) {
	conversationID := harnessbridge.SlackThreadConversationID(channelID, threadTS)

	checkpointKey := harnessbridge.SlackResponseCheckpointKey(channelID, threadTS)
	if conversationID == "" || checkpointKey == "" {
		return false, nil
	}

	state, err := m.store.Load()
	if err != nil {
		return false, fmt.Errorf("load persisted Slack response checkpoint: %w", err)
	}

	checkpoint, ok := state.ResponseCheckpoints[checkpointKey]
	if !ok {
		return false, nil
	}

	managed, _, err := m.ensureThreadBridge(conversationID, harnessbridge.ThreadState{Agent: "main", SeededFromResponse: strings.TrimSpace(state.Threads[conversationID].SeededFromResponse)}, []events.OutputTarget{events.OutputTargetSlackMain})
	if err != nil {
		return true, err
	}

	seededFrom := strings.TrimSpace(state.Threads[conversationID].SeededFromResponse)
	if seededFrom != checkpointKey {
		if seededFrom != "" {
			return true, fmt.Errorf("slack thread already seeded from %s", seededFrom)
		}

		if err := managed.bridge.SeedResponseThread(ctx, events.ResponseCheckpoint{
			ConversationID: checkpoint.ConversationID,
			SessionEntryID: checkpoint.SessionEntryID,
			ResponseID:     checkpoint.ResponseID,
			Model:          checkpoint.Model,
			AssistantText:  checkpoint.AssistantText,
		}, checkpointKey); err != nil {
			return true, fmt.Errorf("seed Slack response-rooted thread: %w", err)
		}

		if err := m.store.MarkThreadSeeded(conversationID, checkpointKey); err != nil {
			return true, fmt.Errorf("persist Slack response-rooted thread seed: %w", err)
		}
	}

	inbound.ConversationID = conversationID
	if inbound.SlackReply != nil {
		inbound.SlackReply.ThreadTS = strings.TrimSpace(threadTS)
	}

	if err := managed.bridge.Submit(ctx, inbound); err != nil {
		return true, fmt.Errorf("submit Slack response-rooted thread reply: %w", err)
	}

	messageTS := ""
	if inbound.SlackReply != nil {
		messageTS = inbound.SlackReply.MessageTS
	}

	m.log.Info("submitted Slack response-rooted thread reply to bridge", "conversation_id", conversationID, "channel", strings.TrimSpace(channelID), "thread_ts", strings.TrimSpace(threadTS), "message_ts", strings.TrimSpace(messageTS), "text_len", len([]rune(inbound.Text)), "seeded_from_response", checkpointKey, "agent", strings.TrimSpace(state.Threads[conversationID].Agent))

	return true, nil
}

func (m *threadBridgeManager) SummarizeThread(ctx context.Context, channelID, threadTS string) (bool, error) {
	conversationID := harnessbridge.SlackThreadConversationID(channelID, threadTS)
	if conversationID == "" {
		return false, nil
	}

	m.mu.Lock()
	managed := m.bridges[conversationID]
	m.mu.Unlock()

	if managed == nil {
		state, err := m.store.Load()
		if err != nil {
			return false, fmt.Errorf("load persisted Slack thread state: %w", err)
		}

		thread, ok := state.Threads[conversationID]
		if !ok {
			return false, nil
		}

		managed, _, err = m.ensureThreadBridge(conversationID, thread, []events.OutputTarget{events.OutputTargetSlackMain})
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

	summary, errSummarize := bridge.Summarize(ctx, slackThreadSummaryPrompt)

	var errPublish error
	if errSummarize == nil {
		errPublish = m.publishThreadSummary(ctx, channelID, threadTS, summary)
	}

	errDrain := m.finishSummarizeThread(conversationID, managed)

	return true, errors.Join(errSummarize, errPublish, errDrain)
}

func (m *threadBridgeManager) RecordResponseCheckpoint(_ context.Context, channelID, messageTS string, checkpoint events.ResponseCheckpoint) error {
	key := harnessbridge.SlackResponseCheckpointKey(channelID, messageTS)
	if key == "" {
		return nil
	}

	if err := m.store.UpsertResponseCheckpoint(key, harnessbridge.ResponseCheckpointState{
		ConversationID: checkpoint.ConversationID,
		SessionEntryID: checkpoint.SessionEntryID,
		ResponseID:     checkpoint.ResponseID,
		Model:          checkpoint.Model,
		AssistantText:  checkpoint.AssistantText,
	}); err != nil {
		return fmt.Errorf("persist Slack response checkpoint: %w", err)
	}

	return nil
}

func (m *threadBridgeManager) PrepareThreadReply(_ context.Context, channelID, threadTS string) (bool, error) {
	conversationID := harnessbridge.SlackThreadConversationID(channelID, threadTS)
	if conversationID == "" {
		return false, nil
	}

	state, err := m.store.Load()
	if err != nil {
		return false, fmt.Errorf("load persisted Slack thread state: %w", err)
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
		return nil, false, errors.New("slack thread conversation ID is required")
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
		return nil, false, fmt.Errorf("start Slack thread bridge: %w", err)
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

func (m *threadBridgeManager) publishThreadSummary(ctx context.Context, channelID, threadTS, summary string) error {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return errors.New("thread summary is empty")
	}

	inbound := events.NewMainInboundMessage(
		events.SourceSystem,
		events.InboundKindInternalize,
		"slack_thread_summary channel="+strings.TrimSpace(channelID)+" thread="+strings.TrimSpace(threadTS),
		"Slack thread summary from channel "+strings.TrimSpace(channelID)+" thread "+strings.TrimSpace(threadTS)+":\n\n"+summary,
		false,
	)
	if err := m.bus.PublishInbound(ctx, inbound); err != nil {
		return fmt.Errorf("publish Slack thread summary: %w", err)
	}

	m.log.Info("enqueued Slack thread summary in main inbound queue", "channel", strings.TrimSpace(channelID), "thread_ts", strings.TrimSpace(threadTS), "text_len", len(summary))

	return nil
}

func (m *threadBridgeManager) publishDiscordThreadSummary(ctx context.Context, threadID, summary string) error {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return errors.New("thread summary is empty")
	}

	inbound := events.NewMainInboundMessage(
		events.SourceSystem,
		events.InboundKindInternalize,
		"discord_thread_summary thread="+strings.TrimSpace(threadID),
		"Discord thread summary from thread "+strings.TrimSpace(threadID)+":\n\n"+summary,
		false,
	)
	if err := m.bus.PublishInbound(ctx, inbound); err != nil {
		return fmt.Errorf("publish Discord thread summary: %w", err)
	}

	m.log.Info("enqueued Discord thread summary in main inbound queue", "thread_id", strings.TrimSpace(threadID), "text_len", len(summary))

	return nil
}
