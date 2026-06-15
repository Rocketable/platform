// Package slackconnector bridges Slack events into rocketclaw.
package slackconnector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"os"
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
	slackTextLimit                = 3800
	slackBlockTextLimit           = 3000
	slackPreferredChunkSize       = 3200
	slackOnDemandCronPrefix       = ":repeat_one:"
	slackOnDemandCronReaction     = "repeat_one"
	slackRobotReaction            = "robot_face"
	slackDiscordRelayReaction     = "calling"
	slackWebVoiceRelayReaction    = "studio_microphone"
	slackExternalMCPRelayReaction = "satellite_antenna"
	slackBufferedReaction         = "hourglass_flowing_sand"
	slackSummaryReaction          = "floppy_disk"
	slackGoalStopSignReaction     = "octagonal_sign"
	slackGoalStopButtonReaction   = "stop_button"
	slackGoalCompleteReaction     = "white_check_mark"
	slackInterruptionReaction     = "exclamation"
	slackMainStackKey             = "main"
	slackImmediatePlaceholder     = "_Thinking..._"
	slackGoalPlaceholder          = "_Pursuing goal..._"
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
	threadRouter       harnessbridge.PrimaryTextRouter
	oneOffCronjobs     oneOffCronjobRunner
	interruptMainTurn  func() *events.InboundMessage

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
	Text        string
	Placeholder string
	State       slackReplyState
	Timer       *time.Timer
}

type slackBufferedMessage struct {
	Text    string
	Content events.InboundContent
	Reply   *events.SlackReplyTarget
}

type threadAgent struct {
	prefix, agent string
	preSeed       bool
}

type oneOffCronjobRunner interface {
	LoadOneOffCronjob(string) (cronjob.OneOffCronjob, error)
	RunOneOffCronjob(context.Context, cronjob.OneOffCronjob, *harnessbridge.RawRunProgress, func(context.Context, cronjob.RunResult, error))
}

