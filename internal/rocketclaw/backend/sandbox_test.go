package backend

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
)

// Every path that loads agents gives a shell: emulated agent the emulated shell, set up per turn, with its
// workingDirectory and commands; one without a workingDirectory does not load.
func TestLoadedEmulatedAgent(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "boxed", "---\ndescription: Boxed\nmodel: gpt-6.1-sol\nshell: emulated\nworkingDirectory: data\ncommands: {hello: printf hi}\n---\nPrompt\n")
	writeAgent(t, workspace, "plain", "---\ndescription: Plain\nmodel: gpt-6.1-sol\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	agents, _, err := LoadRuntimeDefinitions(config.NewLockedConfig(&config.Config{Workspace: workspace}), ".rocketclaw")
	require.NoError(t, err)
	require.Equal(t, harness.CustomShell{Emulated: true}, agents.Items["boxed"].CustomShell)
	require.Equal(t, "data", agents.Items["boxed"].WorkingDirectory)
	require.Equal(t, map[string]string{"hello": "printf hi"}, agents.Items["boxed"].Commands)
	require.Empty(t, agents.Items["plain"].CustomShell)

	writeAgent(t, workspace, "bad", "---\ndescription: Bad\nmodel: gpt-6.1-sol\nshell: emulated\n---\nPrompt\n")

	_, _, err = LoadRuntimeDefinitions(config.NewLockedConfig(&config.Config{Workspace: workspace}), ".rocketclaw")
	require.ErrorContains(t, err, "bad.md: workingDirectory: required non-empty string")
}

// A sandbox that cannot run does not fail startup or reload; the Web shows why, once an agent needs it.
func TestSandboxErrorReachesWeb(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "plain", "---\ndescription: Plain\nmodel: gpt-6.1-sol\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	rt := &Runtime{Cfg: config.NewLockedConfig(&config.Config{Workspace: workspace}), Log: slog.New(slog.DiscardHandler), RunCtx: t.Context(), Sessions: &SessionService{}}
	rt.prepareSandbox()
	require.Empty(t, rt.SandboxError(), "no emulated agent, no sandbox")

	writeAgent(t, workspace, "boxed", "---\nmode: easy\nmodel: gpt-6.1-sol\nspace: Ops\nworkingDirectory: data\n---\nPrompt\n")
	rt.prepareSandbox()
	require.Contains(t, rt.SandboxError(), "prepare sandbox: ", "no sandbox here: this build embeds no rootfs, and off Linux there is none")
}
