---
title: Mark a Session Tab as Unread - Plan
type: feat
date: 2026-11-04
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-brainstorm
execution: code
---

# Mark a Session Tab as Unread - Plan

## Goal Capsule

- **Objective:** A person can mark any session tab as unread from its right-click menu or from the command palette, so the session waits for them, in bold, until they come back to it.
- **Means:** The person's own Unread mark, set on demand through the existing Tabs call, and flagged as self-set so it raises no desktop notification (KTD1, KTD2). The active tab keeps the mark by reporting its view as held, which the server records without clearing the mark, until the person goes to another tab (KTD3).
- **Authority:** This plan's Product Contract, then the Planning Contract. `AGENTS.md` governs code style and verification.
- **Execution profile:** One PR. Additive migration, proto fields, one store method, Web menu item. Lightweight.
- **Stop conditions:** Stop if a held view cannot be reported without changing how other tabs clear, or if the migration cannot add the column without rewriting existing rows.
- **Who finishes:** `ce-work` implements and verifies. `lfg` ships the PR.

## Product Contract

### Summary

Session tabs get a "Mark as unread" item. It sets the person's own Unread mark, so the tab goes bold. On the tab in view, the person stays put and the tab stays bold until they leave it and come back. The item also shows in the command palette as "Tabs: Mark as unread".

### Problem Frame

A person reads a session but wants to come back to it later. Today, having a tab active in a focused window clears its Unread mark at once, and nothing can set the mark again until another turn ends. So there is no way to flag a session as still needing attention.

### Requirements

**Marking**

- R1. A session tab's menu offers "Mark as unread" when a session it acts on is not already unread for this person. Search pages and other non-session tabs never offer it.
- R2. The command palette offers the same item as "Tabs: Mark as unread", acting on the current tab.
- R3. Marking sets this person's Unread mark on the session. It is stored on the server like any Unread mark, so it shows on every device and after a reload, and `is:unread` finds it.
- R4. With several tabs selected, the item marks every selected session tab, as Close acts on the whole selection.
- R5. On a Shared tab the mark is this person's only. Nobody else in the space is marked.

**The tab in view**

- R6. Marking the tab in view keeps the person there, and the mark stays while they keep viewing it. It clears the next time they go to another tab and then come back.

**Notifications**

- R7. A mark the person set themselves raises no desktop notification on any of their windows or devices. A turn that ends afterwards notifies as it does today.

### Key Decisions

