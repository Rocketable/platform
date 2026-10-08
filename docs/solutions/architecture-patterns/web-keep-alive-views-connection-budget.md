---
title: Keeping RocketClaw Web Views Mounted Needs a Stream Budget and a Remount Audit
date: 2026-10-07
category: docs/solutions/architecture-patterns/
module: internal/rocketclaw/web
problem_type: architecture_pattern
component: frontend
severity: high
applies_when:
  - Mounting more than one Transcript at a time (warm tabs, split panes, previews)
  - Adding any long-lived EventSource or streaming fetch to the web client
  - Turning a view that remounts on navigation into one that stays mounted while hidden
related_components:
  - cmd/rocketclaw
  - internal/rocketclaw/frontend/rpc
tags:
  - web
  - eventsource
  - http1-connection-limit
  - keep-alive
  - warm-tabs
  - remount
---

# Keeping RocketClaw Web Views Mounted Needs a Stream Budget and a Remount Audit

## Context

The RocketClaw web client shows one session at a time. `SessionApp` re-keys a
single `MessageScrollerProvider`/`Transcript` on every navigation
(`key={conversation.key}` in `internal/rocketclaw/web/src/ui.tsx`), so each
session view mounts fresh and unmounts on the way out. Much of the client
quietly depends on that.

The warm-tabs variant of the web tabs work (closed PR #212, plan
`docs/plans/2026-10-07-1521-feat-web-tabs-replace-sidebar-plan.md`, U6) kept
several session views mounted and hid the inactive ones. Two kinds of breakage
followed: a connection budget problem, and code that relied on remounting.

## Guidance

### 1. One live stream per page

Each mounted `Transcript` opens a long-lived `GET /stream` EventSource in
`useSessionStream`. The web server is a plain `http.Server` serving a TCP
listener with no TLS (`cmd/rocketclaw/web.go`), so browsers speak HTTP/1.1 to
it. Browsers then allow about six connections per host. Every request shares
that pool: streams, the 2-second polls (`Protocol`, identity, and each
composer's agents and queue queries), `GET /api/ListSessions`, and RPC
`POST`s.

When several transcripts each held a stream, the streams used up the pool.
RPCs and polls then queued behind them and the UI hung. The working design:

- Only the active view opens `/stream`. The effect depends on an `active` flag
  and closes the stream when the view hides.
- A hidden view catches up when it becomes active again. Opening the stream
  already triggers a History delta read from the last applied revision.
- Background running state comes from the session list (`ListSessions`), not
  from a per-view stream.

The same budget applies to any new feature that adds a long-lived connection,
and to the user's other RocketClaw browser tabs on the same host. Count the
streams before adding one. The limit goes away only if the server moves to
HTTP/2, which needs TLS or h2c.

### 2. Audit what relied on remounting

Hidden views that stay mounted keep running their effects and keep their
registrations. Check these four groups:

| What to check | What broke under warm mounting | Fix used |
|---|---|---|
| Context singletons | `SessionCommands.composer` is one ref set through `useImperativeHandle`. The composer that mounted last owned it, so palette `$` commands went to a hidden tab. | Hidden views get a context copy whose `composer` points at an unused ref. Only the active view registers the real one. |
| Global DOM IDs | `SelectionQuote` reads `document.getElementById("transcript-scroll")`. With several transcripts mounted, it returned the first one in the DOM. | Pass each view's own viewport ref down. Only the active viewport gets the `id`. |
| Navigation side effects in effects | The new-chat creation handoff (`useLayoutEffect` in `Transcript`) calls `route.goSession(...)`. A hidden composer that had created a session could still navigate. | Run the handoff only when the view is active. |
| Effects that relied on remount to reset or re-run | `SearchPage` keeps its saved-search state when it stays mounted, so closing the last inner tab did not start a fresh draft. Its URL-sync effect (`history.replaceState` to `/search?...`, deps `[tab, search]`) did not re-run when the user came back. It could also rewrite the URL while another tab was showing. | Reset the state explicitly on close. Add `pathname` to the effect's deps and return early when the path is not `/search`. |

Hidden panes also caused layout trouble:

- A `display: none` viewport measures 0x0. `MessageScrollerProvider`'s
  `autoScroll` reads that as "at the end" and pins there, so pass
  `autoScroll` only to the active view.
- Reordering mounted panes moves their DOM nodes and resets their scroll
  position. Render panes in mount order, not recency order.

## Why This Matters

The stream budget fails silently. Nothing logs an error. Requests just wait for
a free connection, and the UI freezes in ways that look like a backend stall.
The remount assumptions are spread across unrelated components. The tests
passed with one mounted view, because one view never exercises them.

## When to Apply

- Before mounting a second `Transcript`, or anything else that calls
  `useSessionStream`.
- Before adding a new EventSource, streaming `fetch`, or long-poll to the web
  client.
- When converting any page or pane from remount-on-navigate to hidden-but-mounted
  (`TabPane`, `WarmTabs`, or a new keep-alive wrapper).

## Examples

The stream gate from the warm-tabs variant:

```tsx
// HTTP/1.1 allows about six connections per host, so only the active tab streams; others catch up on activation.
useEffect(() => {
  if (!id || !scope || !active) return;
  const stream = new EventSource(`/stream?${new URLSearchParams({ id })}`);
  // ...
  return () => { stream.close(); };
}, [id, scope, active, refreshHistory]);
```

Test that matches the budget: with three session tabs open, at most one
`/stream` connection is open at a time (plan U6 test scenarios).

## Related

- `docs/plans/2026-10-07-1521-feat-web-tabs-replace-sidebar-plan.md`: Risks
  ("V4 complexity") and U6.
- `internal/rocketclaw/web/README.md`: Attachment HTTP contract, which describes
  `/stream`, `ListSessions`, and `Protocol` polling.
