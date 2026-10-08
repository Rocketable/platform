package slackconnector

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/slack-go/slack/slackevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func TestHandleAppMentionEventStartsUnmappedChannelWithAtFallback(t *testing.T) {
	router := newMentionRouter(false, nil)

	server := newAdhocSlackServer(t, "random", nil)
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, []config.SlackChannelConfig{
		{Channel: "#triage", Agents: []string{"triage"}, AllowedUserIDs: []string{"U999"}},
		{Channel: "@", Agents: []string{"adhoc", "factory"}, AllowedUserIDs: []string{"U123"}},
	}, router)
	connector.botUserID = "U999"
	connector.handleAppMentionEvent(context.Background(), newSlackAppMentionEvent(), slackNativeForward{})

	started := router.SubmitMentionCalls()
	require.Len(t, started, 1)
	assert.Equal(t, "adhoc", started[0].Agent)
	assert.Equal(t, "please check this", started[0].Inbound.Text)
}

func TestHandleAppMentionEventUnmappedRootAgentDoesNotSwitch(t *testing.T) {
	router := newMentionRouter(false, nil)

	server := newAdhocSlackServer(t, "random", nil)
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, []config.SlackChannelConfig{
		{Channel: "@", Agents: []string{"adhoc", "factory"}, AllowedUserIDs: []string{"U123"}},
	}, router)
	connector.botUserID = "U999"
	ev := newSlackAppMentionEvent()
	ev.Text = "<@U999> $agent factory hello"
	connector.handleAppMentionEvent(context.Background(), ev, slackNativeForward{})

	started := router.SubmitMentionCalls()
	require.Len(t, started, 1)
	assert.Equal(t, "adhoc", started[0].Agent)
	assert.Equal(t, "$agent factory hello", started[0].Inbound.Text)
}

func TestHandleAppMentionEventGroupDMUsesAtFallback(t *testing.T) {
	router := newMentionRouter(false, nil)

	server := newAdhocSlackServer(t, "mpdm-users", nil)
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, []config.SlackChannelConfig{
		{Channel: "@", Agents: []string{"adhoc"}, AllowedUserIDs: []string{"U123"}},
	}, router)
	connector.botUserID = "U999"
	ev := newSlackAppMentionEvent()
	ev.Channel = "G123"
	connector.handleAppMentionEvent(context.Background(), ev, slackNativeForward{})

	started := router.SubmitMentionCalls()
	require.Len(t, started, 1)
	assert.Equal(t, "adhoc", started[0].Agent)
}

func TestHandleAppMentionEventAdoptsUnknownThreadWithHistory(t *testing.T) {
	var writes []string

	router := newMentionRouter(false, nil)

	server := newAdhocSlackServer(t, "random", []map[string]any{
		{"ts": "171234.0001", "user": "U555", "text": "old"},
		{"ts": "171234.0002", "user": "U555", "text": "keep-1"},
		{"ts": "171234.9999", "user": "U123", "text": "<@U999> $review jump in"},
	}, &writes)
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, []config.SlackChannelConfig{
		{Channel: "@", Agents: []string{"adhoc"}, AllowedUserIDs: []string{"U123"}},
	}, router)
	connector.botUserID = "U999"
	connector.handleAppMentionEvent(context.Background(), newThreadMention("<@U999> $review jump in"), slackNativeForward{})

	started := router.SubmitMentionCalls()
	require.Len(t, started, 1)
	assert.Equal(t, "adhoc", started[0].Agent)
	assert.Equal(t, "171234.0001", started[0].Target.ThreadID)
	assert.Contains(t, started[0].Inbound.Text, "jump in")
	assert.Contains(t, started[0].Inbound.Text, "old")
	assert.Contains(t, started[0].Inbound.Text, "keep-1")
	assert.Equal(t, "$review jump in", started[0].Inbound.Metadata[protocol.InboundRawTextMetadataKey])
	assert.Equal(t, []string{"/reactions.add " + slackRobotReaction + " 171234.9999"}, writes, "the placeholder waits for the turn to start")
}

