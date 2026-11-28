---
title: Drop Old Browser Data Handling in the Web Page - Plan
type: refactor
date: 2026-11-27
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Drop Old Browser Data Handling in the Web Page - Plan

## Goal Capsule

- **Objective:** The RocketClaw Web page carries less code, and what people use today (saved tabs and groups, Search pages and `/search?q=` links, timeline detail settings, the session list) behaves exactly as before.
- **Means:** delete the branches that only read data or links written by older builds of the page (KTD1-KTD4), with their dedicated tests and docs.
- **Authority:** the owner's rule and approved decision (Key Decisions below) govern; this plan governs mechanism.
- **Stop conditions:** stop and report if a removal would change behavior for data the current page writes, if ignored old data would reach code that throws, or if TS source CLOC does not go down.
- **Execution profile:** one PR, Lightweight, web-only (`internal/rocketclaw/web`), plus the docs that describe the removed behavior. No Go change expected.
- **Finish:** `ce-work` implements and verifies; the calling pipeline reviews, ships, and watches CI. No merge.

## Product Contract

### Summary

Remove the Web page's handling of old browser-side data and old links: pre-tab saved searches in `localStorage`, `?agent=` / `?room=` link parameters, tab groups saved without a placeholder row, timeline settings that store details on rows that no longer collapse, and the bare-protocol cache key of the local server's session list.

### Problem Frame

Each of these branches exists only to upgrade data written by an older build. The owner wants fewer lines and accepts odd behavior for old data, which ages out within 90 days. Keeping the branches costs code, tests, and doc text.

### Key Decisions

- **Old browser data and old links may be ignored.** (session-settled: user-approved — chosen over keeping upgrade code for old data: "We're trying to remove lines of code and most changes are forward looking. I'm OK with some odd behavior with old data because in 90 days it will be gone.") Governs R1-R6.
- **Remote-server compatibility stays.** Handling of older remote RocketClaw servers (missing `itemId`, `principals`, `closed`) and old script calls without `status` is a separate, undecided question. (session-settled: user-directed — chosen over removing it in this change: it is not decided yet.) Governs R7.

### Requirements

**Removals**

- R1. A Search page no longer imports searches from the `search-tabs:<owner>` `localStorage` key and no longer deletes that key.
- R2. A `/search` link's `agent` and `room` query parameters are ignored; only `q` is read. Saved searches no longer fold `agentFilter` / `roomFilter`.
- R3. Loading saved tabs no longer adds a placeholder row to a group that lacks one.
- R4. Reading the timeline setting no longer drops `details` saved on categories that do not collapse.
- R5. The local server's session-list cache key uses the same `[server name, protocol]` form as remotes.

**Invariants**

- R6. Data the current page writes, and current links, behave exactly as today. Ignored old values are dropped or left unused; none reaches code that throws or breaks the page.
- R7. Compatibility with older remote RocketClaw servers and old script calls without `status` is unchanged.

### Scope Boundaries

- Not touched: remote-server compatibility (R7), "older cached rows without tags stay usable" in the session list, and the Go side of tabs (it never added placeholders).
- Not built: cleanup of orphaned `search-tabs:*` keys or old IndexedDB snapshots. They are small and harmless; the owner accepted leftover old data.

---

## Planning Contract

### Key Technical Decisions

- KTD1. **Search page reads only the tab's saved page and `q`.** `SearchTabs` seeds its searches from `page?.searches` / `page?.active` alone and builds the link query from `q`. `foldFilters`, `LegacySearch`, the `legacy`/`imported` state and the `removeItem` call go. `owned` stays because closing the last search uses it. `termValue` stays (other callers). Covers R1, R2, inherits the first Key Decision's label.
- KTD2. **`savedTabs` stops adding placeholders.** Delete the `kept` reduce and arrange the filtered tabs directly. An old group without a placeholder then behaves like a group before groups were sticky: it goes once its last tab goes, because `savedState` already drops groups with no rows. The strip draws group headers from members, and `isKeeper` paths only arise from real placeholder rows, so nothing reads a missing placeholder. Covers R3, R6.
- KTD3. **`parseRows` keeps `details` for every category.** Delete the `if (!collapsible.has(id)) return [id, { placement }]` line. Current saves carry no `details` on non-collapsible rows, so they parse to `details: undefined`, which `timelineLevel` treats the same as an absent key. Old saves with such `details` read as Custom until a level is picked; `projectTimeline` ignores `details` for non-collapsible rows, so nothing throws. Covers R4, R6.
- KTD4. **One cache-key form.** `scope` becomes `JSON.stringify([name, protocol.data])` whenever the protocol is known, for the local server (`name === ""`) too, and the comment about the old key goes. The old local snapshot is simply not found and the list refetches. Covers R5, R6.

### Assumptions

- Server-stored Search pages never carried `agentFilter` / `roomFilter`: the import folded them before saving, and the proto has no such fields.
- Test fixtures that seed groups without a placeholder row model old data; updating them to the current shape (a `group:<id>` row with that group) keeps what they test.

---

## Implementation Units

### U1. Remove old Search page storage and link handling

