---
title: Slack Mention Redelivery Must Start At Most One Turn
date: 2026-08-26
last_updated: 2026-11-24
category: docs/solutions/logic-errors/
module: internal/rocketclaw/frontend/slack
problem_type: logic_error
component: assistant
symptoms:
  - "A redelivered Root Slack Mention cleared a follow-up already buffered for the running turn, so the follow-up was never delivered."
  - "Slack's redelivery of the thread parent message (same ts, thread_ts equal to ts) was enqueued and ran as a second turn for the same mention."
  - "The phantom second turn could leave a thinking card that never cleared."
  - "A Slack session turn skipped when it started deleted its turn row, so a Slack retry of that message, or the same root re-sent with a thread mention, was admitted again."
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
  - sticky-slack
  - ambient-listening
---

# Slack Mention Redelivery Must Start At Most One Turn

## Problem

Slack can deliver the same Root Slack Mention more than once: as a repeated `app_mention`, and as a `message` event for the thread parent whose `thread_ts` equals its own `ts`. Since `sticky: slack`, a thread mention also arrives with its root, so the root can reach admission a second time that way. With ambient listening on, `message` events create work too, and Slack sends a `message` copy of every mention, before or after its `app_mention`. One Slack message, mention or not, must start at most one turn, and so must each edit of it, and a redelivery must not disturb work that is already waiting or running in the Channel Session or Thread Session.

## Symptoms

Three failures. The first two are in code that no longer exists:

- **Redelivered root mention cleared buffered follow-ups (historical).** The connector kept an in-memory per-thread stack for mid-turn messages (Buffered Follow-Ups, later Slack Steers). Each root delivery reset that stack, so a redelivery during the first turn dropped a follow-up the user had already sent.
- **Redelivered parent message enqueued a second turn (historical).** The connector's plain-message path treated any message with `thread_ts` set as a thread reply. The parent redelivery arrived mid-turn, became an Enqueued Slack Message on the Thread Queue, and ran as a second turn for the same text after the first finished.
- **A skipped Slack session turn lost its admission record (found in code review of the `sticky: slack` work and fixed there).** A Channel Session or Thread Session turn whose agent is no longer `sticky: slack` is skipped when it starts. The first version ran `DELETE FROM turns WHERE id = $1` on it. That row was the only record that the Slack message had been admitted, so a Slack retry, or the same root delivered again with a thread mention, passed the duplicate check and was admitted again.

## What Didn't Work

The first three points are history. The connector code they name has been removed.

- **Connector-level patches, one per event shape.** The first fix made the stack initialization idempotent: create a missing key, never overwrite an active one. The second fix swallowed a mid-turn message whose `ts` equalled its `thread_ts`. Each patch closed one redelivery shape and missed the other. The tests for each sent only their own shape, so neither caught the other bug.
- **Dropping every parent message in the reply path.** This was too broad at the time, because an idle parent message still started commands and first turns. The ambient `message` handler now does drop every parent message that is not an edit: a new top-level message arrives without `thread_ts`, so the check removes only parent copies.
- **In-memory dedup state in the connector.** The stack map was process-local. A restart, or a redelivery that arrived after the turn finished, could not be recognized from it.
- **Deleting the row of a turn that will not run.** It reads like cleanup: the turn never ran, so nothing seems lost. But the row is not a separate queue entry that the running turn replaced. Starting a queued item turns that same row into the running turn, so deleting a claimed turn deletes the admission record too.

## Solution

Two Slack events can create work, a few connector gates drop the shapes that are never new work, and duplicates are refused at one durable point.

