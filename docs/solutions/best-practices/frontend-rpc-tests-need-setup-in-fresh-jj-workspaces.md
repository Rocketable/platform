---
title: Frontend RPC Tests Need Setup in Every Fresh JJ Workspace
date: 2026-10-29
last_updated: 2026-10-30
category: docs/solutions/best-practices/
module: internal/rocketclaw/frontend/rpc
problem_type: developer_experience
component: development_workflow
severity: medium
resolution_type: environment_setup
applies_when:
  - Running go test ./... or make test from a fresh JJ workspace under .tmp/workspaces/
  - Setting TMPDIR inside .tmp/ as the repository requires, from a workspace checkout on macOS
  - internal/rocketclaw/frontend/rpc tests fail inside a bun test step, or with bind invalid argument, while the rest of the suite passes
  - A worker or reviewer is about to call a frontend/rpc failure an unrelated environment problem
  - Many backend and cmd tests fail at once with connection refused on the ROCKETCLAW_TEST_DATABASE_URL port
  - Starting a Docker PostgreSQL for ROCKETCLAW_TEST_DATABASE_URL that several full runs in one session will share
  - About to run make lint and go test at the same time, in parallel tool calls or in separate workers
symptoms:
  - "bun test output in frontend/rpc says Could not resolve \"@sentry/react\" (web packages not installed)"
  - "After installing them, the same tests fail with error: Cannot find package 'undefined' from '.../internal/rocketclaw/web/src/<name>-transport.test.ts'"
  - TestSessionEntries, TestPagesRealBrowser, TestPublicProgressRealBrowser and the other real-browser tests fail; backend packages pass
  - "Dozens of tests across backend, frontend/rpc and cmd/rocketclaw fail with dial tcp 127.0.0.1:<port>: connect: connection refused, some in 0.00s"
  - "On macOS, about 22 frontend/rpc tests, such as TestHTTPStreams, TestPages and TestSessionReads, fail with listen unix <TMPDIR>/rpc-NNNNNNNNNN/s: bind: invalid argument"
  - "Partway through a session, backend tests fail with SQLSTATE 53100 No space left on device, then the test container is gone and every database test reports connection refused"
  - "Tests fail with is explicitly required in go.mod, but not marked as explicit in vendor/modules.txt, or open ../../../vendor/...: no such file or directory"
related_components:
  - internal/rocketclaw/web
  - internal/rocketclaw/Makefile
  - internal/rocketclaw/frontend/rpc/README.md
  - internal/rocketclaw/web/README.md
  - internal/rocketclaw/backend/harnessbridgetest
  - internal/rocketclaw/mcpclient
  - .github/workflows/test.yml
tags:
  - browser-tests
  - playwright
  - chromium
  - bun
  - tmpdir
  - unix-socket
  - macos
  - path-length
  - jj-workspace
  - frontend-rpc
  - make-test
  - test-environment
  - test-database
  - postgresql
  - tmpfs
  - vendor
  - make-lint
---

# Frontend RPC Tests Need Setup in Every Fresh JJ Workspace

## Context

A plain `go test ./...`, which `AGENTS.md` requires, runs the `internal/rocketclaw/frontend/rpc` tests. In a fresh JJ workspace under `.tmp/workspaces/`, they need three things that the Go toolchain does not provide. Missing any one of them fails tests in a package the change probably did not touch. The full suite also needs a test database that lasts through every full run of the session, and it must not overlap with `make lint`. Both are covered below.

### Web packages and Playwright

Seven Go tests in `internal/rocketclaw/frontend/rpc` start a real backend and then run a Bun test file against it through `runWebTest` (`internal/rocketclaw/frontend/rpc/server_test.go:4018`). That helper runs `bun test <file>` in `internal/rocketclaw/web` with the parent's environment plus the test's URLs (`server_test.go:4024`). It does not skip anything. Six of the seven Bun files open real Chromium. They are `TestSessionEntries`, `TestPagesRealBrowser`, `TestPublicProgressRealBrowser`, `TestReloadRealBackendBrowser`, `TestRevertRealBackendBrowser` and `TestShadowsRealBackendBrowser`. The seventh, `TestSidebarHTTPStreamsBeforeGoTailAndCancels`, needs only the web packages.