func TestHandleAppMentionEventAdoptsBareThreadMention(t *testing.T) {
	router := newMentionRouter(false, nil)

	server := newAdhocSlackServer(t, "triage", []map[string]any{
		{"ts": "171234.0001", "user": "U555", "text": "parent"},
		{"ts": "171234.9999", "user": "U123", "text": "<@U999>"},
	})
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, []config.SlackChannelConfig{
		{Channel: "#triage", Agents: []string{"triage"}, AllowedUserIDs: []string{"U123"}},
		{Channel: "@", Agents: []string{"adhoc"}, AllowedUserIDs: []string{"U123"}},
	}, router)
	connector.botUserID = "U999"
	connector.handleAppMentionEvent(context.Background(), newThreadMention("<@U999>"), slackNativeForward{})

	started := router.SubmitMentionCalls()
	require.Len(t, started, 1)
	assert.Equal(t, "triage", started[0].Agent)
	assert.Contains(t, started[0].Inbound.Text, "parent")
	assert.NotContains(t, started[0].Inbound.Text, "<@U999>")
}

// Cron and External MCP post their root before recording the thread, and startup prunes
// old records, so a mention under a root RocketClaw posted is a report-thread mention.
func TestHandleAppMentionEventIgnoresThreadUnderBotRoot(t *testing.T) {
	for name, root := range map[string]string{"cron report": "🔁 nightly.md | main", "External MCP relay": "📡 MCP request | ext-1 | main"} {
		t.Run(name, func(t *testing.T) {
			var writes []string

			router := newMentionRouter(false, nil)

			server := newAdhocSlackServer(t, "random", []map[string]any{
				{"ts": "171234.0001", "user": "U999", "bot_id": "B999", "text": root},
				{"ts": "171234.9999", "user": "U123", "text": "<@U999> why did this fail?"},
			}, &writes)
			defer server.Close()

			connector := newTestConnectorWithOptions(server.URL, []config.SlackChannelConfig{
				{Channel: "@", Agents: []string{"adhoc"}, AllowedUserIDs: []string{"U123"}},
			}, router)
			connector.botUserID = "U999"
			connector.handleAppMentionEvent(context.Background(), newThreadMention("<@U999> why did this fail?"), slackNativeForward{})

			assert.Empty(t, router.SubmitMentionCalls())
			assert.Empty(t, writes)
		})
	}
}

func TestHandleAppMentionEventRepliesWhenHistoryFetchFails(t *testing.T) {
	var writes []string

	router := newMentionRouter(false, nil)

	server := newAdhocSlackServer(t, "random", []map[string]any{{"ok": false, "error": "internal_error"}}, &writes)
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, []config.SlackChannelConfig{
		{Channel: "@", Agents: []string{"adhoc"}, AllowedUserIDs: []string{"U123"}},
	}, router)
	connector.botUserID = "U999"
	connector.handleAppMentionEvent(context.Background(), newThreadMention("<@U999> jump in"), slackNativeForward{})

	assert.Empty(t, router.SubmitMentionCalls())
	assert.Equal(t, []string{"/chat.postMessage I couldn't read this thread's earlier messages. 171234.0001"}, writes)
}

func TestHandleAppMentionEventAdoptHistoryKeepsNewestFifty(t *testing.T) {
	replies := make([]map[string]any, 0, 52)

	replies = append(replies, map[string]any{"ts": "171234.0000", "user": "U555", "text": "drop-me"})
	for i := range 50 {
		replies = append(replies, map[string]any{"ts": fmt.Sprintf("171234.%04d", i+1), "user": "U555", "text": "keep"})
	}

	replies = append(replies, map[string]any{"ts": "171234.9999", "user": "U123", "text": "<@U999> jump in"})

	router := newMentionRouter(false, nil)

	server := newAdhocSlackServer(t, "random", replies)
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, []config.SlackChannelConfig{
		{Channel: "@", Agents: []string{"adhoc"}, AllowedUserIDs: []string{"U123"}},
	}, router)
	connector.botUserID = "U999"
	ev := newThreadMention("<@U999> jump in")
	ev.ThreadTimeStamp = "171234.0000"
	connector.handleAppMentionEvent(context.Background(), ev, slackNativeForward{})

	started := router.SubmitMentionCalls()
	require.Len(t, started, 1)
	assert.NotContains(t, started[0].Inbound.Text, "drop-me")
	assert.Contains(t, started[0].Inbound.Text, "keep")
}

