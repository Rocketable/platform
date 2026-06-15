package discordtext

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/cronjob"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
)

func TestSendResponseTypesAndRecordsCheckpoints(t *testing.T) {
	fake := newFakeDiscordClient()
	router := newFakeThreadRouter()
	connector := newTestConnector(fake, router)

	require.NoError(t, connector.SendResponse(t.Context(), &events.OutboundMessage{TurnID: "turn-1", ProgressText: "working", DiscordReply: &events.DiscordReplyTarget{ChannelID: "C123"}}))
	require.NoError(t, connector.SendResponse(t.Context(), &events.OutboundMessage{TurnID: "turn-1", ProgressText: "still working", DiscordReply: &events.DiscordReplyTarget{ChannelID: "C123"}}))
	assert.Equal(t, "working", fake.messages[0].send.Content)
	assert.Equal(t, []string{"C123:M1:still working"}, fake.edited)

	checkpoint := events.ResponseCheckpoint{ConversationID: events.MainConversationID(), SessionEntryID: 7, ResponseID: "resp", Model: "gpt-5.5", AssistantText: "answer"}
	err := connector.SendResponse(t.Context(), &events.OutboundMessage{TurnID: "turn-1", Text: "answer", Complete: true, DiscordReply: &events.DiscordReplyTarget{ChannelID: "C123", MessageID: "U1"}, Checkpoint: &checkpoint})
	require.NoError(t, err)
	require.Len(t, fake.messages, 2)
	assert.Equal(t, "C123", fake.messages[1].channelID)
	assert.Equal(t, "answer", fake.messages[1].send.Content)
	require.NotNil(t, fake.messages[1].send.Reference)
	assert.Equal(t, "U1", fake.messages[1].send.Reference.MessageID)
	assert.Equal(t, []string{"C123:M2"}, router.recordedCheckpoints)
	assert.Equal(t, []string{"C123:M1"}, fake.deleted)
}

func TestSendResponsePrefixesGoalProgress(t *testing.T) {
	fake := newFakeDiscordClient()
	connector := newTestConnector(fake, newFakeThreadRouter())

	reply := &events.DiscordReplyTarget{ChannelID: "C123"}
	require.NoError(t, connector.SendResponse(t.Context(), &events.OutboundMessage{TurnID: "turn-1", ProgressText: "working", DiscordReply: reply}))
	require.NoError(t, connector.SendResponse(t.Context(), &events.OutboundMessage{TurnID: "turn-2", ProgressText: "working", GoalTurn: true, DiscordReply: reply}))

	require.Len(t, fake.messages, 2)
	assert.Equal(t, "working", fake.messages[0].send.Content)
	assert.Equal(t, discordGoalProgressPrefix+"\n\nworking", fake.messages[1].send.Content)
}

func TestSendResponseAddsGoalCompleteReactions(t *testing.T) {
	fake := newFakeDiscordClient()
	connector := newTestConnector(fake, newFakeThreadRouter())

	msg := &events.OutboundMessage{Text: "done", Complete: true, GoalComplete: true, DiscordReply: &events.DiscordReplyTarget{ChannelID: "T123", MessageID: "U1", ThreadID: "T123"}}
	require.NoError(t, connector.SendResponse(t.Context(), msg))

	assert.Equal(t, []string{"T123:U1:✅", "T123:M1:✅"}, fake.reactions)
}

func TestHandleMessagePublishesConfiguredChannelInput(t *testing.T) {
	fake := newFakeDiscordClient()
	router := newFakeThreadRouter()
	connector := newTestConnector(fake, router)

	bus := events.New()
	defer bus.Close()

	connector.bus = bus

	connector.handleMessage(t.Context(), &messageCreate{Message: &textMessage{ID: "U1", ChannelID: "C123", Content: "<@BOT> hello", Author: &textUser{ID: "human"}}})

	inbound := readOneInbound(t, bus)
	assert.Equal(t, events.SourceDiscordText, inbound.Source)
	assert.Equal(t, "hello", inbound.Text)
	require.NotNil(t, inbound.DiscordReply)
	assert.Equal(t, "U1", inbound.DiscordReply.MessageID)
}

