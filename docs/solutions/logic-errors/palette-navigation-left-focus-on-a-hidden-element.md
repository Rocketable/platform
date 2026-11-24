---
title: Palette Navigation Left Focus on a Hidden Element, So Pages Ignored the Keyboard
date: 2026-11-24
category: docs/solutions/logic-errors/
module: internal/rocketclaw/web
problem_type: ui_bug
component: frontend
symptoms:
  - "A page opened from the command palette (Settings, Shortcuts, Cron) does not scroll with PageDown or the arrow keys"
  - "Tab after opening a page from the palette jumps to the tab sidebar instead of into the page"
  - "document.activeElement is body after the palette closes"
root_cause: logic_error
resolution_type: code_fix
severity: medium
tags: [focus, command-palette, base-ui, final-focus, keep-alive, tab-pane, keyboard-scroll, accessibility]
---

# Palette Navigation Left Focus on a Hidden Element, So Pages Ignored the Keyboard

## Problem

Opening a page from the command palette left keyboard focus on `document.body`. The page drew, but PageDown and the arrow keys did nothing, and Tab went to the tab sidebar instead of the page.

## Symptoms

- After Cmd/Ctrl+Shift+P → Settings, PageDown does not scroll the Settings page.
- The first Tab lands on the tab strip's "Tab actions" button.
- Closing the palette without choosing anything keeps working as before.

## What Didn't Work

No other fix was tried. One alternative was rejected on reasoning, without a test: focusing `main`. Each page scrolls its own `overflow-y-auto` div *inside* `main` (for example `internal/rocketclaw/web/src/ui.tsx:6598`), and keyboard scrolling looks for a scroller at or above the focused element, so focus on `main` would let Tab into the page but would likely leave PageDown dead. In a Chromium check during the fix, focus on `body` did not scroll the page, and focus on the heading inside the page's scroller did.

## Solution

The palette is a Base UI `Dialog`. By default, closing it returns focus to the element that was focused before it opened (`finalFocus` in `@base-ui/react` `DialogPopup.d.ts`: "`true`: Move focus based on the default behavior (trigger or previously focused element)"). That element is usually the session's message box.

Two changes in `internal/rocketclaw/web/src/ui.tsx`:

- `PageTitle`'s heading is focusable from script and draws no outline (`ui.tsx:3962`):

  ```tsx
  <h1 tabIndex={-1} className="text-lg font-semibold outline-none">{children}</h1>
  ```

- The palette's `DialogContent` keeps the default return focus when the address did not change, and otherwise focuses the visible page heading (`ui.tsx:3015`):

  ```tsx
  finalFocus={() => at() === origin.href || ([...document.querySelectorAll<HTMLElement>("main h1[tabindex]")].find((title) => title.checkVisibility()) ?? true)}
  ```

`tabs.browser.test.ts:225` checks that the Config heading has focus after choosing Settings from the palette. Without the `finalFocus` change it times out.

## Why This Works

Pages and the chat pane stay mounted while hidden. `TabPane` hides with `display: none` (`ui.tsx:491`–`492`), and `WarmTabs` keeps Cron, Agents, Skills and Config mounted (`ui.tsx:496`). So when the palette navigates, the element it wants to give focus back to is still in the DOM but hidden. Focusing a `display: none` element does nothing, and focus falls to `body`.

The page heading sits inside the page's scroll container. With the heading focused, PageDown and the arrow keys scroll that container, and Tab moves to the next control in the page. Comparing `at()` with `origin.href` keeps the old behavior for every close that did not navigate, including Escape after an arrow-key preview, because `back()` restores the original address first (`ui.tsx:2914`).

## Prevention

- Any overlay that navigates (a Dialog, Menu or Popover) and keeps Base UI's default `finalFocus` can hit the same bug, because the element it returns focus to may now be in a hidden `TabPane` or warm page. Give it a `finalFocus` that picks something visible in the destination.
- A new page should render its title through `PageTitle` so the palette can focus it. Pages without one (Search, hosted pages, the agent editor) and sessions fall back to the default return focus. That was not tested here, but it should still land on `body` whenever the opener is hidden.
- When testing focus, assert it on the element itself, for example `page.getByRole("heading", { name }).and(page.locator(":focus"))`. A test that only checks the URL passes while the keyboard is dead.

## Related Issues

- `docs/solutions/architecture-patterns/web-keep-alive-views-connection-budget.md`: why views stay mounted while hidden, and its audit for effects that assume mounted means visible. This bug is the opposite direction: an overlay sending focus *into* a view that just became hidden.
- `docs/solutions/architecture-patterns/fence-the-hidden-opencode-view-and-test-it-where-it-can-fail.md`: keys pressed while focus is on `body` can reach a hidden OpenCode view, another reason not to leave focus there.
- `docs/solutions/best-practices/react-doctor-complexity-limit-shapes-web-components.md`: `CommandPalette` is near react-doctor's complexity limit, so moving the `finalFocus` logic into the component body may need a helper.
