// Package harnessbridge owns the rocketcode library bridge.
package harnessbridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/oai"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
	"github.com/Rocketable/platform/internal/rocketcode"
	anthropic "github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // Registers a WebP decoder for image.Decode.
	"golang.org/x/sync/errgroup"
)

const (
	restartToolName                    = "rocketclaw_restart"
	rawRunToolName                     = "rocketclaw_i_want_human_partner_to_see_this"
	attachFilesToolName                = "rocketclaw_attach_files_to_response"
	updateGoalToolName                 = "rocketclaw_update_goal"
	scheduleMessageToolName            = "rocketclaw_schedule_message"
	resetScheduledMessagesToolName     = "rocketclaw_reset_scheduled_messages"
	internalErrorResponse              = "I hit an internal error while waiting for rocketcode."
	attachmentAccessFallback           = "I can see that you attached a file, but I could not send it to the model. Please re-upload it as a supported image or send a smaller file."
	unsupportedFileFallback            = "I can see that you attached a non-image file. I can inspect image attachments right now, but other file types are not supported yet."
	defaultQueueSize                   = 128
	externalMCPMetadataEntryType       = "mcp_external_metadata"
	externalMCPConversationPrefix      = "external_mcp:"
	goalContinuationLabel              = "goal_continuation"
	goalKickoffLabel                   = "goal"
	rocketclawConversationIDEnv        = "ROCKETCLAW_CONVERSATION_ID"
	rocketclawMetadataEnvPrefix        = "ROCKETCLAW_METADATA_"
	maxInboundAttachmentBytes          = 4 << 20
	maxInboundAttachmentTotalBytes     = 16 << 20
	maxInboundAttachmentResizeInput    = 16 << 20
	maxInboundAttachmentResizeAttempts = 8
)

var errBridgeStopped = errors.New("bridge stopped")

var errInboundAttachmentReductionFailed = errors.New("inbound attachment image reduction failed")

var errInboundAttachmentReductionNotEnough = errors.New("inbound attachment image still exceeds size limit after reduction")

// RawRunExposedToolName is the tool cron prompts use for human-visible output.
const RawRunExposedToolName = rawRunToolName

type toolMode string

const (
	toolModePersistent toolMode = "persistent"
	toolModeCron       toolMode = "cron"
)

// Config controls one rocketcode bridge conversation.
type Config struct {
	ConversationID, Agent string
	ConsumeSharedInbound  bool
	OutputTargets         []events.OutputTarget
	RequestRestart        func(context.Context, string) (string, error)
	SessionService        *SessionService
}

// Bridge forwards rocketclaw messages into one turn-lived rocketcode run per turn.
type Bridge struct {
	log               *slog.Logger
	config            Config
	runtime           *config.Config
	bus               *events.Bus
	inputStop         context.CancelFunc
	requestCh         chan bridgeRequest
	stopCh            chan struct{}
	mu                sync.Mutex
	handling, stopped bool
}

type bridgeRequest struct {
	inbound                   *events.InboundMessage
	summary                   *summaryRequest
	scheduledMessageID        string
	scheduledMessageRecurring bool
}

type summaryRequest struct {
	ctx      context.Context
	prompt   string
	resultCh chan summaryResult
}

type summaryResult struct {
	text string
	err  error
}

type runResult struct {
	turnID, text, thinking string
	sequence               int
	sessionEntryID         int64
	responseID, model      string
	attachments            []events.OutboundAttachment
}

// NewConversation constructs a rocketcode bridge for one conversation.
func NewConversation(cfg *config.Config, bus *events.Bus, bridgeConfig *Config, logger *slog.Logger) *Bridge {
	b := &Bridge{log: nil, config: normalizeConfig(bridgeConfig), runtime: cfg, bus: bus, inputStop: func() {}, requestCh: nil, stopCh: nil, mu: sync.Mutex{}, handling: false}
	b.log = logger.With("component", "rocketcode")

	return b
}

func normalizeConfig(cfg *Config) Config {
	if cfg == nil {
		cfg = new(Config)
	}

	normalized := *cfg
	if strings.TrimSpace(normalized.ConversationID) == "" {
		normalized.ConversationID = events.MainConversationID()
		normalized.ConsumeSharedInbound = true
	}

	if strings.TrimSpace(normalized.Agent) == "" {
		normalized.Agent = "main"
	}

	if len(normalized.OutputTargets) == 0 {
		normalized.OutputTargets = events.MainOutputTargets()
	} else {
		normalized.OutputTargets = append([]events.OutputTarget(nil), normalized.OutputTargets...)
	}

	return normalized
}

// Start begins forwarding and handling messages for the conversation.
func (b *Bridge) Start(ctx context.Context) error {
	b.requestCh = make(chan bridgeRequest, defaultQueueSize)

	b.stopCh = make(chan struct{})

	state, err := b.config.SessionService.Load()
	if err != nil {
		return fmt.Errorf("load scheduled messages: %w", err)
	}

	for id, message := range state.ScheduledMessages {
		if message.ConversationID == b.config.ConversationID {
			b.log.Info("scheduled message restored", "scheduled_message_id", id, "conversation_id", message.ConversationID, "agent", message.Agent, "due_at", message.DueAt, "remaining_ms", time.Until(message.DueAt).Milliseconds())
			b.armScheduledMessage(id, &message)
		}
	}

	if b.config.ConsumeSharedInbound {
		inboundCtx, cancel := context.WithCancel(ctx)

		b.inputStop = cancel
		go b.forwardInbound(inboundCtx)
	}

	go b.loop(ctx)

	return nil
}

// ScheduleMessage schedules one delayed prompt for this conversation.
func (b *Bridge) ScheduleMessage(delay time.Duration, message string, recurring bool) error {
	id := rand.Text()

	scheduled := ScheduledMessageState{ConversationID: b.config.ConversationID, Agent: b.config.Agent, Message: message, DueAt: time.Now().UTC().Add(delay), Recurring: recurring}
	if recurring {
		scheduled.Interval = delay
	}

	if err := b.config.SessionService.updateState(func(state *State) {
		if state.ScheduledMessages == nil {
			state.ScheduledMessages = map[string]ScheduledMessageState{}
		}

		state.ScheduledMessages[id] = scheduled
	}); err != nil {
		b.log.Error("scheduled message persist failed", "scheduled_message_id", id, "conversation_id", scheduled.ConversationID, "agent", scheduled.Agent, "due_at", scheduled.DueAt, "delay_ms", delay.Milliseconds(), "recurring", recurring, "interval_ms", scheduled.Interval.Milliseconds(), "message_len", len([]rune(message)), "error", err)
		return fmt.Errorf("persist scheduled message: %w", err)
	}

	b.log.Info("scheduled message persisted", "scheduled_message_id", id, "conversation_id", scheduled.ConversationID, "agent", scheduled.Agent, "due_at", scheduled.DueAt, "delay_ms", delay.Milliseconds(), "recurring", recurring, "interval_ms", scheduled.Interval.Milliseconds(), "message_len", len([]rune(message)))
	b.armScheduledMessage(id, &scheduled)

	return nil
}

// ResetScheduledMessages deletes pending scheduled prompts for this conversation.
func (b *Bridge) ResetScheduledMessages() error {
	if err := b.config.SessionService.updateState(func(state *State) {
		maps.DeleteFunc(state.ScheduledMessages, func(_ string, message ScheduledMessageState) bool {
			return message.ConversationID == b.config.ConversationID
		})
	}); err != nil {
		return fmt.Errorf("reset scheduled messages: %w", err)
	}

	b.log.Info("scheduled messages reset", "conversation_id", b.config.ConversationID)

	return nil
}

// Stop cancels bridge activity.
func (b *Bridge) Stop() error {
	b.inputStop()

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.stopped {
		return nil
	}

	close(b.stopCh)
	b.stopped = true

	return nil
}

// WaitIdle waits until queued bridge work has drained.
func (b *Bridge) WaitIdle(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		b.mu.Lock()
		handling := b.handling
		b.mu.Unlock()

		if !handling && len(b.requestCh) == 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for bridge idle: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// Submit enqueues one inbound message for this conversation.
func (b *Bridge) Submit(ctx context.Context, msg *events.InboundMessage) error {
	msg.ConversationID = b.config.ConversationID

	return b.enqueue(ctx, bridgeRequest{inbound: msg}, "submit inbound message")
}

// Summarize asks the conversation to produce a short summary.
func (b *Bridge) Summarize(ctx context.Context, prompt string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", errors.New("summary prompt is required")
	}

	request := &summaryRequest{ctx: ctx, prompt: prompt, resultCh: make(chan summaryResult, 1)}

	if err := b.enqueue(ctx, bridgeRequest{summary: request}, "summarize thread"); err != nil {
		return "", err
	}

	b.mu.Lock()
	stopCh := b.stopCh
	b.mu.Unlock()

	select {
	case <-ctx.Done():
		return "", fmt.Errorf("summarize thread: %w", ctx.Err())
	case <-stopCh:
		return "", fmt.Errorf("summarize thread: %w", errBridgeStopped)
	case result := <-request.resultCh:
		return result.text, result.err
	}
}