func TestHandleMessagePublishesInboundAttachments(t *testing.T) {
	fake := newFakeDiscordClient()
	fake.attachmentData = map[string][]byte{"https://cdn/image.png": []byte("png"), "https://cdn/note.txt": []byte("hello")}
	connector := newTestConnector(fake, newFakeThreadRouter())

	bus := events.New()
	defer bus.Close()

	connector.bus = bus

	connector.handleMessage(t.Context(), &messageCreate{Message: &textMessage{ID: "U1", ChannelID: "C123", Content: "see files", Author: &textUser{ID: "human"}, Attachments: []textAttachment{{Filename: "image.png", ContentType: "image/png", URL: "https://cdn/image.png", Size: 3}, {Filename: "note.txt", ContentType: "text/plain", URL: "https://cdn/note.txt", Size: 5}}}})

	inbound := readOneInbound(t, bus)
	assert.Contains(t, inbound.Text, "see files\n\nDiscord text file attachment note.txt:\nhello")
	assert.Equal(t, []events.InboundAttachment{{Name: "image.png", MIMEType: "image/png", Data: []byte("png")}}, inbound.Attachments)
}

func TestHandleMessageStartsManagedThread(t *testing.T) {
	fake := newFakeDiscordClient()
	fake.threadID = "T123"
	router := newFakeThreadRouter()
	connector := newTestConnector(fake, router)
	connector.threadAgents = normalizeThreadAgents(config.ThreadAgents{":thread:": {Agent: "planner", PreSeed: true}})

	connector.handleMessage(t.Context(), &messageCreate{Message: &textMessage{ID: "U1", ChannelID: "C123", Content: ":thread: plan this", Author: &textUser{ID: "human"}}})

	require.Len(t, fake.threads, 1)
	assert.Equal(t, "plan this", fake.threads[0].start.Name)
	assert.Equal(t, "planner", router.startedAgent)
	assert.True(t, router.startedPreSeed)
	require.NotNil(t, router.started.DiscordReply)
	assert.Equal(t, "T123", router.started.DiscordReply.ThreadID)
	assert.Equal(t, "plan this", router.started.Text)
}

func TestHandleMessageStartsManagedThreadWithUnicodeAndAliasPrefixes(t *testing.T) {
	fake := newFakeDiscordClient()
	fake.threadID = "T123"
	router := newFakeThreadRouter()
	connector := newTestConnector(fake, router)
	connector.threadAgents = normalizeThreadAgents(config.ThreadAgents{
		"🧵":         {Agent: "unicode-agent", PreSeed: true},
		":factory:": {Agent: "alias-agent"},
	})

	connector.handleMessage(t.Context(), &messageCreate{Message: &textMessage{ID: "U1", ChannelID: "C123", Content: ":thread: plan this", Author: &textUser{ID: "human"}}})

	assert.Equal(t, "unicode-agent", router.startedAgent)
	assert.True(t, router.startedPreSeed)
	require.NotNil(t, router.started)
	assert.Equal(t, "plan this", router.started.Text)

	fake.threadID = "T124"
	router.started = nil

	connector.handleMessage(t.Context(), &messageCreate{Message: &textMessage{ID: "U2", ChannelID: "C123", Content: "🏭 build it", Author: &textUser{ID: "human"}}})

	assert.Equal(t, "alias-agent", router.startedAgent)
	assert.False(t, router.startedPreSeed)
	require.NotNil(t, router.started)
	assert.Equal(t, "build it", router.started.Text)
}

func TestHandleThreadMessageSubmitsThreadReply(t *testing.T) {
	fake := newFakeDiscordClient()
	fake.channels["T123"] = &textChannel{ID: "T123", ParentID: "C123", Type: channelTypeGuildPublicThread}
	router := newFakeThreadRouter()
	router.submitThreadHandled = true
	connector := newTestConnector(fake, router)

	connector.handleMessage(t.Context(), &messageCreate{Message: &textMessage{ID: "U2", ChannelID: "T123", Content: "follow up", Author: &textUser{ID: "human"}}})

	assert.Equal(t, "T123", router.submittedThreadID)
	require.NotNil(t, router.submitted.DiscordReply)
	assert.Equal(t, "T123", router.submitted.DiscordReply.ThreadID)
}

