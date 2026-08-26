---
title: Slack Thread Parent Message Redelivery Enqueued Second Turn
date: 2026-08-26
category: docs/solutions/logic-errors/
module: internal/rocketclaw/slackconnector
problem_type: logic_error
component: assistant
symptoms:
  - "A Slack hail starts a turn from the app_mention event, then the same parent message event (same ts, thread_ts=ts) is treated as a mid-turn send."
  - "That parent is enqueued on the Thread Queue and popped as a second turn."
  - "The second turn leaves a stuck thinking card."
root_cause: logic_error
resolution_type: code_fix
severity: high
tags:
  - slack
  - app-mention
  - redelivery
  - thread-queue
  - enqueued-slack-message
  - slack-steer
  - dual-delivery
  - thinking-card
---

# Slack Thread Parent Message Redelivery Enqueued Second Turn

## Problem

Slack delivers a Root Slack Mention twice: first as `app_mention`, then as a `message` whose `thread_ts` equals the message `ts`. The first event correctly starts a Managed Slack Thread. The second event is the same parent, not a new human reply.

`handleMessageEvent` treats any present `thread_ts` as a social thread reply (`internal/rocketclaw/slackconnector/connector.go:3254-3265`). After the mention has called `beginSlackStack` (`internal/rocketclaw/slackconnector/connector.go:3632-3633`), that parent looks like a mid-turn plain send.

Steer-or-enqueue then does later-work work: a too-late send becomes an Enqueued Slack Message (`handleMidTurnPlainSend` at FinalAnswer calls `stashEnqueuedMessage`, `internal/rocketclaw/slackconnector/connector.go:4054-4057`). When the hail turn ends, `PickLaterWork` pops the Thread Queue and `ActivateEnqueue` posts a consume card plus a new thinking card (`internal/rocketclaw/slackconnector/connector.go:360-370`).

`handleAppMentionEvent` already refuses real thread replies: if `thread_ts` is set and not equal to `ts`, it returns (`internal/rocketclaw/slackconnector/connector.go:3568-3570`). The message path had no matching parent swallow.

## Symptoms

- After a root hail, Slack's parent `message` redelivery entered `handleMidTurnPlainSend` while the stack was active.
- If the hail turn was already writing the final answer, the parent was stashed as an Enqueued Slack Message (envelope on the root `ts`).
- If the hail turn was still in the tool loop, the same parent became a Slack Steer (hourglass on the root `ts`).
- At turn end, `PickLaterWork` treated that row as later work and `ActivateEnqueue` opened a second thinking card for the same hail text.
- Operators saw one mention start two turns: the real hail, then a 📨 card and a thinking card that could stay up if that second turn never finished.

## What Didn't Work

Excluding every parent from `socialThreadReply` (require `thread_ts != ts` in `handleMessageEvent`) was too broad. Idle `$cron` and first-turn starts use a parent `message` with matching timestamps (`TestHandleMessageEventConsumesBareCronCommands`, `internal/rocketclaw/slackconnector/connector_test.go:10149-10180`). Those would hit the not-allowed-managed-thread return (`internal/rocketclaw/slackconnector/connector.go:3277-3280`).

Relying on the existing app-mention redelivery tests was not enough. They re-fire `app_mention` plus a follow-up with a distinct `ts`. They never send the parent `message` with `ts == thread_ts`.

## Solution

Swallow only the mid-turn parent redelivery. In `handleMidTurnPlainSend`, after the stack-active check, if `MessageTS == ThreadTS`, return true. Idle parents still fall through: stack inactive → return false → `$cron` / first turn continue.

The fix is in [PR #16](https://github.com/Rocketable/platform/pull/16) (unmerged as of writing).

Before (active stack always steered or enqueued):

```go
if !active {
	return false
}
if c.threadRouter.TurnPhase(...) == harnessbridge.ThreadTurnFinalAnswer {
	c.stashEnqueuedMessage(...)
	return true
}
return c.bufferSlackStack(...)
```

After (`internal/rocketclaw/slackconnector/connector.go:4046-4060`):

```go
if !active {
	return false
}
if strings.TrimSpace(replyTarget.MessageTS) == strings.TrimSpace(replyTarget.ThreadTS) {
	return true
}
if c.threadRouter.TurnPhase(...) == harnessbridge.ThreadTurnFinalAnswer {
	c.stashEnqueuedMessage(...)
	return true
}
return c.bufferSlackStack(...)
```

A real mid-turn reply has `MessageTS != ThreadTS` and still becomes a Slack Steer or an Enqueued Slack Message.

## Why This Works

Two Slack events, one hail:

1. `app_mention` starts the Managed Slack Thread and the stack (`handleAppMentionEvent`, `internal/rocketclaw/slackconnector/connector.go:3553-3673`).
2. Parent `message` with `thread_ts == ts` hits `handleMidTurnPlainSend` (`internal/rocketclaw/slackconnector/connector.go:3418-3419`). Stack is active, timestamps match, return true. No envelope, no hourglass, no Thread Queue row.

Idle parent is a different state. No active stack → return false → `$cron` or first turn still run. That is why the swallow lives in `handleMidTurnPlainSend`, not in `socialThreadReply`.

## Prevention

- Do not treat "`thread_ts` present" as "this is a reply." Slack sets `thread_ts` on the parent itself.
- Do not fold the swallow into `socialThreadReply`. Idle parent `$cron` and first turn need that flag.
- Keep `TestHandleMessageEventIgnoresThreadParentRedelivery` (`internal/rocketclaw/slackconnector/connector_test.go:6426-6449`). It fires `handleAppMentionEvent`, then `handleMessageEvent` with `mention.TimeStamp` for both `message_ts` and `thread_ts`, and asserts no Thread Queue row, no extra submit, no envelope, and no hourglass on the parent `ts`. The stub is in FinalAnswer so a regression would enqueue.
- App-mention-only redelivery tests with a distinct follow-up `ts` do not cover this bug.

## Related Issues

- [PR #16](https://github.com/Rocketable/platform/pull/16) — this fix.
- [Slack root app-mention redelivery cleared buffered follow-ups](slack-root-app-mention-redelivery-cleared-buffered-follow-ups.md) — same hail dual-delivery area; that doc is `app_mention` wiping the steer stack via `beginSlackStack`. Different event, different failure.
