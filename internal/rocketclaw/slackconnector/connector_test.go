package slackconnector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/cronjob"
	"github.com/Rocketable/platform/internal/rocketclaw/emoji"
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
)

func TestSlackImageHelpers(t *testing.T) {
	assert.Equal(t, "photo (image/png)", slackFileDescriptor(&slack.File{Name: " photo ", Mimetype: " image/png "}))
	assert.Equal(t, "https://example.com/download", slackFileDownloadURL(&slack.File{URLPrivate: "https://example.com/private", URLPrivateDownload: " https://example.com/download "}))
	assert.Equal(t, "https://example.com/private", slackFileDownloadURL(&slack.File{URLPrivate: " https://example.com/private "}))
	assert.Empty(t, slackFileDownloadURL(nil))
	assert.Equal(t, "title", slackFileDisplayName(&slack.File{Title: " title ", ID: "F123"}))
	assert.Equal(t, "F123", slackFileDisplayName(&slack.File{ID: " F123 "}))
	assert.Equal(t, "unnamed file", slackFileDisplayName(&slack.File{}))
	assert.Equal(t, "unnamed file", slackFileDisplayName(nil))
	assert.Equal(t, "report.txt", slackFileDescriptor(&slack.File{Name: " report.txt "}))
	assert.Equal(t, "unnamed file", slackFileDescriptor(nil))
	assert.True(t, isSlackImageFile(&slack.File{Mimetype: " image/png "}))
	assert.False(t, isSlackImageFile(&slack.File{Mimetype: " application/pdf "}))
	assert.False(t, isSlackImageFile(nil))
	assert.True(t, events.IsTextAttachment("payload.json", "application/octet-stream"))
	assert.True(t, events.IsTextAttachment("report", "text/csv; charset=utf-8"))
	assert.False(t, events.IsTextAttachment("archive.zip", "application/zip"))
	data := mustPNG(t, 2, 2)
	assert.Equal(t, "image/png", normalizedSlackMIMEType(http.DetectContentType(data)))
	assert.Equal(t, "text/plain", normalizedSlackMIMEType(http.DetectContentType(nil)))
}

func TestSlackMessageEventHelpers(t *testing.T) {
	require.Empty(t, slackMessageEventText(nil))
	require.Empty(t, slackMessageEventFiles(nil))
	require.Empty(t, slackMessageEventFiles(&slackevents.MessageEvent{Message: &slack.Msg{}}))

	ev := &slackevents.MessageEvent{
		Text: " fallback ",
		Message: &slack.Msg{
			Text:  " primary ",
			Files: []slack.File{{ID: "F1", Name: "image.png"}},
		},
	}
	require.Equal(t, "primary", slackMessageEventText(ev))
	files := slackMessageEventFiles(ev)
	require.Equal(t, []slack.File{{ID: "F1", Name: "image.png"}}, files)
	files[0].Name = "changed"
	require.Equal(t, "image.png", ev.Message.Files[0].Name)

	ev.Message.Text = " "
	require.Equal(t, "fallback", slackMessageEventText(ev))
}

func TestSplitSlackResponseTextBoundaries(t *testing.T) {
	assert.Nil(t, splitSlackResponseText(""))
	assert.Equal(t, []string{"short"}, splitSlackResponseText("short"))

	withoutBoundary := strings.Repeat("x", slackTextLimit+3)
	chunks := splitSlackResponseText(withoutBoundary)
	require.Len(t, chunks, 2)
	assert.Len(t, []rune(chunks[0]), slackTextLimit)
	assert.Equal(t, "xxx", chunks[1])

	paragraphBoundary := strings.Repeat("a", slackPreferredChunkSize-3) + "\n\n" + strings.Repeat("b", slackTextLimit)
	chunks = splitSlackResponseText(paragraphBoundary)
	require.Len(t, chunks, 2)
	assert.True(t, strings.HasSuffix(chunks[0], "\n\n"))
	assert.Equal(t, strings.Repeat("b", slackTextLimit), chunks[1])

	assert.Equal(t, len("hello\n"), slackChunkBoundary([]rune("hello\nworld")))
	assert.Equal(t, len("hello "), slackChunkBoundary([]rune("hello world")))
	assert.Zero(t, slackChunkBoundary([]rune("helloworld")))

	lateBoundary := strings.Repeat("a", slackPreferredChunkSize) + " " + strings.Repeat("b", slackTextLimit)
	assert.Equal(t, slackPreferredChunkSize+1, slackChunkEnd([]rune(lateBoundary)))
}

func TestProgressTextMessageQuotesAndBoundsText(t *testing.T) {
	assert.Empty(t, slackThinkingMessage(slackImmediatePlaceholder, " \n\t "))
	assert.Equal(t, slackImmediatePlaceholder+"\n\n> beta\n> alpha", slackThinkingMessage(slackImmediatePlaceholder, " alpha\nbeta "))
	assert.Equal(t, slackGoalPlaceholder+"\n\n> beta\n> alpha", slackThinkingMessage(slackGoalPlaceholder, " alpha\nbeta "))

	got := slackThinkingMessage(slackImmediatePlaceholder, strings.Repeat("x", slackBlockTextLimit+20))
	assert.True(t, strings.HasPrefix(got, slackImmediatePlaceholder+"\n\n> "))
	assert.Less(t, len([]rune(got)), slackBlockTextLimit)
}

func TestNormalizeThreadAgentsRoutesLongestPrefix(t *testing.T) {
	agents := normalizeThreadAgents(config.ThreadAgents{
		" :bot: urgent ": {Agent: " urgent-agent ", PreSeed: true},
		":cat:":          {Agent: "cat-agent"},
		":bot:":          {Agent: "main-agent"},
		" ":              {Agent: "ignored"},
		":skip:":         {Agent: " \t "},
	})

	assert.Equal(t, []threadAgent{
		{prefix: ":bot: urgent", agent: "urgent-agent", preSeed: true},
		{prefix: ":bot:", agent: "main-agent"},
		{prefix: ":cat:", agent: "cat-agent"},
	}, agents)

	connector := &Connector{threadAgents: agents}
	agent, preSeed, promptText, ok := connector.threadAgentForText(" :bot: urgent fix production ")
	assert.True(t, ok)
	assert.True(t, preSeed)
	assert.Equal(t, "urgent-agent", agent)
	assert.Equal(t, "fix production", promptText)

	agent, preSeed, promptText, ok = connector.threadAgentForText("plain message")
	assert.False(t, ok)
	assert.Empty(t, agent)
	assert.False(t, preSeed)
	assert.Empty(t, promptText)
	assert.Nil(t, normalizeThreadAgents(config.ThreadAgents{" ": {Agent: " "}}))
}

func TestThreadAgentForTextMatchesUnicodeAndAliasPrefixes(t *testing.T) {
	connector := &Connector{threadAgents: normalizeThreadAgents(config.ThreadAgents{
		"🧵":         {Agent: "unicode-agent", PreSeed: true},
		":factory:": {Agent: "alias-agent"},
	})}

	agent, preSeed, promptText, ok := connector.threadAgentForText(":thread: fix production")
	assert.True(t, ok)
	assert.True(t, preSeed)
	assert.Equal(t, "unicode-agent", agent)
	assert.Equal(t, "fix production", promptText)

	agent, preSeed, promptText, ok = connector.threadAgentForText("🏭 plan buildout")
	assert.True(t, ok)
	assert.False(t, preSeed)
	assert.Equal(t, "alias-agent", agent)
	assert.Equal(t, "plan buildout", promptText)
}

func TestGoalRequestForTextParsesSupportedTriggers(t *testing.T) {
	tests := []struct {
		text        string
		objective   string
		checkScript string
		maxTurns    int
	}{
		{text: "🔁 write docs", objective: "write docs", maxTurns: 20},
		{text: "🏁 write docs", objective: "write docs", maxTurns: 20},
		{text: "🔁 maxTurns: 0 write docs", objective: "write docs", maxTurns: 0},
		{text: "🏁 maxTurns: infinite write docs", objective: "write docs", maxTurns: 0},
		{text: "🏁 checkScript: ./scripts/check.sh fix lint", objective: "fix lint", checkScript: "./scripts/check.sh", maxTurns: 20},
		{text: "🏁 maxTurns: 7 checkScript: \"./scripts/check.sh --linter-mode\" fix lint", objective: "fix lint", checkScript: "./scripts/check.sh --linter-mode", maxTurns: 7},
		{text: `🏁 checkScript: "./scripts/check.sh \literal" fix lint`, objective: "fix lint", checkScript: `./scripts/check.sh \literal`, maxTurns: 20},
		{text: "🏁 fix literal checkScript: ./scripts/check.sh text", objective: "fix literal checkScript: ./scripts/check.sh text", maxTurns: 20},
	}

	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			goal, rejection, ok := harnessbridge.ParseGoalRequest(tt.text)
			require.True(t, ok)
			require.Empty(t, rejection)
			assert.Equal(t, tt.objective, goal.Objective)
			assert.Equal(t, tt.checkScript, goal.CheckScript)
			assert.Equal(t, tt.maxTurns, goal.MaxTurns)
		})
	}
}

func TestSlackGoalParserTextNormalizesTransportEmojiPrefixes(t *testing.T) {
	tests := []struct {
		text      string
		objective string
	}{
		{text: "🔁 write docs", objective: "write docs"},
		{text: ":repeat: write docs", objective: "write docs"},
		{text: "🏁 do the same in more details", objective: "do the same in more details"},
		{text: ":checkered_flag: do the same in more details", objective: "do the same in more details"},
	}

	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			goal, rejection, ok := harnessbridge.ParseGoalRequest(emoji.CanonicalizeLeadingAlias(tt.text))
			require.True(t, ok)
			require.Empty(t, rejection)
			assert.Equal(t, tt.objective, goal.Objective)
		})
	}
}

func TestGoalRequestForTextRejectsMalformedRequests(t *testing.T) {
	tests := []string{
		"🔁",
		"🏁",
		"🔁 maxTurns:",
		"🏁 maxTurns: nope goal",
		"🏁 checkScript:",
		"🏁 checkScript: \"\" fix lint",
		"🏁 checkScript: \"./scripts/check.sh fix lint",
		`🏁 checkScript: "$(./scripts/check.sh)" fix lint`,
		"plain text",
	}

	for _, text := range tests {
		t.Run(text, func(t *testing.T) {
			_, rejection, ok := harnessbridge.ParseGoalRequest(text)
			if text == "plain text" {
				assert.False(t, ok)
				assert.Empty(t, rejection)

				return
			}

			require.True(t, ok)
			assert.NotEmpty(t, rejection)
		})
	}
}

func TestRemoveReactionSkipsInvalidTargetsAndIgnoresNoReaction(t *testing.T) {
	var calls []url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/reactions.remove", r.URL.Path)

		if err := r.ParseForm(); err != nil {
			t.Errorf("parse reactions.remove form: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		calls = append(calls, cloneValues(r.PostForm))

		if len(calls) == 1 {
			writeJSON(t, w, map[string]any{"ok": false, "error": "no_reaction"})
			return
		}

		writeJSON(t, w, map[string]any{"ok": false, "error": "ratelimited"})
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	connector.removeReaction(t.Context(), nil, "eyes", "remove reaction")
	connector.removeReaction(t.Context(), &events.SlackReplyTarget{ChannelID: " ", MessageTS: "111.222"}, "eyes", "remove reaction")
	connector.removeReaction(t.Context(), &events.SlackReplyTarget{ChannelID: "D123", MessageTS: " "}, "eyes", "remove reaction")
	assert.Empty(t, calls)

	connector.removeReaction(t.Context(), &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222"}, "eyes", "remove reaction")
	connector.removeReaction(t.Context(), &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "333.444"}, "robot_face", "remove reaction")

	require.Len(t, calls, 2)
	assert.Equal(t, "eyes", calls[0].Get("name"))
	assert.Equal(t, "111.222", calls[0].Get("timestamp"))
	assert.Equal(t, "robot_face", calls[1].Get("name"))
	assert.Equal(t, "333.444", calls[1].Get("timestamp"))
}

func TestNewConnectorInstallsInertRuntimeDependencies(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	c := New(&config.SlackConfig{BotToken: "xoxb-test", AppToken: "xapp-test"}, bus, nil, nil, inertThreadRouter{}, inertOneOffCronjobs{}, func() *events.InboundMessage { return nil }, testLogger())

	target := events.TextConversationTarget{ChannelID: "D123", MessageID: "111.222", ThreadID: "111.222"}
	handled, err := c.threadRouter.PrepareThreadReply(target)
	require.NoError(t, err)
	assert.False(t, handled)
	handled, err = c.threadRouter.PrepareResponseThreadReply(target)
	require.NoError(t, err)
	assert.False(t, handled)
	handled, err = c.threadRouter.SubmitThreadReply(t.Context(), target, events.NewMainInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true))
	require.NoError(t, err)
	assert.False(t, handled)
	handled, err = c.threadRouter.SubmitResponseThreadReply(t.Context(), target, events.NewMainInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true))
	require.NoError(t, err)
	assert.False(t, handled)
	handled, err = c.threadRouter.SummarizeThread(t.Context(), target)
	require.NoError(t, err)
	assert.False(t, handled)
	require.NoError(t, c.threadRouter.RecordResponseCheckpoint(target, events.ResponseCheckpoint{}))
	require.Error(t, c.threadRouter.StartThread(t.Context(), "main", false, target, events.NewMainInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)))

	_, err = c.oneOffCronjobs.LoadOneOffCronjob("daily")
	require.Error(t, err)

	finished := make(chan error, 1)

	c.oneOffCronjobs.RunOneOffCronjob(t.Context(), cronjob.OneOffCronjob{}, nil, func(_ context.Context, _ cronjob.RunResult, err error) { finished <- err })
	require.Error(t, <-finished)
}

func TestInboundContentDownloadsSlackTextFilesIntoPromptText(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage", "/reactions.add":
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.666"})
		case "/payload.json":
			_, err := w.Write([]byte(`{"ok":true,"rows":[1,2]}`))
			assert.NoError(t, err)
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, bus, nil, nil, nil)
	ev := newSlackMessageEvent("171234.5678", "", "please read this")
	ev.Message = &slack.Msg{Text: "please read this"}
	ev.Message.Files = []slack.File{{Name: "payload.json", Mimetype: "application/json", Size: len(`{"ok":true,"rows":[1,2]}`), URLPrivateDownload: server.URL + "/payload.json"}}

	connector.handleMessageEvent(context.Background(), ev)

	inbound := readOneInbound(t, bus)

	assert.False(t, inbound.HadNonImageAttachments)
	assert.Empty(t, inbound.AttachmentWarnings)
	assert.Contains(t, inbound.Text, "please read this\n\nSlack text file attachment payload.json (application/json):\n")
	assert.Contains(t, inbound.Text, `{"ok":true,"rows":[1,2]}`)

	inbound = newSlackInboundMessage("body", &events.InboundContent{TextAttachments: []string{"Slack text file attachment data.csv:\na,b"}, HadNonImageAttachments: true}, nil)
	assert.False(t, inbound.HadNonImageAttachments)
	assert.Contains(t, inbound.Text, "data.csv")
}

func TestNewSlackInboundMessageCopiesAttachments(t *testing.T) {
	content := &events.InboundContent{
		TextAttachments: []string{"Slack text file attachment notes.txt:\nhello"},
		Attachments:     []events.InboundAttachment{{Name: "photo.png", MIMEType: "image/png", Data: []byte("image")}},
	}
	replyTarget := &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: "333.444"}

	inbound := newSlackInboundMessage(" ", content, replyTarget)
	content.Attachments[0].Data[0] = 'X'
	replyTarget.ThreadTS = "changed"

	assert.Equal(t, "Slack text file attachment notes.txt:\nhello", inbound.Text)
	require.Len(t, inbound.Attachments, 1)
	assert.Equal(t, events.InboundAttachment{Name: "photo.png", MIMEType: "image/png", Data: []byte("image")}, inbound.Attachments[0])
	require.NotNil(t, inbound.SlackReply)
	assert.Equal(t, "333.444", inbound.SlackReply.ThreadTS)
}

func TestDownloadSlackAttachmentsDownloadsImageFilesAsAttachments(t *testing.T) {
	imageData := mustPNG(t, 2, 2)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/photo.png":
			w.Header().Set("Content-Type", "image/png")
			_, err := w.Write(imageData)
			assert.NoError(t, err)
		case "/not-image.png":
			_, err := w.Write([]byte("not an image"))
			assert.NoError(t, err)
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	files := []slack.File{
		{Name: "photo.png", Mimetype: "image/png", Size: len(imageData), URLPrivateDownload: server.URL + "/photo.png"},
		{Name: "not-image.png", Mimetype: "image/png", Size: len("not an image"), URLPrivateDownload: server.URL + "/not-image.png"},
	}

	attachments, textAttachments, hadAttachments, hadNonImageAttachments, warnings := connector.downloadSlackAttachments(context.Background(), files)

	require.Len(t, attachments, 2)
	assert.Equal(t, "photo.png", attachments[0].Name)
	assert.Equal(t, "image/png", attachments[0].MIMEType)
	assert.Equal(t, imageData, attachments[0].Data)
	assert.Equal(t, "not-image.png", attachments[1].Name)
	assert.Equal(t, "image/png", attachments[1].MIMEType)
	assert.Equal(t, []byte("not an image"), attachments[1].Data)
	assert.Empty(t, textAttachments)
	assert.True(t, hadAttachments)
	assert.False(t, hadNonImageAttachments)
	assert.Empty(t, warnings)
}

func TestDownloadSlackAttachmentsReportsSkippedAttachments(t *testing.T) {
	connector := &Connector{log: testLogger()}
	files := []slack.File{
		{Name: "doc.pdf", Mimetype: "application/pdf"},
		{Name: "payload", Mimetype: "application/json", Size: 12},
		{Name: "large.txt", Mimetype: "text/plain", Size: events.MaxInboundTextAttachmentBytes + 1},
		{Name: "missing.txt", Mimetype: "text/plain", Size: 12},
		{Name: "anim.gif", Mimetype: "image/gif"},
		{Name: "huge.png", Mimetype: "image/png", Size: maxSlackImageDownloadBytes + 1},
		{Name: "missing.png", Mimetype: "image/png", Size: 12},
	}

	attachments, textAttachments, hadAttachments, hadNonImageAttachments, warnings := connector.downloadSlackAttachments(context.Background(), files)

	assert.Empty(t, attachments)
	assert.Empty(t, textAttachments)
	assert.True(t, hadAttachments)
	assert.True(t, hadNonImageAttachments)
	assert.Equal(t, []string{
		"Skipped Slack attachment doc.pdf (application/pdf) because it is not an image.",
		"Skipped Slack text attachment payload (application/json) because Slack did not provide a download URL.",
		"Skipped Slack text attachment large.txt (text/plain) because it exceeded the text file size limit.",
		"Skipped Slack text attachment missing.txt (text/plain) because Slack did not provide a download URL.",
		"Skipped Slack attachment anim.gif (image/gif) because Slack did not provide a download URL.",
		"Skipped Slack attachment huge.png (image/png) because it exceeded the Slack attachment download limit.",
		"Skipped Slack attachment missing.png (image/png) because Slack did not provide a download URL.",
	}, warnings)
}

func TestDownloadSlackAttachmentsReportsDownloadAndContentFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/invalid.txt":
			_, err := w.Write([]byte{0xff})
			assert.NoError(t, err)
		case "/empty.txt":
			_, err := w.Write([]byte(" \n\t "))
			assert.NoError(t, err)
		case "/huge.txt":
			_, err := w.Write(bytes.Repeat([]byte("x"), events.MaxInboundTextAttachmentBytes+1))
			assert.NoError(t, err)
		case "/empty.png":
		case "/failed.txt", "/failed.png":
			http.Error(w, "failed", http.StatusInternalServerError)
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	files := []slack.File{
		{Name: "invalid.txt", Mimetype: "text/plain", Size: 1, URLPrivateDownload: server.URL + "/invalid.txt"},
		{Name: "empty.txt", Mimetype: "text/plain", Size: 4, URLPrivateDownload: server.URL + "/empty.txt"},
		{Name: "huge.txt", Mimetype: "text/plain", Size: 1, URLPrivateDownload: server.URL + "/huge.txt"},
		{Name: "failed.txt", Mimetype: "text/plain", Size: 1, URLPrivateDownload: server.URL + "/failed.txt"},
		{Name: "empty.png", Mimetype: "image/png", Size: 1, URLPrivateDownload: server.URL + "/empty.png"},
		{Name: "failed.png", Mimetype: "image/png", Size: 1, URLPrivateDownload: server.URL + "/failed.png"},
	}

	attachments, textAttachments, hadAttachments, hadNonImageAttachments, warnings := connector.downloadSlackAttachments(context.Background(), files)

	assert.Empty(t, attachments)
	assert.Empty(t, textAttachments)
	assert.True(t, hadAttachments)
	assert.False(t, hadNonImageAttachments)
	assert.Equal(t, []string{
		"Skipped Slack text attachment invalid.txt (text/plain) because Slack returned non-UTF-8 text data.",
		"Skipped Slack text attachment empty.txt (text/plain) because Slack returned empty text data.",
		"Skipped Slack text attachment huge.txt (text/plain) because it exceeded the text file size limit.",
		"Skipped Slack text attachment failed.txt (text/plain) because downloading it from Slack failed.",
		"Skipped Slack attachment empty.png (image/png) because Slack returned empty attachment data.",
		"Skipped Slack attachment failed.png (image/png) because downloading it from Slack failed.",
	}, warnings)
}

