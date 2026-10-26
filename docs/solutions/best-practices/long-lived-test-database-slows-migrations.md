---
title: A Long-Lived Test Database Slows Every Migration and Fails Timing Tests
date: 2026-10-20
category: docs/solutions/best-practices/
module: internal/rocketclaw/backend
problem_type: developer_experience
component: development_workflow
severity: medium
resolution_type: environment_setup
applies_when:
  - Pointing ROCKETCLAW_TEST_DATABASE_URL at one PostgreSQL server across many `make test` or `go test` runs
  - Backend test time grows run after run, or tests that wait about a second for a turn start timing out
  - Judging whether a branch made migrations or queries slower
symptoms:
  - The internal/rocketclaw/backend package takes several times longer than before, on old and new code alike
  - TestBridgeDeletes* and other tests that wait about a second for a turn fail under a full run and pass alone
  - CREATE OR REPLACE FUNCTION and CREATE TRIGGER statements in migrations take tens of milliseconds each
related_components:
  - internal/rocketclaw/backend/harnessbridgetest
  - internal/rocketclaw/Makefile
tags:
  - postgresql
  - test-database
  - catalog-bloat
  - migrations
  - flaky-tests
  - make-test
---

# A Long-Lived Test Database Slows Every Migration and Fails Timing Tests

## Context

Every backend test gets a fresh schema on the database at `ROCKETCLAW_TEST_DATABASE_URL`. `harnessbridgetest.IsolatedTestDatabaseURL` creates it with `CREATE SCHEMA` (`internal/rocketclaw/backend/harnessbridgetest/database.go`) and nothing drops it, so each schema and its tables, functions, and triggers stay in the catalog after the test ends. One backend package run adds about 600 schemas.

While building the State Store consolidation (PR #526), the backend package went from 92 s to 464 s over a day of runs against one long-lived container, and three tests that wait about a second for a turn started failing. It looked like the branch's new migrations or queries were slow. Measured, they were not: the unchanged base commit, run against the same database, took 414 s and failed the same tests, while both trees passed in 80 to 97 s on a fresh database. The database held about 42,000 leftover schemas, 6.5 million `pg_class` rows, and 57 GB.

The cost lands on statements that look objects up by name across schemas. Replacing a function with body checking on, or creating a trigger, read one `pg_proc` row per schema that defines a function with that name: about 40,000 rows, 70 to 130 ms per statement instead of under 10 ms. Every test schema runs every migration, so migrations full of notify functions slow every test, and a turn that waits on such work misses a one-second deadline.

## Guidance

- Recreate the test container when backend runs get slower, and before comparing timings between two revisions. Remove its volume too (`docker rm -f -v`); the postgres image declares an anonymous volume that a plain `docker rm -f` keeps.
- Compare a suspected slowdown against the base revision on the same, fresh database before blaming the change.
- Do not raise test timeouts or retry timing tests to get past this.
- `make test` without `ROCKETCLAW_TEST_DATABASE_URL` starts a fresh container per run and does not hit this.

The lasting fix is for `IsolatedTestDatabaseURL` to take a `testing.TB` and drop its schema in `t.Cleanup`; its 58 callers make that a change of its own.

## When to Apply

Count leftover schemas when backend tests slow down on a reused database:

```sql
SELECT count(*) FROM pg_namespace WHERE nspname LIKE 't\_%';
```

Thousands means the catalog, not the code, is the likely cause.

## Related

- `docs/solutions/best-practices/test-postgres-container-needs-larger-shm.md`: the other limit a shared test database hits under parallel tests.
- `docs/solutions/best-practices/postgres-extension-in-migration-with-shared-test-database.md`: another consequence of every test sharing one database.
