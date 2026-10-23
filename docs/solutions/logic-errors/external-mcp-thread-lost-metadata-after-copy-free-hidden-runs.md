---
title: An External MCP Slack Thread Lost Its Metadata When Hidden Runs Stopped Copying History
date: 2026-10-22
category: docs/solutions/logic-errors/
module: internal/rocketclaw/backend
problem_type: logic_error
component: assistant
symptoms:
  - "An agent turn in an External MCP Slack thread ran without ROCKETCLAW_CONVERSATION_ID and every ROCKETCLAW_METADATA_* variable."
  - "Tools that read those variables stopped, so the agent could not reply in the thread."
  - "The External MCP calls themselves still had their metadata; only turns in the Slack thread they report to lost it."
root_cause: logic_error
resolution_type: code_fix
severity: high
tags:
  - external-mcp
  - metadata
  - private-producer-conversation
  - reports-to
  - state-store
  - test-fixture
  - query-plan
---

# An External MCP Slack Thread Lost Its Metadata When Hidden Runs Stopped Copying History

## Problem

The State Store consolidation (commit 77fabe81) stopped copying a hidden run's history into the chat it reports to. One lookup still read only the chat's own entries. That lookup finds the External MCP metadata that becomes the turn's environment, so turns in an External MCP Slack thread lost all of it.

## Symptoms

- A turn in the conversation of an External MCP session's Report Thread (a message sent from Web, since the thread ignores Slack input) ran with no `ROCKETCLAW_CONVERSATION_ID` and no `ROCKETCLAW_METADATA_*` variables. The whole set was missing, not one key.
- A tool that requires one of them (for example a ticket ID) stopped, and the agent could not reply.
- The External MCP calls in the run kept their metadata, because the run's own conversation holds the metadata entry.

## What Didn't Work

- **Ordering the joined lookup by ID.** The first fix joined the conversations a chat shows to `entries` and kept `ORDER BY id DESC LIMIT 1`. Postgres then scanned the entries primary key backward across every entry until it hit a matching metadata row. `TestExternalMCPMetadataLookupUsesIndex` caught this: the plan no longer used `session_entries_mcp_metadata` and filtered on `entry_json`.
- **Filtering by audience.** Applying the history view's audience rule (a reported run's `sandbox` entries hidden) added a `Filter:` on entries. It also changed nothing: migration 049 left a run's original metadata entry as `sandbox` and its copy as `visible`, and both hold the same text. New metadata entries are `both`.

## Solution

`SessionService.externalMCPMetadataEntry` (`internal/rocketclaw/backend/store.go:1547`) now searches every conversation the chat shows: its own, the runs whose `reports_to` is the chat, and the run it continues. This is the same set `sessionHistorySQL` reads (`reporting_runs` in `internal/rocketclaw/backend/revert.go:62`). It takes the newest metadata ID across those conversations, then reads that row:

```sql
WITH shown AS (
    SELECT $1::text AS conversation_id UNION SELECT conversation_id FROM conversations WHERE reports_to = $1
    UNION SELECT continues FROM conversations WHERE conversation_id = $1 AND continues IS NOT NULL
), latest AS (
    SELECT max(e.id) AS id FROM shown s JOIN entries e USING (conversation_id) WHERE e.entry_json::jsonb->>'type' = 'mcp_external_metadata'
)
SELECT e.id, e.entry_json FROM latest l JOIN entries e USING (id)
```

In this session's runs with realistic data (thousands of threads, each with a metadata row), the plan probed `session_entries_mcp_metadata` once per shown conversation with `Index Cond: (conversation_id = ...)`. With only three metadata rows, Postgres chose a bitmap scan over the whole partial index instead. The plan test now seeds 4,096 thread metadata rows, and asserts the index is used, there is no Sort, and no `Filter:` reads `entry_json`.

The caller in `bridge.go` needed no change. It already matches the stored entry against the paired private run's ID and then sets `ROCKETCLAW_CONVERSATION_ID` to the thread (`internal/rocketclaw/backend/bridge.go:2284`).

Fixed in PR #613, released in v0.2.1.

## Why This Works

An External MCP call runs in a private conversation and sets `SyncDestination` to its Slack thread (`cmd/rocketclaw/mcp.go:275`). Starting that turn records the thread as the run's `reports_to` (`internal/rocketclaw/backend/store_dao.go:792`). The run's first turn writes the `mcp_external_metadata` entry into the run's own conversation.

Before 77fabe81 that entry was also copied into the thread, so a lookup of the thread's own rows found it. After 77fabe81 the thread shows the run's entries through `reports_to` and holds no copy. A turn in the thread then found no metadata, so `if foundMetadata || msg.Source == protocol.SourceExternalMCP` (`bridge.go:2252`) was false for any turn not sourced from External MCP (a Web message in the thread's conversation, for example), and the whole metadata block was skipped. Searching the shown set restores exactly what the thread's history already displays.

## Prevention

- When storage moves from "copied into each reader" to "linked from each reader", audit every query that reads the old copy's location, not only the shared history query. A hand-tuned single-purpose lookup (`WHERE conversation_id = $1` on `entries`) is the likely miss, because it bypasses `sessionHistorySQL` for speed. The consolidation plan's rule already said visible-chat readers must union the entries of runs reporting to them; this lookup was a visible-chat reader nobody listed. For each such query, decide whether it means "rows this conversation owns" (deletes, undo, pruning) or "rows this conversation shows" (anything that feeds a turn or a view).
- Build test state the way production builds it. `TestRunTurnSendsExternalMCPMetadataAsDeveloperMessage` stayed green because it hand-inserted `sync_source_entry_id` copies of the run's entries into the thread, the retired copy model. It now sets `reports_to` on the run instead, and it failed with empty variables before the fix. When a change retires a mechanism, grep tests for fixtures that recreate it. Here that is `sync_source_entry_id`. Some uses remain legitimate, because migration 049 kept copies outside a run's `reports_to` chat and `store.go` and `store_summaries.go` still read them. `TestCopiedAttributionSurvivesSourceRetention` keeps one on purpose, to cover those leftover copies. `TestShadowHistorySourcePagesAndChildRevision` built a copy into the chat a run reports to, a state production no longer reaches; it now leaves the entry in the run, and still passes.
- A plan test proves a query is fast, not that it reads the right rows. Pair it with a behavioral test of the rows it must find.
- Seed plan tests with realistic cardinality. With three metadata rows the planner's choice differs from production's, and the test would pin the wrong shape.

## Related Issues

- `CONCEPTS.md`: External MCP Metadata, Private Producer Conversation, Report Thread, State Store
- Plan: `docs/plans/2026-10-19-1213-refactor-state-store-consolidation-plan.md` (introduced the copy-free model)
- Plan: `docs/plans/2026-10-19-2107-feat-principal-env-vars-plan.md` (same environment variables)
