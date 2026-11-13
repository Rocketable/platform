---
title: Frontend RPC Browser Tests Fail Without Their Setup, and That Blocks Verification
date: 2026-10-29
last_updated: 2026-11-12
category: docs/solutions/best-practices/
module: internal/rocketclaw/frontend/rpc
problem_type: developer_experience
component: development_workflow
severity: medium
resolution_type: environment_setup
applies_when:
  - Running RocketClaw tests from a fresh JJ workspace, through make on a Mac or with plain go test on a Linux host
  - internal/rocketclaw/frontend/rpc tests fail inside a bun test step while the rest of the suite passes
  - A worker or reviewer is about to call a frontend/rpc failure an unrelated environment problem
  - About to run make lint or make generate while another test run uses the same workspace
  - Linux-container tests use a long workspace TMPDIR or a Mac-shared temporary directory
symptoms:
  - "bun test output in frontend/rpc says Could not resolve \"@sentry/react\" (web packages not installed)"
  - "error: Cannot find package 'undefined' from '.../internal/rocketclaw/web/src/<name>-transport.test.ts'"
  - "Chromium reports SingletonSocket path too long, RPC sockets fail with bind: invalid argument, or the setgid-preservation test fails"
  - "Tests fail with is explicitly required in go.mod, but not marked as explicit in vendor/modules.txt, or open ../../../vendor/...: no such file or directory"
related_components:
  - internal/rocketclaw/web
  - internal/rocketclaw/Makefile
  - internal/rocketclaw/linux.Containerfile
  - internal/rocketclaw/mcpclient
  - .github/workflows/test.yml
  - cmd/rocketclaw/oai_test.go
tags:
  - frontend-rpc
  - browser-tests
  - playwright
  - bun
  - vendor
  - tmpdir
  - jj-workspace
  - linux-container
---

# Frontend RPC Browser Tests Fail Without Their Setup, and That Blocks Verification

## Context

Nine Go tests in `internal/rocketclaw/frontend/rpc` start a real backend and then run a Bun test file against it through `runWebTest` (`internal/rocketclaw/frontend/rpc/server_test.go:4047`). That helper runs `bun test <file>` in `internal/rocketclaw/web`, with the parent's environment plus the test's URLs (`server_test.go:4050`–`4055`). It skips nothing.

| Test | `runWebTest` call | Opens Chromium |
|---|---|---|
| `TestSessionEntries` | `server_test.go:2108` | yes |
| `TestPagesRealBrowser` | `pages_test.go:251` | yes |
| `TestPublicProgressRealBrowser` | `live_test.go:262` | yes |
| `TestReloadRealBackendBrowser` | `live_test.go:490` | yes |
| `TestRevertRealBackendBrowser` | `live_test.go:582` | yes |
| `TestShadowsRealBackendBrowser` | `shadow_live_test.go:164` | yes |
| `TestRemoteRocketClawRealBrowser` | `remote_test.go:201` | yes |
| `TestSpacePermissionsRealBrowser` | `permissions_live_test.go:165` | yes |
| `TestSidebarHTTPStreamsBeforeGoTailAndCancels` | `sidebar_test.go:265` | no, needs only the web packages |

All paths are under `internal/rocketclaw/frontend/rpc/`. These tests need two things the Go toolchain does not provide:

1. **The web packages in `internal/rocketclaw/web/node_modules`.** A new JJ workspace has none, because `internal/rocketclaw/web/.gitignore:2` ignores them. Without them, Bun cannot load `internal/rocketclaw/web/src/api.ts`, whose first line imports `@sentry/react`.
2. **`ROCKETCLAW_PLAYWRIGHT_MODULE` and `ROCKETCLAW_CHROMIUM`.** `playwright-core` is not in `internal/rocketclaw/web/package.json`. The test files load it from the variable, for example `await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!)` (`internal/rocketclaw/web/src/pages-transport.test.ts:5`). When the variable is unset, Bun prints `Cannot find package 'undefined'`. Nothing in the message names the variable.

On a Mac, `make` provides both. Every target in `internal/rocketclaw` runs again in the `rocketclaw-linux` container (`internal/rocketclaw/Makefile:20`–`24`). That image sets both Playwright variables (`internal/rocketclaw/linux.Containerfile:20`–`21`). `make test` depends on `generate` and `lint` (`Makefile:122`), and both depend on `web-assets`, which runs `bun install --frozen-lockfile` (`Makefile:49`–`56`, `:64`). Plain `go test` of RocketClaw packages does not build on macOS at all (`AGENTS.md:44`).

