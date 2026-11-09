---
title: Frontend RPC Browser Tests Fail Without Their Setup, and That Blocks Verification
date: 2026-10-29
last_updated: 2026-11-09
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
  - Writing worker rules or a verification step that runs RocketClaw tests
symptoms:
  - "bun test output in frontend/rpc says Could not resolve \"@sentry/react\" (web packages not installed)"
  - "error: Cannot find package 'undefined' from '.../internal/rocketclaw/web/src/<name>-transport.test.ts'"
  - TestSessionEntries, TestPagesRealBrowser and the other real-browser tests fail while backend packages pass
  - "Tests fail with is explicitly required in go.mod, but not marked as explicit in vendor/modules.txt, or open ../../../vendor/...: no such file or directory"
related_components:
  - internal/rocketclaw/web
  - internal/rocketclaw/Makefile
  - internal/rocketclaw/linux.Containerfile
  - internal/rocketclaw/mcpclient
  - .github/workflows/test.yml
tags:
  - frontend-rpc
  - browser-tests
  - playwright
  - bun
  - vendor
  - make-lint
  - jj-workspace
  - linux-container
---

# Frontend RPC Browser Tests Fail Without Their Setup, and That Blocks Verification

## Context

Nine Go tests in `internal/rocketclaw/frontend/rpc` start a real backend and then run a Bun test file against it through `runWebTest` (`internal/rocketclaw/frontend/rpc/server_test.go:4000`). That helper runs `bun test <file>` in `internal/rocketclaw/web`, with the parent's environment plus the test's URLs (`server_test.go:4006`). It skips nothing.

| Test | `runWebTest` call | Opens Chromium |
|---|---|---|
| `TestSessionEntries` | `server_test.go:2106` | yes |
| `TestPagesRealBrowser` | `pages_test.go:248` | yes |
| `TestPublicProgressRealBrowser` | `live_test.go:261` | yes |
| `TestReloadRealBackendBrowser` | `live_test.go:487` | yes |
| `TestRevertRealBackendBrowser` | `live_test.go:577` | yes |
| `TestShadowsRealBackendBrowser` | `shadow_live_test.go:161` | yes |
| `TestRemoteRocketClawRealBrowser` | `remote_test.go:197` | yes |
| `TestSpacePermissionsRealBrowser` | `permissions_live_test.go:162` | yes |
| `TestSidebarHTTPStreamsBeforeGoTailAndCancels` | `sidebar_test.go:264` | no, needs only the web packages |

All paths are under `internal/rocketclaw/frontend/rpc/`. These tests need two things the Go toolchain does not provide:

1. **The web packages in `internal/rocketclaw/web/node_modules`.** A new JJ workspace has none, because `internal/rocketclaw/web/.gitignore:2` ignores them. Without them, Bun cannot load `internal/rocketclaw/web/src/api.ts`, whose first line imports `@sentry/react`.
2. **`ROCKETCLAW_PLAYWRIGHT_MODULE` and `ROCKETCLAW_CHROMIUM`.** `playwright-core` is not in `internal/rocketclaw/web/package.json`. The test files load it from the variable, for example `await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!)` (`internal/rocketclaw/web/src/pages-transport.test.ts:5`). When the variable is unset, Bun prints `Cannot find package 'undefined'`. Nothing in the message names the variable.

On a Mac, `make` now provides both. Every target in `internal/rocketclaw` runs again in the `rocketclaw-linux` container (`internal/rocketclaw/Makefile:14`–`18`). That image sets both Playwright variables (`internal/rocketclaw/linux.Containerfile:20`–`21`). `make test` depends on `generate` and `lint` (`Makefile:113`), and both depend on `web-assets`, which runs `bun install --frozen-lockfile` (`Makefile:43`–`44`, `49`, `58`). Plain `go test` of RocketClaw packages does not build on macOS at all (`AGENTS.md`). The macOS Unix-socket path limit that this doc used to warn about no longer applies, because these tests now run on Linux.

The container gets only the make variables given on the command line (`$(MAKEOVERRIDES)`, `Makefile:18`). Variables exported in your shell do not reach it.

`vendor/` is a separate trap. `make lint` and `make generate` depend on `vendor` (`Makefile:49`, `58`), which runs `go mod tidy` and `go mod vendor` (`Makefile:39`–`41`) and so deletes and rewrites `vendor/`. Every container bind-mounts your home folder (`Makefile:15`), so two make runs in one workspace share the same `vendor/`. Some tests also run the Go toolchain mid-run: `buildStdioHelper` calls `go build` (`internal/rocketclaw/mcpclient/registry_test.go:351`, `:354`). A test run that started cleanly can break when another run rewrites `vendor/` under it. Root `make test` orders its own steps for this reason (`Makefile:1`–`2` at the repository root).

