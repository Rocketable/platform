package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunLintCurrentOK(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeLintConfig(t, workspace)
	writeLintAgent(t, filepath.Join(workspace, ".rocketclaw"), "main.md", `---
description: main
---
main
`)

	output := captureStdout(t, func() error { return runLint([]string{"current"}) })
	assert.Equal(t, "rocketclaw lint current: OK\n", output)
}

func TestRunLintDefaultUsesNext(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeLintConfig(t, workspace)
	writeLintAgent(t, workspace, "lint-bad.md", `---
description: bad
permissions:
  bash:
    "echo ok": allow
---
bad
`)

	output, err := captureStdoutAndError(t, func() error { return runLint(nil) })
	var coded exitCoder
	require.True(t, errors.As(err, &coded))
	assert.Equal(t, 1, coded.ExitCode())
	assert.Contains(t, output, "rocketclaw lint next: found")
	assert.Contains(t, output, "RC006 error agents/lint-bad.md")
}

func TestRunLintRejectsUnknownTarget(t *testing.T) {
	err := runLint([]string{"later"})
	require.ErrorContains(t, err, "usage: rocketclaw lint [next|current]")
}

func TestHelpMentionsLint(t *testing.T) {
	output := captureStdout(t, func() error { return run([]string{"help"}) })
	assert.Contains(t, output, "rocketclaw lint [next|current]")
}

func writeLintConfig(t *testing.T, workspace string) {
	t.Helper()
	workspaceJSON, err := json.Marshal(workspace)
	require.NoError(t, err)
	content := `{"workspace":` + string(workspaceJSON) + `,"web_ui":{"enabled":true,"listen_addr":"127.0.0.1:8766"},"openai":{"api_key":"test"}}`
	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(content), 0o600))
}

func writeLintAgent(t *testing.T, root, name, content string) {
	t.Helper()
	agentsRoot := filepath.Join(root, "agents")
	require.NoError(t, os.MkdirAll(agentsRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentsRoot, name), []byte(content), 0o644))
}

func captureStdoutAndError(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	oldStdout := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = oldStdout }()
	errCall := fn()
	require.NoError(t, writer.Close())
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	return string(data), errCall
}
