# RocketClaw Exec Command Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `rocketclaw exec [--timeout <duration>] <agent> <prompt>`, a one-shot non-interactive subcommand that runs a named agent and reports the run as JSONL on stdout.

**Architecture:** One new file, `cmd/rocketclaw/exec.go`, plus a dispatch case and help-text entry in `cmd/rocketclaw/main.go`. All run behavior comes from the existing, unmodified `harnessbridge.RunRawWithProgress`; `exec.go` is a thin adapter that validates arguments, opens the session store, mints a conversation ID, converts progress callbacks into JSONL events, and maps the outcome to an exit code. Nothing under `internal/` changes.

**Tech Stack:** Go 1.26.4, standard library only (`encoding/json`, `flag`, `os/signal`, `math/rand/v2`), `github.com/Rocketable/platform/internal/rocketclaw/{config,harnessbridge}`, `testify` for tests.

**Spec:** `internal/rocketclaw/docs/specs/2026-07-25-rocketclaw-exec-command-design.md`

## Global Constraints

- **Never use `git`.** Use `jj` for all repository operations. Diffs are `jj diff --git`.
- Commit messages use the repository's component prefix: `internal/rocketclaw: <summary>`.
- Temporary files go only in `<repo-root>/.tmp/`. Never `/tmp` or `$TMPDIR`.
- Verification gate before finalizing: `gofmt` on touched files, `go test ./...` from the repo root, then `make lint` and `make test` from `internal/rocketclaw`.
- Coverage floor is `COVERAGE_STABLE_AT = 90.0` and coverage must not decrease against the `@-` baseline.
- Source CLOC budget is `GO_SOURCE_CLOC_BUDGET = 15500` across `cmd/rocketclaw` + `internal/rocketclaw` non-test Go, with a 500-line hazard zone. Never edit the budget.
- Do not add `//nolint`. Do not disable linters. Active linters include `wrapcheck` (every returned error must be wrapped with `%w`), `wsl_v5` (blank line before/after blocks), `funcorder`, `gochecknoglobals` (help text must be `const`), `godot` (comments end with a period), `godox` (the literal words `TODO`, `NOTE`, and `GPT` in comments fail the build), `ireturn`, `dupl`, and `depguard` (no `reflect`, no `unsafe`).
- Error variables are named `errX`, not `xErr` (for example `errRun`, `errWrite`).
- No defensive guards, and no nil-as-disabled for injected dependencies — supply an inert implementation instead.
- Unix-only. No Windows code paths.
- Every exported declaration needs a doc comment starting with its name.

---

## File Structure

| File | Responsibility |
| --- | --- |
| `cmd/rocketclaw/exec.go` (create) | The whole `exec` subcommand: help text, event type and writer, argument parsing and validation, run wiring, outcome mapping. |
| `cmd/rocketclaw/exec_test.go` (create) | Tests for event encoding, outcome mapping, argument validation, and the run wiring driven by a fake runner. |
| `cmd/rocketclaw/main.go` (modify) | Add `case "exec"` to the dispatch switch at line 46-66, and add `exec` to the `helpText` const at line 24. |
| `cmd/rocketclaw/CHEATSHEET.md` (modify) | Add `exec` to the subcommand table. |

Everything lives in one file because the command is a single cohesive adapter of roughly 130 source lines; splitting it would fragment one responsibility across files and add import overhead against the CLOC budget.

---

## Task 1: Event Type, Writer, And Outcome Mapping

Build the pure output layer first: the JSONL event struct, the writer, and the function that turns a finished run into either a `result` or an `error` line. These have no dependency on config, SQLite, or a model, so they are fully testable on their own.

**Files:**
- Create: `cmd/rocketclaw/exec.go`
- Create: `cmd/rocketclaw/exec_test.go`

