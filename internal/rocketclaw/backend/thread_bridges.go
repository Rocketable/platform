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

// errWriteDelivered refuses an agent write that a replayed call already delivered.
var errWriteDelivered = errors.New("this write was already delivered")

// inboundAgentMetadataKey names, in a task's write to a subagent session, the agent type its turn runs as.
const inboundAgentMetadataKey = "rocketclaw_agent"

// subagentPublisher delivers nothing: a subagent session's turns reach its parent through its history.
type subagentPublisher struct{}

func (subagentPublisher) PublishOutbound(_ context.Context, message *protocol.OutboundMessage) error {
	message.MarkDelivered(nil)
	return nil
}

type directBridge interface {
	Run(ctx context.Context) error
	Stop() error
	Submit(ctx context.Context, msg *protocol.InboundMessage) error
	InterruptActiveTurn() *protocol.InboundMessage
	SwitchAgent(agent string)
	PickLaterWork(ctx context.Context) error
}

type bridgeFactory interface {
	newBridge(*Config) directBridge
}

type threadStart struct {
	conversationID, agent, spawnedBy string
	createdBy                        ThreadCreator
}

type threadBridgeManager struct {
	log     *slog.Logger
	runtime *config.LockedConfig
	store   *SessionService
	factory bridgeFactory

	// wake tells Run that a bridge is waiting for its loop.
	wake chan struct{}

	mu           sync.Mutex
	bridges      map[string]directBridge
	pending      map[string]directBridge
	stopping     bool
	slackLookups slackLookup
}

// slackLookup gives session search the stored Slack room names and the Slack
// user and group IDs whose names contain a needle.
type slackLookup interface {
	SidebarChannelAgentChoices(context.Context, string) (string, []string, error)
	SlackTagsMatching(context.Context, string) []string
}

// noSlackLookups is the Slack lookup before Slack is attached: no room names, no tag IDs.
type noSlackLookups struct{}

func (noSlackLookups) SidebarChannelAgentChoices(context.Context, string) (name string, choices []string, err error) {
	return "", nil, nil
}

func (noSlackLookups) SlackTagsMatching(context.Context, string) []string {
	return nil
}

var _ protocol.PrimaryTextRouter = (*threadBridgeManager)(nil)

func newThreadBridgeManager(runtime *config.LockedConfig, store *SessionService, logger *slog.Logger, factory bridgeFactory) *threadBridgeManager {
	return &threadBridgeManager{
		log: logger.With("component", "thread_bridges"), runtime: runtime, store: store, factory: factory,
		wake:    make(chan struct{}, 1),
		bridges: map[string]directBridge{}, pending: map[string]directBridge{},
		slackLookups: noSlackLookups{},
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
	workers, err := queryStrings(ctx, m.store.db, `SELECT DISTINCT COALESCE(m.reports_to, a.conversation_id) FROM turns a
LEFT JOIN conversations m ON m.conversation_id = a.conversation_id WHERE a.phase NOT IN ('waiting', 'done') AND a.takes_lane ORDER BY 1`, "active turn workers")
	if err != nil {
		return err
	}

	pending, err := queryStrings(ctx, m.store.db, `SELECT DISTINCT child_conversation_id FROM shadow_turns WHERE child_turn_id = '' AND terminal = '' AND jsonb_array_length(inputs) > 0`, "pending shadow workers")
	if err != nil {
		return err
	}

	workers = slices.Compact(slices.Sorted(slices.Values(append(workers, pending...))))

	for _, conversationID := range workers {
		thread, recorded, err := m.store.Thread(conversationID)
		if err != nil {
			return fmt.Errorf("load active turn worker: %w", err)
		}

		if !recorded {
			m.log.Warn("active turn worker conversation is not recorded", "conversation_id", conversationID)
			continue
		}

		if _, err := m.ensureThreadBridge(ctx, conversationID, thread); err != nil {
			return fmt.Errorf("start active turn worker: %w", err)
		}
	}

	return nil
}

func (m *threadBridgeManager) StartPendingScheduledMessages(ctx context.Context) error {
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

		if _, err := m.ensureThreadBridge(ctx, conversationID, thread); err != nil {
			return fmt.Errorf("start pending scheduled message bridge: %w", err)
		}
	}

	// A finished hidden run's scheduled messages wait for its handoff, which startup resumes.
	producers, err := queryStrings(ctx, m.store.db, `WITH `+remindersSQL+`
SELECT DISTINCT r.conversation_id FROM reminders r JOIN conversations c USING (conversation_id) WHERE c.producer_inbound_json IS NOT NULL ORDER BY 1`, "pending producer IDs")
	if err != nil {
		return err
	}

	for _, source := range producers {
		if err := m.PickLaterWork(ctx, source); err != nil {
			return err
		}
	}

	return nil
}

