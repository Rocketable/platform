---
title: Many Named Search Pages - Plan
type: feat
date: 2026-10-26
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Many Named Search Pages - Plan

## Goal Capsule

- **Objective:** A person can keep as many Search pages open as they like, each with its own name. They can jump to any of them from Cmd+P and arrange them in tab groups and spaces like any other tab, on every device they use.
- **Means:** Each Search page is its own strip tab at `/search/<id>`. Its name and inner searches ride on that tab in the saved tab state (KTD1, KTD2).
- **Authority:** Product Contract requirements win on behavior. KTDs win on mechanism. Units override neither.
- **Stop conditions:** Stop and report instead of continuing when:
  - a unit would push Go source over `GO_SOURCE_CLOC_BUDGET` (`internal/rocketclaw/Makefile`). The person raised it from 26000 to 26750 for this work; do not raise it further.
  - a unit would push Web TS over `TS_CLOC_BUDGET` (8250, `internal/rocketclaw/web/Makefile`; 8035 at plan time).
  - `react-doctor` fails on a touched React function, and splitting out a component or hook does not fix it. Never suppress the rule without approval.
  - migration number `068` is taken by the time the unit lands.
  - a session-settled decision below proves infeasible.
- **Execution profile:**
  1. Storage and API first (U1).
  2. Then the Web tab model (U2), the Search page (U3), Cmd+P and the opening commands (U4), and docs (U5).
  3. Web units land with the rebuilt, committed bundle in `internal/rocketclaw/internal/web/dist/`.
- **Finishes and ships:** the implementer, through the repo's normal PR flow.

---

## Product Contract

### Summary

Search stops being a single tab. Each Search page is an ordinary tab that can be:
- named
- grouped
- moved to another space
- found in Cmd+P

The small tab bar inside a Search page stays. A page's name and searches are saved on the server with the rest of the person's tabs.

### Problem Frame

Today the strip has exactly one Search tab, because a tab is identified by its location and every search lives at `/search`. Several searches can exist only as inner tabs inside that one page. Those inner tabs have three problems:
- They are kept only in the browser's local storage, so they don't follow the person to other devices.
- They cannot be put in a tab group or a space.
- They are invisible to Cmd+P.

People who track several recurring searches have no way to keep them apart, label them, or file them alongside the sessions they belong with.

### Requirements

**Many pages, each a tab**

- R1. A person can open any number of Search pages. Each is its own tab in the sidebar strip.
- R2. Search pages never open as preview tabs. Opening a session from a result does not replace the Search page's tab.
- R3. A Search page can be added to a tab group, removed from it, reordered, and moved to another space, like any other tab. It keeps its name and searches through every one of these moves.
- R4. Closing a Search page's tab discards that page, including its name and searches. This includes the bulk closes ("Close others", "Close to the right", "Close all", "Close group"), which close Search pages like any other tab.
- R5. A Search page's sidebar label is its name. Without a name, the label is its active search's query. Without a query, the label is "Search".

**Naming**

- R6. A person can rename a Search page from its tab menu ("Rename search"), editing the name in place in the sidebar. Enter or leaving the field saves the name. Escape keeps the old one. An empty name falls back as R5 describes.
- R7. The inner tab bar inside each Search page is kept as it is today: add, close, rename, and switch inner searches.
- R8. Closing a page's last inner search closes the page's tab. It then returns to the last message the person saw, as closing the last search did before. On a Shared Search page it only returns there, and the Shared page and its searches stay as they are for everyone.
- R8a. Escape does not close a Search page, since its searches would go with it. Escape still closes pages that hold nothing, such as Cron or Settings.

**Saved with the person's tabs**

- R9. A page's name, its inner searches, and which one is active are saved on the server with the person's tabs. They follow the person across devices and windows the way tabs and groups do. Typing a query does not save on every keystroke.
- R10. The searches someone saved in this browser before this change are not lost. The first Search page opened after the upgrade takes them in, and the browser copy is then removed.

**Finding and opening**

- R11. When text is typed, Cmd+P lists every open Search page in every space whose label or inner queries match it, labeled as in R5. Choosing one opens it, entering its space when needed. An empty query lists sessions only, so Enter still opens the first session.
- R12. "Sessions: Search" opens a new, empty Search page.
- R13. "Search everywhere for '<text>'" opens the Search page that already has an inner search with exactly that query, and makes that inner search active. When no page has one, it opens a new page with that query.
- R14. A `/search?q=<text>` link keeps working. It opens the `/search` Search page with that query, as it does today.