func TestHandleThreadMessageStartsGoal(t *testing.T) {
	fake := newFakeDiscordClient()
	fake.channels["T123"] = &textChannel{ID: "T123", ParentID: "C123", Type: channelTypeGuildPublicThread}
	router := newFakeThreadRouter()
	connector := newTestConnector(fake, router)

	connector.handleMessage(t.Context(), &messageCreate{Message: &textMessage{ID: "U2", ChannelID: "T123", Content: ":repeat: maxTurns: 3 ship it", Author: &textUser{ID: "human"}}})

	require.NotNil(t, router.startedGoal)
	assert.Equal(t, "T123", router.startedGoal.DiscordReply.ThreadID)
	assert.Empty(t, router.submittedThreadID)
}

func TestHandleMessageStartsTopLevelGoalThread(t *testing.T) {
	fake := newFakeDiscordClient()
	fake.threadID = "T123"
	router := newFakeThreadRouter()
	connector := newTestConnector(fake, router)

	connector.handleMessage(t.Context(), &messageCreate{Message: &textMessage{ID: "U1", ChannelID: "C123", Content: ":checkered_flag: finish docs", Author: &textUser{ID: "human"}}})

	require.Len(t, fake.threads, 1)
	assert.Equal(t, "finish docs", fake.threads[0].start.Name)
	require.NotNil(t, router.startedGoal)
	assert.Equal(t, "T123", router.startedGoal.DiscordReply.ThreadID)
}

func TestHandleDMGoalStartsWithoutGuildThread(t *testing.T) {
	fake := newFakeDiscordClient()
	fake.channels["D123"] = &textChannel{ID: "D123", Type: channelTypeDM}
	router := newFakeThreadRouter()
	connector := newTestConnector(fake, router)

	connector.handleMessage(t.Context(), &messageCreate{Message: &textMessage{ID: "U1", ChannelID: "D123", Content: ":checkered_flag: finish docs", Author: &textUser{ID: "human"}}})

	assert.Empty(t, fake.threads)
	require.NotNil(t, router.startedGoal)
	assert.Equal(t, "D123", router.startedGoal.DiscordReply.ThreadID)
}

func TestHandleDMMessageSubmitsManagedReply(t *testing.T) {
	fake := newFakeDiscordClient()
	fake.channels["D123"] = &textChannel{ID: "D123", Type: channelTypeDM}
	router := newFakeThreadRouter()
	router.submitThreadHandled = true
	connector := newTestConnector(fake, router)

	connector.handleMessage(t.Context(), &messageCreate{Message: &textMessage{ID: "U2", ChannelID: "D123", Content: "follow up", Author: &textUser{ID: "human"}}})

	assert.Equal(t, "D123", router.submittedThreadID)
	require.NotNil(t, router.submitted.DiscordReply)
	assert.Equal(t, "D123", router.submitted.DiscordReply.ThreadID)
}

func TestHandleDMStopMessageStopsMainWhenNoManagedTurn(t *testing.T) {
	fake := newFakeDiscordClient()
	fake.channels["D123"] = &textChannel{ID: "D123", Type: channelTypeDM}
	router := newFakeThreadRouter()
	router.interruptHandled = false
	connector := newTestConnector(fake, router)
	connector.interruptMainTurn = func() *events.InboundMessage {
		return &events.InboundMessage{DiscordReply: &events.DiscordReplyTarget{ChannelID: "D123", MessageID: "MAIN"}}
	}

	connector.handleMessage(t.Context(), &messageCreate{Message: &textMessage{ID: "U2", ChannelID: "D123", Content: discordStopSignEmoji, Author: &textUser{ID: "human"}}})

	assert.Equal(t, "D123", router.interruptedThreadID)
	assert.Equal(t, []string{"D123:MAIN:❗"}, fake.reactions)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()

	for msg := range connector.bus.Inbound(ctx) {
		require.Failf(t, "unexpected inbound message", "%#v", msg)
	}
}