1. **The web packages in `internal/rocketclaw/web/node_modules`.** A new JJ workspace has none, because `internal/rocketclaw/web/.gitignore` ignores `node_modules` and each workspace is its own checkout. Without them, Bun cannot load `internal/rocketclaw/web/src/api.ts`, which starts with `import { captureException } from "@sentry/react"`. The tests fail with `Could not resolve "@sentry/react"`.
2. **`ROCKETCLAW_PLAYWRIGHT_MODULE` and `ROCKETCLAW_CHROMIUM`.** `playwright-core` is not in `internal/rocketclaw/web/package.json`. The test files load it from the path in the variable, for example `await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!)` (`internal/rocketclaw/web/src/pages-transport.test.ts:5`). When the variable is unset, Bun is asked to import `undefined` and prints `Cannot find package 'undefined'`. Neither the error nor the stack names the variable. Running `await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!)` with the variable unset reproduces the exact message on Bun 1.4.4. The standalone `src/*.browser.test.ts` files do the same: run without the variables, they fail with this message rather than skip.

CI does both steps itself. `.github/workflows/test.yml:124` runs `bun install --frozen-lockfile` in `internal/rocketclaw/web`. The "Install Playwright and Chromium for browser tests" step (`test.yml:133`–`140`) installs `playwright-core@1.63.0` and Chromium and exports both variables. Locally, `make test` covers only the first step: root `make test` runs `make generate lint` in `internal/rocketclaw`, and both targets depend on `web-assets`, which runs `bun install --frozen-lockfile` (`internal/rocketclaw/Makefile:15`–`24`). Nothing local sets the Playwright variables. Plain `go test ./...` does neither step.

The component READMEs list the variables (`internal/rocketclaw/web/README.md` under "Local checks" and again next to the saved-list browser tests, `internal/rocketclaw/frontend/rpc/README.md` under "Verify without a live Slack runtime"). Both READMEs link to this doc.

While building session write replies (PR #765), this cost three misdiagnoses in one run. The U1 worker saw seven `@sentry/react` failures and called them "an environment problem, not this change". The U3 worker said the same. After the orchestrator installed the web packages, the U7 worker saw six `Cannot find package 'undefined'` failures and blamed "the local `bun` setup". U7 had edited code inside `TestSessionEntries`, so that edit was compiled but never ran. The orchestrator's own full `go test ./...` then failed the same way. It took a grep for `ROCKETCLAW_PLAYWRIGHT_MODULE` and a read of the CI step to make `frontend/rpc` pass (44.7 s). After that, `go test ./...` exited 0.

### A short TMPDIR on macOS

The repository requires every temporary file to live under `<repo-root>/.tmp/`, so tests run with `TMPDIR` set there. Agent runs work in JJ workspaces nested inside that same directory, at `<main-checkout>/.tmp/workspaces/<date>-<slug>/`. Setting `TMPDIR` to that workspace's own `.tmp/` follows the rule, but on macOS it breaks the `frontend/rpc` tests.

Many of those tests start real gRPC servers on Unix sockets. Each one gets its socket path from `testSocketPath` (`internal/rocketclaw/frontend/rpc/server_test.go:4055`):

```go
dir, err := os.MkdirTemp(os.Getenv("TMPDIR"), "rpc-")
...
socketPath, err := filepath.Abs(filepath.Join(dir, "s"))
```

`make test` reaches these tests through the `coverage-profile` target in `internal/rocketclaw/Makefile`, which runs `go test ... ./...`.

On Darwin, a Unix socket path must fit in a 104-byte buffer (`sun_path`) that also holds a terminating NUL. Go's `syscall` package returns `EINVAL` before calling `bind` when the path is 104 bytes or longer (`SockaddrUnix.sockaddr` in Go's `syscall_bsd.go`), so the longest usable path is 103 bytes. The test sees this as `bind: invalid argument`, which looks nothing like a path-length error.

