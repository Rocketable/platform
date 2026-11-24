package backend

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"log/slog"
	"maps"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing/fstest"
	"time"
	"unicode"

	instrumentation "github.com/Arize-ai/openinference/go/openinference-instrumentation"
	semconv "github.com/Arize-ai/openinference/go/openinference-semantic-conventions"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/mcpclient"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	otelsemconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace/noop"
	"golang.org/x/sync/errgroup"
)

const (
	restartToolName                = "rocketclaw_restart"
	reloadToolName                 = "rocketclaw_reload"
	attachFilesToolName            = "rocketclaw_attach_files_to_response"
	updateGoalToolName             = "rocketclaw_update_goal"
	askUserQuestionToolName        = "ask_user_question"
	scheduleMessageToolName        = "rocketclaw_schedule_message"
	resetScheduledMessagesToolName = "rocketclaw_reset_scheduled_messages"
	respondInThreadToolName        = "slack_respond_in_thread"

	internalErrorResponse        = "I hit an internal error while waiting for rocketcode."
	wakeRetryNotice              = "I will keep retrying and post the background result here when it arrives."
	attachmentAccessFallback     = "I can see that you attached a file, but I could not send it to the model. Please re-upload it as a supported image or send a smaller file."
	unsupportedFileFallback      = "I can see that you attached a non-image file. I can inspect image attachments right now, but other file types are not supported yet."
	defaultQueueSize             = 128
	externalMCPMetadataEntryType = "mcp_external_metadata"
	producerEffectsStep          = "/effects"
	respondInThreadStep          = "/respond-in-thread"
	rocketclawConversationIDEnv  = "ROCKETCLAW_CONVERSATION_ID"
	rocketclawMetadataEnvPrefix  = "ROCKETCLAW_METADATA_"
)

var errTurnInterrupted = errors.New("rocketcode turn interrupted")

type turnMovedError struct {
	thread  string
	stopped bool
}

func (e turnMovedError) Error() string {
	return "rocketcode turn moved into " + e.thread
}

type runtimeRestarter interface {
	Restart(string) string
}

type Config struct {
	ConversationID, Agent string
	RequestRestart        runtimeRestarter
	RequestReload         runtimeReloader
	UserQuestionAsker     protocol.UserQuestionAsker
	SessionService        *SessionService
	Secrets               config.SecretFetcher
}

type Bridge struct {
	log                 *slog.Logger
	config              Config
	runtime             *config.LockedConfig
	bus                 protocol.OutboundPublisher
	requestCh           chan bridgeRequest
	stopCh              chan struct{}
	threads             *threadBridgeManager
	background          *backgroundRegistry
	titles              sync.WaitGroup
	shadow, underShadow bool
	parent              string

	mu                    sync.Mutex
	handling, stopped     bool
	activeReply           *protocol.InboundMessage
	activeAttribution     harness.ReplayAttribution
	activeTurnInterrupts  chan os.Signal
	activeTurnCancel      context.CancelFunc
	activeTurnInterrupted bool
	activeCompletion      *turnCompletion
	producing             *Bridge
	inputOpen             bool
	activeTurnID          string
	steers                []bridgeRequest
	steersRead            int
	historyMutation       bool
	waiting               []bridgeRequest
	settlement            *turnCompletion
	timerWaiting          bool
	goalHeld              bool
	notesOpen             bool
	notesOffered          []string
	movedNotes            []harness.PromptInput
	shadowInputWake       chan struct{}
}

type turnCompletion struct {
	done chan struct{}
	err  error
}

type bridgeRequest struct {
	inbound                   *protocol.InboundMessage
	turnID                    string
	delivery                  *protocol.OutboundMessage
	scheduledMessageID        string
	scheduledMessageRecurring bool
	queueItemID               string
	completion                *turnCompletion
	producer                  *Bridge
	backgroundWake            bool
	released                  bool
}

func (request *bridgeRequest) claimQueueItem(ctx context.Context, db stateStoreDB, conversationID, turnID string) (protocol.ThreadQueueItem, bool, error) {
	item, err := scanThreadQueueItem(db.QueryRowContext(ctx, `UPDATE turns SET id = $3, phase = $4, reason = NULL
WHERE conversation_id = $1 AND input_id = $2 AND phase = $5 AND reason = $6 RETURNING `+waitingColumns, conversationID, request.queueItemID, turnID, turnRunning, turnWaiting, waitQueued))
	if found, err := scanFound("start waiting turn", err); err != nil || !found {
		return item, false, err
	}

	content := item.Content
	content.Text = item.Message

	current := item.Inbound
	if current == nil {
		current = protocol.NewInboundMessageFromContent(item.Source, item.Kind, &content, true)
		current.Metadata[protocol.InboundPrincipalMetadataKey] = item.Principal
	}

	request.inbound.Text = current.Text
	if raw, ok := current.Metadata[protocol.InboundRawTextMetadataKey]; ok {
		if request.inbound.Metadata == nil {
			request.inbound.Metadata = make(map[string]string)
		}

		request.inbound.Metadata[protocol.InboundRawTextMetadataKey] = raw
	}

	copyProvenance(request.inbound, current)

	return item, true, nil
}

func copyProvenance(dst, src *protocol.InboundMessage) {
	dst.Source, dst.Human = src.Source, src.Human
	for _, key := range []string{protocol.InboundOriginMetadataKey, protocol.InboundMediaMetadataKey, protocol.InboundPrincipalMetadataKey, protocol.InboundPrincipalNameMetadataKey, protocol.InboundPrincipalEmailMetadataKey, protocol.InboundSenderSessionMetadataKey, protocol.InboundSenderAgentMetadataKey, protocol.InboundSenderActionMetadataKey} {
		if value, ok := src.Metadata[key]; ok {
			dst.Metadata[key] = value
		} else {
			delete(dst.Metadata, key)
		}
	}
}

type runResult struct {
	turnID, text  string
	responseID    string
	attribution   harness.ReplayAttribution
	attachments   []protocol.OutboundAttachment
	goalCompleted bool
}

func NewConversation(cfg *config.LockedConfig, publisher protocol.OutboundPublisher, bridgeCfg *Config, logger *slog.Logger) *Bridge {
	return &Bridge{log: logger.With("component", "rocketcode"), config: *bridgeCfg, runtime: cfg, bus: publisher, requestCh: make(chan bridgeRequest, defaultQueueSize), stopCh: make(chan struct{})}
}

func (b *Bridge) AppendChildEntry(ctx context.Context, key harness.ChildKey, entry *harness.SessionEntry) error {
	tx, err := b.config.SessionService.beginStateTx(ctx, "review history append")
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `WITH RECURSIVE ancestors AS (
    SELECT conversation_id, parent_conversation_id, kind, retention, slack_channel FROM conversations WHERE conversation_id = $2
    UNION ALL SELECT c.conversation_id, c.parent_conversation_id, c.kind, c.retention, c.slack_channel FROM conversations c JOIN ancestors a ON c.conversation_id = a.parent_conversation_id
)
INSERT INTO conversations (conversation_id, agent, created_by, parent_conversation_id, kind, listed, retention, slack_channel, call_turn, call_id)
SELECT $1, '', '', $2, $3, FALSE, CASE WHEN bool_or(retention = 'private' OR parent_conversation_id IS NULL
    AND kind IN ('slack_thread', 'cron_run', 'one_off_cron_run', 'external_mcp_run')) THEN 'private' ELSE 'nested' END,
    max(slack_channel) FILTER (WHERE conversation_id = $2), $4, $5 FROM ancestors
ON CONFLICT (conversation_id) DO NOTHING`, b.config.ConversationID+key.Path, b.config.ConversationID+key.Parent, KindReview, key.Turn, key.Call); err != nil {
		return fmt.Errorf("record review history: %w", err)
	}

	if _, err := appendSessionEntryDB(ctx, tx, b.config.ConversationID+key.Path, entry); err != nil {
		return err
	}

	return commitState(tx, "review history append")
}

func (b *Bridge) WriteSubagent(ctx context.Context, write *harness.SubagentWrite) (bool, error) {
	target := b.config.ConversationID + write.Key.Path

	if err := b.config.SessionService.startSubagent(ctx, b.config.ConversationID, write); err != nil {
		return false, err
	}

	if _, err := b.threads.switchConversationAgent(target, write.Agent); err != nil {
		return false, err
	}

	inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, write.Prompt, false)
	inbound.PreserveWhitespace = true
	inbound.Metadata = map[string]string{protocol.InboundOriginMetadataKey: "System", protocol.InboundMediaMetadataKey: "Text", protocol.InboundSenderSessionMetadataKey: b.config.ConversationID, protocol.InboundSenderAgentMetadataKey: write.Delegator, inboundAgentMetadataKey: write.Agent}

	outcome, _, err := b.threads.writeSession(ctx, target, b.config.ConversationID+"/"+write.CallKey, sessionWriteMode(cmp.Or(write.Mode, harness.ContinueEnqueue)), inbound, nil)
	if errors.Is(err, errWriteDelivered) {
		return false, nil
	}

	return outcome == sessionSteered, err
}

func (b *Bridge) AwaitSubagent(ctx context.Context, key, callKey string) (string, harness.ReplayAttribution, error) {
	return b.background.awaitSubagent(ctx, b.config.ConversationID+key, b.config.ConversationID+"/"+callKey)
}

func (b *Bridge) Run(ctx context.Context) error {
	if err := b.armPendingScheduledMessages(); err != nil {
		return err
	}

	if err := b.config.SessionService.postStopLines(context.WithoutCancel(ctx), b.config.ConversationID); err != nil {
		return err
	}

	head, ok, err := b.headRequest(context.WithoutCancel(ctx))
	if err != nil {
		return err
	}

	if ok {
		b.handle(ctx, &head)
	}

	b.loop(ctx)
	b.titles.Wait()

	return nil
}

func (b *Bridge) SwitchAgent(agent string) {
	b.mu.Lock()
	b.config.Agent = strings.TrimSpace(agent)
	b.mu.Unlock()
}

func hiddenRun(inbound *protocol.InboundMessage) bool {
	return inbound.SyncDestination != "" || inbound.RequireOutputDecision
}

func (b *Bridge) ScheduleMessage(origin *protocol.InboundMessage, delay time.Duration, message string, recurring bool) error {
	header := reviewOriginHeader(origin)
	if !strings.HasPrefix(header, "[ScheduledMessage ") {
		header = "[ScheduledMessage source=" + header[1:]
	}

	scheduled := protocol.ScheduledMessageState{ConversationID: b.config.ConversationID, Agent: b.agentSnapshot(), Message: message, DueAt: time.Now().UTC().Add(delay), Recurring: recurring, Origin: header}
	if recurring {
		scheduled.Interval = delay
	}

	if hiddenRun(origin) {
		return b.recordProducerEffect(producerEffect{Schedule: &scheduled})
	}

	id := rand.Text()

	if err := b.config.SessionService.PutScheduledMessage(id, &scheduled); err != nil {
		b.log.Error("scheduled message persist failed", "scheduled_message_id", id, "conversation_id", scheduled.ConversationID, "agent", scheduled.Agent, "due_at", scheduled.DueAt, "delay_ms", delay.Milliseconds(), "recurring", recurring, "interval_ms", scheduled.Interval.Milliseconds(), "message_len", len([]rune(message)), "error", err)
		return fmt.Errorf("persist scheduled message: %w", err)
	}

	b.log.Info("scheduled message persisted", "scheduled_message_id", id, "conversation_id", scheduled.ConversationID, "agent", scheduled.Agent, "due_at", scheduled.DueAt, "delay_ms", delay.Milliseconds(), "recurring", recurring, "interval_ms", scheduled.Interval.Milliseconds(), "message_len", len([]rune(message)))
	b.armScheduledMessage(id, &scheduled)

	return nil
}

func (b *Bridge) ResetScheduledMessages(origin *protocol.InboundMessage) error {
	if hiddenRun(origin) {
		return b.recordProducerEffect(producerEffect{Reset: true})
	}

	if err := b.config.SessionService.ResetScheduledMessages(b.config.ConversationID); err != nil {
		return fmt.Errorf("reset scheduled messages: %w", err)
	}

	b.log.Info("scheduled messages reset", "conversation_id", b.config.ConversationID)

	return nil
}

func (b *Bridge) Stop() {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return
	}

	close(b.stopCh)
	b.stopped = true
	b.mu.Unlock()
}

func (b *Bridge) Submit(ctx context.Context, msg *protocol.InboundMessage) error {
	msg.ConversationID = b.config.ConversationID

	return b.enqueue(ctx, &bridgeRequest{inbound: msg}, "submit inbound message")
}

func (b *Bridge) InterruptActiveTurn() *protocol.InboundMessage {
	b.mu.Lock()
	reply := b.activeReply
	interrupts := b.activeTurnInterrupts
	cancel := b.activeTurnCancel
	active := interrupts != nil || cancel != nil || b.handling
	b.activeTurnInterrupted = b.activeTurnInterrupted || active

	var settlement *turnCompletion
	if !active && b.settlement == nil {
		settlement = &turnCompletion{done: make(chan struct{})}
		b.settlement = settlement
	}
	b.mu.Unlock()

	if settlement != nil {
		stopped := b.stopIdleTurn()

		b.mu.Lock()
		b.settlement = nil
		close(settlement.done)
		b.mu.Unlock()

		return stopped
	}

	if cancel != nil {
		cancel()
	}

	select {
	case interrupts <- os.Interrupt:
	default:
	}

	return reply
}

func (b *Bridge) PickLaterWork(ctx context.Context) error {
	if b.shadow {
		select {
		case b.requestCh <- bridgeRequest{}:
		default:
		}

		return nil
	}

	return b.pickLaterWork(ctx, false)
}

func (b *Bridge) stopTarget() *Bridge {
	b.mu.Lock()
	defer b.mu.Unlock()

	return cmp.Or(b.producing, b)
}

func (b *Bridge) recordProducerEffect(effect producerEffect) error {
	ctx := context.Background()
	store := b.config.SessionService

	b.mu.Lock()
	turnID := b.activeTurnID
	b.mu.Unlock()

	data, _ := json.Marshal([]producerEffect{effect})

	if turnID != "" {
		_, err := execState(ctx, store.db, "record producer effect", `INSERT INTO steps (conversation_id, key, value) VALUES ($1, $2, $3)
ON CONFLICT (conversation_id, key) DO UPDATE SET value = (steps.value::jsonb || excluded.value::jsonb)::json`, b.config.ConversationID, turnID+producerEffectsStep, string(removeSessionEntryNUL(data)))

		return err
	}

	tx, err := store.beginStateTx(ctx, "producer effect")
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := lockRunHistories(ctx, tx, b.config.ConversationID); err != nil {
		return err
	}

	target, schedules, err := (stateDAO{db: tx}).applyProducerEffects(ctx, b.config.ConversationID, []producerEffect{effect})
	if err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit producer effect: %w", err)
	}

	return b.threads.armSchedules(ctx, target, schedules)
}

func (m *threadBridgeManager) armSchedules(ctx context.Context, target string, schedules map[string]protocol.ScheduledMessageState) error {
	if len(schedules) == 0 {
		return nil
	}

	destination, err := m.recordedBridge(ctx, target)
	if err != nil {
		return err
	}

	for id, scheduled := range schedules {
		destination.armScheduledMessage(id, &scheduled)
	}

	return nil
}

func (b *Bridge) headRequest(ctx context.Context) (bridgeRequest, bool, error) {
	turn, ok, err := b.config.SessionService.headTurn(ctx, b.config.ConversationID)
	if err == nil && !ok && b.shadow {
		turn, ok, err = b.config.SessionService.claimShadowTurn(ctx, b.config.ConversationID)
	}

	if err != nil || !ok {
		return bridgeRequest{}, false, err
	}

	request := bridgeRequest{inbound: turn.inbound, turnID: turn.id}
	if turn.phase == turnDelivering {
		request.delivery = turn.outbound
	}

	if turn.conversationID != b.config.ConversationID {
		request.producer, err = b.threads.recordedBridge(ctx, turn.conversationID)
		if err != nil {
			return bridgeRequest{}, false, fmt.Errorf("load active turn producer: %w", err)
		}
	}

	b.log.Info("resuming active turn", "conversation_id", turn.conversationID, "turn_id", turn.id, "phase", turn.phase)

	return request, true, nil
}

func (b *Bridge) armPendingScheduledMessages() error {
	scheduledMessages, err := b.config.SessionService.ScheduledMessagesForConversation(b.config.ConversationID)
	if err != nil {
		return fmt.Errorf("load scheduled messages: %w", err)
	}

	for id, message := range scheduledMessages {
		b.log.Info("scheduled message restored", "scheduled_message_id", id, "conversation_id", message.ConversationID, "agent", message.Agent, "due_at", message.DueAt, "remaining_ms", time.Until(message.DueAt).Milliseconds())
		b.armScheduledMessage(id, &message)
	}

	return nil
}

func (b *Bridge) agentSnapshot() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.config.Agent
}

func (b *Bridge) stopIdleTurn() *protocol.InboundMessage {
	ctx := context.Background()

	turn, ok, err := b.config.SessionService.headTurn(ctx, b.config.ConversationID)
	if err != nil || !ok || turn.phase != turnRunning {
		if err != nil {
			b.log.Error("load idle active turn to stop", "conversation_id", b.config.ConversationID, "error", err)
		}

		return nil
	}

	if err := settleStoppedTrace(ctx, conversationJournal{store: b.config.SessionService, conversationID: b.config.ConversationID, log: b.log}, turn.id); err != nil {
		b.log.Error("settle idle active turn calls", "conversation_id", b.config.ConversationID, "turn_id", turn.id, "error", err)
	}

	outbound := b.newOutboundMessage(turn.inbound, turn.id, "", true)
	outbound.Terminal = protocol.TerminalStopped

	finish := turnFinish{conversationID: strings.TrimSpace(turn.conversationID), outbound: outbound, terminal: protocol.TerminalStopped, hidden: hiddenRun(turn.inbound)}

	outbound, err = b.config.SessionService.finishTurn(ctx, turn.id, &finish)
	if err == nil {
		err = b.notifyShadowInputs(ctx, turn.id)
	}

	if err == nil {
		err = b.deliver(ctx, &bridgeRequest{inbound: turn.inbound, turnID: turn.id}, outbound, finish.retry)
	}

	if err != nil {
		b.log.Error("stop idle active turn", "conversation_id", b.config.ConversationID, "turn_id", turn.id, "error", err)
	}

	return turn.inbound
}