func TestHandleDMRunsOnDemandCronWithoutChannelMatch(t *testing.T) {
	fake := newFakeDiscordClient()
	fake.channels["D123"] = &textChannel{ID: "D123", Type: channelTypeDM}
	runner := &fakeOneOffCronjobs{loaded: cronjob.OneOffCronjob{Agent: "cron", RelativePath: "cron/daily.md", SlackChannel: "C999"}, result: cronjob.RunResult{VerbatimMessage: "done"}}
	connector := newTestConnector(fake, newFakeThreadRouter())
	connector.oneOffCronjobs = runner

	connector.handleMessage(t.Context(), &messageCreate{Message: &textMessage{ID: "U1", ChannelID: "D123", Content: "🔂 daily", Author: &textUser{ID: "human"}}})

	assert.Equal(t, []string{"daily"}, runner.targets)
	preview := readOneOutbound(t, connector.bus)
	assert.Contains(t, preview.Text, "File: `cron/daily.md`")
	final := readOneOutbound(t, connector.bus)
	assert.Equal(t, "done", final.Text)
	assert.Equal(t, []cronjob.OneOffCronjob{runner.loaded}, runner.runs)
}

func TestHandleSocialMentionStartsConfiguredAgentThread(t *testing.T) {
	fake := newFakeDiscordClient()
	fake.channels["S123"] = &textChannel{ID: "S123", Type: channelTypeGuildText}
	fake.threadID = "T123"
	router := newFakeThreadRouter()
	connector := newTestConnector(fake, router)
	connector.config.SocialMode = config.TextSocialConfig{Enabled: true, Channels: []config.TextSocialChannelConfig{{Channel: "S123", Agent: "triage", AllowedUserIDs: []string{"social-human"}}}}

	connector.handleMessage(t.Context(), &messageCreate{Message: &textMessage{ID: "U1", ChannelID: "S123", Content: "<@BOT> investigate", Author: &textUser{ID: "social-human"}}})

	require.Len(t, fake.threads, 1)
	assert.Equal(t, "triage", router.startedAgent)
	assert.Equal(t, "investigate", router.started.Text)
}

func TestHandleSocialMessageRequiresAllowedMention(t *testing.T) {
	fake := newFakeDiscordClient()
	fake.channels["S123"] = &textChannel{ID: "S123", Type: channelTypeGuildText}
	connector := newTestConnector(fake, newFakeThreadRouter())
	connector.config.SocialMode = config.TextSocialConfig{Enabled: true, Channels: []config.TextSocialChannelConfig{{Channel: "S123", Agent: "triage", AllowedUserIDs: []string{"social-human"}}}}

	connector.handleMessage(t.Context(), &messageCreate{Message: &textMessage{ID: "U1", ChannelID: "S123", Content: "<@BOT> denied", Author: &textUser{ID: "intruder"}}})
	connector.handleMessage(t.Context(), &messageCreate{Message: &textMessage{ID: "U2", ChannelID: "S123", Content: "no mention", Author: &textUser{ID: "social-human"}}})

	assert.Empty(t, fake.threads)
}

func TestHandleStopReactionInterruptsThread(t *testing.T) {
	fake := newFakeDiscordClient()
	connector := newTestConnector(fake, newFakeThreadRouter())

	connector.handleReaction(t.Context(), &reactionAdd{UserID: "human", ChannelID: "T123", MessageID: "M2", Emoji: textEmoji{Name: discordStopSignEmoji}})

	router := connector.threadRouter.(*fakeThreadRouter)
	assert.Equal(t, "T123", router.interruptedThreadID)
	assert.Equal(t, []string{"T123:M1:❗"}, fake.reactions)
}

func TestHandleStopReactionStopsMainWhenNoManagedTurn(t *testing.T) {
	fake := newFakeDiscordClient()
	fake.channels["D123"] = &textChannel{ID: "D123", Type: channelTypeDM}
	router := newFakeThreadRouter()
	router.interruptHandled = false
	connector := newTestConnector(fake, router)
	connector.interruptMainTurn = func() *events.InboundMessage {
		return &events.InboundMessage{DiscordReply: &events.DiscordReplyTarget{ChannelID: "D123", MessageID: "MAIN"}}
	}

	connector.handleReaction(t.Context(), &reactionAdd{UserID: "human", ChannelID: "D123", MessageID: "M2", Emoji: textEmoji{Name: discordStopSignEmoji}})

	assert.Equal(t, "D123", router.interruptedThreadID)
	assert.Equal(t, []string{"D123:MAIN:❗"}, fake.reactions)
}

