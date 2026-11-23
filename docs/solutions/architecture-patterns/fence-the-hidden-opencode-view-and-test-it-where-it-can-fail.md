---
title: Fence the Hidden OpenCode View, and Test It Where the Fence Can Fail
date: 2026-11-22
category: docs/solutions/architecture-patterns/
module: internal/rocketclaw/oc
problem_type: architecture_pattern
component: frontend
severity: high
applies_when:
  - Changing how the page's one OpenCode view is hidden, shown, mounted or disposed behind other RocketClaw tabs
  - Adding a document- or window-level listener, overlay, or read mark to the copied OpenCode UI or its host
  - Writing a host or browser test that claims a hidden OpenCode view takes no keys, clicks, overlays or read marks
  - Merging upstream OpenCode changes into MarkSessionViewed or the file and diff find shortcut
related_components:
  - internal/rocketclaw/web
  - docs/verification/oc-view-keep
tags:
  - opencode
  - remote-opencode-session
  - hidden-views
  - keep-alive
  - inert
  - kobalte
  - playwright
  - mutation-check
---

# Fence the Hidden OpenCode View, and Test It Where the Fence Can Fail

## Context

Opening an OpenCode tab after any other kind of tab used to rebuild OpenCode's view from scratch. With 100 ms added to each OpenCode request, going from a chat to an OpenCode tab took about 830 ms, and going between two servers' tabs about 840 ms every time. The fix keeps one view for the page. It holds every configured server and stays loaded, hidden, while another tab shows. Coming back from a chat then took about 30 ms. The plan is `docs/plans/2026-11-22-1942-feat-keep-opencode-view-plan.md` and the lifecycle model is `docs/verification/oc-view-keep/`. The change is the commit "internal/rocketclaw: keep one OpenCode view loaded behind every tab". As of this writing it has not been pushed or opened as a PR.

The 2026-11-02 design (`docs/specs/2026-11-02-opencode-tab-switch-design.md`) had turned this down because a hidden view leaks. The copied OpenCode UI assumes it is the only thing on the page and is always in front of the person. A read-only audit of the copied code during this work found these leaks:

- Keys pressed with focus on the page body reach the view: ⌘K, ⌘F, ⌘V, typing that jumps into its composer, and file drops.
- The ⌘F/⌘G find for files and diffs listens on `window` and picks a registered file or diff even when it is hidden.
- The view's portal sits on `document.body`, so its dialogs, menus, popovers and toasts stay on screen after the view is hidden.
- An open Kobalte modal menu sets `body.style.pointerEvents = "none"` and leaves the whole RocketClaw page unclickable.
- `MarkSessionViewed` marks the session viewed on the OpenCode server whenever the window has focus. That mark is shared by every client of that server.

Running one view for all servers, rather than one per server, avoids a second group of problems: settings and the saved model list overwriting each other, duplicate toasts, theme writes fighting, and two views claiming one key press. The copied app already runs several servers in one app, so this was possible.

## Guidance

### Hide the view without collapsing it

Do not use `display: none`. The view reads its width from its container (`internal/rocketclaw/oc/host/mount.tsx`, the `ResizeObserver` on `root`). A width of 0 switches OpenCode to its phone layout, unmounts terminals and side panels, and loses the scroll position. Instead, the view keeps its box (`absolute inset-0` in `RemoteSession`, `internal/rocketclaw/web/src/ui.tsx`) and is hidden with three things:

- `opacity: 0` on `[data-oc-view][data-oc-hidden]` (`internal/rocketclaw/oc/host/scope.css`). This covers both the view's root and its portal, because both carry `data-oc-view`.
- `inert` on the root, the portal and the Web's wrapper (`setVisible` in `mount.tsx`, and `inert={!visible}` in `RemoteSession`).
- A `visible` signal on `View` (`internal/rocketclaw/oc/host/fences.ts`), which the fences read.

Approaches that were tried and dropped:

- **`visibility: hidden`.** The built OpenCode CSS sets `visibility: visible` on some descendants and in keyframes, and a child set to `visible` shows through a hidden parent.
- **An extra `pointer-events: none !important` rule on every descendant.** A red check showed that `inert` alone keeps the page clickable, so the rule was removed.

### Fence each leak at its source

| Leak | Fence |
|---|---|
| Keys and drops with focus on the body | `inView` (`internal/rocketclaw/oc/host/fences.ts`) counts `document.body` as part of the view only while `view.visible()` is true |
| ⌘F/⌘G picks a hidden file or diff | copied `internal/rocketclaw/oc/packages/session-ui/src/pierre/file-find.ts` skips a host whose element sits inside `[data-oc-hidden]` (a `rocketclaw:`-marked edit) |
| Dialogs, palette and menus stay open | `setVisible(false)` closes the dialog stack, then sends one synthetic Escape (below) |
| The hidden session is marked viewed | copied `internal/rocketclaw/oc/packages/app/src/session/route.tsx` `MarkSessionViewed` requires `focus.window && view.visible()` (a `rocketclaw:`-marked edit) |
| RocketClaw's palette entry, quotes and server link reach the hidden view | only the shown view fills `remoteView` (`useRemoteView` in `ui.tsx`) |
| The typing pill watches a hidden session | `TypingPill shown={visible}` |

### Close Kobalte menus with Escape, not with focus

