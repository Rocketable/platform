// Package discordtext bridges Discord guild text messages into rocketclaw.
package discordtext

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/cronjob"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
)

const (
	discordTextLimit          = 1900
	discordPreferredChunkSize = 1700
	discordSummaryEmoji       = "💾"
	discordRepeatOneEmoji     = "🔂"
	discordRepeatOneName      = "repeat_one"
	discordCronPrefix         = ":repeat_one:"
)

// ThreadRouter routes Discord thread messages directly to app-owned thread bridges.
type ThreadRouter interface {
	StartDiscordThread(context.Context, string, bool, *events.InboundMessage) error
	PrepareDiscordThreadReply(context.Context, string) (bool, error)
	PrepareDiscordResponseThreadReply(context.Context, string, string) (bool, error)
	SubmitDiscordThreadReply(context.Context, string, *events.InboundMessage) (bool, error)
	SubmitDiscordResponseThreadReply(context.Context, string, string, string, *events.InboundMessage) (bool, error)
	SummarizeDiscordThread(context.Context, string) (bool, error)
	RecordDiscordResponseCheckpoint(context.Context, string, string, events.ResponseCheckpoint) error
}

type discordClient interface {
	channel(string) (*textChannel, error)
	message(string, string) (*textMessage, error)
	typing(string) error
	sendMessage(string, messageSend) (*postedMessage, error)
	createThread(string, string, threadStart) (*textChannel, error)
	sendAttachments(string, []events.OutboundAttachment) error
	userID() string
	Close() error
}

type oneOffCronjobRunner interface {
	LoadOneOffCronjob(string) (cronjob.OneOffCronjob, error)
	RunOneOffCronjob(context.Context, cronjob.OneOffCronjob, *harnessbridge.RawRunProgress, func(context.Context, cronjob.RunResult, error))
}

type threadAgent struct {
	prefix, agent string
	preSeed       bool
}

// Connector bridges Discord text events into the shared rocketclaw bus.
type Connector struct {
	log            *slog.Logger
	config         config.DiscordTextConfig
	bus            *events.Bus
	threadRouter   ThreadRouter
	oneOffCronjobs oneOffCronjobRunner
	threadAgents   []threadAgent
	client         discordClient
	botUserID      string
}

// New constructs a Discord text connector.
func New(cfg config.DiscordTextConfig, bus *events.Bus, threadAgents config.ThreadAgents, threadRouter ThreadRouter, oneOffCronjobs oneOffCronjobRunner, logger *slog.Logger) *Connector {
	return &Connector{log: logger.With("component", "discord_text"), config: cfg, bus: bus, threadAgents: normalizeThreadAgents(threadAgents), threadRouter: threadRouter, oneOffCronjobs: oneOffCronjobs}
}

// Start connects to Discord and begins consuming text events.
func (c *Connector) Start(ctx context.Context) error {
	client, err := newWire(wireConfig{token: "Bot " + c.config.Token, log: c.log})
	if err != nil {
		return fmt.Errorf("open Discord text wire: %w", err)
	}

	c.client = client

	c.botUserID = client.userID()
	go c.eventLoop(ctx, client.events)

	return nil
}

// Stop disconnects the connector from Discord.
func (c *Connector) Stop(context.Context) error {
	if c.client == nil {
		return nil
	}

	if err := c.client.Close(); err != nil {
		return fmt.Errorf("close Discord text session: %w", err)
	}

	return nil
}

