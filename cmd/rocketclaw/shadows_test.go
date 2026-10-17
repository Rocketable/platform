package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/skel"
	"github.com/stretchr/testify/require"
)

func TestMCPAgentIndexKeepsShadowsOutOfSelectableAgents(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll("agents/parent", 0o700))
	for _, name := range []string{"parent.md", "parent/a.md", "parent/b.md"} {
		require.NoError(t, root.WriteFile(filepath.Join("agents", name), []byte("---\nmodel: gpt-6.1-sol\n---\nAnswer the prompt."), 0o600))
	}

	cfg := &config.Config{Workspace: root.Name()}
	require.NoError(t, skel.SyncEffectiveRuntimeAssets(cfg.Workspace, filepath.Join(cfg.Workspace, cfg.RuntimeDirName()), cfg.Overlays, slog.New(slog.DiscardHandler)))
	index := &mcpAgentIndex{cfg: config.NewLockedConfig(cfg)}
	require.NoError(t, index.Refresh())
	require.True(t, index.Exposed("parent"))
	require.False(t, index.Exposed("parent/a"))
	require.False(t, index.Exposed("parent/b"))
	require.NoError(t, index.Refresh(), "reloading must not turn shadows into standalone choices")
	require.False(t, index.Exposed("parent/a"))
}
