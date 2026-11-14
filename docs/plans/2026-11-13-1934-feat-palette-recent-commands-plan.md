---
title: Palette Recent Commands Per Browser Tab - Plan
type: feat
date: 2026-11-13
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Palette Recent Commands Per Browser Tab - Plan

## Goal Capsule

- **Objective:** In a browser tab, opening the palette with Cmd+P or Cmd+Shift+P shows that tab's most recently run commands at the top, so Cmd+Shift+P then Enter repeats the last command run in that tab.
- **Means:** keep the existing recency list, store it per tab, and list its top three commands above the sessions in Cmd+P (KTD1, KTD2).
- **Authority:** this plan's Requirements, then its KTDs, then the units.
- **Execution profile:** one PR, Lightweight, web client only (`internal/rocketclaw/web`).
- **Stop conditions:** stop if per-tab storage would break a saved-state contract other than the palette's own history.
- **Finishing:** the `lfg` run implements, reviews, and opens the PR. Merging stays with the user.

## Product Contract

### Summary

The palette already ranks commands by when they were last run, but it keeps that history in `localStorage`, which every tab shares and which never expires. This change keeps the history in `sessionStorage`, so each tab ranks commands by its own recent use. Cmd+P with an empty query also lists the tab's three most recent commands above the sessions.

### Problem Frame

Someone repeating an action wants the palette to offer the action they just took in this tab. A shared history mixes in commands run in other tabs, so the top row is not reliably "my last action here". Cmd+P with an empty query shows sessions only, so recent commands are not offered there at all.

### Requirements

**Recency scope**

- R1. The palette's command history belongs to one browser tab: commands run in one tab never reorder the palette in another tab, and the history survives reloads and navigation within the tab.

**Cmd+Shift+P (commands)**

- R2. With an empty query, the most recently run commands available in this tab come first, most recent first, so Enter runs the last command run in this tab.

**Cmd+P (sessions and commands)**

- R3. With an empty query, up to three of the tab's most recently run commands that are available now are listed above the sessions, most recent first; Enter runs the first. The navigation search button opens the same palette and shows the same rows.
- R4. With no command history in the tab, an empty Cmd+P lists sessions only, as today.

### Scope Boundaries

- Typed queries keep today's ranking: match quality first, then recency, then alphabetical, with Timeline levels in the Config slider's order.
- With an empty query, a recently run Timeline level ranks by recency like any command, so Enter repeats it (R2). This replaces the rule that running a level never moves it to the top.
- Empty Cmd+P then Enter now runs the top recent command when the tab has one, instead of opening the first session.
- Commands that open a picker or dialog (Cron: Run, Cron: Timeline, Sessions: New on a server, Sessions: Fork, Sessions: Handoff) repeat by reopening it; the choice made inside it is not repeated.
- Not built: skipping disabled recent commands. A disabled top row is visible as disabled, and Enter on it does nothing harmful.
- Not built: a separate status line for sessions still loading below lifted commands. Sessions appear when they load.
- Not built: a frequency count ("most used" by count). The request's goal, Enter repeats the most recent action, needs recency.
- Not built: keeping a separate cross-tab history for the remote-folder picker. Folder recency becomes per tab with the rest of the history. Evidence that would change this: the person wanting folders remembered across tabs.

## Planning Contract

### Key Technical Decisions

- KTD1. **Store the existing `command-history` list in `sessionStorage` instead of `localStorage`.** One history, one storage call swap. `sessionStorage` is per tab and survives reload, which is exactly R1. The palette remounts on every open, so it reads the tab's current list each time.
- KTD2. **Lift recent commands in Cmd+P inside `paletteRows`.** When the mode is `sessions` and the query is empty, take the already sorted command list, keep the entries that rank as recent, and slice to three. With a filter in the query, Cmd+P keeps listing no commands. This reuses the existing tier and rank sort, so only commands available now are lifted.
- KTD4. **Apply the Timeline exception only to typed queries.** The recency rank treats Timeline levels as unranked only when the query is non-empty, which keeps R2 true for every command.
- KTD3. **Enter in an empty Cmd+P runs the top row.** The existing Enter rule already runs a non-session top row whose label contains the typed text, and every label contains the empty string, so no keyboard change is needed.