**Sharing**

- R15. A Search page can be added to a space's Shared group. There it is one page that everyone in the space sees and edits: its name, its searches, and which search is shown. Sharing takes the sharer's page as the starting point. Anyone can rename it from its Shared row, and edits save to that one page. Whoever takes it out of Shared keeps that one page, with everything people added, as their own Search page, so nobody's work is lost; anyone else keeps only what they had before.
- R16. Opening a link's query happens once, in the page the link led to. A page that reloads in place, such as when it is shared or unshared, shows what it holds and does not take the previous page's query.

### Scope Boundaries

- **Not in scope:**
  - Listing all open tabs (Cron, Settings, hosted pages) in Cmd+P. The person chose to list Search pages only.
  - A palette command to rename a Search page; the tab menu is the entry point.
  - Live sync while typing. An open Search page takes in a newer saved copy, from another window or person, when its window regains focus, unless typed text is still waiting to be saved.
- **Considered and not built:**
  - Restoring a closed Search page. Closing a page discards it, as closing any tab does. Revisit if people lose searches they meant to keep.
  - Keeping results warm when leaving a Search page. Results are refetched on return, as today.
  - Saving typed text on page unload. A query typed less than half a second before a reload or a closed browser may be lost. Leaving the page inside the app saves it at once. Revisit if people report lost keystrokes.

### Assumptions

- "Reuse if same query" (the person's answer) applies only to commands that carry a query. "Sessions: Search" has no query, so it always opens a new page (R12). Otherwise there would be no way to start a second empty page.
- Each save replaces the whole page, so when two people edit the same page, or one edits while typed text from another waits to be saved, the last save wins.

---

## Planning Contract

### Key Technical Decisions

- KTD1. **A Search page's location is `/search/<id>`.** The id is random, made when the page is created. A tab is keyed by location everywhere: the client merges, `openTab`, and the `web_tabs (username, path)` primary key. A distinct location per page lets every existing tab, group, and space mechanism work unchanged. The legacy `/search` location stays valid as one more Search page (R14), so old links and saved strip tabs keep working. A single predicate decides what counts as a Search page: `/search` or anything under `/search/`. (session-settled: user-directed — chosen over keeping search only as inner tabs inside one `/search` page: the person clarified that "search tabs" means the strip's Search pages.)
- KTD2. **The page's content is one optional `search` field on the tab.** That field holds:
  - the page name
  - the inner searches (id, name, query)
  - the active inner search id

  It is a proto message `SearchPage` on `Tab` (`internal/rocketclaw/web/proto/web.proto`). The backend keeps it as one nullable `web_tabs.search JSONB` column, which the RPC layer fills with `protojson` and reads back the same way. The backend treats it as an opaque document of the page, which no SQL reads. At plan time, under the original 26000 Go budget, this cost about 12 lines against 19 of headroom, where typed backend structs would have cost about 35. (session-settled: user-approved — chosen over browser-only storage: names and searches follow the person across devices, like tabs and groups.)
- KTD3. **Keep the inner tab bar.** `SearchTabs` keeps its UI. It reads its starting state from the page's tab instead of local storage, and writes back to the tab state rather than local storage. (session-settled: user-directed — chosen over removing the inner tabs: the person picked "Keep it".)
- KTD4. **Query typing saves after a pause, not per keystroke.** Every tab-state change redraws the app and queues a save; the group-name field already saves only on blur for this reason (`ui.tsx` group editor comment). `SearchTabs` keeps local state as now:
  - Discrete actions save at once: add, close, select, and rename.
  - Query edits save after about half a second of quiet.
  - A pending edit is saved when the page unmounts.
- KTD5. **Cmd+P lists Search pages as rows of their own in the sessions palette.** They show once text is typed, above session rows, and are filtered with the palette's existing match tiers over the label and inner queries. Choosing a row calls `tabs.open(path)`, which already enters the tab's space. (session-settled: user-directed — chosen over listing every open tab: the person picked Search pages only.)
- KTD6. **The opening commands pass the query through the URL.** "Search everywhere" finds a matching page with a query, else makes a new id. It then opens that page permanently with `?q=`. The existing `withQuery` logic in `SearchTabs` then activates or adds the inner search. `tabs.open` gains an optional query-string argument, because a tab's path never includes the query. (session-settled: user-directed — reuse a page with the same query, chosen over always opening a new page.)
- KTD7. **Every place that rebuilds a tab object keeps its other fields.** These places currently rebuild a tab as a fresh object and would silently drop the `search` field, losing a page's name and searches on a move:
  - `moveTo`
  - `leave`
  - `join`
  - group "Move group to …"
  - `savedState`

  They spread the existing tab instead. `mergeTabs`'s change signature includes the `search` field, so a rename or edit in one window wins over a stale copy from another.
