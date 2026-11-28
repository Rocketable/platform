---
title: Drop the pre-baseline State Store ledger upgrade - Plan
type: refactor
date: 2026-11-27
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Drop the pre-baseline State Store ledger upgrade - Plan

## Goal Capsule

- **Objective:** RocketClaw starts a new or already-baselined State Store exactly as before, with less startup code to maintain; a store still on the ledger from before the `001_baseline.sql` squash no longer starts and gets no special handling.
- **Means:** delete the one-time ledger conversion block from State Store startup and its dedicated test (KTD1, KTD2).
- **Authority:** the owner-approved decision recorded in Key Decisions wins over this plan; `AGENTS.md` (Simplification, Tests, Verification, no internal names) governs how the work is done.
- **Execution profile:** one PR, one commit. Lightweight.
- **Stop conditions:** stop and report if removing the block changes how a fresh database or a baseline-ledger database migrates, or if source CLOC does not go down.
- **Who finishes:** the implementing agent opens the PR and watches CI; merging stays with the owner.

## Product Contract

### Summary

Remove the startup step that converts a State Store whose migration ledger still lists `059_legacy_handoff.sql` and refuses older ledgers. Remove the test that covered only that step, and update the docs that describe the upgrade path.

### Problem Frame

`001_baseline.sql` replaced migrations `001` through `059` in v0.2.5. Every release since has carried a `DO $$ ... $$` block in `internal/rocketclaw/backend/store_schema.go` that rewrites an old ledger to the baseline, drops the `legacy_*` tables, and raises for ledgers older than `059`. Releases v0.2.5 through v0.2.80 already did that conversion, so the block now only adds code and a test that lists every migration by name and must be edited by each new migration.

### Requirements

**Startup behavior**

- R1. A brand-new empty database migrates from `001_baseline.sql` onward exactly as today.
- R2. A database whose ledger already holds `001_baseline.sql` starts unaffected.
- R3. A database still carrying the pre-squash ledger fails with the existing `unknown migration in database: <id>` error; nothing converts it and no new check or message is added.

**Tests and docs**

- R4. Tests that covered only the conversion or the pre-`059` refusal are deleted; tests of fresh databases and baseline-ledger restarts stay.
- R5. README and docs that describe the removed upgrade path are updated to match, without naming deployments or hosts.

### Key Decisions

