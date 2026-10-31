---
title: Web Tab State Holds a Placeholder Main Space Before Its First Load
date: 2026-10-31
category: docs/solutions/logic-errors/
module: internal/rocketclaw/web
problem_type: logic_error
component: frontend
severity: medium
symptoms:
  - A page that should open without a tab (Shared, Top Shelf) gets a tab on reload or on arriving from another server
  - The bug shows only for spaces other than Main; tests that use Main pass
root_cause: logic_error
resolution_type: code_fix
tags: [web, tabs, spaces, first-load, top-shelf, shared-tabs, use-tabs]
---

# Web Tab State Holds a Placeholder Main Space Before Its First Load

## Problem

`useTabs` starts from `loadTabs(pathname)` with no saved tabs (`internal/rocketclaw/web/src/ui.tsx`, `useTabs`), so until the server's Tabs reply arrives its `state.spaces` is the placeholder `[{ id: "main", name: "Main" }]` and `state.space` is `"main"` (`savedTabs`). Anything computed from `state.spaces` or `state.space` before the first load treats every other space as unknown. Only `main` has a fixed id; every space created later has a generated one.

## Symptoms

- Top Shelf: opening a shelved remote chat from a space named "Work", or reloading a shelved Search page in a non-Main space, added a tab, though shelved pages must open without one.
- The cross-server browser test passed, because it shelved only from Main.

## What Didn't Work

- The first version mapped each shelved tab's space against `state.spaces`, falling back to `state.space` for a space that no longer exists. On the first load that list is still the placeholder, so every non-Main item was moved to `"main"`. The first load (`loadTabs(pathname, owner, saved.data, reach)`) then entered the real space and found the page unreachable without a tab there.

## Solution

Read spaces from the saved reply when it exists: `useTabs` builds its list of tab-free pages (`reach`) from `saved.data?.spaces ?? state.spaces`. The first-load effect runs only once `saved.data` is present, so that call sees the real spaces. The same helper, given the reply's spaces, rebuilds the list after a space is deleted.

## Why This Works

The saved reply is the only source of real space ids before `setState(loadTabs(...))` replaces the placeholder. Values derived during the render that performs the first load come from the old `state`, so they must take their spaces from `saved.data`, as `loadTabs` itself does.

## Prevention

- Before deriving anything in `useTabs` from `state.spaces` or `state.space`, ask whether it is used by the first load. If so, take spaces from `saved.data`.
- Browser tests of space-dependent tab behavior should use a space other than Main. `internal/rocketclaw/web/src/remote-transport.test.ts` shelves a Search page from a "Side" space and checks, after a reload, that no tab appeared (`expect(await searchTab.count()).toBe(0)`). That check fails with the placeholder-based mapping.
- Switching space saves asynchronously: a test that switches space and then navigates with `page.goto` must first wait until the saved state's `space` is the new one, or the page opens in the old space.

## Related

- `docs/solutions/architecture-patterns/tailnet-pages-calling-another-rocketclaw.md`: pinning a server before an `await` in the same tab code.
