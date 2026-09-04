package slackconnector

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
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
	URL        string
	mu         sync.Mutex
	opened     []messageActionsView
	updated    []messageActionsView
	updatedRaw []string
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
				rec.updatedRaw = append(rec.updatedRaw, string(body))
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
	connector.handleInteractive(t.Context(), newMessageShortcutEvent("U999", "111.2", nil))
}

func TestHandleMessageShortcutUnmanagedExplains(t *testing.T) {
	rec := newMessageActionsRecorder(t)
	connector := newTestConnector(rec.URL)

	connector.handleInteractive(t.Context(), newMessageShortcutEvent("U123", "111.2", nil))

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
	knownY(connector, "social")

	connector.handleInteractive(t.Context(), newMessageShortcutEvent("U123", "111.2", nil))

	require.Len(t, rec.opened, 1)
	assert.Equal(t, "No RocketClaw actions on this message.", rec.lastOpened().View.Blocks[0].Text.Text)
	assert.Empty(t, rec.lastOpened().View.Blocks[0].Elements)
}

func TestHandleMessageShortcutThinkingOffersInterrupt(t *testing.T) {
	rec := newMessageActionsRecorder(t)
	router := newThreadRouterStub()
	router.prepareHandled = true
	connector := newTestConnectorWithOptions(rec.URL, newTestBus(), nil, router, nil)
	knownY(connector, "social")
	connector.pending["k"] = slackReplySlots{ChannelID: "C123", ThinkingTS: "999.1", AnswerTS: "999.2"}

	connector.handleInteractive(t.Context(), newMessageShortcutEvent("U123", "999.1", nil))

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

	turns := waitTurns(t, connector, 1)
	assert.Equal(t, protocol.TurnCancel, turns[0].Kind)
	require.Len(t, rec.updated, 1)
	assert.Equal(t, "Interrupted the turn.", rec.updated[0].View.Blocks[0].Text.Text)
}

func TestHandleMessageShortcutEnvelopeOffersCancelAndSteer(t *testing.T) {
	rec := newMessageActionsRecorder(t)
	router := newThreadRouterStub()
	router.prepareHandled = true
	connector := newTestConnectorWithOptions(rec.URL, newTestBus(), nil, router, nil)
	knownY(connector, "social")
	setLaterWork(connector, []protocol.ThreadQueueItem{{ID: "q1", Message: "later", SlackChannel: "C123", SlackTS: "111.2"}})
	connector.beginSlackStack(slackThreadStackKey(&protocol.SlackReplyTarget{ChannelID: "C123", ThreadTS: "111.2"}))

	connector.handleInteractive(t.Context(), newMessageShortcutEvent("U123", "111.2", nil))

	require.Len(t, rec.opened, 1)
	ids := actionIDs(rec.lastOpened())
	assert.Equal(t, []string{slackMessageActionCancel, slackMessageActionSteer}, ids)
}

func TestHandleMessageShortcutCancelClickDropsSteer(t *testing.T) {
	rec := newMessageActionsRecorder(t)
	router := newThreadRouterStub()
	router.prepareHandled = true
	connector := newTestConnectorWithOptions(rec.URL, newTestBus(), nil, router, nil)
	knownY(connector, "social")

	key := slackThreadStackKey(&protocol.SlackReplyTarget{ChannelID: "C123", ThreadTS: "111.2"})
	connector.beginSlackStack(key)

	content := protocol.InboundContent{Text: "pending steer"}
	reply := &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.2", ThreadTS: "111.2"}
	require.True(t, connector.bufferSlackStack(t.Context(), key, "pending steer", &content, reply, "U123", "", "", nil))

	connector.handleInteractive(t.Context(), newMessageActionsButtonEvent(slackMessageActionCancel, &rocketclawActionsMetadata{ChannelID: "C123", MessageTS: "111.2", ThreadTS: "111.2"}))

	connector.mu.Lock()
	pending, active := connector.stacks[key]
	connector.mu.Unlock()
	assert.True(t, active)
	assert.Empty(t, pending)
	assert.Empty(t, router.goalStops)
	require.Len(t, rec.updated, 1)
	assert.Equal(t, "Cancelled.", rec.updated[0].View.Blocks[0].Text.Text)
}

func TestHandleMessageShortcutAnswerOffersSideAsk(t *testing.T) {
	rec := newMessageActionsRecorder(t)
	router := newThreadRouterStub()
	router.prepareHandled = true
	connector := newTestConnectorWithOptions(rec.URL, newTestBus(), nil, router, nil)
	knownY(connector, "social")

	divider := slack.NewDividerBlock()
	divider.BlockID = sideAskStampValue(t, 42)

	connector.handleInteractive(t.Context(), newMessageShortcutEvent("U123", "111.222", []slack.Block{divider}))

	require.Len(t, rec.opened, 1)
	assert.Equal(t, []string{slackMessageActionSideAsk}, actionIDs(rec.lastOpened()))
}