**Interfaces:**
- Consumes: `harnessbridge.RawRunResult` (fields `Text string`, `VerbatimMessage string`, `Attachments []events.OutboundAttachment`, where `OutboundAttachment` has field `Name string`), and `exitCodeError` from `cmd/rocketclaw/main.go:28`.
- Produces: `type execEvent struct`, `type execEventWriter struct`, `func (w execEventWriter) write(event execEvent) error`, `func writeExecOutcome(writer execEventWriter, result harnessbridge.RawRunResult, errRun error) error`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/rocketclaw/exec_test.go`:

```go
package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecEventWriterEncodesOneLinePerEvent(t *testing.T) {
	var out bytes.Buffer

	writer := execEventWriter{out: &out}
	require.NoError(t, writer.write(execEvent{Type: "start", Agent: "triage", Session: "exec-abc"}))
	require.NoError(t, writer.write(execEvent{Type: "thinking", Text: "reading logs"}))

	assert.Equal(t, "{\"type\":\"start\",\"agent\":\"triage\",\"session\":\"exec-abc\"}\n{\"type\":\"thinking\",\"text\":\"reading logs\"}\n", out.String())
}

func TestWriteExecOutcomeSuccess(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		result harnessbridge.RawRunResult
		want   string
	}{
		{
			name:   "text only",
			result: harnessbridge.RawRunResult{Text: "all done"},
			want:   "{\"type\":\"result\",\"text\":\"all done\",\"ok\":true}\n",
		},
		{
			name:   "with final message",
			result: harnessbridge.RawRunResult{Text: "all done", VerbatimMessage: "the answer"},
			want:   "{\"type\":\"result\",\"text\":\"all done\",\"final\":\"the answer\",\"ok\":true}\n",
		},
		{
			name:   "with attachments",
			result: harnessbridge.RawRunResult{Text: "all done", Attachments: []events.OutboundAttachment{{Name: "report.md"}, {Name: "log.txt"}}},
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

func TestWriteExecOutcomeFailureEmitsErrorAndExitCode(t *testing.T) {
	var out bytes.Buffer

	err := writeExecOutcome(execEventWriter{out: &out}, harnessbridge.RawRunResult{}, errors.New("model unavailable"))
	assert.Equal(t, "{\"type\":\"error\",\"message\":\"model unavailable\"}\n", out.String())

	var coded exitCodeError

	require.ErrorAs(t, err, &coded)
	assert.Equal(t, 1, coded.ExitCode())
	assert.Empty(t, err.Error())
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./cmd/rocketclaw/ -run 'TestExecEventWriter|TestWriteExecOutcome' -v
```

Expected: build failure — `undefined: execEventWriter`, `undefined: execEvent`, `undefined: writeExecOutcome`.

- [ ] **Step 3: Write the implementation**

Create `cmd/rocketclaw/exec.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
)

// execEvent is one JSONL line emitted by rocketclaw exec.
type execEvent struct {
	Type        string   `json:"type"`
	Agent       string   `json:"agent,omitempty"`
	Session     string   `json:"session,omitempty"`
	Text        string   `json:"text,omitempty"`
	Final       string   `json:"final,omitempty"`
	Attachments []string `json:"attachments,omitempty"`
	Message     string   `json:"message,omitempty"`
	OK          bool     `json:"ok,omitempty"`
}

// execEventWriter writes exec events as JSONL.
type execEventWriter struct {
	out io.Writer
}

func (w execEventWriter) write(event execEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal exec event: %w", err)
	}

	if _, err := fmt.Fprintf(w.out, "%s\n", data); err != nil {
		return fmt.Errorf("write exec event: %w", err)
	}

	return nil
}

func writeExecOutcome(writer execEventWriter, result harnessbridge.RawRunResult, errRun error) error {
	if errRun != nil {
		if err := writer.write(execEvent{Type: "error", Message: errRun.Error()}); err != nil {
			return err
		}

		return exitCodeError(1)
	}

	names := make([]string, 0, len(result.Attachments))
	for i := range result.Attachments {
		names = append(names, result.Attachments[i].Name)
	}

	return writer.write(execEvent{Type: "result", Text: result.Text, Final: result.VerbatimMessage, Attachments: names, OK: true})
}
```

The failure path returns `exitCodeError(1)`, whose `Error()` is the empty string, so `main` exits 1 without printing anything extra to stderr — the `error` event on stdout is already the report. Field order in the struct determines JSON key order, which is what the exact-match assertions rely on.

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./cmd/rocketclaw/ -run 'TestExecEventWriter|TestWriteExecOutcome' -v
```

Expected: PASS for all four test functions and subtests.

- [ ] **Step 5: Format and commit**

```bash
gofmt -w cmd/rocketclaw/exec.go cmd/rocketclaw/exec_test.go
jj commit cmd/rocketclaw/exec.go cmd/rocketclaw/exec_test.go -m "internal/rocketclaw: add exec JSONL event encoding"
```

---

## Task 2: Command Surface, Validation, Help, And Run Wiring

Add argument parsing, validation, the help text, the dispatch wiring, and the real run: open the session service, mint a conversation ID, install signal and timeout handling, convert progress callbacks into events, and map the outcome. The runner is injected as an `execRunner` so tests drive the whole path without a model. After this task `rocketclaw exec` is complete.

**Files:**
- Modify: `cmd/rocketclaw/exec.go`
- Modify: `cmd/rocketclaw/exec_test.go`
- Modify: `cmd/rocketclaw/main.go:24` (the `helpText` const) and `cmd/rocketclaw/main.go:46-66` (the dispatch switch)
- Modify: `cmd/rocketclaw/CHEATSHEET.md`

**Interfaces:**
- Consumes: `printStdout(text, name string) error` (main.go:120), `loadRuntimeConfig() (runtimeConfigFile, *config.Config, error)` (main.go:76), `harnessbridge.LoadRuntimeDefinitions(cfg *config.Config, runtimeDir string) (rocketcode.Agents, rocketcode.Skills, error)` (bridge.go:1792), `cfg.RuntimeDirName()`, `cfg.Workspace`, `cfg.Logging.Level`, `newLogger(levelText string) *slog.Logger` (logger.go:9), `harnessbridge.NewSessionServiceIn(workspace, runtimeDir string, logger *slog.Logger) (*SessionService, error)` (store.go:198), `(*SessionService).Stop(context.Context) error` (store.go:999), `harnessbridge.RawRunProgress` (raw_run.go:35), `harnessbridge.RunRawWithProgress` (raw_run.go:49), `writeExecOutcome` from Task 1.
- Produces: `func runExec(args []string) error`, `func runExecIn(ctx context.Context, args []string, out io.Writer) error`, `type execRunner`, `func executeExecRun(ctx context.Context, cfg *config.Config, agent, prompt string, timeout time.Duration, logger *slog.Logger, out io.Writer, run execRunner) error`, `const execHelpText`.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/rocketclaw/exec_test.go`:

```go
func TestRunExecPrintsHelpWithoutConfig(t *testing.T) {
	t.Chdir(t.TempDir())

	for _, args := range [][]string{{}, {"help"}, {"-h"}, {"--help"}} {
		output := captureStdout(t, func() error { return runExec(args) })
		assert.Contains(t, output, "rocketclaw exec [--timeout")
		assert.Contains(t, output, "One JSON object per line on stdout")
	}
}

func TestRunExecInRejectsInvalidInput(t *testing.T) {
	for _, testCase := range []struct {
		name string
		args []string
		want string
	}{
		{name: "no positionals", args: []string{}, want: "exec requires exactly one agent and one prompt"},
		{name: "missing prompt", args: []string{"triage"}, want: "exec requires exactly one agent and one prompt"},
		{name: "extra positional", args: []string{"triage", "do it", "extra"}, want: "exec requires exactly one agent and one prompt"},
		{name: "blank agent", args: []string{"  ", "do it"}, want: "exec agent is required"},
		{name: "blank prompt", args: []string{"triage", "  "}, want: "exec prompt is required"},
		{name: "bad timeout", args: []string{"--timeout", "soon", "triage", "do it"}, want: "parse exec flags"},
		{name: "negative timeout", args: []string{"--timeout", "-5s", "triage", "do it"}, want: "exec timeout must be non-negative"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Chdir(t.TempDir())

			var out bytes.Buffer

			require.ErrorContains(t, runExecIn(t.Context(), testCase.args, &out), testCase.want)
			assert.Empty(t, out.String())
		})
	}
}