// SendResponse posts a streamed response message in Discord.
func (c *Connector) SendResponse(ctx context.Context, msg *events.OutboundMessage) error {
	if msg == nil || c.client == nil {
		return nil
	}

	channelID := c.replyChannel(msg.DiscordReply)
	if channelID == "" {
		return nil
	}

	if strings.TrimSpace(msg.SlackThinking) != "" && strings.TrimSpace(msg.Text) == "" {
		if err := c.client.typing(channelID); err != nil {
			c.log.Warn("send Discord typing indicator", "channel", channelID, "error", err)
		}

		return nil
	}

	if msg.Text != "" && (msg.Complete || msg.SlackPostText) {
		posted, err := c.postResponseChunks(channelID, msg.DiscordReply, splitDiscordResponseText(msg.Text))
		if err != nil {
			return err
		}

		if msg.Complete && msg.Checkpoint != nil && msg.DiscordReply != nil && strings.TrimSpace(msg.DiscordReply.ThreadID) == "" {
			for i := range posted {
				if err := c.threadRouter.RecordDiscordResponseCheckpoint(ctx, posted[i].ChannelID, posted[i].ID, *msg.Checkpoint); err != nil {
					return fmt.Errorf("record Discord response checkpoint: %w", err)
				}
			}
		}
	}

	if msg.Complete && len(msg.Attachments) > 0 {
		if err := c.uploadResponseAttachments(channelID, msg.Attachments); err != nil {
			c.log.Warn("upload Discord response attachments", "error", err)
		}
	}

	return nil
}

// SendCronjobChannelThread posts one scheduled cronjob result in a new Discord thread.
func (c *Connector) SendCronjobChannelThread(_ context.Context, channelID, relativePath, agent, ranAt, text string, attachments []events.OutboundAttachment) error {
	if c.client == nil {
		return errors.New("discord text connector is not started")
	}

	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		channelID = c.config.ChannelID
	}

	root, err := c.client.sendMessage(channelID, messageSend{Content: "Cronjob `" + relativePath + "` ran at `" + ranAt + "` with agent `" + agent + "`."})
	if err != nil {
		return fmt.Errorf("send Discord cronjob thread root: %w", err)
	}

	thread, err := c.createThread(c.client, root.ChannelID, root.ID, relativePath)
	if err != nil {
		return fmt.Errorf("create Discord cronjob thread: %w", err)
	}

	if strings.TrimSpace(text) != "" {
		if _, err := c.postResponseChunks(thread.ID, &events.DiscordReplyTarget{ChannelID: thread.ID, ThreadID: thread.ID}, splitDiscordResponseText(text)); err != nil {
			return fmt.Errorf("send Discord cronjob thread reply: %w", err)
		}
	}

	if len(attachments) > 0 {
		if err := c.uploadResponseAttachments(thread.ID, attachments); err != nil {
			return fmt.Errorf("send Discord cronjob thread attachments: %w", err)
		}
	}

	return nil
}

func (c *Connector) eventLoop(ctx context.Context, textEvents <-chan textEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-textEvents:
			if !ok {
				return
			}

			if event.message != nil {
				c.handleMessage(ctx, event.message)
			}

			if event.reaction != nil {
				c.handleReaction(ctx, event.reaction)
			}
		}
	}
}

