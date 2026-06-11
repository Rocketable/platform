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
		called := false
		prompt := "plain prompt"

		got := expandPromptShellCommands(prompt, func(command string) string {
			called = true
			return command
		})

		require.Equal(t, prompt, got)
		require.False(t, called)
	})

	t.Run("replaces every shell substitution with runner output", func(t *testing.T) {
		commands := []string{}
		got := expandPromptShellCommands("before !`git status` middle !`pwd` after", func(command string) string {
			commands = append(commands, command)
			switch command {
			case "git status":
				return "STATUS"
			case "pwd":
				return "/tmp/project"
			default:
				return ""
			}
		})

		require.Equal(t, []string{"git status", "pwd"}, commands)
		require.Equal(t, "before STATUS middle /tmp/project after", got)
	})

	t.Run("allows empty runner output", func(t *testing.T) {
		got := expandPromptShellCommands("prefix !`false` suffix", func(string) string { return "" })

		require.Equal(t, "prefix  suffix", got)
	})

	t.Run("does not treat empty backticks as a shell command", func(t *testing.T) {
		called := false
		prompt := "before !`` after"

		got := expandPromptShellCommands(prompt, func(command string) string {
			called = true
			return command
		})

		require.Equal(t, prompt, got)
		require.False(t, called)
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
		shellOutput := testPromptShellOutputConfig(t, root, dir)
		env, err := newPromptExpansionEnvironment(root, shellOutput, nil)
		require.NoError(t, err)

		original := Agent{Name: "review", Description: "", Model: "", ReasoningEffort: "", Verbosity: "", MaxRecursion: nil, Prompt: "review !`cat MEMORY.md`", Location: "", Permission: PermissionSet{Buckets: nil}, Frontmatter: nil, FileMode: 0}
		got := original

		expandAgentPrompt(context.Background(), &got, true, &env)

		require.Equal(t, "review workspace memory", got.Prompt)
		require.Equal(t, "review !`cat MEMORY.md`", original.Prompt)
	})
}

func TestPromptExpansionEnvironmentRunsCommandsInRoot(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	require.NoError(t, root.WriteFile("MEMORY.md", []byte("expanded"), 0o644))
	shellOutput := testPromptShellOutputConfig(t, root, dir)
	env, err := newPromptExpansionEnvironment(root, shellOutput, nil)
	require.NoError(t, err)

	got := env.expandShellCommands(context.Background(), "!`cat MEMORY.md`")

	require.Equal(t, "expanded", got)
}

func TestPromptExpansionEnvironmentAppliesShellEnv(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	shellOutput := testPromptShellOutputConfig(t, root, dir)
	t.Setenv("ROCKETCLAW_CONVERSATION_ID", "old")

	env, err := newPromptExpansionEnvironment(root, shellOutput, []string{"ROCKETCLAW_CONVERSATION_ID=new"})
	require.NoError(t, err)

	got := env.expandShellCommands(context.Background(), "!`printf %s \"$ROCKETCLAW_CONVERSATION_ID\"`")

	require.Equal(t, "new", got)
}

func TestPromptExpansionEnvironmentForcesTMPDIR(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	shellOutput := testPromptShellOutputConfig(t, root, dir)
	t.Setenv("TMPDIR", "/process/tmp")

	env, err := newPromptExpansionEnvironment(root, shellOutput, nil)
	require.NoError(t, err)

	got := env.expandShellCommands(context.Background(), "!`printf %s \"$TMPDIR\"`")

	require.Equal(t, filepath.Join(dir, ".tmp", "shell-outputs", "tmp"), got)
}

func TestNewPromptExpansionEnvironmentRejectsInvalidSetup(t *testing.T) {
	t.Run("nil root", func(t *testing.T) {
		var shellOutput shellOutputConfig

		_, err := newPromptExpansionEnvironment(nil, shellOutput, nil)

		require.EqualError(t, err, "prompt expansion root is required")
	})

	t.Run("closed root", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		require.NoError(t, err)
		require.NoError(t, root.Close())

		var shellOutput shellOutputConfig

		_, err = newPromptExpansionEnvironment(root, shellOutput, nil)

		require.ErrorContains(t, err, "stat prompt expansion root")
	})
}

func testPromptExpansionEnvironment(t *testing.T) promptExpansionEnvironment {
	t.Helper()

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	shellOutput := testPromptShellOutputConfig(t, root, dir)
	env, err := newPromptExpansionEnvironment(root, shellOutput, nil)
	require.NoError(t, err)

	return env
}

func testPromptShellOutputConfig(t *testing.T, root *os.Root, dir string) shellOutputConfig {
	t.Helper()

	outputDir := filepath.Join(dir, ".tmp", "shell-outputs")
	require.NoError(t, root.MkdirAll(filepath.Join(".tmp", "shell-outputs"), 0o755))

	return testShellOutputConfig(t, root, outputDir)
}