func (b *Bridge) enqueue(ctx context.Context, request *bridgeRequest, operation string) error {
	b.mu.Lock()

	stopCh, stopped := b.stopCh, b.stopped
	if b.historyMutation {
		if request.queueItemID == "" && request.turnID == "" && request.scheduledMessageID == "" {
			b.mu.Unlock()
			return errors.New("conversation history is busy")
		}

		err := b.preserveRevertRequestLocked(ctx, request)
		b.mu.Unlock()

		return err
	}

	if !stopped && request.inbound != nil && request.inbound.Kind == protocol.InboundKindSteer && b.inputOpen {
		if request.queueItemID == "" {
			request.queueItemID = cmp.Or(request.inbound.Metadata["web_message_id"], rand.Text())
		}

		if slices.ContainsFunc(b.steers, func(steer bridgeRequest) bool { return steer.queueItemID == request.queueItemID }) {
			b.mu.Unlock()
			return nil
		}

		if request.completion == nil {
			request.completion = &turnCompletion{done: make(chan struct{})}
		}

		b.steers = append(b.steers, *request)
		b.saveSteersLocked(ctx)
		b.mu.Unlock()
		b.log.Info("steer admitted", "event", "steer_admitted", "conversation_id", b.config.ConversationID, "queue_item_id", request.queueItemID)

		return nil
	}
	b.mu.Unlock()

	if stopped {
		return b.storeStopped(request, operation)
	}

	select {
	case <-ctx.Done():
		return fmt.Errorf("%s: %w", operation, ctx.Err())
	case <-stopCh:
		return b.storeStopped(request, operation)
	case b.requestCh <- *request:
		return nil
	}
}

type journaledSteer struct {
	ID      string                   `json:"id"`
	Inbound *protocol.InboundMessage `json:"inbound"`
}

func (b *Bridge) saveSteersLocked(ctx context.Context) {
	steers := make([]journaledSteer, 0, len(b.steers))
	for i := range b.steers {
		steer := &b.steers[i]
		steers = append(steers, journaledSteer{ID: steer.queueItemID, Inbound: steer.inbound})
	}

	data, _ := json.Marshal(steers)
	if err := b.config.SessionService.SaveTurnStep(context.WithoutCancel(ctx), b.config.ConversationID, b.activeTurnID+"/steers", data); err != nil {
		b.log.Error("record accepted steers", "conversation_id", b.config.ConversationID, "turn_id", b.activeTurnID, "error", err)
	}
}

func (b *Bridge) storeStopped(request *bridgeRequest, operation string) error {
	msg := request.inbound
	if msg == nil || request.turnID != "" || request.queueItemID != "" || request.scheduledMessageID != "" || request.backgroundWake || msg.GoalAction == protocol.GoalActionContinue {
		return fmt.Errorf("%s: %w", operation, protocol.ErrBridgeStopped)
	}

	if msg.Kind == protocol.InboundKindSteer {
		if slipped, err := b.slipSteer(request); err != nil || slipped {
			return err
		}
	}

	owner := b.config.ConversationID
	if request.producer != nil {
		owner = request.producer.config.ConversationID
	}

	queue, err := b.config.SessionService.ThreadQueueForConversation(owner)
	if err != nil {
		return fmt.Errorf("%s during shutdown: %w", operation, err)
	}

	id := rand.Text()
	item := protocol.ThreadQueueItem{ID: id, ConversationID: owner, Kind: protocol.InboundKindEnqueue, Message: msg.Text, Source: msg.Source, Principal: msg.Metadata[protocol.InboundPrincipalMetadataKey], StashAt: time.Now().UTC(), Inbound: msg}
	item.Position = len(slices.DeleteFunc(queue, func(queued protocol.ThreadQueueItem) bool { return queued.ParkAfter != "" }))

	if err := b.config.SessionService.PutThreadQueueItem(id, &item); err != nil {
		return fmt.Errorf("%s during shutdown: %w", operation, err)
	}

	b.log.Info("stored submission during shutdown", "conversation_id", owner, "queue_item_id", id)

	return nil
}

func (b *Bridge) slipSteer(request *bridgeRequest) (bool, error) {
	ctx := context.Background()

	b.mu.Lock()
	defer b.mu.Unlock()

	request.queueItemID = cmp.Or(request.inbound.Metadata["web_message_id"], rand.Text())

	if b.activeTurnID != "" {
		if !b.inputOpen {
			return false, nil
		}

		b.steers = append(b.steers, *request)
		b.saveSteersLocked(ctx)

		return true, nil
	}

	turn, found, err := b.config.SessionService.headTurn(ctx, b.config.ConversationID)
	if err != nil || !found || turn.phase != turnRunning || turn.conversationID != b.config.ConversationID || hiddenRun(turn.inbound) {
		return false, err
	}

	snapshot, _, err := loadTurnInputs(ctx, b.config.SessionService.db, b.config.ConversationID, turn.id)
	if err != nil || snapshot.Closed {
		return false, err
	}

	key := turn.id + "/steers"

	var steers []journaledSteer

	data, recorded, err := b.config.SessionService.LoadTurnStep(ctx, b.config.ConversationID, key)
	if err != nil {
		return false, err
	}

	if recorded {
		if err := json.Unmarshal(data, &steers); err != nil {
			return false, fmt.Errorf("decode accepted steers: %w", err)
		}
	}

	data, _ = json.Marshal(append(steers, journaledSteer{ID: request.queueItemID, Inbound: request.inbound}))

	if err := b.config.SessionService.SaveTurnStep(ctx, b.config.ConversationID, key, data); err != nil {
		return false, err
	}

	b.log.Info("added steer submitted during shutdown to the interrupted turn", "conversation_id", b.config.ConversationID, "turn_id", turn.id)

	return true, nil
}

func (b *Bridge) loop(ctx context.Context) {
	defer b.storeQueued()

	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-b.stopCh:
			return
		case request := <-b.requestCh:
			if request.inbound == nil && b.shadow {
				head, found, err := b.headRequest(ctx)
				if err != nil {
					b.log.Error("claim shadow work", "error", err)
					continue
				}

				if !found {
					continue
				}

				request = head
			}

			b.handle(ctx, &request)
		}
	}

	b.Stop()
}

func (b *Bridge) storeQueued() {
	for {
		select {
		case request := <-b.requestCh:
			if err := b.storeStopped(&request, "store waiting request"); err != nil && !errors.Is(err, protocol.ErrBridgeStopped) {
				b.log.Error("store waiting request during shutdown", "conversation_id", b.config.ConversationID, "error", err)
			}
		default:
			return
		}
	}
}

func (b *Bridge) handle(ctx context.Context, request *bridgeRequest) {
	b.log.Info("bridge dequeued request", "event", "request_dequeued", "conversation_id", b.config.ConversationID, "has_inbound", request.inbound != nil, "turn_id", request.turnID, "scheduled_message_id", request.scheduledMessageID, "queue_item_id", request.queueItemID, "queue_len", len(b.requestCh))
	b.mu.Lock()

	for b.settlement != nil {
		stop := b.settlement
		b.mu.Unlock()
		<-stop.done
		b.mu.Lock()
	}

	var (
		marker string
		paused bool
	)

	errState := b.config.SessionService.db.QueryRowContext(ctx, `SELECT COALESCE(max(revert_message_id), ''), COALESCE(bool_or(paused), FALSE) FROM conversations WHERE conversation_id = $1`, b.config.ConversationID).Scan(&marker, &paused)

	paused = paused && request.turnID == ""
	if b.historyMutation || marker != "" || paused || errState != nil {
		err := errState
		switch {
		case err != nil:
		case paused && !request.inbound.Human && !request.scheduledMessageRecurring:
			err = AgentPausedError{}
			b.goalHeld = b.goalHeld || request.inbound.GoalAction == protocol.GoalActionContinue
		default:
			err = b.preserveRevertRequestLocked(ctx, request)
		}
		b.mu.Unlock()

		if err != nil && request.completion != nil {
			request.completion.err = err
			close(request.completion.done)
		}

		return
	}

	b.handling = true
	settlement := &turnCompletion{done: make(chan struct{})}
	b.settlement = settlement
	b.activeCompletion = cmp.Or(request.completion, settlement)
	b.mu.Unlock()

	defer func() {
		if err := b.config.SessionService.postStopLines(context.WithoutCancel(ctx), b.config.ConversationID); err != nil {
			b.log.Error("post stop lines", "conversation_id", b.config.ConversationID, "error", err)
		}

		b.mu.Lock()
		b.activeReply = nil
		b.activeCompletion, b.producing = nil, nil
		b.settlement = nil
		waiting := b.timerWaiting
		b.handling, b.activeTurnInterrupted, b.timerWaiting = false, false, false

		close(settlement.done)
		b.mu.Unlock()

		if waiting {
			b.armScheduledMessage("", &protocol.ScheduledMessageState{ConversationID: b.config.ConversationID, DueAt: time.Now()})
		}
	}()

	handler := b

	if request.producer != nil {
		handler = request.producer

		b.mu.Lock()
		b.producing = request.producer
		b.mu.Unlock()
		request.producer.mu.Lock()
		request.producer.activeCompletion = request.completion
		request.producer.handling = true
		request.producer.mu.Unlock()

		defer func() {
			request.producer.mu.Lock()
			waiting := request.producer.timerWaiting
			request.producer.handling, request.producer.activeTurnInterrupted, request.producer.timerWaiting = false, false, false
			request.producer.mu.Unlock()

			if waiting {
				request.producer.armScheduledMessage("", &protocol.ScheduledMessageState{ConversationID: request.producer.config.ConversationID, DueAt: time.Now()})
			}
		}()
	}

	admitted, errHandle := b.activateInbound(ctx, request)
	if !admitted && errHandle == nil {
		if request.completion != nil {
			close(request.completion.done)
		}

		b.pickLaterWorkLogged(ctx, b, &protocol.InboundMessage{})

		return
	}

	if errHandle == nil {
		errHandle = handler.handleInbound(ctx, request)
	} else {
		request.inbound.CompleteResponseWithAttachments("", nil, errHandle)
	}

	if _, persistence := errors.AsType[inputPersistenceError](errHandle); persistence {
		settlement.err = errHandle
		return
	}

	if ctx.Err() != nil {
		return
	}

	if errHandle != nil {
		b.log.Error("handle inbound rocketcode message", "error_type", fmt.Sprintf("%T", errHandle))
	}

	settlement.err = errHandle

	if request.completion != nil {
		request.completion.err = errHandle
		close(request.completion.done)
	}

	b.pickLaterWorkLogged(ctx, handler, request.inbound)

	if handler != b {
		b.pickLaterWorkLogged(ctx, b, &protocol.InboundMessage{})
	}
}

func (b *Bridge) handlingSnapshot() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.handling
}

func (b *Bridge) activateInbound(ctx context.Context, request *bridgeRequest) (bool, error) {
	b.mu.Lock()
	if b.historyMutation {
		err := b.preserveRevertRequestLocked(ctx, request)
		request.completion = nil
		b.mu.Unlock()

		return false, err
	}
	b.mu.Unlock()

	owner := b
	if request.producer != nil {
		owner = request.producer
	}

	agent := owner.agentSnapshot()

	if held, err := b.goalHoldsQueue(request); err != nil || held {
		return false, err
	}

	ctx = context.WithoutCancel(ctx)

	tx, err := b.config.SessionService.beginStateTx(ctx, "turn start")
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, owner.config.ConversationID); err != nil {
		return false, err
	}

	var (
		marker                string
		slackSession, blocked bool
	)

	err = tx.QueryRowContext(ctx, `WITH `+rootUnsettledSQL+`, lane AS (
    SELECT count(*) > 0 AS busy FROM turns WHERE conversation_id = $1 AND phase = 'running' AND takes_lane
)
SELECT COALESCE(max(c.revert_message_id), ''), COALESCE(bool_or(c.kind = 'slack_thread'), FALSE), u.held OR l.busy
FROM unsettled u CROSS JOIN lane l LEFT JOIN conversations c ON c.conversation_id = $1 GROUP BY u.held, l.busy`, owner.config.ConversationID).Scan(&marker, &slackSession, &blocked)
	if err != nil {
		return false, fmt.Errorf("read activation cutoff: %w", err)
	}

	if marker != "" {
		return false, nil
	}

	if request.turnID != "" {
		var taken bool

		err = queryScalar(ctx, tx, "check taken active turn", `SELECT COUNT(*) > 0 FROM turns WHERE id = $1 AND conversation_id = $2 AND (phase = $3 OR $4)`, &taken, request.turnID, owner.config.ConversationID, turnRunning, request.delivery != nil)

		return taken, err
	}

	if blocked {
		b.log.Info("turn start unavailable", "event", "queue_blocked", "conversation_id", owner.config.ConversationID, "queue_item_id", request.queueItemID, "blocker", "root_or_running_turn")
		return false, nil
	}

	turnID := b.config.SessionService.newTurnID()

	var stashedAt int64

	if request.queueItemID != "" {
		var (
			item    protocol.ThreadQueueItem
			claimed bool
		)

		item, claimed, err = request.claimQueueItem(ctx, tx, owner.config.ConversationID, turnID)
		stashedAt = timeUnixNano(item.StashAt)

		if err != nil || !claimed {
			b.log.Info("queue claim unavailable", "event", "queue_claim_unavailable", "conversation_id", owner.config.ConversationID, "queue_item_id", request.queueItemID, "error_type", fmt.Sprintf("%T", err))

			return false, err
		}
	}

	agent = cmp.Or(request.inbound.Metadata[inboundAgentMetadataKey], agent)

	if request.scheduledMessageID != "" && !request.scheduledMessageRecurring {
		started, err := execRows(ctx, tx, "start scheduled turn", `WITH parked AS (UPDATE turns SET park_after = '' WHERE park_after = $2)
UPDATE turns SET id = $3, phase = $4, reason = NULL WHERE conversation_id = $1 AND input_id = $2 AND phase = $5 AND reason = $6`,
			owner.config.ConversationID, request.scheduledMessageID, turnID, turnRunning, turnWaiting, waitScheduled)
		if err != nil || started == 0 {
			return false, err
		}
	}

	if request.backgroundWake {
		if woken, err := claimWakeNotes(ctx, tx, owner.config.ConversationID, turnID, request.inbound); err != nil || !woken {
			return false, err
		}
	}

	if slackSession {
		if skipped, err := owner.skipUnstickyTurn(ctx, tx, request, agent, turnID); err != nil || skipped {
			return false, err
		}
	}

	if err := owner.admitShadowInputs(ctx, tx, request, agent, turnID); err != nil {
		return false, err
	}

	if err := startTurnDB(ctx, tx, turnID, owner.config.ConversationID, request.inbound); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit turn start: %w", err)
	}

	request.turnID = turnID
	b.takeHeldCaller(request)

	if stashAt := timeFromUnixNano(stashedAt); request.queueItemID != "" {
		log := b.log
		if !stashAt.IsZero() {
			log = log.With("queue_age_ms", time.Since(stashAt).Milliseconds())
		}

		log.Info("queue item started", "event", "queue_started", "boundary", "activation", "conversation_id", owner.config.ConversationID, "turn_id", turnID, "queue_item_id", request.queueItemID, "stash_at", stashAt)
	}

	return true, nil
}

func (b *Bridge) goalHoldsQueue(request *bridgeRequest) (bool, error) {
	if request.queueItemID == "" || request.producer != nil || request.inbound.GoalAction == protocol.GoalActionKickoff {
		return false, nil
	}

	goal, found, err := b.config.SessionService.Goal(b.config.ConversationID)
	held := found && goal.Status == GoalStatusActive && !request.released

	if err != nil || held {
		b.log.Info("queue activation unavailable", "event", "queue_blocked", "conversation_id", b.config.ConversationID, "queue_item_id", request.queueItemID, "goal_active", held, "error_type", fmt.Sprintf("%T", err))
	}

	return held, err
}

func (b *Bridge) skipUnstickyTurn(ctx context.Context, tx *sql.Tx, request *bridgeRequest, agent, turnID string) (bool, error) {
	agents, err := LoadRuntimeAgents(b.runtime, b.runtime.Clone().RuntimeDirName())
	if err != nil || agents.Items[agent].Sticky == harness.AgentStickySlack {
		return false, err
	}

	b.log.Warn("Slack session turn skipped because its agent is not sticky: slack", "conversation_id", b.config.ConversationID, "agent", agent, "turn_id", turnID, "queue_item_id", request.queueItemID)

	if _, err := execState(ctx, tx, "close skipped Slack turn", `WITH released AS (
    UPDATE turns SET claimed_by = '', wake = FALSE WHERE conversation_id = $2 AND claimed_by = $1 AND note = $3
)
UPDATE turns SET phase = $4 WHERE id = $1`, turnID, b.config.ConversationID, notePending, turnDone); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit skipped Slack turn: %w", err)
	}

	b.wakeHeldThreads(ctx, request.inbound.SlackReply)

	settled := protocol.NewOutboundMessage(b.config.ConversationID, "")
	settled.Complete, settled.SlackReply = true, request.inbound.SlackReply

	if err := b.bus.PublishOutbound(ctx, settled); err != nil {
		return true, fmt.Errorf("publish skipped Slack turn: %w", err)
	}

	return true, nil
}

