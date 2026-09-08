package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/backend"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
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

func writeExecOutcome(writer execEventWriter, result backend.RawRunResult, errRun error) error {
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

const execHelpText = `rocketclaw exec

Usage:
  rocketclaw exec [--timeout <duration>] <agent> <prompt>

Flags:
  --timeout  Maximum run duration as a Go duration, such as 10m. Zero, the default,
             means no timeout, and because a run retries until the agent calls its
             mandatory decision tool, automated callers should always set one.
             The deadline bounds model and tool work. Session-store reads and
             writes are not cancellable, so a contended store can still delay
             the final event by up to its 30s busy timeout.

  help, -h, and --help print this text and exit 0, in any argument position.

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
  call or tool result protocol.

  result.text is everything the agent said in its final attempt; the full stream
  across retries is carried by the message protocol. result.final is the human-facing
  answer, omitted when the agent chose to say nothing. result.attachments lists
  outbound attachment filenames; the files themselves are not written.

Exit codes:
  0  The run completed and a result event was written.
  1  Anything else, including an error event, a timeout, or an interrupt.

Sessions:
  Each run is persisted under a fresh conversation ID reported by the start
  event, and can be replayed later with rocketclaw_development_observe_session
  when Development MCP is enabled.
  Runs work while the rocketclaw daemon is running.

Example:
  rocketclaw exec triage "summarize today's failures"
`

func runExecIn(ctx context.Context, args []string, out io.Writer, run execRunner) error {
	requestedHelp := len(args) == 0
	for _, arg := range args {
		switch arg {
		case "help", "-h", "--help":
			requestedHelp = true
		}
	}

	if requestedHelp {
		if _, err := fmt.Fprint(out, execHelpText); err != nil {
			return fmt.Errorf("print exec help: %w", err)
		}

		return nil
	}

	flagSet := flag.NewFlagSet("rocketclaw exec", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)

	timeout := flagSet.Duration("timeout", 0, "maximum run duration; zero means no timeout")
	secretsARN := flagSet.String(secretsARNFlag, "", secretsARNUsage)

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

	prompt := remaining[1]
	if strings.TrimSpace(prompt) == "" {
		return errors.New("exec prompt is required")
	}

	if *timeout < 0 {
		return errors.New("exec timeout must be non-negative")
	}

	_, cfg, err := loadRuntimeConfig(*secretsARN)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	agents, _, err := backend.LoadRuntimeDefinitions(cfg, cfg.RuntimeDirName())
	if err != nil {
		return fmt.Errorf("load runtime agents: %w", err)
	}

	if _, ok := agents.Items[agent]; !ok {
		return fmt.Errorf("unknown agent %q", agent)
	}

	return executeExecRun(ctx, cfg, agent, prompt, *timeout, newLogger(cfg.Logging.Level), out, run)
}

// execRunner runs one raw rocketcode turn.
type execRunner func(ctx context.Context, cfg *config.Config, agent, prompt string, logger *slog.Logger, progress *backend.RawRunProgress) (backend.RawRunResult, error)

func executeExecRun(ctx context.Context, cfg *config.Config, agent, prompt string, timeout time.Duration, logger *slog.Logger, out io.Writer, run execRunner) error {
	runCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	if timeout > 0 {
		timedCtx, cancel := context.WithTimeout(runCtx, timeout)
		defer cancel()

		runCtx = timedCtx
	}

	sessions, err := backend.NewSessionServiceIn(cfg.DatabaseURL, logger)
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

	if errCtx := runCtx.Err(); errCtx != nil {
		return writeExecOutcome(execEventWriter{out: out}, backend.RawRunResult{}, fmt.Errorf("exec cancelled before start: %w", errCtx))
	}

	conversationID := "exec-" + rand.Text()
	writer := execEventWriter{out: out}

	if err := writer.write(execEvent{Type: "start", Agent: agent, Session: conversationID}); err != nil {
		return err
	}

	progress := &backend.RawRunProgress{
		SessionService: sessions,
		ConversationID: conversationID,
		Thinking: func(_ context.Context, text string) error {
			return writer.write(execEvent{Type: "thinking", Text: text})
		},
		Message: func(_ context.Context, text string) error {
			return writer.write(execEvent{Type: "message", Text: text})
		},
		RequestRestart: func(context.Context, string) (string, error) {
			return "", errors.New("restart is unavailable in rocketclaw exec")
		},
		RequestReload: func(context.Context, string) (string, error) {
			return "", errors.New("reload is unavailable in rocketclaw exec")
		},
	}

	result, errRun := run(runCtx, cfg, agent, prompt, logger, progress)

	return writeExecOutcome(writer, result, errRun)
}
