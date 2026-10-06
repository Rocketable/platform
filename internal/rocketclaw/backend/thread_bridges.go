package backend

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"golang.org/x/sync/errgroup"
)

type directBridge interface {
	Run(ctx context.Context) error
	Stop() error
	Submit(ctx context.Context, msg *protocol.InboundMessage) error
	InterruptActiveTurn() *protocol.InboundMessage
	SwitchAgent(agent string)
	PickLaterWork(ctx context.Context) error
}

type threadStart struct {
	conversationID, agent string
	createdBy             ThreadCreator
	persistErr            string
}

type threadBridgeManager struct {
	log     *slog.Logger
	runtime *config.Config
	store   *SessionService
	factory func(Config) directBridge

	// wake tells Run that a bridge is waiting for its loop.
	wake chan struct{}

	mu        sync.Mutex
	bridges   map[string]directBridge
	pending   map[string]directBridge
	stopping  bool
	cronRoots cronRootSender
}

// cronRootSender posts a delivered cron report as a new Slack thread root.
type cronRootSender interface {
	SendCronjobRoot(context.Context, *protocol.OutboundMessage) (protocol.TextConversationTarget, error)
}

// noCronRoots is the cron root sender before Slack is attached.
type noCronRoots struct{}

func (noCronRoots) SendCronjobRoot(context.Context, *protocol.OutboundMessage) (protocol.TextConversationTarget, error) {
	return protocol.TextConversationTarget{}, errors.New("slack is not available for cron reports")
}

var _ protocol.PrimaryTextRouter = (*threadBridgeManager)(nil)

func newThreadBridgeManager(runtime *config.Config, store *SessionService, logger *slog.Logger, factory func(Config) directBridge) *threadBridgeManager {
	return &threadBridgeManager{
		log: logger.With("component", "thread_bridges"), runtime: runtime, store: store, factory: factory,
		wake:    make(chan struct{}, 1),
		bridges: map[string]directBridge{}, pending: map[string]directBridge{}, cronRoots: noCronRoots{},
	}
}

// Run runs every bridge loop on ctx. Bridges start lazily, so Run starts each
// loop when its bridge is created. Once ctx is done no loop starts: Run stops
// every bridge, so later submissions are stored durably, and returns when every
// loop has returned.
func (m *threadBridgeManager) Run(ctx context.Context) error {
	var loops errgroup.Group

	for stopping := false; !stopping; {
		select {
		case <-ctx.Done():
		case <-m.wake:
		}

		m.mu.Lock()
		pending := m.pending
		m.pending = map[string]directBridge{}
		m.stopping = ctx.Err() != nil
		stopping = m.stopping
		m.mu.Unlock()

		for conversationID, managed := range pending {
			loops.Go(func() error {
				if err := managed.Run(ctx); err != nil {
					m.log.Error("run text thread bridge", "conversation_id", conversationID, "error", err)
				}

				return nil
			})
		}
	}

	m.mu.Lock()
	bridges := slices.Collect(maps.Values(m.bridges))
	m.mu.Unlock()

	var errStop error
	for _, bridge := range bridges {
		errStop = errors.Join(errStop, bridge.Stop())
	}

	started, stopLog := time.Now(), make(chan struct{})

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			m.mu.Lock()
			for conversationID, managed := range m.bridges {
				if bridge, ok := managed.(*Bridge); ok && bridge.handlingSnapshot() {
					m.log.Info("shutdown waiting for running tool calls", "conversation_id", conversationID, "elapsed", time.Since(started))
				}
			}
			m.mu.Unlock()

			select {
			case <-stopLog:
				return
			case <-ticker.C:
			}
		}
	}()

	errStop = errors.Join(errStop, loops.Wait())

	close(stopLog)

	return errStop
}

