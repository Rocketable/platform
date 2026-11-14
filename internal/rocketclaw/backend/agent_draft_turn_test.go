package backend

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"github.com/stretchr/testify/require"
)

// agentDraftTest runs Run over a workspace whose fixture agents are in workspace agents/, against a provider
// that tells answer which agent file a request runs, by the COL- word in its instructions, and what its last
// user message says.
type agentDraftTest struct {
	rt        *Runtime
	workspace string
	draft     *AgentDraft
	revision  int64
	chat      string
}

func startAgentDraftTest(t *testing.T, agents map[string]string, answer func(w http.ResponseWriter, column, user, body string)) *agentDraftTest {
	t.Helper()

	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "data"), 0o755))

	for name, text := range agents {
		file := filepath.Join(workspace, "agents", name+".md")
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
		require.NoError(t, os.WriteFile(file, []byte(text), 0o600))
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)

		var request struct {
			Instructions string `json:"instructions"`
			Input        []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"input"`
		}

		_ = json.Unmarshal(data, &request)

		user := ""

		for _, item := range request.Input {
			if item.Role == "user" {
				user = string(item.Content)
			}
		}

		w.Header().Set("Content-Type", "application/json")

		switch column := regexp.MustCompile(`COL-\w+`).FindString(request.Instructions); {
		case strings.Contains(string(data), "You are judging one planned coding-agent action."):
			writeRawRunMessage(t, w, "review", "verdict", `{"risk_level":"low","user_authorization":"high","outcome":"allow","rationale":"ok"}`)
		case column == "":
			answerNoteTest(w, r, 0, "")
		default:
			answer(w, column, user, string(data))
		}
	}))
	t.Cleanup(server.Close)

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)

	runtimes := make(chan *Runtime, 1)
	assembler := &frontendAssemblerMock{
		ValidateAssetsFunc: func(*config.Config, string) error { return nil },
		AssembleFunc: func(rt *Runtime) (<-chan struct{}, []func(context.Context) error, error) {
			runtimes <- rt
			return nil, nil, nil
		},
	}

	cfg := &config.Config{Workspace: workspace, DatabaseURL: dsn, AutoApproverModel: "gpt-review", OpenAI: config.OpenAIConfig{APIKey: "test", APIBaseURL: server.URL}}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() {
		done <- Run(ctx, cfg, "", slog.New(slog.NewTextHandler(t.Output(), nil)), assembler, sandbox.None{})
	}()

	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})

	select {
	case rt := <-runtimes:
		return &agentDraftTest{rt: rt, workspace: workspace}
	case err := <-done:
		t.Fatalf("Run returned before assembling: %v", err)
		return nil
	}
}

// create starts a draft of name and its test chat.
func (dt *agentDraftTest) create(t *testing.T, name string, mode harness.AgentMode) {
	t.Helper()

	draft, err := dt.rt.Sessions.CreateAgentDraft(t.Context(), "alice", AgentDraftOrigin{Name: name, Mode: mode})
	require.NoError(t, err)

	dt.draft, dt.revision, dt.chat = draft, draft.Revision, "draft-chat-"+rand.Text()
	_, err = dt.rt.Sessions.recordConversation(t.Context(), &protocol.Conversation{ID: dt.chat, Agent: name, CreatedBy: "alice"}, conversationFacts{kind: KindAgentDraft}, harness.AgentStickyOff)
	require.NoError(t, err)
}

func (dt *agentDraftTest) edit(t *testing.T, op *AgentDraftOp) {
	t.Helper()

	op.BaseRevision, op.Author, op.OpID = dt.revision, "alice", rand.Text()

	var err error

	dt.revision, err = dt.rt.Sessions.ApplyAgentDraftOp(t.Context(), dt.draft.ID, op)
	require.NoError(t, err)
}

// send captures a test message as the page sends one; stash hands it to the test chat.
func (dt *agentDraftTest) send(t *testing.T, text string) *protocol.ThreadQueueItem {
	t.Helper()

	inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindEnqueue, &protocol.InboundContent{Text: text}, true)
	inbound.ConversationID = dt.chat
	inbound.Metadata[protocol.InboundPrincipalMetadataKey], inbound.Metadata["web_message_id"] = "alice", rand.Text()

	_, _, err := dt.rt.Sessions.CaptureAgentDraft(t.Context(), dt.draft.ID, inbound)
	require.NoError(t, err)

	return &protocol.ThreadQueueItem{ID: inbound.Metadata["web_message_id"], Message: text, Principal: "alice", Source: protocol.SourceWeb, Kind: protocol.InboundKindEnqueue, StashAt: time.Now().UTC(), Inbound: inbound}
}

func (dt *agentDraftTest) stash(t *testing.T, items ...*protocol.ThreadQueueItem) {
	t.Helper()

	for _, item := range items {
		require.NoError(t, dt.rt.StashQueueItem(t.Context(), dt.chat, item))
	}
}

// save replaces the live agent as a save does: the workspace file, then Reload.
func (dt *agentDraftTest) save(t *testing.T, name, text string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(dt.workspace, "agents", name+".md"), []byte(text), 0o600))
	_, err := dt.rt.Reload("test save")
	require.NoError(t, err)
}

// noDraftFiles checks no draft file reached the workspace or the runtime directory.
func (dt *agentDraftTest) noDraftFiles(t *testing.T, files ...string) {
	t.Helper()

	for _, dir := range []string{"agents", filepath.Join(config.DefaultRuntimeDir, "agents")} {
		for _, file := range files {
			require.NoFileExists(t, filepath.Join(dt.workspace, dir, file))
		}
	}
}

func collectAnswers(t *testing.T, answers <-chan string, n int) []string {
	t.Helper()

	var got []string

	deadline := time.After(time.Minute)

	for len(got) < n {
		select {
		case answer := <-answers:
			got = append(got, answer)
		case <-deadline:
			t.Fatalf("got only %d answers: %q", len(got), got)
		}
	}

	return got
}

// Each column of a draft test message runs the files captured when the message was sent: edits and saves
// made after it, even before its turn starts, reach only later messages. The draft's variants, never the
// disk's, get each message; the live column answers while the agent keeps its name, in its own mode. Each
// column frames the message with its own definition.
func TestAgentDraftTurnRunsWhatItWasSentWith(t *testing.T) {
	t.Parallel()

	easy := "---\nmode: easy\nmodel: gpt-6.1-sol\nspace: Ops\nworkingDirectory: data\n---\n"
	answers, histories := make(chan string, 16), make(chan [2]string, 16)
	dt := startAgentDraftTest(t, map[string]string{"support-bot": easy + "COL-L1\n", "support-bot/b": easy + "COL-DISKB\n"}, func(w http.ResponseWriter, column, user, body string) {
		answer := column + "/" + regexp.MustCompile(`MSG-\d`).FindString(user) + "/" + regexp.MustCompile(`FRAME-\w+`).FindString(user)
		writeRawRunMessage(t, w, "resp", "msg", answer)

		answers <- answer

		histories <- [2]string{answer, body}
	})

	file := func(frame, column string) string {
		return "---\nmodel: gpt-6.1-sol\nadditionalInstructions: " + frame + "\n---\n" + column + "\n"
	}

	dt.create(t, "support-bot", harness.AgentModeAdvanced)
	dt.edit(t, &AgentDraftOp{Kind: AgentDraftEditFile, Variant: "a", Value: file("FRAME-A", "COL-A1")})
	dt.edit(t, &AgentDraftOp{Kind: AgentDraftAddVariant, Variant: "b", Value: "a"})
	dt.edit(t, &AgentDraftOp{Kind: AgentDraftEditFile, Variant: "b", Value: file("FRAME-B", "COL-B1")})
	m1 := dt.send(t, "MSG-1")

	dt.edit(t, &AgentDraftOp{Kind: AgentDraftEditFile, Variant: "a", Value: file("FRAME-A", "COL-A2")})
	dt.edit(t, &AgentDraftOp{Kind: AgentDraftRemoveVariant, Variant: "b"})
	dt.edit(t, &AgentDraftOp{Kind: AgentDraftAddVariant, Variant: "c", Value: "a"})
	dt.edit(t, &AgentDraftOp{Kind: AgentDraftEditFile, Variant: "c", Value: file("FRAME-C", "COL-C2")})
	dt.save(t, "support-bot", easy+"COL-L2\n")
	m2 := dt.send(t, "MSG-2")

	dt.edit(t, &AgentDraftOp{Kind: AgentDraftEditFile, Variant: "a", Value: file("FRAME-A", "COL-A3")})
	dt.save(t, "support-bot", easy+"COL-L3\n")
	dt.edit(t, &AgentDraftOp{Kind: AgentDraftRename, Value: "support-bot-2"})
	m3 := dt.send(t, "MSG-3")

	// Nothing reaches the chat before every edit and save above.
	dt.stash(t, m1, m2, m3)

	require.ElementsMatch(t, []string{
		"COL-A1/MSG-1/FRAME-A", "COL-B1/MSG-1/FRAME-B", "COL-L1/MSG-1/",
		"COL-A2/MSG-2/FRAME-A", "COL-C2/MSG-2/FRAME-C", "COL-L2/MSG-2/",
		"COL-A3/MSG-3/FRAME-A", "COL-C2/MSG-3/FRAME-C",
	}, collectAnswers(t, answers, 8))

	for range 8 {
		if history := <-histories; history[0] == "COL-C2/MSG-2/FRAME-C" {
			require.Contains(t, history[1], "COL-A1/MSG-1/FRAME-A", "a variant added mid-chat starts from the draft column's transcript")
			require.NotContains(t, history[1], "COL-B1")
		}
	}

	var kind ConversationKind
	require.NoError(t, dt.rt.Sessions.db.QueryRowContext(t.Context(), `SELECT kind FROM conversations WHERE conversation_id = $1`, dt.chat).Scan(&kind))
	require.Equal(t, KindAgentDraft, kind, "creating the shadows keeps the test chat's kind")
	dt.noDraftFiles(t, "support-bot/c.md", "support-bot-2.md", "support-bot-2/c.md")
}

// An unsaved draft, with no file or folder on disk, answers with its variant; its subagent session, running
// the draft agent, loads it through its top-level session's message. Work that would run in the background
// runs inside the test turn, which answers with its result, and no test turn gets a tool that starts a later
// turn: a later turn would run without the files its message was sent with.
func TestAgentDraftTurnRunsUnsavedDraftAndItsSubagent(t *testing.T) {
	t.Parallel()

	answers := make(chan string, 16)
	dt := startAgentDraftTest(t, nil, func(w http.ResponseWriter, column, user, body string) {
		answer := column + "/" + regexp.MustCompile(`MSG-\w+`).FindString(user)

		switch {
		case column == "COL-TA" && strings.Contains(user, "research the flaky test"):
			answer = column + "/subagent"
		case column == "COL-TA" && !strings.Contains(body, "function_call_output"):
			// The model runs the subagent in the background whenever it may.
			writeRawRunFunctionCall(t, w, "resp_task", "task", map[string]any{"description": "research", "prompt": "research the flaky test", "model": "gpt-6-luna", "tools": []string{"bash"},
				"background": strings.Contains(body, "Run the subagent in the background")})

			return
		case strings.Contains(body, "COL-TA/subagent"):
			answer += "/with-result"
		}

		var answerSb286 strings.Builder

		for _, tool := range []string{sessionsWriteToolName, scheduleMessageToolName, resetScheduledMessagesToolName} {
			if strings.Contains(body, tool+"(") {
				answerSb286.WriteString("/" + tool)
			}
		}

		answer += answerSb286.String()

		writeRawRunMessage(t, w, "resp", "msg", answer)

		answers <- answer
	})

	dt.create(t, "triage", harness.AgentModeEasy)
	dt.edit(t, &AgentDraftOp{Kind: AgentDraftEditFile, Variant: "a", Value: "---\nmode: easy\nmodel: gpt-6.1-sol\nsubagents: [gpt-6-luna]\nspace: Ops\nworkingDirectory: data\n---\nCOL-TA\n"})
	dt.edit(t, &AgentDraftOp{Kind: AgentDraftAddVariant, Variant: "b", Value: "a"})
	dt.edit(t, &AgentDraftOp{Kind: AgentDraftEditFile, Variant: "b", Value: "---\nmode: easy\nmodel: gpt-6.1-sol\nspace: Ops\nworkingDirectory: data\n---\nCOL-TB\n"})
	dt.stash(t, dt.send(t, "MSG-T"))

	require.ElementsMatch(t, []string{"COL-TA/subagent", "COL-TA/MSG-T/with-result", "COL-TB/MSG-T"}, collectAnswers(t, answers, 3))
	dt.noDraftFiles(t, "triage.md", "triage/b.md")
}
