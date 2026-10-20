---
title: Running tab title shimmer - Plan
type: feat
date: 2026-10-19
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Running tab title shimmer - Plan

## Goal Capsule

- Objective: a person scanning the tab strip, at the top or in the left sidebar, sees which session tabs have a running turn without the tab's title moving.
- Means: the tab title gets the existing `shimmer` gradient while its session runs, and the running dot is removed (KTD1).
- Authority: this plan, then the project instructions in `AGENTS.md`.
- Stop conditions: the TS CLOC budget would be exceeded, or `shimmer` cannot be applied to the title text.
- Execution profile: one small UI change in `internal/rocketclaw/web/src/ui.tsx`, its browser tests, and the web README.

## Product Contract

### Summary

A running session tab shows its title with the moving gradient instead of a white dot. The dot before the title and the dot in the close button's slot both go away.

### Problem Frame

When a session's turn runs, the active tab (and every tab on touch screens) shows a white dot before the title, which pushes the title to the right. The text jumps each time a turn starts or ends.

### Requirements

**Running indicator**

- R1. While a tab's session has a running turn, the tab title shows the moving gradient, in both the top strip and the left sidebar placement, active or not. A pinned tab in the top strip, which shows no title, pulses its icon in place instead.
- R2. The running indicator does not shift the title or change the tab's width.
- R3. No running dot appears anywhere on the tab.

**Close button and screen readers**

- R4. A running tab's close button behaves like any other tab's close button.
- R5. Screen readers still hear "Turn running" for a running tab.

### Key Decisions

- Gradient replaces both dots, chosen over keeping the close-slot dot on inactive tabs: two indicators for one state is redundant, and the request is to show running through the text. Governs R3, R4.

### Scope Boundaries

- Not changed: the spinner in session list rows of the palette and sidebar session list (`SessionRowContent`), which already sits in the metadata line and does not move the title.
- Considered and not built: a non-moving fallback for people who ask for reduced motion. The `shimmer` utility already turns into plain text there, as trace rows do today; R5 still covers screen readers. Evidence of people missing running state under reduced motion would change this.

## Planning Contract

### Key Technical Decisions

- KTD1. **Use the existing `shimmer` utility** from `shadcn/tailwind.css` (already imported by `internal/rocketclaw/web/app/globals.css`), as `components/ui/trace.tsx` and `components/ui/attachment.tsx` do. It paints a gradient through the text with `background-clip: text` and takes no layout space. (session-settled: user-directed — chosen over the white running dot: the dot pushes the title to the right.)
- KTD2. **Announce running through the tab's `aria-description`**, appending "Turn running" to the existing group/space description. The old in-tab dot sat inside `role="tab"`, whose children are presentational, so the description is the place a screen reader reliably reads it.
- KTD3. **`TabClose` loses its `running` prop and dot**, so its close button is always shown, as it already is for idle tabs.

### Assumptions

- The icon pulse for pinned top-strip tabs uses Tailwind's `animate-pulse`, turned off under reduced motion with `motion-reduce:animate-none` as the existing spinners in `ui.tsx` do. It changes opacity only, so it takes no layout space.
- An editing tab (rename field) shows no shimmer on the input.

## Implementation Units

### U1. Shimmer the running tab title and drop the dots

- **Goal:** R1–R5.
- **Requirements:** R1, R2, R3, R4, R5; KTD1, KTD2, KTD3.
- **Dependencies:** none.
- **Files:** `internal/rocketclaw/web/src/ui.tsx`, `internal/rocketclaw/web/src/tabs.browser.test.ts`, `internal/rocketclaw/web/README.md`.
- **Approach:**
  1. In `TabItem`, delete the `Turn running` dot span before the title and add `shimmer` to the title span's classes when `session.running`. When running and the title is hidden (pinned in the top strip), pulse the tab icon instead.
  2. Pass `aria-description` as the existing description plus "Turn running" when running, joined with ", ".
  3. In `TabClose`, remove the `running` prop, the dot span, and the hover-only opacity classes, and update its comment and its call site.
  4. Rewrite the README "Running turns" bullet in the Tabs section.
- **Patterns to follow:** `data-[status=running]:*:data-[slot=trace-title]:shimmer` in `internal/rocketclaw/web/src/components/ui/trace.tsx`.
- **Test scenarios:**
  - Replace "running tab shows a dot instead of close until hover or focus at desktop width": with Alpha active, inactive running Delta's title has a running animation (computed `animation-name` is not `none`), its close button has opacity 1 without hover, and the strip has no `Turn running` img. After activating Delta, the title still shimmers. Idle Alpha's title has no animation. The title's left edge sits the same distance from the tab icon for running Delta as for idle Alpha (R2).
  - A pinned running Delta in the top strip has an animated icon, and a pinned idle tab's icon is not animated.
  - In "narrow top strip scrolls and running tabs keep a reachable close control", replace the dot opacity assertion with Delta's `aria-description` ending in "Turn running".
  - Update the group test's Delta `aria-description` expectations to include ", Turn running".
- **Verification:** the scenarios above pass.

## Verification Contract

| Command (from `internal/rocketclaw/web`) | Proves |
|---|---|
| `make lint` | eslint, `tsc --noEmit`, react-doctor |
| `make test` | browser tests, including `tabs.browser.test.ts`, and the TS CLOC budget |

Repo root `go test ./...`, `make lint`, and `make test` also run, as `AGENTS.md` requires.

## Definition of Done

- U1 test scenarios pass, including the same title position for running and idle tabs (R2), and `Turn running` no longer appears as an img anywhere in the tab strip.
- No dead code from the dot remains (`TabClose`'s `running` prop and its hover-opacity branch).
- The web README describes the shimmer, not the dot.