// SeedResponseThread initializes an empty thread session from a main-session response checkpoint.
func (b *Bridge) SeedResponseThread(ctx context.Context, checkpoint events.ResponseCheckpoint, _ string) error {
	if checkpoint.SessionEntryID <= 0 {
		return errors.New("response checkpoint session entry ID is required")
	}

	assistantText := strings.TrimSpace(checkpoint.AssistantText)
	if assistantText == "" {
		return errors.New("response checkpoint assistant text is required")
	}

	threadStore := newSessionStore(b.config.ConversationID, b.config.SessionService)

	threadEntries, err := b.observeSessionEntries(ctx, b.config.ConversationID)
	if err != nil {
		return fmt.Errorf("load response-rooted thread session: %w", err)
	}

	if len(threadEntries) > 0 {
		return nil
	}

	mainConversationID := strings.TrimSpace(checkpoint.ConversationID)
	if mainConversationID == "" {
		mainConversationID = events.MainConversationID()
	}

	observed, err := b.observeSessionEntries(ctx, mainConversationID)
	if err != nil {
		return fmt.Errorf("load main session checkpoint entries: %w", err)
	}

	seedEntries := make([]rocketcode.SessionEntry, 0, len(observed))
	found := false

	for i := range observed {
		entry := observed[i].Entry
		if observed[i].ID == checkpoint.SessionEntryID {
			items, err := rocketcode.ReplayInputToParams(entry.ReplayInput)
			if err != nil {
				return fmt.Errorf("decode checkpoint replay input: %w", err)
			}

			for j := range items {
				role, _, ok := replayInputMessageRoleText(&items[j])
				if !ok || strings.TrimSpace(role) == "assistant" {
					entry.ReplayInput, err = rocketcode.ReplayInputFromParams(items[:j])
					if err != nil {
						return fmt.Errorf("encode checkpoint replay input: %w", err)
					}

					break
				}
			}

			seedEntries = append(seedEntries, entry)
			found = true

			break
		}

		if observed[i].ID < checkpoint.SessionEntryID {
			seedEntries = append(seedEntries, entry)
		}
	}

	if !found {
		return fmt.Errorf("main session checkpoint entry %d was not found", checkpoint.SessionEntryID)
	}

	model, err := compactModel(strings.TrimSpace(checkpoint.Model))
	if err != nil {
		return err
	}

	seedReplay, err := b.compactSeedReplay(ctx, seedEntries, model)
	if err != nil {
		return fmt.Errorf("compact main session checkpoint: %w", err)
	}

	assistantReplay, err := replayInputForMessage("assistant", assistantText)
	if err != nil {
		return fmt.Errorf("encode response-rooted thread assistant seed: %w", err)
	}

	seedReplay = append(seedReplay, assistantReplay...)

	_, err = threadStore.outID(rocketcode.SessionEntry{
		Version:     1,
		Type:        "response_thread_seed",
		Timestamp:   time.Now().UTC(),
		ResponseID:  strings.TrimSpace(checkpoint.ResponseID),
		Model:       strings.TrimSpace(checkpoint.Model),
		ReplayInput: seedReplay,
	})
	if err != nil {
		return fmt.Errorf("persist response-rooted thread seed: %w", err)
	}

	return nil
}

// SeedThreadFromMain initializes an empty thread session from current main-session context.
func (b *Bridge) SeedThreadFromMain(ctx context.Context) error {
	threadEntries, err := b.observeSessionEntries(ctx, b.config.ConversationID)
	if err != nil {
		return fmt.Errorf("load Slack thread session: %w", err)
	}

	if len(threadEntries) > 0 {
		return nil
	}

	observed, err := b.observeSessionEntries(ctx, events.MainConversationID())
	if err != nil {
		return fmt.Errorf("load main session entries: %w", err)
	}

	if len(observed) == 0 {
		return nil
	}

	seedEntries := make([]rocketcode.SessionEntry, 0, len(observed))
	model := responses.ResponseCompactParamsModel("")

	for i := range observed {
		entry := observed[i].Entry

		seedEntries = append(seedEntries, entry)
		if strings.TrimSpace(entry.Model) != "" {
			model = responses.ResponseCompactParamsModel(strings.TrimSpace(entry.Model))
		}
	}

	compactionModel, err := compactModel(string(model))
	if err != nil {
		return err
	}

	seedReplay, err := b.compactSeedReplay(ctx, seedEntries, compactionModel)
	if err != nil {
		return fmt.Errorf("compact main session: %w", err)
	}

	threadStore := newSessionStore(b.config.ConversationID, b.config.SessionService)

	_, err = threadStore.outID(rocketcode.SessionEntry{Version: 1, Type: "main_thread_seed", Timestamp: time.Now().UTC(), Model: string(model), ReplayInput: seedReplay})
	if err != nil {
		return fmt.Errorf("persist main-thread seed: %w", err)
	}

	return nil
}

// SeedThreadFromCron initializes an empty Slack thread session from cron output.
func (b *Bridge) SeedThreadFromCron(ctx context.Context, seedText string) error {
	seedText = strings.TrimSpace(seedText)
	if seedText == "" {
		return errors.New("cron thread seed text is required")
	}

	threadStore := newSessionStore(b.config.ConversationID, b.config.SessionService)

	threadEntries, err := b.observeSessionEntries(ctx, b.config.ConversationID)
	if err != nil {
		return fmt.Errorf("load Slack cron thread session: %w", err)
	}

	if len(threadEntries) > 0 {
		return nil
	}

	seedReplay, err := replayInputForMessage("assistant", seedText)
	if err != nil {
		return fmt.Errorf("encode cron thread seed: %w", err)
	}

	_, err = threadStore.outID(rocketcode.SessionEntry{Version: 1, Type: "cron_thread_seed", Timestamp: time.Now().UTC(), ReplayInput: seedReplay})
	if err != nil {
		return fmt.Errorf("persist cron thread seed: %w", err)
	}

	return nil
}

func compactModel(model string) (responses.ResponseCompactParamsModel, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return responses.ResponseCompactParamsModelGPT5_4, nil
	}

	provider, apiModel, ok := strings.Cut(model, "/")
	if !ok {
		return responses.ResponseCompactParamsModel(model), nil
	}

	switch provider {
	case "openai":
		if strings.TrimSpace(apiModel) == "" {
			return "", fmt.Errorf("invalid OpenAI checkpoint model %q", model)
		}

		return responses.ResponseCompactParamsModel(apiModel), nil
	case "anthropic":
		return "", fmt.Errorf("response checkpoint compaction does not support Anthropic model %q", model)
	default:
		return "", fmt.Errorf("response checkpoint compaction does not support provider %q", provider)
	}
}

func (b *Bridge) enqueue(ctx context.Context, request bridgeRequest, operation string) error {
	b.mu.Lock()
	stopCh, stopped := b.stopCh, b.stopped
	b.mu.Unlock()

	if stopped {
		return fmt.Errorf("%s: %w", operation, errBridgeStopped)
	}

	select {
	case <-ctx.Done():
		return fmt.Errorf("%s: %w", operation, ctx.Err())
	case <-stopCh:
		return fmt.Errorf("%s: %w", operation, errBridgeStopped)
	case b.requestCh <- request:
		return nil
	}
}

func (b *Bridge) compactSeedReplay(ctx context.Context, entries []rocketcode.SessionEntry, model responses.ResponseCompactParamsModel) ([]json.RawMessage, error) {
	b.log.Info("starting seed replay compaction", "conversation_id", b.config.ConversationID, "entries", len(entries), "model", model)

	input := []responses.ResponseInputItemUnionParam{}

	for i := range entries {
		items, err := rocketcode.ReplayInputToParams(entries[i].ReplayInput)
		if err != nil {
			return nil, fmt.Errorf("build compaction input: entry %d: %w", i+1, err)
		}

		input = append(input, items...)
	}

	for i := len(input) - 1; i >= 0; i-- {
		if input[i].OfCompaction != nil {
			input = input[i:]

			break
		}
	}

	client, err := b.openAIClient()
	if err != nil {
		return nil, fmt.Errorf("prepare OpenAI client: %w", err)
	}

	if model == "" {
		model = responses.ResponseCompactParamsModelGPT5_4
	}

	params := responses.ResponseCompactParams{Model: model, Input: responses.ResponseCompactParamsInputUnion{OfResponseInputItemArray: input}}

	if b.runtime.OpenAI.RocketCodeAuth == "chatgpt" {
		root, err := os.OpenRoot(b.runtime.Workspace)
		if err != nil {
			return nil, fmt.Errorf("open workspace root: %w", err)
		}

		agents, _, err := loadRocketCodeDefinitionsIn(root, b.runtime.Workspace, b.runtime.WorkDirName(), toolModePersistent)
		_ = root.Close()

		if err != nil {
			return nil, fmt.Errorf("open workspace agent: %w", err)
		}

		if agent, ok := agents.Items[b.config.Agent]; ok {
			if instructions := strings.TrimSpace(agent.Prompt); instructions != "" {
				params.Instructions = openai.String(instructions)
			}
		}
	}

	compacted, err := client.Responses.Compact(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("compact seed replay: %w", err)
	}

	return compactedOutputToReplayInput(compacted.Output)
}

func (b *Bridge) observeSessionEntries(ctx context.Context, conversationID string) ([]ObservedSessionEntry, error) {
	return b.config.SessionService.ObserveEntries(ctx, conversationID, 0)
}

func (b *Bridge) forwardInbound(ctx context.Context) {
	for msg := range b.bus.Inbound(ctx) {
		if msg == nil || msg.ConversationID != b.config.ConversationID {
			continue
		}

		if err := b.Submit(ctx, msg); err != nil && !errors.Is(err, context.Canceled) {
			b.log.Error("submit shared inbound rocketcode message", "error", err)
		}
	}
}

func (b *Bridge) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			_ = b.Stop()
			return
		case <-b.stopCh:
			return
		case request := <-b.requestCh:
			b.log.Info("bridge dequeued request", "conversation_id", b.config.ConversationID, "has_inbound", request.inbound != nil, "has_summary", request.summary != nil, "scheduled_message_id", request.scheduledMessageID, "queue_len", len(b.requestCh))
			b.setHandling(true)

			if request.inbound != nil {
				errHandle := b.handleInbound(ctx, request.inbound)
				if errHandle != nil && !errors.Is(errHandle, context.Canceled) {
					b.log.Error("handle inbound rocketcode message", "error", errHandle)
				}

				if errHandle == nil && request.scheduledMessageID != "" && !request.scheduledMessageRecurring {
					if errDelete := b.config.SessionService.updateState(func(state *State) { delete(state.ScheduledMessages, request.scheduledMessageID) }); errDelete != nil {
						b.log.Error("delete handled scheduled message", "error", errDelete)
					} else {
						b.log.Info("scheduled message deleted after successful handling", "scheduled_message_id", request.scheduledMessageID, "conversation_id", b.config.ConversationID)
					}
				}
			} else if request.summary != nil {
				b.handleSummary(ctx, request.summary)
			}

			b.setHandling(false)
		}
	}
}

func (b *Bridge) setHandling(handling bool) {
	b.mu.Lock()
	b.handling = handling
	b.mu.Unlock()
}

func (b *Bridge) handleSummary(_ context.Context, request *summaryRequest) {
	result, err := b.runTurn(request.ctx, events.NewMainInboundMessage(events.SourceSystem, events.InboundKindPrompt, "", request.prompt, false), fmt.Sprintf("turn-%d", time.Now().UnixNano()), false)
	if err != nil {
		request.resultCh <- summaryResult{text: "", err: fmt.Errorf("summarize thread: %w", err)}
		return
	}

	request.resultCh <- summaryResult{text: result.text, err: nil}
}

