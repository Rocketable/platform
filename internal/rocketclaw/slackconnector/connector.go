// Package slackconnector bridges Slack events into rocketclaw.
package slackconnector

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/cronjob"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
)

const (
	slackFileDownloadTimeout      = 30 * time.Second
	maxSlackImageDownloadBytes    = 16 << 20
	maxSlackTextBytesPerFile      = 256 << 10
	slackTextLimit                = 3800
	slackBlockTextLimit           = 3000
	slackPreferredChunkSize       = 3200
	slackOnDemandCronPrefix       = ":repeat_one:"
	slackRobotReaction            = "robot_face"
	slackDiscordRelayReaction     = "calling"
	slackWebVoiceRelayReaction    = "studio_microphone"
	slackExternalMCPRelayReaction = "satellite_antenna"
	slackBufferedReaction         = "hourglass_flowing_sand"
	slackSummaryReaction          = "floppy_disk"
	slackMainStackKey             = "main"
	slackImmediatePlaceholder     = "_Thinking..._"
	slackAnswerPlaceholder        = "\u200B"
	slackThinkingFlushInterval    = 2 * time.Second
)

const (
	slackSummaryInProgressReaction = "hourglass_flowing_sand"
	slackSummaryCompleteReaction   = "white_check_mark"
)

var errSlackDownloadLimitExceeded = errors.New("slack file download exceeded size limit")

type humanProfileSnapshot struct {
	DisplayName, IconURL string
}

type slackInboundContent struct {
	Text                   string
	TextAttachments        []string
	Attachments            []events.InboundAttachment
	HadAttachments         bool
	HadNonImageAttachments bool
	AttachmentWarnings     []string
}

type limitedBuffer struct {
	limit int
	data  bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.data.Len()
	if remaining <= 0 {
		return 0, errSlackDownloadLimitExceeded
	}

	if len(p) > remaining {
		_, _ = b.data.Write(p[:remaining])
		return remaining, errSlackDownloadLimitExceeded
	}

	n, _ := b.data.Write(p)

	return n, nil
}

func (b *limitedBuffer) Bytes() []byte { return b.data.Bytes() }

// Connector bridges Slack DM events into the shared rocketclaw bus.
type Connector struct {
	log    *slog.Logger
	config config.SlackConfig
	bus    *events.Bus

	emergencySafeWords []string
	threadAgents       []threadAgent
	threadRouter       ThreadRouter
	oneOffCronjobs     oneOffCronjobRunner

	api          *slack.Client
	botUserID    string
	socketEvents chan slackSocketEvent
	inboundStop  context.CancelFunc

	newSocketClient func(*slack.Client) *socketmode.Client
	runSocketClient func(context.Context, *socketmode.Client) error
	ackSocketEvent  func(*socketmode.Client, socketmode.Request) error
	reconnectDelay  time.Duration

	mu       sync.Mutex
	replies  map[string]slackReplySlots
	pending  map[string]slackReplySlots
	thinking map[string]slackThinkingState
	stacks   map[string][]slackBufferedMessage

	humanProfile *humanProfileSnapshot
}

type slackReplyState struct {
	ChannelID, MessageTS string
}

type slackReplySlots struct {
	ChannelID, ThinkingTS, AnswerTS, Key string
}

type slackSocketEvent struct {
	event socketmode.Event
}

type slackThinkingState struct {
	Text  string
	State slackReplyState
	Timer *time.Timer
}

type slackBufferedMessage struct {
	Text    string
	Content slackInboundContent
	Reply   *events.SlackReplyTarget
}

type threadAgent struct {
	prefix, agent string
	preSeed       bool
}

// ThreadRouter routes Slack thread messages directly to app-owned thread bridges.
type ThreadRouter interface {
	StartThread(context.Context, string, bool, *events.InboundMessage) error
	PrepareThreadReply(context.Context, string, string) (bool, error)
	PrepareResponseThreadReply(context.Context, string, string) (bool, error)
	SubmitThreadReply(context.Context, string, string, *events.InboundMessage) (bool, error)
	SubmitResponseThreadReply(context.Context, string, string, *events.InboundMessage) (bool, error)
	SummarizeThread(context.Context, string, string) (bool, error)
	RecordResponseCheckpoint(context.Context, string, string, events.ResponseCheckpoint) error
}

type oneOffCronjobRunner interface {
	LoadOneOffCronjob(string) (cronjob.OneOffCronjob, error)
	RunOneOffCronjob(context.Context, cronjob.OneOffCronjob, *harnessbridge.RawRunProgress, func(context.Context, cronjob.RunResult, error))
}

type inertThreadRouter struct{}

func (inertThreadRouter) StartThread(context.Context, string, bool, *events.InboundMessage) error {
	return errors.New("slack thread routing is not configured")
}
func (inertThreadRouter) PrepareThreadReply(context.Context, string, string) (bool, error) {
	return false, nil
}
func (inertThreadRouter) PrepareResponseThreadReply(context.Context, string, string) (bool, error) {
	return false, nil
}
func (inertThreadRouter) SubmitThreadReply(context.Context, string, string, *events.InboundMessage) (bool, error) {
	return false, nil
}
func (inertThreadRouter) SubmitResponseThreadReply(context.Context, string, string, *events.InboundMessage) (bool, error) {
	return false, nil
}
func (inertThreadRouter) SummarizeThread(context.Context, string, string) (bool, error) {
	return false, nil
}
func (inertThreadRouter) RecordResponseCheckpoint(context.Context, string, string, events.ResponseCheckpoint) error {
	return nil
}

type inertOneOffCronjobs struct{}

func (inertOneOffCronjobs) LoadOneOffCronjob(string) (cronjob.OneOffCronjob, error) {
	return cronjob.OneOffCronjob{}, errors.New("on-demand cronjobs are not configured")
}

func (inertOneOffCronjobs) RunOneOffCronjob(ctx context.Context, _ cronjob.OneOffCronjob, _ *harnessbridge.RawRunProgress, finish func(context.Context, cronjob.RunResult, error)) {
	finish(ctx, cronjob.RunResult{}, errors.New("on-demand cronjobs are not configured"))
}

// New constructs a Slack connector.
func New(cfg *config.SlackConfig, bus *events.Bus, emergencySafeWords []string, threadAgents config.ThreadAgents, threadRouter ThreadRouter, oneOffCronjobs oneOffCronjobRunner, logger *slog.Logger) *Connector {
	api := slack.New(cfg.BotToken, slack.OptionAppLevelToken(cfg.AppToken), slack.OptionRetry(3))

	return &Connector{
		log: logger.With("component", "slack"), config: *cfg, bus: bus,
		emergencySafeWords: slices.Clone(emergencySafeWords), threadAgents: normalizeThreadAgents(threadAgents), threadRouter: threadRouter, oneOffCronjobs: oneOffCronjobs,
		api: api, socketEvents: make(chan slackSocketEvent, 50),
		newSocketClient: func(api *slack.Client) *socketmode.Client {
			return socketmode.New(api)
		},
		runSocketClient: func(ctx context.Context, client *socketmode.Client) error {
			return client.RunContext(ctx)
		},
		ackSocketEvent: func(client *socketmode.Client, req socketmode.Request) error {
			return client.Ack(req)
		},
		reconnectDelay: time.Second,
		replies:        map[string]slackReplySlots{}, pending: map[string]slackReplySlots{}, thinking: map[string]slackThinkingState{}, stacks: map[string][]slackBufferedMessage{},
	}
}

// Name returns the connector identifier used in logs.
func (c *Connector) Name() string { return "slack" }

// Start authenticates with Slack and begins consuming events.
func (c *Connector) Start(ctx context.Context) error {
	inboundCtx, inboundStop := context.WithCancel(ctx)

	auth, err := c.api.AuthTest()
	if err != nil {
		inboundStop()
		return fmt.Errorf("slack auth test failed: %w", err)
	}

	c.botUserID = auth.UserID

	profile, err := c.fetchHumanProfile(ctx)
	if err != nil {
		c.log.Warn("fetch Slack human profile", "error", err)
	} else {
		c.humanProfile = profile
	}

	c.mu.Lock()
	c.inboundStop = inboundStop
	c.mu.Unlock()

	go c.eventLoop(inboundCtx)
	go c.runSocketLoop(inboundCtx)

	return nil
}

// Stop stops Slack socket intake while leaving response delivery usable.
func (c *Connector) Stop(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.inboundStop != nil {
		c.inboundStop()
	}

	return nil
}

