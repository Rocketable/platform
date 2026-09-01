package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/backend/harnessbridgetest"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteExecOutcomeSuccess(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		result backend.RawRunResult
		want   string
	}{
		{
			name:   "text only",
			result: backend.RawRunResult{Text: "all done"},
			want:   "{\"type\":\"result\",\"text\":\"all done\",\"ok\":true}\n",
		},
		{
			name:   "with attachments",
			result: backend.RawRunResult{Text: "all done", Attachments: []protocol.OutboundAttachment{{Name: "report.md"}, {Name: "log.txt"}}},
			want:   "{\"type\":\"result\",\"text\":\"all done\",\"attachments\":[\"report.md\",\"log.txt\"],\"ok\":true}\n",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var out bytes.Buffer

			require.NoError(t, writeExecOutcome(execEventWriter{out: &out}, testCase.result, nil))
			assert.Equal(t, testCase.want, out.String())
		})
	}
}

// execRunnerNotCalled returns a runner that fails the test if the run ever starts.
func execRunnerNotCalled(t *testing.T) execRunner {
	t.Helper()

	return func(context.Context, *config.Config, string, string, *slog.Logger, *backend.RawRunProgress) (backend.RawRunResult, error) {
		t.Fatal("exec run must not start")

		return backend.RawRunResult{}, nil
	}
}

func TestRunExecInRejectsInvalidInput(t *testing.T) {
	for _, testCase := range []struct {
		name string
		args []string
		want string
	}{
		{name: "missing prompt", args: []string{"triage"}, want: "exec requires exactly one agent and one prompt"},
		{name: "extra positional", args: []string{"triage", "do it", "extra"}, want: "exec requires exactly one agent and one prompt"},
		{name: "blank agent", args: []string{"  ", "do it"}, want: "exec agent is required"},
		{name: "blank prompt", args: []string{"triage", "  "}, want: "exec prompt is required"},
		{name: "bad timeout", args: []string{"--timeout", "soon", "triage", "do it"}, want: "parse exec flags"},
		{name: "negative timeout", args: []string{"--timeout", "-5s", "triage", "do it"}, want: "exec timeout must be non-negative"},
		{name: "missing config", args: []string{"triage", "do it"}, want: "load config"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Chdir(t.TempDir())

			var out bytes.Buffer

			require.ErrorContains(t, runExecIn(t.Context(), testCase.args, &out, execRunnerNotCalled(t)), testCase.want)
			assert.Empty(t, out.String())
		})
	}
}

func TestRunExecInRejectsUnknownAgent(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeLintConfig(t, workspace)
	writeLintAgent(t, filepath.Join(workspace, config.DefaultRuntimeDir), "main.md", "---\n---\nMain agent.\n")

	var out bytes.Buffer

	require.ErrorContains(t, runExecIn(t.Context(), []string{"missing", "do it"}, &out, execRunnerNotCalled(t)), `unknown agent "missing"`)
	assert.Empty(t, out.String())
}

func TestRunExecInPrintsHelpInAnyArgumentPosition(t *testing.T) {
	for _, testCase := range []struct {
		name string
		args []string
	}{
		{name: "no arguments", args: []string{}},
		{name: "after flags", args: []string{"--timeout", "1m", "--help"}},
		{name: "after prompt", args: []string{"triage", "do it", "-h"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			args := testCase.args
			t.Chdir(t.TempDir())

			var out bytes.Buffer

			require.NoError(t, runExecIn(t.Context(), args, &out, execRunnerNotCalled(t)))
			assert.Contains(t, out.String(), "rocketclaw exec [--timeout")
			assert.Contains(t, out.String(), "One JSON object per line on stdout")
		})
	}
}

func TestRunExecInPassesPromptVerbatim(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(defaultConfigPath, []byte(fmt.Sprintf(`{"workspace":%q,"database_url":%q,"slack":{"bot_token":"xoxb","app_token":"xapp","channels":[{"channel":"#ops","agents":["main"],"allowed_user_ids":["U123"]}]},"openai":{"api_key":"test"}}`, workspace, dsn)), 0o600))
	writeLintAgent(t, filepath.Join(workspace, config.DefaultRuntimeDir), "main.md", "---\n---\nMain agent.\n")

	var out bytes.Buffer

	seen := ""
	run := func(_ context.Context, _ *config.Config, _, prompt string, _ *slog.Logger, _ *backend.RawRunProgress) (backend.RawRunResult, error) {
		seen = prompt

		return backend.RawRunResult{Text: "done"}, nil
	}

	require.NoError(t, runExecIn(t.Context(), []string{"main", "  keep  my spacing  "}, &out, run))
	assert.Equal(t, "  keep  my spacing  ", seen)
}

func TestHelpMentionsExec(t *testing.T) {
	assert.Contains(t, helpText, "rocketclaw exec [--timeout <duration>] <agent> <prompt>")
	assert.Contains(t, helpText, "exec         Run one agent once and print the run as JSONL.")
}

