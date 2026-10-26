---
title: "fix: Keep host shortcuts active in embedded terminals"
date: 2026-10-26
type: fix
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Keep host shortcuts active in embedded terminals

## Goal Capsule

**Objective:** People can use RocketClaw's global shortcuts while working anywhere in an embedded session.
**Means:** Claim host shortcuts in the existing capture listener (KTD1).
**Authority:** The requested host-first behavior supersedes the documented terminal exception; other behavior stays unchanged.
**Execution:** One bounded change, test-first. Implementation, local verification, review, and publication belong to the executor.
**Stop conditions:** Required checks cannot run, or evidence shows the existing listener cannot deliver the requested precedence without a broader change.

## Product Contract

### Summary

Keep RocketClaw's global shortcuts active when an embedded terminal has focus.

### Problem Frame

The host listener explicitly returns for terminal targets, so the terminal receives shortcuts that the surrounding application otherwise owns.

### Requirements

- R1. The existing global palette, sidebar, new-session, and tab-navigation shortcuts take precedence inside the embedded view, including its terminal, without also sending input to the terminal. Repeated keydown events are consumed without repeating the host action.
- R2. Keys not bound by RocketClaw remain available to the embedded view; Escape still does not close a remote tab.
- R3. Update the existing browser coverage and Web README to describe the new precedence.

### Scope Boundaries

No new shortcuts, keybinding framework, copied upstream changes, server API changes, or data changes.
Do not change browser refresh bindings or terminal-specific shortcuts that have no host binding.

## Planning Contract

### Key Technical Decisions

- KTD1. **Reuse the window capture listener.** Remove its terminal-target exemption and delete the unused predicate. Prevent default and stop propagation for matched host shortcuts, including repeated events; execute each action only for a non-repeated event. The embedded command handler observes cancellation, but the terminal library does not, so cancellation alone cannot prevent shell input.
- KTD2. **Extend the existing real-browser scenario.** `internal/rocketclaw/web/src/opencode.browser.test.ts` already opens a terminal and tests the old exception; replace that assertion with host-palette checks, preserving the non-conflicting embedded palette and socket-lifecycle checks.

No new abstractions or runtime dependencies are needed.

## Implementation Units

### U1. Apply host shortcut precedence throughout the embedded view

**Goal:** Deliver R1–R3 using KTD1 and KTD2.
**Dependencies:** None.
**Files:** `internal/rocketclaw/web/src/ui.tsx`, `internal/rocketclaw/web/src/opencode.browser.test.ts`, `internal/rocketclaw/web/README.md`, and rebuilt `internal/rocketclaw/internal/web/dist`.
**Approach:** Remove the exemption, consume matching events in the host listener, update the terminal assertions, and correct the README's terminal-focus rule.
**Execution note:** First observe the updated browser expectation fail against the unchanged bundle, then change production code and rebuild before retesting.
**Patterns to follow:** Existing window capture handling, palette locators, and real embedded terminal fixtures.
**Test scenarios:**
1. Focus the terminal, press the host session-palette shortcut with Control and Meta, and see the host palette rather than the embedded palette, with no terminal input.
2. Refocus the terminal, press the host command-palette shortcut, and see the host command palette without terminal input. Repeated host chords produce neither another action nor terminal input.
3. An ordinary terminal key still reaches its socket; the existing embedded palette shortcut from the composer and terminal socket cleanup still work.
**Verification:** The updated browser scenario fails before the change and passes afterward, with the rest of the Web suite passing.

## Verification Contract

Run Web lint and Web tests, rebuilding the tracked bundle before browser tests.
Run the repository's lint and test targets, including unchanged coverage and source-line budgets, with a dedicated Docker PostgreSQL 18 instance and all scratch output under `.tmp/`.
Inspect the final diff to ensure no copied upstream, backend, fixture data, or unrelated behavior changed.

## Definition of Done

R1–R3 hold, all required local checks pass, the bundle matches source, and independent review finds no blocking issue.
No abandoned experiments or unrelated edits remain.
