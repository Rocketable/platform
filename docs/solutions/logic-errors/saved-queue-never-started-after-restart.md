---
title: Saved Queue Rows Never Started After Restart
date: 2026-09-20
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

Deleting the old backlog and waiting for new activity would drop saved work. A separate replay path would duplicate ordinary processing and risk competing with live workers. Blocking later work on leftover unfinished-turn records would strand later messages behind a failed live turn that still had an `active_turns` row.

## Solution

Keep queued conversations through retention, then discover distinct `thread_queue.conversation_id` values once after frontend attachment and recoveries are submitted. Each recorded conversation reuses the existing worker and later-work rules. Pending work is blocked only for recoveries selected at this startup, not for every leftover checkpoint.

```1598:1602:internal/rocketclaw/backend/store.go
func shouldPruneThreadConversation(ctx context.Context, db stateStoreDB, conversationID string, cutoff time.Time) (bool, error) {
	queued, err := conversationExists(ctx, db, `thread_queue`, `conversation_id`, conversationID)
	if err != nil || queued {
		return false, err
	}
```

```430:442:internal/rocketclaw/backend/thread_bridges.go
func (m *threadBridgeManager) StartQueuedConversations() error {
	conversationIDs, err := m.store.queuedConversationIDs(context.Background())
	if err != nil {
		return fmt.Errorf("load queued conversations: %w", err)
	}
	for _, conversationID := range conversationIDs {
		if err := m.PickLaterWork(context.Background(), conversationID); err != nil {
			return err
		}
	}

	return nil
}
```

Private External MCP recovery runs on the destination worker with the private conversation as producer. After that recovery finishes or is abandoned, both sides pick later work. Schedule deletion and park-link clearing share one transaction so a failed delete cannot unpark waiting rows.

The code change is pending in [PR #68](https://github.com/Rocketable/platform/pull/68). Production cleanup of Wallace's six historical rows remains operator work and is not part of the fix.

## Why This Works

The leftover was an unclaimed Thread Queue row after save-before-start, not a missing checkpoint. Retention had to keep that row. Startup had to find conversations that had no other trigger. Ordinary later-work selection already knows goal, schedule, and park order, so discovery only needs to wake each conversation once. Blocking only selected recoveries lets a failed live turn still release later messages.

## Prevention

Keep a real-store startup test that seeds queued rows with no other work, asserts they complete after `Run`, and asserts a second start does not replay them (`TestRunStartsPersistedQueueWithoutOtherWork`).

## Related Issues

- [PR #68](https://github.com/Rocketable/platform/pull/68) (pending): recover saved queues on startup
- Investigation: `internal/rocketclaw/docs/investigations/2026-09-20-wallace-stranded-mcp-queue.md`
