# Residual Review Findings

Source: ce-code-review `20260816-145051-40863-0`
Bookmark: `rocketclaw-postgres-store`
Plan: `docs/plans/2026-08-16-001-feat-rocketclaw-postgres-store-plan.md`

## Residual Review Findings

- P1 `internal/rocketclaw/harnessbridge/store_bootstrap_test.go:44` Bootstrap test ignores most imported tables. AE2 asks for one row per copied table; the test only asserts `session_entries`. Not applied: confidence 75 with no cross-persona agreement. Tracker: no sink (GitHub Issues disabled).
- P0 `internal/rocketclaw/harnessbridge/store_bootstrap.go:51` Missing sqlite marks bootstrap and skips later import. Settled conflict: U2 writes the marker on a fresh open when leftover sqlite is absent.
- P1 `internal/rocketclaw/harnessbridge/store.go:1366` fc check reports ok before leftover sqlite import. Settled conflict: U3/KD7 makes `fc check` a PostgreSQL ping, not an importer.

Applied in the isolated review change: capture femtoclaw-first selection, capture help text, delete `fcTestConfigJSON`.