func TestSocketLoopRecreatesClientAndKeepsStableEventChannel(t *testing.T) {
	connector := newTestConnector("http://slack.test")
	connector.reconnectDelay = 0

	clients := make(chan *socketmode.Client, 2)
	releases := make(chan struct{})
	connector.newSocketClient = func(api *slack.Client) *socketmode.Client {
		client := socketmode.New(api)
		clients <- client

		return client
	}

	errStale := errors.New("stale socket")
	connector.runSocketClient = func(ctx context.Context, client *socketmode.Client) error {
		client.Events <- socketmode.Event{Type: socketmode.EventTypeConnecting}

		select {
		case <-releases:
			return errStale
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go connector.runSocketLoop(ctx)

	firstClient := <-clients
	firstEvent := <-connector.socketEvents

	releases <- struct{}{}

	secondClient := <-clients
	secondEvent := <-connector.socketEvents

	cancel()

	require.NotSame(t, firstClient, secondClient)
	assert.Equal(t, socketmode.EventTypeConnecting, firstEvent.event.Type)
	assert.Equal(t, socketmode.EventTypeConnecting, secondEvent.event.Type)
}

func TestStopCancelsSocketLoop(t *testing.T) {
	connector := newTestConnector("http://slack.test")
	connector.reconnectDelay = 0

	inboundCtx, inboundStop := context.WithCancel(context.Background())
	connector.inboundStop = inboundStop

	started := make(chan struct{})
	done := make(chan struct{})
	connector.runSocketClient = func(ctx context.Context, _ *socketmode.Client) error {
		close(started)
		<-ctx.Done()
		close(done)

		return ctx.Err()
	}

	go connector.runSocketLoop(inboundCtx)

	<-started
	require.NoError(t, connector.Stop(context.Background()))

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Slack socket loop was not canceled")
	}
}

func TestStopBeforeStart(t *testing.T) {
	connector := newTestConnector("http://slack.test")
	require.NoError(t, connector.Stop(context.Background()))
}

func TestStartStopCancelsInboundContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth.test":
			writeJSON(t, w, map[string]any{"ok": true, "user_id": "UBOT"})
		case "/users.profile.get":
			writeJSON(t, w, map[string]any{"ok": true, "profile": map[string]any{"display_name": "human", "image_72": "https://example.com/avatar.png"}})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	started := make(chan struct{})
	done := make(chan struct{})
	connector.runSocketClient = func(ctx context.Context, _ *socketmode.Client) error {
		close(started)
		<-ctx.Done()
		close(done)

		return ctx.Err()
	}

	require.NoError(t, connector.Start(context.Background()))
	<-started
	require.NoError(t, connector.Stop(context.Background()))

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Slack Start context was not canceled")
	}
}

func TestSocketLoopRecreatesWhenStableEventChannelIsFull(t *testing.T) {
	connector := newTestConnector("http://slack.test")

	connector.socketEvents = make(chan slackSocketEvent, 1)
	connector.socketEvents <- slackSocketEvent{}

	connector.reconnectDelay = 0

	clients := make(chan *socketmode.Client, 2)
	release := make(chan struct{})
	sentEvent := make(chan struct{}, 1)
	connector.newSocketClient = func(api *slack.Client) *socketmode.Client {
		client := socketmode.New(api)
		clients <- client

		return client
	}

	errStale := errors.New("stale socket")
	connector.runSocketClient = func(ctx context.Context, client *socketmode.Client) error {
		select {
		case client.Events <- socketmode.Event{Type: socketmode.EventTypeConnecting}:
		case <-ctx.Done():
			return ctx.Err()
		}

		select {
		case sentEvent <- struct{}{}:
		default:
		}

		select {
		case <-release:
			return errStale
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	ctx := t.Context()

	go connector.runSocketLoop(ctx)

	firstClient := <-clients

	<-sentEvent

	release <- struct{}{}

	select {
	case secondClient := <-clients:
		require.NotSame(t, firstClient, secondClient)
	case <-time.After(time.Second):
		t.Fatal("socket loop did not recreate client while stable event channel was full")
	}
}

func TestSocketLoopRecreatesWhenClientEventChannelCloses(t *testing.T) {
	connector := newTestConnector("http://slack.test")
	connector.reconnectDelay = 0

	clients := make(chan *socketmode.Client, 2)
	created := 0
	connector.newSocketClient = func(api *slack.Client) *socketmode.Client {
		client := socketmode.New(api)

		created++
		if created == 1 {
			close(client.Events)
		}

		clients <- client

		return client
	}
	connector.runSocketClient = func(ctx context.Context, _ *socketmode.Client) error {
		<-ctx.Done()
		return ctx.Err()
	}

	ctx := t.Context()

	go connector.runSocketLoop(ctx)

	firstClient := <-clients
	select {
	case secondClient := <-clients:
		require.NotSame(t, firstClient, secondClient)
	case <-time.After(time.Second):
		t.Fatal("socket loop did not recreate client after client event channel closed")
	}

	select {
	case event := <-connector.socketEvents:
		t.Fatalf("socketEvents received %v; want no zero-value event from closed client channel", event.event.Type)
	default:
	}
}

func TestSocketLoopAcksEventsAPIBeforeEnqueue(t *testing.T) {
	connector := newTestConnector("http://slack.test")

	connector.socketEvents = make(chan slackSocketEvent, 1)
	connector.socketEvents <- slackSocketEvent{}

	acked := make(chan string, 1)
	ackSeen := make(chan struct{})
	release := make(chan struct{})
	connector.reconnectDelay = 0
	errStale := errors.New("stale socket")
	sent := false
	connector.runSocketClient = func(ctx context.Context, client *socketmode.Client) error {
		if sent {
			<-ctx.Done()

			return ctx.Err()
		}

		sent = true

		client.Events <- socketmode.Event{
			Type:    socketmode.EventTypeEventsAPI,
			Request: &socketmode.Request{EnvelopeID: "blocked"},
			Data:    slackevents.EventsAPIEvent{},
		}

		<-ackSeen
		<-release

		return errStale
	}

	connector.ackSocketEvent = func(_ *socketmode.Client, req socketmode.Request) error {
		acked <- req.EnvelopeID

		close(ackSeen)

		return nil
	}

	ctx := t.Context()

	go connector.runSocketLoop(ctx)

	select {
	case envelopeID := <-acked:
		assert.Equal(t, "blocked", envelopeID)
	case <-time.After(time.Second):
		t.Fatal("socket loop did not ack Events API request while socketEvents was full")
	}

	<-connector.socketEvents

	select {
	case socketEvent := <-connector.socketEvents:
		assert.Equal(t, "blocked", socketEvent.event.Request.EnvelopeID)
		close(release)
	case <-time.After(time.Second):
		t.Fatal("socket loop did not enqueue Events API request after socketEvents was drained")
	}
}

func TestSocketLoopEnqueuesEventsAPIWhenAckFails(t *testing.T) {
	connector := newTestConnector("http://slack.test")
	connector.reconnectDelay = 0
	connector.runSocketClient = func(ctx context.Context, client *socketmode.Client) error {
		client.Events <- socketmode.Event{
			Type:    socketmode.EventTypeEventsAPI,
			Request: &socketmode.Request{EnvelopeID: "ack-failed"},
			Data:    slackevents.EventsAPIEvent{},
		}

		<-ctx.Done()

		return ctx.Err()
	}

	errAck := errors.New("ack failed")
	connector.ackSocketEvent = func(_ *socketmode.Client, _ socketmode.Request) error {
		return errAck
	}

	ctx := t.Context()

	go connector.runSocketLoop(ctx)

	select {
	case socketEvent := <-connector.socketEvents:
		assert.Equal(t, "ack-failed", socketEvent.event.Request.EnvelopeID)
	case <-time.After(time.Second):
		t.Fatal("socket loop did not enqueue Events API request after ack failure")
	}
}

func TestHandleEventsAPIIgnoresUnknownEventData(_ *testing.T) {
	connector := newTestConnector("http://slack.test")

	connector.handleEventsAPI(context.Background(), socketmode.Event{Data: "not events api"})
	connector.handleEventsAPI(context.Background(), socketmode.Event{Data: slackevents.EventsAPIEvent{}})
}

func TestEventLoopRoutesEventsAPI(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var (
		posted    []url.Values
		reactions []string
	)

	server := newSlackStackTestServer(t, &posted, &reactions)
	defer server.Close()

	connector := newTestConnector(server.URL)
	connector.bus = bus

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		connector.eventLoop(ctx)
		close(done)
	}()

	connector.socketEvents <- slackSocketEvent{event: socketmode.Event{Type: socketmode.EventTypeConnecting, Request: &socketmode.Request{EnvelopeID: "ignored"}}}

	event := newSlackEventsAPIEvent(newTestSlackMessageEvent())

	event.Type = socketmode.EventTypeEventsAPI
	connector.socketEvents <- slackSocketEvent{event: event}

	inbound := readOneInbound(t, bus)
	assert.Equal(t, "hello.", inbound.Text)
	require.Len(t, posted, 2)
	assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Slack event loop did not stop")
	}
}

func TestHandleMessageEventIgnoresUnroutableMessages(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	connector := newTestConnectorWithOptions("http://slack.test", bus, nil, nil, nil)
	connector.botUserID = "UBOT"

	cases := []struct {
		name  string
		event *slackevents.MessageEvent
	}{
		{name: "nil event", event: nil},
		{name: "empty user", event: &slackevents.MessageEvent{Channel: "D123", Text: "hello"}},
		{name: "bot user", event: &slackevents.MessageEvent{User: "UBOT", Channel: "D123", Text: "hello"}},
		{name: "bot message", event: &slackevents.MessageEvent{User: "U123", BotID: "B123", Channel: "D123", Text: "hello"}},
		{name: "unsupported subtype", event: &slackevents.MessageEvent{User: "U123", Channel: "D123", SubType: "message_changed", Text: "hello"}},
		{name: "not dm", event: &slackevents.MessageEvent{User: "U123", Channel: "C123", Text: "hello"}},
		{name: "empty content", event: &slackevents.MessageEvent{User: "U123", Channel: "D123", Text: " \t\n "}},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(_ *testing.T) {
			connector.handleMessageEvent(context.Background(), tt.event)
		})
	}

	bus.StopInbound()

	for inbound := range bus.Inbound(context.Background()) {
		t.Fatalf("unexpected inbound message %q", inbound.Text)
	}
}

func TestLimitedBufferStopsAtLimit(t *testing.T) {
	b := &limitedBuffer{limit: 5}

	n, err := b.Write([]byte("abc"))
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Equal(t, []byte("abc"), b.Bytes())

	n, err = b.Write([]byte("def"))
	require.ErrorIs(t, err, errSlackDownloadLimitExceeded)
	assert.Equal(t, 2, n)
	assert.Equal(t, []byte("abcde"), b.Bytes())

	n, err = b.Write([]byte("g"))
	require.ErrorIs(t, err, errSlackDownloadLimitExceeded)
	assert.Zero(t, n)
	assert.Equal(t, []byte("abcde"), b.Bytes())
}

func mustPNG(t *testing.T, width, height int) []byte {
	t.Helper()

	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, color.NRGBA{R: uint8(x*31 + y*17), G: uint8(x*13 + y*29), B: uint8(x*7 + y*19), A: 255})
		}
	}

	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, img))

	return b.Bytes()
}

func TestSendDiscordRelay(t *testing.T) {
	tests := []struct {
		name        string
		cached      *humanProfileSnapshot
		wantText    string
		wantContain []string
		wantUser    string
		wantIconURL string
	}{
		{
			name:        "uses customized authorship",
			cached:      &humanProfileSnapshot{DisplayName: "Hally", IconURL: "https://example.com/avatar.png"},
			wantText:    "hello from Discord",
			wantContain: nil,
			wantUser:    "Hally",
			wantIconURL: "https://example.com/avatar.png",
		},
		{
			name:        "falls back to quoted message",
			cached:      nil,
			wantText:    "",
			wantContain: []string{"Discord utterance:", "> hello from Discord"},
			wantUser:    "",
			wantIconURL: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var posted []url.Values

			var reacted []url.Values

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/users.profile.get":
					http.Error(w, `{"ok":false,"error":"missing_scope"}`, http.StatusForbidden)
				case "/chat.postMessage":
					if !assert.NoError(t, r.ParseForm()) {
						return
					}

					posted = append(posted, cloneValues(r.PostForm))
					writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "123.456", "text": posted[len(posted)-1].Get("text")})
				case "/reactions.add":
					if !assert.NoError(t, r.ParseForm()) {
						return
					}

					reacted = append(reacted, cloneValues(r.PostForm))

					writeJSON(t, w, map[string]any{"ok": true})
				default:
					assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
					return
				}
			}))
			defer server.Close()

			connector := newTestConnector(server.URL)
			connector.humanProfile = tt.cached
			replyTarget, err := connector.SendDiscordRelay(context.Background(), "hello from Discord")
			require.NoError(t, err)

			require.Len(t, posted, 3)
			assert.Equal(t, "D123", posted[0].Get("channel"))
			require.NotNil(t, replyTarget)
			assert.Equal(t, "D123", replyTarget.ChannelID)
			assert.Equal(t, "123.456", replyTarget.MessageTS)
			require.Len(t, reacted, 2)
			assert.ElementsMatch(t, []string{slackRobotReaction, slackDiscordRelayReaction}, []string{reacted[0].Get("name"), reacted[1].Get("name")})

			for _, reaction := range reacted {
				assert.Equal(t, "D123", reaction.Get("channel"))
				assert.Equal(t, "123.456", reaction.Get("timestamp"))
			}

			if tt.wantText != "" {
				assert.Equal(t, tt.wantText, posted[0].Get("text"))
			}

			for _, want := range tt.wantContain {
				assert.Contains(t, posted[0].Get("text"), want)
			}

			assert.Equal(t, tt.wantUser, posted[0].Get("username"))
			assert.Equal(t, tt.wantIconURL, posted[0].Get("icon_url"))
			assert.Equal(t, slackImmediatePlaceholder, posted[1].Get("text"))
			assert.Equal(t, slackAnswerPlaceholder, posted[2].Get("text"))
		})
	}
}

func TestSendDiscordRelayEdges(t *testing.T) {
	t.Run("empty transcription is silent", func(t *testing.T) {
		connector := newTestConnector("http://127.0.0.1:1")
		replyTarget, err := connector.SendDiscordRelay(t.Context(), " \n\t ")
		require.NoError(t, err)
		assert.Nil(t, replyTarget)
	})

	t.Run("post failure is reported", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/chat.postMessage":
				writeJSON(t, w, map[string]any{"ok": false, "error": "ratelimited"})
			default:
				assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
			}
		}))
		defer server.Close()

		connector := newTestConnector(server.URL)
		connector.humanProfile = &humanProfileSnapshot{DisplayName: "Hally"}
		replyTarget, err := connector.SendDiscordRelay(t.Context(), "hello from Discord")
		require.ErrorContains(t, err, "send Slack Discord relay")
		assert.Nil(t, replyTarget)
	})

	t.Run("placeholder failure is reported", func(t *testing.T) {
		posts := 0

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/chat.postMessage":
				posts++
				if posts == 2 {
					writeJSON(t, w, map[string]any{"ok": false, "error": "ratelimited"})
					return
				}

				writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "123.456"})
			default:
				assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
			}
		}))
		defer server.Close()

		connector := newTestConnector(server.URL)
		connector.humanProfile = &humanProfileSnapshot{DisplayName: "Hally"}
		replyTarget, err := connector.SendDiscordRelay(t.Context(), "hello from Discord")
		require.ErrorContains(t, err, "post Slack thinking placeholder")
		assert.Nil(t, replyTarget)
	})
}

func TestSendExternalMCPThreadRelay(t *testing.T) {
	var posted []url.Values

	var reacted []url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": fmt.Sprintf("222.%d", len(posted)), "text": posted[len(posted)-1].Get("text")})
		case "/reactions.add":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			reacted = append(reacted, cloneValues(r.PostForm))

			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	replyTarget, err := connector.SendExternalMCPThreadRelay(context.Background(), "D123", "123.456", "follow up", nil)
	require.NoError(t, err)
	require.NotNil(t, replyTarget)
	assert.Equal(t, events.SlackReplyTarget{ChannelID: "D123", MessageTS: "222.1", ThreadTS: "123.456"}, *replyTarget)
	require.Len(t, posted, 3)
	assert.Equal(t, "D123", posted[0].Get("channel"))
	assert.Equal(t, "follow up", posted[0].Get("text"))
	assert.Equal(t, "123.456", posted[0].Get("thread_ts"))
	assert.Equal(t, slackImmediatePlaceholder, posted[1].Get("text"))
	assert.Equal(t, "123.456", posted[1].Get("thread_ts"))
	assert.Equal(t, slackAnswerPlaceholder, posted[2].Get("text"))
	assert.Equal(t, "123.456", posted[2].Get("thread_ts"))
	require.Len(t, reacted, 2)
	assert.ElementsMatch(t, []string{slackRobotReaction, slackExternalMCPRelayReaction}, []string{reacted[0].Get("name"), reacted[1].Get("name")})

	for _, reaction := range reacted {
		assert.Equal(t, "D123", reaction.Get("channel"))
		assert.Equal(t, "222.1", reaction.Get("timestamp"))
	}
}

func TestSendDiscordRelaySucceedsWhenProvenanceReactionFails(t *testing.T) {
	var posted []url.Values

	var reactionNames []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users.profile.get":
			writeJSON(t, w, map[string]any{"ok": false, "error": "missing_scope"})
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "123.456", "text": posted[len(posted)-1].Get("text")})
		case "/reactions.add":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			reactionNames = append(reactionNames, r.PostForm.Get("name"))
			if len(reactionNames) == 2 {
				writeJSON(t, w, map[string]any{"ok": false, "error": "failed_to_add_reaction"})
				return
			}

			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	replyTarget, err := connector.SendDiscordRelay(context.Background(), "hello from Discord")
	require.NoError(t, err)
	require.NotNil(t, replyTarget)
	require.Len(t, posted, 3)
	assert.Equal(t, "D123", posted[0].Get("channel"))
	assert.Equal(t, slackImmediatePlaceholder, posted[1].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[2].Get("text"))
	assert.ElementsMatch(t, []string{slackRobotReaction, slackDiscordRelayReaction}, reactionNames)
}

func TestSendExternalMCPRelayCanPostTopLevelChannelRelay(t *testing.T) {
	var (
		uploadURL, completed          url.Values
		posted                        []url.Values
		uploadedName, uploadedContent string
	)

	var server *httptest.Server

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": posted[len(posted)-1].Get("channel"), "ts": fmt.Sprintf("123.%d", len(posted)), "text": posted[len(posted)-1].Get("text")})
		case "/files.getUploadURLExternal":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			uploadURL = cloneValues(r.PostForm)

			writeJSON(t, w, map[string]any{"ok": true, "upload_url": server.URL + "/upload", "file_id": "F123"})
		case "/upload":
			if !assert.NoError(t, r.ParseMultipartForm(1<<20)) {
				return
			}

			file, header, err := r.FormFile("file")
			if !assert.NoError(t, err) {
				return
			}

			defer func() { assert.NoError(t, file.Close()) }()

			data, err := io.ReadAll(file)
			if !assert.NoError(t, err) {
				return
			}

			uploadedName = header.Filename
			uploadedContent = string(data)

			writeJSON(t, w, map[string]any{"ok": true})
		case "/files.completeUploadExternal":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			completed = cloneValues(r.PostForm)

			writeJSON(t, w, map[string]any{"ok": true, "files": []map[string]string{{"id": "F123", "title": "red.png"}}})
		case "/reactions.add":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	replyTarget, err := connector.SendExternalMCPRelay(context.Background(), "#triage", "hello", []events.OutboundAttachment{{Name: "red.png", MIMEType: "image/png", Data: []byte("png")}})
	require.NoError(t, err)
	require.NotNil(t, replyTarget)
	require.Len(t, posted, 3)
	assert.Equal(t, "#triage", posted[0].Get("channel"))
	assert.Empty(t, posted[0].Get("thread_ts"))
	assert.Equal(t, "hello", posted[0].Get("text"))
	assert.Equal(t, slackImmediatePlaceholder, posted[1].Get("text"))
	assert.Equal(t, "123.1", posted[1].Get("thread_ts"))
	assert.Equal(t, slackAnswerPlaceholder, posted[2].Get("text"))
	assert.Equal(t, "123.1", posted[2].Get("thread_ts"))
	assert.Equal(t, "#triage", replyTarget.ChannelID)
	assert.Equal(t, "123.1", replyTarget.MessageTS)
	assert.Equal(t, "123.1", replyTarget.ThreadTS)
	assert.Equal(t, "red.png", uploadURL.Get("filename"))
	assert.Equal(t, "red.png", uploadedName)
	assert.Equal(t, "png", uploadedContent)
	assert.Equal(t, "#triage", completed.Get("channel_id"))
	assert.Equal(t, "123.1", completed.Get("thread_ts"))
}

