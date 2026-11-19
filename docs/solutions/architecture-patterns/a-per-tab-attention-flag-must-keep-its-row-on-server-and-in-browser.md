---
title: A Per-Tab Attention Flag Must Keep Its Row on the Server and in the Browser
date: 2026-11-19
category: docs/solutions/architecture-patterns/
module: internal/rocketclaw/backend
problem_type: architecture_pattern
component: database
severity: medium
applies_when:
  - Adding a per-person, per-tab choice stored as a column of web_tab_attention
  - Gating a server mark or park step on a web_tab_attention column
  - Changing which attention rows ChangeAttention deletes or which tab rows the browser keeps from ListSessions
related_components:
  - internal/rocketclaw/web
  - internal/rocketclaw/frontend/rpc
tags: [tab-attention, web-tab-attention, unfollowed, auto-park, follow, listed-tabs, idle-row-delete, marks]
---

# A Per-Tab Attention Flag Must Keep Its Row on the Server and in the Browser

## Context

`web_tab_attention` holds one row per person and tab location: the unread mark, the park, its timer, and per-tab choices such as `auto_park` and, since 2026-11-19, `unfollowed` (migration `105_web_tab_unfollowed.sql`). Most of a tab's life it has no mark and no park, so two places treat such a row as empty and drop it:

- **Server.** `ChangeAttention` (`internal/rocketclaw/backend/attention.go`) ends with a "delete idle attention" statement that removes a changed row once it has no mark, no park, no timer and no remembered question park.
- **Browser.** `listedTabs` (`internal/rocketclaw/web/src/session-list.ts`) keeps only the tab rows of a listing that carry a mark, a park or a timer. Session rows are not filtered there; this affects Search, Cron, Pages and other location tabs.

A choice stored only as a column on that row disappears when either place drops it. `auto_park` hit the server case first. Follow hit both: the server case was caught while planning, and the browser case only by the plan's feasibility review. Without the browser fix, an unfollowed `/cron` tab with no mark never reached `view.marks`, so its menu kept offering Unfollow and could never offer Follow again.

## Guidance

When a new per-tab choice lives in `web_tab_attention`:

1. Add its "set" state to the idle-row delete in `ChangeAttention`, next to `auto_park IS NULL` and `NOT unfollowed`, so a row that holds only the choice survives.
2. Add it to the `listedTabs` filter, next to `tab.unfollowed`, so a location row that holds only the choice reaches the browser.
3. Add both actions to the `until` and `resumes` rules in `ChangeAttention` deliberately. The `until` rule now lists only the actions that write it (`park`, `park_done`, `unpark`), so a new action keeps the timer unless it is added there; `resumes` still lists the actions that keep a remembered question park.
4. Test the row's survival for both a session path and a location path, as `TestAttentionMatrix` does with `"unfollow"` → `read unfollowed`, and the browser filter as `session-list.test.ts` does for an unfollowed row.

A server step gated on the column needs the row to exist. `markTabsSQL` skips unfollowed tabs with `WHERE NOT a.unfollowed` on its `ON CONFLICT … DO UPDATE`. That only works because an unfollowed tab always has a row: if the idle delete removed it, the next mark would take the insert branch and land as if the tab were followed.

## Why This Matters

Both drops are silent. Nothing fails: the server writes the next mark as if the person never chose, or the menu shows the wrong label, and no test notices unless it checks a row holding only the choice.

## When to Apply

- A new per-person tab setting, or a change to an existing one, stored in `web_tab_attention`.
- A new statement that deletes or filters `web_tab_attention` rows or `ListSessions` tab rows.

## Examples

The Follow change touched exactly these places: the idle delete gained `AND NOT unfollowed`, `markTabsSQL` gained `WHERE NOT a.unfollowed`, `wakeDue` leaves the mark fields alone for unfollowed rows, and `listedTabs` gained `|| tab.unfollowed`. The plan is `docs/plans/2026-11-19-1133-feat-card-folding-shared-group-follow-plan.md`, and the model is `docs/verification/tab-attention/`.

## Related

- `docs/plans/2026-11-18-0900-feat-park-until-idle-plan.md`: added `auto_park` and the idle-row rule this pattern extends.
- `docs/solutions/logic-errors/remote-opencode-fork-marker-dropped-by-sidebar-remote-branch.md`: a column must also be read in both branches of the sidebar listing.