func TestRunExecInRequiresConfig(t *testing.T) {
	t.Chdir(t.TempDir())

	var out bytes.Buffer

	require.ErrorContains(t, runExecIn(t.Context(), []string{"triage", "do it"}, &out), "load config")
	assert.Empty(t, out.String())
}

func TestRunExecInRejectsUnknownAgent(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	writeLintConfig(t, workspace)
	writeLintAgent(t, filepath.Join(workspace, config.DefaultRuntimeDir), "main.md", "---\n---\nMain agent.\n")

	var out bytes.Buffer

	require.ErrorContains(t, runExecIn(t.Context(), []string{"missing", "do it"}, &out), `unknown agent "missing"`)
	assert.Empty(t, out.String())
}

func TestHelpMentionsExec(t *testing.T) {
	assert.Contains(t, helpText, "rocketclaw exec [--timeout <duration>] <agent> <prompt>")
	assert.Contains(t, helpText, "exec         Run one agent once and print the run as JSONL.")
}
```

These reuse `captureStdout` (`main_test.go:204`), `writeLintConfig` and `writeLintAgent` (`lint_test.go:100` and `lint_test.go:112`). Add `"path/filepath"` and `"github.com/Rocketable/platform/internal/rocketclaw/config"` to the test imports.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./cmd/rocketclaw/ -run 'TestRunExec|TestHelpMentionsExec' -v
```

