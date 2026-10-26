package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunLintTargets(t *testing.T) {
	for _, target := range []string{"current", "next"} {
		t.Run(target, func(t *testing.T) {
			workspace := t.TempDir()
			t.Chdir(workspace)
			writeLintConfig(t, workspace)
			serveLint(t)

			root := workspace
			if target == "current" {
				root = filepath.Join(workspace, ".rocketclaw")
			}
			writeLintAgent(t, root, "main.md", "---\ndescription: main\n---\nmain\n")

			output := captureStdout(t, func() error { return runLint([]string{target}) })
			assert.Equal(t, "rocketclaw lint "+target+": OK\n", output)
		})
	}
}

func TestRunLintDefaultUsesNext(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeLintConfig(t, workspace)
	serveLint(t)
	writeLintAgent(t, workspace, "lint-bad.md", `---
description: bad
permissions:
  bash:
    "echo ok": allow
---
bad
`)

	output, err := captureStdoutAndError(t, func() error { return runLint(nil) })
	assert.Equal(t, exitCodeError(1), err)
	assert.Contains(t, output, "rocketclaw lint next: found")
	assert.Contains(t, output, "RC006 error agents/lint-bad.md: plural permissions frontmatter is ignored; use permission\n")
}

// The server's own error is told apart from an unreachable server, and neither prints a result.
func TestRunLintReportsServerFailures(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeLintConfig(t, workspace)
	writeLintAgent(t, filepath.Join(workspace, ".rocketclaw"), "main.md", "---\nmode: easy\nspace: Ops\nworkingDirectory: data\ndescription: main\n---\nmain\n")

	_, cfg, err := loadRuntimeConfig("")
	require.NoError(t, err)
	_, port, err := net.SplitHostPort(cfg.Web.ListenAddress)
	require.NoError(t, err)

	output, err := captureStdoutAndError(t, func() error { return runLint([]string{"current"}) })
	assert.Empty(t, output)
	require.ErrorContains(t, err, "could not reach the running rocketclaw server at 127.0.0.1:"+port)
	assert.Equal(t, 1, exitCodeForError(err))

	serveLint(t)
	output, err = captureStdoutAndError(t, func() error { return runLint([]string{"current"}) })
	assert.Empty(t, output)
	require.ErrorContains(t, err, "rocketclaw server at 127.0.0.1:"+port+" failed lint")
	require.ErrorContains(t, err, "load agents: main.md: description: not an easy-mode key")
	assert.Equal(t, 1, exitCodeForError(err))
}

// A server too old to know InspectAgents answers with a plain-text 404.
func TestRunLintReportsOldServer(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeLintConfig(t, workspace)

	_, cfg, err := loadRuntimeConfig("")
	require.NoError(t, err)
	listener, err := net.Listen("tcp", cfg.Web.ListenAddress)
	require.NoError(t, err)

	server := httptest.NewUnstartedServer(http.NotFoundHandler())
	require.NoError(t, server.Listener.Close())
	server.Listener = listener
	server.StartTLS()
	t.Cleanup(server.Close)

	output, err := captureStdoutAndError(t, func() error { return runLint([]string{"current"}) })
	assert.Empty(t, output)
	require.ErrorContains(t, err, "the running rocketclaw server does not support this command; restart it after upgrading")
}

// Lint loads easy agents as startup does, and checks their routines with the cron manager's own check.
func TestRunLintEasyAgents(t *testing.T) {
	easy := "---\nmode: easy\nspace: Ops\nworkingDirectory: data\n"
	for _, tt := range []struct{ name, agent, err string }{
		{"valid", easy + "routines: [30m]\n---\nmain\n", ""},
		{"unknown key", easy + "description: main\n---\nmain\n", "load agents: main.md: description: not an easy-mode key"},
		{"repeated routine", easy + "routines: [30m, 30m]\n---\nmain\n", `validate cron definitions: agents/main.md: routines: schedule "30m" is listed twice`},
		{"timestamp routine", easy + "routines: [\"2026-11-15T09:00:00Z\"]\n---\nmain\n", `validate cron definitions: agents/main.md: routines: schedule "2026-11-15T09:00:00Z": a routine repeats; for a single future run use rocketclaw_schedule_message`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			t.Chdir(workspace)
			writeLintConfig(t, workspace)
			serveLint(t)
			writeLintAgent(t, workspace, "main.md", tt.agent)

			output, err := captureStdoutAndError(t, func() error { return runLint(nil) })
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, "rocketclaw lint next: OK\n", output)
		})
	}
}

func TestRunLintRejectsUnknownTarget(t *testing.T) {
	err := runLint([]string{"later"})
	require.ErrorContains(t, err, "usage: rocketclaw lint [next|current]")
}

// Settings print the live Behavior Settings without who may use the server.
func TestRunSettings(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeLintConfig(t, workspace)
	serveLint(t)

	output := captureStdout(t, func() error { return runSettings(nil) })

	var settings config.Behavior
	require.NoError(t, json.Unmarshal([]byte(output), &settings))
	assert.Equal(t, "gpt-6.1-sol", settings.AutoApproverModel)
	assert.Equal(t, []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"main"}}}, settings.Slack.Channels)
	assert.Contains(t, output, "{\n  \"auto_approver_model\": \"gpt-6.1-sol\",\n")
	assert.NotContains(t, output, "U123")

	require.ErrorContains(t, runSettings([]string{"current"}), "usage: rocketclaw settings")
}

// writeLintConfig writes a rocketclaw.json whose Web listens on a loopback port that was free a moment ago.
func writeLintConfig(t *testing.T, workspace string) {
	t.Helper()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	require.NoError(t, probe.Close())

	data := config.Config{Workspace: workspace, DatabaseURL: "postgres://localhost/rocketclaw_test?sslmode=disable", Web: config.WebConfig{ListenAddress: probe.Addr().String()}, Slack: config.SlackConfig{BotToken: "xoxb", AppToken: "xapp", Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"main"}, AllowedUserIDs: []string{"U123"}}}}, OpenAI: config.OpenAIConfig{APIKey: "test"}, AutoApproverModel: "gpt-6.1-sol"}
	content, err := json.Marshal(data)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(defaultConfigPath, content, 0o600))
}

// serveLint starts the Web for the rocketclaw.json in the working directory, as rocketclaw run serves it.
func serveLint(t *testing.T) {
	t.Helper()
	_, cfg, err := loadRuntimeConfig("")
	require.NoError(t, err)
	stop, err := startWebRPC(&backend.Runtime{Cfg: config.NewLockedConfig(cfg), Log: slog.New(slog.DiscardHandler), Sessions: webSessions(t), Assemble: processAssembler{}}, &mockWebChannels{}, &mockWebCron{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stop(t.Context())) })
}

func writeLintAgent(t *testing.T, root, name, content string) {
	t.Helper()
	content = "---\nmodel: gpt-6.1-sol\n" + content[len("---\n"):]

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
