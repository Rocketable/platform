---
title: RocketClaw Actions on a Remote OpenCode Session Call Its API Through the Web Proxy
date: 2026-10-31
category: docs/solutions/architecture-patterns/
module: internal/rocketclaw/web
problem_type: architecture_pattern
component: frontend
severity: medium
applies_when:
  - Adding a RocketClaw command, menu item, or dialog that changes a Remote OpenCode Session
  - Tempted to reach into the mounted OpenCode view (host/api.ts Mounted) to run one of OpenCode's own commands
  - Tempted to edit the copied OpenCode UI under internal/rocketclaw/oc/packages to make a RocketClaw feature work
  - A feature must work on phones or in a narrow pane, where OpenCode's view drops its session header
related_components:
  - internal/rocketclaw/oc
  - internal/rocketclaw/frontend/rpc
tags:
  - opencode
  - remote-opencode-session
  - command-palette
  - sidebar
  - narrow-view
  - copied-upstream
  - web
---

# RocketClaw Actions on a Remote OpenCode Session Call Its API Through the Web Proxy

## Context

People asked for "Delete session" in RocketClaw's command palette (⌘P and ⌘⇧P) and in the left
sidebar tab menu, because on a phone OpenCode's own ••• menu, with Rename, Export and Delete, is
not there.

The first version went through the mounted OpenCode view. `internal/rocketclaw/oc/host/api.ts` already gives RocketClaw
`openPalette()`, so a second method that ran OpenCode's `session.delete` command looked like the
natural extension. It did not work where it was needed:

- `internal/rocketclaw/oc/packages/app/src/session/timeline/message-timeline.tsx:309` registers
  `session.rename` and `session.delete` only when `props.hideHeader` is false, and line 521 hides
  the header, ••• menu included, under the same flag.
- `internal/rocketclaw/oc/packages/app/src/session/screen.tsx:298` passes
  `hideHeader={!isDesktop()}`, and `internal/rocketclaw/oc/packages/app/src/session/model.ts:29`
  makes `isDesktop` a `(min-width: 768px)` query. `internal/rocketclaw/oc/host/overrides.ts` points
  width queries at `internal/rocketclaw/oc/host/media.ts`, which reads the view's container width, so
  a phone, or a narrow pane on a desktop, has neither the menu nor the command.

Making the command exist on narrow views meant editing the copied upstream file. The owner threw
that whole version away: a request for a RocketClaw command is not permission to change the copied
OpenCode UI, and `internal/rocketclaw/oc/AGENTS.md:21` allows editing a copied file only when the
change cannot be made from outside it. Here it could.

## Guidance

Build the action in the Web, and call the OpenCode server's own API through the Web's proxy:

- `internal/rocketclaw/frontend/rpc/relay.go:26` proxies GET, POST, PUT, PATCH and DELETE under
  `/oc/{server}/api/`, adding the server's password and refusing cross-site origins. The copied view
  uses the same route, so anything the view can do, the Web can do with a plain `fetch`.
- Delete is `DELETE /oc/<server>/api/session/<remoteSessionId>`. The view's own delete calls the same
  endpoint, `sdk.api.session.remove({ sessionID })` in
  `internal/rocketclaw/oc/packages/app/src/runtime/server/runtime.tsx:241`.
- Confirm with a RocketClaw dialog, close the tab, and invalidate the session list. RocketClaw's
  synced copy goes on its next poll, which deletes rows the server no longer lists within the synced
  window (`internal/rocketclaw/backend/opencode.go:424`).

In `internal/rocketclaw/web/src/ui.tsx` this is `DeleteSessionDialog` (line 2627), opened through
the existing `SessionCommand` flow with mode `"delete"`. The palette adds it in `useSessionActions`
(line 2587) and the sidebar tab menu in `menuItems` (line 1445, for one tab outside the Shared
group), both only for rows with a `server`, meaning remote OpenCode sessions:

```tsx
const response = await fetch(`${serverURL()}/oc/${session.server}/api/session/${session.remoteSessionId}`, { method: "DELETE" });
if (!response.ok) throw new Error(`${session.server} did not delete the session (HTTP ${response.status}).`);
```

Keep `Mounted` for what only the view can do, such as opening OpenCode's own palette
(`openPalette`). Don't add methods that run OpenCode commands from outside it.

## Why This Matters

- **Narrow screens.** OpenCode's view decides which of its commands exist from its own layout. An
  action routed through those commands silently does nothing on phones, the case that prompted this
  work.
- **Upstream merges.** Every edit to a copied file is a conflict on the next upstream merge
  (`internal/rocketclaw/oc/AGENTS.md`). A Web-only change keeps `internal/rocketclaw/oc/` and the
  committed `oc-dist` untouched.
- **Scope.** The owner treats an unrequested change to the copied OpenCode UI as a severe mistake,
  severe enough to discard the whole change.

## When to Apply

- Any new RocketClaw-side action on a remote OpenCode session: rename, delete, archive, export, and
  similar.
- Before extending `internal/rocketclaw/oc/host/api.ts`, check whether the proxy API already covers the action.

## Examples

Avoid, routing through the view:

```ts
// internal/rocketclaw/oc/host/mount.tsx: runs OpenCode's command; on a narrow view it is not registered, so nothing happens.
deleteSession: () => command.options.find((item) => item.id === "session.delete")?.onSelect?.("palette")
```

Prefer, the Web calling the API through the proxy, as `DeleteSessionDialog` does above.

Testing it in `internal/rocketclaw/web/src/opencode.browser.test.ts`:

- The fake server there answers any RPC it does not know, `ListConfig` included, with `{}` (line 82).
  The New session page then shows `["","config"] data is undefined`. A screenshot or recording that
  lands there shows that error, which the real server never causes.
- Closing the last tab goes to `/`, which opens a New session tab, so the tab count does not drop.
  Check that the deleted session's own tab is gone and the URL moved off it instead.
- Browser tests run the built `dist`; rebuild it after changing `ui.tsx`
  (`web-browser-tests-run-the-last-bundle-that-built.md`).

## Related

- `docs/solutions/logic-errors/remote-opencode-fork-marker-dropped-by-sidebar-remote-branch.md`
  covers another Web action on a Remote OpenCode Session (fork) and how such sessions open at
  `/oc/`.
- `docs/solutions/best-practices/react-doctor-complexity-limit-shapes-web-components.md`: palette
  and sidebar commands in `ui.tsx` must fit react-doctor's limits.
- `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`