// StartActiveTurns starts every conversation worker that owns an unfinished
// active turn; each runs that turn before any other work.
func (m *threadBridgeManager) StartActiveTurns(ctx context.Context) error {
	workers, err := m.store.activeTurnWorkers(ctx)
	if err != nil {
		return err
	}

	for _, conversationID := range workers {
		thread, recorded, err := m.store.Thread(conversationID)
		if err != nil {
			return fmt.Errorf("load active turn worker: %w", err)
		}

		if !recorded {
			m.log.Warn("active turn worker conversation is not recorded", "conversation_id", conversationID)
			continue
		}

		if _, err := m.ensureThreadBridge(conversationID, thread); err != nil {
			return fmt.Errorf("start active turn worker: %w", err)
		}
	}

	return nil
}

func (m *threadBridgeManager) StartPendingScheduledMessages() error {
	scheduledMessages, err := m.store.ScheduledMessages()
	if err != nil {
		return fmt.Errorf("load pending scheduled message bridges: %w", err)
	}

	for _, message := range scheduledMessages {
		conversationID := strings.TrimSpace(message.ConversationID)
		if _, err := m.ensureThreadBridge(conversationID, ThreadState{Agent: message.Agent}); err != nil {
			return fmt.Errorf("start pending scheduled message bridge: %w", err)
		}
	}

	return nil
}

// StartActiveGoals continues each active goal, except where an unfinished turn
// resumes first and continues the goal itself.
func (m *threadBridgeManager) StartActiveGoals() error {
	threads, err := m.store.ActiveGoalThreads()
	if err != nil {
		return fmt.Errorf("load active goal bridges: %w", err)
	}

	goals, err := m.store.ActiveGoals()
	if err != nil {
		return fmt.Errorf("load active goals: %w", err)
	}

	for conversationID, thread := range threads {
		resuming, err := m.store.HasActiveTurn(context.Background(), conversationID)
		if err != nil {
			return err
		}

		if resuming {
			continue
		}

		managed, err := m.ensureThreadBridge(conversationID, thread)
		if err != nil {
			return fmt.Errorf("start active goal bridge: %w", err)
		}

		inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "Continue the active goal loop.", false)
		inbound.GoalAction = protocol.GoalActionContinue
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

	if inbound.SlackReply != nil {
		inbound.SlackReply.ThreadTS = strings.TrimSpace(target.ThreadID)
	}

	managed, err := m.ensureThreadBridge(conversationID, thread)
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

// StartNewThread creates a named Web session, submits its first turn, and
// links to it at this machine's Tailscale IPv4 on the Web port.
func (m *threadBridgeManager) StartNewThread(ctx context.Context, req *protocol.StartNewThreadRequest) (protocol.StartNewThreadResult, error) {
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

	_, port, err := net.SplitHostPort(m.runtime.Web.ListenAddress)
	if err != nil {
		return protocol.StartNewThreadResult{}, fmt.Errorf("web.listen_address: %w", err)
	}

	tailscaleIPs, err := exec.CommandContext(ctx, "tailscale", "ip", "-4").Output()
	if err != nil {
		return protocol.StartNewThreadResult{}, fmt.Errorf("find this machine's Tailscale IPv4 for Web links: %w", err)
	}

	host, _, _ := strings.Cut(strings.TrimSpace(string(tailscaleIPs)), "\n")

	conversationID := rand.Text()

	managed, err := m.ensureStartedThread(&threadStart{conversationID: conversationID, agent: targetAgent, createdBy: ThreadCreator(req.CreatedBy), persistErr: "persist new Web session"})
	if err != nil {
		return protocol.StartNewThreadResult{}, err
	}

	if _, err := m.store.UpdateConversationDetails(ctx, conversationID, nil, &req.Title, nil); err != nil {
		return protocol.StartNewThreadResult{}, fmt.Errorf("name new Web session: %w", err)
	}

	inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, req.Prompt, false)
	inbound.PreserveWhitespace = true
	inbound.ConversationID = conversationID
	inbound.Metadata = map[string]string{protocol.InboundOriginMetadataKey: "System", protocol.InboundMediaMetadataKey: "Text"}

	if err := m.submitInbound(ctx, managed, inbound, "new Web session first prompt"); err != nil {
		return protocol.StartNewThreadResult{}, err
	}

	return protocol.StartNewThreadResult{ConversationID: conversationID, URL: "http://" + net.JoinHostPort(host, port) + "/s/" + base64.RawURLEncoding.EncodeToString([]byte(conversationID))}, nil
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

	inbound.GoalAction = protocol.GoalActionKickoff
	inbound.ConversationID = conversationID

	return m.submitInbound(ctx, managed, inbound, "goal thread start")
}