- KTD9. **A Shared Search page is stored on its Shared row.** `web_shared_tabs` gains the same `search` JSONB column, and `SharedTab` gains the `SearchPage` field. Sharing adds the row with the sharer's page. Edits go through a separate update-only `editShared` request carrying the page they were made from, and the server applies one only when the saved page is still that base; a refused edit is made again onto the newer page (their searches, plus this person's additions, changes, and closings), so a window with an older copy never erases what others added, and an edit from a window that missed the page leaving Shared never brings it back. Removing the page from Shared returns the page the server held, which becomes the remover's own tab, merged with any searches of their own page at that location. A save from a Web that predates Search pages leaves a tab's saved page in place. Deleting a space moves the page along with its Shared row. (session-settled: user-directed — one page shared by all, chosen over a copy taken at sharing time and over an empty page: Shared tabs are the same for everyone.)
- KTD8. **Migration `068`.** `067` is taken by the in-flight `behavior-settings-postgresql` branch (`067_behavior_settings.sql`). The migration runner orders by name, so a gap is harmless.

### High-Level Technical Design

```mermaid
flowchart LR
  P[Cmd+P / Search everywhere] -->|tabs.open('/search/id', '?q=')| T[useTabs state: Tab.search]
  T -->|savedState + flush| R[Tabs RPC]
  R -->|protojson| D[(web_tabs.search JSONB)]
  T -->|tab for path| S[SearchPage / SearchTabs]
  S -->|editTab on actions; debounced on typing| T
  T --> L[TabItem label: name, else active query, else Search]
```

### Sources & Research

- `internal/rocketclaw/web/src/ui.tsx`:
  - tab model (`Tab`, `openTab`, `join`, `leave`, `savedState`, `mergeTabs`)
  - `useTabs` (`moveTo`, `commands`, `groupCommands`, `open`)
  - `TabStrip`, `TabItem`, `TabRename`, `SearchPage`/`SearchTabs`
  - `paletteRows`, `CommandPalette`
- `internal/rocketclaw/frontend/rpc/tabs.go` (`tabs`, `savedTabState`)
- `internal/rocketclaw/backend/store_dao.go` (`Tab`, `TabState`, `SaveTabState`)
- `internal/rocketclaw/backend/migrations/060_web_tab_rows.sql`
- `internal/rocketclaw/internal/web/assets.go` (SPA route list)
- `docs/solutions/best-practices/react-doctor-complexity-limit-shapes-web-components.md` and `docs/solutions/performance-issues/search-page-froze-rendering-every-result.md`

---

## Implementation Units

### U1. Store and serve a Search page's content

**Goal:** Tab state and Shared tabs carry an optional Search page document end to end.

**Requirements:** R9, R15

**Dependencies:** none

**Files:**
- `internal/rocketclaw/backend/migrations/068_web_tab_search.sql` (new)
- `internal/rocketclaw/web/proto/web.proto`, plus regenerated `internal/rocketclaw/frontend/rpc/web.pb.go`
- `internal/rocketclaw/backend/store_dao.go`
- `internal/rocketclaw/frontend/rpc/tabs.go`
- `internal/rocketclaw/internal/web/assets.go`
- Tests: `internal/rocketclaw/backend/store_test.go`, `internal/rocketclaw/frontend/rpc/tabs_test.go`, `internal/rocketclaw/internal/web/assets_test.go`

**Approach:**
1. The migration adds a nullable `search JSONB` to `web_tabs`. The down migration drops it.
2. The proto adds `SavedSearch { id, name, query }`, `SearchPage { name, repeated SavedSearch searches, active }`, and `Tab.search = 6`. Regenerate the Go code with the repo's existing generator.
3. The backend `Tab` gets one field holding the page's JSON (nil for other tabs).
   - `TabState` selects and scans it.
   - `SaveTabState` passes it as one more unnest column: text, with an empty string mapped to NULL.
   - The upsert writes `search`.
   - The "clear tab state" update for unread rows nulls it.