func (b *Bridge) handleInbound(ctx context.Context, msg *events.InboundMessage) error {
	if msg.Label == goalContinuationLabel {
		goal, ok, err := b.config.SessionService.Goal(b.config.ConversationID)
		if err != nil {
			return fmt.Errorf("load goal continuation state: %w", err)
		}

		if !ok || strings.TrimSpace(goal.Status) != GoalStatusActive {
			msg.CompleteResponse("", nil)
			return nil
		}
	}

	turnID := fmt.Sprintf("turn-%d", time.Now().UnixNano())
	started := time.Now()
	result := runResult{turnID: turnID, text: "", thinking: "", sequence: 0, sessionEntryID: 0, responseID: "", model: ""}

	var errLog error

	slackChannel, slackMessageTS, slackThreadTS := "", "", ""
	if reply := msg.SlackReply; reply != nil {
		slackChannel, slackMessageTS, slackThreadTS = reply.ChannelID, reply.MessageTS, reply.ThreadTS
	}

	discordChannel, discordMessageID, discordThreadID := "", "", ""
	if reply := msg.DiscordReply; reply != nil {
		discordChannel, discordMessageID, discordThreadID = reply.ChannelID, reply.MessageID, reply.ThreadID
	}

	normalizeInboundAttachments(msg)

	b.log.Info("starting rocketcode turn", "conversation_id", b.config.ConversationID, "turn_id", turnID, "source", msg.Source, "kind", msg.Kind, "label", msg.Label, "text_len", len([]rune(msg.Text)), "attachment_count", len(msg.Attachments), "slack_channel", slackChannel, "slack_message_ts", slackMessageTS, "slack_thread_ts", slackThreadTS, "discord_channel", discordChannel, "discord_message_id", discordMessageID, "discord_thread_id", discordThreadID)

	defer func() {
		b.log.Info("finished rocketcode turn", "conversation_id", b.config.ConversationID, "turn_id", turnID, "duration_ms", time.Since(started).Milliseconds(), "text_len", len([]rune(result.text)), "thinking_len", len([]rune(result.thinking)), "session_entry_id", result.sessionEntryID, "error", errLog)
	}()

	publish := msg.Kind != events.InboundKindInternalize
	if fallback := attachmentFallback(msg); fallback != "" {
		result.text = fallback
		errPublish := b.publishFinal(ctx, msg, result, publish)
		errLog = errPublish

		return errPublish
	}

	var errTurn error

	result, errTurn = b.runTurn(ctx, msg, turnID, publish)
	if errTurn != nil {
		b.log.Error("run rocketcode turn", "error", errTurn)

		if !publish {
			msg.CompleteResponse("", errTurn)
			errLog = errTurn

			return errTurn
		}

		text := internalErrorResponse + "\n\n" + errTurn.Error()
		result = runResult{turnID: turnID, text: text, thinking: "", sequence: 0, sessionEntryID: 0, responseID: "", model: ""}
		errPublish := b.publishFinal(ctx, msg, result, true)
		errLog = errors.Join(errTurn, errPublish)

		return errPublish
	}

	result.turnID = turnID
	errPublish := b.publishFinal(ctx, msg, result, publish)

	errLog = errPublish
	if errPublish != nil || !publish {
		return errPublish
	}

	if errGoal := b.finishGoalTurn(ctx, msg); errGoal != nil {
		errLog = errGoal
		return errGoal
	}

	return nil
}

//nolint:gocritic // runResult is kept by value to avoid nil handling in the hot publish path.
func (b *Bridge) publishFinal(ctx context.Context, msg *events.InboundMessage, result runResult, publish bool) error {
	if !publish {
		if msg.ConversationID == events.MainConversationID() && (msg.VerbatimMessage != "" || len(msg.VerbatimAttachments) > 0) {
			outbound := b.newOutboundMessage(msg, result.turnID, result.sequence+1, msg.VerbatimMessage, "", true)

			outbound.Attachments = events.CloneOutboundAttachments(msg.VerbatimAttachments)
			if result.sessionEntryID > 0 {
				outbound.Checkpoint = &events.ResponseCheckpoint{
					ConversationID: b.config.ConversationID,
					SessionEntryID: result.sessionEntryID,
					ResponseID:     result.responseID,
					Model:          result.model,
					AssistantText:  msg.VerbatimMessage,
				}
			}

			if err := b.bus.PublishOutbound(ctx, outbound); err != nil {
				msg.CompleteResponse("", err)
				return fmt.Errorf("publish verbatim outbound message: %w", err)
			}

			if err := outbound.WaitDelivered(ctx); err != nil {
				msg.CompleteResponse("", err)
				return fmt.Errorf("wait for verbatim outbound delivery: %w", err)
			}
		}

		msg.CompleteResponse("", nil)

		return nil
	}

	outbound := b.newOutboundMessage(msg, result.turnID, result.sequence+1, result.text, "", true)

	outbound.Attachments = events.CloneOutboundAttachments(result.attachments)
	if goal, ok, err := b.config.SessionService.Goal(b.config.ConversationID); err == nil && ok && strings.TrimSpace(goal.Status) == GoalStatusComplete {
		outbound.GoalComplete = true
	}

	if b.config.ConversationID == events.MainConversationID() && msg.Kind == events.InboundKindPrompt && strings.TrimSpace(result.text) != "" && result.sessionEntryID > 0 {
		outbound.Checkpoint = &events.ResponseCheckpoint{
			ConversationID: b.config.ConversationID,
			SessionEntryID: result.sessionEntryID,
			ResponseID:     result.responseID,
			Model:          result.model,
			AssistantText:  result.text,
		}
	}

	if err := b.bus.PublishOutbound(ctx, outbound); err != nil {
		msg.CompleteResponse("", err)
		return fmt.Errorf("publish final outbound message: %w", err)
	}

	msg.CompleteResponseWithAttachments(result.text, result.attachments, nil)

	if err := outbound.WaitDelivered(ctx); err != nil {
		return fmt.Errorf("wait for final outbound delivery: %w", err)
	}

	return nil
}

func (b *Bridge) finishGoalTurn(ctx context.Context, msg *events.InboundMessage) error {
	goalBefore, ok, err := b.config.SessionService.Goal(b.config.ConversationID)
	if err != nil {
		return fmt.Errorf("load goal after turn: %w", err)
	}

	if !ok {
		return nil
	}

	if strings.TrimSpace(goalBefore.Status) != GoalStatusActive {
		return nil
	}

	goal, ok, err := b.config.SessionService.AccountGoalTurn(b.config.ConversationID)
	if err != nil {
		return fmt.Errorf("account goal turn: %w", err)
	}

	if !ok || strings.TrimSpace(goal.Status) != GoalStatusActive {
		return nil
	}

	return b.enqueueGoalContinuation(ctx, &goal, msg)
}

func (b *Bridge) enqueueGoalContinuation(ctx context.Context, goal *GoalState, msg *events.InboundMessage) error {
	inbound := events.NewMainInboundMessage(events.SourceSystem, events.InboundKindPrompt, goalContinuationLabel, goalContinuationPrompt(goal), false)

	inbound.ConversationID = b.config.ConversationID
	if msg != nil && msg.SlackReply != nil {
		inbound.SlackReply = &events.SlackReplyTarget{ChannelID: msg.SlackReply.ChannelID, MessageTS: msg.SlackReply.ThreadTS, ThreadTS: msg.SlackReply.ThreadTS}
	} else if channelID, threadTS, ok := SlackThreadTarget(b.config.ConversationID); ok {
		inbound.SlackReply = &events.SlackReplyTarget{ChannelID: channelID, MessageTS: threadTS, ThreadTS: threadTS}
	}

	return b.enqueue(ctx, bridgeRequest{inbound: inbound}, "submit goal continuation")
}