// SendResponse posts or updates a streamed response message in Slack.
func (c *Connector) SendResponse(ctx context.Context, msg *events.OutboundMessage) error {
	if msg == nil {
		return nil
	}

	slots, ok := c.replyState(msg.TurnID)
	if !ok && strings.TrimSpace(msg.TurnID) != "" {
		slots, ok = c.claimPendingState(msg.SlackReply)
		if ok {
			c.setReplyState(msg.TurnID, slots)
			c.log.Info("claimed Slack placeholder", "turn_id", msg.TurnID, "channel", slots.ChannelID, "thinking_ts", slots.ThinkingTS, "answer_ts", slots.AnswerTS, "reply_channel", msg.SlackReply.ChannelID, "reply_message_ts", msg.SlackReply.MessageTS, "reply_thread_ts", msg.SlackReply.ThreadTS)
		}
	}

	thinkingText := strings.TrimSpace(msg.SlackThinking)

	switch {
	case msg.Text != "" && (msg.Complete || msg.SlackPostText):
		chunks := splitSlackResponseText(msg.Text)

		var (
			posted []slackReplyState
			err    error
		)

		if msg.Complete && ok {
			if len(chunks) == 1 && slots.AnswerTS != "" {
				if _, _, _, errUpdate := c.api.UpdateMessageContext(ctx, slots.ChannelID, slots.AnswerTS, slack.MsgOptionText(chunks[0], false), slack.MsgOptionBlocks()); errUpdate != nil {
					return fmt.Errorf("update Slack answer placeholder len=%d: %w", len([]rune(chunks[0])), errUpdate)
				}

				posted = []slackReplyState{{ChannelID: slots.ChannelID, MessageTS: slots.AnswerTS}}
			} else if slots.AnswerTS != "" {
				c.deleteSlackMessage(ctx, slackReplyState{ChannelID: slots.ChannelID, MessageTS: slots.AnswerTS}, "delete Slack answer placeholder")
			}
		}

		if posted == nil {
			channelID, threadTS := slackReplyDestination(c.config.Room, msg.SlackReply)
			posted, err = c.postResponseChunks(ctx, channelID, threadTS, chunks)
		}

		if err != nil {
			return err
		}

		_, threadTS := slackReplyDestination(c.config.Room, msg.SlackReply)
		if msg.Complete && msg.Checkpoint != nil && threadTS == "" && c.threadRouter != nil {
			for i := range posted {
				if err := c.threadRouter.RecordResponseCheckpoint(ctx, posted[i].ChannelID, posted[i].MessageTS, *msg.Checkpoint); err != nil {
					return fmt.Errorf("record Slack response checkpoint: %w", err)
				}
			}
		}

	case thinkingText != "":
		if ok {
			c.bufferSlackThinking(msg.TurnID, slackReplyState{ChannelID: slots.ChannelID, MessageTS: slots.ThinkingTS}, thinkingText)
		} else {
			channelID, threadTS := slackReplyDestination(c.config.Room, msg.SlackReply)

			postedChannelID, postedThinkingTS, postedAnswerTS, err := c.postReplyPlaceholderPair(ctx, channelID, threadTS)
			if err != nil {
				return fmt.Errorf("send Slack reply placeholders len=%d: %w", len([]rune(thinkingText)), err)
			}

			slots = slackReplySlots{ChannelID: postedChannelID, ThinkingTS: postedThinkingTS, AnswerTS: postedAnswerTS}
			c.setReplyState(msg.TurnID, slots)
			c.bufferSlackThinking(msg.TurnID, slackReplyState{ChannelID: slots.ChannelID, MessageTS: slots.ThinkingTS}, thinkingText)
		}
	}

	if msg.Complete {
		if err := c.finishCompleteResponse(ctx, msg, slots, ok); err != nil {
			return err
		}
	}

	return nil
}

func slackThinkingMessage(thinking string) string {
	thinking = strings.TrimSpace(thinking)
	if thinking == "" {
		return ""
	}

	prefix := slackImmediatePlaceholder + "\n\n"
	prefixLen := len([]rune(prefix)) + len([]rune("> "))
	thinkingRunes := []rune(thinking)
	start := len(thinkingRunes)
	used := prefixLen

	for start > 0 {
		extra := 1
		if thinkingRunes[start-1] == '\n' {
			extra += 2
		}

		if used+extra >= slackBlockTextLimit {
			break
		}

		used += extra
		start--
	}

	body := strings.TrimLeftFunc(string(thinkingRunes[start:]), unicode.IsSpace)
	if body == "" {
		return slackImmediatePlaceholder
	}

	var quoted strings.Builder

	quoted.WriteString(prefix)

	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		quoted.WriteString("> ")
		quoted.WriteString(scanner.Text())
		quoted.WriteByte('\n')
	}

	return strings.TrimRight(quoted.String(), "\n")
}

func splitSlackResponseText(text string) []string {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}

	chunks := make([]string, 0, len(runes)/slackPreferredChunkSize+1)
	for len(runes) > 0 {
		if len(runes) < slackTextLimit {
			chunks = append(chunks, string(runes))
			break
		}

		end := slackChunkEnd(runes)
		chunks = append(chunks, string(runes[:end]))
		runes = runes[end:]
	}

	return chunks
}

func slackChunkEnd(runes []rune) int {
	preferredLimit := min(len(runes), slackPreferredChunkSize)
	if end := slackChunkBoundary(runes[:preferredLimit]); end > 0 {
		return end
	}

	maxLimit := min(len(runes), slackTextLimit)
	if end := slackChunkBoundary(runes[:maxLimit]); end > 0 {
		return end
	}

	return maxLimit
}

func slackChunkBoundary(runes []rune) int {
	if end := lastSlackChunkBoundary(runes, func(i int) bool {
		return i > 0 && runes[i-1] == '\n' && runes[i] == '\n'
	}); end > 0 {
		return end
	}

	if end := lastSlackChunkBoundary(runes, func(i int) bool {
		return runes[i] == '\n'
	}); end > 0 {
		return end
	}

	return lastSlackChunkBoundary(runes, func(i int) bool {
		return unicode.IsSpace(runes[i]) && runes[i] != '\n'
	})
}

func lastSlackChunkBoundary(runes []rune, match func(int) bool) int {
	for i := range slices.Backward(runes) {
		if match(i) {
			return i + 1
		}
	}

	return 0
}

func slackReplyDestination(defaultChannelID string, replyTarget *events.SlackReplyTarget) (channelID, threadTS string) {
	channelID = defaultChannelID
	if replyTarget == nil {
		return channelID, ""
	}

	if strings.TrimSpace(replyTarget.ChannelID) != "" {
		channelID = replyTarget.ChannelID
	}

	return channelID, strings.TrimSpace(replyTarget.ThreadTS)
}

// SendDiscordRelay mirrors a Discord utterance into Slack before the main session handles it.
func (c *Connector) SendDiscordRelay(ctx context.Context, text string) (*events.SlackReplyTarget, error) {
	return c.sendVoiceRelay(ctx, text, slackDiscordRelayReaction, "send Slack Discord relay", quoteDiscordRelay, false)
}

// SendWebVoiceRelay mirrors a browser web voice utterance into Slack before the main session handles it.
func (c *Connector) SendWebVoiceRelay(ctx context.Context, text string) (*events.SlackReplyTarget, error) {
	return c.sendVoiceRelay(ctx, text, slackWebVoiceRelayReaction, "send Slack web voice relay", quoteDiscordRelay, false)
}

//nolint:funcorder // Shared helper is kept next to the voice relay entrypoints.
func (c *Connector) sendVoiceRelay(
	ctx context.Context,
	text string,
	reaction string,
	errLabel string,
	quote func(string) string,
	preserveQuotedText bool,
) (*events.SlackReplyTarget, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}

	options := []slack.MsgOption{slack.MsgOptionText(text, false)}

	profile := c.humanProfile
	if profile == nil {
		fetched, err := c.fetchHumanProfile(ctx)
		if err == nil {
			c.humanProfile = fetched
			profile = fetched
		}
	}

	if profile != nil {
		if preserveQuotedText {
			options = []slack.MsgOption{slack.MsgOptionText(quote(text), false)}
		}

		options = append(options, slack.MsgOptionUsername(profile.DisplayName))
		if profile.IconURL != "" {
			options = append(options, slack.MsgOptionIconURL(profile.IconURL))
		}
	} else {
		options = []slack.MsgOption{slack.MsgOptionText(quote(text), false)}
	}

	channelID, messageTS, err := c.api.PostMessageContext(ctx, c.config.Room, options...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errLabel, err)
	}

	replyTarget := &events.SlackReplyTarget{ChannelID: channelID, MessageTS: messageTS, ThreadTS: ""}
	if _, err := c.createReplyPlaceholders(ctx, replyTarget); err != nil {
		return nil, err
	}

	c.ensureSlackStack(slackMainStackKey)
	c.addRobotReaction(ctx, replyTarget)
	c.addReaction(ctx, replyTarget, reaction, "add Slack voice relay reaction")

	return replyTarget, nil
}

// SendExternalMCPRelay mirrors an external MCP prompt into Slack before the main session handles it.
func (c *Connector) SendExternalMCPRelay(ctx context.Context, channelID, text string, attachments []events.OutboundAttachment) (*events.SlackReplyTarget, error) {
	return c.sendExternalMCPRelay(ctx, channelID, "", text, attachments)
}

// SendExternalMCPThreadRelay mirrors an external MCP follow-up into an existing Slack thread.
func (c *Connector) SendExternalMCPThreadRelay(ctx context.Context, channelID, threadTS, text string, attachments []events.OutboundAttachment) (*events.SlackReplyTarget, error) {
	return c.sendExternalMCPRelay(ctx, channelID, threadTS, text, attachments)
}

// CleanupPendingReplyPlaceholder removes a relay placeholder that no response turn claimed.
func (c *Connector) CleanupPendingReplyPlaceholder(ctx context.Context, replyTarget *events.SlackReplyTarget) {
	if slots, ok := c.claimPendingState(replyTarget); ok {
		c.deleteSlackMessage(ctx, slackReplyState{ChannelID: slots.ChannelID, MessageTS: slots.AnswerTS}, "delete Slack answer placeholder")
		c.deleteSlackMessage(ctx, slackReplyState{ChannelID: slots.ChannelID, MessageTS: slots.ThinkingTS}, "delete Slack thinking message")
	}
}

// SendCronjobChannelThread posts one scheduled cronjob result in a new Slack channel thread.
func (c *Connector) SendCronjobChannelThread(ctx context.Context, channelID, relativePath, agent, ranAt, text string, attachments []events.OutboundAttachment) error {
	postedChannelID, threadTS, err := c.api.PostMessageContext(ctx, channelID, slack.MsgOptionText("Cronjob `"+relativePath+"` ran at `"+ranAt+"` with agent `"+agent+"`.", false))
	if err != nil {
		return fmt.Errorf("send Slack cronjob thread root: %w", err)
	}

	if strings.TrimSpace(text) != "" {
		if _, err := c.postResponseChunks(ctx, postedChannelID, threadTS, splitSlackResponseText(text)); err != nil {
			return fmt.Errorf("send Slack cronjob thread reply: %w", err)
		}
	}

	if len(attachments) > 0 {
		if err := c.uploadResponseAttachments(ctx, postedChannelID, threadTS, attachments); err != nil {
			return fmt.Errorf("send Slack cronjob thread attachments: %w", err)
		}
	}

	return nil
}