1. The connector's `handleEventsAPI` (`internal/rocketclaw/frontend/slack/connector.go`) creates work only from `AppMentionEvent` and, with ambient listening on (`slack.ambient_decision_model` set), from `MessageEvent` through `handleMessageEvent`. Its other cases are channel renames, `AgentSessionStoppedEvent` and `AgentSessionTitleChangedEvent`. Before any lookup, `ambientCandidate` (same file) drops a `message` event that is not an edit when its `thread_ts` equals its `ts` (the parent redelivery) or its subtype is not plain, `file_share` or `thread_broadcast`, and drops any `message` whose text mentions the bot as `<@BOT>` or `<@BOT|name>`, because the mention path keeps it. An edit (`message_changed` with `edited`) skips the parent check and the screen, and is admitted only in a thread that already has its session. The full gate is in the "Ambient listening" section of `docs/verification/slack-sticky/README.md`.
2. `handleAppMentionEvent` (same file) builds the inbound message and calls `threadRouter.SubmitMention`. Before submitting, it sets the thread's agent session to `processing`, but only when no turn of the thread is running; a redelivery during a turn leaves the status alone (see [the agent session status learning](../architecture-patterns/slack-agent-session-status-follows-the-running-turn.md)). A refused redelivery returns `false, nil`, so it gets no failure reply, and `settleSession` sets the thread back to `active` unless one of its turns is running. `handleMessageEvent` sets the ambient metadata key and calls the same `SubmitMention`; it writes no status and posts no failure reply, and it ignores a refusal.
3. `threadBridgeManager.SubmitMention` (`internal/rocketclaw/backend/thread_bridges.go`) makes the Thread Queue item ID `"slack:" + channel + ":" + message ts`, with `":" + edit ts` appended for an edit (`SlackReplyTarget.EditTS`), so each edit is admitted once and a redelivered edit is refused. A thread mention that carries its root admits the root first. An ambient message always goes to the Thread Session of its thread, or of its own `ts` for a top-level message, never to a Channel Session. Inside one State Store transaction, holding the history locks of every session it may write, it checks each ID in two places across all conversations of the Slack channel, whatever their agent: any model turn row (`turns` with `takes_lane`, whether waiting, running or finished, matched on `input_id` or the inbound `web_message_id`) and saved history from the last 24 hours (`entries` whose `replay_input` carries that `input_id`). A message it finds is skipped and nothing is written for it.
4. The turn row stays the admission record for the turn's whole life. Starting a queued item does not add a running row next to the waiting one: `bridgeRequest.claimQueueItem` (`internal/rocketclaw/backend/bridge.go`) runs `UPDATE turns SET id = <turn ID>, phase = 'running'` on the waiting row itself, and a finished turn keeps its row in phase `done`. So a path that decides, after the claim, not to run the turn must close the row, never delete it. `Bridge.skipUnstickyTurn` (same file) sets the skipped turn to phase `done` with no terminal, and in the same statement gives back the pending Completion Notes it had claimed, with `wake = FALSE`, so the next turn there takes them without a second wake.

Tests that pin this behavior:

- `TestHandleAppMentionEventRedeliveryStartsOnce` (`internal/rocketclaw/frontend/slack/connector_test.go`), with ambient listening on, sends the root mention, its parent `message`, the thread mention's `message` copy and the thread mention, twice each. It expects four `SubmitMention` calls (the two `message` events never reach the router), and each refused redelivery sets the idle thread back to `active`.
- `TestHandleEventsAPIIgnoresMessagesAndReactions` (same file) shows, with ambient listening on, that a parent redelivery in a Watched Channel, a `message` that mentions the bot plainly or by alias, an unsupported subtype, a `message_changed` without an edit, a reaction and an edited mention cause no screen, no submission and no status.
- `TestThreadBridgeManagerSubmitMentionQueuesEachMentionOnce` (`internal/rocketclaw/backend/thread_bridges_test.go`) refuses a redelivery in each state: while the mention waits, mid-turn, after a restart, and after its turn has finished. It also checks that a mention never steers the running turn.
- `TestSubmitMentionAdmitsEachSlackMessageOnceAcrossAgents` and `TestSubmitMentionAdmitsItsRootFirst` (same file) check that the duplicate check covers the whole channel and every agent, and that a root admitted with its thread mention is refused when Slack later delivers it on its own.
- `TestSubmitMentionAdmitsAmbientMessagesOnceToTheirThread` (same file) refuses a mention after its ambient copy and an ambient copy after its mention, admits an edit once under its own ID, and queues ambient messages in their thread's Thread Session.
- `TestSlackSessionTurnOfAgentNoLongerStickySlackDoesNotRun` (`internal/rocketclaw/backend/active_turn_test.go`) skips a turn, then expects a Slack retry of the same message and the same root sent with a thread mention not to be admitted again, and the skipped turn's claimed note to be left pending for the next turn.