func (b *Bridge) runTurn(ctx context.Context, msg *events.InboundMessage, turnID string, publish bool) (runResult, error) {
	root, err := os.OpenRoot(b.runtime.Workspace)
	if err != nil {
		return runResult{}, fmt.Errorf("open workspace root: %w", err)
	}

	defer func() { _ = root.Close() }()

	agents, skills, err := loadRocketCodeDefinitionsIn(root, b.runtime.Workspace, b.runtime.WorkDirName(), toolModePersistent)
	if err != nil {
		return runResult{}, fmt.Errorf("open workspace agent and skills: %w", err)
	}

	appendOverlayPromptToAgent(agents, b.config.Agent, b.runtime)

	if err := root.MkdirAll(filepath.ToSlash(filepath.Join(b.runtime.WorkDirName(), ".rocketcode", "shell-outputs")), 0o755); err != nil {
		return runResult{}, fmt.Errorf("create rocketcode shell output dir: %w", err)
	}

	shellOutputDir, store := filepath.Join(b.runtime.Workspace, b.runtime.WorkDirName(), ".rocketcode", "shell-outputs"), newSessionStore(b.config.ConversationID, b.config.SessionService)

	var shellEnv map[string]string

	sessionIn := store.in()

	if strings.HasPrefix(b.config.ConversationID, externalMCPConversationPrefix) {
		entries, err := b.observeSessionEntries(ctx, b.config.ConversationID)
		if err != nil {
			return runResult{}, fmt.Errorf("load external MCP metadata: %w", err)
		}

		metadataEnv, ok := externalMCPStoredMetadataEnv(b.config.ConversationID, entries)
		if !ok {
			metadataEnv = externalMCPMetadataEnv(b.config.ConversationID, msg.Metadata)

			shellEnv = metadataEnv

			replayInput, err := replayInputForMessage("developer", externalMCPMetadataDeveloperMessage("This external MCP thread has metadata:", metadataEnv))
			if err != nil {
				return runResult{}, fmt.Errorf("encode external MCP metadata: %w", err)
			}

			if _, err := store.outID(rocketcode.SessionEntry{Version: 1, Type: externalMCPMetadataEntryType, Timestamp: time.Now().UTC(), ReplayInput: replayInput}); err != nil {
				return runResult{}, fmt.Errorf("append external MCP metadata: %w", err)
			}
		} else {
			shellEnv = metadataEnv

			transientEnv := externalMCPMetadataEnv(b.config.ConversationID, msg.Metadata)
			for key := range metadataEnv {
				delete(transientEnv, key)
			}

			if len(transientEnv) > 0 {
				shellEnv = maps.Clone(metadataEnv)
				maps.Copy(shellEnv, transientEnv)

				replayInput, err := replayInputForMessage("developer", externalMCPMetadataDeveloperMessage("This external MCP turn has additional metadata:", transientEnv))
				if err != nil {
					return runResult{}, fmt.Errorf("encode transient external MCP metadata: %w", err)
				}

				sessionIn = func(yield func(rocketcode.SessionEntry, error) bool) {
					for entry, err := range store.in() {
						if !yield(entry, err) {
							return
						}
					}

					yield(rocketcode.SessionEntry{Version: 1, Type: externalMCPMetadataEntryType, Timestamp: time.Now().UTC(), ReplayInput: replayInput}, nil)
				}
			}
		}
	}

	providers, err := b.rocketcodeProviders()
	if err != nil {
		return runResult{}, fmt.Errorf("prepare RocketCode providers: %w", err)
	}

	attachments := new(outboundAttachmentCollector)

	observed, err := b.observeSessionEntries(ctx, b.config.ConversationID)
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
			if replayInputRawKind(raw) == "compaction" {
				compactionCount++
			}
		}
	}

	b.log.Info("prepared rocketcode session history", "conversation_id", b.config.ConversationID, "turn_id", turnID, "entry_count", len(observed), "replay_item_count", replayItemCount, "history_bytes", historyBytes, "compaction_count", compactionCount, "latest_entry_id", latestEntryID, "latest_entry_type", latestEntryType)

	rocketcodeConfig := b.rocketcodeConfig(shellOutputDir, shellEnv, attachments.Tool(root))
	rocketcodeConfig.InterAgentFilter = interAgentFilterConfig(agents)

	looper, err := rocketcode.NewWithProviders(providers, &rocketcodeConfig, root, agents, skills, b.config.Agent, io.Discard)
	if err != nil {
		return runResult{}, fmt.Errorf("prepare rocketcode turn: %w", err)
	}

	input := make(chan rocketcode.PromptInput, 1)
	output := make(chan rocketcode.ChatResponse, 128)
	interrupts := make(chan os.Signal, 1)

	prompt, err := b.buildPrompt(msg)
	if err != nil {
		return runResult{}, err
	}

	input <- rocketcode.PromptInput{Role: "", Text: prompt, Attachments: attachmentsFromInbound(msg.Attachments), Responses: output}

	close(input)

	var group errgroup.Group

	result := runResult{turnID: turnID, text: "", thinking: "", sequence: 0, sessionEntryID: 0, responseID: "", model: ""}

	var (
		appendedMu         sync.Mutex
		appendedID         int64
		appendedResponseID string
		appendedModel      string
	)

	sessionOut := func(entry rocketcode.SessionEntry) error {
		id, err := store.outID(entry)
		if err != nil {
			return err
		}

		appendedMu.Lock()
		appendedID = id
		appendedResponseID = entry.ResponseID
		appendedModel = entry.Model
		appendedMu.Unlock()

		return nil
	}

	b.log.Info("starting rocketcode looper", "conversation_id", b.config.ConversationID, "turn_id", turnID, "agent", b.config.Agent)

	group.Go(func() error { return looper.Loop(ctx, input, sessionIn, sessionOut, interrupts) })

	looperStarted := time.Now()
	firstOutput := false

	firstOutputTimer := time.AfterFunc(30*time.Second, func() {
		b.log.Warn("rocketcode turn has no first output yet", "conversation_id", b.config.ConversationID, "turn_id", turnID, "elapsed_ms", time.Since(looperStarted).Milliseconds(), "entry_count", len(observed), "replay_item_count", replayItemCount, "history_bytes", historyBytes, "compaction_count", compactionCount)
	})
	defer firstOutputTimer.Stop()

	for item := range output {
		if !firstOutput {
			firstOutput = true

			firstOutputTimer.Stop()
			b.log.Info("received first rocketcode response item", "conversation_id", b.config.ConversationID, "turn_id", turnID, "kind", item.Kind, "elapsed_ms", time.Since(looperStarted).Milliseconds())
		}

		if publish {
			if err := b.processResponse(ctx, msg, &result, item); err != nil {
				return result, err
			}

			continue
		}

		if item.Kind == rocketcode.ChatResponseAssistantMessage {
			result.text = appendText(result.text, item.Text)
		}
	}

	if err := group.Wait(); err != nil {
		b.log.Info("rocketcode looper returned", "conversation_id", b.config.ConversationID, "turn_id", turnID, "duration_ms", time.Since(looperStarted).Milliseconds(), "error", err)
		return result, fmt.Errorf("run rocketcode turn: %w", err)
	}

	b.log.Info("rocketcode looper returned", "conversation_id", b.config.ConversationID, "turn_id", turnID, "duration_ms", time.Since(looperStarted).Milliseconds(), "error", nil)

	appendedMu.Lock()
	result.sessionEntryID = appendedID
	result.responseID = appendedResponseID
	result.model = appendedModel
	appendedMu.Unlock()

	result.attachments = attachments.Attachments()

	return result, nil
}

func (b *Bridge) processResponse(ctx context.Context, msg *events.InboundMessage, result *runResult, item rocketcode.ChatResponse) error {
	switch item.Kind {
	case rocketcode.ChatResponseAssistantCommentary, rocketcode.ChatResponseAssistantTool, rocketcode.ChatResponseReasoningSummary:
		thinking := rocketcodeThinkingText(item)
		if thinking == "" {
			return nil
		}

		b.log.Debug("rocketcode thinking update", "kind", item.Kind, "text_len", len([]rune(thinking)), "text", thinking)
		result.thinking = appendText(result.thinking, thinking)
		result.sequence++
		outbound := b.newOutboundMessage(msg, result.turnID, result.sequence, "", result.thinking, false)

		outbound.Targets = []events.OutputTarget{events.OutputTargetSlackMain}
		if msg != nil && msg.Source == events.SourceWebVoice {
			outbound.Targets = append(outbound.Targets, events.OutputTargetWebUI)
		}

		if err := b.bus.PublishOutbound(ctx, outbound); err != nil {
			return fmt.Errorf("publish rocketcode progress: %w", err)
		}
	case rocketcode.ChatResponseAssistantMessage:
		result.text = appendText(result.text, item.Text)

		result.sequence++
		if err := b.bus.PublishOutbound(ctx, b.newOutboundMessage(msg, result.turnID, result.sequence, result.text, "", false)); err != nil {
			return fmt.Errorf("publish rocketcode answer snapshot: %w", err)
		}
	}

	return nil
}
func formatToolDiagnostic(diagnostic *rocketcode.ToolDiagnostic) string {
	name := strings.TrimSpace(diagnostic.Name)
	if name == "" {
		name = "tool"
	}

	switch strings.TrimSpace(diagnostic.Phase) {
	case "call":
		details := formatToolCallDetails(diagnostic)
		if details == "" {
			return name + " started"
		}

		return details
	case "result":
		if strings.Contains(diagnostic.Result, "tool call denied:") {
			return diagnostic.Result
		}

		return ""
	default:
		return name
	}
}

func rocketcodeThinkingText(item rocketcode.ChatResponse) string {
	if item.Tool != nil {
		return formatToolDiagnostic(item.Tool)
	}

	if item.Subagent != nil {
		return formatSubagentDiagnostic(item.Subagent)
	}

	return strings.TrimSpace(item.Text)
}

func formatToolCallDetails(diagnostic *rocketcode.ToolDiagnostic) string {
	status := strings.TrimSpace(diagnostic.Status)
	detail := ""

	for _, raw := range []json.RawMessage{diagnostic.Action, diagnostic.Arguments} {
		if len(raw) == 0 || detail != "" {
			continue
		}

		var args map[string]any
		if err := json.Unmarshal(raw, &args); err != nil {
			detail = strings.TrimSpace(string(raw))

			continue
		}

		for _, key := range []string{"description", "command", "question", "query", "url", "filePath", "pattern", "name", "subagent_type"} {
			if text, ok := args[key].(string); ok && strings.TrimSpace(text) != "" {
				detail = strings.TrimSpace(text)

				break
			}
		}

		if detail != "" {
			continue
		}

		if queries, ok := args["queries"].([]any); ok {
			var parts []string

			for _, query := range queries {
				text, ok := query.(string)
				if !ok || strings.TrimSpace(text) == "" {
					continue
				}

				parts = append(parts, strings.TrimSpace(text))
			}

			detail = strings.Join(parts, ", ")
		}
	}

	if status == "" {
		return detail
	}

	name := strings.TrimSpace(diagnostic.Name)
	if name == "" {
		name = "tool"
	}

	if detail == "" {
		return name + " " + status
	}

	return name + " " + status + ": " + detail
}

func formatSubagentDiagnostic(diagnostic *rocketcode.SubagentDiagnostic) string {
	parts := []string{"subagent"}
	if diagnostic.Total > 0 {
		parts = append(parts, fmt.Sprintf("(%d/%d)", diagnostic.Index, diagnostic.Total))
	}

	if name := strings.TrimSpace(diagnostic.Name); name != "" {
		parts = append(parts, name)
	}

	if label := strings.TrimSpace(diagnostic.Label); label != "" {
		parts = append(parts, label)
	}

	text := strings.TrimSpace(diagnostic.Text)
	switch {
	case diagnostic.Tool != nil:
		text = formatToolDiagnostic(diagnostic.Tool)
		if text == "" {
			return ""
		}

		if name := strings.TrimSpace(diagnostic.Tool.Name); name != "" {
			text = name + ": " + text
		}
	case diagnostic.Subagent != nil:
		text = formatSubagentDiagnostic(diagnostic.Subagent)
		if text == "" {
			return ""
		}
	case diagnostic.Provider != nil:
		if text == "" {
			return ""
		}
	}

	if text == "" {
		return strings.Join(parts, " ")
	}

	return strings.Join(parts, " ") + ": " + text
}

func (b *Bridge) openAIClient() (*openai.Client, error) {
	options := []option.RequestOption{}
	if b.runtime.OpenAI.RocketCodeAuth != "chatgpt" {
		options = append(options, option.WithAPIKey(b.runtime.OpenAI.APIKey))
	}

	if b.log != nil {
		options = append(options, option.WithMiddleware(func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
			startedAt := time.Now()
			resp, err := next(req)

			status := 0
			if resp != nil {
				status = resp.StatusCode
			}

			if status != http.StatusOK || err != nil {
				errProvider := err
				if errProvider == nil {
					errProvider = fmt.Errorf("provider returned status %d", status)
				}

				b.log.Error("provider request failed", "method", req.Method, "path", req.URL.Path, "status", status, "duration", time.Since(startedAt), "error", errProvider)
			} else if time.Since(startedAt) > time.Minute {
				b.log.Info("provider request completed", "method", req.Method, "path", req.URL.Path, "status", status, "duration", time.Since(startedAt), "error", err)
			}

			return resp, err
		}))
	}

	if b.runtime.OpenAI.RocketCodeAuth != "chatgpt" && strings.TrimSpace(b.runtime.OpenAI.APIBaseURL) != "" {
		options = append(options, option.WithBaseURL(b.runtime.OpenAI.APIBaseURL))
	}

	if b.runtime.OpenAI.RocketCodeAuth == "chatgpt" {
		client, err := oai.NewChatGPTClientIn(b.runtime.Workspace, b.runtime.WorkDirName(), options...)
		if err != nil {
			return nil, fmt.Errorf("create ChatGPT OAuth OpenAI client: %w", err)
		}

		return client, nil
	}

	client := openai.NewClient(options...)

	return &client, nil
}

