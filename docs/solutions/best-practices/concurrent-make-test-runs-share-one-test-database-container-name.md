---
title: Concurrent make test Runs on a Mac Share One Test Database Container Name
date: 2026-11-09
category: docs/solutions/best-practices/
module: internal/rocketclaw
problem_type: developer_experience
component: development_workflow
severity: medium
root_cause: concurrency
resolution_type: workflow_improvement
applies_when:
  - Running make test or make check-code-metrics for RocketClaw on a Mac while another workspace may run them too
  - "make test fails with `Conflict. The container name \"/rocketclaw-pg-<n>\" is already in use`"
  - Many backend or frontend tests fail at once with connection refused to 127.0.0.1 while creating their test schema
  - Changing how the coverage-profile recipe names, starts or removes its PostgreSQL container
symptoms:
  - "`docker: Error response from daemon: Conflict. The container name \"/rocketclaw-pg-14\" is already in use by container ...` followed by `make[1]: *** [Makefile:146: coverage-profile] Error 125`"
  - "Dozens of tests fail in one run with `create test schema: failed to connect to user=postgres database=rocketclaw_test: 127.0.0.1:<port> ... connect: connection refused`"
  - The same commit passes when no other workspace is running RocketClaw tests
related_components:
  - internal/rocketclaw/linux.Containerfile
tags:
  - make
  - make-test
  - coverage-profile
  - test-database
  - postgresql
  - docker
  - colima
  - linux-container
  - container-name
  - concurrent-workspaces
---

# Concurrent make test Runs on a Mac Share One Test Database Container Name

## Context

On a Mac, every `make` target in `internal/rocketclaw` runs again in the `rocketclaw-linux` container, and that container gets the Colima VM's Docker socket (`internal/rocketclaw/Makefile:14`–`18`). So every RocketClaw test run on the machine, from every JJ workspace, starts its sibling containers on one shared Docker daemon.

When `ROCKETCLAW_TEST_DATABASE_URL` is unset, the `coverage-profile` recipe starts its own PostgreSQL container and names it after the recipe shell's PID: `name=rocketclaw-pg-$$$$` (`Makefile:156`). On a normal host, PIDs differ between concurrent runs. Inside the container they often do not: `make` is PID 1 (see `docs/solutions/best-practices/coverage-profile-kills-its-own-shell-when-make-is-pid-1.md`). Repo-root `make test` and a direct `make check-code-metrics` start a fresh container for `check-code-metrics`, which runs `coverage-profile` as a sub-make (`Makefile:119`–`120`), so the recipe shell gets the same small PID each time. On 2026-11-09, from repo-root `make test`, it was `rocketclaw-pg-14`. `make -C internal/rocketclaw test` runs generate and lint first in the same container, so its PID is larger and less predictable, but still not guaranteed unique on the daemon. (Running `make coverage-profile` directly on a Mac stops earlier, for the reason that doc gives.) Two workspaces running `make test` at the same time therefore ask for the same container name.

The recipe's cleanup makes a collision worse than a single failure. It sets `name` before `docker run` and removes by name on exit (`Makefile:147`–`149`, `:153`, `:156`):

- The run that loses the race fails its `docker run` with `Conflict` and exits, and its trap runs `docker rm -f -v rocketclaw-pg-14`. That removes the **other** run's database, so the other run's tests start failing too.
- When this session retried `make test`, its own container started, and then dozens of tests failed with `connection refused` on the container's port. The likely cause, not proven here, is the same trap from another run removing the container with the shared name. Nothing else in the recipe removes it before the tests end.

Nothing in the output names the cause. The second symptom looks like a flaky database or a broken test, and the failing tests change from run to run.

## Guidance

Until the Makefile is fixed, give a run its own uniquely named database whenever another workspace may run RocketClaw tests at the same time. The recipe skips its own container when `ROCKETCLAW_TEST_DATABASE_URL` is set (`Makefile:154`):