## Why This Works

Both historical bugs had the same cause: the connector decided whether an event was new work from its own in-memory state, and each event shape needed its own rule. Now the decision is made where the work is stored, and both events that can create work reach it with the same ID for the same Slack message. The ID comes from Slack's message `ts`, which stays the same across redeliveries and across the `app_mention` and `message` copies of one message. Because the check and the insert happen in the same transaction, two concurrent deliveries cannot both pass. The check also covers waiting, running, finished and saved work, so a redelivery after a restart or after the answer is refused too. A refused redelivery writes nothing, so queued and running work is left as it was.

The check only works while every admitted message keeps a row or a saved entry. The skipped-turn bug was a hole on the other side of that rule: the check was right, but one code path removed what it looks for.

## Prevention

- Dedupe Slack redeliveries at the durable boundary, keyed by Slack's message `ts`. Never decide whether an event is new work from in-memory connector state such as maps, stacks or "turn active" flags. In-memory state may decide only what Slack shows, such as the agent session status.
- Any new Slack entry point that can create work must go through `SubmitMention` and its `slack:<channel>:<message ts>` ID, or `slack:<channel>:<message ts>:<edit ts>` for an edit. Do not write to the Thread Queue directly from the connector.
- Do not take "`thread_ts` is set" to mean "this is a reply". Slack also sets `thread_ts` on the thread parent. A reply is `thread_ts != "" && thread_ts != ts`.
- Do not add Slack event types to the switch in `handleEventsAPI` without deciding how their redeliveries are deduped. The ambient `message` handler is the one plain-message path; keep its parent-redelivery and subtype gates.
- Keep dropping `message` events that mention the bot. The duplicate check admits whichever copy of a message arrives first and refuses the other, so the first copy decides what kind of turn runs. A mention admitted from its `message` copy would run as an Ambient Turn: unaddressed, possibly silent, and with no status or Stop. The model's `ambient-mentions` check, which treats mention text as plain, must fail on `MentionNeverAmbient`.
- Any change to the admission SQL must keep the check and the insert in one transaction, and must keep both lookups: turn rows and saved history. Do not shorten the saved-history window below how long Slack keeps redelivering (the `short-window` check in `docs/verification/slack-sticky/` must fail).
- Never `DELETE` the turn row of an admitted Slack message, whether it waits, was claimed, or was skipped. A turn that will not run is closed as phase `done`, and anything it had claimed, such as Completion Notes, is given back in the same transaction. Before adding a new reason to drop a turn, check what reads that row afterwards.
- Redelivery tests must send every shape Slack uses (repeated `app_mention`, parent `message` with `ts == thread_ts`, the `message` copy of a mention, threaded reply, repeated edit). Run them with ambient listening on, since that is when `message` events reach the router. Assert both the number of accepted submissions and the visible receipts.

## Related Issues

- [Saved queue never started after restart](saved-queue-never-started-after-restart.md): the same Thread Queue rows after a restart.
- [A Slack mention thread's agent session status follows its running turn](../architecture-patterns/slack-agent-session-status-follows-the-running-turn.md): the same mention entry point, for the status Slack shows rather than the work admitted.
- Historical fixes, now removed: [PR #1](https://github.com/Rocketable/platform/pull/1) (idempotent stack initialization) and [PR #16](https://github.com/Rocketable/platform/pull/16) (mid-turn parent message swallowed).
- Plan `docs/plans/2026-11-18-2322-feat-sticky-slack-sessions-plan.md` and model `docs/verification/slack-sticky/`: channel-wide admission for `sticky: slack` (the `per-conv-dedup` check must fail).
- Plan `docs/plans/2026-11-24-1520-feat-slack-ambient-listening-plan.md` and the ambient extension of the same model: `message` intake and one admission per message, whichever event carried it (`OneAdmission`).
- `CONCEPTS.md`: Thread Queue, Managed Slack Thread, Channel Session, Thread Session, Root Slack Mention, Joined Thread, Watched Channel and Ambient Turn. Buffered Follow-Up and Slack Steer are listed there as retired.
