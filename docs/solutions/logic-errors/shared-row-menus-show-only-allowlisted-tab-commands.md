---
title: Shared Row Menus Show Only the Tab Commands Their Allowlist Names
date: 2026-11-20
category: docs/solutions/logic-errors/
module: internal/rocketclaw/web
problem_type: logic_error
component: frontend
symptoms:
  - "Right-clicking a Shared row offered no Move to space, although the same session's own tab did."
  - "A tab command added to the tab menu never appears on Shared rows, and no test or error says so."
root_cause: logic_error
resolution_type: code_fix
severity: medium
tags: [web, tabs, shared-group, tab-menu, context-menu, move-to-space, menu-items, adopt, spaces]
---

# Shared Row Menus Show Only the Tab Commands Their Allowlist Names

## Problem

A Shared row's context menu is built from the same `commands` list as a person's own tab, then filtered through a fixed list of command keys. A command whose key is not on that list never shows on Shared rows. "Move to space" and "Restore to" were missing for that reason.

## Symptoms

- Right-clicking a row in a space's Shared group offered Pull requests, Rename session, Copy link, Remove from Shared, the attention items and Add to group, but no Move to space.
- The same session's own tab in the strip did offer Move to space, so the gap looked like a bug in spaces, not in the menu.

## What Didn't Work

- Reading the checkout first. It was behind the deployed build and had neither Unfollow nor Add to group on Shared rows, so it did not match the reported menu. Reading the code on the latest `main` was the first useful step.
- Searching for where Shared-row menus are built by label. The labels ("Move to space") come from `commands`, which builds them for every tab. The filter that drops them is in `menuItems`, about 270 lines away, and matches keys, not labels.

## Solution

Three changes in `internal/rocketclaw/web/src/ui.tsx`, plus tests:

- `menuItems` (`ui.tsx:1728`) filters a Shared row's commands with a key allowlist (`ui.tsx:1733`). `restore|space` was added to it:

  ```ts
  /^(link|unshare|duplicate|read|unread|park|unpark|follow|unfollow|group|restore|space)/
  ```

- A Shared row may have no tab of the person's own. For it, `selected` is the Shared location itself (`ui.tsx:1448`), and `adopt` adds the selected locations to the person's tabs (`ui.tsx:1453`). `moveTo` only changes tabs that already exist (`ui.tsx:1286`), so the move commands now call `update(adopt)` first, as the group commands already did.
- A Shared row that is not in the strip, such as one only in Shared or a parked one, takes the early return at `ui.tsx:1476`, which adds only the list now named `placing` (`ui.tsx:1454`). The Restore and Move to space commands moved into that list, so they reach every Shared row. Restore now compares the Birth Space with the Shared row's space when the person has no tab of it (`ui.tsx:1464`).

Moving from a Shared row changes only the person's own tab, like every tab move, and the row stays in Shared for everyone. The one exception is the one every move already has: moving a Hosted Page tab also moves the owning session's Birth Space through `movePages` (`ui.tsx:1275`).

Tests in `internal/rocketclaw/web/src/tabs.browser.test.ts`: the new test at line 2197 moves a Shared row to another space and checks that the person's tab lands there and the row stays in Shared. The parked Shared row test's full menu now lists Move to space.

## Why This Works

The tab menu and the Shared-row menu come from one `commands` function, but they decide what to show in two ways. A person's own tab shows everything `commands` returns. A Shared row shows only keys on the allowlist. The allowlist was the only thing between the existing move commands and Shared rows, and `adopt` covers the case the move commands did not handle: a location the person has no tab of.

The same gap has been fixed one command at a time several times:

- PR #1440 added `group` to the allowlist and introduced `adopt`.
- PR #1455 added `follow|unfollow`.
- PR #1525 made the not-in-strip branch offer the group commands to parked Shared rows.
- This change added `restore|space`.

## Prevention

- When you add a command to `commands`, decide whether Shared rows should offer it. If yes, add its key prefix to the allowlist in `menuItems`. If it acts on the person's tabs, make it work when the person has no tab of the location (`update(adopt)` first). If it should also show on parked or Shared-only rows, put it in the list the not-in-strip branch returns.
- Check it with `sharedMenu` in `tabs.browser.test.ts` on a Shared row the person has no tab of, with at least two spaces. Move to space lists only other spaces, so it does not show at all in a one-space test.
- Owner follow-up, not done here: the allowlist is the reason this keeps recurring. Filtering a Shared row's commands by what it must not offer (closing, Add to Shared, and Delete session, which `menuItems` already adds only for non-Shared tabs) would make new commands appear on Shared rows by default. That changes which commands Shared rows offer, so it is a product decision.

## Related Issues

- `docs/solutions/logic-errors/moving-a-page-tab-must-move-its-sessions-birth-space.md`: what Move to space does for Hosted Page tabs.
- `docs/solutions/architecture-patterns/a-per-tab-attention-flag-must-keep-its-row-on-server-and-in-browser.md`: the Follow/Unfollow data side.
- `docs/solutions/architecture-patterns/kept-tab-groups-hold-their-place-with-a-placeholder-tab.md`: the group commands that share the `placing` list.
- `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`: rebuild the bundle before running `tabs.browser.test.ts`.
- `CONCEPTS.md`: Shared Group, Birth Space.
