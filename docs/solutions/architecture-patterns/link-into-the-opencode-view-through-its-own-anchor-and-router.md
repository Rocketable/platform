---
title: Link Into the OpenCode View Through Its Own Anchor and Router
date: 2026-11-18
category: docs/solutions/architecture-patterns/
module: internal/rocketclaw/oc
problem_type: architecture_pattern
component: frontend
severity: medium
applies_when:
  - RocketClaw opens a Remote OpenCode Session at a place inside it, such as a message
  - Code in internal/rocketclaw/oc/host writes the mounted view's memory history
  - A semport merge reshapes the timeline's cursor, follow, or dock-resize code
related_components:
  - internal/rocketclaw/web
  - docs/verification/oc-view-session
tags:
  - opencode
  - remote-opencode-session
  - deep-link
  - memory-router
  - semport
---

# Link Into the OpenCode View Through Its Own Anchor and Router

## Context

Message deep links (`/oc/<id>?message=<msg_id>`) needed the copied OpenCode view to open at one prompt. Upstream already has this: a `#message-<id>` hash on the view's route pages older history toward the prompt and scrolls to it (`internal/rocketclaw/oc/packages/app/src/session/timeline/cursor.ts`, `cursor-machine.ts`). The view runs on a `MemoryRouter` in `internal/rocketclaw/oc/host/mount.tsx`, so RocketClaw's address never reaches it unless the host passes the hash in.

Two first attempts were wrong:

- Writing the hash straight into the host's view history (`history.set`) for a session already shown. When the view showed a subagent of the tab's session, that write moved it to the parent without going through `ViewHistory`'s stale-location guard (`internal/rocketclaw/oc/host/navigation.tsx`). That is a step the `docs/verification/oc-view-session` model does not contain.
- Assuming the anchor alone holds on a first load. It does not: the cursor jumps, then the composer dock settles its height, the dock's resize handler sees the view still near the end and scrolls back there, the follow state returns to "following", and the cursor clears the hash.

## Guidance

- Turn RocketClaw's `?message=` into upstream's `#message-<id>`; do not build scrolling or history paging.
- A new mount opens at the hash (`MountOptions.messageID`). Moving to another session uses `createViewHistory().show`, which closes dialogs and sets the Back floor. A link into the tab's own session goes through the router's `navigate`, captured inside the router tree in `mount.tsx`, exactly as OpenCode's own anchor writes do. That keeps the stale guard, keeps Back, and stays the "app navigates in the family" transition the model already checks.
- The dock-settle snap lives in a closure in `interaction.ts` that `internal/rocketclaw/oc/host/` cannot reach. It is held by one `rocketclaw:`-marked condition: the dock does not stick to the end while the cursor's jump is fresh, seeking, or pending. A semport merge that reshapes that handler must keep the condition. `opencode.browser.test.ts` ("a message link opens a remote session at that prompt…") fails without it.

## Why This Matters

The owner treats unrequested copied-file edits as costly because every one becomes a merge point for the semport loop. Both mistakes looked like small host-side shortcuts. One quietly widened the session-identity protocol past what TLC checked; the other made links work only when the view was already settled, which a test against an already-open session would never show.

## When to Apply

- Any host code that changes where the mounted view is, not just message links.
- Any upstream merge touching `interaction.ts`'s dock `createResizeObserver` or the cursor/follow machines.

## Examples

Same-session link in `mount.tsx` `show(sessionID, messageID)`: `navigateView(href(sessionID, messageID))` rather than `history.set({ value: href(...) })`.

## Related

- docs/solutions/architecture-patterns/web-acts-on-remote-opencode-sessions-through-the-api-proxy.md
- docs/plans/2026-11-18-0721-feat-message-deeplinks-plan.md (KTD3, KTD4, KTD9)
- docs/verification/oc-view-session/README.md
