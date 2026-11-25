---
title: Frontend RPC Browser Tests Fail Without Their Setup, and That Blocks Verification
date: 2026-10-29
last_updated: 2026-11-25
category: docs/solutions/best-practices/
module: internal/rocketclaw/frontend/rpc
problem_type: developer_experience
component: development_workflow
severity: medium
resolution_type: environment_setup
applies_when:
  - Running RocketClaw tests from a fresh JJ workspace
  - internal/rocketclaw/frontend/rpc tests fail inside a bun test step while the rest of the suite passes
  - A worker or reviewer is about to call a frontend/rpc failure an unrelated environment problem
  - About to run make vendor or go generate while another test run uses the same workspace
  - Tests run with a long TMPDIR, such as one under a deeply nested workspace
symptoms:
  - "bun test output in frontend/rpc says Could not resolve \"@tanstack/react-query\" (web packages not installed)"
  - "error: Cannot find package 'undefined' from '.../internal/rocketclaw/web/src/<name>-transport.test.ts'"
  - "Chromium reports SingletonSocket path too long, or RPC sockets fail with bind: invalid argument"
  - "Tests fail with is explicitly required in go.mod, but not marked as explicit in vendor/modules.txt, or open ../../../vendor/...: no such file or directory"
related_components:
  - internal/rocketclaw/web
  - internal/rocketclaw/Makefile
  - internal/rocketclaw/mcpclient
  - .github/workflows/test.yml
tags:
  - frontend-rpc
  - browser-tests
  - playwright
  - bun
  - vendor
  - tmpdir
  - jj-workspace
---

# Frontend RPC Browser Tests Fail Without Their Setup, and That Blocks Verification

## Context

Eleven Go tests in `internal/rocketclaw/frontend/rpc` start a real backend and then run a Bun test file against it through `runWebTest` (`internal/rocketclaw/frontend/rpc/server_test.go:4044`). That helper runs `bun test <file>` in `internal/rocketclaw/web`, with the parent's environment plus the test's URLs (`server_test.go:4047`–`4050`). It skips nothing.

| Test | `runWebTest` call | Opens Chromium |
|---|---|---|
| `TestSessionEntries` | `server_test.go:2149` | yes |
| `TestPagesRealBrowser` | `pages_test.go:248` | yes |
| `TestPublicProgressRealBrowser` | `live_test.go:267` | yes |
| `TestReloadRealBackendBrowser` | `live_test.go:497` | yes |
| `TestRevertRealBackendBrowser` | `live_test.go:590` | yes |
| `TestShadowsRealBackendBrowser` | `shadow_live_test.go:166` | yes |
| `TestRemoteRocketClawRealBrowser` | `remote_test.go:191` | yes |
| `TestSpacePermissionsRealBrowser` | `permissions_live_test.go:164` | yes |
| `TestAgentEditorEditingAndSavingRealBackendBrowser` | `agent_editor_live_test.go:236` | yes |
| `TestAgentEditorPreviewAndChatRealBackendBrowser` | `agent_editor_live_test.go:236` | yes |
| `TestSidebarHTTPStreamsBeforeGoTailAndCancels` | `sidebar_test.go:232` | no, needs only the web packages |

All paths are under `internal/rocketclaw/frontend/rpc/`. These tests need two things the Go toolchain does not provide:

1. **The web packages in `internal/rocketclaw/web/node_modules`.** A new JJ workspace has none, because `internal/rocketclaw/web/.gitignore:2` ignores them. Without them, Bun cannot load `internal/rocketclaw/web/src/api.ts`, whose first line imports `@tanstack/react-query`.
2. **`ROCKETCLAW_PLAYWRIGHT_MODULE` and `ROCKETCLAW_CHROMIUM`.** `playwright-core` is not in `internal/rocketclaw/web/package.json`. The test files load it from the variable, for example `await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!)` (`internal/rocketclaw/web/src/pages-transport.test.ts:5`). When the variable is unset, Bun prints `Cannot find package 'undefined'`. Nothing in the message names the variable.

