package slackconnector

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func TestSideAskEmptyQuestionDoesNotStartRunner(t *testing.T) {
	recorder := newSideAskModalRecorder(t)
	runner := &recordingSideAskRunner{}
	connector := newTestConnector(recorder.URL)
	connector.sideAsk = runner

	connector.handleInteractive(t.Context(), socketmode.Event{Data: newSideAskSubmitCallback(sideAskStampValue(t, 42), "social", "   ")})

	assert.Empty(t, runner.snapshot())
	assert.NotContains(t, recorder.paths(), "/chat.postMessage")
}

func TestSideAskViewClosedMidRunCancelsOnlySideAsk(t *testing.T) {
	recorder := newSideAskModalRecorder(t)
	runner := newBlockingSideAskRunner()
	router := newThreadRouterStub()
	router.prepareHandled = true
	connector := newTestConnectorWithOptions(recorder.URL, nil, nil, router, nil)
	connector.sideAsk = runner
	require.True(t, connector.reserveSideAsk("U123"))

	connector.handleInteractive(t.Context(), socketmode.Event{Data: newSideAskSubmitCallback(sideAskStampValue(t, 42), "social", "What broke?")})

	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("Side Ask did not start")
	}

	connector.handleInteractive(t.Context(), socketmode.Event{Data: slack.InteractionCallback{
		Type: slack.InteractionTypeViewClosed,
		User: slack.User{ID: "U123"},
		View: slack.View{CallbackID: slackSideAskViewCallbackID, PrivateMetadata: sideAskStampValue(t, 42)},
	}})

	select {
	case err := <-runner.done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("view_closed did not cancel Side Ask")
	}

	assert.Empty(t, router.goalStops)
	assert.Empty(t, router.conversationStops)
	assert.NotContains(t, recorder.paths(), "/chat.postMessage")
	assert.NotContains(t, recorder.paths(), "/chat.startStream")
}

func TestSideAskThreadStopDoesNotCancelSideAsk(t *testing.T) {
	var (
		posted    []url.Values
		reactions []string
	)

	server := newSlackStackTestServer(t, &posted, &reactions)
	t.Cleanup(server.Close)

	runner := newBlockingSideAskRunner()
	router := newThreadRouterStub()
	router.prepareHandled = true
	connector := newTestConnectorWithOptions(server.URL, nil, nil, router, nil)
	connector.sideAsk = runner
	require.True(t, connector.reserveSideAsk("U123"))

	connector.handleInteractive(t.Context(), socketmode.Event{Data: newSideAskSubmitCallback(sideAskStampValue(t, 42), "social", "What broke?")})

	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("Side Ask did not start")
	}

	event := newSlackMessageEvent("333.444", "111.222", "$stop")
	event.Channel = "C123"
	connector.handleMessageEvent(t.Context(), event, slackNativeForward{})

	turns := waitTurns(t, connector, 1)
	assert.Equal(t, protocol.TurnCancel, turns[0].Kind)

	select {
	case err := <-runner.done:
		t.Fatalf("thread $stop cancelled Side Ask: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	connector.cancelSideAskView("U123", "")

	select {
	case <-runner.done:
	case <-time.After(time.Second):
		t.Fatal("cleanup cancel did not finish Side Ask")
	}
}

func TestSideAskDuringActiveThreadTurnDoesNotTakeStackOrPost(t *testing.T) {
	recorder := newSideAskModalRecorder(t)
	runner := &recordingSideAskRunner{}
	connector := newTestConnector(recorder.URL)
	connector.sideAsk = runner
	require.True(t, connector.reserveSideAsk("U123"))

	key := slackThreadStackKey(&protocol.SlackReplyTarget{ChannelID: "C123", ThreadTS: "111.222"})
	connector.beginSlackStack(key)

	connector.handleInteractive(t.Context(), socketmode.Event{Data: newSideAskSubmitCallback(sideAskStampValue(t, 42), "social", "What broke?")})
	require.Eventually(t, func() bool { return len(runner.snapshot()) == 1 }, time.Second, 10*time.Millisecond)

	connector.mu.Lock()
	buffered, active := connector.stacks[key]
	connector.mu.Unlock()
	assert.True(t, active)
	assert.Empty(t, buffered)
	assert.NotContains(t, recorder.paths(), "/chat.postMessage")
	assert.NotContains(t, recorder.paths(), "/chat.startStream")
}

func TestSideAskEndDoesNotPromoteBufferedFollowUp(t *testing.T) {
	recorder := newSideAskModalRecorder(t)
	runner := &recordingSideAskRunner{}
	router := newThreadRouterStub()
	router.submitHandled = true
	connector := newTestConnectorWithOptions(recorder.URL, nil, nil, router, nil)
	connector.sideAsk = runner
	require.True(t, connector.reserveSideAsk("U123"))

	key := slackThreadStackKey(&protocol.SlackReplyTarget{ChannelID: "C123", ThreadTS: "111.222"})
	connector.beginSlackStack(key)

	followUp := &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "222.334", ThreadTS: "111.222"}
	content := protocol.InboundContent{Text: "thread follow-up"}
	require.True(t, connector.bufferSlackStack(t.Context(), key, content.Text, &content, followUp, "U123", "", "U123", nil))

	connector.handleInteractive(t.Context(), socketmode.Event{Data: newSideAskSubmitCallback(sideAskStampValue(t, 42), "social", "What broke?")})
	require.Eventually(t, func() bool { return len(runner.snapshot()) == 1 }, time.Second, 10*time.Millisecond)

	assert.Empty(t, router.repliesSnapshot())
	connector.mu.Lock()
	buffered := slices.Clone(connector.stacks[key])
	connector.mu.Unlock()
	require.Len(t, buffered, 1)
	assert.Equal(t, "thread follow-up", buffered[0].Text)

	connector.promoteSlackStack(key)

	assert.Empty(t, router.repliesSnapshot())
	connector.mu.Lock()
	buffered = slices.Clone(connector.stacks[key])
	connector.mu.Unlock()
	require.Len(t, buffered, 1)
	assert.Equal(t, "thread follow-up", buffered[0].Text)
}

