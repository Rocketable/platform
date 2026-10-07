---
title: Platform Tools Inside Execute - Plan
type: refactor
date: 2026-10-07
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Platform Tools Inside Execute - Plan

## Goal Capsule

- **Objective:** Agents reach every RocketClaw platform tool only from Execute scripts, and every tool call made inside a script is still saved and shown wherever tool calls are shown today.
- **Means:** RocketCode hides all embedder custom tools from the model and records each Execute-script call in the turn trace (KTD1, KTD2).
- **Authority:** The user's direction in session, then these R-IDs, then KTDs. `AGENTS.md` governs style and verification.
- **Stop conditions:** Stop if a call made inside Execute cannot be saved or shown, or if a platform behavior depends on a top-level call that has no Execute equivalent.

## Product Contract

### Summary

The model's top-level tools become `execute`, `load_execute_result`, `task`, `skill`, and `find_skills` (plus provider-run `websearch` when allowed). All RocketClaw platform tools run inside Execute. The "show this to the human" cron tool is removed; a cron reply is its output.

### Problem Frame

Platform tools were offered twice: as top-level tools and inside Execute. Everything should go through Code Mode. Moving them must not hide work: history, attachments, and session inspection read tool calls from saved conversations, and a call made inside a script was never saved there.

### Requirements

**Tool surface**

- R1. Every embedder custom tool is callable only inside Execute; a direct call returns "tool not found".
- R2. Permissions, schemas, and outputs of platform tools are unchanged inside Execute.

**Saved and shown**

- R3. Each tool call made inside an Execute script is saved with the turn: name, arguments, result, and the Execute call it ran under. It is never sent back to the model.
- R4. Web history shows those calls right after their Execute call, including attachments from `rocketclaw_attach_files_to_response`; `rocketclaw_get_session` prints them.
- R5. A resumed script saves each call once.

**Cron**

- R6. `rocketclaw_i_want_human_partner_to_see_this` is removed. A cron reply is posted as written; an empty reply is silent.

**Docs**

- R7. README, CHEATSHEET, skeleton skills, and the cron example describe the new surface.

## Planning Contract

### Key Technical Decisions

- KTD1. **One rule in RocketCode.** `assembleTools` removes every code-mode-only tool (sandbox host tools and all embedder custom tools) from the model-facing map after binding it into Execute. No per-tool flag. Governs R1, R2.
- KTD2. **Save script calls in the turn trace.** `recordHostCall` writes a `function_call` and `function_call_output` record, in replay item shape plus `parent_call_id`, into the turn trace beside public progress. The trace is persisted live and merged into the stored entry but never replayed to the model. Records are deduplicated by exact content, so a replayed journal step does not add a second copy. Governs R3, R5.
- KTD3. **Readers use the existing item shape.** Web history projects trace calls after their parent Execute call (or at the end while the parent is not yet in replay) through the same `historyEvent` and `ReplayAttachments` code as direct calls. `rocketclaw_get_session` already renders trace items. Governs R4.
- KTD4. **Cron output is the reply.** Remove the decision tool, its retry prompt, the cron tool mode, and delivery-text extraction. Governs R6.
- KTD5. **Workflow tool lists ignore names that live inside Execute**, like they already ignore sandbox host tools; permissions still govern those tools. Governs R1.

## Implementation Units

### U1. RocketCode: Execute-only custom tools and saved script calls

- **Files:** `internal/rocketcode/tools.go`, `internal/rocketcode/mcp_tools.go`, `internal/rocketcode/public_progress.go`, `internal/rocketcode/custom_tools.go`, `internal/rocketcode/looper.go`, tests in `internal/rocketcode/mcp_tools_test.go` and `internal/rocketcode/custom_tools_test.go`.
- **Test scenarios:** model tools are only `execute` and `load_execute_result` for an agent with platform grants; a script's nested call and result are saved under the Execute call ID.

### U2. RocketClaw: remove the cron decision tool and show script calls

- **Files:** `internal/rocketclaw/backend/bridge.go`, `raw_run.go`, `store_summaries.go`, `session_tools.go`, `internal/rocketclaw/frontend/rpc/server.go`, `internal/rocketclaw/frontend/cron/manager.go`, `internal/rocketclaw/web/src/ui.tsx`, docs, and their tests.
- **Test scenarios:** platform tools never top-level; calls inside Execute saved with the turn and saved once after a restart; Web history shows a script's tag call and its attachment; `rocketclaw_get_session` prints a script call; silent cron replies stay silent; workflow tag limits work through Execute.

## Verification Contract

| Gate | Command |
|---|---|
| Format | `gofmt` on touched Go files |
| Tests | `go test ./...` |
| Lint | `make lint` |
| Full suite with budgets | `make test` |

## Definition of Done

- R1 through R7 hold, proven by the tests above.
- `SOURCE_CLOC_BUDGET` is untouched and `make test` passes.