- Marking the tab in view keeps the person there with the mark held until they leave and come back. (session-settled: user-directed — chosen over switching to another tab Gmail-style, and over offering the item only on tabs not in view: the person stays where they are, and the palette, which acts on the current tab, keeps working.) Governs R6.
- No desktop notification for a self-set mark (session-settled: user-approved — chosen over letting the mark notify like a turn's end: the person just did it, so a notification would be noise). Governs R7.
- The mark is per person on Shared tabs, as Unread already is (session-settled: user-approved — chosen over marking everyone in the space: Unread is a personal state). Governs R5.

### Scope Boundaries

- "Mark as read" is not built. Opening the tab already clears the mark.
- No instant bold before the next session poll (about 2 seconds). The poll already carries every mark change.
- Top Shelf rows from another RocketClaw get the item only if their menu already comes from the tab commands this plan changes. No new cross-server call is added.

---

## Planning Contract

### Key Technical Decisions

- KTD1. **Mark through the existing Tabs call.** Add `string mark_unread = 13` (a tab path) to `TabsRequest`, beside `unmute`. `changeTabs` checks `visibleConversation` first, as `muteTab` does, then calls a new `SessionService.MarkUnread(ctx, username, path)`. The store upserts the person's `web_tabs` row under `tabPathSQL`, the way turn ends write marks (`internal/rocketclaw/backend/store_dao.go` turn-finish SQL), so a closed or Shared tab gets a row like a turn-end mark gives it. A row that is already unread is left as it is, so a turn-end mark keeps its time and its notification. Implements R3, R5.
- KTD2. **A self-set mark is flagged as such, in the database.** New migration `083_web_tab_marked_unread.sql` adds `marked_unread BOOLEAN NOT NULL DEFAULT FALSE` to `web_tabs`. `MarkUnread` sets it true. Both turn-end mark writers set it false: the turn-finish SQL in `store_dao.go` and the remote OpenCode sync in `internal/rocketclaw/backend/opencode.go`. The listing reads it with `unread_at` (`internal/rocketclaw/backend/store.go`), and `Session` gains `bool marked_unread = 33`, set only while `unread_at` is set. `TabNotifications` in `internal/rocketclaw/web/src/ui.tsx` skips a row whose mark is self-set. A flag only this window kept would leave the person's other windows and devices notifying, which breaks R7. The listing's per-row fingerprint covers the new field, so the poll delivers it. Implements R7.
- KTD3. **The tab in view keeps its mark through a held view.** The session poll sends `viewing` for the active session in a focused window, and `ListSessions` calls `ViewSession`, which records the view and clears the mark (`internal/rocketclaw/frontend/rpc/server.go`). Marking the active tab stores the held server and session in `sessionStorage`, so a reload of the same browser tab keeps the hold. While the page shows that session on that server, the poll still sends `viewing` plus a new `hold` flag on `ListSessionsRequest`. With `hold`, `ViewSession` records the view but leaves `unread_at` alone. The person still counts as viewing, so the 10-second rule in both turn-end writers still keeps a turn that ends while they watch from marking or notifying (R7). Navigation to a different session clears the hold, a palette preview of another session included, so coming back clears the mark as usual. A poll already in flight when the mark lands can still clear it (see Risks). Implements R6, R7. (session-settled: user-directed — chosen over switching to another tab Gmail-style, and over offering the item only on tabs not in view: the person stays where they are, and the palette keeps working.) Governs R6.
- KTD4. **The item lives in `useTabs().commands`.** The right-click menu (`menuItems`) and the palette (`tabCommands`) both read this list. It is gated like the notify items, on `pathSession(path)`, and is shown while any targeted session row lacks `unreadAt`. `menuItems` filters a Shared row's commands by key, so the new key joins that filter. On a multi-selection it sends one call per selected session path. Implements R1, R2, R4, R5.

### Assumptions

- Session listing polls every 2 seconds, so the bold title appears on the next poll rather than at the click.
- `changeTabs` may cross the gocyclo limit with one more case. If it does, move the case's body into a helper the way `muteTab` already is.

### Risks

- A poll that left the browser with `viewing` set just before the mark arrives can clear a mark set on the tab in view. The window is under one poll interval. If tests show it, re-mark is the user's recovery. A server-side fix would need a view counter and is not built.

---

## Implementation Units

### U1. Store a self-set Unread mark

- **Goal:** The store can mark a session unread for one person, flagged as self-set, and turn ends clear the flag.
- **Requirements:** R3, R5, R7. KTD1, KTD2.
- **Dependencies:** none.
- **Files:** `internal/rocketclaw/backend/migrations/083_web_tab_marked_unread.sql` (new), `internal/rocketclaw/backend/store_dao.go`, `internal/rocketclaw/backend/opencode.go`, `internal/rocketclaw/backend/store.go`, `internal/rocketclaw/backend/unread_test.go`.
- **Approach:**
  1. Add the column (KTD2).
  2. Add `MarkUnread` next to `ViewSession`, with its doc comment stating the rule.
  3. Give `ViewSession` the held-view case (KTD3): it records the view and leaves `unread_at` alone.
  4. Set `marked_unread = false` in both turn-end upserts.
  5. Read `marked_unread` into the listing row (a `MarkedUnread bool` beside `UnreadAt`).
- **Patterns to follow:** `ViewSession` and `MuteTab` in `store_dao.go`; the `unreadBy` helper in `unread_test.go`.
- **Test scenarios:** (`unread_test.go` runs against the real test database, so it proves the SQL. It does not prove the RPC wiring.)
  - Alice marks a read session: alice has it unread and self-set, bob does not.
  - Alice marks a session whose tab she closed: the closed tab's row holds the mark and `is:unread` finds it.
  - Alice marks, then a turn alice sent ends while she is not viewing: still unread, no longer self-set.
  - A turn-end mark, then alice marks: `unread_at` keeps the turn's time and stays not self-set.
  - Alice marks, then views it as held: the mark stays and stays self-set. A turn alice sent then ends: still self-set, because she counts as viewing.
  - Alice marks, holds, then views it without hold: the mark clears.
  - A Shared session marked by alice leaves every other person unmarked.
- **Verification:** The new tests pass through `make -C internal/rocketclaw test`.

### U2. Serve the mark through the Tabs call and the listing

- **Goal:** The Web can set the mark, report a held view, and read whether a mark is self-set.
- **Requirements:** R3, R6, R7. KTD1, KTD2, KTD3.
- **Dependencies:** U1.
- **Files:** `internal/rocketclaw/web/proto/web.proto`, `internal/rocketclaw/frontend/rpc/web.pb.go` and `internal/rocketclaw/frontend/rpc/protocol.gen.go` (regenerated with `go generate ./internal/rocketclaw/frontend/rpc`), `internal/rocketclaw/frontend/rpc/tabs.go`, `internal/rocketclaw/frontend/rpc/server.go`, `internal/rocketclaw/frontend/rpc/http.go` (reads `hold` from the query beside `viewing`), `internal/rocketclaw/frontend/rpc/tabs_test.go`, `internal/rocketclaw/frontend/rpc/reads_test.go` (the existing `viewing` test), and the Web's generated proto types if the Web build generates them.
- **Approach:**
  1. Add `mark_unread` to `TabsRequest`, `hold` to `ListSessionsRequest`, and `marked_unread` to `Session`, with comments in the file's style. Update the `TabsRequest` comment's list of one-change kinds.
  2. Add the `changeTabs` case (KTD1).
  3. Pass `hold` from `listSessions` to `ViewSession` (KTD3).
  4. Set `MarkedUnread` in `sidebarSession` only alongside `UnreadAt`.
- **Patterns to follow:** The `Unmute` case and `muteTab` in `tabs.go`, and the `UnreadAt` mapping in `sidebarSession`.
- **Test scenarios:** (`tabs_test.go` drives the RPC server against the test database. It proves authorization and wiring, not the Web.)
  - A Tabs call with `mark_unread` for a visible session marks the caller only, and the next `ListSessions` row has `unreadAt` and `markedUnread` set.
  - A Tabs call with `mark_unread` for a conversation the caller may not see is refused and writes nothing.
  - `ListSessions` with `viewing` and `hold` leaves the caller's mark in the listing. Without `hold` it clears the mark.
- **Verification:** The proto hash file matches the proto, and RPC tests pass through `make -C internal/rocketclaw test`.

### U3. Web: the menu item, the held mark, and no notification

- **Goal:** People mark tabs from the menu and the palette, the tab in view keeps its mark, and self-set marks stay silent.
- **Requirements:** R1, R2, R4, R5, R6, R7. KTD3, KTD4.
- **Dependencies:** U2.
- **Files:** `internal/rocketclaw/web/src/ui.tsx`, `internal/rocketclaw/web/src/tabs.browser.test.ts`, `internal/rocketclaw/web/src/notifications.browser.test.ts`.
- **Approach:**
  1. Add the `unread` command in `useTabs().commands` (KTD4). It calls the Tabs mutation once per selected session path and records the held session when the active tab is among them (KTD3).
  2. Add the key to `menuItems`'s Shared filter.
  3. In the session poll, send `hold` with `viewing` for the held session, and clear the hold on navigation to another session (KTD3).
  4. In `TabNotifications`, skip rows whose mark is self-set (KTD2).
  5. Keep new branches out of `SessionApp` and `CommandPalette`, per `docs/solutions/best-practices/react-doctor-complexity-limit-shapes-web-components.md`.
- **Execution note:** Browser tests run the last built bundle. Rebuild with `make` before trusting a pass, per `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`.
- **Test scenarios:** (Browser tests use a mocked server, so they prove the requests the page sends and what it renders. U1 and U2 prove the server side.)
  - Right-clicking a read, inactive session tab shows "Mark as unread". Choosing it sends a Tabs call with that tab's path.
  - The menu of a tab already unread, and of a Search page tab, has no "Mark as unread".
  - The palette lists "Tabs: Mark as unread" on a read session and sends the call for the current tab.
  - With two session tabs selected, the item sends one call per tab.
  - With an unread tab and a read tab selected, right-clicking the unread one still offers the item.
  - A Shared tab's menu offers the item.
  - After marking the active tab, later polls send that session with `hold`. After going to another tab and back, the poll sends it without `hold`.
  - After marking the active tab and reloading the page, polls still send that session with `hold`.
  - A row that turns unread with `markedUnread` set raises no notification. The same row turning unread later without it does notify.
- **Verification:** `make -C internal/rocketclaw/web lint test` passes, react-doctor included.

### U4. Document the new way to set Unread

- **Goal:** The README and the glossary describe self-set marks.
- **Requirements:** R1, R2, R6, R7.
- **Dependencies:** U3.
- **Files:** `internal/rocketclaw/web/README.md` (the Unread bullet, the notification paragraph, and the tab menu and palette lists where they enumerate items), `CONCEPTS.md` (Unread).
- **Approach:**
  1. Add the self-set mark to the Unread entry in both files: how it is set, that it holds on the tab in view until the person leaves and returns, and that it raises no notification.
  2. Say in the README's notification paragraph that a mark the person set themselves does not notify.
  3. Add "Mark as unread" to the README's tab menu items and "Tabs: Mark as unread" to its palette items, where those lists exist.
- **Test expectation:** none -- documentation only.
- **Verification:** The README's Unread bullet, notification paragraph, and menu and palette lists, and `CONCEPTS.md`, agree with R1, R2, R6, R7.

---

## Verification Contract

| Check | Command | Proves |
|---|---|---|
| Proto regenerated | `go generate ./internal/rocketclaw/frontend/rpc` | `web.pb.go` and `protocol.gen.go` match `web.proto` |
| Go format | `gofmt -l` on touched Go files | No formatting drift |
| RocketClaw Go tests | `make -C internal/rocketclaw test` (Linux container on a Mac) | U1, U2 |
| Web lint and tests | `make -C internal/rocketclaw/web lint test` | U3, react-doctor limits |
| Repo lint and tests | `make lint` and `make test` | Repo-wide gates, CLOC budget |

## Definition of Done

- R1 to R7 hold, each proved by a U1 to U3 test scenario.
- Every command in the Verification Contract passes.
- No abandoned-attempt code, debug output, or unused helper is left in the diff.
- README and `CONCEPTS.md` describe self-set marks (U4).
