---
title: "Drop Operator SQLite Migrator - Plan"
type: refactor
date: 2026-08-30
topic: rocketclaw-drop-sqlite-migrator
artifact_contract: ce-unified-plan/v1
artifact_readiness: implementation-ready
product_contract_source: ce-plan-bootstrap
execution: code
---

# Drop Operator SQLite Migrator - Plan

## Goal Capsule

- **Objective:** Operators have no leftover sqlite copy into the State Store. RocketClaw stays PostgreSQL-only. Leftover sqlite files stay unused on disk.
- **Means:** Delete the `fc migrate` command and the leftover sqlite reader it is the only caller of (KTD1).
- **Product authority:** Product Contract below is source of WHAT. Planning Contract is HOW.
- **Open blockers:** None.
- **Stop conditions:** `fc migrate` is unknown. The leftover sqlite reader is gone. Operator docs no longer describe that copy. `make lint` and `make test` pass.

---

## Product Contract

### Summary

Remove the leftover SQLite → PostgreSQL operator copy from the rocketclaw CLI so that command no longer exists. PostgreSQL as the State Store stays. Embedded SQL schema migrations stay. Other inspect and check commands stay. Leftover sqlite files are not deleted from disk.

### Problem Frame

The Operator SQLite Migrator existed so a large leftover store could move to PostgreSQL without a start-time copy. Every workspace has moved. The copy path is leftover surface.

<!-- ce-section: work-relationships -->
### How This Work Fits Together

This plan is the deletion that `docs/plans/2026-08-17-2231-feat-rocketclaw-sqlite-cutover-plan.md` deferred until every workspace had moved.

- PostgreSQL as the State Store — **Depends on** that cutover already being done.
- Drop the unused `store_bootstrap` table from PostgreSQL schema — **Can proceed independently** after this deletion. Not in this plan.

### Key Decisions

- KD1. **Leftover sqlite copy is gone.** Governs R1, R3.
  (session-settled: user-approved — chosen over keep the operator copy: leftover sqlite cutover is finished)

### Requirements

**Command**

- R1. `fc migrate` is an unknown rocketcode command.
- R2. Other `fc` commands stay.

**Docs**

- R3. Help and operator docs do not describe leftover sqlite copy.

**Store**

- R4. HEAD still opens the State Store from PostgreSQL only.
- R5. Leftover sqlite files stay on disk.
- R6. Embedded SQL schema migrations on PostgreSQL open stay.

### Acceptance Examples

- AE1. Old migrate command
  - **Covers R1.**
  - **Given:** HEAD without the operator copy.
  - **When:** The operator runs `fc migrate`.
  - **Then:** The run is an unknown rocketcode command. The PostgreSQL store is unchanged.

### Scope Boundaries

- Do not delete leftover sqlite files from disk.
- Do not drop `modernc.org/sqlite` from the module. Quickweb still uses it.
- Do not rename the PostgreSQL RocketCode adapter that is still called `sqliteSessionStore`.
- Do not change `state.sqlite3.lock` naming or skel preservation of leftover sqlite files.
- Do not add a dedicated "migrate removed" error or a test that `migrate` is rejected.
- Do not rewrite historical plans or residual-review notes.
- Quickweb SQLite stays.

#### Deferred to Follow-Up Work

- Drop the unused `store_bootstrap` table from PostgreSQL schema.

### Sources / Research

- `docs/plans/2026-08-17-2231-feat-rocketclaw-sqlite-cutover-plan.md` — deferred this deletion until cutover finished.
- `docs/plans/2026-08-16-001-feat-rocketclaw-postgres-store-plan.md` — put sqlite-read code in `internal/rocketclaw/harnessbridge/store_bootstrap.go` so a later delete could remove the copy path.
- `CONCEPTS.md` — State Store and Operator SQLite Migrator.

---

## Planning Contract

### Key Technical Decisions

- KTD1. **Delete the leftover sqlite reader with the command.** The only production caller is `fc migrate`. Delete `internal/rocketclaw/harnessbridge/store_bootstrap.go`. Governs R1.
  (session-settled: user-approved — chosen over leave the reader in the library: the command is the only caller)
- KTD2. **Remove `migrate` from both `fc` command switches.** The first switch's default is unknown command. The second switch's default is `fc check`. Leaving `migrate` only in the first switch would make `fc migrate` run check. Governs R1.
- KTD3. **Delete migrate tests. Do not add a migrate unknown-command test.** Drop help assertions that list `fc migrate`. Delete all of `internal/rocketclaw/harnessbridge/store_bootstrap_test.go`, including the open-ignores-sqlite tests. Those tests exist to drive the sqlite reader. After KTD1, open-ignores-leftover is the absence of a copy path. `store_test.go` already covers PostgreSQL open. Governs R1, R4.
- KTD4. **Delete migrate-only helpers.** Delete `sessionDBPathIn` and `sessionDBSchemaVersion`. Keep `prepareSessionDBPathIn`. It still creates the runtime dir and guards leftover `state.sqlite3` as a no-symlink path before locking `state.sqlite3.lock`. Governs R5, R6.