func (b *Bridge) takeHeldCaller(request *bridgeRequest) {
	b.mu.Lock()
	defer b.mu.Unlock()

	i := slices.IndexFunc(b.waiting, func(held bridgeRequest) bool { return held.queueItemID == request.queueItemID })
	if i < 0 {
		return
	}

	held := b.waiting[i].completion
	b.waiting = slices.Delete(b.waiting, i, i+1)

	switch {
	case held == nil:
	case request.completion == nil:
		request.completion, b.activeCompletion = held, held
	default:
		close(held.done)
	}
}

const rootUnsettledSQL = `thread AS (
    SELECT slack_channel, 'slack:' || slack_channel || ':' || slack_thread AS root FROM conversations
    WHERE conversation_id = $1 AND kind = 'slack_thread' AND slack_thread IS NOT NULL
), unsettled AS (
    SELECT count(*) > 0 AS held FROM thread h
    JOIN conversations c ON c.slack_channel = h.slack_channel AND c.kind = 'slack_thread' AND c.slack_thread IS NULL
    JOIN turns t ON t.conversation_id = c.conversation_id AND t.input_id = h.root WHERE t.phase <> 'done'
)`

func (b *Bridge) wakeHeldThreads(ctx context.Context, reply *protocol.SlackReplyTarget) {
	if reply == nil {
		return
	}

	threads, err := queryStrings(ctx, b.config.SessionService.db, `WITH channel AS (
    SELECT slack_channel FROM conversations WHERE conversation_id = $1 AND kind = 'slack_thread' AND slack_thread IS NULL
)
SELECT c.conversation_id FROM conversations c JOIN channel USING (slack_channel) WHERE c.kind = 'slack_thread' AND c.slack_thread = $2 ORDER BY 1`, "held thread sessions", b.config.ConversationID, reply.ThreadTS)
	for _, conversationID := range threads {
		err = errors.Join(err, b.threads.PickLaterWork(ctx, conversationID))
	}

	if err != nil {
		b.log.Error("wake thread sessions after their root", "conversation_id", b.config.ConversationID, "error", err)
	}
}

func claimWakeNotes(ctx context.Context, tx stateStoreDB, conversationID, turnID string, inbound *protocol.InboundMessage) (bool, error) {
	notes, err := claimBackgroundNotes(ctx, tx, conversationID, turnID, false)
	if err != nil || !slices.ContainsFunc(notes, func(note backgroundJob) bool { return note.wake && !note.wakeAfter.After(time.Now()) }) {
		return false, err
	}

	if inbound.SyncDestination == "" && slices.ContainsFunc(notes, func(note backgroundJob) bool { return note.origin.SyncDestination != "" }) {
		return false, nil
	}

	ids := make([]string, len(notes))
	for i := range notes {
		ids[i] = notes[i].jobID
	}

	inbound.Metadata = map[string]string{backgroundNotesMetadataKey: strings.Join(ids, " ")}
	if !slices.ContainsFunc(notes, func(note backgroundJob) bool { return note.kind != turnReply }) {
		inbound.Metadata[sessionRepliesMetadataKey] = "true"
	}

	return true, nil
}

func (b *Bridge) pickLaterWork(ctx context.Context, fromTimer bool) error {
	b.mu.Lock()
	stopped, notesOpen, mutation, turnID := b.stopped, b.notesOpen, b.historyMutation, b.activeTurnID
	busy := fromTimer && b.handling
	b.timerWaiting = b.timerWaiting || busy
	b.mu.Unlock()

	if mutation {
		return nil
	}

	var (
		marker string
		paused bool
	)

	err := b.config.SessionService.db.QueryRowContext(ctx, `SELECT COALESCE(max(revert_message_id), ''), COALESCE(bool_or(paused), FALSE) FROM conversations WHERE conversation_id = $1`, b.config.ConversationID).Scan(&marker, &paused)
	if err != nil {
		return fmt.Errorf("read later-work cutoff: %w", err)
	}

	if marker != "" || paused {
		return nil
	}

	if stopped {
		return fmt.Errorf("pick later work: %w", protocol.ErrBridgeStopped)
	}

	if busy || len(b.requestCh) > 0 {
		b.log.Info("later work blocked", "event", "queue_blocked", "conversation_id", b.config.ConversationID, "blocker", "active_or_queued_request")
		return nil
	}

	var private, blocked, resume bool
	if err := b.config.SessionService.db.QueryRowContext(ctx, `WITH self AS (
    SELECT count(*) > 0 AS private FROM conversations WHERE conversation_id = $1 AND producer_inbound_json IS NOT NULL
), producing AS (
    SELECT count(*) > 0 AS producing FROM turns a JOIN conversations c ON c.conversation_id = a.conversation_id
    WHERE a.phase NOT IN ('waiting', 'done') AND a.takes_lane AND c.reports_to = $1
), lane AS (
    SELECT count(*) > 0 AS running, COALESCE(bool_or(id <> $2), FALSE) AS idle FROM turns WHERE conversation_id = $1 AND phase = 'running' AND takes_lane
), `+rootUnsettledSQL+`
SELECT s.private, p.producing OR u.held OR l.running, l.idle AND NOT (p.producing OR u.held)
FROM self s CROSS JOIN producing p CROSS JOIN unsettled u CROSS JOIN lane l`, b.config.ConversationID, turnID).Scan(&private, &blocked, &resume); err != nil {
		return fmt.Errorf("check later-work producer delivery: %w", err)
	}

	if private {
		if err := b.handoffProducer(ctx); err != nil {
			return err
		}
	}

	if resume {
		return b.resumeRunningTurn(ctx)
	}

	if blocked {
		return nil
	}

	goal, ok, err := b.config.SessionService.Goal(b.config.ConversationID)
	if err != nil {
		return fmt.Errorf("load goal for later work: %w", err)
	}

	if ok && strings.TrimSpace(goal.Status) == GoalStatusActive {
		b.log.Info("later work blocked", "event", "queue_blocked", "conversation_id", b.config.ConversationID, "blocker", "goal")
		return nil
	}

	note, wake, err := b.config.SessionService.backgroundWake(ctx, b.config.ConversationID)
	if err != nil {
		return fmt.Errorf("load background notes for later work: %w", err)
	}

	switch {
	case !wake || notesOpen:
	case note.wakeAfter.After(time.Now()):
		b.armScheduledMessage("", &protocol.ScheduledMessageState{ConversationID: b.config.ConversationID, DueAt: note.wakeAfter})
	default:
		inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "", false)
		inbound.ConversationID, inbound.SlackReply = b.config.ConversationID, protocol.Clone(note.origin.SlackReply)
		inbound.SyncDestination, inbound.RequireOutputDecision, inbound.Cronjob = note.origin.SyncDestination, note.origin.RequireOutputDecision, note.origin.Cronjob
		inbound.OriginHeader = reviewOriginHeader(note.origin)

		if inbound.SyncDestination == "" {
			return b.enqueue(ctx, &bridgeRequest{inbound: inbound, backgroundWake: true}, "submit background wake")
		}

		destination, err := b.threads.recordedBridge(ctx, inbound.SyncDestination)
		if err != nil {
			return fmt.Errorf("load background wake destination: %w", err)
		}

		return destination.enqueue(ctx, &bridgeRequest{inbound: inbound, backgroundWake: true, producer: b}, "submit background wake")
	}

	queue, err := b.config.SessionService.ThreadQueueForConversation(b.config.ConversationID)
	if err != nil {
		return fmt.Errorf("load thread queue for later work: %w", err)
	}

	scheduled, err := b.config.SessionService.ScheduledMessagesForConversation(b.config.ConversationID)
	if err != nil {
		return fmt.Errorf("load scheduled messages for later work: %w", err)
	}

	now := time.Now().UTC()

	rows := protocol.MixedLaterWork(queue, scheduled)
	if len(rows) == 0 {
		return nil
	}

	head := rows[0]
	if head.Kind == protocol.LaterWorkQueued {
		return b.submitEnqueuedItem(ctx, &head.Queue)
	}

	if private {
		return nil
	}

	if head.Scheduled.DueAt.After(now) {
		b.log.Info("later work not due", "event", "queue_blocked", "conversation_id", b.config.ConversationID, "scheduled_message_id", head.ScheduledID, "blocker", "not_due", "due_at", head.Scheduled.DueAt, "remaining_ms", head.Scheduled.DueAt.Sub(now).Milliseconds())
		return nil
	}

	return b.submitDueScheduled(ctx, head.ScheduledID, &head.Scheduled, now)
}

func (b *Bridge) resumeRunningTurn(ctx context.Context) error {
	head, found, err := b.headRequest(ctx)
	if err != nil || !found {
		return err
	}

	return b.enqueue(ctx, &head, "resume running turn")
}

func (b *Bridge) handoffProducer(ctx context.Context) error {
	runtimeCfg := b.runtime.Clone()
	store, source := b.config.SessionService, b.config.ConversationID
	web := "web:" + source

	tx, err := store.beginStateTx(ctx, "producer handoff")
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	if err := lockHistories(ctx, tx, []string{source, web}); err != nil {
		return err
	}

	dao := stateDAO{db: tx}

	var (
		data   []byte
		owner  string
		active bool
	)
	if err := tx.QueryRowContext(ctx, `WITH busy AS (
    SELECT count(*) > 0 AS active FROM turns WHERE conversation_id = $1 AND phase NOT IN ('waiting', 'done') AND takes_lane
)
SELECT m.producer_inbound_json, COALESCE(m.reports_to, ''), t.active FROM conversations m CROSS JOIN busy t
WHERE m.conversation_id = $1`, source).Scan(&data, &owner, &active); err != nil {
		return fmt.Errorf("read producer handoff: %w", err)
	}

	if active {
		return nil
	}

	scheduled, err := dao.scheduledMessages(ctx, source)
	if err != nil {
		return err
	}

	if owner == "" {
		var earliest *protocol.ScheduledMessageState

		for _, message := range scheduled {
			if earliest == nil || message.DueAt.Before(earliest.DueAt) {
				earliest = &message
			}
		}

		if earliest == nil {
			return nil
		}

		if earliest.DueAt.After(time.Now()) {
			b.armScheduledMessage("", earliest)
			return nil
		}

		var inbound protocol.InboundMessage
		if err := json.Unmarshal(data, &inbound); err != nil {
			return fmt.Errorf("decode producer routing: %w", err)
		}

		definitions, err := LoadRuntimeAgents(b.runtime, runtimeCfg.RuntimeDirName())
		if err != nil {
			return fmt.Errorf("load web agents: %w", err)
		}

		if definitions.Items[inbound.Cronjob.Agent].Sticky == harness.AgentStickyGlobal {
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit producer handoff check: %w", err)
			}

			return b.openCronReport(ctx, &protocol.OutboundMessage{Cronjob: inbound.Cronjob})
		}

		var recorded bool
		if err := tx.QueryRowContext(ctx, `SELECT count(*) > 0 FROM conversations WHERE conversation_id = $1`, web).Scan(&recorded); err != nil {
			return fmt.Errorf("read canonical web conversation: %w", err)
		}

		if !recorded {
			choices := slices.DeleteFunc(definitions.PrimaryNames(), func(name string) bool { return definitions.Items[name].Sticky != harness.AgentStickyOff })
			if len(choices) == 0 {
				return errors.New("create canonical web conversation: no loaded agents")
			}

			space := cmp.Or(inbound.Cronjob.Space, definitions.Items[inbound.Cronjob.Agent].Space)
			if _, err := dao.createConversation(ctx, protocol.Conversation{ID: web, Agent: choices[0], CreatedBy: string(ThreadCreatedByCron), Space: space}, conversationFacts{kind: KindWebChat, cronRun: source}, harness.AgentStickyOff); err != nil {
				return err
			}

			if _, err := b.config.SessionService.reconcileShadows(ctx, tx, web, choices[0]); err != nil {
				return err
			}
		}

		owner = web
	}

	owner, schedules, err := dao.bindRun(ctx, source, owner)
	if err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit producer handoff: %w", err)
	}

	if len(schedules) > 0 {
		return b.threads.armSchedules(ctx, owner, schedules)
	}

	destination, err := b.threads.recordedBridge(ctx, owner)
	if err != nil {
		return err
	}

	return destination.pickLaterWork(ctx, true)
}

func (b *Bridge) submitEnqueuedItem(ctx context.Context, item *protocol.ThreadQueueItem) error {
	b.mu.Lock()
	for i := range b.waiting {
		if b.waiting[i].queueItemID == item.ID {
			request := b.waiting[i]
			b.waiting = slices.Delete(b.waiting, i, i+1)
			b.mu.Unlock()

			return b.enqueue(ctx, &request, "resume preserved request")
		}
	}
	b.mu.Unlock()

	if inbound := item.Inbound; inbound != nil {
		if inbound.SyncDestination == "" {
			return b.enqueue(ctx, &bridgeRequest{inbound: inbound, queueItemID: item.ID}, "submit stored message")
		}

		destination, err := b.threads.recordedBridge(ctx, inbound.SyncDestination)
		if err != nil {
			return fmt.Errorf("load stored message destination: %w", err)
		}

		return destination.enqueue(ctx, &bridgeRequest{inbound: inbound, queueItemID: item.ID, producer: b}, "submit stored message")
	}

	content := item.Content
	content.Text = item.Message
	inbound := protocol.NewInboundMessageFromContent(item.Source, cmp.Or(item.Kind, protocol.InboundKindEnqueue), &content, true)

	inbound.ConversationID = b.config.ConversationID
	if principal := strings.TrimSpace(item.Principal); principal != "" {
		inbound.Metadata[protocol.InboundPrincipalMetadataKey] = principal
	}

	inbound.Metadata["web_message_id"] = item.ID

	if item.SlackChannel != "" {
		inbound.SlackReply = &protocol.SlackReplyTarget{ChannelID: item.SlackChannel, MessageTS: item.SlackTS, ThreadTS: item.SlackTS}
	}

	if item.SlackReply != nil {
		inbound.SlackReply = new(*item.SlackReply)
	}

	startedAt := time.Now()
	err := b.enqueue(ctx, &bridgeRequest{inbound: inbound, queueItemID: item.ID}, "submit enqueued message")

	log := b.log
	if !item.StashAt.IsZero() {
		log = log.With("queue_age_ms", time.Since(item.StashAt).Milliseconds())
	}

	log.Info("queue item admission returned", "event", "queue_admitted", "conversation_id", b.config.ConversationID, "queue_item_id", item.ID, "admitted", err == nil, "duration_ms", time.Since(startedAt).Milliseconds(), "error_type", fmt.Sprintf("%T", err))

	return err
}

func (b *Bridge) submitDueScheduled(ctx context.Context, id string, armed *protocol.ScheduledMessageState, now time.Time) error {
	b.mu.Lock()
	if b.historyMutation {
		b.mu.Unlock()
		return nil
	}

	if b.stopped {
		b.mu.Unlock()
		return fmt.Errorf("submit scheduled message: %w", protocol.ErrBridgeStopped)
	}

	stored, ready, err := b.config.SessionService.ClaimScheduledMessage(id, armed.ConversationID, armed.DueAt, now)
	b.mu.Unlock()

	if err != nil {
		return fmt.Errorf("claim scheduled message: %w", err)
	}

	if !ready {
		b.log.Warn("scheduled message missing or stale at due time", "scheduled_message_id", id, "conversation_id", armed.ConversationID)
		return nil
	}

	inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, armed.Message, false)

	inbound.ConversationID, inbound.OriginHeader = b.config.ConversationID, stored.Origin
	if err := b.enqueue(ctx, &bridgeRequest{inbound: inbound, scheduledMessageID: id, scheduledMessageRecurring: stored.Recurring}, "submit scheduled message"); err != nil {
		return err
	}

	if stored.Recurring {
		b.armScheduledMessage(id, &stored)
	}

	b.log.Info("scheduled message enqueued", "event", "schedule_admitted", "scheduled_message_id", id, "conversation_id", armed.ConversationID, "due_at", armed.DueAt, "overdue_ms", now.Sub(armed.DueAt).Milliseconds(), "recurring", stored.Recurring, "queue_len", len(b.requestCh))

	return nil
}

func (b *Bridge) pickLaterWorkLogged(ctx context.Context, worker *Bridge, inbound *protocol.InboundMessage) {
	errPick := worker.PickLaterWork(ctx)
	if errPick != nil {
		b.log.Error("pick later work", "error", errPick)

		if (inbound.SyncDestination != "" || inbound.RequireOutputDecision) && !errors.Is(errPick, protocol.ErrBridgeStopped) {
			worker.armScheduledMessage("", &protocol.ScheduledMessageState{ConversationID: worker.config.ConversationID, DueAt: time.Now().Add(time.Second)})
		}
	}
}

func (b *Bridge) wakePrompt(ctx context.Context, msg *protocol.InboundMessage) error {
	ids := strings.Fields(msg.Metadata[backgroundNotesMetadataKey])
	if len(ids) == 0 {
		return nil
	}

	notes, err := b.config.SessionService.notedBackgroundJobs(ctx, b.config.ConversationID, ids[:1])
	if err != nil || len(notes) == 0 {
		return err
	}

	msg.Text, msg.Attachments = b.config.SessionService.backgroundNoteInput(&notes[0])
	msg.Metadata[backgroundNotesMetadataKey] = ids[0]

	return nil
}