**Linux execution does not remove socket-path limits or make a Mac bind mount behave like a Linux filesystem.** During PR #1071 verification, a repository-local `TMPDIR` under the long Mac workspace caused RPC socket binds to fail with `invalid argument` and Chromium to abort with `Socket path too long: .../org.chromium.Chromium.../SingletonSocket`. A separate probe inside the Linux image showed `chmod 02640` followed by `stat` returning `0640` on the Mac-shared directory, but `02640` on a Linux-native Docker volume. That explains the failure of `TestSaveOAILoginPreservesFullFileMode`, which asserts the setgid bit (`cmd/rocketclaw/oai_test.go:191`–`199`). These are observations from this setup, not a claim that every Mac share drops setgid. (session history)

The socket helper appends `rpc-<random>/s` beneath `TMPDIR` (`server_test.go:4084`–`4094`), so even a Linux-native volume at the original long pathname did not solve its bind failure. The successful setup needed **both a short container-visible pathname and native storage**. The first fresh-volume retry then failed with `mkdir /w/.tmp/t/go-build...: permission denied`; initializing the volumes for `dev` fixed that without running tests as root. The image deliberately drops privileges to `dev` (`internal/rocketclaw/linux.Containerfile:33`–`43`). (session history)

The Mac launcher forwards make variables given on the command line (`$(MAKEOVERRIDES)`, `Makefile:10`–`14`, `:24`), not arbitrary exported shell variables. Pass temporary paths as make arguments, and make sure those paths exist inside the container.

`vendor/` is a separate trap. `make lint` and `make generate` depend on `vendor` (`Makefile:55`, `:64`), which runs `go mod tidy` and `go mod vendor` (`Makefile:45`–`47`) and so deletes and rewrites `vendor/`. Every container bind-mounts your home folder (`Makefile:21`), so two make runs in one workspace share the same `vendor/`. Some tests also run the Go toolchain mid-run: `buildStdioHelper` calls `go build` (`internal/rocketclaw/mcpclient/registry_test.go:351`, `:354`). A test run that started cleanly can break when another run rewrites `vendor/` under it. Root `make test` orders its own steps for this reason (`Makefile:1`–`5` at the repository root).