4. `tabs.go` converts `Tab.Search` to and from JSON with `protojson` in `savedTabState` and in the state read-back.
5. The Share case stores the Shared Search page's document, and a new update-only `editShared` case saves edits to it (KTD9).
6. `assets.go` serves the SPA for `/search/` paths.

**Patterns to follow:** how `group_id` and `space` are carried in `SaveTabState`, and how `ShareTab` and `DeleteSpace` carry a Shared row.

**Test scenarios:**
- Saving a tab with a search document and reading it back returns the same document. A tab without one reads back without one.
- A save that moves a Search tab to another group or space keeps its document.
- Through the RPC: a `TabsRequest.State` with a `Tab.Search` page round-trips proto-equal in a later read.
- Sharing a Search page stores its page for everyone; sharing it again keeps that page. An edit replaces the page and keeps its place, and an edit to a page no longer shared adds nothing. Deleting the space moves the page with the row.
- `GET /search/abc` serves `index.html`.

**Verification:** the backend and RPC tests pass, and Go source CLOC stays under budget.

### U2. Search pages in the tab model

**Goal:** Search pages are first-class strip tabs: permanent, nameable, labeled, and safe through moves.

**Requirements:** R1, R2, R3, R4, R5, R6, R8a, R15, R16

**Dependencies:** U1

**Files:**
- `internal/rocketclaw/web/src/ui.tsx`
- `internal/rocketclaw/web/src/types.ts`
- Test: `internal/rocketclaw/web/src/tabs.browser.test.ts`

**Approach:**
1. `SavedTabs` tab type and `Tab` gain `search?: { name?, searches, active }`.
2. Add a Search page predicate and a label helper (R5). Use them in `TabItem` and in Cmd+P (U4).
3. `openTab` never opens a Search page as preview (R2).
4. `moveTo`, `leave`, `join`, group moves, and `savedState` keep `search` (KTD7). `mergeTabs` includes it in the change signature.
5. `useTabs` exposes a way to change one tab's `search`.
6. The tab menu offers "Rename search" for a single Search page, as it offers "Rename session" for sessions.
   - The in-place field writes the name into the tab state.
   - `TabRename` is generalized just enough to save through either path.
7. "Add to Shared" on a Search page shares it with the person's page. Its Shared row offers "Rename search", and the page shown there saves to the Shared page (R15).
8. `useRoute().search` matches every Search page location, and the Escape page-close skips Search pages; the Shortcuts page's Escape row reads "Pages other than sessions and Search pages" (R8a).

**Patterns to follow:** "Rename session" (`TabStrip` menu items, `TabRename`). The `/` share exclusion in `commands`.

**Test scenarios:**
- Opening `/search/a` and then `/search/b` gives two Search tabs in the strip.
- Opening a session from a Search page leaves the Search tab in place, because it is not a preview tab.
- A Search tab named "Triage" shows "Triage". Unnamed with active query "is:unread", it shows "is:unread". With neither, it shows "Search".
- "Rename search", then typing a name and pressing Enter, relabels the tab. Escape keeps the old name.
- Adding a named Search tab to a group, then moving it to another space, keeps its name in the saved state the server receives.
- Adding a named Search page to Shared shows its name in the Shared row. Typing there and renaming the row save to the Shared page. Another person's change shows in the open page once the window is focused. Removing it from Shared keeps the Shared page, with that change, as the person's own tab (R15, R16).
- Pressing Escape on a Search page leaves its tab open, and its close button closes it (R8a, `session-list.browser.test.ts`).

**Verification:** `tabs.browser.test.ts` passes, including updated expectations for the single "Search" tab.

### U3. Search page content from tab state

**Goal:** Each Search page reads and writes its inner searches through its tab. Legacy local searches are imported once.

**Requirements:** R7, R8, R9, R10, R14

**Dependencies:** U2

**Files:**
- `internal/rocketclaw/web/src/ui.tsx` (`SearchPage`, `SearchTabs`)
- Test: `internal/rocketclaw/web/src/search-page.browser.test.ts`

**Approach:**
1. `SearchPage` waits for the person's tab state to load, then mounts `SearchTabs` keyed by the page's location. Two pages never share component state.
2. The starting state comes from the tab's `search`. When the tab has none, it comes from the legacy local-storage entry for this person, with the existing legacy filter folding. After that first load, the local entry is removed (R10). A `?q=` on the URL goes through the existing `withQuery`.
3. Writes go to the tab state per KTD4. A page shared in the space saves to its Shared page (KTD9); only the person's own page takes the old browser searches.
4. The URL rewrite and the `popstate` hand-off use the page's own location instead of `/search`.
5. Closing the last inner search closes the page's tab (R8).
6. The draft-tab bookkeeping that existed only to avoid saving an untouched local entry goes away, since the strip tab exists anyway.