func (c *Connector) handleMessage(ctx context.Context, ev *messageCreate) {
	if ev == nil || ev.Message == nil || ev.Message.Author == nil || ev.Message.Author.Bot || ev.Message.Author.ID != c.config.HumanUserID {
		return
	}

	msg := ev.Message

	if msg.Author.ID == c.botUserID {
		return
	}

	channel, err := c.client.channel(msg.ChannelID)
	if err != nil {
		c.log.Warn("fetch Discord channel", "channel", msg.ChannelID, "error", err)
		return
	}

	isThread := channel.Type == channelTypeGuildPublicThread || channel.Type == channelTypeGuildPrivateThread || channel.Type == channelTypeGuildNewsThread

	parentID := strings.TrimSpace(channel.ParentID)
	if !isThread && msg.ChannelID != c.config.ChannelID || isThread && parentID != c.config.ChannelID {
		return
	}

	text := stripBotMention(strings.TrimSpace(msg.Content), c.botUserID)

	reply := &events.DiscordReplyTarget{ChannelID: msg.ChannelID, MessageID: msg.ID}
	if isThread {
		reply.ThreadID = msg.ChannelID

		handled, err := c.threadRouter.SubmitDiscordThreadReply(ctx, msg.ChannelID, newDiscordInboundMessage(text, reply))
		if err != nil {
			c.log.Error("submit Discord thread reply", "thread", msg.ChannelID, "error", err)
		}

		if handled {
			return
		}
	}

	if msg.MessageReference != nil && strings.TrimSpace(msg.MessageReference.MessageID) != "" {
		handled, err := c.handleResponseThreadReply(ctx, ev, text)
		if err != nil {
			c.log.Error("submit Discord response-rooted thread reply", "channel", msg.ChannelID, "message", msg.ID, "error", err)
		}

		if handled {
			return
		}
	}

	if matched, agent, preSeed, promptText := c.threadAgentPrompt(text); matched {
		thread, err := c.createThread(c.client, msg.ChannelID, msg.ID, promptText)
		if err != nil {
			c.log.Error("create Discord managed thread", "channel", msg.ChannelID, "message", msg.ID, "error", err)
			return
		}

		reply.ThreadID = thread.ID

		reply.ChannelID = thread.ID
		if err := c.threadRouter.StartDiscordThread(ctx, agent, preSeed, newDiscordInboundMessage(promptText, reply)); err != nil {
			c.log.Error("start Discord thread bridge", "thread", thread.ID, "error", err)
		}

		return
	}

	if err := c.bus.PublishInbound(ctx, newDiscordInboundMessage(text, reply)); err != nil {
		c.log.Error("publish Discord text inbound", "error", err)
	}
}

func (c *Connector) handleResponseThreadReply(ctx context.Context, ev *messageCreate, text string) (bool, error) {
	msg := ev.Message

	reference := msg.MessageReference
	if reference == nil || strings.TrimSpace(reference.MessageID) == "" {
		return false, nil
	}

	if handled, err := c.threadRouter.PrepareDiscordResponseThreadReply(ctx, msg.ChannelID, reference.MessageID); err != nil || !handled {
		if err != nil {
			return handled, fmt.Errorf("prepare Discord response-rooted thread reply: %w", err)
		}

		return handled, nil
	}

	thread, err := c.createThread(c.client, msg.ChannelID, reference.MessageID, text)
	if err != nil {
		return true, err
	}

	reply := &events.DiscordReplyTarget{ChannelID: thread.ID, MessageID: msg.ID, ThreadID: thread.ID}

	handled, err := c.threadRouter.SubmitDiscordResponseThreadReply(ctx, msg.ChannelID, reference.MessageID, thread.ID, newDiscordInboundMessage(text, reply))
	if err != nil {
		return handled, fmt.Errorf("submit Discord response-rooted thread reply: %w", err)
	}

	return handled, nil
}

func (c *Connector) handleReaction(ctx context.Context, ev *reactionAdd) {
	if ev == nil || ev.UserID != c.config.HumanUserID {
		return
	}

	switch ev.Emoji.Name {
	case discordSummaryEmoji:
		c.handleSummaryReaction(ctx, ev)
	case discordRepeatOneEmoji, discordRepeatOneName:
		c.handleOnDemandCronReaction(ctx, ev)
	}
}

func (c *Connector) handleSummaryReaction(ctx context.Context, ev *reactionAdd) {
	handled, err := c.threadRouter.SummarizeDiscordThread(ctx, ev.ChannelID)
	if err != nil {
		c.log.Error("summarize Discord thread", "thread", ev.ChannelID, "error", err)
	}

	if handled {
		c.log.Info("accepted Discord thread summary request", "thread", ev.ChannelID, "message", ev.MessageID)
	}
}

