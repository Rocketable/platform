---
title: Cron timeline journal - Plan
type: feat
date: 2026-10-26
topic: web-cron-timeline
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Cron timeline journal - Plan

## Goal Capsule

- Objective: a person using the RocketClaw Web can pick any cron job, including a retired one, and read every run it ever made as a timeline, then open any run's transcript without leaving that view.
- Means: a `Cron: Timeline` palette command that picks a cron and opens a journal page at `/cron?timeline=<stem>`, built from the existing `ListCronJobs` data (KTD1, KTD2).
- Authority: this Product Contract, then the Planning Contract, then the project instructions in `AGENTS.md`.
- Stop conditions: the TS source CLOC budget (`TS_CLOC_BUDGET` 8250 in `internal/rocketclaw/web/Makefile`) would be exceeded and no honest deletion brings it back; react-doctor flags a rule that cannot be met without a suppression; evidence shows a session-settled decision cannot work.
- Execution profile: Web UI only (`internal/rocketclaw/web/src/ui.tsx`), its browser test, the web README, and the rebuilt embedded SPA. No Go, proto, or backend change.
- Who finishes: `ce-work` implements and verifies locally; the `lfg` pipeline reviews, opens the PR, and watches CI. Merging stays with the user.
- Open blockers: none.

## Product Contract

### Summary

The command palette gains `Cron: Timeline`. It lists every cron (active or retired, in the current space) with its run count and last run. Picking one opens a journal page with a summary of the cron, a bar per day of runs, and every run newest first, grouped by UTC day. On wide screens the picked run's transcript stays beside the list; on narrow screens it opens under the run.

### Problem Frame

The Cron dashboard shows a 36-hour window and a flat list of runs per cron. People who want to know how a cron has behaved over its whole life (how often it ran, when it went quiet, which runs delivered a chat) have no view for that. A cron like HEARTBEAT has about 700 runs over 30 days, which the dashboard's list does not make readable.

### Requirements

**Picking a cron**
- R1. The palette offers `Cron: Timeline`, which switches the palette to a cron picker without closing it, the way `Cron: Run` does.
- R2. The picker lists every cron stem that has a definition or at least one run, in the current space, filtered by typed text. Each row shows its run count and last run time, and marks crons with no definition as retired.
- R3. Choosing a cron opens `/cron?timeline=<stem>`.

**The journal page**
- R4. The page shows the cron's name with an Active or Retired label, its agent and channel, and its schedule split into one tag per cron expression.
- R5. Summary cards show the run count (and sub-call count), delivered runs as a count and percentage, the typical (median) and longest gap between runs, the last run as a relative and absolute time, and the next expected run.
- R6. A bar per UTC day, from the first run through today (or through the last run for a retired cron), shows that day's run count with the delivered share solid. Bars keep a minimum width; when the days do not fit, the strip scrolls sideways inside its own row, opening on the newest day, and the page itself never scrolls sideways. Clicking a bar scrolls the list to that day, loading older days if needed.
- R7. Runs are listed newest first under a sticky header per UTC day. Each row shows the time, Delivered or Silent, its sub-call count, and the gap since the previous run. Upcoming expected runs appear above the list.
- R8. A run is one distinct `origin`. `ListCronJobs` returns a row per chat a run reached (the chat it reports to, chats continuing it, copies), so rows sharing an `origin` are one run. Sub-call rows (`origin` containing `/call_`) are not runs; they fold under their parent run and can be previewed from it.
- R12. A run is Delivered when one of its rows has a chat other than `web:<origin>`. That chat is the continuation `CreateSession` makes when someone opens a silent run, so opening a run never turns it Delivered.
- R9. Picking a run shows its transcript with the existing run preview and its Open chat link. On wide screens the preview stays in a column beside the list; on narrow screens it opens under the run.
- R10. The list shows whole days of about 60 runs at first, with a button to show older runs.
- R11. While cron jobs load, the page says so. A failed load shows the error. A stem the current space does not have shows a "No cron named … in this space" message instead of an empty journal.

### Key Decisions

- The journal design approved in the prototype bakeoff. (session-settled: user-approved — chosen over a palette-inline run log, a day-by-hour heatmap, a monospace jj-log printout, and a zoomable track: the user picked the journal variant and approved its polished wide layout.) Governs R4, R5, R6, R7, R9, R12.
- A wide two-column layout. (session-settled: user-directed — chosen over the narrow single-column journal: the user asked for it "wider, nicer, more polished" and approved the result.) Governs R9.