Expected: build failure — `undefined: runExec`, `undefined: runExecIn`.

- [ ] **Step 3: Add the help text and command entry point**

Add to `cmd/rocketclaw/exec.go`, and extend the import block with `"context"`, `"errors"`, `"flag"`, `"log/slog"`, `"os"`, `"strings"`, `"time"`, and `"github.com/Rocketable/platform/internal/rocketclaw/config"`:

```go
const execHelpText = `rocketclaw exec

Usage:
  rocketclaw exec [--timeout <duration>] <agent> <prompt>

Flags:
  --timeout  Maximum run duration as a Go duration, such as 10m. Zero, the default, means no timeout.

Output:
  One JSON object per line on stdout. Logs and pre-run errors go to stderr.
  Every object has a "type" field. Empty fields are omitted.

Events:
  start     Emitted once before the run. Fields: agent, session.
  thinking  Agent commentary, tool activity, and reasoning as prose. Field: text.
  message   One assistant message. Field: text.
  result    Emitted once on success, always last. Fields: ok, text, final, attachments.
  error     Emitted once on failure, always last, and replaces result. Field: message.

  Tool activity is reported as thinking prose. There are no structured tool
  call or tool result events.

  result.text is everything the agent said. result.final is the human-facing
  answer, omitted when the agent chose to say nothing. result.attachments lists
  outbound attachment filenames; the files themselves are not written.

Exit codes:
  0  The run completed and a result event was written.
  1  Anything else, including an error event, a timeout, or an interrupt.

Sessions:
  Each run is persisted under a fresh conversation ID reported by the start
  event, and can be replayed later with rocketclaw fc observe <conversation-id>.
  Runs work while the rocketclaw daemon is running.

Example:
  rocketclaw exec triage "summarize today's failures"
`

func runExec(args []string) error {
	if len(args) == 0 {
		return printStdout(execHelpText, "exec help")
	}

	switch args[0] {
	case "help", "-h", "--help":
		return printStdout(execHelpText, "exec help")
	}

	return runExecIn(context.Background(), args, os.Stdout)
}

func runExecIn(ctx context.Context, args []string, out io.Writer) error {
	flagSet := flag.NewFlagSet("rocketclaw exec", flag.ContinueOnError)
	timeout := flagSet.Duration("timeout", 0, "maximum run duration; zero means no timeout")

	if err := flagSet.Parse(args); err != nil {
		return fmt.Errorf("parse exec flags: %w", err)
	}

	remaining := flagSet.Args()
	if len(remaining) != 2 {
		return errors.New("exec requires exactly one agent and one prompt")
	}

	agent := strings.TrimSpace(remaining[0])
	if agent == "" {
		return errors.New("exec agent is required")
	}

	prompt := strings.TrimSpace(remaining[1])
	if prompt == "" {
		return errors.New("exec prompt is required")
	}

	if *timeout < 0 {
		return errors.New("exec timeout must be non-negative")
	}

	_, cfg, err := loadRuntimeConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	agents, _, err := harnessbridge.LoadRuntimeDefinitions(cfg, cfg.RuntimeDirName())
	if err != nil {
		return fmt.Errorf("load runtime agents: %w", err)
	}

	if _, ok := agents.Items[agent]; !ok {
		return fmt.Errorf("unknown agent %q", agent)
	}

	return executeExecRun(ctx, cfg, agent, prompt, *timeout, newLogger(cfg.Logging.Level), out, harnessbridge.RunRawWithProgress)
}
```