func (b *Bridge) openInputWindow(ctx context.Context, request *bridgeRequest) (bool, error) {
	msg := request.inbound

	var recorded []journaledSteer

	canonical, captured, err := loadTurnInputs(ctx, b.config.SessionService.db, b.config.ConversationID, request.turnID)
	if err != nil {
		return false, inputPersistenceError{cause: err}
	}

	if data, found, err := b.config.SessionService.LoadTurnStep(ctx, b.config.ConversationID, request.turnID+"/steers"); err != nil {
		return false, inputPersistenceError{cause: err}
	} else if found {
		if err := json.Unmarshal(data, &recorded); err != nil {
			return false, inputPersistenceError{cause: fmt.Errorf("decode accepted steers: %w", err)}
		}
	}

	b.mu.Lock()

	var paused bool
	if err := queryScalar(ctx, b.config.SessionService.db, "read input pause", `SELECT COALESCE(bool_or(paused), FALSE) FROM conversations WHERE conversation_id = $1`, &paused, b.config.ConversationID); err != nil {
		b.mu.Unlock()
		return false, inputPersistenceError{cause: err}
	}

	b.inputOpen, b.activeTurnID = !hiddenRun(msg) && !b.historyMutation && !paused && !canonical.Closed && request.delivery == nil, request.turnID
	b.notesOffered = strings.Fields(msg.Metadata[backgroundNotesMetadataKey])
	b.notesOffered = b.notesOffered[:min(len(b.notesOffered), 1)]

	for _, steer := range recorded {
		if !slices.ContainsFunc(b.steers, func(request bridgeRequest) bool { return request.queueItemID == steer.ID }) {
			b.steers = append(b.steers, bridgeRequest{inbound: steer.Inbound, queueItemID: steer.ID, completion: &turnCompletion{done: make(chan struct{})}})
		}
	}

	b.mu.Unlock()

	return captured, nil
}

func (b *Bridge) handleInbound(ctx context.Context, request *bridgeRequest) (err error) {
	msg := request.inbound

	captured, err := b.openInputWindow(ctx, request)
	if err != nil {
		return err
	}

	defer func() {
		err = b.settleSteers(ctx, err)
	}()

	if request.delivery != nil {
		if err := b.notifyShadowInputs(ctx, request.turnID); err != nil {
			return inputPersistenceError{cause: err}
		}

		if err := b.deliver(ctx, request, request.delivery, 0); err != nil {
			return err
		}

		return b.finishGoalTurn(ctx, request)
	}

	if stopped, err := b.finishStoppedShadow(ctx, request); err != nil || stopped {
		return err
	}

	if msg.GoalAction == protocol.GoalActionContinue && !captured {
		if skipped, err := b.skipObsoleteGoal(ctx, request); skipped || err != nil {
			return err
		}
	}

	turnID := request.turnID
	started := time.Now()
	result := runResult{turnID: turnID}
	finish := turnFinish{conversationID: strings.TrimSpace(b.config.ConversationID)}
	prepare := !captured && !b.shadow

	var errLog, errTurn error
	if prepare {
		errTurn = b.wakePrompt(ctx, msg)
	}

	slackChannel, slackMessageTS, slackThreadTS := "", "", ""
	if reply := msg.SlackReply; reply != nil {
		slackChannel, slackMessageTS, slackThreadTS = reply.ChannelID, reply.MessageTS, reply.ThreadTS
	}

	b.log.Info("starting rocketcode turn", "event", "turn_started", "conversation_id", b.config.ConversationID, "turn_id", turnID, "queue_item_id", request.queueItemID, "scheduled_message_id", request.scheduledMessageID, "source", msg.Source, "kind", msg.Kind, "text_len", len([]rune(msg.Text)), "attachment_count", len(msg.Attachments), "slack_channel", slackChannel, "slack_message_ts", slackMessageTS, "slack_thread_ts", slackThreadTS)

	defer func() {
		outcome := "completed"
		if errors.Is(errLog, errTurnInterrupted) && err == nil {
			outcome = "stopped"
		} else if errLog != nil {
			outcome = "failed"
		}

		b.log.Info("finished rocketcode turn", "event", "turn_finished", "outcome", outcome, "conversation_id", b.config.ConversationID, "turn_id", turnID, "duration_ms", time.Since(started).Milliseconds(), "text_len", len([]rune(result.text)), "error_type", fmt.Sprintf("%T", errLog))
	}()

	if fallback := attachmentFallback(msg); prepare && fallback != "" {
		b.publishConsumed(ctx, msg, "")

		result.text = fallback
		errLog = b.finish(ctx, request, &finish, &result, "")

		return errLog
	}

	if errTurn == nil {
		result, errTurn = b.runTurn(ctx, msg, turnID, &finish)
	}

	if errTurn != nil {
		if _, persistence := errors.AsType[inputPersistenceError](errTurn); persistence {
			return errTurn
		}

		if ctx.Err() != nil && !errors.Is(errTurn, errTurnInterrupted) {
			errLog = errTurn

			return fmt.Errorf("leave turn %q for restart: %w", turnID, errors.Join(protocol.ErrBridgeStopped, errTurn))
		}

		if moved, ok := errors.AsType[turnMovedError](errTurn); ok {
			return b.handOver(ctx, msg.SlackReply, moved)
		}

		if errors.Is(errTurn, errTurnInterrupted) {
			result = runResult{turnID: turnID}
			errPublish := b.finish(ctx, request, &finish, &result, protocol.TerminalStopped)
			errLog = errors.Join(errTurn, errPublish)

			return errPublish
		}

		b.log.Error("run rocketcode turn", "error_type", fmt.Sprintf("%T", errTurn))

		result = runResult{turnID: turnID, text: internalErrorResponse + "\n\n" + errTurn.Error()}
		errLog = errors.Join(errTurn, b.finish(ctx, request, &finish, &result, protocol.TerminalFailed))

		return errLog
	}

	finish.accountGoal = msg.GoalAction != protocol.GoalActionNone
	if errPublish := b.finish(ctx, request, &finish, &result, ""); errPublish != nil {
		errLog = errPublish
		return errPublish
	}

	if errGoal := b.finishGoalTurn(ctx, request); errGoal != nil {
		errLog = errGoal
		return errGoal
	}

	return nil
}

func (b *Bridge) moveRatcheted(ctx context.Context, turnID string, origin *protocol.InboundMessage, summarize bool) (string, error) {
	store, quiet := b.config.SessionService, context.WithoutCancel(ctx)

	_, ratcheted, err := store.LoadTurnStep(quiet, b.config.ConversationID, turnID+respondInThreadStep)
	if err != nil {
		return "", inputPersistenceError{cause: err}
	}

	if !ratcheted {
		return "", nil
	}

	reply, agent := origin.SlackReply, b.agentSnapshot()

	thread, err := slackSessionID(quiet, store.db, reply.ChannelID, reply.MessageTS, "", agent)
	if err != nil {
		return "", inputPersistenceError{cause: err}
	}

	summary := ""
	if summarize {
		if summary = b.channelSummary(ctx, turnID); ctx.Err() != nil {
			return "", fmt.Errorf("summarize before moving turn %q: %w", turnID, ctx.Err())
		}
	}

	note := "The previous request moved into Thread Session " + strconv.Quote(thread)
	if url, err := b.threads.WebURL(thread); err == nil {
		note += " (" + url + ")"
	}

	note += ", the conversation of the Slack thread under its mention, where it continues and its answer posts. You can write there with rocketclaw_sessions."

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.historyMutation {
		return "", nil
	}

	if err := store.moveTurn(quiet, turnID, b.config.ConversationID, protocol.Conversation{ID: thread, Agent: agent}, conversationFacts{kind: KindSlackThread, slackChannel: reply.ChannelID, slackThread: reply.MessageTS}, summary, note); err != nil {
		return "", inputPersistenceError{cause: err}
	}

	b.inputOpen, b.notesOpen = false, false

	return thread, nil
}

func (b *Bridge) channelSummary(ctx context.Context, turnID string) string {
	runtimeCfg := b.runtime.Clone()

	var (
		anchor     int64
		transcript strings.Builder
	)

	agents, err := LoadRuntimeAgents(b.runtime, runtimeCfg.RuntimeDirName())
	if err == nil {
		err = queryScalar(ctx, b.config.SessionService.db, "read turn history anchor", `SELECT history_anchor_id FROM turns WHERE id = $1`, &anchor, turnID)
	}

	entries, errEntries := b.config.SessionService.ObserveEntries(ctx, b.config.ConversationID)
	err = errors.Join(err, errEntries)

	for i := range entries {
		if entries[i].ID > anchor {
			break
		}

		messages, errMessages := replayInputMessages(entries[i].Entry.ReplayInput)
		err = errors.Join(err, errMessages)

		for _, message := range messages {
			fmt.Fprintf(&transcript, "%s: %s\n", message.role, message.text)
		}
	}

	summary := ""
	if agent := agents.Items[b.agentSnapshot()]; err == nil && transcript.Len() > 0 {
		if summary, err = harness.GenerateSummary(ctx, newModelResolver(runtimeCfg, b.log), agent.Model, agent.ReasoningEffort, transcript.String()); err == nil {
			summary = "This request came from Channel Session " + strconv.Quote(b.config.ConversationID) + ", the Slack channel's own conversation. Its earlier history, summarized:\n" + summary
		}
	}

	if err != nil {
		b.log.Warn("summarize the Channel Session for its Thread Session", "conversation_id", b.config.ConversationID, "turn_id", turnID, "error", err)
	}

	return summary
}

func (b *Bridge) handOver(ctx context.Context, reply *protocol.SlackReplyTarget, moved turnMovedError) error {
	if moved.stopped {
		thread, err := b.threads.recordedBridge(ctx, moved.thread)
		if err != nil {
			return err
		}

		thread.InterruptActiveTurn()
	}

	b.wakeHeldThreads(ctx, reply)

	return nil
}

func (b *Bridge) skipObsoleteGoal(ctx context.Context, request *bridgeRequest) (bool, error) {
	goal, ok, err := b.config.SessionService.Goal(b.config.ConversationID)
	if err != nil {
		return false, inputPersistenceError{cause: fmt.Errorf("load goal continuation state: %w", err)}
	}

	if ok && strings.TrimSpace(goal.Status) == GoalStatusActive {
		return false, nil
	}

	outbound := protocol.NewOutboundMessage(b.config.ConversationID, "")
	if _, err := b.config.SessionService.finishTurn(ctx, request.turnID, &turnFinish{conversationID: strings.TrimSpace(b.config.ConversationID), outbound: outbound}); err != nil {
		return true, err
	}

	if err := b.notifyShadowInputs(ctx, request.turnID); err != nil {
		return true, inputPersistenceError{cause: err}
	}

	request.inbound.CompleteResponseWithAttachments("", nil, nil)

	return true, b.config.SessionService.closeTurn(context.WithoutCancel(ctx), request.turnID)
}

func (b *Bridge) finish(ctx context.Context, request *bridgeRequest, finish *turnFinish, result *runResult, terminal protocol.Terminal) error {
	msg := request.inbound

	outbound := b.newOutboundMessage(msg, result.turnID, result.text, true)
	if !b.shadow {
		snapshot, _, err := loadTurnInputs(ctx, b.config.SessionService.db, b.config.ConversationID, request.turnID)
		if err != nil {
			return inputPersistenceError{cause: err}
		}

		outbound.Agent = cmp.Or(snapshot.Agent, outbound.Agent)
	}

	outbound.Agent = cmp.Or(result.attribution.Agent, outbound.Agent)
	outbound.Model, outbound.ReasoningEffort = result.attribution.Model, result.attribution.ReasoningEffort
	outbound.Terminal = cmp.Or(terminal, protocol.TerminalComplete)
	outbound.Attachments = protocol.CloneOutboundAttachments(result.attachments)
	outbound.GoalComplete = result.goalCompleted

	if terminal != "" && msg.SyncDestination == "" {
		outbound.Cronjob = nil
	}

	finish.outbound, finish.terminal, finish.hidden = outbound, terminal, hiddenRun(msg)

	outbound, err := b.config.SessionService.finishTurn(context.WithoutCancel(ctx), request.turnID, finish)
	if err != nil {
		return fmt.Errorf("finish turn %q: %w", request.turnID, err)
	}

	if err := b.notifyShadowInputs(ctx, request.turnID); err != nil {
		return inputPersistenceError{cause: err}
	}

	if err := b.threads.armSchedules(ctx, finish.target, finish.schedules); err != nil {
		return err
	}

	return b.deliver(ctx, request, outbound, finish.retry)
}

func (b *Bridge) deliver(ctx context.Context, request *bridgeRequest, outbound *protocol.OutboundMessage, retry int) error {
	msg := request.inbound
	if b.shadow {
		return b.config.SessionService.closeTurn(context.WithoutCancel(ctx), request.turnID)
	}

	if retry == 1 {
		outbound.Text = strings.TrimSpace(outbound.Text + "\n\n" + wakeRetryNotice)
	}

	b.mu.Lock()
	b.inputOpen = false
	b.mu.Unlock()

	startedAt := time.Now()
	if err := b.bus.PublishOutbound(context.WithoutCancel(ctx), outbound); err != nil {
		b.log.Info("final delivery publication failed", "event", "delivery_acknowledged", "boundary", "bus_publish", "conversation_id", b.config.ConversationID, "turn_id", request.turnID, "acknowledged", false, "duration_ms", time.Since(startedAt).Milliseconds(), "error_type", fmt.Sprintf("%T", err))
		msg.CompleteResponseWithAttachments("", nil, err)

		return fmt.Errorf("publish final outbound message: %w", err)
	}

	b.log.Info("final delivery acknowledgement returned", "event", "delivery_acknowledged", "boundary", "frontend_ack", "conversation_id", b.config.ConversationID, "turn_id", request.turnID, "acknowledged", true, "duration_ms", time.Since(startedAt).Milliseconds(), "error_type", "<nil>")

	if outbound.Cronjob != nil && msg.SyncDestination == "" && (strings.TrimSpace(outbound.Text) != "" || len(outbound.Attachments) > 0) {
		if err := b.openCronReport(ctx, outbound); err != nil {
			msg.CompleteResponseWithAttachments(outbound.Text, outbound.Attachments, err)
			return err
		}
	}

	msg.CompleteResponseWithAttachments(outbound.Text, outbound.Attachments, nil)

	if err := b.config.SessionService.closeTurn(context.WithoutCancel(ctx), request.turnID); err != nil {
		return err
	}

	b.wakeHeldThreads(context.WithoutCancel(ctx), msg.SlackReply)

	if msg.SyncDestination == "" || retry > 1 && msg.Metadata[backgroundNotesMetadataKey] != "" {
		return nil
	}

	reply := protocol.CloneOutboundMessage(outbound)
	reply.ConversationID = msg.SyncDestination

	if err := b.bus.PublishOutbound(context.WithoutCancel(ctx), reply); err != nil {
		return fmt.Errorf("publish run reply: %w", err)
	}

	return nil
}

func (b *Bridge) openCronReport(ctx context.Context, outbound *protocol.OutboundMessage) error {
	agents, err := LoadRuntimeAgents(b.runtime, b.runtime.Clone().RuntimeDirName())
	if err != nil {
		return fmt.Errorf("load cron agents: %w", err)
	}

	agent := agents.Items[outbound.Cronjob.Agent]
	destination := protocol.Conversation{ID: "web:" + b.config.ConversationID, Agent: outbound.Cronjob.Agent, CreatedBy: string(ThreadCreatedByCron), Space: cmp.Or(outbound.Cronjob.Space, agent.Space)}
	facts := conversationFacts{kind: KindWebChat, cronRun: b.config.ConversationID}

	if agent.Sticky != harness.AgentStickyOff {
		destination.ID, facts.cronRun = rand.Text(), ""
	}

	ctx = context.WithoutCancel(ctx)
	if _, err := b.config.SessionService.recordConversation(ctx, &destination, facts, agent.Sticky); err != nil {
		return fmt.Errorf("record cron report chat: %w", err)
	}

	tx, err := b.config.SessionService.beginStateTx(ctx, "cron destination binding")
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockHistories(ctx, tx, []string{b.config.ConversationID, destination.ID}); err != nil {
		return err
	}

	destinationID, schedules, err := (stateDAO{db: tx}).bindRun(ctx, b.config.ConversationID, destination.ID)
	if err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit cron destination binding: %w", err)
	}

	return b.threads.armSchedules(ctx, destinationID, schedules)
}

func (b *Bridge) settleSteers(ctx context.Context, err error) error {
	b.mu.Lock()

	b.inputOpen, b.activeTurnID = false, ""
	if _, persistence := errors.AsType[inputPersistenceError](err); persistence {
		b.mu.Unlock()
		return err
	}

	steers := b.steers

	b.steers, b.steersRead, b.notesOffered, b.movedNotes = nil, 0, nil, nil
	if b.historyMutation {
		for i := range steers {
			err = errors.Join(err, b.preserveRevertRequestLocked(context.WithoutCancel(ctx), &steers[i]))
		}

		steers = nil
	}
	b.mu.Unlock()

	if ctx.Err() == nil {
		for i := range steers {
			steer := &steers[i]
			steer.completion.err = err
			close(steer.completion.done)
		}
	}

	return err
}

func (b *Bridge) turnInputs(ctx context.Context, turnID string) (snapshot turnInputSnapshot, captured bool, err error) {
	if b.shadow {
		snapshot, captured, err = b.config.SessionService.shadowInputs(ctx, b.config.ConversationID, turnID)
	} else {
		snapshot, captured, err = loadTurnInputs(ctx, b.config.SessionService.db, b.config.ConversationID, turnID)
	}

	if err != nil {
		err = inputPersistenceError{cause: err}
	}

	return snapshot, captured, err
}

func (b *Bridge) finishGoalTurn(ctx context.Context, request *bridgeRequest) error {
	msg := request.inbound

	goal, ok, err := b.config.SessionService.Goal(b.config.ConversationID)
	if err != nil {
		return fmt.Errorf("load goal after turn: %w", err)
	}

	if !ok || strings.TrimSpace(goal.Status) != GoalStatusActive {
		return nil
	}

	inbound := goalContinuation(b.config.ConversationID, &goal)
	inbound.Text += "\n\n" + goalSteeringPrompt(&goal)

	if msg != nil && msg.SlackReply != nil {
		*inbound.SlackReply = *msg.SlackReply
	}

	if err := b.enqueue(ctx, &bridgeRequest{inbound: inbound, completion: request.completion}, "submit goal continuation"); err != nil {
		return err
	}

	request.completion = nil

	return nil
}

