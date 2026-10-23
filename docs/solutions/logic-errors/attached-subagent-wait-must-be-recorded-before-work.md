---
title: A Resumed Task Call Must Find Its Delivered Prompt, Not Hold Its Subagent
date: 2026-10-14
category: docs/solutions/logic-errors/
module: internal/rocketclaw/backend
problem_type: logic_error
component: assistant
symptoms:
  - "After a restart, a queued follow-up or a Completion Note wake ran a subagent before the parent's resumed foreground task call replayed."
  - "A steer joined into a foreground (attached) subagent run could be delivered twice: once as a separate follow-up and again when the resumed turn re-offered it."
  - "Follow-ups queued behind an attached run could run ahead of the run they were queued behind."
root_cause: missing_workflow_step
resolution_type: code_fix
severity: high
related_components:
  - internal/rocketcode
tags:
  - background-job
  - subagent
  - subagent-session
  - steer
  - follow-up
  - startup-recovery
  - exactly-once
---

# A Resumed Task Call Must Find Its Delivered Prompt, Not Hold Its Subagent

## Problem
A parent that calls `task` in the foreground waits on its subagent. When subagents ran inside the parent's call, with their own follow-up rows, steer claims, and wakes in the Background Job registry, only memory knew that the subagent was busy for that call. After a restart, recovery saw an idle subagent and started its queued follow-ups or woke it before the parent's resumed turn replayed its `task` call. The replayed call then ran the subagent again, so a prompt, steer, or follow-up could be delivered twice or out of order.

## What Didn't Work
- **Refusing steers into attached runs.** It broke the "steer is steer" contract: a steer to a busy subagent must join its running turn whether or not the parent waits in the foreground.
- **A "hold" marker written at shutdown.** It covers graceful shutdown only; a hard crash writes nothing.
- **An attached-wait row written before the work starts** (`claimed_turn_id = job_id`, checked by every start path). It worked, but it was a second copy of what the session turn runner already does for top-level sessions, and every new start path had to remember to check it.

## Solution
A subagent is a session (`conversations.parent_conversation_id`), run by its own bridge on the same turn runner as top-level sessions. A `task` call delivers its prompt the way `rocketclaw_sessions_write` does (`Bridge.WriteSubagent` → `threadBridgeManager.writeSession`): one Thread Queue row whose ID is the parent conversation plus the call's tool call key, or a steer into the running turn. Then it waits for the turn that runs that input (`backgroundRegistry.awaitSubagent`), reading the State Store: the saved entry whose replay input carries the input ID, a stopped or failed turn row, or a row still queued or running.

- The delivery is idempotent: `reconcileWebDB` finds the input ID in the queue, an active turn, or saved history, so a replayed call delivers nothing (`errWriteDelivered`) and waits for the same turn.
- A replayed call whose subagent turn already saved answers from that saved entry without a model call.
- The subagent's own queue, steers, wakes, and restart recovery are the session's, so they keep the order and exactly-once guarantees every session has; there is no subagent-only start path to guard.
- Waiters wake when any turn finishes (`finishTurn` → `conversationJobs.turnFinished`).

## Why This Works
The prompt is written ahead of any work it starts, under a key the replaying call can compute, and the turn runner already serializes, recovers, and saves each session's turns exactly once. The parent never owns the subagent's lifecycle, so there is nothing to hold.

## Prevention
- Deliver into a subagent only through its session (`WriteSubagent`), never by starting its turn directly.
- Restart tests must cover graceful shutdown and hard crash: `TestRestartResumesBackgroundSubagent`, and `TestTaskRunsSubagentAsSession` for replayed writes and waits.

## Related Issues
- Plan: `docs/plans/2026-10-13-1309-feat-agent-session-control-plan.md`, Review Revisions REV1a and REV5.
- `docs/solutions/logic-errors/saved-queue-never-started-after-restart.md` (durable queued work must start after restart).
- `docs/solutions/logic-errors/subagent-delivery-paths-must-run-guardrails.md` (every subagent delivery path must run its guardrails).