- **Goal:** Search pages read only current saved pages and `q` links.
- **Requirements:** R1, R2, R6; KTD1.
- **Dependencies:** none.
- **Files:** `internal/rocketclaw/web/src/ui.tsx`, `internal/rocketclaw/web/src/search-page.browser.test.ts`, `internal/rocketclaw/web/README.md`.
- **Approach:**
  1. In `SearchTabs`, drop the legacy key, the import flag, the `localStorage` read and removal, and the filter folding; seed from the page.
  2. Delete `foldFilters` and `LegacySearch`, and the comments that describe the import.
  3. Delete the test "searches this browser kept stay when a page that already has searches is edited", the `search-tabs:` assertions in "saved search tabs" and the last-close test, and turn "search URL follows the active tab, folds legacy filters, …" into a test that opens the canonical `?q=agent%3A… outage` link with no legacy storage.
  4. Remove the README sentence on older `agent=` / `room=` links and saved searches.
- **Test scenarios:**
  - Opening `/search?q=agent%3Aacme-cs-support+outage` opens one tab with the agent pill and `outage` typed; reload keeps it; a second open of the same link reuses the tab (existing URL test, minus legacy setup).
  - Saved search tabs at both widths keep working (existing test, minus the `search-tabs:` check).
- **Verification:** the Search page test file passes; no `search-tabs` or `agentFilter` remains under `internal/rocketclaw/web/src`.

### U2. Stop upgrading tab groups without a placeholder

- **Goal:** `savedTabs` loads groups as saved.
- **Requirements:** R3, R6; KTD2.
- **Dependencies:** none.
- **Files:** `internal/rocketclaw/web/src/ui.tsx`, `internal/rocketclaw/web/src/tabs.browser.test.ts`, `internal/rocketclaw/web/src/spaces.browser.test.ts`, `internal/rocketclaw/web/README.md`, `docs/solutions/architecture-patterns/kept-tab-groups-hold-their-place-with-a-placeholder-tab.md`.
- **Approach:**
  1. Delete the `kept` reduce in `savedTabs` and its comment sentence.
  2. Where a browser test seeds `saved.state` with a group and no placeholder and then relies on the group staying or on a `group:<id>` row, add the placeholder row to the fixture. Leave fixtures that do not depend on it.
  3. Update the README sentence and the solution doc bullet that describe the upgrade.
- **Test scenarios:**
  - Existing group tests (Add to new group, Delete group, sticky empty group, moving grouped tabs between spaces) pass with fixtures in the current shape.
- **Verification:** tab and space test files pass.

### U3. Keep timeline details as saved

- **Goal:** `parseRows` no longer special-cases non-collapsible rows.
- **Requirements:** R4, R6; KTD3.
- **Dependencies:** none.
- **Files:** `internal/rocketclaw/web/src/timeline-detail.ts`, `internal/rocketclaw/web/src/timeline-detail.test.tsx`.
- **Approach:** delete the one line, and delete the parse case that stores `details` on every category and expects the Everything level.
- **Test scenarios:**
  - Remaining parse cases (bad JSON, wrong version, invalid values falling back to Compact) still pass.
  - A level saved by `setTimelineRows` reads back as the same level.
- **Verification:** `timeline-detail.test.tsx` passes.

### U4. One session-list cache key form

- **Goal:** the local server's snapshot key matches remotes.
- **Requirements:** R5, R6; KTD4.
- **Dependencies:** none.
- **Files:** `internal/rocketclaw/web/src/ui.tsx`, `internal/rocketclaw/web/src/session-list-app_test.ts`.
- **Approach:** change `scope` and drop its comment. Update the shared app scenario helper's direct IndexedDB reads and seeds that use `[owner, "test-protocol"]` to the new protocol key.
- **Test scenarios:**
  - The session-list app scenario restores a saved list, keeps owners apart, and drops a snapshot when the protocol changes (existing scenario with the new key).
- **Verification:** `session-list.browser.test.ts` and `session-list-composer.browser.test.ts` pass.

---

## Verification Contract

| Gate | Command | Applies to |
|---|---|---|
| Install | `bun install` in `internal/rocketclaw/web` | before tests |
| Lint (react-doctor, TS budget) | `make -C internal/rocketclaw/web lint` | all units |
| Web tests | `make -C internal/rocketclaw/web test-files TESTS="src/search-page.browser.test.ts src/tabs.browser.test.ts src/spaces.browser.test.ts src/timeline-detail.test.tsx src/session-list.browser.test.ts src/session-list-composer.browser.test.ts src/tabs.test.ts"` | U1-U4 |
| Embedded build | `go -C internal/rocketclaw generate ./internal/web` | web change; regenerate `internal/rocketclaw/internal/web/dist/index.html.gz`, never hand-edit |
| Size | `make -C internal/rocketclaw/web cloc` before and after; source must go down | all |
| Go budget | `make -C internal/rocketclaw check-cloc-budget` | only if Go is touched |

## Definition of Done

- R1-R5 hold, and R6-R7 are rechecked against the diff: every removed branch either drops the old value or leaves it unused, with no path to a throw.
- Lint and the listed test files pass; the embedded dist is regenerated if web sources changed.
- TS source CLOC is lower than before (11837).
- No comments added; comments describing removed behavior are gone; no dead code or abandoned attempts remain in the diff.
- PR text names no internal deployments, people, or channels.
