---
title: The Web Shares One Connection, and Keeping Views Mounted Needs a Remount Audit
date: 2026-10-07
last_updated: 2026-11-01
category: docs/solutions/architecture-patterns/
module: internal/rocketclaw/web
problem_type: architecture_pattern
component: frontend
severity: high
applies_when:
  - Adding or growing a response the Web fetches on every page load, poll, or tab switch
  - Small Web requests (Identity, Tabs, /oc/ calls) wait many seconds on a slow or relayed link
  - Turning a view that remounts on navigation into one that stays mounted while hidden, such as adding a page to WarmTabs or wrapping a view in TabPane
  - Mounting a second Transcript at once (warm session tabs, split panes, previews)
  - Adding a context ref, document-wide id lookup, or navigation or URL effect to a view that can be hidden
related_components:
  - cmd/rocketclaw
  - internal/rocketclaw/frontend/rpc
tags:
  - web
  - http2
  - head-of-line-blocking
  - tailnet-relay
  - list-sessions
  - keep-alive
  - warm-tabs
  - remount
  - hidden-views
  - react-effects
  - eventsource
---

# The Web Shares One Connection, and Keeping Views Mounted Needs a Remount Audit

## Context

Two things limit how much a Web page can do in the background: what its one connection to the server
can carry, and code that assumes a view mounts when it is shown and unmounts when the person leaves.

### The connection

