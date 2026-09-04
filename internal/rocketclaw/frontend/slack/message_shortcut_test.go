package slackconnector

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

type messageActionsView struct {
	TriggerID string `json:"trigger_id"`
	View      struct {
		CallbackID string `json:"callback_id"`
		Blocks     []struct {
			Type     string                `json:"type"`
			Text     struct{ Text string } `json:"text"`
			Elements []struct {
				ActionID string                `json:"action_id"`
				Text     struct{ Text string } `json:"text"`
			} `json:"elements"`
		} `json:"blocks"`
	} `json:"view"`
}

type messageActionsRecorder struct {
	URL     string
	mu      sync.Mutex
	opened  []messageActionsView
	updated []messageActionsView
}

func (r *messageActionsRecorder) lastOpened() messageActionsView {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.opened[len(r.opened)-1]
}

func newMessageActionsRecorder(t *testing.T) *messageActionsRecorder {
	t.Helper()

	rec := &messageActionsRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.info":
			writeJSON(t, w, map[string]any{"ok": true, "channel": map[string]any{"id": "C123", "name": "social"}})
		case "/views.open", "/views.update":
			body, errRead := io.ReadAll(r.Body)
			if !assert.NoError(t, errRead) {
				return
			}

			var view messageActionsView
			if !assert.NoError(t, json.Unmarshal(body, &view)) {
				return
			}

			rec.mu.Lock()
			if r.URL.Path == "/views.open" {
				rec.opened = append(rec.opened, view)
			} else {
				rec.updated = append(rec.updated, view)
			}
			rec.mu.Unlock()
			writeJSON(t, w, map[string]any{"ok": true, "view": map[string]any{"id": "V-actions"}})
		case "/reactions.add", "/reactions.remove":
			writeJSON(t, w, map[string]any{"ok": true})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	rec.URL = server.URL

	return rec
}

func TestHandleMessageShortcutUnauthorizedIsSilent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
	}))
	defer server.Close()

	connector := newTestConnector(server.URL)
	connector.handleInteractive(t.Context(), newMessageShortcutEvent("U999", "111.2"))
}

func TestHandleMessageShortcutUnmanagedExplains(t *testing.T) {
	rec := newMessageActionsRecorder(t)
	connector := newTestConnector(rec.URL)

	connector.handleInteractive(t.Context(), newMessageShortcutEvent("U123", "111.2"))

	require.Len(t, rec.opened, 1)
	assert.Equal(t, slackMessageShortcutCallbackID, rec.lastOpened().View.CallbackID)
	assert.Equal(t, "This message isn't part of a RocketClaw conversation.", rec.lastOpened().View.Blocks[0].Text.Text)
	assert.Empty(t, rec.lastOpened().View.Blocks[0].Elements)
}

func TestHandleMessageShortcutManagedWithoutControlsExplains(t *testing.T) {
	rec := newMessageActionsRecorder(t)
	router := newThreadRouterStub()
	router.prepareHandled = true
	connector := newTestConnectorWithOptions(rec.URL, newTestBus(), nil, router, nil)

	connector.handleInteractive(t.Context(), newMessageShortcutEvent("U123", "111.2"))

	require.Len(t, rec.opened, 1)
	assert.Equal(t, "No RocketClaw actions on this message.", rec.lastOpened().View.Blocks[0].Text.Text)
	assert.Empty(t, rec.lastOpened().View.Blocks[0].Elements)
}

func TestHandleMessageShortcutThinkingOffersInterrupt(t *testing.T) {
	rec := newMessageActionsRecorder(t)
	router := newThreadRouterStub()
	router.prepareHandled = true
	connector := newTestConnectorWithOptions(rec.URL, newTestBus(), nil, router, nil)
	connector.pending["k"] = slackReplySlots{ChannelID: "C123", ThinkingTS: "999.1", AnswerTS: "999.2"}

	connector.handleInteractive(t.Context(), newMessageShortcutEvent("U123", "999.1"))

	require.Len(t, rec.opened, 1)
	require.Len(t, rec.lastOpened().View.Blocks[0].Elements, 1)
	assert.Equal(t, slackMessageActionInterrupt, rec.lastOpened().View.Blocks[0].Elements[0].ActionID)
}