func (c *Connector) deleteSlackMessage(ctx context.Context, state slackReplyState, logMessage string) {
	if strings.TrimSpace(state.ChannelID) == "" || strings.TrimSpace(state.MessageTS) == "" {
		return
	}

	if _, _, err := c.api.DeleteMessageContext(ctx, state.ChannelID, state.MessageTS); err != nil {
		c.log.Warn(logMessage, "channel", state.ChannelID, "message_ts", state.MessageTS, "error", err)
	}
}

func (c *Connector) finishCompleteResponse(ctx context.Context, msg *events.OutboundMessage, slots slackReplySlots, hasSlots bool) error {
	if len(msg.Attachments) > 0 {
		channelID, threadTS := slackReplyDestination(c.config.Room, msg.SlackReply)
		if err := c.uploadResponseAttachments(ctx, channelID, threadTS, msg.Attachments); err != nil {
			c.log.Warn("upload Slack response attachments", "error", err)
		}
	}

	if hasSlots && strings.TrimSpace(msg.Text) == "" {
		c.deleteSlackMessage(ctx, slackReplyState{ChannelID: slots.ChannelID, MessageTS: slots.AnswerTS}, "delete Slack answer placeholder")
	}

	if hasSlots {
		c.deleteSlackMessage(ctx, slackReplyState{ChannelID: slots.ChannelID, MessageTS: slots.ThinkingTS}, "delete Slack thinking message")
	}

	c.clearSlackThinking(msg.TurnID)
	c.clearReplyState(msg.TurnID)

	if msg.SlackReply != nil && strings.TrimSpace(msg.SlackReply.ChannelID) != "" && strings.TrimSpace(msg.SlackReply.MessageTS) != "" {
		if err := c.api.RemoveReactionContext(ctx, slackRobotReaction, slack.NewRefToMessage(msg.SlackReply.ChannelID, msg.SlackReply.MessageTS)); err != nil {
			c.log.Warn("remove Slack robot reaction", "channel", msg.SlackReply.ChannelID, "message_ts", msg.SlackReply.MessageTS, "error", err)
		}
	}

	if strings.TrimSpace(msg.TurnID) != "" {
		if threadKey := slackThreadStackKey(msg.SlackReply); threadKey != "" {
			channelID, threadTS := slackReplyDestination(c.config.Room, msg.SlackReply)
			c.promoteSlackStack(ctx, threadKey, func(submitCtx context.Context, inbound *events.InboundMessage) error {
				_, err := c.threadRouter.SubmitThreadReply(submitCtx, channelID, threadTS, inbound)
				if err != nil {
					return fmt.Errorf("submit buffered Slack thread reply: %w", err)
				}

				return nil
			})
		} else {
			c.promoteSlackStack(ctx, slackMainStackKey, c.bus.PublishInbound)
		}
	}

	return nil
}

func (c *Connector) uploadResponseAttachments(ctx context.Context, channelID, threadTS string, attachments []events.OutboundAttachment) error {
	for i := range attachments {
		attachment := attachments[i]

		name := strings.TrimSpace(attachment.Name)
		if name == "" {
			name = "attachment"
		}

		_, err := c.api.UploadFileContext(ctx, slack.UploadFileParameters{Reader: bytes.NewReader(attachment.Data), FileSize: len(attachment.Data), Filename: name, Title: name, Channel: channelID, ThreadTimestamp: threadTS})
		if err != nil {
			return fmt.Errorf("upload Slack attachment %q: %w", name, err)
		}
	}

	return nil
}

func (c *Connector) sendExternalMCPRelay(ctx context.Context, channelID, threadTS, text string, attachments []events.OutboundAttachment) (*events.SlackReplyTarget, error) {
	text = strings.TrimSpace(text)
	if text == "" && len(attachments) == 0 {
		return nil, nil
	}

	threadTS = strings.TrimSpace(threadTS)

	var replyTarget *events.SlackReplyTarget

	if err := func() error {
		c.mu.Lock()
		defer c.mu.Unlock()

		options := []slack.MsgOption{slack.MsgOptionText(text, false)}
		if threadTS != "" {
			options = append(options, slack.MsgOptionTS(threadTS))
		}

		postedChannelID, messageTS, err := c.api.PostMessageContext(ctx, channelID, options...)
		if err != nil {
			return fmt.Errorf("send Slack external MCP relay: %w", err)
		}

		attachmentThreadTS := threadTS
		if attachmentThreadTS == "" {
			attachmentThreadTS = messageTS
		}

		replyTarget = &events.SlackReplyTarget{ChannelID: postedChannelID, MessageTS: messageTS, ThreadTS: attachmentThreadTS}

		if len(attachments) > 0 {
			if err := c.uploadResponseAttachments(ctx, postedChannelID, attachmentThreadTS, attachments); err != nil {
				return fmt.Errorf("send Slack external MCP relay attachments: %w", err)
			}
		}

		placeholderChannelID, thinkingTS, answerTS, err := c.postReplyPlaceholderPair(ctx, postedChannelID, replyTarget.ThreadTS)
		if err != nil {
			return err
		}

		c.createReplyPlaceholderStateLocked(replyTarget, placeholderChannelID, thinkingTS, answerTS)
		c.ensureSlackStackLocked(slackThreadStackKey(replyTarget))
		c.log.Info("created Slack reply placeholders", "channel", replyTarget.ChannelID, "message_ts", replyTarget.MessageTS, "thread_ts", replyTarget.ThreadTS, "placeholder_channel", placeholderChannelID, "thinking_ts", thinkingTS, "answer_ts", answerTS)

		return nil
	}(); err != nil {
		return nil, err
	}

	c.addRobotReaction(ctx, replyTarget)
	c.addReaction(ctx, replyTarget, slackExternalMCPRelayReaction, "add Slack external MCP relay reaction")

	return replyTarget, nil
}

func (c *Connector) bufferSlackThinking(turnID string, state slackReplyState, text string) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" || strings.TrimSpace(text) == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.thinking == nil {
		c.thinking = map[string]slackThinkingState{}
	}

	pending := c.thinking[turnID]
	pending.Text = text
	pending.State = state

	if pending.Timer != nil {
		pending.Timer.Reset(slackThinkingFlushInterval)
	} else {
		pending.Timer = time.AfterFunc(slackThinkingFlushInterval, func() {
			if err := c.flushSlackThinking(context.Background(), turnID); err != nil && c.log != nil {
				c.log.Warn("flush Slack thinking update", "turn_id", turnID, "error", err)
			}
		})
	}

	c.thinking[turnID] = pending
}

func (c *Connector) flushSlackThinking(ctx context.Context, turnID string) error {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return nil
	}

	c.mu.Lock()

	pending, ok := c.thinking[turnID]
	if !ok {
		c.mu.Unlock()
		return nil
	}

	pending.Timer = nil
	c.thinking[turnID] = pending
	c.mu.Unlock()

	thinkingText := slackThinkingMessage(pending.Text)

	var err error

	if thinkingText != "" {
		block := slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, thinkingText, false, false), nil, nil)
		if _, _, _, errUpdate := c.api.UpdateMessageContext(ctx, pending.State.ChannelID, pending.State.MessageTS, slack.MsgOptionText(thinkingText, false), slack.MsgOptionBlocks(block)); errUpdate != nil {
			err = fmt.Errorf("update Slack thinking message len=%d: %w", len([]rune(thinkingText)), errUpdate)
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	current, ok := c.thinking[turnID]
	if !ok {
		return err
	}

	if err != nil {
		return err
	}

	if current.Text == pending.Text && current.Timer == nil {
		delete(c.thinking, turnID)
	}

	return nil
}

func (c *Connector) clearSlackThinking(turnID string) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	pending, ok := c.thinking[turnID]
	if !ok {
		return
	}

	if pending.Timer != nil {
		pending.Timer.Stop()
	}

	delete(c.thinking, turnID)
}

func (c *Connector) acceptMainSlackMessage(ctx context.Context, text string, content *slackInboundContent, replyTarget *events.SlackReplyTarget) bool {
	key := slackMainStackKey
	if c.bufferSlackStack(ctx, key, text, content, replyTarget) {
		return false
	}

	c.beginSlackStack(key)

	c.createReplyPlaceholdersOrWarn(ctx, replyTarget, "channel", replyTarget.ChannelID, "message_ts", replyTarget.MessageTS)

	if err := c.bus.PublishInbound(ctx, newSlackInboundMessage(text, content, replyTarget)); err != nil {
		c.log.Error("publish Slack inbound message", "error", err)
		c.finishSlackStack(key)

		return false
	}

	c.addRobotReaction(ctx, replyTarget)

	return true
}

func slackThreadStackKey(replyTarget *events.SlackReplyTarget) string {
	if replyTarget == nil {
		return ""
	}

	channelID := strings.TrimSpace(replyTarget.ChannelID)

	threadTS := strings.TrimSpace(replyTarget.ThreadTS)
	if channelID == "" || threadTS == "" {
		return ""
	}

	return "thread\x00" + channelID + "\x00" + threadTS
}

func (c *Connector) beginSlackStack(key string) {
	c.mu.Lock()
	c.stacks[key] = nil
	c.mu.Unlock()
}

func (c *Connector) bufferSlackStack(ctx context.Context, key, text string, content *slackInboundContent, replyTarget *events.SlackReplyTarget) bool {
	c.mu.Lock()

	_, active := c.stacks[key]
	if active {
		c.stacks[key] = append(c.stacks[key], slackBufferedMessage{Text: text, Content: *content, Reply: replyTarget})
	}
	c.mu.Unlock()

	if active {
		c.addReaction(ctx, replyTarget, slackBufferedReaction, "add Slack buffered reaction")
	}

	return active
}