func (b *Bridge) anthropicClient() *anthropic.Client {
	if strings.TrimSpace(b.runtime.Anthropic.APIKey) == "" {
		return nil
	}

	options := []anthropicoption.RequestOption{anthropicoption.WithAPIKey(b.runtime.Anthropic.APIKey)}
	if strings.TrimSpace(b.runtime.Anthropic.APIBaseURL) != "" {
		options = append(options, anthropicoption.WithBaseURL(b.runtime.Anthropic.APIBaseURL))
	}

	client := anthropic.NewClient(options...)

	return &client
}

func (b *Bridge) rocketcodeProviders() (rocketcode.Providers, error) {
	openAIClient, err := b.openAIClient()
	if err != nil {
		return rocketcode.Providers{}, err
	}

	return rocketcode.Providers{OpenAI: openAIClient, Anthropic: b.anthropicClient()}, nil
}

func (b *Bridge) rocketcodeConfig(shellOutputDir string, shellEnv map[string]string, customTools ...rocketcode.Tool) rocketcode.Config {
	tools := make([]rocketcode.Tool, 0, 3+len(customTools))

	tools = append(tools, restartTool(b.config.RequestRestart, func(ctx context.Context) error {
		return b.config.SessionService.MarkRestartRequester(ctx, b.config.ConversationID)
	}), scheduleMessageTool(b.ScheduleMessage, b.log), resetScheduledMessagesTool(b.ResetScheduledMessages))
	if goal, ok, err := b.config.SessionService.Goal(b.config.ConversationID); err == nil && ok && strings.TrimSpace(goal.Status) == GoalStatusActive {
		tools = append(tools, updateGoalTool(b))
	}

	tools = append(tools, customTools...)

	return rocketcode.Config{Model: "", ReasoningEffort: "", ShellOutputDir: shellOutputDir, Diagnostics: true, ExperimentalStrongerSkills: true, ExpandPromptShellCommands: rocketcode.PromptShellCommandExpansion{PrimaryPrompts: true, SubagentPrompts: true, SkillPrompts: true, InputPrompts: false}, CompactThreshold: 0, CompactionSteering: "", ParallelToolCalls: 16, InterAgentFilter: rocketcode.InterAgentFilterConfig{Prompt: "", Model: "", ReasoningEffort: "", Verbosity: "", Permission: rocketcode.PermissionSet{Buckets: nil}}, CustomTools: tools, ShellEnv: shellEnv}
}

func interAgentFilterConfig(agents rocketcode.Agents) rocketcode.InterAgentFilterConfig {
	guardrail, ok := agents.Items["guardrail"]
	if !ok {
		return rocketcode.InterAgentFilterConfig{}
	}

	return rocketcode.InterAgentFilterConfig{Prompt: guardrail.Prompt, Model: guardrail.Model, ReasoningEffort: guardrail.ReasoningEffort, Verbosity: guardrail.Verbosity, Permission: guardrail.Permission}
}

func appendOverlayPromptToAgent(agents rocketcode.Agents, agentName string, cfg *config.Config) {
	section := overlayPromptSection(cfg, skel.OverlayInfos(cfg.Workspace, cfg.WorkDirName(), cfg.Overlays))
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
		ref := info.Ref
		if ref == "" {
			ref = "HEAD"
		}

		lines = append(lines,
			"- "+info.Spec,
			"  Git URL: "+info.URL,
			"  Ref: "+ref,
			"  Clone path: "+info.ClonePath,
		)
	}

	lines = append(lines,
		"",
		"To update an overlay:",
		"- Edit the listed clone path when the requested change belongs to that overlay.",
		"- Commit and push overlay repository changes before restart.",
		"- Uncommitted, untracked, or unconfigured files under "+filepath.Join(cfg.WorkDirName(), "overlays")+" may be discarded on startup/restart.",
		"- Do not treat generated effective files under "+filepath.Join(cfg.WorkDirName(), "agents")+", "+filepath.Join(cfg.WorkDirName(), "skills")+", "+filepath.Join(cfg.WorkDirName(), "cron")+", or "+filepath.Join(cfg.WorkDirName(), "scripts")+" as source of truth.",
		"- Restart RocketClaw after overlay source/config changes so overlays are fetched and merged again.",
		"- Local workspace agents/, skills/, cron/, and scripts/ override configured overlays.",
	)

	return strings.Join(lines, "\n")
}

func loadRocketCodeDefinitionsIn(root *os.Root, workspace, workDir string, mode toolMode) (rocketcode.Agents, rocketcode.Skills, error) {
	rootFS := root.FS()

	agentsFS, err := fs.Sub(rootFS, filepath.ToSlash(filepath.Join(workDir, "agents")))
	if err != nil {
		return rocketcode.Agents{}, rocketcode.Skills{}, fmt.Errorf("open agents dir: %w", err)
	}

	skillsFS, err := fs.Sub(rootFS, filepath.ToSlash(filepath.Join(workDir, "skills")))
	if err != nil {
		return rocketcode.Agents{}, rocketcode.Skills{}, fmt.Errorf("open skills dir: %w", err)
	}

	agentResult := rocketcode.LoadAgents(agentsFS)
	for _, err := range agentResult.Errors {
		if _, ok := errors.AsType[*rocketcode.AgentMaxRecursionError](err); ok {
			return rocketcode.Agents{}, rocketcode.Skills{}, err
		}
	}

	skillsRoot := filepath.Join(workspace, workDir, "skills")
	skillResult := rocketcode.LoadSkills(skillsFS, skillsRoot)

	tools := []string{restartToolName, scheduleMessageToolName, resetScheduledMessagesToolName, attachFilesToolName}
	if mode == toolModeCron {
		tools = append(tools, rawRunToolName)
	}

	for name := range agentResult.Agents.Items {
		agent := agentResult.Agents.Items[name]

		agent.Model = strings.TrimPrefix(agent.Model, "openai/")

		for _, tool := range tools {
			action, matched := agent.Permission.Evaluate("rocketclaw", tool)
			if matched && action == rocketcode.PermissionDeny {
				continue
			}

			if err := agent.Permission.Allow("rocketclaw", tool); err != nil {
				return rocketcode.Agents{}, rocketcode.Skills{}, fmt.Errorf("prepare agent %q permission: %w", name, err)
			}
		}

		agentResult.Agents.Items[name] = agent
	}

	return agentResult.Agents, skillResult.Skills, nil
}

// ExternalMCPAgentsIn returns agents externally selectable through MCP in workDir.
func ExternalMCPAgentsIn(workspace, workDir string) ([]string, error) {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, fmt.Errorf("open workspace root: %w", err)
	}

	defer func() { _ = root.Close() }()

	agents, _, err := loadRocketCodeDefinitionsIn(root, workspace, workDir, toolModePersistent)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(agents.Items))
	for name := range agents.Items {
		names = append(names, name)
	}

	slices.Sort(names)

	return names, nil
}

func restartTool(requestRestart func(context.Context, string) (string, error), recordRestartRequester func(context.Context) error) rocketcode.Tool {
	return rocketcode.Tool{Name: restartToolName, Description: "Restart rocketclaw only after completing an explicitly requested runtime configuration change that requires reload, such as changes to rocketclaw.json, agents/, skills/, or cron/. The reason field must explain why rocketclaw needs to restart. Do not call this after memory, ledger, audit, report, workspace, source-code, generated artifact, log, transcript, or data-file edits.", Permission: "rocketclaw", VisibilitySubjects: []string{restartToolName}, Subjects: func(json.RawMessage) ([]string, error) { return []string{restartToolName}, nil }, Parameters: map[string]any{"properties": map[string]any{"reason": map[string]any{"type": "string"}}, "required": []string{"reason"}}, Call: func(ctx context.Context, raw json.RawMessage, _ chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
		var input struct {
			Reason string `json:"reason"`
		}

		if err := json.Unmarshal(raw, &input); err != nil {
			return rocketcode.ToolResult{}, fmt.Errorf("parse restart request: %w", err)
		}

		reason := strings.TrimSpace(input.Reason)
		if reason == "" {
			return rocketcode.ToolResult{}, errors.New("reason is required")
		}

		if err := recordRestartRequester(ctx); err != nil {
			return rocketcode.ToolResult{}, err
		}

		output, err := requestRestart(ctx, reason)
		if err != nil {
			return rocketcode.ToolResult{}, err
		}

		return rocketcode.TextToolResult(output), nil
	}}
}