**Patterns to follow:** the existing `SearchTabs` behavior and tests. Keep `react-doctor` passing, and split out a hook if the component crosses the complexity limit.

**Test scenarios:**
- A Search page whose tab carries two searches shows both inner tabs, with the saved active one selected.
- Adding, renaming, and closing an inner search changes the tab state the server receives.
- Typing a query saves once after the pause, not once per keystroke. Leaving the page right after typing still saves the last query.
- With a legacy `search-tabs:<user>` entry and a tab with no `search`, the page shows the legacy searches, and the entry is removed.
- `/search?q=foo` opens the `/search` page with an inner search "foo" active.
- Closing the only inner search closes the Search tab.
- Existing `search-page.browser.test.ts` behaviors (results, filters, inner rename and keyboard) still pass, with their local-storage setup moved to tab state.

**Verification:** `search-page.browser.test.ts` passes.

### U4. Cmd+P rows and opening commands

**Goal:** Search pages can be found in Cmd+P, and the search commands follow R12–R13.

**Requirements:** R11, R12, R13

**Dependencies:** U2, U3

**Files:**
- `internal/rocketclaw/web/src/ui.tsx` (`paletteRows`, `CommandPalette`, `useTabs.open`)
- Tests: `internal/rocketclaw/web/src/palette.test.ts` and/or `internal/rocketclaw/web/src/session-list.browser.test.ts`

**Approach:**
1. `CommandPalette` passes the person's Search tabs, from every space, to `paletteRows`.
2. In sessions mode, `paletteRows` lists them above the session rows:
   - Each row is labeled per R5, with its space name as detail.
   - Rows are filtered with the existing match tiers over the label and inner queries, and kept in strip order.
   - Choosing a row opens the page.
3. `tabs.open` takes an optional query string (KTD6).
4. "Sessions: Search" opens a new `/search/<id>`.
5. "Search everywhere" opens the page whose inner searches include exactly the typed text, else a new id, with `?q=`.

**Patterns to follow:** how `paletteRows` builds command rows and session rows. `tabs.open` for permanent opens.

**Test scenarios:**
- With Search pages "Triage" (space Main) and "Deploys" (space Ops), Cmd+P with an empty query lists sessions only. Typing "e" lists both pages above the session rows, and typing "dep" lists only "Deploys".
- Typing a query that matches one page's inner search lists that page.
- Choosing "Deploys" from Main enters Ops and opens it.
- "Sessions: Search" twice gives two Search tabs.
- "Search everywhere for 'foo'" with no page having "foo" opens a new Search tab with "foo" active. Doing it again reuses that tab rather than opening a third.

**Verification:** the palette tests pass.

### U5. Docs

**Goal:** Project vocabulary names the concept.

**Requirements:** all, for discoverability

**Dependencies:** U2

**Files:** `CONCEPTS.md`

**Approach:** Under Web Spaces, add a short "Search Page" entry, covering:
- a tab of its own, with a name and inner searches
- saved with the person's tabs
- never preview
- one page for everyone when Shared

**Test expectation:** none -- documentation only.

**Verification:** the entry reads consistently with the Space and Shared Group entries.

---

## Verification Contract

- `gofmt` on touched Go files, `go test ./...`, `make lint`, and `make test` from the repo root.
- From `internal/rocketclaw/web`:
  - `make lint` (eslint, `tsc`, react-doctor)
  - the browser tests for `tabs`, `search-page`, `session-list`, and `palette`
  - `make check-cloc-budget`
- The Go CLOC budget check from `internal/rocketclaw` passes.
- The rebuilt bundle in `internal/rocketclaw/internal/web/dist/` is committed with the Web changes.

## Definition of Done

- R1–R16 hold, each covered by a test named in its unit or, for R10 and R14, by the U3 scenarios.
- Go source CLOC is under the raised 26750 budget, and Web TS source CLOC is under 8250.
- No leftover code from dead-end attempts, no `//nolint`, and no react-doctor suppressions.
- The README impact was considered. README describes `/search` query syntax only, which is unchanged, so no README update is needed.