func (c *Connector) promoteSlackStack(ctx context.Context, key string, submit func(context.Context, *events.InboundMessage) error) {
	c.mu.Lock()

	buffered, ok := c.stacks[key]
	if !ok {
		c.mu.Unlock()

		return
	}

	if len(buffered) == 0 {
		delete(c.stacks, key)
		c.mu.Unlock()

		return
	}

	c.stacks[key] = nil
	c.mu.Unlock()

	for _, msg := range buffered {
		c.removeReaction(ctx, msg.Reply, slackBufferedReaction, "remove Slack buffered reaction")
	}

	latest := buffered[len(buffered)-1].Reply
	c.addRobotReaction(ctx, latest)

	c.createReplyPlaceholdersOrWarn(ctx, latest, "channel", latest.ChannelID, "message_ts", latest.MessageTS)

	text, content := combineSlackBufferedMessages(buffered)
	if err := submit(ctx, newSlackInboundMessage(text, &content, latest)); err != nil {
		c.log.Error("publish buffered Slack inbound message", "error", err)
		c.finishSlackStack(key)
	}
}

func (c *Connector) finishSlackStack(key string) {
	c.mu.Lock()
	delete(c.stacks, key)
	c.mu.Unlock()
}

func combineSlackBufferedMessages(buffered []slackBufferedMessage) (string, slackInboundContent) {
	parts := make([]string, 0, len(buffered))

	var content slackInboundContent

	for _, msg := range buffered {
		if text := strings.TrimSpace(msg.Text); text != "" {
			parts = append(parts, text)
		}

		content.Attachments = append(content.Attachments, msg.Content.Attachments...)
		content.TextAttachments = append(content.TextAttachments, msg.Content.TextAttachments...)
		content.HadAttachments = content.HadAttachments || msg.Content.HadAttachments
		content.HadNonImageAttachments = content.HadNonImageAttachments || msg.Content.HadNonImageAttachments
		content.AttachmentWarnings = append(content.AttachmentWarnings, msg.Content.AttachmentWarnings...)
	}

	content.Text = strings.Join(parts, "\n\n")

	return content.Text, content
}

func (c *Connector) postResponseChunks(ctx context.Context, channelID, threadTS string, chunks []string) ([]slackReplyState, error) {
	posted := make([]slackReplyState, 0, len(chunks))
	for i := range chunks {
		options := []slack.MsgOption{slack.MsgOptionText(chunks[i], false)}
		if threadTS != "" {
			options = append(options, slack.MsgOptionTS(threadTS))
		}

		postedChannelID, postedTS, err := c.api.PostMessageContext(ctx, channelID, options...)
		if err != nil {
			for _, v := range slices.Backward(posted) {
				if _, _, errDelete := c.api.DeleteMessageContext(ctx, v.ChannelID, v.MessageTS); errDelete != nil {
					c.log.Warn("delete partial Slack response chunk", "channel", v.ChannelID, "message_ts", v.MessageTS, "error", errDelete)
				}
			}

			return nil, fmt.Errorf("send Slack response chunk %d/%d len=%d: %w", i+1, len(chunks), len([]rune(chunks[i])), err)
		}

		posted = append(posted, slackReplyState{ChannelID: postedChannelID, MessageTS: postedTS})
	}

	return posted, nil
}

func (c *Connector) eventLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case socketEvent := <-c.socketEvents:
			event := socketEvent.event

			if event.Request != nil {
				c.log.Debug(
					"received Slack socket event",
					"event_type", event.Type,
					"request_type", event.Request.Type,
					"envelope_id", event.Request.EnvelopeID,
					"retry_attempt", event.Request.RetryAttempt,
					"retry_reason", event.Request.RetryReason,
				)
			} else {
				c.log.Debug("received Slack socket event", "event_type", event.Type)
			}

			if event.Type == socketmode.EventTypeEventsAPI {
				c.handleEventsAPI(ctx, event)
			}
		}
	}
}

func (c *Connector) runSocketLoop(ctx context.Context) {
	for ctx.Err() == nil {
		client := c.newSocketClient(c.api)
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)

		go func() {
			done <- c.runSocketClient(runCtx, client)
		}()

		var errRun error

	clientLoop:
		for {
			select {
			case <-ctx.Done():
				cancel()

				return
			case event, ok := <-client.Events:
				if !ok {
					cancel()
					break clientLoop
				}

				if event.Type == socketmode.EventTypeEventsAPI && event.Request != nil {
					if err := c.ackSocketEvent(client, *event.Request); err != nil {
						c.log.Warn("ack Slack socket event", "error", err)
					}
				}

				select {
				case c.socketEvents <- slackSocketEvent{event: event}:
				case <-ctx.Done():
					cancel()

					return
				case errRun = <-done:
					cancel()
					break clientLoop
				}
			case errRun = <-done:
				cancel()
				break clientLoop
			}
		}

		if errRun != nil && ctx.Err() == nil {
			c.log.Warn("Slack socket mode stopped", "error", errRun)
		}

		if c.reconnectDelay > 0 {
			select {
			case <-time.After(c.reconnectDelay):
			case <-ctx.Done():
				return
			}
		}
	}
}

func (c *Connector) handleEventsAPI(ctx context.Context, event socketmode.Event) {
	eventsAPIEvent, ok := event.Data.(slackevents.EventsAPIEvent)
	if !ok {
		return
	}

	if ev, ok := eventsAPIEvent.InnerEvent.Data.(*slackevents.MessageEvent); ok {
		c.handleMessageEvent(ctx, ev)
		return
	}

	if ev, ok := eventsAPIEvent.InnerEvent.Data.(*slackevents.AppMentionEvent); ok {
		c.handleAppMentionEvent(ctx, ev)
		return
	}

	if ev, ok := eventsAPIEvent.InnerEvent.Data.(*slackevents.ReactionAddedEvent); ok {
		c.handleReactionAddedEvent(ctx, ev)
	}
}

