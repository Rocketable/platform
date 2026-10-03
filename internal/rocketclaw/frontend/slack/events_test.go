package slackconnector

import (
	"context"
	"iter"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func TestStartEventsRoutesAndAcknowledgesSubscription(t *testing.T) {
	var (
		posted    []url.Values
		reactions []string
	)

	server := newSlackStackTestServer(t, &posted, &reactions)
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, nil, inertThreadRouter{})
	messages := []*protocol.OutboundMessage{
		{ConversationID: "web:private", Text: "not for Slack", Complete: true, SlackReply: &protocol.SlackReplyTarget{ChannelID: "CWRONG", MessageTS: "9.9"}},
		{ConversationID: "slack-thread:C123:111.0", Agent: "main", Text: "first answer", Complete: true},
		{ConversationID: "slack-thread:C456:222.0", Agent: "main", Text: "second answer", Complete: true, SlackReply: &protocol.SlackReplyTarget{ChannelID: "CWRONG", ThreadTS: "9.0", MessageTS: "222.1"}},
	}

	events := make([]protocol.Event, len(messages))
	for i, message := range messages {
		events[i] = protocol.Event{Message: message, Acknowledgement: make(chan error, 1)}
	}

	backend := &backendMock{SubscribeFunc: func(ctx context.Context) iter.Seq[protocol.Event] {
		assert.Equal(t, t.Context(), ctx)
		return slices.Values(events)
	}}
	done := connector.StartEvents(t.Context(), backend)
	require.Len(t, backend.SubscribeCalls(), 1, "subscribe before StartEvents returns")
	<-done

	for _, event := range events {
		require.Len(t, event.Acknowledgement, 1, "exactly one acknowledgement per event")
		require.NoError(t, <-event.Acknowledgement)
	}

	require.Len(t, posted, 2, "non-Slack output must not be delivered")
	assert.Equal(t, "C123", posted[0].Get("channel"))
	assert.Equal(t, "111.0", posted[0].Get("thread_ts"))
	assert.Contains(t, posted[0].Get("blocks"), `"text":"first answer"`)
	assert.Equal(t, "C456", posted[1].Get("channel"))
	assert.Equal(t, "222.0", posted[1].Get("thread_ts"))
	assert.Contains(t, posted[1].Get("blocks"), `"text":"second answer"`)
	assert.Equal(t, &protocol.SlackReplyTarget{ChannelID: "C123", ThreadTS: "111.0", MessageTS: "111.0"}, messages[1].SlackReply)
	assert.Equal(t, &protocol.SlackReplyTarget{ChannelID: "C456", ThreadTS: "222.0", MessageTS: "222.1"}, messages[2].SlackReply)
}

func TestStartEventsKeepsScheduledCronPrivateAndRepliesToOneOff(t *testing.T) {
	var posted []url.Values

	reactions := []string{}

	server := newSlackStackTestServer(t, &posted, &reactions)
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, nil, inertThreadRouter{})
	oneOff := &protocol.OutboundMessage{
		ConversationID: "slack-thread:C123:111.0",
		Text:           "one-off body",
		Complete:       true,
		Cronjob:        &protocol.CronjobMessage{RelativePath: "cron/daily.md", Agent: "planner", RanAt: "2000-01-02T03:04:05Z"},
	}
	private := &protocol.OutboundMessage{
		ConversationID: "cron:daily:run-1",
		Text:           "scheduled body",
		Complete:       true,
		Cronjob:        &protocol.CronjobMessage{RelativePath: "cron/daily.md", Agent: "planner", RanAt: "2000-01-02T03:04:05Z"},
	}
	events := []protocol.Event{
		{Message: oneOff, Acknowledgement: make(chan error, 1)},
		{Message: private, Acknowledgement: make(chan error, 1)},
	}
	backend := &backendMock{SubscribeFunc: func(context.Context) iter.Seq[protocol.Event] {
		return slices.Values(events)
	}}
	done := connector.StartEvents(t.Context(), backend)
	<-done
	require.NoError(t, <-events[0].Acknowledgement)
	require.NoError(t, <-events[1].Acknowledgement)

	assert.Equal(t, &protocol.SlackReplyTarget{ChannelID: "C123", ThreadTS: "111.0"}, oneOff.SlackReply)
	assert.Nil(t, private.SlackReply)
	require.Len(t, posted, 1)
	assert.Equal(t, "111.0", posted[0].Get("thread_ts"))
	assert.Contains(t, posted[0].Get("blocks"), "one-off body")
}

func TestStartEventsAcknowledgesDeliveryFailureAfterAborting(t *testing.T) {
	var (
		paths   []string
		deleted []url.Values
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if !assert.NoError(t, r.ParseForm()) {
			return
		}

		switch r.URL.Path {
		case "/chat.update":
			_, err := w.Write([]byte(`{"ok":false,"error":"update_failed"}`))
			assert.NoError(t, err)
		case "/chat.delete":
			deleted = append(deleted, cloneValues(r.PostForm))
			fallthrough
		case "/reactions.remove":
			_, err := w.Write([]byte(`{"ok":true}`))
			assert.NoError(t, err)
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, nil, inertThreadRouter{})
	connector.replies["turn-1"] = slackReplyState{ChannelID: "C123", MessageTS: "answer-1"}
	event := protocol.Event{
		Message:         &protocol.OutboundMessage{ConversationID: "slack-thread:C123:111.0", TurnID: "turn-1", Agent: "main", Text: "answer", Complete: true},
		Acknowledgement: make(chan error, 1),
	}
	backend := &backendMock{SubscribeFunc: func(ctx context.Context) iter.Seq[protocol.Event] {
		assert.Equal(t, t.Context(), ctx)
		return slices.Values([]protocol.Event{event})
	}}
	done := connector.StartEvents(t.Context(), backend)
	<-done
	require.Len(t, event.Acknowledgement, 1)
	require.ErrorContains(t, <-event.Acknowledgement, "update_failed")
	assert.Equal(t, []string{"/chat.update", "/chat.delete", "/reactions.remove"}, paths)
	require.Len(t, deleted, 1)
	assert.Equal(t, "C123", deleted[0].Get("channel"))
	assert.Equal(t, "answer-1", deleted[0].Get("ts"))
	assert.NotContains(t, connector.replies, "turn-1")
}

func TestStartEventsAcknowledgesConsumedInputFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/chat.postMessage", r.URL.Path)

		_, err := w.Write([]byte(`{"ok":false,"error":"channel_not_found"}`))
		assert.NoError(t, err)
	}))
	defer server.Close()

	message := protocol.NewOutboundMessage("slack-thread:C123:111.0", "")
	message.ConsumedID, message.ConsumedText, message.ConsumedSource = "web-input", "Hello there!", protocol.SourceWeb
	message.ConsumedRawText = "Hello there!"
	event := protocol.Event{Message: message, Acknowledgement: make(chan error, 1)}
	backend := &backendMock{SubscribeFunc: func(context.Context) iter.Seq[protocol.Event] {
		return slices.Values([]protocol.Event{event})
	}}
	connector := newTestConnector(server.URL)
	<-connector.StartEvents(t.Context(), backend)
	require.ErrorContains(t, <-event.Acknowledgement, "channel_not_found")
}
