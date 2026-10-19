---
title: Web desktop notifications when a tab finishes answering
type: feat
status: ready
date: 2026-10-19
product_contract_source: ce-plan-bootstrap
depth: lightweight
---

# Web desktop notifications when a tab finishes answering

## Problem Frame

People start a turn in a RocketClaw Web tab, switch to other work, and have to keep checking back to see whether the agent is done. The tab strip's running dot only helps when the RocketClaw window is in view. A desktop notification when an open tab finishes answering lets them come back exactly when the answer is ready, for RocketClaw sessions and remote OpenCode sessions alike.

## Requirements

- R1. When an open session tab's turn stops running, the browser shows a desktop notification naming the session.
- R2. R1 applies to RocketClaw session tabs and remote OpenCode session tabs (`opencode:<server>:<id>`).
- R3. No notification for the tab the person is looking at: the active tab while the window has focus.
- R4. Clicking the notification focuses the window and opens that tab.
- R5. Notifications need the browser's permission, asked for only on a person's gesture.

## Key Technical Decisions

- **Trigger: finished answering only**, chosen over also notifying when an agent waits for input: the request asked for a notification "when a tab has finished answering".
- **Both tab kinds**, chosen over RocketClaw-only: the request names both RocketClaw and OpenCode tabs.
- **Signal: the sidebar row's `running` flag.** `SidebarOwner` (`internal/rocketclaw/web/src/ui.tsx`) already polls `ListSessions` every 2s, and every row carries `running`: RocketClaw rows from active turns (`internal/rocketclaw/backend/store.go`), OpenCode rows from `/api/session/active` synced every 5s (`internal/rocketclaw/backend/opencode.go`). A running→not-running transition on an open tab's row is "finished answering" for both kinds, with no backend change. OpenCode tabs have no RocketClaw transcript stream, so the per-tab `/stream` cannot serve R2.
- **Shape: a render-less `TabNotifications` component beside `TabPlacement`**, reading `Sidebar` rows and `TabActions` state, the same pattern `TabPlacement` uses so row updates do not redraw the app.
- **Permission: on by default**, as requested. While permission is undecided, the first click or key press calls `Notification.requestPermission()`, since browsers allow the request only from a gesture. No in-app off switch: the browser's site settings own the permission.
- **Platform boundary:** browsers without the `Notification` API (iOS Safari outside an installed web app) get no notifications.
- **Duplicate windows:** every window that saw the turn run notifies, under `tag: <session id>`, so the system shows one notification per session.

## Scope Boundaries

- Out: notifications for pending questions or permission prompts, sounds, an in-app on/off preference, backend or push (service worker) notifications when no RocketClaw window is open.

## Assumptions

- A turn shorter than one sidebar poll may never be seen running and then gets no notification. Hidden windows' timers are throttled by browsers, so a notification can arrive late (up to about a minute after long hiding in Chrome). Acceptable for a first version. The upgrade path is server push.
- The notification body is "Finished answering", and the title is the tab's title text.

## Implementation Units

### U1. Notify on finished turns, and ask for permission

- Files: `internal/rocketclaw/web/src/ui.tsx`, `internal/rocketclaw/web/README.md`
- Approach: add `TabNotifications` next to `TabPlacement` and mount it beside it. Keep the previously running session IDs in a ref. On each rows/tabs change, for each row that was running and no longer is, whose `sessionPath` is an open tab and which is not (focused window and active tab), create `new Notification(title, { body, tag: id })` whose click focuses the window, navigates to the tab, and closes the notification. While permission is undecided, ask for it on the first click or key press. Document both in the README Tabs section.
- Test file: `internal/rocketclaw/web/src/notifications.browser.test.ts`
- Test scenarios:
  - With permission granted and a recording `Notification` stub, a RocketClaw tab and an OpenCode tab whose rows go running→idle while another tab is active each raise one notification with their title and session-id tag.
  - The active tab of a focused window finishing raises none.
  - A row that was never seen running raises none.
  - Clicking a notification opens its tab.
  - While permission is `default`, nothing asks for it until the first click, which does.
- Verification: `make lint`, `make test` in `internal/rocketclaw/web` (includes the CLOC budget), `go test ./...`, `make lint`, `make test` at the repo root.