func (c *Connector) handleMessageEvent(ctx context.Context, ev *slackevents.MessageEvent) { //nolint:gocyclo // Slack event routing is deliberately kept in arrival order.
	if ev == nil {
		c.log.Debug("ignored Slack message event", "reason", "nil_event")
		return
	}

	if ev.User == "" {
		c.log.Debug(
			"ignored Slack message event",
			"reason", "empty_user",
			"channel", ev.Channel,
			"channel_type", ev.ChannelType,
			"bot_id_present", ev.BotID != "",
		)

		return
	}

	if ev.User == c.botUserID {
		c.log.Debug(
			"ignored Slack message event",
			"reason", "bot_user",
			"user", ev.User,
			"channel", ev.Channel,
			"channel_type", ev.ChannelType,
		)

		return
	}

	if ev.BotID != "" {
		c.log.Debug(
			"ignored Slack message event",
			"reason", "bot_message",
			"user", ev.User,
			"channel", ev.Channel,
			"channel_type", ev.ChannelType,
			"bot_id_present", true,
		)

		return
	}

	subtype := strings.TrimSpace(ev.SubType)
	if subtype != "" && subtype != slack.MsgSubTypeFileShare {
		c.log.Debug(
			"ignored Slack message event",
			"reason", "unsupported_subtype",
			"user", ev.User,
			"channel", ev.Channel,
			"channel_type", ev.ChannelType,
			"subtype", subtype,
		)

		return
	}

	threadTS := strings.TrimSpace(ev.ThreadTimeStamp)
	dmMessage := ev.Channel == c.config.Room && ev.User == c.config.HumanUserID && strings.HasPrefix(ev.Channel, "D")

	socialThreadReply := c.config.SocialMode.Enabled && threadTS != "" && !strings.HasPrefix(ev.Channel, "D") && c.socialModeAllowsUser(ev.User)

	text := strings.TrimSpace(slackMessageEventText(ev))
	if socialThreadReply {
		text = c.stripSlackBotMention(text)
	}

	fileCount := len(slackMessageEventFiles(ev))
	c.log.Debug(
		"received Slack message event",
		"user", ev.User,
		"channel", ev.Channel,
		"message_ts", ev.TimeStamp,
		"channel_type", ev.ChannelType,
		"subtype", subtype,
		"thread_ts_present", threadTS != "",
		"text_len", len(text),
		"file_count", fileCount,
		"room_match", ev.Channel == c.config.Room,
		"human_match", ev.User == c.config.HumanUserID,
		"dm_channel", strings.HasPrefix(ev.Channel, "D"),
		"dm_message", dmMessage,
		"social_thread_reply", socialThreadReply,
	)

	if !dmMessage && !socialThreadReply {
		c.log.Debug(
			"ignored Slack message event",
			"reason", "not_dm_or_allowed_social_thread",
			"user", ev.User,
			"channel", ev.Channel,
			"channel_type", ev.ChannelType,
			"thread_ts_present", threadTS != "",
			"room_match", ev.Channel == c.config.Room,
			"human_match", ev.User == c.config.HumanUserID,
			"dm_channel", strings.HasPrefix(ev.Channel, "D"),
			"social_thread_reply", socialThreadReply,
		)

		return
	}

	if text == "" && fileCount == 0 {
		c.log.Debug(
			"ignored Slack message event",
			"reason", "empty_text_and_no_files",
			"user", ev.User,
			"channel", ev.Channel,
			"channel_type", ev.ChannelType,
			"thread_ts_present", threadTS != "",
		)

		return
	}

	normalizedText := strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r):
			return unicode.ToLower(r)
		case unicode.IsDigit(r):
			return r
		default:
			return -1
		}
	}, text)
	if slices.Contains(c.emergencySafeWords, normalizedText) {
		os.Exit(254)
	}

	replyTarget := &events.SlackReplyTarget{ChannelID: ev.Channel, MessageTS: ev.TimeStamp, ThreadTS: threadTS}
	if threadTS != "" {
		handled, err := c.threadRouter.PrepareThreadReply(ctx, ev.Channel, threadTS)
		if err != nil {
			c.log.Error("prepare Slack thread reply", "error", err, "channel", ev.Channel, "thread_ts", threadTS)
			return
		}

		responseRooted := false

		if !handled {
			if socialThreadReply {
				return
			}

			responseRooted, err = c.threadRouter.PrepareResponseThreadReply(ctx, ev.Channel, threadTS)
			if err != nil {
				c.log.Error("prepare Slack response-rooted thread reply", "error", err, "channel", ev.Channel, "thread_ts", threadTS)
				return
			}

			if !responseRooted {
				if c.isBotAuthoredSlackMessage(ctx, ev.Channel, threadTS) {
					if err := c.postSlackThreadReply(ctx, ev.Channel, threadTS, "I can’t start an inherited-context thread from that older response because it was sent before thread checkpoints were recorded. Reply to a newer AI response, or ask again in the main DM."); err != nil {
						c.log.Warn("post Slack response-rooted thread failure", "error", err, "channel", ev.Channel, "thread_ts", threadTS)
					}
				}

				return
			}
		}

		content := c.inboundContentForMessageEvent(ctx, ev)
		content.Text = text

		key := slackThreadStackKey(replyTarget)
		if c.bufferSlackStack(ctx, key, content.Text, &content, replyTarget) {
			return
		}

		c.beginSlackStack(key)

		c.createReplyPlaceholdersOrWarn(ctx, replyTarget, "channel", ev.Channel, "message_ts", ev.TimeStamp, "thread_ts", threadTS)

		inbound := newSlackInboundMessage(content.Text, &content, replyTarget)

		// Log reading guide: correlate by channel/message_ts/thread_ts. A pre-turn stuck placeholder is proven by a created placeholder, this handoff with pending_placeholder=true, then a submission failure before bridge/rocketcode logs and no later claimed-placeholder log.
		c.log.Info("handing Slack thread reply to router", "channel", ev.Channel, "message_ts", ev.TimeStamp, "thread_ts", threadTS, "response_rooted", responseRooted, "pending_placeholder", c.hasPendingState(replyTarget))

		if responseRooted {
			handled, err = c.threadRouter.SubmitResponseThreadReply(ctx, ev.Channel, threadTS, inbound)
		} else {
			handled, err = c.threadRouter.SubmitThreadReply(ctx, ev.Channel, threadTS, inbound)
		}

		if err != nil {
			c.log.Error("submit Slack thread reply", "error", err, "channel", ev.Channel, "message_ts", ev.TimeStamp, "thread_ts", threadTS, "pending_placeholder", c.hasPendingState(replyTarget))
			c.finishSlackStack(key)

			return
		}

		if !handled {
			c.log.Warn("Slack thread reply was not handled after placeholder", "channel", ev.Channel, "message_ts", ev.TimeStamp, "thread_ts", threadTS, "response_rooted", responseRooted, "pending_placeholder", c.hasPendingState(replyTarget))
			c.finishSlackStack(key)

			return
		}

		c.addRobotReaction(ctx, replyTarget)
		c.log.Info(
			"accepted Slack thread reply",
			"user", ev.User,
			"channel", ev.Channel,
			"thread_ts", threadTS,
			"text_len", len(text),
			"attachment_count", len(content.Attachments),
		)

		return
	}

	if strings.HasPrefix(text, slackOnDemandCronPrefix) || strings.HasPrefix(text, "🔂") {
		target := strings.TrimSpace(strings.TrimPrefix(text, slackOnDemandCronPrefix))
		if after, ok := strings.CutPrefix(text, "🔂"); ok {
			target = strings.TrimSpace(after)
		}

		replyTarget.ThreadTS = ev.TimeStamp
		c.handleOnDemandCronRequest(ctx, ev, target, replyTarget)

		return
	}

	if agent, preSeed, promptText, ok := c.threadAgentForText(text); ok {
		replyTarget.ThreadTS = ev.TimeStamp
		key := slackThreadStackKey(replyTarget)
		c.beginSlackStack(key)

		c.createReplyPlaceholdersOrWarn(ctx, replyTarget, "channel", ev.Channel, "message_ts", ev.TimeStamp, "agent", agent)

		content := c.inboundContentForMessageEvent(ctx, ev)
		content.Text = text
		inbound := newSlackInboundMessage(promptText, &content, replyTarget)
		c.log.Info("handing Slack thread start to router", "channel", ev.Channel, "message_ts", ev.TimeStamp, "agent", agent, "pending_placeholder", c.hasPendingState(replyTarget))

		if err := c.threadRouter.StartThread(ctx, agent, preSeed, inbound); err != nil {
			c.log.Error("start Slack thread", "error", err, "channel", ev.Channel, "message_ts", ev.TimeStamp, "agent", agent, "pending_placeholder", c.hasPendingState(replyTarget))
			c.finishSlackStack(key)

			return
		}

		c.addRobotReaction(ctx, replyTarget)
		c.log.Info(
			"accepted Slack thread start",
			"user", ev.User,
			"channel", ev.Channel,
			"message_ts", ev.TimeStamp,
			"agent", agent,
			"text_len", len(promptText),
			"attachment_count", len(content.Attachments),
		)

		return
	}

	replyTarget.ThreadTS = ""
	content := c.inboundContentForMessageEvent(ctx, ev)

	content.Text = text
	if !c.acceptMainSlackMessage(ctx, text, &content, replyTarget) {
		return
	}

	c.log.Info(
		"accepted Slack inbound message",
		"user", ev.User,
		"channel", ev.Channel,
		"subtype", subtype,
		"text_len", len(text),
		"attachment_count", len(content.Attachments),
	)
}

func (c *Connector) isBotAuthoredSlackMessage(ctx context.Context, channelID, messageTS string) bool {
	channelID = strings.TrimSpace(channelID)

	messageTS = strings.TrimSpace(messageTS)
	if channelID == "" || messageTS == "" {
		return false
	}

	history, err := c.api.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{
		ChannelID:          channelID,
		Cursor:             "",
		Latest:             messageTS,
		Oldest:             messageTS,
		Inclusive:          true,
		Limit:              1,
		IncludeAllMetadata: false,
	})
	if err != nil {
		c.log.Warn("load Slack thread root history", "error", err, "channel", channelID, "message_ts", messageTS)
		return false
	}

	if history == nil || len(history.Messages) == 0 {
		return false
	}

	message := history.Messages[0]

	return strings.TrimSpace(message.BotID) != "" || strings.TrimSpace(message.User) == strings.TrimSpace(c.botUserID)
}
func (c *Connector) handleReactionAddedEvent(ctx context.Context, ev *slackevents.ReactionAddedEvent) {
	if ev == nil {
		return
	}

	if strings.TrimSpace(ev.Reaction) != slackSummaryReaction {
		return
	}

	if ev.Item.Type != "message" {
		return
	}

	channelID := strings.TrimSpace(ev.Item.Channel)

	messageTS := strings.TrimSpace(ev.Item.Timestamp)
	if channelID == "" || messageTS == "" {
		return
	}

	if strings.HasPrefix(channelID, "D") {
		if ev.User != c.config.HumanUserID || channelID != c.config.Room {
			return
		}
	} else if !c.config.SocialMode.Enabled || !c.socialModeAllowsUser(ev.User) {
		return
	}

	threadTS, handled, err := c.resolveManagedThreadTS(ctx, channelID, messageTS)
	if err != nil {
		c.log.Error("resolve Slack thread summary target", "error", err, "channel", channelID, "message_ts", messageTS)
		return
	}

	if !handled {
		return
	}

	statusTarget := &events.SlackReplyTarget{ChannelID: channelID, MessageTS: messageTS, ThreadTS: threadTS}
	c.removeReaction(ctx, statusTarget, slackSummaryInProgressReaction, "remove Slack thread summary in-progress reaction")
	c.removeReaction(ctx, statusTarget, slackSummaryCompleteReaction, "remove Slack thread summary complete reaction")
	c.addReaction(ctx, statusTarget, slackSummaryInProgressReaction, "add Slack thread summary in-progress reaction")

	handled, err = c.threadRouter.SummarizeThread(ctx, channelID, threadTS)
	c.removeReaction(ctx, statusTarget, slackSummaryInProgressReaction, "remove Slack thread summary in-progress reaction")

	if err != nil {
		c.log.Error("summarize Slack thread", "error", err, "channel", channelID, "thread_ts", threadTS)

		if errPost := c.postSlackThreadReply(ctx, channelID, threadTS, "I couldn't summarize this Slack thread right now."); errPost != nil {
			c.log.Warn("post Slack thread summary failure", "error", errPost, "channel", channelID, "thread_ts", threadTS)
		}

		return
	}

	if !handled {
		return
	}

	c.addReaction(ctx, statusTarget, slackSummaryCompleteReaction, "add Slack thread summary complete reaction")

	c.log.Info("accepted Slack thread summary request", "user", ev.User, "channel", channelID, "thread_ts", threadTS, "message_ts", messageTS)
}

func (c *Connector) addRobotReaction(ctx context.Context, replyTarget *events.SlackReplyTarget) {
	c.addReaction(ctx, replyTarget, slackRobotReaction, "add Slack robot reaction")
}

func (c *Connector) addReaction(ctx context.Context, replyTarget *events.SlackReplyTarget, reaction, logMessage string) {
	if replyTarget == nil || strings.TrimSpace(replyTarget.ChannelID) == "" || strings.TrimSpace(replyTarget.MessageTS) == "" {
		return
	}

	if err := c.api.AddReactionContext(ctx, reaction, slack.NewRefToMessage(replyTarget.ChannelID, replyTarget.MessageTS)); err != nil {
		c.log.Warn(logMessage, "channel", replyTarget.ChannelID, "message_ts", replyTarget.MessageTS, "error", err)
	}
}