func turnReplayEntries(observed []ObservedSessionEntry, restored, contextEntries []harness.SessionEntry) iter.Seq2[harness.SessionEntry, error] {
	return func(yield func(harness.SessionEntry, error) bool) {
		for i := range observed {
			if (len(restored) == 0 || observed[i].Entry.Type != externalMCPMetadataEntryType) && !yield(observed[i].Entry, nil) {
				return
			}
		}

		for _, entries := range [][]harness.SessionEntry{restored, contextEntries} {
			for i := range entries {
				entry := entries[i]

				entry.Timestamp = time.Now().UTC()
				if !yield(entry, nil) {
					return
				}
			}
		}
	}
}

//nolint:gocyclo // Turn execution coordinates model, tools, progress, and goal accounting.
func (b *Bridge) runTurn(ctx context.Context, msg *protocol.InboundMessage, turnID string, finish *turnFinish) (result runResult, err error) {
	runtimeCfg := b.runtime.Clone()

	var header string
	defer func() {
		if header == "" {
			b.publishConsumed(ctx, msg, "")
		}
	}()

	snapshot, captured, err := b.turnInputs(ctx, turnID)
	if err != nil {
		return runResult{}, err
	}

	agentName := b.agentSnapshot()
	if !b.shadow {
		agentName = cmp.Or(snapshot.Agent, agentName)
	}

	ctx = instrumentation.WithUser(instrumentation.WithSession(ctx, b.config.ConversationID), cmp.Or(msg.Metadata[protocol.InboundPrincipalEmailMetadataKey], msg.Metadata[protocol.InboundPrincipalMetadataKey]))

	tracer := noop.NewTracerProvider().Tracer("rocketclaw")
	if runtimeCfg.PostHog.ProjectToken != "" {
		tracer = otel.Tracer("rocketclaw")
	}

	ctx, span := tracer.Start(ctx, "rocketclaw.turn")

	instrumentation.ApplyContextAttributes(ctx, span)
	span.SetAttributes(attribute.String(semconv.OpenInferenceSpanKind, semconv.SpanKindAgent), otelsemconv.GenAIOperationNameInvokeAgent)
	span.SetAttributes(
		attribute.String(semconv.AgentName, agentName),
		otelsemconv.GenAIAgentName(agentName),
		attribute.String(semconv.SessionID, b.config.ConversationID),
		otelsemconv.GenAIConversationID(b.config.ConversationID),
		attribute.String("rocketclaw.conversation_id", b.config.ConversationID),
		attribute.String("rocketclaw.turn_id", turnID),
		attribute.String("rocketclaw.source", string(msg.Source)),
		attribute.String("rocketclaw.kind", string(msg.Kind)),
		attribute.Int("rocketclaw.attachment_count", len(msg.Attachments)),
		attribute.String(semconv.InputValue, instrumentation.RedactedValue),
		attribute.String("$ai_input_state", instrumentation.RedactedValue),
	)

	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}

		span.SetAttributes(
			attribute.String(semconv.OutputValue, instrumentation.RedactedValue),
			attribute.String("rocketclaw.response_id", result.responseID),
			attribute.String("rocketclaw.model", result.attribution.Model),
		)
		span.End()
	}()

	root, err := os.OpenRoot(runtimeCfg.Workspace)
	if err != nil {
		return runResult{}, fmt.Errorf("open workspace root: %w", err)
	}

	shared := &turnRoot{root: root, users: 1}
	defer shared.release()

	self, rootAgent, replyTurn, origin := b.config.ConversationID, agentName, turnID, msg

	var jobs []backgroundKey

	if b.parent != "" {
		if self, replyTurn, origin, jobs, err = b.config.SessionService.subagentLineage(ctx, b.config.ConversationID); err != nil {
			return runResult{}, err
		}
	}

	layer, err := agentDraftFiles(origin)
	if err != nil {
		return runResult{}, err
	}

	agents, skills, err := loadRocketCodeDefinitionsIn(root, b.runtime, runtimeCfg.RuntimeDirName(), layer)
	if err != nil {
		return runResult{}, fmt.Errorf("open workspace agent and skills: %w", err)
	}

	appendOverlayPromptToAgent(agents, agentName, runtimeCfg)

	if agent, ok := agents.Items[agentName]; ok && b.parent != "" {
		var owner *Bridge
		if owner, err = b.threads.recordedBridge(ctx, self); err != nil {
			return runResult{}, err
		}

		rootAgent, agent.MaxRecursion = owner.agentSnapshot(), nil
		if limit := agents.Items[rootAgent].MaxRecursion; limit != nil {
			agent.MaxRecursion = new(max(*limit-len(jobs), 0))
		}

		var (
			model sql.NullString
			tools []byte
		)

		if err = b.config.SessionService.db.QueryRowContext(ctx, `SELECT subagent_model, COALESCE(subagent_tools, 'null') FROM conversations WHERE conversation_id = $1`, b.config.ConversationID).Scan(&model, &tools); err != nil {
			return runResult{}, fmt.Errorf("read subagent model: %w", err)
		}

		if model.Valid {
			agent.Model, agent.ReasoningEffort = harness.ParseModelRef(model.String)
		}

		if err = json.Unmarshal(tools, &agent.Tools); err != nil {
			return runResult{}, fmt.Errorf("decode subagent tools: %w", err)
		}

		agents.Items[agentName] = agent
	}

	conversationID := strings.TrimSpace(b.config.ConversationID)

	var (
		shellEnv     map[string]string
		childContext []harness.SessionEntry
	)

	if captured {
		if !b.shadow {
			msg.GoalTurn = snapshot.GoalTurn
		}

		shellEnv, childContext = snapshot.Environment, snapshot.ChildContext
	} else {
		if goal, ok, err := b.config.SessionService.Goal(b.config.ConversationID); err != nil {
			return runResult{}, fmt.Errorf("load active goal note: %w", err)
		} else if ok && strings.TrimSpace(goal.Status) == GoalStatusActive {
			msg.GoalTurn = true

			if note := strings.TrimSpace(goal.Note); note != "" {
				replayInput := replayInputForMessage("developer", "RocketClaw goal state:\nStatus: progress\nLast reported note:\n"+note)

				entry := harness.SessionEntry{Version: 1, Type: "goal_state", ReplayInput: replayInput}
				snapshot.ContextEntries = append(snapshot.ContextEntries, entry)
			}
		}

		if shellEnv, childContext, err = b.turnShellEnv(ctx, self, origin, &snapshot.ContextEntries); err != nil {
			return runResult{}, err
		}
	}

	var channel string

	if msg.Source == protocol.SourceSlack && !msg.GoalTurn {
		slackChannel, _, channelSession, err := b.config.SessionService.SlackThread(ctx, b.config.ConversationID)
		if err != nil {
			return runResult{}, err
		}

		if channelSession {
			channel = slackChannel
		}
	}

	if channel != "" && !captured {
		b.threads.mu.Lock()
		slack := b.threads.slackLookups
		b.threads.mu.Unlock()

		name, _, err := slack.SidebarChannelAgentChoices(ctx, channel)
		if err != nil {
			return runResult{}, fmt.Errorf("read Slack channel name: %w", err)
		}

		note := "This is the Channel Session of Slack channel #" + name + " (ID " + channel + "). Your answer posts at the top of the channel. Each mention is one person's request. To answer in the mention's thread instead, call slack_respond_in_thread(true) in its own execute call before other work."
		snapshot.ContextEntries = append(snapshot.ContextEntries, harness.SessionEntry{Version: 1, ReplayInput: replayInputForMessage("developer", note)})
	}

	shellTempRel := rocketcodeShellTempRel(runtimeCfg.RuntimeDirName(), b.shellEnvFor(shellEnv)[rocketclawConversationIDEnv])
	if err := root.MkdirAll(shellTempRel, 0o700); err != nil {
		return runResult{}, fmt.Errorf("create rocketcode shell temp dir: %w", err)
	}

	shellTempDir := filepath.Join(runtimeCfg.Workspace, filepath.FromSlash(shellTempRel))

	providerLog := b.log.With("conversation_id", b.config.ConversationID, "turn_id", turnID, "agent", agentName, "source", string(msg.Source), "kind", string(msg.Kind), "human", msg.Human, "goal_turn", msg.GoalTurn, "attachment_count", len(msg.Attachments))
	resolver := newModelResolver(runtimeCfg, providerLog)

	observed, err := b.config.SessionService.ObserveEntries(ctx, b.config.ConversationID)
	if err != nil {
		return runResult{}, fmt.Errorf("load rocketcode session history metrics: %w", err)
	}

	replayItemCount, historyBytes, compactionCount, latestEntryID, latestEntryType := 0, 0, 0, int64(0), ""
	for i := range observed {
		latestEntryID, latestEntryType = observed[i].ID, observed[i].Entry.Type
		for j := range observed[i].Entry.ReplayInput {
			raw := observed[i].Entry.ReplayInput[j]
			replayItemCount++

			historyBytes += len(raw)
			if harness.ReplayInputRawKind(raw) == "compaction" {
				compactionCount++
			}
		}
	}

	b.log.Info("prepared rocketcode session history", "conversation_id", b.config.ConversationID, "turn_id", turnID, "entry_count", len(observed), "replay_item_count", replayItemCount, "history_bytes", historyBytes, "compaction_count", compactionCount, "latest_entry_id", latestEntryID, "latest_entry_type", latestEntryType)

	var restored []harness.SessionEntry
	if captured && len(childContext) > 0 {
		restored = childContext[:1]
	}

	sessionIn := turnReplayEntries(observed, restored, snapshot.ContextEntries)

	tagConversationID := cmp.Or(origin.SyncDestination, self)

	if err := b.setEasyShell(ctx, runtimeCfg, agents, skills, agentName, b.shellEnvFor(shellEnv), self); err != nil {
		return runResult{}, err
	}

	agent := agents.Items[agentName]

	customTools := append(sessionTagTools(b.config.SessionService, tagConversationID), currentSessionIDTool(self))
	if agents.Items[rootAgent].Space == "" {
		customTools = append(customTools, sessionsMoveTool(b.config.SessionService, self))
	}

	if b.parent == "" {
		customTools = append(customTools, b.scheduleMessageTool(origin), b.resetScheduledMessagesTool(origin))
	}

	if replyTurn != "" || slices.ContainsFunc(jobs, b.background.detached) {
		customTools = append(customTools, attachFilesTool(root, &agent.CustomShell, b.config.SessionService, b.config.ConversationID, replyTurn))
	}

	if agent.Permission.Enabled("rocketclaw", restartToolName) {
		customTools = append(customTools, restartTool(b.config.RequestRestart))
	}

	for i := range customTools {
		if tool := customTools[i]; tool.TurnBound && b.parent != "" {
			customTools[i].Call = func(ctx context.Context, raw json.RawMessage, output chan<- harness.ChatResponse) (harness.ToolResult, error) {
				if slices.ContainsFunc(jobs, b.background.detached) {
					return harness.TextToolResult(fmt.Sprintf(harness.TurnBoundRefused, tool.Name)), nil
				}

				return tool.Call(ctx, raw, output)
			}
		}
	}

	originHeader := reviewOriginHeader(origin)

	if msg.Human && msg.Source == protocol.SourceWeb {
		customTools = append(customTools, askUserQuestionTool(b.config.UserQuestionAsker, msg), navigateTool(b.threads, msg.Metadata[protocol.InboundPrincipalEmailMetadataKey]))
	}

	if channel != "" {
		customTools = append(customTools, respondInThreadTool(b.config.SessionService, b.config.ConversationID, turnID))
	}

	if runtimeCfg.Slack.FeedbackChannel != "" {
		customTools = append(customTools, harnessFeedbackTool(b.config.SessionService, b.threads, resolver, runtimeCfg, agentName, self))
	}

	customTools = append(customTools, sessionsWriteTool(b, origin, msg, rootAgent, self, agents))
	customTools = append(customTools, pageSpace{sessions: b.config.SessionService, threads: b.threads, agent: rootAgent, space: agents.Items[rootAgent].Space}.nativeTools(root, &agent.CustomShell)...)

	rocketcodeConfig := b.rocketcodeConfig(shellTempDir, shellEnv, customTools...)

	rocketcodeConfig.ChildContext, rocketcodeConfig.OriginHeader = childContext, originHeader

	switch {
	case layer != nil:
		rocketcodeConfig.CustomTools = slices.DeleteFunc(rocketcodeConfig.CustomTools, func(tool harness.Tool) bool {
			return slices.Contains([]string{sessionsWriteToolName, scheduleMessageToolName, resetScheduledMessagesToolName, updateGoalToolName}, tool.Name)
		})
	case !b.shadow && !b.underShadow:
		rocketcodeConfig.BackgroundJobs = backgroundTurn{registry: b.background, root: shared, conversationID: b.config.ConversationID, origin: msg}
	}

	looper, err := harness.NewWithModelResolver(resolver, &rocketcodeConfig, root, agents, skills, agentName)
	if err != nil {
		return runResult{}, fmt.Errorf("prepare rocketcode turn: %w", err)
	}

	looper.SteerDrain = b
	result = runResult{turnID: turnID, attribution: harness.ReplayAttribution{Agent: agentName, Model: looper.DisplayModel, ReasoningEffort: new(string(looper.ReasoningEffort))}}

	input := make(chan harness.PromptInput, 1)
	output := make(chan harness.ChatResponse, 128)
	interrupts := make(chan os.Signal, 1)

	activeReply := new(protocol.InboundMessage)

	activeReply.SyncDestination = msg.SyncDestination
	activeReply.RequireOutputDecision = msg.RequireOutputDecision
	activeReply.SlackReply = protocol.Clone(msg.SlackReply)

	turnCtx, cancelTurn := context.WithCancel(ctx)

	b.mu.Lock()
	b.activeReply = activeReply
	b.activeAttribution = result.attribution
	b.activeTurnInterrupts = interrupts
	b.activeTurnCancel = cancelTurn
	b.activeTurnInterrupted = b.activeTurnInterrupted || b.historyMutation

	b.notesOpen = !b.activeTurnInterrupted
	if b.activeTurnInterrupted {
		interrupts <- os.Interrupt

		cancelTurn()
	}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		b.activeReply = nil
		b.activeAttribution = harness.ReplayAttribution{}
		b.activeTurnInterrupts = nil
		b.activeTurnCancel = nil
		b.activeTurnInterrupted = false
		b.notesOpen = false
		b.mu.Unlock()
		cancelTurn()
	}()

	if b.shadow {
		var stopped bool
		if err := b.config.SessionService.db.QueryRowContext(ctx, `SELECT terminal = $3 FROM shadow_turns WHERE child_conversation_id = $1 AND child_turn_id = $2`, b.config.ConversationID, turnID, protocol.TerminalStopped).Scan(&stopped); err != nil {
			return runResult{}, inputPersistenceError{cause: fmt.Errorf("read active shadow stop: %w", err)}
		}

		if stopped {
			return runResult{}, errTurnInterrupted
		}
	}

	var prepared harness.PromptInput

	if captured && len(snapshot.Inputs) == 0 {
		b.log.Error("turn has captured input but no prompt", "conversation_id", b.config.ConversationID, "turn_id", turnID, "shadow", b.shadow,
			"agent", snapshot.Agent, "closed", snapshot.Closed, "goal_turn", snapshot.GoalTurn,
			"context_entries", len(snapshot.ContextEntries), "child_context", len(snapshot.ChildContext))

		return runResult{}, emptyTurnInputsError{TurnID: turnID}
	}

	if captured {
		prepared = snapshot.Inputs[0]

		if b.shadow && layer != nil {
			var data []byte
			if err := queryScalar(ctx, b.config.SessionService.db, "read shadow admission", `SELECT inbound_json FROM shadow_turns WHERE child_conversation_id = $1 AND child_turn_id = $2`, &data, b.config.ConversationID, turnID); err != nil {
				return runResult{}, err
			}

			parent := new(protocol.InboundMessage)
			if err := json.Unmarshal(data, parent); err != nil {
				return runResult{}, fmt.Errorf("decode shadow admission: %w", err)
			}

			prepared.Texts = []string{buildPrompt(parent, &agent, false)}
			prepared.Header, _, _ = strings.Cut(prepared.Texts[0], "\n\n")
		}
	} else {
		agent := agents.Items[agentName]

		prompt, err := b.buildPrompt(msg, &agent)
		if err != nil {
			return runResult{}, err
		}

		header, _, _ = strings.Cut(prompt, "\n\n")
		prepared = harness.PromptInput{ID: cmp.Or(msg.Metadata["web_message_id"], msg.Metadata[backgroundNotesMetadataKey], msg.Metadata[agentDraftMessageMetadataKey], turnID+"/initial"), TurnID: turnID, Texts: []string{prompt}, Header: header, Attachments: attachmentsFromInbound(msg.Attachments), DirectSkill: inboundDirectSkill(msg)}
		prepared.ExpandShellCommands = msg.Cronjob != nil && msg.Metadata[backgroundNotesMetadataKey] == ""
		snapshot.Inputs, snapshot.Environment, snapshot.ChildContext = []harness.PromptInput{prepared}, shellEnv, childContext

		snapshot.GoalTurn = msg.GoalTurn
		if err := b.config.SessionService.captureTurnInputs(ctx, b.config.ConversationID, turnID, &snapshot); err != nil {
			return runResult{}, err
		}
	}

	if err := b.notifyShadowInputs(ctx, turnID); err != nil {
		return runResult{}, inputPersistenceError{cause: err}
	}

	if !b.shadow {
		if err := b.startShadowWorkers(ctx, turnID); err != nil {
			return runResult{}, inputPersistenceError{cause: err}
		}
	}

	header = prepared.Header
	b.publishConsumed(ctx, msg, header)

	if !b.shadow {
		outbound := b.newOutboundMessage(msg, turnID, "", false)

		outbound.Agent = agentName
		if err := b.bus.PublishOutbound(ctx, outbound); err != nil {
			return result, fmt.Errorf("publish rocketcode turn start: %w", err)
		}
	}

	if request := strings.TrimSpace(cmp.Or(msg.Metadata[protocol.InboundRawTextMetadataKey], msg.Text)); !captured && len(observed) == 0 && msg.Human && msg.Source == protocol.SourceWeb && !b.shadow && layer == nil && request != "" {
		b.titles.Go(func() {
			var name string
			if queryScalar(ctx, b.config.SessionService.db, "read session name", `SELECT name FROM conversations WHERE conversation_id = $1`, &name, b.config.ConversationID) == nil && name != "" {
				return
			}

			title, err := harness.GenerateTitle(ctx, resolver, runtimeCfg.TitleModel, request, "")
			if err == nil {
				_, err = execState(ctx, b.config.SessionService.db, "name session", `UPDATE conversations SET name = $2 WHERE conversation_id = $1 AND name = ''`, b.config.ConversationID, title)
			}

			if err != nil {
				b.log.Warn("name session from its first message", "conversation_id", b.config.ConversationID, "error", err)
			}
		})
	}

	prepared.TurnID, prepared.Responses = turnID, output
	input <- prepared

	close(input)

	var group errgroup.Group

	staged := new(memoryStore)

	b.log.Info("starting rocketcode looper", "conversation_id", b.config.ConversationID, "turn_id", turnID, "agent", agentName)

	looperStarted := time.Now()

	group.Go(func() error { return looper.Loop(turnCtx, input, sessionIn, staged, interrupts) })

	firstOutput, firstText := false, false

	firstOutputTimer := time.AfterFunc(30*time.Second, func() {
		b.log.Warn("rocketcode turn has no first output yet", "conversation_id", b.config.ConversationID, "turn_id", turnID, "elapsed_ms", time.Since(looperStarted).Milliseconds(), "entry_count", len(observed), "replay_item_count", replayItemCount, "history_bytes", historyBytes, "compaction_count", compactionCount)
	})
	defer firstOutputTimer.Stop()

	for item := range output {
		if !firstOutput {
			firstOutput = true

			firstOutputTimer.Stop()
			b.log.Info("received first rocketcode response item", "event", "first_response_item", "conversation_id", b.config.ConversationID, "turn_id", turnID, "kind", item.Kind, "duration_ms", time.Since(looperStarted).Milliseconds())
		}

		if item.Kind == harness.ChatResponseAssistantMessage {
			if !firstText && item.Text != "" {
				firstText = true

				b.log.Info("received first assistant text", "event", "first_assistant_text", "conversation_id", b.config.ConversationID, "turn_id", turnID, "duration_ms", time.Since(looperStarted).Milliseconds())
			}

			result.text = appendText(result.text, item.Text)
		}
	}

	err = group.Wait()

	b.mu.Lock()
	interrupted := b.activeTurnInterrupted
	b.mu.Unlock()

	finish.conversationID = conversationID
	finish.entries = append(finish.entries, staged.entries...)

	b.log.Info("rocketcode looper returned", "event", "generation_finished", "conversation_id", b.config.ConversationID, "turn_id", turnID, "duration_ms", time.Since(looperStarted).Milliseconds(), "interrupted", interrupted, "failed", err != nil, "error_type", fmt.Sprintf("%T", err))

	if moved, ok := errors.AsType[turnMovedError](err); ok {
		moved.stopped = interrupted
		return result, moved
	}

	if interrupted {
		if thread, errMove := b.moveRatcheted(ctx, turnID, msg, false); errMove != nil || thread != "" {
			return result, cmp.Or[error](errMove, turnMovedError{thread: thread, stopped: true})
		}

		return result, errTurnInterrupted
	}

	if err != nil {
		return result, fmt.Errorf("run rocketcode turn: %w", err)
	}

	if len(staged.entries) > 0 {
		result.responseID = staged.entries[len(staged.entries)-1].ResponseID
	}

	if b.parent != "" && msg.Metadata[backgroundNotesMetadataKey] != "" {
		var taken bool
		if err := queryScalar(ctx, b.config.SessionService.db, "read wake turn's queued inputs", `SELECT count(*) > 0 FROM turns q
JOIN steps s ON s.conversation_id = q.conversation_id AND s.key = $2 AND s.value::jsonb->'applied_input_ids' ? q.input_id
WHERE q.conversation_id = $1 AND q.phase = 'waiting'`, &taken, b.config.ConversationID, turnID); err != nil {
			return result, err
		}

		if !taken {
			finish.report = harness.TaskResult(result.text)
		}
	}

	if !msg.RequireOutputDecision || strings.TrimSpace(result.text) != "" {
		if result.attachments, err = b.config.SessionService.TurnAttachments(ctx, b.config.ConversationID, turnID); err != nil {
			return result, err
		}

		if err := b.config.SessionService.loadAttachmentData(ctx, result.attachments); err != nil {
			return result, err
		}
	}

	if msg.GoalTurn {
		goal, ok, err := b.config.SessionService.Goal(b.config.ConversationID)
		if err != nil {
			return result, fmt.Errorf("load goal completion status: %w", err)
		}

		result.goalCompleted = ok && strings.TrimSpace(goal.Status) == GoalStatusComplete
	}

	return result, nil
}

