---
title: A canceled transaction's Commit error is not deterministic
date: 2026-10-26
last_updated: 2026-10-26
category: docs/solutions/best-practices/
module: internal/rocketclaw/backend
problem_type: best_practice
component: database
severity: medium
applies_when:
  - A test forces a database/sql transaction to fail by canceling the context it was started with, after BeginTx and before Commit
  - A test or caller checks which error Commit returned, by text or with errors.Is
  - Proving that a suspected CI-only race is fixed with repeated local runs
symptoms:
  - "A test that cancels the context of a database/sql transaction and expects Commit to fail with \"context canceled\" passes locally and fails in CI"
  - "The CI failure reads \"sql: transaction has already been committed or rolled back\" instead"
root_cause: concurrency
resolution_type: test_fix
retire_when: Go's database/sql Tx.Commit returns one fixed error once its context is canceled. Check Tx.Commit and Tx.awaitDone in database/sql/sql.go of the Go version named in go.mod.
tags: [database-sql, transaction, context-cancel, flaky-test, err-tx-done, race-detector]
---

# A canceled transaction's Commit error is not deterministic

## Context

`Runtime.SaveBehavior` (`internal/rocketclaw/backend/runtime.go:98`) opens its transaction with `r.Sessions.db.BeginTx(r.RunCtx, nil)` (line 113) and commits last, wrapping any failure as `commit behavior settings: %w` (lines 145-146). `TestRuntimeSaveBehavior`'s "commit" case (`internal/rocketclaw/backend/runtime_reload_test.go:324`) makes that commit fail by canceling `RunCtx` from the External MCP refresh mock (lines 234-235), which runs just before `tx.Commit()`.

The test first expected `context.Canceled.Error()`. It passed locally, then failed once in CI on PR #685 (open, unmerged as of this writing), shard `rocketclaw (0)`:

```
Error "behavior settings rejected, nothing changed: commit behavior settings: sql: transaction has already been committed or rolled back" does not contain "context canceled"
```

The save was still rejected. Only the error text differed.

Reproducing it locally took more than repeating the test (session history):

- `go test ./backend -run '^TestRuntimeSaveBehavior$' -count=100` passed every run.
- `-count=150 -cpu=1,2 -race` failed once in 300 runs, with the same message as CI.

The first local check after the fix was a plain `-count=20`, reported as "passes 20 times in a row". That run proves nothing, because the unfixed test also passed 100 plain runs. The real evidence was 400 runs of `-count=200 -cpu=1,2 -race` with no failures, and then a green CI on the new head. Those runs probably never took the `ErrTxDone` path, so the claim that the state assertions hold there rests on the source reading below, not on an observed run.

## Guidance

When a test cancels a transaction's context to make `Commit` fail, assert on the caller's own wrap and on the resulting state. Do not assert on the error `database/sql` returns:

```go
// Before: flaky. Commit may return sql.ErrTxDone instead.
{"commit", ..., context.Canceled.Error(), 2},

// After: SaveBehavior wraps every commit failure the same way.
{"commit", ..., "commit behavior settings", 2},
```

The assertions after it in the same loop (`runtime_reload_test.go:333-338`) check the real contract: live behavior, the stored row, the log level, and the runtime assets are unchanged. If code really needs to recognize this failure with `errors.Is`, accept both errors:

```go
errors.Is(err, context.Canceled) || errors.Is(err, sql.ErrTxDone)
```

To check a fix for a race like this, first reproduce the failure with the unfixed code, then run the same command on the fix:

```sh
go test ./backend -run '^TestX$' -count=150 -cpu=1,2 -race
```

A plain `-count=N` run that passes is no evidence if the unfixed code also passes it.

## Why This Matters

`BeginTx` starts a goroutine for the transaction (`go tx.awaitDone()`). It waits on the transaction's context and rolls the transaction back as soon as the context is canceled. That rollback marks the transaction done first. `Tx.Commit` checks the context before anything else:

```go
case <-tx.ctx.Done():
    if tx.done.Load() {
        return ErrTxDone
    }
    return tx.ctx.Err()
```

So the error depends on whether `awaitDone` has marked the transaction done when `Commit` runs:

- not yet: `Commit` returns `context.Canceled`;
- already: `Commit` returns `sql.ErrTxDone` ("transaction has already been committed or rolled back").

Both are correct results of the same cancellation. Either way nothing is committed: `Commit` returns before calling the driver, and `awaitDone` rolls the transaction back. Goroutine scheduling decides which error you get. On a laptop it was `context.Canceled` in every plain run. Locally, `ErrTxDone` showed up only under `-race` with `-cpu=1,2`. Why CI hits it more often was not measured; slower or busier runners are the likely reason. (Source: the standard library's `$(go env GOROOT)/src/database/sql/sql.go` in Go 1.27.3, the version in `go.mod`: `Tx.awaitDone` at line 2214, `Tx.Commit` at line 2292, `go tx.awaitDone()` at line 1930.)

The final test does not show any of this. It asserts a looser string than before. A reviewer who wants the test to be "more precise" would bring back `context.Canceled` or `errors.Is(err, context.Canceled)`, and the test would pass locally and flake in CI again.

## When to Apply

- The context is canceled after `BeginTx` and before `Commit` runs, whether by the test or by shutdown. This is the window that races.
- Canceling before the operation starts is deterministic. `DB.BeginTx` and queries get their connection from `DB.conn`, which checks the context first and returns `ctx.Err()` (`sql.go:1328-1332`). Tests in `internal/rocketclaw/backend` that cancel first and then call the store (for example `store_test.go:64-68` and `origin_pairs_test.go:148-158`) are not affected.
- Production code that classifies a `Commit` error as cancellation must also accept `sql.ErrTxDone`. No production code here does this today. The only non-test `errors.Is(..., context.Canceled)` in `internal` or `cmd` (`internal/rocketclaw/frontend/rpc/voice.go:330`) checks a context's cause, not a database error.

## Examples

PR #685 changed the test's expected string from `context.Canceled.Error()` to `"commit behavior settings"` and kept the state assertions. CI then passed all 15 checks on the PR's updated head.

## Related

- `docs/solutions/logic-errors/plain-cancel-of-a-child-context-can-erase-the-shutdown-cause.md`: another CI-only flake where which cancellation error surfaced depended on ordering.
- `docs/solutions/best-practices/read-count-tests-need-autovacuum-off.md`: another test that passed every local run and failed in CI, with a recipe for reproducing CI-like conditions locally.
