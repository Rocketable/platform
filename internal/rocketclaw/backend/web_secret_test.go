package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

func sidebarAsking(t *testing.T, service *SessionService, conversationID string) bool {
	t.Helper()

	for row, err := range service.sidebarSessions(t.Context(), "", nil) {
		require.NoError(t, err)

		if row.Conversation.ID == conversationID {
			return row.Asking
		}
	}

	t.Fatalf("%s is not in the sidebar", conversationID)

	return false
}

func pendingSecret(t *testing.T, service *SessionService, conversationID, name string) SecretRequest {
	t.Helper()

	var pending []SecretRequest

	require.Eventually(t, func() bool {
		pending = service.PendingSecrets(conversationID, alice)
		return len(pending) == 1 && pending[0].Name == name
	}, 10*time.Second, 10*time.Millisecond)

	return pending[0]
}

func requireNoStoredCopy(t *testing.T, service *SessionService, value string) {
	t.Helper()

	tables, err := queryStrings(t.Context(), service.db, `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'`, "tables")
	require.NoError(t, err)
	require.NotEmpty(t, tables)

	for _, table := range tables {
		var found bool
		require.NoError(t, service.db.QueryRowContext(t.Context(), fmt.Sprintf(`SELECT count(*) > 0 FROM %q t WHERE strpos(t::text, $1) > 0`, table), value).Scan(&found))
		assert.False(t, found, "table %s holds the secret", table)
	}
}

func TestWebTurnAsksForSecret(t *testing.T) {
	t.Parallel()

	const value = "ghp_secretvalue123"

	outputs, child := make(chan string, 4), make(chan string, 4)
	nt := newNoteTest(t, func(w http.ResponseWriter, r *http.Request, n int, body string) {
		switch subagent, results := strings.Contains(body, `"input":[{"content":"Research`), strings.Count(body, `"type":"function_call_output"`); {
		case subagent:
			child <- body

			if results == 0 {
				writeExecuteCall(t, w, "child", "def main():\n    return bash(command='printf \"<%s>\" \"${GITHUB_TOKEN-unset}\"')\n")
				return
			}

			writeResponseMessage(t, w, "child", "child", "done")
		case results == 0:
			writeExecuteCall(t, w, "ask", "def main():\n    saved = ask_secret(name='GITHUB_TOKEN', reason='open a pull request')\n    return saved + bash(command='printf \"<%s>\" \"$GITHUB_TOKEN\"').output\n")
		case results == 1:
			outputs <- body

			writeResponseFunctionCall(t, w, "delegate", "task", map[string]string{"description": "check", "prompt": "check the token", "subagent_type": "researcher"})
		default:
			answerNoteTest(w, r, n, body)
		}
	})
	writeAgent(t, nt.cfg.Workspace, "primary", "---\ndescription: Agent\nmodel: gpt-6.1-sol\npermission:\n  bash: allow\n  task: allow\n---\nPrompt\n")
	writeAgent(t, nt.cfg.Workspace, "researcher", "---\ndescription: Researcher\nmodel: gpt-6.1-sol\npermission:\n  bash: allow\n---\nResearch\n")

	manager, _ := nt.run(t, ignoreOutbound)
	require.NoError(t, nt.bridge(t, manager, nt.conversationID).Submit(t.Context(), webMessage(protocol.InboundKindPrompt, "open the pull request", alice)))
	assert.Contains(t, <-nt.requests, "- ask_secret(", "a person's Web turn gets the secret tool")

	asked := pendingSecret(t, nt.service, nt.conversationID, "GITHUB_TOKEN")
	assert.Equal(t, "open a pull request", asked.Reason)
	assert.Empty(t, nt.service.PendingSecrets(nt.conversationID, bob), "only the person who started the turn sees the request")
	requireNotPending(t, nt.service.AnswerSecret(nt.conversationID, asked.ID, bob, []byte(value), false), nt.conversationID, asked.ID)
	assert.True(t, sidebarAsking(t, nt.service, nt.conversationID), "a waiting request makes the session ask")
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.Equal(c, []string{alice + " loud"}, attentionOn(t, nt.service, nt.conversationID))
	}, 5*time.Second, 10*time.Millisecond, "the request reaches the person whose turn asks")

	_, invalid := errors.AsType[*harness.TurnSecretError](nt.service.AnswerSecret(nt.conversationID, asked.ID, alice, []byte("short"), false))
	assert.True(t, invalid, "a value the store refuses leaves the request waiting")
	require.NoError(t, nt.service.AnswerSecret(nt.conversationID, asked.ID, alice, []byte(value), false))

	body := <-outputs
	assert.Contains(t, body, "Saved: $GITHUB_TOKEN is set in every shell command for the rest of this turn only")
	assert.Contains(t, body, "<[secret:GITHUB_TOKEN]>", "a later command of the turn sees the value, and the model sees its placeholder")
	assert.NotContains(t, body, value)

	assert.NotContains(t, <-child, "- ask_secret(", "a subagent is not offered the secret tool")
	assert.Contains(t, <-child, "<unset>", "a subagent does not get the turn's secrets")
	assert.Equal(t, "answer", readFinalFor(t, nt.finals, nt.conversationID).Text)
	nt.waitIdle(t, manager, nt.conversationID)

	assert.Empty(t, nt.service.PendingSecrets(nt.conversationID, alice))
	assert.False(t, sidebarAsking(t, nt.service, nt.conversationID))
	requireNotPending(t, nt.service.AnswerSecret(nt.conversationID, asked.ID, alice, []byte(value), false), nt.conversationID, asked.ID)
	requireNoStoredCopy(t, nt.service, value)
}

