---
title: Keeping Web Views Mounted Needs a Remount Audit
date: 2026-10-07
last_updated: 2026-10-29
category: docs/solutions/architecture-patterns/
module: internal/rocketclaw/web
problem_type: architecture_pattern
component: frontend
severity: high
applies_when:
  - Turning a view that remounts on navigation into one that stays mounted while hidden
  - Adding a page to WarmTabs or wrapping a view in TabPane
  - Mounting a second Transcript at once (warm session tabs, split panes, previews)
  - Adding a context ref, document-wide id lookup, or navigation or URL effect to a view that can be hidden
tags:
  - web
  - keep-alive
  - warm-tabs
  - remount
  - hidden-views
  - react-effects
  - eventsource
---

# Keeping Web Views Mounted Needs a Remount Audit

## Context

Much of the web client assumes that a view mounts when it becomes visible and unmounts when the user
leaves. Keeping a view mounted but hidden breaks that assumption in places spread across unrelated
components. In `internal/rocketclaw/web/src/ui.tsx` today:

- `WarmTabs` keeps Cron, Agents, Skills and Config mounted once visited (or after preload) and hides
  the inactive ones with `TabPane`.
- `SessionApp` renders one `MessageScrollerProvider key={conversation.key}` and `Transcript` inside
  `TabPane show={showChat}`. `conversation` changes only while a chat route shows, so on any
  non-chat page the last session's `Transcript` stays mounted and hidden.

The warm-tabs variant of the tabs work (closed PR #212, plan U6) kept several session transcripts
mounted at once and hit most of the breakage below. Its rule that only the active view may open
`/stream` came from the server speaking plain HTTP/1.1, where browsers allow about six connections per
host; the server now serves HTTPS (`cmd/rocketclaw/web.go`, `cmd/rocketclaw/tailnet.go`), so browsers
carry every request over one HTTP/2 connection and that limit is gone.

## Guidance

Before a view stays mounted while hidden, or before a second instance of it mounts, check each risk:

| Risk | Current example | What to do |
|---|---|---|
| Single global ref in context | `SessionCommands.composer` is one ref created in `SessionApp`. One composer mounts today, so palette `$` commands are unambiguous, but on a non-chat page the ref still points at the hidden composer. | With several instances, give hidden views a context copy whose ref is unused. Only the active view gets the real one. |
| Imperative handle bound to one instance | `Composer` binds that ref with `useImperativeHandle` and no deps. With several composers the one that rendered last owns it, and an unmounting one resets it to `null` even if another owned it. | Bind the handle only from the active view. |
| Document-wide id lookup | `SelectionQuote` calls `document.getElementById("transcript-scroll")`, and `openCall` queries `#transcript-scroll [data-tool-call-id=...]`. Only `Transcript` renders that viewport, so the id is unique today. | With two transcripts mounted these return the first one in DOM order. Pass each view its own viewport ref, or set the id only on the active viewport. |
| Effects that assume mount means visible | The new-chat handoff in `Transcript` is a `useLayoutEffect` with no deps that calls `route.goSession(...)` once the draft has its session id, but only while `location.pathname === "/"`, so a chat created while another page shows opens when New session shows again (`tabs.browser.test.ts`). `SearchTabs`, the body of every Search page, focuses its input in a mount-only layout effect. | Gate navigation, focus and similar user-facing side effects on the view being active. Re-run them when the view becomes active, not only on mount. |
| URL-sync effects that would fight the active tab | `SearchTabs` calls `history.replaceState` to its own Search page location (`/search` or `/search/<id>`, plus `?q=`) in an effect with deps `[tab, search, path]`. That is safe only because `SearchPage` mounts only while the route is a Search page, one at a time. | If such a page becomes warm, return early off its own path. |
| State that relies on remount | `SearchPage` keys `SearchTabs` by owner, location, and whether the page is Shared, so sharing or unsharing a page starts it fresh. `SearchTabs` saves typing still waiting to be saved in its unmount cleanup, and a link's `?q=` is used once, by the mount it led to. | If such a page becomes warm, save waiting typing when it is hidden, not only on unmount, and reset what the key change resets explicitly. The warm-tabs variant kept a stale saved-search draft for lack of that. |

Hidden panes also need two layout checks:

- A `display: none` viewport measures 0x0. In the warm-tabs variant `MessageScrollerProvider`'s
  `autoScroll` read that as "at the end" and pinned there. The hidden `Transcript` still gets
  `autoScroll` today, so check the scroll position after coming back from a non-chat page.
- Reordering mounted panes moves their DOM nodes and resets scroll. Render panes in a stable order,
  as `WarmTabs` does.

### Hidden views keep working

A hidden view keeps its stream and effects running, so its per-view work still costs while nobody sees
it. `useSessionStream` opens `/stream` with deps `[id, scope, refreshHistory]` and has no visibility
gate, so the hidden `Transcript` keeps its EventSource open and re-reads History on every change to that
session. Warm pages keep whatever queries and effects they started. Decide for each one whether it
should keep running while hidden.

## Why This Matters

None of these failures throws. They show up as commands going to the wrong composer, navigation the
user did not ask for, a rewritten URL, or a wrong scroll position. Tests with one visible view never
exercise them.

## When to Apply

- Before adding a page to `WarmTabs`, wrapping a view in `TabPane`, or adding another keep-alive wrapper.
- Before mounting a second `Transcript`, or anything that uses `useSessionStream` or `#transcript-scroll`.
- When adding a context ref, document-level lookup, or navigation, URL or focus effect to a view that
  can be hidden.

## Examples

The chat pane stays mounted, hidden on non-chat pages:

```tsx
<TabPane show={showChat}>
  <MessageScrollerProvider key={conversation.key} autoScroll scrollEdgeThreshold={48}>
    <Transcript id={conversation.id} /* ... */ />
  </MessageScrollerProvider>
</TabPane>
```

## Related

- `docs/plans/2026-10-07-1521-feat-web-tabs-replace-sidebar-plan.md`: Risks ("V4 complexity") and U6.
- `docs/solutions/performance-issues/search-page-froze-rendering-every-result.md`: another web
  constraint that only shows with realistic usage.
- `internal/rocketclaw/web/README.md`: Attachment HTTP contract, which describes `/stream`.