- **Drop the pre-squash upgrade path with no replacement check.** (session-settled: user-directed — chosen over keeping the conversion or adding a dedicated refusal message: releases since v0.2.5 already convert those stores, and the migration library's unknown-migration error is enough.) Governs R1, R2, R3.

### Scope Boundaries

- `CREATE TABLE IF NOT EXISTS pg_migrations` and `lockMigrationsSQL` stay; each migration still takes the lock in `applySessionMigration`.
- Historical plans under `docs/plans/` and dated investigation inventories are point-in-time records and stay unchanged.
- No new error type, message, or test for the pre-squash case (R3).

#### Deferred to Follow-Up Work

- With the block gone, the startup transaction only creates the ledger table and takes and releases the lock. Collapsing it into one `CREATE TABLE IF NOT EXISTS` call would remove more lines, but it changes the startup locking, which the approved decision did not cover.

---

## Planning Contract

### Key Technical Decisions

- KTD1. **Delete only the `DO` block from the startup query list.** The `CREATE TABLE IF NOT EXISTS pg_migrations` statement and the `lockMigrationsSQL` statement stay in the startup transaction as they are, so startup ordering and `TestSessionMigrationsSerializeStartup` (which waits on a `LOCK TABLE pg_migrations` query) are unchanged. Instantiates the Key Decision; governs R1, R2, R3.
- KTD2. **Delete `TestInitializeSessionDBAdoptsReleasedStore` whole.** Every assertion in it exercises the removed block: the pre-`059` refusal, the `059` conversion, the dropped `legacy_` table, and data kept across conversion. Baseline-ledger restarts stay covered by `TestSessionMigrationRollsBackWithItsLedgerWrite` and `TestSessionMigrationsCancelPlanning`, which both run `initializeSessionDB` again on an already-migrated store, and by every test that builds a store with `newColdTestSessionServiceAt` and initializes it again. Governs R4.
- KTD3. **README names the last release that converts.** `README.md` currently says a store from before v0.2.0 must run v0.2.0 first. After this change a store from before v0.2.5 must first run a release from v0.2.5 to v0.2.80 (v0.2.80 is the newest tag and still converts). Governs R5.

### Stateful-design applicability

The formal-modeling rule (pack: formal-modeling, stateful-design.md) applies to changed concurrency or recovery behavior. This change deletes a branch inside an already-serialized startup transaction and adds no actor, step, or interleaving, so no model is written.

### Assumptions

- "Tests of the released-store ledger" in the request means the baseline-ledger restart case (R2), which the tests named in KTD2 already cover. `TestInitializeSessionDBAdoptsReleasedStore` itself only tests the pre-squash conversion, despite its name.
- Citations of `store_schema.go` line numbers in `docs/solutions/` are kept accurate by shifting them with the deletion; older verification records are left as written.

---

## Implementation Units

### U1. Remove the conversion block, its test, and its docs

- **Goal:** startup no longer converts or refuses pre-squash ledgers, and nothing describes that path as current.
- **Requirements:** R1–R5; KTD1, KTD2, KTD3.
- **Dependencies:** none.
- **Files:**
  - `internal/rocketclaw/backend/store_schema.go`
  - `internal/rocketclaw/backend/store_schema_test.go`
  - `README.md`
  - `docs/solutions/runtime-errors/deployment-binary-missing-live-migrations.md`
  - `docs/solutions/best-practices/finding-merged-commits-that-silently-undid-earlier-ones.md`
  - `docs/solutions/best-practices/postgres-extension-in-migration-with-shared-test-database.md`
  - `docs/verification/2026-10-19-state-store-consolidation-rehearsal.md`
- **Approach:**
  1. In `initializeSessionDB`, delete the `DO $$ ... $$` entry from the startup query slice (KTD1).
  2. Delete `TestInitializeSessionDBAdoptsReleasedStore`, then remove any helper or import it alone used (KTD2).
  3. Update the `database_url` line in `README.md` (KTD3).
  4. In the deployment-binary learning, say the conversion ran in v0.2.5 through v0.2.80, that later releases reject a pre-squash ledger with `unknown migration in database: <id>`, and shift its `store_schema.go` line citations.
  5. In the merged-commits learning, drop the `store_schema.go:42-57` citation of the removed block and shift the `90-91` citation.
  6. In the extension learning, shift the `store_schema.go:122` citation.
  7. In the rehearsal's historical note, add that releases after v0.2.80 no longer adopt a pre-baseline store.
- **Patterns to follow:** the unknown-migration loop already in `initializeSessionDB`; `TestSessionMigrationRollsBackWithItsLedgerWrite` for the unknown-migration error.
- **Test scenarios:**
  - Fresh database: `TestSessionMigrationsSerializeStartup` and `TestSessionMigrationRollsBackWithItsLedgerWrite` still migrate an empty `pg_migrations` to the full set.
  - Baseline ledger restart: `TestSessionMigrationRollsBackWithItsLedgerWrite` and `TestSessionMigrationsCancelPlanning` still re-run `initializeSessionDB` on a migrated store without error.
  - Unknown ledger row: the existing `unknown.sql` case still fails with `unknown migration`, which is the path a pre-squash ledger now takes (R3). No new test.
- **Verification:** the backend package passes whole, lint is clean, and no reference to `059_legacy_handoff.sql` or the `predates` message remains outside historical plans and records.

---

## Verification Contract

| Gate | Command | Proves |
|---|---|---|
| Format | `gofmt -l` on touched Go files | no formatting drift |
| Lint | `make -C internal/rocketclaw lint` | lint clean |
| Package tests | `make -C internal/rocketclaw coverage-profile GOTESTPKGS="./backend"` | R1, R2, R3, R4 |
| Size | `make -C internal/rocketclaw check-cloc-budget`, plus `make -C internal/rocketclaw cloc` before and after | source CLOC goes down from 45230 |
| Leftovers | search for `059_legacy_handoff`, `predates 059`, `legacy_thread_queue` | R4, R5 |

## Definition of Done

- U1 is done when every gate above passes and the leftovers search finds only historical plans and dated records.
- Source CLOC after is below 45230.
- The PR title, body and commit message name no deployment, host, or count of deployments.
- No abandoned or experimental code remains in the diff.
