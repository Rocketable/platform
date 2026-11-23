---
title: A Hold On Queued Work Is Checked When The Turn Starts, Not Stored As A Held Row
date: 2026-11-18
last_updated: 2026-11-23
category: docs/solutions/architecture-patterns/
module: internal/rocketclaw/backend
problem_type: architecture_pattern
component: assistant
severity: high
applies_when:
  - "Adding a rule that some conversation must start no turn until something elsewhere settles"
  - "Reaching for the Thread Queue's held waiting reason to park work"
  - "Changing activateInbound, pickLaterWork, releaseHeldInputs or the goal hold"
  - "Writing an RPC that holds a conversation's history or queue and wakes the queue when it is done"
tags:
  - thread-queue
  - held
  - activate-inbound
  - sticky-slack
  - thread-session
  - queue-gate
  - context-without-cancel
---

# A Hold On Queued Work Is Checked When The Turn Starts, Not Stored As A Held Row

## Context

`sticky: slack` needed a Thread Session to start no turn while its thread's root mention still waits or runs, unmoved, in a Channel Session. The first plan stored the hold: give the Thread Session's mentions the Thread Queue's existing `held` waiting reason, and release them when the root settles. Plan review found that this hold would not hold:

- `held` is a one-shot park, not a gate. `releaseHeldInputs` (`internal/rocketclaw/backend/thread_bridges.go:913-917`) flips every `held` row of a conversation to `queued`. It runs on every Web input and goal start (`Runtime.RunTurn` and `Runtime.StartGoal` call `bridge.releaseHeld`, `internal/rocketclaw/backend/conversations.go:177` and `:232`), on every agent write (`admitAgentWrite`, `thread_bridges.go:887`), and when someone stashes a non-held item (`stashQueueItem`, `thread_bridges.go:1145-1149`). A released request is also marked `released`, which skips the goal hold (`goalHoldsQueue`, `internal/rocketclaw/backend/bridge.go:1033-1046`).
- `pickLaterWork` is not the only way in. Web input and goal kickoffs go straight to `bridge.enqueue` (`conversations.go:210`, `:256`, and through `Bridge.Submit` at `:266`), then `handle` → `activateInbound` (`bridge.go:847`). A gate checked only in `pickLaterWork` misses them.

So Web input into a held Thread Session would have released the stored hold and run before the root's turn moved there.

## Guidance

Compute the hold from durable state at the moment a new turn would start, inside `activateInbound`, in the same transaction that claims the row. Use the existing goal hold as the model: `goalHoldsQueue` is asked on every activation (`bridge.go:916`), and nothing is stored.

The Thread Session hold is built this way:

- `rootUnsettledSQL` (`bridge.go:1100-1107`) asks whether the thread's root mention, `slack:<channel>:<thread ts>`, has a turn row that is not `done` in a Channel Session of the same channel.
- `activateInbound` joins it with the running-lane check (`bridge.go:937-941`). When either blocks, it starts nothing and leaves the request waiting (`bridge.go:958-961`). This happens before `claimQueueItem`, so no row has changed.
- `pickLaterWork` reads the same CTE (`bridge.go:1185-1194`) so it does not pick work that would just be refused.
- Because nothing is stored, nothing needs to be released. What a stored flag's release did becomes a wake: when the root's turn closes, moves or is skipped, `wakeHeldThreads` (`bridge.go:1109-1125`) runs `PickLaterWork` on the thread's Thread Sessions.

## Why This Matters

A stored hold is only as strong as every path that can clear it or skip it, and in this queue those paths belong to other features (Web, goals, agent writes, stashing) and keep growing. A check made when the turn starts sits on the one path every new turn takes, so a new way to submit work cannot get around it. It also reads the live state: if the root is already done, the turn starts, with no flag left behind to go stale.

The hold also needs a wake. Without one, a refused turn waits until something else happens to wake that conversation. The model's `no-wake-on-release` check shows this: it must fail on `Settles`.

The wake must also outlive the request that took the hold. A Web RPC that holds a conversation's history (Undo, Revert, a Message Tree pick, branch delete) sets `historyMutation`, and `pickLaterWork` starts nothing while that flag is set. The RPC's deferred release then wakes the queue. `pickLaterWork` reads the database with the context it is given (`internal/rocketclaw/backend/bridge.go:1145`), and the deferred call drops its error. So a wake passed the request context does nothing once the browser has gone away, for example during a long interrupt, and the waiting rows sit until another input or a restart. Review of the message-tree branch found this in `StageRevert` and `DeleteBranch` (reliability P2). Both now wake with `context.WithoutCancel(ctx)` (`internal/rocketclaw/backend/revert.go:392-396`, `internal/rocketclaw/backend/branches.go:160-164`). No test covers it yet. A deterministic one needs a way to cancel the request after the hold is taken and before the deferred wake runs.

## When to Apply

- Any rule of the form "this conversation's queue must not start a turn while X". Put it in `activateInbound` for new turns, next to the goal hold and the root hold, and mirror it in `pickLaterWork` only as a skip.
- Wake the held conversations when X ends, with a context that the caller's cancellation cannot stop (`context.WithoutCancel`), on every return path.
- Use `held` only for what it is: an item parked until the next input to that conversation, such as a stopped parent's subagent prompt (`CONCEPTS.md`, Subagent Session).

## Examples

The model in `docs/verification/slack-sticky/` checks the hold when a turn fires (`Held(k)` in `SlackSticky.tla`). It sends Web input into a held Thread Session:

- `no-hold`, which drops the check, must fail on `HeldUntilRootSettles`.
- `witness-web-held` must reach a state where that Web input waited and ran after the root's turn moved (`NoWebHeldAfterMove` is the expected violation).

`TestSlackChannelSessionHoldsThreadUntilRootCloses` (`internal/rocketclaw/backend/active_turn_test.go:580-617`) sends Web input through `RunTurn` to a held Thread Session. It expects nothing to run until the Channel Session turn is stopped, and then the thread's mention to run before the Web input.

## Related

- Plan `docs/plans/2026-11-18-2322-feat-sticky-slack-sessions-plan.md`, KTD6 (the hold) and KTD4 (the running-lane check in the same query).
- [Attached subagent wait must be recorded before work](../logic-errors/attached-subagent-wait-must-be-recorded-before-work.md): the one place `held` is the right tool, a prompt parked until the next write.
- [Saved queue never started after restart](../logic-errors/saved-queue-never-started-after-restart.md): waiting rows start again without new input, so a refused turn must stay a waiting row.
- `CONCEPTS.md`: Thread Queue, Channel Session, Thread Session.
