package harnessbridge

import (
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPToolsNotInRocketClawAutoAllowList(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.4\nmode: primary\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	root := openWorkspaceRoot(t, workspace)

	agents, _, err := loadRocketCodeDefinitions(root, workspace, toolModePersistent)
	require.NoError(t, err)

	action, matched := agents.Items["main"].Permission.Evaluate("rocketclaw", "execute")
	assert.False(t, matched)
	assert.Equal(t, rocketcode.PermissionDeny, action)

	action, matched = agents.Items["main"].Permission.Evaluate("mcp", "execute")
	assert.False(t, matched)
	assert.Equal(t, rocketcode.PermissionDeny, action)
}

func TestRocketcodeConfigIncludesMCPServers(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Workspace: t.TempDir(),
		MCPServers: map[string]config.MCPServerConfig{
			"demo": {URL: "http://127.0.0.1:9"},
		},
	}
	bridge := NewConversation(cfg, nil, &Config{ConversationID: "c", Agent: "main", StartNewThread: testNoopStartNewThread, SessionService: newTestSessionService(t)}, slog.New(slog.DiscardHandler))
	rc := bridge.rocketcodeConfig(t.TempDir(), nil, nil)
	require.Len(t, rc.MCPServers, 1)
	assert.Equal(t, "http://127.0.0.1:9", rc.MCPServers["demo"].URL)
	assert.Equal(t, cfg.Workspace, rc.MCPWorkspace)

	empty := NewConversation(&config.Config{Workspace: t.TempDir()}, nil, &Config{ConversationID: "c2", Agent: "main", StartNewThread: testNoopStartNewThread, SessionService: newTestSessionService(t)}, slog.New(slog.DiscardHandler))
	emptyRC := empty.rocketcodeConfig(t.TempDir(), nil, nil)
	assert.Nil(t, emptyRC.MCPServers)
}

func TestWorkflowPrepareOmitsMCPTools(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeMainAgentSkills(t, workspace, "---\ndescription: Main\nmodel: gpt-5.5\npermission:\n  read: {\"*\": allow}\n  mcp: {\"demo.*\": allow}\n---\nPrompt\n")

	cfg := &config.Config{
		Workspace: workspace,
		MCPServers: map[string]config.MCPServerConfig{
			"demo": {URL: "http://127.0.0.1:9"},
		},
	}
	root, agents, skills, resolver, err := prepareRocketCode(cfg, "main", slog.New(slog.DiscardHandler), toolModeWorkflow)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	require.NoError(t, root.MkdirAll(filepath.ToSlash(filepath.Join(cfg.RuntimeDirName(), ".rocketcode")), 0o755))
	shellRel := filepath.ToSlash(filepath.Join(cfg.RuntimeDirName(), ".rocketcode", "wf-mcp"))
	require.NoError(t, root.Mkdir(shellRel, 0o700))

	// Workflow path does not pass MCPServers; host grants still register execute.
	// RestrictTools keeps execute and strips task / direct host tools.
	runtime, err := rocketcode.NewWithModelResolver(resolver, &rocketcode.Config{
		ShellOutputDir: filepath.Join(cfg.Workspace, filepath.FromSlash(shellRel)),
		Diagnostics:    true,
		ChildRunLogger: rocketcode.DiscardChildRunLog,
		CheckpointSink: rocketcode.InertCheckpointSink{},
	}, root, agents, skills, "main", io.Discard)
	require.NoError(t, err)

	tools := slices.DeleteFunc(slices.Collect(maps.Keys(runtime.Tools)), func(name string) bool {
		return name == "task" || rocketcode.CodeModeOnlyHostTool(name)
	})
	require.NoError(t, runtime.RestrictTools(tools))

	_, hasExecute := runtime.Tools["execute"]
	assert.True(t, hasExecute)

	_, hasRead := runtime.Tools["read"]
	assert.False(t, hasRead)

	_, hasTask := runtime.Tools["task"]
	assert.False(t, hasTask)
}