func TestHandleMessageShortcutInterruptClickStopsTurn(t *testing.T) {
	rec := newMessageActionsRecorder(t)
	router := newThreadRouterStub()
	router.stopResult = &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "999.1", ThreadTS: "111.0"}
	connector := newTestConnectorWithOptions(rec.URL, newTestBus(), nil, router, nil)
	connector.pending["k"] = slackReplySlots{ChannelID: "C123", ThinkingTS: "999.1", AnswerTS: "999.2"}

	connector.handleInteractive(t.Context(), newMessageActionsButtonEvent(slackMessageActionInterrupt, &rocketclawActionsMetadata{ChannelID: "C123", MessageTS: "999.1", ThreadTS: "111.0"}))

	assert.Equal(t, []string{"slack-thread:C123:111.0"}, router.conversationStops)
	require.Len(t, rec.updated, 1)
	assert.Equal(t, "Interrupted the turn.", rec.updated[0].View.Blocks[0].Text.Text)
}

func TestHandleMessageShortcutEnvelopeOffersCancelAndSteer(t *testing.T) {
	rec := newMessageActionsRecorder(t)
	router := newThreadRouterStub()
	router.prepareHandled = true
	router.busy = true
	router.queue = []protocol.ThreadQueueItem{{ID: "q1", Message: "later", SlackChannel: "C123", SlackTS: "111.2"}}
	connector := newTestConnectorWithOptions(rec.URL, newTestBus(), nil, router, nil)

	connector.handleInteractive(t.Context(), newMessageShortcutEvent("U123", "111.2"))

	require.Len(t, rec.opened, 1)
	ids := actionIDs(rec.lastOpened())
	assert.Equal(t, []string{slackMessageActionCancel, slackMessageActionSteer}, ids)
}

func TestHandleMessageShortcutSteerClickConvertsEnqueue(t *testing.T) {
	rec := newMessageActionsRecorder(t)
	router := newThreadRouterStub()
	router.prepareHandled = true
	router.busy = true
	router.queue = []protocol.ThreadQueueItem{{ID: "q1", Message: "later", SlackChannel: "C123", SlackTS: "111.2"}}
	connector := newTestConnectorWithOptions(rec.URL, newTestBus(), nil, router, nil)
	connector.handleInteractive(t.Context(), newMessageActionsButtonEvent(slackMessageActionSteer, &rocketclawActionsMetadata{ChannelID: "C123", MessageTS: "111.2", ThreadTS: "111.2"}))
	require.Len(t, rec.updated, 1)
	assert.Equal(t, "Converted to a steer.", rec.updated[0].View.Blocks[0].Text.Text)
	require.Equal(t, protocol.InboundKindSteer, router.queueSnapshot()[0].Kind)
}

func TestConvertQueuedEnvelopePromoteError(t *testing.T) {
	rec := newMessageActionsRecorder(t)
	router := newThreadRouterStub()
	router.prepareHandled = true
	router.busy = true
	router.errQueue = errors.New("promote failed")
	router.queue = []protocol.ThreadQueueItem{{ID: "q1", Message: "later", SlackChannel: "C123", SlackTS: "111.2"}}
	connector := newTestConnectorWithOptions(rec.URL, newTestBus(), nil, router, nil)
	connector.handleInteractive(t.Context(), newMessageActionsButtonEvent(slackMessageActionSteer, &rocketclawActionsMetadata{ChannelID: "C123", MessageTS: "111.2", ThreadTS: "111.2"}))
	require.Len(t, rec.updated, 1)
	assert.Equal(t, "No RocketClaw actions on this message.", rec.updated[0].View.Blocks[0].Text.Text)
}

func TestFindQueuedEnvelopeResolvesMissingThreadTS(t *testing.T) {
	router := newThreadRouterStub()
	router.prepareHandled = true
	router.queue = []protocol.ThreadQueueItem{{ID: "q1", SlackChannel: "C123", SlackTS: "111.2"}}
	connector := newTestConnectorWithOptions("http://slack.test", nil, nil, router, nil)
	target, item, ok := connector.findQueuedEnvelope(t.Context(), "C123", "111.2", "")
	require.True(t, ok)
	assert.Equal(t, "q1", item.ID)
	assert.Equal(t, "C123", target.ChannelID)

	router.errPrepare = errors.New("resolve failed")
	_, _, ok = connector.findQueuedEnvelope(t.Context(), "C123", "111.2", "")
	require.False(t, ok)

	router.errPrepare = nil
	router.prepareHandled = false
	_, _, ok = connector.findQueuedEnvelope(t.Context(), "C123", "111.2", "")
	require.False(t, ok)
}