```sh
D="docker --context colima-rocketclaw-linux"
N=rocketclaw-pg-$(basename "$PWD")-$(openssl rand -hex 3)
$D run -d --name "$N" -e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_USER=postgres \
  -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=rocketclaw_test -p 127.0.0.1::5432 postgres:18
P=$($D port "$N" 5432/tcp | head -1); P=${P##*:}
until $D exec "$N" pg_isready -U postgres -d rocketclaw_test >/dev/null 2>&1; do sleep 1; done
make test ROCKETCLAW_TEST_DATABASE_URL="postgres://postgres:postgres@127.0.0.1:$P/rocketclaw_test?sslmode=disable"
$D rm -f -v "$N"
```

This passed `make test` on 2026-11-09 after two failed attempts. The URL's `127.0.0.1` works because the test container runs with `--network host` inside the same VM (`Makefile:15`). Remove the container yourself afterwards: the recipe's cleanup only removes a container it started. If the backend suite hits shared-memory errors on this database, add `--shm-size=1g` (`docs/solutions/best-practices/test-postgres-container-needs-larger-shm.md`).

Before retrying a failed run, `docker --context colima-rocketclaw-linux ps -a --filter name=rocketclaw-pg` shows whether another run holds the name. It may already be gone by the time you look, as it was here.

**The real fix belongs in `internal/rocketclaw/Makefile`. This doc does not make it.** Name the container so concurrent runs cannot share it, or do not name it at all: drop `--name`, keep the ID that `docker run -d` prints, and use that ID for `docker port`, `docker exec` and the cleanup. Then a failed `docker run` leaves no name for the trap to remove. Once that lands, remove the workaround from this doc.

## Why This Matters

`$$` is unique only among processes in one PID namespace, and the recipe assumes it is unique on the Docker daemon. That held when Mac runs used the host's Docker. It stopped holding when they moved into the Linux container, where the PID is the same for every run and the daemon is shared. Running several JJ workspaces in parallel is the normal way agents work in this repo, so two `make test` runs overlapping is common. Without this doc, the first symptom gets a retry and the second sends someone debugging whichever tests happened to fail.

## When to Apply

- Before running `make test` while another workspace may be running RocketClaw tests.
- When a RocketClaw make target fails with `Conflict` on a `rocketclaw-pg-` name, or many unrelated tests fail with `connection refused` on the test database.
- When editing the `coverage-profile` recipe's container setup or cleanup. Any `$$` used to name a resource on the shared daemon has the same problem.

## Examples

Before, with another workspace running `make test`:

```text
docker: Error response from daemon: Conflict. The container name "/rocketclaw-pg-14" is already in use by container "ae22cef5dc3a…".
make[1]: *** [Makefile:146: coverage-profile] Error 125
```

Retried, 77 tests failed, for example:

```text
create test schema: failed to connect to `user=postgres database=rocketclaw_test`: 127.0.0.1:32776 (127.0.0.1): dial error: dial tcp 127.0.0.1:32776: connect: connection refused
```

After, with the uniquely named database above: `coverage budget ok` and exit 0.

## Related

- `docs/solutions/best-practices/coverage-profile-kills-its-own-shell-when-make-is-pid-1.md`: why `make` is PID 1 in the container. Each run gets a fresh PID namespace, which is why the recipe shell's PID repeats; making `make` not PID 1 alone would not make the name unique.
- `docs/solutions/best-practices/test-postgres-container-needs-larger-shm.md`: the shared-memory limit a self-started test database can hit.
- `docs/solutions/best-practices/frontend-rpc-tests-need-setup-in-fresh-jj-workspaces.md`: the rest of the setup the browser tests need, and why `make lint` must not overlap a test run.
- `docs/solutions/best-practices/long-lived-test-database-slows-migrations.md`: why a reused `ROCKETCLAW_TEST_DATABASE_URL` database should be recreated for each run.
