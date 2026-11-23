---
title: A Migration That Changes What a Staged Undo Marker Means Must Settle What the Old Code Settled Later
date: 2026-11-23
category: docs/solutions/logic-errors/
module: internal/rocketclaw/backend
problem_type: logic_error
component: assistant
related_components:
  - database
symptoms:
  - "After migration 108, a chat whose staged Undo marker had a cron, External MCP or Slack message after it kept the marker, so that outside message sat on a side branch where branch delete could remove it."
  - "A migrated `X:0` marker whose entry X the chat does not link (a shadow's `anchor+1:0`, or an entry another conversation owns) resolved to an empty path."
  - "The migration test passed: it covered only a staged marker with nothing outside after it."
root_cause: missing_workflow_step
resolution_type: migration
severity: high
tags:
  - message-tree
  - migration
  - legacy-state
  - revert-marker
  - outside-message-floor
  - history-links
  - data-migration
---

# A Migration That Changes What a Staged Undo Marker Means Must Settle What the Old Code Settled Later

## Problem

Before the Message Tree, Undo only staged a marker in `conversations.revert_message_id`. That marker was a pending state, and the next admission settled it: it deleted the hidden suffix, or cancelled the marker. The Message Tree reuses the column as the Current Point, which now lasts. Plan KTD13 (`docs/plans/2026-11-23-0931-feat-message-tree-plan.md`) said "A staged marker stays as that chat's current point, so its hidden suffix becomes a branch." That kept every legacy marker as it was, including markers the old code would never have kept. The migrated rows then broke the new design's invariants from the first moment.

## Symptoms

- **Outside message after the marker.** A chat whose marker had a cron report, External MCP report or Slack message after it came out of the migration with that message off the current path. The design depends on outside messages always being on the current path (the outside-message floor). Branch delete leans on that to need no outside-message check of its own. So the migrated message could be deleted with a branch.
- **`X:0` on an entry the chat does not link.** The old read cut by ID: `X:0` meant "entries with `id < X`", whether or not the chat showed X (`sessionHistorySQL` on main, `e.id < split_part(c.marker, ':', 1)::bigint`). Shadow children store `anchor+1:0`, and that entry can belong to another conversation or not exist. The new read resolves `X:0` through X's own link in this chat (`leaf` in `internal/rocketclaw/backend/revert.go:53-56`). With no link for X, the path was empty.
- `TestHistoryLinksMigration` passed throughout, because its only staged marker had nothing outside after it.

## What Didn't Work

- **"Keep the marker, the suffix becomes a branch."** This is correct for a marker the old code would have committed. It is wrong for one it would have cancelled. The old `settleRevertDB` (`internal/rocketclaw/backend/revert.go` on main) read the marker and the floor. When an outside entry had arrived after the marker (`!afterRevertFloor(marker, floor)`), it cleared the marker for the chat and its shadow children, so the outside message stayed in the history. The plan carried the column's text format over and assumed the meaning came with it. But the old meaning included "resolved later by settle", and the migration dropped that step.
- **Trusting the TLC model.** The model in `docs/verification/message-tree/` passed, but it starts from an empty chat with no staged marker (`Init` in `MessageTree.tla`), so it never reaches a migrated row.

Review found both cases as P1s: deployment D1, which the adversarial reviewer also raised, and deployment D2.

## Solution

On the `message-tree` branch, in "internal/rocketclaw: fix history loss and races found in review" (not merged as of this writing), migration 108 settles legacy markers before the new code runs:

1. **Cancel what the old settle would have cancelled** (`internal/rocketclaw/backend/migrations/108_history_links.sql:22-35`). For each staged marker on a non-shadow chat, it looks at every linked item after the marker. It clears the marker on the chat and its shadow children (`OR c.shadow_parent = x.conversation_id`) when one of those items came from outside:
   - an entry owned by another conversation (a report), compaction items aside;
   - a user item whose `prompt_header` is not `[Web …]` or `[System …]`;
   - a user item with no header in a `slack_thread` chat.
2. **Rewrite `X:0` markers the chat does not link** (`108_history_links.sql:37-50`). The marker moves to the end of the greatest linked entry below X, which keeps what `id < X` showed. With no such entry, it moves to the first linked entry at length 0.
3. `TestHistoryLinksMigration` (`internal/rocketclaw/backend/store_schema_test.go:613`) adds one chat per legacy state. Its assertion messages state the rules: "a report after the marker cancels it, as the old settle did", "an outside message after the marker cancels it", and "a marker on an entry the chat does not show keeps the entries before it" (`store_schema_test.go:688-690`). The test also checks each chat's resulting marker.

## Why This Works

A staged marker was half of a two-step operation, and settle was the second half. A migration that turns the first half into lasting state has to finish the second half itself, for every row, the way the old code would have. Otherwise rows land in states the new code never creates and its invariants rule out. After the migration, every legacy marker is in a state the new design could have reached: committed markers become branches, cancelled markers are cleared, and ID-cut markers point at a linked entry.

## Prevention

- When a migration changes what a stored value means, list every pending state the old code left for later: settle, commit, cancel, prune, retry. For each one, write what the old code would eventually have done. The migration either does that or maps the row to a state the new invariants allow.
- Also list the readers whose meaning changes, not only the writers. Here the reader changed from an ID cut to a link walk, and that change alone broke `X:0` for unlinked entries.
- Check each migrated state against the new design's invariants, not just against "reads the same as before". The outside-message floor was the invariant the migrated rows broke.
- Give the migration test one fixture per legacy state, including the states the old code would have resolved differently, and assert the resulting stored state as well as what the chat reads.
- If a formal model gates the feature, make its starting states include these migrated rows, or state that the migration rules them out.

## Related Issues

- `docs/solutions/logic-errors/history-links-must-not-cascade-from-entries-another-conversation-owns.md`: the other P1 data finding in the same review of migration 108.
- `docs/solutions/runtime-errors/deployment-binary-missing-live-migrations.md`: migration 108 is also a one-way door for rollback.
- Plan `docs/plans/2026-11-23-0931-feat-message-tree-plan.md`, KTD2 (the marker becomes the Current Point), KTD4 (outside arrivals and the removed settle) and KTD13 (the migration).
- `CONCEPTS.md`: Current Point, Message Tree.
