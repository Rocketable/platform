---
title: Web Composer Agent Search, Code Wrap, and Agent Mismatch - Plan
type: feat
date: 2026-10-05
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Web Composer Agent Search, Code Wrap, and Agent Mismatch - Plan

## Goal Capsule

- **Objective:** People using the RocketClaw web UI can find an agent quickly in a long agent list, choose to read long code lines without side-scrolling, and always see the agent that will actually answer their next message.
- **Means:** A searchable agent picker built from base-ui Combobox primitives (KTD1, KTD2), one shared browser-local wrap preference for code blocks (KTD3), and a frontend-only agent derivation that reuses the existing `$agent` switch (KTD4, KTD5).
- **Authority:** Requirements govern product behavior. The session-settled Key Decision governs the mismatch behavior. KTDs govern mechanism within the Requirements. Units override neither.
- **Execution profile:** Three web units in `internal/rocketclaw/web`, plus README notes. U2 builds on U1; U3 is independent. No Go or RPC changes.
- **Stop conditions:**
  - Stop and report if the web source CLOC would cross the 5250 hazard line. Do not raise or route around the budget.
  - Stop and report if the command-palette "Choose agent" path cannot keep the Combobox open with the planned focus rule (KTD2).
  - Stop and report if the mismatch fix needs a backend change. KTD4 keeps the backend contract unchanged.
- **Completion owner:** The implementer completes all units and runs the Verification Contract. Commit, push, and deployment need separate authority.

---

## Product Contract

### Summary

Add type-to-filter search to the composer's agent selector, matching agent name or model. Add a wrap toggle to every code block, remembered for all blocks in this browser. When a session's agent isn't allowed in its channel or no longer exists, show the first allowed agent and switch to it on the next send.

### Problem Frame

The agent selector lists every allowed agent with its model, and some installations have 30 or more agents. Finding one means scrolling a long popup.

Code blocks in the transcript never wrap. Prose-like content, such as a proposed customer reply, gets cut off on the right and must be read by scrolling sideways line by line.

A session can run an agent that its Slack channel doesn't allow. For example, an External MCP call can start a thread on agent A in a channel that allows only B and C. The selector button then shows A while its list offers only B and C. The person can't tell which agent will answer, and can't pick the one the button shows.

### Requirements

**Agent search**

- R1. The agent selector has a search box that filters agents by case-insensitive substring match on agent name or model. When nothing matches, the popup says so.
- R2. On fine-pointer devices, the search box receives focus whenever the selector opens, including from the command palette's "Choose agent". On coarse-pointer (touch) devices, opening the selector doesn't focus the search box, so the keyboard doesn't cover the list.
- R3. Existing selector behavior keeps working: arrow keys and Enter pick an agent, Escape closes, the palette's "Choose agent" opens it, rows keep touch-sized targets, and the trigger keeps its current size limits on narrow screens. Closing the selector clears the search text.

**Code block wrap**

- R4. Every code block offers a wrap toggle. When on, long lines wrap, including long unbroken tokens, in both the inline block and the expanded view. When off, blocks scroll sideways as they do today.
- R5. One wrap preference applies to all code blocks. It takes effect immediately on every block on screen, persists across reloads in this browser, and defaults to off.

**Agent mismatch**

- R6. When a session's current agent is set but missing from the agents the selector lists, the selector shows the first listed agent. The next send, unless it is a stash, a stop, or a typed `$agent` command, switches the session to that agent before delivering the message. Popping a stashed message or sending queued work from the queue panel switches first in the same way.
- R7. Otherwise, selection behaves as today. A session whose agent is listed keeps it. An agent the person picked from the list wins. A new session defaults to `main` when `main` is listed, and to the first listed agent otherwise.
- R8. When the switch fails, the message isn't sent, the draft is kept, and the error is shown. This is the existing failure path for agent switches.

### Key Decisions

- **Switch to the first allowed agent on the next send.** The selector never shows an agent the list can't offer. Governs R6, R8. (session-settled: user-directed — chosen over showing the current agent as a disabled list row, a warning on the selector button, a selectable "not allowed" row, and forcing an explicit choice before sending: the selector should name the agent that will actually answer.)
  - **Conflict call-out:** today, a session on a disallowed agent can still send. After this change, its next send depends on the `$agent` switch. That switch checks the live Slack channel policy, but the list comes from stored channel facts. When the live check fails (for example, Slack unavailable) or disagrees with the stored facts, the session can't send anything except a stash, a stop, or a typed `$agent` command until that resolves. Stale stored facts could also move a thread away from an agent that is actually allowed. See Risks.

### Scope Boundaries

- No backend or RPC changes. `ListAgents` keeps returning the stored current agent even when it isn't listed; a server test pins this.
- No change to how External MCP or Slack choose agents, and no server-side rejection of prompts to disallowed agents.
- No change to Slack's `$agent` command or Slack-side selectors.
- The theme selector keeps using the existing Select component.