// New constructs a Slack connector.
func New(cfg *config.SlackConfig, bus *events.Bus, emergencySafeWords []string, threadAgents config.ThreadAgents, threadRouter harnessbridge.PrimaryTextRouter, oneOffCronjobs oneOffCronjobRunner, interruptMainTurn func() *events.InboundMessage, logger *slog.Logger) *Connector {
	api := slack.New(cfg.BotToken, slack.OptionAppLevelToken(cfg.AppToken), slack.OptionRetry(3))

	return &Connector{
		log: logger.With("component", "slack"), config: *cfg, bus: bus,
		emergencySafeWords: slices.Clone(emergencySafeWords), threadAgents: normalizeThreadAgents(threadAgents), threadRouter: threadRouter, oneOffCronjobs: oneOffCronjobs, interruptMainTurn: interruptMainTurn,
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

	thinkingText := strings.TrimSpace(msg.ProgressText)

	placeholder := slackImmediatePlaceholder
	if msg.GoalTurn {
		placeholder = slackGoalPlaceholder
	}

	switch {
	case msg.Text != "" && (msg.Complete || msg.PostProgressText):
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

		channelID, threadTS := slackReplyDestination(c.config.Room, msg.SlackReply)
		if msg.Complete && msg.Checkpoint != nil && threadTS == "" {
			for i := range posted {
				if err := c.threadRouter.RecordResponseCheckpoint(events.TextConversationTarget{ChannelID: posted[i].ChannelID, MessageID: posted[i].MessageTS}, *msg.Checkpoint); err != nil {
					return fmt.Errorf("record Slack response checkpoint: %w", err)
				}
			}
		}

		if msg.Complete && msg.GoalComplete {
			c.addGoalCompleteReactions(ctx, channelID, threadTS, posted)
		}

	case thinkingText != "":
		if ok {
			c.bufferProgressText(msg.TurnID, slackReplyState{ChannelID: slots.ChannelID, MessageTS: slots.ThinkingTS}, placeholder, thinkingText)
		} else {
			channelID, threadTS := slackReplyDestination(c.config.Room, msg.SlackReply)

			postedChannelID, postedThinkingTS, postedAnswerTS, err := c.postReplyPlaceholderPair(ctx, channelID, threadTS, placeholder)
			if err != nil {
				return fmt.Errorf("send Slack reply placeholders len=%d: %w", len([]rune(thinkingText)), err)
			}

			slots = slackReplySlots{ChannelID: postedChannelID, ThinkingTS: postedThinkingTS, AnswerTS: postedAnswerTS}
			c.setReplyState(msg.TurnID, slots)
			c.bufferProgressText(msg.TurnID, slackReplyState{ChannelID: slots.ChannelID, MessageTS: slots.ThinkingTS}, placeholder, thinkingText)
		}
	}

	if msg.Complete {
		if err := c.finishCompleteResponse(ctx, msg, slots, ok); err != nil {
			return err
		}
	}

	return nil
}

func slackThinkingMessage(placeholder, thinking string) string {
	thinking = strings.TrimSpace(thinking)
	if thinking == "" {
		return ""
	}

	prefix := placeholder + "\n\n"
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
		return placeholder
	}

	var quoted strings.Builder

	lines := strings.Split(body, "\n")

	quoted.WriteString(prefix)

	for i := len(lines) - 1; i >= 0; i-- {
		quoted.WriteString("> ")
		quoted.WriteString(lines[i])
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
	return c.sendVoiceRelay(ctx, text, slackDiscordRelayReaction, "send Slack Discord relay")
}

// SendWebVoiceRelay mirrors a browser web voice utterance into Slack before the main session handles it.
func (c *Connector) SendWebVoiceRelay(ctx context.Context, text string) (*events.SlackReplyTarget, error) {
	return c.sendVoiceRelay(ctx, text, slackWebVoiceRelayReaction, "send Slack web voice relay")
}

//nolint:funcorder // Shared helper is kept next to the voice relay entrypoints.
func (c *Connector) sendVoiceRelay(ctx context.Context, text, reaction, errLabel string) (*events.SlackReplyTarget, error) {
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
		options = append(options, slack.MsgOptionUsername(profile.DisplayName))
		if profile.IconURL != "" {
			options = append(options, slack.MsgOptionIconURL(profile.IconURL))
		}
	} else {
		options = []slack.MsgOption{slack.MsgOptionText(quoteDiscordRelay(text), false)}
	}

	channelID, messageTS, err := c.api.PostMessageContext(ctx, c.config.Room, options...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errLabel, err)
	}

	replyTarget := &events.SlackReplyTarget{ChannelID: channelID, MessageTS: messageTS, ThreadTS: ""}
	if _, err := c.createReplyPlaceholders(ctx, replyTarget, slackImmediatePlaceholder); err != nil {
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

	seedText := "Cronjob " + relativePath + " ran at " + ranAt + " with agent " + strings.TrimSpace(agent) + "."
	if strings.TrimSpace(text) != "" {
		seedText += "\n\nHuman-visible cron output:\n" + strings.TrimSpace(text)
	}

	if names := events.AttachmentNamesSpeech(attachments); names != "" {
		seedText += "\n\n" + names
	}

	if err := c.threadRouter.RegisterCronThread(ctx, events.TextConversationTarget{ChannelID: postedChannelID, ThreadID: threadTS}, agent, seedText); err != nil {
		return fmt.Errorf("register Slack cronjob thread: %w", err)
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

	c.clearProgressText(msg.TurnID)
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
				_, err := c.threadRouter.SubmitThreadReply(submitCtx, events.TextConversationTarget{ChannelID: channelID, ThreadID: threadTS}, inbound)
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

		placeholderChannelID, thinkingTS, answerTS, err := c.postReplyPlaceholderPair(ctx, postedChannelID, replyTarget.ThreadTS, slackImmediatePlaceholder)
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

func (c *Connector) bufferProgressText(turnID string, state slackReplyState, placeholder, text string) {
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
	pending.Placeholder = placeholder
	pending.State = state

	if pending.Timer != nil {
		pending.Timer.Reset(slackThinkingFlushInterval)
	} else {
		pending.Timer = time.AfterFunc(slackThinkingFlushInterval, func() {
			if err := c.flushProgressText(context.Background(), turnID); err != nil && c.log != nil {
				c.log.Warn("flush Slack thinking update", "turn_id", turnID, "error", err)
			}
		})
	}

	c.thinking[turnID] = pending
}

func (c *Connector) addGoalCompleteReactions(ctx context.Context, channelID, threadTS string, posted []slackReplyState) {
	if threadTS != "" {
		c.addReaction(ctx, &events.SlackReplyTarget{ChannelID: channelID, MessageTS: threadTS, ThreadTS: threadTS}, slackGoalCompleteReaction, "add Slack goal complete root reaction")
	}

	if len(posted) > 0 {
		last := posted[len(posted)-1]
		c.addReaction(ctx, &events.SlackReplyTarget{ChannelID: last.ChannelID, MessageTS: last.MessageTS, ThreadTS: threadTS}, slackGoalCompleteReaction, "add Slack goal complete last reaction")
	}
}

func (c *Connector) flushProgressText(ctx context.Context, turnID string) error {
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

	thinkingText := slackThinkingMessage(pending.Placeholder, pending.Text)

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

func (c *Connector) clearProgressText(turnID string) {
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

func (c *Connector) acceptMainSlackMessage(ctx context.Context, text string, content *events.InboundContent, replyTarget *events.SlackReplyTarget) bool {
	key := slackMainStackKey
	if c.bufferSlackStack(ctx, key, text, content, replyTarget) {
		return false
	}

	c.beginSlackStack(key)

	c.createReplyPlaceholdersOrWarn(ctx, replyTarget, slackImmediatePlaceholder, "channel", replyTarget.ChannelID, "message_ts", replyTarget.MessageTS)

	if err := c.bus.PublishInbound(ctx, newSlackInboundMessage(text, content, replyTarget)); err != nil {
		c.log.Error("publish Slack inbound message", "error", err)
		c.finishSlackStack(key)

		c.warnConsumeReservedPlaceholder(ctx, replyTarget, "I couldn't start processing that Slack message: "+err.Error(), "consume Slack inbound publish failure placeholder")

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

func (c *Connector) beginSlackStack(key string) { c.mu.Lock(); c.stacks[key] = nil; c.mu.Unlock() }

func (c *Connector) bufferSlackStack(ctx context.Context, key, text string, content *events.InboundContent, replyTarget *events.SlackReplyTarget) bool {
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

	c.createReplyPlaceholdersOrWarn(ctx, latest, slackImmediatePlaceholder, "channel", latest.ChannelID, "message_ts", latest.MessageTS)

	text, content := combineSlackBufferedMessages(buffered)
	if err := submit(ctx, newSlackInboundMessage(text, &content, latest)); err != nil {
		c.log.Error("publish buffered Slack inbound message", "error", err)
		c.finishSlackStack(key)

		c.warnConsumeReservedPlaceholder(ctx, latest, "I couldn't process the queued Slack follow-up: "+err.Error(), "consume buffered Slack publish failure placeholder")
	}
}

func (c *Connector) finishSlackStack(key string) { c.mu.Lock(); delete(c.stacks, key); c.mu.Unlock() }

func combineSlackBufferedMessages(buffered []slackBufferedMessage) (string, events.InboundContent) {
	parts := make([]string, 0, len(buffered))

	var content events.InboundContent

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
				c.log.Debug("received Slack socket event", "event_type", event.Type, "request_type", event.Request.Type, "envelope_id", event.Request.EnvelopeID, "retry_attempt", event.Request.RetryAttempt, "retry_reason", event.Request.RetryReason)
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
		c.log.Debug("ignored Slack message event", "reason", "empty_user", "channel", ev.Channel, "channel_type", ev.ChannelType, "bot_id_present", ev.BotID != "")

		return
	}

	if ev.User == c.botUserID {
		c.log.Debug("ignored Slack message event", "reason", "bot_user", "user", ev.User, "channel", ev.Channel, "channel_type", ev.ChannelType)

		return
	}

	if ev.BotID != "" {
		c.log.Debug("ignored Slack message event", "reason", "bot_message", "user", ev.User, "channel", ev.Channel, "channel_type", ev.ChannelType, "bot_id_present", true)

		return
	}

	subtype := strings.TrimSpace(ev.SubType)
	if subtype != "" && subtype != slack.MsgSubTypeFileShare {
		c.log.Debug("ignored Slack message event", "reason", "unsupported_subtype", "user", ev.User, "channel", ev.Channel, "channel_type", ev.ChannelType, "subtype", subtype)

		return
	}

	threadTS := strings.TrimSpace(ev.ThreadTimeStamp)
	dmMessage := ev.Channel == c.config.Room && ev.User == c.config.HumanUserID && strings.HasPrefix(ev.Channel, "D")

	socialThreadReply := false

	if c.config.SocialMode.Enabled && threadTS != "" && !strings.HasPrefix(ev.Channel, "D") && c.socialModeCouldAllowUser(ev.User) {
		channel, _, ok := c.socialModeChannel(ctx, ev.Channel)
		socialThreadReply = ok && c.socialModeAllowsUser(channel, ev.User)
	}

	rawText := strings.TrimSpace(slackMessageEventText(ev))

	text := rawText
	if socialThreadReply {
		text = c.stripSlackBotMention(text)
	}

	fileCount := len(slackMessageEventFiles(ev))
	c.log.Debug("received Slack message event", "user", ev.User, "channel", ev.Channel, "message_ts", ev.TimeStamp, "channel_type", ev.ChannelType, "subtype", subtype, "thread_ts_present", threadTS != "", "text_len", len(text), "file_count", fileCount, "room_match", ev.Channel == c.config.Room, "human_match", ev.User == c.config.HumanUserID, "dm_channel", strings.HasPrefix(ev.Channel, "D"), "dm_message", dmMessage, "social_thread_reply", socialThreadReply)

	if !dmMessage && !socialThreadReply {
		c.log.Debug("ignored Slack message event", "reason", "not_dm_or_allowed_social_thread", "user", ev.User, "channel", ev.Channel, "channel_type", ev.ChannelType, "thread_ts_present", threadTS != "", "room_match", ev.Channel == c.config.Room, "human_match", ev.User == c.config.HumanUserID, "dm_channel", strings.HasPrefix(ev.Channel, "D"), "social_thread_reply", socialThreadReply)

		return
	}

	if text == "" && fileCount == 0 {
		c.log.Debug("ignored Slack message event", "reason", "empty_text_and_no_files", "user", ev.User, "channel", ev.Channel, "channel_type", ev.ChannelType, "thread_ts_present", threadTS != "")

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

	if socialThreadReply && c.slackSocialThreadReplyPingsAway(rawText) {
		c.log.Debug("ignored Slack social thread reply", "reason", "pinged_other_without_bot_mention", "user", ev.User, "channel", ev.Channel, "message_ts", ev.TimeStamp, "thread_ts", threadTS)

		return
	}

	replyTarget := &events.SlackReplyTarget{ChannelID: ev.Channel, MessageTS: ev.TimeStamp, ThreadTS: threadTS}
	if threadTS != "" {
		handled, err := c.threadRouter.PrepareThreadReply(events.TextConversationTarget{ChannelID: ev.Channel, ThreadID: threadTS})
		if err != nil {
			c.log.Error("prepare Slack thread reply", "error", err, "channel", ev.Channel, "thread_ts", threadTS)
			return
		}

		responseRooted := false

		if !handled {
			if socialThreadReply {
				return
			}

			responseRooted, err = c.threadRouter.PrepareResponseThreadReply(events.TextConversationTarget{ChannelID: ev.Channel, MessageID: threadTS})
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

		switch strings.TrimSpace(text) {
		case "🛑", "⏹️":
			if err := c.stopSlackThread(ctx, ev.Channel, threadTS); err != nil {
				c.log.Error("stop Slack goal thread", "error", err, "channel", ev.Channel, "thread_ts", threadTS)
				return
			}

			return
		}

		if handled {
			goal, rejection, isGoal := harnessbridge.ParseGoalRequest(text)
			if isGoal {
				if rejection != "" {
					if err := c.postSlackThreadReply(ctx, ev.Channel, threadTS, rejection); err != nil {
						c.log.Warn("post Slack thread goal rejection", "error", err, "channel", ev.Channel, "message_ts", ev.TimeStamp, "thread_ts", threadTS)
					}

					return
				}

				content := c.inboundContentForMessageEvent(ctx, ev)
				content.Text = text

				key := slackThreadStackKey(replyTarget)
				if c.bufferSlackStack(ctx, key, content.Text, &content, replyTarget) {
					return
				}

				c.beginSlackStack(key)
				c.createReplyPlaceholdersOrWarn(ctx, replyTarget, slackGoalPlaceholder, "channel", ev.Channel, "message_ts", ev.TimeStamp, "thread_ts", threadTS)

				inbound := newSlackInboundMessage(goal.Objective, &content, replyTarget)
				if !c.startSlackGoal(ctx, key, replyTarget, "", goal, inbound) {
					return
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

		c.createReplyPlaceholdersOrWarn(ctx, replyTarget, slackImmediatePlaceholder, "channel", ev.Channel, "message_ts", ev.TimeStamp, "thread_ts", threadTS)

		inbound := newSlackInboundMessage(content.Text, &content, replyTarget)

		// Log reading guide: correlate by channel/message_ts/thread_ts. A pre-turn stuck placeholder is proven by a created placeholder, this handoff with pending_placeholder=true, then a submission failure before bridge/rocketcode logs and no later claimed-placeholder log.
		c.log.Info("handing Slack thread reply to router", "channel", ev.Channel, "message_ts", ev.TimeStamp, "thread_ts", threadTS, "response_rooted", responseRooted, "pending_placeholder", c.hasPendingState(replyTarget))

		if responseRooted {
			handled, err = c.threadRouter.SubmitResponseThreadReply(ctx, events.TextConversationTarget{ChannelID: ev.Channel, MessageID: threadTS, ThreadID: threadTS}, inbound)
		} else {
			handled, err = c.threadRouter.SubmitThreadReply(ctx, events.TextConversationTarget{ChannelID: ev.Channel, ThreadID: threadTS}, inbound)
		}

		if err != nil {
			c.log.Error("submit Slack thread reply", "error", err, "channel", ev.Channel, "message_ts", ev.TimeStamp, "thread_ts", threadTS, "pending_placeholder", c.hasPendingState(replyTarget))
			c.finishSlackStack(key)

			c.warnConsumeReservedPlaceholder(ctx, replyTarget, "I couldn't submit that Slack thread reply: "+err.Error(), "consume Slack thread reply error placeholder")

			return
		}

		if !handled {
			c.log.Warn("Slack thread reply was not handled after placeholder", "channel", ev.Channel, "message_ts", ev.TimeStamp, "thread_ts", threadTS, "response_rooted", responseRooted, "pending_placeholder", c.hasPendingState(replyTarget))
			c.finishSlackStack(key)

			c.warnConsumeReservedPlaceholder(ctx, replyTarget, "I couldn't find an active managed thread for that reply.", "consume unhandled Slack thread reply placeholder")

			return
		}

		c.addRobotReaction(ctx, replyTarget)
		c.log.Info("accepted Slack thread reply", "user", ev.User, "channel", ev.Channel, "thread_ts", threadTS, "text_len", len(text), "attachment_count", len(content.Attachments))

		return
	}

	if text := strings.TrimSpace(text); text == "🛑" || text == "⏹️" {
		c.stopMainSlack(ctx)
		return
	}

	if goal, rejection, ok := harnessbridge.ParseGoalRequest(text); ok {
		replyTarget.ThreadTS = ev.TimeStamp
		if rejection != "" {
			if err := c.postSlackThreadReply(ctx, ev.Channel, ev.TimeStamp, rejection); err != nil {
				c.log.Warn("post Slack goal rejection", "error", err, "channel", ev.Channel, "message_ts", ev.TimeStamp)
			}

			return
		}

		key := slackThreadStackKey(replyTarget)
		c.beginSlackStack(key)
		c.createReplyPlaceholdersOrWarn(ctx, replyTarget, slackGoalPlaceholder, "channel", ev.Channel, "message_ts", ev.TimeStamp, "agent", "main")

		content := c.inboundContentForMessageEvent(ctx, ev)
		content.Text = text

		inbound := newSlackInboundMessage(goal.Objective, &content, replyTarget)
		if !c.startSlackGoal(ctx, key, replyTarget, "main", goal, inbound) {
			return
		}

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

		c.createReplyPlaceholdersOrWarn(ctx, replyTarget, slackImmediatePlaceholder, "channel", ev.Channel, "message_ts", ev.TimeStamp, "agent", agent)

		content := c.inboundContentForMessageEvent(ctx, ev)
		content.Text = text
		inbound := newSlackInboundMessage(promptText, &content, replyTarget)
		c.log.Info("handing Slack thread start to router", "channel", ev.Channel, "message_ts", ev.TimeStamp, "agent", agent, "pending_placeholder", c.hasPendingState(replyTarget))

		if err := c.threadRouter.StartThread(ctx, agent, preSeed, events.TextConversationTarget{ChannelID: replyTarget.ChannelID, ThreadID: replyTarget.ThreadTS}, inbound); err != nil {
			c.log.Error("start Slack thread", "error", err, "channel", ev.Channel, "message_ts", ev.TimeStamp, "agent", agent, "pending_placeholder", c.hasPendingState(replyTarget))
			c.finishSlackStack(key)

			c.warnConsumeReservedPlaceholder(ctx, replyTarget, "I couldn't start that managed thread: "+err.Error(), "consume Slack thread start rejection placeholder")

			return
		}

		c.addRobotReaction(ctx, replyTarget)
		c.log.Info("accepted Slack thread start", "user", ev.User, "channel", ev.Channel, "message_ts", ev.TimeStamp, "agent", agent, "text_len", len(promptText), "attachment_count", len(content.Attachments))

		return
	}

	replyTarget.ThreadTS = ""
	content := c.inboundContentForMessageEvent(ctx, ev)

	content.Text = text
	if !c.acceptMainSlackMessage(ctx, text, &content, replyTarget) {
		return
	}

	c.log.Info("accepted Slack inbound message", "user", ev.User, "channel", ev.Channel, "subtype", subtype, "text_len", len(text), "attachment_count", len(content.Attachments))
}

func (c *Connector) isBotAuthoredSlackMessage(ctx context.Context, channelID, messageTS string) bool {
	channelID = strings.TrimSpace(channelID)

	messageTS = strings.TrimSpace(messageTS)
	if channelID == "" || messageTS == "" {
		return false
	}

	history, err := c.api.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{ChannelID: channelID, Cursor: "", Latest: messageTS, Oldest: messageTS, Inclusive: true, Limit: 1, IncludeAllMetadata: false})
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

	reaction := strings.TrimSpace(ev.Reaction)
	switch reaction {
	case slackSummaryReaction, slackOnDemandCronReaction, slackGoalStopSignReaction, slackGoalStopButtonReaction:
	default:
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
	} else {
		if !c.config.SocialMode.Enabled || !c.socialModeCouldAllowUser(ev.User) {
			return
		}

		channel, _, ok := c.socialModeChannel(ctx, channelID)
		if !ok || !c.socialModeAllowsUser(channel, ev.User) {
			return
		}
	}

	if reaction == slackOnDemandCronReaction {
		c.handleOnDemandCronReaction(ctx, ev, channelID, messageTS)
		return
	}

	threadTS, handled, err := c.resolveManagedThreadTS(ctx, channelID, messageTS)
	if err != nil {
		c.log.Error("resolve Slack thread summary target", "error", err, "channel", channelID, "message_ts", messageTS)
		return
	}

	if !handled {
		if (reaction == slackGoalStopSignReaction || reaction == slackGoalStopButtonReaction) && strings.HasPrefix(channelID, "D") {
			c.stopMainSlack(ctx)
		}

		return
	}

	switch reaction {
	case slackGoalStopSignReaction, slackGoalStopButtonReaction:
		if err := c.stopSlackThread(ctx, channelID, threadTS); err != nil {
			c.log.Error("stop Slack goal thread by reaction", "error", err, "channel", channelID, "thread_ts", threadTS, "message_ts", messageTS)
			return
		}

		return
	}

	statusTarget := &events.SlackReplyTarget{ChannelID: channelID, MessageTS: messageTS, ThreadTS: threadTS}
	c.removeReaction(ctx, statusTarget, slackSummaryInProgressReaction, "remove Slack thread summary in-progress reaction")
	c.removeReaction(ctx, statusTarget, slackSummaryCompleteReaction, "remove Slack thread summary complete reaction")
	c.addReaction(ctx, statusTarget, slackSummaryInProgressReaction, "add Slack thread summary in-progress reaction")

	handled, err = c.threadRouter.SummarizeThread(ctx, events.TextConversationTarget{ChannelID: channelID, ThreadID: threadTS})
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

func (c *Connector) handleOnDemandCronReaction(ctx context.Context, ev *slackevents.ReactionAddedEvent, channelID, messageTS string) {
	history, err := c.api.GetConversationHistoryContext(ctx, &slack.GetConversationHistoryParameters{ChannelID: channelID, Cursor: "", Latest: messageTS, Oldest: messageTS, Inclusive: true, Limit: 1, IncludeAllMetadata: false})
	if err != nil {
		c.log.Warn("load Slack on-demand cron reaction message", "error", err, "channel", channelID, "message_ts", messageTS)
		return
	}

	if history == nil || len(history.Messages) == 0 {
		return
	}

	message := history.Messages[0]

	threadTS := strings.TrimSpace(message.ThreadTimestamp)
	if threadTS == "" {
		threadTS = messageTS
	}

	replyTarget := &events.SlackReplyTarget{ChannelID: channelID, MessageTS: messageTS, ThreadTS: threadTS}

	target, ok := cronjob.OnDemandCronTarget(message.Text, slackOnDemandCronPrefix, "🔂")
	if !ok {
		if errPost := c.publishOnDemandCronReply(ctx, replyTarget, "React with `:repeat_one:` to a message containing exactly one cron target, such as `:repeat_one: daily`, `🔂 daily`, `daily`, `daily.md`, or a scheduled cron thread root containing `cron/daily.md`.", true); errPost != nil {
			c.log.Warn("publish Slack on-demand cron reaction usage", "error", errPost, "channel", channelID, "message_ts", messageTS, "thread_ts", threadTS)
		}

		return
	}

	c.handleOnDemandCronRequest(ctx, &slackevents.MessageEvent{User: ev.User, Channel: channelID, TimeStamp: messageTS, ThreadTimeStamp: message.ThreadTimestamp, Text: message.Text}, target, replyTarget)
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

	if ev.User == "" || ev.User == c.botUserID || ev.BotID != "" || strings.HasPrefix(ev.Channel, "D") || !c.socialModeCouldAllowUser(ev.User) {
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

	channel, agent, ok := c.socialModeChannel(ctx, ev.Channel)
	if !ok || !c.socialModeAllowsUser(channel, ev.User) {
		return
	}

	replyTarget := &events.SlackReplyTarget{ChannelID: ev.Channel, MessageTS: ev.TimeStamp, ThreadTS: threadTS}

	goal, rejection, isGoal := harnessbridge.ParseGoalRequest(text)
	if isGoal && rejection != "" {
		if err := c.postSlackThreadReply(ctx, ev.Channel, threadTS, rejection); err != nil {
			c.log.Warn("post Slack social goal rejection", "error", err, "channel", ev.Channel, "message_ts", ev.TimeStamp, "thread_ts", threadTS)
		}

		return
	}

	key := slackThreadStackKey(replyTarget)
	c.beginSlackStack(key)

	if !isGoal {
		for i := range c.threadAgents {
			if c.threadAgents[i].agent == agent {
				prefix := c.threadAgents[i].prefix
				if len(prefix) > 2 && strings.HasPrefix(prefix, ":") && strings.HasSuffix(prefix, ":") {
					c.addReaction(ctx, replyTarget, strings.Trim(prefix, ":"), "add Slack social agent reaction")
				}

				break
			}
		}
	}

	placeholder := slackImmediatePlaceholder
	if isGoal {
		placeholder = slackGoalPlaceholder
	}

	c.createReplyPlaceholdersOrWarn(ctx, replyTarget, placeholder, "channel", ev.Channel, "message_ts", ev.TimeStamp, "agent", agent)

	content := events.InboundContent{Text: ev.Text}
	if len(ev.Files) > 0 {
		content.Attachments, content.TextAttachments, content.HadAttachments, content.HadNonImageAttachments, content.AttachmentWarnings = c.downloadSlackAttachments(ctx, ev.Files)
	}

	promptSource := text
	if isGoal {
		promptSource = goal.Objective
	}

	promptText := c.socialPromptWithContext(ctx, ev.Channel, ev.TimeStamp, promptSource)
	content.Text = promptText

	if isGoal {
		if !c.startSlackGoal(ctx, key, replyTarget, agent, goal, newSlackInboundMessage(promptText, &content, replyTarget)) {
			return
		}

		return
	}

	c.log.Info("handing Slack social thread to router", "channel", ev.Channel, "message_ts", ev.TimeStamp, "thread_ts", threadTS, "agent", agent, "pending_placeholder", c.hasPendingState(replyTarget))

	if err := c.threadRouter.StartThread(ctx, agent, false, events.TextConversationTarget{ChannelID: replyTarget.ChannelID, ThreadID: replyTarget.ThreadTS}, newSlackInboundMessage(promptText, &content, replyTarget)); err != nil {
		c.log.Error("start Slack social thread", "error", err, "channel", ev.Channel, "message_ts", ev.TimeStamp, "agent", agent, "pending_placeholder", c.hasPendingState(replyTarget))
		c.finishSlackStack(key)

		c.warnConsumeReservedPlaceholder(ctx, replyTarget, "I couldn't start that managed thread: "+err.Error(), "consume Slack social thread start rejection placeholder")

		return
	}

	c.addRobotReaction(ctx, replyTarget)
	c.log.Info("accepted Slack social mention", "user", ev.User, "channel", ev.Channel, "message_ts", ev.TimeStamp, "thread_ts", threadTS, "agent", agent, "text_len", len(text), "attachment_count", len(content.Attachments))
}

func (c *Connector) socialModeChannel(ctx context.Context, channelID string) (channelName, agent string, ok bool) {
	if len(c.config.SocialMode.Channels) == 0 {
		return "", "", false
	}

	channel, err := c.api.GetConversationInfoContext(ctx, &slack.GetConversationInfoInput{ChannelID: channelID})
	if err != nil || channel == nil {
		return "", "", false
	}

	name := "#" + strings.TrimSpace(channel.Name)
	if name == "#" {
		return "", "", false
	}

	for _, configured := range c.config.SocialMode.Channels {
		if configured.Channel == name {
			return name, configured.Agent, true
		}
	}

	return name, "", false
}

func (c *Connector) socialModeCouldAllowUser(userID string) bool {
	userID = strings.TrimSpace(userID)
	for _, channel := range c.config.SocialMode.Channels {
		if slices.Contains(channel.AllowedUserIDs, userID) {
			return true
		}
	}

	return false
}

func (c *Connector) socialModeAllowsUser(channel, userID string) bool {
	userID = strings.TrimSpace(userID)

	for _, configured := range c.config.SocialMode.Channels {
		if configured.Channel == channel {
			return slices.Contains(configured.AllowedUserIDs, userID)
		}
	}

	return false
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

func (c *Connector) slackSocialThreadReplyPingsAway(text string) bool {
	botUserID := strings.TrimSpace(c.botUserID)
	pingedOther := false

	for {
		start := strings.IndexByte(text, '<')
		if start < 0 {
			return pingedOther
		}

		text = text[start+1:]

		end := strings.IndexByte(text, '>')
		if end < 0 {
			return pingedOther
		}

		token := text[:end]
		text = text[end+1:]

		if botUserID != "" && strings.HasPrefix(token, "@"+botUserID) && (len(token) == len(botUserID)+1 || token[len(botUserID)+1] == '|') {
			return false
		}

		pingedOther = pingedOther || strings.HasPrefix(token, "@") || token == "!channel" || token == "!here" || token == "!everyone" || strings.HasPrefix(token, "!subteam^")
	}
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

func newSlackInboundMessage(text string, content *events.InboundContent, replyTarget *events.SlackReplyTarget) *events.InboundMessage {
	contentCopy := *content
	contentCopy.Text = text

	inbound := events.NewMainInboundMessageFromContent(events.SourceSlack, events.InboundKindPrompt, "", &contentCopy, true)
	if replyTarget != nil && strings.TrimSpace(replyTarget.ThreadTS) != "" {
		inbound.ConversationID = ""
	}

	if replyTarget != nil {
		inbound.SlackReply = &events.SlackReplyTarget{
			ChannelID: replyTarget.ChannelID,
			MessageTS: replyTarget.MessageTS,
			ThreadTS:  replyTarget.ThreadTS,
		}
	}

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
	loaded, err := c.oneOffCronjobs.LoadOneOffCronjob(target)
	if err != nil {
		if errPost := c.publishOnDemandCronReply(ctx, replyTarget, "I couldn't find that cronjob. Use a top-level cron filename like `daily` or `daily.md`.", true); errPost != nil {
			c.log.Warn("publish Slack on-demand cron rejection", "error", errPost, "channel", ev.Channel, "message_ts", ev.TimeStamp, "thread_ts", replyTarget.ThreadTS)
		}

		c.log.Info("rejected Slack on-demand cron request", "user", ev.User, "channel", ev.Channel, "message_ts", ev.TimeStamp, "requested_cron", strings.TrimSpace(target), "error", err)

		return
	}

	if !strings.HasPrefix(ev.Channel, "D") && !c.cronTargetsSlackChannel(ctx, loaded, ev.Channel) {
		if errPost := c.publishOnDemandCronReply(ctx, replyTarget, "That cronjob is not configured to run in this Slack channel.", true); errPost != nil {
			c.log.Warn("publish Slack on-demand cron channel rejection", "error", errPost, "channel", ev.Channel, "message_ts", ev.TimeStamp, "thread_ts", replyTarget.ThreadTS)
		}

		c.log.Info("rejected Slack on-demand cron channel", "user", ev.User, "channel", ev.Channel, "message_ts", ev.TimeStamp, "requested_cron", strings.TrimSpace(target), "cron", loaded.RelativePath, "slack_channel", loaded.SlackChannel)

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

	if slots, err := c.createReplyPlaceholders(ctx, replyTarget, slackImmediatePlaceholder); err != nil {
		c.log.Warn("create Slack on-demand cron reply placeholders", "error", err)
	} else if slots.Key != "" {
		c.setReplyState(turnID, slots)
	}

	go c.runOnDemandCron(ctx, requestLog, loaded, replyTarget, turnID)
}

func (c *Connector) cronTargetsSlackChannel(ctx context.Context, loaded cronjob.OneOffCronjob, channelID string) bool {
	slackChannel := strings.TrimSpace(loaded.SlackChannel)

	channelID = strings.TrimSpace(channelID)
	if slackChannel == "" || channelID == "" {
		return false
	}

	if slackChannel == channelID {
		return true
	}

	channel, err := c.api.GetConversationInfoContext(ctx, &slack.GetConversationInfoInput{ChannelID: channelID})
	if err != nil || channel == nil {
		return false
	}

	return slackChannel == "#"+strings.TrimSpace(channel.Name)
}

func (c *Connector) runOnDemandCron(ctx context.Context, log *slog.Logger, loaded cronjob.OneOffCronjob, replyTarget *events.SlackReplyTarget, turnID string) {
	thinking := ""
	publish := func(ctx context.Context, text, thinkingText string, complete, postText bool, attachments []events.OutboundAttachment) error {
		outbound := events.NewMainOutboundMessage(events.SourceSystem, text, events.OutputTargetSlackMain)
		outbound.ProgressText = thinkingText
		outbound.PostProgressText = postText
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

			if thinking != "" {
				thinking += "\n"
			}

			thinking += text

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
	outbound.PostProgressText = !complete
	outbound.SlackReply = cloneSlackReplyTarget(replyTarget)

	if err := c.bus.PublishOutbound(ctx, outbound); err != nil {
		return fmt.Errorf("publish Slack on-demand cron reply: %w", err)
	}

	return nil
}

func (c *Connector) consumeReservedPlaceholder(ctx context.Context, replyTarget *events.SlackReplyTarget, text string) error {
	msg := events.NewMainOutboundMessage(events.SourceSystem, strings.TrimSpace(text), events.OutputTargetSlackMain)
	msg.TurnID = fmt.Sprintf("slack-abort-%d", time.Now().UnixNano())
	msg.Complete = true
	msg.SlackReply = cloneSlackReplyTarget(replyTarget)

	return c.SendResponse(ctx, msg)
}

func (c *Connector) warnConsumeReservedPlaceholder(ctx context.Context, replyTarget *events.SlackReplyTarget, text, logMessage string) {
	if err := c.consumeReservedPlaceholder(ctx, replyTarget, text); err != nil {
		c.log.Warn(logMessage, "error", err, "channel", replyTarget.ChannelID, "message_ts", replyTarget.MessageTS, "thread_ts", replyTarget.ThreadTS)
	}
}

func cloneSlackReplyTarget(replyTarget *events.SlackReplyTarget) *events.SlackReplyTarget {
	if replyTarget == nil {
		return nil
	}

	return &events.SlackReplyTarget{ChannelID: replyTarget.ChannelID, MessageTS: replyTarget.MessageTS, ThreadTS: replyTarget.ThreadTS}
}

func (c *Connector) createReplyPlaceholders(ctx context.Context, replyTarget *events.SlackReplyTarget, placeholder string) (slackReplySlots, error) {
	if replyTarget == nil {
		return slackReplySlots{}, nil
	}

	channelID := strings.TrimSpace(replyTarget.ChannelID)
	if channelID == "" {
		return slackReplySlots{}, nil
	}

	placeholderChannelID, thinkingTS, answerTS, err := c.postReplyPlaceholderPair(ctx, channelID, replyTarget.ThreadTS, placeholder)
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

func (c *Connector) postReplyPlaceholderPair(ctx context.Context, channelID, threadTS, placeholder string) (placeholderChannelID, thinkingTS, answerTS string, err error) {
	options := []slack.MsgOption{slack.MsgOptionText(placeholder, false)}
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

func (c *Connector) createReplyPlaceholdersOrWarn(ctx context.Context, replyTarget *events.SlackReplyTarget, placeholder string, attrs ...any) {
	if _, err := c.createReplyPlaceholders(ctx, replyTarget, placeholder); err != nil {
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
	handled, err = c.threadRouter.PrepareThreadReply(events.TextConversationTarget{ChannelID: channelID, ThreadID: messageTS})
	if err != nil {
		return "", false, fmt.Errorf("prepare Slack thread reply: %w", err)
	}

	if handled {
		return messageTS, true, nil
	}

	item, err := c.api.GetReactionsContext(ctx, slack.NewRefToMessage(channelID, messageTS), slack.GetReactionsParameters{Full: true})
	if err != nil {
		return "", false, fmt.Errorf("load Slack message reactions: %w", err)
	}

	threadTS = strings.TrimSpace(item.Message.ThreadTimestamp)

	handled, err = c.threadRouter.PrepareThreadReply(events.TextConversationTarget{ChannelID: channelID, ThreadID: threadTS})
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

func (c *Connector) startSlackGoal(ctx context.Context, key string, replyTarget *events.SlackReplyTarget, agent string, goal harnessbridge.GoalRequest, inbound *events.InboundMessage) bool {
	if err := c.threadRouter.StartGoalInThread(ctx, agent, goal.Objective, goal.CheckScript, goal.MaxTurns, events.TextConversationTarget{ChannelID: replyTarget.ChannelID, ThreadID: replyTarget.ThreadTS}, inbound); err != nil {
		c.finishSlackStack(key)

		if errors.Is(err, harnessbridge.ErrGoalAlreadyActive) {
			c.addReaction(ctx, replyTarget, slackInterruptionReaction, "add Slack duplicate goal rejection reaction")
			c.warnConsumeReservedPlaceholder(ctx, replyTarget, "A goal is already in progress in this thread. Finish or stop it before starting another.", "consume Slack duplicate goal rejection placeholder")
		} else {
			c.warnConsumeReservedPlaceholder(ctx, replyTarget, "I couldn't start that goal: "+err.Error(), "consume Slack goal rejection placeholder")
		}

		return false
	}

	c.addRobotReaction(ctx, replyTarget)

	return true
}

func (c *Connector) stopMainSlack(ctx context.Context) {
	marker := c.interruptMainTurn()
	c.finishSlackStack(slackMainStackKey)

	if marker != nil && marker.SlackReply != nil {
		c.addReaction(ctx, marker.SlackReply, slackInterruptionReaction, "add Slack main interruption reaction")
	}
}

func (c *Connector) stopSlackThread(ctx context.Context, channelID, threadTS string) error {
	marker, err := c.threadRouter.InterruptThread(events.TextConversationTarget{ChannelID: channelID, ThreadID: threadTS})
	if err != nil {
		return fmt.Errorf("stop Slack thread: %w", err)
	}

	c.finishSlackStack(slackThreadStackKey(&events.SlackReplyTarget{ChannelID: channelID, ThreadTS: threadTS}))

	if marker != nil && marker.SlackReply != nil {
		c.addReaction(ctx, marker.SlackReply, slackInterruptionReaction, "add Slack interruption reaction")
	}

	return nil
}

func (c *Connector) inboundContentForMessageEvent(ctx context.Context, ev *slackevents.MessageEvent) events.InboundContent {
	var content events.InboundContent

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
			if events.IsTextAttachment(slackFileDisplayName(file), file.Mimetype) {
				if file.Size > events.MaxInboundTextAttachmentBytes {
					warnings = append(warnings, "Skipped Slack text attachment "+slackFileDescriptor(file)+" because it exceeded the text file size limit.")

					continue
				}

				downloadURL := slackFileDownloadURL(file)
				if downloadURL == "" {
					warnings = append(warnings, "Skipped Slack text attachment "+slackFileDescriptor(file)+" because Slack did not provide a download URL.")

					continue
				}

				var buffer limitedBuffer

				buffer.limit = events.MaxInboundTextAttachmentBytes

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
