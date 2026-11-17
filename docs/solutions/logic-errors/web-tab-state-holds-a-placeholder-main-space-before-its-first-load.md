---
title: Web Tab State Knows Only a Placeholder Main Space Until the Saved Tabs Arrive
date: 2026-10-31
last_updated: 2026-11-17
category: docs/solutions/logic-errors/
module: internal/rocketclaw/web
problem_type: logic_error
component: frontend
severity: medium
symptoms:
  - A choice that depends on the space, such as which space to enter or whether a page needs a tab, is wrong after a reload or after arriving from another server
  - The bug shows only in spaces other than Main; tests that use Main pass
root_cause: logic_error
resolution_type: code_fix
tags: [web, tabs, spaces, first-load, arriving-space, remote-rocketclaws, use-tabs]
---

# Web Tab State Knows Only a Placeholder Main Space Until the Saved Tabs Arrive

## Problem

`useTabs` in `internal/rocketclaw/web/src/ui.tsx` starts from `loadTabs(pathname)` with no saved tabs. Until the server's Tabs reply arrives, `savedTabs` fills in a placeholder: `state.spaces` is `[{ id: "main", name: "Main" }]` and `state.space` is `"main"`. Anything that reads `state.spaces` or `state.space` before the first load sees every other space as unknown.

Only Main has a fixed id: the baseline migration inserts it as `'main'` (`internal/rocketclaw/backend/migrations/001_baseline.sql:492`), and every other space gets a random UUID (`001_baseline.sql:379`). So a check against the placeholder passes for Main and fails for every other space.

## Symptoms

- After a reload, or after moving to another server, the page lands in Main or treats a page in another space as if that space did not exist.
- Tests that only use Main pass, because Main's id is the one the placeholder has.

## What Didn't Work

- Deciding space-dependent things from `state` during render, or in any code that runs before the first load. That `state` is still the placeholder, so every non-Main space falls back to Main.
- History: the removed Top Shelf (commit 91fd75f767b6, migration `101_drop_top_shelf.sql`) hit this first. It mapped each shelved tab's space against `state.spaces` and moved unknown ones to the current space. On the first load every non-Main item went to Main. Its browser test passed, because it shelved only from Main.

## Solution

Decide anything space-dependent inside the load that has the saved reply, never from render-time state.

`useTabs` runs its first load in its merge effect. The effect does nothing until `saved.data` is present. Then, because the placeholder state has no owner yet, it calls `loadTabs(pathname, owner, saved.data, shared)` for the page's own server (another server's tabs, shown on its card, take `savedTabs(owner, saved.data)`). `loadTabs` builds its state with `savedTabs(owner, saved)`, so it sees the real spaces.

The current instance is the arriving space. When a person picks a space or tab on another server's card, `enter`, `newIn` and `go` (and `useVisit` for a tab on another server) store the target space in the module-level `arriving` and navigate to that server. They do not try to set the space themselves. On that server's page, `useTabs` first renders the placeholder and leaves `arriving` alone, because `loadTabs` touches `arriving` only when it gets saved tabs. When the saved reply arrives, `loadTabs` enters `arriving` if it is one of the real spaces, and then clears `arriving` either way.

## Why This Works

The saved reply is the only source of real space ids before `setState(loadTabs(...))` replaces the placeholder. Values computed during the render that runs the first load come from the old `state`, so they must get their spaces from `saved.data`, as `loadTabs` does. If `arriving` were checked against `state.spaces` at the first render, it would find only Main, drop the chosen space, and open the page in Main.

## Prevention

- Before computing anything in `useTabs` from `state.spaces` or `state.space`, ask whether the first load uses it. If it does, move the decision into `loadTabs`, or take spaces from `saved.data`.
- Test space-dependent tab behavior in a space other than Main. In `internal/rocketclaw/web/src/remote-transport.test.ts`, the "enter spaces" stage creates a "Side" space locally and a "Billing" space on the remote, enters each through its card, and checks that each server saved it as the current space. Entering Billing on the remote goes through `arriving`.
- Switching space saves asynchronously. After switching, poll `/api/Tabs` until the saved `state.space` is the new space before navigating with `page.goto`, or the page opens in the old space. The `current` helper in that stage does this.

## Related Issues

- `docs/solutions/architecture-patterns/tailnet-pages-calling-another-rocketclaw.md`: pin the server before an `await` in the same tab code.
- `docs/solutions/architecture-patterns/kept-tab-groups-hold-their-place-with-a-placeholder-tab.md`: a different placeholder, the tab a kept group holds.
