---
title: A Plain Cancel of a Child Context Can Erase the Shutdown Cause
date: 2026-10-19
category: docs/solutions/logic-errors/
module: internal/rocketclaw/backend
problem_type: logic_error
component: assistant
symptoms:
  - "TestShutdownKeepsRunLockAliveDuringBash/background=false failed now and then in CI, finishing in under a second although its bash command sleeps for 2 seconds."
  - "Shutdown released the run lock and stopped the frontends while a foreground bash command was still running; the command never finished."
  - "It never reproduced locally, in isolation or under CPU load, but failed in about a quarter of CI's failed runs after it began."
root_cause: concurrency
resolution_type: code_fix
severity: medium
related_components:
  - internal/rocketcode
tags:
  - shutdown
  - context-cause
  - cancellation
  - bash
  - flaky-test
---

# A Plain Cancel of a Child Context Can Erase the Shutdown Cause

## Problem

Shutdown is supposed to let a running foreground bash call finish. RocketCode
knows it is a shutdown, rather than an interrupt, only from the cancellation
cause: `callContext` in `internal/rocketcode/looper.go` keeps a non-resumable
call running when the cause is `rocketcode.ErrShutdown`, and cancels it for any
other cause. Shutdown cancels the run's work context with `errShutdown`
(`internal/rocketclaw/backend/app.go`), which wraps `rocketcode.ErrShutdown`.

Under load, the turn sometimes saw `context.Canceled` instead, and its bash
command was killed.

## Root cause

When a context is canceled with a cause, Go closes its Done channel first and
only then walks its descendants to cancel them with the same cause. A goroutine
waiting on that Done channel can run before the walk reaches a deep descendant.

`threadBridgeManager.Run` waits on the work context's Done channel and then
calls `Bridge.Stop` on every bridge. `Bridge.Stop` used to cancel the active
turn's context with a plain cancel. When it got there first, the turn's cause
became `context.Canceled`; the propagating `errShutdown` then found the turn
already canceled and changed nothing. From there the background registry
relays the cause to the `execute` script, `callContext` treats it as an
interrupt, and bash, which honors its context since the change titled "let
stop interrupt a running bash command" (commit 37163263), kills the command.

Before that change, bash ignored every cancellation except a killed background
job, so the race existed but was harmless. That is why the failure appeared
abruptly and only under parallel load.

## Solution

`Bridge.Stop` no longer cancels the active turn. Every caller that can have a
running turn has already canceled the bridge's context, so the turn ends with
that context's own cause: `errShutdown` on shutdown, `context.Canceled` when
the run lock is lost. A user's stop (`InterruptActiveTurn`) and a stopped
shadow turn still cancel the turn directly, so they still interrupt bash.

An earlier version of the fix had `Stop` cancel with `errShutdown` instead.
Review caught that this would let bash keep running after a lost run lock,
which cancels the work context with a plain `context.Canceled`: leaving the
cause to the parent context is the correct fix, not stamping a different one.

The regression test is `TestBridgeStopDoesNotClassifyOrdinaryTurnAsInterrupted`
in `internal/rocketclaw/backend/bridge_test.go`: it stops a bridge before its
turn's parent context is canceled and checks the turn ends with the parent's
cause, for both `errShutdown` and `context.Canceled`. The fix is pending in the
pull request for the `fix-shutdown-bridge-stop` bookmark.

## How it was found

The failure could not be reproduced locally (about 20 runs on macOS and
Linux). The GitHub Actions history located it: across 200 failed runs, the
fast `background=false` failure first appeared on main at the commit after
37163263 landed, in none of the 62 failed runs before it and in 16 of the 65
after. Reading that commit pointed at cancellation causes, and forcing
`Bridge.Stop` to run before `cancelWork(errShutdown)` reproduced the exact CI
symptoms every time.

## Prevention

Never cancel a descendant of a context whose cancellation cause carries
meaning, such as `ErrShutdown`, with a plain cancel while that parent may be
canceling: the plain cancel can win and erase the cause. Let the parent's
cancellation reach the descendant, or cancel the descendant with the parent's
cause. When a flaky test resists local reproduction, bisect its first
appearance in the CI failure history before theorizing.
