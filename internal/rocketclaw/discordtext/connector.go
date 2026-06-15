// Package discordtext bridges Discord guild text messages into rocketclaw.
package discordtext

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

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
	discordStopSignEmoji      = "🛑"
	discordStopButtonEmoji    = "⏹️"
	discordInterruptedEmoji   = "❗"
	discordGoalCompleteEmoji  = "✅"
	maxDiscordAttachmentBytes = 16 << 20
)

type discordClient interface {
	channel(channelID string) (*textChannel, error)
	message(channelID, messageID string) (*textMessage, error)
	sendMessage(channelID string, send messageSend) (*postedMessage, error)
	editMessage(channelID, messageID string, send messageSend) error
	deleteMessage(channelID, messageID string) error
	addReaction(channelID, messageID, emoji string) error
	downloadAttachment(ctx context.Context, rawURL string, limit int64) ([]byte, error)
	createThread(channelID, messageID string, start threadStart) (*textChannel, error)
	sendAttachments(channelID string, attachments []events.OutboundAttachment) error
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
	log               *slog.Logger
	config            config.DiscordTextConfig
	bus               *events.Bus
	threadRouter      harnessbridge.PrimaryTextRouter
	oneOffCronjobs    oneOffCronjobRunner
	interruptMainTurn func() *events.InboundMessage
	threadAgents      []threadAgent
	client            discordClient
	botUserID         string
	mu                sync.Mutex
	progress          map[string]*postedMessage
	roots             map[string]string
}

// New constructs a Discord text connector.
func New(cfg *config.DiscordTextConfig, bus *events.Bus, threadAgents config.ThreadAgents, threadRouter harnessbridge.PrimaryTextRouter, oneOffCronjobs oneOffCronjobRunner, interruptMainTurn func() *events.InboundMessage, logger *slog.Logger) *Connector {
	return &Connector{log: logger.With("component", "discord_text"), config: *cfg, bus: bus, threadAgents: normalizeThreadAgents(threadAgents), threadRouter: threadRouter, oneOffCronjobs: oneOffCronjobs, interruptMainTurn: interruptMainTurn}
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

// SendVoiceRelay mirrors a voice utterance into Discord text before the main session handles it.
func (c *Connector) SendVoiceRelay(_ context.Context, text string) (*events.DiscordReplyTarget, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}

	posted, err := c.client.sendMessage(c.config.ChannelID, messageSend{Content: text})
	if err != nil {
		return nil, fmt.Errorf("send Discord voice relay: %w", err)
	}

	return &events.DiscordReplyTarget{ChannelID: posted.ChannelID, MessageID: posted.ID}, nil
}

// SendExternalMCPRelay mirrors an external MCP prompt into Discord before the session handles it.
func (c *Connector) SendExternalMCPRelay(_ context.Context, channelID, text string, attachments []events.OutboundAttachment) (*events.DiscordReplyTarget, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}

	if channelID = strings.TrimSpace(channelID); channelID == "" {
		channelID = c.config.ChannelID
	}

	root, err := c.client.sendMessage(channelID, messageSend{Content: text})
	if err != nil {
		return nil, fmt.Errorf("send Discord external MCP relay root: %w", err)
	}

	thread, err := c.createThread(root.ChannelID, root.ID, text)
	if err != nil {
		return nil, fmt.Errorf("create Discord external MCP thread: %w", err)
	}

	if len(attachments) > 0 {
		if err := c.uploadResponseAttachments(thread.ID, attachments); err != nil {
			return nil, fmt.Errorf("send Discord external MCP relay attachments: %w", err)
		}
	}

	c.recordThreadRoot(root.ChannelID, root.ID, thread.ID)

	return &events.DiscordReplyTarget{ChannelID: root.ChannelID, MessageID: root.ID, ThreadID: thread.ID}, nil
}

