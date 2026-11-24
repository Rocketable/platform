---
title: A Slack Mention Thread's Agent Session Status Follows Its Running Turn
date: 2026-11-04
last_updated: 2026-11-24
category: docs/solutions/architecture-patterns/
module: internal/rocketclaw/frontend/slack
problem_type: architecture_pattern
component: assistant
severity: medium
applies_when:
  - Adding or changing code that calls agents.sessions.setStatus for a Slack mention thread
  - Showing a running turn's progress in Slack (status, checklist, or a new kind of update)
  - Publishing anything from a turn to connectors while the turn runs
  - Adding a kind of Slack turn that shows no status, like an Ambient Turn
related_components:
  - internal/rocketclaw/backend
  - internal/rocketclaw/protocol
tags: [slack, agent-session, setstatus, processing, suspended, stop, checklist, plan-block, turn-progress, polling, outbound-bus, ambient-turn, deferred]
---

# A Slack Mention Thread's Agent Session Status Follows Its Running Turn

## Context

A mention thread used to show four signals for one running turn: a 🤖 reaction, a *Thinking...* placeholder message, a "working" footer line, and Slack's own agent-session working state. The final answer was then edited into the placeholder, so Slack had no new reply to highlight. PR #957 (released in v0.2.26) replaced all of them with Slack's agent session: the status shows working, needs-someone or idle, a checklist message lists the turn's scripts and subagents, and the answer posts as a new reply.

The status now has six writers in `internal/rocketclaw/frontend/slack/connector.go`:

| Writer | When | Sends |
|---|---|---|
| `handleAppMentionEvent` | a mention passes the allowlist and report-thread checks, before `SubmitMention` | `processing`, with the session title and initiator, only when no turn of the thread is running |
| `settleSession` | the mention started no turn | `active`, unless a turn of the thread is running |
| `startSession` | a turn in a mention thread starts or resumes, unless its outbound is `Deferred` | `processing` |
| `pollSession` | every 3 seconds while the turn runs | `suspended` while a Web question waits, `processing` otherwise, re-sent before Slack's one-hour limit; `active` once the turn no longer runs |
| `handleAgentSessionStopped` | Slack's Stop button | `active`, or the running turn's current status again |
| `finishResponse` | the turn's final was delivered, or its delivery was abandoned (`AbortResponse`) | `active` (mention threads only, never for a `Deferred` outbound) |

An Ambient Turn writes no status at all. Its intake, `handleMessageEvent`, sends none, and the backend marks its outbound messages `Deferred` (`listensAmbient` in `internal/rocketclaw/backend/bridge.go`), which keeps it out of `startSession` and the `finishResponse` settle. A `Deferred` final with no answer, or one that failed or was stopped, returns from `SendResponse` before anything is posted or settled.

## Guidance

**The running turn's `slackSession` owns the thread's status.** Its `status` field is the last status sent for the turn, and `pollSession` sends only when the wanted status differs from it, or when the 50-minute renewal is due. Any other writer that sends a status while a turn runs makes that cache wrong, and the poll then never corrects Slack. So a writer for a thread with a running turn either goes through `sendStatus`, which updates the cache, or sends nothing. The mention handler sends nothing when `sessionTurn` finds a session. The Stop handler re-sends `session.status` for a turn that keeps running.

**An Ambient Turn has no `slackSession`, so its thread looks idle to every writer.** A mention that arrives while one runs sends `processing` with its title and initiator, then waits in the Thread Queue. The Ambient Turn's end writes nothing, so that status stands until the mention's own turn starts its session. A Stop pressed meanwhile finds no session and only sends `active`; it reaches neither the Ambient Turn nor the waiting mention. Do not give a `Deferred` turn a session or a settle to "tidy" this: the status it would write belongs to the mention, not to the hidden turn.

**One lock for all of it.** `responseMu` guards the `sessions` map and every session's fields. Every status write for a turn happens under it, which orders a poll, a final and a Stop against each other. `pollSession` reads the database before taking the lock and then checks that the session is still the one its timer belongs to, so a slow query never holds up answer delivery or the Slack event loop.

