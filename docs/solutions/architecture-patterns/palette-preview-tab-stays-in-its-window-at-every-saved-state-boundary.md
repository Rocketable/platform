---
title: The Palette Preview Tab Stays in Its Window at Every Saved-State Boundary
date: 2026-11-03
last_updated: 2026-11-17
category: docs/solutions/architecture-patterns/
module: internal/rocketclaw/web
problem_type: architecture_pattern
component: frontend
severity: medium
applies_when:
  - Changing what useTabs saves, merges, or derives from TabState (savedState, openTab, flush, the saved-state merge effect)
  - Adding a field to the saved tab state that is computed from the tab list or the active tab
  - Adding another tab or tab field that must stay in one browser window
  - Writing a Web browser test that relies on a palette arrow press, or any other navigation, to trigger a tab save
related_components:
  - internal/rocketclaw/web/src/tabs.browser.test.ts
tags: [web, tabs, preview-tab, palette, saved-tabs, tab-state, window-local, use-tabs]
---

# The Palette Preview Tab Stays in Its Window at Every Saved-State Boundary

## Context

PR #934 (merged 2026-11-03 as "give every opened location a tab of its own") removed VS Code-style preview tabs. Every opened location now gets an ordinary tab. One preview stays: arrowing through the Ctrl+P session list shows each session live in one italic tab. Enter keeps it as an ordinary tab. Escape, typing, or a command closes it. The person asked for it to be "isolated at browser level": the server, other windows, and other devices must never see it.

The preview still lives in `TabState` as a `Tab` with `preview: true` (`internal/rocketclaw/web/src/ui.tsx:960`). Strip drawing, arranging, and the active tab all work on that list unchanged. The catch is that `TabState` is also what gets saved and merged. So the preview has to be stopped at every point where window state becomes saved state, or saved state becomes window state. Dropping it from the saved tab list was the first fix, and it was not enough. Review and testing found four more problems, one at a time.

## Guidance

Treat the preview as window-only at each of these five boundaries. All five are needed:

1. **The saved tab list.** `savedState` drops tabs with `preview` (`ui.tsx:1075`).
2. **Values derived from tabs.** `savedState` picks which groups to keep from the tabs it saves, not from `state.tabs` (`ui.tsx:1076`). It was added so a group whose only member was the preview would not be saved. Since every group now also holds a saved placeholder tab, that case is gone, but the filter still drops a deleted group, and any new value derived from tabs should come from the saved tabs too.
3. **`last` in `openTab`.** A preview open leaves `last` alone: `last = preview ? state.last : { ...state.last, [current]: path }` (`ui.tsx:1023`). Before this, the preview tab itself was never saved, but the space's last tab was saved as the previewed session, which other windows and devices read.
4. **Saves that change nothing.** `flush` skips a save whose saved form is identical to `queue.base` (`ui.tsx:1241`). Arrowing only changes the window's state. Without the skip, each arrow press sent an identical save. Each save rewrites the person's tabs on the server and bumps the revision, which makes every other window merge.
5. **Putting the preview back after a merge.** The merge effect builds the new tabs from the saved form, so the preview is always missing from the result. The effect puts this window's preview back where it was, unless the merged tabs already have that location as an ordinary tab or the preview's space is gone (`ui.tsx:1225-1227`):

   ```ts
   const next = savedTabs(owner, { spaces: saved.data.spaces, state: mergeTabs(base!, savedState(state), saved.data.state) }), preview = state.tabs.find((tab) => tab.preview);
   const placed: Tab[] = next.tabs.map((tab) => ({ ...tab, unplaced: state.tabs.find((item) => item.path === tab.path)?.unplaced }));
   const tabs = preview && !placed.some((tab) => tab.path === preview.path) && next.spaces.some((item) => item.id === preview.space) ? arrange(placed.toSpliced(state.tabs.indexOf(preview), 0, preview)) : placed;
   ```

   Without this, another window's change arriving while the palette was open removed the italic tab. The window then went to the space's first tab, with the palette still open on the previewed row.

These related rules come from the same work:

