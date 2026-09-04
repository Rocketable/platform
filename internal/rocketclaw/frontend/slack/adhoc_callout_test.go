package slackconnector

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func TestHandleAppMentionEventStartsUnmappedChannelWithAtFallback(t *testing.T) {
	router := newThreadRouterStub()

	server := newAdhocSlackServer(t, "random", nil)
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, newTestBus(), []config.SlackChannelConfig{
		{Channel: "#triage", Agents: []string{"triage"}, AllowedUserIDs: []string{"U999"}},
		{Channel: "@", Agents: []string{"adhoc", "factory"}, AllowedUserIDs: []string{"U123"}},
	}, router, nil)
	connector.botUserID = "U999"
	connector.handleAppMentionEvent(context.Background(), newSlackAppMentionEvent(), slackNativeForward{})

	started := convTurns(connector)
	require.Len(t, started, 1)
	assert.Equal(t, "adhoc", started[0].Agent)
	assert.Equal(t, "please check this", started[0].Text)
}

func TestHandleAppMentionEventUnmappedRootAgentDoesNotSwitch(t *testing.T) {
	router := newThreadRouterStub()

	server := newAdhocSlackServer(t, "random", nil)
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, newTestBus(), []config.SlackChannelConfig{
		{Channel: "@", Agents: []string{"adhoc", "factory"}, AllowedUserIDs: []string{"U123"}},
	}, router, nil)
	connector.botUserID = "U999"
	ev := newSlackAppMentionEvent()
	ev.Text = "<@U999> $agent factory hello"
	connector.handleAppMentionEvent(context.Background(), ev, slackNativeForward{})

	started := convTurns(connector)
	require.Len(t, started, 1)
	assert.Equal(t, "adhoc", started[0].Agent)
	assert.Equal(t, "$agent factory hello", started[0].Text)
}

func TestHandleAppMentionEventGroupDMUsesAtFallback(t *testing.T) {
	router := newThreadRouterStub()

	server := newAdhocSlackServer(t, "mpdm-users", nil)
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, newTestBus(), []config.SlackChannelConfig{
		{Channel: "@", Agents: []string{"adhoc"}, AllowedUserIDs: []string{"U123"}},
	}, router, nil)
	connector.botUserID = "U999"
	ev := newSlackAppMentionEvent()
	ev.Channel = "G123"
	connector.handleAppMentionEvent(context.Background(), ev, slackNativeForward{})

	started := convTurns(connector)
	require.Len(t, started, 1)
	assert.Equal(t, "adhoc", started[0].Agent)
}

func TestHandleMessageEventAdoptsUnmanagedThreadWithHistory(t *testing.T) {
	router := newThreadRouterStub()

	server := newAdhocSlackServer(t, "random", []map[string]any{
		{"ts": "171234.0001", "text": "old"},
		{"ts": "171234.0002", "text": "keep-1"},
		{"ts": "171234.9999", "text": "<@U999> jump in"},
	})
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, newTestBus(), []config.SlackChannelConfig{
		{Channel: "@", Agents: []string{"adhoc"}, AllowedUserIDs: []string{"U123"}},
	}, router, nil)
	connector.botUserID = "U999"
	ev := newSlackMessageEvent("171234.9999", "171234.0001", "<@U999> jump in")
	connector.handleMessageEvent(context.Background(), ev, slackNativeForward{})

	started := convTurns(connector)
	require.Len(t, started, 1)
	assert.Equal(t, "adhoc", started[0].Agent)
	_, threadTS, ok := protocol.SlackThreadTarget(started[0].ID)
	require.True(t, ok)
	assert.Equal(t, "171234.0001", threadTS)
	assert.Contains(t, started[0].Text, "jump in")
	assert.Contains(t, started[0].Text, "old")
	assert.Contains(t, started[0].Text, "keep-1")
}

func TestHandleMessageEventAdoptsBareBotMention(t *testing.T) {
	router := newThreadRouterStub()

	server := newAdhocSlackServer(t, "triage", []map[string]any{
		{"ts": "171234.0001", "text": "parent"},
		{"ts": "171234.9999", "text": "<@U999>"},
	})
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, newTestBus(), []config.SlackChannelConfig{
		{Channel: "#triage", Agents: []string{"triage"}, AllowedUserIDs: []string{"U123"}},
		{Channel: "@", Agents: []string{"adhoc"}, AllowedUserIDs: []string{"U123"}},
	}, router, nil)
	connector.botUserID = "U999"
	ev := newSlackMessageEvent("171234.9999", "171234.0001", "<@U999>")
	connector.handleMessageEvent(context.Background(), ev, slackNativeForward{})

	started := convTurns(connector)
	require.Len(t, started, 1)
	assert.Equal(t, "triage", started[0].Agent)
	assert.Contains(t, started[0].Text, "parent")
	assert.NotContains(t, started[0].Text, "<@U999>")
}

func TestHandleAppMentionEventIgnoresUnallowlistedAtHail(t *testing.T) {
	router := newThreadRouterStub()

	server := newAdhocSlackServer(t, "random", nil)
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, newTestBus(), []config.SlackChannelConfig{
		{Channel: "@", Agents: []string{"adhoc"}, AllowedUserIDs: []string{"U456"}},
	}, router, nil)
	connector.botUserID = "U999"
	connector.handleAppMentionEvent(context.Background(), newSlackAppMentionEvent(), slackNativeForward{})

	assert.Empty(t, convTurns(connector))
}

