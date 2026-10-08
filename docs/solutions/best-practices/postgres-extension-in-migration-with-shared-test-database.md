---
title: Creating a PostgreSQL Extension From a Migration When Test Schemas Share One Database
date: 2026-10-08
category: docs/solutions/best-practices/
module: internal/rocketclaw/backend
problem_type: best_practice
component: database
severity: medium
applies_when:
  - A migration under internal/rocketclaw/backend/migrations needs CREATE EXTENSION (pg_trgm, btree_gin, and similar)
  - An index or column uses an operator class, type, or function that an extension provides
  - Tests run migrations concurrently in many schemas of one PostgreSQL database
  - Writing the Down step of a migration that created an extension
related_components:
  - internal/rocketclaw/backend/harnessbridgetest
  - internal/rocketclaw/backend/store_schema.go
tags:
  - postgresql
  - migrations
  - pg-trgm
  - create-extension
  - search-path
  - test-isolation
  - advisory-lock
---

# Creating a PostgreSQL Extension From a Migration When Test Schemas Share One Database

## Context

The indexed message search added `internal/rocketclaw/backend/migrations/030_message_search.sql`, the first RocketClaw migration that creates a PostgreSQL extension (`pg_trgm`, for a trigram GIN index on lowercased message text). Before that, every migration only created objects inside the current schema.

The test harness does not give each test its own database. `harnessbridgetest.IsolatedTestDatabaseURL` (`internal/rocketclaw/backend/harnessbridgetest/database.go:38-48`) runs `CREATE SCHEMA t_<random>` on the shared `ROCKETCLAW_TEST_DATABASE_URL` database and sets `search_path` to that one schema only (`options=-csearch_path=<schema>`). The schema migration lock is per schema too: `store_schema.go:43` takes `pg_advisory_lock(hashtext('rocketclaw schema migrations'), hashtext(current_schema()))`, so many test schemas run the same migrations at the same time.

An extension does not belong to a schema the way a table does. Each database has at most one copy of an extension. Its objects (functions, operator classes) live in whichever schema it was created in. That conflicts with the per-schema test layout in two ways.

## Guidance

Write the extension part of the migration like this (as in `030_message_search.sql`):

```sql
-- +migrate Up
-- Schemas sharing one database must not race to create the extension.
SELECT pg_advisory_xact_lock(hashtext('rocketclaw pg_trgm extension'));
CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;

CREATE INDEX message_search_text ON message_search USING gin (text_lower public.gin_trgm_ops);

-- +migrate Down
DROP TABLE message_search;   -- tables only; never DROP EXTENSION
```

Four rules:

1. **Serialize creation with a database-wide transaction advisory lock.** The key must not include `current_schema()`, unlike the migration lock. `pg_advisory_xact_lock` is released at commit, so nothing has to unlock it.
2. **Pin the extension to `public` with `WITH SCHEMA public`.** Without it, the extension's objects go into the first schema on the creating session's `search_path`. Here that is one test's private `t_<random>` schema.
3. **Schema-qualify every extension object you use** (`public.gin_trgm_ops`, `public.similarity(...)`, and so on). Test sessions have only their own schema on `search_path`, so an unqualified `gin_trgm_ops` does not resolve, even when the extension is installed in `public`.
4. **Do not drop the extension in Down.** It is shared by every schema in the database. Other test schemas, and their indexes, still depend on it. Down removes only the migration's own tables and indexes.

## Why This Matters

Without these rules the failures depend on timing and test order, and they look unrelated to the migration:

- **No lock:** two schemas migrating at once can both see the extension as missing. Both run `CREATE EXTENSION`, and one transaction fails on the catalog's uniqueness check. `IF NOT EXISTS` does not stop this race, because both checks run before either transaction commits.
- **No `WITH SCHEMA public`:** the first test to migrate installs the operator class in its own schema. `IF NOT EXISTS` then makes every later schema skip the install, and their `CREATE INDEX ... gin_trgm_ops` cannot find the operator class.
- **Unqualified operator class:** fails in every test schema, because none of them has `public` on `search_path`. A production database with the default `search_path` would not show the bug, so it reaches tests first or, worse, only some environments.
- **DROP EXTENSION in Down:** `TestBackgroundJobsMigration` in `internal/rocketclaw/backend/store_schema_test.go` runs the last three Down migrations (`migrate.Down, 3`), which included 030 when this was written, while other tests use the same database. Dropping the shared extension there would break those tests, or fail because of their dependent indexes.

## When to Apply

- Any new migration that creates an extension, or uses a type, function, or operator class an extension provides.
- Reusing `pg_trgm` from a later migration: still write `public.gin_trgm_ops`. Repeating the lock and `CREATE EXTENSION IF NOT EXISTS` is harmless and keeps the migration self-contained.
- The same reasoning covers anything else that exists once per database rather than once per schema, such as roles, event triggers, and database settings.

## Examples

Before (works against a single fresh database, fails when tests migrate many schemas at once):

```sql
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX message_search_text ON message_search USING gin (text_lower gin_trgm_ops);
-- +migrate Down
DROP TABLE message_search;
DROP EXTENSION pg_trgm;
```

After: see the Guidance block above, which matches `internal/rocketclaw/backend/migrations/030_message_search.sql`.

Production note: `pg_trgm` is a trusted extension (PostgreSQL 13 and later). A role without superuser can create it if it has `CREATE` privilege on the database. Check that the production database role has that privilege before deploying a migration that creates an extension, since startup runs migrations (`README.md`: "RocketClaw upgrades the database schema at startup").

## Related

- `docs/solutions/runtime-errors/deployment-binary-missing-live-migrations.md`: another way a migration can break startup against the live database.
- Plan: `docs/plans/2026-10-08-1107-feat-indexed-message-search-plan.md`