func TestExecuteExecRunStreamsEventsInOrder(t *testing.T) {
	workspace := t.TempDir()

	var out bytes.Buffer

	session := ""
	run := func(ctx context.Context, _ *config.Config, agent, prompt string, _ *slog.Logger, progress *backend.RawRunProgress) (backend.RawRunResult, error) {
		session = progress.ConversationID

		assert.Equal(t, "triage", agent)
		assert.Equal(t, "check logs", prompt)
		require.NoError(t, progress.Thinking(ctx, "reading logs"))
		require.NoError(t, progress.Message(ctx, "found it"))

		_, err := progress.RequestRestart(ctx, "")
		assert.ErrorContains(t, err, "unavailable in rocketclaw exec")

		_, err = progress.RequestReload(ctx, "")
		assert.ErrorContains(t, err, "unavailable in rocketclaw exec")

		return backend.RawRunResult{Text: "found it", VerbatimMessage: "found it"}, nil
	}

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	cfg := &config.Config{Workspace: workspace, DatabaseURL: dsn}
	require.NoError(t, executeExecRun(t.Context(), cfg, "triage", "check logs", 0, slog.New(slog.DiscardHandler), &out, run))

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	require.Len(t, lines, 4)
	assert.Equal(t, "{\"type\":\"start\",\"agent\":\"triage\",\"session\":\""+session+"\"}", lines[0])
	assert.Equal(t, "{\"type\":\"thinking\",\"text\":\"reading logs\"}", lines[1])
	assert.Equal(t, "{\"type\":\"message\",\"text\":\"found it\"}", lines[2])
	assert.Equal(t, "{\"type\":\"result\",\"text\":\"found it\",\"final\":\"found it\",\"ok\":true}", lines[3])
	assert.True(t, strings.HasPrefix(session, "exec-"))
}

func TestExecuteExecRunPersistsSession(t *testing.T) {
	workspace := t.TempDir()

	var out bytes.Buffer

	run := func(ctx context.Context, _ *config.Config, _, _ string, _ *slog.Logger, progress *backend.RawRunProgress) (backend.RawRunResult, error) {
		_, err := progress.SessionService.AppendEntryID(ctx, progress.ConversationID, &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC()})

		return backend.RawRunResult{Text: "found it"}, err
	}

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	cfg := &config.Config{Workspace: workspace, DatabaseURL: dsn}
	require.NoError(t, executeExecRun(t.Context(), cfg, "triage", "check logs", 0, slog.New(slog.DiscardHandler), &out, run))

	summaries, err := backend.ListSessionsInOptions(t.Context(), cfg.DatabaseURL, backend.SessionListOptions{})
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.True(t, strings.HasPrefix(summaries[0].ConversationID, "exec-"))
}

func TestExecuteExecRunEmitsSingleErrorLineOnFailure(t *testing.T) {
	workspace := t.TempDir()

	var out bytes.Buffer

	run := func(context.Context, *config.Config, string, string, *slog.Logger, *backend.RawRunProgress) (backend.RawRunResult, error) {
		return backend.RawRunResult{}, errors.New("model unavailable")
	}

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	cfg := &config.Config{Workspace: workspace, DatabaseURL: dsn}
	err = executeExecRun(t.Context(), cfg, "triage", "check logs", 0, slog.New(slog.DiscardHandler), &out, run)

	var coded exitCodeError

	require.ErrorAs(t, err, &coded)
	assert.Equal(t, 1, coded.ExitCode())

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	require.Len(t, lines, 2)
	assert.Equal(t, "{\"type\":\"error\",\"message\":\"model unavailable\"}", lines[1])
}

func TestExecuteExecRunCancelledDuringRunEmitsStartThenSingleError(t *testing.T) {
	workspace := t.TempDir()

	var out bytes.Buffer

	runCtx, cancel := context.WithCancel(t.Context())
	defer cancel()

	session := ""
	run := func(ctx context.Context, _ *config.Config, _, _ string, _ *slog.Logger, progress *backend.RawRunProgress) (backend.RawRunResult, error) {
		session = progress.ConversationID

		cancel()
		<-ctx.Done()

		return backend.RawRunResult{}, fmt.Errorf("run cancelled: %w", ctx.Err())
	}

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	cfg := &config.Config{Workspace: workspace, DatabaseURL: dsn}
	err = executeExecRun(runCtx, cfg, "triage", "check logs", time.Minute, slog.New(slog.DiscardHandler), &out, run)

	require.Error(t, err)

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	require.Len(t, lines, 2)
	assert.Equal(t, "{\"type\":\"start\",\"agent\":\"triage\",\"session\":\""+session+"\"}", lines[0])
	assert.Equal(t, "{\"type\":\"error\",\"message\":\"run cancelled: context canceled\"}", lines[1])
}

func TestExecuteExecRunCancelledBeforeStartEmitsOnlyError(t *testing.T) {
	workspace := t.TempDir()

	var out bytes.Buffer

	runCtx, cancel := context.WithCancel(t.Context())
	cancel()

	run := func(context.Context, *config.Config, string, string, *slog.Logger, *backend.RawRunProgress) (backend.RawRunResult, error) {
		t.Fatal("run must not start once the context is already done")

		return backend.RawRunResult{}, nil
	}

	dsn, err := harnessbridgetest.IsolatedTestDatabaseURL()
	require.NoError(t, err)
	cfg := &config.Config{Workspace: workspace, DatabaseURL: dsn}
	err = executeExecRun(runCtx, cfg, "triage", "check logs", time.Minute, slog.New(slog.DiscardHandler), &out, run)

	var coded exitCodeError

	require.ErrorAs(t, err, &coded)
	assert.Equal(t, 1, coded.ExitCode())
	assert.Equal(t, "{\"type\":\"error\",\"message\":\"exec cancelled before start: context canceled\"}\n", out.String())
}