`os.MkdirTemp` names the directory `rpc-` plus a random unsigned 32-bit number in decimal. That number is usually 10 digits long. So the socket path is `len(TMPDIR) + 17` bytes at most (`/rpc-` is 5, the number is up to 10, `/s` is 2). Any `TMPDIR` longer than 86 bytes can fail.

A workspace root is already long before its own `.tmp/` is added. `$PWD/.tmp` adds 5 bytes to it, and `$PWD/.tmp/test-tmp` adds 14, so the second only fits when the workspace root is 72 bytes or shorter. On the search-agent-filter run (2026-10-26), the workspace root was `/Users/ucirello/Projects/platform/.tmp/workspaces/20261026-search-agent-filter-any`, which is 82 bytes:

| `TMPDIR` | Length | Longest socket path | Result |
|---|---|---|---|
| workspace `.tmp` | 87 | 104 | Fails whenever the random number has 10 digits, which is about 77% of the time |
| workspace `.tmp/test-tmp` | 96 | 113 | Always fails |
| main checkout `.tmp` | 38 | 55 | Always fits |

The workspace this doc was merged in, `.../20261029-expanding-subsession-management-support`, has a 98-byte root. Its `.tmp` alone is 103 bytes, so no directory under the workspace's `.tmp/` can fit.

During the search-agent-filter run, root `make test` failed 22 tests, all in `frontend/rpc`, with 52 `bind: invalid argument` errors. The rerun used a short `TMPDIR` that was still inside the repository's `.tmp/`, and the socket failures went away. The same limit had already come up in `docs/plans/2026-09-24-1402-feat-settle-snooze-plan.md`, which used `.tmp/st` at the repository root for a socket test.

### A test database that lasts the whole session

Plain `go test ./...` needs `ROCKETCLAW_TEST_DATABASE_URL`. Without it, `IsolatedTestDatabaseURL` returns `ROCKETCLAW_TEST_DATABASE_URL is required` (`internal/rocketclaw/backend/harnessbridgetest/database.go:18`–`21`). `make test` starts its own container only when the variable is unset (`internal/rocketclaw/Makefile:82`). If `.tmp/test-env.sh` exports the variable, `make test` uses your container too.

Three documented limits stack up on a container that one session reuses:

- Docker's default 64 MB `/dev/shm` runs out under parallel tests (`test-postgres-container-needs-larger-shm.md`).
- Every test makes a schema with `CREATE SCHEMA` (`database.go:38`), and nothing drops it (`long-lived-test-database-slows-migrations.md`). One full Go run of `./internal/rocketclaw/... ./cmd/rocketclaw/...` left about 1.2 GB of schemas. After that run and a `make test`, the container held 2.4 GB.
- The `postgres:18` image declares `/var/lib/postgresql` as an anonymous volume, so that data lands on the Colima VM's 19 GB root disk. Other sessions' containers share that disk (`colima-bind-mounts-fill-vm-disk.md`).

On the easy-mode agents run (2026-10-30), the container was started with `docker run ... postgres:18` and no other options. The first full run passed. During a worker's later runs, `/dev/shm` reached 96% and PostgreSQL failed with SQLSTATE 53100 `No space left on device`. The container was recreated with `--shm-size=1g` and `-c max_connections=300`. A later worker then reported that "the disk filled up". The container was gone, and every database package failed with `connection refused`. The Mac had 90 GiB free. Nobody checked the VM disk at the time, so treat the VM-disk explanation as the likely cause, not a measured one. Two changes stopped the failures: keeping the data directory on a 3 GB `--tmpfs`, and recreating the container before each full run. After that, every full run that did not overlap with `make lint` (next section) passed, and so did `make test`. A 3 GB tmpfs holds two full runs, not three.