#### Deferred to Follow-Up Work

- Making the selector's list use the same live channel policy as the `$agent` check, so the two can't disagree.
- A visible hint that an automatic switch will happen on send.

---

## Planning Contract

### Key Technical Decisions

- KTD1. **Build the picker from base-ui Combobox primitives, with the search input inside the popup, directly in `ui.tsx`.** The installed `@base-ui/react` 1.8.0 Combobox is a filterable select. It keeps `role="combobox"` on the trigger, supports a controlled `open`/`onOpenChange` (so `agentOpen` keeps working), and takes a custom `filter` for name-or-model matching. A new shared shadcn combobox file would spend too much of the roughly 205-line CLOC headroom. Putting an `Input` inside the existing Select fights Select's own typeahead and keyboard handling.
- KTD2. **Decide initial focus with `matchMedia("(pointer: coarse)")`, not base-ui's open-type default.** Base-ui skips input focus only when a touch opened the popup. The palette opens the picker from code, so the default would still raise the phone keyboard. When the palette asks for the agent picker, the composer stops moving focus to the textarea; focus stays with the picker. The app already uses this media query in the command palette.
- KTD3. **Store the wrap preference in a module-level store in `transcript-text.tsx`, following `timeline-detail.ts`.** Use a shared value, a listener set, `useSyncExternalStore`, and the `storage` event, under the key `code-wrap`. Many `CodeBlock`s are mounted at once, so per-block state can't update the others. The server snapshot returns "off" without touching `localStorage`. Bun has no `localStorage`, and `transcript-text.test.tsx` renders `CodeBlock` without stubs. Wrapped blocks use `whitespace-pre-wrap` plus `wrap-anywhere` and drop the horizontal scrollbar. In the compact handoff variant, the toggle appears only in the expanded dialog, because that header shows no code.
- KTD4. **Fix the mismatch in the web client only, by deriving two values from the catalog.** `selected` is what the trigger shows. The switch target is what `sendComposer` receives as `selected` for existing sessions; see the High-Level Technical Design table. It instantiates the Key Decision for R6, R7, and R8 (session-settled: user-directed — inherited from the Key Decision; chosen over keeping the unlisted agent on the trigger). Order is the backend's list order (`catalog[0]`), never re-sorted on the client. `ListAgents` keeps the allowed list's own order, so a Slack channel's first configured agent comes first; other web sessions stay alphabetical. This also fixes two existing bugs:
  - A picked agent that later leaves the list no longer triggers a doomed switch.
  - The new-session page no longer shows `main` when no `main` agent exists.
- KTD5. **Skip the automatic switch for stop sends and typed `$agent` commands as well as stashes.** A `$stop` must not wait on, or be blocked by, an agent switch. A typed `$agent` is already an explicit choice, and it may be the only way out when the first listed agent is rejected. This narrows the existing `!stashing` guard in `sendComposer` to also exclude `stopping` and text that starts with `$agent`.
- KTD6. **Queue-panel actions apply the same switch target first.** Otherwise a popped stash, or queued work sent from its row, would be answered by the unlisted agent while the selector shows another one. Pop and Send/Steer send `$agent <target>` before acting and, on failure, show the error without acting (R8). The switch holds the draft's `sending` flag, so a composer send and a queue action never switch at the same time; queue buttons are disabled while a send is in flight.

### High-Level Technical Design

Composer agent derivation (KTD4). "Listed" means present in `catalog` from `ListAgents`. "Current" is `currentAgent`: `main` on the new-session page, otherwise the server's stored agent, which may be empty.

| Case | Trigger shows (`selected`) | Switch target sent to `sendComposer` |
|---|---|---|
| Person picked an agent that is still listed | picked agent | picked agent |
| No listed pick, current is listed | current | none |
| No listed pick, current is non-empty and not listed (R6) | `catalog[0]` | `catalog[0]` |
| No listed pick, current is empty | `catalog[0]` | none |
| Catalog empty | current agent, trigger disabled | none |

On the new-session page, current is `main`, so the trigger shows `main` when it is listed and `catalog[0]` otherwise. The session is created with `selected`, as today (R7).

### Risks

| Risk | Mitigation |
|---|---|
| Sends from a mismatched session fail when Slack's live policy check fails or disagrees with stored facts (Key Decision call-out) | Existing error path keeps the draft (R8); stash and stop still work; deferred follow-up aligns the two policy sources |
| Stale stored channel facts move a thread off an agent that live policy allows | Accepted with the Key Decision; the switch is visible in the transcript as an agent change |
| A switch on a Slack thread changes the agent for everyone in that thread | Same as today's explicit `$agent`; accepted |
| A steer sent while busy is still answered by the old agent, because a switch affects future turns | Accepted; matches today's explicit-pick behavior |
| Combobox closes when the palette hands off focus | KTD2 focus rule plus a browser test at 1280 and 390 with touch; stop condition if it can't hold |
| CLOC headroom (about 205 lines before 5250) | KTD1 inline primitives; check `make cloc` after each unit |

