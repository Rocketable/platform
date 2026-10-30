package backend

import (
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/mcpclient"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
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
	bridge := NewConversation(config.NewLockedConfig(cfg), discardPublisher{}, &Config{ConversationID: "c", Agent: "main", RequestReload: &runtimeReloaderMock{ReloadFunc: testNoopRestart}, RequestRestart: &runtimeRestarterMock{restartFunc: testNoopRestart}, UserQuestionAsker: &webQuestions{store: store, waiting: map[webQuestionKey]chan protocol.AskUserQuestionAnswer{}}, SessionService: store}, slog.New(slog.DiscardHandler))
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

	empty := NewConversation(config.NewLockedConfig(&config.Config{Workspace: t.TempDir()}), discardPublisher{}, &Config{ConversationID: "c2", Agent: "main", RequestReload: &runtimeReloaderMock{ReloadFunc: testNoopRestart}, RequestRestart: &runtimeRestarterMock{restartFunc: testNoopRestart}, UserQuestionAsker: &webQuestions{store: store, waiting: map[webQuestionKey]chan protocol.AskUserQuestionAnswer{}}, SessionService: store}, slog.New(slog.DiscardHandler))
	emptyRC := empty.rocketcodeConfig(t.TempDir(), nil)
	assert.Nil(t, emptyRC.MCPServers)
}

func TestWorkflowPrepareOmitsMCPTools(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeMainAgentSkills(t, workspace, "---\ndescription: Main\nmodel: gpt-6.1-sol\npermission:\n  read: {\"*\": allow}\n  mcp: {\"demo.*\": allow}\n---\nPrompt\n")

	cfg := &config.Config{
		Workspace: workspace,
		MCPServers: map[string]config.MCPServerConfig{
			"demo": {URL: "http://127.0.0.1:9"},
		},
	}
	root, agents, skills, resolver, err := prepareRocketCode(config.NewLockedConfig(cfg), "main", slog.New(slog.DiscardHandler), toolModeWorkflow)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	require.NoError(t, root.MkdirAll(filepath.ToSlash(filepath.Join(cfg.RuntimeDirName(), ".rocketcode")), 0o755))
	shellRel := filepath.ToSlash(filepath.Join(cfg.RuntimeDirName(), ".rocketcode", "wf-mcp"))
	require.NoError(t, root.Mkdir(shellRel, 0o700))

	// Workflow path does not pass MCPServers; host grants still register execute.
	// RestrictTools keeps execute and strips task / direct host tools.
	runtime, err := harness.NewWithModelResolver(resolver, &harness.Config{
		ShellTempDir:      filepath.Join(cfg.Workspace, filepath.FromSlash(shellRel)),
		RetainedResultDir: "retained",
		Diagnostics:       true,
		ChildSessions:     harness.InertChildSessions{},
		Journal:           harness.InertJournal{},
		ShellCommand:      harness.DefaultShellCommand{},
	}, root, agents, skills, "main", io.Discard)
	require.NoError(t, err)

	tools := slices.DeleteFunc(slices.Collect(maps.Keys(runtime.Tools)), func(name string) bool {
		return name == "task" || harness.CodeModeOnlyHostTool(name)
	})
	require.NoError(t, runtime.RestrictTools(tools))

	_, hasExecute := runtime.Tools["execute"]
	assert.True(t, hasExecute)

	_, hasRead := runtime.Tools["read"]
	assert.False(t, hasRead)

	_, hasTask := runtime.Tools["task"]
	assert.False(t, hasTask)
}
