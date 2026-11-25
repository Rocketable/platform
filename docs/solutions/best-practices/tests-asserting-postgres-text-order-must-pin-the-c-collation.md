---
title: Tests that assert PostgreSQL text order must pin the C collation
date: 2026-11-25
category: docs/solutions/best-practices/
module: internal/rocketclaw/backend
problem_type: best_practice
component: database
severity: low
applies_when:
  - A test compares rows from queryStrings or queryRows against an exact ordered list
  - The query sorts text with a bare ORDER BY, by position or by name
  - The sorted values can differ only in punctuation, case or spaces, such as paths, JSON text or IDs with separators
  - Adding COLLATE "C" to an ORDER BY 1 or to the ORDER BY of a UNION
symptoms:
  - A test passes on the default test database and fails on a database initialized with another locale
  - Expected order looks wrong by byte order, with "tab /voices" before "tab /voice/x"
  - "ERROR: collations are not supported by type integer (SQLSTATE 42804)"
  - "ERROR: invalid UNION/INTERSECT/EXCEPT ORDER BY clause"
root_cause: test_isolation
resolution_type: test_fix
related_components:
  - internal/rocketclaw/Makefile
tags:
  - postgresql
  - collation
  - order-by
  - union
  - migration-test
  - en-us-utf8
---

# Tests that assert PostgreSQL text order must pin the C collation

## Context

`TestDropVoiceMigration` in `internal/rocketclaw/backend/store_schema_test.go` gathers rows from several tables with `UNION ALL`, sorts them and compares the result to an exact `[]string`. The first draft sorted with a bare `ORDER BY 1` and listed `"tab /voice/x"` before `"tab /voices"`, which is byte order. The test failed, and the expected list was reordered to match what the database returned. It then passed locally, and would have passed in CI.

It passed only because of the database's default collation. The `internal/rocketclaw/Makefile` test target and the CI workflow both start the official `postgres` image without locale options. That image creates the database with the `en_US.utf8` libc collation. glibc's `en_US` collation ignores punctuation on its first pass, so it compares `voicex` with `voices` and puts `/voices` first. The `C` collation and ICU's root collation (`und-x-icu`) compare `/` (0x2F) before any letter and put `/voice/x` first. Against a test database created with `C` or ICU, the same test fails. A test database a developer set up with `ROCKETCLAW_TEST_DATABASE_URL` is one example. A code review caught this. No test or lint run here could, because every run uses the same collation.

Two obvious fixes fail:

- **`ORDER BY 1 COLLATE "C"`** fails with `collations are not supported by type integer (SQLSTATE 42804)`. `ORDER BY` reads only a bare integer as a column position. `1 COLLATE "C"` is an expression applied to the integer constant `1`. This happens in any query, not only in a union.
- **`ORDER BY entry COLLATE "C"` on the union itself** fails with `invalid UNION/INTERSECT/EXCEPT ORDER BY clause`. The detail reads "Only result column names can be used, not expressions or functions". PostgreSQL's hint says to move the union into `FROM`, but that makes a derived table, which the repository's SQL rules in `AGENTS.md` forbid.

## Guidance

When a test asserts the order of text rows, pin the collation in the query. Do not copy whatever order the test database returns. Two shapes work, and both were checked on PostgreSQL 19:

1. **Name the union in a CTE and sort the CTE's column.** This is what `TestDropVoiceMigration` does now:

   ```sql
   WITH entries AS (
       SELECT 'tab ' || path AS entry FROM web_tabs
       UNION ALL SELECT 'shared ' || path FROM web_shared_tabs
   )
   SELECT entry FROM entries ORDER BY entry COLLATE "C"
   ```

2. **Put `COLLATE "C"` on the selected value and keep the bare `ORDER BY 1`.** The output column then carries the `C` collation, so sorting by position uses it. `SessionService.SlackChannelIDs` in `internal/rocketclaw/backend/store.go` does this in a single `SELECT`. In a union, one branch is enough. An explicit collation on one input sets the collation of the union's column, as in `SELECT 'tab ' || path COLLATE "C" FROM web_tabs UNION ALL SELECT 'shared ' || path FROM web_shared_tabs ORDER BY 1`. `COLLATE` binds tighter than `||`, so it applies to `path`, and the explicit collation carries through the concatenation.

When order is not what the test checks, compare without it instead: use `require.ElementsMatch`, or sort the slice in Go with `slices.Sort`. Go compares strings byte by byte, which matches `C`.

Production queries whose order matters already pin `C` in the same way. Examples are `shadowHistoryDB` in `history.go` and the principal query in `message_search.go`.

## Why This Matters

The default collation hides the problem. Every local and CI run uses `en_US.utf8`, so a test that depends on it stays green until someone runs it against a database created differently. Then the failure points at the migration, not the environment. Pinning `C` makes the expected list read in plain byte order, which is also the order a reader expects.

## When to Apply

- The sorted values can tie on letters and differ only in `/`, `-`, `_`, spaces, braces or letter case. `en_US.utf8` gives `a, b, B` where `C` gives `B, a, b`.
- Lowercase values that differ at a letter before any punctuation sort the same under `en_US.utf8`, `C` and `und-x-icu`. `TestRoutinesTabPathsMigration` in the same file still uses a bare `ORDER BY 1`, and its current rows (`tab /cronjobs`, `tab /routines`, `tab /routines/ZGFpbHk` and the rest) were checked to sort the same under all three. A new row could still make it depend on the collation. `tab /routinesx` is one: `en_US.utf8` puts it before `tab /routines/ZGFpbHk`, while `C` puts it after.
- Many other backend tests sort conversation IDs with `ORDER BY 1`. That is safe only while the IDs differ by letters or digits before any punctuation.

## Examples

Before (passes only under `en_US.utf8`):

```sql
SELECT 'tab ' || path FROM web_tabs UNION ALL SELECT 'shared ' || path FROM web_shared_tabs ORDER BY 1
```

```go
require.Equal(t, []string{"shared /agents", "tab /voices", "tab /voice/x"}, paths)
```

After (the same result under every collation):

```sql
WITH entries AS (
    SELECT 'tab ' || path AS entry FROM web_tabs UNION ALL SELECT 'shared ' || path FROM web_shared_tabs
)
SELECT entry FROM entries ORDER BY entry COLLATE "C"
```

```go
require.Equal(t, []string{"shared /agents", "tab /voice/x", "tab /voices"}, paths)
```

## Related

- `docs/solutions/best-practices/test-postgres-container-needs-larger-shm.md`: how the test PostgreSQL container is started.
- `docs/solutions/best-practices/read-count-tests-need-autovacuum-off.md`: another test assertion that depends on database state the test does not control.
