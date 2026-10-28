package backend

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/justbash"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

// newJustBashServer returns a just-bash server whose cache dir has a short path, because macOS limits
// Unix socket paths to 104 bytes.
func newJustBashServer(t *testing.T) *justbash.Server {
	t.Helper()

	cache, err := os.MkdirTemp("", "jb")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(cache) })

	server := justbash.NewServer(cache, filepath.Join(cache, "rocketclaw.json"), config.AWSFetcher{})

	t.Cleanup(func() { require.NoError(t, server.Close()) })

	return server
}

func TestJustBashAgentTurn(t *testing.T) {
	t.Parallel()

	workspace, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-6.1-sol\nshell: just-bash\npermission:\n  bash: allow\n---\nWorking in !`pwd`\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	service := newTestSessionServiceAt(t, workspace)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	folder := filepath.Join(workspace, "justbash", conversationID)

	type turnSeen struct{ instructions, output, captured string }

	seen := make(chan turnSeen, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Instructions string
			Input        []struct{ Type, Output string }
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			return
		}

		w.Header().Set("Content-Type", "application/json")

		if last := body.Input[len(body.Input)-1]; last.Type == "function_call_output" {
			var captured string
			assert.NoError(t, service.db.QueryRowContext(r.Context(), `SELECT value::text FROM steps WHERE conversation_id = $1 AND key LIKE '%/canonical'`, conversationID).Scan(&captured))

			seen <- turnSeen{instructions: body.Instructions, output: last.Output, captured: captured}

			writeRawRunMessage(t, w, "done", "message", "done")

			return
		}

		writeRawRunFunctionCall(t, w, "write", "execute", executeBashScript(`echo hi > f.txt; pwd`))
	}))
	t.Cleanup(server.Close)

	require.NoError(t, service.UpsertThread(conversationID, ThreadState{Agent: "main"}))

	finals := make(chan *protocol.OutboundMessage, 4)
	cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIKey: "test", APIBaseURL: server.URL}}
	bridge := NewConversation(config.NewLockedConfig(cfg), finalsPublisher{finals: finals}, &Config{ConversationID: conversationID, Agent: "main", RequestRestart: &runtimeRestarterMock{restartFunc: testNoopRestart}, SessionService: service}, slog.New(slog.DiscardHandler))
	bridge.justbash = newJustBashServer(t)
	runTestBridge(t, bridge)

	require.NoError(t, bridge.Submit(t.Context(), protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "go", true)))

	readFinal(t, finals)

	got := <-seen
	require.Contains(t, got.instructions, "Working in "+folder, "the prompt's shell snippet ran in just-bash, in the agent's folder")
	require.Contains(t, got.output, folder)
	require.NotContains(t, got.captured, "ROCKETCLAW_JUSTBASH", "the captured turn environment never holds the session's variables")

	data, err := os.ReadFile(filepath.Join(folder, "f.txt"))
	require.NoError(t, err)
	require.Equal(t, "hi\n", string(data))
}

func TestJustBashSettingsFailAgentLoad(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-6.1-sol\nshell: just-bash\ncustomShell: /bin/sh\n---\nPrompt\n")

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	_, _, err = readRocketCodeDefinitionsIn(root, &config.Config{Workspace: workspace}, ".rocketclaw", toolModePersistent)
	require.ErrorContains(t, err, "main.md: customShell: cannot be combined with shell: just-bash")
}

func TestJustBashSessionLastsWhileTurnRootIsHeld(t *testing.T) {
	t.Parallel()

	workspace, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	server := newJustBashServer(t)
	settings, _, err := justbash.ParseSettings(map[string]any{"shell": "just-bash"})
	require.NoError(t, err)

	session, err := server.Register(justbash.Turn{Workspace: workspace, RuntimeDir: filepath.Join(workspace, ".rocketclaw"), Agents: map[string]justbash.Settings{"main": settings}, Env: map[string]string{rocketclawConversationIDEnv: "c1"}})
	require.NoError(t, err)

	launcher, err := server.Launcher("main")
	require.NoError(t, err)

	run := func() (string, error) {
		cmd := exec.CommandContext(t.Context(), launcher, "echo ok")
		cmd.Dir = workspace
		cmd.Env = os.Environ()

		for k, v := range session.Env() {
			cmd.Env = append(cmd.Env, k+"="+v)
		}

		out, err := cmd.CombinedOutput()

		return string(out), err
	}

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	shared := &turnRoot{root: root, users: 1, justbash: session}
	shared.hold()    // A background job of the turn.
	shared.release() // The turn ends.

	out, err := run()
	require.NoError(t, err, out)
	require.Equal(t, "ok\n", out)

	shared.release() // The job ends.

	out, err = run()
	require.Error(t, err)
	require.Contains(t, out, "unknown or ended just-bash session")
}

func TestJustBashGoalCheckAndWorkflowWorker(t *testing.T) {
	t.Parallel()

	workspace, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	check := filepath.Join(workspace, "checks", "check.sh")
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-6.1-sol\nshell: just-bash\nworkingDirectory: checks\npermission:\n  bash:\n    \"*\": allow\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "workflows"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".rocketclaw", "workflows", "where.star"), []byte("meta = {\"name\": \"where\", \"description\": \"Where\"}\ndef main(args): return agent(\"print\")\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Dir(check), 0o755))
	// just-bash runs a goal check as a bash script, from the agent's folder.
	require.NoError(t, os.WriteFile(check, []byte("#!/bin/bash\nprintf 'checked in %s' \"$(pwd)\"\n"), 0o755))

	service := newTestSessionServiceAt(t, workspace)
	recordTestConversations(t, service, "main", map[string]ConversationKind{"thread-1": KindWebChat})

	outputs := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []struct{ Type, Output string }
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			return
		}

		w.Header().Set("Content-Type", "application/json")

		if last := body.Input[len(body.Input)-1]; last.Type == "function_call_output" {
			outputs <- last.Output

			writeRawRunMessage(t, w, "done", "message", "done")

			return
		}

		writeRawRunFunctionCall(t, w, "print", "execute", executeBashScript("pwd"))
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIKey: "test", APIBaseURL: server.URL}}
	finals := make(chan *protocol.OutboundMessage, 4)
	bridge := NewConversation(config.NewLockedConfig(cfg), finalsPublisher{finals: finals}, &Config{ConversationID: "thread-1", Agent: "main", RequestRestart: &runtimeRestarterMock{restartFunc: testNoopRestart}, SessionService: service}, slog.New(slog.DiscardHandler))
	bridge.justbash = newJustBashServer(t)

	t.Run("goal check", func(t *testing.T) {
		root, err := os.OpenRoot(workspace)
		require.NoError(t, err)
		t.Cleanup(func() { _ = root.Close() })

		agents, _, err := readRocketCodeDefinitionsIn(root, cfg, ".rocketclaw", toolModePersistent)
		require.NoError(t, err)

		env, session, err := bridge.startJustBash(agents, nil, "thread-1")
		require.NoError(t, err)
		t.Cleanup(session.Release)

		output, passed := bridge.runGoalCheck(t.Context(), check, env)
		require.True(t, passed, output)
		require.Equal(t, "checked in "+filepath.Dir(check), output)
	})

	t.Run("workflow worker", func(t *testing.T) {
		runTestBridge(t, bridge)

		msg := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "$workflow where", true)
		msg.Workflow = protocol.WorkflowInvocation{Name: "where"}
		require.NoError(t, bridge.Submit(t.Context(), msg))

		readFinal(t, finals)
		require.Contains(t, <-outputs, filepath.Dir(check))
	})
}
