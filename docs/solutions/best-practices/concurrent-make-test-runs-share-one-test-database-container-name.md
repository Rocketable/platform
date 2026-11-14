---
title: Concurrent make test Runs on a Mac Must Not Share Docker Names or Volume Paths
date: 2026-11-09
last_updated: 2026-11-14
category: docs/solutions/best-practices/
module: internal/rocketclaw
problem_type: developer_experience
component: development_workflow
severity: medium
root_cause: concurrency
resolution_type: code_fix
applies_when:
  - Running make test or make check-code-metrics for RocketClaw on a Mac while another workspace may run them too
  - Changing how the coverage-profile recipe starts or removes its PostgreSQL container
  - Changing where the sandbox-test recipe keeps its test binary, image or coverage data on a Mac
  - Adding any Docker container, volume path or temporary name to a recipe that runs in the rocketclaw-linux container
symptoms:
  - "`docker: Error response from daemon: Conflict. The container name \"/rocketclaw-pg-14\" is already in use by container ...` followed by `make[1]: *** [Makefile:146: coverage-profile] Error 125`"
  - "Dozens of tests fail in one run with `create test schema: failed to connect to user=postgres database=rocketclaw_test: 127.0.0.1:<port> ... connect: connection refused`"
  - "An emulated shell test fails with a stack trace through another workspace's source files"
  - The same commit passes when no other workspace is running RocketClaw tests
related_components:
  - internal/rocketclaw/linux.Containerfile
  - internal/rocketclaw/sandbox
tags:
  - make-test
  - coverage-profile
  - sandbox-test
  - test-database
  - docker
  - linux-container
  - concurrent-workspaces
---

# Concurrent make test Runs on a Mac Must Not Share Docker Names or Volume Paths

## Context

On a Mac, every `make` target in `internal/rocketclaw` runs again in the `rocketclaw-linux` container. Every run, from every JJ workspace, gets the Colima VM's Docker socket and the same `rocketclaw-linux-sandbox` volume (`internal/rocketclaw/Makefile:25`–`29`). Two shared things broke runs that overlapped:

- **The test database's name.** `coverage-profile` named its PostgreSQL container `rocketclaw-pg-$$$$`, after the recipe shell's PID. Inside the container `make` is PID 1 (`docs/solutions/best-practices/coverage-profile-kills-its-own-shell-when-make-is-pid-1.md`), so that PID repeated from run to run; `rocketclaw-pg-14` and `rocketclaw-pg-12` were both seen. The run that lost the name failed with `Conflict`, and its exit trap then removed the winner's database by name, so dozens of the other run's tests failed with `connection refused`.
- **The emulated shell's test folder.** Every workspace's `sandbox-test` used `/sandbox` itself as `SANDBOX_TEST_DIR`. Each run compiled its test binary to `/sandbox/harness.test`, deleted `/sandbox/gocover`, and prepared the image in `/sandbox/sandbox`, where preparing deletes the shared `state` folder (`internal/rocketclaw/sandbox/runtime_linux.go:100`). On 2026-11-14 a run failed `TestEmulatedShellLimits` with a stack trace through `.tmp/workspaces/20261025-config-in-postgresql/...`: it had run another workspace's binary. Sessions had started working around it with folders of their own (`/sandbox/deps1200`, `/sandbox/deps-1109-1000`).

Nothing in either failure names the other run, so both looked like flaky tests.

## Guidance

Both are fixed in `internal/rocketclaw/Makefile`:

- `coverage-profile` keeps the ID that `docker run -d` prints instead of choosing a name (`:183`). A failed `docker run` leaves no ID, so the exit trap removes nothing (`:176`).
- On a Mac, each workspace gets `/sandbox/<its path with / as _>` as `SANDBOX_TEST_DIR` (`:12`, `:27`). Only the package cache, `/sandbox/packages`, is shared, so a workspace's first run builds its image without downloading the packages again. Each run writes its workspace's path to `.workspace` in its folder and removes folders whose workspace no longer exists (`:105`). Folders without a `.workspace` file, such as the hand-made ones above, are left alone.

Concurrent runs no longer need their own `ROCKETCLAW_TEST_DATABASE_URL`. A database of your own still works, for example to give it more shared memory (`docs/solutions/best-practices/test-postgres-container-needs-larger-shm.md`): the recipe starts no container when the URL is set (`:181`). Start it on the container's Docker context and pass the URL as a make argument, since the container receives only those:

```sh
D="docker --context colima-rocketclaw-linux"
N=$($D run -d --shm-size=1g -e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_USER=postgres \
  -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=rocketclaw_test -p 127.0.0.1::5432 postgres:18)
P=$($D port "$N" 5432/tcp | head -1); P=${P##*:}
until $D exec "$N" pg_isready -U postgres -d rocketclaw_test >/dev/null 2>&1; do sleep 1; done
make test ROCKETCLAW_TEST_DATABASE_URL="postgres://postgres:postgres@127.0.0.1:$P/rocketclaw_test?sslmode=disable"
$D rm -f -v "$N"
```

`127.0.0.1` works because the test container runs with `--network host` inside the same VM (`:26`).

Keep the rule when changing these recipes: a name or path on the shared daemon or volume must be unique per run or per workspace. A PID from inside the container is neither, and neither is a fixed path under `/sandbox`.

The folders the old layout used, `/sandbox/harness.test`, `/sandbox/gocover`, `/sandbox/guest.out`, `/sandbox/tmp` and `/sandbox/sandbox`, stay until removed by hand. Remove them only once no workspace runs a branch without this fix, since those runs still use them.

## Why This Matters

Agents run several JJ workspaces at once in this repository, so overlapping `make test` runs are ordinary. Before the fix, each overlap cost a failed run, then a rerun or a hunt through whichever tests happened to fail. A wait for the other run to finish was the only reliable workaround, which made every run slower.

## When to Apply

- When a RocketClaw make target on a Mac fails while another workspace is testing.
- When adding a container, a volume path, or a generated name to a recipe that runs in the container.

## Examples

Before, with another workspace running `make test`:

```text
docker: Error response from daemon: Conflict. The container name "/rocketclaw-pg-12" is already in use by container "c9ce6e8cc127…".
make: *** [Makefile:160: coverage-profile] Error 125
```

```text
--- FAIL: TestEmulatedShellLimits (19.52s)
  … /Users/…/.tmp/workspaces/20261025-config-in-postgresql/internal/rocketclaw/harness/decision_test.go:62 …
```

After: the database container has an ID of its own, and a workspace's test folder is `/sandbox/_Users_ucirello_Projects_platform`.

## Related

- `docs/solutions/best-practices/coverage-profile-kills-its-own-shell-when-make-is-pid-1.md`: why `make` is PID 1 in the container.
- `docs/solutions/best-practices/test-postgres-container-needs-larger-shm.md`: the shared-memory limit a self-started test database can hit.
- `docs/solutions/best-practices/long-lived-test-database-slows-migrations.md`: why a reused `ROCKETCLAW_TEST_DATABASE_URL` database should be recreated for each run.