func (c *Connector) handleAppMentionEvent(ctx context.Context, ev *slackevents.AppMentionEvent) {
	if ev == nil || !c.config.SocialMode.Enabled {
		return
	}

	if ev.User == "" || ev.User == c.botUserID || ev.BotID != "" || strings.HasPrefix(ev.Channel, "D") || !c.socialModeAllowsUser(ev.User) {
		return
	}

	text := strings.TrimSpace(c.stripSlackBotMention(ev.Text))
	if text == "" && len(ev.Files) == 0 {
		return
	}

	threadTS := strings.TrimSpace(ev.ThreadTimeStamp)
	if threadTS != "" && threadTS != strings.TrimSpace(ev.TimeStamp) {
		return
	}

	if threadTS == "" {
		threadTS = ev.TimeStamp
	}

	if len(c.config.SocialMode.ChannelAgents) == 0 {
		return
	}

	channel, err := c.api.GetConversationInfoContext(ctx, &slack.GetConversationInfoInput{ChannelID: ev.Channel})
	if err != nil {
		return
	}

	agent, ok := c.config.SocialMode.ChannelAgents["#"+channel.Name]
	if !ok {
		return
	}

	replyTarget := &events.SlackReplyTarget{ChannelID: ev.Channel, MessageTS: ev.TimeStamp, ThreadTS: threadTS}
	key := slackThreadStackKey(replyTarget)
	c.beginSlackStack(key)

	for i := range c.threadAgents {
		if c.threadAgents[i].agent == agent {
			prefix := c.threadAgents[i].prefix
			if len(prefix) > 2 && strings.HasPrefix(prefix, ":") && strings.HasSuffix(prefix, ":") {
				c.addReaction(ctx, replyTarget, strings.Trim(prefix, ":"), "add Slack social agent reaction")
			}

			break
		}
	}

	c.createReplyPlaceholdersOrWarn(ctx, replyTarget, "channel", ev.Channel, "message_ts", ev.TimeStamp, "agent", agent)

	content := slackInboundContent{Text: ev.Text}
	if len(ev.Files) > 0 {
		content.Attachments, content.TextAttachments, content.HadAttachments, content.HadNonImageAttachments, content.AttachmentWarnings = c.downloadSlackAttachments(ctx, ev.Files)
	}

	promptText := c.socialPromptWithContext(ctx, ev.Channel, ev.TimeStamp, text)
	content.Text = promptText

	c.log.Info("handing Slack social thread to router", "channel", ev.Channel, "message_ts", ev.TimeStamp, "thread_ts", threadTS, "agent", agent, "pending_placeholder", c.hasPendingState(replyTarget))

	if err := c.threadRouter.StartThread(ctx, agent, false, newSlackInboundMessage(promptText, &content, replyTarget)); err != nil {
		c.log.Error("start Slack social thread", "error", err, "channel", ev.Channel, "message_ts", ev.TimeStamp, "agent", agent, "pending_placeholder", c.hasPendingState(replyTarget))
		c.finishSlackStack(key)

		return
	}

	c.addRobotReaction(ctx, replyTarget)
	c.log.Info("accepted Slack social mention", "user", ev.User, "channel", ev.Channel, "message_ts", ev.TimeStamp, "thread_ts", threadTS, "agent", agent, "text_len", len(text), "attachment_count", len(content.Attachments))
}

func (c *Connector) socialModeAllowsUser(userID string) bool {
	return slices.Contains(c.config.SocialMode.AllowedUserIDs, strings.TrimSpace(userID))
}

func (c *Connector) stripSlackBotMention(text string) string {
	text = strings.TrimSpace(text)

	botUserID := strings.TrimSpace(c.botUserID)
	if botUserID == "" || text == "" {
		return text
	}

	for _, mention := range []string{"<@" + botUserID + ">", "<@" + botUserID + "|"} {
		if !strings.HasPrefix(text, mention) {
			continue
		}

		if mention[len(mention)-1] == '|' {
			if _, after, ok := strings.Cut(text, ">"); ok {
				return strings.TrimSpace(after)
			}
		}

		return strings.TrimSpace(strings.TrimPrefix(text, mention))
	}

	return text
}

func (c *Connector) socialPromptWithContext(ctx context.Context, channelID, mentionTS, text string) string {
	limit := c.config.SocialMode.ContextMessages
	if limit <= 0 {
		return text
	}

	history, err := c.api.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{ChannelID: channelID, Latest: mentionTS, Inclusive: false, Limit: limit})
	if err != nil {
		c.log.Warn("load Slack social context", "error", err, "channel", channelID, "message_ts", mentionTS)
		return text
	}

	if history == nil || len(history.Messages) == 0 {
		return text
	}

	lines := make([]string, 0, len(history.Messages))
	for i := range slices.Backward(history.Messages) {
		message := history.Messages[i]

		messageText := strings.TrimSpace(message.Text)
		if messageText == "" {
			continue
		}

		user := strings.TrimSpace(message.User)
		if user == "" {
			lines = append(lines, "- "+messageText)
		} else {
			lines = append(lines, "- <@"+user+">: "+messageText)
		}
	}

	if len(lines) == 0 {
		return text
	}

	return "Recent Slack channel context before the mention:\n" + strings.Join(lines, "\n") + "\n\nMention:\n" + text
}

func (c *Connector) removeReaction(ctx context.Context, replyTarget *events.SlackReplyTarget, reaction, logMessage string) {
	if replyTarget == nil || strings.TrimSpace(replyTarget.ChannelID) == "" || strings.TrimSpace(replyTarget.MessageTS) == "" {
		return
	}

	if err := c.api.RemoveReactionContext(ctx, reaction, slack.NewRefToMessage(replyTarget.ChannelID, replyTarget.MessageTS)); err != nil && err.Error() != "no_reaction" {
		c.log.Warn(logMessage, "channel", replyTarget.ChannelID, "message_ts", replyTarget.MessageTS, "error", err)
	}
}

