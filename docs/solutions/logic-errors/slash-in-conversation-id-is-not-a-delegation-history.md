---
title: A Slash in a Conversation ID Does Not Mark a Delegation History
date: 2026-10-08
category: docs/solutions/logic-errors/
module: internal/rocketclaw/backend
problem_type: logic_error
component: assistant
symptoms:
  - "Web chats continued from a cron run (IDs like web:cron:cron/daily.md:...) never matched message search."
  - "Those chats were still marked indexed, so the search did not report them as incomplete and backfill skipped them."
root_cause: logic_error
resolution_type: code_fix
severity: high
related_components:
  - internal/rocketclaw/frontend/rpc
tags:
  - delegation-history
  - conversation-id
  - message-search
  - cron
  - visibility
---

# A Slash in a Conversation ID Does Not Mark a Delegation History

## Problem

The first version of indexed message search skipped indexing any conversation whose ID contained `/`. The goal was to keep Delegation Histories out of search. But some chats that Web shows also have a `/` in their ID, so they became unsearchable without any error.

## Symptoms

- A Web chat continued from a cron run returned no message search hits, even for text it plainly contained.
- The chat was still recorded as indexed in `message_search_indexed`. So `SearchMessages` reported the index as complete, and the backfill never came back to it. Nothing showed that results were missing.

## What Didn't Work

- **Using ID shape as the test.** `CONCEPTS.md` describes a Delegation History ID as the producing conversation's ID plus `/<tool call ID>-...`. That makes "contains `/`" look like a safe marker. It holds in only one direction: every Delegation History ID has a `/`, but not every ID with a `/` is a Delegation History.

## Solution

Cron conversation IDs carry the job's relative path, which always has a `/` (`internal/rocketclaw/config/cron.go` builds `"cron/" + name + ".md"`, and runs use IDs of the form `cron:cron/daily.md:<time>:<nonce>`). When a cron run hands off to Web, the Web chat's ID is `"web:" + source` (`handoffProducer` in `internal/rocketclaw/backend/bridge.go`, and `internal/rocketclaw/frontend/rpc/server.go` when Web opens a chat for a recorded cron run). So a normal, visible Web chat can have an ID like `web:cron:cron/daily.md:1`.

The fix removed the `/` check. Indexing now covers every conversation that has a producer, except hidden `cron:` and `one-off-cron:` runs (`indexEntryMessages` in `internal/rocketclaw/backend/message_search.go`). Search decides what to show at query time: `searchMessagesSQL` joins `managed_conversations` and applies `visibleChatsSQL`. A Delegation History has no web chat of its own and no `managed_conversations` row, so its indexed rows are never returned.

Regression coverage: `TestMessageSearchIndexesSavedEntries` (`internal/rocketclaw/backend/message_search_test.go`) saves entries to `web:cron:cron/daily.md:1` and to its Delegation History `web:cron:cron/daily.md:1/call-1`. It asserts that search returns the Web chat's messages and none from the Delegation History.

## Why This Works

What makes a Delegation History different is that Web and Slack do not show it on its own: it has no Managed Slack Thread or web chat. Its ID format is a side effect of that, not the definition. The `managed_conversations` row is the real record of "Web shows this chat", and the same check already drives `unindexedChatsSQL`, the "index complete" flag, and backfill. Filtering on that row keeps indexing, completeness, and visibility consistent. An ID-shape filter at write time can disagree with all three and leave nothing behind that shows the gap.

## Prevention

- Do not classify a conversation by a `/` (or any other character) in its ID. To keep Delegation Histories out, join `managed_conversations` or reuse `visibleChatsSQL`. The `cron:` and `one-off-cron:` prefix checks are a deliberate exception: those IDs are minted by the cron manager with a fixed prefix.
- Prefer filtering at read time over skipping at write time when the decision is about visibility. A visibility rule that changes later then needs no reindex.
- When a feature writes a "done" marker (indexed, synced, migrated), test it against every conversation ID shape Web can show, including `web:cron:...` and `web:one-off-cron:...` handoff chats.

## Related Issues

- `CONCEPTS.md`: Delegation History, Private Producer Conversation
- Plan: `docs/plans/2026-10-08-1107-feat-indexed-message-search-plan.md`
- `docs/solutions/best-practices/postgres-extension-in-migration-with-shared-test-database.md` (same feature, different lesson)