func providerLogAttrs(req *http.Request, resp *http.Response, status int, duration time.Duration, err error, boundary string) []any {
	retryCount, _ := strconv.Atoi(req.Header.Get("X-Stainless-Retry-Count"))

	outcome := "success"
	if err != nil {
		outcome = "transport_error"
	} else if status < 200 || status >= 300 {
		outcome = "http_error"
	}

	event := "provider_sdk_attempt"
	if boundary == "sdk_return" {
		event = "provider_sdk_return"

		if err != nil {
			outcome = "sdk_error"
		}
	}

	attrs := []any{"event", event, "boundary", boundary, "method", req.Method, "status", status, "duration_ms", duration.Milliseconds(), "retry_count", retryCount, "outcome", outcome, "error_type", fmt.Sprintf("%T", err)}
	if resp == nil {
		return attrs
	}

	for _, key := range []string{"X-Request-ID", "X-Oai-Request-Id", "Cf-Ray"} {
		if requestID := resp.Header.Get(key); requestID != "" {
			attrs = append(attrs, "provider_request_id", requestID)
			break
		}
	}

	for _, header := range []struct{ name, field string }{
		{"Retry-After", "retry_after"}, {"Retry-After-Ms", "retry_after_ms"},
		{"X-Ratelimit-Reset-Requests", "ratelimit_reset_requests"}, {"X-Ratelimit-Reset-Tokens", "ratelimit_reset_tokens"},
	} {
		if value := resp.Header.Get(header.name); value != "" {
			attrs = append(attrs, header.field, value)
		}
	}

	return attrs
}

func rocketcodeShellTempRel(runtimeDir, conversationID string) string {
	id := cmp.Or(strings.TrimSpace(conversationID), "anonymous")

	id = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}

		return '_'
	}, id)

	return filepath.ToSlash(filepath.Join(runtimeDir, ".rocketcode", "tmp", id))
}

func (b *Bridge) turnShellEnv(ctx context.Context, self string, origin *protocol.InboundMessage, contextEntries *[]harness.SessionEntry) (map[string]string, []harness.SessionEntry, error) {
	var (
		shellEnv     map[string]string
		childContext []harness.SessionEntry
	)

	metadataEntry, err := b.config.SessionService.externalMCPMetadataEntry(ctx, self)
	if err != nil {
		return nil, nil, fmt.Errorf("load external MCP metadata: %w", err)
	}

	if metadataEntry != nil || origin.Source == protocol.SourceExternalMCP {
		metadataConversationID := self

		_, session, paired, err := b.config.SessionService.ExternalMCPSessionByConversationID(self)
		if err != nil {
			return nil, nil, fmt.Errorf("load external MCP pairing for metadata environment: %w", err)
		}

		if paired && session.PrivateConversationID != "" {
			metadataConversationID = session.PrivateConversationID
		}

		metadataEnv, ok := externalMCPStoredMetadataEnv(metadataConversationID, metadataEntry)
		if !ok {
			metadataEnv = externalMCPMetadataEnv(b.config.ConversationID, origin.ExternalMCPMetadata)
		}

		replayInput := replayInputForMessage("developer", externalMCPMetadataDeveloperMessage("This external MCP thread has metadata:", metadataEnv))

		childContext = append(childContext, harness.SessionEntry{Version: 1, ReplayInput: replayInput})
		shellEnv = metadataEnv

		if !ok {
			if _, err := b.config.SessionService.AppendEntryID(context.Background(), b.config.ConversationID, &harness.SessionEntry{Version: 1, Type: externalMCPMetadataEntryType, Timestamp: time.Now().UTC(), ReplayInput: replayInput}); err != nil {
				return nil, nil, fmt.Errorf("append external MCP metadata: %w", err)
			}
		} else {
			metadataEnv[rocketclawConversationIDEnv] = b.config.ConversationID

			transientEnv := externalMCPMetadataEnv(b.config.ConversationID, origin.ExternalMCPMetadata)
			for key := range metadataEnv {
				delete(transientEnv, key)
			}

			if len(transientEnv) > 0 {
				maps.Copy(shellEnv, transientEnv)

				replayInput := replayInputForMessage("developer", externalMCPMetadataDeveloperMessage("This external MCP turn has additional metadata:", transientEnv))

				childContext = append(childContext, harness.SessionEntry{Version: 1, ReplayInput: replayInput})

				entry := harness.SessionEntry{Version: 1, Type: externalMCPMetadataEntryType, ReplayInput: replayInput}
				*contextEntries = append(*contextEntries, entry)
			}
		}
	}

	if shellEnv == nil {
		shellEnv = map[string]string{rocketclawConversationIDEnv: b.config.ConversationID}
	}

	if name, email := origin.Metadata[protocol.InboundPrincipalNameMetadataKey], origin.Metadata[protocol.InboundPrincipalEmailMetadataKey]; name != "" {
		shellEnv["ROCKETCLAW_PRINCIPAL_NAME"] = name

		if email != "" {
			shellEnv["ROCKETCLAW_PRINCIPAL_EMAIL"], shellEnv["ROCKETCLAW_PRINCIPAL_ID"] = email, fmt.Sprintf("%x", sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email)))))
		}
	}

	return shellEnv, childContext, nil
}

func (b *Bridge) shellEnvFor(turnEnv map[string]string) map[string]string {
	if !b.shadow && !b.underShadow {
		return turnEnv
	}

	env := make(map[string]string, len(turnEnv)+1)
	maps.Copy(env, turnEnv)

	env["ROCKETCLAW_SHADOW"] = b.config.Agent
	if _, ok := turnEnv[rocketclawConversationIDEnv]; ok {
		env[rocketclawConversationIDEnv] = b.config.ConversationID
	}

	return env
}

func (b *Bridge) rocketcodeConfig(shellTempDir string, shellEnv map[string]string, customTools ...harness.Tool) harness.Config {
	runtimeCfg := b.runtime.Clone()
	tools := make([]harness.Tool, 0, 6+len(customTools))
	shellEnv = b.shellEnvFor(shellEnv)

	tools = append(tools, reloadTool(b.config.RequestReload), listSessionsTool(b.config.SessionService, b.threads), getSessionTool(b.config.SessionService), sessionsGetURLTool(b.threads), stopBackgroundJobTool(b.background, b.config.ConversationID))
	if goal, ok, err := b.config.SessionService.Goal(b.config.ConversationID); err == nil && ok && strings.TrimSpace(goal.Status) == GoalStatusActive {
		tools = append(tools, updateGoalTool(b.config.SessionService, b.config.ConversationID))
	}

	if len(runtimeCfg.OpenCode) > 0 {
		tools = append(tools, openCodeAPITool(runtimeCfg.OpenCode))
	}

	tools = append(tools, customTools...)

	if b.shadow || b.underShadow {
		tools = slices.DeleteFunc(tools, func(tool harness.Tool) bool {
			return slices.Contains([]string{askUserQuestionToolName, navigateToolName, sessionsWriteToolName, scheduleMessageToolName, resetScheduledMessagesToolName, updateGoalToolName, reloadToolName, restartToolName}, tool.Name)
		})
	}

	return harness.Config{AutoApproverModel: runtimeCfg.AutoApproverModel, AutoApproverDecisionModel: runtimeCfg.AutoApproverDecision.Model, AutoApproverMinConfidence: runtimeCfg.AutoApproverDecision.MinConfidence, Deciders: newDeciders(runtimeCfg), ShellTempDir: shellTempDir, SpillDir: rocketcodeSpillDir(runtimeCfg), RetainedResultDir: rocketcodeRetainedDir(b.config.ConversationID), ExpandPromptShellCommands: harness.PromptShellCommandExpansion{PrimaryPrompts: true, SubagentPrompts: true, SkillPrompts: true}, ParallelToolCalls: 16, Observability: harness.ObservabilityConfig{Enabled: runtimeCfg.PostHog.ProjectToken != "", Tracer: otel.Tracer("rocketcode")}, ChildSessions: b, Journal: conversationJournal{store: b.config.SessionService, conversationID: b.config.ConversationID, log: b.log}, BackgroundJobs: harness.InertBackgroundJobs{}, CustomTools: tools, ShellEnv: shellEnv, Sandbox: *b.config.SessionService.shells.current(), MCPServers: toMCPClientServers(runtimeCfg.MCPServers), MCPWorkspace: runtimeCfg.Workspace}
}

func toMCPClientServers(servers map[string]config.MCPServerConfig) map[string]mcpclient.ServerConfig {
	if len(servers) == 0 {
		return nil
	}

	out := make(map[string]mcpclient.ServerConfig, len(servers))
	for name := range servers {
		server := servers[name]
		out[name] = mcpclient.ServerConfig{Command: server.Command, Args: server.Args, Env: server.Env, Cwd: server.Cwd, URL: server.URL, Headers: server.Headers}
	}

	return out
}

func appendOverlayPromptToAgent(agents harness.Agents, agentName string, cfg *config.Config) {
	section := overlayPromptSection(cfg, skel.OverlayInfos(filepath.Join(cfg.Workspace, cfg.RuntimeDirName()), cfg.Overlays))
	if section == "" {
		return
	}

	agent, ok := agents.Items[agentName]
	if !ok {
		return
	}

	agent.Prompt = strings.TrimSpace(agent.Prompt + "\n\n" + section)
	agents.Items[agentName] = agent
}

func overlayPromptSection(cfg *config.Config, overlays []skel.OverlayInfo) string {
	if len(overlays) == 0 {
		return ""
	}

	lines := []string{
		"## Runtime Overlays",
		"",
		"Overlays are configured git repositories whose agents/, skills/, cron/, and scripts/ trees are merged into this RocketClaw runtime at startup. They let shared runtime assets be maintained outside this workspace. Effective runtime assets are built from embedded assets first, then configured overlays in selected runtime config order, then local workspace overlays last.",
		"",
		"Configured overlays, in application order:",
	}

	for _, info := range overlays {
		lines = append(lines,
			"- "+info.Spec,
			"  Git URL: "+info.URL,
			"  Ref: "+cmp.Or(info.Ref, "HEAD"),
			"  Clone path: "+info.ClonePath,
		)
	}

	lines = append(lines,
		"",
		"To update an overlay:",
		"- Edit the listed clone path when the requested change belongs to that overlay.",
		"- Commit and push overlay repository changes before reload or restart.",
		"- Every clone under "+filepath.Join(cfg.RuntimeDirName(), "overlays")+" is replaced by a fresh download on every startup, restart, reload, agent editor save, and overlay list save; anything not pushed is lost.",
		"- Do not treat generated effective files under "+filepath.Join(cfg.RuntimeDirName(), "agents")+", "+filepath.Join(cfg.RuntimeDirName(), "skills")+", "+filepath.Join(cfg.RuntimeDirName(), "cron")+", or "+filepath.Join(cfg.RuntimeDirName(), "scripts")+" as source of truth.",
		"- Reload RocketClaw after already-configured overlay source changes so overlays are fetched and merged again. To change which overlays are configured, an admin opens Settings ("+cfg.Web.Tsnet.Origin+"/config) → Behavior settings → Edit → Overlays and saves.",
		"- Local workspace agents/, skills/, cron/, and scripts/ override configured overlays.",
	)

	return strings.Join(lines, "\n")
}

func loadRocketCodeDefinitionsIn(root *os.Root, locked *config.LockedConfig, runtimeDir string, layer map[string]string) (agents harness.Agents, skills harness.Skills, err error) {
	err = locked.Do(func(cfg *config.Config) error {
		agents, skills, err = readRocketCodeDefinitionsIn(root, cfg, runtimeDir, layer, true)
		return err
	})

	return agents, skills, err
}

