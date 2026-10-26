---
title: Saved Queue Rows Never Started After Restart
date: 2026-09-20
last_updated: 2026-10-22
category: docs/solutions/logic-errors/
module: internal/rocketclaw/backend
problem_type: logic_error
component: assistant
symptoms:
  - "A queued message saved before process stop stayed in the Thread Queue after restart."
  - "Nothing started that work until another incoming message, checkpoint, goal, or schedule woke the conversation."
  - "Startup cleanup could drop a historyless conversation that still had queued rows."
root_cause: missing_workflow_step
resolution_type: code_fix
severity: high
tags:
  - thread-queue
  - startup-recovery
  - enqueued-slack-message
  - later-work
  - prune
---

# Saved Queue Rows Never Started After Restart

## Problem

RocketClaw could save a waiting message and then stop before that conversation's worker claimed it. After restart the row was still in the Thread Queue, but nothing woke the worker. A person had to send another message, or some other startup path had to run, before the saved work started.

## Symptoms

- A conversation with only saved queue rows stayed idle after restart.
- Wallace production had six historical queued rows and no active checkpoints; they did not start on their own.
- A historyless recorded conversation with a queue row could be pruned at startup, so the payload disappeared with the conversation record.

## What Didn't Work

Deleting the old backlog and waiting for new activity would drop saved work. A separate replay path would duplicate ordinary processing and risk competing with live workers. Blocking later work on leftover unfinished-turn records would strand later messages behind a failed live turn that still had an unfinished-turn row (then `active_turns`).

## Solution

Keep queued conversations through retention, then discover the distinct conversations that have waiting `turns` rows other than scheduled messages (formerly `thread_queue`) once at startup, after Slack is attached and conversations with an unfinished turn have been started. Each recorded conversation reuses the existing worker and later-work rules.

Since 2026-10-05 (step-journal change) there is no separate recovery phase. An interrupted turn is a lane-taking `turns` row that is neither `waiting` nor `done`; each worker runs its oldest such row, its own or that of a hidden run that reports to it, before anything else (`headTurn`), and `StartActiveTurns` wakes those workers before `StartQueuedConversations`. A turn that failed or was stopped is kept with phase `done` for the transcript, so it never holds back later queue rows.

```1870:1873:internal/rocketclaw/backend/store.go
	pending, err := conversationHasPendingWork(ctx, db, conversationID)
	if err != nil || pending {
		return false, err
	}
```

`conversationHasPendingWork` counts, among other work, `SELECT 1 FROM turns WHERE conversation_id = $1 AND phase = 'waiting'`.

```422:435:internal/rocketclaw/backend/thread_bridges.go
func (m *threadBridgeManager) StartQueuedConversations(ctx context.Context) error {
	conversationIDs, err := queryStrings(ctx, m.store.db, `SELECT DISTINCT conversation_id FROM turns WHERE phase = 'waiting' AND reason <> $1 ORDER BY conversation_id`, "queued conversation IDs", waitScheduled)
	if err != nil {
		return fmt.Errorf("load queued conversations: %w", err)
	}

	for _, conversationID := range conversationIDs {
		if err := m.PickLaterWork(ctx, conversationID); err != nil {
			return err
		}
	}

	return nil
}
```

A private External MCP turn's row runs on its destination worker (the chat its conversation reports to, `conversations.reports_to`, recorded from `SyncDestination` when the turn starts); after it finishes, both sides pick later work. Schedule deletion and park-link clearing share one transaction so a failed delete cannot unpark waiting rows.

The code change landed in [PR #68](https://github.com/Rocketable/platform/pull/68). Production cleanup of Wallace's six historical rows remains operator work and is not part of the fix.

## Why This Works

The leftover was an unclaimed Thread Queue row after save-before-start, not a missing checkpoint. Retention had to keep that row. Startup had to find conversations that had no other trigger. Ordinary later-work selection already knows goal, schedule, and park order, so discovery only needs to wake each conversation once. Treating only rows that are not `done` as the queue head lets a failed live turn still release later messages.

## Prevention

Keep a real-store startup test that seeds queued rows with no other work, asserts they complete after `Run`, and asserts a second start does not replay them (`TestRunStartsPersistedQueueWithoutOtherWork`).

## Related Issues

- [PR #68](https://github.com/Rocketable/platform/pull/68): recover saved queues on startup
- Investigation: `docs/investigations/2026-09-20-wallace-stranded-mcp-queue.md`