func TestWebTurnSecretIsGoneAfterRestart(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "primary", "---\ndescription: Primary\nmodel: gpt-6.1-sol\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	service := newTestSessionServiceAt(t, workspace)

	bodies := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if answerTitleRequest(w, r) {
			return
		}

		body, _ := io.ReadAll(r.Body)
		bodies <- string(body)

		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(string(body), "function_call_output") {
			writeResponseMessage(t, w, "resp_2", "msg_1", "answer")
			return
		}

		writeExecuteCall(t, w, "ask", "def main():\n    first = ask_secret(name='GITHUB_TOKEN', reason='push')\n    return first + ' | ' + ask_secret(name='NPM_TOKEN', reason='publish')\n")
	}))
	t.Cleanup(server.Close)

	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, service.UpsertThread(conversationID, ThreadState{Agent: "primary"}))

	finals := make(chan *protocol.OutboundMessage, 4)
	cfg := config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIKey: "test", APIBaseURL: server.URL}})
	newBridge := func() *Bridge {
		return NewConversation(cfg, finalsPublisher{finals: finals}, &Config{ConversationID: conversationID, Agent: "primary", RequestRestart: &runtimeRestarterMock{RestartFunc: func(string) string { return "" }}, SessionService: service, UserQuestionAsker: newTestWebQuestions(service).questions}, slog.New(slog.DiscardHandler))
	}

	bridge := newBridge()
	shutdown := runTestBridge(t, bridge)
	require.NoError(t, bridge.Submit(t.Context(), webMessage(protocol.InboundKindPrompt, "publish it", alice)))
	<-bodies

	require.NoError(t, service.AnswerSecret(conversationID, pendingSecret(t, service, conversationID, "GITHUB_TOKEN").ID, alice, []byte("ghp_secretvalue123"), false))
	pendingSecret(t, service, conversationID, "NPM_TOKEN")

	shutdown()
	require.Eventually(t, func() bool { return !bridge.handlingSnapshot() }, 5*time.Second, 10*time.Millisecond)
	assert.Empty(t, service.PendingSecrets(conversationID, alice), "a request does not outlive its process")

	runTestBridge(t, newBridge())
	assert.Equal(t, "answer", readFinal(t, finals).Text)
	assert.Contains(t, <-bodies, `RocketClaw restarted, so the secret GITHUB_TOKEN is gone. Call ask_secret again if you still need it. | RocketClaw restarted, so the secret NPM_TOKEN is gone. Call ask_secret again if you still need it.`, "both the saved and the interrupted ask replay as gone")
	assert.Empty(t, service.PendingSecrets(conversationID, alice), "the replay asks nothing")
}

