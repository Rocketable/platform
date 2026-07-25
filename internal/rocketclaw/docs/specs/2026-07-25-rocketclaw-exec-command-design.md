# RocketClaw Exec Command

## Goal

Give RocketClaw a one-shot, non-interactive command that runs a named agent against a single prompt and reports the run as JSONL on stdout, so automated callers and other agents can drive RocketClaw without Slack, MCP, or cron.

## Command Surface

```text
rocketclaw exec [--timeout <duration>] <agent> <prompt>
```

`exec` is a new case in the `cmd/rocketclaw/main.go` dispatch switch, implemented in a new `cmd/rocketclaw/exec.go`. The existing `run` subcommand keeps starting the daemon and is untouched.

Both positional arguments are required. The prompt is passed verbatim, including any leading or trailing whitespace; it is trimmed only to test whether it is blank. The agent name is trimmed, because it is a lookup key. The prompt is never read from stdin, and `@attach:` attachment syntax is not supported. `--timeout` accepts a Go duration and defaults to zero, meaning no timeout. Argument parsing uses `flag.NewFlagSet("rocketclaw exec", flag.ContinueOnError)` followed by positional validation, matching `fc observe`. The flag set writes to `io.Discard` so a malformed flag is reported once, by the caller's error path, rather than twice.

`exec` with no arguments, or with `help`, `-h`, or `--help` in any argument position, prints `execHelpText` to stdout and exits zero. Scanning every position matters because the command is written to be driven by agents: `exec --timeout 1m --help` and `exec <agent> --help` must both produce help rather than a flag-package usage error or a run whose prompt is the literal string `--help`. Help printing happens before flag parsing and before any configuration load, so help works in a directory without a configuration file.

The command requires `rocketclaw.json` or the legacy `femtoclaw.json`, resolved through the existing `loadRuntimeConfig`. The agent name must exist in the runtime agent set loaded by `harnessbridge.LoadRuntimeDefinitions(cfg, cfg.RuntimeDirName())`; an unknown name fails before any model request. An empty agent name is rejected rather than defaulting to `main`, because a CLI caller that omits the agent has made a mistake.

## Execution

`exec` calls the existing `harnessbridge.RunRawWithProgress` unchanged. That function already loads runtime-directory agents and skills, applies overlay prompts, builds providers from the configured OpenAI credentials or `rocketclaw oai login` session, creates and removes a per-run shell output directory, and persists session entries. Two of its behaviors are inherited deliberately rather than worked around: the prompt carries the raw run's `Cron` provenance header, and the run is not complete until the agent calls the mandatory decision tool. Changing either would mean modifying a path that cron depends on, which this change does not do.

`exec` supplies a fully populated `harnessbridge.RawRunProgress`. No field is left nil.

| Field | Behavior in `exec` |
| --- | --- |
| `SessionService` | Opened with `harnessbridge.NewSessionServiceIn(cfg.Workspace, cfg.RuntimeDirName(), logger)` and closed when the run ends. |
| `ConversationID` | Freshly minted, `exec-` followed by `rand.Text()`. |
| `Thinking` | Writes one `thinking` event. |
| `Message` | Writes one `message` event. |
| `ScheduleMessage`, `ResetScheduledMessages` | Inert; the raw run path never invokes them. |
| `RequestRestart`, `RequestReload` | Report that daemon restart and asset reload are unavailable in a one-shot run. |

## Session Persistence

Runs persist. Session entries are written under the minted conversation ID into the same `state.sqlite3` the daemon uses, so `rocketclaw fc list` and `rocketclaw fc observe <id>` see an `exec` run like any other conversation. Sessions are derived from the `session_entries` rows, so no separate registration step is needed.

`exec` does not acquire the state-store lock and therefore runs while the daemon is running. This is safe because the store is opened with `PRAGMA journal_mode = WAL` and `PRAGMA busy_timeout = 30000`, and because `exec` only appends a new conversation that no other process holds. The invariant the lock protects is narrower than "all writes": mutating or deleting conversations the daemon may hold in memory requires the lock, as `fc delete` and `fc check` do, while appending a fresh conversation does not.