### `make lint` rewrites what `go test` compiles

Root `make lint` runs `make lint` in `internal/rocketclaw`. That target depends on `web-assets` (`bun install` and `bun run build`) and on `vendor` (`go mod tidy` and `go mod vendor`) (`internal/rocketclaw/Makefile:11`–`16`, `24`). `go mod vendor` deletes and rewrites `vendor/`. Some tests also run the Go toolchain while they run: `buildStdioHelper` calls `go build` (`internal/rocketclaw/mcpclient/registry_test.go:371`). So a test package that started cleanly can still break mid-run.

On the easy-mode run, `go test` and `make lint` were started at the same time. The tests failed in `frontend/rpc` and `mcpclient` with `is explicitly required in go.mod, but not marked as explicit in vendor/modules.txt` and `open ../../../vendor/golang.org/x/sync/errgroup/errgroup.go: no such file or directory`. The message points at `go.mod`, but nothing was wrong with it. `make lint` finished with 0 issues and changed no tracked files, and a rerun of `go test` on its own passed. The root `Makefile` already orders its own `make test` this way. Its first comment says "generate rewrites vendor/ and the mocks, so it finishes before" the tests start. Nothing stops an agent from running the two commands side by side, though.

## Guidance

These failures are a setup gap, and they block verification. They are not an unrelated failure to report and move past. Set up once per workspace, before the first full test run:

```sh
# From the workspace root. The main checkout contains .tmp/workspaces/.
main=${PWD%%/.tmp/workspaces/*}

# 1. A short TMPDIR under the main checkout's .tmp/, not the workspace's own .tmp/,
#    so socket paths stay under the macOS limit.
mkdir -p .tmp "$main/.tmp/t"
export TMPDIR="$main/.tmp/t"

# 2. Web packages for this workspace (root make test does this too; go test ./... and make test in internal/rocketclaw/web do not).
(cd internal/rocketclaw/web && bun install --frozen-lockfile)

# 3. playwright-core and Chromium, shared by every workspace, at the version CI pins
#    in .github/workflows/test.yml (1.63.0 as of this writing).
dir="$main/.tmp/playwright-core"
[ -d "$dir/node_modules/playwright-core" ] || (mkdir -p "$dir" && cd "$dir" && bun add playwright-core@1.63.0)
(cd "$dir" && ./node_modules/.bin/playwright-core install --no-shell chromium)

# 4. A PostgreSQL container for this workspace. Pick a port no other session uses.
pg="${PWD##*/}-pg" port=55432

# 5. Keep everything in one sourced file. pg_fresh recreates the container, with a
#    large /dev/shm and its data in memory, and waits for its real server over TCP.
cat > .tmp/test-env.sh <<EOF
export TMPDIR="$main/.tmp/t"
export ROCKETCLAW_PLAYWRIGHT_MODULE="$dir/node_modules/playwright-core"
export ROCKETCLAW_CHROMIUM="$(cd "$dir" && bun -e 'console.log(require("playwright-core").chromium.executablePath())')"
export ROCKETCLAW_TEST_DATABASE_URL="postgres://pg:pg@127.0.0.1:$port/pg?sslmode=disable"
pg_fresh() {
  docker rm -f -v $pg >/dev/null 2>&1
  docker run -d --name $pg --shm-size=1g --tmpfs /var/lib/postgresql:rw,size=3g -e POSTGRES_USER=pg -e POSTGRES_PASSWORD=pg -e POSTGRES_DB=pg -p 127.0.0.1:$port:5432 postgres:18 -c max_connections=300 -c fsync=off >/dev/null || return
  until docker exec $pg pg_isready -U pg -h 127.0.0.1 >/dev/null 2>&1; do sleep 1; done
}
EOF
. .tmp/test-env.sh
pg_fresh
go test ./internal/rocketclaw/frontend/rpc/ -count=1
```

