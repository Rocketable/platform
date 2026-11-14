---
title: PR Pane Jumped Because Its Loading Line Followed Every Background Refetch
date: 2026-11-13
category: docs/solutions/logic-errors/
module: internal/rocketclaw/web
problem_type: ui_bug
component: frontend
severity: medium
symptoms:
  - While a thread's agent works, the Pull requests pane shows "Finding pull requests…" above the PR every few seconds
  - The PR content moves down and back up on each thread update
  - "\"No pull requests found for this thread.\" flickers on threads without a PR"
root_cause: logic_error
resolution_type: code_fix
tags: [layout-shift, tanstack-query, is-fetching, is-pending, keep-previous-data, background-refresh, pull-requests-pane]
---

# PR Pane Jumped Because Its Loading Line Followed Every Background Refetch

## Problem

The Pull requests pane (`internal/rocketclaw/web/src/thread-pr-panel.tsx`) showed its "Finding pull requests…" line whenever a lookup was in flight (`isFetching`). Its queries look again on every thread update, so while an agent worked the line was inserted above the PR and removed every couple of seconds, and the whole pane jumped.

## Symptoms

- While a thread's agent is working, "Finding pull requests…" appears above the PR every few seconds, then disappears.
- Everything below it, the PR header, summary and file list, moves down and back up. A recording at desktop, tablet and mobile sizes measured 12 moves of 28 px in 12 seconds; after the fix it measured none.
- On a thread without a PR, "No pull requests found for this thread." disappears and reappears the same way.

## What Didn't Work

- Computing the new flag inside `ThreadPRContent` behaved correctly but pushed that component over react-doctor's complexity limit, so `make lint` failed. The flag now lives in the `useThreadPRs` hook next to `loading` (see [react-doctor-complexity-limit-shapes-web-components](../best-practices/react-doctor-complexity-limit-shapes-web-components.md)).

## Solution

Show the status line only while the query has nothing to show yet (`isPending`), and keep `isFetching` for what must wait for a lookup to settle: the **Find again** button, and `ThreadPRWatch`'s head-commit query (`thread-pr-panel.tsx:34`):

```tsx
// Before: every background refetch inserted the line above the PR.
const loading = created.isFetching || remote && checkout.isFetching;
{loading && <p role="status">Finding pull requests…</p>}
{!loading && !created.error && !checkoutError && !urls.length && <p role="status">No pull requests found for this thread.</p>}

// After: only the first look shows it; later looks keep the result in place.
const loading = created.isFetching || remote && checkout.isFetching;   // still disables "Find again"
const finding = created.isPending || remote && checkout.isPending;
{finding && <p role="status">Finding pull requests…</p>}
{!finding && !created.error && !checkoutError && !urls.length && <p role="status">No pull requests found for this thread.</p>}
```

Both flags are computed in `useThreadPRs` (`thread-pr-panel.tsx:25-27`). During a background look the disabled **Find again** button (`thread-pr-panel.tsx:54`) is the only in-progress cue; it sits in the header, where it moves nothing.

## Why This Works

Two facts combine:

1. **The query key changes about every 2 seconds while an agent works.** `ThreadPRPanel` passes the sidebar row's `updatedAt` as `version` (`ui.tsx:4644`), both queries put `version` in their key (`thread-pr-panel.tsx:14`, `:15`), and the sidebar polls `ListSessions` again 2 seconds after each listing (`ui.tsx:701`; see the poll table in [web-keep-alive-views-connection-budget](../architecture-patterns/web-keep-alive-views-connection-budget.md)). While its agent works, a thread's `updatedAt` was observed to change on almost every poll, so the pane starts a new lookup almost every poll.
2. **`keepPreviousData` keeps the old result on screen and reports success.** When the key changes and there is earlier data, TanStack Query fills the new result with it, sets `status` to `'success'` and `isPlaceholderData` to `true` (query-core `queryObserver.ts`, `createResult`). So `isPending` stays false while `isFetching` is true for the whole lookup.

`isFetching` therefore means "a background lookup is running", which is nearly always true during agent work, while `isPending` means "there is nothing to show yet". If the first key is still pending when the key changes, there is no earlier data to keep, `isPending` stays true and the line still shows. That is correct, because the pane is empty.

## Prevention

- In `internal/rocketclaw/web`, inline loading text, space-taking spinners and empty-state messages follow `isPending` (or another "no data yet" state), not `isFetching`, when the query uses `placeholderData` or is keyed on something that changes during agent work, such as `updatedAt`. Background refreshes stay silent and must not insert, remove or resize content. This matches the session list's rule in `internal/rocketclaw/web/README.md:858` ("Routine background refreshes are silent").
- When a background lookup needs a visible cue, use something that already takes its space: a disabled button, a same-size icon swap, or a style change.
- Regression test shape, from `internal/rocketclaw/web/src/github-pr.browser.test.ts`: hold the lookup's response (`await discovery`, line 375; `discovery = held.promise`, line 472), wait until **Find again** is disabled so the lookup is known to be in flight, then assert the status line is absent and the PR title has not moved (lines 475-476). The browser test runs the last built bundle, so rebuild before checking it fails without the fix (see [web-browser-tests-run-the-last-bundle-that-built](../best-practices/web-browser-tests-run-the-last-bundle-that-built.md)).

## Related

- [pr-pane-missed-prs-in-jj-workspaces-and-subagent-sessions](../integration-issues/pr-pane-missed-prs-in-jj-workspaces-and-subagent-sessions.md): the same pane's discovery misses (a different problem).
- `docs/verification/thread-pr/README.md`: the formal model of thread PR discovery and reading; it does not cover what the pane shows during a refetch, and this fix does not change its transitions.