The first attempt sent a `focusin` to the body to close open menus. It did not work. Kobalte's modal menus call `preventDefault` on focus leaving (`onFocusOutside` in `@kobalte/core`), so they stay open on purpose. They close on Escape: `createEscapeKeyDown` listens for `keydown` on the document, and the menu's Escape handler closes the menu (`context.close(true)`).

So `setVisible(false)` dispatches one `keydown` Escape on the body. It adds a document listener that stops propagation during that dispatch and removes it straight after. Kobalte's listener was added to the document earlier, so it runs first. The page's bubbling `window` Escape handler (`onEscape` in `ui.tsx`) never sees the event. That handler closes the current page tab on Escape, and without the stop, hiding the view would close the page the person just opened.

Capture-phase `window` listeners still receive this synthetic Escape. Any new capture-phase Escape handler on the page will also run whenever the view is hidden.

### Test the fences where they can fail

Each new test was checked by removing its fix and confirming the test failed. Each problem below made a test pass when it should have failed, or made it fail for the wrong reason.

- **Playwright counts `opacity: 0` as visible.** `isVisible()` and `getByText(...).waitFor()` match the hidden view. Point locators at `main [data-oc-view]:not([data-oc-hidden])` (`internal/rocketclaw/web/src/opencode.browser.test.ts`). Otherwise a wait for "the session shows" passes while the view is still hidden.
- **A chat page hides a click leak.** With `inert` removed, the click check on a chat page still passed. The chat's own transcript is stacked above the hidden view, so `elementFromPoint` never reached the view. The check runs on `/shortcuts`, a page with little content, where it fails without `inert`. It probes the center of every box inside `main` with `elementFromPoint` and asserts that none lands in `[data-oc-view]`.
- **happy-dom cannot show the click lock going away.** In the host test (`internal/rocketclaw/oc/host/mount.test.tsx`), a menu closed by the Escape stays in the portal, marked `data-closed`. A wait for "no menu and the body takes pointer events again" timed out. The host test therefore asserts only `data-closed` after hiding. The real-browser test proves the page takes clicks again: it opens a menu, leaves for the chat, clicks the chat's composer, and checks `document.body.style.pointerEvents === ""`.
- **The fake OpenCode server's request log is never cleared.** `reset()` in `internal/rocketclaw/oc/host/test/server.ts` does not empty `server.requests`. A test that counts requests passed alone and failed in the full file, because earlier tests' reads were counted too. Take `const before = server.requests.length` at the start and count from `server.requests.slice(before)`.
- **Browser tests run the last bundle that built.** After each mutation, run `go generate ./internal/rocketclaw/internal/web` before the browser test (see `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`).
- **Silence from a red-check script is not a result.** A script that grepped only for `(pass)`, `(fail)` and `error:` printed nothing for a passing run, because bun does not print `(pass)` lines here. Grep the ` N pass` and ` N fail` summary lines instead.

## Why This Matters

None of these leaks throws an error. Each one looks like OpenCode misbehaving somewhere else:

- ⌘K opens a palette for a session nobody is looking at.
- ⌘F stops opening the browser's own find.
- The RocketClaw page ignores clicks until one click is spent dismissing an invisible menu.
- An OpenCode session shows as read for everyone on that server while the person was on a chat.

The tests are easy to fool. Playwright, a crowded page, happy-dom and a shared request log each made at least one check pass without its fix. That is why every fence was proven by removing it.

## When to Apply

- Adding a listener on `document` or `window`, or an overlay in the portal, to the copied UI or `internal/rocketclaw/oc/host/`: decide what it does while `view.visible()` is false.
- Merging upstream changes to `MarkSessionViewed`, `file-find.ts`, Kobalte, or the dialog stack: keep the `rocketclaw:` conditions, and rerun the hidden-view tests in `mount.test.tsx` and `opencode.browser.test.ts`.
- Changing how `RemoteSession` hides its wrapper, or the `[data-oc-hidden]` CSS: keep the box size, and keep `inert` on every layer.
- Writing any new "hidden X does not Y" test: run it on a page where Y would be observable, and prove it fails with the fence removed.

## Examples

Hiding in `mount.tsx`:

```ts
setVisible(next) {
  setVisible(next)

  if (!next) {
    closeDialogs()
    const stop = (event: Event) => event.stopPropagation()
    document.addEventListener("keydown", stop)
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }))
    document.removeEventListener("keydown", stop)
  }
  for (const element of [root, portal]) {
    element.toggleAttribute(hidden, !next)
    element.inert = !next
  }
},
```

A locator that does not match the hidden view, and a click check run on a page where a leak would show:

```ts
const view = page.locator("main [data-oc-view]:not([data-oc-hidden])");
await visit("/shortcuts");
await page.getByRole("main").getByRole("listitem").first().waitFor();
expect(await underPointer()).toBe(false);
```

## Related

- `docs/plans/2026-11-22-1942-feat-keep-opencode-view-plan.md`: design, lifecycle diagram, measurements.
- `docs/verification/oc-view-keep/README.md`: the TLC model of mounting, hiding and disposing. It models only the read-mark and `remoteView` fences; keys, overlays and the typing pill use the same visible flag.
- `docs/specs/2026-11-02-opencode-tab-switch-design.md`: the earlier design that declined keeping the view hidden.
- `docs/solutions/architecture-patterns/web-keep-alive-views-connection-budget.md`: the remount audit for RocketClaw's own React views kept mounted while hidden.
- `docs/solutions/architecture-patterns/link-into-the-opencode-view-through-its-own-anchor-and-router.md`: how the host moves the view between sessions.