func readRocketCodeDefinitionsIn(root *os.Root, cfg *config.Config, runtimeDir string, layer map[string]string, includeSkills bool) (harness.Agents, harness.Skills, error) {
	rootFS := root.FS()

	agentsFS, err := fs.Sub(rootFS, filepath.ToSlash(filepath.Join(runtimeDir, "agents")))
	if err != nil {
		return harness.Agents{}, harness.Skills{}, fmt.Errorf("open agents dir: %w", err)
	}

	if layer != nil {
		files := fstest.MapFS{}

		if err := fs.WalkDir(agentsFS, ".", func(name string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}

			data, err := fs.ReadFile(agentsFS, name)
			if err != nil {
				return fmt.Errorf("read %s: %w", name, err)
			}

			files[name] = &fstest.MapFile{Data: data}

			return nil
		}); err != nil {
			return harness.Agents{}, harness.Skills{}, fmt.Errorf("read agents dir: %w", err)
		}

		for name, text := range layer {
			files[name] = &fstest.MapFile{Data: []byte(text)}
		}

		agentsFS = files
	}

	skillsFS, err := fs.Sub(rootFS, filepath.ToSlash(filepath.Join(runtimeDir, "skills")))
	if err != nil {
		return harness.Agents{}, harness.Skills{}, fmt.Errorf("open skills dir: %w", err)
	}

	agents, err := loadAgentDefinitions(agentsFS, skillsFS, cfg)
	if err != nil {
		return harness.Agents{}, harness.Skills{}, err
	}

	var skills harness.Skills
	if includeSkills {
		skills = harness.LoadSkills(skillsFS, filepath.Join(cfg.Workspace, runtimeDir, "skills")).Skills
	}

	tools := []string{reloadToolName, scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName, updateGoalToolName, askUserQuestionToolName, navigateToolName, respondInThreadToolName}

	for name := range agents.Items {
		agent := agents.Items[name]

		groups, err := checkedAgentTagGroups(&agent)
		if err != nil {
			if strings.Contains(name, "/") {
				slog.Warn("load shadow definition", "path", agent.Location, "error", err)
				delete(agents.Items, name)

				continue
			}

			return harness.Agents{}, harness.Skills{}, fmt.Errorf("%s: %w", agent.Location, err)
		}

		agent.Permission.Buckets = slices.DeleteFunc(agent.Permission.Buckets, func(bucket harness.PermissionBucket) bool {
			return bucket.Name == "rocketclaw_tags"
		})
		for i, bucket := range agent.Permission.Buckets {
			if bucket.Name == "rocketclaw" {
				agent.Permission.Buckets[i].Rules = slices.DeleteFunc(bucket.Rules, func(rule harness.PermissionRule) bool {
					return slices.Contains([]string{setTagToolName, getTagsToolName}, rule.Pattern)
				})
			}
		}

		setSessionTagTools(&agent, groups)

		if agent.Approver != nil {
			setSessionTagTools(agent.Approver, agent.Approver.TagGroups)
		}

		for _, tool := range tools {
			action, matched := agent.Permission.Evaluate("rocketclaw", tool)
			if matched && action == harness.PermissionDeny {
				continue
			}

			_ = agent.Permission.Set("rocketclaw", tool, harness.PermissionAllow)
		}

		agents.Items[name] = agent
	}

	return agents, skills, nil
}

func loadAgentDefinitions(agentsFS, skillsFS fs.FS, cfg *config.Config) (harness.Agents, error) {
	result := harness.LoadAgents(agentsFS, &harness.AgentLoadConfig{MCPFacets: cfg.MCPFacets(), Providers: cfg.ProviderNames(), DecisionProviders: cfg.DecisionProviderNames(), OpenCodeServers: slices.Collect(maps.Keys(cfg.OpenCode)), Skills: skillsFS})

	for name := range result.Agents.Items {
		agent := result.Agents.Items[name]
		if parent, ok := result.Agents.Items[filepath.Dir(name)]; ok && parent.Mode != agent.Mode {
			slog.Warn("load shadow definition", "path", agent.Location, "error", fmt.Errorf("mode %s does not match parent %s's mode %s", agent.Mode, parent.Name, parent.Mode))
			delete(result.Agents.Items, name)
		}
	}

	var errLoads []error

	for _, err := range result.Errors {
		location, _, _ := strings.Cut(err.Error(), ": ")
		if strings.Contains(location, "/") {
			slog.Warn("load shadow definition", "path", location, "error", err)
			continue
		}

		errLoads = append(errLoads, err)
	}

	return result.Agents, errors.Join(errLoads...)
}

func LoadRuntimeDefinitions(cfg *config.LockedConfig, runtimeDir string) (harness.Agents, harness.Skills, error) {
	root, err := os.OpenRoot(cfg.Clone().Workspace)
	if err != nil {
		return harness.Agents{}, harness.Skills{}, fmt.Errorf("open workspace root: %w", err)
	}

	defer func() { _ = root.Close() }()

	return loadRocketCodeDefinitionsIn(root, cfg, runtimeDir, nil)
}

func LoadRuntimeAgents(cfg *config.LockedConfig, runtimeDir string) (agents harness.Agents, err error) {
	root, err := os.OpenRoot(cfg.Clone().Workspace)
	if err != nil {
		return harness.Agents{}, fmt.Errorf("open workspace root: %w", err)
	}

	defer func() { _ = root.Close() }()

	err = cfg.Do(func(current *config.Config) error {
		agents, _, err = readRocketCodeDefinitionsIn(root, current, runtimeDir, nil, false)
		return err
	})

	return agents, err
}

func ExternalMCPAgentsIn(cfg *config.LockedConfig, runtimeDir string) ([]string, error) {
	agents, err := LoadRuntimeAgents(cfg, runtimeDir)
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(agents.PrimaryNames(), func(name string) bool { return agents.Items[name].Sticky != harness.AgentStickyOff }), nil
}

func parseReasonArg(raw json.RawMessage, op string) (string, error) {
	var input struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", fmt.Errorf("parse %s request: %w", op, err)
	}

	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		return "", errors.New("reason is required")
	}

	return reason, nil
}

func restartTool(requestRestart runtimeRestarter) harness.Tool {
	return harness.Tool{Name: restartToolName, TurnBound: true, Description: "Restart rocketclaw only after completing an explicitly requested setup change that requires restart, such as changes to setup fields in rocketclaw.json or femtoclaw.json. Behavior Settings (the auto-approver model, Slack channels, the overlay list, and the rest) are edited by an admin on the Web Config page and apply on save without a restart. Use rocketclaw_reload instead for agents/, skills/, cron/, scripts/, or already-configured overlay repository content changes. The reason field must explain why rocketclaw needs to restart. Do not call this after memory, ledger, audit, report, workspace, source-code, generated artifact, log, transcript, or data-file edits.", Permission: "rocketclaw", VisibilitySubjects: []string{restartToolName}, Subjects: func(json.RawMessage) ([]string, error) { return []string{restartToolName}, nil }, Parameters: map[string]any{"properties": map[string]any{"reason": map[string]any{"type": "string"}}, "required": []string{"reason"}}, Call: func(_ context.Context, raw json.RawMessage, _ chan<- harness.ChatResponse) (harness.ToolResult, error) {
		reason, err := parseReasonArg(raw, "restart")
		if err != nil {
			return harness.ToolResult{}, err
		}

		return harness.TextToolResult(requestRestart.Restart(reason)), nil
	}}
}

func reloadTool(requestReload runtimeReloader) harness.Tool {
	return harness.Tool{Name: reloadToolName, Description: "Reload rocketclaw runtime assets after changing agents/, skills/, cron/, scripts/, or already-configured overlay repository content. The reason field must explain what runtime assets changed. This validates staged runtime assets before changing the live runtime. It does not reread rocketclaw.json or femtoclaw.json. Behavior Settings, including the overlay list, are edited by an admin on the Web Config page and apply on save.", Permission: "rocketclaw", VisibilitySubjects: []string{reloadToolName}, Subjects: func(json.RawMessage) ([]string, error) { return []string{reloadToolName}, nil }, Parameters: map[string]any{"properties": map[string]any{"reason": map[string]any{"type": "string"}}, "required": []string{"reason"}}, Call: func(_ context.Context, raw json.RawMessage, _ chan<- harness.ChatResponse) (harness.ToolResult, error) {
		reason, err := parseReasonArg(raw, "reload")
		if err != nil {
			return harness.ToolResult{}, err
		}

		output, err := requestReload.Reload(reason)
		if err != nil {
			return harness.TextToolResult("rocketclaw_reload failed:\n\n" + err.Error()), nil
		}

		return harness.TextToolResult(output), nil
	}}
}

func (b *Bridge) scheduleMessageTool(origin *protocol.InboundMessage) harness.Tool {
	return harness.Tool{Name: scheduleMessageToolName, Description: "Schedule a message to the current rocketclaw conversation after a positive delay up to " + stateRetention.String() + ", such as 12h or 168h. Set recurring to false for one-shot schedules or true to repeat at intervals of at least 1m until scheduled messages are reset.", Permission: "rocketclaw", VisibilitySubjects: []string{scheduleMessageToolName}, Subjects: func(json.RawMessage) ([]string, error) { return []string{scheduleMessageToolName}, nil }, Parameters: map[string]any{"properties": map[string]any{"message": map[string]any{"type": "string"}, "send_this_in": map[string]any{"type": "string"}, "recurring": map[string]any{"type": "boolean"}}, "required": []string{"message", "send_this_in", "recurring"}}, Call: func(_ context.Context, raw json.RawMessage, _ chan<- harness.ChatResponse) (harness.ToolResult, error) {
		b.log.Info("rocketclaw schedule message tool called")

		var input struct {
			Message    string `json:"message"`
			SendThisIn string `json:"send_this_in"`
			Recurring  bool   `json:"recurring"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return harness.ToolResult{}, fmt.Errorf("parse scheduled message: %w", err)
		}

		message := input.Message
		delay, err := time.ParseDuration(input.SendThisIn)

		if strings.TrimSpace(message) == "" {
			return harness.ToolResult{}, errors.New("message is required")
		}

		if err != nil {
			return harness.ToolResult{}, fmt.Errorf("parse send_this_in: %w", err)
		}

		if delay <= 0 || delay > stateRetention {
			return harness.ToolResult{}, fmt.Errorf("send_this_in must be greater than 0 and at most %s", stateRetention)
		}

		if input.Recurring && delay < time.Minute {
			return harness.ToolResult{}, errors.New("recurring send_this_in must be at least 1m")
		}

		if err := b.ScheduleMessage(origin, delay, message, input.Recurring); err != nil {
			b.log.Error("rocketclaw schedule message tool failed", "delay", delay, "delay_ms", delay.Milliseconds(), "recurring", input.Recurring, "message_len", len([]rune(message)), "error", err)
			return harness.ToolResult{}, err
		}

		if input.Recurring {
			return harness.TextToolResult("scheduled recurring message every " + delay.String()), nil
		}

		return harness.TextToolResult("scheduled message in " + delay.String()), nil
	}}
}

type outboundAttachmentInput struct {
	Path          string `json:"path"`
	Name          string `json:"name"`
	MIMEType      string `json:"mime_type"`
	Content       string `json:"content"`
	ContentBase64 string `json:"content_base64"`
}

type attachFilesInput struct {
	Attachments []outboundAttachmentInput `json:"attachments"`
}

func attachFilesTool(root *os.Root, shell *harness.CustomShell, sessions *SessionService, conversationID, turnID string) harness.Tool {
	parameters := map[string]any{
		"properties": map[string]any{
			"attachments": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":           map[string]any{"type": "string"},
						"name":           map[string]any{"type": "string"},
						"mime_type":      map[string]any{"type": "string"},
						"content":        map[string]any{"type": "string"},
						"content_base64": map[string]any{"type": "string"},
					},
					"required":             []string{"path", "name", "mime_type", "content", "content_base64"},
					"additionalProperties": false,
				},
			},
		},
		"required": []string{"attachments"},
	}

	return harness.Tool{Name: attachFilesToolName, TurnBound: true, Description: "Attach files to your reply. attachments: list of {path, name, mime_type, content, content_base64} dicts, \"\" if unused\nEvery dict needs all five keys. Fill one source: path (a file in your workspace), content (text) or content_base64.\nAn empty name becomes the path's file name and an empty mime_type is guessed.\nCall before the final response finishes.", Permission: "rocketclaw", VisibilitySubjects: []string{attachFilesToolName}, Subjects: func(json.RawMessage) ([]string, error) { return []string{attachFilesToolName}, nil }, Parameters: parameters, Call: func(ctx context.Context, raw json.RawMessage, _ chan<- harness.ChatResponse) (harness.ToolResult, error) {
		var input attachFilesInput
		if err := json.Unmarshal(raw, &input); err != nil {
			return harness.ToolResult{}, fmt.Errorf("parse response attachments: %w", err)
		}

		ids := make([]string, 0, len(input.Attachments))
		for i := range input.Attachments {
			attachment, err := outboundAttachment(root, shell, &input.Attachments[i])
			if err != nil {
				return harness.ToolResult{}, err
			}

			attachment.ID = rand.Text()
			if err := sessions.SaveAttachment(ctx, conversationID, turnID, &attachment, false); err != nil {
				return harness.ToolResult{}, err
			}

			ids = append(ids, attachment.ID)
		}

		return harness.TextToolResult("queued attachments for final response: " + strings.Join(ids, " ")), nil
	}}
}

func outboundAttachment(root *os.Root, shell *harness.CustomShell, input *outboundAttachmentInput) (protocol.OutboundAttachment, error) {
	name := strings.TrimSpace(input.Name)
	path := strings.TrimSpace(input.Path)
	mimeType := strings.TrimSpace(input.MIMEType)

	var data []byte

	switch {
	case input.ContentBase64 != "":
		decoded, err := base64.StdEncoding.DecodeString(input.ContentBase64)
		if err != nil {
			return protocol.OutboundAttachment{}, fmt.Errorf("decode attachment %q: %w", name, err)
		}

		data = decoded
	case input.Content != "":
		data = []byte(input.Content)
	case path != "":
		if shell.Dir != "" {
			easyRoot, rel, err := harness.EasyRoot(shell, path)
			if err != nil {
				return protocol.OutboundAttachment{}, fmt.Errorf("read attachment: %w", err)
			}

			defer func() { _ = easyRoot.Close() }()

			root, path = easyRoot, rel
		}

		read, err := root.ReadFile(path)
		if err != nil {
			return protocol.OutboundAttachment{}, fmt.Errorf("read attachment %q: %w", path, err)
		}

		data = read

		name = cmp.Or(name, filepath.Base(path))
	default:
		return protocol.OutboundAttachment{}, fmt.Errorf("attachment %q has no content or path", name)
	}

	name = cmp.Or(name, "attachment")
	mimeType = cmp.Or(mimeType, mime.TypeByExtension(filepath.Ext(name)), http.DetectContentType(data))

	return protocol.OutboundAttachment{Name: name, MIMEType: protocol.NormalizeMIMEType(mimeType), Data: data}, nil
}

func (b *Bridge) resetScheduledMessagesTool(origin *protocol.InboundMessage) harness.Tool {
	return harness.Tool{Name: resetScheduledMessagesToolName, Description: "Delete pending scheduled messages for the current rocketclaw conversation.", Permission: "rocketclaw", VisibilitySubjects: []string{scheduleMessageToolName}, Subjects: func(json.RawMessage) ([]string, error) { return []string{scheduleMessageToolName}, nil }, Parameters: map[string]any{"properties": map[string]any{}}, Call: func(context.Context, json.RawMessage, chan<- harness.ChatResponse) (harness.ToolResult, error) {
		if err := b.ResetScheduledMessages(origin); err != nil {
			return harness.ToolResult{}, err
		}

		return harness.TextToolResult("scheduled messages reset"), nil
	}}
}

func askUserQuestionTool(asker protocol.UserQuestionAsker, msg *protocol.InboundMessage) harness.Tool {
	return harness.Tool{Name: askUserQuestionToolName, Resumable: true, TurnBound: true, Description: "Ask the human partner and wait. options: list of {label, value, description} dicts; multiple: True or False.\nThe question shows where they started this turn. All four arguments are required: pass details=\"\" or options=[] when you have none, and multiple=True only when several options may be picked.\nThe options array is only for concrete predefined choices to show as buttons/selects; do not include catch-all choices like Custom, Other, or Free text.", Permission: "rocketclaw", VisibilitySubjects: []string{askUserQuestionToolName}, Subjects: func(json.RawMessage) ([]string, error) { return []string{askUserQuestionToolName}, nil }, Parameters: map[string]any{"properties": map[string]any{"question": map[string]any{"type": "string"}, "details": map[string]any{"type": "string"}, "options": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"label": map[string]any{"type": "string"}, "value": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}}, "required": []string{"label", "value", "description"}}}, "multiple": map[string]any{"type": "boolean"}}, "required": []string{"question", "details", "options", "multiple"}}, Call: func(ctx context.Context, raw json.RawMessage, _ chan<- harness.ChatResponse) (harness.ToolResult, error) {
		var req protocol.AskUserQuestionRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return harness.ToolResult{}, fmt.Errorf("parse human question: %w", err)
		}

		replacer := strings.NewReplacer("_", " ", "-", " ")
		req.Options = slices.DeleteFunc(req.Options, func(option protocol.AskUserQuestionOption) bool {
			label := strings.Join(strings.Fields(strings.ToLower(replacer.Replace(option.Label))), " ")
			value := strings.Join(strings.Fields(strings.ToLower(replacer.Replace(option.Value))), " ")

			return label == "custom" || label == "custom answer" || label == "custom response" || label == "free text" || label == "other" || value == "custom" || value == "custom answer" || value == "custom response" || value == "free text" || value == "other"
		})

		req.ID, req.ConversationID = harness.ToolCallKey(ctx), msg.ConversationID

		if moved, ok := ctx.Value(backgroundMovedKey{}).(context.Context); ok {
			var cancel context.CancelCauseFunc

			ctx, cancel = context.WithCancelCause(ctx)
			defer cancel(nil)
			defer context.AfterFunc(moved, func() { cancel(context.Cause(moved)) })()
		}

		answer, err := asker.AskUserQuestion(ctx, &req)
		if err != nil {
			if errors.Is(context.Cause(ctx), errMovedToBackground) {
				return harness.TextToolResult(questionWithdrawn), nil
			}

			return harness.ToolResult{}, fmt.Errorf("ask user question: %w", err)
		}

		data, _ := json.Marshal(answer)

		return harness.TextToolResult(string(data)), nil
	}}
}

func respondInThreadTool(store *SessionService, conversationID, turnID string) harness.Tool {
	return harness.Tool{Name: respondInThreadToolName, Resumable: true, TurnBound: true, Description: "Choose where your answer to this Slack mention goes. With in_thread false, or if you never call this tool, your answer posts at the top of the channel. The first call with in_thread true is final: the rest of this turn moves into the Slack thread of the mention, where the conversation continues and your answer posts, and later calls change nothing. Call it with true in its own execute call before other work, because the move happens when that execute call returns.", Permission: "rocketclaw", VisibilitySubjects: []string{respondInThreadToolName}, Subjects: func(json.RawMessage) ([]string, error) { return []string{respondInThreadToolName}, nil }, Parameters: map[string]any{"properties": map[string]any{"in_thread": map[string]any{"type": "boolean"}}, "required": []string{"in_thread"}}, Call: func(ctx context.Context, raw json.RawMessage, _ chan<- harness.ChatResponse) (harness.ToolResult, error) {
		var input struct {
			InThread bool `json:"in_thread"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return harness.ToolResult{}, fmt.Errorf("parse thread choice: %w", err)
		}

		var err error

		moved, key := input.InThread, turnID+respondInThreadStep
		if moved {
			err = store.SaveTurnStep(ctx, conversationID, key, json.RawMessage("true"))
		} else {
			_, moved, err = store.LoadTurnStep(ctx, conversationID, key)
		}

		if err != nil {
			return harness.ToolResult{}, err
		}

		if moved {
			return harness.TextToolResult("The rest of this turn now runs in the Thread Session of the mention's Slack thread, and your answer posts there."), nil
		}

		return harness.TextToolResult("Your answer stays at the top of the channel."), nil
	}}
}