func (c *Connector) replyState(turnID string) (slackReplySlots, bool) {
	if strings.TrimSpace(turnID) == "" {
		return slackReplySlots{}, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	state, ok := c.replies[turnID]

	return state, ok
}

func (c *Connector) setReplyState(turnID string, state slackReplySlots) {
	if strings.TrimSpace(turnID) == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.replies == nil {
		c.replies = map[string]slackReplySlots{}
	}

	c.replies[turnID] = state
	if state.Key != "" {
		delete(c.pending, state.Key)
	}
}

func (c *Connector) claimPendingState(replyTarget *events.SlackReplyTarget) (slackReplySlots, bool) {
	key := slackPendingKey(replyTarget)
	if key == "" {
		return slackReplySlots{}, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	state, ok := c.pending[key]
	if !ok {
		return slackReplySlots{}, false
	}

	delete(c.pending, key)

	return state, true
}

func (c *Connector) hasPendingState(replyTarget *events.SlackReplyTarget) bool {
	key := slackPendingKey(replyTarget)
	if key == "" {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	_, ok := c.pending[key]

	return ok
}

func (c *Connector) clearReplyState(turnID string) {
	if strings.TrimSpace(turnID) == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.replies, turnID)
}

func newSlackInboundMessage(text string, content *slackInboundContent, replyTarget *events.SlackReplyTarget) *events.InboundMessage {
	if len(content.TextAttachments) > 0 {
		attachmentText := strings.Join(content.TextAttachments, "\n\n")
		if strings.TrimSpace(text) == "" {
			text = attachmentText
		} else {
			text += "\n\n" + attachmentText
		}
	}

	inbound := events.NewMainInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", text, true)
	if replyTarget != nil && strings.TrimSpace(replyTarget.ThreadTS) != "" {
		inbound.ConversationID = ""
	}

	if len(content.Attachments) > 0 {
		inbound.Attachments = make([]events.InboundAttachment, 0, len(content.Attachments))
		for i := range content.Attachments {
			inbound.Attachments = append(inbound.Attachments, events.InboundAttachment{
				Name:     content.Attachments[i].Name,
				MIMEType: content.Attachments[i].MIMEType,
				Data:     append([]byte(nil), content.Attachments[i].Data...),
			})
		}
	}

	if replyTarget != nil {
		inbound.SlackReply = &events.SlackReplyTarget{
			ChannelID: replyTarget.ChannelID,
			MessageTS: replyTarget.MessageTS,
			ThreadTS:  replyTarget.ThreadTS,
		}
	}

	inbound.HadAttachments = content.HadAttachments
	inbound.HadNonImageAttachments = content.HadNonImageAttachments && len(content.TextAttachments) == 0
	inbound.AttachmentWarnings = append([]string(nil), content.AttachmentWarnings...)

	return inbound
}

func slackPendingKey(replyTarget *events.SlackReplyTarget) string {
	if replyTarget == nil {
		return ""
	}

	channelID := strings.TrimSpace(replyTarget.ChannelID)
	messageTS := strings.TrimSpace(replyTarget.MessageTS)
	threadTS := strings.TrimSpace(replyTarget.ThreadTS)

	if channelID == "" || messageTS == "" {
		return ""
	}

	return channelID + "\x00" + messageTS + "\x00" + threadTS
}

func normalizeThreadAgents(threadAgents config.ThreadAgents) []threadAgent {
	if len(threadAgents) == 0 {
		return nil
	}

	normalized := make([]threadAgent, 0, len(threadAgents))
	for prefix, entry := range threadAgents {
		prefix = strings.TrimSpace(prefix)

		agent := strings.TrimSpace(entry.Agent)
		if prefix == "" || agent == "" {
			continue
		}

		normalized = append(normalized, threadAgent{prefix: prefix, agent: agent, preSeed: entry.PreSeed})
	}

	slices.SortFunc(normalized, func(a, b threadAgent) int {
		if len(a.prefix) != len(b.prefix) {
			return len(b.prefix) - len(a.prefix)
		}

		return strings.Compare(a.prefix, b.prefix)
	})

	if len(normalized) == 0 {
		return nil
	}

	return normalized
}

func (c *Connector) threadAgentForText(text string) (agent string, preSeed bool, promptText string, ok bool) {
	text = strings.TrimSpace(text)

	for i := range c.threadAgents {
		candidate := c.threadAgents[i]
		if !strings.HasPrefix(text, candidate.prefix) {
			continue
		}

		return candidate.agent, candidate.preSeed, strings.TrimSpace(strings.TrimPrefix(text, candidate.prefix)), true
	}

	return "", false, "", false
}

func (c *Connector) handleOnDemandCronRequest(ctx context.Context, ev *slackevents.MessageEvent, target string, replyTarget *events.SlackReplyTarget) {
	if ev == nil || replyTarget == nil {
		return
	}

	loaded, err := c.oneOffCronjobs.LoadOneOffCronjob(target)
	if err != nil {
		if errPost := c.publishOnDemandCronReply(ctx, replyTarget, "I couldn't find that cronjob. Use a top-level cron filename like `daily` or `daily.md`.", true); errPost != nil {
			c.log.Warn("publish Slack on-demand cron rejection", "error", errPost, "channel", ev.Channel, "message_ts", ev.TimeStamp, "thread_ts", replyTarget.ThreadTS)
		}

		c.log.Info("rejected Slack on-demand cron request", "user", ev.User, "channel", ev.Channel, "message_ts", ev.TimeStamp, "requested_cron", strings.TrimSpace(target), "error", err)

		return
	}

	preview := "One-off cronjob starting.\n\nFile: `" + loaded.RelativePath + "`\nAgent: `" + strings.TrimSpace(loaded.Agent) + "`"
	if err := c.publishOnDemandCronReply(ctx, replyTarget, preview, false); err != nil {
		c.log.Warn("publish Slack on-demand cron preview", "error", err, "channel", ev.Channel, "message_ts", ev.TimeStamp, "thread_ts", replyTarget.ThreadTS, "cron", loaded.RelativePath)
	}

	startedAt := time.Now()
	requestLog := c.log.With(
		"requester", ev.User,
		"target_cron", loaded.RelativePath,
		"start_time", startedAt.Format(time.RFC3339),
		"channel", ev.Channel,
		"message_ts", ev.TimeStamp,
		"thread_ts", replyTarget.ThreadTS,
	)
	requestLog.Info("starting Slack one-off cronjob thread")

	c.addRobotReaction(ctx, replyTarget)
	c.log.Info("accepted Slack on-demand cron request", "user", ev.User, "channel", ev.Channel, "message_ts", ev.TimeStamp, "thread_ts", replyTarget.ThreadTS, "cron", loaded.RelativePath, "agent", loaded.Agent)

	turnID := fmt.Sprintf("one-off-cron-%d", time.Now().UnixNano())

	if slots, err := c.createReplyPlaceholders(ctx, replyTarget); err != nil {
		c.log.Warn("create Slack on-demand cron reply placeholders", "error", err)
	} else if slots.Key != "" {
		c.setReplyState(turnID, slots)
	}

	go c.runOnDemandCron(ctx, requestLog, loaded, replyTarget, turnID)
}

func (c *Connector) runOnDemandCron(ctx context.Context, log *slog.Logger, loaded cronjob.OneOffCronjob, replyTarget *events.SlackReplyTarget, turnID string) {
	thinking := ""
	publish := func(ctx context.Context, text, thinkingText string, complete, postText bool, attachments []events.OutboundAttachment) error {
		outbound := events.NewMainOutboundMessage(events.SourceSystem, text, events.OutputTargetSlackMain)
		outbound.SlackThinking = thinkingText
		outbound.SlackPostText = postText
		outbound.TurnID = turnID
		outbound.Complete = complete
		outbound.SlackReply = cloneSlackReplyTarget(replyTarget)
		outbound.Attachments = events.CloneOutboundAttachments(attachments)

		if err := c.bus.PublishOutbound(ctx, outbound); err != nil {
			return fmt.Errorf("publish Slack on-demand cron output: %w", err)
		}

		return nil
	}
	progress := &harnessbridge.RawRunProgress{
		Thinking: func(ctx context.Context, text string) error {
			text = strings.TrimSpace(text)
			if text == "" {
				return nil
			}

			if thinking == "" {
				thinking = text
			} else {
				thinking += "\n" + text
			}

			return publish(ctx, "", thinking, false, false, nil)
		},
		Message: func(ctx context.Context, text string) error {
			text = strings.TrimSpace(text)
			if text == "" {
				return nil
			}

			return publish(ctx, text, "", false, true, nil)
		},
	}

	c.oneOffCronjobs.RunOneOffCronjob(ctx, loaded, progress, func(ctx context.Context, result cronjob.RunResult, err error) {
		if err != nil {
			log.Error("completed Slack one-off cronjob thread", "finish_time", time.Now().Format(time.RFC3339), "outcome", "failed", "error", err)

			if errPublish := publish(ctx, "I couldn't run that on-demand cron right now.", "", true, false, nil); errPublish != nil {
				c.log.Warn("publish Slack on-demand cron failure", "error", errPublish)
			}

			return
		}

		payload := strings.TrimSpace(result.VerbatimMessage)
		if payload == "" && len(result.Attachments) == 0 {
			payload = "Cronjob completed and decided to emit no human-visible output."
		}

		if err := publish(ctx, payload, "", true, false, result.Attachments); err != nil {
			log.Error("completed Slack one-off cronjob thread", "finish_time", time.Now().Format(time.RFC3339), "outcome", "failed", "error", err)
			return
		}

		log.Info("completed Slack one-off cronjob thread", "finish_time", time.Now().Format(time.RFC3339), "outcome", "succeeded")
	})
}

func (c *Connector) publishOnDemandCronReply(ctx context.Context, replyTarget *events.SlackReplyTarget, text string, complete bool) error {
	text = strings.TrimSpace(text)
	if text == "" || replyTarget == nil {
		return nil
	}

	outbound := events.NewMainOutboundMessage(events.SourceSystem, text, events.OutputTargetSlackMain)
	outbound.Complete = complete
	outbound.SlackPostText = !complete
	outbound.SlackReply = cloneSlackReplyTarget(replyTarget)

	if err := c.bus.PublishOutbound(ctx, outbound); err != nil {
		return fmt.Errorf("publish Slack on-demand cron reply: %w", err)
	}

	return nil
}

func cloneSlackReplyTarget(replyTarget *events.SlackReplyTarget) *events.SlackReplyTarget {
	if replyTarget == nil {
		return nil
	}

	return &events.SlackReplyTarget{ChannelID: replyTarget.ChannelID, MessageTS: replyTarget.MessageTS, ThreadTS: replyTarget.ThreadTS}
}

func (c *Connector) createReplyPlaceholders(ctx context.Context, replyTarget *events.SlackReplyTarget) (slackReplySlots, error) {
	if replyTarget == nil {
		return slackReplySlots{}, nil
	}

	channelID := strings.TrimSpace(replyTarget.ChannelID)
	if channelID == "" {
		return slackReplySlots{}, nil
	}

	placeholderChannelID, thinkingTS, answerTS, err := c.postReplyPlaceholderPair(ctx, channelID, replyTarget.ThreadTS)
	if err != nil {
		return slackReplySlots{}, err
	}

	c.mu.Lock()
	slots := c.createReplyPlaceholderStateLocked(replyTarget, placeholderChannelID, thinkingTS, answerTS)
	c.mu.Unlock()
	c.log.Info("created Slack reply placeholders", "channel", replyTarget.ChannelID, "message_ts", replyTarget.MessageTS, "thread_ts", replyTarget.ThreadTS, "placeholder_channel", placeholderChannelID, "thinking_ts", thinkingTS, "answer_ts", answerTS)

	return slots, nil
}

func (c *Connector) createReplyPlaceholderStateLocked(replyTarget *events.SlackReplyTarget, placeholderChannelID, thinkingTS, answerTS string) slackReplySlots {
	key := slackPendingKey(replyTarget)
	if key == "" {
		return slackReplySlots{}
	}

	slots := slackReplySlots{ChannelID: placeholderChannelID, ThinkingTS: thinkingTS, AnswerTS: answerTS, Key: key}
	c.pending[key] = slots

	return slots
}

func (c *Connector) postReplyPlaceholderPair(ctx context.Context, channelID, threadTS string) (placeholderChannelID, thinkingTS, answerTS string, err error) {
	options := []slack.MsgOption{slack.MsgOptionText(slackImmediatePlaceholder, false)}
	if threadTS = strings.TrimSpace(threadTS); threadTS != "" {
		options = append(options, slack.MsgOptionTS(threadTS))
	}

	placeholderChannelID, thinkingTS, err = c.api.PostMessageContext(ctx, channelID, options...)
	if err != nil {
		return "", "", "", fmt.Errorf("post Slack thinking placeholder: %w", err)
	}

	options = []slack.MsgOption{slack.MsgOptionText(slackAnswerPlaceholder, false)}
	if threadTS != "" {
		options = append(options, slack.MsgOptionTS(threadTS))
	}

	_, answerTS, err = c.api.PostMessageContext(ctx, placeholderChannelID, options...)
	if err != nil {
		c.deleteSlackMessage(ctx, slackReplyState{ChannelID: placeholderChannelID, MessageTS: thinkingTS}, "delete Slack thinking placeholder after answer placeholder failure")
		return "", "", "", fmt.Errorf("post Slack answer placeholder: %w", err)
	}

	return placeholderChannelID, thinkingTS, answerTS, nil
}

func (c *Connector) createReplyPlaceholdersOrWarn(ctx context.Context, replyTarget *events.SlackReplyTarget, attrs ...any) {
	if _, err := c.createReplyPlaceholders(ctx, replyTarget); err != nil {
		c.log.Warn("create Slack reply placeholder", append([]any{"error", err}, attrs...)...)
	}
}

func (c *Connector) ensureSlackStack(key string) {
	if strings.TrimSpace(key) == "" {
		return
	}

	c.mu.Lock()
	c.ensureSlackStackLocked(key)
	c.mu.Unlock()
}

func (c *Connector) ensureSlackStackLocked(key string) {
	if strings.TrimSpace(key) == "" {
		return
	}

	if _, ok := c.stacks[key]; !ok {
		c.stacks[key] = nil
	}
}

func (c *Connector) resolveManagedThreadTS(ctx context.Context, channelID, messageTS string) (threadTS string, handled bool, err error) {
	handled, err = c.threadRouter.PrepareThreadReply(ctx, channelID, messageTS)
	if err != nil {
		return "", false, fmt.Errorf("prepare Slack thread reply: %w", err)
	}

	if handled {
		return messageTS, true, nil
	}

	history, err := c.api.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{
		ChannelID:          channelID,
		Cursor:             "",
		Latest:             messageTS,
		Oldest:             messageTS,
		Inclusive:          true,
		Limit:              1,
		IncludeAllMetadata: false,
	})
	if err != nil {
		return "", false, fmt.Errorf("load Slack message history: %w", err)
	}

	if history == nil || len(history.Messages) == 0 {
		return "", false, nil
	}

	threadTS = strings.TrimSpace(history.Messages[0].ThreadTimestamp)
	if threadTS == "" {
		return "", false, nil
	}

	handled, err = c.threadRouter.PrepareThreadReply(ctx, channelID, threadTS)
	if err != nil {
		return "", false, fmt.Errorf("prepare Slack thread reply: %w", err)
	}

	return threadTS, handled, nil
}

func (c *Connector) postSlackThreadReply(ctx context.Context, channelID, threadTS, text string) error {
	channelID = strings.TrimSpace(channelID)
	threadTS = strings.TrimSpace(threadTS)

	text = strings.TrimSpace(text)
	if channelID == "" || threadTS == "" || text == "" {
		return nil
	}

	if _, _, err := c.api.PostMessageContext(ctx, channelID, slack.MsgOptionText(text, false), slack.MsgOptionTS(threadTS)); err != nil {
		return fmt.Errorf("send Slack thread reply: %w", err)
	}

	return nil
}

func (c *Connector) inboundContentForMessageEvent(ctx context.Context, ev *slackevents.MessageEvent) slackInboundContent {
	var content slackInboundContent

	content.Text = slackMessageEventText(ev)

	files := slackMessageEventFiles(ev)
	if len(files) == 0 {
		return content
	}

	content.Attachments, content.TextAttachments, content.HadAttachments, content.HadNonImageAttachments, content.AttachmentWarnings = c.downloadSlackAttachments(ctx, files)

	return content
}

func (c *Connector) downloadSlackAttachments(ctx context.Context, files []slack.File) (attachments []events.InboundAttachment, textAttachments []string, hadAttachments, hadNonImageAttachments bool, warnings []string) {
	for i := range files {
		file := &files[i]
		warnSkip := func(reason string) {
			warnings = append(warnings, "Skipped Slack attachment "+slackFileDescriptor(file)+" because "+reason+".")
		}

		if !isSlackImageFile(file) {
			if isSlackTextFile(file) {
				if file.Size > maxSlackTextBytesPerFile {
					warnings = append(warnings, "Skipped Slack text attachment "+slackFileDescriptor(file)+" because it exceeded the text file size limit.")

					continue
				}

				downloadURL := slackFileDownloadURL(file)
				if downloadURL == "" {
					warnings = append(warnings, "Skipped Slack text attachment "+slackFileDescriptor(file)+" because Slack did not provide a download URL.")

					continue
				}

				var buffer limitedBuffer

				buffer.limit = maxSlackTextBytesPerFile

				downloadCtx, cancel := context.WithTimeout(ctx, slackFileDownloadTimeout)
				err := c.api.GetFileContext(downloadCtx, downloadURL, &buffer)

				cancel()

				if err != nil {
					if errors.Is(err, errSlackDownloadLimitExceeded) {
						warnings = append(warnings, "Skipped Slack text attachment "+slackFileDescriptor(file)+" because it exceeded the text file size limit.")
					} else {
						c.log.Warn("download Slack text attachment", "file", slackFileDisplayName(file), "mime_type", normalizedSlackMIMEType(file.Mimetype), "error", err)
						warnings = append(warnings, "Skipped Slack text attachment "+slackFileDescriptor(file)+" because downloading it from Slack failed.")
					}

					continue
				}

				if !utf8.Valid(buffer.Bytes()) || bytes.Contains(buffer.Bytes(), []byte{0}) {
					warnings = append(warnings, "Skipped Slack text attachment "+slackFileDescriptor(file)+" because Slack returned non-UTF-8 text data.")

					continue
				}

				data := string(buffer.Bytes())
				if strings.TrimSpace(data) == "" {
					warnings = append(warnings, "Skipped Slack text attachment "+slackFileDescriptor(file)+" because Slack returned empty text data.")

					continue
				}

				textAttachments = append(textAttachments, "Slack text file attachment "+slackFileDescriptor(file)+":\n"+data)

				continue
			}

			hadNonImageAttachments = true

			warnings = append(warnings, "Skipped Slack attachment "+slackFileDescriptor(file)+" because it is not an image.")

			continue
		}

		hadAttachments = true

		mimeType := normalizedSlackMIMEType(file.Mimetype)
		if file.Size > maxSlackImageDownloadBytes {
			warnSkip("it exceeded the Slack attachment download limit")
			continue
		}

		downloadURL := slackFileDownloadURL(file)
		if downloadURL == "" {
			warnSkip("Slack did not provide a download URL")
			continue
		}

		var buffer limitedBuffer

		buffer.limit = maxSlackImageDownloadBytes
		downloadCtx, cancel := context.WithTimeout(ctx, slackFileDownloadTimeout)
		err := c.api.GetFileContext(downloadCtx, downloadURL, &buffer)

		cancel()

		if err != nil {
			if errors.Is(err, errSlackDownloadLimitExceeded) {
				warnSkip("it exceeded the Slack attachment download limit")
			} else {
				c.log.Warn("download Slack attachment", "file", slackFileDisplayName(file), "mime_type", mimeType, "error", err)
				warnSkip("downloading it from Slack failed")
			}

			continue
		}

		data := append([]byte(nil), buffer.Bytes()...)
		if len(data) == 0 {
			warnSkip("Slack returned empty attachment data")
			continue
		}

		attachments = append(attachments, events.InboundAttachment{
			Name:     slackFileDisplayName(file),
			MIMEType: mimeType,
			Data:     data,
		})
	}

	return attachments, textAttachments, hadAttachments, hadNonImageAttachments, warnings
}

func isSlackTextFile(file *slack.File) bool {
	if file == nil {
		return false
	}

	mimeType := normalizedSlackMIMEType(file.Mimetype)
	if strings.HasPrefix(mimeType, "text/") {
		return true
	}

	switch mimeType {
	case "application/json", "application/jsonl", "application/ld+json", "application/xml", "application/yaml", "application/x-yaml", "application/toml", "application/x-toml", "application/csv", "application/x-ndjson":
		return true
	}

	switch strings.ToLower(filepath.Ext(slackFileDisplayName(file))) {
	case ".txt", ".md", ".markdown", ".csv", ".tsv", ".json", ".jsonl", ".ndjson", ".yaml", ".yml", ".toml", ".xml", ".ini", ".log":
		return true
	}

	return false
}

func slackMessageEventText(ev *slackevents.MessageEvent) string {
	if ev == nil {
		return ""
	}

	if ev.Message != nil {
		if text := strings.TrimSpace(ev.Message.Text); text != "" {
			return text
		}
	}

	return strings.TrimSpace(ev.Text)
}

func slackMessageEventFiles(ev *slackevents.MessageEvent) []slack.File {
	if ev == nil || ev.Message == nil || len(ev.Message.Files) == 0 {
		return nil
	}

	return append([]slack.File(nil), ev.Message.Files...)
}

func isSlackImageFile(file *slack.File) bool {
	if file == nil {
		return false
	}

	return strings.HasPrefix(normalizedSlackMIMEType(file.Mimetype), "image/")
}

func normalizedSlackMIMEType(mimeType string) string {
	if mediaType, _, err := mime.ParseMediaType(mimeType); err == nil {
		mimeType = mediaType
	}

	return strings.ToLower(strings.TrimSpace(mimeType))
}

func slackMIMETypeLabel(mimeType string) string {
	mimeType = normalizedSlackMIMEType(mimeType)
	if mimeType == "" {
		return "an unknown MIME type"
	}

	return mimeType
}

func slackFileDownloadURL(file *slack.File) string {
	if file == nil {
		return ""
	}

	if downloadURL := strings.TrimSpace(file.URLPrivateDownload); downloadURL != "" {
		return downloadURL
	}

	return strings.TrimSpace(file.URLPrivate)
}

func slackFileDisplayName(file *slack.File) string {
	if file == nil {
		return "unnamed file"
	}

	for _, candidate := range []string{file.Name, file.Title, file.ID} {
		if name := strings.TrimSpace(candidate); name != "" {
			return name
		}
	}

	return "unnamed file"
}

func slackFileDescriptor(file *slack.File) string {
	name := slackFileDisplayName(file)

	mimeType := ""
	if file != nil {
		mimeType = normalizedSlackMIMEType(file.Mimetype)
	}

	if mimeType == "" {
		return name
	}

	return name + " (" + mimeType + ")"
}

func (c *Connector) fetchHumanProfile(ctx context.Context) (*humanProfileSnapshot, error) {
	profile, err := c.api.GetUserProfileContext(ctx, &slack.GetUserProfileParameters{UserID: c.config.HumanUserID, IncludeLabels: false})
	if err != nil {
		return nil, fmt.Errorf("fetch Slack human profile: %w", err)
	}

	displayName := strings.TrimSpace(profile.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(profile.RealName)
	}

	if displayName == "" {
		displayName = c.config.HumanUserID
	}

	iconURL := ""

	for _, candidate := range []string{profile.ImageOriginal, profile.Image1024, profile.Image512, profile.Image192, profile.Image72, profile.Image48, profile.Image32, profile.Image24} {
		if imageURL := strings.TrimSpace(candidate); imageURL != "" {
			iconURL = imageURL
			break
		}
	}

	return &humanProfileSnapshot{
		DisplayName: displayName,
		IconURL:     iconURL,
	}, nil
}

func quoteDiscordRelay(text string) string {
	quotedLines := make([]string, 0, strings.Count(text, "\n")+1)
	for line := range strings.SplitSeq(text, "\n") {
		quotedLines = append(quotedLines, "> "+line)
	}

	return "Discord utterance:\n" + strings.Join(quotedLines, "\n")
}