func TestDeleteQueuedEnvelopeLogsError(t *testing.T) {
	router := newThreadRouterStub()
	router.errQueue = errors.New("delete failed")
	connector := newTestConnectorWithOptions("http://slack.test", nil, nil, router, nil)
	ok := connector.deleteQueuedEnvelope(t.Context(), protocol.TextConversationTarget{ChannelID: "C123", ThreadID: "111.2"}, &protocol.ThreadQueueItem{ID: "q1", SlackChannel: "C123", SlackTS: "111.2"})
	require.False(t, ok)
}

func TestHandleRocketclawActionsInteractiveRejectsInvalidPayloads(t *testing.T) {
	rec := newMessageActionsRecorder(t)
	connector := newTestConnectorWithOptions(rec.URL, newTestBus(), nil, newThreadRouterStub(), nil)
	assert.False(t, connector.handleRocketclawActionsInteractive(t.Context(), &slack.InteractionCallback{}))
	assert.True(t, connector.handleRocketclawActionsInteractive(t.Context(), &slack.InteractionCallback{
		Type: slack.InteractionTypeBlockActions,
		View: slack.View{CallbackID: slackMessageShortcutCallbackID, PrivateMetadata: "not-json"},
	}))
	assert.True(t, connector.handleRocketclawActionsInteractive(t.Context(), &slack.InteractionCallback{
		Type: slack.InteractionTypeBlockActions,
		View: slack.View{ID: "V-actions", CallbackID: slackMessageShortcutCallbackID, PrivateMetadata: "{}"},
	}))
}

func TestHandleMessageShortcutCancelClickDropsSteer(t *testing.T) {
	rec := newMessageActionsRecorder(t)
	router := newThreadRouterStub()
	router.prepareHandled = true
	router.queue = []protocol.ThreadQueueItem{{ID: "backend-steer", Kind: protocol.InboundKindSteer, Message: "pending steer", Principal: "U123", SlackChannel: "C123", SlackTS: "111.2"}}
	connector := newTestConnectorWithOptions(rec.URL, newTestBus(), nil, router, nil)
	connector.handleInteractive(t.Context(), newMessageShortcutEvent("U123", "111.2"))
	assert.Equal(t, []string{slackMessageActionCancel}, actionIDs(rec.lastOpened()))

	connector.handleInteractive(t.Context(), newMessageActionsButtonEvent(slackMessageActionCancel, &rocketclawActionsMetadata{ChannelID: "C123", MessageTS: "111.2", ThreadTS: "111.2"}))

	assert.Empty(t, router.queueSnapshot())
	assert.Empty(t, connector.stacks)
	assert.Empty(t, router.goalStops)
	assert.Empty(t, router.conversationStops)
	require.Len(t, rec.updated, 1)
	assert.Equal(t, "Cancelled.", rec.updated[0].View.Blocks[0].Text.Text)
	connector.handleInteractive(t.Context(), newMessageActionsButtonEvent(slackMessageActionCancel, &rocketclawActionsMetadata{ChannelID: "C123", MessageTS: "111.2", ThreadTS: "111.2"}))
	require.Len(t, rec.updated, 2)
	assert.Equal(t, "No RocketClaw actions on this message.", rec.updated[1].View.Blocks[0].Text.Text)
}

func actionIDs(view messageActionsView) []string {
	var ids []string

	for _, block := range view.View.Blocks {
		for _, element := range block.Elements {
			ids = append(ids, element.ActionID)
		}
	}

	return ids
}

func newMessageShortcutEvent(userID, messageTS string) socketmode.Event {
	return socketmode.Event{Data: slack.InteractionCallback{
		Type:       slack.InteractionTypeMessageAction,
		CallbackID: slackMessageShortcutCallbackID,
		User:       slack.User{ID: userID},
		TriggerID:  "trigger-shortcut",
		Channel:    slack.Channel{GroupConversation: slack.GroupConversation{Conversation: slack.Conversation{ID: "C123"}}},
		Message:    slack.Message{Msg: slack.Msg{Timestamp: messageTS, ThreadTimestamp: messageTS}},
	}}
}

func newMessageActionsButtonEvent(actionID string, metadata *rocketclawActionsMetadata) socketmode.Event {
	encoded, _ := json.Marshal(metadata)

	return socketmode.Event{Data: slack.InteractionCallback{
		Type: slack.InteractionTypeBlockActions,
		User: slack.User{ID: "U123"},
		View: slack.View{ID: "V-actions", CallbackID: slackMessageShortcutCallbackID, PrivateMetadata: string(encoded)},
		ActionCallback: slack.ActionCallbacks{BlockActions: []*slack.BlockAction{{
			ActionID: actionID,
		}}},
	}}
}
