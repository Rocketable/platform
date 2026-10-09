package backend

import (
	"context"
	"errors"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func newTestWebQuestions(service *SessionService) *Runtime {
	return &Runtime{Sessions: service, questions: &webQuestions{store: service, waiting: map[webQuestionKey]chan protocol.AskUserQuestionAnswer{}}}
}

func requireNotPending(t *testing.T, err error, conversationID, questionID string) {
	t.Helper()

	notPending, ok := errors.AsType[*QuestionNotPendingError](err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, &QuestionNotPendingError{ConversationID: conversationID, QuestionID: questionID}, notPending)
}

func TestWebQuestionIsListedAndTakesTheFirstAnswer(t *testing.T) {
	service := newTestSessionService(t)
	rt := newTestWebQuestions(service)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	req := &protocol.AskUserQuestionRequest{ID: "turn-1/call/c1", ConversationID: conversationID, Question: "Which?", Options: []protocol.AskUserQuestionOption{{Label: "A", Value: "a"}, {Label: "B", Value: "b"}}, Multiple: true}

	listening, cancel := context.WithTimeout(t.Context(), 10*time.Second) // A missing notification fails, not hangs.
	defer cancel()

	changes, stop := iter.Pull2(service.Changes(listening, conversationID))
	defer stop()

	_, err, _ := changes() // The initial refresh.
	require.NoError(t, err)

	answered := make(chan protocol.AskUserQuestionAnswer, 1)

	go func() {
		answer, err := rt.questions.AskUserQuestion(t.Context(), req)
		assert.NoError(t, err)

		answered <- answer
	}()

	change, err, _ := changes()
	require.NoError(t, err)
	assert.Equal(t, conversationID, change.ConversationID, "saving the question wakes the conversation's Web views")

	pending, err := rt.PendingQuestions(t.Context(), conversationID)
	require.NoError(t, err)
	assert.Equal(t, []protocol.AskUserQuestionRequest{*req}, pending)

	answer := protocol.AskUserQuestionAnswer{Selected: []string{"a", "b"}, Custom: "and c"}
	requireNotPending(t, rt.AnswerQuestion(t.Context(), "web:other", req.ID, answer), "web:other", req.ID)

	requireNotPending(t, rt.AnswerQuestion(t.Context(), conversationID, "turn-2/call/c1", answer), conversationID, "turn-2/call/c1")

	_, saved, err := service.LoadTurnStep(t.Context(), conversationID, "turn-2/call/c1/answer")
	require.NoError(t, err)
	assert.False(t, saved, "a refused answer saves nothing")
	assert.Empty(t, answered, "a refused answer wakes nothing")

	require.Eventually(t, func() bool { return rt.AnswerQuestion(t.Context(), conversationID, req.ID, answer) == nil }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, protocol.AskUserQuestionAnswer{Selected: []string{"a", "b"}, Custom: "and c", Source: protocol.SourceWeb}, <-answered)

	change, err, _ = changes()
	require.NoError(t, err)
	assert.Equal(t, conversationID, change.ConversationID, "saving the answer wakes the conversation's Web views")

	pending, err = rt.PendingQuestions(t.Context(), conversationID)
	require.NoError(t, err)
	assert.Empty(t, pending)

	requireNotPending(t, rt.AnswerQuestion(t.Context(), conversationID, req.ID, protocol.AskUserQuestionAnswer{Custom: "late"}), conversationID, req.ID)
}

func TestWebQuestionWithdrawsUnlessLeftForRestart(t *testing.T) {
	service := newTestSessionService(t)
	rt := newTestWebQuestions(service)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	req := &protocol.AskUserQuestionRequest{ID: "turn-1/call/c1", ConversationID: conversationID, Question: "Ship?"}

	ask := func(cause error) {
		ctx, cancel := context.WithCancelCause(t.Context())
		done := make(chan error, 1)

		go func() {
			_, err := rt.questions.AskUserQuestion(ctx, req)
			done <- err
		}()

		require.Eventually(t, func() bool {
			pending, err := rt.PendingQuestions(t.Context(), conversationID)
			return err == nil && len(pending) == 1
		}, 5*time.Second, 10*time.Millisecond)
		cancel(cause)
		require.ErrorIs(t, <-done, context.Canceled)
	}

	ask(errMovedToBackground)

	pending, err := rt.PendingQuestions(t.Context(), conversationID)
	require.NoError(t, err)
	assert.Empty(t, pending, "moving the script to the background withdraws the question")

	ask(errShutdown)

	pending, err = rt.PendingQuestions(t.Context(), conversationID)
	require.NoError(t, err)
	assert.Len(t, pending, 1, "a turn left for restart keeps its question listed")
	requireNotPending(t, rt.AnswerQuestion(t.Context(), conversationID, req.ID, protocol.AskUserQuestionAnswer{Custom: "early"}), conversationID, req.ID)

	// An answer saved before the restart returns at once when the resumed turn asks again.
	require.NoError(t, service.SaveTurnStep(t.Context(), conversationID, req.ID+"/answer", []byte(`{"selected":["yes"],"custom":"","source":"web"}`)))

	answer, err := newTestWebQuestions(service).questions.AskUserQuestion(t.Context(), req)
	require.NoError(t, err)
	assert.Equal(t, protocol.AskUserQuestionAnswer{Selected: []string{"yes"}, Source: protocol.SourceWeb}, answer)

	pending, err = rt.PendingQuestions(t.Context(), conversationID)
	require.NoError(t, err)
	assert.Empty(t, pending)
}

// A person's Web turn, here in a Slack-thread conversation, asks in Web, and its question
// survives a restart: the resumed turn waits on it again and the Web answer continues the turn.
func TestWebTurnQuestionContinuesAfterRestart(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	service := newTestSessionServiceAt(t, workspace)

	bodies := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies <- string(body)

		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(string(body), "function_call_output") {
			writeRawRunMessage(t, w, "resp_2", "msg_1", "answer")
			return
		}

		writeRawRunFunctionCall(t, w, "resp_1", "execute", struct {
			Code string `json:"code"`
		}{Code: "def main():\n    return ask_user_question(question=\"Ship?\", details=\"\", options=[], multiple=False)\n"})
	}))
	t.Cleanup(server.Close)

	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, service.UpsertThread(conversationID, ThreadState{Agent: "main"}))

	finals := make(chan *protocol.OutboundMessage, 4)
	cfg := config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIKey: "test", APIBaseURL: server.URL}})
	newBridge := func(rt *Runtime) *Bridge {
		return NewConversation(cfg, finalsPublisher{finals: finals}, &Config{ConversationID: conversationID, Agent: "main", RequestRestart: testNoopRestart, StartNewThread: testNoopStartNewThread, SessionService: service, UserQuestionAsker: rt.questions}, slog.New(slog.DiscardHandler))
	}
	pending := func(rt *Runtime) []protocol.AskUserQuestionRequest {
		var listed []protocol.AskUserQuestionRequest

		require.Eventually(t, func() bool {
			var err error

			listed, err = rt.PendingQuestions(t.Context(), conversationID)

			return err == nil && len(listed) == 1
		}, 10*time.Second, 10*time.Millisecond)

		return listed
	}

	first := newTestWebQuestions(service)
	bridge := newBridge(first)
	shutdown := runTestBridge(t, bridge)
	require.NoError(t, bridge.Submit(t.Context(), protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "ship it?", true)))
	assert.Contains(t, <-bodies, "- ask_user_question(", "a person's Web turn gets the question tool")

	asked := pending(first)
	assert.Equal(t, "Ship?", asked[0].Question)

	shutdown()
	require.Eventually(t, func() bool { return !bridge.handlingSnapshot() }, 5*time.Second, 10*time.Millisecond)
	assert.Empty(t, finals)
	assert.Equal(t, asked, pending(first), "the question stays listed while the process restarts")

	restarted := newTestWebQuestions(service)
	runTestBridge(t, newBridge(restarted))
	require.Eventually(t, func() bool {
		return restarted.AnswerQuestion(t.Context(), conversationID, asked[0].ID, protocol.AskUserQuestionAnswer{Selected: []string{"yes"}}) == nil
	}, 10*time.Second, 10*time.Millisecond, "the resumed turn waits on the same question")

	assert.Equal(t, "answer", readFinal(t, finals).Text)
	require.Len(t, bodies, 1, "the recorded model call is not repeated")
	assert.Contains(t, <-bodies, `"output":"{\"selected\":[\"yes\"],\"custom\":\"\",\"source\":\"web\"}"`, "the Web answer is the tool result")

	listed, err := restarted.PendingQuestions(t.Context(), conversationID)
	require.NoError(t, err)
	assert.Empty(t, listed)
}