func TestAskSecretToolEndsWithoutAValue(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	recordTestConversations(t, service, "main", map[string]ConversationKind{"web:chat": KindWebChat})

	msg := webMessage(protocol.InboundKindPrompt, "publish", alice)
	msg.ConversationID = "web:chat"

	_, err := askSecretTool(service, harness.NewTurnSecretStore(), msg).Call(t.Context(), []byte(`{"name":"PATH","reason":"x"}`), nil)
	_, invalid := errors.AsType[*harness.TurnSecretError](err)
	require.True(t, invalid, "got %v", err)
	assert.Empty(t, service.PendingSecrets("web:chat", alice), "a bad name is refused before anyone is asked")

	for _, tc := range []struct {
		name string
		end  func(stop, move context.CancelCauseFunc) error
		want string
	}{
		{"decline", func(context.CancelCauseFunc, context.CancelCauseFunc) error {
			return service.AnswerSecret("web:chat", "", alice, nil, true)
		}, "Declined: the user chose not to give NPM_TOKEN"},
		{"stop", func(stop, _ context.CancelCauseFunc) error {
			stop(errTurnInterrupted)
			return nil
		}, "No answer: the turn stopped"},
		{"move to background", func(_, move context.CancelCauseFunc) error {
			move(errMovedToBackground)
			return nil
		}, "No answer: the turn stopped or moved this script to the background"},
	} {
		secrets := harness.NewTurnSecretStore()
		require.NoError(t, secrets.Save("GITHUB_TOKEN", []byte("ghp_heldvalue123")))

		moved, move := context.WithCancelCause(context.Background())
		ctx, stop := context.WithCancelCause(context.WithValue(t.Context(), backgroundMovedKey{}, moved))
		results := make(chan harness.ToolResult, 1)

		go func() {
			result, err := askSecretTool(service, secrets, msg).Call(ctx, []byte(`{"name":"NPM_TOKEN","reason":"publish"}`), nil)
			assert.NoError(t, err)

			results <- result
		}()

		pendingSecret(t, service, "web:chat", "NPM_TOKEN")
		require.NoError(t, tc.end(stop, move), tc.name)
		assert.True(t, strings.HasPrefix((<-results).Output, tc.want), tc.name)
		assert.Empty(t, service.PendingSecrets("web:chat", alice), tc.name)
		assert.False(t, secrets.Holds("NPM_TOKEN"), tc.name)
		assert.True(t, secrets.Holds("GITHUB_TOKEN"), "%s: a saved secret stays usable", tc.name)
		stop(nil)
		move(nil)
	}
}

func TestTurnSecretsLastUntilTheTurnsLastJob(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	recordTestConversations(t, store, "main", map[string]ConversationKind{"main": KindWebChat})
	registry, notes := newTestBackgroundRegistry(store)

	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)

	shared := &turnRoot{root: root, secrets: harness.NewTurnSecretStore(), users: 1}
	require.NoError(t, shared.secrets.Save("GITHUB_TOKEN", []byte("ghp_heldvalue123")))

	started, release := make(chan struct{}, 1), make(chan struct{})
	_, err = backgroundTurn{registry: registry, root: shared, conversationID: "main", origin: &protocol.InboundMessage{}}.Run(t.Context(), &harness.BackgroundJob{ID: "turn-1/call/a", Kind: harness.BackgroundKindExecute, Detached: true}, blockedWork(started, release))
	require.NoError(t, err)
	<-started

	shared.release()
	assert.True(t, shared.secrets.Holds("GITHUB_TOKEN"), "the run ended but its background job still holds the turn")

	close(release)
	require.Equal(t, "main", <-notes)
	require.Eventually(t, func() bool { return !shared.secrets.Holds("GITHUB_TOKEN") }, 5*time.Second, 10*time.Millisecond, "the last job erases the turn's secrets")
}