func TestExternalMCPRelayUsesAnswerPlaceholderForStackedReply(t *testing.T) {
	server, posted, updated := newExternalMCPReplyServer(t)
	defer server.Close()

	connector := newTestConnector(server.URL)
	first, err := connector.SendExternalMCPThreadRelay(context.Background(), "D123", "111.222", "first", nil)
	require.NoError(t, err)
	second, err := connector.SendExternalMCPThreadRelay(context.Background(), "D123", "111.222", "second", nil)
	require.NoError(t, err)
	require.NotNil(t, second)

	final := events.NewMainOutboundMessage(events.SourceExternalMCP, "first answer", events.OutputTargetSlackMain)
	final.TurnID = "turn-1"
	final.Complete = true
	final.SlackReply = first
	require.NoError(t, connector.SendResponse(context.Background(), final))

	require.Len(t, *posted, 6)
	require.Len(t, *updated, 1)
	assert.Equal(t, "555.3", (*updated)[0].Get("ts"))
	assert.Equal(t, "first answer", (*updated)[0].Get("text"))
}

func TestExternalMCPRelayCreatesAnswerPlaceholderUpFront(t *testing.T) {
	server, posted, updated := newExternalMCPReplyServer(t)
	defer server.Close()

	connector := newTestConnector(server.URL)
	first, err := connector.SendExternalMCPThreadRelay(context.Background(), "D123", "111.222", "first", nil)
	require.NoError(t, err)
	require.Len(t, *posted, 3)
	assert.Equal(t, slackAnswerPlaceholder, (*posted)[2].Get("text"))

	thinking := events.NewMainOutboundMessage(events.SourceExternalMCP, "", events.OutputTargetSlackMain)
	thinking.TurnID = "turn-1"
	thinking.ProgressText = "working"
	thinking.SlackReply = first
	require.NoError(t, connector.SendResponse(context.Background(), thinking))

	_, err = connector.SendExternalMCPThreadRelay(context.Background(), "D123", "111.222", "second", nil)
	require.NoError(t, err)

	final := events.NewMainOutboundMessage(events.SourceExternalMCP, "first answer", events.OutputTargetSlackMain)
	final.TurnID = "turn-1"
	final.Complete = true
	final.SlackReply = first
	require.NoError(t, connector.SendResponse(context.Background(), final))

	require.Len(t, *posted, 6)
	require.Len(t, *updated, 1)
	assert.Equal(t, slackAnswerPlaceholder, (*posted)[2].Get("text"))
	assert.Equal(t, "555.3", (*updated)[0].Get("ts"))
	assert.Equal(t, "first answer", (*updated)[0].Get("text"))
}

func newExternalMCPReplyServer(t *testing.T) (server *httptest.Server, posted, updated *[]url.Values) {
	t.Helper()

	var postedValues, updatedValues []url.Values

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			postedValues = append(postedValues, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": fmt.Sprintf("555.%d", len(postedValues)), "text": postedValues[len(postedValues)-1].Get("text")})
		case "/chat.update":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			updatedValues = append(updatedValues, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": updatedValues[len(updatedValues)-1].Get("ts"), "text": updatedValues[len(updatedValues)-1].Get("text")})
		case "/reactions.add", "/reactions.remove", "/chat.delete":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))

	return server, &postedValues, &updatedValues
}

func TestExternalMCPRelayTailResponseUpdatesAnswerPlaceholder(t *testing.T) {
	server, posted, updated := newExternalMCPReplyServer(t)
	defer server.Close()

	connector := newTestConnector(server.URL)
	replyTarget, err := connector.SendExternalMCPThreadRelay(context.Background(), "D123", "111.222", "tail", nil)
	require.NoError(t, err)

	final := events.NewMainOutboundMessage(events.SourceExternalMCP, "tail answer", events.OutputTargetSlackMain)
	final.TurnID = "turn-1"
	final.Complete = true
	final.SlackReply = replyTarget
	require.NoError(t, connector.SendResponse(context.Background(), final))

	require.Len(t, *posted, 3)
	assert.Equal(t, "tail", (*posted)[0].Get("text"))
	assert.Equal(t, slackImmediatePlaceholder, (*posted)[1].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, (*posted)[2].Get("text"))
	require.Len(t, *updated, 1)
	assert.Equal(t, "555.3", (*updated)[0].Get("ts"))
	assert.Equal(t, "tail answer", (*updated)[0].Get("text"))
}

func TestExternalMCPRelayStackedTailResponseUpdatesAnswerPlaceholder(t *testing.T) {
	server, posted, updated := newExternalMCPReplyServer(t)
	defer server.Close()

	connector := newTestConnector(server.URL)
	_, err := connector.SendExternalMCPThreadRelay(context.Background(), "D123", "111.222", "first", nil)
	require.NoError(t, err)
	tail, err := connector.SendExternalMCPThreadRelay(context.Background(), "D123", "111.222", "second", nil)
	require.NoError(t, err)

	final := events.NewMainOutboundMessage(events.SourceExternalMCP, "second answer", events.OutputTargetSlackMain)
	final.TurnID = "turn-2"
	final.Complete = true
	final.SlackReply = tail
	require.NoError(t, connector.SendResponse(context.Background(), final))

	require.Len(t, *posted, 6)
	assert.Equal(t, slackAnswerPlaceholder, (*posted)[2].Get("text"))
	assert.Equal(t, "second", (*posted)[3].Get("text"))
	assert.Equal(t, slackImmediatePlaceholder, (*posted)[4].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, (*posted)[5].Get("text"))
	require.Len(t, *updated, 1)
	assert.Equal(t, "555.6", (*updated)[0].Get("ts"))
	assert.Equal(t, "second answer", (*updated)[0].Get("text"))
}

func TestExternalMCPRelaySerializesInputAndPlaceholders(t *testing.T) {
	var posted []url.Values

	firstThinkingPlaceholder := make(chan struct{})
	releaseFirstThinkingPlaceholder := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			if len(posted) == 2 {
				close(firstThinkingPlaceholder)
				<-releaseFirstThinkingPlaceholder
			}

			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": fmt.Sprintf("555.%d", len(posted)), "text": posted[len(posted)-1].Get("text")})
		case "/reactions.add":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	errFirst := make(chan error, 1)
	errSecond := make(chan error, 1)
	secondStarted := make(chan struct{})

	go func() {
		_, err := connector.SendExternalMCPThreadRelay(context.Background(), "D123", "111.222", "first", nil)
		errFirst <- err
	}()

	<-firstThinkingPlaceholder

	go func() {
		close(secondStarted)

		_, err := connector.SendExternalMCPThreadRelay(context.Background(), "D123", "111.222", "second", nil)
		errSecond <- err
	}()

	<-secondStarted

	if connector.mu.TryLock() {
		connector.mu.Unlock()
		close(releaseFirstThinkingPlaceholder)
		t.Fatal("first relay released placeholder serialization before first answer placeholder completed")
	}

	close(releaseFirstThinkingPlaceholder)
	require.NoError(t, <-errFirst)
	require.NoError(t, <-errSecond)

	require.Len(t, posted, 6)
	assert.Equal(t, "first", posted[0].Get("text"))
	assert.Equal(t, slackImmediatePlaceholder, posted[1].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[2].Get("text"))
	assert.Equal(t, "second", posted[3].Get("text"))
	assert.Equal(t, slackImmediatePlaceholder, posted[4].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[5].Get("text"))
}

func TestSendExternalMCPRelayReturnsPlaceholderError(t *testing.T) {
	posts := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			posts++
			if posts == 2 {
				writeJSON(t, w, map[string]any{"ok": false, "error": "ratelimited"})
				return
			}

			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": fmt.Sprintf("555.%d", posts)})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	replyTarget, err := connector.SendExternalMCPRelay(context.Background(), "D123", "hello", nil)
	require.ErrorContains(t, err, "post Slack thinking placeholder")
	assert.Nil(t, replyTarget)
}

func TestCleanupPendingReplyPlaceholderDeletesUnclaimedExternalMCPThinking(t *testing.T) {
	var deleted []url.Values

	posts := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			posts++
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": fmt.Sprintf("555.%d", posts)})
		case "/chat.delete":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			deleted = append(deleted, cloneValues(r.PostForm))

			writeJSON(t, w, map[string]any{"ok": true})
		case "/reactions.add":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	replyTarget, err := connector.SendExternalMCPRelay(context.Background(), "D123", "hello", nil)
	require.NoError(t, err)
	require.True(t, connector.hasPendingState(replyTarget))

	connector.CleanupPendingReplyPlaceholder(context.Background(), replyTarget)
	connector.CleanupPendingReplyPlaceholder(context.Background(), replyTarget)

	assert.False(t, connector.hasPendingState(replyTarget))
	require.Len(t, deleted, 2)
	assert.Equal(t, "555.3", deleted[0].Get("ts"))
	assert.Equal(t, "555.2", deleted[1].Get("ts"))
}

func TestReplyStateTracksPendingSlots(t *testing.T) {
	replyTarget := &events.SlackReplyTarget{ChannelID: " D123 ", MessageTS: " 111.222 ", ThreadTS: " 333.444 "}
	key := slackPendingKey(replyTarget)
	slots := slackReplySlots{ChannelID: "D123", ThinkingTS: "555.1", AnswerTS: "555.2", Key: key}
	connector := &Connector{pending: map[string]slackReplySlots{key: slots}}

	assert.Equal(t, "D123\x00111.222\x00333.444", key)
	assert.False(t, connector.hasPendingState(nil))
	assert.True(t, connector.hasPendingState(replyTarget))

	connector.setReplyState("turn-1", slots)
	assert.False(t, connector.hasPendingState(replyTarget))

	got, ok := connector.replyState("turn-1")
	require.True(t, ok)
	assert.Equal(t, "555.2", got.AnswerTS)

	claimed, ok := connector.claimPendingState(replyTarget)
	assert.False(t, ok)
	assert.Equal(t, slackReplySlots{}, claimed)

	connector.clearReplyState(" ")
	_, ok = connector.replyState("turn-1")
	assert.True(t, ok)

	connector.clearReplyState("turn-1")
	_, ok = connector.replyState("turn-1")
	assert.False(t, ok)
}

func TestSendExternalMCPRelayEdgeFailures(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		connector := newTestConnector("http://127.0.0.1:1")
		replyTarget, err := connector.SendExternalMCPRelay(context.Background(), "D123", " ", nil)
		require.NoError(t, err)
		assert.Nil(t, replyTarget)
	})

	t.Run("post", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/chat.postMessage", r.URL.Path)
			writeJSON(t, w, map[string]any{"ok": false, "error": "ratelimited"})
		}))
		defer server.Close()

		connector := newTestConnector(server.URL)
		replyTarget, err := connector.SendExternalMCPRelay(context.Background(), "D123", "hello", nil)
		require.ErrorContains(t, err, "send Slack external MCP relay")
		assert.Nil(t, replyTarget)
	})

	t.Run("attachment", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/chat.postMessage":
				writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.1"})
			case "/files.getUploadURLExternal":
				writeJSON(t, w, map[string]any{"ok": false, "error": "ratelimited"})
			default:
				assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
			}
		}))
		defer server.Close()

		connector := newTestConnector(server.URL)
		replyTarget, err := connector.SendExternalMCPRelay(context.Background(), "D123", "hello", []events.OutboundAttachment{{Name: "report.txt", Data: []byte("report")}})
		require.ErrorContains(t, err, "send Slack external MCP relay attachments")
		assert.Nil(t, replyTarget)
	})
}

func TestSendCronjobChannelThreadPostsRootThenThreadReply(t *testing.T) {
	var (
		posted               []url.Values
		uploadURL, completed url.Values
		uploadedContent      string
	)

	var server *httptest.Server

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))

			ts := "111.222"
			if len(posted) == 2 {
				ts = "333.444"
			}

			writeJSON(t, w, map[string]any{"ok": true, "channel": posted[len(posted)-1].Get("channel"), "ts": ts, "text": posted[len(posted)-1].Get("text")})
		case "/files.getUploadURLExternal":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			uploadURL = cloneValues(r.PostForm)

			writeJSON(t, w, map[string]any{"ok": true, "upload_url": server.URL + "/upload", "file_id": "F123"})
		case "/upload":
			if !assert.NoError(t, r.ParseMultipartForm(1<<20)) {
				return
			}

			file, _, err := r.FormFile("file")
			if !assert.NoError(t, err) {
				return
			}

			defer func() { assert.NoError(t, file.Close()) }()

			data, err := io.ReadAll(file)
			if !assert.NoError(t, err) {
				return
			}

			uploadedContent = string(data)

			writeJSON(t, w, map[string]any{"ok": true})
		case "/files.completeUploadExternal":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			completed = cloneValues(r.PostForm)

			writeJSON(t, w, map[string]any{"ok": true, "files": []map[string]string{{"id": "F123", "title": "report.txt"}}})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	router := newThreadRouterStub()
	connector := newTestConnectorWithOptions(server.URL, nil, nil, router, nil)
	require.NoError(t, connector.SendCronjobChannelThread(context.Background(), "#triage", "cron/daily.md", "planner", "2000-01-02T03:04:05Z", "final payload", []events.OutboundAttachment{{Name: "report.txt", Data: []byte("report body")}}))

	require.Len(t, posted, 2)
	assert.Equal(t, "#triage", posted[0].Get("channel"))
	assert.Contains(t, posted[0].Get("text"), "Cronjob `cron/daily.md` ran at `2000-01-02T03:04:05Z` with agent `planner`.")
	assert.Empty(t, posted[0].Get("thread_ts"))
	assert.Equal(t, "#triage", posted[1].Get("channel"))
	assert.Equal(t, "final payload", posted[1].Get("text"))
	assert.Equal(t, "111.222", posted[1].Get("thread_ts"))
	assert.Equal(t, "report.txt", uploadURL.Get("filename"))
	assert.Equal(t, "report body", uploadedContent)
	assert.Equal(t, "#triage", completed.Get("channel_id"))
	assert.Equal(t, "111.222", completed.Get("thread_ts"))

	registrations := router.cronRegistrationsSnapshot()
	require.Len(t, registrations, 1)
	assert.Equal(t, cronThreadRegistration{channelID: "#triage", threadTS: "111.222", agent: "planner", seedText: "Cronjob cron/daily.md ran at 2000-01-02T03:04:05Z with agent planner.\n\nHuman-visible cron output:\nfinal payload\n\nAttached files: report.txt."}, registrations[0])
}

func TestSendCronjobChannelThreadReportsSlackFailures(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/chat.postMessage", r.URL.Path)
			writeJSON(t, w, map[string]any{"ok": false, "error": "ratelimited"})
		}))
		defer server.Close()

		connector := newTestConnector(server.URL)
		err := connector.SendCronjobChannelThread(context.Background(), "#triage", "cron/daily.md", "planner", "2000-01-02T03:04:05Z", "final payload", nil)
		require.ErrorContains(t, err, "send Slack cronjob thread root")
	})

	t.Run("reply", func(t *testing.T) {
		posts := 0

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/chat.postMessage", r.URL.Path)

			posts++
			if posts == 2 {
				writeJSON(t, w, map[string]any{"ok": false, "error": "ratelimited"})
				return
			}

			writeJSON(t, w, map[string]any{"ok": true, "channel": "#triage", "ts": "111.222"})
		}))
		defer server.Close()

		connector := newTestConnector(server.URL)
		err := connector.SendCronjobChannelThread(context.Background(), "#triage", "cron/daily.md", "planner", "2000-01-02T03:04:05Z", "final payload", nil)
		require.ErrorContains(t, err, "send Slack cronjob thread reply")
		assert.Equal(t, 2, posts)
	})

	t.Run("attachments", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/chat.postMessage":
				writeJSON(t, w, map[string]any{"ok": true, "channel": "#triage", "ts": "111.222"})
			case "/files.getUploadURLExternal":
				writeJSON(t, w, map[string]any{"ok": false, "error": "ratelimited"})
			default:
				assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
			}
		}))
		defer server.Close()

		connector := newTestConnector(server.URL)
		err := connector.SendCronjobChannelThread(context.Background(), "#triage", "cron/daily.md", "planner", "2000-01-02T03:04:05Z", "", []events.OutboundAttachment{{Name: "report.txt", Data: []byte("report")}})
		require.ErrorContains(t, err, "send Slack cronjob thread attachments")
	})
}

func TestSendWebVoiceRelayUsesStudioMicrophoneReactionAndDiscordEquivalentText(t *testing.T) {
	var posted []url.Values

	var reactionNames []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users.profile.get":
			writeJSON(t, w, map[string]any{"ok": false, "error": "missing_scope"})
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "123.456", "text": posted[len(posted)-1].Get("text")})
		case "/reactions.add":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			reactionNames = append(reactionNames, r.PostForm.Get("name"))

			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	replyTarget, err := connector.SendWebVoiceRelay(context.Background(), "hello from browser")
	require.NoError(t, err)
	require.NotNil(t, replyTarget)
	require.Len(t, posted, 3)
	assert.Equal(t, "D123", posted[0].Get("channel"))
	assert.Contains(t, posted[0].Get("text"), "Discord utterance:")
	assert.NotContains(t, posted[0].Get("text"), "Browser voice utterance:")
	assert.Equal(t, slackImmediatePlaceholder, posted[1].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[2].Get("text"))
	assert.ElementsMatch(t, []string{slackRobotReaction, slackWebVoiceRelayReaction}, reactionNames)
}

func TestSendWebVoiceRelayUsesStudioMicrophoneReactionWithProfileDecoration(t *testing.T) {
	var posted []url.Values

	var reactionNames []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users.profile.get":
			writeJSON(t, w, map[string]any{
				"ok": true,
				"profile": map[string]any{
					"display_name": "Hally",
					"image_512":    "https://example.com/avatar.png",
				},
			})
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "123.456", "text": posted[len(posted)-1].Get("text")})
		case "/reactions.add":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			reactionNames = append(reactionNames, r.PostForm.Get("name"))

			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	replyTarget, err := connector.SendWebVoiceRelay(context.Background(), "hello from browser")
	require.NoError(t, err)
	require.NotNil(t, replyTarget)
	require.Len(t, posted, 3)
	assert.Equal(t, "Hally", posted[0].Get("username"))
	assert.Equal(t, "https://example.com/avatar.png", posted[0].Get("icon_url"))
	assert.Equal(t, "hello from browser", posted[0].Get("text"))
	assert.Equal(t, slackImmediatePlaceholder, posted[1].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[2].Get("text"))
	assert.ElementsMatch(t, []string{slackRobotReaction, slackWebVoiceRelayReaction}, reactionNames)
}

func TestSendWebVoiceRelayUsesProfileNameFallbacks(t *testing.T) {
	for _, tt := range []struct {
		name        string
		profile     map[string]any
		wantUser    string
		wantIconURL string
	}{
		{
			name: "real name",
			profile: map[string]any{
				"real_name": "Hally Real",
				"image_24":  "https://example.com/avatar-24.png",
			},
			wantUser:    "Hally Real",
			wantIconURL: "https://example.com/avatar-24.png",
		},
		{
			name:        "configured human user",
			profile:     map[string]any{},
			wantUser:    "U123",
			wantIconURL: "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var posted []url.Values

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/users.profile.get":
					writeJSON(t, w, map[string]any{"ok": true, "profile": tt.profile})
				case "/chat.postMessage":
					if !assert.NoError(t, r.ParseForm()) {
						return
					}

					posted = append(posted, cloneValues(r.PostForm))
					writeJSON(t, w, map[string]any{
						"ok":      true,
						"channel": "D123",
						"ts":      "123.456",
						"text":    posted[len(posted)-1].Get("text"),
					})
				case "/reactions.add":
					writeJSON(t, w, map[string]any{"ok": true})
				default:
					assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
				}
			}))
			defer server.Close()

			connector := newTestConnector(server.URL)
			replyTarget, err := connector.SendWebVoiceRelay(t.Context(), "hello from browser")
			require.NoError(t, err)
			require.NotNil(t, replyTarget)
			require.Len(t, posted, 3)
			assert.Equal(t, tt.wantUser, posted[0].Get("username"))
			assert.Equal(t, tt.wantIconURL, posted[0].Get("icon_url"))
			assert.Equal(t, "hello from browser", posted[0].Get("text"))
		})
	}
}

func TestSendResponseStreamsThinkingInPlaceThenReplacesItWithFinalAnswer(t *testing.T) {
	var posted, deleted, updated []url.Values

	removed := url.Values{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.666", "text": posted[len(posted)-1].Get("text")})
		case "/chat.update":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			updated = append(updated, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": updated[len(updated)-1].Get("ts"), "text": updated[len(updated)-1].Get("text")})
		case "/chat.delete":
			_ = r.ParseForm()
			deleted = append(deleted, cloneValues(r.PostForm))

			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": r.PostForm.Get("ts")})
		case "/reactions.remove":
			_ = r.ParseForm()
			removed = cloneValues(r.PostForm)

			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	first := events.NewMainOutboundMessage(events.SourceSlack, "", events.MainOutputTargets()...)
	first.TurnID = "turn-1"
	first.ProgressText = "first thought"
	first.SlackReply = &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: ""}
	require.NoError(t, connector.SendResponse(context.Background(), first))

	second := events.NewMainOutboundMessage(events.SourceSlack, "", events.MainOutputTargets()...)
	second.TurnID = "turn-1"
	second.ProgressText = "first thought\nsecond thought"
	second.SlackReply = &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: ""}
	require.NoError(t, connector.SendResponse(context.Background(), second))
	require.Empty(t, updated)
	require.NoError(t, connector.flushProgressText(context.Background(), "turn-1"))

	partial := events.NewMainOutboundMessage(events.SourceSlack, "Partial answer", events.MainOutputTargets()...)
	partial.TurnID = "turn-1"
	partial.SlackReply = &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: ""}
	require.NoError(t, connector.SendResponse(context.Background(), partial))

	final := events.NewMainOutboundMessage(events.SourceSlack, "Final answer", events.MainOutputTargets()...)
	final.TurnID = "turn-1"
	final.Complete = true
	final.SlackReply = &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: ""}
	require.NoError(t, connector.SendResponse(context.Background(), final))

	require.Len(t, posted, 2)
	require.Len(t, updated, 2)
	assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	assert.Equal(t, "_Thinking..._\n\n> second thought\n> first thought", updated[0].Get("text"))
	assert.Equal(t, updated[0].Get("text"), thinkingBlockText(t, updated[0]))
	assert.Equal(t, "Final answer", updated[1].Get("text"))
	assert.Equal(t, "111.222", removed.Get("timestamp"))
	require.Len(t, deleted, 1)
	assert.Equal(t, "555.666", deleted[0].Get("ts"))
}