// A refused mention gets the failure reply; a committed one keeps the 🤖 even when its pickup failed.
func TestHandleAppMentionEventRepliesWhenSubmitFails(t *testing.T) {
	for _, tt := range []struct {
		name     string
		accepted bool
		writes   []string
	}{
		{name: "refused", writes: []string{"/chat.postMessage I couldn't take that request: " + assert.AnError.Error() + " 171234.0001"}},
		{name: "committed", accepted: true, writes: []string{"/reactions.add " + slackRobotReaction + " 171234.9999"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var writes []string

			router := newMentionRouter(false, nil)
			router.SubmitMentionFunc = func(context.Context, string, protocol.TextConversationTarget, *protocol.InboundMessage) (bool, error) {
				return tt.accepted, assert.AnError
			}

			server := newAdhocSlackServer(t, "random", []map[string]any{{"ts": "171234.0001", "user": "U555", "text": "parent"}}, &writes)
			defer server.Close()

			connector := newTestConnectorWithOptions(server.URL, []config.SlackChannelConfig{
				{Channel: "@", Agents: []string{"adhoc"}, AllowedUserIDs: []string{"U123"}},
			}, router)
			connector.botUserID = "U999"
			connector.handleAppMentionEvent(context.Background(), newThreadMention("<@U999> jump in"), slackNativeForward{})

			require.Len(t, router.SubmitMentionCalls(), 1)
			assert.Equal(t, tt.writes, writes)
		})
	}
}

func TestHandleAppMentionEventIgnoresUnallowlistedAtHail(t *testing.T) {
	router := newMentionRouter(false, nil)

	server := newAdhocSlackServer(t, "random", nil)
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, []config.SlackChannelConfig{
		{Channel: "@", Agents: []string{"adhoc"}, AllowedUserIDs: []string{"U456"}},
	}, router)
	connector.botUserID = "U999"
	connector.handleAppMentionEvent(context.Background(), newSlackAppMentionEvent(), slackNativeForward{})

	assert.Empty(t, router.SubmitMentionCalls())
}

func TestHandleAppMentionEventIgnoresDirectMessageWithAtRow(t *testing.T) {
	router := newMentionRouter(false, nil)
	connector := newTestConnectorWithOptions("http://127.0.0.1", []config.SlackChannelConfig{
		{Channel: "@", Agents: []string{"adhoc"}, AllowedUserIDs: []string{"U123"}},
	}, router)
	connector.botUserID = "U999"
	ev := newSlackAppMentionEvent()
	ev.Channel = "D123"
	connector.handleAppMentionEvent(context.Background(), ev, slackNativeForward{})

	assert.Empty(t, router.SubmitMentionCalls())
}

func TestHandleAppMentionEventBareRootStillIgnored(t *testing.T) {
	router := newMentionRouter(false, nil)

	server := newAdhocSlackServer(t, "random", nil)
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, []config.SlackChannelConfig{
		{Channel: "@", Agents: []string{"adhoc"}, AllowedUserIDs: []string{"U123"}},
	}, router)
	connector.botUserID = "U999"
	ev := newSlackAppMentionEvent()
	ev.Text = "<@U999>"
	connector.handleAppMentionEvent(context.Background(), ev, slackNativeForward{})

	assert.Empty(t, router.SubmitMentionCalls())
}

func newAdhocSlackServer(t *testing.T, channelName string, replies []map[string]any, writes ...*[]string) *httptest.Server {
	t.Helper()

	if replies == nil {
		replies = []map[string]any{}
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(writes) > 0 && r.URL.Path != "/conversations.info" && r.URL.Path != "/conversations.replies" && r.URL.Path != "/users.info" {
			if !assert.NoError(t, r.ParseForm()) {
				return
			}

			*writes[0] = append(*writes[0], strings.Join([]string{r.URL.Path, r.PostForm.Get("text") + r.PostForm.Get("name"), r.PostForm.Get("thread_ts") + r.PostForm.Get("timestamp")}, " "))
		}

		switch r.URL.Path {
		case "/conversations.info":
			writeJSON(t, w, map[string]any{"ok": true, "channel": map[string]any{"id": "C123", "name": channelName}})
		case "/conversations.history":
			writeJSON(t, w, map[string]any{"ok": true, "messages": []map[string]any{}})
		case "/conversations.replies":
			if replies[0]["ok"] == false {
				writeJSON(t, w, replies[0])
				return
			}

			writeJSON(t, w, map[string]any{"ok": true, "messages": replies, "has_more": false})
		case "/chat.postMessage", "/chat.update":
			writeJSON(t, w, map[string]any{"ok": true, "channel": "C123", "ts": "555.666"})
		case "/chat.delete":
			writeJSON(t, w, map[string]any{"ok": true})
		case "/reactions.add", "/reactions.remove":
			writeJSON(t, w, map[string]any{"ok": true})
		case "/users.info":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
}

func newThreadMention(text string) *slackevents.AppMentionEvent {
	ev := newSlackAppMentionEvent()
	ev.TimeStamp, ev.ThreadTimeStamp, ev.Text = "171234.9999", "171234.0001", text

	return ev
}
