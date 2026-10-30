---
title: A Web Page That Calls Another RocketClaw Must Read Its Refusals and Pin Its Server
date: 2026-10-30
category: docs/solutions/architecture-patterns/
module: internal/rocketclaw/frontend/rpc
problem_type: architecture_decision
component: frontend
severity: medium
applies_when:
  - Changing NewTailnetHandler's cross-site rules or the order of its checks
  - Adding Web code that calls rpc, fetch or EventSource for a remote RocketClaw
  - Adding state to the Web session list that is kept per server
tags:
  - remote-rocketclaws
  - tailnet
  - cors
  - react-doctor
  - web
---

# A Web Page That Calls Another RocketClaw Must Read Its Refusals and Pin Its Server

## Context

Remote RocketClaws let one Web page operate other RocketClaw servers. The browser calls each remote's tsnet HTTPS origin directly. `NewTailnetHandler` (`internal/rocketclaw/frontend/rpc/http.go`) trusts `https` pages from its own tailnet: it answers their CORS preflight, sets `Access-Control-Allow-Origin` and rewrites the request to same-origin, so the existing origin checks hold. The page keeps one session list per server (`ServerSessions` in `internal/rocketclaw/web/src/ui.tsx`). It treats a remote that cannot be reached as offline and keeps its rows. A refusal (`RPCError`) clears them.

Review found three things that look right in isolation and break across servers.

## Guidance

- **Set the CORS headers before any refusal.** The handler first set `Access-Control-Allow-Origin` only after `WhoIs` signed the caller in. A refused caller's 401 then carried no CORS header, so the browser hid it and `fetch` threw a `TypeError`. The page could not tell "you are refused" from "the server is down", so it greyed the rows instead of clearing them. The trust check now runs first. Preflight is still answered only after sign-in.
- **Pin the server before any `await`.** `rpc` defaults to the page's current server, `server()`, read from the URL. Code that runs after an `await`, or a save queue that keeps flushing after its component unmounted, can read a different server once the person moved to another one. Tab saves would then post one server's tabs to the other. Capture the server once per mount and pass it explicitly (`tabsCall(name, ...)`, `mutations.tabs(input, name)`, and the `sync` ref in `useTabs`). Query keys that belong to a named server start with `{ server }` (`queryKeyHash` in `api.ts`).
- **Gather per-server views by nesting providers, not by reporting up.** A per-server child that pushes its view to the parent in an effect trips react-doctor's `no-pass-data-to-parent` and `no-prop-callback-in-effect`. Taking the server name as a prop trips `no-adjust-state-on-prop-change` on the list's existing reset effect, even though the component is keyed by that name. `SidebarOwner` instead nests one `ServerName.Provider` + `ServerSessions` per server. Each adds its view to the map it got from outside, and `SidebarMerge` reads the full map, so data only flows down.

## Why This Matters

Each mistake fails quietly. An unreadable refusal leaves stale chats listed under a person the remote no longer admits. A save sent to the wrong server overwrites that server's tabs, or loses the change, whenever the revision numbers happen to line up. Neither shows an error at the call site.

## When to Apply

- Any change to `NewTailnetHandler`'s order of checks.
- Any new remote call in the Web page that runs after an `await` or outlives its component.
- Any new per-server state in the session list.

## Examples

Before, a refusal unreadable to the page:

```go
who, err := client.WhoIs(r.Context(), r.RemoteAddr)
if err != nil || ... { httpRPCError(w, ...); return } // no CORS header yet
if origin := r.Header.Get("Origin"); tailnetPage(r, client, suffix, origin) { w.Header().Set("Access-Control-Allow-Origin", origin) }
```

After, the page reads every answer, refusals included:

```go
page := tailnetPage(r, client, suffix, origin)
if page { w.Header().Set("Access-Control-Allow-Origin", origin); w.Header().Add("Vary", "Origin") }
who, err := client.WhoIs(r.Context(), r.RemoteAddr)
```

`TestTailnetCrossSitePages` checks the readable refusal, and `TestRemoteRocketClawRealBrowser` (`src/remote-transport.test.ts`) drives the whole flow in Chromium.

Related: `docs/solutions/best-practices/react-doctor-complexity-limit-shapes-web-components.md` and `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`.