On the session write replies work (PR #765), three workers in a row called these failures "an environment problem, not this change", and one edit inside `TestSessionEntries` was compiled but never ran.

## Guidance

- **On a Mac, run RocketClaw tests through `make`.** It provides the Web dependencies, but custom temporary paths still need the setup below. If another workspace may run RocketClaw tests at the same time, give the run its own database as `concurrent-make-test-runs-share-one-test-database-container-name.md` shows: pass `ROCKETCLAW_TEST_DATABASE_URL=...` as a make argument, not as an exported variable.
- **Keep temporary storage short and Linux-native without leaving repository `.tmp/`.** In the verified setup, an ignored launcher wrapper mounted the workspace at `/w`, a per-run native volume at `/w/.tmp/t` for `TMPDIR`, and another at `/w/.tmp/run/publish-sandbox` for sandbox fixtures. It translated the working directory and forwarded `TMPDIR`, `SANDBOX_TEST_DIR` and `GOCACHE` make assignments from the host prefix to `/w`. It also replaced the forwarded `TMPDIR` value with `/w/.tmp/t`: setting Docker's environment alone would not override an explicit make assignment. Keep the existing privileged/cgroup setup, Docker socket, home-cache volume and normal image entrypoint; do not replace these with a bare container or run the tests as root. These mount paths are inside the repository as seen by the container; the volumes themselves are stored by Docker, not in the Mac bind mount. (session history)
- **Initialize fresh native volumes for the test user.** A one-off `--entrypoint chown` run set `dev:dev` ownership of the two mounted volume roots. Only that initialization bypassed the entrypoint; the checks used its normal non-root execution. Use run-owned volume names, and retain or remove only your own volumes. The native sandbox requirement is also stated in `internal/rocketclaw/Makefile:71`–`74`. (session history)
- **Keep the durable launcher fix in one place.** The Mac launcher in `internal/rocketclaw/Makefile` should own short repository-local native temp mounts and path translation, rather than requiring a new wrapper for each run. This capture documents the verified workaround; it does not change that launcher.
- **On a Mac, a focused run may skip `web-assets`.** `coverage-profile` and `check-coverage` do not depend on it (`Makefile:145`, `:155`). In a fresh workspace, run `make -C internal/rocketclaw web-assets` once before a focused run, such as the runner in `coverage-profile-kills-its-own-shell-when-make-is-pid-1.md`. This is inferred from the Makefile and has not been run.
- **On a Linux host with plain `go test ./...`, do what CI does.** Run `bun install --frozen-lockfile` in `internal/rocketclaw/web` (`.github/workflows/test.yml:120`). Install `playwright-core` and Chromium and export both variables, as the "Install Playwright and Chromium for browser tests" step does (`test.yml:130`–`138`). Plain `go test` also needs `ROCKETCLAW_TEST_DATABASE_URL` (`internal/rocketclaw/backend/harnessbridgetest/database.go:18`–`21`).
- **Never run `make lint` or `make generate` alongside another test run in the same workspace.** That means no parallel tool calls, no background jobs, and no worker running one while the coordinator runs the other. `make test` already runs `generate` and `lint` before its tests.
- **Treat these failures as blocking.** A `frontend/rpc` failure from missing setup means the browser tests did not run. Fix the setup and rerun before you report the change as verified. Put this rule in worker instructions.

Read the errors this way:

| Error | Cause | Can still happen when |
|---|---|---|
| `Could not resolve "@sentry/react"` (or another web package) in a `bun test` step | No `internal/rocketclaw/web/node_modules` | A Linux host runs plain `go test`; a focused Mac run skips `web-assets` in a fresh workspace (inferred, not run) |
| `Cannot find package 'undefined'` in a `bun test` step | `ROCKETCLAW_PLAYWRIGHT_MODULE` is unset where the tests ran | A Linux host runs plain `go test` without CI's Playwright step |
| `not marked as explicit in vendor/modules.txt`, or `open ../../../vendor/...: no such file or directory` | `make lint` or `make generate` rewrote `vendor/` during the test run | Any platform, when those targets overlap another test run in the same workspace. Wait for it to finish and rerun |
| Chromium `SingletonSocket` path too long, or RPC `listen unix ...: bind: invalid argument` | Temporary socket pathname is too long | Linux containers too, when `TMPDIR` retains the long Mac workspace prefix |
| Setgid assertion gets `0640` instead of `02640` | In the observed setup, the Mac-shared temp filesystem dropped setgid | A Linux process still writes through a Mac bind mount; confirm with a chmod/stat probe before blaming credential-save code |
| `mkdir /w/.tmp/t/go-build...: permission denied` | Fresh native volume is not writable by `dev` | Initialize the run's volume ownership, then keep the normal non-root entrypoint |

The `vendor/modules.txt` message points at `go.mod`, but nothing is wrong with it. Do not "fix" `go.mod`.

## Why This Matters

Dependency and vendor errors can hide their setup cause; socket and permission failures can look like application regressions even inside a Linux container. "Environment, not this change" is not a verification result. Leaving the setup broken leaves the browser or permission checks unrun, and weakening those checks would hide the contract they protect.

## When to Apply

- Before the first test run in a new JJ workspace, especially for a focused run or on a Linux host.
- When a worker report blames "the environment", "local bun setup" or "web packages not installed" for `frontend/rpc` failures.
- When planning parallel verification in one workspace.
- When adding a test that calls `runWebTest`: update the table above.
- When moving temporary files under repository `.tmp/`, especially in a deeply nested workspace or a custom Linux verification launcher.

## Examples

Before, with `ROCKETCLAW_PLAYWRIGHT_MODULE` unset:

```text
--- FAIL: TestPublicProgressRealBrowser (1.59s)
    live_test.go:<line>: ... bun test v1.4.4
        src/public-progress-transport.test.ts:
        error: Cannot find package 'undefined' from '.../internal/rocketclaw/web/src/public-progress-transport.test.ts'
```

Before, with `go test` started alongside `make lint`:

```text
open ../../../vendor/golang.org/x/sync/errgroup/errgroup.go: no such file or directory
FAIL	github.com/Rocketable/platform/internal/rocketclaw/mcpclient	0.683s
```

Installing the web packages and setting the variables resolved the earlier dependency failures. Temporary storage is a separate prerequisite:

For the temporary-storage failure, the same-image probe separated the mount behavior from application code: (session history)

| Temporary file location inside the container | Requested mode | Observed mode |
|---|---|---|
| `/w/.tmp/run/tmp` on the Mac bind mount | `02640` | `0640` |
| `/w/.tmp/t` on the native volume | `02640` | `02640` |

After the short-path mapping and `dev` ownership initialization, repository-root `make lint` and `make test` both exited zero, including the 91.8% coverage requirement. An additional `check-code-metrics` run on the exact feature tree also exited zero after an unrelated lint-inserted blank line was removed. No feature logic or test expectation was changed to make these environment failures pass. (session history)

## Related

- `docs/solutions/best-practices/concurrent-make-test-runs-share-one-test-database-container-name.md`: on a Mac, how to give `make test` its own database.
- `docs/solutions/best-practices/coverage-profile-kills-its-own-shell-when-make-is-pid-1.md`: how to run a focused test subset on a Mac.
- `docs/solutions/best-practices/test-postgres-container-needs-larger-shm.md`: why a self-started test database needs `--shm-size`.
- `docs/solutions/best-practices/long-lived-test-database-slows-migrations.md`: why a reused test database should be recreated for each run.
- `docs/solutions/best-practices/colima-bind-mounts-fill-vm-disk.md`: how to find what fills a Colima VM disk.
- `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`: these tests serve the prebuilt `dist` bundle, so a stale build is a separate trap.
- `docs/solutions/best-practices/crash-dumps-in-web-change-the-built-stylesheet.md`: after a Chromium crash, preserve its dump under `.tmp/` before rebuilding; a dump left in the Web directory can affect the bundle.