func (c *Connector) handleOnDemandCronReaction(ctx context.Context, ev *reactionAdd) {
	message, err := c.client.message(ev.ChannelID, ev.MessageID)
	if err != nil {
		c.log.Warn("fetch Discord cron reaction message", "channel", ev.ChannelID, "message", ev.MessageID, "error", err)
		return
	}

	reply := &events.DiscordReplyTarget{ChannelID: ev.ChannelID, MessageID: ev.MessageID}

	target, ok := cronjob.OnDemandCronTarget(message.Content, discordCronPrefix, discordRepeatOneEmoji)
	if !ok {
		c.publishOnDemandCronReply(ctx, reply, "React with `🔂` to a message containing exactly one cron target, such as `🔂 daily`, `daily`, `daily.md`, or a scheduled cron thread root containing `cron/daily.md`.", true)
		return
	}

	loaded, err := c.oneOffCronjobs.LoadOneOffCronjob(target)
	if err != nil {
		c.publishOnDemandCronReply(ctx, reply, "I couldn't find that cronjob. Use a top-level cron filename like `daily` or `daily.md`.", true)
		return
	}

	if strings.TrimSpace(loaded.SlackChannel) != ev.ChannelID {
		c.publishOnDemandCronReply(ctx, reply, "That cronjob is not configured to run in this Discord channel.", true)
		return
	}

	preview := "One-off cronjob starting.\n\nFile: `" + loaded.RelativePath + "`\nAgent: `" + strings.TrimSpace(loaded.Agent) + "`"
	c.publishOnDemandCronReply(ctx, reply, preview, false)

	turnID := fmt.Sprintf("discord-one-off-cron-%d", time.Now().UnixNano())
	go c.runOnDemandCron(ctx, loaded, reply, turnID)
}

func (c *Connector) runOnDemandCron(ctx context.Context, loaded cronjob.OneOffCronjob, reply *events.DiscordReplyTarget, turnID string) {
	thinking := ""
	publish := func(ctx context.Context, text, thinkingText string, complete, postText bool, attachments []events.OutboundAttachment) error {
		outbound := events.NewMainOutboundMessage(events.SourceSystem, text, events.OutputTargetDiscordText)
		outbound.SlackThinking = thinkingText
		outbound.SlackPostText = postText
		outbound.TurnID = turnID
		outbound.Complete = complete
		outbound.DiscordReply = &events.DiscordReplyTarget{ChannelID: reply.ChannelID, MessageID: reply.MessageID, ThreadID: reply.ThreadID}
		outbound.Attachments = events.CloneOutboundAttachments(attachments)

		if err := c.bus.PublishOutbound(ctx, outbound); err != nil {
			return fmt.Errorf("publish Discord on-demand cron output: %w", err)
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
			if errPublish := publish(ctx, "I couldn't run that on-demand cron right now.", "", true, false, nil); errPublish != nil {
				c.log.Warn("publish Discord on-demand cron failure", "error", errPublish)
			}

			return
		}

		payload := strings.TrimSpace(result.VerbatimMessage)
		if payload == "" && len(result.Attachments) == 0 {
			payload = "Cronjob completed and decided to emit no human-visible output."
		}

		if err := publish(ctx, payload, "", true, false, result.Attachments); err != nil {
			c.log.Warn("publish Discord on-demand cron result", "error", err)
		}
	})
}

func (c *Connector) publishOnDemandCronReply(ctx context.Context, reply *events.DiscordReplyTarget, text string, complete bool) {
	outbound := events.NewMainOutboundMessage(events.SourceSystem, strings.TrimSpace(text), events.OutputTargetDiscordText)
	outbound.Complete = complete
	outbound.SlackPostText = !complete
	outbound.DiscordReply = &events.DiscordReplyTarget{ChannelID: reply.ChannelID, MessageID: reply.MessageID, ThreadID: reply.ThreadID}

	if err := c.bus.PublishOutbound(ctx, outbound); err != nil {
		c.log.Warn("publish Discord on-demand cron reply", "error", err)
	}
}

func (c *Connector) createThread(client discordClient, channelID, messageID, text string) (*textChannel, error) {
	name := threadName(text)

	thread, err := client.createThread(channelID, messageID, threadStart{Name: name, AutoArchiveDuration: 1440})
	if err != nil {
		return nil, fmt.Errorf("create Discord thread: %w", err)
	}

	return thread, nil
}