func TestSideAskUsesChosenAgentWithoutChangingThreadOwner(t *testing.T) {
	recorder := newSideAskModalRecorder(t)
	runner := &recordingSideAskRunner{}
	router := newThreadRouterStub()
	router.threadAgent = "social"
	router.threadAgentHandled = true
	connector := newTestConnectorWithOptions(recorder.URL, nil, []config.SlackChannelConfig{{
		Channel: "#social", Agents: []string{"social", "planner"}, AllowedUserIDs: []string{"U123"},
	}}, router, nil)
	connector.sideAsk = runner
	require.True(t, connector.reserveSideAsk("U123"))

	connector.handleInteractive(t.Context(), socketmode.Event{Data: newSideAskSubmitCallback(sideAskStampValue(t, 42), "planner", "What broke?")})
	require.Eventually(t, func() bool { return len(runner.snapshot()) == 1 }, time.Second, 10*time.Millisecond)

	assert.Equal(t, "planner", runner.snapshot()[0].Agent)
	assert.Empty(t, router.switched)

	agent, err := connector.conv.ConversationAgent(protocol.SlackThreadConversationID("C123", "111.222"))
	require.NoError(t, err)
	assert.Equal(t, "social", agent)
}

func TestSideAskThinkingUpdatesModalWithoutThreadPosts(t *testing.T) {
	recorder := newSideAskModalRecorder(t)
	connector := newTestConnector(recorder.URL)
	connector.sideAskHost = scriptedSideAskHost{run: func(ctx context.Context, req protocol.SideAskRequest) error {
		require.NoError(t, req.Thinking(ctx, "considering the first card"))
		require.NoError(t, req.Message(ctx, "the private answer"))

		return nil
	}}
	connector.sideAsk = sideAskAdapter{c: connector}
	require.True(t, connector.reserveSideAsk("U123"))

	connector.handleInteractive(t.Context(), socketmode.Event{Data: newSideAskSubmitCallback(sideAskStampValue(t, 42), "social", "What broke?")})
	require.Eventually(t, func() bool { return len(recorder.views()) >= 2 }, time.Second, 10*time.Millisecond)

	paths := recorder.paths()
	assert.NotContains(t, paths, "/chat.startStream")
	assert.NotContains(t, paths, "/chat.postMessage")
	assert.Contains(t, paths, "/views.update")

	views := recorder.views()
	assert.True(t, sideAskViewHasText(&views[0], "considering the first card"))
	assert.Nil(t, views[0].Submit)
	assert.Equal(t, "Close", views[0].Close.Text)
	assert.True(t, sideAskViewHasText(&views[len(views)-1], "the private answer"))
	assert.Nil(t, views[len(views)-1].Submit)
	assert.Equal(t, "Close", views[len(views)-1].Close.Text)
}