Also declare the injected runner type, whose signature matches `harnessbridge.RunRawWithProgress` exactly:

```go
// execRunner runs one raw rocketcode turn.
type execRunner func(ctx context.Context, cfg *config.Config, agent, prompt string, logger *slog.Logger, progress *harnessbridge.RawRunProgress) (harnessbridge.RawRunResult, error)
```

`executeExecRun` is added in Step 7. Do not write a placeholder or stub version of it; the package will not build until Step 7, which is expected.

- [ ] **Step 4: Wire the dispatch and the top-level help**

In `cmd/rocketclaw/main.go`, add a case to the switch in `run` immediately after `case "run":`:

```go
		case "exec":
			return runExec(args[1:])
```

In the same file, edit the `helpText` const. It is a single-line Go string with literal `\n` escapes, not a raw string. Add `  rocketclaw exec [--timeout <duration>] <agent> <prompt>\n` to the `Usage:` block right after the `rocketclaw run` line, and add `  exec         Run one agent once and print the run as JSONL.\n` to the `Commands:` block right after the `run` line. Keep the two-space indent and the existing column alignment of the descriptions.

- [ ] **Step 5: Write the failing run-wiring tests**

Append to `cmd/rocketclaw/exec_test.go`:

```go
func TestExecuteExecRunStreamsEventsInOrder(t *testing.T) {
	workspace := t.TempDir()

	var out bytes.Buffer

	session := ""
	run := func(ctx context.Context, _ *config.Config, agent, prompt string, _ *slog.Logger, progress *harnessbridge.RawRunProgress) (harnessbridge.RawRunResult, error) {
		session = progress.ConversationID

		assert.Equal(t, "triage", agent)
		assert.Equal(t, "check logs", prompt)
		require.NoError(t, progress.Thinking(ctx, "reading logs"))
		require.NoError(t, progress.Message(ctx, "found it"))

		return harnessbridge.RawRunResult{Text: "found it", VerbatimMessage: "found it"}, nil
	}

	cfg := &config.Config{Workspace: workspace}
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

	run := func(ctx context.Context, _ *config.Config, _, _ string, _ *slog.Logger, progress *harnessbridge.RawRunProgress) (harnessbridge.RawRunResult, error) {
		_, err := progress.SessionService.AppendEntryID(ctx, progress.ConversationID, fcTestEntry("check logs", "found it"))

		return harnessbridge.RawRunResult{Text: "found it"}, err
	}

	cfg := &config.Config{Workspace: workspace}
	require.NoError(t, executeExecRun(t.Context(), cfg, "triage", "check logs", 0, slog.New(slog.DiscardHandler), &out, run))

	summaries, err := harnessbridge.ListSessionsInOptions(t.Context(), workspace, config.DefaultRuntimeDir, harnessbridge.SessionListOptions{})
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.True(t, strings.HasPrefix(summaries[0].ConversationID, "exec-"))
}

func TestExecuteExecRunEmitsSingleErrorLineOnFailure(t *testing.T) {
	workspace := t.TempDir()

	var out bytes.Buffer

	run := func(context.Context, *config.Config, string, string, *slog.Logger, *harnessbridge.RawRunProgress) (harnessbridge.RawRunResult, error) {
		return harnessbridge.RawRunResult{}, errors.New("model unavailable")
	}

	cfg := &config.Config{Workspace: workspace}
	err := executeExecRun(t.Context(), cfg, "triage", "check logs", 0, slog.New(slog.DiscardHandler), &out, run)

	var coded exitCodeError

	require.ErrorAs(t, err, &coded)
	assert.Equal(t, 1, coded.ExitCode())

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	require.Len(t, lines, 2)
	assert.Equal(t, "{\"type\":\"error\",\"message\":\"model unavailable\"}", lines[1])
}

func TestExecuteExecRunAppliesTimeout(t *testing.T) {
	workspace := t.TempDir()

	var out bytes.Buffer

	run := func(ctx context.Context, _ *config.Config, _, _ string, _ *slog.Logger, _ *harnessbridge.RawRunProgress) (harnessbridge.RawRunResult, error) {
		<-ctx.Done()

		return harnessbridge.RawRunResult{}, fmt.Errorf("run cancelled: %w", ctx.Err())
	}

	cfg := &config.Config{Workspace: workspace}
	err := executeExecRun(t.Context(), cfg, "triage", "check logs", time.Millisecond, slog.New(slog.DiscardHandler), &out, run)

	require.Error(t, err)
	assert.Contains(t, out.String(), "context deadline exceeded")
}
```

