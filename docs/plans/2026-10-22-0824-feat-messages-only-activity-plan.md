---
title: Messages-only activity visibility - Plan
type: feat
date: 2026-10-22
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---
# Messages-only activity visibility - Plan

## Goal Capsule
Readers choosing Messages only can read the conversation without failed tool panels.
Use the existing projection boundary (KTD1).
Show a small failure indication without restoring the failed tool panel.
Stop if delivered files or another detail level would change unintentionally.

## Product Contract
### Summary
Hide failed tool rows in Messages only while retaining delivered files and background-job navigation.

### Problem Frame
The Messages only preset hides each activity category, but the projection overrides that choice for failed and background tools.
Its name and description therefore promise less activity than it actually shows.

### Requirements
- R1. Messages only hides failed, denied, and aborted tool rows that have no delivered files and are not background jobs.
- R2. User messages, assistant replies, and delivered files remain visible in their original order.
- R3. Background-job rows remain visible at every level; other presets and Custom settings retain their existing failure, grouping, and expansion behavior.
- R4. Existing saved settings continue to work without changing their format or key.
- R5. A turn with a failed tool shows a small failure indication in Messages only. Raising detail through the command palette reveals the tool details.

### Scope Boundaries
No backend, delivery, polling, storage, or new preference changes.
No new warning panel, preference, or replacement activity summary; only the small R5 indication.

## Planning Contract
### Key Technical Decisions
- KTD1. **Reuse projection and tool status.** Apply R1 in `projectTimeline` in `internal/rocketclaw/web/src/timeline-detail.ts`; use the existing tool-status classifier for R5 in the turn renderer.
- KTD2. **Reuse existing tests.** Update the projection tests and the failed-block expectations in `internal/rocketclaw/web/src/delegations.browser.test.ts`; retain `internal/rocketclaw/web/src/background.browser.test.ts` as the navigation contract.

### Assumptions
Keeping delivered files visible best preserves conversation content (R2), even when those files arrive on tool rows.
This proposal deliberately changes the historical failure exception in `docs/plans/2026-10-02-0931-feat-web-timeline-detail-plan.md` and the current tests.
Background jobs keep their existing exception because their Open action requires a rendered target; changing that navigation is outside the failed-panel request.
A turn that ends with a failed or aborted call and no assistant reply still shows its small failure indication.

## Implementation Units
### U1. Honor Messages-only activity visibility
**Goal:** Deliver R1-R5 using KTD1-KTD2.
**Dependencies:** None.
**Files:** `internal/rocketclaw/web/src/timeline-detail.ts`, `internal/rocketclaw/web/src/timeline-detail.test.tsx`, `internal/rocketclaw/web/src/ui.tsx`, `internal/rocketclaw/web/src/delegations.browser.test.ts`, `internal/rocketclaw/web/README.md`, and the rebuilt embedded web bundle under `internal/rocketclaw/internal/web/dist`. Verify the existing `internal/rocketclaw/web/src/background.browser.test.ts` unchanged.
**Approach:** Recognize the selected preset once in the existing projection and restrict the failure exception for R1. Leave delivered files, background jobs, and other modes untouched. Document the resulting Messages-only behavior.
**Execution note:** Change the existing expected output first and observe its failure before editing projection.
**Test scenarios:**
1. Failed, denied, and aborted calls without files yield no activity row in Messages only; the existing failed code-mode block disappears in the browser.
2. Delivered files still yield a visible row in Messages only, including a failed or background call carrying a file.
3. Quiet still exposes a hidden failed call, Compact still groups it, and background rows remain visible and reachable through Open at Messages only.
4. A conversation with replies and hidden activity keeps reply order; settings parsing and matching stay unchanged.
5. Messages only shows the compact failure indication without error details; raising detail through the command palette restores those details.
**Verification:** Exact projection outputs pass, the rebuilt browser timeline responds to the existing preset controls, and web lint/tests and line budgets pass.

## Verification Contract
Run the focused `bun test src/timeline-detail.test.tsx` check before and after projection changes.
Rebuild with `make -C internal/rocketclaw web-assets` before browser tests.
Run `make -C internal/rocketclaw/web lint` and `make -C internal/rocketclaw/web test`, with the repository's local test database and browser harness configured.
Inspect the final diff for unrelated behavior changes and verify source budgets without changing their limits.

## Definition of Done
U1 meets R1-R5, its tests show the intended red/green transition, and README guidance matches the implementation.
No abandoned implementation, new preference, or replacement activity panel remains.