func TestHandleConfiguredChannelStopReactionStopsMainWhenNoManagedTurn(t *testing.T) {
	fake := newFakeDiscordClient()
	fake.channels["C123"] = &textChannel{ID: "C123", Type: channelTypeGuildText}
	router := newFakeThreadRouter()
	router.interruptHandled = false
	connector := newTestConnector(fake, router)
	connector.interruptMainTurn = func() *events.InboundMessage {
		return &events.InboundMessage{DiscordReply: &events.DiscordReplyTarget{ChannelID: "C123", MessageID: "MAIN"}}
	}

	connector.handleReaction(t.Context(), &reactionAdd{UserID: "human", ChannelID: "C123", MessageID: "M2", Emoji: textEmoji{Name: discordStopButtonEmoji}})

	assert.Equal(t, "C123", router.interruptedThreadID)
	assert.Equal(t, []string{"C123:MAIN:❗"}, fake.reactions)
}

func TestHandleReactionSummarizesThread(t *testing.T) {
	connector := newTestConnector(newFakeDiscordClient(), newFakeThreadRouter())
	router := connector.threadRouter.(*fakeThreadRouter)
	router.summaryHandled = true

	connector.handleReaction(t.Context(), &reactionAdd{UserID: "human", ChannelID: "T123", MessageID: "M1", Emoji: textEmoji{Name: discordSummaryEmoji}})

	assert.Equal(t, "T123", router.summarizedThreadID)
}

func TestHandleReactionRunsOnDemandCron(t *testing.T) {
	fake := newFakeDiscordClient()
	fake.reactionMessageContent = "🔂 daily"
	runner := &fakeOneOffCronjobs{loaded: cronjob.OneOffCronjob{Agent: "cron", Prompt: "daily prompt", RelativePath: "cron/daily.md", SlackChannel: "C123"}, result: cronjob.RunResult{VerbatimMessage: "done"}}
	connector := newTestConnector(fake, newFakeThreadRouter())
	connector.oneOffCronjobs = runner

	connector.handleReaction(t.Context(), &reactionAdd{UserID: "human", ChannelID: "C123", MessageID: "M1", Emoji: textEmoji{Name: discordRepeatOneEmoji}})

	assert.Equal(t, []string{"daily"}, runner.targets)
	preview := readOneOutbound(t, connector.bus)
	assert.Contains(t, preview.Text, "File: `cron/daily.md`")
	require.NotNil(t, preview.DiscordReply)
	assert.Equal(t, "C123", preview.DiscordReply.ChannelID)
	assert.Equal(t, "M1", preview.DiscordReply.MessageID)
	final := readOneOutbound(t, connector.bus)
	assert.Equal(t, "done", final.Text)
	assert.True(t, final.Complete)
	assert.Equal(t, []cronjob.OneOffCronjob{runner.loaded}, runner.runs)
}

func TestHandleReactionAllowsSocialCronUser(t *testing.T) {
	fake := newFakeDiscordClient()
	fake.channels["S123"] = &textChannel{ID: "S123", Type: channelTypeGuildText}
	fake.reactionMessageContent = "🔂 daily"
	runner := &fakeOneOffCronjobs{loaded: cronjob.OneOffCronjob{Agent: "cron", RelativePath: "cron/daily.md", SlackChannel: "S123"}, result: cronjob.RunResult{VerbatimMessage: "done"}}
	connector := newTestConnector(fake, newFakeThreadRouter())
	connector.config.SocialMode = config.TextSocialConfig{Enabled: true, Channels: []config.TextSocialChannelConfig{{Channel: "S123", Agent: "triage", AllowedUserIDs: []string{"social-human"}}}}
	connector.oneOffCronjobs = runner

	connector.handleReaction(t.Context(), &reactionAdd{UserID: "social-human", ChannelID: "S123", MessageID: "M1", Emoji: textEmoji{Name: discordRepeatOneEmoji}})

	assert.Equal(t, []string{"daily"}, runner.targets)
	_ = readOneOutbound(t, connector.bus)
	_ = readOneOutbound(t, connector.bus)
	assert.Equal(t, []cronjob.OneOffCronjob{runner.loaded}, runner.runs)
}