## Output

Stdout carries JSONL and nothing else: one JSON object per line, written through an injected `io.Writer`. Diagnostics, logs, and fatal error text go to stderr. Every object has a `type`; empty fields are omitted.

| Type | Fields | When |
| --- | --- | --- |
| `start` | `agent`, `session` | Once, before the run begins. |
| `thinking` | `text` | Each commentary, tool, or reasoning update, as the prose the raw run already produces. |
| `message` | `text` | Each assistant message. |
| `result` | `ok`, `text`, `final`, `attachments` | Once, on success. |
| `error` | `message` | Once, on failure. Replaces `result`. |

`result.text` is everything the agent said in its final attempt; because the raw run retries until the agent calls its mandatory decision tool, earlier attempts survive only as streamed `message` events. `result.final` is the human-facing answer the agent passed to the mandatory decision tool, and is omitted when the agent chose to say nothing. `result.attachments` lists the filenames of outbound attachments the agent produced, and is omitted when there are none; `exec` reports the names only and does not write the files.

Because the events are derived from the raw run's prose callbacks, there are no structured `tool_call` or `tool_result` events. Tool activity appears inside `thinking` text.

Exactly one `result` or one `error` is emitted per invocation, always as the last line.

## Failure, Timeout, And Interrupts

An expired `--timeout`, SIGINT, or SIGTERM cancels the run context, and the command emits a single `error` line describing the cause before exiting. Lines are written whole, so a cancelled run never leaves truncated JSON on stdout.

The signal handler and the timeout are installed before the session store is opened, so the deadline covers store startup and an interrupt during startup cannot kill the process silently. If the context is already done by the time the store is ready, `exec` emits one `error` line and never emits `start`, so stdout never carries a `start` without a terminating event.

The deadline bounds model and tool work, not session-store I/O. `SessionService` reads and writes use `context.Background()` throughout `harnessbridge`, which the daemon, cron, and MCP all share, so making them cancellable would mean reworking that package rather than this command. A contended store can therefore delay the final event by up to its 30-second busy timeout past the deadline. `execHelpText` states this so an automated caller is not misled.

Failures before the run starts — missing configuration, unknown agent, malformed flags, wrong argument count — are reported on stderr with nothing written to stdout.

Exit codes are `0` on `result` and `1` on every failure, using the existing error return path in `main`.

## Help Text

`execHelpText` is a `const` in the existing `Usage:` and `Commands:` house style, and is detailed enough that an agent reading it can drive the command correctly on the first attempt. It documents the invocation form, `--timeout`, the requirement that stdout is JSONL and logs are on stderr, every event type with its fields and emission rule, the exit codes, and one worked example. It states that tool activity is reported as `thinking` prose rather than structured events, and that runs are persisted and inspectable with `rocketclaw fc observe`.

## Testing

Test at the boundary the CLI owns, keeping the run itself out of scope since `RunRawWithProgress` is already covered:

- a table-driven test over argument and flag validation: no arguments prints help, help aliases print help without a configuration file, missing prompt, extra positionals, unparseable `--timeout`, missing configuration, and unknown agent;
- event encoding asserted as exact JSONL against a `bytes.Buffer` through the injected writer, covering `start`, `thinking`, `message`, `result` with and without `final` and `attachments`, and `error`;
- a test that a cancelled run emits exactly one terminating `error` line and no partial line;
- a test that `exec` appears in the top-level help text, matching `TestHelpMentionsLint`.

Coverage must not decrease against the baseline until it reaches the 90% stable threshold, and the change must stay inside the RocketClaw CLOC budget, which is why `exec.go` stays a thin adapter over `harnessbridge` and adds no run logic of its own.

## Documentation

Add `exec` to the `Usage:` and `Commands:` blocks of `helpText` in `cmd/rocketclaw/main.go`, and to the subcommand table in `cmd/rocketclaw/CHEATSHEET.md`. The repository README stays high-level and refers readers to `rocketclaw help`, so it does not need an update.