func TestSendResponseClampsThinkingToSlackLimit(t *testing.T) {
	var posted, updated []url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			assert.Less(t, len([]rune(posted[len(posted)-1].Get("text"))), slackBlockTextLimit)
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.666", "text": posted[len(posted)-1].Get("text")})
		case "/chat.update":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			updated = append(updated, cloneValues(r.PostForm))
			assert.Less(t, len([]rune(updated[len(updated)-1].Get("text"))), slackBlockTextLimit)
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.666", "text": updated[len(updated)-1].Get("text")})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	first := events.NewMainOutboundMessage(events.SourceSlack, "", events.MainOutputTargets()...)
	first.TurnID = "turn-1"
	first.ProgressText = "brief thought"
	first.SlackReply = &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: ""}
	require.NoError(t, connector.SendResponse(context.Background(), first))

	second := events.NewMainOutboundMessage(events.SourceSlack, "", events.MainOutputTargets()...)
	second.TurnID = "turn-1"
	second.ProgressText = strings.Repeat("0123456789", 450) + "TAIL MARKER"
	second.SlackReply = &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: ""}
	require.NoError(t, connector.SendResponse(context.Background(), second))
	require.NoError(t, connector.flushProgressText(context.Background(), "turn-1"))

	require.Len(t, posted, 2)
	require.Len(t, updated, 1)
	assert.Contains(t, updated[0].Get("text"), "TAIL MARKER")
	assert.True(t, strings.HasPrefix(updated[0].Get("text"), slackImmediatePlaceholder+"\n\n> "))
	assert.Equal(t, updated[0].Get("text"), thinkingBlockText(t, updated[0]))
}

func TestSendResponseUsesGoalPlaceholderForGoalProgress(t *testing.T) {
	var posted, updated []url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.666", "text": posted[len(posted)-1].Get("text")})
		case "/chat.update":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			updated = append(updated, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.666", "text": updated[len(updated)-1].Get("text")})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	first := events.NewMainOutboundMessage(events.SourceSlack, "", events.MainOutputTargets()...)
	first.TurnID = "turn-1"
	first.ProgressText = "first thought"
	first.GoalTurn = true
	first.SlackReply = &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: ""}
	require.NoError(t, connector.SendResponse(context.Background(), first))

	second := events.NewMainOutboundMessage(events.SourceSlack, "", events.MainOutputTargets()...)
	second.TurnID = "turn-1"
	second.ProgressText = "first thought\nsecond thought"
	second.GoalTurn = true
	second.SlackReply = &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: ""}
	require.NoError(t, connector.SendResponse(context.Background(), second))
	require.NoError(t, connector.flushProgressText(context.Background(), "turn-1"))

	require.Len(t, posted, 2)
	require.Len(t, updated, 1)
	assert.Equal(t, slackGoalPlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	assert.Equal(t, slackGoalPlaceholder+"\n\n> second thought\n> first thought", updated[0].Get("text"))
	assert.Equal(t, updated[0].Get("text"), thinkingBlockText(t, updated[0]))
}