func TestHandleReactionRejectsCronForDifferentDiscordChannel(t *testing.T) {
	fake := newFakeDiscordClient()
	fake.reactionMessageContent = "daily"
	runner := &fakeOneOffCronjobs{loaded: cronjob.OneOffCronjob{Agent: "cron", Prompt: "daily prompt", RelativePath: "cron/daily.md", SlackChannel: "C999"}}
	connector := newTestConnector(fake, newFakeThreadRouter())
	connector.oneOffCronjobs = runner

	connector.handleReaction(t.Context(), &reactionAdd{UserID: "human", ChannelID: "C123", MessageID: "M1", Emoji: textEmoji{Name: discordRepeatOneEmoji}})

	assert.Equal(t, []string{"daily"}, runner.targets)
	assert.Empty(t, runner.runs)
	rejection := readOneOutbound(t, connector.bus)
	assert.Equal(t, "That cronjob is not configured to run in this Discord channel.", rejection.Text)
	require.NotNil(t, rejection.DiscordReply)
	assert.Equal(t, "C123", rejection.DiscordReply.ChannelID)
	assert.Equal(t, "M1", rejection.DiscordReply.MessageID)
}

func TestHandleReactionRejectsInvalidCronTarget(t *testing.T) {
	fake := newFakeDiscordClient()
	fake.reactionMessageContent = "daily weekly"
	runner := &fakeOneOffCronjobs{}
	connector := newTestConnector(fake, newFakeThreadRouter())
	connector.oneOffCronjobs = runner

	connector.handleReaction(t.Context(), &reactionAdd{UserID: "human", ChannelID: "C123", MessageID: "M1", Emoji: textEmoji{Name: discordRepeatOneEmoji}})

	assert.Empty(t, runner.targets)
	rejection := readOneOutbound(t, connector.bus)
	assert.Contains(t, rejection.Text, "exactly one cron target")
}

func TestHandleReactionIgnoresUnauthorizedCronReaction(t *testing.T) {
	fake := newFakeDiscordClient()
	runner := &fakeOneOffCronjobs{}
	connector := newTestConnector(fake, newFakeThreadRouter())
	connector.oneOffCronjobs = runner

	connector.handleReaction(t.Context(), &reactionAdd{UserID: "other", ChannelID: "C123", MessageID: "M1", Emoji: textEmoji{Name: discordRepeatOneEmoji}})

	assert.Empty(t, runner.targets)
}

func TestSendCronjobChannelThreadCreatesThreadAndPosts(t *testing.T) {
	fake := newFakeDiscordClient()
	fake.threadID = "T123"
	router := newFakeThreadRouter()
	connector := newTestConnector(fake, router)

	require.NoError(t, connector.SendCronjobChannelThread(t.Context(), "C123", "cron", "agent", "2026-06-02", "answer", []events.OutboundAttachment{{Name: "a.txt", Data: []byte("a")}}))

	require.Len(t, fake.messages, 2)
	assert.Contains(t, fake.messages[0].send.Content, "cron")
	require.Len(t, fake.threads, 1)
	assert.Equal(t, "cron", fake.threads[0].start.Name)
	assert.Equal(t, "T123", fake.messages[1].channelID)
	assert.Equal(t, "answer", fake.messages[1].send.Content)
	assert.Equal(t, []string{"T123"}, fake.attachments)
	assert.Equal(t, "T123:agent:Cronjob cron ran at 2026-06-02 with agent agent.\n\nHuman-visible cron output:\nanswer\n\nAttached files: a.txt.", router.registeredCron)
}

