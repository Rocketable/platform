package slackconnector

import (
	"context"
	"iter"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"

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

	router := &primaryTextRouterMock{
		MentionThreadFunc: func(protocol.TextConversationTarget) (bool, error) { return true, nil },
		WebURLFunc:        func(string) (string, error) { return testFooterLink, nil },
	}
	connector := newTestConnectorWithOptions(server.URL, testSocialChannels(), router)
	messages := []*protocol.OutboundMessage{
		{ConversationID: "web:private", Text: "not for Slack", Complete: true, SlackReply: &protocol.SlackReplyTarget{ChannelID: "CWRONG", MessageTS: "9.9"}},
		{ConversationID: "slack-thread:C123:111.0", TurnID: "turn-web", Agent: "main", Text: "first answer", Complete: true, Terminal: protocol.TerminalComplete},
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
	assert.Contains(t, posted[0].Get("blocks"), `{"type":"context","elements":[{"type":"mrkdwn","text":"main · done · \u003c`+testFooterLink+`|Open in Web\u003e"}]}]`)
	assert.Equal(t, "C456", posted[1].Get("channel"))
	assert.Equal(t, "222.0", posted[1].Get("thread_ts"))
	assert.Contains(t, posted[1].Get("blocks"), `"text":"second answer"`)
	assert.Equal(t, &protocol.SlackReplyTarget{ChannelID: "C123", ThreadTS: "111.0", MessageTS: "111.0"}, messages[1].SlackReply)
	assert.Equal(t, &protocol.SlackReplyTarget{ChannelID: "C456", ThreadTS: "222.0", MessageTS: "222.1"}, messages[2].SlackReply)
}

func TestStartEventsAcknowledgesDeliveryFailureAfterAborting(t *testing.T) {
	var paths []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if !assert.NoError(t, r.ParseForm()) {
			return
		}

		switch r.URL.Path {
		case "/chat.postMessage":
			_, err := w.Write([]byte(`{"ok":false,"error":"post_failed"}`))
			assert.NoError(t, err)
		case "/agents.sessions.setStatus":
			assert.Equal(t, "active", r.PostForm.Get("status"))

			_, err := w.Write([]byte(`{"ok":true}`))
			assert.NoError(t, err)
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	defer server.Close()

	connector := newTestConnectorWithOptions(server.URL, testSocialChannels(), inertThreadRouter{})
	connector.sessions["turn-1"] = &slackSession{conversationID: "slack-thread:C123:111.0", poll: time.NewTimer(time.Hour)}
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
	require.ErrorContains(t, <-event.Acknowledgement, "post_failed")
	assert.Equal(t, []string{"/chat.postMessage", "/agents.sessions.setStatus"}, paths)
	assert.Empty(t, connector.sessions)
}

func TestStartEventsAcknowledgesThreadLookupFailure(t *testing.T) {
	connector := newTestConnector("http://127.0.0.1")
	connector.facts.(*channelFactsStoreMock).SlackThreadFunc = func(context.Context, string) (string, string, bool, error) {
		return "", "", false, assert.AnError
	}
	event := protocol.Event{Message: &protocol.OutboundMessage{ConversationID: "slack-thread:C123:111.0", TurnID: "turn-1", Text: "answer", Complete: true}, Acknowledgement: make(chan error, 1)}
	backend := &backendMock{SubscribeFunc: func(context.Context) iter.Seq[protocol.Event] {
		return slices.Values([]protocol.Event{event})
	}}

	<-connector.StartEvents(t.Context(), backend)
	require.ErrorIs(t, <-event.Acknowledgement, assert.AnError)
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

func TestStartEventsPostsChannelSessionTurnsAtTheTop(t *testing.T) {
	var (
		posted []url.Values
		writes []string
	)

	server := newSlackStackTestServer(t, &posted, &writes)
	defer server.Close()

	var linked []string

	connector := newTestConnectorWithOptions(server.URL, testSocialChannels(), &primaryTextRouterMock{WebURLFunc: func(conversationID string) (string, error) {
		linked = append(linked, conversationID)
		return testFooterLink, nil
	}})

	const channelSession = "slack-channel:C123:main"

	mirror := protocol.NewOutboundMessage(channelSession, "")
	mirror.ConsumedID, mirror.ConsumedText, mirror.ConsumedRawText, mirror.ConsumedSource = "web-input", "from the Web", "from the Web", protocol.SourceWeb
	mention := &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.222", ThreadTS: "111.222"}
	turn := func(turnID, text string, reply *protocol.SlackReplyTarget, quiet, complete bool) *protocol.OutboundMessage {
		return &protocol.OutboundMessage{ConversationID: channelSession, TurnID: turnID, Agent: "main", Text: text, Complete: complete, Terminal: protocol.TerminalComplete, SlackReply: protocol.Clone(reply), ChannelQuiet: quiet}
	}
	messages := []*protocol.OutboundMessage{
		mirror,
		turn("turn-web", "web answer", nil, false, true),
		turn("turn-mention", "", mention, false, false),
		turn("turn-mention", "mention answer", mention, false, true),
		turn("turn-job", "job answer", nil, false, true),
		turn("turn-reply", "", mention, true, false),
		turn("turn-reply", "reply answer", mention, true, true),
		{ConversationID: channelSession + "/call-1", TurnID: "turn-sub", Text: "subagent answer", Complete: true},
	}

	events := make([]protocol.Event, len(messages))
	for i, message := range messages {
		events[i] = protocol.Event{Message: message, Acknowledgement: make(chan error, 1)}
	}

	<-connector.StartEvents(t.Context(), &backendMock{SubscribeFunc: func(context.Context) iter.Seq[protocol.Event] { return slices.Values(events) }})

	for _, event := range events {
		require.NoError(t, <-event.Acknowledgement)
	}

	texts := make([]string, len(posted))
	for i, post := range posted {
		texts[i] = post.Get("text")

		assert.Equal(t, "C123", post.Get("channel"))
		assert.Empty(t, post.Get("thread_ts"), "posted at the top of the channel")
		assert.JSONEq(t, `{"event_type":"rocketclaw_channel_reply","event_payload":{}}`, post.Get("metadata"))
	}

	assert.Equal(t, []string{"📡 web\nfrom the Web", "web answer", "mention answer", "job answer"}, texts, "session-reply wakes and subagents post nothing")

	footer, _ := slackContextTexts(t, posted[1].Get("blocks"))
	assert.Equal(t, []string{"main · done · <" + testFooterLink + "|Open in Web>"}, footer)
	assert.Equal(t, []string{channelSession, channelSession, channelSession}, linked, "each footer links the Channel Session")
	assert.Equal(t, []string{"/agents.sessions.setStatus processing 111.222", "/agents.sessions.setStatus active 111.222"}, writes, "the status shows on the mention's thread")
}
