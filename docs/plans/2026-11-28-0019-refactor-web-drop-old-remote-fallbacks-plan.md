---
title: Drop web fallbacks for older connected servers and old script calls
date: 2026-11-28
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

## Goal Capsule

- Objective: the Web interface carries less code for data shapes only servers older than about three weeks still send, without ever throwing on that data.
- Means: delete the web-side fallbacks for a missing transcript `itemId`, a missing `closed` or `principals` list, and a script call without `status` (KTD1-KTD4).
- Authority: this plan's settled decisions, then `AGENTS.md`, then the code.
- Stop conditions: a removal that would let old-server data throw in the browser keeps its fallback (R4); a removal that makes source TS CLOC grow is wrong.
- Execution profile: one PR, web code only. The implementing agent ships it.

## Product Contract

### Summary

Remove the Web interface's handling of fields that current servers always send. A connected server older than about three weeks may then show tabs, senders, queued items or script calls wrongly.

### Problem Frame

The web keeps fallbacks for servers that predate `itemId` (2026-09-17), `principals` (2026-10-15) and `closed` (2026-11-03), and for script calls saved before calls carried a status. Every current server sends these fields: the RPC encodes responses with default values, every History event gets an item ID, and a denied script call is recorded with status `denied`. The fallbacks only serve old data that ages out within 90 days.

### Key Decisions

- **Remove fallbacks for older connected servers and old stored script calls.** (session-settled: user-approved — chosen over keeping them for old data: "We're trying to remove lines of code and most changes are forward looking. I'm OK with some odd behavior with old data because in 90 days it will be gone.") Governs R1-R3.
- **A wrong display is acceptable; a thrown error is not.** (session-settled: user-approved — chosen over removing every guard regardless of effect: old-server data can still reach the browser at runtime.) Governs R4.
- **Leave server-side defaults alone.** (session-settled: user-approved — chosen over removing them in the same change: the scope is the web.) Governs R5.

### Requirements

- R1: A History transcript line takes its ID from `inputId` or `itemId` only; the `messageId` and text-derived fallbacks go.
- R2: A script call reads as denied only from its own `status`; the approver-outcome fallback goes.
- R3: `SavedTabs.closed` and `CodeModeCall.status` become required TypeScript fields.
- R4: Data from an older server that lacks these fields must not throw anywhere in the web.
- R5: Go code is unchanged.

### Scope Boundaries

- The `principals ?? []` merge in `searchAll` stays (KTD3).
- The `?? noClosed` in `useTabs` stays: it also covers the tabs query before it loads, like `?? noShared` beside it.
- The server's rebuilding of script calls from permission reviews for chats saved before calls were recorded stays (R5); it is a candidate for a separate Go change.

## Planning Contract

### Key Technical Decisions

- KTD1: `historyLines` in `internal/rocketclaw/web/src/ui.tsx` returns `id: message.inputId || message.itemId` and drops its `seen` map. Every History event the server builds sets `ItemId` (`internal/rocketclaw/frontend/rpc/server.go`, `transcriptEntry` and `publicProgress`). An old event missing both gives an undefined ID; nothing calls a string method on a line ID, so it renders, possibly merged or misordered.
- KTD2: `codeModeCallRow` uses `call.status === "denied"` alone. A call the approver denies fails with `permissionDeniedError`, which `CodeModeCall.finish` records as `denied` (`internal/rocketclaw/harness/public_progress.go`). Calls rebuilt from old reviews carry no status and will show as plain rows.
- KTD3: `principals` keeps its `?? []` and stays optional. Without it, `flatMap` puts `undefined` into the list for an old remote, and typing `principal:` in search calls `.toLowerCase()` on it in `searchChoices` and throws. Any replacement guard is the same fallback moved elsewhere, which R4 forbids removing.
- KTD4: `closed` becomes required in `SavedTabs`, and `ClosedTab` reads `SavedTabs["closed"][number]`. Runtime absence still falls to `noClosed`.

## Implementation Units

### U1. Drop the fallbacks

- Goal: R1-R4.
- Files: `internal/rocketclaw/web/src/ui.tsx`, `internal/rocketclaw/web/src/types.ts`, and any test fixture that only fed the removed fallbacks.
- Approach: apply KTD1, KTD2 and KTD4; make `CodeModeCall.status` required. Fix type errors in fixtures by adding the field, and delete tests that only checked a removed fallback.
- Test scenarios: existing transcript, delegation (script call rows, denied call), tab and search browser tests pass unchanged; `tsc` passes.
- Verification: the Verification Contract.

## Verification Contract

- `make -C internal/rocketclaw/web lint` (eslint, `tsc`, react-doctor).
- `make -C internal/rocketclaw/web test-files TESTS="..."` for the files covering `historyLines`, script call rows and closed tabs: `src/transcript.test.ts`, `src/delegations.browser.test.ts`, `src/spaces.browser.test.ts`, `src/tabs.browser.test.ts`, `src/session-list-composer.browser.test.ts`, `src/long-chat.browser.test.ts`, `src/typing.browser.test.ts`, `src/shadows.browser.test.ts`, `src/tree-panel.browser.test.ts`, after `bun install`.
- `go -C internal/rocketclaw generate ./internal/web` to regenerate `internal/rocketclaw/internal/web/dist/index.html.gz`.
- `make -C internal/rocketclaw/web cloc`: source CLOC lower than 11824.
- No Go checks: no Go file changes.

## Definition of Done

- R1-R5 hold, with the CLOC measurement recorded.
- The verification commands above pass.
- No experimental or abandoned code remains in the diff.