### Assumptions

- "The most 3 used commands" means the three most recently run, because the stated goal is that Enter repeats the most recent action.
- Cmd+Shift+P keeps ordering all of the tab's recent commands first, not only three, since its top three are still the three most recent.
- A tab duplicated by the browser starts with a copy of the source tab's history; that is how `sessionStorage` behaves and it is acceptable.

### Deferred / Open Questions

Non-blocking; built as stated above, for the requester to correct after review.

- Should empty Cmd+P then Enter keep opening the first session instead of running the top recent command?
- Should "most used" mean a usage count rather than recency? A count would change the stored `command-history` format.

## Implementation Units

### U1. Per-tab history and recent commands in Cmd+P

- **Goal:** Satisfy R1 through R4.
- **Requirements:** R1, R2, R3, R4.
- **Dependencies:** none.
- **Files:**
  - `internal/rocketclaw/web/src/ui.tsx` (`CommandPalette` history read and write; `paletteRows` command list)
  - `internal/rocketclaw/web/src/session-list.browser.test.ts`
  - `internal/rocketclaw/web/src/session-commands.browser.test.ts`
  - `internal/rocketclaw/web/src/opencode.browser.test.ts`
- **Approach:**
  1. In `CommandPalette`, read and write `command-history` through `sessionStorage` (KTD1).
  2. In `paletteRows`, hoist the recency rank so the `sessions` mode with an empty query can take the top three recent commands from the sorted list (KTD2), and limit the Timeline exception to typed queries (KTD4). Update the comments that say an empty query lists sessions only, or that running a level never moves it up.
  3. Point the browser tests that seed or read `command-history` at `sessionStorage`, and update the test comment "sessions only when empty".
- **Patterns to follow:** the existing `rank` and `tier` sort in `paletteRows`; the existing seeding in `session-list.browser.test.ts` near "The search button opens one palette".
- **Test scenarios:**
  - Covers R3. Seed four recent keys for commands that exist; open Cmd+P with an empty query: the first three rows are those commands in recency order, then sessions.
  - Covers R3. Press Enter in that empty Cmd+P: the most recent command runs.
  - Covers R3. Seed a recent key for a command not available (a folder key or an unknown key) first: it is skipped and the next available recent command leads.
  - Covers R3. The existing check near "The search button opens one palette", which seeds `tab-close-others`, now expects "Tabs: Close others" to lead the empty palette above the sessions.
  - Covers R4. In a tab with no `command-history` in `sessionStorage`, empty Cmd+P shows no command rows.
  - Covers R2. Run a Timeline level from Cmd+Shift+P, reopen it with an empty query: that level is the first row.
  - Covers R1. In the first tab, run a command that is not the default top row; a second page in the same browser context opens Cmd+Shift+P and its first row is not that command.
  - Covers R2. The existing Cmd+Shift+P assertions (most recent first, survives reload, unmatched search leaves history unchanged) pass against `sessionStorage`.
- **Seam:** browser tests catch ordering, Enter behavior and storage scope end to end; they do not cover mobile tab duplication.
- **Verification:** the scenarios above pass in `make -C internal/rocketclaw/web test`, and lint passes.

## Verification Contract

| Check | Command | Proves |
| --- | --- | --- |
| Web lint (eslint, tsc, react-doctor) | `make -C internal/rocketclaw/web lint` | no type or complexity regressions |
| Web browser tests | `make -C internal/rocketclaw/web test` | R1 to R4 scenarios and existing palette behavior |
| Repo lint and CLOC budget | `make lint` | budgets hold |

## Definition of Done

- R1 to R4 hold, shown by the U1 scenarios.
- No remaining `localStorage` reads or writes of `command-history` in source or tests.
- Comments in `paletteRows` describe the new empty-query behavior.
- No abandoned-attempt code in the diff.
