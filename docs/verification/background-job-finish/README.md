# Saving a finished background job: bounded verification

`BackgroundJobFinish.tla` models what happens after one background job's work returns: the worker saves the job's
row (`finishBackgroundJob`, which also marks its note pending), the database may refuse that save for a while or for
good, the registry may shut down at any time, the next start reports a still-running row killed, and a turn delivers
the note.

**Passing checks do not prove the Go code correct.** They cover this small finite model only.

## The model

| Process | Fairness | Go (`internal/rocketclaw/backend/background.go`) |
|---|---|---|
| `Worker` | weak | `backgroundRegistry.work` after `RunBackground` returns: `finishBackgroundJob`, its retries, `noteReady` |
| `Shutdown` | weak | `backgroundRegistry.shutdown` (it may also never happen): close `stopping`, cancel the detached runs still in `r.runs`, `group.Wait`; then the next start's `recoverJobs`, which turns a running row into a killed one with a pending note |
| `Delivery` | weak | a turn that delivers a pending note, woken by `noteReady` or by the restart |

The work returns `ok`, or (only after shutdown started) `cancelled`. The database refuses the first `fails` saves
(`0..MaxFails`), or every save (`dbForever`). Both are chosen at the start.

| Constant | Meaning | Fixed code |
|---|---|---|
| `MaxFails` | most refusals before the database works again | 3 |
| `RetryForever` | retry a refused save until it works (`FALSE`: give up after `MaxFails` attempts, the code before the fix) | `TRUE` |
| `StopRetryOnShutdown` | stop retrying once shutdown starts | `TRUE` |
| `SaveCleanWhenAbandoned` | save a run whose work returned cleanly while shutdown cancelled it (`FALSE`: the code before the fix left it for the next start, which reported it killed) | `TRUE` |
| `SaveCancelled` | save a cancelled run as completed (a broken variant) | `FALSE` |
| `WakeWhileStopping` | call `noteReady` while shutdown runs (`TRUE`: the code before the fix, when a save landed during shutdown) | `FALSE` |

## Properties

- `ExactlyOnce`: a note is delivered at most once.
- `SavedOnceDbWorks` (liveness): a finished job whose database works again is saved, unless shutdown starts first.
- `SavedNoteDelivered` (liveness): a saved note is delivered.
- `ShutdownNotBlocked` (liveness): once shutdown starts, it finishes.
- `FinishedNeverKilled`: a run whose work returned cleanly is reported killed only when every save was refused until
  shutdown started.
- `CancelledNeverCompleted`: a cancelled run is never saved as completed.
- `NoWakeWhileStopping`: no note wakes a turn while shutdown runs; such a note is delivered after the next start.

Witnesses, each of which must fail: `NeverSavedAfterRefusals` (a save works after refusals) and
`NeverGaveUpOnShutdown` (the retries stop because shutdown started).

## Run

```sh
make -C docs/verification/background-job-finish
```

Requires Java 11+, `curl` and Make; the Makefile downloads `tla2tools.jar` v1.8.0 and checks its SHA-256, and writes to
the repository root's `.tmp/verification/background-job-finish/`, like the other models here.

## Results

Recorded 2026-11-22 on Linux (aarch64), OpenJDK 21.0.13.1, TLC `2026.11.19.180144` (rev `bc1b506`), one worker.
`make -k all` exited 0; every check matched its expected outcome.

| Check | Shape | Expected | Result | Generated / distinct states | Depth |
|---|---|---|---|---:|---:|
| `fixed` | the code with both fixes | every property holds | pass, complete state space | 632 / 430 | 16 |
| `give-up-after-three` | `RetryForever = FALSE` (before the retry fix) | `SavedOnceDbWorks` violated | violated | 511 / 330 | 14 |
| `block-shutdown` | `StopRetryOnShutdown = FALSE` | `ShutdownNotBlocked` violated | violated | 554 / 352 | 16 |
| `skip-clean-abandoned` | `SaveCleanWhenAbandoned = FALSE` (before the shutdown fix) | `FinishedNeverKilled` violated | violated | 229 / 170 | 6 |
| `save-cancelled` | `SaveCancelled = TRUE` | `CancelledNeverCompleted` violated | violated | 160 / 116 | 5 |
| `wake-while-stopping` | `WakeWhileStopping = TRUE` | `NoWakeWhileStopping` violated | violated | 219 / 153 | 6 |
| `witness-saved-after-refusals` | fixed | `NeverSavedAfterRefusals` violated | violated | 230 / 161 | 6 |
| `witness-gave-up-on-shutdown` | fixed | `NeverGaveUpOnShutdown` violated | violated | 227 / 159 | 6 |

`give-up-after-three`: three refused saves, the database works again, no shutdown, and the job is never saved: no note
until the next start, which then reports a finished job killed. `block-shutdown`: the database refuses every save,
shutdown starts, and the worker keeps retrying, so shutdown never finishes. `skip-clean-abandoned`: shutdown starts, the
work returns cleanly, the worker skips the save, and the next start reports the job killed. `save-cancelled`: shutdown
cancels the work and the cancelled run is saved completed. `wake-while-stopping`: the save lands during shutdown and the
note wakes a turn.

## Limits

One job, one shutdown and restart, delivery abstracted to one step. Backoff timing is not modelled: a retry is just
another attempt. The model assumes a cancelled run's work ends in an error: every host tool in a script and the task
wait report a cancelled context as an error (tested per tool). In Go the shutdown check before `noteReady` is not
atomic with shutdown starting; the runtime stops the bridge loops before it shuts the registry down, so a wake in that
gap starts no turn.