### Assumptions

None beyond the confirmed Product Contract.

### Risks

- A workspace that never finished cutover cannot copy leftover sqlite after this ships. KD1 accepted that.

---

## Implementation Units

### U1. Delete the operator copy

- **Goal:** Remove `fc migrate` and the leftover sqlite reader. Covers R1, R2, R4, R5, R6, AE1.
- **Requirements:** R1, R2, R4, R5, R6. Approach follows KTD1, KTD2, KTD3, KTD4.
- **Dependencies:** None.
- **Files:** `cmd/rocketclaw/fc.go`, `cmd/rocketclaw/fc_test.go`, `cmd/rocketclaw/main.go`, `cmd/rocketclaw/main_test.go`, `internal/rocketclaw/harnessbridge/store_bootstrap.go`, `internal/rocketclaw/harnessbridge/store_bootstrap_test.go`, `internal/rocketclaw/harnessbridge/store.go`, `internal/rocketclaw/harnessbridge/store_schema.go`
- **Approach:**
  1. Remove `migrate` from both `fc` command switches, help text, `runFCMigrateIn`, and migrate-only errors (KTD2).
  2. Delete `internal/rocketclaw/harnessbridge/store_bootstrap.go` (KTD1).
  3. Delete `sessionDBPathIn` and `sessionDBSchemaVersion`. Leave `prepareSessionDBPathIn` and PostgreSQL schema init (KTD4).
  4. Delete migrate tests and drop help assertions that list `fc migrate`. Do not add a migrate unknown-command test (KTD3).
  5. Do not `go mod tidy` expecting to drop `modernc.org/sqlite`.
- **Patterns to follow:** `TestRunFCUnknownCommand` already covers unknown `fc` verbs. AGENTS.md removal rule: delete the behavior and its tests. Do not add a rejection path.
- **Test scenarios:**
  - Help for `fc` and top-level help no longer lists `fc migrate`.
  - Existing unknown-command coverage still treats unknown verbs as unknown. Do not add a `migrate`-specific unknown test.
  - Remaining `fc list`, `observe`, `delete`, and `check` tests still pass.
  - `internal/rocketclaw/harnessbridge` tests still pass with no sqlite reader file.
- **Verification:** `cmd/rocketclaw` and `internal/rocketclaw/harnessbridge` tests pass with `ROCKETCLAW_TEST_DATABASE_URL`. Production rocketclaw code in those packages has no sqlite driver import.

### U2. Strip operator migrator docs

- **Goal:** Operator-facing docs match PostgreSQL-only start with no leftover sqlite copy. Covers R3.
- **Requirements:** R3.
- **Dependencies:** U1.
- **Files:** `CONCEPTS.md`, `README.md`, `cmd/rocketclaw/CHEATSHEET.md`
- **Approach:**
  1. Remove the Operator SQLite Migrator heading and the relationship bullet in `CONCEPTS.md`. Keep State Store as PostgreSQL and `run` ignores `state.sqlite3`.
  2. Remove the two `fc migrate` sentences in `README.md`. Keep embedded SQL migrations on open, `fc check` ping, leftover file ignored.
  3. Remove the migrate sentence and command-table row in `cmd/rocketclaw/CHEATSHEET.md`.
- **Test scenarios:**
  - Test expectation: none -- documentation only.
- **Verification:** Those three files no longer name `fc migrate` or Operator SQLite Migrator.

---

## Verification Contract

| Check | Command |
| --- | --- |
| Package tests | `ROCKETCLAW_TEST_DATABASE_URL` set; `go test` `cmd/rocketclaw` and `internal/rocketclaw/harnessbridge` |
| Full suite | `make test` |
| Lint | `make lint` |

Do not edit `GO_SOURCE_CLOC_BUDGET`. Deleting the reader is a CLOC win.

---

## Definition of Done

- AE1 covered. R1–R6 true.
- `fc migrate` is unknown. No leftover sqlite reader in rocketclaw production code.
- `CONCEPTS.md`, `README.md`, and `cmd/rocketclaw/CHEATSHEET.md` no longer describe leftover sqlite copy.
- Help no longer lists `fc migrate`.
- Abandoned migrate helpers and tests are deleted.
- `make lint` and `make test` pass.
- No migrate-removed shim or rejection test.
