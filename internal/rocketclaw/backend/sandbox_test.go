package backend

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/sandbox"
)

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

	warnings, err := validateRuntimeAssets(config.NewLockedConfig(&config.Config{Workspace: workspace}), ".rocketclaw", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.Equal(t, "Agent bad was not loaded: workingDirectory: required non-empty string.", warnings)
}

func TestSandboxErrorReachesWeb(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "plain", "---\ndescription: Plain\nmodel: gpt-6.1-sol\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	rt := &Runtime{Cfg: config.NewLockedConfig(&config.Config{Workspace: workspace}), Log: slog.New(slog.DiscardHandler), RunCtx: t.Context(), Sessions: &SessionService{}, sandboxes: sandbox.Bubblewrap{}}
	rt.prepareSandbox()
	require.Empty(t, rt.SandboxError(), "no emulated agent, no sandbox")

	t.Setenv("PATH", t.TempDir())

	writeAgent(t, workspace, "boxed", "---\nmode: easy\nmodel: gpt-6.1-sol\nspace: Ops\nworkingDirectory: data\n---\nPrompt\n")
	rt.prepareSandbox()
	err := rt.Sessions.shells.current().Wait(t.Context())
	require.Error(t, err, "preparation ends with a reason, which the Web then shows")
	require.Equal(t, "Emulated shell unavailable: "+err.Error(), rt.SandboxError())

	agents := filepath.Join(workspace, ".rocketclaw", "agents")
	require.NoError(t, os.RemoveAll(agents))
	require.NoError(t, os.WriteFile(agents, []byte("not a folder"), 0o644))
	rt.prepareSandbox()
	require.Contains(t, rt.SandboxError(), "Emulated shell unavailable: RocketClaw couldn't read its agents to tell whether one needs the sandbox: read agents dir: ")

	rt.Sessions.shells.errSandbox = sandbox.ErrPreparing
	require.Equal(t, "Emulated shell getting ready: the first start builds the sandbox's Ubuntu image, which takes a minute or two; commands wait for it", rt.SandboxError())
}