func (c *Connector) postResponseChunks(channelID string, reply *events.DiscordReplyTarget, chunks []string) ([]*postedMessage, error) {
	posted := make([]*postedMessage, 0, len(chunks))
	for i := range chunks {
		send := messageSend{Content: chunks[i]}
		if i == 0 && reply != nil && strings.TrimSpace(reply.MessageID) != "" {
			fail := false
			send.Reference = &messageReference{MessageID: reply.MessageID, ChannelID: reply.ChannelID, FailIfNotExists: &fail}
		}

		msg, err := c.client.sendMessage(channelID, send)
		if err != nil {
			return posted, fmt.Errorf("send Discord response: %w", err)
		}

		posted = append(posted, msg)
	}

	return posted, nil
}

func (c *Connector) uploadResponseAttachments(channelID string, attachments []events.OutboundAttachment) error {
	if err := c.client.sendAttachments(channelID, attachments); err != nil {
		return fmt.Errorf("send Discord attachments: %w", err)
	}

	return nil
}

func (c *Connector) replyChannel(reply *events.DiscordReplyTarget) string {
	if reply == nil {
		return strings.TrimSpace(c.config.ChannelID)
	}

	if strings.TrimSpace(reply.ThreadID) != "" {
		return strings.TrimSpace(reply.ThreadID)
	}

	if strings.TrimSpace(reply.ChannelID) != "" {
		return strings.TrimSpace(reply.ChannelID)
	}

	return strings.TrimSpace(c.config.ChannelID)
}

func (c *Connector) threadAgentPrompt(text string) (matched bool, agent string, preSeed bool, prompt string) {
	for _, entry := range c.threadAgents {
		if after, ok := strings.CutPrefix(text, entry.prefix); ok {
			return true, entry.agent, entry.preSeed, strings.TrimSpace(after)
		}
	}

	return false, "", false, ""
}

func newDiscordInboundMessage(text string, reply *events.DiscordReplyTarget) *events.InboundMessage {
	inbound := events.NewMainInboundMessage(events.SourceDiscordText, events.InboundKindPrompt, "", text, true)
	inbound.DiscordReply = reply

	return inbound
}

func stripBotMention(text, botUserID string) string {
	if botUserID == "" {
		return text
	}

	text = strings.ReplaceAll(text, "<@"+botUserID+">", "")
	text = strings.ReplaceAll(text, "<@!"+botUserID+">", "")

	return strings.TrimSpace(text)
}

func threadName(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "RocketClaw"
	}

	runes := []rune(text)
	if len(runes) > 80 {
		runes = runes[:77]
		return string(runes) + "..."
	}

	return text
}

func splitDiscordResponseText(text string) []string {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}

	chunks := make([]string, 0, len(runes)/discordPreferredChunkSize+1)
	for len(runes) > 0 {
		if len(runes) < discordTextLimit {
			chunks = append(chunks, string(runes))
			break
		}

		end := discordChunkEnd(runes)
		chunks = append(chunks, string(runes[:end]))
		runes = runes[end:]
	}

	return chunks
}

func discordChunkEnd(runes []rune) int {
	preferredLimit := min(len(runes), discordPreferredChunkSize)
	if end := lastDiscordChunkBoundary(runes[:preferredLimit]); end > 0 {
		return end
	}

	return min(len(runes), discordTextLimit)
}

func lastDiscordChunkBoundary(runes []rune) int {
	for i := range slices.Backward(runes) {
		if i > 0 && unicode.IsSpace(runes[i]) {
			return i + 1
		}
	}

	return 0
}

func normalizeThreadAgents(threadAgents config.ThreadAgents) []threadAgent {
	normalized := make([]threadAgent, 0, len(threadAgents))
	for prefix, entry := range threadAgents {
		prefix = strings.TrimSpace(prefix)

		entry.Agent = strings.TrimSpace(entry.Agent)
		if prefix == "" || entry.Agent == "" {
			continue
		}

		normalized = append(normalized, threadAgent{prefix: prefix, agent: entry.Agent, preSeed: entry.PreSeed})
	}

	slices.SortFunc(normalized, func(a, b threadAgent) int { return strings.Compare(b.prefix, a.prefix) })

	return normalized
}
