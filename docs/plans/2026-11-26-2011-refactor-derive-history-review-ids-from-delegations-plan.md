---
title: Derive History Review IDs From Delegations - Plan
type: refactor
date: 2026-11-26
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Derive History Review IDs From Delegations - Plan

## Goal Capsule

- **Objective:** A Web chat's history shows the same Permission Review reasons and reviewed Code Mode calls as today, from a server with less code to maintain.
- **Means:** drop the extra review-ID query from the history snapshot and pick review IDs out of the delegations the snapshot already reads (KTD1).
- **Authority:** the owner-approved decision > this plan's Requirements > KTDs > unit Approach. `AGENTS.md` governs style, the Simplification and Tests rules, and verification.
- **Stop conditions:** stop and report if the derived list can differ from today's input to `reviewRationales` in any case, or if source CLOC does not go down.
- **Execution profile:** one PR with one commit.
- **Finishing:** `lfg` implements, reviews and opens the PR. Merging stays with the user.

## Product Contract

### Summary

Delete `HistorySnapshot.Reviews` and the query that fills it in `ObserveHistory`. `Server.reviewRationales` takes the snapshot's delegations and handles only those whose kind is `review`.

### Problem Frame

`ObserveHistory` (`internal/rocketclaw/backend/history.go`) reads the delegations of a history page with `delegationsSQL`. It then sends every delegation ID back to Postgres to ask which ones are reviews. Each delegation row already carries `conversations.kind` for the same conversation, read in the same repeatable-read transaction, so the second query only repeats an answer the server already holds.

### Requirements

- R1. `reviewRationales` reads entries for exactly the review conversations it reads today, once each, for every history request, including source, from and before filters and pages with no delegations.
- R2. `HistoryResponse.ReviewRationales` and the Code Mode calls attached to unrecorded `execute` events are unchanged.
- R3. `HistorySnapshot.Reviews` and its query are gone, and source CLOC goes down.

### Key Decisions

- **Derive review IDs from delegations instead of querying them.** (session-settled: user-approved — chosen over keeping the separate `kind = 'review'` query: each delegation already carries the kind of the same row in the same snapshot; approved only if the derived list is exactly equal.) Governs R1, R3.

### Scope Boundaries

- `delegationsSQL`, `addDelegations` and `linkCalls` stay unchanged.
- No new tests. The History tests in `internal/rocketclaw/frontend/rpc/live_test.go` already assert `ReviewRationales` through the real store (two reviews under one call, HTTP review reasons, polling with a revision).
- Not built: deduplication or sorting before the loop. KTD2 shows the list has no duplicates, and the loop does not depend on order.

## Planning Contract

### Key Technical Decisions

- KTD1. **`reviewRationales` takes `[]backend.Delegation` and skips any delegation whose `Kind` is not `backend.KindReview`, using `.ID` where it used the review string.** `addDelegations` already branches on `delegation.Kind`, so this follows the existing pattern. Implements the settled decision governing R1, R3.
- KTD2. **Equivalence holds without extra code.**
  1. Same set: today's query keeps the delegation IDs whose `conversations.kind = 'review'`. Each delegation's `Kind` is `c.kind` of that same row, read in the same `REPEATABLE READ` transaction, so the filter is identical.
  2. No duplicates: `children` is `DISTINCT` over `(c.conversation_id, p.producer, c.call_id, c.call_turn IS NULL, c.kind)`. The join forces `p.producer = c.parent_conversation_id`, so every column is fixed by the `conversations` row. `conversation_id` is that table's primary key (`managed_conversations_pkey`, never dropped), and the `LATERAL ... LIMIT 1` adds at most one row per child. Each ID appears at most once, like today.
  3. Order: today's query has no `ORDER BY`, so its order is unspecified. The derived list follows `ORDER BY c.id`. Each loop iteration writes its own map key and its own `unrecorded[review]` event, so the result does not depend on order.
  4. Empty: with no delegations, today's query returns nil, and the derived loop also skips everything. `range` treats both the same.
  5. Filters: today's IDs come from `view.Delegations`, which already applied source, from and before. The derived list reads the same slice.

## Implementation Units

### U1. Read review IDs from the snapshot's delegations

- **Goal:** the history snapshot runs one fewer query, and review reasons come from the delegations.
- **Requirements:** R1, R2, R3; KTD1, KTD2.
- **Dependencies:** none.
- **Files:** `internal/rocketclaw/backend/history.go`, `internal/rocketclaw/frontend/rpc/server.go`; existing coverage in `internal/rocketclaw/frontend/rpc/live_test.go`.
- **Approach:**
  1. In `history.go`, delete the `Reviews` field from `HistorySnapshot`, and delete the `ids` loop and the `queryStrings` review query in `ObserveHistory`.
  2. In `server.go`, pass `view.Delegations` to `reviewRationales` and change its parameter to delegations (KTD1).
- **Patterns to follow:** the `delegation.Kind` check in `addDelegations`.
- **Test scenarios:** Test expectation: none -- behavior is unchanged (KTD2). The existing History tests in `live_test.go` that assert `ReviewRationales` keep covering R2.
- **Verification:** the `./backend` and `./frontend/rpc` package tests pass, and source CLOC drops.

## Verification Contract

| Gate | Command |
|---|---|
| Format | `gofmt -l` on the two touched files prints nothing |
| Lint | `make -C internal/rocketclaw lint` |
| Package tests | `make -C internal/rocketclaw coverage-profile GOTESTPKGS="./backend ./frontend/rpc"` after `bun install` in `internal/rocketclaw/web` |
| Size budget | `make -C internal/rocketclaw check-cloc-budget` |
| Size report | `make -C internal/rocketclaw cloc` before (45406) and after |

## Definition of Done

- R1 to R3 hold and every gate in the Verification Contract passes.
- Source CLOC after the change is lower than 45406.
- No other file changes and no leftover experiments.
