---
title: Web App Update Bar - Plan
type: feat
date: 2026-10-22
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Web App Update Bar - Plan

## Goal Capsule

- **Objective:** After a web UI release, people with RocketClaw already open see a bar at the top of the page telling them a new version is available, and one click gets them onto it. Nobody has to be told to hard refresh.
- **Means:** The open page periodically re-reads the app's index page and shows the bar when that page no longer references the scripts and styles this tab is running (KTD1).
- **Authority:** The Product Contract below wins on behavior; KTDs win on mechanism; units implement both.
- **Stop conditions:** Stop and report if the bar cannot be added without pushing JS/TS source past the 8,000-line budget, or if `SessionApp` would need a new branch to host it.
- **Execution profile:** One small frontend unit plus one browser test; no Go or protocol change.
- **Who finishes:** `ce-work` implements and verifies; the LFG pipeline reviews, commits, and opens the PR.

## Product Contract

### Summary

Add a bar at the top of the RocketClaw web UI that appears when the server is running a newer (or older) web build than the one the tab loaded. The bar says a new version is available and has a single "Refresh" button that reloads the page. Nothing reloads on its own.

### Problem Frame

Every frontend release leaves already-open tabs running the old UI, and the person shipping has to tell coworkers to refresh. The app already reloads open tabs automatically, but only when `web.proto` changes (`ProtocolGuard` in `internal/rocketclaw/web/src/ui.tsx`), so a UI-only release goes unnoticed. A plain refresh is enough to pick up a release because every response is sent `Cache-Control: no-cache` (`internal/rocketclaw/internal/web/assets.go`); the missing piece is telling people a refresh is needed.

### Key Decisions

- **Notify with a top bar the person clicks; never reload automatically for a web release.** (session-settled: user-directed — chosen over reloading the page automatically when the session is idle: the user asked to "just add the bar on the top, the user clicks and reloads") Governs R1, R2, R3.

### Requirements

**Showing the bar**

- R1. When the server serves a different web build than the one the open tab loaded, a bar appears at the top of the page saying a new version of RocketClaw is available.
- R4. A tab running the build the server currently serves shows no bar, and a failed check never makes the bar appear.
- R5. A visible tab notices a release within about a minute; a tab that was in the background notices when it becomes visible again.
- R6. Once shown, the bar stays until the page is refreshed or a later successful check finds the server serving this tab's build again; a failed check leaves the bar as it was.

**Acting on it**

- R2. The bar has one button, labeled "Refresh", that reloads the page so it loads the new build.
- R3. A web build change never reloads the page without that click, except the existing `ProtocolGuard` reload when `web.proto` changes (see Scope Boundaries).

### Scope Boundaries

Non-goals, considered and not built:

- Automatic reload when idle or hidden. Settled out by the Key Decision above.
- A dismiss control. Not requested; the bar is one line and clears on refresh. Would change if people report the bar getting in the way.
- Warning before refresh when a reply is still pending. The click is deliberate and drafts are already persisted in IndexedDB. Would change if people report losing private replies by clicking Refresh mid-turn.
- Changing `ProtocolGuard`'s existing instant reload on `web.proto` changes. Existing behavior, outside this request.
- A build-ID field on `/api/Protocol`, cache-header changes, chunk-load error handling. Alternatives or adjacent work from `docs/ideation/2026-10-22-web-app-update-ideation.html`; not needed for R1-R6.

## Planning Contract

### Key Technical Decisions

- KTD1. **Detect a new build by re-fetching `/` and checking it still references this page's own entry script and stylesheet.** The built `index.html` names the content-hashed entry script and stylesheet (`internal/rocketclaw/web/build.ts`), and the server re-serves it on `/` with `no-cache`. If the fetched HTML no longer contains the raw `src` attribute of this page's `script[type="module"]` or the raw `href` attribute of its stylesheet link, the server is on a different build. Read the attributes, not the DOM `.src`/`.href` properties, because the built `index.html` uses root-relative paths and the properties return absolute URLs that would never match. This needs no Go, proto, or generated-code change, and compares "different", not "newer", so rollbacks also show the bar. Rejected: a new build-ID field on `ProtocolResponse` (needs a `web.proto` edit, `protoc` regeneration, plumbing the embedded build from `internal/web` into `frontend/rpc`, and test updates, for the same result); folding the build into `protoSha256` (resets the sidebar and the IndexedDB session-list cache on every UI release, and `make generate` does not regenerate `frontend/rpc`). Cost of this choice: tabs opened before the release that ships this bar will not see it for that one release.
- KTD2. **Check with a TanStack Query `useQuery` on a 60-second `refetchInterval`, relying on the client's default refetch when the tab becomes visible.** The shared `QueryClient` sets no `refetchIntervalInBackground`, so hidden tabs pause and re-check on `visibilitychange`, which gives R5. A 60-second interval keeps the extra request off the connection budget documented in `docs/solutions/architecture-patterns/web-keep-alive-views-connection-budget.md`; the existing 2-second `ProtocolGuard` poll is not reused because its response carries no build identity. The `/` check joins the `refreshed` set in `internal/rocketclaw/web/src/sentry.ts` so it does not open Sentry request spans, as the other polled requests already don't.
- KTD3. **The bar is its own component mounted at the `App` root next to `ProtocolGuard`, showing content only when a new build is detected.** That puts it on every page and keeps its logic out of `SessionApp`, which is at the react-doctor complexity limit (`docs/solutions/best-practices/react-doctor-complexity-limit-shapes-web-components.md`).
- KTD4. **Copy avoids the word "Reload".** In `CONCEPTS.md`, "Reload" means overlay hot-load and `reload-transport.test.ts` asserts no button named exactly "Reload". The button says "Refresh"; the message says a new version of RocketClaw is available. Uses the shared `Button` from `internal/rocketclaw/web/src/components/ui/button.tsx`. The message sits in a `role="status"` region that is always mounted and empty until a new build is detected, so screen readers announce it, following `CopyPageLink`'s `sr-only` status span in `ui.tsx`; the button sits outside that region.
- KTD5. **The bar is an in-flow first row, not a fixed overlay.** `html` and `body` are `overflow: hidden` (`internal/rocketclaw/web/app/globals.css`) and `SessionApp`'s root is `flex h-dvh … overflow-hidden`, so an in-flow strip above it would push the bottom navigation off-screen, and a fixed overlay would cover the tab strip until refresh. Wrap the App root in a full-height column with the bar first and `SessionApp` filling the rest, changing only `SessionApp`'s root `h-dvh` to a flex-fill class (a className change, not a new branch). Pad the bar's right edge like the tab strip's so the fixed top-right `ThemeToggle` never covers the Refresh button.