The Web server serves HTTPS on both of its listeners (`cmd/rocketclaw/web.go:104`,
`cmd/rocketclaw/tailnet.go:190`). Browsers therefore speak HTTP/2 to it and send every request from a
page over one TCP connection. That removed the HTTP/1.1 limit of about six connections per host. The
warm-tabs variant of the tabs work (closed PR #212, plan U6) hit that limit, and it is why only the
active view was allowed to open `/stream`.

The move to HTTP/2 did not remove the budget. It changed its shape. Every response now shares one TCP
send path, so a large response delays every small one queued behind it. On a fast link nobody notices.
On a slow link the whole page stalls.

That happened on 2026-11-01. A browser reached one deployment through its tsnet name, and Tailscale
relayed the traffic through a DERP server (`tailscale ping` reported "via DERP(sfo)"). The HAR from that
browser showed the sidebar list arriving at about 43 KB/s. An OpenCode session (`/oc/...`) showed
"Loading…" for 15–20 s, and switching between OpenCode tabs made it worse. In the HAR:

- Tiny requests waited 15–17 s for their first byte, then were all released within about a second of
  each other. This hit OpenCode calls such as `/oc/local/api/info` and RocketClaw's own `Identity`,
  `Protocol` and `Tabs` alike.
- Once the first byte arrived, each download was quick.
- The page was pushing this through the link (person with 5,248 sessions):

| What | Size | When |
|---|---|---|
| `ListSessions`, the full listing | 7.5 MB raw, about 1.3 MB gzipped | every page load |
| `ListSessions` poll with nothing changed | about 364 KB raw, about 80 KB gzipped, about 270 KB of it the `order` of every session ID | about every 2 s plus the listing's own time (`ui.tsx:696`) |
| `ListCronJobs` | 1.1 MB raw, 225 KB gzipped | every page load, even on an OpenCode page |
| A new OpenCode view | about 40 requests and 1–2 MB | every OpenCode tab switch |

Goroutine dumps taken every second during a stall showed the cause. The handlers had already finished
and were blocked in `net/http/internal/http2.(*serverConn).writeHeaders`, waiting for the connection's
only writer. That writer was blocked in `gonet.(*TCPConn).Write` (tsnet's network stack), still unable
to send 275 bytes 5 s later. Up to eight finished responses waited inside the server for about 18 s.
The rest of RocketClaw was idle: about 150 goroutines and no database waits.

### Remounts

Much of the Web client assumes that a view mounts when it becomes visible and unmounts when the person
leaves. Keeping a view mounted but hidden breaks that assumption in places spread across unrelated
components. In `internal/rocketclaw/web/src/ui.tsx` today:

- `WarmTabs` keeps Cron, Agents, Skills and Config mounted once visited (or after preload) and hides
  the inactive ones with `TabPane`.
- `SessionApp` renders one `MessageScrollerProvider key={conversation.key}` and `Transcript` inside
  `TabPane show={showChat}`. `conversation` changes only while a chat route shows, so on any
  non-chat page the last session's `Transcript` stays mounted and hidden.

The warm-tabs variant kept several session transcripts mounted at once and hit most of the breakage in
part 2 below.

## Guidance

### 1. Budget the bytes on the one connection

- **Treat a slow link as normal.** The browser and the server can be on different machines. Phones,
  hotel Wi-Fi and Tailscale relays are ordinary. A direct Tailscale connection would help some setups,
  but it cannot be guaranteed, so the fix is to send less.
- **Count bytes, not connections.** Before adding work to a page load, a poll, or a tab switch, add up
  what it sends. Everything shares one send path with the request the person is waiting for.
- **A poll that finds nothing changed should cost almost nothing.** In the table above, the quiet poll
  resent every session ID, and a reload downloaded the full listing again. PR #892 (open, not merged as
  of this writing; the line references in this list are from its branch) changes that:
  - The completion no longer sends `order`. It names the listing version it was sent since, plus the
    IDs removed since then (`internal/rocketclaw/web/proto/web.proto:214-219`).
  - The server keeps each person's last 8 listings and skips rows whose fingerprint has not changed
    (`internal/rocketclaw/frontend/rpc/server.go:62-75`, `:234`).
  - Listing events leave out default values (`internal/rocketclaw/frontend/rpc/http.go:389`).
  - On reload, the page loads the saved listing and its version before the first poll, so the poll asks
    only for changes (`ui.tsx:653`).
  - The first message carries the owner and the OpenCode servers, so OpenCode tabs mount without waiting
    for the rows (`ui.tsx:675`, `:2615`).
- **Know what #892 leaves alone.** The server still lists every session on each poll; only the bytes
  sent go down. A poll naming a listing the server no longer holds gets every row again. That happens
  after a server restart, or once more than 8 pollers have run since. The full first listing, the
  `WarmTabs` preload of agents, skills, cron jobs and config on every page load (`ui.tsx:472`,
  `preload.ts`), and the OpenCode view remount on every tab switch (`RemoteSession`'s effect,
  `ui.tsx:2600-2613`) are all still there. Sending only tab sessions in full and moving search to the
  server is planned in `docs/plans/2026-11-01-1608-perf-slim-sidebar-list-plan.md`.
- **Keeping views mounted trades bursts for steady traffic.** A kept-alive OpenCode view would avoid
  the 1–2 MB burst on each switch, but a hidden view's streams and polls keep using the connection. It
  was proposed and rejected here, because one session's state could leak into another. Part 2 shows
  why that risk is real.

#### Diagnosing small requests that wait

- Get a HAR from the person's own browser. Many tiny requests with long waits for the first byte,
  released together and then downloaded quickly, point to a queue on the connection, not to slow
  handlers.
- While they reproduce it, take goroutine dumps every second. pprof is on by default
  (`--pprof`, `cmd/rocketclaw/serve.go:28`) and listens at 127.0.0.1:6060. That address is reachable
  only from the server's own network namespace, so inside a container run `curl` there. A deployment
  can turn it off with `--pprof=false`. Finished handlers in `http2.(*serverConn).writeHeaders`, behind
  a connection writer stuck in a TCP `Write`, mean the send path is full.
- Evidence that misled this investigation:
  - OpenCode looked guilty. Its event stream failed and retried every 3 s, and every `/oc/` call
    stalled. But RocketClaw's own calls stalled in the same window, and the dumps showed the proxy
    already held OpenCode's answers.
  - A headless browser on the server's machine opened the session in 2.7 s. The relay's load varies,
    so the person's HAR is the evidence.
  - Opening the server's direct IP to bypass the relay could not sign the person in. The server saw the
    container's gateway address, not a tailnet identity, so that comparison failed at sign-in.
  - "Browser and server are on the same Mac" made the relay look like the bug. They need not share a
    machine, and the page has to work over any link.
  - A few calls finished quickly inside the stall window. This session never explained them, so do not
    take a few fast responses as proof that the connection is not the problem.
  - "The server speaks HTTP/2, so the six-connection limit does not apply" is true, and it rules
    nothing out.

### 2. Audit what relied on remounting

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

#### Hidden views keep working

A hidden view keeps its stream and effects running, so its per-view work still costs while nobody sees
it, and it spends the same connection budget as part 1. `useSessionStream` opens `/stream` with deps
`[id, scope, refreshHistory]` and has no visibility gate, so the hidden `Transcript` keeps its
EventSource open and re-reads History on every change to that session. Warm pages keep whatever
queries and effects they started. Decide for each one whether it should keep running while hidden.

## Why This Matters

Neither problem shows up in tests or on localhost. The connection problem needs realistic data
(thousands of sessions) and a slow link. It then looks like a hung server, and the obvious suspects
(OpenCode, the database, the six-connection limit) all check out clean. The remount problems never
throw. They show up as commands going to the wrong composer, navigation the person did not ask for, a
rewritten URL, or a wrong scroll position. Tests with one visible view never exercise them.

## When to Apply

- Before adding data to a response the Web fetches on every page load or poll, or adding a new poll,
  preload, or stream.
- When Web pages are slow on one device or network and fine on another.
- Before adding a page to `WarmTabs`, wrapping a view in `TabPane`, or adding another keep-alive wrapper.
- Before mounting a second `Transcript`, or anything that uses `useSessionStream` or `#transcript-scroll`.
- When adding a context ref, document-level lookup, or navigation, URL or focus effect to a view that
  can be hidden.

## Examples

The listing's completion message, before and after PR #892, for a poll where nothing changed:

```text
before: {"sessions":[],"owner":"…","upstreamSuccess":true,"opencodeServers":{…},"agentTags":{…},"version":"…","order":[…every session ID…]}
after:  {"upstreamSuccess":true,"version":"…","since":"…"}
```

The chat pane stays mounted, hidden on non-chat pages:

```tsx
<TabPane show={showChat}>
  <MessageScrollerProvider key={conversation.key} autoScroll scrollEdgeThreshold={48}>
    <Transcript id={conversation.id} /* ... */ />
  </MessageScrollerProvider>
</TabPane>
```

## Related

- `docs/plans/2026-11-01-1608-perf-slim-sidebar-list-plan.md`: the plan for shrinking the session
  listing; PR #892 is its first part.
- `docs/plans/2026-10-07-1521-feat-web-tabs-replace-sidebar-plan.md`: Risks ("V4 complexity") and U6.
- `docs/solutions/architecture-patterns/tailnet-pages-calling-another-rocketclaw.md`: the browser
  calls each Remote RocketClaw's tsnet origin directly, so each of those has its own connection and
  its own listing.
- `docs/solutions/performance-issues/search-page-froze-rendering-every-result.md`: another web
  constraint that only shows with realistic usage.
- `internal/rocketclaw/web/README.md`: Attachment HTTP contract, which describes `/stream`.