- **Any open that does not take the preview slot closes the preview** (`ui.tsx:1021-1022`). The first version closed it only on a non-preview open. Arrowing onto a session that was already open as an ordinary tab then left a stray italic tab behind.
- **The preview open replaces the history entry.** `open(..., preview = true)` calls `navigate(path + query, true)` (`ui.tsx:1337`). The palette's `peek` uses it, so browsing the palette adds no history entries (`ui.tsx:2848-2853`).
- **Moving a tab removes its `preview` flag** (`relocate`, `ui.tsx:1043`), and so do space deletion (`leave`) and moving tabs or groups to another space. If a space is deleted in another window while you arrow, the preview becomes an ordinary saved tab in the first space. This rare case was left as is on purpose: the window is showing that session anyway.

## Why This Matters

None of these problems fails loudly. The saved tab list was clean after the first fix, and the browser tests passed. The first version of the palette test even waited for the leaked `last` save, so the test treated the leak as expected behavior. The others only showed up through review or a recording:

- the `last` leak and the dropped preview after a merge: found while making the preview window-only;
- identical saves on every arrow press: found by an efficiency review;
- the group derived from all tabs: found by a code-quality review.

Today's comments in `ui.tsx` no longer list the boundaries. A later cleanup pass deleted them.

Two notes on the identical-save skip:

- **Tests that used an arrow press to trigger a save stop working.** "palette search handoffs stay in their opening space" held the personal save that an arrow press started. After the skip, no save came, and the test timed out at 30 s. It now moves a tab with Shift+ArrowDown to make the save (`tabs.browser.test.ts:1417-1423`). At 390px the strip sits in the closed drawer, so the test opens it with Show sidebar first. A test that needs a tab save has to make a change the saved form can see.
- **The skip compares `JSON.stringify` output.** When `queue.base` comes from this window's own save, it compares like with like. After a merge, `queue.base` is the server's reply until this window saves again. The Go server encodes that reply with protojson and `EmitDefaultValues` (`internal/rocketclaw/frontend/rpc/http.go:68`), which writes empty fields such as `"group": ""`. `savedState` leaves those fields out. Reading the code, the first preview after a merge can therefore still send one save. That save has identical content and carries no preview. No test covers this. The test server in `tabs.browser.test.ts` stores the client's own JSON, so its encoding always matches.

## When to Apply

- Adding a saved field that is computed from tabs or from the active tab, such as another per-space pointer or another derived list. Decide what it should be while a preview is showing. Usually it should ignore the preview.
- Changing `mergeTabs`, the merge effect, `savedTabs`, or `flush`. The merge output never contains the preview, so any new rebuild path has to put it back or drop it on purpose.
- Adding another kind of window-only tab state. It needs the same five boundaries. Kept-group placeholders are the opposite case: they are saved, and readers must skip them.
- Writing a test that needs a tab save. Do not rely on palette navigation to cause one.

## Examples

The browser test "a palette preview stays in its window: never saved, and kept while other windows' changes arrive" (`tabs.browser.test.ts:322-342`) covers boundaries 1, 3, 4 and 5:

- It checks that `saved.revision` stays the same after an arrow press.
- It adds a tab to the fake server's state, then sends a window `focus` event.
- It checks that the italic tab and the URL stay put, and that Escape returns to the starting tab.

During the work, it was confirmed to fail with either the `last` fix or the merge fix undone. Boundary 2, the group filter, has no test of its own.

The cheapest way to cover all five would be a unit check that does not need a browser. It would check that the saved form of a state is the same with or without its preview tab, and that a preview `openTab` leaves `last` unchanged. It needs no product change: `tabs.test.ts` already lifts `tabsCall` out of `ui.tsx` through the TypeScript API without exporting it, and the same technique can lift `savedState` and `openTab` with their helpers. It has not been done.

When testing a reverted fix by hand, rebuild before each run. Browser tests serve the last built `dist`, not `internal/rocketclaw/web/src/`. This session lost two runs to a stale bundle (see `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`).

## Related

- `docs/solutions/architecture-patterns/kept-tab-groups-hold-their-place-with-a-placeholder-tab.md`: the placeholder tab that is saved and must be skipped by readers, the opposite direction of this one.
- `internal/rocketclaw/web/README.md`, Tabs: the user-facing description of the palette preview.
- PR #934 (commit "give every opened location a tab of its own"): removes saved preview tabs (migration `081_drop_web_tab_preview.sql`, `preview` reserved in `web.proto`) and adds the boundaries above.