func scheduleMessageTool(schedule func(time.Duration, string, bool) error, logger *slog.Logger) rocketcode.Tool {
	return rocketcode.Tool{Name: scheduleMessageToolName, Description: "Schedule a message to the current rocketclaw conversation after a short delay. Set recurring to false for one-shot schedules or true to repeat until scheduled messages are reset.", Permission: "rocketclaw", VisibilitySubjects: []string{scheduleMessageToolName}, Subjects: func(json.RawMessage) ([]string, error) { return []string{scheduleMessageToolName}, nil }, Parameters: map[string]any{"properties": map[string]any{"message": map[string]any{"type": "string"}, "send_this_in": map[string]any{"type": "string"}, "recurring": map[string]any{"type": "boolean"}}, "required": []string{"message", "send_this_in", "recurring"}}, Call: func(_ context.Context, raw json.RawMessage, _ chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
		logger.Info("rocketclaw schedule message tool called")

		var input struct {
			Message    string `json:"message"`
			SendThisIn string `json:"send_this_in"`
			Recurring  bool   `json:"recurring"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return rocketcode.ToolResult{}, fmt.Errorf("parse scheduled message: %w", err)
		}

		message := input.Message
		delay, err := time.ParseDuration(input.SendThisIn)

		if strings.TrimSpace(message) == "" {
			return rocketcode.ToolResult{}, errors.New("message is required")
		}

		if err != nil {
			return rocketcode.ToolResult{}, fmt.Errorf("parse send_this_in: %w", err)
		}

		if delay <= 0 || delay > time.Hour {
			return rocketcode.ToolResult{}, errors.New("send_this_in must be greater than 0 and at most 1h")
		}

		if input.Recurring && delay < time.Minute {
			return rocketcode.ToolResult{}, errors.New("recurring send_this_in must be at least 1m")
		}

		if err := schedule(delay, message, input.Recurring); err != nil {
			logger.Error("rocketclaw schedule message tool failed", "delay", delay, "delay_ms", delay.Milliseconds(), "recurring", input.Recurring, "message_len", len([]rune(message)), "error", err)
			return rocketcode.ToolResult{}, err
		}

		if input.Recurring {
			return rocketcode.TextToolResult("scheduled recurring message every " + delay.String()), nil
		}

		return rocketcode.TextToolResult("scheduled message in " + delay.String()), nil
	}}
}

type outboundAttachmentCollector struct {
	mu          sync.Mutex
	attachments []events.OutboundAttachment
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

func (c *outboundAttachmentCollector) Tool(root *os.Root) rocketcode.Tool {
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

	return rocketcode.Tool{Name: attachFilesToolName, Description: "Queue files to attach to the final human-visible response. Call before the final response finishes.", Permission: "rocketclaw", VisibilitySubjects: []string{attachFilesToolName}, Subjects: func(json.RawMessage) ([]string, error) { return []string{attachFilesToolName}, nil }, Parameters: parameters, Call: func(_ context.Context, raw json.RawMessage, _ chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
		var input attachFilesInput
		if err := json.Unmarshal(raw, &input); err != nil {
			return rocketcode.ToolResult{}, fmt.Errorf("parse response attachments: %w", err)
		}

		attachments := make([]events.OutboundAttachment, 0, len(input.Attachments))
		for i := range input.Attachments {
			attachment, err := outboundAttachment(root, &input.Attachments[i])
			if err != nil {
				return rocketcode.ToolResult{}, err
			}

			attachments = append(attachments, attachment)
		}

		c.mu.Lock()
		c.attachments = append(c.attachments, attachments...)
		c.mu.Unlock()

		return rocketcode.TextToolResult("queued attachments for final response"), nil
	}}
}

func (c *outboundAttachmentCollector) Attachments() []events.OutboundAttachment {
	c.mu.Lock()
	defer c.mu.Unlock()

	return events.CloneOutboundAttachments(c.attachments)
}

func outboundAttachment(root *os.Root, input *outboundAttachmentInput) (events.OutboundAttachment, error) {
	name := strings.TrimSpace(input.Name)
	path := strings.TrimSpace(input.Path)
	mimeType := strings.TrimSpace(input.MIMEType)

	var data []byte

	switch {
	case input.ContentBase64 != "":
		decoded, err := base64.StdEncoding.DecodeString(input.ContentBase64)
		if err != nil {
			return events.OutboundAttachment{}, fmt.Errorf("decode attachment %q: %w", name, err)
		}

		data = decoded
	case input.Content != "":
		data = []byte(input.Content)
	case path != "":
		read, err := root.ReadFile(path)
		if err != nil {
			return events.OutboundAttachment{}, fmt.Errorf("read attachment %q: %w", path, err)
		}

		data = read

		if name == "" {
			name = filepath.Base(path)
		}
	default:
		return events.OutboundAttachment{}, fmt.Errorf("attachment %q has no content or path", name)
	}

	if name == "" {
		name = "attachment"
	}

	if mimeType == "" {
		mimeType = mime.TypeByExtension(filepath.Ext(name))
	}

	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}

	mediaType, _, err := mime.ParseMediaType(mimeType)
	if err == nil {
		mimeType = mediaType
	}

	return events.OutboundAttachment{Name: name, MIMEType: strings.ToLower(strings.TrimSpace(mimeType)), Data: append([]byte(nil), data...)}, nil
}

func resetScheduledMessagesTool(reset func() error) rocketcode.Tool {
	return rocketcode.Tool{Name: resetScheduledMessagesToolName, Description: "Delete pending scheduled messages for the current rocketclaw conversation.", Permission: "rocketclaw", VisibilitySubjects: []string{scheduleMessageToolName}, Subjects: func(json.RawMessage) ([]string, error) { return []string{scheduleMessageToolName}, nil }, Parameters: map[string]any{"properties": map[string]any{}}, Call: func(context.Context, json.RawMessage, chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
		if err := reset(); err != nil {
			return rocketcode.ToolResult{}, err
		}

		return rocketcode.TextToolResult("scheduled messages reset"), nil
	}}
}

func updateGoalTool(b *Bridge) rocketcode.Tool {
	store := b.config.SessionService
	conversationID := b.config.ConversationID

	return rocketcode.Tool{Name: updateGoalToolName, Description: "Update the active RocketClaw goal loop status for this conversation. Use complete when the goal is achieved, blocked when progress cannot continue, or paused when continuation should stop for now.", Permission: "rocketclaw", VisibilitySubjects: []string{updateGoalToolName}, Subjects: func(json.RawMessage) ([]string, error) { return []string{updateGoalToolName}, nil }, Parameters: map[string]any{"properties": map[string]any{"status": map[string]any{"type": "string", "enum": []string{GoalStatusComplete, GoalStatusBlocked, GoalStatusPaused}}, "note": map[string]any{"type": "string"}}, "required": []string{"status"}}, Call: func(ctx context.Context, raw json.RawMessage, _ chan<- rocketcode.ChatResponse) (rocketcode.ToolResult, error) {
		var input struct {
			Status string `json:"status"`
			Note   string `json:"note"`
		}
		if err := json.Unmarshal(raw, &input); err != nil {
			return rocketcode.ToolResult{}, fmt.Errorf("parse goal update: %w", err)
		}

		if input.Status == GoalStatusComplete {
			current, ok, err := store.Goal(conversationID)
			if err != nil {
				return rocketcode.ToolResult{}, err
			}

			if ok && strings.TrimSpace(current.CheckScript) != "" {
				output, passed := b.runGoalCheck(ctx, current.CheckScript)
				if !passed {
					return rocketcode.TextToolResult(output), nil
				}
			}
		}

		goal, err := store.UpdateGoalStatus(conversationID, input.Status, input.Note)
		if err != nil {
			return rocketcode.ToolResult{}, err
		}

		return rocketcode.TextToolResult("goal marked " + strings.TrimSpace(goal.Status)), nil
	}}
}

func (b *Bridge) runGoalCheck(ctx context.Context, script string) (string, bool) {
	root, err := os.OpenRoot(b.runtime.Workspace)
	if err != nil {
		return "goal check failed before execution: " + err.Error(), false
	}

	defer func() { _ = root.Close() }()

	agents, _, err := loadRocketCodeDefinitionsIn(root, b.runtime.Workspace, b.runtime.WorkDirName(), toolModePersistent)
	if err != nil {
		return "goal check failed before execution: " + err.Error(), false
	}

	agent, ok := agents.Items[b.config.Agent]
	if !ok {
		return "goal check failed before execution: active agent " + b.config.Agent + " is not configured", false
	}

	check, err := validateGoalCheckScript(root, b.runtime.Workspace, script, agent.Permission)
	if err != nil {
		return "goal check failed before execution: " + err.Error(), false
	}

	if err := root.MkdirAll(filepath.ToSlash(filepath.Join(b.runtime.WorkDirName(), ".rocketcode", "shell-outputs")), 0o755); err != nil {
		return "goal check failed before execution: " + err.Error(), false
	}

	result, err := rocketcode.RunBash(ctx, root, filepath.Join(b.runtime.Workspace, b.runtime.WorkDirName(), ".rocketcode", "shell-outputs"), nil, false, rocketcode.BashCommand{Command: check.command, Timeout: goalCheckTimeout, Workdir: "", Description: "Run goal completion check"})
	if err != nil {
		return "goal check failed before execution: " + err.Error(), false
	}

	if result.Success {
		return result.Output, true
	}

	return "goal check did not pass. Continue working from this output:\n\n" + result.Output, false
}

func (b *Bridge) armScheduledMessage(id string, message *ScheduledMessageState) {
	armed := *message
	time.AfterFunc(max(time.Until(armed.DueAt), 0), func() {
		var (
			stored  ScheduledMessageState
			threads map[string]ThreadState
			ready   bool
		)

		if err := b.config.SessionService.updateState(func(state *State) {
			current, ok := state.ScheduledMessages[id]
			if !ok || current.ConversationID != armed.ConversationID || !current.DueAt.Equal(armed.DueAt) {
				return
			}

			stored = current
			threads = maps.Clone(state.Threads)
			ready = true

			if current.Recurring {
				current.DueAt = time.Now().UTC().Add(current.Interval)
				state.ScheduledMessages[id] = current
				stored = current
			}
		}); err != nil {
			b.log.Error("prepare scheduled message", "scheduled_message_id", id, "conversation_id", armed.ConversationID, "error", err)
			return
		}

		if !ready {
			b.log.Warn("scheduled message missing or stale at due time", "scheduled_message_id", id, "conversation_id", armed.ConversationID)
			return
		}

		inbound := events.NewMainInboundMessage(events.SourceSystem, events.InboundKindPrompt, "scheduled_message", armed.Message, false)

		if rest, ok := strings.CutPrefix(armed.ConversationID, "slack-thread:"); ok {
			if channelID, threadTS, ok := strings.Cut(rest, ":"); ok {
				inbound.SlackReply = &events.SlackReplyTarget{ChannelID: channelID, MessageTS: threadTS, ThreadTS: threadTS}
			}
		} else if threadID, ok := strings.CutPrefix(armed.ConversationID, "discord-thread:"); ok {
			inbound.DiscordReply = &events.DiscordReplyTarget{ChannelID: threadID, ThreadID: threadID}
		} else if strings.HasPrefix(armed.ConversationID, externalMCPConversationPrefix) {
			for conversationID, thread := range threads {
				if strings.TrimSpace(thread.SeededFromResponse) != armed.ConversationID {
					continue
				}

				if rest, ok := strings.CutPrefix(conversationID, "slack-thread:"); ok {
					channelID, threadTS, ok := strings.Cut(rest, ":")
					if !ok {
						continue
					}

					inbound.SlackReply = &events.SlackReplyTarget{ChannelID: channelID, MessageTS: threadTS, ThreadTS: threadTS}

					break
				}

				if threadID, ok := strings.CutPrefix(conversationID, "discord-thread:"); ok {
					inbound.DiscordReply = &events.DiscordReplyTarget{ChannelID: threadID, ThreadID: threadID}
					break
				}
			}
		}

		inbound.ConversationID = b.config.ConversationID
		if err := b.enqueue(context.Background(), bridgeRequest{inbound: inbound, scheduledMessageID: id, scheduledMessageRecurring: stored.Recurring}, "submit scheduled message"); err != nil {
			b.log.Error("scheduled message enqueue failed", "scheduled_message_id", id, "conversation_id", armed.ConversationID, "error", err)
			return
		}

		if stored.Recurring {
			b.armScheduledMessage(id, &stored)
		}

		b.log.Info("scheduled message enqueued", "scheduled_message_id", id, "conversation_id", armed.ConversationID, "recurring", stored.Recurring, "queue_len", len(b.requestCh))
	})
}

func (b *Bridge) newOutboundMessage(msg *events.InboundMessage, turnID string, sequence int, text, thinking string, complete bool) *events.OutboundMessage {
	source := events.SourceSystem
	if msg != nil {
		source = msg.Source
	}

	outbound := events.NewMainOutboundMessage(source, text, b.config.OutputTargets...)
	outbound.SlackThinking = thinking
	outbound.ConversationID = b.config.ConversationID
	outbound.TurnID = turnID
	outbound.Sequence = sequence

	outbound.Complete = complete
	if msg != nil {
		outbound.WebSessionID = msg.WebSessionID
		if msg.SlackReply != nil {
			outbound.SlackReply = &events.SlackReplyTarget{ChannelID: msg.SlackReply.ChannelID, MessageTS: msg.SlackReply.MessageTS, ThreadTS: msg.SlackReply.ThreadTS}
		}

		if msg.DiscordReply != nil {
			outbound.DiscordReply = &events.DiscordReplyTarget{ChannelID: msg.DiscordReply.ChannelID, MessageID: msg.DiscordReply.MessageID, ThreadID: msg.DiscordReply.ThreadID}
		}
	}

	if msg != nil && msg.Source == events.SourceWebVoice {
		targets := make([]events.OutputTarget, 0, len(outbound.Targets)+1)
		for _, target := range outbound.Targets {
			if target != events.OutputTargetDiscord {
				targets = append(targets, target)
			}
		}

		if !slices.Contains(targets, events.OutputTargetWebUI) {
			targets = append(targets, events.OutputTargetWebUI)
		}

		outbound.Targets = targets
	}

	return outbound
}

type replayInputMessage struct {
	role string
	text string
}

func replayInputForMessage(role, text string) ([]json.RawMessage, error) {
	message := responses.EasyInputMessageParam{Role: responses.EasyInputMessageRole(role), Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(text)}, Type: "message"}

	raw, err := rocketcode.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{{OfMessage: &message}})
	if err != nil {
		return nil, fmt.Errorf("encode replay input message: %w", err)
	}

	return raw, nil
}

func replayInputMessages(raw []json.RawMessage) ([]replayInputMessage, error) {
	items, err := rocketcode.ReplayInputToParams(raw)
	if err != nil {
		return nil, fmt.Errorf("decode replay input messages: %w", err)
	}

	messages := []replayInputMessage{}

	for i := range items {
		role, text, ok := replayInputMessageRoleText(&items[i])
		if ok && strings.TrimSpace(text) != "" {
			messages = append(messages, replayInputMessage{role: role, text: text})
		}
	}

	return messages, nil
}

func replayInputMessageRoleText(item *responses.ResponseInputItemUnionParam) (role, text string, ok bool) {
	if item.OfMessage != nil {
		return string(item.OfMessage.Role), item.OfMessage.Content.OfString.Value, true
	}

	if item.OfInputMessage == nil {
		return "", "", false
	}

	parts := make([]string, 0, len(item.OfInputMessage.Content))
	for i := range item.OfInputMessage.Content {
		text := item.OfInputMessage.Content[i].GetText()
		if text != nil {
			parts = append(parts, *text)
		}
	}

	return item.OfInputMessage.Role, strings.Join(parts, ""), true
}

func replayInputRawKind(raw json.RawMessage) string {
	var object struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}

	return object.Type
}

func compactedOutputToReplayInput(items []responses.ResponseOutputItemUnion) ([]json.RawMessage, error) {
	input := make([]responses.ResponseInputItemUnionParam, 0, len(items))
	for i := range items {
		switch items[i].Type {
		case "message":
			parts := make([]string, 0, len(items[i].Content))
			for j := range items[i].Content {
				if items[i].Content[j].Type == "output_text" {
					parts = append(parts, items[i].Content[j].Text)
				}
			}

			role := strings.TrimSpace(string(items[i].Role))
			if role == "" {
				role = "user"
			}

			message := responses.EasyInputMessageParam{Role: responses.EasyInputMessageRole(role), Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(strings.Join(parts, ""))}, Type: "message"}
			if items[i].Phase != "" {
				message.Phase = responses.EasyInputMessagePhase(items[i].Phase)
			}

			input = append(input, responses.ResponseInputItemUnionParam{OfMessage: &message})
		case "compaction", "compaction_summary":
			input = append(input, responses.ResponseInputItemUnionParam{OfCompaction: &responses.ResponseCompactionItemParam{ID: openai.String(items[i].ID), EncryptedContent: items[i].EncryptedContent, Type: "compaction"}})
		case "reasoning":
			summary := ""
			if len(items[i].Summary) > 0 {
				summary = items[i].Summary[0].Text
			}

			reasoning := responses.ResponseReasoningItemParam{ID: items[i].ID, Summary: []responses.ResponseReasoningItemSummaryParam{{Text: summary}}, Type: "reasoning"}
			if items[i].EncryptedContent != "" {
				reasoning.EncryptedContent = openai.String(items[i].EncryptedContent)
			}

			input = append(input, responses.ResponseInputItemUnionParam{OfReasoning: &reasoning})
		default:
			return nil, fmt.Errorf("unsupported compacted output item kind %q", items[i].Type)
		}
	}

	raw, err := rocketcode.ReplayInputFromParams(input)
	if err != nil {
		return nil, fmt.Errorf("encode compacted replay input: %w", err)
	}

	return raw, nil
}

func (b *Bridge) buildPrompt(msg *events.InboundMessage) (string, error) {
	prompt := buildPrompt(msg)

	goal, ok, err := b.config.SessionService.Goal(b.config.ConversationID)
	if err != nil {
		return "", fmt.Errorf("load active goal: %w", err)
	}

	if !ok || strings.TrimSpace(goal.Status) != GoalStatusActive {
		return prompt, nil
	}

	return prompt + "\n\n" + goalSteeringPrompt(&goal), nil
}

func buildPrompt(msg *events.InboundMessage) string {
	label := "User message"
	instruction := "Reply in plain text suitable for both Slack and text-to-speech. Avoid markdown unless it is necessary."

	body := strings.TrimSpace(msg.Text)
	if msg.Kind == events.InboundKindInternalize {
		label = "Internal note"
		instruction = "Internalize the following note into the active conversation state exactly as written. Respect the content of the message and do not paraphrase, summarize, translate, or normalize whitespace. Do not reply or acknowledge it unless the human explicitly asks you to."
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

	return instruction + "\n\n" + label + ":\n" + body
}

func goalSteeringPrompt(goal *GoalState) string {
	turnBudget := "unlimited"
	if goal.MaxTurns > 0 {
		turnBudget = fmt.Sprintf("%d of %d turns used", goal.TurnsUsed, goal.MaxTurns)
	}

	prompt := "Active goal loop:\nObjective:\n" + strings.TrimSpace(goal.Objective) + "\n\nTurn budget: " + turnBudget
	if checkScript := strings.TrimSpace(goal.CheckScript); checkScript != "" {
		prompt += "\n\nCompletion check command:\n" + checkScript + "\n\nCalling rocketclaw_update_goal with status complete runs the check command. If the check fails, use the returned failure output to continue working instead of declaring done."
	}

	return prompt + "\n\nContinue making concrete progress toward the objective. When the objective is achieved, call rocketclaw_update_goal with status complete. If progress cannot continue, call rocketclaw_update_goal with status blocked. If the loop should stop for now, call rocketclaw_update_goal with status paused."
}

func goalContinuationPrompt(goal *GoalState) string {
	return "Continue the active goal loop.\n\n" + goalSteeringPrompt(goal)
}

func externalMCPMetadataEnv(conversationID string, metadata map[string]string) map[string]string {
	env := map[string]string{rocketclawConversationIDEnv: strings.TrimSpace(conversationID)}
	for key, value := range metadata {
		env[rocketclawMetadataEnvPrefix+externalMCPMetadataEnvKey(key)] = value
	}

	return env
}

func externalMCPMetadataEnvKey(key string) string {
	return strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' {
			return r
		}

		return '_'
	}, strings.ToUpper(key))
}

func externalMCPStoredMetadataEnv(conversationID string, entries []ObservedSessionEntry) (map[string]string, bool) {
	conversationLine := rocketclawConversationIDEnv + "=" + strconv.Quote(conversationID)

	for i := range slices.Backward(entries) {
		entry := entries[i].Entry
		if entry.Type != externalMCPMetadataEntryType {
			continue
		}

		messages, err := replayInputMessages(entry.ReplayInput)
		if err != nil {
			continue
		}

		for j := range messages {
			if !strings.Contains(messages[j].text, conversationLine) {
				continue
			}

			env := map[string]string{}

			for line := range strings.SplitSeq(messages[j].text, "\n") {
				key, value, ok := strings.Cut(line, "=")
				if ok && strings.HasPrefix(key, "ROCKETCLAW_") {
					value, _ = strconv.Unquote(value)
					env[key] = value
				}
			}

			return env, true
		}
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

func attachmentFallback(msg *events.InboundMessage) string {
	if len(msg.Attachments) > 0 {
		return ""
	}

	if !msg.HadAttachments && !msg.HadNonImageAttachments {
		return ""
	}

	fallback := unsupportedFileFallback
	if msg.HadAttachments {
		fallback = attachmentAccessFallback
	}

	if notes := attachmentWarningsText(msg.AttachmentWarnings); notes != "" {
		fallback += "\n\nAttachment notes:\n" + notes
	}

	return fallback
}

func normalizeInboundAttachments(msg *events.InboundMessage) {
	if len(msg.Attachments) == 0 {
		return
	}

	msg.HadAttachments = true
	attachments := make([]events.InboundAttachment, 0, len(msg.Attachments))
	totalBytes := 0

	for i := range msg.Attachments {
		attachment := msg.Attachments[i]

		name := strings.TrimSpace(attachment.Name)
		if name == "" {
			name = fmt.Sprintf("attachment-%d", i+1)
		}

		data := append([]byte(nil), attachment.Data...)

		mimeType := modelAttachmentMIMEType(data, attachment.MIMEType, name)
		if len(data) == 0 {
			msg.AttachmentWarnings = append(msg.AttachmentWarnings, "Skipped attachment "+name+" because it was empty.")
			continue
		}

		if !isSupportedInboundAttachmentMIME(mimeType) {
			msg.AttachmentWarnings = append(msg.AttachmentWarnings, "Skipped attachment "+name+" because "+mimeType+" is not supported.")
			continue
		}

		targetLimit := min(maxInboundAttachmentBytes, maxInboundAttachmentTotalBytes-totalBytes)
		if targetLimit <= 0 {
			msg.AttachmentWarnings = append(msg.AttachmentWarnings, "Skipped attachment "+name+" because the message exceeded the attachment size budget.")
			continue
		}

		if len(data) > maxInboundAttachmentResizeInput {
			msg.AttachmentWarnings = append(msg.AttachmentWarnings, "Skipped attachment "+name+" because it was too large to attempt size reduction.")
			continue
		}

		data, mimeType, _, err := fitInboundImageWithinLimit(mimeType, data, targetLimit)
		if err != nil {
			msg.AttachmentWarnings = append(msg.AttachmentWarnings, "Skipped attachment "+name+" because "+inboundAttachmentReductionFailureReason(err, targetLimit)+".")
			continue
		}

		totalBytes += len(data)
		attachments = append(attachments, events.InboundAttachment{Name: name, MIMEType: mimeType, Data: data})
	}

	msg.Attachments = attachments
}

func modelAttachmentMIMEType(data []byte, declaredMIMEType, name string) string {
	if len(data) > 0 {
		return normalizeMIMEType(http.DetectContentType(data))
	}

	if mimeType := normalizeMIMEType(declaredMIMEType); mimeType != "" {
		return mimeType
	}

	return normalizeMIMEType(mime.TypeByExtension(filepath.Ext(name)))
}

func normalizeMIMEType(mimeType string) string {
	if mediaType, _, err := mime.ParseMediaType(mimeType); err == nil {
		mimeType = mediaType
	}

	return strings.ToLower(strings.TrimSpace(mimeType))
}

func isSupportedInboundAttachmentMIME(mimeType string) bool {
	switch normalizeMIMEType(mimeType) {
	case "image/jpeg", "image/jpg", "image/png", "image/webp":
		return true
	default:
		return false
	}
}

func fitInboundImageWithinLimit(mimeType string, data []byte, targetLimit int) (transformedData []byte, transformedMIMEType string, changed bool, err error) {
	mimeType = normalizeMIMEType(mimeType)
	if len(data) <= targetLimit {
		return data, mimeType, false, nil
	}

	if targetLimit <= 0 {
		return nil, "", false, errInboundAttachmentReductionNotEnough
	}

	if mimeType == "image/png" {
		transformed, changed, err := resizePNGWithinLimit(data, targetLimit)
		if err == nil {
			return transformed, mimeType, changed, nil
		}

		if !errors.Is(err, errInboundAttachmentReductionNotEnough) {
			return nil, "", false, err
		}
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", false, fmt.Errorf("%w: decode image: %w", errInboundAttachmentReductionFailed, err)
	}

	transformed, transformedMIMEType, err := lossyReduceInboundImageWithinLimit(img, targetLimit)
	if err != nil {
		return nil, "", false, err
	}

	return transformed, transformedMIMEType, true, nil
}

func resizePNGWithinLimit(data []byte, targetLimit int) (transformed []byte, changed bool, err error) {
	if targetLimit <= 0 {
		return nil, false, errInboundAttachmentReductionNotEnough
	}

	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false, fmt.Errorf("%w: decode png: %w", errInboundAttachmentReductionFailed, err)
	}

	encoded, err := encodeInboundPNG(src)
	if err != nil {
		return nil, false, fmt.Errorf("%w: encode png: %w", errInboundAttachmentReductionFailed, err)
	}

	if len(encoded) <= targetLimit {
		return encoded, !bytes.Equal(encoded, data), nil
	}

	originalBounds := src.Bounds()

	originalWidth, originalHeight := originalBounds.Dx(), originalBounds.Dy()
	if originalWidth <= 1 || originalHeight <= 1 {
		return nil, false, errInboundAttachmentReductionNotEnough
	}

	transformed, err = reduceResizedImageWithinLimit(src, len(encoded), targetLimit, func(img image.Image, targetLimit int) ([]byte, int, error) {
		encoded, err := encodeInboundPNG(img)
		if err != nil {
			return nil, 0, fmt.Errorf("%w: encode resized png: %w", errInboundAttachmentReductionFailed, err)
		}

		if len(encoded) <= targetLimit {
			return encoded, len(encoded), nil
		}

		return nil, len(encoded), nil
	})

	return transformed, transformed != nil, err
}

func lossyReduceInboundImageWithinLimit(img image.Image, targetLimit int) (transformed []byte, transformedMIMEType string, err error) {
	flattened := flattenInboundImageForJPEG(img)

	candidate, candidateSize, err := encodeInboundImageAsJPEGWithinLimit(flattened, targetLimit)
	if err != nil {
		return nil, "", err
	}

	if candidate != nil {
		return candidate, "image/jpeg", nil
	}

	candidate, err = reduceResizedImageWithinLimit(flattened, candidateSize, targetLimit, encodeInboundImageAsJPEGWithinLimit)
	if err != nil {
		return nil, "", err
	}

	return candidate, "image/jpeg", nil
}

func reduceResizedImageWithinLimit(src image.Image, currentSize, targetLimit int, encode func(image.Image, int) ([]byte, int, error)) ([]byte, error) {
	bounds := src.Bounds()

	currentWidth, currentHeight := bounds.Dx(), bounds.Dy()
	for range maxInboundAttachmentResizeAttempts {
		if currentWidth <= 1 || currentHeight <= 1 {
			break
		}

		nextWidth, nextHeight := nextImageResizeDimensions(currentWidth, currentHeight, currentSize, targetLimit)
		if nextWidth >= currentWidth && nextHeight >= currentHeight {
			break
		}

		resized := image.NewNRGBA(image.Rect(0, 0, nextWidth, nextHeight))
		xdraw.CatmullRom.Scale(resized, resized.Bounds(), src, bounds, xdraw.Over, nil)

		candidate, candidateSize, err := encode(resized, targetLimit)
		if err != nil {
			return nil, err
		}

		if candidate != nil {
			return candidate, nil
		}

		currentWidth, currentHeight, currentSize = nextWidth, nextHeight, candidateSize
	}

	return nil, errInboundAttachmentReductionNotEnough
}

func flattenInboundImageForJPEG(src image.Image) *image.NRGBA {
	bounds := src.Bounds()

	flattened := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := src.At(x, y).RGBA()
			alpha := float64(a) / 0xffff
			red := uint8(math.Round((float64(r>>8) * alpha) + (255 * (1 - alpha))))
			green := uint8(math.Round((float64(g>>8) * alpha) + (255 * (1 - alpha))))
			blue := uint8(math.Round((float64(b>>8) * alpha) + (255 * (1 - alpha))))
			flattened.Set(x-bounds.Min.X, y-bounds.Min.Y, color.NRGBA{R: red, G: green, B: blue, A: 0xff})
		}
	}

	return flattened
}

func encodeInboundImageAsJPEGWithinLimit(img image.Image, targetLimit int) (candidate []byte, candidateSize int, err error) {
	bestSize := 0

	for quality := 95; quality >= 50; quality -= 5 {
		candidate, err := encodeInboundJPEG(img, quality)
		if err != nil {
			return nil, 0, fmt.Errorf("%w: encode jpeg: %w", errInboundAttachmentReductionFailed, err)
		}

		candidateSize := len(candidate)
		if bestSize == 0 || candidateSize < bestSize {
			bestSize = candidateSize
		}

		if candidateSize <= targetLimit {
			return candidate, candidateSize, nil
		}
	}

	if bestSize == 0 {
		return nil, 0, errInboundAttachmentReductionFailed
	}

	return nil, bestSize, nil
}

func encodeInboundJPEG(img image.Image, quality int) (data []byte, err error) {
	var buffer bytes.Buffer

	options := jpeg.Options{Quality: quality}
	if err := jpeg.Encode(&buffer, img, &options); err != nil {
		return nil, fmt.Errorf("encode jpeg: %w", err)
	}

	return buffer.Bytes(), nil
}

func encodeInboundPNG(img image.Image) (data []byte, err error) {
	var buffer bytes.Buffer

	encoder := png.Encoder{CompressionLevel: png.BestCompression, BufferPool: nil}
	if err := encoder.Encode(&buffer, img); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}

	return buffer.Bytes(), nil
}

func nextImageResizeDimensions(currentWidth, currentHeight, currentSize, targetLimit int) (nextWidth, nextHeight int) {
	scale := math.Sqrt(float64(targetLimit) / float64(currentSize))

	scale *= 0.92
	if scale >= 1 {
		scale = 0.92
	}

	nextWidth = max(1, int(math.Round(float64(currentWidth)*scale)))
	nextHeight = max(1, int(math.Round(float64(currentHeight)*scale)))

	if nextWidth >= currentWidth && currentWidth > 1 {
		nextWidth = currentWidth - 1
	}

	if nextHeight >= currentHeight && currentHeight > 1 {
		nextHeight = currentHeight - 1
	}

	return nextWidth, nextHeight
}

func inboundAttachmentReductionFailureReason(err error, targetLimit int) string {
	if errors.Is(err, errInboundAttachmentReductionFailed) {
		return "image reduction failed"
	}

	if targetLimit < maxInboundAttachmentBytes {
		return "it still exceeded the remaining attachment budget after reduction"
	}

	return "it still exceeded the per-file size limit after reduction"
}

func attachmentsFromInbound(inbound []events.InboundAttachment) []rocketcode.Attachment {
	attachments := make([]rocketcode.Attachment, 0, len(inbound))
	for i := range inbound {
		mimeType := normalizeMIMEType(inbound[i].MIMEType)
		attachments = append(attachments, rocketcode.Attachment{MIME: mimeType, Filename: inbound[i].Name, URL: "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(inbound[i].Data)})
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
