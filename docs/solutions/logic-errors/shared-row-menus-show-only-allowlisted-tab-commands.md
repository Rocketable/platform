---
title: Shared Row Menus Missed New Tab Commands Because They Filtered the Tab Menu
date: 2026-11-20
last_updated: 2026-11-20
category: docs/solutions/logic-errors/
module: internal/rocketclaw/web
problem_type: logic_error
component: frontend
symptoms:
  - "Right-clicking a Shared row offered no Move to space, although the same session's own tab did."
  - "A tab command added to the tab menu did not appear on Shared rows, and no test or error said so."
  - "The same tab offered different items from its Shared row, its own row and the page bar's menu."
root_cause: logic_error
resolution_type: code_fix
severity: medium
tags: [web, tabs, shared-group, tab-menu, context-menu, move-to-space, menu-items, adopt, spaces, parked, menu-parity]
---

# Shared Row Menus Missed New Tab Commands Because They Filtered the Tab Menu

## Problem

Every tab menu on the page's server is built by one `commands` function in `internal/rocketclaw/web/src/ui.tsx`, but before the 2026-11-20 menu unification (the change this update ships with, unmerged as of this writing) its result was cut down differently depending on where the menu opened. A Shared row's menu kept only the command keys on a fixed allowlist, and rows outside the person's strip (parked tabs, Shared-only rows) took an early return that dropped more. Each new tab command was therefore missing from Shared rows until someone added its key to the allowlist.

## Symptoms

- Right-clicking a row in a space's Shared group offered Pull requests, Rename session, Copy link, Remove from Shared, the attention items and Add to group, but no Move to space.
- The same session's own tab in the strip did offer Move to space, so the gap looked like a bug in spaces, not in the menu.
- The person reported it as whack-a-mole: each fix added one more command to Shared rows, and the next new command was missing again.

## What Didn't Work

- Reading the checkout first. It was behind the deployed build and had neither Unfollow nor Add to group on Shared rows, so it did not match the reported menu. Reading the code on the latest `main` was the first useful step.
- Searching for where Shared-row menus are built by label. The labels ("Move to space") come from `commands`, which builds them for every tab. The filter that dropped them was in `menuItems`, about 270 lines away, and matched keys, not labels.
- Adding the missing keys to the allowlist, one command at a time:
  - PR #1440 added `group` and introduced `adopt`.
  - PR #1455 added `follow|unfollow`.
  - PR #1525 made the not-in-strip branch offer the group commands to parked Shared rows.
  - PR #1569 added `restore|space` and moved Restore and Move to space into `placing`.
- A denylist (drop only what a Shared row must not offer) was proposed in this doc's first version and rejected. It still keeps a second way to build a menu, so a new command that should differ on Shared rows would again need someone to remember the list.

## Solution

The menu no longer depends on where it opened, only on the tab:

- `menuItems` (`ui.tsx:1725`) calls `tabs.commands(menu.path, view, targets, menu.space)` for every menu (`ui.tsx:1730`). The `menu.shared` branch with the key allowlist is gone, as is the `!menu.shared` condition on Delete session and the `menu.shared ||` shortcut on renaming and Pull requests (`pullRequestItems`, `ui.tsx:1733`).
- `commands` has no early return for rows outside the strip. Only the close commands depend on where the tab is (`ui.tsx:1474`), and the rest always follow (`ui.tsx:1480`):

  ```ts
  const closings = !shownOf(space).some((item) => item.path === path) ? tab ? [closing] : [] : [
    closing, /* Close others, Close to the right, Close all */
  ];
  return [...closings, link, ...share, ...copy, ...attention, ...placing];
  ```

- The Shared row, the person's own tab, a Parked row of the page's server and the page bar's **…** (`TabTitle`, `ui.tsx:863`) all reach `menuItems`, so they list the same items for the same tab. Another server's Parked rows get only Pull requests and the attention items (`ui.tsx:2180`).

What the menu offers now:

- The four close commands while the tab is in the person's strip; Close alone for a tab of theirs that is not in the strip, such as a parked one; none for a Shared-only row.
- Add to Shared or Remove from Shared, by whether the tab is shared in its space.
- Rename for a session or Search page, and Regenerate title and Pull requests for a session, when the menu acts on one tab. Delete session too when that session is on the page's server.
- The attention items for every tab, and the group commands, Restore and Move to space wherever they apply, now including parked tabs of the person's own, which used to lack those three.

## Why This Works

The allowlist and the early return were the only things that made a tab's menu differ by surface. With them gone there is one way to build a menu, so a command added to `commands` shows everywhere it applies without anyone remembering a second list.

The close commands stay gated on strip membership. For a row the person has no tab of, the reason is easy to miss. `close` only closes the person's own tabs, and Close to the right slices from `there.findIndex(path) + 1`. For a path the person has no tab of, `findIndex` is `-1`, so the slice starts at `0` and Close to the right would close every tab of theirs in the space.

Commands that act on the person's tabs must work for a location they have no tab of. For a Shared-only row, `selected` is the Shared location itself (`ui.tsx:1448`), and `adopt` adds it to the person's tabs (`ui.tsx:1453`) before the group and move commands change it. `moveTo` alone only changes tabs that already exist (`ui.tsx:1286`).

One behavior followed from the unification: when the Shared row's path is also among the person's selected tabs, its menu acts on the whole selection, as the same tab's own menu does.

## Prevention

- Do not branch a tab menu on where it opened. Gate an item on a fact about the tab: whether it is in the person's strip, whether they have a tab of it, whether it is shared, whether it is a session, or how many tabs the menu acts on.
- A new command in `commands` must work when the person has no tab of the location (`update(adopt)` first) and when the tab is parked.
- `tabs.browser.test.ts` ("Shared hides while empty…", `tabs.browser.test.ts:1767`–`1769`) checks that a Shared row's menu, the same tab's own menu and the page bar's "Page actions" menu list the same items. Against the pre-fix `ui.tsx` it fails, because the Shared row lacked the close commands and Delete session.

## Related Issues

- `docs/solutions/logic-errors/moving-a-page-tab-must-move-its-sessions-birth-space.md`: why Move to space once moved a Hosted Page's session, and why it no longer does.
- `docs/solutions/architecture-patterns/a-per-tab-attention-flag-must-keep-its-row-on-server-and-in-browser.md`: the Follow/Unfollow data side.
- `docs/solutions/architecture-patterns/kept-tab-groups-hold-their-place-with-a-placeholder-tab.md`: the group commands in the `placing` list.
- `docs/solutions/architecture-patterns/web-acts-on-remote-opencode-sessions-through-the-api-proxy.md`: where Delete session is added to the tab menu.
- `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`: rebuild the bundle before running `tabs.browser.test.ts`.
- `CONCEPTS.md`: Shared Group, Birth Space.
