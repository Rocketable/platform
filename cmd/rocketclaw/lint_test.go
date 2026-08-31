package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
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

func TestRunLintResolvesConfiguredModel(t *testing.T) {
	for _, target := range []string{"current", "next"} {
		t.Run(target, func(t *testing.T) {
			workspace := t.TempDir()
			t.Chdir(workspace)
			writeLintConfig(t, workspace, map[string]string{"coding-high": "software-development-sol"})

			root := workspace
			if target == "current" {
				root = filepath.Join(workspace, ".rocketclaw")
			}
			writeLintAgent(t, root, "main.md", "---\ndescription: main\n---\nmain\n", `{{ model "coding-high" }}`)

			output := captureStdout(t, func() error { return runLint([]string{target}) })
			assert.Equal(t, "rocketclaw lint "+target+": OK\n", output)
		})
	}
}

func TestRunLintDefaultUsesNext(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeLintConfig(t, workspace)
	writeLintAgent(t, workspace, "lint-bad.md", `---
description: bad
permissions:
  shell:
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

func TestRunLintReasoningEffortXHighFails(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeLintConfig(t, workspace)
	writeLintAgent(t, filepath.Join(workspace, ".rocketclaw"), "expensive.md", `---
description: expensive
reasoningEffort: xhigh
---
expensive
`)

	output, err := captureStdoutAndError(t, func() error { return runLint([]string{"current"}) })
	var coded exitCoder
	require.True(t, errors.As(err, &coded))
	assert.Equal(t, 1, coded.ExitCode())
	assert.Contains(t, output, "rocketclaw lint current: found 1 findings")
	assert.Contains(t, output, "RC008 error agents/expensive.md")
}

func TestRunLintRejectsUnknownTarget(t *testing.T) {
	err := runLint([]string{"later"})
	require.ErrorContains(t, err, "usage: rocketclaw lint [next|current]")
}

func TestHelpMentionsLint(t *testing.T) {
	output := captureStdout(t, func() error { return run([]string{"help"}) })
	assert.Contains(t, output, "rocketclaw lint [next|current]")
}

func writeLintConfig(t *testing.T, workspace string, models ...map[string]string) {
	t.Helper()
	data := config.Config{Workspace: workspace, DatabaseURL: "postgres://localhost/rocketclaw_test?sslmode=disable", Slack: config.SlackConfig{BotToken: "xoxb", AppToken: "xapp", Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"main"}, AllowedUserIDs: []string{"U123"}}}}, OpenAI: config.OpenAIConfig{APIKey: "test"}}
	if len(models) > 0 {
		data.Models = models[0]
	}
	content, err := json.Marshal(data)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(defaultConfigPath, content, 0o600))
}

func writeLintAgent(t *testing.T, root, name, content string, models ...string) {
	t.Helper()
	model := "gpt-5.5"
	if len(models) > 0 {
		model = models[0]
	}
	content = fmt.Sprintf("---\nmodel: %q\n", model) + content[len("---\n"):]

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