// StartActiveGoals continues each active goal, except where an unfinished turn
// resumes first and continues the goal itself.
func (m *threadBridgeManager) StartActiveGoals(ctx context.Context) error {
	threads, err := m.store.ActiveGoalThreads()
	if err != nil {
		return fmt.Errorf("load active goal bridges: %w", err)
	}

	for conversationID, thread := range threads {
		resuming, err := m.store.HasActiveTurn(ctx, conversationID)
		if err != nil {
			return err
		}

		if resuming {
			continue
		}

		managed, err := m.ensureThreadBridge(ctx, conversationID, thread)
		if err != nil {
			return fmt.Errorf("start active goal bridge: %w", err)
		}

		goal, _, err := m.store.Goal(conversationID)
		if err != nil {
			return fmt.Errorf("load active goal: %w", err)
		}

		inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "Continue the active goal loop.", false)
		inbound.GoalAction = protocol.GoalActionContinue
		inbound.ConversationID = conversationID
		inbound.Metadata = map[string]string{protocol.InboundPrincipalNameMetadataKey: goal.PrincipalName, protocol.InboundPrincipalEmailMetadataKey: goal.PrincipalEmail}
		inbound.SlackReply = &protocol.SlackReplyTarget{}

		if err := managed.Submit(ctx, inbound); err != nil {
			return fmt.Errorf("submit active goal continuation: %w", err)
		}
	}

	return nil
}

// SwitchConversationAgent persists selection and updates the existing live bridge.
// Frontends validate their current human/producer policy before calling it.
func (r *Runtime) SwitchConversationAgent(conversationID, agent string) (bool, error) {
	if err := rejectShadowMutation(context.Background(), r.threads.store.db, conversationID); err != nil {
		return false, err
	}

	switched, err := r.threads.switchConversationAgent(conversationID, agent)
	if err != nil {
		return false, fmt.Errorf("switch conversation agent: %w", err)
	}

	return switched, nil
}