func TestSideAskOmitsEmptyThinkingBlock(t *testing.T) {
	recorder := newSideAskModalRecorder(t)
	connector := newTestConnector(recorder.URL)
	connector.sideAskHost = scriptedSideAskHost{run: func(ctx context.Context, req protocol.SideAskRequest) error {
		return req.Message(ctx, "just the answer")
	}}
	connector.sideAsk = sideAskAdapter{c: connector}
	require.True(t, connector.reserveSideAsk("U123"))

	connector.handleInteractive(t.Context(), socketmode.Event{Data: newSideAskSubmitCallback(sideAskStampValue(t, 42), "social", "What broke?")})
	require.Eventually(t, func() bool { return len(recorder.views()) == 1 }, time.Second, 10*time.Millisecond)

	view := recorder.views()[0]
	assert.True(t, sideAskViewHasText(&view, "just the answer"))
	assert.False(t, sideAskViewHasText(&view, slackImmediatePlaceholder))
	assert.False(t, sideAskViewHasEmptyThinking(&view))
	assert.Nil(t, view.Submit)
}

func TestSideAskRunnerErrorUpdatesCloseOnlyErrorView(t *testing.T) {
	recorder := newSideAskModalRecorder(t)
	connector := newTestConnector(recorder.URL)
	connector.sideAskHost = scriptedSideAskHost{run: func(context.Context, protocol.SideAskRequest) error {
		return errors.New("model failed")
	}}
	connector.sideAsk = sideAskAdapter{c: connector}
	require.True(t, connector.reserveSideAsk("U123"))

	connector.handleInteractive(t.Context(), socketmode.Event{Data: newSideAskSubmitCallback(sideAskStampValue(t, 42), "social", "What broke?")})
	require.Eventually(t, func() bool { return len(recorder.views()) == 1 }, time.Second, 10*time.Millisecond)

	view := recorder.views()[0]
	assert.Nil(t, view.Submit)
	assert.Equal(t, "Close", view.Close.Text)
	assert.True(t, sideAskViewHasText(&view, "model failed"))
	assert.NotContains(t, recorder.paths(), "/chat.postMessage")
	assert.NotContains(t, recorder.paths(), "/chat.startStream")
}

func TestSideAskRejectsAgentOutsideChannelList(t *testing.T) {
	recorder := newSideAskModalRecorder(t)
	runner := &recordingSideAskRunner{}
	connector := newTestConnector(recorder.URL)
	connector.sideAsk = runner
	require.True(t, connector.reserveSideAsk("U123"))

	connector.handleInteractive(t.Context(), socketmode.Event{Data: newSideAskSubmitCallback(sideAskStampValue(t, 42), "planner", "What broke?")})

	assert.Empty(t, runner.snapshot())
}

