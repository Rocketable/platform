---
title: A Slack Mention Thread's Agent Session Status Follows Its Running Turn
date: 2026-11-04
category: docs/solutions/architecture-patterns/
module: internal/rocketclaw/frontend/slack
problem_type: architecture_pattern
component: assistant
severity: medium
applies_when:
  - Adding or changing code that calls agents.sessions.setStatus for a Slack mention thread
  - Showing a running turn's progress in Slack (status, checklist, or a new kind of update)
  - Publishing anything from a turn to connectors while the turn runs
related_components:
  - internal/rocketclaw/backend
  - internal/rocketclaw/protocol
tags: [slack, agent-session, setstatus, processing, suspended, stop, checklist, plan-block, turn-progress, polling, outbound-bus]
---

# A Slack Mention Thread's Agent Session Status Follows Its Running Turn

## Context

A mention thread used to show four signals for one running turn: a 🤖 reaction, a *Thinking...* placeholder message, a "working" footer line, and Slack's own agent-session working state. The final answer was then edited into the placeholder, so Slack had no new reply to highlight. PR #957 (unmerged as of this writing) replaced all of them with Slack's agent session: the status shows working, needs-someone or idle, a checklist message lists the turn's scripts and subagents, and the answer posts as a new reply.

The status now has six writers in `internal/rocketclaw/frontend/slack/connector.go`:

| Writer | When | Sends |
|---|---|---|
| `handleAppMentionEvent` (`:1104`) | a mention passes the allowlist and report-thread checks, before `SubmitMention` | `processing`, with the session title and initiator, only when no turn of the thread is running (`:1161`) |
| `settleSession` (`:582`) | the mention started no turn | `active`, unless a turn of the thread is running |
| `startSession` (`:470`) | a turn in a mention thread starts or resumes | `processing` |
| `pollSession` (`:480`) | every 3 seconds while the turn runs | `suspended` while a Web question waits, `processing` otherwise, re-sent before Slack's one-hour limit (`:507`); `active` once the turn no longer runs |
| `handleAgentSessionStopped` (`:1057`) | Slack's Stop button | `active`, or the running turn's current status again |
| `finishResponse` (`:881`) | the turn's final was delivered, or its delivery was abandoned (`AbortResponse`) | `active` (mention threads only) |

## Guidance

**The running turn's `slackSession` owns the thread's status.** Its `status` field is the last status sent for the turn, and `pollSession` sends only when the wanted status differs from it, or when the 50-minute renewal is due. Any other writer that sends a status while a turn runs makes that cache wrong, and the poll then never corrects Slack. So a writer for a thread with a running turn either goes through `sendStatus` (`:571`), which updates the cache, or sends nothing. The mention handler sends nothing when `sessionTurn` finds a session. The Stop handler re-sends `session.status` for a turn that keeps running.

**One lock for all of it.** `responseMu` (`:85`) guards the `sessions` map and every session's fields. Every status write for a turn happens under it, which orders a poll, a final and a Stop against each other. `pollSession` reads the database before taking the lock and then checks that the session is still the one its timer belongs to, so a slow query never holds up answer delivery or the Slack event loop.

**Read durable turn state; don't publish progress from the turn.** `SessionService.TurnProgress` (`internal/rocketclaw/backend/store_dao.go:1230`) answers whether the turn still runs (its row is not `waiting` or `done`), whether the conversation has an unanswered Web question, and its latest 50 `execute` and `task` calls, titled by the `description` argument their call gave. The obvious alternative, having the turn publish an outbound message when a question starts or a step finishes, was rejected: `Runtime.PublishOutbound` (`internal/rocketclaw/backend/runtime.go:125`) waits for every connector to acknowledge each event, so every mid-turn event would stall the agent on Slack's API.

**Slack follows the backend, not the connector's memory.** A turn whose final never reaches Slack (for example after an `inputPersistenceError`, which leaves the row `running` until Stop or a restart) would otherwise keep its session, and its renewal would show the thread working forever. The poll ends the session as soon as `TurnProgress` reports the turn is no longer running, so Slack is stuck exactly as long as the Web shows the turn running, and Stop clears both.

## Why This Matters

The rule was learned from a bug found in review. While a Web-started turn waited on a question, its thread showed `suspended`. A new mention in that thread, or Slack redelivering an old one, sent `processing` unconditionally. The session cache still said `suspended`, so the poll saw no change and never sent it again: Slack showed "working" until someone answered in Web. `TestMentionDuringWaitingTurnKeepsItSuspended` (`connector_test.go:1713`) fails on that code: the status list ends in an extra `processing`.

Two Slack limits shape the rest:

- A `processing` session times out to `active` after one hour unless it is sent again, and a session shows Stop only while `processing`. That is why the poll renews 10 minutes before `slackWorkingStateLifetime` (`connector.go:37`).
- A streamed message (`chat.startStream`) is cut off after 10 minutes, and scenario runs take longer. So the checklist is one plan-block message edited at most once per poll, Slack's documented pattern for long tasks. It is the only message RocketClaw edits; answers are never edited.

## When to Apply

- Adding a status writer: route it through the running turn's session or skip it when one exists, and add a case to the status-list tests.
- Showing new mid-turn progress in Slack: add it to `TurnProgress` and render it from `pollSession`, rather than publishing it on the outbound bus.
- Changing the poll interval or renewal: keep the renewal inside Slack's one-hour limit, and keep checklist edits to at most one per poll, plus the final one.

## Examples

The mention handler before and after the fix:

```go
// Before: overwrote a waiting turn's suspended status.
c.setSessionStatus(ctx, &slack.AgentSessionSetStatusParameters{Status: slack.AgentSessionStatusProcessing, ...})

// After: only a thread with no running turn gets processing (and its title and initiator).
c.responseMu.Lock()
if turnID, _ := c.sessionTurn(protocol.SlackThreadConversationID(ev.Channel, threadTS)); turnID == "" {
	c.setSessionStatus(ctx, &slack.AgentSessionSetStatusParameters{Status: slack.AgentSessionStatusProcessing, ...})
}
c.responseMu.Unlock()
```

Tests that hold the pattern: `TestMentionTurnShowsProgress`, `TestMentionTurnPollRenewsAndEndsStaleSession` (`:1663`), `TestMentionTurnPollsUntilItEnds` (`:1735`) and `TestMentionDuringWaitingTurnKeepsItSuspended` in `connector_test.go`; `TestTurnProgress` in `internal/rocketclaw/backend/active_turn_test.go`.

Not yet confirmed against a live workspace: slack-go sends `"type":"task_card"` on each checklist task, while Slack's plan-block reference shows tasks without a type (the Python SDK sends it too). If Slack rejects the block, the checklist is missing and `show Slack turn checklist` is logged.

## Related

- [Slack mention redelivery must start at most one turn](../logic-errors/slack-thread-parent-message-redelivery-enqueued-second-turn.md): the same mention entry point. That doc keeps work admission durable in `SubmitMention`; this one keeps the displayed status consistent. Gating the status write on the in-memory session cache decides only what Slack shows, never whether work is admitted.
- Slack's agent sessions guide: https://docs.slack.dev/ai/agent-sessions
