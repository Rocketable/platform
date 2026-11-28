---
title: Drop queued-message loose columns
date: 2026-11-27
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

## Goal Capsule

- Objective: queued, stashed and held messages behave exactly as they do today for every message saved by the current code, while the code and the `turns` table stop carrying a second, older way to store them.
- Means: every queue item is read and written through its stored full message (`inbound_json`); the per-field copies and the code that rebuilt a message from them are deleted, and a migration drops the columns nothing current reads or writes (KTD1, KTD2, KTD3).
- Authority: the owner's settled decisions below, then `AGENTS.md`, then this plan.
- Stop conditions: stop and report if any current writer of a queue item leaves `Inbound` nil, if a scheduled message, background job, note, shadow or running turn reads or writes a column this plan drops, or if the change alters the behavior modeled in `docs/verification/thread-queue-reorder/`.
- Execution profile: one PR, one commit, implemented by `ce-work` in the isolated workspace, shipped by `lfg`.

---

## Product Contract

### Summary

Remove support for waiting messages saved without their full message (`turns.inbound_json` NULL) and drop the `turns` columns only that old format used.

### Problem Frame

Queue items were once stored as loose columns (text, principal, source, attachments, Slack reply target). Every current writer also stores the full message as `inbound_json`, but five read paths still rebuild a message from the loose columns when `inbound_json` is NULL, and every write still fills the loose columns. Deployments no longer hold any such non-scheduled waiting row, so the rebuild branches and the duplicate columns only cost code.

### Requirements

**Queue items**

- R1. Every queue item carries its full message; code that rebuilt a message from loose columns when it was missing is removed from `backend/bridge.go`, `backend/conversations.go`, `backend/revert.go`, `backend/thread_bridges.go`, `backend/store_dao.go` and `frontend/rpc/server.go`.
- R2. `protocol.ThreadQueueItem` loses the fields only the old format needed (`Message`, `Content`, `Source`, `Principal`, `SlackChannel`, `SlackTS`, `SlackReply`), and readers take those facts from `Inbound`.
- R3. Queue order, claim, reorder, edit, pop, promote, stash and release behave as before for current rows; the Web queue list shows the same text, edit text, delivery and principal.

**Schema**

- R4. A new migration drops `turns.principal`, `turns.source`, `turns.content`, `turns.slack_reply`, `turns.slack_channel` and `turns.slack_ts`, with a Down that re-adds them with their original types (all nullable, no defaults).
- R5. `turns.message` stays: scheduled messages and reply jobs read and write it.

**Owner rules**

- R6. Old data may behave oddly; no compatibility shim, rejection path or data migration is added.
- R7. The OpenAI request `store` setting is not touched (it must stay false).

### Scope Boundaries

- Not changing scheduled messages, background jobs, notes, shadows, running turns or `shadow_turns`.
- Not changing the Web RPC shape (`QueueItem` keeps its fields).
- Not deleting rows; the owner already removed every old-format non-scheduled waiting row.

### Sources

- Owner decision passed by the parent session (settled, see KTDs).
- `backend/migrations/001_baseline.sql` (`turns` definition, indexes, triggers), `115_drop_voice.sql`, `117_drop_attachment_original_unverified.sql`, `120_drop_conversation_agent_draft_id.sql` (drop-migration conventions).

---

## Planning Contract

### Key Technical Decisions

