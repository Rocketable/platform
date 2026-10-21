package rocketcode

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExpandPromptShellCommands(t *testing.T) {
	t.Run("returns original prompt when there are no substitutions", func(t *testing.T) {
		env := testPromptExpansionEnvironment(t)
		prompt := "plain prompt"

		got := env.expandShellCommands(t.Context(), prompt)

		require.Equal(t, prompt, got)
	})

	t.Run("replaces every shell substitution with runner output", func(t *testing.T) {
		env := testPromptExpansionEnvironment(t)
		got := env.expandShellCommands(t.Context(), "before !`printf STATUS; printf 1 >> order.txt` middle !`printf /workspace/project; printf 2 >> order.txt` after")

		order, err := env.root.ReadFile("order.txt")
		require.NoError(t, err)
		require.Equal(t, "12", string(order))
		require.Equal(t, "before STATUS middle /workspace/project after", got)
	})

	t.Run("allows empty runner output", func(t *testing.T) {
		env := testPromptExpansionEnvironment(t)
		got := env.expandShellCommands(t.Context(), "prefix !`false` suffix")

		require.Equal(t, "prefix  suffix", got)
	})

	t.Run("does not treat empty backticks as a shell command", func(t *testing.T) {
		env := testPromptExpansionEnvironment(t)
		prompt := "before !`` after"

		got := env.expandShellCommands(t.Context(), prompt)

		require.Equal(t, prompt, got)
	})
}

func TestExpandAgentPrompt(t *testing.T) {
	t.Run("leaves prompt unchanged when disabled", func(t *testing.T) {
		original := Agent{Name: "review", Description: "", Model: "", ReasoningEffort: "", Verbosity: "", MaxRecursion: nil, Prompt: "review !`git status`", Location: "", Permission: PermissionSet{Buckets: nil}, Frontmatter: nil, FileMode: 0}
		got := original

		env := testPromptExpansionEnvironment(t)
		expandAgentPrompt(context.Background(), &got, false, &env)

		require.Equal(t, original, got)
	})

	t.Run("expands prompt when enabled", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, root.Close()) })

		require.NoError(t, root.WriteFile("MEMORY.md", []byte("workspace memory"), 0o644))
		shellTemp := testPromptShellTempConfig(t, root, dir)
		env, err := newPromptExpansionEnvironment(root, shellTemp, nil, DefaultShellCommand{})
		require.NoError(t, err)

		original := Agent{Name: "review", Description: "", Model: "", ReasoningEffort: "", Verbosity: "", MaxRecursion: nil, Prompt: "review !`cat MEMORY.md`", Location: "", Permission: PermissionSet{Buckets: nil}, Frontmatter: nil, FileMode: 0}
		got := original

		expandAgentPrompt(context.Background(), &got, true, &env)

		require.Equal(t, "review workspace memory", got.Prompt)
		require.Equal(t, "review !`cat MEMORY.md`", original.Prompt)
	})

	t.Run("expands through the agent custom shell", func(t *testing.T) {
		env := testPromptExpansionEnvironment(t)
		got := Agent{Name: "review", Prompt: "review !`echo hi`", CustomShell: writeCustomShell(t, env.root)}

		expandAgentPrompt(context.Background(), &got, true, &env)

		require.Equal(t, "review custom:1:echo hi", got.Prompt)
	})

	t.Run("missing custom shell expands to empty text", func(t *testing.T) {
		env := testPromptExpansionEnvironment(t)
		got := Agent{Name: "review", Prompt: "review !`echo hi` done", CustomShell: filepath.Join(env.root.Name(), "missing")}

		expandAgentPrompt(context.Background(), &got, true, &env)

		require.Equal(t, "review  done", got.Prompt)
	})
}

func TestPromptExpansionEnvironmentRunsCommandsInRoot(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	require.NoError(t, root.WriteFile("MEMORY.md", []byte("expanded"), 0o644))
	shellTemp := testPromptShellTempConfig(t, root, dir)
	env, err := newPromptExpansionEnvironment(root, shellTemp, nil, DefaultShellCommand{})
	require.NoError(t, err)

	got := env.expandShellCommands(context.Background(), "before !`cat MEMORY.md` middle !`cat MEMORY.md; exit 7` after")

	require.Equal(t, "before expanded middle expanded after", got)
}

func TestPromptExpansionEnvironmentAppliesShellEnv(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	shellTemp := testPromptShellTempConfig(t, root, dir)
	t.Setenv("ROCKETCLAW_CONVERSATION_ID", "old")

	env, err := newPromptExpansionEnvironment(root, shellTemp, []string{"ROCKETCLAW_CONVERSATION_ID=new"}, DefaultShellCommand{})
	require.NoError(t, err)

	got := env.expandShellCommands(context.Background(), "!`printf %s \"$ROCKETCLAW_CONVERSATION_ID\"`")

	require.Equal(t, "new", got)
}

func TestPromptExpansionEnvironmentForcesTMPDIR(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	shellTemp := testPromptShellTempConfig(t, root, dir)
	t.Setenv("TMPDIR", "/process/tmp")

	env, err := newPromptExpansionEnvironment(root, shellTemp, nil, DefaultShellCommand{})
	require.NoError(t, err)

	got := env.expandShellCommands(context.Background(), "!`printf %s \"$TMPDIR\"`")

	require.Equal(t, filepath.Join(dir, ".tmp", "shell-tmp"), got)
}

func TestNewPromptExpansionEnvironmentRejectsInvalidSetup(t *testing.T) {
	t.Run("nil root", func(t *testing.T) {
		var shellTemp shellTempConfig

		_, err := newPromptExpansionEnvironment(nil, shellTemp, nil, DefaultShellCommand{})

		require.EqualError(t, err, "prompt expansion root is required")
	})

	t.Run("closed root", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		require.NoError(t, err)
		require.NoError(t, root.Close())

		var shellTemp shellTempConfig

		_, err = newPromptExpansionEnvironment(root, shellTemp, nil, DefaultShellCommand{})

		require.ErrorContains(t, err, "stat prompt expansion root")
	})
}

func testPromptExpansionEnvironment(t *testing.T) promptExpansionEnvironment {
	t.Helper()

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	shellTemp := testPromptShellTempConfig(t, root, dir)
	env, err := newPromptExpansionEnvironment(root, shellTemp, nil, DefaultShellCommand{})
	require.NoError(t, err)

	return env
}

func testPromptShellTempConfig(t *testing.T, root *os.Root, dir string) shellTempConfig {
	t.Helper()

	outputDir := filepath.Join(dir, ".tmp", "shell-tmp")
	require.NoError(t, root.MkdirAll(filepath.Join(".tmp", "shell-tmp"), 0o755))

	return testShellTempConfig(t, root, outputDir)
}