### Scope Boundaries

- No backend, proto, or Go change; the page uses `ListCronJobs` as it is today.
- No new route path; the Go handler already serves the SPA at `/cron`, so the query string carries the stem.
- Times stay in UTC, matching `cronWhen` and the dashboard's run labels.
- Not built: a "run now" button on the journal page (the dashboard already has one, and a mis-click starts a real job); highlighting unusually long gaps (schedules like HEARTBEAT's legitimately change cadence at night and on weekends, so a ratio rule flags normal gaps). Evidence that people miss outages would change this call.

#### Deferred to Follow-Up Work

- A link from each dashboard cron group to its journal.

## Planning Contract

### Key Technical Decisions

- KTD1. **Query-string route on `/cron`.** `CronPage` renders the journal when `timeline` is present in `useSearch()`. `internal/rocketclaw/internal/web/assets.go` only serves the SPA for fixed paths, so a new path would need a Go change for no product gain.
- KTD2. **Palette mode `timeline` beside `cron`.** The palette mode union gains `"timeline"`; `paletteRows` builds the picker from the same `cronJobs` query, and `Cron: Timeline` reuses the `Cron: Run` mode switch. Recent-command bookkeeping and key hints stay limited to the `sessions` and `commands` modes.
- KTD3. **One space filter for dashboard, palette, and journal.** The dashboard's "a job outside this space hides its runs too" logic moves into a shared `cronInSpace` helper so the palette picker and the journal filter runs the same way.
- KTD4. **Split the journal into small components for react-doctor.** `CronTimeline` holds state and layout; `CronJournalSummary`, `CronDayBars`, `CronUpcoming`, `CronPreviewPanel`, and `CronJournalRun` carry the branches, so no function crosses `no-high-complexity-react-function` (see `docs/solutions/best-practices/react-doctor-complexity-limit-shapes-web-components.md`).
- KTD5. **`CronRunPreview` gains a `tall` option.** The side panel needs a taller transcript area than the dashboard's inline preview; one optional prop changes the scroll area's max height and nothing else.

- KTD6. **Group run rows by `origin` before counting.** `ListCronJobs` (`internal/rocketclaw/frontend/rpc/server.go`, from `CronRunSources` in `internal/rocketclaw/backend/store.go`) emits one row per chat a run reached. The journal and the picker group rows by `origin` (R8); a run's preview and Open chat use its first row with a chat, else its bare row, and its Delivered flag follows R12. Grouping also keeps React keys unique.

### Assumptions

- The `ListCronJobs` response lists run rows newest first (it sorts by `lastRun` descending), as the dashboard already relies on; gaps are computed from that order.
- A sub-call's parent run is the `origin` prefix before `/call_`, as in the live data. Providers whose tool-call IDs do not start with `call_` would show their sub-calls as runs.

### Sources

- Approved prototype: jj change `wumwkmzrwwsv` in workspace `20261026-cron-timeline-journal`. It already holds the source for R1–R11 except R6's narrow-screen scrolling, R8's grouping by `origin`, and R12.
- Existing pieces reused: `CronPage`, `CronRunPreview`, `CronChatLink`, `cronWhen`, `useInSpace`, `paletteRows`, `paletteCopy`, `paletteEmpty` in `internal/rocketclaw/web/src/ui.tsx`.

## Implementation Units

### U1. Cron: Timeline palette picker

- **Goal:** the palette offers `Cron: Timeline` and a cron picker that opens the journal.
- **Requirements:** R1, R2, R3; KTD2, KTD3.
- **Dependencies:** none.
- **Files:** `internal/rocketclaw/web/src/ui.tsx`, `internal/rocketclaw/web/src/session-list.browser.test.ts`.
- **Approach:**
  1. Keep the prototype's palette code as the approved implementation: the `"timeline"` mode, its `paletteCopy` entry, the `cronJobs` query and "Loading…" in that mode, and `cronInSpace` shared with `CronPage`.
  2. Change the picker's run count to count distinct non-sub-call `origin`s (KTD6).
- **Patterns to follow:** the `Cron: Run` command and `mode === "cron"` branch of `paletteRows`.
- **Test scenarios:**
  - The command palette's full command list (the existing exact-list assertion) includes `Cron: Timeline` in its sorted place.
  - Choosing `Cron: Timeline` keeps the palette open with the cron picker focused, listing `daily` with its run count and `retired` marked retired.
  - Choosing `daily` in the picker lands on `/cron?timeline=daily`.
- **Verification:** the palette tests pass and `Cron: Run` still lists only defined crons and runs them.

### U2. Journal page

- **Goal:** `/cron?timeline=<stem>` shows the journal described in R4–R11.
- **Requirements:** R4–R12; KTD1, KTD4, KTD5, KTD6.
- **Dependencies:** U1 (shares `cronInSpace` and the route).
- **Files:** `internal/rocketclaw/web/src/ui.tsx`, `internal/rocketclaw/web/src/session-list.browser.test.ts`.
- **Approach:**
  1. Keep the prototype's journal as the approved implementation: `CronPage`'s status handling (R11), the components in KTD4, the wide grid with the sticky preview column, and `CronRunPreview`'s `tall` option (KTD5). Change its source only for the steps below or where a gate fails.
  2. Group run rows by `origin` and derive each run's preview row and Delivered flag (KTD6, R12).
  3. Give day bars a minimum width and let the strip scroll sideways, opening on the newest day (R6).
- **Patterns to follow:** the dashboard's `CronRunPreview` usage and `PageTitle`; Tailwind tokens already used on the Cron page (cyan for ran, amber outline for expected).
- **Test scenarios:**
  - `/cron?timeline=daily` shows the Active label, the `24h` schedule tag, a Runs card of 1, the upcoming runs, and one run row marked Delivered under the day header for that run's own UTC date.
  - A run listed under two chats (two `daily` rows sharing an `origin` with different `nextRun`s) shows as one run row and a Runs card of 1.
  - A silent run whose only chat is `web:<origin>` still shows as Silent and does not count toward Delivered.
  - Runs spanning about 200 days at 390 px wide leave the page without horizontal overflow, with the newest day's bar visible.
  - Clicking the `daily` run shows the run preview with its Open chat link; at 390 px wide it renders under the run, at 1280 px in the side column.
  - `/cron?timeline=retired` shows the Retired label and its run; `/cron?timeline=no-such-cron` shows the "No cron named no-such-cron in this space." message.
  - A run whose origin is `<parent>/call_x` is counted as a sub-call of `<parent>`, not as a run (extend the mock jobs with one such row).
- **Verification:** the browser test passes at both widths; react-doctor reports no issue in `ui.tsx`.

### U3. README and embedded SPA

- **Goal:** the docs list the new command and the embedded Web build matches the source.
- **Requirements:** R1.
- **Dependencies:** U1, U2.
- **Files:** `internal/rocketclaw/web/README.md`, `internal/rocketclaw/internal/web/dist/`.
- **Approach:** add `Cron: Timeline` to the README's palette command list with one sentence on the journal; rebuild the SPA so the committed `dist` matches.
- **Test expectation:** none -- documentation and generated build output; `internal/rocketclaw/internal/web` Go tests cover the embedded build.
- **Verification:** `go test ./internal/rocketclaw/internal/web/...` passes against the rebuilt `dist`.

## Verification Contract

| Gate | Command (from `internal/rocketclaw/web` unless noted) | Proves |
|---|---|---|
| Types | `bunx tsc --noEmit` | the mode union and new props typecheck |
| Lint | `make lint` | oxlint, tsc, and react-doctor pass with no suppression |
| Browser tests | `make test` (includes the CLOC budget check) | U1 and U2 scenarios, no regression in palette or Cron page tests |
| Budget | `make -s cloc` | source CLOC stays below 8250 |
| Embedded build | `go test ./internal/rocketclaw/internal/web/...` from the repo root | the rebuilt `dist` is served |

## Definition of Done

- Every R1–R12 behavior is visible against the browser test fixtures, and U1–U3 verification holds.
- `make lint` and `make test` pass in `internal/rocketclaw/web`; source CLOC is below the budget.
- No prototype-only leftovers remain: no other bakeoff variants' code, no debug output, no unused helpers.
- The committed `dist` comes from a build of the committed source.
