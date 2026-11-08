package backend

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/mcpclient"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPToolsNotInRocketClawAutoAllowList(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.4\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	root := openWorkspaceRoot(t, workspace)

	agents, _, err := loadRocketCodeDefinitionsIn(root, config.NewLockedConfig(&config.Config{Workspace: workspace}), config.DefaultRuntimeDir, toolModePersistent)
	require.NoError(t, err)

	action, matched := agents.Items["main"].Permission.Evaluate("rocketclaw", "execute")
	assert.False(t, matched)
	assert.Equal(t, harness.PermissionDeny, action)

	action, matched = agents.Items["main"].Permission.Evaluate("mcp", "execute")
	assert.False(t, matched)
	assert.Equal(t, harness.PermissionDeny, action)
}

func TestRocketcodeConfigIncludesMCPServers(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Workspace: t.TempDir(),
		MCPServers: map[string]config.MCPServerConfig{
			"demo": {
				Command: "demo-command",
				Args:    []string{"--stdio"},
				Env:     map[string]string{"DEMO": "value"},
				Cwd:     "demo-dir",
				URL:     "http://127.0.0.1:9",
				Headers: map[string]string{"Authorization": "test-token"},
				Tools:   config.MCPToolGroups{Read: []string{"list_*"}, Write: []string{"update_*"}},
			},
		},
	}
	want := mcpclient.ServerConfig{Command: "demo-command", Args: []string{"--stdio"}, Env: map[string]string{"DEMO": "value"}, Cwd: "demo-dir", URL: "http://127.0.0.1:9", Headers: map[string]string{"Authorization": "test-token"}}
	store := newTestSessionService(t)
	bridge := NewConversation(config.NewLockedConfig(cfg), discardPublisher{}, &Config{ConversationID: "c", Agent: "main", RequestReload: &runtimeReloaderMock{ReloadFunc: testNoopRestart}, RequestRestart: &runtimeRestarterMock{restartFunc: func(string) string { return "" }}, UserQuestionAsker: &webQuestions{store: store, waiting: map[webQuestionKey]chan protocol.AskUserQuestionAnswer{}}, SessionService: store}, slog.New(slog.DiscardHandler))
	rc := bridge.rocketcodeConfig(t.TempDir(), nil)
	require.Len(t, rc.MCPServers, 1)
	assert.Equal(t, want, rc.MCPServers["demo"])
	assert.Equal(t, cfg.Workspace, rc.MCPWorkspace)

	converted := bridge.rocketcodeConfig(t.TempDir(), nil).MCPServers
	converted["demo"].Args[0] = "changed"
	converted["demo"].Env["DEMO"] = "changed"
	converted["demo"].Headers["Authorization"] = "changed"

	assert.Equal(t, want, rc.MCPServers["demo"])
	assert.Equal(t, rc.MCPServers, bridge.rocketcodeConfig(t.TempDir(), nil).MCPServers)

	empty := NewConversation(config.NewLockedConfig(&config.Config{Workspace: t.TempDir()}), discardPublisher{}, &Config{ConversationID: "c2", Agent: "main", RequestReload: &runtimeReloaderMock{ReloadFunc: testNoopRestart}, RequestRestart: &runtimeRestarterMock{restartFunc: func(string) string { return "" }}, UserQuestionAsker: &webQuestions{store: store, waiting: map[webQuestionKey]chan protocol.AskUserQuestionAnswer{}}, SessionService: store}, slog.New(slog.DiscardHandler))
	emptyRC := empty.rocketcodeConfig(t.TempDir(), nil)
	assert.Nil(t, emptyRC.MCPServers)
}

func TestWorkflowPrepareOmitsMCPTools(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeMainAgentSkills(t, workspace, "---\ndescription: Main\nmodel: gpt-6.1-sol\npermission:\n  read: {\"*\": allow}\n  mcp: {\"demo.*\": allow}\n---\nPrompt\n")

	requests := make(chan []string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools        []struct{ Name string }
			Instructions string
		}
		if err := json.NewDecoder(r.Body).Decode(&body); !assert.NoError(t, err) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		assert.NotContains(t, body.Instructions, "MCP namespaces:", "workflow workers must not advertise configured MCP servers")

		names := make([]string, len(body.Tools))
		for i, tool := range body.Tools {
			names[i] = tool.Name
		}

		requests <- names

		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "response", "message", "ok")
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{
		Workspace: workspace,
		OpenAI:    config.OpenAIConfig{APIBaseURL: server.URL},
		MCPServers: map[string]config.MCPServerConfig{
			"demo": {URL: "http://127.0.0.1:9"},
		},
	}
	run, err := newWorkflowAgentRunner(config.NewLockedConfig(cfg), &sandbox.Runtime{}, "main", harness.InertJournal{}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, run.Close()) })

	result, err := run.Run(t.Context(), &workflow.AgentRequest{Prompt: "probe"})
	require.NoError(t, err)
	require.JSONEq(t, `"ok"`, string(result))

	tools := <-requests
	assert.Contains(t, tools, "execute")
	assert.NotContains(t, tools, "read")
	assert.NotContains(t, tools, "task")
}
