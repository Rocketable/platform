---
title: Slack Mention Redelivery Must Start At Most One Turn
date: 2026-08-26
last_updated: 2026-10-26
category: docs/solutions/logic-errors/
module: internal/rocketclaw/frontend/slack
problem_type: logic_error
component: assistant
symptoms:
  - "A redelivered Root Slack Mention cleared a follow-up already buffered for the running turn, so the follow-up was never delivered."
  - "Slack's redelivery of the thread parent message (same ts, thread_ts equal to ts) was enqueued and ran as a second turn for the same mention."
  - "The phantom second turn could leave a thinking card that never cleared."
root_cause: logic_error
resolution_type: code_fix
severity: high
tags:
  - slack
  - app-mention
  - redelivery
  - dual-delivery
  - thread-queue
  - at-most-once
  - idempotency
---

# Slack Mention Redelivery Must Start At Most One Turn

## Problem

Slack can deliver the same Root Slack Mention more than once: as a repeated `app_mention`, and as a `message` event for the thread parent whose `thread_ts` equals its own `ts`. One mention must start at most one turn, and a redelivery must not disturb work that is already waiting or running in the Managed Slack Thread.

## Symptoms

Two historical failures, both in code that no longer exists:

- **Redelivered root mention cleared buffered follow-ups (historical).** The connector kept an in-memory per-thread stack for mid-turn messages (Buffered Follow-Ups, later Slack Steers). Each root delivery reset that stack, so a redelivery during the first turn dropped a follow-up the user had already sent.
- **Redelivered parent message enqueued a second turn (historical).** The connector's plain-message path treated any message with `thread_ts` set as a thread reply. The parent redelivery arrived mid-turn, became an Enqueued Slack Message on the Thread Queue, and ran as a second turn for the same text after the first finished.

## What Didn't Work

All of this is history. The connector code it names has been removed.

- **Connector-level patches, one per event shape.** The first fix made the stack initialization idempotent: create a missing key, never overwrite an active one. The second fix swallowed a mid-turn message whose `ts` equalled its `thread_ts`. Each patch closed one redelivery shape and missed the other. The tests for each sent only their own shape, so neither caught the other bug.
- **Dropping every parent message in the reply path.** This was too broad at the time, because an idle parent message still started commands and first turns.
- **In-memory dedup state in the connector.** The stack map was process-local. A restart, or a redelivery that arrived after the turn finished, could not be recognized from it.

## Solution

Slack intake is now mention-only, and duplicates are refused at one durable point.

1. The connector handles only channel renames, `AppMentionEvent` and `AgentSessionStoppedEvent` (`internal/rocketclaw/frontend/slack/connector.go:1430-1439`). Plain messages, including the parent `message` redelivery, never reach the router. Nothing else in Slack drives an agent.
2. `handleAppMentionEvent` (`internal/rocketclaw/frontend/slack/connector.go:1493-1581`) builds the inbound message and calls `threadRouter.SubmitMention` (line 1565). It adds the robot reaction only when the mention is accepted (lines 1577-1580). A refused redelivery returns `false, nil`, so it gets no reaction and no failure reply.
3. `threadBridgeManager.SubmitMention` (`internal/rocketclaw/backend/thread_bridges.go:338-374`) makes the Thread Queue item ID `"slack:" + channel + ":" + message ts` (line 343). Inside one State Store transaction, holding the session history lock, it checks for that ID in two places: any model turn row (`turns` with `takes_lane`, whether waiting, running or finished) and saved history (`entries` whose `replay_input` carries that `input_id`) (SQL at lines 361-368). If it finds the ID, it returns `false, nil` (lines 372-373) and writes nothing.

Tests that pin this behavior:

- `TestHandleAppMentionEventRedeliveryReactsOnce` (`internal/rocketclaw/frontend/slack/connector_test.go:4242-4271`) sends the root mention, the parent `message` and a threaded reply mention, twice each. It expects four `SubmitMention` calls (the two parent messages never reach the router) and exactly two robot reactions.
- `TestHandleEventsAPIIgnoresMessagesAndReactions` (`internal/rocketclaw/frontend/slack/connector_test.go:4180-4198`) shows that a parent `message` with `ts == thread_ts`, a plain reply, a reaction and an edited mention cause no router call.
- `TestThreadBridgeManagerSubmitMentionQueuesEachMentionOnce` (`internal/rocketclaw/backend/thread_bridges_test.go:239-305`) refuses a redelivery in each state: while the mention waits, mid-turn, after a restart, and after its turn has finished. It also checks that a mention never steers the running turn.

## Why This Works

Both historical bugs had the same cause: the connector decided whether an event was new work from its own in-memory state, and each event shape needed its own rule. Now only one kind of Slack event can create work, and the decision is made where the work is stored. The ID comes from Slack's message `ts`, which stays the same across redeliveries. Because the check and the insert happen in the same transaction, two concurrent deliveries cannot both pass. The check also covers waiting, running, finished and saved work, so a redelivery after a restart or after the answer is refused too. A refused redelivery writes nothing, so queued and running work is left as it was.

## Prevention

- Dedupe Slack redeliveries at the durable boundary, keyed by Slack's message `ts`. Never dedupe with in-memory connector state such as maps, stacks or "turn active" flags.
- Any new Slack entry point that can create work must go through `SubmitMention` and its `slack:<channel>:<message ts>` ID. Do not write to the Thread Queue directly from the connector.
- Do not take "`thread_ts` is set" to mean "this is a reply". Slack also sets `thread_ts` on the thread parent.
- Do not add Slack event types to the switch in `handleEventsAPI` without deciding how their redeliveries are deduped. A plain-message handler would bring back the parent-redelivery path.
- Any change to the admission SQL must keep the check and the insert in one transaction, and must keep both lookups: turn rows and saved history.
- Redelivery tests must send every shape Slack uses (repeated `app_mention`, parent `message` with `ts == thread_ts`, threaded reply). Assert both the number of accepted submissions and the visible receipts.

## Related Issues

- [Saved queue never started after restart](saved-queue-never-started-after-restart.md): the same Thread Queue rows after a restart.
- Historical fixes, now removed: [PR #1](https://github.com/Rocketable/platform/pull/1) (idempotent stack initialization) and [PR #16](https://github.com/Rocketable/platform/pull/16) (mid-turn parent message swallowed).
- `CONCEPTS.md`: Thread Queue, Managed Slack Thread and Root Slack Mention. Buffered Follow-Up, Slack Steer and Enqueued Slack Message are listed there as historical.