---

## Implementation Units

### U1. Agent mismatch derivation and send switch

- **Goal:** The selector shows, and the next send switches to, the first listed agent when the current agent isn't listed.
- **Requirements:** R6, R7, R8; KTD4, KTD5, KTD6; Key Decision (session-settled).
- **Dependencies:** none.
- **Files:**
  - `internal/rocketclaw/web/src/ui.tsx` (`SessionComposer` agent derivation, the `send` argument, the `sendComposer` switch guard)
  - `internal/rocketclaw/web/src/transcript.test.ts`
  - `internal/rocketclaw/web/src/session-list.browser.test.ts`
  - `internal/rocketclaw/web/README.md`
- **Approach:**
  1. Replace the `selected` fallback with the KTD4 table: fall back to the current agent only when it is listed, otherwise to `catalog[0]`.
  2. Have `send` pass the KTD4 switch target for existing sessions instead of `draft.agent`.
  3. In `sendComposer`, extend the switch guard to skip stop sends and typed `$agent` commands (KTD5).
  4. In the queue panel's pop and Send/Steer handlers, send the switch target first (KTD6).
  4. Update README lines about "existing threads retain their agent" to describe the switch for unlisted agents.
- **Patterns to follow:** existing `sendComposer` tests in `transcript.test.ts`, which extract private functions from `ui.tsx`. Mocked `ListAgents` and `ctrl.currentAgents` in `session-list.browser.test.ts`; the mock already applies `$agent X`.
- **Test scenarios:**
  - Covers R6: in the browser, a session whose `currentAgent` is `retired` and whose agents are `[other, main]` shows `other` on the trigger. Sending `hello` makes the server receive `$agent other` and then `hello`. After the refresh, the trigger still shows `other`.
  - Covers R7: with the current agent listed, sending makes no `$agent` request (existing tests stay green).
  - Edge: an empty `currentAgent` shows `catalog[0]` and sends no `$agent`.
  - Edge: on the new-session page with agents `[other]` and no `main`, the trigger shows `other`, and the created session uses `other`.
  - Edge: a person picks `main`, then the catalog drops `main`. The trigger shows the KTD4 fallback, and no `$agent main` is sent.
  - Unit, KTD5: `sendComposer` with text `$stop` and a switch target different from the current agent sends no `$agent` request. A stash with a mismatch also sends none. Typed `$agent planner` in a mismatched session sends only `$agent planner`.
  - Browser, KTD6: in a mismatched session (current `retired`, agents `[other, main]`), stash `hello`, then pop it. The server receives `$agent other` before the pop. A rejected `$agent` leaves the stash held. Sending a queued row also switches first.
  - Error, R8: the mock rejects `$agent other`. The error appears, the draft text is kept, and `hello` is never sent.
- **Verification:** All scenarios pass. The server test asserting that the stored current agent survives removal still passes unchanged.

### U2. Searchable agent picker

- **Goal:** Replace the composer's agent Select with a Combobox whose popup has a search box that filters by name or model.
- **Requirements:** R1, R2, R3; KTD1, KTD2.
- **Dependencies:** U1, which changes the same JSX's `selected` value.
- **Files:**
  - `internal/rocketclaw/web/src/ui.tsx` (`Composer` agent control, the imperative "agent" command, imports)
  - `internal/rocketclaw/web/src/session-list.browser.test.ts`
  - `internal/rocketclaw/web/src/session-commands.browser.test.ts`
  - `internal/rocketclaw/web/README.md`
- **Approach:**
  1. Build the picker from `@base-ui/react/combobox` parts: trigger with value and icon, then portal, positioner, popup holding an input, an empty state, and a list of items.
  2. Keep the current trigger classes and size limits, the popup width and `side="top"` placement, and the row markup (name, then `model · reasoning`).
  3. Filter with a case-insensitive substring match over name and model, following the cron palette filter. Turn on `autoHighlight` so the first match is highlighted while typing and Enter picks it.
  4. Apply KTD2 for initial focus. In the imperative "agent" command, stop focusing the textarea.
  5. Give the search input an accessible name that doesn't contain "Choose agent", because tests match that name as a substring on the trigger.
  6. Clear the search text when the picker closes. Keep the textarea's Escape-to-close handling.
  7. Remove the composer's now-unused Select imports, keeping those the theme selector needs.
  8. Add a README line about agent search.
