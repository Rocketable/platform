package backend

import (
	"log/slog"
	"os"
	"path/filepath"
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

	agents, _, err := loadRocketCodeDefinitionsIn(root, config.NewLockedConfig(&config.Config{Workspace: workspace}), config.DefaultRuntimeDir, nil)
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
				Tools:   map[string][]string{"read": {"list_*"}, "write": {"update_*"}},
			},
		},
	}
	want := mcpclient.ServerConfig{Command: "demo-command", Args: []string{"--stdio"}, Env: map[string]string{"DEMO": "value"}, Cwd: "demo-dir", URL: "http://127.0.0.1:9", Headers: map[string]string{"Authorization": "test-token"}}
	store := newTestSessionService(t)
	bridge := NewConversation(config.NewLockedConfig(cfg), discardPublisher{}, &Config{ConversationID: "c", Agent: "main", RequestReload: &runtimeReloaderMock{ReloadFunc: testNoopRestart}, RequestRestart: &runtimeRestarterMock{RestartFunc: func(string) string { return "" }}, UserQuestionAsker: &webQuestions{store: store, waiting: map[webQuestionKey]chan protocol.AskUserQuestionAnswer{}}, SessionService: store}, slog.New(slog.DiscardHandler))
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

	empty := NewConversation(config.NewLockedConfig(&config.Config{Workspace: t.TempDir()}), discardPublisher{}, &Config{ConversationID: "c2", Agent: "main", RequestReload: &runtimeReloaderMock{ReloadFunc: testNoopRestart}, RequestRestart: &runtimeRestarterMock{RestartFunc: func(string) string { return "" }}, UserQuestionAsker: &webQuestions{store: store, waiting: map[webQuestionKey]chan protocol.AskUserQuestionAnswer{}}, SessionService: store}, slog.New(slog.DiscardHandler))
	emptyRC := empty.rocketcodeConfig(t.TempDir(), nil)
	assert.Nil(t, emptyRC.MCPServers)
}
