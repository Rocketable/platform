---
title: Hide working sessions in the picker
date: 2026-10-14
type: feat
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

## Goal Capsule

Let users temporarily keep working sessions out of the session picker and see them return when their turns end.
Use a local picker switch and the existing live session state (KTD1).
Build and verify this reversible draft proposal; publish a draft PR, never merge it.
The request and repository rules override this plan. Stop if the option requires backend state or a new preference system.

## Product Contract

### Requirements

- R1. Offer **Hide working sessions** only in the session picker, off by default.
- R2. When enabled, omit sessions whose live `running` state is true; restore them automatically when it becomes false.
- R3. Mouse, arrow keys, and Enter select only visible rows, including Enter waiting for a search response and an empty filtered result.
- R4. Filter row-derived query suggestions consistently; preserve existing query processing and loading/error states without inventing counts.
- R5. Leave backend work, current chat tabs, full Search results, and the removed session sidebar unchanged.
- R6. Use only picker runtime state, with no persistent preference, new model fields, or dependency.

### Draft Assumption

Picker-only hiding is the proposed scope, not a settled decision about Search.
The PR body must ask exactly: **Should this hide working sessions only in the picker, or in Search too?**
Search hiding is deferred and must not be implemented in this change.

## Planning Contract

- KTD1. Keep the switch in `CommandPalette`, using the installed Switch and Field components. (session-settled: user-directed — chosen over a persistent global setting: this option needs only runtime picker state.) The palette currently remounts on opening; the option therefore resets on reopening.
- KTD2. Filter existing session rows, not server results or backend state. Keep matching completeness based on the unfiltered list so intentionally hidden matches do not become missing/loading results.
- KTD3. Async Enter must use the latest eligible rows at response time, not the first unfiltered or stale captured match. Keep the default Search call unfiltered.

### Data Flow

Live session rows + server match order -> eligible picker rows -> displayed rows and selection.
Live session rows -> eligible row-derived suggestions.
Pending Enter response + latest eligible rows -> first eligible match, or no navigation.
Off -> existing rows; on + running -> omitted; on + ended -> restored.

### Grounding

`internal/rocketclaw/web/src/ui.tsx` owns `CommandPalette`, `paletteRows`, `useSessionSearch.enter`, and `SessionSearch` suggestions.
`internal/rocketclaw/web/src/session-list.browser.test.ts` serves the actual embedded App with synthetic session/search/stream fixtures.
Installed Switch supports controlled `checked` and `onCheckedChange`; no component install is needed.

## Implementation Units

### U1. Local picker filtering

- Requirements: R1-R6; KTD1-KTD3.
- Files: `internal/rocketclaw/web/src/ui.tsx`, `internal/rocketclaw/web/src/session-list.browser.test.ts`, `internal/rocketclaw/web/README.md`, tracked `internal/rocketclaw/internal/web/dist/` assets.
- Execution note: Add a narrow browser scenario first and demonstrate its failure before implementation where practical; rebuild tracked assets before every browser run.
- Scenarios: Default off includes working rows; enable by mouse and keyboard; live start hides and completion restores without closing the picker; existing chat/tab stays open; visible ordering and arrow/mouse/Enter navigation agree; server-first working match is skipped by delayed Enter; no eligible result does not navigate; row-only tags disappear when their rows are hidden; full Search is unchanged.
- Approach: Reuse current matching, ordering, and mounted state. A small latest-row ref may be needed for deferred Enter; do not introduce a general preference or filtering framework.

## Verification Contract

Run `bun install --frozen-lockfile` in `internal/rocketclaw/web`, `make -C internal/rocketclaw web-assets`, the focused real-browser scenario, then `make -C internal/rocketclaw/web lint` and `make -C internal/rocketclaw/web test` including its budget gate.
Use workspace `.tmp/` for all temporary output and `TMPDIR`, the existing browser installation, and an owned isolated PostgreSQL 18 container for RPC browser tests; remove only that container afterward.
If any Go code changes, apply the mandatory Go standards and run all root Go gates; otherwise avoid Go edits.
Before publication, simplify and review the actual diff, verify current base, include the rebuilt bundle, and wait for CI to decide the exact published head.

## Definition of Done

R1-R6 hold in the real App, local gates pass, and a labeled draft PR contains the single scope question with no Search hiding.
README impact is assessed and the small user-facing picker option documented if needed.
No foreign refs or PRs are changed; no merge or draft promotion occurs.