func TestHandleResponseThreadReplyCreatesThread(t *testing.T) {
	fake := newFakeDiscordClient()
	fake.threadID = "T123"
	router := newFakeThreadRouter()
	router.prepareResponseHandled = true
	router.submitResponseHandled = true
	connector := newTestConnector(fake, router)

	handled, err := connector.handleResponseThreadReply(t.Context(), &messageCreate{Message: &textMessage{ID: "U2", ChannelID: "C123", Content: "follow up", Author: &textUser{ID: "human"}, MessageReference: &messageReference{MessageID: "M1"}}}, "follow up")

	require.NoError(t, err)
	assert.True(t, handled)
	assert.Equal(t, "C123:M1", router.preparedResponse)
	assert.Equal(t, "C123:M1:T123", router.submittedResponse)
}

func newTestConnector(client *fakeDiscordClient, router *fakeThreadRouter) *Connector {
	return &Connector{
		log:               slog.New(slog.DiscardHandler),
		config:            config.DiscordTextConfig{Enabled: true, Token: "token", ChannelID: "C123", HumanUserID: "human"},
		bus:               events.New(),
		threadRouter:      router,
		oneOffCronjobs:    inertOneOffCronjobs{},
		interruptMainTurn: func() *events.InboundMessage { return nil },
		client:            client,
		botUserID:         "BOT",
	}
}

type fakeDiscordClient struct {
	channels               map[string]*textChannel
	attachmentData         map[string][]byte
	threadID               string
	reactionMessageContent string
	edited                 []string
	deleted                []string
	messages               []fakeMessageSend
	threads                []fakeThreadStart
	attachments            []string
	reactions              []string
}

type fakeMessageSend struct {
	channelID string
	send      messageSend
}

type fakeThreadStart struct {
	channelID, messageID string
	start                threadStart
}

func newFakeDiscordClient() *fakeDiscordClient {
	return &fakeDiscordClient{channels: map[string]*textChannel{"C123": {ID: "C123", Type: channelTypeGuildText}}, threadID: "T1"}
}

func (f *fakeDiscordClient) Close() error { return nil }

func (f *fakeDiscordClient) channel(channelID string) (*textChannel, error) {
	return f.channels[channelID], nil
}

func (f *fakeDiscordClient) message(channelID, messageID string) (*textMessage, error) {
	return &textMessage{ID: messageID, ChannelID: channelID, Content: f.reactionMessageContent}, nil
}

func (f *fakeDiscordClient) sendMessage(channelID string, send messageSend) (*postedMessage, error) {
	f.messages = append(f.messages, fakeMessageSend{channelID: channelID, send: send})
	return &postedMessage{ID: "M" + string(rune(len(f.messages)+'0')), ChannelID: channelID, Content: send.Content}, nil
}

func (f *fakeDiscordClient) editMessage(channelID, messageID string, send messageSend) error {
	f.edited = append(f.edited, channelID+":"+messageID+":"+send.Content)
	return nil
}

func (f *fakeDiscordClient) deleteMessage(channelID, messageID string) error {
	f.deleted = append(f.deleted, channelID+":"+messageID)
	return nil
}

func (f *fakeDiscordClient) createThread(channelID, messageID string, start threadStart) (*textChannel, error) {
	f.threads = append(f.threads, fakeThreadStart{channelID: channelID, messageID: messageID, start: start})
	return &textChannel{ID: f.threadID, ParentID: channelID, Type: channelTypeGuildPublicThread}, nil
}

func (f *fakeDiscordClient) addReaction(channelID, messageID, emoji string) error {
	f.reactions = append(f.reactions, channelID+":"+messageID+":"+emoji)
	return nil
}

func (f *fakeDiscordClient) downloadAttachment(_ context.Context, rawURL string, _ int64) ([]byte, error) {
	return append([]byte(nil), f.attachmentData[rawURL]...), nil
}

func (f *fakeDiscordClient) sendAttachments(channelID string, _ []events.OutboundAttachment) error {
	f.attachments = append(f.attachments, channelID)
	return nil
}

func (f *fakeDiscordClient) userID() string { return "BOT" }

type fakeThreadRouter struct {
	startedAgent, submittedThreadID, summarizedThreadID string
	registeredCron                                      string
	preparedResponse, submittedResponse                 string
	startedPreSeed, submitThreadHandled, summaryHandled bool
	prepareResponseHandled, submitResponseHandled       bool
	interruptHandled                                    bool
	started, submitted                                  *events.InboundMessage
	startedGoal                                         *events.InboundMessage
	interruptedThreadID                                 string
	recordedCheckpoints                                 []string
}

