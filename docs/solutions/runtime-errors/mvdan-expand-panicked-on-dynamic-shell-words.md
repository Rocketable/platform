---
title: mvdan.cc/sh expand Panicked on Dynamic Shell Words and Took Down the Server
date: 2026-11-23
category: docs/solutions/runtime-errors/
module: internal/rocketclaw/harness
problem_type: runtime_error
component: service_layer
symptoms:
  - "panic: runtime error: invalid memory address or nil pointer dereference in mvdan.cc/sh/v3/expand.(*Config).wordField"
  - The whole RocketClaw process exits; the Web hangs, Slack and cron stop, and running turns abort mid-tool-call
  - Opening a conversation whose shell history has a process substitution and a GitHub link crashes the server again
root_cause: wrong_api
resolution_type: code_fix
severity: critical
framework_version: mvdan.cc/sh v3.14.1
tags: [mvdan-sh, shell-parser, panic, process-substitution, grpc, fuzzing, pull-requests-pane, bash-permissions]
---

# mvdan.cc/sh expand Panicked on Dynamic Shell Words and Took Down the Server

## Problem

Code that reads shell commands with `mvdan.cc/sh` turned each word into a string with `expand.Literal` or `expand.Fields` and an empty `expand.Config{}`. For some words `expand` panics instead of returning an error. The words came from commands agents had run, so ordinary commands such as `diff <(a) <(b)` killed the RocketClaw process.

## Symptoms

- The process died with a nil-pointer panic whose stack ran `expand.(*Config).wordField` (`expand.go:616`) ← `expand.Literal` ← `ghCommand` ← `PullRequestsIn` ← `(*Server).findPullRequests` ← the gRPC unary handler.
- Everything in the process stopped with it: the Web, Slack, cron, and every running turn, whose in-flight tool calls came back as aborted because the runtime stopped.
- It recurred on viewing, not on running: the Web sends each successful shell call that printed a GitHub link to `FindPullRequests` when it shows a conversation (`internal/rocketclaw/web/src/thread-pr.ts:81-96`), so reopening the same conversation crashed the server again.

## What Didn't Work

- **Reading the trace after the fact.** The process logs only to its terminal; the restart's own start-up logging scrolled the panic away before anyone looked. The trace was caught only after capturing the terminal output to a file and waiting for the next crash.
- **Suspecting the code that was busy at the time.** The first crash landed while a scheduled turn ran a parallel Code Mode `map` of `bash` calls, and the same morning had changed an HTTP response-writer wrapper and Code Mode's dictionary walk. None of these was the cause: `net/http` recovers handler panics, and the dictionary walk's race returns errors. The busy turn was a victim.
- **An inert `ProcSubst` hook alone.** Giving the config a `ProcSubst` that returns `""` fixes the nil call, but fuzzing found a second panic in seconds: `${0@A$}` parses, and `expand` then calls `panic` on purpose for the unknown `@` operator (`vendor/mvdan.cc/sh/v3/expand/param.go:313`). `expand` holds several such "cannot happen" panics (`arith.go`, `expand.go`, `param.go`), so no set of config hooks makes it safe on arbitrary words.

## Solution

Two layers, used by all three callers.

First, configure the library explicitly. `expand.Config` has four behavior fields. Three document what nil does, and that is the behavior wanted here: `Env` (no variables), `CmdSubst` (an `UnexpectedCommandError`) and `ReadDir2`/`ReadDir` (globbing off). `ProcSubst` documents nothing for nil and is called unchecked, so it gets an implementation that refuses (`internal/rocketclaw/harness/shell.go:326`):

```go
func literalWordConfig() *expand.Config {
	return &expand.Config{ProcSubst: func(*syntax.ProcSubst) (string, error) { return "", errProcessSubstitution }}
}
```

The config is built per call because `expand` writes to it while expanding. Filling the documented-nil fields with stand-ins would be wrong, not safer: a `ReadDir2` that lists nothing turns globbing on and runs more library code on outside input.

Second, restrict the input, because no config stops `expand`'s deliberate panics. Only words made entirely of literal text reach it: plain, single-quoted or double-quoted parts. Every other word is treated as unknown. The check already existed inline in the sandbox path check; it became one helper (`internal/rocketclaw/harness/shell.go:314`):

