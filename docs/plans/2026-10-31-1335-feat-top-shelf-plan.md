---
title: Top Shelf - Plan
type: feat
date: 2026-10-31
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Top Shelf - Plan

## Goal Capsule

- **Objective:** A person working several RocketClaws from one Web page can jump to any tab on any of those servers in one click, from a list that never changes as they move between spaces and servers.
- **Means:** a personal "Top Shelf" tab group above the space picker, stored on the local server (KTD1, KTD2).
- **Authority:** the user's decisions in Key Decisions win; then this plan; then repository instructions.
- **Stop conditions:** a settled decision proves unworkable; web lint or the web CLOC budget cannot be met honestly.
- **Execution profile:** one pull request; the work is already partly built in this workspace.

---

## Product Contract

### Summary

Add Top Shelf: a tab group above the space picker that appears only when the local RocketClaw has remote RocketClaws configured. It holds tabs from any server, is the same in every space and on every server, and opening one of its tabs shows that page on its own server and space without creating a tab.

### Problem Frame

With remote RocketClaws, tabs live per server and per space. Reaching a chat on another server means switching server, then space, then finding the tab. People who work several servers keep a handful of pages they visit constantly across all of them.

### Key Decisions

- **Personal, not shared.** Governs R1. (session-settled: user-directed — chosen over a Top Shelf shared by everyone like the Shared group: it is one person's navigation aid)
- **Behaves like any other tab group for adding, removing and ordering.** Governs R3, R4. (session-settled: user-directed — chosen over a fixed list edited only by menu: it should feel like the groups people already use)
- **Opening shows the page and stays on the shelf; no tab is created.** Governs R5. (session-settled: user-directed — chosen over opening as a normal tab in the space's list: the shelf is the place to return to)
- **Each item shows where it came from.** Governs R6. (session-settled: user-directed — chosen over a bare title: tabs on different servers can share titles)
- **Search pages are fully supported on the shelf.** Governs R7. (session-settled: user-directed — chosen over keeping Search pages off the shelf and over opening their tab: every tab kind should work the same)
- **Web CLOC budget raised from 8300 to 9300.** (session-settled: user-directed — chosen over freeing room by simplifying unrelated web code)

### Requirements

**Presence and storage**
- R1. Top Shelf is one person's own, kept by the local server, and shown above the space picker only when remote RocketClaws are configured.
- R2. It is the same in every space and on every server's view.

**Editing**
- R3. Any tab except New session can be added from its menu, or by dragging it onto the shelf; an item is removed from its menu, its close button, or middle-click.
- R4. Items reorder by dragging or Shift+Arrow, and the shelf collapses like a group, showing only its active item, bold while a hidden item is unread, and "Needs you" while a hidden item's session waits on the person.

**Opening**
- R5. Clicking an item opens its page on its server and in its space, without creating a tab, and the item stays highlighted while its page shows.
- R6. Each item shows its server and space, for example "wallace · Main".
- R7. A shelved Search page opens without a tab; its edits save into the shelf. A tab of the same Search page in that space takes precedence, as it does over Shared.

**Notifications**
- R8. A shelved session notifies like an open tab, and counts in the window title's "needs you" count.

### Scope Boundaries

- Cmd/Ctrl+[ ] stepping stays within the current space's Shared and tabs; it does not step through the shelf, whose items can sit on other servers.
- Mute and snooze stay in a tab's own menu on its server; shelf items do not offer them.
- A shelved item keeps the space it was shelved from; moving the original tab later does not update it. An item whose space was deleted opens in the current space.
- One window's shelf saves go one at a time, in order, but saves carry no revision: two windows editing the shelf keep the last save, and a window sees another's edits when it polls or regains focus. Considered and not built because the loss is visible and the shelf is one person's; a revision would be warranted if people report lost shelf edits.

---

## Planning Contract

### Key Technical Decisions

- KTD1. **One JSONB row per person** (`web_top_shelves`), holding the Web's `TopShelf` message in protobuf JSON, read and replaced through the existing Tabs RPC (`TabsRequest.top_shelf`, `TabsResponse.top_shelf`). Mirrors how Search pages are stored (`068_web_tab_search.sql`).
- KTD2. **The Web reads the shelf from the local server's cached Tabs reply** (`queries.tabs(owner, "")`), which the local view or the local session list already polls; saves write the whole shelf and update that cache at once, cancelling a poll in flight.
- KTD3. **Opening without a tab reuses Shared's path:** a server's own shelved tabs join the list `openTab`, `loadTabs` and `entry` treat as reachable without a tab. A shelved tab of a deleted space counts in the current space.
- KTD4. **A shelved Search page edits the shelf's copy:** `SearchPage` falls back to the shelf's page when no Shared or own tab holds it, and `editSearch` saves into the shelf when no own tab exists.
- KTD5. **First load waits for the shelf** when remotes are configured, so reloading a shelved page does not add a tab.
- KTD6. **Shelf rows identify items by index, not path,** since two servers can shelve the same path (`/agents`).
- KTD7. **react-doctor shapes the rows:** each row is its own component, so its session list context value is memoized, and the origin label sits outside `TabItem`, keeping `TabItem` unchanged.

---

## Implementation Units

### U1. Storage and RPC

- **Goal:** the local server keeps each person's shelf. (R1)
- **Files:** `internal/rocketclaw/web/proto/web.proto`, `internal/rocketclaw/frontend/rpc/web.pb.go`, `internal/rocketclaw/frontend/rpc/protocol.gen.go`, `internal/rocketclaw/backend/migrations/079_web_top_shelves.sql`, `internal/rocketclaw/backend/store_dao.go`, `internal/rocketclaw/frontend/rpc/tabs.go`, `internal/rocketclaw/frontend/rpc/tabs_test.go`
- **Approach:** KTD1.
- **Test scenarios:**
  - A person with nothing shelved gets no shelf in the reply.
  - A saved shelf, including a Search page and a remote item, comes back unchanged on a plain read.
  - Another person does not see it.
  - Saving an empty shelf leaves it empty.
- **Verification:** `TestTabs` passes.

### U2. Web shelf and opening without a tab

- **Goal:** the shelf, its editing, and opening without a tab, Search pages included. (R2–R8)
- **Dependencies:** U1
- **Files:** `internal/rocketclaw/web/src/ui.tsx`, `internal/rocketclaw/web/src/types.ts`, `internal/rocketclaw/web/src/api.ts`, `internal/rocketclaw/web/Makefile`
- **Approach:** KTD2–KTD7; collapse, unread and "Needs you" follow `SharedTabs`.
- **Test scenarios:** covered by U3.
- **Verification:** `make lint` and `make cloc` in `internal/rocketclaw/web` pass.

### U3. Cross-server browser test

- **Goal:** prove the shelf across a local and a remote RocketClaw. (R3, R5, R6, R7)
- **Dependencies:** U2
- **Files:** `internal/rocketclaw/web/src/remote-transport.test.ts`
- **Test scenarios:**
  - On the remote view, "Add to Top Shelf" on the remote chat's tab puts it on the shelf labelled "work · Main"; closing that tab keeps the item.
  - From a local space, clicking the item opens the remote chat on the remote, with no tab in the remote's strip and the item selected.
  - A local Search page shelved and closed opens from the shelf with its query and no tab; an edit to its query survives a reload.
  - Removing an item from its menu takes it off the shelf.
- **Verification:** `TestRemoteRocketClawRealBrowser` passes.

### U4. Docs

- **Goal:** describe Top Shelf where remote RocketClaws are described.
- **Files:** `README.md`, `internal/rocketclaw/web/README.md`, `CONCEPTS.md`
- **Test expectation:** none -- documentation.

---

## Verification Contract

| Gate | Command |
|---|---|
| Web lint, CLOC | `make lint`, `make cloc` in `internal/rocketclaw/web` |
| Web tests | `make test` in `internal/rocketclaw/web` |
| Go | `go test ./...`, root `make lint`, root `make test` |

Source `.tmp/test-env.sh` first, and check that `bun run build` ends with `Built SPA:` before trusting a browser test.

## Definition of Done

- R1–R8 hold, and every gate above passes on the final commit.
- No abandoned-attempt code remains in the diff.