- KTD1. `ThreadQueueItem.Inbound` is required for queue items; nil-inbound rebuild branches are deleted, not replaced with errors. (session-settled: user-approved — chosen over keeping a fallback for old rows: deployments hold no such rows and old data may behave oddly.)
- KTD2. Drop the six loose columns; keep `message`. Evidence: the only SQL that names `principal`, `source`, `content`, `slack_reply`, `slack_channel` or `slack_ts` on `turns` is `putThreadQueueItem`, `waitingColumns`, the `EditQueueItem` update, and the attachment-retention payloads in `backend/store.go` that `concat(message, content::text, inbound_json::text, outbound_json::text)` (queue rows' attachments are inside `inbound_json`). `message` is read by `remindersSQL`, `putScheduledMessage`, the due-schedule claim (`store_dao.go` ~183), `backgroundJobColumns`, job inserts and `fork.go`. No view, trigger column list, index, generated column or check constraint names the dropped columns; `claimBackgroundNotes` uses `t.*`/`SELECT *` only to union rows of the same table. (session-settled: user-approved — chosen over keeping all columns: unused columns only cost schema and code.)
- KTD3. `ThreadQueueItem.Message` is dropped too and queue rows stop writing `turns.message`. The Web list falls back to `Inbound.Text` where it used `Message`; every current writer without a raw-text metadata entry set `Message` to `Inbound.Text`, so the result is identical. Principal comes from `Inbound.Metadata[principal]`, which every current writer sets to the same value as the old column (agent writes differ but are always shown as "agent … (session …)").
- KTD4. `EditQueueItem` rebuilds the edited text from `Inbound` alone: the text-attachment suffix already comes from `Inbound.Text` minus the raw text; the stored `content` only contributed the same suffix.
- KTD5. Migration `122_drop_turn_queue_columns.sql`, following 117/120: `ALTER TABLE turns DROP COLUMN …` in Up, `ADD COLUMN` with original types in Down. Renumber if main gains a 122 before merge.

### Behavior trace and verified model

The reorder model in `docs/verification/thread-queue-reorder/` covers position ranking, the history lock and the worker's claim. None of that SQL changes; `queueItems` still hides items that are neither human nor from another session (only the `Inbound != nil` guard goes). The model's mapping holds and needs no edit.

### Assumptions

- No non-scheduled waiting row with NULL `inbound_json` exists on any deployment (owner verified).
- Concurrent helpers edit other hunks of `frontend/rpc/server.go`; rebase before pushing.

### Deferred / Open Questions

- Deferred: during a rolling deploy an older binary that still names the dropped columns fails its queue writes until it is replaced; earlier column drops (117, 120) accepted the same window.

---

## Implementation Units

### U1. Queue items always carry their message

- **Goal:** R1–R3, R5–R7.
- **Files:** `internal/rocketclaw/protocol/later_work.go`, `internal/rocketclaw/backend/store_dao.go`, `internal/rocketclaw/backend/bridge.go`, `internal/rocketclaw/backend/conversations.go`, `internal/rocketclaw/backend/revert.go`, `internal/rocketclaw/backend/thread_bridges.go`, `internal/rocketclaw/backend/agent_draft_chat.go`, `internal/rocketclaw/backend/store.go`, `internal/rocketclaw/frontend/rpc/server.go`, and the tests that build queue items (`backend/*_test.go`, `frontend/rpc/server_test.go`, `frontend/rpc/sidebar_test.go`, `protocol/later_work_test.go`).
- **Approach:** remove the seven fields; shrink `waitingColumns` to `input_id, conversation_id, created_at_unix_ns, position, park_after, delivery, inbound_json`; `putThreadQueueItem` always marshals `Inbound`; `scanThreadQueueItem` always decodes it; delete the rebuild branches in `claimQueueItem`, `submitEnqueuedItem` (including its old-path-only admission log), `StashQueueItem`, `mutateWebQueue`, `promoteQueueItem`; `EditQueueItem` updates only `inbound_json`; `StashQueueItem` reads `Inbound.Source`; `listQueue` reads text, raw text and principal from `Inbound`; drop `content::text` from the two attachment payload concats; drop the `Inbound != nil` guard in `queueItems`. Update constructions to stop setting removed fields.
- **Tests:** delete tests (or cases) that only covered rebuilding a message from loose columns (Slack reply target from `slack_channel`/`slack_ts`/`slack_reply`, principal or content from columns). Fixtures that built old-format rows get an `Inbound` (a tiny shared test helper is fine). Keep queue order, claim, edit, pop, promote, stash, release and list tests passing unchanged in their assertions.
- **Verification:** touched packages' tests pass.

### U2. Drop the columns

- **Goal:** R4.
- **Files:** `internal/rocketclaw/backend/migrations/122_drop_turn_queue_columns.sql`; `internal/rocketclaw/backend/store_schema_test.go` only if it lists migrations.
- **Approach:** Up drops the six columns in one `ALTER TABLE`; Down re-adds `principal text, source text, content jsonb, slack_reply jsonb, slack_channel text, slack_ts text`.
- **Tests:** existing migration Down/Up round-trip tests (`store_schema_test.go`, which migrate down to 99 and 76 and back) exercise the Down.
- **Verification:** backend tests pass against the migrated schema.

---

## Verification Contract

- `gofmt -l` on touched Go files.
- `make -C internal/rocketclaw lint`.
- `(cd internal/rocketclaw/web && bun install)` then `make -C internal/rocketclaw coverage-profile GOTESTPKGS="./backend ./frontend/rpc ./protocol"`.
- `make -C internal/rocketclaw check-cloc-budget`; source CLOC before is 45318 and must go down.
- Grep after the change: no `SlackChannel`, `SlackTS`, `SlackReply`, `Principal`, `Content`, `Source`, `Message` on `ThreadQueueItem`; no `slack_ts`, `slack_reply` anywhere outside migrations; no `item.Inbound == nil`/`!= nil` on queue items.

## Definition of Done

- All requirements met, verification green, CLOC lower than 45318.
- No dead-end or experimental code left in the diff; no new comments.
- PR open with CI decided, not merged.
