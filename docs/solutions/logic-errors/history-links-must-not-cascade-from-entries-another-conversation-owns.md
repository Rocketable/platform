---
title: A Chat's History Links Must Not Cascade From Entries Another Conversation Owns
date: 2026-11-23
category: docs/solutions/logic-errors/
module: internal/rocketclaw/backend
problem_type: logic_error
component: assistant
related_components:
  - database
symptoms:
  - "Deleting a run that reports into a chat cut the chat's history: the messages before the run's report dropped off the chat's path, so the model and the transcript no longer saw them."
  - "Nothing failed or logged; a foreign key cascade removed the chat's own link row along with the run's entry."
root_cause: data_integrity
resolution_type: code_fix
severity: high
tags:
  - message-tree
  - history-links
  - foreign-key
  - on-delete-cascade
  - reports-to
  - owns-vs-shows
  - data-loss
---

# A Chat's History Links Must Not Cascade From Entries Another Conversation Owns

## Problem

The Message Tree stores each chat's path as rows in `history_links`, keyed by the viewing conversation. A chat also shows entries it does not own: the runs that report to it and the run it continues. Migration 108 first declared `entry_id bigint NOT NULL REFERENCES entries (id) ON DELETE CASCADE`. So when the owner of a shown entry deleted it, the cascade also deleted the viewing chat's link row. That row was the one holding the step back to the chat's earlier messages, so the chat's path was cut there.

## Symptoms

- A chat shows `first`, a run's `report`, then `second`. After the run is deleted, the chat's path reads only `second`. Everything before the report is gone from the model replay and the transcript, though those entries are still stored, and the tree no longer shows one line through the report.
- Nothing errors. The walk back from the Current Point just ends when the next link row is missing.
- The cascade ran under every entry deleter, not just `DeleteSession`: the sticky-message prune and `deleteAfterRevertFloorDB` relied on it too. Each of them now removes its own links explicitly.

## What Didn't Work

- **Planning the `parent_entry_id` side only.** Plan KTD1 (`docs/plans/2026-11-23-0931-feat-message-tree-plan.md`) gave `parent_entry_id` no foreign key on purpose: a link whose parent was pruned turns that entry into a root. But `entry_id` kept `ON DELETE CASCADE`, on the belief that a link should die with its entry. That holds only when the link's viewer also owns the entry. A link row also carries the step back (`parent_entry_id`) for every entry before it. So cascading a cross-owner link cuts the viewer's path, not just one message.
- **Model checking.** The PlusCal model (`docs/verification/message-tree/`) has one chat and no reporting runs, and its README lists "every reader of history" as out of scope. So no check could reach this. The correctness and data-migration reviewers found it (review of the message-tree branch, both P1).

## Solution

Fixed on the `message-tree` branch in "internal/rocketclaw: fix history loss and races found in review" (not merged as of this writing):

1. **No foreign key from links to entries.** `internal/rocketclaw/backend/migrations/108_history_links.sql:4` now reads `entry_id bigint NOT NULL`. The link still cascades from its viewing conversation (`:3`), so deleting a chat removes its links.
2. **Each deleter removes only its own conversation's links, on purpose, in the same statement:**
   - `deleteAfterRevertFloorDB`: `internal/rocketclaw/backend/store.go:786-787`
   - `DeleteSession`: `store.go:888-889`, which matches `e.conversation_id = l.conversation_id`, so a run's own links go and the chats that show it keep theirs
   - sticky prune: `store.go:1468-1469`
   - branch delete, plus the shadow comparisons it removes: `internal/rocketclaw/backend/branches.go:212-213` and `:223`
3. **Readers walk links and hide missing entries.** `current_path` follows `history_links` only (`internal/rocketclaw/backend/revert.go:57-60`). Entries are joined after the walk (`effective_entries`, `revert.go:61-64`), so a deleted entry drops out of the path and the walk still goes through it. The tree and branch reads start from the links and `LEFT JOIN` the entries (`internal/rocketclaw/backend/tree.go:33`, `branches.go:19`).

```sql
-- before: one owner's delete cut every viewer's path
entry_id bigint NOT NULL REFERENCES entries (id) ON DELETE CASCADE,
-- after
entry_id bigint NOT NULL,
```

`TestDeletedRunEntriesKeepTheChatPath` (`internal/rocketclaw/backend/history_links_test.go:58`) deletes a run that reports into a chat. It checks that the run's own links are gone, that the chat still reads `first`, `one`, `second`, `three`, and that the tree keeps one unbroken line through the deleted report.

## Why This Works

An entry has one owner, but a link row belongs to the conversation that views the entry, and it holds that viewer's step back to the entry before. Once owner and viewer differ, "delete the entry" and "delete this viewer's links" are separate decisions. Only the owner's delete path knows which links are its own. A cascade can't tell them apart. With no cascade, an owner's delete leaves a viewer's link row behind, still pointing at the earlier entry. The walk passes through it, and the entry join hides the missing message. The viewer's links go when the viewer itself is deleted, through the conversation foreign key.

## Prevention

- In a link or edge table keyed by a viewer, never cascade on the row being pointed at when the walk can cross owners. Cascade only on the viewer.
- Every new code path that deletes `entries` must also delete its own conversation's `history_links` rows in the same statement (`l.conversation_id` = the owner, joined on the owner's entries), and leave other viewers' links alone. The list in Solution step 2 is the current set to copy from.
- Readers of a linked path follow the links first and join the referenced rows last, so a missing row hides one message instead of cutting off everything behind it.
- When a feature makes one conversation show rows another owns, check every delete as well as every read. The "owns vs shows" split in `docs/solutions/logic-errors/external-mcp-thread-lost-metadata-after-copy-free-hidden-runs.md` applies to delete paths too: an owner's delete must not reach into a viewer's own rows.
- Test cross-owner deletes the way production does them. Delete the owner of an entry in the middle of the path through the real delete path, then read the viewer's whole path, as `TestDeletedRunEntriesKeepTheChatPath` does.

## Related Issues

- `docs/solutions/logic-errors/external-mcp-thread-lost-metadata-after-copy-free-hidden-runs.md`: the same owns/shows split, on the read side.
- Plan `docs/plans/2026-11-23-0931-feat-message-tree-plan.md`, KTD1 (per-chat links) and KTD13 (migration 108).
- `CONCEPTS.md`: Message Tree, Current Point, Report Thread.
