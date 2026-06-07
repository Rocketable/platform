# 0005. SQLite State Store

Status: Accepted
Human approval required for meaning changes: Yes

## Decision

RocketClaw treats `<runtime-dir>/state.sqlite3` as a single-process-owned SQLite state store with one centralized opener, one SQLite configuration site, and one schema initialization path.

## Scope

This ADR governs RocketClaw access to `<runtime-dir>/state.sqlite3`, including daemon runtime access and operational commands such as `rocketclaw fc`. It does not govern unrelated standalone SQLite files outside RocketClaw state/session storage.

## Context

RocketClaw stores persistent RocketCode sessions, managed thread routing, response checkpoints, external MCP mappings, scheduled messages, and restart notifications in one workspace-local SQLite file. The daemon and operational commands may access that file concurrently from separate processes. Divergent open paths, SQLite PRAGMAs, or connection limits would make lock behavior and durability depend on which interface touched the file.

## Normative Contracts

### Centralized Opening

- RocketClaw state/session SQLite access must go through one opener in `internal/rocketclaw/harnessbridge`.
- That opener may expose read-write and read-only modes, but it remains the only RocketClaw code path allowed to call `sql.Open("sqlite", ...)` for `<runtime-dir>/state.sqlite3`.
- The read-only opener mode must use SQLite URI `mode=ro` and must not create the runtime directory, create the database file, set persistent PRAGMAs, initialize schema, run migrations, vacuum, checkpoint, or otherwise mutate the state store.
- That opener is the only place SQLite PRAGMAs for `<runtime-dir>/state.sqlite3` are configured. Read-only mode may configure only connection-local read behavior such as `busy_timeout`.
- That opener is the only place schema initialization and schema migrations for `<runtime-dir>/state.sqlite3` are applied.
- All RocketClaw interfaces that inspect or mutate the state store, including the daemon runtime and `rocketclaw fc`, must use this opener or wrappers that delegate to it.

### Connection Concurrency

- Each RocketClaw SQLite handle for `<runtime-dir>/state.sqlite3` must set `SetMaxOpenConns(1)`.
- Each RocketClaw SQLite handle for `<runtime-dir>/state.sqlite3` must set `SetMaxIdleConns(1)`.
- These process-local limits do not replace SQLite cross-process locking; they ensure each RocketClaw process has one database/sql connection competing for the file.

### Daemon Ownership Lock

- Daemon startup must acquire and hold a runtime-owned advisory lock for `<runtime-dir>/state.sqlite3` ownership before opening the state store.
- A second daemon must fail startup while that lock is held.
- `rocketclaw fc delete` and `rocketclaw fc vacuum` must refuse to run while that lock is held because they mutate or maintain the state store outside the daemon.
- `rocketclaw fc list` and `rocketclaw fc observe` remain allowed while the daemon is running because they are inspection commands, and must use the read-only opener mode.

### Required PRAGMAs

The centralized opener must configure these PRAGMAs for `<runtime-dir>/state.sqlite3`:

- `PRAGMA journal_mode = WAL`
- `PRAGMA synchronous = NORMAL`
- `PRAGMA busy_timeout = 30000`
- `PRAGMA cache_size = -64000`
- `PRAGMA mmap_size = 268435456`
- `PRAGMA temp_store = MEMORY`
- `PRAGMA auto_vacuum = INCREMENTAL`
- `PRAGMA page_size = 4096`

### Existing Databases

- Existing databases may require a manual `VACUUM` after `auto_vacuum = INCREMENTAL` is introduced before incremental auto-vacuum is fully active for that database file.

### Startup Maintenance

- Daemon startup must run SQLite cleanup after startup retention pruning and before normal connector, cron, and bridge startup continues.
- Daemon startup cleanup must run through the already-open centralized SQLite handle, not by opening a second SQLite handle.
- Daemon startup cleanup must run `PRAGMA optimize`, full `VACUUM`, and `PRAGMA wal_checkpoint(TRUNCATE)`.
- Full `VACUUM` must be daemon-startup-only maintenance and must not run implicitly from `openSessionDB` or inspection commands such as `rocketclaw fc list`.
- A startup cleanup error or busy checkpoint result must be logged but must not fail startup.

## Non-Goals

- This ADR does not require changing unrelated SQLite users outside RocketClaw state/session storage.
- This ADR does not require automatic compaction or operational scheduling of database maintenance beyond daemon-startup SQLite cleanup.
- This ADR does not define Slack placeholder cleanup or restart replay semantics.

## Evidence

- `internal/rocketclaw/harnessbridge/store.go`
- `cmd/rocketclaw/fc.go`
- `internal/rocketclaw/app/app.go`

## Consequences

- Future RocketClaw state-store changes must preserve the single opener and centralized SQLite configuration unless this ADR is updated first.
- Tests may use separate SQLite connections only when the test intentionally models an external competing process or a corrupted database setup.
- Adding a second production RocketClaw `sql.Open("sqlite", ...)` path for `<runtime-dir>/state.sqlite3` violates this ADR.

## Changelog

- 2026-06-05: Initial accepted snapshot.
- 2026-06-05: Added startup WAL checkpoint/truncation after retention pruning while keeping full `VACUUM` manual only.
- 2026-06-05: Replaced manual-only full `VACUUM` policy with daemon-startup cleanup after retention pruning; inspection opens still must not vacuum implicitly.
- 2026-06-07: Added daemon ownership locking and required `rocketclaw fc delete` / `rocketclaw fc vacuum` to refuse while the daemon holds the state-store lock.
- 2026-06-07: Required read-only state-store opener mode with SQLite URI `mode=ro` for `rocketclaw fc list` and `rocketclaw fc observe`.
