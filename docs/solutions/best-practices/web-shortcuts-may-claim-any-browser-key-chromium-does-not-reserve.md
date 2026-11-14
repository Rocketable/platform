---
title: Web Shortcuts May Claim Any Browser Key Chromium Does Not Reserve
date: 2026-11-13
category: docs/solutions/best-practices/
module: internal/rocketclaw/web
problem_type: developer_experience
component: frontend
severity: medium
applies_when:
  - Choosing a new RocketClaw Web keyboard shortcut that Chrome already binds, such as Cmd/Ctrl+Shift+B for the bookmarks bar
  - Deciding whether a page's preventDefault will stop a browser action on macOS as well as Linux
  - Checking a shortcut against the real browser, which headless browser tests cannot do
tags:
  - web
  - keyboard-shortcuts
  - chromium
  - browser-accelerators
  - playwright
  - real-key-probe
---

# Web Shortcuts May Claim Any Browser Key Chromium Does Not Reserve

## Context

Cmd/Ctrl+Shift+B, which toggles the right pane, is also Chrome's key for showing and hiding the bookmarks bar. Before using it, the question was whether a page can take that key at all, especially on macOS, where Chrome's menus own their key equivalents.

The repository already guarded the opposite case. A comment and assertion in `internal/rocketclaw/web/src/session-list.browser.test.ts:292-296` say that Chromium keeps new and closed tabs, windows and tab cycling for itself. They also say headless test browsers hand those keys over anyway, so a passing test proves nothing about them. They did not say whether the remaining browser keys reach the page, or how to check one for real.

## Guidance

**Chromium reserves a short, fixed list, and every other browser key reaches the page first.** `kReservedCommandIds` in Chromium's `chrome/browser/ui/accelerator_table.cc` lists close tab, close window, new window, new incognito window, new isolated window, new tab, restore tab, select next and previous tab, cycle tabs, and exit. `BrowserCommandController::IsReservedCommandOrKey` (`chrome/browser/ui/browser_command_controller.cc`) withholds only those from the page on macOS, Windows and Linux. ChromeOS also reserves its hardware Back, Forward and Refresh keys, and Linux gives the page any key the user bound as a text-editing hotkey. For any other key, such as `IDC_SHOW_BOOKMARK_BAR` on Cmd/Ctrl+Shift+B, the page gets the keydown first, and `preventDefault()` stops Chrome's action. Installed app windows reserve nothing. In fullscreen off macOS, everything except exit and the fullscreen toggle goes to the page.

**macOS follows the same rule.** `ChromeCommandDispatcherDelegate prePerformKeyEquivalent` (`chrome/browser/ui/cocoa/chrome_command_dispatcher_delegate.mm`) runs before the page. It runs a command there only when `BrowserNativeWidgetMac::WillExecuteCommand` agrees, and that function returns false before the page for every command `IsReservedCommandOrKey` does not reserve (`chrome/browser/ui/views/frame/browser_native_widget_mac.mm`). A key the page handles never reaches the main menu; only unhandled keys are passed to it (`chrome_command_dispatcher_delegate.mm`, `postPerformKeyEquivalent`).

**A Chrome extension bound to the same key still wins.** Its accelerators run at high priority before the page. Firefox and Safari were not checked.

**Claim the key in a capture-phase window listener, and claim it even when there is nothing to do.** RocketClaw's Cmd/Ctrl+Shift+A and S (`AttentionButton`, `internal/rocketclaw/web/src/ui.tsx:1522-1538`) and Cmd/Ctrl+Shift+B (`PanelToggle`, `ui.tsx:840-859`, added with this learning) call `preventDefault()` and `stopPropagation()` before checking whether they can act. A key that sometimes falls through to the browser would sometimes open the bookmarks bar instead, which is harder to learn than a key that does nothing.

**Do not use a Playwright test to decide this.** Playwright sends keys with CDP `Input.dispatchKeyEvent` and no native key code, so Chrome delivers them straight into the page and its own shortcuts never see them. A test can only show that the page handler runs. The new Cmd/Ctrl+Shift+B assertions in `internal/rocketclaw/web/src/github-pr.browser.test.ts:513-518` cover the handler, not Chrome.

## Why This Matters

Without the reserved list, every browser shortcut looks off-limits, and a good key gets rejected for nothing. Trusting a passing headless test instead picks reserved keys like Cmd+T or Cmd+W that do nothing for a real user, which is the trap the existing test comment already records. The macOS menu path is where a reasonable guess goes wrong, because the menu shows the key and looks as if it should win.

## When to Apply

- A proposed shortcut collides with a Chrome menu shortcut. Check `kReservedCommandIds` first: reserved means pick another key (RocketClaw adds Alt, as in mod+Alt+N), not reserved means the page can take it.
- A user reports that a RocketClaw shortcut does a browser action instead. Run the probe below in that browser before changing the handler.

## Examples

A real-key probe sends an operating-system keystroke to a visible Chrome window, so the event takes Chrome's full path. CDP is used only to load the page and read its state afterwards. The probe was run on macOS. Keep the profile and script under the repository's `.tmp/`, and bring the probe's Chrome to the front by process ID so the user's own Chrome windows never get the key:

```js
const child = spawn(exe, [`--user-data-dir=${dir}`, "--remote-debugging-port=9333", "--no-first-run", "--no-default-browser-check", "about:blank"]);
const browser = await chromium.connectOverCDP("http://127.0.0.1:9333"); // playwright-core
const page = browser.contexts()[0].pages()[0];
await page.goto(`data:text/html,${encodeURIComponent(html)}`); // records keydowns and innerHeight on resize
execFileSync("osascript", ["-e", `tell application "System Events"
  set frontmost of (first process whose unix id is ${child.pid}) to true
  delay 0.6
  keystroke "b" using {command down, shift down}
end tell`]);
```

The bookmarks bar shows as a smaller `innerHeight`. Run a control page whose listener does not call `preventDefault()` next to the page that does, so the test can tell "blocked" from "never fired". Also record whether the page saw the keydown, which catches a focused address bar taking the key first. Results on 2026-11-13 with a new profile:

| Browser | Control page | Page calling `preventDefault()` |
|---|---|---|
| Chrome for Testing (Playwright chromium-1243) | saw the key; bar appeared (557 → 523 px) | saw the key; no bar (557 px) |
| Google Chrome 157.0.8103.52 | saw the key; bar appeared (613 → 579 px) | saw the key; no bar (613 px) |

System Events must be allowed to send keystrokes from the terminal running the probe.

## Related

- `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`: a mutation check of a Web shortcut needs a rebuilt `dist` before the browser test sees it.
- `docs/solutions/best-practices/frontend-rpc-tests-need-setup-in-fresh-jj-workspaces.md`: the `ROCKETCLAW_PLAYWRIGHT_MODULE` and `ROCKETCLAW_CHROMIUM` setup the probe reuses.
- `docs/plans/2026-10-26-0808-fix-embedded-shortcut-priority-plan.md`: why host shortcuts are claimed in the capture-phase window listener ahead of embedded terminals.
