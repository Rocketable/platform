---
title: Drop conversations.agent_draft_id - Plan
type: refactor
date: 2026-11-26
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Drop conversations.agent_draft_id - Plan

## Goal Capsule

- Objective: Anyone reading the State Store schema no longer finds a draft-to-chat link that nothing uses, and agent draft test chats work exactly as before.
- Means: Delete the one write in `startAgentDraftTestChat` and drop the column in a new migration (KTD1, KTD2).
- Authority: Product Contract R-IDs win on behavior; KTDs win on mechanism.
- Stop conditions: Stop and report if any reader of `conversations.agent_draft_id` turns up (Go, SQL, TS, proto, docs), or if any code deletes `agent_drafts` rows.
- Execution profile: one PR, one commit. Lightweight.
- Finishing: the implementer verifies locally, then the change ships as a pull request.

## Product Contract

### Summary

Remove the `conversations.agent_draft_id` column and the single UPDATE that sets it when an agent draft's test chat starts.

### Problem Frame

Migration `093_agent_drafts.sql` added `conversations.agent_draft_id` to bind a draft test chat to its draft. The binding that the product actually uses is the draft's own `TestChat` field (set through the `AgentDraftSetTestChat` op) and the `agent_draft` conversation kind, which keeps those chats out of normal lists. The column is written and never read, so it is dead state.

### Requirements

- R1. Starting an agent draft test chat no longer writes `conversations.agent_draft_id`.
- R2. After migrations run, `conversations` has no `agent_draft_id` column.
- R3. Draft test chats behave as before: the chat is recorded with kind `agent_draft`, and the draft's `TestChat` points at it.

### Key Decisions

- Remove the column, not only the write. (session-settled: user-directed — chosen over keeping the column and only removing the write: it is unused, so the schema should not carry it.) Governs R1, R2.

### Scope Boundaries

- Historical plan `docs/plans/2026-11-11-1123-feat-agent-editor-plan.md` mentions the column. Plans are point-in-time records, so it stays unchanged.
- Already-applied migrations (`093_agent_drafts.sql`) are never edited.

## Planning Contract

### Key Technical Decisions

- KTD1. **New migration `120_drop_conversation_agent_draft_id.sql`.** Migration 119 is the highest on `main`, so the next number is 120. Up drops the column with `ALTER TABLE conversations DROP COLUMN agent_draft_id;`, matching `117_drop_attachment_original_unverified.sql`. Dropping the column also drops its foreign key to `agent_drafts(id)`.
- KTD2. **Down re-adds the column as 093 created it** (`TEXT REFERENCES agent_drafts (id)`, nullable). Recent drop migrations (115, 117) write Down sections. Tests also migrate down below 093 (`store_schema_test.go` runs `migrate.Down` to 91), and 093's Down runs `DROP COLUMN agent_draft_id`, which would fail if 120's Down did not restore the column. The values are not restored; nothing reads them.
- KTD3. **Register the migration in the released-store test.** `TestInitializeSessionDBAdoptsReleasedStore` in `internal/rocketclaw/backend/store_schema_test.go` lists every migration file twice (the release list and the expected ledger); append `120_drop_conversation_agent_draft_id.sql` to both. `sessionMigrationCount` counts embedded files, so it needs no edit.
- KTD4. **One commit.** The merge queue lands commits one at a time. Removing the write and dropping the column together keeps every landed commit consistent; dropping the column alone would break the UPDATE.

### Assumptions

- The "unused" gate was checked before planning: the only reference outside migrations and the historical plan is the UPDATE in `internal/rocketclaw/backend/agent_draft_chat.go`. Every wildcard select over `conversations` (`visibleChatsSQL` in `message_search.go`, `chatOriginSQL` and `shownChatsSQL` in `store.go`, the fork delegation copy in `fork.go`) ends in an explicit column list, so none scans the column by position. No view, trigger, generated column, index, or `search_text` expression names it. No Go code deletes `agent_drafts` rows, so the foreign key has no behavioral effect today.
- `execState` and `draft.ID` stay used elsewhere in `startAgentDraftTestChat`, so deleting the UPDATE leaves no other dead code.

## Implementation Units

### U1. Remove the write and drop the column

- **Goal:** Satisfy R1 and R2 while keeping R3.
- **Requirements:** R1, R2, R3; KTD1, KTD2, KTD3, KTD4.
- **Dependencies:** none.
- **Files:**
  - `internal/rocketclaw/backend/agent_draft_chat.go` (modify)
  - `internal/rocketclaw/backend/migrations/120_drop_conversation_agent_draft_id.sql` (create)
  - `internal/rocketclaw/backend/store_schema_test.go` (modify)
- **Approach:**
  1. Delete the "bind agent draft test chat" `execState` call in `startAgentDraftTestChat`.
  2. Add migration 120 with the Up and Down from KTD1 and KTD2, in the `-- +migrate Up` / `-- +migrate Down` format.
  3. Append the file name to both lists in `TestInitializeSessionDBAdoptsReleasedStore`.
- **Patterns to follow:** `internal/rocketclaw/backend/migrations/117_drop_attachment_original_unverified.sql`.
- **Test scenarios:**
  - Test expectation: none -- the change removes dead state; no behavior changes. Existing agent draft tests in `internal/rocketclaw/backend/agent_draft_chat_test.go` cover R3 (test chat start and send), and the schema tests cover R2 (every migration runs up on a real Postgres, the released-store ledger matches, and down-migration tests exercise 120's Down before 093's).
- **Verification:** The backend package tests pass against a real Postgres, and no reference to `agent_draft_id` remains outside migrations 093 and 120 and the historical plan.

## Verification Contract

| Gate | Command |
|---|---|
| Format | `gofmt -l` on touched Go files prints nothing |
| Lint | `make -C internal/rocketclaw lint` |
| Backend tests (run migrations on real Postgres) | `make -C internal/rocketclaw coverage-profile GOTESTPKGS="./backend"` |
| Size budget | `make -C internal/rocketclaw check-cloc-budget`; report source CLOC before and after |

## Definition of Done

- R1 to R3 hold and every gate in the Verification Contract passes.
- A repo-wide search for `agent_draft_id` finds only migrations 093 and 120 and the historical plan.
- No abandoned-attempt code remains in the diff.
