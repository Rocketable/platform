---
title: Web Tab Groups Inside Spaces - Plan
type: feat
date: 2026-10-17
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Web Tab Groups Inside Spaces - Plan

## Goal Capsule

- **Objective:** RocketClaw web users can keep separate spaces of tabs and, inside each space, organize tabs into named, colored, collapsible groups.
- **Means:** a sixth tab-group variant that combines V1 Chrome groups and V3 Spaces from `docs/plans/2026-10-17-1339-feat-web-tab-groups-plan.md` (KTD1-KTD3).
- **Authority:** the user's request, then this plan, then the parent plan's Requirements and KTDs, which still apply unless this plan overrides them. `AGENTS.md` binds every change.
- **Stop conditions:** stop and report if the combination cannot meet R1-R6 without TypeScript source CLOC reaching 7500.
- **Execution profile:** one unit, U1, in its own workspace.
- **Finishing:** one PR against `main` with bookmark `tab-groups-chrome-spaces`. PRs #448 (Chrome-style) and #445 (Spaces) stay open and unchanged. Merging stays with the user.

---

## Product Contract

### Problem Frame

The user compared five tab-group variants and kept three: Chrome-style groups (#448), automatic groups by agent (#444), and Spaces (#445). Spaces is close to a larger project the user has in mind. The user asked for a fourth variant that combines Chrome-style groups with Spaces, without changing the three that stay.

### Requirements

- R1. Spaces behave as in V3 (parent plan U3): a space menu button at the start of the strip, New/Rename/Delete space, Move to "<space>" in the tab menu, only the current space's tabs shown, a return to each space's last active tab, and a switch to the owning space when opening a location that lives there.
- R2. Groups behave as in V1 (parent plan U1) inside the current space: Add to new group, Add to group "<name>", Remove from group, the colored header chip that collapses, and the group editor with name, nine colors, New tab in group, Ungroup, and Close group.
- R3. A group lives in exactly one space:
  - The tab menu lists only the current space's groups.
  - Moving one tab to another space removes it from its group.
  - The group editor adds **Move group to "<space>"** for each other space. It moves every member and keeps the group's name, color, and collapsed state.
  - Deleting a space moves its tabs and its groups, intact, to the first space.
- R4. New tab in group and entering an empty space both move the single New session tab (parent KTD9) into that space, and into the group for New tab in group.
- R5. Pinned tabs have no group and no space, and show in every space (parent KTD3).
- R6. Everything the parent plan's R2-R7 require still holds: storage per owner that survives reload, both placements, keyboard and screen reader support, existing tab behavior, the active tab always visible, and README documentation.

### Scope Boundaries

- PRs #448 and #445 are not modified.
- No server storage, no sync between windows, and no saved groups (parent Scope Boundaries).

---

## Planning Contract

### Key Technical Decisions

- KTD1. **Start from both variants.** The work starts from a JJ merge of `tab-groups-chrome` and `tab-groups-spaces` above the plan change `wpnxkzqv`. The finished tree ships as one linear commit on top of `wpnxkzqv`, not as a merge, so the PR shows one change. (session-settled: user-directed — chosen over modifying the existing Spaces PR: the user asked to keep the three variants and add a fourth.)
- KTD2. **One stored tab shape.** A stored `Tab` may carry both `group` and `space`. Group and space metadata live under one per-variant key, `tab-groups-chrome-spaces:<owner>`, following parent KTD3, so this build never misreads the V1 or V3 keys.
- KTD3. **Space filtering before grouping.** The strip first keeps the current space's tabs plus pinned tabs, then applies V1's contiguity ordering (parent KTD4) and collapse. Each variant's existing ordering and keyboard code is reused; the combination does not get a second rendering path.

### Risks

- **CLOC.** V1 added 68 lines and V3 added 54. Together they likely pass the 7250 hazard line. Using the hazard zone is allowed when the PR reports the count (parent Assumptions). 7500 is a stop.
- **Merge conflicts.** `ui.tsx`, `tabs.browser.test.ts`, the web README, and `dist` conflict between the two parents. Rebuild `dist` rather than resolving it by hand.

---

## Implementation Units

### U1. Chrome groups inside spaces

- **Goal:** one build that has both spaces and groups, with groups scoped to spaces.
- **Requirements:** R1-R6. Governed by KTD1-KTD3.
- **Files:** `internal/rocketclaw/web/src/ui.tsx`, `internal/rocketclaw/web/src/tabs.browser.test.ts`, `internal/rocketclaw/web/README.md`, rebuilt `internal/rocketclaw/internal/web/dist/`.
- **Approach:**
  1. Resolve the merge, keeping both features.
  2. Move both metadata stores under the KTD2 key.
  3. Add the space rules from R3 and R4.
  4. Keep both variants' existing browser tests passing, with storage keys updated, and add one combined test.
- **Test scenarios:**
  - In space "Main", group Alpha and Bravo as "Work" in green. Create space "Ops". "Work" is not offered in Ops' tab menu.
  - Move group "Work" to "Ops". Alpha and Bravo appear under "Work" in Ops with the same name and color, and Main no longer shows them.
  - Moving Alpha alone back to Main removes it from "Work".
  - After reload, the spaces, groups, colors, collapsed state, and memberships are the same.
  - Deleting "Ops" moves "Work" intact to Main.
  - New tab in group, with New session open in another space, moves New session into the current space and into the group.
  - A pinned tab shows in both spaces and in no group.
- **Verification:** the scenarios pass, the existing V1 and V3 tab tests pass, the web lint and tests pass, and source CLOC is below 7500.

## Verification Contract

Same as the parent plan: `make -C internal/rocketclaw/web lint` and `make -C internal/rocketclaw/web test` with the Playwright environment set, plus a rebuilt and committed `dist`. Root `make lint`/`make test` run in CI.

## Definition of Done

- Bookmark `tab-groups-chrome-spaces` with an open PR against `main`, holding the plan change `wpnxkzqv` and one linear variant commit.
- The PR description states the combination, the space rules for groups (R3), how to try it, the CLOC count, and any changed existing assertion.
- CI has a result.