Unix socket paths are limited to 108 bytes on Linux. The socket helper appends `rpc-<random>/s` beneath `TMPDIR` (`testSocketPath`, `server_test.go:4068`–`4079`), and Chromium puts its `SingletonSocket` there too, so a long `TMPDIR`, such as one under a deeply nested workspace's `.tmp/`, makes RPC socket binds fail with `invalid argument` and Chromium abort with `Socket path too long`. Keep `TMPDIR` short.

`vendor/` is a separate trap. `make vendor` runs `go mod tidy` and `go mod vendor` and so deletes and rewrites `vendor/`. Two make runs in one workspace share the same `vendor/`. Some tests also run the Go toolchain mid-run: `buildStdioHelper` calls `go build` (`internal/rocketclaw/mcpclient/registry_test.go:351`, `:354`). A test run that started cleanly can break when another run rewrites `vendor/` under it. `go generate`, which `make test` runs first, deletes and rewrites the mocks and the built bundles the same way.

On the session write replies work (PR #765), three workers in a row called these failures "an environment problem, not this change", and one edit inside `TestSessionEntries` was compiled but never ran.

## Guidance

- **Run `make test`** in `internal/rocketclaw` or `internal/rocketclaw/web`: it sets both Playwright variables and starts a database. A focused `go test` needs the same two exports from the top of either Makefile, plus `ROCKETCLAW_TEST_DATABASE_URL`.
- **Never run `make vendor` or `go generate` alongside another test run in the same workspace.** That means no parallel tool calls, no background jobs, and no worker running one while the coordinator runs the other. `make test` already runs `go generate` and `lint` before its tests.
- **Treat these failures as blocking.** A `frontend/rpc` failure from missing setup means the browser tests did not run. Fix the setup and rerun before you report the change as verified. Put this rule in worker instructions.

Read the errors this way:

| Error | Cause | Can still happen when |
|---|---|---|
| `Could not resolve "@tanstack/react-query"` (or another web package) in a `bun test` step | No `internal/rocketclaw/web/node_modules` | A fresh workspace runs `go test` before `bun install`, or a focused run without `go generate ./internal/rocketclaw/internal/web` |
| `Cannot find package 'undefined'` in a `bun test` step | `ROCKETCLAW_PLAYWRIGHT_MODULE` is unset where the tests ran | A focused `go test` without those exports |
| `not marked as explicit in vendor/modules.txt`, or `open ../../../vendor/...: no such file or directory` | `make vendor` rewrote `vendor/` during the test run | Any platform, when those targets overlap another test run in the same workspace. Wait for it to finish and rerun |
| Chromium `SingletonSocket` path too long, or RPC `listen unix ...: bind: invalid argument` | Temporary socket pathname is too long | `TMPDIR` sits under a long workspace path |

The `vendor/modules.txt` message points at `go.mod`, but nothing is wrong with it. Do not "fix" `go.mod`.

## Why This Matters

Dependency and vendor errors can hide their setup cause; socket failures can look like application regressions. "Environment, not this change" is not a verification result. Leaving the setup broken leaves the browser or permission checks unrun, and weakening those checks would hide the contract they protect.

## When to Apply

- Before the first test run in a new JJ workspace, especially for a focused run.
- When a worker report blames "the environment", "local bun setup" or "web packages not installed" for `frontend/rpc` failures.
- When planning parallel verification in one workspace.
- When adding a test that calls `runWebTest`: update the table above.
- When moving temporary files under repository `.tmp/`, especially in a deeply nested workspace.

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

Installing the web packages and setting the variables resolved both.

## Related

- `docs/solutions/best-practices/test-postgres-container-needs-larger-shm.md`: why a self-started test database needs `--shm-size`.
- `docs/solutions/best-practices/long-lived-test-database-slows-migrations.md`: why a reused test database should be recreated for each run.
- `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`: these tests serve the prebuilt `dist` bundle, so a stale build is a separate trap.
- `docs/solutions/best-practices/crash-dumps-in-web-change-the-built-stylesheet.md`: a Chromium crash dump left in `internal/rocketclaw/web` can change the built stylesheet; remove it (or move it under `.tmp/` to keep it) before rebuilding.