func TestSideAskViewClosedCancelsWithoutChannelLookup(t *testing.T) {
	recorder := newSideAskModalRecorder(t)
	runner := newBlockingSideAskRunner()
	connector := newTestConnector(recorder.URL)
	connector.sideAsk = runner
	require.True(t, connector.reserveSideAsk("U123"))

	connector.handleInteractive(t.Context(), socketmode.Event{Data: newSideAskSubmitCallback(sideAskStampValue(t, 42), "social", "What broke?")})

	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("Side Ask did not start")
	}

	connector.handleInteractive(t.Context(), socketmode.Event{Data: slack.InteractionCallback{
		Type: slack.InteractionTypeViewClosed,
		User: slack.User{ID: "U123"},
		View: slack.View{CallbackID: slackSideAskViewCallbackID, ID: "V-side-ask"},
	}})

	select {
	case err := <-runner.done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("view_closed did not cancel Side Ask")
	}
}

func TestSideAskSubmitWithoutReservationStartsNoRunner(t *testing.T) {
	recorder := newSideAskModalRecorder(t)
	runner := &recordingSideAskRunner{}
	connector := newTestConnector(recorder.URL)
	connector.sideAsk = runner

	connector.handleInteractive(t.Context(), socketmode.Event{Data: newSideAskSubmitCallback(sideAskStampValue(t, 42), "social", "What broke?")})

	assert.Empty(t, runner.snapshot())
}

type scriptedSideAskHost struct {
	run func(context.Context, protocol.SideAskRequest) error
}

func (s scriptedSideAskHost) Run(ctx context.Context, req protocol.SideAskRequest) error {
	return s.run(ctx, req)
}

type blockingSideAskRunner struct {
	started chan struct{}
	done    chan error
}

func newBlockingSideAskRunner() *blockingSideAskRunner {
	return &blockingSideAskRunner{started: make(chan struct{}), done: make(chan error, 1)}
}

func (r *blockingSideAskRunner) RunSideAsk(ctx context.Context, _ *sideAskRequest) {
	close(r.started)
	<-ctx.Done()

	r.done <- ctx.Err()
}

type sideAskModalRecorder struct {
	URL string
	mu  sync.Mutex
	ops []string
	got []slack.ModalViewRequest
}

func newSideAskModalRecorder(t *testing.T) *sideAskModalRecorder {
	t.Helper()

	recorder := &sideAskModalRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.mu.Lock()
		recorder.ops = append(recorder.ops, r.URL.Path)
		recorder.mu.Unlock()

		switch r.URL.Path {
		case "/conversations.info":
			writeJSON(t, w, map[string]any{"ok": true, "channel": map[string]any{"id": "C123", "name": "social"}})
		case "/views.update":
			body, err := io.ReadAll(r.Body)
			if !assert.NoError(t, err) {
				return
			}

			var payload struct {
				View slack.ModalViewRequest `json:"view"`
			}
			if !assert.NoError(t, json.Unmarshal(body, &payload)) {
				return
			}

			recorder.mu.Lock()
			recorder.got = append(recorder.got, payload.View)
			recorder.mu.Unlock()
			writeJSON(t, w, map[string]any{"ok": true, "view": map[string]any{"id": "V-side-ask"}})
		case "/reactions.add", "/reactions.remove", "/chat.postMessage", "/chat.startStream", "/chat.update":
			writeJSON(t, w, map[string]any{"ok": true, "channel": "C123", "ts": "555.1"})
		default:
			assert.Failf(t, "unexpected Slack API path", "%q", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	recorder.URL = server.URL

	return recorder
}

func (r *sideAskModalRecorder) paths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.ops)
}

func (r *sideAskModalRecorder) views() []slack.ModalViewRequest {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.got)
}

func sideAskViewHasText(view *slack.ModalViewRequest, want string) bool {
	for _, block := range view.Blocks.BlockSet {
		section, ok := block.(*slack.SectionBlock)
		if !ok || section.Text == nil {
			continue
		}

		if strings.Contains(section.Text.Text, want) {
			return true
		}
	}

	return false
}

func sideAskViewHasEmptyThinking(view *slack.ModalViewRequest) bool {
	for _, block := range view.Blocks.BlockSet {
		section, ok := block.(*slack.SectionBlock)
		if !ok || section.Text == nil {
			continue
		}

		text := strings.TrimSpace(section.Text.Text)
		if text == "" || text == ">" || text == slackImmediatePlaceholder {
			return true
		}
	}

	return false
}
