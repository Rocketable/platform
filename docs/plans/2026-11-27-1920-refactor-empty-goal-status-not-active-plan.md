---
title: Stop Treating an Empty Goal Status as Active - Plan
type: refactor
date: 2026-11-27
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Stop Treating an Empty Goal Status as Active - Plan

## Goal Capsule

- **Objective:** goals keep starting, continuing, stopping, resuming and finishing exactly as they do today, while the State Store and the bridge read a goal's status only as one of its real values.
- **Means:** delete every branch, SQL clause and trim that reads an empty or padded `goal_status` as `active` (KTD1, KTD2, KTD3).
- **Authority:** the owner's decision in the invoking request, then this plan, then the repository's `AGENTS.md`.
- **Execution profile:** one PR, two small units, no behavior change for any status a goal can be written with.
- **Stop conditions:** stop and report if any current write path can store an empty or padded `goal_status` for a goal, or if source CLOC does not go down from 45230.
- **Finishing:** the implementing agent builds, verifies, and opens the PR. The owner merges.

## Product Contract

### Summary

Remove the code that treats a goal with an empty status as active: two SQL `goal_status = ''` alternatives, the `''` in the begin-goal guard, the read-time default in `stateDAO.goal`, and the `strings.TrimSpace` around `goal.Status` in the bridge. Remove the test row that only covers an empty-status goal.

### Problem Frame

An older release could leave a goal with an empty status, so several queries and checks still count `''` as active, and the bridge trims statuses it reads. Every goal written today gets one of the real statuses, and no stored goal has an empty status. The aliases cost lines, make each status check read as if a fifth state existed, and need a dedicated test.

### Requirements

**State Store**
- R1. A goal counts as active only when its stored status is `active`: listing active goal threads, setting a goal's status, accounting a goal turn, and refusing to begin a goal over an active one.
- R2. Reading a goal returns its stored status unchanged.

**Bridge**
- R3. Every check of a loaded goal's status in `internal/rocketclaw/backend/bridge.go` compares the stored value directly, and the update-goal tool reply names the stored status as is.

**Tests**
- R4. Tests that only cover empty-status goals are deleted; the coverage of real statuses stays.

### Key Decisions

- **Empty goal status is no longer read as active.** (session-settled: user-approved — chosen over keeping the empty-status alias for old data: every goal written today gets a status and no stored goal has an empty one.) Governs R1, R2, R3, R4.
- **No migration.** (session-settled: user-directed — chosen over a data migration that rewrites empty statuses: no stored goal has one.) Governs R1, R2.

### Scope Boundaries

- `COALESCE(c.goal_status, '')` in the listing query maps a NULL status (no goal) to a non-active value. It is not an empty-status alias and stays.
- The model's update-goal tool input is still trimmed before validation; that is input from outside the process.
- Non-goal, considered and not built: trimming the `status` argument inside `setGoalStatus` is redundant because every caller passes a constant or an already-trimmed value, but it is not an empty-status special case. Left as is to keep the diff to the decision.
- The web Goal panel has no empty-status case; nothing changes there.

## Planning Contract

### Key Technical Decisions

- KTD1. **Narrow each SQL predicate to the real status.** `ActiveGoalThreads` and `setGoalStatus` in `store.go` and the goal-turn accounting in `store_dao.go` match `goal_status = <active>` only. The begin-goal guard becomes `COALESCE(goal_status <> <active>, TRUE)`, which still lets a goal begin on a conversation that never had one (NULL) or whose goal ended.
- KTD2. **Delete the read-time default in `stateDAO.goal`.** It runs on read, not write, so no current caller depends on it to store a status: `beginGoalDB` writes `active`, `ResumeGoal` writes `active`, `setGoalStatus` writes a validated constant, and turn accounting writes `active` or `budget_exhausted`. Clearing a stale goal sets the status and the objective to NULL together, so `goal` reports no goal rather than an empty status.
- KTD3. **Drop the bridge's `strings.TrimSpace(goal.Status)`.** Statuses only reach the store from the constants above, and `bridge.go` already compares `goal.Status == GoalStatusActive` untrimmed where it decides whether a queued item is held.

### Assumptions

- The stateful-design pack (pack: formal-modeling, stateful-design.md) does not apply: no transition changes for any status a goal can hold, and the empty status being removed is unreachable.

## Implementation Units

### U1. State Store reads only real goal statuses

- **Goal:** remove the empty-status alias from the State Store.
- **Requirements:** R1, R2, R4; KTD1, KTD2.
- **Dependencies:** none.
- **Files:** `internal/rocketclaw/backend/store.go`, `internal/rocketclaw/backend/store_dao.go`, `internal/rocketclaw/backend/store_test.go`.
- **Approach:**
  1. In `store.go`, narrow the `ActiveGoalThreads`, `setGoalStatus` and `beginGoalDB` predicates per KTD1.
  2. In `store_dao.go`, narrow the goal-turn accounting `WHERE` and delete the empty-status default in `stateDAO.goal`.
  3. In `TestSessionServiceActiveGoalThreads`, drop the `legacy` conversation and the `UPDATE ... goal_status = ''`; keep the active and terminal rows.
- **Test scenarios:**
  - `TestSessionServiceActiveGoalThreads` lists the active goal and omits the completed one.
  - Existing goal tests (begin after terminal, begin over active refused, progress, budget exhaustion, resume, stop) pass unchanged.
- **Verification:** no `goal_status = ''` or `''` status alias remains in `internal/rocketclaw`, and the backend package passes.

### U2. Bridge compares goal statuses directly

- **Goal:** drop the trim around loaded goal statuses in the bridge.
- **Requirements:** R3; KTD3.
- **Dependencies:** U1.
- **Files:** `internal/rocketclaw/backend/bridge.go`.
- **Approach:** replace each `strings.TrimSpace(goal.Status)` with `goal.Status`.
- **Test scenarios:**
  - Test expectation: none -- no reachable status changes behavior; existing bridge goal tests cover the active, complete and tool-reply paths.
- **Verification:** no `TrimSpace(goal.Status)` remains in `internal/rocketclaw`, and the backend package passes.

## Verification Contract

| Gate | Command |
|---|---|
| Format | `gofmt -l` on touched Go files prints nothing |
| Lint | `make -C internal/rocketclaw lint` |
| Tests | `make -C internal/rocketclaw coverage-profile GOTESTPKGS="./backend"` |
| Size | `make -C internal/rocketclaw check-cloc-budget`, with source CLOC below 45230 |

## Definition of Done

- R1 to R4 hold, and the grep checks in U1 and U2 find nothing.
- All gates in the Verification Contract pass.
- No migration and no comments were added.
- No abandoned experimental code remains in the diff.