`fcTestEntry` already exists in `fc_test.go`. Add `"fmt"` and `"log/slog"` to the test imports if not already present.

- [ ] **Step 6: Run the tests to verify they fail**

```bash
go test ./cmd/rocketclaw/ -run TestExecuteExecRun -v
```

Expected: build failure — `undefined: executeExecRun`.

- [ ] **Step 7: Write the run implementation**

In `cmd/rocketclaw/exec.go`, add the following below the `execRunner` type declaration. Add `"math/rand/v2"`, `"os/signal"`, and `"syscall"` to the imports.

```go
func executeExecRun(ctx context.Context, cfg *config.Config, agent, prompt string, timeout time.Duration, logger *slog.Logger, out io.Writer, run execRunner) error {
	sessions, err := harnessbridge.NewSessionServiceIn(cfg.Workspace, cfg.RuntimeDirName(), logger)
	if err != nil {
		return fmt.Errorf("start rocketcode session service: %w", err)
	}

	defer func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()

		if err := sessions.Stop(stopCtx); err != nil {
			logger.Warn("stop rocketcode session service", "error", err)
		}
	}()

	conversationID := "exec-" + rand.Text()
	writer := execEventWriter{out: out}

	if err := writer.write(execEvent{Type: "start", Agent: agent, Session: conversationID}); err != nil {
		return err
	}

	runCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	if timeout > 0 {
		timedCtx, cancel := context.WithTimeout(runCtx, timeout)
		defer cancel()

		runCtx = timedCtx
	}

	progress := &harnessbridge.RawRunProgress{
		SessionService:         sessions,
		ConversationID:         conversationID,
		Thinking:               func(_ context.Context, text string) error { return writer.write(execEvent{Type: "thinking", Text: text}) },
		Message:                func(_ context.Context, text string) error { return writer.write(execEvent{Type: "message", Text: text}) },
		ScheduleMessage:        func(time.Duration, string, bool) error { return nil },
		ResetScheduledMessages: func() error { return nil },
		RequestRestart:         func(context.Context, string) (string, error) { return "", errors.New("restart is unavailable in rocketclaw exec") },
		RequestReload:          func(context.Context, string) (string, error) { return "", errors.New("reload is unavailable in rocketclaw exec") },
	}

	result, errRun := run(runCtx, cfg, agent, prompt, logger, progress)

	return writeExecOutcome(writer, result, errRun)
}
```

`ScheduleMessage` and `ResetScheduledMessages` are inert because the raw run path never invokes them; they exist so no field is nil. `RequestRestart` and `RequestReload` return errors so an agent that reaches for them learns they are unavailable in a one-shot run rather than believing they succeeded.

All events are written from a single goroutine: `start` before the run, the callbacks from the run's own output drain, and the outcome after it returns. No mutex is needed.

