package rocketcode

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSandboxedShellSystemBash(t *testing.T) {
	dir := t.TempDir()

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	outputDir := filepath.Join(dir, ".tmp", "shell-outputs")
	require.NoError(t, root.MkdirAll(filepath.Join(".tmp", "shell-outputs"), 0o755))

	require.NoError(t, root.MkdirAll("nested", 0o755))
	require.NoError(t, root.WriteFile("root.txt", []byte("root\n"), 0o644))
	require.NoError(t, root.WriteFile("nested/file.txt", []byte("nested\n"), 0o644))
	require.NoError(t, root.WriteFile(".env", []byte("SECRET=value\n"), 0o644))
	require.NoError(t, root.WriteFile(".env.example", []byte("SECRET=example\n"), 0o644))

	shellOutput := testShellOutputConfig(t, root, outputDir)
	sss := newSandboxedShellSystem(root, &shellOutput, nil, false)

	t.Run("basic success", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "echo test", Timeout: 0, Workdir: "", Description: "Echo test"})
		require.Contains(t, got, "test")
	})
	t.Run("captures stderr", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "echo stdout_msg && echo stderr_msg >&2", Timeout: 0, Workdir: "", Description: "stderr"})
		require.Contains(t, got, "stdout_msg")
		require.Contains(t, got, "stderr_msg")
	})
	t.Run("empty output", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "true", Timeout: 0, Workdir: "", Description: "No output"})
		require.Equal(t, "(no output)", got)
	})
	t.Run("non zero exit includes metadata", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "exit 42", Timeout: 0, Workdir: "", Description: "Non zero"})
		require.Contains(t, got, "(no output)")
		require.Contains(t, got, "Command exited with code 42")
	})
	t.Run("default workdir is sandbox root", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "pwd", Timeout: 0, Workdir: "", Description: "pwd"})
		require.Contains(t, got, dir)
	})
	t.Run("nested workdir is honored", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "pwd && ls", Timeout: 0, Workdir: filepath.Join(dir, "nested"), Description: "nested pwd"})
		require.Contains(t, got, "file.txt")
	})

	t.Run("external workdir is rejected", func(t *testing.T) {
		workdir := t.TempDir()
		got := sss.Bash(context.Background(), bashParams{Command: "pwd", Timeout: 0, Workdir: workdir, Description: "external pwd"})
		require.Equal(t, fmt.Sprintf("resolve workdir %q: path escapes root: %s", workdir, workdir), got)
	})

	t.Run("direct external file access is denied", func(t *testing.T) {
		externalDir := t.TempDir()
		externalFile := filepath.Join(externalDir, "secret.txt")
		require.NoError(t, os.WriteFile(externalFile, []byte("secret\n"), 0o644))

		got := sss.Bash(context.Background(), bashParams{Command: "cat " + externalFile, Timeout: 0, Workdir: "", Description: "external cat"})
		require.Contains(t, got, "bash command denied: external path access is blocked")
		require.Contains(t, got, externalFile)
	})

	t.Run("relative external file access is denied", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "cat ../outside.txt", Timeout: 0, Workdir: "", Description: "relative external cat"})
		require.Equal(t, "bash command denied: external path access is blocked: ../outside.txt", got)
	})

	t.Run("external cd is denied", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "cd /tmp", Timeout: 0, Workdir: "", Description: "external cd"})
		require.Equal(t, "bash command denied: external path access is blocked: /tmp", got)
	})

	t.Run("direct env file access is denied", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "cat .env", Timeout: 0, Workdir: "", Description: "env cat"})
		require.Equal(t, "bash command denied: "+deniedEnvAccessMessage(".env"), got)
	})

	t.Run("env example file access is allowed", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "cat .env.example", Timeout: 0, Workdir: "", Description: "env example cat"})
		require.Contains(t, got, "SECRET=example")
	})

	t.Run("timeout adds metadata and preserves output", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: "echo started && sleep 10", Timeout: 100, Workdir: "", Description: "timeout"})
		require.Contains(t, got, "started")
		require.Contains(t, got, "bash tool terminated command after exceeding timeout 100 ms")
	})

	t.Run("sets tmpdir under shell output dir", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: `printf %s "$TMPDIR"`, Timeout: 0, Workdir: "", Description: "tmpdir"})
		require.Equal(t, filepath.Join(outputDir, "tmp"), got)

		info, err := os.Stat(filepath.Join(outputDir, "tmp"))
		require.NoError(t, err)
		require.Equal(t, fs.FileMode(0o700), info.Mode().Perm())
	})

	t.Run("applies configured env", func(t *testing.T) {
		sss := newSandboxedShellSystem(root, &shellOutput, []string{"ROCKETCLAW_CONVERSATION_ID=configured"}, false)

		got := sss.Bash(context.Background(), bashParams{Command: `printf %s "$ROCKETCLAW_CONVERSATION_ID"`, Timeout: 0, Workdir: "", Description: "configured env"})

		require.Equal(t, "configured", got)
	})

	t.Run("configured env overrides process env", func(t *testing.T) {
		t.Setenv("ROCKETCLAW_CONVERSATION_ID", "old")

		sss := newSandboxedShellSystem(root, &shellOutput, []string{"ROCKETCLAW_CONVERSATION_ID=new"}, false)

		got := sss.Bash(context.Background(), bashParams{Command: `printf %s "$ROCKETCLAW_CONVERSATION_ID"`, Timeout: 0, Workdir: "", Description: "override env"})

		require.Equal(t, "new", got)
	})

	t.Run("tmpdir overrides configured env", func(t *testing.T) {
		sss := newSandboxedShellSystem(root, &shellOutput, []string{"TMPDIR=/not/rocketcode"}, false)

		got := sss.Bash(context.Background(), bashParams{Command: `printf %s "$TMPDIR"`, Timeout: 0, Workdir: "", Description: "tmpdir precedence"})

		require.Equal(t, filepath.Join(outputDir, "tmp"), got)
	})

	t.Run("mktemp uses shell output tmpdir", func(t *testing.T) {
		got := sss.Bash(context.Background(), bashParams{Command: `tmp="$TMPDIR/script-temp"; touch "$tmp"; printf %s "$tmp"`, Timeout: 0, Workdir: "", Description: "mktemp"})
		tempPath := strings.TrimSpace(got)
		rel, err := filepath.Rel(filepath.Join(outputDir, "tmp"), tempPath)
		require.NoError(t, err)
		require.NotContains(t, rel, "..")
	})

	t.Run("truncates output exceeding line limit", func(t *testing.T) {
		cmd := "i=1; while [ $i -le 2100 ]; do echo $i; i=$((i+1)); done"
		got := sss.Bash(context.Background(), bashParams{Command: cmd, Timeout: 0, Workdir: "", Description: "many lines"})
		require.Contains(t, got, "...output truncated...")
		require.Contains(t, got, "Full output saved to:")
		require.NotContains(t, got, "1\n2\n3")
		require.Contains(t, got, "2099")
		require.Contains(t, got, "2100")
	})

	t.Run("truncates output exceeding byte limit", func(t *testing.T) {
		cmd := "i=1; while [ $i -le 60000 ]; do printf a; i=$((i+1)); done"
		got := sss.Bash(context.Background(), bashParams{Command: cmd, Timeout: 0, Workdir: "", Description: "many bytes"})
		require.Contains(t, got, "...output truncated...")
		require.Contains(t, got, "Full output saved to:")
	})

	t.Run("saved full output contains all lines", func(t *testing.T) {
		cmd := "i=1; while [ $i -le 2100 ]; do echo $i; i=$((i+1)); done"
		got := sss.Bash(context.Background(), bashParams{Command: cmd, Timeout: 0, Workdir: "", Description: "saved output"})
		path := savedOutputPath(t, got)
		require.Equal(t, filepath.ToSlash(filepath.Join(".tmp", "shell-outputs")), filepath.ToSlash(filepath.Dir(path)))

		content, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
		require.NoError(t, err)

		lines := strings.Split(strings.TrimSpace(string(content)), "\n")
		require.Len(t, lines, 2100)
		require.Equal(t, "1", lines[0])
		require.Equal(t, "2100", lines[len(lines)-1])
	})

	t.Run("temp files garbage collect after ttl", func(t *testing.T) {
		path := filepath.ToSlash(filepath.Join(".tmp", "shell-outputs", "rocketcode-bash-old"))
		require.NoError(t, root.WriteFile(path, []byte("old"), 0o600))

		sss.tempFiles[temporaryFile(path)] = creationTime(time.Now().Add(-shellTempFileTTL - time.Minute))
		_ = sss.Bash(context.Background(), bashParams{Command: "true", Timeout: 0, Workdir: "", Description: "gc trigger"})

		_, err := root.Stat(path)
		require.Error(t, err)
		require.True(t, os.IsNotExist(err), "expected temp file removal, got %v", err)
	})
}