Source `.tmp/test-env.sh` and run `pg_fresh` before every full `go test ./...` and every `make test`. Plain `go test ./...` and `make test` in `internal/rocketclaw/web` start no database, so they need the URL from this file. Only root `make test` starts its own PostgreSQL, and only when the variable is unset (`internal/rocketclaw/Makefile:82`–`101`); with the URL exported, it uses this container instead. A full run leaves about 1.2 GB of schemas, and the 3 GB tmpfs holds two runs, so do not skip `pg_fresh`. `docker rm -f -v` also removes the image's anonymous volume, which a plain `docker rm -f` leaves on the VM disk. When a coordinator hands work to subagents, put `. .tmp/test-env.sh && pg_fresh` in the worker rules. Each worker starts a fresh shell and does not inherit the variables. Parallel workers sharing one container must not run `pg_fresh` while another worker's tests are running.

The container is named for this workspace on purpose. Several agent sessions on one machine run their own test containers side by side, and a URL that points at a container this workspace did not start can stop answering partway through a run. On the remote-RocketClaws run (2026-10-30), the container behind `127.0.0.1:32832` disappeared between two full runs. `docker ps -a` no longer listed it, while other sessions' containers kept running. Root `make test` then failed `internal/rocketclaw/backend`, and `go test ./...` failed many `cmd/rocketclaw` tests, all with `connection refused`, which read like a regression. Starting a workspace-owned container on the same port, with `--shm-size=1g`, made both `make test` and `go test ./...` pass again. Pick a free port, and remove the container with `docker rm -f -v "${PWD##*/}-pg"` when the workspace is done.

Run `make lint` and `go test` one after the other, never at the same time. That means no parallel tool calls, no background jobs, and no worker running one while the coordinator runs the other. `make test` already runs lint and generate before its own tests.

The tmpfs uses the Colima VM's memory (12 GiB on this Mac as of this writing), not its disk. Recreating the container is what frees it. `-c fsync=off` is safe because the data is thrown away anyway. `-c max_connections=300` came from the same session, but no failure showed that it was needed.

`$main/.tmp/t` stays inside `<repo-root>/.tmp/` as the temporary-directory rule requires. Before a long run, check the budget: `printf %s "$TMPDIR" | wc -c` must print 86 or less. Do not use `$PWD/.tmp` or `$PWD/.tmp/test-tmp` from a workspace, and do not fall back to `/tmp`, which breaks the repository's temporary-directory rule.

Read these messages this way:

| Error | Missing piece |
|---|---|
| `Could not resolve "@sentry/react"` (or any other web package) in a `bun test` step | `bun install --frozen-lockfile` in `internal/rocketclaw/web` |
| `Cannot find package 'undefined'` in a `bun test` step | `ROCKETCLAW_PLAYWRIGHT_MODULE` unset in the environment `go test` ran with |
| `listen unix <TMPDIR>/rpc-NNNNNNNNNN/s: bind: invalid argument` | `TMPDIR` longer than 86 bytes; count it before you debug the test |
| SQLSTATE 53100 `No space left on device` from PostgreSQL | The container's `/dev/shm` is too small; recreate it with `pg_fresh` |
| `dial tcp 127.0.0.1:<port>: connect: connection refused` across many database packages | The container behind `ROCKETCLAW_TEST_DATABASE_URL` is gone or full. Check `docker ps -a`. If the URL pointed at a container this workspace did not start, switch to the workspace-owned one from the setup above. If it was yours, the VM disk or the tmpfs probably filled up: check the VM with `cd ~ && colima ssh -- df -h /`, and if the container still runs, `docker exec "${PWD##*/}-pg" df -h /var/lib/postgresql`. Then run `pg_fresh` |
| `not marked as explicit in vendor/modules.txt`, or `open ../../../vendor/...: no such file or directory` | `make lint` (or `make generate`) was rewriting `vendor/` while the tests ran; wait for it to finish and rerun |