func (m *threadBridgeManager) SkillDescriptions(name string) ([]protocol.SkillDescription, error) {
	agents, skills, err := LoadRuntimeDefinitions(m.runtime, m.runtime.RuntimeDirName())
	if err != nil {
		return nil, err
	}

	agent, ok := agents.Items[name]
	if !ok {
		return nil, fmt.Errorf("agent %q is not configured", name)
	}

	var descriptions []protocol.SkillDescription
	for _, skill := range skills.Available(&agent) {
		descriptions = append(descriptions, protocol.SkillDescription{Name: skill.Name, Description: skill.Description})
	}

	return descriptions, nil
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

func (m *threadBridgeManager) StartQueuedConversations() error {
	conversationIDs, err := m.store.queuedConversationIDs(context.Background())
	if err != nil {
		return fmt.Errorf("load queued conversations: %w", err)
	}

	for _, conversationID := range conversationIDs {
		if err := m.PickLaterWork(context.Background(), conversationID); err != nil {
			return err
		}
	}

	return nil
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

	managed, err := m.ensureThreadBridge(conversationID, thread)
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

// ThreadBusy reports a reserved or running pair, or an unfinished active turn
// waiting to resume, so Slack redeliveries are swallowed rather than started.
func (m *threadBridgeManager) ThreadBusy(target protocol.TextConversationTarget) bool {
	conversationID := protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID)
	if m.store.PairBusyFor(conversationID) {
		return true
	}

	active, err := m.store.HasActiveTurn(context.Background(), conversationID)
	if err != nil {
		m.log.Error("check active turn for busy thread", "conversation_id", conversationID, "error", err)
	}

	return active
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

func (m *threadBridgeManager) recordedBridge(conversationID string) (*Bridge, error) {
	thread, recorded, err := m.store.Thread(conversationID)
	if err != nil {
		return nil, err
	}

	if !recorded {
		return nil, fmt.Errorf("conversation %q is not recorded", conversationID)
	}

	managed, err := m.ensureThreadBridge(conversationID, thread)
	if err != nil {
		return nil, err
	}

	return managed.(*Bridge), nil
}

func (m *threadBridgeManager) queueItems(conversationID string) ([]protocol.ThreadQueueItem, error) {
	items, err := m.store.ThreadQueueForConversation(conversationID)
	if err != nil {
		return nil, fmt.Errorf("list thread queue: %w", err)
	}

	items = slices.DeleteFunc(items, func(item protocol.ThreadQueueItem) bool { return item.Inbound != nil && !item.Inbound.Human })

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

	managed, err := m.ensureThreadBridge(conversationID, thread)
	if err != nil {
		return false, err
	}

	item, claimed, err := (stateDAO{db: m.store.db}).claimThreadQueueItem(ctx, conversationID, id)
	if err != nil || !claimed {
		return false, err
	}

	inbound := item.Inbound
	if inbound == nil {
		content := item.Content
		content.Text = item.Message
		inbound = protocol.NewInboundMessageFromContent(item.Source, cmp.Or(item.Kind, protocol.InboundKindEnqueue), &content, true)
		inbound.Metadata[protocol.InboundPrincipalMetadataKey] = item.Principal
		inbound.Metadata["web_message_id"] = id

		inbound.SlackReply = &protocol.SlackReplyTarget{ChannelID: item.SlackChannel, MessageTS: item.SlackTS, ThreadTS: cmp.Or(threadTS, item.SlackTS)}
		if item.SlackReply != nil {
			inbound.SlackReply = new(*item.SlackReply)
		}
	}

	kind, human := inbound.Kind, inbound.Human

	inbound.Kind, inbound.Human = protocol.InboundKindSteer, true
	if err := managed.Submit(ctx, inbound); err != nil {
		inbound.Kind, inbound.Human = kind, human

		return false, errors.Join(err, m.store.PutThreadQueueItem(id, &item))
	}

	log := m.log
	if !item.StashAt.IsZero() {
		log = log.With("queue_age_ms", time.Since(item.StashAt).Milliseconds())
	}

	log.Info("queue item promoted", "event", "queue_promoted", "conversation_id", conversationID, "queue_item_id", id)

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
				bridge.saveSteersLocked(ctx)

				request.completion.err = context.Canceled
				close(request.completion.done)
				bridge.mu.Unlock()
				request.inbound.CompleteResponseWithAttachments("", nil, context.Canceled)
				m.log.Info("steer removed", "event", "steer_removed", "conversation_id", conversationID, "queue_item_id", id)

				return true, nil
			}
		}
		bridge.mu.Unlock()
	}

	removed, err := execRows(ctx, m.store.db, "delete queue item", "count deleted queue items", `DELETE FROM thread_queue WHERE conversation_id = $1 AND queue_item_id = $2`, conversationID, id)
	if err != nil || removed == 0 {
		return false, err
	}

	m.log.Info("queue item removed", "event", "queue_removed", "conversation_id", conversationID, "queue_item_id", id)

	return true, m.PickLaterWork(ctx, conversationID)
}