On the session write replies work (PR #765), three workers in a row called these failures "an environment problem, not this change", and one edit inside `TestSessionEntries` was compiled but never ran.

## Guidance

- **On a Mac, run RocketClaw tests through `make`.** `make test` needs no per-workspace setup. If another workspace may run RocketClaw tests at the same time, give the run its own database as `concurrent-make-test-runs-share-one-test-database-container-name.md` shows: pass `ROCKETCLAW_TEST_DATABASE_URL=...` as a make argument, not as an exported variable.
- **On a Mac, a focused run may skip `web-assets`.** `coverage-profile` and `check-coverage` do not depend on it (`Makefile:136`, `145`). In a fresh workspace, run `make -C internal/rocketclaw web-assets` once before a focused run, such as the runner in `coverage-profile-kills-its-own-shell-when-make-is-pid-1.md`. This is inferred from the Makefile and has not been run.
- **On a Linux host with plain `go test ./...`, do what CI does.** Run `bun install --frozen-lockfile` in `internal/rocketclaw/web` (`.github/workflows/test.yml:120`). Install `playwright-core` and Chromium and export both variables, as the "Install Playwright and Chromium for browser tests" step does (`test.yml:130`–`138`). Plain `go test` also needs `ROCKETCLAW_TEST_DATABASE_URL` (`internal/rocketclaw/backend/harnessbridgetest/database.go:18`–`21`).
- **Never run `make lint` or `make generate` alongside another test run in the same workspace.** That means no parallel tool calls, no background jobs, and no worker running one while the coordinator runs the other. `make test` already runs `generate` and `lint` before its tests.
- **Treat these failures as blocking.** A `frontend/rpc` failure from missing setup means the browser tests did not run. Fix the setup and rerun before you report the change as verified. Put this rule in worker instructions.

Read the errors this way:

| Error | Cause | Can still happen when |
|---|---|---|
| `Could not resolve "@sentry/react"` (or another web package) in a `bun test` step | No `internal/rocketclaw/web/node_modules` | A Linux host runs plain `go test`; a focused Mac run skips `web-assets` in a fresh workspace (inferred, not run) |
| `Cannot find package 'undefined'` in a `bun test` step | `ROCKETCLAW_PLAYWRIGHT_MODULE` is unset where the tests ran | A Linux host runs plain `go test` without CI's Playwright step |
| `not marked as explicit in vendor/modules.txt`, or `open ../../../vendor/...: no such file or directory` | `make lint` or `make generate` rewrote `vendor/` during the test run | Any platform, when those targets overlap another test run in the same workspace. Wait for it to finish and rerun |

The `vendor/modules.txt` message points at `go.mod`, but nothing is wrong with it. Do not "fix" `go.mod`.

## Why This Matters

None of these messages names its cause, and all of them show up in a package the change probably did not touch. "Environment, not this change" sounds true, so it passes review. But it leaves the browser tests unrun, and a change to the Web transport or to one of the tests above gets no browser check at all.

## When to Apply

- Before the first test run in a new JJ workspace, especially for a focused run or on a Linux host.
- When a worker report blames "the environment", "local bun setup" or "web packages not installed" for `frontend/rpc` failures.
- When planning parallel verification in one workspace.
- When adding a test that calls `runWebTest`: update the table above.

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

After, with the web packages and the variables in place, `frontend/rpc` passes.

## Related

- `docs/solutions/best-practices/concurrent-make-test-runs-share-one-test-database-container-name.md`: on a Mac, how to give `make test` its own database.
- `docs/solutions/best-practices/coverage-profile-kills-its-own-shell-when-make-is-pid-1.md`: how to run a focused test subset on a Mac.
- `docs/solutions/best-practices/test-postgres-container-needs-larger-shm.md`: why a self-started test database needs `--shm-size`.
- `docs/solutions/best-practices/long-lived-test-database-slows-migrations.md`: why a reused test database should be recreated for each run.
- `docs/solutions/best-practices/colima-bind-mounts-fill-vm-disk.md`: how to find what fills a Colima VM disk.
- `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`: these tests serve the prebuilt `dist` bundle, so a stale build is a separate trap.
