---
title: Every Path That Delivers a Prompt to a Subagent Must Run Its Guardrails
date: 2026-10-13
category: docs/solutions/logic-errors/
module: internal/rocketcode
problem_type: logic_error
component: assistant
symptoms:
  - "A task continue sent while the subagent was busy (steer, enqueue, or stop) reached a guardrailed subagent without the delegation review."
  - "The answer of a queued follow-up reached the parent without the response review or the <task_result> wrapping."
  - "The same continue was reviewed or not depending only on whether the subagent happened to be running."
root_cause: logic_error
resolution_type: code_fix
severity: high
related_components:
  - internal/rocketclaw/backend
tags:
  - guardrail
  - task
  - subagent
  - background-job
  - continue
  - steer
---

# Every Path That Delivers a Prompt to a Subagent Must Run Its Guardrails

## Problem
An agent type with a `guardrail` is promised that every delegation prompt is reviewed before it runs and every answer is reviewed before the parent sees it. Those checks lived only inside `runTask` (`internal/rocketcode/tasks.go`). When `task`'s `continue` gained steer, enqueue, and stop modes for a busy subagent, the new paths delivered prompts without ever calling `runTask`, so the guardrail silently stopped applying.

## Symptoms
- A parent could get an unreviewed prompt into a guardrailed subagent just by continuing it while it ran.
- A steer was injected straight into the running turn through the Completion Note drain (`backgroundTurn.DrainNotes`) with no review.
- A queued follow-up ran through a separate continue path, which returned the raw answer with no response review.

## What Didn't Work
- Relying on `runTask` as the single choke point. Before this change a busy continue was refused, so `runTask` really was the only delivery path. The new modes made the Background Job registry (`internal/rocketclaw/backend/background.go`, `backgroundTurn.Run` → `queueFollowUp`) a second delivery path that returns before the wrapped tool call runs.

## Solution
- The delegation and response reviews share `toolFactory.guardTask` (`internal/rocketcode/tasks.go`). A new subagent's prompt is reviewed in `runTask`, and a continue's prompt in `backgroundTool` (`internal/rocketcode/background.go`), each right before `ChildSessions.WriteSubagent` delivers it into the subagent's session. A rejected prompt is never queued or steered, and the caller gets the usual `delegation blocked` result.
- Every answer leaves through `toolFactory.awaitTask`, which waits for the subagent session's turn (`ChildSessions.AwaitSubagent`) and runs the response review and the `<task_result>` wrapping. A background job resumed after a restart answers through `Runtime.AwaitTask`, the same code; the job's `turns` row keeps the prompt (`turns.message`) the review quotes.

## Why This Works
The guardrail contract is about prompts reaching a subagent and answers leaving it. There are exactly two places where either happens, `WriteSubagent` and `AwaitSubagent`, and every task path goes through them, whether the subagent is idle, running, or busy with queued work.

## Prevention
- When adding any new way to deliver text to a subagent, trace it to a delegation-stage `guardTask` call before `WriteSubagent` and to `awaitTask` before the parent sees the answer.
- Test a guarded subagent type through each delivery mode (`TestTaskContinueGuardsDelivery`, `TestTaskWritesSubagentSession`).
- A continue's named `subagent_type` wins (OpenCode `packages/core/src/tool/plugin/subagent.ts` precedent): an unknown type is refused, the caller needs `task` permission for that type, and that type's guardrails review the prompt and the answer. The subagent session switches to it; a steer into a busy subagent carries the type on its `PromptInput.Agent`, and `appendSteers` switches the turn record from its next model step (`looper.asRecordAgent` rebuilds the looper as that type, for any session), while the step in progress keeps the old type. The answer is reviewed by the guardrail of the type the turn ended as, which `AwaitSubagent` reads from the saved turn (`TestSteerSwitchedSubagentAnswerGuardedByFinalType`). When the new type runs on another provider, the turn's items and the history before them are converted with `SessionEntry.forProvider`, the conversion `loadSession` and `openTurn` apply (`TestSteerSwitchConvertsTurnForProvider`).

## Related Issues
- Plan: `docs/plans/2026-10-13-1309-feat-agent-session-control-plan.md` (KTD13–KTD16, superseded by REV5).
- `docs/solutions/logic-errors/saved-queue-never-started-after-restart.md` (durable later work).