### Assumptions

- `GET /` from an open tab returns the same index page the tab loaded, with the same authentication. Verified in `assets.go`: `/` is an app route served from the embedded `index.html`; the Sentry variant only inserts a config script and keeps the asset tags.
- Bun's entry hash changes when only a lazily loaded chunk changes, because the entry references chunk file names. Not verified. If false, a release that changes only a Shiki chunk shows no bar, which is acceptable because those chunks only change with Shiki upgrades.

## Implementation Units

### U1. Web app update bar

- **Goal:** Show the "new version available" bar with a working "Refresh" button when the served build differs from the tab's build.
- **Requirements:** R1, R2, R3, R4, R5, R6; Key Decision above; KTD1, KTD2, KTD3, KTD4, KTD5.
- **Dependencies:** none.
- **Files:**
  - `internal/rocketclaw/web/src/ui.tsx` (modify: new component beside `ProtocolGuard`, App-root column wrapper, `SessionApp` root className)
  - `internal/rocketclaw/web/src/sentry.ts` (modify: add `/` to the `refreshed` set)
  - `internal/rocketclaw/web/src/sentry.browser.test.ts` (modify: include `/` in the polled paths asserted to produce no spans)
  - `internal/rocketclaw/web/src/web-update.browser.test.ts` (create)
- **Approach:**
  1. Read this page's entry script and stylesheet attribute values once (KTD1).
  2. Query `/` as text per KTD2; a non-OK response or network error is a failed check (R4).
  3. Show the bar from the last successful check: new build when that HTML lacks either reference (KTD1); a failed check keeps the previous result (R4, R6).
  4. Render the bar per KTD4 and KTD5; the button calls `window.location.reload()` (R2). Nothing else reloads (R3).
- **Patterns to follow:** `ProtocolGuard` in `internal/rocketclaw/web/src/ui.tsx` for a root-level query-driven component; `CopyPageLink`'s always-mounted `role="status"` span for announcements; the tab strip's right margin for clearing `ThemeToggle`.
- **Test scenarios:** (browser test modeled on `internal/rocketclaw/web/src/background.browser.test.ts`, which serves the built `dist/` from `Bun.serve` and drives Chromium; trigger re-checks by dispatching a `visibilitychange` event on `document`, which TanStack Query v5 refetches on, rather than waiting 60 seconds)
  - Page loaded while the server serves the current `index.html` shows no bar after a re-check.
  - Server switches to an `index.html` whose entry script name differs; after a re-check the bar shows the new-version message and a "Refresh" button.
  - Server switches to an `index.html` whose only change is the stylesheet name; after a re-check the bar appears.
  - Server answers `/` with HTTP 500; after a re-check no bar appears.
  - With the bar showing, a re-check that gets HTTP 500 leaves the bar showing.
  - With the bar showing, the server goes back to the current `index.html` (a rollback); after a re-check the bar disappears.
  - With the bar showing, the page does not reload by itself across further re-checks.
  - With the bar showing at desktop and mobile widths, the tab strip, the bottom navigation, the theme toggle, and the Refresh button are all visible in the viewport.
  - Clicking "Refresh" fires a page load; with the server back on the current `index.html`, the reloaded page shows no bar.
- **Verification:** The new browser test passes, `make lint` in `internal/rocketclaw/web` passes (including react-doctor), and the JS/TS source budget check stays under 8,000 lines.

## Verification Contract

| Gate | Command (from) | Proves |
|---|---|---|
| Web lint, types, react-doctor | `make lint` (`internal/rocketclaw/web`) | Component style, types, complexity limit |
| New behavior | `bun test src/web-update.browser.test.ts` after `make build` (`internal/rocketclaw/web`) | R1-R6 |
| Web suite and budget | `make test` (`internal/rocketclaw/web`) | No regressions; runs `check-cloc-budget` |
| Repo gates | `make lint` and `make test` (repo root) | AGENTS.md required checks |

## Definition of Done

- U1 test scenarios pass and the existing web browser tests still pass.
- `make lint` and `make test` pass at the repo root, and the JS/TS source count stays below 8,000 lines.
- No change to `web.proto`, generated code, Go handlers, or `ProtocolGuard`'s behavior.
- No experimental or abandoned code is left in the diff.
- The PR notes that tabs opened before this release need one last manual refresh to pick it up (KTD1).