- **Patterns to follow:** cron filter in `paletteRows`; `(pointer: coarse)` use in `CommandPalette`; base-ui Combobox "input inside popup" docs in the installed package.
- **Test scenarios:**
  - Covers R1: with agents `other` (model `gpt`) and `main` (model `root/model`), typing `root` leaves only `main`, and typing `OTH` leaves only `other`.
  - Covers R1: typing `zzz` shows the empty-state message and no options.
  - Covers R3: ArrowDown and Enter pick the highlighted agent, and the trigger text updates. Escape closes. Reopening shows the full list with an empty search.
  - Covers R1: typing `root` and pressing Enter picks `main`, closes the picker, and the trigger shows `main`.
  - Covers R2: at width 1280, the palette's "Choose agent" opens the picker and it stays open, with the search input focused.
  - Covers R2: at width 390 with touch, the same path opens the picker, but the search input isn't focused.
  - Covers R3: the existing assertions keep passing: the combobox named "Choose agent", option names, `innerText` of the trigger, trigger height of at least 44 and width of at most 144 at 320px, the popup wider than the trigger, and the single option for the cron session. Update the popup selector if the slot name changes.
  - Edge: the 2-second agents refetch while the popup is open doesn't reset the search text or close the popup.
- **Verification:** The browser suites pass at all their configured widths, and `make cloc` stays below 5250.

### U3. Code block wrap toggle

- **Goal:** Every code block can wrap long lines, with one remembered preference.
- **Requirements:** R4, R5; KTD3.
- **Dependencies:** none.
- **Files:**
  - `internal/rocketclaw/web/src/transcript-text.tsx`
  - `internal/rocketclaw/web/src/transcript-text.test.tsx`
  - `internal/rocketclaw/web/src/session-list.browser.test.ts`
  - `internal/rocketclaw/web/README.md`
- **Approach:**
  1. Add the KTD3 store and a toggle button with `aria-pressed`, using the `WrapText` icon. Like Copy, it appears in the expanded dialog's header for every block. Normal blocks also show it in the inline header next to Expand; compact blocks don't.
  2. Switch both `<pre>` elements between today's classes and the wrapped classes. Hide the horizontal `ScrollBar` while wrapped.
  3. Add a README line describing the toggle and that it is remembered in this browser.
- **Patterns to follow:** `timeline-detail.ts` store; the icon `Button` with `aria-pressed` used in `SessionActions`.
- **Test scenarios:**
  - Unit, R5 default: `renderToStaticMarkup(<CodeBlock …/>)` without `localStorage` renders `whitespace-pre` and an unpressed toggle (existing tests stay green).
  - Covers R4: in the browser at 1280 and 390, a fenced block with a long line overflows by default. After the toggle, `scrollWidth <= clientWidth`, including for a single 300-character unbroken token.
  - Covers R5: with two code blocks on the page, toggling one wraps both. After a reload, both are still wrapped and the toggles are pressed.
  - Covers R4: the expanded dialog's `<pre>` follows the preference. Toggling inside a normal block's dialog also wraps the inline block.
  - Covers R4: toggling off restores horizontal overflow.
  - Edge: the handoff dialog's compact header shows no wrap toggle, and the existing check that its buttons are at least 44px tall still passes.
  - Test isolation: each browser test starts with no `code-wrap` key.
- **Verification:** All scenarios pass, and unit tests run without `localStorage` stubs.

---

## Verification Contract

All commands run from `internal/rocketclaw/web` unless noted. This workspace has no `node_modules` yet. Keep `TMPDIR` under `<repo-root>/.tmp/`. The browser suites run inside `bun test` and don't skip themselves, so `ROCKETCLAW_PLAYWRIGHT_MODULE` and `ROCKETCLAW_CHROMIUM` must be set for every `bun test` and `make test` run.

| Gate | Command | Proves |
|---|---|---|
| Install | `bun install --frozen-lockfile` | Dependencies match the lockfile |
| Build | `bun run build` | Bundle and Tailwind classes regenerate for the browser suites, which serve the built assets |
| Tests | `bun test` | U1–U3 unit and browser scenarios, including the `sendComposer` guard and the `CodeBlock` server-render default |
| Lint | `make lint` (oxlint, `tsc --noEmit`, react-doctor at warning level) | No new lint or type findings |
| Tests and budget | `make test` | Tests plus the CLOC budget (below 5250 hazard, 5500 fail) |
| Repository gates | repo-root `make lint` and `make test` | No regressions elsewhere |

No Go files change, so `gofmt` and `go test ./...` are covered by the repository gates only.

---

## Definition of Done

- U1–U3 are implemented, and each unit's test scenarios exist and pass.
- Every Verification Contract gate passes, with web source CLOC below 5250.
- The README describes agent search, the wrap toggle, and the switch for unlisted agents.
- The touched diff has no unused imports, dead experiments, or abandoned approaches.
