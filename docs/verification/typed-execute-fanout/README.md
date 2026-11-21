# Fan-out in `execute` scripts: bounded verification

`FanOut.tla` models the fan-out builtins of `execute` scripts: `gather`/`map`, `try_gather`/`try_map` and
`race`/`race_first`, together with the tool calls their branches make, the guardian review of those calls, the
concurrency limits and cancellation (by the turn, by `try_gather` after a failure, by `race` after a win).

It models the Go code of the commit that adds it (typed results in execute scripts), with the fixes before it in the
pull request. Run against the first version (`5fbe215d`), it found two places where the code disagreed with the approved
behaviour and one wording question; all three are now settled, see
[Mismatches](#mismatches-found-in-the-first-version-and-their-fixes). The design (all switches `TRUE`) and the code
(`code-*` checks) are now the same shape; the switches turned off one at a time show what each fix or safeguard
prevents.

**Passing checks do not prove that code correct.** They show the properties hold for this small finite model.

## The behaviour required

Approved (RESULT-MODEL.md rule 7, owner decision Q2, README "Code Mode"):

- `gather`/`map`: every branch runs to the end; each slot gets its Result; a crashing branch (`.unwrap()` on a
  failure, `fail()`, a Starlark error, the branch's step budget) becomes a Failure in its slot; the script goes on.
- `try_gather`/`try_map`: the first Failure, a crash included, cancels the rest and is returned; otherwise the list.
  "First" is first in time: the first Failure a branch stores, not the one earliest in the list (owner decision).
- `race`: the first Success wins and cancels the rest; failures and crashes are losses; if every branch loses, a
  Failure. `race_first`: whatever finishes first, a crash as a Failure (G1).
- A cancelled turn stops everything.

Required by the task, and part of the design here:

- A tool call stopped by cancellation is recorded as **cancelled**: not denied by the guardian, not failed.
- A branch whose context is cancelled starts no new tool call.

## The model

| Process | Fairness | Go |
|---|---|---|
| `Main` | weak | `runGather` / `runRace` (`internal/rocketclaw/codemode/fanout.go`): the `group.Go` loop, `group.Wait`, choosing the return value |
| `Branch[b]`, b ∈ 1..`N` | weak | one `group.Go` goroutine: `slots.Acquire`, `callBranch`, storing the result (`results[i]` or `finish`), `slots.Release`, errgroup's first-error record |
| `Child` | weak | with `Nested`, branch 1's body is itself a `gather` of one branch: `releaseParentSlot`, the inner goroutine, the slot taken back |
| `Turn` | none | the turn's context being cancelled, at any time (`Cancelable`) |
| procedure `ToolCall` | its caller's | one host call: `newKwargsValueBuiltin` (`codemode/codemode.go`) → `recordHostCall` (`harness/mcp_tools.go`) → `toolHostCall.run` → `CheckNestedToolCall` / `reviewNestedCall` (`harness/permission_gate.go`) → `reviewPermission` (`harness/permission_review.go`); the record is `CodeModeCall.finish` + `recordCodeModeCall` (`harness/public_progress.go`) |

A branch makes one tool call and then ends with a success, a failure or a crash, chosen freely. What the tool call
returned does not decide the branch's result: a script can return a failed call's Result or ignore it. Tool calls
share no lock: each runs on its own and ends when its context is cancelled. That holds for `bash` too, which kills the
command's process group (or the emulated shell's cgroup); bash calls no longer wait for each other.

| Constant | Meaning | Values checked |
|---|---|---|
| `N` | branches | 3 (2 with `Nested`) |
| `Limit` | the call's `concurrency=` (errgroup `SetLimit`; default 16, max 64) | 2 |
| `Slots` | the slot pool shared by every fan-out of one script (`semaphore.NewWeighted(MaxConcurrency)`, 64, made per `codemode.Run`) | 2; 1 with `Nested`, so nested fan-outs compete for it |
| `Op` | `"gather"` (also `map`), `"try"` (`try_gather`/`try_map`), `"race"`, `"race_first"` | per check |
| `Nested` | branch 1 runs an inner `gather` of one branch | per check |
| `Cancelable` | the turn may be cancelled | per check |
| `Review` | every tool call goes to the guardian before it runs | `TRUE` except `code-cancel-error` and `cancel-recorded-error` |
| `HangFrom` | tool calls (and their reviews) of branches `>= HangFrom` never finish on their own, only by cancellation; `N + 2` means none hang | per check |

Design switches, all `TRUE` in the design and in the Go code (`code-*` checks); each broken check turns off one:

| Switch | `FALSE` means | Go today |
|---|---|---|
| `RecordCancelled` | a call cancelled during review is recorded `denied` with a guardian `deny`; one cancelled while running is recorded `error` (the code before its fix) | `TRUE` |
| `CheckBeforeCall` | a branch whose context is already cancelled may still start a tool call (the code before its fix) | `TRUE` |
| `FirstInTime` | `try` returns the stored Failure with the lowest index, not the first one stored (the code before the owner's decision) | `TRUE` |
| `CancelSiblings` | `try_gather`'s first failure does not cancel the rest | `TRUE` |
| `CrashIsFailure` | a crashing branch makes `gather` return an error (the script stops) | `TRUE` |
| `RaceWaitsForSuccess` | `race` takes the first branch to finish, failure or not | `TRUE` |
| `ReleaseWhileNested` | a branch keeps its slot while its own nested fan-out waits for slots | `TRUE` |

| Model step | Go |
|---|---|
| `MSpawn`/`MGo` | `for i, value := range values { group.Go(...) }`; `group.Go` blocks while `Limit` goroutines run; `runRace` first checks `raceCtx.Err()` and stops starting branches, `runGather` does not check |
| `MWait` | `group.Wait()`; then `gather`: the list, or an error if any goroutine returned a Go error; `try`: the failure the first failing branch stored (`firstFailure.Do`) when the first error is `errBranchFailed`; `race`: `winner, errWin` |
| `BAcquire` | `slots.Acquire(groupCtx, 1)`; x/sync v0.25 fails at once if the context is done; on failure `runGather` returns an error, `runRace` returns `nil` |
| `BPreCheck` | `callBranch`'s first `context.Cause(ctx)` check, then the thread starts |
| `ToolCheck` | the branch reaches a tool call. `thread.Cancel` (run by `context.AfterFunc`, in its own goroutine) only takes effect at the next function entry or loop back-edge (`starlark/interp.go` `checkSteps`), so `newKwargsValueBuiltin` checks `context.Cause(ctx)` before `CallValue` and returns its error without calling or recording the tool. Otherwise `recordHostCall` records the call `running` |
| `ReviewEnd` | the guardian answers allow or deny, or the review sees the cancelled context: `reviewNestedCall` then returns the context's error without storing the review's decision, and the call is recorded `cancelled` (`recordHostCall`'s `runCtx.Err() != nil` branch; for MCP calls, `execute`'s permission callback) |
| `Running` | the tool returns ok or an error, or returns because its context was cancelled; `recordHostCall` records `cancelled` for the last |
| `BOutcome` | `callBranch`'s second `context.Cause(ctx)` check (a cancelled branch returns a Go error, never a value); a Starlark error becomes `NewError(op, ...)`, a Failure |
| `BCommit` | `results[i] = result` and, for a Failure in `try`, `firstFailure.Do(...)` (`gather`/`try`); `finish(value, err)` under the mutex (`race`) |
| `BRelease` | the deferred `slots.Release(1)` |
| `BDone` | errgroup's goroutine wrapper: the first non-nil error is kept and cancels `groupCtx`; then `done()` frees the `Limit` place. `try` returns `errBranchFailed` for a failed result |
| `BPreCheck`/`BReacquire` with `Nested` | `releaseParentSlot`: `Release(1)` before the inner fan-out, `Acquire(context.Background(), 1)` after it |

Turn cancellation, `try`'s first error and `race`'s win all cancel the same context tree (`groupCtx` is derived from
the turn's context, and from `raceCtx` in `race`), so the model has one `Cancelled` flag for all of them.

## Properties

Safety (invariants):

- `TypeOK`.
- `SlotCap`: at most `Slots` branches hold a slot, nested fan-outs included.
- `GroupLimit`: at most `Limit` goroutines of the call run at once.
- `OneResultPerSlot`: no slot is written twice, and a returned list has a Result in every slot.
- `GatherCompletes`: unless the turn is cancelled, `gather` returns its list (a crash never stops the script).
- `StopsOnlyOnCancel`: the fan-out stops the script only when the turn is cancelled.
- `TryReturnsFirstFailure`: `try` returns a Failure whenever some branch's failure was stored, and then the first one
  stored; it returns the list only if every branch succeeded.
- `NoBranchStartsAfterDecision`: no branch gets past `callBranch`'s first check after `try` has decided (its first
  error is recorded) or `race` has a winner.
- `NoCallStartsAfterCancel`: no tool call starts while its branch's context is cancelled.
- `CancelledCallsRecordedCancelled`: a call ended by cancellation is recorded `cancelled`, and its review is never
  recorded as a guardian `deny`.
- `DeniedOnlyByGuardian`: a call recorded `denied` was denied by the guardian.
- `AllStoppedWhenDone`: when the fan-out returns, no branch, tool call or review is still running.
- `RaceNoFailureWhileSuccess`: `race` never returns a Failure when some branch succeeded, and (unless the turn is
  cancelled) returns a Failure only when every branch lost.
- `RaceFirstReturnsFirstFinished`: `race_first` returns the first branch to finish.

Liveness (weak fairness on everything but the turn):

- `Terminates`: the fan-out returns.
- `TurnCancelStops` (with hanging calls): once the turn is cancelled, the fan-out returns, so every branch, call
  and review has stopped (`AllStoppedWhenDone`).
- `DecisionStops` (with hanging calls): once `try` has decided or `race` has a winner, the fan-out returns.

Witnesses, each of which must fail: `NeverCancelledInReview` (a sibling's call is cancelled during its review, as in
the live run), `NeverCancelledWhileRunning`, `NeverFirstAboveLowest` (`try` returns a failure that is not the
lowest-index one stored, so first in time and lowest index really differ), `NeverGatherCrashInList`,
`NeverRaceLossThenWin` (the first branch to finish loses and `race` still returns a later success),
`NeverRaceAllLose`, `NeverSlotHandOff` (a branch waits to take its slot back while nested branches hold them all) and
`witness-hang` (hanging calls really hang without cancellation).

## Run

From the repository root:

```sh
make -C docs/verification/typed-execute-fanout          # translate, then every check
make -C docs/verification/typed-execute-fanout try      # one check
```

Requires Java 11+, `curl` and Make. The Makefile downloads `tla2tools.jar` v1.8.0 and verifies SHA-256
`fdffef247f563ef6edbfd44958d2a82db79e7564e07cb673262d896353fa6713`, as the other models here do. The tools, the
translator's copy, Java's temporary files, TLC's working files and each check's log go to the repository root's
`.tmp/verification/typed-execute-fanout/`. A passing check needs TLC exit 0 and `Model checking completed. No error has
been found.`; a check that must fail needs TLC exit 12 (safety) or 13 (liveness) and its named violation.

## Results

Recorded 2026-11-22 on Linux (aarch64, 16 cores), OpenJDK 21.0.13.1, TLC `2026.11.19.180144 (rev: bc1b506)`, `pcal.trans`
1.12, one TLC worker, against the code with the three fixes. `make -k` ran every check in 6 minutes 44 seconds and
exited 0; every check matched its expected outcome. "all" is every invariant except the witnesses. The `code-*` checks
now use the same switches as the design, so `code-gather` explores exactly the states `gather` does, and so on; they
are kept so that the Go code's shape stays checked by name.

| Check | Shape | Expected | Result | Generated / distinct states | Depth | Time |
|---|---|---|---|---:|---:|---:|
| `gather` | design, `gather` | all + `GatherCompletes`, `Terminates` | pass, complete state space | 820,222 / 317,074 | 40 | 01min 04s |
| `try` | design, `try_gather` | all + `TryReturnsFirstFailure`, `Terminates` | pass, complete state space | 353,398 / 138,874 | 40 | 31s |
| `race` | design, `race` | all + `RaceNoFailureWhileSuccess`, `Terminates` | pass, complete state space | 712,711 / 280,275 | 40 | 51s |
| `race-first` | design, `race_first` | all + `RaceFirstReturnsFirstFinished`, `Terminates` | pass, complete state space | 193,321 / 75,711 | 33 | 12s |
| `nested` | design, `try_gather`, nested, 1 slot | all + `TryReturnsFirstFailure`, `Terminates` | pass, complete state space | 34,531 / 14,923 | 35 | 04s |
| `hang-turn` | design, `gather`, every call hangs, turn cancelled | all + `GatherCompletes`, `TurnCancelStops` | pass, complete state space | 2,479 / 996 | 31 | 02s |
| `hang-try` | design, `try_gather`, branches 2–3 hang | all + `TryReturnsFirstFailure`, `DecisionStops` | pass, complete state space | 8,203 / 3,088 | 32 | 02s |
| `hang-race` | design, `race`, branches 2–3 hang | all + `RaceNoFailureWhileSuccess`, `DecisionStops` | pass, complete state space | 8,638 / 3,274 | 32 | 02s |
| `hang-nested` | design, nested, every call hangs, turn cancelled | all + `TryReturnsFirstFailure`, `TurnCancelStops` | pass, complete state space | 1,346 / 599 | 29 | 01s |
| `code-gather` | Go code, `gather` | all + `GatherCompletes`, `Terminates` | pass, complete state space | 820,222 / 317,074 | 40 | 58s |
| `code-try` | Go code, `try_gather` | all + `TryReturnsFirstFailure`, `Terminates` | pass, complete state space | 353,398 / 138,874 | 40 | 23s |
| `code-race` | Go code, `race` | all + `RaceNoFailureWhileSuccess`, `Terminates` | pass, complete state space | 712,711 / 280,275 | 40 | 49s |
| `code-race-first` | Go code, `race_first` | all + `RaceFirstReturnsFirstFinished`, `Terminates` | pass, complete state space | 193,321 / 75,711 | 33 | 11s |
| `code-nested` | Go code, nested | all + `TryReturnsFirstFailure`, `Terminates` | pass, complete state space | 34,531 / 14,923 | 35 | 03s |
| `code-hang-turn` | Go code, every call hangs, turn cancelled | all + `GatherCompletes`, `TurnCancelStops` | pass, complete state space | 2,479 / 996 | 31 | 01s |
| `code-hang-try` | Go code, `try_gather`, branches 2–3 hang | all + `TryReturnsFirstFailure`, `DecisionStops` | pass, complete state space | 8,203 / 3,088 | 32 | 01s |
| `code-hang-nested` | Go code, nested, every call hangs, turn cancelled | all + `TryReturnsFirstFailure`, `TurnCancelStops` | pass, complete state space | 1,346 / 599 | 29 | 01s |
| `code-cancel-denied` | Go code, `try_gather`, reviewed calls, no turn cancel | all + `TryReturnsFirstFailure`, `Terminates` | pass, complete state space | 248,070 / 91,354 | 40 | 15s |
| `code-cancel-error` | Go code, `try_gather`, no review, no turn cancel | all + `TryReturnsFirstFailure`, `Terminates` | pass, complete state space | 92,051 / 33,780 | 40 | 06s |
| `code-call-after-cancel` | Go code, `try_gather`, no turn cancel | all + `TryReturnsFirstFailure`, `Terminates` | pass, complete state space | 248,070 / 91,354 | 40 | 15s |
| `cancel-recorded-denied` | `RecordCancelled` off, `try_gather`, reviewed calls | `CancelledCallsRecordedCancelled` violated | violated | 7,491 / 3,047 | 19 | 01s |
| `cancel-recorded-error` | `RecordCancelled` off, `try_gather`, no review | `CancelledCallsRecordedCancelled` violated | violated | 7,470 / 2,969 | 21 | 01s |
| `call-after-cancel` | `CheckBeforeCall` off, `try_gather` | `NoCallStartsAfterCancel` violated | violated | 5,366 / 2,176 | 18 | 01s |
| `try-lowest-index` | `FirstInTime` off, `try_gather` | `TryReturnsFirstFailure` violated | violated | 100,603 / 37,712 | 30 | 01s |
| `try-no-cancel` | `try_gather` without the cancel | `NoBranchStartsAfterDecision` violated | violated | 3,965 / 1,586 | 17 | 01s |
| `try-no-cancel-hang` | `try_gather` without the cancel, branches 2–3 hang | `DecisionStops` violated | violated | 5,839 / 2,176 | 27 | 01s |
| `gather-crash-stops` | a crash stops `gather` | `GatherCompletes` violated | violated | 28,646 / 10,980 | 24 | 01s |
| `race-any` | `race` takes the first to finish | `RaceNoFailureWhileSuccess` violated | violated | 1,651 / 781 | 14 | 01s |
| `nested-hold-slot` | nested branch keeps its slot | `Terminates` violated | violated | 2,139 / 936 | 30 | 01s |
| `witness-cancel-in-review` | design, `try_gather` | `NeverCancelledInReview` violated | violated | 7,491 / 3,047 | 19 | 01s |
| `witness-cancel-while-running` | design, `try_gather` | `NeverCancelledWhileRunning` violated | violated | 10,664 / 4,295 | 20 | 01s |
| `witness-try-not-lowest` | design, `try_gather` | `NeverFirstAboveLowest` violated | violated | 100,603 / 37,712 | 30 | 01s |
| `witness-gather-crash` | design, `gather` | `NeverGatherCrashInList` violated | violated | 475,031 / 165,703 | 36 | 04s |
| `witness-race-loss-then-win` | design, `race` | `NeverRaceLossThenWin` violated | violated | 56,338 / 23,247 | 25 | 01s |
| `witness-race-all-lose` | design, `race` | `NeverRaceAllLose` violated | violated | 441,118 / 163,097 | 36 | 04s |
| `witness-slot-hand-off` | design, nested | `NeverSlotHandOff` violated | violated | 2,240 / 979 | 18 | 00s |
| `witness-hang` | design, `gather`, branch 3 hangs, no cancel | `Terminates` violated | violated | 80,679 / 27,760 | 33 | 05s |

Counts for failing checks are the part explored before the first counterexample. Every counterexample was read;
each fails for the reason given under [Mismatches](#mismatches-found-in-the-first-version-and-their-fixes) and
[What the checks show](#what-the-checks-show). The witnesses show: a sibling cancelled during its review
(`witness-cancel-in-review`, the live run's path) and while running; branch 2 storing the first failure, branch 1
storing one after it, and `try_gather` returning branch 2's; a crash as a Failure inside `gather`'s list; `race` returning branch 1's success
after branch 2 lost first; `race` returning a Failure when all three lose; branch 1 waiting to take its slot back while
branch 2 holds it; and hanging calls never ending without a cancellation.

## Mismatches found in the first version and their fixes

### 1. A call cancelled by its fan-out was recorded as a guardian denial or as a failure (bug, fixed)

Against `5fbe215d`, `code-cancel-denied` and `code-cancel-error` violated `CancelledCallsRecordedCancelled` (they are
now `cancel-recorded-denied` and `cancel-recorded-error`, with `RecordCancelled = FALSE`).

- In review (the live run): branch 1's call is denied by the guardian, branch 1 returns that failure, `try_gather`
  cancels the rest while branch 2's `read` is waiting for its review. `reviewPermission` saw the cancelled context and
  returned `permissionReviewFailure("automatic permission review failed: ...")`, an `Outcome: deny`, which
  `reviewNestedCall` stored and turned into `permissionDeniedError`; `finish` recorded `denied` and
  `recordCodeModeCall` attached the guardian `deny` and its reason. MCP calls in `execute`'s permission callback did the
  same, recorded as `error`.
- While running: the tool returned its context error, and `finish` recorded `error`.

The same happened on `main` without `try_gather` (a crashing `gather` branch, or a stopped turn), so the fix is its own
commit before the feature ("show a script call stopped by its script as cancelled"): `codeModeCallStatus` has a
`cancelled` value; `reviewNestedCall` returns the context's error without storing the review's decision when the
context is done; a call whose run ends with its context done is recorded `cancelled`, and so is an MCP call whose
check failed because its context was done; the Web shows "cancelled"; and `backend/model_resolver.go` logs a cancelled
provider request at INFO instead of ERROR (not modelled). `code-cancel-denied` and `code-cancel-error` now pass with
every property. Go tests: the cancelled `try_gather` script in `TestExecuteRecordsEachCommand` (a reviewed host call
and a reviewed MCP call cancelled in review), `TestBackgroundForegroundCallKeepsTurnCancellation` (a call stopped
while running) and `TestOpenAIClientLogsProviderRequestsOnError` (the log level).

### 2. A branch could start new tool calls after it was cancelled (bug, fixed)

Against `5fbe215d`, `code-call-after-cancel` violated `NoCallStartsAfterCancel` (it is now `call-after-cancel`, with
`CheckBeforeCall = FALSE`): branch 2 passes `callBranch`'s first check, branch 1's failure makes `try_gather` cancel,
and branch 2 then starts its call with the context already cancelled.

Nothing checked the context between `callBranch`'s first check and a tool call, and `thread.Cancel` only takes effect
at the next function entry or loop back-edge. This was also on `main` (a stopped turn, `race`), so the fix is its own
commit before the feature ("start no tool call once a script is stopped"): `newKwargsValueBuiltin` returns
`context.Cause(ctx)` before `CallValue`, so the call is neither made nor recorded. A cancellation that lands just after
that check is the same as one that lands during the call, which fix 1 records as cancelled. `code-call-after-cancel`
now passes with every property. Go test: `TestToolIsNotCalledOnceItsScriptIsCancelled`.

### 3. "The first failure" (wording, decided: first in time)

`try_gather` returned `results[slices.IndexFunc(results, IsFailure)]`, the lowest index among the failures stored when
`Wait` returned, while README and the `execute` prompt say "the first failure". The owner decided that "first" means
first in time. `runGather` now keeps the first failure a branch stores (`sync.Once`) and returns it, which is
`TryReturnsFirstFailure` (it replaces `TryReturnsLowestFailure`). `try-lowest-index` (`FirstInTime = FALSE`, the old
code) violates it: branch 2 stores its failure first, branch 1 stores one just after (before the cancellation reaches
it), and `try` returns branch 1's. `witness-try-not-lowest` shows that the design really can return a failure that is
not the lowest-index one. Go test: `TestTryGatherReturnsTheFailureThatCameFirst` (the later branch fails at once, the
earlier one only after the cancel, and the later branch's failure is returned). In Go the two rules differ only when
two branches fail within the moment before the cancellation reaches the second, so that test also passes on the old
code; the model is what checks the difference.

Also seen, not counted as mismatches:

- `race` under turn cancellation: if a loss was stored before the turn was cancelled, `runRace` returns that Failure
  with no error instead of stopping (and with no stored result at all it returns `nil, nil`, which Starlark turns into
  "internal error: nil (not None) returned from race"). The main thread then stops at its next function entry or loop
  back-edge, and `codemode.Run` reports the cancellation, so the script still stops; the model does not check
  `RaceNoFailureWhileSuccess`'s second half when the turn is cancelled.
- `runGather` keeps calling `group.Go` after the decision; those goroutines fail `slots.Acquire` at once, so no branch
  body starts (`NoBranchStartsAfterDecision` holds).

## What the checks show

- The Go code satisfies every property: every `code-*` check passes, including `code-cancel-denied`,
  `code-cancel-error` and `code-call-after-cancel`, which failed against the first version.
- `try-no-cancel`: without the cancel, branch 3 starts after the first failure; `try-no-cancel-hang`: branch 1
  crashes, nothing cancels the hanging branches 2 and 3, and `try_gather` never returns.
- `gather-crash-stops`: branch 1 crashes, `gather` returns an error, the other branches are cancelled and the script
  stops.
- `race-any`: branch 1 fails first and wins; branches 2 and 3 never start, and `race` returns a Failure.
- `nested-hold-slot`: branch 1 keeps the only slot while its nested branch waits for it; nothing ever runs again.
  `releaseParentSlot` is what prevents this.

## Limits

- Bounded: 3 branches (2 with nesting), one level of nesting with one inner branch, one tool call per branch.
- Durations are not modelled; "hangs" means "takes longer than anything else".
- A call either needs review or not for the whole check (`Review`); redirect reviews inside `webfetch` behave like
  the review step.
- Non-resumable calls are not cancelled when the turn stops because RocketClaw shuts down (`callContext`,
  `ErrShutdown`); that is intended and not modelled. Cancellation reaches tools only through their context; whether
  each tool honours it is not modelled (a call may finish normally after the cancellation).
- The rendering of the returned value and the step status (C+) are not modelled.
