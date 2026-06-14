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

	require.NoError(t, connector.SendResponse(t.Context(), &events.OutboundMessage{SlackThinking: "working", DiscordReply: &events.DiscordReplyTarget{ChannelID: "C123"}}))
	assert.Equal(t, []string{"C123"}, fake.typed)

	checkpoint := events.ResponseCheckpoint{ConversationID: events.MainConversationID(), SessionEntryID: 7, ResponseID: "resp", Model: "gpt-5.5", AssistantText: "answer"}
	err := connector.SendResponse(t.Context(), &events.OutboundMessage{Text: "answer", Complete: true, DiscordReply: &events.DiscordReplyTarget{ChannelID: "C123", MessageID: "U1"}, Checkpoint: &checkpoint})
	require.NoError(t, err)
	require.Len(t, fake.messages, 1)
	assert.Equal(t, "C123", fake.messages[0].channelID)
	assert.Equal(t, "answer", fake.messages[0].send.Content)
	require.NotNil(t, fake.messages[0].send.Reference)
	assert.Equal(t, "U1", fake.messages[0].send.Reference.MessageID)
	assert.Equal(t, []string{"C123:M1"}, router.recordedCheckpoints)
}

func TestHandleMessagePublishesConfiguredChannelInput(t *testing.T) {
	fake := newFakeDiscordClient()
	router := newFakeThreadRouter()
	connector := newTestConnector(fake, router)

	bus := events.New()
	defer bus.Close()

	connector.bus = bus

	connector.handleMessage(t.Context(), &messageCreate{Message: &textMessage{ID: "U1", ChannelID: "C123", Content: "<@BOT> hello", Author: &textUser{ID: "human"}}})

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	var inbound *events.InboundMessage
	for msg := range bus.Inbound(ctx) {
		inbound = msg
		break
	}

	require.NotNil(t, inbound)
	assert.Equal(t, events.SourceDiscordText, inbound.Source)
	assert.Equal(t, "hello", inbound.Text)
	require.NotNil(t, inbound.DiscordReply)
	assert.Equal(t, "U1", inbound.DiscordReply.MessageID)
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
	connector := newTestConnector(fake, newFakeThreadRouter())

	require.NoError(t, connector.SendCronjobChannelThread(t.Context(), "C123", "cron", "agent", "2026-06-02", "answer", []events.OutboundAttachment{{Name: "a.txt", Data: []byte("a")}}))

	require.Len(t, fake.messages, 2)
	assert.Contains(t, fake.messages[0].send.Content, "cron")
	require.Len(t, fake.threads, 1)
	assert.Equal(t, "cron", fake.threads[0].start.Name)
	assert.Equal(t, "T123", fake.messages[1].channelID)
	assert.Equal(t, "answer", fake.messages[1].send.Content)
	assert.Equal(t, []string{"T123"}, fake.attachments)
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
		log:            slog.New(slog.DiscardHandler),
		config:         config.DiscordTextConfig{Enabled: true, Token: "token", ChannelID: "C123", HumanUserID: "human"},
		bus:            events.New(),
		threadRouter:   router,
		oneOffCronjobs: inertOneOffCronjobs{},
		client:         client,
		botUserID:      "BOT",
	}
}

type fakeDiscordClient struct {
	channels               map[string]*textChannel
	threadID               string
	reactionMessageContent string
	typed                  []string
	messages               []fakeMessageSend
	threads                []fakeThreadStart
	attachments            []string
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

func (f *fakeDiscordClient) typing(channelID string) error {
	f.typed = append(f.typed, channelID)
	return nil
}

func (f *fakeDiscordClient) sendMessage(channelID string, send messageSend) (*postedMessage, error) {
	f.messages = append(f.messages, fakeMessageSend{channelID: channelID, send: send})
	return &postedMessage{ID: "M" + string(rune(len(f.messages)+'0')), ChannelID: channelID, Content: send.Content}, nil
}

func (f *fakeDiscordClient) createThread(channelID, messageID string, start threadStart) (*textChannel, error) {
	f.threads = append(f.threads, fakeThreadStart{channelID: channelID, messageID: messageID, start: start})
	return &textChannel{ID: f.threadID, ParentID: channelID, Type: channelTypeGuildPublicThread}, nil
}

func (f *fakeDiscordClient) sendAttachments(channelID string, _ []events.OutboundAttachment) error {
	f.attachments = append(f.attachments, channelID)
	return nil
}

func (f *fakeDiscordClient) userID() string { return "BOT" }

type fakeThreadRouter struct {
	startedAgent, submittedThreadID, summarizedThreadID string
	preparedResponse, submittedResponse                 string
	startedPreSeed, submitThreadHandled, summaryHandled bool
	prepareResponseHandled, submitResponseHandled       bool
	started, submitted                                  *events.InboundMessage
	recordedCheckpoints                                 []string
}

func newFakeThreadRouter() *fakeThreadRouter { return &fakeThreadRouter{} }

func (f *fakeThreadRouter) StartDiscordThread(_ context.Context, agent string, preSeed bool, inbound *events.InboundMessage) error {
	f.startedAgent = agent
	f.startedPreSeed = preSeed
	f.started = inbound

	return nil
}

func (f *fakeThreadRouter) PrepareDiscordThreadReply(context.Context, string) (bool, error) {
	return false, nil
}

func (f *fakeThreadRouter) PrepareDiscordResponseThreadReply(_ context.Context, channelID, messageID string) (bool, error) {
	f.preparedResponse = channelID + ":" + messageID
	return f.prepareResponseHandled, nil
}

func (f *fakeThreadRouter) SubmitDiscordThreadReply(_ context.Context, threadID string, inbound *events.InboundMessage) (bool, error) {
	f.submittedThreadID = threadID
	f.submitted = inbound

	return f.submitThreadHandled, nil
}

func (f *fakeThreadRouter) SubmitDiscordResponseThreadReply(_ context.Context, channelID, messageID, threadID string, _ *events.InboundMessage) (bool, error) {
	f.submittedResponse = channelID + ":" + messageID + ":" + threadID
	return f.submitResponseHandled, nil
}

func (f *fakeThreadRouter) SummarizeDiscordThread(_ context.Context, threadID string) (bool, error) {
	f.summarizedThreadID = threadID
	return f.summaryHandled, nil
}

func (f *fakeThreadRouter) RecordDiscordResponseCheckpoint(_ context.Context, channelID, messageID string, _ events.ResponseCheckpoint) error {
	f.recordedCheckpoints = append(f.recordedCheckpoints, channelID+":"+messageID)
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