**Read durable turn state; don't publish progress from the turn.** `SessionService.TurnProgress` (`internal/rocketclaw/backend/store_dao.go`) answers whether the turn still runs (its row is not `waiting` or `done`), whether the conversation has an unanswered Web question, and its latest 50 `execute` and `task` calls, titled by the `description` argument their call gave. The obvious alternative, having the turn publish an outbound message when a question starts or a step finishes, was rejected: `Runtime.PublishOutbound` (`internal/rocketclaw/backend/runtime.go`) waits for every connector to acknowledge each event, so every mid-turn event would stall the agent on Slack's API.

**Slack follows the backend, not the connector's memory.** A turn whose final never reaches Slack (for example after an `inputPersistenceError`, which leaves the row `running` until Stop or a restart) would otherwise keep its session, and its renewal would show the thread working forever. The poll ends the session as soon as `TurnProgress` reports the turn is no longer running, so Slack is stuck exactly as long as the Web shows the turn running, and Stop clears both.

## Why This Matters

The rule was learned from a bug found in review. While a Web-started turn waited on a question, its thread showed `suspended`. A new mention in that thread, or Slack redelivering an old one, sent `processing` unconditionally. The session cache still said `suspended`, so the poll saw no change and never sent it again: Slack showed "working" until someone answered in Web. `TestMentionDuringWaitingTurnKeepsItSuspended` (`connector_test.go`) fails on that code: the status list ends in an extra `processing`.

Two Slack limits shape the rest:

- A `processing` session times out to `active` after one hour unless it is sent again, and a session shows Stop only while `processing`. That is why the poll renews 10 minutes before `slackWorkingStateLifetime` (`connector.go`).
- A streamed message (`chat.startStream`) is cut off after 10 minutes, and scenario runs take longer. So the checklist is one plan-block message edited at most once per poll, Slack's documented pattern for long tasks. It is the only message RocketClaw edits; answers are never edited.

## When to Apply

- Adding a status writer: route it through the running turn's session or skip it when one exists, and add a case to the status-list tests.
- Showing new mid-turn progress in Slack: add it to `TurnProgress` and render it from `pollSession`, rather than publishing it on the outbound bus.
- Adding a Slack turn that must stay invisible until it answers: mark its outbound `Deferred` and write no status for it anywhere, so a mention queued behind it keeps its own.
- Changing the poll interval or renewal: keep the renewal inside Slack's one-hour limit, and keep checklist edits to at most one per poll, plus the final one.

## Examples

The mention handler before and after the fix:

```go
// Before: overwrote a waiting turn's suspended status.
c.setSessionStatus(ctx, &slack.AgentSessionSetStatusParameters{Status: slack.AgentSessionStatusProcessing, ...})

// After: only a thread with no running turn gets processing (and its title and initiator).
c.responseMu.Lock()
if turnID, _ := c.sessionTurn(ev.Channel, threadTS); turnID == "" {
	c.setSessionStatus(ctx, &slack.AgentSessionSetStatusParameters{Status: slack.AgentSessionStatusProcessing, ...})
}
c.responseMu.Unlock()
```

Tests that hold the pattern: `TestMentionTurnShowsProgress`, `TestMentionTurnPollRenewsAndEndsStaleSession`, `TestMentionTurnPollsUntilItEnds`, `TestMentionDuringWaitingTurnKeepsItSuspended` and `TestDeferredTurnPostsOnlyItsAnswer` (no session, no status, Stop leaves the turn running) in `connector_test.go`; `TestTurnProgress` in `internal/rocketclaw/backend/active_turn_test.go`. The model in `docs/verification/slack-sticky/` checks `AmbientWritesNoStatus`; its `deferred-session` check, which tracks an Ambient Turn as a status session, must fail.

Not yet confirmed against a live workspace: slack-go sends `"type":"task_card"` on each checklist task, while Slack's plan-block reference shows tasks without a type (the Python SDK sends it too). If Slack rejects the block, the checklist is missing and `show Slack turn checklist` is logged.

## Related

- [Slack mention redelivery must start at most one turn](../logic-errors/slack-thread-parent-message-redelivery-enqueued-second-turn.md): the same mention entry point. That doc keeps work admission durable in `SubmitMention`; this one keeps the displayed status consistent. Gating the status write on the in-memory session cache decides only what Slack shows, never whether work is admitted.
- Slack's agent sessions guide: https://docs.slack.dev/ai/agent-sessions
