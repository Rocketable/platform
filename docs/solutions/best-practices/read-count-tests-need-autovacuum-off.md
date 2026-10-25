---
title: Tests That Count PostgreSQL Reads Must Turn Autovacuum Off for Their Tables
date: 2026-10-25
category: docs/solutions/best-practices/
module: internal/rocketclaw/backend
problem_type: best_practice
component: database
severity: medium
applies_when:
  - A test asserts how many rows a query reads, through pg_stat_get_tuples_returned or similar counters
  - The test loads enough rows to wake autovacuum, or updates many rows after its own ANALYZE
  - The read count depends on which plan the planner picks, not only on the data
symptoms:
  - A read-count assertion fails rarely in CI but passes on every local run
  - The failing count is a different number on each failure (seen so far as 4864 and 5120)
root_cause: test_isolation
resolution_type: test_fix
related_components:
  - internal/rocketclaw/backend/harnessbridgetest
tags:
  - postgresql
  - autovacuum
  - query-planner
  - flaky-test
  - test-isolation
  - pg-stat
---

# Tests That Count PostgreSQL Reads Must Turn Autovacuum Off for Their Tables

## Context

`TestChatListingReadsScaleWithReportingRuns` (`internal/rocketclaw/backend/store_schema_test.go:123`) checks that the chat listing reads a number of rows that grows with chats and cron runs, never with entries. It loads 1,280 conversations and about 10,000 entries, runs `ANALYZE` (line 143), calls `saveEntryFacts` on 256 chats (line 151), then sums `pg_stat_get_tuples_returned` over the two tables and their indexes (line 155) and requires fewer than 4,096 (line 179).

It usually reads 1,806 rows. Once in CI it read 4,864, which pushed a web-only PR (#661) out of the merge queue. Every local run passed, and the CI failure had nothing to do with that PR's change.

## Guidance

When a test counts the rows a query reads, turn autovacuum off for every table it fills, before loading data:

```sql
ALTER TABLE conversations SET (autovacuum_enabled = false);
ALTER TABLE entries SET (autovacuum_enabled = false);
```

Then set the planner's statistics yourself with `ANALYZE` at the point the test means them to apply. This is safe in the shared test database: each test runs in its own schema (`harnessbridgetest.IsolatedTestDatabaseURL`), so the setting reaches only that test's tables. PR #663 added it to this test (`store_schema_test.go:133-134`).

To check whether a read-count test is exposed, run it under CI-like conditions on a throwaway test database:

```sql
ALTER SYSTEM SET autovacuum_naptime = '1s';
SELECT pg_reload_conf();
-- and, from another connection, hold a snapshot open for the whole run:
BEGIN ISOLATION LEVEL REPEATABLE READ; SELECT count(*) FROM pg_class; SELECT pg_sleep(900);
```

Without the fix, the chat listing test failed 3 runs out of 6 this way. With it, 12 out of 12 passed. Reset the setting with `ALTER SYSTEM RESET autovacuum_naptime` afterwards.

## Why This Matters

The rows a query reads depend on the plan, and the plan depends on statistics, table sizes, and how much of each table is marked all-visible. Autovacuum changes all three. Inserts beyond the Postgres default thresholds (50 rows plus 10% for analyze, 1,000 rows plus 20% for insert-triggered vacuum) make a test's own tables eligible. Once eligible, autovacuum can run at any moment during the test, between the test's `ANALYZE` and its measurement.

The same query and data gave these counts depending on what maintenance had run on `conversations`:

| Maintenance before the measurement | Rows read |
|---|---|
| `VACUUM` | 1,536 |
| none (the test's own `ANALYZE` only) | 1,806 |
| `VACUUM` and `ANALYZE` | 2,816 |
| `ANALYZE` after `saveEntryFacts` | 3,086 |
| `VACUUM` and `ANALYZE` of both tables, with another transaction open | 4,864 |
| real autovacuum every second, with another transaction open | 5,120 |

The open transaction matters. In CI, tests run in parallel against one database, so some transaction is usually open. `VACUUM` then cannot remove the row versions that `saveEntryFacts` replaced, which changes what the planner sees. On a laptop nothing else is running, so the failure almost never shows up locally.

None of this is a product problem. Every one of those plans read the same number of rows with 8, 32, or 128 entries per conversation, so the listing still never reads more as entries grow. The test was measuring a plan it did not control.

Maintenance itself does not add to the counters: `ANALYZE` and `VACUUM` on these tables left `pg_stat_get_tuples_returned` unchanged. Only the plan change moves the number.

## When to Apply

- Writing a new test that asserts a read count, a scan count, or anything else that depends on the plan.
- A read-count test that inserts or updates more than a few dozen rows.

Small tests are not exposed. The other read-count tests here (`TestDelegationLookupStopsAtFirstChildEntry`, `TestChatOriginFactsSkipsKnownSourceLookup`, `TestSidebarSessionsReadNoHistory`) passed 8 out of 8 under the same stress without the setting.

## Related

- `docs/solutions/best-practices/postgres-extension-in-migration-with-shared-test-database.md`, another consequence of test schemas sharing one database.
- `docs/solutions/best-practices/test-postgres-container-needs-larger-shm.md`, setting up the local test database.