Note the deliberate omission: `exec` does not call `harnessbridge.AcquireStateStoreLock`, unlike `fc delete` and `fc check`. That is by design, so `exec` works while the daemon is running. It is safe because the store is opened with `PRAGMA journal_mode = WAL` and `PRAGMA busy_timeout = 30000`, and because `exec` only appends a brand-new conversation that no other process holds. Do not add the lock.

- [ ] **Step 8: Run the exec tests to verify they pass**

```bash
go test ./cmd/rocketclaw/ -run 'TestRunExec|TestHelpMentionsExec|TestExecuteExecRun' -v
```

Expected: PASS for every exec test function and subtest.

- [ ] **Step 9: Run the whole package**

```bash
go test ./cmd/rocketclaw/ -count 1
```

Expected: `ok` with no failures.

- [ ] **Step 10: Update the cheat sheet**

In `cmd/rocketclaw/CHEATSHEET.md`, find the subcommand table near line 197 and add a row for `exec` in dispatch order, immediately after the `run` row, describing it as "Run one agent once, non-interactively, and print the run as JSONL." Match the existing column formatting exactly.

- [ ] **Step 11: Format and commit**

```bash
gofmt -w cmd/rocketclaw/exec.go cmd/rocketclaw/exec_test.go cmd/rocketclaw/main.go
jj commit cmd/rocketclaw/exec.go cmd/rocketclaw/exec_test.go cmd/rocketclaw/main.go cmd/rocketclaw/CHEATSHEET.md -m "internal/rocketclaw: run agents one-shot with rocketclaw exec"
```

---

## Task 3: Verification

Run the repository gates and confirm the command behaves end to end.

**Files:** none changed unless a gate fails.

- [ ] **Step 1: Run the full test suite**

```bash
go test ./... 2>&1 | grep -v '^ok' | head -20
```

Expected: no output, meaning every package passed.

- [ ] **Step 2: Run the linter**

```bash
cd internal/rocketclaw && make lint
```

Expected: clean exit. If `wsl_v5` complains about blank lines, fix the spacing rather than suppressing the rule. If `dupl` flags the two one-line progress callbacks, extract a small helper instead of adding `//nolint`.

- [ ] **Step 3: Run the coverage and CLOC gates**

```bash
cd internal/rocketclaw && make test
```

Expected: `coverage budget ok:` and no CLOC failure. The first run builds a baseline workspace under `.tmp/` and is slow.

If coverage dropped, the uncovered lines are almost certainly inside `executeExecRun`'s early failure branches. Add a test that makes `NewSessionServiceIn` fail by pointing `cfg.Workspace` at a path that is a regular file rather than a directory, and a test that makes the writer fail by passing an `io.Writer` that always returns an error.

- [ ] **Step 4: Smoke test the help output**

```bash
go run ./cmd/rocketclaw exec --help
```

Expected: the full `execHelpText`, exit 0, and it works from a directory with no `rocketclaw.json`.

```bash
go run ./cmd/rocketclaw help | grep exec
```

Expected: both the usage line and the command description.

- [ ] **Step 5: Smoke test a real run**

In a workspace that has a valid `rocketclaw.json` and a configured agent:

```bash
go run ./cmd/rocketclaw exec main "reply with the single word ok" 2>/dev/null
```

Expected: a `start` line, zero or more `thinking` and `message` lines, and a final `result` line with `"ok":true`. Then confirm the run persisted, using the session ID from the `start` line:

```bash
go run ./cmd/rocketclaw fc observe <session-id> | head -3
```

Expected: stored session entries as JSONL.

- [ ] **Step 6: Review the complete diff**

```bash
jj diff --git --from <commit-before-task-1> --to @-
```

Confirm only `cmd/rocketclaw/exec.go`, `cmd/rocketclaw/exec_test.go`, `cmd/rocketclaw/main.go`, and `cmd/rocketclaw/CHEATSHEET.md` changed, and that nothing under `internal/` was touched.

- [ ] **Step 7: State README impact**

`AGENTS.md` requires the final response to state whether README impact was considered. It was: `README.md:39` points readers to `rocketclaw help` rather than listing subcommands, and `rocketclaw help` now lists `exec`, so no README change is needed.