func updateGoalTool(store *SessionService, conversationID string) harness.Tool {
	return harness.Tool{Name: updateGoalToolName, Description: "Update the active RocketClaw goal loop status for this conversation. Use progress when reporting continuing progress, complete when the goal is achieved, or blocked when progress cannot continue.", Permission: "rocketclaw", VisibilitySubjects: []string{updateGoalToolName}, Subjects: func(json.RawMessage) ([]string, error) { return []string{updateGoalToolName}, nil }, Parameters: map[string]any{"properties": map[string]any{"status": map[string]any{"type": "string", "enum": []string{GoalStatusProgress, GoalStatusComplete, GoalStatusBlocked}}, "note": map[string]any{"type": "string", "description": "Status note for the goal update. Use this to explain what is going on, what changed, what you are thinking, where the goal is heading next, what was completed, or what is blocking progress. It should mirror the substance of the visible Progress summary."}}, "required": []string{"status"}}, Call: func(_ context.Context, raw json.RawMessage, _ chan<- harness.ChatResponse) (harness.ToolResult, error) {
		var input struct {
			Status string `json:"status"`
			Note   string `json:"note"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return harness.ToolResult{}, fmt.Errorf("parse goal update: %w", err)
		}

		goal, err := store.UpdateGoalStatus(conversationID, input.Status, input.Note)
		if err != nil {
			return harness.ToolResult{}, err
		}

		if strings.TrimSpace(input.Status) == GoalStatusProgress {
			return harness.TextToolResult("goal progress recorded"), nil
		}

		return harness.TextToolResult("goal marked " + strings.TrimSpace(goal.Status)), nil
	}}
}

func (b *Bridge) armScheduledMessage(id string, message *protocol.ScheduledMessageState) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.stopped {
		return
	}

	armed := *message
	time.AfterFunc(max(time.Until(armed.DueAt), 0), func() {
		if err := b.pickLaterWork(context.Background(), true); err != nil {
			b.log.Error("scheduled message enqueue failed", "scheduled_message_id", id, "conversation_id", armed.ConversationID, "error", err)

			if id == "" && !errors.Is(err, protocol.ErrBridgeStopped) {
				armed.DueAt = time.Now().Add(time.Second)
				b.armScheduledMessage(id, &armed)
			}
		}
	})
}

func (b *Bridge) newOutboundMessage(msg *protocol.InboundMessage, turnID, text string, complete bool) *protocol.OutboundMessage {
	outbound := protocol.NewOutboundMessage(b.config.ConversationID, text)
	outbound.ConversationID = b.config.ConversationID
	outbound.SourceConversationID = b.config.ConversationID

	outbound.Agent = b.agentSnapshot()

	outbound.TurnID = turnID

	outbound.Complete = complete
	if msg != nil {
		outbound.Cronjob = msg.Cronjob

		goal, goalOK, err := b.config.SessionService.Goal(b.config.ConversationID)
		accounted := msg.GoalAction != protocol.GoalActionNone
		statusActive := err == nil && goalOK && strings.TrimSpace(goal.Status) == GoalStatusActive

		outbound.GoalTurn = accounted || msg.GoalTurn || statusActive

		if outbound.GoalTurn && err == nil && goalOK && goal.MaxTurns > 0 {
			outbound.GoalTurnNumber = goal.TurnsUsed + 1
			outbound.GoalMaxTurns = goal.MaxTurns
		}

		outbound.GoalActive = outbound.GoalTurn && statusActive && (!accounted || goal.MaxTurns <= 0 || goal.TurnsUsed+1 < goal.MaxTurns)
		outbound.ChannelQuiet = outbound.GoalTurn || msg.Metadata[sessionRepliesMetadataKey] != "" ||
			msg.Source == protocol.SourceSystem && msg.Metadata[protocol.InboundSenderSessionMetadataKey] == "" && msg.Metadata[backgroundNotesMetadataKey] == ""

		outbound.SlackReply = protocol.Clone(msg.SlackReply)
	}

	return outbound
}

type replayInputMessage struct{ role, text string }

func replayInputForMessage(role, text string) []json.RawMessage {
	message := responses.EasyInputMessageParam{Role: responses.EasyInputMessageRole(role), Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(text)}, Type: "message"}

	raw, _ := harness.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{{OfMessage: &message}})

	return raw
}

func replayInputMessages(raw []json.RawMessage) ([]replayInputMessage, error) {
	items, err := harness.ReplayInputToParams(raw)
	if err != nil {
		return nil, fmt.Errorf("decode replay input messages: %w", err)
	}

	messages := []replayInputMessage{}

	for i := range items {
		role, text, ok, err := ReplayInputMessageRoleText(&items[i], raw[i])
		if err != nil {
			return nil, err
		}

		if ok && strings.TrimSpace(text) != "" {
			messages = append(messages, replayInputMessage{role: role, text: text})
		}
	}

	return messages, nil
}

func ReplayInputMessageRoleText(item *responses.ResponseInputItemUnionParam, raw json.RawMessage) (role, text string, ok bool, err error) {
	if item.OfMessage != nil && item.OfMessage.Role == "assistant" && len(item.OfMessage.Content.OfInputItemContentList) > 0 {
		var output responses.ResponseOutputMessageParam
		if err := json.Unmarshal(raw, &output); err != nil {
			return "", "", false, fmt.Errorf("decode assistant history: %w", err)
		}

		item = &responses.ResponseInputItemUnionParam{OfOutputMessage: &output}
	}

	var message strings.Builder

	if item.OfOutputMessage != nil {
		for _, part := range item.OfOutputMessage.Content {
			if part.OfOutputText != nil {
				message.WriteString(part.OfOutputText.Text)
			}
		}

		return "assistant", message.String(), true, nil
	}

	var content responses.ResponseInputMessageContentListParam

	switch {
	case item.OfMessage != nil:
		role, content = string(item.OfMessage.Role), item.OfMessage.Content.OfInputItemContentList
		if len(content) == 0 {
			text = item.OfMessage.Content.OfString.Value
		}
	case item.OfInputMessage != nil:
		role, content = item.OfInputMessage.Role, item.OfInputMessage.Content
	default:
		return "", "", false, nil
	}

	for i := range content {
		if text := content[i].GetText(); text != nil {
			message.WriteString(*text)
		}
	}

	text = cmp.Or(text, message.String())

	if item.OfMessage != nil && (role == "user" || role == "developer") {
		if header, _ := item.OfMessage.ExtraFields()["prompt_header"].(string); header != "" {
			text = strings.TrimPrefix(text, header+"\n\n")
		}
	}

	return role, text, true, nil
}

const defaultReplyInstruction = "Reply in plain text suitable for Slack. Avoid markdown unless it is necessary."

func (b *Bridge) buildPrompt(msg *protocol.InboundMessage, agent *harness.Agent) (string, error) {
	prompt := buildPrompt(msg, agent, false)

	goal, ok, err := b.config.SessionService.Goal(b.config.ConversationID)
	if err != nil {
		return "", fmt.Errorf("load active goal: %w", err)
	}

	if !ok || strings.TrimSpace(goal.Status) != GoalStatusActive {
		return prompt, nil
	}

	return prompt + "\n\n" + goalSteeringPrompt(&goal), nil
}

func buildPrompt(msg *protocol.InboundMessage, agent *harness.Agent, steer bool) string {
	instruction := defaultReplyInstruction

	if agent != nil {
		if override, ok := agent.Frontmatter["additionalInstructions"].(string); ok && strings.TrimSpace(override) != "" {
			instruction = override
		}

		if agent.Mode == harness.AgentModeEasy {
			instruction = ""
		}
	}

	body := strings.TrimSpace(msg.Text)
	if msg.PreserveWhitespace {
		body = msg.Text
	}

	if body == "" && len(msg.Attachments) > 0 {
		body = "User attached a file with no accompanying text."
		if len(msg.Attachments) > 1 {
			body = fmt.Sprintf("User attached %d files with no accompanying text.", len(msg.Attachments))
		}
	}

	if notes := attachmentWarningsText(msg.AttachmentWarnings); notes != "" {
		if body == "" {
			body = "Attachment notes:\n" + notes
		} else {
			body += "\n\nAttachment notes:\n" + notes
		}
	}

	provenance := provenanceFromInbound(msg)
	provenance.additionalInstructions = instruction

	if session := msg.Metadata[protocol.InboundSenderSessionMetadataKey]; session != "" {
		sender := "session " + session + " (agent " + msg.Metadata[protocol.InboundSenderAgentMetadataKey] + ")"
		switch {
		case msg.Metadata[protocol.InboundSenderActionMetadataKey] == protocol.SenderActionStopped:
			provenance.sender = sender + " stopped your previous turn and sent this"
		case steer:
			provenance.sender = "steer from " + sender
		default:
			provenance.sender = "message from " + sender
		}

		if msg.Metadata[inboundAgentMetadataKey] == "" {
			provenance.sender += "; your final answer returns to that session automatically, so do not write back to it"
		}
	}

	return provenanceHeader(&provenance) + "\n\n" + body
}

func inboundDirectSkill(msg *protocol.InboundMessage) *harness.PromptInputDirectSkill {
	if !msg.Human || msg.Source != protocol.SourceWeb || msg.Metadata[protocol.InboundMediaMetadataKey] == "Voice" ||
		(msg.Kind != protocol.InboundKindPrompt && msg.Kind != protocol.InboundKindSteer && msg.Kind != protocol.InboundKindEnqueue) {
		return nil
	}

	text, ok := msg.Metadata[protocol.InboundRawTextMetadataKey]
	if !ok {
		text = msg.Text
	}

	return parseDirectSkillTrigger(text)
}

func splitFirstWord(text string) (word, rest string) {
	if i := strings.IndexFunc(text, unicode.IsSpace); i >= 0 {
		return text[:i], text[i:]
	}

	return text, ""
}

func parseDirectSkillTrigger(text string) *harness.PromptInputDirectSkill {
	rest, ok := strings.CutPrefix(strings.TrimLeftFunc(text, unicode.IsSpace), "/")
	if !ok {
		return nil
	}

	name, arguments := splitFirstWord(rest)
	arguments = strings.TrimLeftFunc(arguments, unicode.IsSpace)
	plainIfUnknown := true

	switch strings.ToLower(name) {
	case "", "agent", "stop", "enqueue", "goal":
		return nil
	case "skill":
		name, arguments = splitFirstWord(arguments)
		arguments = strings.TrimLeftFunc(arguments, unicode.IsSpace)
		plainIfUnknown = false
	}

	return &harness.PromptInputDirectSkill{Name: name, Arguments: arguments, PlainIfUnknown: plainIfUnknown}
}

type promptProvenance struct {
	origin, media, principal, sender, additionalInstructions string
}

func provenanceFromInbound(msg *protocol.InboundMessage) promptProvenance {
	origin := "System"

	switch msg.Source {
	case protocol.SourceSlack:
		origin = "Slack"
	case protocol.SourceWeb:
		origin = "Web"
	case protocol.SourceExternalMCP:
		origin = "ExternalMCP"
	case protocol.SourceSystem:
		origin = "System"
	}

	provenance := promptProvenance{origin: origin, media: "Text"}
	if origin := canonicalOverride(msg.Metadata[protocol.InboundOriginMetadataKey], "Slack", "Cron", "ExternalMCP", "System"); origin != "" {
		provenance.origin = origin
	}

	if media := canonicalOverride(msg.Metadata[protocol.InboundMediaMetadataKey], "Text", "Voice"); media != "" {
		provenance.media = media
	}

	if msg.Human {
		provenance.principal = strings.TrimSpace(msg.Metadata[protocol.InboundPrincipalMetadataKey])
	}

	return provenance
}

func canonicalOverride(value string, allowed ...string) string {
	if value = strings.TrimSpace(value); slices.Contains(allowed, value) {
		return value
	}

	return ""
}

func provenanceHeader(provenance *promptProvenance) string {
	header := "[" + cmp.Or(provenanceToken(provenance.origin), "System")
	if media := provenanceToken(provenance.media); media != "" && media != "Text" {
		header += " media=" + media
	}

	if principal := strings.TrimSpace(provenance.principal); principal != "" {
		header += " principal=" + strconv.Quote(principal)
	}

	if provenance.sender != "" {
		header += " sender=" + strconv.Quote(provenance.sender)
	}

	if provenance.additionalInstructions != "" {
		header += " additional_instructions=" + strconv.Quote(provenance.additionalInstructions)
	}

	return header + "]"
}

func reviewOriginHeader(origin *protocol.InboundMessage) string {
	if origin.OriginHeader != "" {
		return origin.OriginHeader
	}

	provenance := provenanceFromInbound(origin)
	if origin.Cronjob != nil {
		provenance.origin = "Cron"
	}

	provenance.principal = cmp.Or(provenance.principal, origin.Metadata[protocol.InboundPrincipalNameMetadataKey])

	if agent := origin.Metadata[protocol.InboundSenderAgentMetadataKey]; agent != "" {
		provenance.sender = "agent " + agent
	}

	return provenanceHeader(&provenance)
}

func provenanceToken(value string) string {
	fields := strings.Fields(value)
	value = strings.Join(fields, "_")
	value = strings.ReplaceAll(value, "=", "-")
	value = strings.ReplaceAll(value, "[", "(")
	value = strings.ReplaceAll(value, "]", ")")

	return value
}

func goalSteeringPrompt(goal *GoalState) string {
	turnBudget := "unlimited"
	if goal.MaxTurns > 0 {
		turnBudget = fmt.Sprintf("%d of %d turns used", goal.TurnsUsed, goal.MaxTurns)
	}

	return "Active goal loop:\nObjective:\n" + strings.TrimSpace(goal.Objective) + "\n\nTurn budget: " + turnBudget + "\n\nContinue making concrete progress toward the objective. At the end of every visible goal response, include a Progress summary: section. For status progress, summarize what changed this turn, the current state, and the next concrete step. For status complete, summarize what was achieved and any validation or check result. For status blocked, summarize what happened, the concrete blocker, and what human input, access, or decision is needed. Call rocketclaw_update_goal with status progress, complete, or blocked, and put the same substance from the visible Progress summary in note."
}

func externalMCPMetadataEnv(conversationID string, metadata map[string]string) map[string]string {
	env := map[string]string{rocketclawConversationIDEnv: strings.TrimSpace(conversationID)}

	for key, value := range metadata {
		env[externalMCPMetadataVariable(key)] = value
	}

	return env
}

func externalMCPMetadataVariable(key string) string {
	return rocketclawMetadataEnvPrefix + strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' {
			return r
		}

		return '_'
	}, strings.ToUpper(key))
}

func externalMCPStoredMetadataEnv(conversationID string, entry *harness.SessionEntry) (map[string]string, bool) {
	if entry == nil || entry.Type != externalMCPMetadataEntryType {
		return nil, false
	}

	messages, err := replayInputMessages(entry.ReplayInput)
	if err != nil {
		return nil, false
	}

	conversationLine := rocketclawConversationIDEnv + "=" + strconv.Quote(conversationID)
	for _, message := range messages {
		if !strings.Contains(message.text, conversationLine) {
			continue
		}

		env := map[string]string{}

		for line := range strings.SplitSeq(message.text, "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok && strings.HasPrefix(key, "ROCKETCLAW_") {
				value, _ = strconv.Unquote(value)
				env[key] = value
			}
		}

		return env, true
	}

	return nil, false
}

func externalMCPMetadataDeveloperMessage(heading string, env map[string]string) string {
	lines := append(make([]string, 0, len(env)+1), heading)
	for _, key := range slices.Sorted(maps.Keys(env)) {
		lines = append(lines, key+"="+strconv.Quote(env[key]))
	}

	return strings.Join(lines, "\n")
}

func attachmentWarningsText(warnings []string) string {
	lines := []string{}

	for _, warning := range warnings {
		if warning = strings.TrimSpace(warning); warning != "" {
			lines = append(lines, "- "+warning)
		}
	}

	return strings.Join(lines, "\n")
}

func attachmentFallback(msg *protocol.InboundMessage) string {
	if len(msg.Attachments) > 0 {
		return ""
	}

	var fallback string

	switch msg.AttachmentPresence {
	case protocol.AttachmentPresenceNone:
		return ""
	case protocol.AttachmentPresenceUnsupported:
		fallback = unsupportedFileFallback
	case protocol.AttachmentPresenceImages:
		fallback = attachmentAccessFallback
	}

	if notes := attachmentWarningsText(msg.AttachmentWarnings); notes != "" {
		fallback += "\n\nAttachment notes:\n" + notes
	}

	return fallback
}

func attachmentsFromInbound(inbound []protocol.InboundAttachment) []harness.Attachment {
	attachments := make([]harness.Attachment, 0, len(inbound))
	for i := range inbound {
		mimeType := protocol.NormalizeMIMEType(inbound[i].MIMEType)
		attachments = append(attachments, harness.Attachment{MIME: mimeType, Filename: inbound[i].Name, URL: "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(inbound[i].Data)})
	}

	return attachments
}

func appendText(existing, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return existing
	}

	if existing != "" {
		text = existing + "\n" + text
	}

	return text
}