func TestSandboxedShellSystemSandboxedBashMissingPlatformTool(t *testing.T) {
	dir := t.TempDir()

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	outputDir := filepath.Join(dir, ".tmp", "shell-outputs")
	require.NoError(t, root.MkdirAll(filepath.Join(".tmp", "shell-outputs"), 0o755))

	shellOutput := testShellOutputConfig(t, root, outputDir)
	sss := newSandboxedShellSystem(root, &shellOutput, nil, true)

	t.Setenv("PATH", "")

	got := sss.Bash(context.Background(), bashParams{Command: "true", Timeout: 0, Workdir: "", Description: "sandbox missing tool"})

	require.Contains(t, got, "sandboxed bash:")
	require.Contains(t, got, "not found")
}

func TestSandboxedBashResolvePathsUsesRoot(t *testing.T) {
	dir := t.TempDir()

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	outputDir := filepath.Join(dir, ".tmp", "shell-outputs")
	require.NoError(t, root.MkdirAll(filepath.Join(".tmp", "shell-outputs", "tmp"), 0o700))
	require.NoError(t, root.MkdirAll("nested", 0o755))

	bash := newSandboxedBash(root, testShellOutputConfig(t, root, outputDir), nil)

	paths, err := bash.resolvePaths("nested")
	require.NoError(t, err)
	require.Equal(t, filepath.Clean(dir), filepath.Clean(paths.workspace))
	require.Equal(t, filepath.Join(dir, "nested"), filepath.Clean(paths.workdir))
	require.Equal(t, filepath.Join(outputDir, "tmp"), paths.tmp)
	require.Equal(t, "/work/nested", paths.sandboxWorkdir)
	require.Equal(t, "/work/.tmp/shell-outputs/tmp", paths.sandboxTmp)

	_, err = bash.resolvePaths("../outside")
	require.ErrorContains(t, err, "resolve sandboxed bash workdir")
}

func TestSandboxedBashEnv(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")

	got := sandboxedBashEnv("/work", "/work/nested", "/work/.tmp/shell-outputs/tmp", []string{"PATH=/custom/bin", "FOO=bar"}, "/usr/bin:/bin")

	require.Equal(t, []string{
		"PATH=/custom/bin",
		"HOME=/work",
		"PWD=/work/nested",
		"TMPDIR=/work/.tmp/shell-outputs/tmp",
		"TERM=xterm-256color",
		"FOO=bar",
	}, got)
}

func testShellOutputConfig(t *testing.T, root *os.Root, outputDir string) shellOutputConfig {
	t.Helper()

	config, err := newShellOutputConfig(root, outputDir)
	require.NoError(t, err)

	return config
}

func savedOutputPath(t *testing.T, output string) string {
	t.Helper()

	re := regexp.MustCompile(`Full output saved to: (\S+)`)
	match := re.FindStringSubmatch(output)
	require.Len(t, match, 2)

	return match[1]
}
