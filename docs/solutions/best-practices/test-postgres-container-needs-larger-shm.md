---
title: The Test PostgreSQL Container Needs a Larger /dev/shm
date: 2026-10-08
category: docs/solutions/best-practices/
module: internal/rocketclaw/backend
problem_type: developer_experience
component: development_workflow
severity: medium
resolution_type: environment_setup
applies_when:
  - Running the internal/rocketclaw backend tests against PostgreSQL in Docker
  - Starting a long-lived local test database and pointing ROCKETCLAW_TEST_DATABASE_URL at it
  - The backend suite fails with PostgreSQL shared-memory errors that do not reproduce when a test runs alone
symptoms:
  - The backend test suite fails with PostgreSQL shared-memory errors once many tests run in parallel
  - The same tests pass when run alone or with less parallelism
related_components:
  - internal/rocketclaw/Makefile
  - .github/workflows/test.yml
  - internal/rocketclaw/backend/harnessbridgetest
tags:
  - postgresql
  - docker
  - shm-size
  - shared-memory
  - make-test
  - test-database
---

# The Test PostgreSQL Container Needs a Larger /dev/shm

## Context

The backend tests do not each get their own database. Each one creates a fresh schema on the database at `ROCKETCLAW_TEST_DATABASE_URL` (`internal/rocketclaw/backend/harnessbridgetest/database.go`), migrates it, and runs against it. `go test` runs packages concurrently, and parallel tests within a package add more, so a single PostgreSQL server carries many concurrent sessions and migrations.

Docker gives a container a 64 MB `/dev/shm` unless told otherwise. PostgreSQL on Linux keeps its dynamic shared memory there by default (`dynamic_shared_memory_type = posix`). Parallel query workers, among other things, use it. With enough concurrent work, 64 MB runs out and PostgreSQL fails the query with a shared-memory error. The test that fails depends on timing, not on what it tests.

While building the indexed message search (`docs/plans/2026-10-08-1107-feat-indexed-message-search-plan.md`), the backend suite began failing this way on a local Docker PostgreSQL. Starting the container with `--shm-size=1g` fixed it.

## Guidance

Give any Docker PostgreSQL used for these tests a larger `/dev/shm`:

```bash
docker run -d --name rocketclaw-test-pg --shm-size=1g \
  -e POSTGRES_HOST_AUTH_METHOD=trust \
  -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=rocketclaw_test \
  -p 127.0.0.1:55432:5432 postgres:18
export ROCKETCLAW_TEST_DATABASE_URL='postgres://postgres:postgres@127.0.0.1:55432/rocketclaw_test?sslmode=disable'
```

As of this writing, neither of the repo's own container setups sets it:

- `make test` in `internal/rocketclaw` (target `check-coverage` in `internal/rocketclaw/Makefile`) runs `docker run -d --name "$name" ... postgres:$latest` with no `--shm-size` when `ROCKETCLAW_TEST_DATABASE_URL` is unset.
- The `postgres` service in `.github/workflows/test.yml` sets only health-check `options`. GitHub Actions service containers accept `--shm-size` in that same `options` string.

If either path starts failing with shared-memory errors, add `--shm-size=1g` there. Do not lower test parallelism or retry flaky tests to work around it.

## Why This Matters

The failure looks like a flaky test, or like a bug in whatever query happened to fail, so the natural response is to debug that test. The real limit is the container's memory filesystem, and it grows with the number of tests that hit the database at once. Every new test that runs in parallel against PostgreSQL moves the suite closer to it, so it will come back as the suite grows.

## When to Apply

- Setting up a local test database for `internal/rocketclaw`.
- Seeing PostgreSQL errors that mention shared memory, or `No space left on device` on a shared memory segment, during a full backend run but not when the test runs alone.
- Changing how `make test` or CI starts PostgreSQL.

## Examples

Before: `docker run -d ... postgres:18` (64 MB `/dev/shm`). The backend suite failed with shared-memory errors once many isolated schemas were running in parallel.

After: `docker run -d --shm-size=1g ... postgres:18`. The same suite passed.

## Related

- `docs/solutions/best-practices/postgres-extension-in-migration-with-shared-test-database.md`: another consequence of every test sharing one database.