func TestHandleAppMentionEventIgnoresDirectMessageWithAtRow(t *testing.T) {
	router := newThreadRouterStub()
	connector := newTestConnectorWithOptions("http://127.0.0.1", newTestBus(), []config.SlackChannelConfig{
		{Channel: "@", Agents: []string{"adhoc"}, AllowedUserIDs: []string{"U123"}},
	}, router, nil)
	connector.botUserID = "U999"
	ev := newSlackAppMentionEvent()
	ev.Channel = "D123"
	connector.handleAppMentionEvent(context.Background(), ev, slackNativeForward{})

	assert.Empty(t, convTurns(connector))
}

func TestHandleMessageEventAdoptHistoryFetchFailureStillStarts(t *testing.T) {
	router := newThreadRouterStub()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.info":
			writeJSON(t, w, map[string]any{"ok": true, "channel": map[string]any{"id": "C123", "name": "random"}})
		case "/conversations.replies":
			writeJSON(t, w, map[string]any{"ok": false, "error": "internal_error"})
		case "/chat.startStream", "/chat.postMessage", "/chat.update", "/chat.delete", "/reactions.add", "/reactions.remove", "/conversations.history":
			writeJSON(t, w, map[string]any{"ok": true, "channel": "C123", "ts": "555.666", "messages": []map[string]any{}})
		case "/users.info":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, newTestBus(), []config.SlackChannelConfig{
		{Channel: "@", Agents: []string{"adhoc"}, AllowedUserIDs: []string{"U123"}},
	}, router, nil)
	connector.botUserID = "U999"
	ev := newSlackMessageEvent("171234.9999", "171234.0001", "<@U999> jump in")
	connector.handleMessageEvent(context.Background(), ev, slackNativeForward{})

	started := convTurns(connector)
	require.Len(t, started, 1)
	assert.Equal(t, "jump in", started[0].Text)
}

func TestHandleMessageEventAdoptHistoryKeepsNewestFifty(t *testing.T) {
	replies := make([]map[string]any, 0, 52)

	replies = append(replies, map[string]any{"ts": "171234.0000", "text": "drop-me"})
	for i := range 50 {
		replies = append(replies, map[string]any{"ts": fmt.Sprintf("171234.%04d", i+1), "text": "keep"})
	}

	replies = append(replies, map[string]any{"ts": "171234.9999", "text": "<@U999> jump in"})

	router := newThreadRouterStub()

	server := newAdhocSlackServer(t, "random", replies)
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, newTestBus(), []config.SlackChannelConfig{
		{Channel: "@", Agents: []string{"adhoc"}, AllowedUserIDs: []string{"U123"}},
	}, router, nil)
	connector.botUserID = "U999"
	ev := newSlackMessageEvent("171234.9999", "171234.0000", "<@U999> jump in")
	connector.handleMessageEvent(context.Background(), ev, slackNativeForward{})

	started := convTurns(connector)
	require.Len(t, started, 1)
	assert.NotContains(t, started[0].Text, "drop-me")
	assert.Contains(t, started[0].Text, "keep")
}

func TestHandleMessageEventAdoptStartErrorConsumesPlaceholder(t *testing.T) {
	router := newThreadRouterStub()

	server := newAdhocSlackServer(t, "random", []map[string]any{{"ts": "171234.0001", "text": "parent"}})
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, newTestBus(), []config.SlackChannelConfig{
		{Channel: "@", Agents: []string{"adhoc"}, AllowedUserIDs: []string{"U123"}},
	}, router, nil)
	connector.conv = inertConversationBackend{}
	connector.botUserID = "U999"
	ev := newSlackMessageEvent("171234.9999", "171234.0001", "<@U999> jump in")
	connector.handleMessageEvent(context.Background(), ev, slackNativeForward{})

	connector.mu.Lock()
	_, active := connector.stacks[slackThreadStackKey(&protocol.SlackReplyTarget{ChannelID: "C123", ThreadTS: "171234.0001"})]
	connector.mu.Unlock()
	assert.False(t, active)
}

func TestHandleAppMentionEventBareRootStillIgnored(t *testing.T) {
	router := newThreadRouterStub()

	server := newAdhocSlackServer(t, "random", nil)
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, newTestBus(), []config.SlackChannelConfig{
		{Channel: "@", Agents: []string{"adhoc"}, AllowedUserIDs: []string{"U123"}},
	}, router, nil)
	connector.botUserID = "U999"
	ev := newSlackAppMentionEvent()
	ev.Text = "<@U999>"
	connector.handleAppMentionEvent(context.Background(), ev, slackNativeForward{})

	assert.Empty(t, convTurns(connector))
}

func newAdhocSlackServer(t *testing.T, channelName string, replies []map[string]any) *httptest.Server {
	t.Helper()

	if replies == nil {
		replies = []map[string]any{}
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.info":
			writeJSON(t, w, map[string]any{"ok": true, "channel": map[string]any{"id": "C123", "name": channelName}})
		case "/conversations.history":
			writeJSON(t, w, map[string]any{"ok": true, "messages": []map[string]any{}})
		case "/conversations.replies":
			writeJSON(t, w, map[string]any{"ok": true, "messages": replies, "has_more": false})
		case "/chat.startStream", "/chat.postMessage", "/chat.update":
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
