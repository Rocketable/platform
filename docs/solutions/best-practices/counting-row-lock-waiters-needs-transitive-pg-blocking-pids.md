---
title: Counting the statements queued behind a row lock needs pg_blocking_pids followed transitively
date: 2026-11-23
category: docs/solutions/best-practices/
module: internal/rocketclaw/backend
problem_type: best_practice
component: database
severity: medium
applies_when:
  - A test holds a row lock (SELECT ... FOR UPDATE) in a transaction and waits until N concurrent statements queue behind it before releasing
  - A wait condition counts rows of pg_stat_activity whose pg_blocking_pids contains the lock holder's pid
  - Copying a lock-waiter check from one test into another that waits for more than one waiter
symptoms:
  - "A require.Eventually that waits for two waiters on one row fails every run with \"Condition never satisfied\""
  - Only one waiter is ever counted, even though both statements are visibly blocked
root_cause: wrong_api
resolution_type: test_fix
tags: [postgresql, pg-blocking-pids, row-lock, lock-queue, recursive-cte, flaky-test, concurrency-test]
---

# Counting the statements queued behind a row lock needs pg_blocking_pids followed transitively

## Context

Several backend tests prove that two concurrent calls serialize correctly. They hold a row lock in a transaction, start the calls, wait until both are queued behind the lock, then commit and check that exactly one call won. If the test commits before both calls are queued, the second call may run after the first finishes, and the race the test claims to cover never happens.

`TestNavigateTool` (`internal/rocketclaw/backend/session_tools_test.go`) does this with two `PollAttention` calls against the person's `web_tab_states` row. Its first version reused the waiter check from `TestTabViewLocksStateBeforeAttention` (`internal/rocketclaw/backend/unread_test.go:315`) and counted direct waiters:

```sql
SELECT count(*) FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))
```

and waited for `blocked == 2`. Under `go test -race -count 5` it failed with `Condition never satisfied` after its 5-second wait, and the run took 26 seconds, about five such timeouts. Both calls were queued on the row, but with this query the count can only reach 1 (see Why This Matters).

## Guidance

To count every statement waiting on a lock, follow `pg_blocking_pids` transitively from the holder with a recursive CTE, and subtract the holder itself:

```sql
WITH RECURSIVE waiting AS (
    SELECT $1::int AS pid
    UNION SELECT a.pid FROM pg_stat_activity a JOIN waiting w ON w.pid = ANY(pg_blocking_pids(a.pid))
)
SELECT count(*) - 1 FROM waiting
```

With this query, `TestNavigateTool` passed 10 of 10 runs under `-race`. The backend tests already use it in three places:

- `internal/rocketclaw/backend/session_tools_test.go:844-853` (`TestNavigateTool`, two `PollAttention` calls on one `web_tab_states` row)
- `internal/rocketclaw/backend/store_test.go:2684-2694` (two `EditSharedSearch` calls on one `web_shared_tabs` row)
- `internal/rocketclaw/backend/agent_drafts_test.go:72-82` (presence writes behind a transaction-scoped advisory lock)

Two details matter:

- `$1::int` is required. Without the cast, the parameter's type is unknown and resolves to `text`, and Postgres rejects the join with `operator does not exist: text = integer`.
- `UNION` (not `UNION ALL`) removes duplicates. A third waiter lists both earlier waiters as blockers, so `UNION ALL` would count it twice.

The direct form still gives the right answer when a test only needs to know that someone is waiting, because the first waiter always lists the holder. `unread_test.go:315`, `shadows_test.go:1070`, `attention_test.go:259` and `message_search_test.go:636` use it that way. Some of them wrap it in `EXISTS (SELECT ...)`, a subquery that the SQL rules in `AGENTS.md` do not allow in new or touched queries; `count(*) > 0` asks the same thing without one.

## Why This Matters

`pg_blocking_pids(pid)` lists the processes a backend is waiting on directly. It does not list every process ahead of it. For a row lock, Postgres queues waiters through a separate tuple lock:

1. The first waiter takes the tuple lock and waits on the holder's transaction ID. Its blockers are `{holder}`.
2. Every later waiter waits for that tuple lock, which the first waiter holds. Its blockers are the waiters ahead of it, not the holder.

Checked on the test image (PostgreSQL 18.7), with one `FOR UPDATE` holder and three `UPDATE`s of the same row:

| Waiter | `pg_blocking_pids` | `wait_event` |
|---|---|---|
| first | `{holder}` | `transactionid` |
| second | `{first}` | `tuple` |
| third | `{first, second}` | `tuple` |

The direct count returned 1 and the transitive count returned 3. `INSERT ... ON CONFLICT DO UPDATE` waiters, which is what `PollAttention` runs (`internal/rocketclaw/backend/attention.go:274`), gave the same shape: direct 1, transitive 2.

Advisory locks behave differently. Two waiters on `pg_advisory_xact_lock(42)` reported `{holder}` and `{holder, first}`, so a direct count does see both. That is why the direct form looks correct when it is copied from a test that uses an advisory lock. The transitive query is correct for both kinds of lock, so use it whenever the count is more than one.

## When to Apply

- Writing a race test that waits for a specific number of statements to queue behind a row lock taken with `FOR UPDATE`, `UPDATE`, `DELETE`, or `INSERT ... ON CONFLICT DO UPDATE`.
- Reusing a lock-waiter check from another test. Check whether the source test waited on an advisory lock or on a row lock, and whether it counted to one or to more.
- An `Eventually` waiting for `== 2` (or more) waiters times out even though the waiting goroutines are clearly blocked.

## Examples

Before (always 1 for two waiters on one row):

```go
require.Eventually(t, func() bool {
	var blocked int
	require.NoError(t, service.db.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))`, holder).Scan(&blocked))

	return blocked == 2
}, 5*time.Second, time.Millisecond, "both windows wait for the state lock")
```

After:

```go
require.Eventually(t, func() bool {
	var blocked int
	require.NoError(t, service.db.QueryRowContext(t.Context(), `WITH RECURSIVE waiting AS (
    SELECT $1::int AS pid
    UNION SELECT a.pid FROM pg_stat_activity a JOIN waiting w ON w.pid = ANY(pg_blocking_pids(a.pid))
)
SELECT count(*) - 1 FROM waiting`, holder).Scan(&blocked))

	return blocked == 2
}, 5*time.Second, time.Millisecond, "both windows wait for the state lock")
```

A stronger fix, not made here: the backend tests check for lock waiters in two forms. One shared test helper that counts waiters transitively would remove the direct form that can be copied by mistake. Its callers would compare the result with `> 0` or `== N`.

## Related

- [PostgreSQL system information functions, `pg_blocking_pids`](https://www.postgresql.org/docs/current/functions-info.html)
- `docs/solutions/best-practices/canceled-transaction-commit-error-is-not-deterministic.md`, another concurrency test in this package that behaves differently in CI and locally.
- `docs/solutions/best-practices/read-count-tests-need-autovacuum-off.md`, another Postgres behavior that backend tests have to control.