func newFakeThreadRouter() *fakeThreadRouter { return &fakeThreadRouter{interruptHandled: true} }

func (f *fakeThreadRouter) StartThread(_ context.Context, agent string, preSeed bool, target events.TextConversationTarget, inbound *events.InboundMessage) error {
	_ = target
	f.startedAgent = agent
	f.startedPreSeed = preSeed
	f.started = inbound

	return nil
}

func (f *fakeThreadRouter) PrepareResponseThreadReply(target events.TextConversationTarget) (bool, error) {
	f.preparedResponse = target.ChannelID + ":" + target.MessageID
	return f.prepareResponseHandled, nil
}

func (f *fakeThreadRouter) PrepareThreadReply(target events.TextConversationTarget) (bool, error) {
	_ = target
	return false, nil
}

func (f *fakeThreadRouter) SubmitThreadReply(_ context.Context, target events.TextConversationTarget, inbound *events.InboundMessage) (bool, error) {
	f.submittedThreadID = target.ThreadID
	f.submitted = inbound

	return f.submitThreadHandled, nil
}

func (f *fakeThreadRouter) StartGoalInThread(_ context.Context, _, _, _ string, _ int, _ events.TextConversationTarget, inbound *events.InboundMessage) error {
	f.startedGoal = inbound

	return nil
}

func (f *fakeThreadRouter) InterruptThread(target events.TextConversationTarget) (*events.InboundMessage, error) {
	f.interruptedThreadID = target.ThreadID
	if !f.interruptHandled {
		return nil, nil
	}

	return &events.InboundMessage{DiscordReply: &events.DiscordReplyTarget{ChannelID: target.ThreadID, MessageID: "M1", ThreadID: target.ThreadID}}, nil
}

func (f *fakeThreadRouter) SubmitResponseThreadReply(_ context.Context, target events.TextConversationTarget, _ *events.InboundMessage) (bool, error) {
	f.submittedResponse = target.ChannelID + ":" + target.MessageID + ":" + target.ThreadID
	return f.submitResponseHandled, nil
}

func (f *fakeThreadRouter) SummarizeThread(_ context.Context, target events.TextConversationTarget) (bool, error) {
	f.summarizedThreadID = target.ThreadID
	return f.summaryHandled, nil
}

func (f *fakeThreadRouter) RecordResponseCheckpoint(target events.TextConversationTarget, _ events.ResponseCheckpoint) error {
	f.recordedCheckpoints = append(f.recordedCheckpoints, target.ChannelID+":"+target.MessageID)
	return nil
}

func (f *fakeThreadRouter) RegisterCronThread(_ context.Context, target events.TextConversationTarget, agent, seedText string) error {
	f.registeredCron = target.ThreadID + ":" + agent + ":" + seedText
	return nil
}

type fakeOneOffCronjobs struct {
	targets []string
	loaded  cronjob.OneOffCronjob
	runs    []cronjob.OneOffCronjob
	result  cronjob.RunResult
	errLoad error
	errRun  error
}

func (f *fakeOneOffCronjobs) LoadOneOffCronjob(target string) (cronjob.OneOffCronjob, error) {
	f.targets = append(f.targets, target)
	return f.loaded, f.errLoad
}

func (f *fakeOneOffCronjobs) RunOneOffCronjob(ctx context.Context, loaded cronjob.OneOffCronjob, _ *harnessbridge.RawRunProgress, finish func(context.Context, cronjob.RunResult, error)) {
	f.runs = append(f.runs, loaded)
	finish(ctx, f.result, f.errRun)
}

func readOneOutbound(t *testing.T, bus *events.Bus) *events.OutboundMessage {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	for msg := range bus.Outbound(ctx) {
		return msg
	}

	require.Fail(t, "timed out waiting for outbound message")

	return nil
}

func readOneInbound(t *testing.T, bus *events.Bus) *events.InboundMessage {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	for msg := range bus.Inbound(ctx) {
		return msg
	}

	require.Fail(t, "timed out waiting for inbound message")

	return nil
}
