# 0005. SQLite State Store

Status: Accepted
Human approval required for meaning changes: Yes

## Decision

RocketClaw treats `<runtime-dir>/state.sqlite3` as a single-process-owned SQLite state store with one centralized opener, one SQLite configuration site, and one schema initialization path.

## Scope

This ADR governs RocketClaw access to `<runtime-dir>/state.sqlite3`, including daemon runtime access and operational commands such as `rocketclaw fc`. It does not govern unrelated standalone SQLite files outside RocketClaw state/session storage.

## Context

RocketClaw stores persistent RocketCode sessions, managed thread routing, response checkpoints, external MCP mappings, scheduled messages, Slack goal-loop state, and restart notifications in one workspace-local SQLite file. The daemon and operational commands may access that file concurrently from separate processes. Divergent open paths, SQLite PRAGMAs, or connection limits would make lock behavior and durability depend on which interface touched the file.

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
- `rocketclaw fc delete` must refuse to run while that lock is held because it mutates the state store outside the daemon.
- `rocketclaw fc list` and `rocketclaw fc observe` remain allowed while the daemon is running because they are inspection commands, and must use the read-only opener mode.

### Inspection Queries

- Read-only inspection commands must not create, migrate, vacuum, checkpoint, or otherwise mutate `<runtime-dir>/state.sqlite3`.
- `rocketclaw fc list` bounded inspection options `--since`, `--until`, and `--limit` must apply inside the session-store query path before table output is produced.
- Bounded `rocketclaw fc list` selection is based on each conversation's latest stored entry timestamp. Time comparisons must use SQLite date/time comparison, such as `julianday(entry_timestamp)`, rather than RFC3339Nano text ordering.
- When bounded by `--limit N`, `rocketclaw fc list` selects the `N` most recently updated sessions, ordered by latest update descending and then conversation ID for stable ties. `--limit 0` means no limit.
- `--no-message-preview` changes only the displayed columns for `rocketclaw fc list`; it must not change which sessions are selected.

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

For newly created state stores, `PRAGMA page_size = 4096` and `PRAGMA auto_vacuum = INCREMENTAL` must be applied before any tables or indexes are created so the database is created ready for incremental vacuum.

### Existing Databases

- Existing databases created before `auto_vacuum = INCREMENTAL` was active may require an external manual rebuild before incremental auto-vacuum is fully active for that database file.

### Daemon Maintenance

- Daemon startup must quick-check an existing state store before pruning, checkpointing, applying restart notifications, or starting connectors.
- If the quick-check proves corruption, daemon startup must attempt copy-first recovery while holding the daemon ownership lock.
- Startup corruption recovery must invoke the external `sqlite3` command-line shell `.recover` command only for a corruption-proven existing database.
- Startup corruption recovery must snapshot `state.sqlite3`, `state.sqlite3-wal`, and `state.sqlite3-shm` when present into `<runtime-dir>/tmp/`, recover from that snapshot into a fresh database, validate the recovered database, move the corrupt live database files aside, and install only the validated recovered main database.
- If recovery fails, daemon startup must fail and must not continue to pruning, checkpoint, restart notification application, connector startup, or another state-store mutation.
- Daemon startup must run WAL checkpoint cleanup after startup retention pruning and before normal connector, cron, and bridge startup continues.
- Daemon startup checkpoint cleanup must run through the already-open centralized SQLite handle, not by opening a second SQLite handle.
- Daemon startup checkpoint cleanup must run `PRAGMA wal_checkpoint(TRUNCATE)`.
- After the state service is opened, the daemon must start one background incremental-vacuum loop using the already-open centralized SQLite handle, not a second SQLite handle.
- The background incremental-vacuum loop must run `PRAGMA incremental_vacuum` once immediately without blocking the startup sequence, then once per hour until shutdown.
- Shutdown must cancel the background incremental-vacuum loop and wait for it before closing the state store.
- Full `VACUUM` must not run from daemon startup, `openSessionDB`, inspection commands such as `rocketclaw fc list`, or operational commands such as `rocketclaw fc delete`.
- A startup checkpoint cleanup error, busy checkpoint result, or background incremental-vacuum error must be logged but must not fail startup or stop the daemon.

## Non-Goals

- This ADR does not require changing unrelated SQLite users outside RocketClaw state/session storage.
- This ADR does not require automatic compaction beyond the daemon-owned background incremental-vacuum loop.
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
- 2026-06-07: Added daemon startup copy-first corruption recovery using the external `sqlite3` shell `.recover` command only after quick-check proves corruption.
- 2026-06-11: Added Slack goal-loop state to the contents stored in the centralized RocketClaw SQLite state store.
- 2026-06-12: Specified query-level bounded `rocketclaw fc list` inspection semantics for `--since`, `--until`, `--limit`, and `--no-message-preview`.
- 2026-06-12: Removed operational `rocketclaw fc vacuum`, replaced startup full `VACUUM` with daemon-owned background incremental vacuum, and required new state stores to be created ready for incremental vacuum.