// MentionThread treats a slack-thread: conversation that earlier versions created for a cron
// report or bound to an External MCP session as a report thread; the prefix keeps cron's own
// web: chats out.
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
	if err := tx.QueryRowContext(ctx, `WITH seen AS (
    SELECT 1 FROM turns WHERE conversation_id = $1 AND takes_lane AND (input_id = $2 OR inbound_json->'Metadata'->>'web_message_id' = $2)
    UNION ALL SELECT 1 FROM entries e CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry_json::jsonb->'replay_input', 'null'::jsonb)) item
    WHERE e.conversation_id = $1 AND item->>'input_id' = $2 LIMIT 1
), queued AS (
    SELECT COALESCE(MAX(position), -1) + 1 AS position FROM turns WHERE conversation_id = $1 AND phase = 'waiting'
)
SELECT count(s.*) > 0, q.position FROM queued q LEFT JOIN seen s ON TRUE GROUP BY q.position`, conversationID, id).Scan(&seen, &position); err != nil {
		return false, fmt.Errorf("read admitted Slack mention: %w", err)
	}

	if seen {
		return false, nil
	}

	created, err := (stateDAO{db: tx}).createConversation(ctx, protocol.Conversation{ID: conversationID, Agent: agent}, conversationFacts{kind: KindSlackThread, slackChannel: target.ChannelID, slackThread: target.ThreadID})
	if err != nil {
		return false, err
	}

	if created {
		if _, err := m.store.reconcileShadows(ctx, tx, conversationID, agent); err != nil {
			return false, err
		}
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

// WebURL links to conversationID at webOrigin.
func (m *threadBridgeManager) WebURL(ctx context.Context, conversationID string) (string, error) {
	origin, err := m.webOrigin(ctx)
	if err != nil {
		return "", err
	}

	return origin + "/s/" + base64.RawURLEncoding.EncodeToString([]byte(conversationID)), nil
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

func (m *threadBridgeManager) StartQueuedConversations(ctx context.Context) error {
	conversationIDs, err := queryStrings(ctx, m.store.db, `SELECT DISTINCT conversation_id FROM turns WHERE phase = 'waiting' AND reason <> $1 ORDER BY conversation_id`, "queued conversation IDs", waitScheduled)
	if err != nil {
		return fmt.Errorf("load queued conversations: %w", err)
	}

	for _, conversationID := range conversationIDs {
		if err := m.PickLaterWork(ctx, conversationID); err != nil {
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

	managed, err := m.ensureThreadBridge(ctx, conversationID, thread)
	if err != nil {
		return err
	}

	if err := managed.PickLaterWork(ctx); err != nil {
		return fmt.Errorf("pick later work: %w", err)
	}

	return nil
}

func (m *threadBridgeManager) InterruptConversation(conversationID string) *protocol.InboundMessage {
	m.mu.Lock()
	managed := m.bridges[conversationID]
	m.mu.Unlock()

	if lane, ok := managed.(*Bridge); ok {
		managed = lane.stopTarget()
	}

	if managed == nil {
		return nil
	}

	return managed.InterruptActiveTurn()
}

// webOrigin is the tsnet node's HTTPS origin once it serves. Before that it is
// this machine's Tailscale IPv4 on the Web port, asking tailscale on every
// call, so a changed address needs no restart.
func (m *threadBridgeManager) webOrigin(ctx context.Context) (string, error) {
	cfg := m.runtime.Clone()
	if cfg.Web.Tsnet != nil && cfg.Web.Tsnet.Origin != "" {
		return cfg.Web.Tsnet.Origin, nil
	}

	_, port, err := net.SplitHostPort(cfg.Web.ListenAddress)
	if err != nil {
		return "", fmt.Errorf("web.listen_address: %w", err)
	}

	tailscaleIPs, err := exec.CommandContext(ctx, "tailscale", "ip", "-4").Output()
	if err != nil {
		return "", fmt.Errorf("find this machine's Tailscale IPv4 for Web links: %w", err)
	}

	host, _, _ := strings.Cut(strings.TrimSpace(string(tailscaleIPs)), "\n")

	return "https://" + net.JoinHostPort(host, port), nil
}

// createSession creates a Web session named title for start's agent, unless allowed excludes it, and
// submits inbound as its first turn. It mints start.conversationID and returns its WebURL, looked up
// before any side effect.
func (m *threadBridgeManager) createSession(ctx context.Context, start *threadStart, allowed []string, title string, inbound *protocol.InboundMessage) (string, error) {
	if len(allowed) > 0 && !slices.Contains(allowed, start.agent) {
		return "", fmt.Errorf("agent %q is not allowed on this source surface", start.agent)
	}

	agents, err := ExternalMCPAgentsIn(m.runtime, m.runtime.Clone().RuntimeDirName())
	if err != nil {
		return "", fmt.Errorf("load configured agents: %w", err)
	}

	if !slices.Contains(agents, start.agent) {
		return "", fmt.Errorf("agent %q is not configured", start.agent)
	}

	start.conversationID = rand.Text()

	url, err := m.WebURL(ctx, start.conversationID)
	if err != nil {
		return "", err
	}

	thread := ThreadState{Agent: start.agent, CreatedBy: start.createdBy, spawnedBy: start.spawnedBy}

	managed, err := m.ensureThreadBridge(ctx, start.conversationID, thread)
	if err != nil {
		return "", err
	}

	if err := m.store.UpsertThread(start.conversationID, thread); err != nil {
		m.mu.Lock()
		delete(m.bridges, start.conversationID)
		m.mu.Unlock()

		_ = managed.Stop()

		return "", fmt.Errorf("persist new Web session: %w", err)
	}

	if _, err := m.store.UpdateConversationDetails(ctx, start.conversationID, nil, &title); err != nil {
		return "", fmt.Errorf("name new Web session: %w", err)
	}

	if err := managed.Submit(ctx, inbound); err != nil {
		return "", fmt.Errorf("submit new Web session first prompt: %w", err)
	}

	return url, nil
}

// writeSession delivers an agent's write, inbound under queue item ID id, to the session target
// without waiting for its turn. It returns what became of the write and, for a stop write, whether a
// turn was running.
func (m *threadBridgeManager) writeSession(ctx context.Context, target, id string, mode sessionWriteMode, inbound *protocol.InboundMessage) (sessionWriteOutcome, bool, error) {
	b, err := m.recordedBridge(ctx, target)
	if err != nil {
		return "", false, err
	}

	inbound.ConversationID, inbound.Metadata["web_message_id"] = target, id
	if mode == sessionWriteStop {
		inbound.Metadata[protocol.InboundSenderActionMetadataKey] = protocol.SenderActionStopped
	}

	outcome, resubmit, err := b.admitAgentWrite(ctx, mode, inbound)
	for i := range resubmit {
		select {
		case b.requestCh <- resubmit[i]:
		case <-b.stopCh:
			err = errors.Join(err, b.storeStopped(&resubmit[i], "store waiting request"))
		}
	}

	if err != nil || inbound.Kind != protocol.InboundKindEnqueue {
		return outcome, false, err
	}

	var running bool
	if mode == sessionWriteStop {
		if running, err = m.stopSession(target); err != nil {
			return "", false, err
		}
	}

	return outcome, running, m.PickLaterWork(ctx, target)
}

// admitAgentWrite stores inbound, an agent's write, as a Thread Queue row first, so it outlives a
// staged revert and a replayed write adds nothing, and a steer joins the running turn while its input
// is open. A stop write's row goes first, and queued requests already handed to the loop wait behind
// it, as StageRevert's do; it returns the loop's other requests to resubmit.
func (b *Bridge) admitAgentWrite(ctx context.Context, mode sessionWriteMode, inbound *protocol.InboundMessage) (outcome sessionWriteOutcome, resubmit []bridgeRequest, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.historyMutation {
		return "", nil, errors.New("conversation history is busy; retry the write later")
	}

	outcome, inbound.Kind = sessionQueued, protocol.InboundKindEnqueue

	switch {
	case mode == sessionWriteStop:
		outcome = sessionStopped
	case mode == sessionWriteStash:
		outcome, inbound.Kind = sessionStashed, protocol.InboundKindHeld
	case mode == sessionWriteSteer && b.inputOpen && !b.stopped:
		outcome, inbound.Kind = sessionSteered, protocol.InboundKindSteer
	}

	for draining := mode == sessionWriteStop; draining; {
		select {
		case request := <-b.requestCh:
			if request.queueItemID != "" && request.producer == nil {
				b.waiting = append(b.waiting, request)
			} else {
				resubmit = append(resubmit, request)
			}
		default:
			draining = false
		}
	}

	tx, err := b.config.SessionService.beginStateTx(ctx, "agent write")
	if err != nil {
		return "", resubmit, err
	}

	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, b.config.ConversationID); err != nil {
		return "", resubmit, err
	}

	if accepted, err := reconcileWebDB(ctx, tx, inbound); err != nil {
		return "", resubmit, err
	} else if accepted {
		return "", resubmit, errWriteDelivered
	}

	item := protocol.ThreadQueueItem{ID: inbound.Metadata["web_message_id"], ConversationID: b.config.ConversationID, Message: inbound.Text, Kind: inbound.Kind, Source: inbound.Source, Inbound: inbound, StashAt: time.Now().UTC()}

	if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN $2 THEN COALESCE(MIN(position) - 1, 0) ELSE COALESCE(MAX(position) + 1, 0) END FROM turns WHERE conversation_id = $1 AND phase = 'waiting'`, b.config.ConversationID, mode == sessionWriteStop).Scan(&item.Position); err != nil {
		return "", resubmit, fmt.Errorf("read agent write position: %w", err)
	}

	if mode != sessionWriteStash {
		if _, err := releaseHeldInputs(ctx, tx, b.config.ConversationID, &item.Position); err != nil {
			return "", resubmit, err
		}
	}

	if err := putThreadQueueItem(ctx, tx, item.ID, &item); err != nil {
		return "", resubmit, err
	}

	if err := tx.Commit(); err != nil {
		return "", resubmit, fmt.Errorf("commit agent write: %w", err)
	}

	if inbound.Kind == protocol.InboundKindSteer {
		b.steers = append(b.steers, bridgeRequest{inbound: inbound, queueItemID: item.ID, completion: &turnCompletion{done: make(chan struct{})}})
		b.saveSteersLocked(ctx)
	}

	return outcome, resubmit, nil
}

// releaseHeldInputs releases the inputs stopped parents held in conversationID's queue (awaitSubagent): held
// rows whose inbound is still an enqueue, unlike a stash's. At position, a newer write's, they run before it,
// as older rows; with no position they keep theirs. It returns them in queue order.
func releaseHeldInputs(ctx context.Context, db stateStoreDB, conversationID string, position *int) ([]protocol.ThreadQueueItem, error) {
	return queryRows(ctx, db, `WITH released AS (UPDATE turns SET reason = $3, delivery = $4, position = COALESCE($5, position)
    WHERE conversation_id = $1 AND phase = 'waiting' AND reason = $2 RETURNING `+waitingColumns+`)
SELECT * FROM released ORDER BY position, created_at_unix_ns, input_id COLLATE "C"`, "released held inputs", scanThreadQueueItem, conversationID, waitHeld, waitQueued, protocol.InboundKindEnqueue, position)
}

// releaseHeld releases the inputs stopped parents held in b's queue (releaseHeldInputs) for a person's
// write, which reaches the loop directly, and hands them to the loop first. Later work would hand over
// only the queue's head, and only once the loop holds no request, so after that write.
func (b *Bridge) releaseHeld(ctx context.Context) error {
	held, err := releaseHeldInputs(ctx, b.config.SessionService.db, b.config.ConversationID, nil)
	for i := range held {
		err = errors.Join(err, b.enqueue(ctx, &bridgeRequest{inbound: held[i].Inbound, queueItemID: held[i].ID, released: true}, "release held input"))
	}

	return err
}

// stopSession stops target's goal and current turn without waiting, as Slack's stop does, and reports
// whether a turn was running.
func (m *threadBridgeManager) stopSession(target string) (bool, error) {
	if err := m.store.StopGoal(target); err != nil {
		return false, fmt.Errorf("stop goal: %w", err)
	}

	return m.InterruptConversation(target) != nil, nil
}

// noteReady has conversationID's bridge, started when needed, deliver a new Completion Note: a
// running turn takes it at its next step, and otherwise the bridge's later work wakes the conversation.
func (m *threadBridgeManager) noteReady(conversationID string) {
	if err := m.PickLaterWork(context.Background(), conversationID); err != nil {
		m.log.Warn("pick later work for a background note", "conversation_id", conversationID, "error", err)
	}
}

// continueSubagent answers, after a restart, the task job awaiting a turn of its subagent session, once
// that turn ends, as the job's task call would have.
func (m *threadBridgeManager) continueSubagent(ctx context.Context, job *backgroundJob) (string, error) {
	runtimeCfg := m.runtime.Clone()

	bridge, err := m.recordedBridge(ctx, job.conversationID)
	if err != nil {
		return "", err
	}

	agentName := bridge.agentSnapshot()

	root, agents, skills, resolver, err := prepareRocketCode(m.runtime, agentName, bridge.log, toolModePersistent)
	if err != nil {
		return "", err
	}

	defer func() { _ = root.Close() }()

	shellTempRel := rocketcodeShellTempRel(runtimeCfg.RuntimeDirName(), job.conversationID)
	if err := root.MkdirAll(shellTempRel, 0o700); err != nil {
		return "", fmt.Errorf("create rocketcode shell temp dir: %w", err)
	}

	rocketcodeConfig := bridge.rocketcodeConfig(filepath.Join(runtimeCfg.Workspace, filepath.FromSlash(shellTempRel)), nil)

	runtime, err := rocketcode.NewWithModelResolver(resolver, &rocketcodeConfig, root, agents, skills, agentName, io.Discard)
	if err != nil {
		return "", fmt.Errorf("prepare task resume: %w", err)
	}

	output, err := runtime.AwaitTask(ctx, job.jobID, job.subagentKey, job.agent, job.input)
	if err != nil {
		return "", fmt.Errorf("await subagent: %w", err)
	}

	return output, nil
}

func (m *threadBridgeManager) recordedBridge(ctx context.Context, conversationID string) (*Bridge, error) {
	thread, recorded, err := m.store.Thread(conversationID)
	if err != nil {
		return nil, err
	}

	if !recorded {
		return nil, fmt.Errorf("conversation %q is not recorded", conversationID)
	}

	managed, err := m.ensureThreadBridge(ctx, conversationID, thread)
	if err != nil {
		return nil, err
	}

	return managed.(*Bridge), nil
}

func (m *threadBridgeManager) queueItems(conversationID string) ([]protocol.ThreadQueueItem, error) {
	var marker string
	if err := queryScalar(context.Background(), m.store.db, "read queue cutoff", `SELECT COALESCE(max(revert_message_id), '') FROM conversations WHERE conversation_id = $1`, &marker, conversationID); err != nil || marker != "" {
		return nil, err
	}

	items, err := m.store.ThreadQueueForConversation(conversationID)
	if err != nil {
		return nil, fmt.Errorf("list thread queue: %w", err)
	}

	// Shutdown stores other system prompts here; agent writes carry their sender.
	items = slices.DeleteFunc(items, func(item protocol.ThreadQueueItem) bool {
		return item.Inbound != nil && !item.Inbound.Human && item.Inbound.Metadata[protocol.InboundSenderSessionMetadataKey] == ""
	})

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

			item := protocol.ThreadQueueItem{ID: request.queueItemID, ConversationID: conversationID, Kind: protocol.InboundKindSteer, Message: inbound.Text, Principal: inbound.Metadata[protocol.InboundPrincipalMetadataKey], Inbound: inbound}
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

	managed, err := m.ensureThreadBridge(ctx, conversationID, thread)
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

	kind := inbound.Kind

	inbound.Kind = protocol.InboundKindSteer
	if err := managed.Submit(ctx, inbound); err != nil {
		inbound.Kind = kind

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

	removed, err := execRows(ctx, m.store.db, "delete queue item", "count deleted queue items", `DELETE FROM turns WHERE conversation_id = $1 AND input_id = $2 AND phase = 'waiting'`, conversationID, id)
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

	if item.Kind != protocol.InboundKindHeld {
		if _, err := releaseHeldInputs(ctx, m.store.db, conversationID, &item.Position); err != nil {
			return err
		}
	}

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

	managed, err := m.ensureThreadBridge(ctx, conversationID, thread)
	if err != nil {
		return err
	}

	return managed.(*Bridge).submitEnqueuedItem(ctx, item)
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

func (m *threadBridgeManager) ensureThreadBridge(ctx context.Context, conversationID string, thread ThreadState) (directBridge, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, errors.New("text thread conversation ID is required")
	}

	m.mu.Lock()
	managed := m.bridges[conversationID]
	m.mu.Unlock()

	if managed != nil {
		return managed, nil
	}

	agent := strings.TrimSpace(thread.Agent)
	if agent == "" {
		return nil, errors.New("text thread agent is required")
	}

	var shadow, underShadow bool
	if err := m.store.db.QueryRowContext(ctx, `WITH RECURSIVE starts AS (
    SELECT $1::text AS id
), `+ancestorsSQL+`
SELECT COALESCE(bool_or(b.conversation_id = $1), FALSE), COALESCE(bool_or(b.conversation_id <> $1), FALSE) FROM ancestors a
JOIN conversations b ON b.conversation_id = a.conversation_id WHERE b.shadow_parent IS NOT NULL`, conversationID).Scan(&shadow, &underShadow); err != nil {
		return nil, fmt.Errorf("load worker ownership: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if managed := m.bridges[conversationID]; managed != nil {
		return managed, nil
	}

	managed = m.factory.newBridge(&Config{ConversationID: conversationID, Agent: agent})
	if bridge, ok := managed.(*Bridge); ok {
		bridge.threads, bridge.shadow, bridge.underShadow, bridge.parent = m, shadow, underShadow, thread.parent
		if thread.parent != "" {
			bridge.bus = subagentPublisher{}
		}
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