// SendExternalMCPThreadRelay mirrors an external MCP follow-up into an existing Discord thread.
func (c *Connector) SendExternalMCPThreadRelay(_ context.Context, threadID, text string, attachments []events.OutboundAttachment) (*events.DiscordReplyTarget, error) {
	posted, err := c.client.sendMessage(threadID, messageSend{Content: strings.TrimSpace(text)})
	if err != nil {
		return nil, fmt.Errorf("send Discord external MCP thread relay: %w", err)
	}

	if len(attachments) > 0 {
		if err := c.uploadResponseAttachments(threadID, attachments); err != nil {
			return nil, fmt.Errorf("send Discord external MCP thread relay attachments: %w", err)
		}
	}

	return &events.DiscordReplyTarget{ChannelID: threadID, MessageID: posted.ID, ThreadID: threadID}, nil
}

// SendResponse posts a streamed response message in Discord.
func (c *Connector) SendResponse(_ context.Context, msg *events.OutboundMessage) error {
	if msg == nil || c.client == nil {
		return nil
	}

	channelID := c.replyChannel(msg.DiscordReply)
	if channelID == "" {
		return nil
	}

	if strings.TrimSpace(msg.ProgressText) != "" && strings.TrimSpace(msg.Text) == "" {
		return c.sendProgress(channelID, msg)
	}

	var posted []*postedMessage

	if msg.Text != "" && (msg.Complete || msg.PostProgressText) {
		var err error

		posted, err = c.postResponseChunks(channelID, msg.DiscordReply, splitDiscordResponseText(msg.Text))
		if err != nil {
			return err
		}

		if msg.Complete && msg.Checkpoint != nil && msg.DiscordReply != nil && strings.TrimSpace(msg.DiscordReply.ThreadID) == "" {
			for i := range posted {
				if err := c.threadRouter.RecordResponseCheckpoint(events.TextConversationTarget{ChannelID: posted[i].ChannelID, MessageID: posted[i].ID}, *msg.Checkpoint); err != nil {
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

	if msg.Complete && msg.GoalComplete {
		if msg.DiscordReply != nil && strings.TrimSpace(msg.DiscordReply.ChannelID) != "" && strings.TrimSpace(msg.DiscordReply.MessageID) != "" {
			c.addReaction(msg.DiscordReply.ChannelID, msg.DiscordReply.MessageID, discordGoalCompleteEmoji)
		}

		if len(posted) > 0 {
			last := posted[len(posted)-1]
			c.addReaction(last.ChannelID, last.ID, discordGoalCompleteEmoji)
		}
	}

	if msg.Complete {
		c.deleteProgress(msg.TurnID)
	}

	return nil
}

// SendCronjobChannelThread posts one scheduled cronjob result in a new Discord thread.
func (c *Connector) SendCronjobChannelThread(ctx context.Context, channelID, relativePath, agent, ranAt, text string, attachments []events.OutboundAttachment) error {
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

	thread, err := c.createThread(root.ChannelID, root.ID, relativePath)
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

	seedText := "Cronjob " + relativePath + " ran at " + ranAt + " with agent " + strings.TrimSpace(agent) + "."
	if strings.TrimSpace(text) != "" {
		seedText += "\n\nHuman-visible cron output:\n" + strings.TrimSpace(text)
	}

	if names := events.AttachmentNamesSpeech(attachments); names != "" {
		seedText += "\n\n" + names
	}

	if err := c.threadRouter.RegisterCronThread(ctx, events.TextConversationTarget{ThreadID: thread.ID}, agent, seedText); err != nil {
		return fmt.Errorf("register Discord cronjob thread: %w", err)
	}

	return nil
}

func (c *Connector) sendProgress(channelID string, msg *events.OutboundMessage) error {
	text := strings.TrimSpace(msg.ProgressText)

	c.mu.Lock()
	progress := c.progress[strings.TrimSpace(msg.TurnID)]
	c.mu.Unlock()

	if progress != nil {
		return c.client.editMessage(progress.ChannelID, progress.ID, messageSend{Content: text})
	}

	posted, err := c.client.sendMessage(channelID, messageSend{Content: text})
	if err != nil {
		return err
	}

	if strings.TrimSpace(msg.TurnID) != "" {
		c.mu.Lock()
		if c.progress == nil {
			c.progress = map[string]*postedMessage{}
		}

		c.progress[strings.TrimSpace(msg.TurnID)] = posted
		c.mu.Unlock()
	}

	return nil
}

func (c *Connector) deleteProgress(turnID string) {
	turnID = strings.TrimSpace(turnID)

	c.mu.Lock()
	progress := c.progress[turnID]
	delete(c.progress, turnID)
	c.mu.Unlock()

	if progress != nil {
		if err := c.client.deleteMessage(progress.ChannelID, progress.ID); err != nil {
			c.log.Warn("delete Discord progress message", "channel", progress.ChannelID, "message", progress.ID, "error", err)
		}
	}
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

//nolint:gocyclo // Message routing branches mirror Discord event semantics.
func (c *Connector) handleMessage(ctx context.Context, ev *messageCreate) {
	if ev == nil || ev.Message == nil || ev.Message.Author == nil || ev.Message.Author.Bot {
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

	isDM := channel.Type == channelTypeDM
	isThread := channel.Type == channelTypeGuildPublicThread || channel.Type == channelTypeGuildPrivateThread || channel.Type == channelTypeGuildNewsThread

	parentID := strings.TrimSpace(channel.ParentID)

	baseChannelID := msg.ChannelID
	if isThread {
		baseChannelID = parentID
	}

	socialChannel := config.TextSocialChannelConfig{}
	social := false

	if c.config.SocialMode.Enabled {
		for _, channel := range c.config.SocialMode.Channels {
			if channel.Channel == strings.TrimSpace(baseChannelID) {
				socialChannel, social = channel, true
				break
			}
		}
	}

	if !isDM && baseChannelID != c.config.ChannelID && !social {
		return
	}

	if isDM || !social && baseChannelID == c.config.ChannelID {
		if msg.Author.ID != c.config.HumanUserID {
			return
		}
	} else if social {
		if !slices.Contains(socialChannel.AllowedUserIDs, msg.Author.ID) {
			return
		}
	}

	mentioned := c.botUserID != "" && (strings.Contains(msg.Content, "<@"+c.botUserID+">") || strings.Contains(msg.Content, "<@!"+c.botUserID+">"))
	text := stripBotMention(strings.TrimSpace(msg.Content), c.botUserID)

	reply := &events.DiscordReplyTarget{ChannelID: msg.ChannelID, MessageID: msg.ID}
	if isDM {
		reply.ThreadID = msg.ChannelID

		switch strings.TrimSpace(text) {
		case discordStopSignEmoji, discordStopButtonEmoji:
			if !c.stopDiscordThread(msg.ChannelID) {
				c.stopMainDiscord()
			}

			return
		}

		goal, rejection, isGoal := harnessbridge.ParseGoalRequest(text)
		if isGoal {
			if rejection != "" {
				c.publishOnDemandCronReply(ctx, reply, rejection, true)
			} else {
				c.startGoal(ctx, "main", reply, goal, c.inboundMessage(ctx, msg, text, reply))
			}

			return
		}

		handled, err := c.threadRouter.SubmitThreadReply(ctx, events.TextConversationTarget{ThreadID: msg.ChannelID}, c.inboundMessage(ctx, msg, text, reply))
		if err != nil {
			c.log.Error("submit Discord DM managed reply", "channel", msg.ChannelID, "error", err)
		}

		if handled {
			return
		}
	}

	if isThread {
		reply.ThreadID = msg.ChannelID

		switch strings.TrimSpace(text) {
		case discordStopSignEmoji, discordStopButtonEmoji:
			c.stopDiscordThread(msg.ChannelID)
			return
		}

		goal, rejection, isGoal := harnessbridge.ParseGoalRequest(text)
		if isGoal {
			if rejection != "" {
				c.publishOnDemandCronReply(ctx, reply, rejection, true)
			} else {
				c.startGoal(ctx, "", reply, goal, c.inboundMessage(ctx, msg, text, reply))
			}

			return
		}

		handled, err := c.threadRouter.SubmitThreadReply(ctx, events.TextConversationTarget{ThreadID: msg.ChannelID}, c.inboundMessage(ctx, msg, text, reply))
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

	if strings.HasPrefix(text, discordCronPrefix) || strings.HasPrefix(text, discordRepeatOneEmoji) {
		target := strings.TrimSpace(strings.TrimPrefix(text, discordCronPrefix))
		if after, ok := strings.CutPrefix(text, discordRepeatOneEmoji); ok {
			target = strings.TrimSpace(after)
		}

		c.handleOnDemandCronRequest(ctx, reply, target, baseChannelID, isDM)

		return
	}

	if social && !mentioned {
		return
	}

	if goal, rejection, ok := harnessbridge.ParseGoalRequest(text); ok {
		if rejection != "" {
			c.publishOnDemandCronReply(ctx, reply, rejection, true)
			return
		}

		if !isDM {
			thread, err := c.createThread(msg.ChannelID, msg.ID, goal.Objective)
			if err != nil {
				c.log.Error("create Discord goal thread", "channel", msg.ChannelID, "message", msg.ID, "error", err)
				return
			}

			reply.ThreadID = thread.ID
			c.recordThreadRoot(msg.ChannelID, msg.ID, thread.ID)
		}

		agent := "main"
		if social {
			agent = socialChannel.Agent
		}

		c.startGoal(ctx, agent, reply, goal, c.inboundMessage(ctx, msg, text, reply))

		return
	}

	if social {
		thread, err := c.createThread(msg.ChannelID, msg.ID, text)
		if err != nil {
			c.log.Error("create Discord social thread", "channel", msg.ChannelID, "message", msg.ID, "error", err)
			return
		}

		reply.ThreadID = thread.ID
		c.recordThreadRoot(msg.ChannelID, msg.ID, thread.ID)

		if err := c.threadRouter.StartThread(ctx, socialChannel.Agent, false, events.TextConversationTarget{ThreadID: thread.ID}, c.inboundMessage(ctx, msg, text, reply)); err != nil {
			c.log.Error("start Discord social thread bridge", "thread", thread.ID, "error", err)
		}

		return
	}

	if matched, agent, preSeed, promptText := c.threadAgentPrompt(text); matched {
		if !isDM {
			thread, err := c.createThread(msg.ChannelID, msg.ID, promptText)
			if err != nil {
				c.log.Error("create Discord managed thread", "channel", msg.ChannelID, "message", msg.ID, "error", err)
				return
			}

			reply.ThreadID = thread.ID
			c.recordThreadRoot(msg.ChannelID, msg.ID, thread.ID)
		}

		if err := c.threadRouter.StartThread(ctx, agent, preSeed, events.TextConversationTarget{ThreadID: reply.ThreadID}, c.inboundMessage(ctx, msg, promptText, reply)); err != nil {
			c.log.Error("start Discord thread bridge", "thread", reply.ThreadID, "error", err)
		}

		return
	}

	if err := c.bus.PublishInbound(ctx, c.inboundMessage(ctx, msg, text, reply)); err != nil {
		c.log.Error("publish Discord text inbound", "error", err)
	}
}

func (c *Connector) handleResponseThreadReply(ctx context.Context, ev *messageCreate, text string) (bool, error) {
	msg := ev.Message

	reference := msg.MessageReference
	if reference == nil || strings.TrimSpace(reference.MessageID) == "" {
		return false, nil
	}

	if handled, err := c.threadRouter.PrepareResponseThreadReply(events.TextConversationTarget{ChannelID: msg.ChannelID, MessageID: reference.MessageID}); err != nil || !handled {
		if err != nil {
			return handled, fmt.Errorf("prepare Discord response-rooted thread reply: %w", err)
		}

		return handled, nil
	}

	thread, err := c.createThread(msg.ChannelID, reference.MessageID, text)
	if err != nil {
		return true, err
	}

	reply := &events.DiscordReplyTarget{ChannelID: thread.ID, MessageID: msg.ID, ThreadID: thread.ID}

	handled, err := c.threadRouter.SubmitResponseThreadReply(ctx, events.TextConversationTarget{ChannelID: msg.ChannelID, MessageID: reference.MessageID, ThreadID: thread.ID}, c.inboundMessage(ctx, msg, text, reply))
	if err != nil {
		return handled, fmt.Errorf("submit Discord response-rooted thread reply: %w", err)
	}

	return handled, nil
}

func (c *Connector) handleReaction(ctx context.Context, ev *reactionAdd) {
	if ev == nil {
		return
	}

	if ev.UserID != c.config.HumanUserID {
		allowed := false

		channelID := strings.TrimSpace(ev.ChannelID)
		if channel, err := c.client.channel(channelID); err == nil && strings.TrimSpace(channel.ParentID) != "" {
			channelID = strings.TrimSpace(channel.ParentID)
		}

		if c.config.SocialMode.Enabled {
			for _, channel := range c.config.SocialMode.Channels {
				if channel.Channel == channelID && slices.Contains(channel.AllowedUserIDs, ev.UserID) {
					allowed = true
					break
				}
			}
		}

		if !allowed {
			return
		}
	}

	switch ev.Emoji.Name {
	case discordSummaryEmoji:
		c.handleSummaryReaction(ctx, ev)
	case discordRepeatOneEmoji, discordRepeatOneName:
		c.handleOnDemandCronReaction(ctx, ev)
	case discordStopSignEmoji, discordStopButtonEmoji:
		if c.stopDiscordThread(c.threadForReaction(ev.ChannelID, ev.MessageID)) {
			return
		}

		if ev.UserID == c.config.HumanUserID {
			channelID := strings.TrimSpace(ev.ChannelID)

			channel, err := c.client.channel(channelID)
			if err == nil && (channel.Type == channelTypeDM || channelID == c.config.ChannelID) {
				c.stopMainDiscord()
			}
		}
	}
}

func (c *Connector) recordThreadRoot(channelID, messageID, threadID string) {
	c.mu.Lock()
	if c.roots == nil {
		c.roots = map[string]string{}
	}

	c.roots[channelID+":"+messageID] = threadID
	c.mu.Unlock()
}

func (c *Connector) threadForReaction(channelID, messageID string) string {
	c.mu.Lock()
	threadID := c.roots[channelID+":"+messageID]
	c.mu.Unlock()

	if threadID != "" {
		return threadID
	}

	message, err := c.client.message(channelID, messageID)
	if err == nil && message.Thread != nil && strings.TrimSpace(message.Thread.ID) != "" {
		return strings.TrimSpace(message.Thread.ID)
	}

	return channelID
}

func (c *Connector) stopDiscordThread(threadID string) bool {
	marker, err := c.threadRouter.InterruptThread(events.TextConversationTarget{ThreadID: threadID})
	if err != nil {
		c.log.Error("stop Discord thread", "thread", threadID, "error", err)
		return false
	}

	if marker != nil {
		c.addReaction(marker.DiscordReply.ChannelID, marker.DiscordReply.MessageID, discordInterruptedEmoji)
	}

	return marker != nil
}

func (c *Connector) stopMainDiscord() {
	marker := c.interruptMainTurn()
	if marker != nil && marker.DiscordReply != nil {
		c.addReaction(marker.DiscordReply.ChannelID, marker.DiscordReply.MessageID, discordInterruptedEmoji)
	}
}

func (c *Connector) addReaction(channelID, messageID, emoji string) {
	if err := c.client.addReaction(channelID, messageID, emoji); err != nil {
		c.log.Warn("add Discord reaction", "channel", channelID, "message", messageID, "emoji", emoji, "error", err)
	}
}

func (c *Connector) startGoal(ctx context.Context, agent string, reply *events.DiscordReplyTarget, goal harnessbridge.GoalRequest, inbound *events.InboundMessage) {
	if err := c.threadRouter.StartGoalInThread(ctx, agent, goal.Objective, goal.CheckScript, goal.MaxTurns, events.TextConversationTarget{ThreadID: reply.ThreadID}, inbound); err != nil {
		if errors.Is(err, harnessbridge.ErrGoalAlreadyActive) {
			c.addReaction(reply.ChannelID, reply.MessageID, discordInterruptedEmoji)
			c.publishOnDemandCronReply(ctx, reply, "A goal is already in progress in this thread. Finish or stop it before starting another.", true)
		} else {
			c.publishOnDemandCronReply(ctx, reply, "I couldn't start that goal: "+err.Error(), true)
		}
	}
}

func (c *Connector) handleSummaryReaction(ctx context.Context, ev *reactionAdd) {
	handled, err := c.threadRouter.SummarizeThread(ctx, events.TextConversationTarget{ThreadID: ev.ChannelID})
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

	c.handleOnDemandCronRequest(ctx, reply, target, ev.ChannelID, false)
}

func (c *Connector) handleOnDemandCronRequest(ctx context.Context, reply *events.DiscordReplyTarget, target, channelID string, dm bool) {
	loaded, err := c.oneOffCronjobs.LoadOneOffCronjob(target)
	if err != nil {
		c.publishOnDemandCronReply(ctx, reply, "I couldn't find that cronjob. Use a top-level cron filename like `daily` or `daily.md`.", true)
		return
	}

	if !dm && strings.TrimSpace(loaded.SlackChannel) != strings.TrimSpace(channelID) {
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
		outbound.ProgressText = thinkingText
		outbound.PostProgressText = postText
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
	outbound.PostProgressText = !complete
	outbound.DiscordReply = &events.DiscordReplyTarget{ChannelID: reply.ChannelID, MessageID: reply.MessageID, ThreadID: reply.ThreadID}

	if err := c.bus.PublishOutbound(ctx, outbound); err != nil {
		c.log.Warn("publish Discord on-demand cron reply", "error", err)
	}
}

func (c *Connector) createThread(channelID, messageID, text string) (*textChannel, error) {
	name := threadName(text)

	thread, err := c.client.createThread(channelID, messageID, threadStart{Name: name, AutoArchiveDuration: 1440})
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

func (c *Connector) inboundMessage(ctx context.Context, msg *textMessage, text string, reply *events.DiscordReplyTarget) *events.InboundMessage {
	content := events.InboundContent{Text: text}

	for _, attachment := range msg.Attachments {
		name, mimeType := strings.TrimSpace(attachment.Filename), strings.TrimSpace(attachment.ContentType)
		if strings.HasPrefix(mimeType, "image/") {
			content.HadAttachments = true
			if attachment.Size > maxDiscordAttachmentBytes {
				content.AttachmentWarnings = append(content.AttachmentWarnings, "Skipped Discord attachment "+name+" because it exceeded the Discord attachment download limit.")
				continue
			}

			data, err := c.client.downloadAttachment(ctx, strings.TrimSpace(attachment.URL), maxDiscordAttachmentBytes)
			if err != nil || len(data) == 0 {
				content.AttachmentWarnings = append(content.AttachmentWarnings, "Skipped Discord attachment "+name+" because downloading it from Discord failed.")
				continue
			}

			content.Attachments = append(content.Attachments, events.InboundAttachment{Name: name, MIMEType: mimeType, Data: data})

			continue
		}

		if !events.IsTextAttachment(name, mimeType) {
			content.HadNonImageAttachments = true
			content.AttachmentWarnings = append(content.AttachmentWarnings, "Skipped Discord attachment "+name+" because it is not an image.")

			continue
		}

		if attachment.Size > events.MaxInboundTextAttachmentBytes {
			content.AttachmentWarnings = append(content.AttachmentWarnings, "Skipped Discord text attachment "+name+" because it exceeded the text file size limit.")
			continue
		}

		data, err := c.client.downloadAttachment(ctx, strings.TrimSpace(attachment.URL), events.MaxInboundTextAttachmentBytes)
		if err != nil || len(data) == 0 || !utf8.Valid(data) || strings.Contains(string(data), "\x00") {
			content.AttachmentWarnings = append(content.AttachmentWarnings, "Skipped Discord text attachment "+name+" because downloading it from Discord failed.")
			continue
		}

		content.TextAttachments = append(content.TextAttachments, "Discord text file attachment "+name+":\n"+string(data))
	}

	inbound := events.NewMainInboundMessageFromContent(events.SourceDiscordText, events.InboundKindPrompt, "", &content, true)
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