func thinkingBlockText(t *testing.T, values url.Values) string {
	t.Helper()

	var blocks []struct {
		Type string `json:"type"`
		Text struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"text"`
	}

	require.NoError(t, json.Unmarshal([]byte(values.Get("blocks")), &blocks))
	require.Len(t, blocks, 1)
	assert.Equal(t, "section", blocks[0].Type)
	assert.Equal(t, "mrkdwn", blocks[0].Text.Type)

	return blocks[0].Text.Text
}

func TestSendResponseSplitsLongFinalAnswerIntoThreadMessages(t *testing.T) {
	var posted, deleted []url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			assert.Less(t, len([]rune(posted[len(posted)-1].Get("text"))), slackTextLimit)
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.666", "text": posted[len(posted)-1].Get("text")})
		case "/chat.delete":
			_ = r.ParseForm()
			deleted = append(deleted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": deleted[len(deleted)-1].Get("ts")})
		case "/reactions.remove":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	replyTarget := &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: "111.222"}
	_, err := connector.createReplyPlaceholders(context.Background(), replyTarget, slackImmediatePlaceholder)
	require.NoError(t, err)

	paragraph := strings.Repeat("word ", 170) + "\n\n"
	longText := strings.Repeat(paragraph, 8) + "closing line"

	msg := events.NewMainOutboundMessage(events.SourceSlack, longText, events.OutputTargetSlackMain)
	msg.TurnID = "turn-thread"
	msg.Complete = true
	msg.SlackReply = replyTarget
	require.NoError(t, connector.SendResponse(context.Background(), msg))

	require.Len(t, deleted, 2)
	assert.Equal(t, "555.666", deleted[0].Get("ts"))
	assert.Equal(t, "555.666", deleted[1].Get("ts"))
	require.Greater(t, len(posted), 3)

	chunks := posted[2:]

	var rebuilt strings.Builder

	for i := range chunks {
		assert.Equal(t, "111.222", chunks[i].Get("thread_ts"))
		assert.Less(t, len([]rune(chunks[i].Get("text"))), slackTextLimit)

		if i < len(chunks)-1 {
			text := chunks[i].Get("text")
			last := []rune(text)[len([]rune(text))-1]
			assert.True(t, last == '\n' || last == ' ' || last == '\t')
		}

		rebuilt.WriteString(chunks[i].Get("text"))
	}

	assert.Equal(t, longText, rebuilt.String())
}

func TestPostResponseChunksDeletesPostedChunksOnFailure(t *testing.T) {
	var posted, deleted []url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			if len(posted) == 3 {
				writeJSON(t, w, map[string]any{"ok": false, "error": "ratelimited"})
				return
			}

			writeJSON(t, w, map[string]any{
				"ok":      true,
				"channel": "D123",
				"ts":      "555." + strconv.Itoa(len(posted)),
				"text":    posted[len(posted)-1].Get("text"),
			})
		case "/chat.delete":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			deleted = append(deleted, cloneValues(r.PostForm))

			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	_, err := connector.postResponseChunks(context.Background(), "D123", "111.222", []string{"one", "two", "three"})
	require.ErrorContains(t, err, "send Slack response chunk 3/3")

	require.Len(t, posted, 3)
	assert.Equal(t, "111.222", posted[0].Get("thread_ts"))
	assert.Equal(t, "111.222", posted[1].Get("thread_ts"))
	assert.Equal(t, "111.222", posted[2].Get("thread_ts"))
	require.Len(t, deleted, 2)
	assert.Equal(t, "555.2", deleted[0].Get("ts"))
	assert.Equal(t, "555.1", deleted[1].Get("ts"))
}

func TestSendResponseUpdatesTailAnswerPlaceholder(t *testing.T) {
	var deleted, posted, updated []url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.666", "text": posted[len(posted)-1].Get("text")})
		case "/chat.update":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			updated = append(updated, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": updated[len(updated)-1].Get("ts"), "text": updated[len(updated)-1].Get("text")})
		case "/chat.delete":
			_ = r.ParseForm()
			deleted = append(deleted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": deleted[len(deleted)-1].Get("ts")})
		case "/reactions.remove":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	replyTarget := &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: "111.222"}
	_, err := connector.createReplyPlaceholders(context.Background(), replyTarget, slackImmediatePlaceholder)
	require.NoError(t, err)

	msg := events.NewMainOutboundMessage(events.SourceSlack, "thread answer", events.OutputTargetSlackMain)
	msg.TurnID = "turn-thread"
	msg.Complete = true
	msg.SlackReply = replyTarget
	require.NoError(t, connector.SendResponse(context.Background(), msg))

	require.Len(t, deleted, 1)
	assert.Equal(t, "555.666", deleted[0].Get("ts"))
	require.Len(t, posted, 2)
	assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	assert.Equal(t, "111.222", posted[1].Get("thread_ts"))
	require.Len(t, updated, 1)
	assert.Equal(t, "thread answer", updated[0].Get("text"))
}

func TestSendResponseUpdatesNonTailAnswerPlaceholder(t *testing.T) {
	var deleted, posted, updated []url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555." + strconv.Itoa(len(posted)), "text": posted[len(posted)-1].Get("text")})
		case "/chat.update":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			updated = append(updated, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": updated[len(updated)-1].Get("ts"), "text": updated[len(updated)-1].Get("text")})
		case "/chat.delete":
			_ = r.ParseForm()
			deleted = append(deleted, cloneValues(r.PostForm))

			writeJSON(t, w, map[string]any{"ok": true})
		case "/reactions.remove":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	first := &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.1", ThreadTS: "111.1"}

	_, err := connector.createReplyPlaceholders(context.Background(), first, slackImmediatePlaceholder)
	require.NoError(t, err)

	thinking := events.NewMainOutboundMessage(events.SourceSlack, "", events.OutputTargetSlackMain)
	thinking.TurnID = "turn-thread"
	thinking.ProgressText = "thinking"
	thinking.SlackReply = first
	require.NoError(t, connector.SendResponse(context.Background(), thinking))

	msg := events.NewMainOutboundMessage(events.SourceSlack, "first answer", events.OutputTargetSlackMain)
	msg.TurnID = "turn-thread"
	msg.Complete = true
	msg.SlackReply = first
	require.NoError(t, connector.SendResponse(context.Background(), msg))

	require.Len(t, posted, 2)
	require.Len(t, updated, 1)
	assert.Equal(t, "first answer", updated[0].Get("text"))
	assert.Equal(t, "555.2", updated[0].Get("ts"))
	require.Len(t, deleted, 1)
	assert.Equal(t, "555.1", deleted[0].Get("ts"))
}

func TestSendResponseSucceedsWhenThinkingDeleteFails(t *testing.T) {
	var deleted, posted, updated []url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555." + strconv.Itoa(len(posted)), "text": posted[len(posted)-1].Get("text")})
		case "/chat.update":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			updated = append(updated, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": updated[len(updated)-1].Get("ts"), "text": updated[len(updated)-1].Get("text")})
		case "/chat.delete":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			deleted = append(deleted, cloneValues(r.PostForm))

			writeJSON(t, w, map[string]any{"ok": false, "error": "message_not_found"})
		case "/reactions.remove":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	replyTarget := &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: "111.222"}
	_, err := connector.createReplyPlaceholders(context.Background(), replyTarget, slackImmediatePlaceholder)
	require.NoError(t, err)

	msg := events.NewMainOutboundMessage(events.SourceSlack, "final answer", events.OutputTargetSlackMain)
	msg.TurnID = "turn-thread"
	msg.Complete = true
	msg.SlackReply = replyTarget
	require.NoError(t, connector.SendResponse(context.Background(), msg))

	require.Len(t, posted, 2)
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	require.Len(t, updated, 1)
	assert.Equal(t, "final answer", updated[0].Get("text"))
	require.Len(t, deleted, 1)
	assert.Equal(t, "555.1", deleted[0].Get("ts"))
}

func TestSendResponseDeletesPlaceholdersForEmptyFinal(t *testing.T) {
	var deleted, posted []url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555." + strconv.Itoa(len(posted)), "text": posted[len(posted)-1].Get("text")})
		case "/chat.delete":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			deleted = append(deleted, cloneValues(r.PostForm))

			writeJSON(t, w, map[string]any{"ok": true})
		case "/reactions.remove":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	replyTarget := &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: "111.222"}
	_, err := connector.createReplyPlaceholders(context.Background(), replyTarget, slackImmediatePlaceholder)
	require.NoError(t, err)

	msg := events.NewMainOutboundMessage(events.SourceSlack, "", events.OutputTargetSlackMain)
	msg.TurnID = "turn-thread"
	msg.Complete = true
	msg.SlackReply = replyTarget
	require.NoError(t, connector.SendResponse(context.Background(), msg))

	require.Len(t, posted, 2)
	assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	require.Len(t, deleted, 2)
	assert.Equal(t, "555.2", deleted[0].Get("ts"))
	assert.Equal(t, "555.1", deleted[1].Get("ts"))
}

func TestCreateReplyPlaceholdersCreatesThinkingAndAnswerPlaceholders(t *testing.T) {
	var posted []url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))

			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555." + strconv.Itoa(len(posted)), "text": posted[len(posted)-1].Get("text")})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	_, err := connector.createReplyPlaceholders(context.Background(), &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: "111.222"}, slackImmediatePlaceholder)

	require.NoError(t, err)
	require.Len(t, posted, 2)
	assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
	assert.Equal(t, "111.222", posted[0].Get("thread_ts"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	assert.Equal(t, "111.222", posted[1].Get("thread_ts"))
	assert.True(t, connector.hasPendingState(&events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: "111.222"}))
}

func TestCreateReplyPlaceholdersSkipsMissingReplyTarget(t *testing.T) {
	connector := newTestConnector("http://127.0.0.1:1")

	for _, replyTarget := range []*events.SlackReplyTarget{nil, &events.SlackReplyTarget{ChannelID: " "}} {
		slots, err := connector.createReplyPlaceholders(context.Background(), replyTarget, slackImmediatePlaceholder)
		require.NoError(t, err)
		assert.Equal(t, slackReplySlots{}, slots)
	}
}

func TestCreateReplyPlaceholdersDeletesThinkingWhenAnswerPlaceholderFails(t *testing.T) {
	var posted, deleted []url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			if len(posted) == 2 {
				writeJSON(t, w, map[string]any{"ok": false, "error": "ratelimited"})
				return
			}

			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.1"})
		case "/chat.delete":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			deleted = append(deleted, cloneValues(r.PostForm))

			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	replyTarget := &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: "111.222"}
	slots, err := connector.createReplyPlaceholders(context.Background(), replyTarget, slackImmediatePlaceholder)
	require.ErrorContains(t, err, "post Slack answer placeholder")
	assert.Equal(t, slackReplySlots{}, slots)
	require.Len(t, posted, 2)
	require.Len(t, deleted, 1)
	assert.Equal(t, "555.1", deleted[0].Get("ts"))
	assert.False(t, connector.hasPendingState(replyTarget))
}

func TestPublishOnDemandCronReplyPublishesPostTextAndReportsBusErrors(t *testing.T) {
	bus := events.New()
	connector := newTestConnectorWithOptions("http://slack.test", bus, nil, nil, nil)
	replyTarget := &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: "333.444"}

	require.NoError(t, connector.publishOnDemandCronReply(context.Background(), nil, "ignored", false))
	require.NoError(t, connector.publishOnDemandCronReply(context.Background(), replyTarget, " ", false))
	assert.Nil(t, cloneSlackReplyTarget(nil))

	require.NoError(t, connector.publishOnDemandCronReply(context.Background(), replyTarget, " preview ", false))
	outbound := readOneOutbound(t, bus)
	assert.Equal(t, "preview", outbound.Text)
	assert.False(t, outbound.Complete)
	assert.True(t, outbound.PostProgressText)
	require.NotNil(t, outbound.SlackReply)
	assert.Equal(t, "333.444", outbound.SlackReply.ThreadTS)

	bus.Close()

	err := connector.publishOnDemandCronReply(context.Background(), replyTarget, "final", true)
	require.ErrorContains(t, err, "publish Slack on-demand cron reply")
}

func TestPostSlackThreadReplySkipsBlankAndReportsPostError(t *testing.T) {
	connector := newTestConnector("http://127.0.0.1:1")
	require.NoError(t, connector.postSlackThreadReply(context.Background(), "D123", "111.222", " "))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/chat.postMessage", r.URL.Path)
		writeJSON(t, w, map[string]any{"ok": false, "error": "ratelimited"})
	}))
	defer server.Close()

	connector = newTestConnector(server.URL)
	err := connector.postSlackThreadReply(context.Background(), "D123", "111.222", "reply")
	require.ErrorContains(t, err, "send Slack thread reply")
}

func TestSendResponseWithBlankTurnIDDoesNotClaimPendingPlaceholder(t *testing.T) {
	var posted []url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555." + strconv.Itoa(len(posted)), "text": posted[len(posted)-1].Get("text")})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	replyTarget := &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: "111.222"}
	_, err := connector.createReplyPlaceholders(context.Background(), replyTarget, slackImmediatePlaceholder)
	require.NoError(t, err)

	msg := events.NewMainOutboundMessage(events.SourceSlack, "metadata", events.OutputTargetSlackMain)
	msg.PostProgressText = true
	msg.SlackReply = replyTarget
	require.NoError(t, connector.SendResponse(context.Background(), msg))

	require.Len(t, posted, 3)
	assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	assert.Equal(t, "metadata", posted[2].Get("text"))
	assert.True(t, connector.hasPendingState(replyTarget))
}

func TestSendResponseUploadsAttachmentsToSlackThread(t *testing.T) {
	var (
		posted, uploadURL, completed  url.Values
		uploadedName, uploadedContent string
	)

	var server *httptest.Server

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = cloneValues(r.PostForm)
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.666", "text": posted.Get("text")})
		case "/files.getUploadURLExternal":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			uploadURL = cloneValues(r.PostForm)

			writeJSON(t, w, map[string]any{"ok": true, "upload_url": server.URL + "/upload", "file_id": "F123"})
		case "/upload":
			if !assert.NoError(t, r.ParseMultipartForm(1<<20)) {
				return
			}

			file, header, err := r.FormFile("file")
			if !assert.NoError(t, err) {
				return
			}

			defer func() { assert.NoError(t, file.Close()) }()

			data, err := io.ReadAll(file)
			if !assert.NoError(t, err) {
				return
			}

			uploadedName = header.Filename
			uploadedContent = string(data)

			writeJSON(t, w, map[string]any{"ok": true})
		case "/files.completeUploadExternal":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			completed = cloneValues(r.PostForm)

			writeJSON(t, w, map[string]any{"ok": true, "files": []map[string]string{{"id": "F123", "title": "report.txt"}}})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	msg := events.NewMainOutboundMessage(events.SourceSystem, "final payload", events.OutputTargetSlackMain)
	msg.Complete = true
	msg.SlackReply = &events.SlackReplyTarget{ChannelID: "D123", ThreadTS: "111.222"}
	msg.Attachments = []events.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report body")}}
	require.NoError(t, connector.SendResponse(context.Background(), msg))

	assert.Equal(t, "final payload", posted.Get("text"))
	assert.Equal(t, "111.222", posted.Get("thread_ts"))
	assert.Equal(t, "report.txt", uploadURL.Get("filename"))
	assert.Equal(t, strconv.Itoa(len("report body")), uploadURL.Get("length"))
	assert.Equal(t, "report.txt", uploadedName)
	assert.Equal(t, "report body", uploadedContent)
	assert.Equal(t, "D123", completed.Get("channel_id"))
	assert.Equal(t, "111.222", completed.Get("thread_ts"))
}

func TestSendResponseDoesNotFailWhenAttachmentUploadFails(t *testing.T) {
	var posted []url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.666", "text": posted[len(posted)-1].Get("text")})
		case "/files.getUploadURLExternal":
			writeJSON(t, w, map[string]any{"ok": false, "error": "missing_scope"})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	msg := events.NewMainOutboundMessage(events.SourceSystem, "final payload", events.OutputTargetSlackMain)
	msg.Complete = true
	msg.SlackReply = &events.SlackReplyTarget{ChannelID: "D123", ThreadTS: "111.222"}
	msg.Attachments = []events.OutboundAttachment{{Name: "example-com.png", MIMEType: "image/png", Data: []byte("png")}}
	require.NoError(t, connector.SendResponse(context.Background(), msg))

	require.Len(t, posted, 1)
	assert.Equal(t, "final payload", posted[0].Get("text"))
	assert.Equal(t, "111.222", posted[0].Get("thread_ts"))
}

func TestSendResponseRecordsCheckpointForEveryTopLevelChunk(t *testing.T) {
	var posted []url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": fmt.Sprintf("555.%d", len(posted)), "text": posted[len(posted)-1].Get("text")})
		case "/reactions.remove":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	router := newThreadRouterStub()
	connector := newTestConnectorWithOptions(server.URL, events.New(), nil, router, nil)
	text := strings.Repeat("chunk text ", 900)
	msg := events.NewMainOutboundMessage(events.SourceSlack, text, events.OutputTargetSlackMain)
	msg.TurnID = "turn-main"
	msg.Complete = true
	msg.Checkpoint = &events.ResponseCheckpoint{ConversationID: "main", SessionEntryID: 9, ResponseID: "resp-9", Model: "gpt-5.4", AssistantText: text}

	require.NoError(t, connector.SendResponse(context.Background(), msg))
	require.Greater(t, len(posted), 1)

	checkpoints := router.checkpointsSnapshot()
	require.Len(t, checkpoints, len(posted))

	for i := range checkpoints {
		assert.Equal(t, "D123", checkpoints[i].channelID)
		assert.Equal(t, fmt.Sprintf("555.%d", i+1), checkpoints[i].messageTS)
		assert.Equal(t, *msg.Checkpoint, checkpoints[i].checkpoint)
	}
}

func TestSendResponsePropagatesCheckpointRecordingError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
				return
			}

			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.1", "text": r.PostForm.Get("text")})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	errCheckpoint := errors.New("store unavailable")
	router := newThreadRouterStub()
	router.errCheckpoint = errCheckpoint
	connector := newTestConnectorWithOptions(server.URL, events.New(), nil, router, nil)
	msg := events.NewMainOutboundMessage(events.SourceSlack, "answer", events.OutputTargetSlackMain)
	msg.Complete = true
	msg.Checkpoint = &events.ResponseCheckpoint{ConversationID: "main", SessionEntryID: 9, ResponseID: "resp-9", Model: "gpt-5.5", AssistantText: "answer"}

	err := connector.SendResponse(context.Background(), msg)
	require.ErrorIs(t, err, errCheckpoint)
	require.ErrorContains(t, err, "record Slack response checkpoint")

	checkpoints := router.checkpointsSnapshot()
	require.Len(t, checkpoints, 1)
	assert.Equal(t, "D123", checkpoints[0].channelID)
	assert.Equal(t, "555.1", checkpoints[0].messageTS)
}

func TestHandleEventsAPICreatesImmediatePlaceholderForMainMessage(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var posted []url.Values

	reactionCalls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.666", "text": posted[len(posted)-1].Get("text")})
		case "/reactions.add":
			reactionCalls++

			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	connector.bus = bus
	connector.handleEventsAPI(context.Background(), newSlackEventsAPIEvent(
		newTestSlackMessageEvent(),
	))

	require.Len(t, posted, 2)
	assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	assert.Empty(t, posted[0].Get("thread_ts"))
	assert.Equal(t, 1, reactionCalls)
	inbound := readOneInbound(t, bus)
	assert.Equal(t, events.MainConversationID(), inbound.ConversationID)
	assert.NotNil(t, inbound.SlackReply)
}

func TestHandleMessageEventFinishesStackWhenThreadReplySubmitFails(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var posted []url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.info":
			writeJSON(t, w, map[string]any{"ok": true, "channel": map[string]any{"id": "C123", "name": "social"}})
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": fmt.Sprintf("555.%d", len(posted)), "text": posted[len(posted)-1].Get("text")})
		case "/chat.update":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": r.PostForm.Get("ts"), "text": r.PostForm.Get("text")})
		case "/chat.delete", "/reactions.remove":
			writeJSON(t, w, map[string]any{"ok": true})
		case "/reactions.add":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	router := newThreadRouterStub()
	router.prepareHandled = true
	router.submitHandled = true
	router.errSubmit = errors.New("submit failed")
	connector := newTestConnectorWithOptions(server.URL, bus, nil, router, nil)
	connector.config.SocialMode = config.TextSocialConfig{Enabled: true, Channels: []config.TextSocialChannelConfig{{Channel: "#social", Agent: "social", AllowedUserIDs: []string{"U123"}}}, ContextMessages: 2}

	first := newSlackMessageEvent("171234.9999", "171234.5678", "status?")
	first.Channel = "C123"
	connector.handleMessageEvent(context.Background(), first)

	second := newSlackMessageEvent("171235.9999", "171234.5678", "again?")
	second.Channel = "C123"
	connector.handleMessageEvent(context.Background(), second)

	replies := router.repliesSnapshot()
	require.Len(t, replies, 2)
	assert.Equal(t, "status?", replies[0].inbound.Text)
	assert.Equal(t, "again?", replies[1].inbound.Text)
	require.Len(t, posted, 6)
	assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	assert.Contains(t, posted[2].Get("text"), "couldn't submit that Slack thread reply")
	assert.Equal(t, slackImmediatePlaceholder, posted[3].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[4].Get("text"))
	assert.Contains(t, posted[5].Get("text"), "couldn't submit that Slack thread reply")
}

func TestHandleMessageEventFinishesStackWhenThreadReplyUnhandled(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var (
		posted    []url.Values
		reactions []string
	)

	server := newSlackStackTestServer(t, &posted, &reactions)
	defer server.Close()

	router := newThreadRouterStub()
	router.prepareHandled = true
	connector := newTestConnectorWithOptions(server.URL, bus, nil, router, nil)
	connector.config.SocialMode = config.TextSocialConfig{Enabled: true, Channels: []config.TextSocialChannelConfig{{Channel: "#social", Agent: "social", AllowedUserIDs: []string{"U123"}}}, ContextMessages: 2}

	first := newSlackMessageEvent("171234.9999", "171234.5678", "status?")
	first.Channel = "C123"
	connector.handleMessageEvent(context.Background(), first)

	second := newSlackMessageEvent("171235.9999", "171234.5678", "again?")
	second.Channel = "C123"
	connector.handleMessageEvent(context.Background(), second)

	replies := router.repliesSnapshot()
	require.Len(t, replies, 2)
	assert.Equal(t, "status?", replies[0].inbound.Text)
	assert.Equal(t, "again?", replies[1].inbound.Text)
	require.Len(t, posted, 6)
	assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	assert.Contains(t, posted[2].Get("text"), "couldn't find an active managed thread")
	assert.Equal(t, slackImmediatePlaceholder, posted[3].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[4].Get("text"))
	assert.Contains(t, posted[5].Get("text"), "couldn't find an active managed thread")
	assert.Contains(t, reactions, "/reactions.remove "+slackRobotReaction+" 171234.9999")
	assert.Contains(t, reactions, "/reactions.remove "+slackRobotReaction+" 171235.9999")
}

func TestHandleMessageEventBuffersSlackMessagesWhileActive(t *testing.T) {
	for _, thread := range []bool{false, true} {
		t.Run(fmt.Sprintf("thread=%t", thread), func(t *testing.T) {
			bus := events.New()
			defer bus.Close()

			var (
				posted    []url.Values
				reactions []string
			)

			server := newSlackStackTestServer(t, &posted, &reactions)
			defer server.Close()

			router := newThreadRouterStub()
			router.submitHandled = true
			connector := newTestConnectorWithOptions(server.URL, bus, config.ThreadAgents{":thread:": {Agent: "main", PreSeed: true}}, router, nil)

			final := events.NewMainOutboundMessage(events.SourceSlack, "done", events.OutputTargetSlackMain)
			final.TurnID = "turn-1"
			final.Complete = true

			if thread {
				connector.handleMessageEvent(context.Background(), newSlackMessageEvent("111.1", "", ":thread: first"))

				started := router.startedSnapshot()
				require.Len(t, started, 1)
				final.SlackReply = started[0].inbound.SlackReply
			} else {
				connector.handleMessageEvent(context.Background(), newSlackMessageEvent("111.1", "", "first"))

				final.SlackReply = readOneInbound(t, bus).SlackReply
			}

			threadTS := ""
			if thread {
				threadTS = "111.1"
			}

			connector.handleMessageEvent(context.Background(), newSlackMessageEvent("111.2", threadTS, "second"))
			connector.handleMessageEvent(context.Background(), newSlackMessageEvent("111.3", threadTS, "third"))

			require.NoError(t, connector.SendResponse(context.Background(), final))

			if thread {
				replies := router.repliesSnapshot()
				require.Len(t, replies, 1)
				assert.Equal(t, "second\n\nthird", replies[0].inbound.Text)
				assert.Equal(t, "111.3", replies[0].inbound.SlackReply.MessageTS)
				assert.Equal(t, "111.1", replies[0].inbound.SlackReply.ThreadTS)
			} else {
				promoted := readOneInbound(t, bus)
				assert.Equal(t, "second\n\nthird", promoted.Text)
				assert.Equal(t, "111.3", promoted.SlackReply.MessageTS)
			}

			assertNeverInbound(t, bus)

			require.Len(t, posted, 5)
			assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
			assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
			assert.Equal(t, "done", posted[2].Get("text"))
			assert.Equal(t, "555.2", posted[2].Get("ts"))
			assert.Equal(t, slackImmediatePlaceholder, posted[3].Get("text"))
			assert.Equal(t, slackAnswerPlaceholder, posted[4].Get("text"))

			if thread {
				assert.Equal(t, "111.1", posted[0].Get("thread_ts"))
				assert.Equal(t, "111.1", posted[1].Get("thread_ts"))
				assert.Equal(t, "111.1", posted[3].Get("thread_ts"))
				assert.Equal(t, "111.1", posted[4].Get("thread_ts"))
			}

			for _, want := range []string{slackRobotReaction + " 111.1", slackBufferedReaction + " 111.2", slackBufferedReaction + " 111.3"} {
				assert.Contains(t, reactions, "/reactions.add "+want)
			}

			for _, want := range []string{slackBufferedReaction + " 111.2", slackBufferedReaction + " 111.3"} {
				assert.Contains(t, reactions, "/reactions.remove "+want)
			}
		})
	}
}

func TestHandleMessageEventClearsSlackMainStackWhenPublishFails(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	bus.StopInbound()

	var (
		posted    []url.Values
		reactions []string
	)

	server := newSlackStackTestServer(t, &posted, &reactions)
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, bus, nil, newThreadRouterStub(), nil)
	connector.handleMessageEvent(context.Background(), newSlackMessageEvent("171234.5678", "", "hello main"))

	assert.Contains(t, reactions, "/reactions.remove "+slackRobotReaction+" 171234.5678")
	require.Len(t, posted, 3)
	assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	assert.Contains(t, posted[2].Get("text"), "couldn't start processing")
	assert.Equal(t, "555.2", posted[2].Get("ts"))

	connector.mu.Lock()
	_, active := connector.stacks[slackMainStackKey]
	connector.mu.Unlock()
	assert.False(t, active)
}

func TestHandleMessageEventStartsConfiguredSlackThread(t *testing.T) {
	for _, tt := range []struct {
		name         string
		threadAgents config.ThreadAgents
		text         string
		wantAgent    string
		wantPreSeed  bool
		wantPrompt   string
	}{
		{name: "prompt text", threadAgents: config.ThreadAgents{":thread:": {Agent: "main", PreSeed: true}}, text: ":thread: hello from thread", wantAgent: "main", wantPreSeed: true, wantPrompt: "hello from thread"},
		{name: "prefix only", threadAgents: config.ThreadAgents{":factory:": {Agent: "factory", PreSeed: false}}, text: ":factory:", wantAgent: "factory", wantPreSeed: false, wantPrompt: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bus := events.New()
			defer bus.Close()

			var posted []url.Values

			router := newThreadRouterStub()
			router.onStart = func() {
				require.Len(t, posted, 2)
				assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
				assert.Equal(t, "171234.5678", posted[0].Get("thread_ts"))
				assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
				assert.Equal(t, "171234.5678", posted[1].Get("thread_ts"))
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/chat.postMessage":
					if !assert.NoError(t, r.ParseForm()) {
						return
					}

					posted = append(posted, cloneValues(r.PostForm))
					writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.666", "text": posted[len(posted)-1].Get("text")})
				case "/reactions.add":
					writeJSON(t, w, map[string]any{"ok": true})
				default:
					assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
				}
			}))
			defer server.Close()

			connector := newTestConnectorWithOptions(server.URL, bus, tt.threadAgents, router, nil)
			connector.handleMessageEvent(context.Background(), newSlackMessageEvent("171234.5678", "", tt.text))

			started := router.startedSnapshot()
			require.Len(t, started, 1)
			assert.Equal(t, tt.wantAgent, started[0].agent)
			assert.Equal(t, tt.wantPreSeed, started[0].preSeed)
			assert.Equal(t, tt.wantPrompt, started[0].inbound.Text)
			assertNeverInbound(t, bus)
		})
	}
}

func TestHandleMessageEventClearsSlackStackWhenConfiguredThreadStartFails(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var (
		posted    []url.Values
		reactions []string
	)

	server := newSlackStackTestServer(t, &posted, &reactions)
	defer server.Close()

	router := newThreadRouterStub()
	router.errStart = errors.New("start failed")
	connector := newTestConnectorWithOptions(server.URL, bus, config.ThreadAgents{":thread:": {Agent: "main", PreSeed: true}}, router, nil)

	connector.handleMessageEvent(context.Background(), newSlackMessageEvent("171234.5678", "", ":thread: hello from thread"))

	started := router.startedSnapshot()
	require.Len(t, started, 1)
	assert.Equal(t, "hello from thread", started[0].inbound.Text)
	assertNeverInbound(t, bus)
	assert.Contains(t, reactions, "/reactions.remove "+slackRobotReaction+" 171234.5678")
	require.Len(t, posted, 3)
	assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	assert.Contains(t, posted[2].Get("text"), "couldn't start that managed thread")
	assert.Equal(t, "555.2", posted[2].Get("ts"))

	connector.mu.Lock()
	_, active := connector.stacks[slackThreadStackKey(&events.SlackReplyTarget{ChannelID: "D123", ThreadTS: "171234.5678"})]
	connector.mu.Unlock()
	assert.False(t, active)
}

func TestHandleMessageEventConsumesGoalStartRejectionPlaceholder(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var (
		posted    []url.Values
		reactions []string
	)

	server := newSlackStackTestServer(t, &posted, &reactions)
	defer server.Close()

	router := newThreadRouterStub()
	router.errStart = errors.New("check script denied")
	connector := newTestConnectorWithOptions(server.URL, bus, nil, router, nil)

	connector.handleMessageEvent(context.Background(), newSlackMessageEvent("171234.5678", "", "🏁 checkScript: ./scripts/check.sh fix lint"))

	require.Len(t, router.goalStarts, 1)
	assert.Equal(t, "fix lint", router.goalStarts[0].objective)
	assert.Equal(t, "./scripts/check.sh", router.goalStarts[0].checkScript)
	assert.Contains(t, reactions, "/reactions.remove "+slackRobotReaction+" 171234.5678")
	require.Len(t, posted, 3)
	assert.Equal(t, slackGoalPlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	assert.Contains(t, posted[2].Get("text"), "couldn't start that goal")
	assert.False(t, connector.hasPendingState(&events.SlackReplyTarget{ChannelID: "D123", MessageTS: "171234.5678", ThreadTS: "171234.5678"}))
}

func TestHandleMessageEventStartsGoalInExistingManagedThread(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var (
		posted    []url.Values
		reactions []string
	)

	server := newSlackStackTestServer(t, &posted, &reactions)
	defer server.Close()

	router := newThreadRouterStub()
	router.prepareHandled = true
	connector := newTestConnectorWithOptions(server.URL, bus, nil, router, nil)

	connector.handleMessageEvent(context.Background(), newSlackMessageEvent("222.333", "111.222", ":checkered_flag: maxTurns: 2 fix lint"))

	require.Len(t, router.goalStarts, 1)
	assert.Empty(t, router.goalStarts[0].agent)
	assert.Equal(t, "fix lint", router.goalStarts[0].objective)
	assert.Equal(t, 2, router.goalStarts[0].maxTurns)
	assert.Equal(t, "fix lint", router.goalStarts[0].inbound.Text)
	assert.Contains(t, reactions, "/reactions.add "+slackRobotReaction+" 222.333")
	require.Len(t, posted, 2)
	assert.Equal(t, slackGoalPlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	assertNeverInbound(t, bus)
}

func TestHandleMessageEventRejectsDuplicateActiveGoal(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var (
		posted    []url.Values
		reactions []string
	)

	server := newSlackStackTestServer(t, &posted, &reactions)
	defer server.Close()

	router := newThreadRouterStub()
	router.prepareHandled = true
	router.errStart = harnessbridge.ErrGoalAlreadyActive
	connector := newTestConnectorWithOptions(server.URL, bus, nil, router, nil)

	connector.handleMessageEvent(context.Background(), newSlackMessageEvent("222.333", "111.222", "🏁 another goal"))

	require.Len(t, router.goalStarts, 1)
	assert.Contains(t, reactions, "/reactions.add "+slackInterruptionReaction+" 222.333")
	require.Len(t, posted, 3)
	assert.Equal(t, slackGoalPlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	assert.Contains(t, posted[2].Get("text"), "already in progress")
	assertNeverInbound(t, bus)
}

func TestHandleMessageEventStopMarksOriginalTurnStart(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var (
		posted    []url.Values
		reactions []string
	)

	server := newSlackStackTestServer(t, &posted, &reactions)
	defer server.Close()

	router := newThreadRouterStub()
	router.prepareHandled = true
	router.stopResult = &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "222.333", ThreadTS: "111.222"}
	connector := newTestConnectorWithOptions(server.URL, bus, nil, router, nil)

	connector.handleMessageEvent(context.Background(), newSlackMessageEvent("333.444", "111.222", "🛑"))

	require.Len(t, router.goalStops, 1)
	assert.Equal(t, goalThreadStopCall{channelID: "D123", threadTS: "111.222"}, router.goalStops[0])
	assert.Contains(t, reactions, "/reactions.add "+slackInterruptionReaction+" 222.333")
	assert.Empty(t, posted)
	assertNeverInbound(t, bus)
}

func TestHandleMessageEventStopMainMarksOriginalTurnStart(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var reactions []string

	server := newSlackStackTestServer(t, nil, &reactions)
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, bus, nil, newThreadRouterStub(), nil)
	connector.interruptMainTurn = func() *events.InboundMessage {
		return &events.InboundMessage{SlackReply: &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "222.333"}}
	}

	connector.handleMessageEvent(context.Background(), newSlackMessageEvent("333.444", "", "🛑"))

	assert.Contains(t, reactions, "/reactions.add "+slackInterruptionReaction+" 222.333")
	assertNeverInbound(t, bus)
}

func TestHandleEventsAPIStartsSocialThreadWithChannelContext(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var posted []url.Values

	router := newThreadRouterStub()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.info":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			assert.Equal(t, "C123", r.PostForm.Get("channel"))
			writeJSON(t, w, map[string]any{"ok": true, "channel": map[string]any{"id": "C123", "name": "social"}})
		case "/conversations.history":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			assert.Equal(t, "C123", r.PostForm.Get("channel"))
			assert.Equal(t, "171234.5678", r.PostForm.Get("latest"))
			assert.Equal(t, "2", r.PostForm.Get("limit"))
			writeJSON(t, w, map[string]any{"ok": true, "messages": []map[string]any{{"user": "U222", "text": "newer context"}, {"user": "U111", "text": "older context"}}})
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "C123", "ts": "555.666", "text": posted[len(posted)-1].Get("text")})
		case "/reactions.add":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, bus, nil, router, nil)
	connector.botUserID = "U999"
	connector.config.SocialMode = config.TextSocialConfig{Enabled: true, Channels: []config.TextSocialChannelConfig{{Channel: "#social", Agent: "social", AllowedUserIDs: []string{"U123"}}}, ContextMessages: 2}
	connector.handleEventsAPI(context.Background(), newSlackEventsAPIEvent(
		newSlackAppMentionEvent(),
	))

	started := router.startedSnapshot()
	require.Len(t, started, 1)
	assert.Equal(t, "social", started[0].agent)
	assert.False(t, started[0].preSeed)
	assert.Equal(t, "C123", started[0].inbound.SlackReply.ChannelID)
	assert.Equal(t, "171234.5678", started[0].inbound.SlackReply.ThreadTS)
	assert.Contains(t, started[0].inbound.Text, "- <@U111>: older context\n- <@U222>: newer context")
	assert.Contains(t, started[0].inbound.Text, "Mention:\nplease check this")
	require.Len(t, posted, 2)
	assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	assert.Equal(t, "171234.5678", posted[0].Get("thread_ts"))
	assertNeverInbound(t, bus)
}

func TestHandleAppMentionEventUsesChannelAgentAndPrefixReaction(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var (
		posted        []url.Values
		reactionNames []string
	)

	router := newThreadRouterStub()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.info":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			assert.Equal(t, "C123", r.PostForm.Get("channel"))
			writeJSON(t, w, map[string]any{"ok": true, "channel": map[string]any{"id": "C123", "name": "triage"}})
		case "/conversations.history":
			writeJSON(t, w, map[string]any{"ok": true, "messages": []map[string]any{}})
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "C123", "ts": "555.666", "text": posted[len(posted)-1].Get("text")})
		case "/chat.update":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "C123", "ts": r.PostForm.Get("ts"), "text": r.PostForm.Get("text")})
		case "/chat.delete":
			writeJSON(t, w, map[string]any{"ok": true})
		case "/reactions.add":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			reactionNames = append(reactionNames, r.PostForm.Get("name"))

			writeJSON(t, w, map[string]any{"ok": true})
		case "/reactions.remove":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			reactionNames = append(reactionNames, r.PostForm.Get("name"))

			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, bus, config.ThreadAgents{":triage:": {Agent: "triage", PreSeed: true}}, router, nil)
	connector.botUserID = "U999"
	connector.config.SocialMode = config.TextSocialConfig{Enabled: true, Channels: []config.TextSocialChannelConfig{{Channel: "#triage", Agent: "triage", AllowedUserIDs: []string{"U123"}}}, ContextMessages: 2}
	connector.handleAppMentionEvent(context.Background(), newSlackAppMentionEvent())

	started := router.startedSnapshot()
	require.Len(t, started, 1)
	assert.Equal(t, "triage", started[0].agent)
	require.Len(t, posted, 2)
	assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	assert.ElementsMatch(t, []string{"triage", slackRobotReaction}, reactionNames)
	assertNeverInbound(t, bus)
}

func TestHandleAppMentionEventClearsSlackStackWhenThreadStartFails(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var (
		posted        []url.Values
		reactionNames []string
	)

	router := newThreadRouterStub()
	router.errStart = errors.New("start failed")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.info":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			assert.Equal(t, "C123", r.PostForm.Get("channel"))
			writeJSON(t, w, map[string]any{"ok": true, "channel": map[string]any{"id": "C123", "name": "triage"}})
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "C123", "ts": "555.666", "text": posted[len(posted)-1].Get("text")})
		case "/chat.update":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "C123", "ts": r.PostForm.Get("ts"), "text": r.PostForm.Get("text")})
		case "/chat.delete":
			writeJSON(t, w, map[string]any{"ok": true})
		case "/reactions.add":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			reactionNames = append(reactionNames, r.PostForm.Get("name"))

			writeJSON(t, w, map[string]any{"ok": true})
		case "/reactions.remove":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			reactionNames = append(reactionNames, r.PostForm.Get("name"))

			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, bus, config.ThreadAgents{":triage:": {Agent: "triage", PreSeed: true}}, router, nil)
	connector.botUserID = "U999"
	connector.config.SocialMode = config.TextSocialConfig{Enabled: true, Channels: []config.TextSocialChannelConfig{{Channel: "#triage", Agent: "triage", AllowedUserIDs: []string{"U123"}}}}
	connector.handleAppMentionEvent(context.Background(), newSlackAppMentionEvent())

	started := router.startedSnapshot()
	require.Len(t, started, 1)
	assert.Equal(t, "triage", started[0].agent)
	assert.Equal(t, "please check this", started[0].inbound.Text)
	require.Len(t, posted, 3)
	assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	assert.Contains(t, posted[2].Get("text"), "couldn't start that managed thread")
	assert.Equal(t, []string{"triage", slackRobotReaction}, reactionNames)
	assertNeverInbound(t, bus)

	connector.mu.Lock()
	_, active := connector.stacks[slackThreadStackKey(&events.SlackReplyTarget{ChannelID: "C123", ThreadTS: "171234.5678"})]
	connector.mu.Unlock()
	assert.False(t, active)
}

func TestHandleAppMentionEventIgnoresUnmappedChannel(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	router := newThreadRouterStub()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.info":
			writeJSON(t, w, map[string]any{"ok": true, "channel": map[string]any{"id": "C123", "name": "random"}})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, bus, nil, router, nil)
	connector.botUserID = "U999"
	connector.config.SocialMode = config.TextSocialConfig{Enabled: true, Channels: []config.TextSocialChannelConfig{{Channel: "#triage", Agent: "triage", AllowedUserIDs: []string{"U123"}}}, ContextMessages: 2}
	connector.handleAppMentionEvent(context.Background(), newSlackAppMentionEvent())

	assert.Empty(t, router.startedSnapshot())
	assertNeverInbound(t, bus)
}

func TestHandleAppMentionEventRequiresSocialModeAndAllowlist(t *testing.T) {
	for _, tt := range []struct {
		name    string
		config  config.TextSocialConfig
		user    string
		channel string
	}{
		{name: "disabled", config: config.TextSocialConfig{Enabled: false, Channels: []config.TextSocialChannelConfig{{Channel: "#social", Agent: "social", AllowedUserIDs: []string{"U123"}}}, ContextMessages: 1}, user: "U123", channel: "C123"},
		{name: "not allowlisted", config: config.TextSocialConfig{Enabled: true, Channels: []config.TextSocialChannelConfig{{Channel: "#social", Agent: "social", AllowedUserIDs: []string{"U456"}}}, ContextMessages: 1}, user: "U123", channel: "C123"},
		{name: "dm ignored", config: config.TextSocialConfig{Enabled: true, Channels: []config.TextSocialChannelConfig{{Channel: "#social", Agent: "social", AllowedUserIDs: []string{"U123"}}}, ContextMessages: 1}, user: "U123", channel: "D123"},
		{name: "empty channel agents", config: config.TextSocialConfig{Enabled: true, ContextMessages: 1}, user: "U123", channel: "C123"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			router := newThreadRouterStub()
			connector := newTestConnectorWithOptions("http://127.0.0.1", events.New(), nil, router, nil)
			connector.botUserID = "U999"
			connector.config.SocialMode = tt.config

			ev := newSlackAppMentionEvent()
			ev.User = tt.user
			ev.Channel = tt.channel
			connector.handleAppMentionEvent(context.Background(), ev)

			assert.Empty(t, router.startedSnapshot())
		})
	}
}

func TestHandleAppMentionEventUsesPerChannelAllowlist(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	router := newThreadRouterStub()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.info":
			writeJSON(t, w, map[string]any{"ok": true, "channel": map[string]any{"id": "C123", "name": "triage"}})
		case "/conversations.history":
			writeJSON(t, w, map[string]any{"ok": true, "messages": []map[string]any{}})
		case "/chat.postMessage":
			writeJSON(t, w, map[string]any{"ok": true, "channel": "C123", "ts": "555.666"})
		case "/reactions.add":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, bus, nil, router, nil)
	connector.botUserID = "U777"
	connector.config.SocialMode = config.TextSocialConfig{Enabled: true, Channels: []config.TextSocialChannelConfig{{Channel: "#triage", Agent: "triage", AllowedUserIDs: []string{"U999"}}}, ContextMessages: 1}

	allowed := newSlackAppMentionEvent()
	allowed.User = "U999"
	connector.handleAppMentionEvent(context.Background(), allowed)

	denied := newSlackAppMentionEvent()
	denied.User = "U123"
	denied.TimeStamp = "171234.9999"
	connector.handleAppMentionEvent(context.Background(), denied)

	started := router.startedSnapshot()
	require.Len(t, started, 1)
	assert.Equal(t, "triage", started[0].agent)
	assertNeverInbound(t, bus)
}

func TestHandleMessageEventRoutesManagedSocialThreadReply(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var posted []url.Values

	server := newSlackStackTestServer(t, &posted, new([]string))
	defer server.Close()

	router := newThreadRouterStub()
	router.prepareHandled = true
	router.submitHandled = true
	connector := newTestConnectorWithOptions(server.URL, bus, nil, router, nil)
	connector.config.SocialMode = config.TextSocialConfig{Enabled: true, Channels: []config.TextSocialChannelConfig{{Channel: "#social", Agent: "social", AllowedUserIDs: []string{"U123"}}}, ContextMessages: 2}

	ev := newSlackMessageEvent("171234.9999", "171234.5678", "refer to <#C111|triage>")
	ev.Channel = "C123"
	connector.handleMessageEvent(context.Background(), ev)

	replies := router.repliesSnapshot()
	require.Len(t, replies, 1)
	assert.Equal(t, "C123", replies[0].channelID)
	assert.Equal(t, "171234.5678", replies[0].threadTS)
	assert.Equal(t, "refer to <#C111|triage>", replies[0].inbound.Text)
	require.Len(t, posted, 2)
	assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	assert.Equal(t, "171234.5678", posted[0].Get("thread_ts"))
}

func TestHandleMessageEventUsesPerChannelAllowlist(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var posted []url.Values

	server := newSlackStackTestServer(t, &posted, new([]string))
	defer server.Close()

	router := newThreadRouterStub()
	router.prepareHandled = true
	router.submitHandled = true
	connector := newTestConnectorWithOptions(server.URL, bus, nil, router, nil)
	connector.config.SocialMode = config.TextSocialConfig{Enabled: true, Channels: []config.TextSocialChannelConfig{{Channel: "#social", Agent: "social", AllowedUserIDs: []string{"U999"}}}, ContextMessages: 1}

	allowed := newSlackMessageEvent("171234.9999", "171234.5678", "allowed follow up")
	allowed.User = "U999"
	allowed.Channel = "C123"
	connector.handleMessageEvent(context.Background(), allowed)

	denied := newSlackMessageEvent("171234.9998", "171234.5678", "denied follow up")
	denied.User = "U123"
	denied.Channel = "C123"
	connector.handleMessageEvent(context.Background(), denied)

	replies := router.repliesSnapshot()
	require.Len(t, replies, 1)
	assert.Equal(t, "allowed follow up", replies[0].inbound.Text)
	require.Len(t, posted, 2)
}

func TestHandleMessageEventSilentlySkipsSocialThreadReplyPingingAway(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.info":
			writeJSON(t, w, map[string]any{"ok": true, "channel": map[string]any{"id": "C123", "name": "social"}})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	router := newThreadRouterStub()
	router.prepareHandled = true
	router.submitHandled = true

	connector := newTestConnectorWithOptions(server.URL, bus, nil, router, nil)
	connector.botUserID = "U999"
	connector.config.SocialMode = config.TextSocialConfig{Enabled: true, Channels: []config.TextSocialChannelConfig{{Channel: "#social", Agent: "social", AllowedUserIDs: []string{"U123"}}}, ContextMessages: 2}

	ev := newSlackMessageEvent("171234.9999", "171234.5678", "<@U111> please check this")
	ev.Channel = "C123"
	ev.Message = &slack.Msg{Files: []slack.File{{URLPrivateDownload: server.URL + "/file.png", Mimetype: "image/png"}}}
	connector.handleMessageEvent(context.Background(), ev)

	assert.Empty(t, router.repliesSnapshot())
	assertNeverInbound(t, bus)
}

func TestHandleMessageEventRoutesSocialThreadReplyPingingBotToo(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var posted []url.Values

	server := newSlackStackTestServer(t, &posted, new([]string))
	defer server.Close()

	router := newThreadRouterStub()
	router.prepareHandled = true
	router.submitHandled = true
	connector := newTestConnectorWithOptions(server.URL, bus, nil, router, nil)
	connector.botUserID = "U999"
	connector.config.SocialMode = config.TextSocialConfig{Enabled: true, Channels: []config.TextSocialChannelConfig{{Channel: "#social", Agent: "social", AllowedUserIDs: []string{"U123"}}}, ContextMessages: 2}

	ev := newSlackMessageEvent("171234.9999", "171234.5678", "<@U111> <@U999> please check this")
	ev.Channel = "C123"
	connector.handleMessageEvent(context.Background(), ev)

	replies := router.repliesSnapshot()
	require.Len(t, replies, 1)
	assert.Equal(t, "<@U111> <@U999> please check this", replies[0].inbound.Text)
	require.Len(t, posted, 2)
	assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
}

func TestThreadedSocialMentionHandledOnceAndStripped(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var posted []url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.info":
			writeJSON(t, w, map[string]any{"ok": true, "channel": map[string]any{"id": "C123", "name": "social"}})
		case "/conversations.history":
			writeJSON(t, w, map[string]any{"ok": true, "messages": []map[string]any{}})
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "C123", "ts": "555.666", "text": posted[len(posted)-1].Get("text")})
		case "/reactions.add":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	router := newThreadRouterStub()
	router.submitHandled = true
	connector := newTestConnectorWithOptions(server.URL, bus, nil, router, nil)
	connector.botUserID = "U999"
	connector.config.SocialMode = config.TextSocialConfig{Enabled: true, Channels: []config.TextSocialChannelConfig{{Channel: "#social", Agent: "social", AllowedUserIDs: []string{"U123"}}}, ContextMessages: 2}

	mention := newSlackAppMentionEvent()
	mention.TimeStamp = "171234.9999"
	mention.ThreadTimeStamp = "171234.5678"
	mention.Text = "<@U999> -- where did that come from?"
	connector.handleAppMentionEvent(context.Background(), mention)

	message := newSlackMessageEvent("171234.9999", "171234.5678", "<@U999> -- where did that come from?")
	message.Channel = "C123"
	connector.handleMessageEvent(context.Background(), message)

	replies := router.repliesSnapshot()
	require.Len(t, replies, 1)
	assert.Equal(t, "-- where did that come from?", replies[0].inbound.Text)
	assert.Empty(t, router.startedSnapshot())
	require.Len(t, posted, 2)
	assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	assert.Equal(t, "171234.5678", posted[0].Get("thread_ts"))
	assertNeverInbound(t, bus)
}

func TestStripSlackBotMention(t *testing.T) {
	connector := newTestConnector("http://127.0.0.1")
	connector.botUserID = "U999"

	for _, tt := range []struct {
		name string
		text string
		want string
	}{
		{name: "plain mention", text: " <@U999> hello ", want: "hello"},
		{name: "aliased mention", text: "<@U999|Wallace> hello", want: "hello"},
		{name: "different mention", text: "<@U111> hello", want: "<@U111> hello"},
		{name: "empty text", text: " ", want: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, connector.stripSlackBotMention(tt.text))
		})
	}

	connector.botUserID = ""
	assert.Equal(t, "<@U999> hello", connector.stripSlackBotMention("<@U999> hello"))
}

func TestSocialModeAllowsUserChecksOnlyMatchingChannel(t *testing.T) {
	connector := newTestConnector("http://127.0.0.1")
	connector.config.SocialMode = config.TextSocialConfig{
		Channels: []config.TextSocialChannelConfig{
			{Channel: "#override", Agent: "override", AllowedUserIDs: []string{"U999"}},
			{Channel: "#team", Agent: "team", AllowedUserIDs: []string{"U123"}},
		},
	}

	assert.True(t, connector.socialModeAllowsUser("#override", "U999"))
	assert.False(t, connector.socialModeAllowsUser("#override", "U123"))
	assert.True(t, connector.socialModeAllowsUser("#team", "U123"))
	assert.False(t, connector.socialModeAllowsUser("#unknown", "U123"))
}

func TestSlackSocialThreadReplyPingsAway(t *testing.T) {
	connector := newTestConnector("http://127.0.0.1")
	connector.botUserID = "U999"

	for _, tt := range []struct {
		name string
		text string
		want bool
	}{
		{name: "user", text: "<@U111> please check", want: true},
		{name: "aliased user", text: "<@U111|Ada> please check", want: true},
		{name: "other bot counts as user", text: "<@B111> please check", want: true},
		{name: "channel reference", text: "<#C111|triage> please check", want: false},
		{name: "broadcast", text: "<!here> please check", want: true},
		{name: "user group", text: "<!subteam^S111|ops> please check", want: true},
		{name: "bot mention overrides user", text: "<@U111> <@U999> please check", want: false},
		{name: "aliased bot mention overrides channel", text: "<#C111|triage> <@U999|RocketClaw> please check", want: false},
		{name: "raw at word", text: "@human please check", want: false},
		{name: "date markup", text: "<!date^1712345678^{date_short}|today> please check", want: false},
		{name: "link markup", text: "<https://example.com|site> please check", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, connector.slackSocialThreadReplyPingsAway(tt.text))
		})
	}
}

func TestSocialPromptWithContextIncludesRecentMessages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !assert.Equal(t, "/conversations.history", r.URL.Path) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if !assert.NoError(t, r.ParseForm()) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		assert.Equal(t, "C123", r.FormValue("channel"))
		assert.Equal(t, "171234.5678", r.FormValue("latest"))
		assert.Equal(t, "3", r.FormValue("limit"))

		writeJSON(t, w, map[string]any{"ok": true, "messages": []map[string]any{
			{"text": "latest", "user": "U456"},
			{"text": "   ", "user": "U789"},
			{"text": "oldest"},
		}})
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	connector.config.SocialMode.ContextMessages = 3

	got := connector.socialPromptWithContext(context.Background(), "C123", "171234.5678", "hello")
	assert.Equal(t, "Recent Slack channel context before the mention:\n- oldest\n- <@U456>: latest\n\nMention:\nhello", got)
}

func TestSocialPromptWithContextKeepsOriginalTextForContextEdges(t *testing.T) {
	called := make(chan struct{}, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called <- struct{}{}

		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	connector.config.SocialMode.ContextMessages = 0
	assert.Equal(t, "hello", connector.socialPromptWithContext(context.Background(), "C123", "171234.5678", "hello"))

	select {
	case <-called:
		t.Fatal("socialPromptWithContext called Slack API with context disabled")
	default:
	}

	for _, tt := range []struct {
		name    string
		payload map[string]any
	}{
		{name: "api error", payload: map[string]any{"ok": false, "error": "fatal_error"}},
		{name: "empty history", payload: map[string]any{"ok": true, "messages": []map[string]any{}}},
		{name: "blank history", payload: map[string]any{"ok": true, "messages": []map[string]any{{"text": "   ", "user": "U456"}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called := make(chan struct{}, 1)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called <- struct{}{}

				assert.Equal(t, "/conversations.history", r.URL.Path)
				writeJSON(t, w, tt.payload)
			}))
			defer server.Close()

			connector := newTestConnector(server.URL)
			connector.config.SocialMode.ContextMessages = 2
			got := connector.socialPromptWithContext(context.Background(), "C123", "171234.5678", "hello")
			assert.Equal(t, "hello", got)

			select {
			case <-called:
			default:
				t.Fatal("socialPromptWithContext did not call Slack API")
			}
		})
	}
}

func TestHandleMessageEventIgnoresUnknownSocialThreadReply(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	router := newThreadRouterStub()
	connector := newTestConnectorWithOptions("http://127.0.0.1", bus, nil, router, nil)
	connector.config.SocialMode = config.TextSocialConfig{Enabled: true, ContextMessages: 2}

	ev := newSlackMessageEvent("171234.9999", "171234.5678", "follow up")
	ev.Channel = "C123"
	connector.handleMessageEvent(context.Background(), ev)

	assert.Empty(t, router.repliesSnapshot())
	assertNeverInbound(t, bus)
}

func TestHandleMessageEventRunsOnDemandCronInSlackThread(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var posted, deleted, updated []url.Values

	reactionCalls := 0
	router := newThreadRouterStub()
	router.submitHandled = true
	runner := newOneOffCronjobLoaderStub()
	runner.loaded = cronjob.OneOffCronjob{Agent: "cron", Prompt: "daily prompt", RelativePath: "cron/daily.md"}
	runner.runResult = cronjob.RunResult{Text: "normal text", VerbatimMessage: "final payload"}
	runner.onRun = func(ctx context.Context, progress *harnessbridge.RawRunProgress) {
		require.NoError(t, progress.Thinking(ctx, "thinking one"))
		require.NoError(t, progress.Thinking(ctx, "thinking two"))
		require.NoError(t, progress.Message(ctx, "assistant message"))
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.666", "text": posted[len(posted)-1].Get("text")})
		case "/chat.update":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			updated = append(updated, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": updated[len(updated)-1].Get("ts"), "text": updated[len(updated)-1].Get("text")})
		case "/chat.delete":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			deleted = append(deleted, cloneValues(r.PostForm))

			writeJSON(t, w, map[string]any{"ok": true})
		case "/reactions.add":
			reactionCalls++

			writeJSON(t, w, map[string]any{"ok": true})
		case "/reactions.remove":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, bus, config.ThreadAgents{":thread:": {Agent: "main", PreSeed: true}}, router, runner)
	connector.handleMessageEvent(context.Background(), newSlackMessageEvent("171234.5678", "", "🔂 daily"))

	assert.Equal(t, []string{"daily"}, runner.targetsSnapshot())
	require.Len(t, posted, 2)
	assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	assert.Equal(t, 1, reactionCalls)
	preview := readOneOutbound(t, bus)
	assert.Contains(t, preview.Text, "File: `cron/daily.md`")
	assert.Contains(t, preview.Text, "Agent: `cron`")
	assert.NotContains(t, preview.Text, "daily prompt")
	assert.False(t, preview.Complete)
	assert.True(t, preview.PostProgressText)
	require.NotNil(t, preview.SlackReply)
	assert.Equal(t, "171234.5678", preview.SlackReply.ThreadTS)
	require.NoError(t, connector.SendResponse(context.Background(), preview))
	require.Len(t, posted, 3)
	assert.Equal(t, preview.Text, posted[2].Get("text"))
	assert.Equal(t, "171234.5678", posted[2].Get("thread_ts"))

	thinking := readOneOutbound(t, bus)
	assert.Equal(t, "thinking one", thinking.ProgressText)
	assert.False(t, thinking.Complete)
	require.NotNil(t, thinking.SlackReply)
	assert.Equal(t, "171234.5678", thinking.SlackReply.ThreadTS)
	require.NoError(t, connector.SendResponse(context.Background(), thinking))
	require.Len(t, posted, 3)
	assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
	assert.Equal(t, "171234.5678", posted[0].Get("thread_ts"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	assert.Equal(t, "171234.5678", posted[1].Get("thread_ts"))

	thinking = readOneOutbound(t, bus)
	assert.Equal(t, "thinking one\nthinking two", thinking.ProgressText)
	require.NoError(t, connector.SendResponse(context.Background(), thinking))
	require.Len(t, posted, 3)
	require.NoError(t, connector.flushProgressText(context.Background(), thinking.TurnID))
	require.Len(t, updated, 1)
	assert.Contains(t, updated[0].Get("text"), "thinking one")
	assert.Contains(t, updated[0].Get("text"), "thinking two")

	message := readOneOutbound(t, bus)
	assert.Equal(t, "assistant message", message.Text)
	assert.True(t, message.PostProgressText)
	assert.False(t, message.Complete)
	require.NotNil(t, message.SlackReply)
	assert.Equal(t, "171234.5678", message.SlackReply.ThreadTS)
	require.NoError(t, connector.SendResponse(context.Background(), message))
	require.Len(t, posted, 4)
	assert.Equal(t, "assistant message", posted[3].Get("text"))
	assert.Equal(t, "171234.5678", posted[3].Get("thread_ts"))

	final := readOneOutbound(t, bus)
	assert.Equal(t, "final payload", final.Text)
	assert.True(t, final.Complete)
	require.NotNil(t, final.SlackReply)
	assert.Equal(t, "171234.5678", final.SlackReply.ThreadTS)
	require.NoError(t, connector.SendResponse(context.Background(), final))
	require.Len(t, posted, 4)
	require.Len(t, deleted, 1)
	assert.Equal(t, "555.666", deleted[0].Get("ts"))
	require.Len(t, updated, 2)
	assert.Equal(t, "final payload", updated[1].Get("text"))

	assert.Empty(t, router.startedSnapshot())
	assert.Equal(t, []cronjob.OneOffCronjob{{Agent: "cron", Prompt: "daily prompt", RelativePath: "cron/daily.md"}}, runner.runsSnapshot())
	assertNeverInbound(t, bus)
}

func TestHandleMessageEventRejectsInvalidOnDemandCronRequest(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var posted []url.Values

	router := newThreadRouterStub()
	runner := newOneOffCronjobLoaderStub()
	runner.errLoad = assert.AnError

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.666", "text": posted[len(posted)-1].Get("text")})
		case "/reactions.remove":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, bus, config.ThreadAgents{":thread:": {Agent: "main", PreSeed: true}}, router, runner)
	connector.handleMessageEvent(context.Background(), newSlackMessageEvent("171234.5678", "", ":repeat_one: ../bad"))

	assert.Empty(t, posted)
	assert.Equal(t, []string{"../bad"}, runner.targetsSnapshot())

	outbound := readOneOutbound(t, bus)
	assert.Equal(t, "I couldn't find that cronjob. Use a top-level cron filename like `daily` or `daily.md`.", outbound.Text)
	assert.True(t, outbound.Complete)
	require.NotNil(t, outbound.SlackReply)
	assert.Equal(t, "171234.5678", outbound.SlackReply.ThreadTS)
	require.NoError(t, connector.SendResponse(context.Background(), outbound))
	require.Len(t, posted, 1)
	assert.Equal(t, outbound.Text, posted[0].Get("text"))
	assert.Equal(t, "171234.5678", posted[0].Get("thread_ts"))
	assertNeverInbound(t, bus)
}

func TestHandleMessageEventReportsOnDemandCronRunFailure(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var posted, updated []url.Values

	router := newThreadRouterStub()
	runner := newOneOffCronjobLoaderStub()
	runner.loaded = cronjob.OneOffCronjob{Agent: "cron", Prompt: "daily prompt", RelativePath: "cron/daily.md"}
	runner.errRun = assert.AnError

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.666", "text": posted[len(posted)-1].Get("text")})
		case "/chat.update":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			updated = append(updated, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": updated[len(updated)-1].Get("ts"), "text": updated[len(updated)-1].Get("text")})
		case "/reactions.add":
			writeJSON(t, w, map[string]any{"ok": true})
		case "/reactions.remove":
			writeJSON(t, w, map[string]any{"ok": true})
		case "/chat.delete":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, bus, config.ThreadAgents{":thread:": {Agent: "main", PreSeed: true}}, router, runner)
	connector.handleMessageEvent(context.Background(), newSlackMessageEvent("171234.5678", "", "🔂 daily"))

	require.Len(t, posted, 2)
	assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	preview := readOneOutbound(t, bus)
	assert.Contains(t, preview.Text, "File: `cron/daily.md`")
	require.NoError(t, connector.SendResponse(context.Background(), preview))
	require.Len(t, posted, 3)
	assert.Equal(t, preview.Text, posted[2].Get("text"))
	assert.Equal(t, "171234.5678", posted[2].Get("thread_ts"))

	failure := readOneOutbound(t, bus)
	assert.Equal(t, "I couldn't run that on-demand cron right now.", failure.Text)
	require.NotNil(t, failure.SlackReply)
	assert.Equal(t, "171234.5678", failure.SlackReply.ThreadTS)
	require.NoError(t, connector.SendResponse(context.Background(), failure))
	require.Len(t, posted, 3)
	require.Len(t, updated, 1)
	assert.Equal(t, failure.Text, updated[0].Get("text"))
	assert.Empty(t, router.startedSnapshot())
}

func TestRunOnDemandCronIgnoresBlankProgressAndPublishesEmptyResultFallback(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	runner := newOneOffCronjobLoaderStub()
	runner.onRun = func(ctx context.Context, progress *harnessbridge.RawRunProgress) {
		require.NoError(t, progress.Thinking(ctx, " \t "))
		require.NoError(t, progress.Message(ctx, " \n "))
	}

	connector := newTestConnectorWithOptions("http://127.0.0.1", bus, nil, nil, runner)
	loaded := cronjob.OneOffCronjob{Agent: "cron", Prompt: "daily prompt", RelativePath: "cron/daily.md"}
	replyTarget := &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "171234.5678", ThreadTS: "171234.5678"}

	connector.runOnDemandCron(context.Background(), testLogger(), loaded, replyTarget, "turn-1")

	outbound := readOneOutbound(t, bus)
	assert.Equal(t, "Cronjob completed and decided to emit no human-visible output.", outbound.Text)
	assert.True(t, outbound.Complete)
	require.NotNil(t, outbound.SlackReply)
	assert.Equal(t, replyTarget, outbound.SlackReply)
	assert.Equal(t, []cronjob.OneOffCronjob{loaded}, runner.runsSnapshot())
}

func TestHandleMessageEventIgnoresUnknownThreadReplies(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	router := newThreadRouterStub()
	connector := newTestConnectorWithOptions("http://127.0.0.1", bus, config.ThreadAgents{":thread:": {Agent: "main", PreSeed: true}}, router, nil)
	connector.handleMessageEvent(context.Background(), newSlackMessageEvent("171234.9999", "171234.5678", "follow up"))

	assert.Empty(t, router.repliesSnapshot())
	assertNeverInbound(t, bus)
}

func TestHandleMessageEventSkipsThreadReplyWhenPrepareFails(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	router := newThreadRouterStub()
	router.errPrepare = errors.New("prepare failed")
	connector := newTestConnectorWithOptions("http://127.0.0.1", bus, nil, router, inertOneOffCronjobs{})

	connector.handleMessageEvent(context.Background(), newSlackMessageEvent("171234.9999", "171234.5678", "follow up"))

	assert.Empty(t, router.repliesSnapshot())
	assertNeverInbound(t, bus)
}

func TestHandleMessageEventSkipsResponseRootedThreadWhenPrepareFails(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	router := newThreadRouterStub()
	router.prepareResults = []bool{false}
	router.errPrepareResponse = errors.New("response prepare failed")
	connector := newTestConnectorWithOptions("http://127.0.0.1", bus, nil, router, inertOneOffCronjobs{})

	connector.handleMessageEvent(context.Background(), newSlackMessageEvent("171234.9999", "171234.5678", "follow up"))

	assert.Empty(t, router.repliesSnapshot())
	assertNeverInbound(t, bus)
}

func TestHandleMessageEventStartsResponseRootedThreadReply(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var posted []url.Values

	server := newSlackStackTestServer(t, &posted, new([]string))
	defer server.Close()

	router := newThreadRouterStub()
	router.prepareResults = []bool{false}
	router.prepareResponseHandled = true
	router.submitHandled = true
	connector := newTestConnectorWithOptions(server.URL, bus, config.ThreadAgents{":thread:": {Agent: "main", PreSeed: true}}, router, nil)
	connector.handleMessageEvent(context.Background(), newSlackMessageEvent("171234.9999", "171234.5678", "follow up"))

	replies := router.repliesSnapshot()
	require.Len(t, replies, 1)
	assert.Equal(t, "D123", replies[0].channelID)
	assert.Equal(t, "171234.5678", replies[0].threadTS)
	assert.Equal(t, "follow up", replies[0].inbound.Text)
	require.Len(t, posted, 2)
	assert.Equal(t, slackImmediatePlaceholder, posted[0].Get("text"))
	assert.Equal(t, slackAnswerPlaceholder, posted[1].Get("text"))
	assert.Equal(t, "171234.5678", posted[0].Get("thread_ts"))
}

func TestResolveManagedThreadTSFallsBackToSlackReactions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/reactions.get":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			assert.Equal(t, "D123", r.PostForm.Get("channel"))
			assert.Equal(t, "171234.9999", r.PostForm.Get("timestamp"))
			assert.Equal(t, "true", r.PostForm.Get("full"))
			writeJSON(t, w, map[string]any{"ok": true, "type": "message", "channel": "D123", "message": map[string]any{"ts": "171234.9999", "thread_ts": "171234.5678", "text": "follow up"}})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	bus := events.New()
	defer bus.Close()

	router := newThreadRouterStub()
	router.prepareResults = []bool{false, true}
	connector := newTestConnectorWithOptions(server.URL, bus, config.ThreadAgents{":thread:": {Agent: "main", PreSeed: true}}, router, nil)

	threadTS, handled, err := connector.resolveManagedThreadTS(context.Background(), "D123", "171234.9999")
	require.NoError(t, err)
	assert.True(t, handled)
	assert.Equal(t, "171234.5678", threadTS)
}

func TestResolveManagedThreadTSEdgeCases(t *testing.T) {
	errPrepare := errors.New("thread router unavailable")
	router := newThreadRouterStub()
	router.errPrepare = errPrepare
	connector := newTestConnectorWithOptions("http://slack.test", events.New(), nil, router, nil)

	_, _, err := connector.resolveManagedThreadTS(context.Background(), "D123", "171234.9999")
	require.ErrorIs(t, err, errPrepare)
	require.ErrorContains(t, err, "prepare Slack thread reply")

	tests := []struct {
		name     string
		response map[string]any
		wantErr  string
	}{
		{
			name:     "reactions error",
			response: map[string]any{"ok": false, "error": "ratelimited"},
			wantErr:  "load Slack message reactions",
		},
		{
			name:     "blank thread timestamp",
			response: map[string]any{"ok": true, "type": "message", "channel": "D123", "message": map[string]any{"ts": "171234.9999", "text": "reply"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/reactions.get", r.URL.Path)
				writeJSON(t, w, tt.response)
			}))
			defer server.Close()

			bus := events.New()
			defer bus.Close()

			connector := newTestConnectorWithOptions(server.URL, bus, nil, newThreadRouterStub(), nil)

			threadTS, handled, err := connector.resolveManagedThreadTS(context.Background(), "D123", "171234.9999")
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Empty(t, threadTS)
			assert.False(t, handled)
		})
	}
}

func TestHandleMessageEventReportsOldBotResponseWithoutCheckpoint(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var posted []url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.history":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
				return
			}

			writeJSON(t, w, map[string]any{"ok": true, "messages": []map[string]any{{"ts": "171234.5678", "bot_id": "B123", "text": "old answer"}}})
		case "/chat.postMessage":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
				return
			}

			posted = append(posted, cloneValues(r.PostForm))

			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "888.999"})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	router := newThreadRouterStub()
	connector := newTestConnectorWithOptions(server.URL, bus, config.ThreadAgents{":thread:": {Agent: "main", PreSeed: true}}, router, nil)
	connector.handleMessageEvent(context.Background(), newSlackMessageEvent("171234.9999", "171234.5678", "follow up"))

	require.Len(t, posted, 1)
	assert.Equal(t, "171234.5678", posted[0].Get("thread_ts"))
	assert.Contains(t, posted[0].Get("text"), "before thread checkpoints were recorded")
	assert.Empty(t, router.repliesSnapshot())
	assertNeverInbound(t, bus)
}

func TestIsBotAuthoredSlackMessageEdges(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	connector := newTestConnectorWithOptions("http://127.0.0.1", bus, nil, inertThreadRouter{}, inertOneOffCronjobs{})
	assert.False(t, connector.isBotAuthoredSlackMessage(context.Background(), " ", "171234.5678"))
	assert.False(t, connector.isBotAuthoredSlackMessage(context.Background(), "D123", " "))

	for _, tt := range []struct {
		name     string
		response map[string]any
		want     bool
	}{
		{name: "history error", response: map[string]any{"ok": false, "error": "ratelimited"}},
		{name: "empty history", response: map[string]any{"ok": true, "messages": []map[string]any{}}},
		{name: "bot user", response: map[string]any{"ok": true, "messages": []map[string]any{{"user": "UBOT"}}}, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/conversations.history", r.URL.Path)
				writeJSON(t, w, tt.response)
			}))
			defer server.Close()

			bus := events.New()
			defer bus.Close()

			connector := newTestConnectorWithOptions(server.URL, bus, nil, inertThreadRouter{}, inertOneOffCronjobs{})
			connector.botUserID = "UBOT"
			assert.Equal(t, tt.want, connector.isBotAuthoredSlackMessage(context.Background(), "D123", "171234.5678"))
		})
	}
}

func TestHandleEventsAPISummarizesThreadRootReaction(t *testing.T) {
	router := newThreadRouterStub()
	router.prepareHandled = true
	router.summarizeHandled = true

	var reactionCalls []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reactionCalls = append(reactionCalls, r.URL.Path+" "+r.FormValue("name"))

		writeJSON(t, w, map[string]any{"ok": true})
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, events.New(), config.ThreadAgents{":thread:": {Agent: "main", PreSeed: true}}, router, nil)
	for range 2 {
		connector.handleEventsAPI(context.Background(), newSlackEventsAPIEvent(
			newTestReactionAddedEvent("U123", slackSummaryReaction, "171234.5678"),
		))
	}

	summaries := router.summariesSnapshot()
	require.Len(t, summaries, 2)
	assert.Equal(t, "171234.5678", summaries[1].threadTS)

	want := []string{
		"/reactions.remove " + slackSummaryInProgressReaction,
		"/reactions.remove " + slackSummaryCompleteReaction,
		"/reactions.add " + slackSummaryInProgressReaction,
		"/reactions.remove " + slackSummaryInProgressReaction,
		"/reactions.add " + slackSummaryCompleteReaction,
	}
	assert.Equal(t, append(want, want...), reactionCalls)
}

func TestHandleReactionAddedEventSummarizesSocialThreadForAllowedUser(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	router := newThreadRouterStub()
	router.prepareHandled = true
	router.summarizeHandled = true

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.info":
			writeJSON(t, w, map[string]any{"ok": true, "channel": map[string]any{"id": "C123", "name": "social"}})
		case "/reactions.add", "/reactions.remove":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, bus, nil, router, nil)
	connector.config.SocialMode = config.TextSocialConfig{Enabled: true, Channels: []config.TextSocialChannelConfig{{Channel: "#social", Agent: "social", AllowedUserIDs: []string{"U456"}}}, ContextMessages: 1}

	ev := newTestReactionAddedEvent("U456", slackSummaryReaction, "171234.5678")
	ev.Item.Channel = "C123"
	connector.handleReactionAddedEvent(context.Background(), ev)

	summaries := router.summariesSnapshot()
	require.Len(t, summaries, 1)
	assert.Equal(t, "C123", summaries[0].channelID)
	assert.Equal(t, "171234.5678", summaries[0].threadTS)
}

func TestHandleReactionAddedEventUsesPerChannelAllowlist(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	router := newThreadRouterStub()
	router.prepareHandled = true
	router.summarizeHandled = true

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.info":
			writeJSON(t, w, map[string]any{"ok": true, "channel": map[string]any{"id": "C123", "name": "social"}})
		case "/reactions.add", "/reactions.remove":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, bus, nil, router, nil)
	connector.config.SocialMode = config.TextSocialConfig{Enabled: true, Channels: []config.TextSocialChannelConfig{{Channel: "#social", Agent: "social", AllowedUserIDs: []string{"U999"}}}, ContextMessages: 1}

	allowed := newTestReactionAddedEvent("U999", slackSummaryReaction, "171234.5678")
	allowed.Item.Channel = "C123"
	connector.handleReactionAddedEvent(context.Background(), allowed)

	denied := newTestReactionAddedEvent("U123", slackSummaryReaction, "171234.9999")
	denied.Item.Channel = "C123"
	connector.handleReactionAddedEvent(context.Background(), denied)

	summaries := router.summariesSnapshot()
	require.Len(t, summaries, 1)
	assert.Equal(t, "171234.5678", summaries[0].threadTS)
}

func TestHandleReactionAddedEventResolvesReplyThreadAndPostsFailure(t *testing.T) {
	router := newThreadRouterStub()
	router.prepareResults = []bool{false, true}
	router.summarizeHandled = true
	router.summarizeErr = assert.AnError

	posted := url.Values{}
	reactionCalls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/reactions.get":
			reactionCalls++

			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			writeJSON(t, w, map[string]any{
				"ok":      true,
				"type":    "message",
				"channel": "D123",
				"message": map[string]any{"ts": "171234.9999", "thread_ts": "171234.5678"},
			})
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = cloneValues(r.PostForm)

			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "888.999"})
		case "/reactions.add", "/reactions.remove":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, events.New(), config.ThreadAgents{":thread:": {Agent: "main", PreSeed: true}}, router, nil)
	connector.handleReactionAddedEvent(context.Background(), newTestReactionAddedEvent("U123", slackSummaryReaction, "171234.9999"))

	assert.Equal(t, 1, reactionCalls)

	summaries := router.summariesSnapshot()
	require.Len(t, summaries, 1)
	assert.Equal(t, "171234.5678", summaries[0].threadTS)
	assert.Equal(t, "D123", posted.Get("channel"))
	assert.Equal(t, "171234.5678", posted.Get("thread_ts"))
	assert.Equal(t, "I couldn't summarize this Slack thread right now.", posted.Get("text"))
}

func TestHandleReactionAddedEventStopsReplyThread(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var reactions []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/reactions.get":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			assert.Equal(t, "171234.9999", r.PostForm.Get("timestamp"))
			writeJSON(t, w, map[string]any{"ok": true, "type": "message", "channel": "D123", "message": map[string]any{"ts": "171234.9999", "thread_ts": "171234.5678", "text": "follow up"}})
		case "/reactions.add":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			reactions = append(reactions, r.URL.Path+" "+r.PostForm.Get("name")+" "+r.PostForm.Get("timestamp"))

			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	router := newThreadRouterStub()
	router.prepareResults = []bool{false, true}
	router.stopResult = &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "222.333", ThreadTS: "171234.5678"}
	connector := newTestConnectorWithOptions(server.URL, bus, nil, router, nil)

	connector.handleReactionAddedEvent(context.Background(), newTestReactionAddedEvent("U123", slackGoalStopSignReaction, "171234.9999"))

	assert.Equal(t, []goalThreadStopCall{{channelID: "D123", threadTS: "171234.5678"}}, router.goalStops)
	assert.Contains(t, reactions, "/reactions.add "+slackInterruptionReaction+" 222.333")
}

func TestHandleReactionAddedEventStopsMainWhenNoThreadResolved(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	var reactions []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/reactions.get":
			writeJSON(t, w, map[string]any{"ok": true, "type": "message", "channel": "D123", "message": map[string]any{"ts": "171234.9999", "text": "main reply"}})
		case "/reactions.add":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			reactions = append(reactions, r.URL.Path+" "+r.PostForm.Get("name")+" "+r.PostForm.Get("timestamp"))

			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	router := newThreadRouterStub()
	connector := newTestConnectorWithOptions(server.URL, bus, nil, router, nil)
	connector.interruptMainTurn = func() *events.InboundMessage {
		return &events.InboundMessage{SlackReply: &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "222.333"}}
	}

	connector.handleReactionAddedEvent(context.Background(), newTestReactionAddedEvent("U123", slackGoalStopSignReaction, "171234.9999"))

	assert.Empty(t, router.goalStops)
	assert.Contains(t, reactions, "/reactions.add "+slackInterruptionReaction+" 222.333")
}

func TestHandleReactionAddedEventRunsOnDemandCron(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	runner := newOneOffCronjobLoaderStub()
	runner.loaded = cronjob.OneOffCronjob{Agent: "cron", Prompt: "daily prompt", RelativePath: "cron/daily.md"}
	runner.runResult = cronjob.RunResult{VerbatimMessage: "done"}

	posted := 0
	reactions := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.history":
			writeJSON(t, w, map[string]any{"ok": true, "messages": []map[string]any{{"ts": "171234.5678", "text": ":repeat_one: daily"}}})
		case "/chat.postMessage":
			posted++
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": fmt.Sprintf("555.%d", posted)})
		case "/reactions.add":
			reactions++

			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, bus, nil, nil, runner)
	connector.handleReactionAddedEvent(context.Background(), newTestReactionAddedEvent("U123", slackOnDemandCronReaction, "171234.5678"))

	assert.Equal(t, []string{"daily"}, runner.targetsSnapshot())
	preview := readOneOutbound(t, bus)
	assert.Contains(t, preview.Text, "File: `cron/daily.md`")
	require.NotNil(t, preview.SlackReply)
	assert.Equal(t, "171234.5678", preview.SlackReply.ThreadTS)
	final := readOneOutbound(t, bus)
	assert.Equal(t, "done", final.Text)
	assert.True(t, final.Complete)
	assert.Equal(t, []cronjob.OneOffCronjob{runner.loaded}, runner.runsSnapshot())
	assert.Equal(t, 2, posted)
	assert.Equal(t, 1, reactions)
}

func TestHandleReactionAddedEventIgnoresUnauthorizedCronReaction(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	runner := newOneOffCronjobLoaderStub()

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, bus, nil, nil, runner)
	connector.handleReactionAddedEvent(context.Background(), newTestReactionAddedEvent("U999", slackOnDemandCronReaction, "171234.5678"))

	assert.Empty(t, runner.targetsSnapshot())
	assertNeverInbound(t, bus)
}

func TestHandleReactionAddedEventRejectsInvalidCronReactionTarget(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	runner := newOneOffCronjobLoaderStub()

	var posted []url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.history":
			writeJSON(t, w, map[string]any{"ok": true, "messages": []map[string]any{{"ts": "171234.5678", "text": "daily weekly"}}})
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))

			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555.666"})
		case "/reactions.remove":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, bus, nil, nil, runner)
	connector.handleReactionAddedEvent(context.Background(), newTestReactionAddedEvent("U123", slackOnDemandCronReaction, "171234.5678"))

	assert.Empty(t, runner.targetsSnapshot())
	outbound := readOneOutbound(t, bus)
	assert.Contains(t, outbound.Text, "exactly one cron target")
	require.NotNil(t, outbound.SlackReply)
	assert.Equal(t, "171234.5678", outbound.SlackReply.ThreadTS)
	require.NoError(t, connector.SendResponse(context.Background(), outbound))
	require.Len(t, posted, 1)
	assert.Equal(t, outbound.Text, posted[0].Get("text"))
	assert.Equal(t, "171234.5678", posted[0].Get("thread_ts"))
}

func TestHandleReactionAddedEventRejectsCronForDifferentChannel(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	runner := newOneOffCronjobLoaderStub()
	runner.loaded = cronjob.OneOffCronjob{Agent: "cron", Prompt: "daily prompt", RelativePath: "cron/daily.md", SlackChannel: "#ops"}

	var posted []url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.history":
			writeJSON(t, w, map[string]any{"ok": true, "messages": []map[string]any{{"ts": "171234.5678", "text": "daily"}}})
		case "/conversations.info":
			writeJSON(t, w, map[string]any{"ok": true, "channel": map[string]any{"id": "C123", "name": "triage"}})
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			posted = append(posted, cloneValues(r.PostForm))

			writeJSON(t, w, map[string]any{"ok": true, "channel": "C123", "ts": "555.666"})
		case "/reactions.remove":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, bus, nil, nil, runner)
	connector.config.SocialMode = config.TextSocialConfig{Enabled: true, Channels: []config.TextSocialChannelConfig{{Channel: "#triage", Agent: "triage", AllowedUserIDs: []string{"U456"}}}, ContextMessages: 1}
	ev := newTestReactionAddedEvent("U456", slackOnDemandCronReaction, "171234.5678")
	ev.Item.Channel = "C123"
	connector.handleReactionAddedEvent(context.Background(), ev)

	assert.Equal(t, []string{"daily"}, runner.targetsSnapshot())
	assert.Empty(t, runner.runsSnapshot())
	outbound := readOneOutbound(t, bus)
	assert.Equal(t, "That cronjob is not configured to run in this Slack channel.", outbound.Text)
	require.NotNil(t, outbound.SlackReply)
	assert.Equal(t, "C123", outbound.SlackReply.ChannelID)
	assert.Equal(t, "171234.5678", outbound.SlackReply.ThreadTS)
	require.NoError(t, connector.SendResponse(context.Background(), outbound))
	require.Len(t, posted, 1)
	assert.Equal(t, "C123", posted[0].Get("channel"))
	assert.Equal(t, "171234.5678", posted[0].Get("thread_ts"))
}

func TestHandleReactionAddedEventRerunsScheduledCronThreadRoot(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	runner := newOneOffCronjobLoaderStub()
	runner.loaded = cronjob.OneOffCronjob{Agent: "cron", Prompt: "daily prompt", RelativePath: "cron/daily.md", SlackChannel: "#triage"}
	runner.runResult = cronjob.RunResult{VerbatimMessage: "done"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.history":
			writeJSON(t, w, map[string]any{"ok": true, "messages": []map[string]any{{"ts": "171234.5678", "text": "Cronjob `cron/daily.md` ran at `2026-06-02T10:00:00Z` with agent `cron`."}}})
		case "/conversations.info":
			writeJSON(t, w, map[string]any{"ok": true, "channel": map[string]any{"id": "C123", "name": "triage"}})
		case "/chat.postMessage", "/reactions.add":
			writeJSON(t, w, map[string]any{"ok": true, "channel": "C123", "ts": "555.666"})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, bus, nil, nil, runner)
	connector.config.SocialMode = config.TextSocialConfig{Enabled: true, Channels: []config.TextSocialChannelConfig{{Channel: "#triage", Agent: "triage", AllowedUserIDs: []string{"U456"}}}, ContextMessages: 1}
	ev := newTestReactionAddedEvent("U456", slackOnDemandCronReaction, "171234.5678")
	ev.Item.Channel = "C123"
	connector.handleReactionAddedEvent(context.Background(), ev)

	assert.Equal(t, []string{"daily"}, runner.targetsSnapshot())
	preview := readOneOutbound(t, bus)
	require.NotNil(t, preview.SlackReply)
	assert.Equal(t, "C123", preview.SlackReply.ChannelID)
	assert.Equal(t, "171234.5678", preview.SlackReply.ThreadTS)
	final := readOneOutbound(t, bus)
	assert.Equal(t, "done", final.Text)
}

func TestHandleReactionAddedEventStopsWhenThreadResolutionFails(t *testing.T) {
	router := newThreadRouterStub()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/reactions.get":
			writeJSON(t, w, map[string]any{"ok": false, "error": "ratelimited"})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	bus := events.New()
	defer bus.Close()

	connector := newTestConnectorWithOptions(server.URL, bus, nil, router, inertOneOffCronjobs{})
	connector.handleReactionAddedEvent(context.Background(), newTestReactionAddedEvent("U123", slackSummaryReaction, "171234.9999"))

	assert.Empty(t, router.summariesSnapshot())
}

func TestHandleReactionAddedEventIgnoresNonHumanAndWrongReaction(t *testing.T) {
	bus := events.New()
	defer bus.Close()

	router := newThreadRouterStub()
	router.prepareHandled = true
	router.summarizeHandled = true

	connector := newTestConnectorWithOptions("http://127.0.0.1", bus, config.ThreadAgents{":thread:": {Agent: "main", PreSeed: true}}, router, nil)
	connector.config.SocialMode = config.TextSocialConfig{Enabled: true, ContextMessages: 1}

	connector.handleReactionAddedEvent(context.Background(), newTestReactionAddedEvent("U999", slackSummaryReaction, "171234.5678"))
	connector.handleReactionAddedEvent(context.Background(), newTestReactionAddedEvent("U123", slackRobotReaction, "171234.5678"))
	connector.handleReactionAddedEvent(context.Background(), newTestReactionAddedEvent("U456", slackSummaryReaction, "171234.5678"))

	ev := newTestReactionAddedEvent("U999", slackSummaryReaction, "171234.5678")
	ev.Item.Channel = "C123"
	connector.handleReactionAddedEvent(context.Background(), ev)
	connector.handleReactionAddedEvent(context.Background(), nil)

	ev = newTestReactionAddedEvent("U123", slackSummaryReaction, "171234.5678")
	ev.Item.Type = "file"
	connector.handleReactionAddedEvent(context.Background(), ev)

	ev = newTestReactionAddedEvent("U123", slackSummaryReaction, "171234.5678")
	ev.Item.Channel = " "
	connector.handleReactionAddedEvent(context.Background(), ev)

	ev = newTestReactionAddedEvent("U123", slackSummaryReaction, " ")
	connector.handleReactionAddedEvent(context.Background(), ev)

	assert.Empty(t, router.summariesSnapshot())
}

func newTestSlackMessageEvent() *slackevents.MessageEvent {
	message := new(slackevents.MessageEvent)
	message.User = "U123"
	message.Channel = "D123"
	message.TimeStamp = "171234.5678"
	message.Text = "hello."

	return message
}

func newTestReactionAddedEvent(user, reaction, timestamp string) *slackevents.ReactionAddedEvent {
	return &slackevents.ReactionAddedEvent{
		Type:           "reaction_added",
		User:           user,
		Reaction:       reaction,
		ItemUser:       "U123",
		Item:           slackevents.Item{Type: "message", Channel: "D123", Message: nil, File: nil, Comment: nil, Timestamp: timestamp},
		EventTimestamp: timestamp,
	}
}

func newTestConnector(apiURL string) *Connector {
	return newTestConnectorWithOptions(apiURL, nil, nil, nil, nil)
}

func newTestConnectorWithOptions(apiURL string, bus *events.Bus, threadAgents config.ThreadAgents, router harnessbridge.PrimaryTextRouter, runner oneOffCronjobRunner) *Connector {
	logger := testLogger()
	testConfig := new(config.Config)
	testConfig.Workspace = "/tmp/workspace"
	testConfig.OpenAI.APIKey = "test-key"
	testConfig.Slack.Enabled = true
	testConfig.Slack.BotToken = "xoxb-test"
	testConfig.Slack.AppToken = "xapp-test"
	testConfig.Slack.Room = "D123"

	testConfig.Slack.HumanUserID = "U123"
	if err := testConfig.Validate(); err != nil {
		panic(err)
	}

	if bus == nil {
		bus = events.New()
	}

	if router == nil {
		router = inertThreadRouter{}
	}

	if runner == nil {
		runner = inertOneOffCronjobs{}
	}

	connector := new(Connector)
	connector.log = logger
	connector.config = testConfig.Slack
	connector.bus = bus
	connector.threadAgents = normalizeThreadAgents(threadAgents)
	connector.threadRouter = router
	connector.oneOffCronjobs = runner
	connector.interruptMainTurn = func() *events.InboundMessage { return nil }
	connector.api = slack.New("xoxb-test", slack.OptionAPIURL(apiURL+"/"))
	connector.socketEvents = make(chan slackSocketEvent, 50)
	connector.newSocketClient = func(api *slack.Client) *socketmode.Client {
		return socketmode.New(api)
	}
	connector.runSocketClient = func(ctx context.Context, client *socketmode.Client) error {
		return client.RunContext(ctx)
	}
	connector.ackSocketEvent = func(client *socketmode.Client, req socketmode.Request) error {
		return client.Ack(req)
	}
	connector.reconnectDelay = time.Second
	connector.replies = map[string]slackReplySlots{}
	connector.pending = map[string]slackReplySlots{}
	connector.thinking = map[string]slackThinkingState{}
	connector.stacks = map[string][]slackBufferedMessage{}

	return connector
}

func newSlackMessageEvent(messageTS, threadTS, text string) *slackevents.MessageEvent {
	message := new(slackevents.MessageEvent)
	message.User = "U123"
	message.Channel = "D123"
	message.TimeStamp = messageTS
	message.ThreadTimeStamp = threadTS
	message.Text = text

	return message
}

func newSlackAppMentionEvent() *slackevents.AppMentionEvent {
	return &slackevents.AppMentionEvent{User: "U123", Channel: "C123", TimeStamp: "171234.5678", Text: "<@U999> please check this"}
}

func newSlackEventsAPIEvent(data any) socketmode.Event {
	return socketmode.Event{
		Data: slackevents.EventsAPIEvent{
			InnerEvent: slackevents.EventsAPIInnerEvent{Data: data},
		},
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func cloneValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, vals := range values {
		cloned[key] = append([]string(nil), vals...)
	}

	return cloned
}

func writeJSON(t *testing.T, w http.ResponseWriter, payload map[string]any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(payload))
}

func newSlackStackTestServer(t *testing.T, posted *[]url.Values, reactions *[]string) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.info":
			writeJSON(t, w, map[string]any{"ok": true, "channel": map[string]any{"id": "C123", "name": "social"}})
		case "/chat.postMessage":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			*posted = append(*posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": "D123", "ts": "555." + strconv.Itoa(len(*posted)), "text": (*posted)[len(*posted)-1].Get("text")})
		case "/chat.delete":
			writeJSON(t, w, map[string]any{"ok": true})
		case "/chat.update":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			*posted = append(*posted, cloneValues(r.PostForm))
			writeJSON(t, w, map[string]any{"ok": true, "channel": r.PostForm.Get("channel"), "ts": r.PostForm.Get("ts"), "text": r.PostForm.Get("text")})
		case "/reactions.add", "/reactions.remove":
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			*reactions = append(*reactions, r.URL.Path+" "+r.PostForm.Get("name")+" "+r.PostForm.Get("timestamp"))

			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
}

func readOneInbound(t *testing.T, bus *events.Bus) *events.InboundMessage {
	t.Helper()

	timeout := time.Second

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for msg := range bus.Inbound(ctx) {
		return msg
	}

	require.Failf(t, "timed out waiting for inbound message", "after %s", timeout)

	return nil
}

func readOneOutbound(t *testing.T, bus *events.Bus) *events.OutboundMessage {
	t.Helper()

	timeout := time.Second

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for msg := range bus.Outbound(ctx) {
		return msg
	}

	require.Failf(t, "timed out waiting for outbound message", "after %s", timeout)

	return nil
}

func assertNeverInbound(t *testing.T, bus *events.Bus) {
	t.Helper()

	bus.StopInbound()

	var messages []*events.InboundMessage
	for msg := range bus.Inbound(context.Background()) {
		messages = append(messages, msg)
	}

	require.Empty(t, messages, "unexpected inbound messages after inbound was stopped")
}

type threadRouterStub struct {
	mu                     sync.Mutex
	started                []threadStartCall
	replies                []threadReplyCall
	summaries              []threadSummaryCall
	checkpoints            []threadCheckpointCall
	cronRegistrations      []cronThreadRegistration
	goalStarts             []goalThreadStartCall
	goalStops              []goalThreadStopCall
	submitHandled          bool
	summarizeHandled       bool
	summarizeErr           error
	prepareHandled         bool
	prepareResponseHandled bool
	prepareResults         []bool
	errStart               error
	errSubmit              error
	errPrepare             error
	errPrepareResponse     error
	errCheckpoint          error
	stopResult             *events.SlackReplyTarget
	onStart                func()
	onReply                func()
}

func newThreadRouterStub() *threadRouterStub {
	return &threadRouterStub{}
}

type threadStartCall struct {
	channelID string
	threadTS  string
	agent     string
	preSeed   bool
	inbound   *events.InboundMessage
}

type threadReplyCall struct {
	channelID string
	threadTS  string
	inbound   *events.InboundMessage
}

type threadSummaryCall struct {
	channelID string
	threadTS  string
}

type threadCheckpointCall struct {
	channelID  string
	messageTS  string
	checkpoint events.ResponseCheckpoint
}

type cronThreadRegistration struct {
	channelID, threadTS, agent, seedText string
}

type goalThreadStartCall struct {
	agent       string
	objective   string
	checkScript string
	maxTurns    int
	inbound     *events.InboundMessage
}

type goalThreadStopCall struct {
	channelID string
	threadTS  string
}

func (s *threadRouterStub) StartThread(_ context.Context, agent string, preSeed bool, target events.TextConversationTarget, inbound *events.InboundMessage) error {
	if s.onStart != nil {
		s.onStart()
	}

	s.mu.Lock()
	s.started = append(s.started, threadStartCall{channelID: target.ChannelID, threadTS: target.ThreadID, agent: agent, preSeed: preSeed, inbound: inbound})
	errStart := s.errStart
	s.mu.Unlock()

	return errStart
}

func (s *threadRouterStub) StartGoalInThread(_ context.Context, agent, objective, checkScript string, maxTurns int, target events.TextConversationTarget, inbound *events.InboundMessage) error {
	s.mu.Lock()
	s.goalStarts = append(s.goalStarts, goalThreadStartCall{agent: agent, objective: objective, checkScript: checkScript, maxTurns: maxTurns, inbound: inbound})
	errStart := s.errStart
	s.mu.Unlock()

	_ = target

	return errStart
}

func (s *threadRouterStub) InterruptThread(target events.TextConversationTarget) (*events.InboundMessage, error) {
	s.mu.Lock()
	s.goalStops = append(s.goalStops, goalThreadStopCall{channelID: target.ChannelID, threadTS: target.ThreadID})
	result := s.stopResult
	s.mu.Unlock()

	if result == nil {
		result = &events.SlackReplyTarget{ChannelID: target.ChannelID, MessageTS: target.ThreadID, ThreadTS: target.ThreadID}
	}

	return &events.InboundMessage{SlackReply: result}, nil
}

func (s *threadRouterStub) RegisterCronThread(_ context.Context, target events.TextConversationTarget, agent, seedText string) error {
	s.mu.Lock()
	s.cronRegistrations = append(s.cronRegistrations, cronThreadRegistration{channelID: target.ChannelID, threadTS: target.ThreadID, agent: agent, seedText: seedText})
	errStart := s.errStart
	s.mu.Unlock()

	return errStart
}

func (s *threadRouterStub) PrepareThreadReply(target events.TextConversationTarget) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_ = target

	if s.errPrepare != nil {
		return false, s.errPrepare
	}

	if len(s.prepareResults) > 0 {
		result := s.prepareResults[0]
		s.prepareResults = s.prepareResults[1:]

		return result, nil
	}

	if s.prepareHandled {
		return true, nil
	}

	return s.submitHandled, nil
}

func (s *threadRouterStub) PrepareResponseThreadReply(target events.TextConversationTarget) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_ = target

	if s.errPrepareResponse != nil {
		return false, s.errPrepareResponse
	}

	return s.prepareResponseHandled, nil
}

func (s *threadRouterStub) SubmitThreadReply(_ context.Context, target events.TextConversationTarget, inbound *events.InboundMessage) (bool, error) {
	if s.onReply != nil {
		s.onReply()
	}

	s.mu.Lock()
	s.replies = append(s.replies, threadReplyCall{channelID: target.ChannelID, threadTS: target.ThreadID, inbound: inbound})
	s.mu.Unlock()

	return s.submitHandled, s.errSubmit
}

func (s *threadRouterStub) SubmitResponseThreadReply(ctx context.Context, target events.TextConversationTarget, inbound *events.InboundMessage) (bool, error) {
	return s.SubmitThreadReply(ctx, target, inbound)
}

func (s *threadRouterStub) SummarizeThread(_ context.Context, target events.TextConversationTarget) (bool, error) {
	s.mu.Lock()
	s.summaries = append(s.summaries, threadSummaryCall{channelID: target.ChannelID, threadTS: target.ThreadID})
	s.mu.Unlock()

	return s.summarizeHandled, s.summarizeErr
}

func (s *threadRouterStub) RecordResponseCheckpoint(target events.TextConversationTarget, checkpoint events.ResponseCheckpoint) error {
	s.mu.Lock()
	s.checkpoints = append(s.checkpoints, threadCheckpointCall{channelID: target.ChannelID, messageTS: target.MessageID, checkpoint: checkpoint})
	err := s.errCheckpoint
	s.mu.Unlock()

	return err
}

func (s *threadRouterStub) startedSnapshot() []threadStartCall {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]threadStartCall(nil), s.started...)
}

func (s *threadRouterStub) repliesSnapshot() []threadReplyCall {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]threadReplyCall(nil), s.replies...)
}

func (s *threadRouterStub) summariesSnapshot() []threadSummaryCall {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]threadSummaryCall(nil), s.summaries...)
}

func (s *threadRouterStub) checkpointsSnapshot() []threadCheckpointCall {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]threadCheckpointCall(nil), s.checkpoints...)
}

func (s *threadRouterStub) cronRegistrationsSnapshot() []cronThreadRegistration {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]cronThreadRegistration(nil), s.cronRegistrations...)
}

type oneOffCronjobLoaderStub struct {
	mu        sync.Mutex
	targets   []string
	loaded    cronjob.OneOffCronjob
	errLoad   error
	runs      []cronjob.OneOffCronjob
	runResult cronjob.RunResult
	errRun    error
	onRun     func(context.Context, *harnessbridge.RawRunProgress)
}

func newOneOffCronjobLoaderStub() *oneOffCronjobLoaderStub {
	return &oneOffCronjobLoaderStub{
		mu:        sync.Mutex{},
		targets:   nil,
		loaded:    cronjob.OneOffCronjob{Agent: "", Prompt: "", RelativePath: ""},
		errLoad:   nil,
		runs:      nil,
		runResult: cronjob.RunResult{Text: "", VerbatimMessage: ""},
		errRun:    nil,
		onRun:     nil,
	}
}

func (s *oneOffCronjobLoaderStub) LoadOneOffCronjob(target string) (cronjob.OneOffCronjob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.targets = append(s.targets, target)
	loaded := s.loaded
	err := s.errLoad

	return loaded, err
}

func (s *oneOffCronjobLoaderStub) RunOneOffCronjob(ctx context.Context, loaded cronjob.OneOffCronjob, progress *harnessbridge.RawRunProgress, finish func(context.Context, cronjob.RunResult, error)) {
	s.mu.Lock()
	s.runs = append(s.runs, loaded)
	onRun := s.onRun
	result := s.runResult
	err := s.errRun
	s.mu.Unlock()

	if onRun != nil {
		onRun(ctx, progress)
	}

	finish(ctx, result, err)
}

func (s *oneOffCronjobLoaderStub) targetsSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.targets...)
}

func (s *oneOffCronjobLoaderStub) runsSnapshot() []cronjob.OneOffCronjob {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]cronjob.OneOffCronjob(nil), s.runs...)
}
