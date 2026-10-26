---
title: The Search Page Froze Because It Drew Every Result, Not Because Searches Weren't Cancelled
date: 2026-10-26
category: docs/solutions/performance-issues/
module: internal/rocketclaw/web
problem_type: performance_issue
component: frontend
symptoms:
  - Typing on /search lagged, and a keystroke could take seconds to appear.
  - Short or common queries (one or two letters, common words) were the slow ones.
  - Sentry long-frame spans on /search entered through MessagePort.onmessage and could not be mapped to a component.
root_cause: missing_size_limit
resolution_type: code_fix
severity: high
related_components:
  - internal/rocketclaw/frontend/rpc
tags:
  - search
  - web
  - rendering
  - typing-lag
  - cancellation
  - long-frames
  - source-maps
  - incremental-rendering
---

# The Search Page Froze Because It Drew Every Result, Not Because Searches Weren't Cancelled

## Problem

Typing on `/search` lagged badly. The obvious suspects were searches not being cancelled as the user typed, and missing source maps hiding the slow code. Neither was the cause. The page drew every result row a broad query returned, and drawing them blocked the main thread, so keystrokes queued behind it.

## Symptoms

- A keystroke could take seconds to show, mostly right after a short or common query's answer arrived.
- The same query typed steadily (no pause) felt fine, because each answer was aborted before it arrived.
- Sentry stalls on `/search` entered through `MessagePort.onmessage`, with nothing pointing at our source.

## What Didn't Work

- **Blaming cancellation.** It already works end to end. `useSessionSearch` in `internal/rocketclaw/web/src/ui.tsx` aborts the previous request when a newer one starts. On the server, `sharedSearch` in `internal/rocketclaw/frontend/rpc/session_commands.go` runs the scan on a context it cancels once the last waiting caller leaves (`TestSearchSessionsSharedFlightCancellation` covers this). Aborted database queries disappear well before an unaborted search would finish.
- **Waiting for source maps to name the slow code.** Sentry maps error stack traces, not timing spans. A long frame entering through `MessagePort.onmessage` is React's scheduler running a render: no map can name the component, because the browser only records where the task started. Treating "unmapped" as a blocker stalls the investigation indefinitely.
- **Looking only at server time.** The search query itself is fast. The cost is in the size of the answer and in rendering it, and neither shows in database statistics.
- **Memoizing the result list** (PR 674) stopped unchanged groups from being rebuilt on each keystroke. That was a real saving, but the first render of a large answer still drew everything.

## Solution

Draw rows a page at a time (PR 679). `SearchResults` draws `searchPage` (200) rows across the result groups. An invisible marker after the last row is watched by an `IntersectionObserver` rooted on the results panel with a one-screen bottom margin; when it comes near, the next page is drawn. A new response starts again at one page. `SearchResultGroup` takes `drawn`, slices its hits to it, and still shows the full hit count.

Observed behavior is unchanged: every result is reachable by scrolling, in the same order, with the same counts. The one visible difference is that the browser's find-in-page only finds rows already drawn.

The browser test `a broad search draws a page of rows and scrolling draws the rest` in `search-page.browser.test.ts` serves 1,000 sessions with 3 hits each. The old code draws all 3,000 rows at once; the fix draws one page and reaches every row by scrolling.

## Why This Works

A one-letter query can match nearly every chat, and each message hit renders highlighted transcript text. Rendering is proportional to rows drawn, so bounding the rows drawn bounds the work per answer no matter how much the server returns. Cancellation could never help: the freeze happens after an answer arrives, which is exactly when the user has paused typing.

## Prevention

- When a screen is slow to type into, reproduce it before theorizing. Drive the page in headless Chrome (`playwright-core`, as the browser tests do) against realistic data. Record per keystroke: request sizes, long tasks, `event` timing entries for `keydown`, and DOM node count after settling. A large DOM node count points at rendering; a large response with a small DOM points at the transport.
- Read stalls that enter through `MessagePort.onmessage` or a React-managed handler as "this screen renders too much", then find the screen and reproduce it. Map a stall position only when it enters through a listener added directly with `addEventListener`.
- Any list fed by an unbounded server response must bound what it draws, or the server must bound what it sends. Prefer the one that keeps observed behavior unchanged.
- The server still returns the full answer, full message text included. If broad searches stay slow once rendering is bounded, look there next: the page only shows a window of each message.

## Related Issues

- `docs/solutions/architecture-patterns/web-keep-alive-views-connection-budget.md`: another web constraint that only shows with realistic usage.
- `docs/solutions/runtime-errors/execute-uncapped-results-exceeded-model-context.md`: the same missing-size-limit shape in a different subsystem.
