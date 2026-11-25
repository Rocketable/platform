---
title: Tabs Cache Writes Must Keep the Read Timestamp, or the Tab Merge Runs Again
date: 2026-11-25
category: docs/solutions/logic-errors/
module: internal/rocketclaw/web
problem_type: logic_error
component: frontend
severity: high
symptoms:
  - Browser tests far from the sidebar fail after a change that only adds a write to the tabs query cache
  - The background jobs list renders empty, and other tests time out after 30 seconds waiting for page elements
  - A failing test still fails when run alone, so load or a flaky environment is not the cause
root_cause: wrong_api
resolution_type: code_fix
tags: [web, tabs, tanstack-query, set-query-data, data-updated-at, use-tabs, merge-effect, recent-tabs, optimistic-update]
---

# Tabs Cache Writes Must Keep the Read Timestamp, or the Tab Merge Runs Again

## Problem

The sidebar's Recent and Parked lists are saved by `saveList` in `internal/rocketclaw/web/src/ui.tsx`. A first version wrote each change into the local tabs query cache with `queryClient.setQueryData` right away, then wrote again when the server answered. A plain `setQueryData` gives the cache a new `dataUpdatedAt`. In `useTabs`, a new `dataUpdatedAt` means "a new saved state was read from the server", so the merge effect ran again on every page the person opened. The browser suite broke in places that have nothing to do with the sidebar.

## Symptoms

- After the visit writes went in, 22 browser test files failed. The change was expected to break only the tests that look for the old sidebar cards and the Parked box.
- `background.browser.test.ts` showed an empty background jobs list instead of its five rows. The composer tests timed out after 30 s waiting for the page.
- Each of these still failed when run alone.
- Passing the old timestamp fixed 11 of the 22 files. The other 11 had other causes, mostly the expected ones: tests that look for the old cards and the Parked box.

## What Didn't Work

- The first guess was load or the environment, because 22 failing files was far more than the change should break. Running `background` and `composer` alone ruled that out: they failed the same way.
- Nothing in the failing tests pointed at the tabs cache. The jobs list and composer never read the lists. The link only appears when you know that the merge effect keys on the cache timestamp, not on the cached data.
- Later, to stop a tabs read that started before a list change from landing after it and flipping the list back, each save refetched the tabs query once its reply arrived. That broke 16 browser test files the same way: a refetch is a real read, so it bumps the timestamp and the merge runs again, and a list is saved on every page that opens a new tab.

## Solution

`saveList` writes the server's answer into the cache with the timestamp the cache already has, and keeps its optimistic list outside the cache until then:

```ts
queryClient.setQueryData<SavedTabs>(queryKey, (old) => old && { ...old, lists: reply.lists }, { updatedAt: queryClient.getQueryState(queryKey)?.dataUpdatedAt });
```

While a change saves, the sidebar shows the client's own list (`listSaves.pending`, read through `useLists`), so no cache write happens before the answer. When the answer comes while a tabs read is still in flight, `saveList` waits for that read to settle, through a query-cache subscription, then writes the answer over whatever it brought, still keeping that read's timestamp. Nothing refetches.

## Why This Works

- TanStack Query's `setQueryData` stores `dataUpdatedAt: options.updatedAt ?? Date.now()` (`successState` in `@tanstack/query-core/src/query.ts`, version 5.108.4 in this tree). Without `updatedAt`, every write looks newer than the last fetch.
- In `useTabs`, `sync.current.at` records the timestamp of "the read last taken in". The merge effect lists `saved.data` and `saved.dataUpdatedAt` as dependencies. It returns early only when `queue.at === saved.dataUpdatedAt` and the owner is unchanged. On any other timestamp, it takes the cached saved state as new. With no newer revision, it checks whether the space still exists, takes in the spaces, and navigates to the space's entry if the tab in view is not one of that space's tabs. With a newer revision, it merges the saved tabs with this window's changes and replaces the tab state. Test fakes that answer `Tabs` without a `revision` give `Number(undefined)`, which is `NaN`. `NaN <= queue.revision` is never true, so in those tests every pass takes the full merge path.
- `saveList` runs, through `useRecordVisit`, whenever a focused window opens a tab Recent lacks, starting right after the first load, and through `ParkedSync` whenever a tab is parked or unparked. A write that changes the timestamp therefore restarts the merge pass on those pages. A list change is not a new read: it changes only `lists`, which the merge effect never reads. When the write keeps the old timestamp, the effect still runs for the new `saved.data` but stops at its first check.

## Prevention

- A new code path that writes the local tabs query cache (`queries.tabs(owner, "")`) passes `{ updatedAt: queryClient.getQueryState(queryKey)?.dataUpdatedAt }`, unless the merge effect should take the write in as a newly read saved state. The same applies to any query whose consumer uses `dataUpdatedAt` to decide that a fetch arrived.
- To keep a stale in-flight read from undoing an optimistic change, hold the optimistic value outside the cache until that read settles, as `saveList` does. Do not refetch or invalidate after each save.
- The older writers `saveLayout`, `AutoParkRow`'s toggle, and `useTabs`' `spaceChange` still write without `updatedAt`. They run only on explicit actions, and the suite passes with them. This session did not check whether they should also keep the timestamp. Do not copy them as the pattern for a write that runs on every navigation.
- The browser suite caught this, but the failures surfaced far from the cause. If unrelated browser tests start failing right after a change adds a tabs cache write, check this first. A unit test of `saveList` would need it exported from `ui.tsx`, which is a product code change. It is not written here.

## Related Issues

- [The Palette Preview Tab Stays in Its Window at Every Saved-State Boundary](../architecture-patterns/palette-preview-tab-stays-in-its-window-at-every-saved-state-boundary.md): what the merge effect rebuilds, and why a rebuild path has to put the preview tab back.
- [Web Tab State Knows Only a Placeholder Main Space Until the Saved Tabs Arrive](web-tab-state-holds-a-placeholder-main-space-before-its-first-load.md): the merge effect's first-load branch.
- [PR Pane Jumped Because Its Loading Line Followed Every Background Refetch](pr-pane-jumped-because-its-loading-line-followed-every-background-refetch.md): another TanStack Query state field (`isFetching`) that does not mean what the UI assumed.
