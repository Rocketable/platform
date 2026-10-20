---
title: A New Migration Bumps Every Rollback Step Count in the Backend Tests
date: 2026-10-19
category: docs/solutions/best-practices/
module: internal/rocketclaw/backend
problem_type: developer_experience
component: development_workflow
severity: medium
applies_when:
  - Adding a new SQL file under internal/rocketclaw/backend/migrations
  - Migration tests fail after a new migration with "expected N, actual N+1" or a rollback that stops one migration short
tags:
  - migrations
  - sql-migrate
  - migrate-down
  - pg-migrations
  - test-maintenance
---

# A New Migration Bumps Every Rollback Step Count in the Backend Tests

## Context

Several backend tests roll the schema back to just before an older migration with `migrate.Down, N`, where N counts every migration from that one to the newest. Others assert the total number of `pg_migrations` rows. Both encode the current number of migrations, so adding `045_*.sql` broke about thirty tests at once with count mismatches and rollbacks that landed one migration short. Nothing in the failures says which literals to change.

## Guidance

When you add one migration, add one to each of these, and nothing else:

- every `ExecMaxContext(..., migrate.Down, N)` in `internal/rocketclaw/backend/*_test.go`, together with its matching `migrate.Up, N` and the `require.Equal(t, N, n)` that checks how many steps ran;
- every total `pg_migrations` count, such as `assert.Equal(t, 47, n)` in `store_test.go` and `store_schema_test.go`, and the partial count asserted after a failed ledger write in `store_schema_test.go`;
- any comment that spells the count in words, such as "the eighteen migrations that follow it" in `active_turn_test.go`.

Leave the single-step `migrate.Down, 1` / `migrate.Up, 1` calls in `origin_pairs_test.go` alone: they step back and forth over one migration, not to a fixed point.

To find the sites, search for the literals instead of reading failures one at a time:

```bash
grep -n "migrate.Down, [0-9]\+\|migrate.Up, [0-9]\+\|pg_migrations" internal/rocketclaw/backend/*_test.go
```

The commit that added `044_opencode_session_replies.sql` is the precedent: its diff to these test files is exactly this bump, and `jj diff --git -r <that commit> -- 'glob:internal/rocketclaw/backend/*_test.go'` shows every site.

## Why This Matters

The failures look like regressions in unrelated features (background jobs, origin pairs, script calls, cron resume), so they invite debugging the wrong code. A wrong bump is quiet in a different way: a rollback that stops one migration short or goes one too far can still pass while testing a different schema than the test describes.

## When to Apply

- Every new migration file in `internal/rocketclaw/backend/migrations`.
- Removing or squashing migrations needs the same sites changed in the other direction.

## Related

- docs/solutions/best-practices/postgres-extension-in-migration-with-shared-test-database.md
- docs/solutions/best-practices/test-postgres-container-needs-larger-shm.md