## Why This Matters

None of these messages points at its cause. All of them show up in a package the change probably did not touch, and every agent run starts in a fresh workspace, so the failures come back every time. The socket error can also look random, because the path length depends on the random directory name, and that pushes an agent toward calling the suite flaky. The comment in `testSocketPath` says `TMPDIR` should be short, but it does not say what "short" means.

"Environment, not this change" is literally true, which is why it keeps passing review. But it leaves tests unrun: a change that touches `TestSessionEntries` or the Web transport code gets no browser check at all, and the required `go test ./...` and `make test` are not actually green. Longer workspace slugs make the socket limit worse.

The database and `vendor/` failures are just as misleading. The database errors show up only after several runs, so the first green run seems to prove the setup works. The `vendor/modules.txt` message points at `go.mod`, which invites a `go mod` "fix" for a problem that is already gone by the time anyone looks. Both get worse in exactly the sessions that verify most: many full runs, and workers running checks in parallel.

## When to Apply

- Right after `ce-worktree` or any new JJ workspace, before the first `go test ./...` or `make test`.
- Whenever a worker report blames "the environment", "local bun setup" or "web packages not installed" for `frontend/rpc` failures.
- When writing worker rules, a verification contract or a plan step that runs Go tests or sets `TMPDIR` in an agent workspace.
- When adding a new test that binds a Unix socket under `TMPDIR`. Reuse `testSocketPath` and keep the added path short.
- Before each full `go test ./...` or `make test` in a long session: run `pg_fresh` first.
- When planning parallel verification: `make lint`, `make generate` and `make test` each rewrite `vendor/` and the web build, so none of them may overlap with another `go test`.

## Examples

Before. A fresh workspace with `ROCKETCLAW_TEST_DATABASE_URL` set and nothing else:

```text
--- FAIL: TestPublicProgressRealBrowser (1.59s)
    live_test.go:249: ... bun test v1.4.4
        src/public-progress-transport.test.ts:
        error: Cannot find package 'undefined' from '.../internal/rocketclaw/web/src/public-progress-transport.test.ts'
```

Before, on macOS, with `TMPDIR` set to the workspace's `.tmp/` (87 bytes on the search-agent-filter run): root `make test` fails 22 `frontend/rpc` tests with `listen unix .../.tmp/rpc-NNNNNNNNNN/s: bind: invalid argument`.

After `bun install` and sourcing the variables:

```text
ok  	github.com/Rocketable/platform/internal/rocketclaw/frontend/rpc	44.684s
```

Before, on the easy-mode run, with `go test` started alongside `make lint`:

```text
open ../../../vendor/golang.org/x/sync/errgroup/errgroup.go: no such file or directory
FAIL	github.com/Rocketable/platform/internal/rocketclaw/mcpclient	0.683s
```

After one full run and one `make test` on a `pg_fresh` container:

```text
tmpfs           3.0G  2.4G  688M  78% /var/lib/postgresql
shm             1.0G   46M  979M   5% /dev/shm
```

## Related

- `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`: these tests serve the prebuilt `dist` bundle, so a stale build is a separate trap.
- `docs/solutions/best-practices/test-postgres-container-needs-larger-shm.md`: why the container needs `--shm-size`.
- `docs/solutions/best-practices/long-lived-test-database-slows-migrations.md`: leftover schemas also slow every migration on a reused database.
- `docs/solutions/best-practices/colima-bind-mounts-fill-vm-disk.md`: how to find what fills the Colima VM disk.
- `docs/plans/2026-09-24-1402-feat-settle-snooze-plan.md`: an earlier run that hit the same socket-path limit and used `.tmp/st`.