```go
func quotedLiteralWord(word *syntax.Word) bool {
	for part := range syntax.Preorder(word) {
		switch part.(type) {
		case *syntax.Word, *syntax.Lit, *syntax.SglQuoted, *syntax.DblQuoted:
		default:
			return false
		}
	}

	return true
}
```

The pull request finder reads a dynamic word as an empty field (`internal/rocketclaw/harness/public_progress.go:317`). Before:

```go
field, _ := expand.Literal(&expand.Config{}, word)
```

After:

```go
field := ""
if quotedLiteralWord(word) {
	field, _ = expand.Literal(literalWordConfig(), word)
}
```

The bash permission matcher treats a dynamic first word as no match (`internal/rocketclaw/harness/permission.go:745`), and `deniedBashPath` keeps skipping dynamic words (`internal/rocketclaw/harness/shell.go:346`).

Regression cases: `diff <(gh pr view 7) <(gh pr view 8)` and `echo ${0@A$}` in `internal/rocketclaw/harness/testdata/pr-discovery-cases.json`, and `<(ls) a/b` against a `*/*` bash rule in `internal/rocketclaw/harness/permission_test.go:279`. Each panicked before the fix. `TestLiteralWordConfig` (`internal/rocketclaw/harness/shell_test.go:344`) passes both substitution kinds straight to the config, without the input check, and expects errors instead of panics.

## Why This Works

`expand` evaluates a word. Evaluating parameter, command, arithmetic and process substitution parts needs hooks and an environment, and v3.14.1 checks some missing hooks (`if cfg.CmdSubst == nil`, `expand.go:634`) but calls others directly (`cfg.ProcSubst(wp)`, `expand.go:616`). It also panics on parser-accepted input it does not handle. The explicit `ProcSubst` removes the nil call even if a dynamic word slips past the input check. A word built only from literal and quoted text needs none of the hooks, so `expand` reduces to quote removal and `$'…'` escapes and cannot reach the deliberate panics either. Walking the syntax tree with `syntax.Walk` stays safe; only expansion was the hazard.

Words skipped this way had been expanded against an empty environment, so their values were already meaningless. One edge did change: `env A=$B gh pr create` is no longer seen as a create, because the `A=$B` word now reads as empty instead of `A=`. A plain `A=$B gh pr create` is an assignment, not an argument, and is unaffected.

The crash took the whole process because the gRPC server is built with `grpc.NewServer()` and no interceptors (`cmd/rocketclaw/web.go:32`), and grpc-go does not recover handler panics. A panic in any request handler, or in any goroutine without its own recover such as Code Mode's parallel branches, ends the process.

## Prevention

- **Lint rule.** depguard's `shell-expand` rule in `internal/rocketclaw/.golangci.yml:70` denies `mvdan.cc/sh/v3/expand` everywhere except tests and the three files above, and its message points at `quotedLiteralWord`. A rule like it that allowed only the files importing `expand` at the time, `permission.go` and `shell.go`, would have flagged the change that added the crash, the first to import `expand` in `public_progress.go`. It cannot see a new call inside the three allowed files; review those calls for the check.
- **Treat shell text as untrusted.** Commands come from models and, through `FindPullRequests`, from any signed-in browser. Code that feeds them to a third-party parser or evaluator must not panic on any input.
- **Fuzz parser-facing code before shipping.** A throwaway fuzz test, `f.Fuzz(func(_ *testing.T, command string) { PullRequestsIn(command, "https://github.com/o/r/pull/1") })`, found both panics within seconds; hand-picked cases and a replay of real history did not contain either shape. Keep what it finds as regression cases.
- **Know that one handler panic is a full outage.** Until the server has a panic boundary for gRPC handlers, a panic in any request takes down Slack, cron and every turn with it.

## Related Issues

- `docs/solutions/integration-issues/pr-pane-missed-prs-in-jj-workspaces-and-subagent-sessions.md` — the change that moved pull request detection onto `mvdan.cc/sh` on the server (PR #1768).
- `docs/solutions/architecture-patterns/code-mode-scripts-format-on-starlark-go-parser-not-buildifier.md` — fuzzing code built on another third-party parser.
