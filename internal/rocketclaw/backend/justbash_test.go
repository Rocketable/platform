package backend

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
)

// Every path that loads agents gives a shell: emulated agent the just-bash wrapper as its shell.
func TestLoadedJustBashAgentRunsThroughWrapper(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "boxed", "---\ndescription: Boxed\nmodel: gpt-6.1-sol\nshell: emulated\nworkingDirectory: data\ncommands: {hello: printf hi}\n---\nPrompt\n")
	writeAgent(t, workspace, "plain", "---\ndescription: Plain\nmodel: gpt-6.1-sol\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	agents, _, err := LoadRuntimeDefinitions(config.NewLockedConfig(&config.Config{Workspace: workspace}), ".rocketclaw")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(workspace, ".rocketclaw", "justbash", "wrapper.ts"), agents.Items["boxed"].CustomShell.Path)
	require.JSONEq(t, `{"workspace":"`+workspace+`","runtimeDir":"`+filepath.Join(workspace, ".rocketclaw")+`","workingDirectory":"data","commands":{"hello":"printf hi"}}`, string(agents.Items["boxed"].CustomShell.Setup))
	require.Empty(t, agents.Items["plain"].CustomShell)

	writeAgent(t, workspace, "bad", "---\ndescription: Bad\nmodel: gpt-6.1-sol\nshell: emulated\n---\nPrompt\n")

	_, _, err = LoadRuntimeDefinitions(config.NewLockedConfig(&config.Config{Workspace: workspace}), ".rocketclaw")
	require.ErrorContains(t, err, "bad.md: workingDirectory: required with shell: emulated")
}

// A failed install does not fail startup or reload; the Web shows why.
func TestJustBashInstallErrorReachesWeb(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "plain", "---\ndescription: Plain\nmodel: gpt-6.1-sol\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	rt := &Runtime{Cfg: config.NewLockedConfig(&config.Config{Workspace: workspace}), Log: slog.New(slog.DiscardHandler)}
	rt.installJustBash()
	require.Empty(t, rt.JustBashInstallError(), "no emulated agent, no install")

	writeAgent(t, workspace, "boxed", "---\nmode: easy\nmodel: gpt-6.1-sol\nspace: Ops\nworkingDirectory: data\n---\nPrompt\n")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".rocketclaw", "justbash"), nil, 0o600)) // A file blocks the install directory.
	rt.installJustBash()
	require.Contains(t, rt.JustBashInstallError(), "install just-bash")
}
