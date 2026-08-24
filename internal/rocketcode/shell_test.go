package rocketcode

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSandboxedShellSystemBash(t *testing.T) {
	dir := t.TempDir()

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	outputDir := filepath.Join(dir, ".tmp", "shell-tmp")
	require.NoError(t, root.MkdirAll(filepath.Join(".tmp", "shell-tmp"), 0o755))

	require.NoError(t, root.MkdirAll("nested", 0o755))
	require.NoError(t, root.WriteFile("root.txt", []byte("root\n"), 0o644))
	require.NoError(t, root.WriteFile("nested/file.txt", []byte("nested\n"), 0o644))
	require.NoError(t, root.WriteFile(".env", []byte("SECRET=value\n"), 0o644))
	require.NoError(t, root.WriteFile(".env.example", []byte("SECRET=example\n"), 0o644))

	shellTemp := testShellTempConfig(t, root, outputDir)
	sss := newSandboxedShellSystem(root, &shellTemp, nil, DefaultShellCommand)

	t.Run("basic success", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "echo test", TimeoutMillisecond: 0, Workdir: "", Description: "Echo test"}).String()
		require.Contains(t, got, "test")
	})
	t.Run("captures stderr", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "echo stdout_msg && echo stderr_msg >&2", TimeoutMillisecond: 0, Workdir: "", Description: "stderr"}).String()
		require.Contains(t, got, "stdout_msg")
		require.Contains(t, got, "stderr_msg")
	})
	t.Run("empty output", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "true", TimeoutMillisecond: 0, Workdir: "", Description: "No output"}).String()
		require.Equal(t, "(no output)", got)
	})
	t.Run("non zero exit sets error code", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "exit 42", TimeoutMillisecond: 0, Workdir: "", Description: "Non zero"})
		require.Equal(t, "(no output)", got.String())
		require.Equal(t, "42", got.ErrorCode)
		require.False(t, got.Success)
	})
	t.Run("default workdir is sandbox root", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "pwd", TimeoutMillisecond: 0, Workdir: "", Description: "pwd"}).String()
		require.Contains(t, got, dir)
	})
	t.Run("nested workdir is honored", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "pwd && ls", TimeoutMillisecond: 0, Workdir: filepath.Join(dir, "nested"), Description: "nested pwd"}).String()
		require.Contains(t, got, "file.txt")
	})

	t.Run("external workdir is rejected", func(t *testing.T) {
		workdir := t.TempDir()
		got := sss.Bash(context.Background(), bashParams{Command: "pwd", TimeoutMillisecond: 0, Workdir: workdir, Description: "external pwd"}).String()
		require.Equal(t, fmt.Sprintf("resolve workdir %q: path escapes root: %s", workdir, workdir), got)
	})

	t.Run("direct external file access is denied", func(t *testing.T) {
		externalDir := t.TempDir()
		externalFile := filepath.Join(externalDir, "secret.txt")
		require.NoError(t, os.WriteFile(externalFile, []byte("secret\n"), 0o644))

		got := sss.Bash(context.Background(), bashParams{Command: "cat " + externalFile, TimeoutMillisecond: 0, Workdir: "", Description: "external cat"}).String()
		require.Contains(t, got, "bash command denied: external path access is blocked")
		require.Contains(t, got, externalFile)
	})

	t.Run("relative external file access is denied", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "cat ../outside.txt", TimeoutMillisecond: 0, Workdir: "", Description: "relative external cat"}).String()
		require.Equal(t, "bash command denied: external path access is blocked: ../outside.txt", got)
	})

	t.Run("external cd is denied", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "cd /tmp", TimeoutMillisecond: 0, Workdir: "", Description: "external cd"}).String()
		require.Equal(t, "bash command denied: external path access is blocked: /tmp", got)
	})

	t.Run("direct env file access is denied", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "cat .env", TimeoutMillisecond: 0, Workdir: "", Description: "env cat"}).String()
		require.Equal(t, "bash command denied: "+deniedEnvAccessMessage(".env"), got)
	})

	t.Run("env example file access is allowed", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "cat .env.example", TimeoutMillisecond: 0, Workdir: "", Description: "env example cat"}).String()
		require.Contains(t, got, "SECRET=example")
	})

	t.Run("timeout sets error code and preserves output", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "echo started && sleep 10", TimeoutMillisecond: 100, Workdir: "", Description: "timeout"})
		require.Contains(t, got.String(), "started")
		require.Equal(t, "timeout", got.ErrorCode)
		require.False(t, got.Success)
	})

	t.Run("sets tmpdir to shell temp dir", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: `printf %s "$TMPDIR"`, TimeoutMillisecond: 0, Workdir: "", Description: "tmpdir"}).String()
		require.Equal(t, outputDir, got)

		info, err := os.Stat(outputDir)
		require.NoError(t, err)
		require.Equal(t, fs.FileMode(0o700), info.Mode().Perm())
	})

	t.Run("applies configured env", func(t *testing.T) {
		sss := newSandboxedShellSystem(root, &shellTemp, []string{"ROCKETCLAW_CONVERSATION_ID=configured"}, DefaultShellCommand)

		got := sss.Bash(context.Background(), bashParams{Command: `printf %s "$ROCKETCLAW_CONVERSATION_ID"`, TimeoutMillisecond: 0, Workdir: "", Description: "configured env"})

		require.Equal(t, "configured", got.String())
	})

	t.Run("configured env overrides process env", func(t *testing.T) {
		t.Setenv("ROCKETCLAW_CONVERSATION_ID", "old")

		sss := newSandboxedShellSystem(root, &shellTemp, []string{"ROCKETCLAW_CONVERSATION_ID=new"}, DefaultShellCommand)

		got := sss.Bash(context.Background(), bashParams{Command: `printf %s "$ROCKETCLAW_CONVERSATION_ID"`, TimeoutMillisecond: 0, Workdir: "", Description: "override env"})

		require.Equal(t, "new", got.String())
	})

	t.Run("tmpdir overrides configured env", func(t *testing.T) {
		sss := newSandboxedShellSystem(root, &shellTemp, []string{"TMPDIR=/not/rocketcode"}, DefaultShellCommand)

		got := sss.Bash(context.Background(), bashParams{Command: `printf %s "$TMPDIR"`, TimeoutMillisecond: 0, Workdir: "", Description: "tmpdir precedence"})

		require.Equal(t, outputDir, got.String())
	})

	t.Run("mktemp uses shell temp dir", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: `tmp="$TMPDIR/script-temp"; touch "$tmp"; printf %s "$tmp"`, TimeoutMillisecond: 0, Workdir: "", Description: "mktemp"})
		tempPath := strings.TrimSpace(got.String())
		rel, err := filepath.Rel(outputDir, tempPath)
		require.NoError(t, err)
		require.NotContains(t, rel, "..")
	})

	t.Run("returns full multi-line output", func(t *testing.T) {
		cmd := "i=1; while [ $i -le 2100 ]; do echo $i; i=$((i+1)); done"
		got := sss.Bash(context.Background(), bashParams{Command: cmd, TimeoutMillisecond: 0, Workdir: "", Description: "many lines"})
		require.Equal(t, got.Output, got.String())
		require.Contains(t, got.Output, "1\n2\n3")
		require.Contains(t, got.Output, "2099\n2100")
		require.NotContains(t, got.Output, "...output truncated...")
		require.NotContains(t, got.Output, "full_output")
		require.Empty(t, got.ErrorCode)
		require.True(t, got.Success)
	})
}

func testShellTempConfig(t *testing.T, root *os.Root, outputDir string) shellTempConfig {
	t.Helper()

	config, err := newShellTempConfig(root, outputDir)
	require.NoError(t, err)

	return config
}

func TestShellCommandOverride(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	outputDir := filepath.Join(dir, ".tmp", "shell-tmp")
	require.NoError(t, root.MkdirAll(filepath.Join(".tmp", "shell-tmp"), 0o755))
	shellTemp := testShellTempConfig(t, root, outputDir)

	var saw string

	sss := newSandboxedShellSystem(root, &shellTemp, nil, func(command string) (string, []string) {
		saw = command
		return "/bin/sh", []string{"-c", "printf mocked"}
	})
	got := sss.Bash(context.Background(), bashParams{Command: "gh pr view 1", TimeoutMillisecond: 0})

	require.Equal(t, "gh pr view 1", saw)
	require.Contains(t, got.String(), "mocked")
}
