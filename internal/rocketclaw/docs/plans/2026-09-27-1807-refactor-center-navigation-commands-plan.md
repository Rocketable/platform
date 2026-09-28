---
title: Center Navigation Commands - Plan
type: refactor
date: 2026-09-27
topic: center-navigation-commands
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Center Navigation Commands - Plan

## Goal Capsule

- **Objective:** People can start or find a session and open commands from a compact center navigation row without losing access to the existing pages.
- **Means:** Keep only the three requested center actions and use the existing command palette for the displaced page links (KTD1).
- **Authority:** The user's center-row-only correction and visible-prefix choice govern this plan.
- **Stop condition:** Do not alter the footer handle, sidebar toggle, page behavior, or command search syntax.

## Product Contract

### Summary

The center row of the bottom bar will contain New session, Search sessions, and Open command palette, each with a recognizable glyph. Commands in the palette will have visible category prefixes.

### Problem Frame

The center row currently includes five page shortcuts in addition to session actions. It is more crowded than needed because those pages already appear in the command palette.

### Key Decisions

- **Center row only** (session-settled: user-directed — chosen over changing the entire bottom bar: the request is to reduce the buttons in the center). Governs R1, R2.
- **Visible command prefixes** (session-settled: user-directed — chosen over typed category filters: the user chose names such as `Session: Name` rather than new search syntax). Governs R3.

### Requirements

**Center row**

- R1. Show New session, Search sessions, and Open command palette in the center row, with accessible names and distinct glyphs.
- R2. Preserve the existing footer collapse handle and the separate sidebar toggle; remove only the five page shortcuts from the center row.

**Command palette**

- R3. Show category-prefixed names for command actions, while preserving ordinary text search and action behavior.
- R4. Keep the displaced pages reachable through their existing command-palette entries and existing navigation elsewhere.

### Scope Boundaries

- Do not add typed category filters, change keyboard shortcuts, or redesign the session-search and cron modes.
- Do not change page contents, the mobile sidebar, or the footer's collapse and swipe behavior.

## Planning Contract

### Key Technical Decisions

- KTD1. **Reuse the existing palette modes and page actions.** The new button opens the existing commands mode; no new routing or command registry is needed. Governs R1, R4.
- KTD2. **Prefix labels only in the commands palette.** Include session actions, built-in commands, pages, cron, and sidebar actions while leaving shared action labels outside the palette alone. Preserve action keys and selection behavior. Governs R3.

## Implementation Units

### U1. Narrow the center row and label palette commands

- **Goal:** Deliver R1–R4 within the existing web navigation and palette.
- **Requirements:** R1, R2, R3, R4.
- **Dependencies:** None.
- **Files:** `internal/rocketclaw/web/src/ui.tsx`, `internal/rocketclaw/web/src/session-list.browser.test.ts`, `internal/rocketclaw/web/README.md`.
- **Approach:** Replace the five page buttons with an Open command palette button in the center row. Prefix palette command labels by their subject without changing shared labels or search parsing; leave separate footer controls intact. Update browser tests that assumed page links live in the footer: reach pages through the palette and return through existing page Close/Escape behavior rather than recreating the removed active-link toggle.
- **Patterns to follow:** Existing New session and Search sessions icon buttons; existing commands mode and `paletteRows` entries in `ui.tsx`.
- **Test scenarios:**
  - At desktop and mobile widths, the center row has exactly the three named actions; New session and Search sessions still perform their original actions.
  - Clicking Open command palette shows the existing commands dialog and its prefixed entries; selecting a displaced page entry still navigates there.
  - The collapse handle and desktop sidebar toggle still work without a layout shift; typing ordinary command text still matches entries.
- **Verification:** Web browser tests and lint/build pass; no new command-search behavior appears.

## Verification Contract

- Run the web test suite and browser navigation tests, web lint and build, and the web source-CLOC budget check.
- Inspect the changed footer and palette on desktop and mobile; verify page navigation through the palette and preserved keyboard shortcuts.

## Definition of Done

- The three center actions work and have accessible labels; former center page links remain usable in the palette.
- The handle, sidebar toggle, search behavior, and shortcuts retain their current behavior.
- Relevant tests, lint, build, and CLOC budget pass; no abandoned attempt remains in the diff.
