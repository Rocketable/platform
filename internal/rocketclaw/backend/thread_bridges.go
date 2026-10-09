package backend

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
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
}

type threadBridgeManager struct {
	log     *slog.Logger
	runtime *config.LockedConfig
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

// cronRootSender posts a delivered cron report as a new Slack thread root, then ends
// that root with a footer linking the conversation the report is bound to.
type cronRootSender interface {
	SendCronjobRoot(context.Context, *protocol.OutboundMessage) (protocol.TextConversationTarget, error)
	EditCronjobRootFooter(ctx context.Context, msg *protocol.OutboundMessage, root protocol.TextConversationTarget, conversationID string) error
}

// noCronRoots is the cron root sender before Slack is attached.
type noCronRoots struct{}

func (noCronRoots) SendCronjobRoot(context.Context, *protocol.OutboundMessage) (protocol.TextConversationTarget, error) {
	return protocol.TextConversationTarget{}, errors.New("slack is not available for cron reports")
}

func (noCronRoots) EditCronjobRootFooter(context.Context, *protocol.OutboundMessage, protocol.TextConversationTarget, string) error {
	return errors.New("slack is not available for cron reports")
}

var _ protocol.PrimaryTextRouter = (*threadBridgeManager)(nil)

func newThreadBridgeManager(runtime *config.LockedConfig, store *SessionService, logger *slog.Logger, factory func(Config) directBridge) *threadBridgeManager {
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

		thread, _, err := m.store.Thread(conversationID)
		if err != nil {
			return fmt.Errorf("load scheduled conversation selection: %w", err)
		}

		if _, err := m.ensureThreadBridge(conversationID, thread); err != nil {
			return fmt.Errorf("start pending scheduled message bridge: %w", err)
		}
	}

	producers, err := m.store.pendingProducerIDs(context.Background())
	if err != nil {
		return err
	}

	for _, source := range producers {
		if err := m.PickLaterWork(context.Background(), source); err != nil {
			return err
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
		inbound.SlackReply = &protocol.SlackReplyTarget{}

		if err := managed.Submit(context.Background(), inbound); err != nil {
			return fmt.Errorf("submit active goal continuation: %w", err)
		}
	}

	return nil
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

// MentionThread treats a slack-thread: conversation cron created, or one an External MCP
// session binds, as a report thread; the prefix keeps cron's own web: chats out.
func (m *threadBridgeManager) MentionThread(target protocol.TextConversationTarget) (recorded, report bool, err error) {
	conversationID := protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID)

	thread, recorded, err := m.store.Thread(conversationID)
	if err != nil {
		return false, false, err
	}

	_, _, paired, err := m.store.ExternalMCPSessionByConversationID(conversationID)
	if err != nil {
		return false, false, err
	}

	return recorded, thread.CreatedBy == ThreadCreatedByCron || paired, nil
}

// SubmitMention admits a Slack mention under its item ID, the input ID its active turn and
// history record, so a redelivery that finds the ID waiting, running, or answered is refused.
// The check, the conversation record, and the queue row share one history transaction.
func (m *threadBridgeManager) SubmitMention(ctx context.Context, agent string, target protocol.TextConversationTarget, inbound *protocol.InboundMessage) (bool, error) {
	conversationID := protocol.SlackThreadConversationID(target.ChannelID, target.ThreadID)
	id := "slack:" + inbound.SlackReply.ChannelID + ":" + inbound.SlackReply.MessageTS
	inbound.ConversationID, inbound.Metadata["web_message_id"] = conversationID, id

	tx, err := m.store.beginStateTx(ctx, "Slack mention admission")
	if err != nil {
		return false, err
	}

	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, conversationID); err != nil {
		return false, err
	}

	var (
		seen     bool
		position int
	)
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM active_turns WHERE conversation_id = $1 AND inbound_json->'Metadata'->>'web_message_id' = $2)
    OR EXISTS (SELECT 1 FROM thread_queue WHERE conversation_id = $1 AND queue_item_id = $2)
    OR EXISTS (SELECT 1 FROM session_entries e CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry_json::jsonb->'replay_input', 'null'::jsonb)) item
        WHERE e.conversation_id = $1 AND item->>'input_id' = $2),
    (SELECT COALESCE(MAX(position), -1) + 1 FROM thread_queue WHERE conversation_id = $1)`, conversationID, id).Scan(&seen, &position); err != nil {
		return false, fmt.Errorf("read admitted Slack mention: %w", err)
	}

	if seen {
		return false, nil
	}

	if err := (stateDAO{db: tx}).createConversation(ctx, protocol.Conversation{ID: conversationID, Agent: agent}); err != nil {
		return false, err
	}

	item := protocol.ThreadQueueItem{ID: id, ConversationID: conversationID, Message: inbound.Text, Principal: inbound.Metadata[protocol.InboundPrincipalMetadataKey], Kind: protocol.InboundKindEnqueue, Source: inbound.Source, Inbound: inbound, Position: position, StashAt: time.Now().UTC()}
	if err := putThreadQueueItem(ctx, tx, id, &item); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit Slack mention admission: %w", err)
	}

	return true, m.PickLaterWork(ctx, conversationID)
}

// StartNewThread creates a named Web session, submits its first turn, and
// links to it with WebURL.
func (m *threadBridgeManager) StartNewThread(ctx context.Context, req *protocol.StartNewThreadRequest) (protocol.StartNewThreadResult, error) {
	targetAgent := cmp.Or(strings.TrimSpace(req.Agent), strings.TrimSpace(req.CurrentAgent), "main")
	if len(req.AllowedAgents) > 0 && !slices.Contains(req.AllowedAgents, targetAgent) {
		return protocol.StartNewThreadResult{}, fmt.Errorf("agent %q is not allowed on this source surface", targetAgent)
	}

	agents, err := ExternalMCPAgentsIn(m.runtime, m.runtime.Clone().RuntimeDirName())
	if err != nil {
		return protocol.StartNewThreadResult{}, fmt.Errorf("load configured agents: %w", err)
	}

	if !slices.Contains(agents, targetAgent) {
		return protocol.StartNewThreadResult{}, fmt.Errorf("agent %q is not configured", targetAgent)
	}

	conversationID := rand.Text()

	url, err := m.WebURL(ctx, conversationID)
	if err != nil {
		return protocol.StartNewThreadResult{}, err
	}

	managed, err := m.ensureStartedThread(&threadStart{conversationID: conversationID, agent: targetAgent, createdBy: ThreadCreator(req.CreatedBy)})
	if err != nil {
		return protocol.StartNewThreadResult{}, err
	}

	if _, err := m.store.UpdateConversationDetails(ctx, conversationID, nil, &req.Title); err != nil {
		return protocol.StartNewThreadResult{}, fmt.Errorf("name new Web session: %w", err)
	}

	inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, req.Prompt, false)
	inbound.PreserveWhitespace = true
	inbound.ConversationID = conversationID
	inbound.Metadata = map[string]string{protocol.InboundOriginMetadataKey: "System", protocol.InboundMediaMetadataKey: "Text"}

	if err := managed.Submit(ctx, inbound); err != nil {
		return protocol.StartNewThreadResult{}, fmt.Errorf("submit new Web session first prompt: %w", err)
	}

	return protocol.StartNewThreadResult{ConversationID: conversationID, URL: url}, nil
}

// WebURL links to conversationID at this machine's Tailscale IPv4 on the Web port. It
// asks tailscale on every call, so a changed address needs no restart.
func (m *threadBridgeManager) WebURL(ctx context.Context, conversationID string) (string, error) {
	_, port, err := net.SplitHostPort(m.runtime.Clone().Web.ListenAddress)
	if err != nil {
		return "", fmt.Errorf("web.listen_address: %w", err)
	}

	tailscaleIPs, err := exec.CommandContext(ctx, "tailscale", "ip", "-4").Output()
	if err != nil {
		return "", fmt.Errorf("find this machine's Tailscale IPv4 for Web links: %w", err)
	}

	host, _, _ := strings.Cut(strings.TrimSpace(string(tailscaleIPs)), "\n")

	return "http://" + net.JoinHostPort(host, port) + "/s/" + base64.RawURLEncoding.EncodeToString([]byte(conversationID)), nil
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

// noteReady has conversationID's bridge, started when needed, deliver a new Completion Note: a
// running turn takes it at its next step, and otherwise the bridge's later work wakes the conversation.
func (m *threadBridgeManager) noteReady(conversationID string) {
	if err := m.PickLaterWork(context.Background(), conversationID); err != nil {
		m.log.Warn("pick later work for a background note", "conversation_id", conversationID, "error", err)
	}
}

// continueSubagent runs input as a new turn of the subagent a subagent job runs, or without input
// resumes the turn it journaled, with the settings and tools a turn of its conversation gives
// subagents.
func (m *threadBridgeManager) continueSubagent(ctx context.Context, job *backgroundJob, input string) (string, error) {
	runtimeCfg := m.runtime.Clone()

	bridge, err := m.recordedBridge(job.conversationID)
	if err != nil {
		return "", err
	}

	agentName := bridge.agentSnapshot()

	root, agents, skills, resolver, err := prepareRocketCode(m.runtime, agentName, bridge.log, toolModePersistent)
	if err != nil {
		return "", err
	}

	shared := &turnRoot{root: root, users: 1}
	defer shared.release()

	shellTempRel := rocketcodeShellTempRel(runtimeCfg.RuntimeDirName(), job.conversationID)
	if err := root.MkdirAll(shellTempRel, 0o700); err != nil {
		return "", fmt.Errorf("create rocketcode shell temp dir: %w", err)
	}

	rocketcodeConfig := bridge.rocketcodeConfig(filepath.Join(runtimeCfg.Workspace, filepath.FromSlash(shellTempRel)), nil, append(sessionTagTools(m.store, cmp.Or(job.origin.SyncDestination, job.conversationID)), bridge.scheduleMessageTool(job.origin), bridge.resetScheduledMessagesTool(job.origin))...)
	rocketcodeConfig.BackgroundJobs = backgroundTurn{registry: bridge.background, root: shared, conversationID: job.conversationID, origin: job.origin}

	runtime, err := rocketcode.NewWithModelResolver(resolver, &rocketcodeConfig, root, agents, skills, agentName, io.Discard)
	if err != nil {
		return "", fmt.Errorf("prepare subagent wake: %w", err)
	}

	output, err := runtime.ContinueSubagent(ctx, job.jobID, job.subagentKey, job.agent, input)
	if err != nil {
		return "", fmt.Errorf("continue subagent: %w", err)
	}

	return output, nil
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
	var marker string

	err := m.store.db.QueryRowContext(context.Background(), `SELECT COALESCE((SELECT revert_message_id FROM managed_conversations WHERE conversation_id = $1), '')`, conversationID).Scan(&marker)
	if err != nil {
		return nil, fmt.Errorf("read queue cutoff: %w", err)
	}

	if marker != "" {
		return nil, nil
	}

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
		for i := bridge.steersRead; i < len(bridge.steers); i++ {
			request := &bridge.steers[i]
			items = slices.DeleteFunc(items, func(item protocol.ThreadQueueItem) bool { return item.ID == request.queueItemID })
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

func (m *threadBridgeManager) promoteQueueItem(ctx context.Context, conversationID, id string) (bool, error) {
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

		inbound.SlackReply = &protocol.SlackReplyTarget{ChannelID: item.SlackChannel, MessageTS: item.SlackTS, ThreadTS: item.SlackTS}
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
		for i := range bridge.waiting {
			if bridge.waiting[i].queueItemID != id {
				continue
			}

			request := bridge.waiting[i]

			if err := m.store.DeleteThreadQueueItem(id); err != nil {
				bridge.mu.Unlock()
				return false, err
			}

			bridge.waiting = slices.Delete(bridge.waiting, i, i+1)

			if request.completion != nil {
				request.completion.err = context.Canceled
				close(request.completion.done)
			}
			bridge.mu.Unlock()

			return true, nil
		}

		for i := bridge.steersRead; i < len(bridge.steers); i++ {
			request := bridge.steers[i]
			if request.queueItemID == id {
				if err := m.store.DeleteThreadQueueItem(id); err != nil {
					bridge.mu.Unlock()
					return false, err
				}

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

			return nil, fmt.Errorf("persist new Web session: %w", err)
		}
	}

	return managed, nil
}

func (m *threadBridgeManager) switchConversationAgent(conversationID, agent string) (bool, error) {
	ok, err := m.store.SetThreadAgentIfExists(conversationID, agent)
	if err != nil {
		return false, fmt.Errorf("persist conversation agent switch: %w", err)
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

	managed := m.factory(Config{ConversationID: conversationID, Agent: agent})
	if bridge, ok := managed.(*Bridge); ok {
		bridge.threads = m
	}

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