func TestHandleMessageShortcutSideAskClickUpdatesToForm(t *testing.T) {
	rec := newMessageActionsRecorder(t)
	router := newThreadRouterStub()
	router.prepareHandled = true
	connector := newTestConnectorWithOptions(rec.URL, newTestBus(), nil, router, nil)
	knownY(connector, "social")

	stamp := sideAskStampValue(t, 42)

	connector.handleInteractive(t.Context(), newMessageActionsButtonEvent(slackMessageActionSideAsk, &rocketclawActionsMetadata{
		ChannelID: "C123", MessageTS: "111.222", ThreadTS: "111.222", Stamp: parseTestSideAskStamp(t, stamp),
	}))

	require.Len(t, rec.updated, 1)
	assert.Equal(t, slackSideAskViewCallbackID, rec.updated[0].View.CallbackID)
}

func TestHandleMessageShortcutSideAskChooserListsChannelAgentsAndPreselectsThreadAgent(t *testing.T) {
	rec := newMessageActionsRecorder(t)
	router := newThreadRouterStub()
	router.threadAgent = "planner"
	router.threadAgentHandled = true
	connector := newTestConnectorWithOptions(rec.URL, nil, []config.SlackChannelConfig{{
		Channel: "#social", Agents: []string{"social", "planner"}, AllowedUserIDs: []string{"U123"},
	}}, router, nil)
	knownY(connector, "planner")

	connector.handleInteractive(t.Context(), newMessageActionsButtonEvent(slackMessageActionSideAsk, &rocketclawActionsMetadata{
		ChannelID: "C123", MessageTS: "111.222", ThreadTS: "111.222", Stamp: parseTestSideAskStamp(t, sideAskStampValue(t, 7)),
	}))

	require.Len(t, rec.updatedRaw, 1)

	var view sideAskOpenedView
	require.NoError(t, json.Unmarshal([]byte(rec.updatedRaw[0]), &view))
	assert.Equal(t, slackSideAskViewCallbackID, view.View.CallbackID)

	var agentBlock *sideAskOpenedBlock

	for i := range view.View.Blocks {
		if view.View.Blocks[i].Element.Type == "static_select" {
			agentBlock = &view.View.Blocks[i]
			break
		}
	}

	require.NotNil(t, agentBlock)

	values := make([]string, 0, len(agentBlock.Element.Options))
	for _, option := range agentBlock.Element.Options {
		values = append(values, option.Value)
	}

	assert.Equal(t, []string{"social", "planner"}, values)
	assert.Equal(t, "planner", agentBlock.Element.InitialOption.Value)
}

func TestHandleMessageShortcutSecondSideAskWhileLiveIsRefused(t *testing.T) {
	rec := newMessageActionsRecorder(t)
	router := newThreadRouterStub()
	router.prepareHandled = true
	connector := newTestConnectorWithOptions(rec.URL, newTestBus(), nil, router, nil)
	knownY(connector, "social")

	for _, entryID := range []int64{42, 43} {
		connector.handleInteractive(t.Context(), newMessageActionsButtonEvent(slackMessageActionSideAsk, &rocketclawActionsMetadata{
			ChannelID: "C123", MessageTS: "111.222", ThreadTS: "111.222", Stamp: parseTestSideAskStamp(t, sideAskStampValue(t, entryID)),
		}))
	}

	require.Len(t, rec.updated, 2)
	assert.Equal(t, slackSideAskViewCallbackID, rec.updated[0].View.CallbackID)
	assert.Equal(t, "No RocketClaw actions on this message.", rec.updated[1].View.Blocks[0].Text.Text)
}

func TestHandleMessageShortcutSideAskDismissStartsNoRunner(t *testing.T) {
	rec := newMessageActionsRecorder(t)
	runner := &recordingSideAskRunner{}
	router := newThreadRouterStub()
	router.prepareHandled = true
	connector := newTestConnectorWithOptions(rec.URL, newTestBus(), nil, router, nil)
	knownY(connector, "social")
	connector.sideAsk = runner
	metadata := sideAskStampValue(t, 42)

	connector.handleInteractive(t.Context(), newMessageActionsButtonEvent(slackMessageActionSideAsk, &rocketclawActionsMetadata{
		ChannelID: "C123", MessageTS: "111.222", ThreadTS: "111.222", Stamp: parseTestSideAskStamp(t, metadata),
	}))
	require.Len(t, rec.updated, 1)

	connector.handleInteractive(t.Context(), socketmode.Event{Data: slack.InteractionCallback{
		Type: slack.InteractionTypeViewClosed,
		User: slack.User{ID: "U123"},
		View: slack.View{CallbackID: slackSideAskViewCallbackID, PrivateMetadata: metadata},
	}})

	assert.Empty(t, runner.snapshot())
}

func parseTestSideAskStamp(t *testing.T, raw string) sideAskStamp {
	t.Helper()

	var stamp sideAskStamp
	require.NoError(t, json.Unmarshal([]byte(raw), &stamp))

	return stamp
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

func newMessageShortcutEvent(userID, messageTS string, blocks []slack.Block) socketmode.Event {
	return socketmode.Event{Data: slack.InteractionCallback{
		Type:       slack.InteractionTypeMessageAction,
		CallbackID: slackMessageShortcutCallbackID,
		User:       slack.User{ID: userID},
		TriggerID:  "trigger-shortcut",
		Channel:    slack.Channel{GroupConversation: slack.GroupConversation{Conversation: slack.Conversation{ID: "C123"}}},
		Message:    slack.Message{Msg: slack.Msg{Timestamp: messageTS, ThreadTimestamp: messageTS, Blocks: slack.Blocks{BlockSet: blocks}}},
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