func (m *threadBridgeManager) stashQueueItem(ctx context.Context, conversationID string, item *protocol.ThreadQueueItem) error {
	item.ConversationID = conversationID

	existing, err := m.store.ThreadQueueForConversation(conversationID)
	if err != nil {
		return fmt.Errorf("list thread queue: %w", err)
	}

	item.Position = len(slices.DeleteFunc(existing, func(queued protocol.ThreadQueueItem) bool {
		return strings.TrimSpace(queued.ParkAfter) != ""
	}))
	item.ParkAfter = ""

	startedAt := time.Now()
	if err := m.store.PutThreadQueueItem(item.ID, item); err != nil {
		m.log.Error("queue persistence failed", "event", "queue_persist_failed", "conversation_id", conversationID, "queue_item_id", item.ID, "duration_ms", time.Since(startedAt).Milliseconds(), "error_type", fmt.Sprintf("%T", err))
		return fmt.Errorf("stash thread queue item: %w", err)
	}

	m.log.Info("queue item persisted", "event", "queue_persisted", "conversation_id", conversationID, "queue_item_id", item.ID, "kind", item.Kind, "position", item.Position, "stash_at", item.StashAt, "duration_ms", time.Since(startedAt).Milliseconds())

	if item.Kind == protocol.InboundKindHeld {
		return nil
	}

	thread, recorded, err := m.store.Thread(conversationID)
	if err != nil || !recorded {
		return err
	}

	managed, err := m.ensureThreadBridge(conversationID, thread)
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

func (m *threadBridgeManager) ensureStartedThread(start *threadStart) (directBridge, error) {
	thread, recorded, err := m.store.Thread(start.conversationID)
	if err != nil {
		return nil, err
	}

	if !recorded {
		thread = ThreadState{Agent: start.agent, CreatedBy: start.createdBy}
	}

	managed, err := m.ensureThreadBridge(start.conversationID, thread)
	if err != nil {
		return nil, err
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

func (m *threadBridgeManager) ensureThreadBridge(conversationID string, thread ThreadState) (directBridge, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, errors.New("text thread conversation ID is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if managed := m.bridges[conversationID]; managed != nil {
		return managed, nil
	}

	agent := strings.TrimSpace(thread.Agent)
	if agent == "" {
		return nil, errors.New("text thread agent is required")
	}

	managed := m.factory(Config{ConversationID: conversationID, Agent: agent, UserQuestionAsker: protocol.NoUserQuestionAsker()})
	m.bridges[conversationID] = managed

	if m.stopping {
		if err := managed.Stop(); err != nil {
			return nil, fmt.Errorf("stop text thread bridge during shutdown: %w", err)
		}

		return managed, nil
	}

	m.pending[conversationID] = managed
	select {
	case m.wake <- struct{}{}:
	default:
	}

	return managed, nil
}
