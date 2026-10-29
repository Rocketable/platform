---
title: Go Web Transport Tests Need Web Packages and Playwright in Every Workspace
date: 2026-10-29
category: docs/solutions/best-practices/
module: internal/rocketclaw/frontend/rpc
problem_type: developer_experience
component: development_workflow
severity: medium
resolution_type: environment_setup
applies_when:
  - Running go test ./... or make test from a fresh JJ workspace under .tmp/workspaces/
  - internal/rocketclaw/frontend/rpc tests fail inside a bun test step while the rest of the suite passes
  - A worker or reviewer is about to call a frontend/rpc failure an unrelated environment problem
symptoms:
  - "bun test output in frontend/rpc says Could not resolve \"@sentry/react\" (web packages not installed)"
  - "After installing them, the same tests fail with error: Cannot find package 'undefined' from '.../internal/rocketclaw/web/src/<name>-transport.test.ts'"
  - TestSessionEntries, TestPagesRealBrowser, TestPublicProgressRealBrowser and the other real-browser tests fail; backend packages pass
related_components:
  - internal/rocketclaw/web
  - internal/rocketclaw/Makefile
  - .github/workflows/test.yml
tags:
  - browser-tests
  - playwright
  - chromium
  - bun
  - jj-workspace
  - frontend-rpc
  - make-test
  - test-environment
---

# Go Web Transport Tests Need Web Packages and Playwright in Every Workspace

## Context

Seven Go tests in `internal/rocketclaw/frontend/rpc` start a real backend and then run a Bun test file against it through `runWebTest` (`internal/rocketclaw/frontend/rpc/server_test.go:3853`). That helper runs `bun test <file>` in `internal/rocketclaw/web` with the parent's environment plus the test's URLs (`server_test.go:3870`–`3873`). It does not skip anything. Six of the seven Bun files open real Chromium. They are `TestSessionEntries`, `TestPagesRealBrowser`, `TestPublicProgressRealBrowser`, `TestReloadRealBackendBrowser`, `TestRevertRealBackendBrowser` and `TestShadowsRealBackendBrowser`. The seventh, `TestSidebarHTTPStreamsBeforeGoTailAndCancels`, needs only the web packages.

So a plain `go test ./...`, which `AGENTS.md` requires, needs two things that the Go toolchain does not provide:

1. **The web packages in `internal/rocketclaw/web/node_modules`.** A new JJ workspace has none, because `internal/rocketclaw/web/.gitignore` ignores `node_modules` and each workspace is its own checkout. Without them, Bun cannot load `internal/rocketclaw/web/src/api.ts`, which starts with `import { captureException } from "@sentry/react"`. The tests fail with `Could not resolve "@sentry/react"`.
2. **`ROCKETCLAW_PLAYWRIGHT_MODULE` and `ROCKETCLAW_CHROMIUM`.** `playwright-core` is not in `internal/rocketclaw/web/package.json`. The test files load it from the path in the variable, for example `await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!)` (`internal/rocketclaw/web/src/pages-transport.test.ts:5`). When the variable is unset, Bun is asked to import `undefined` and prints `Cannot find package 'undefined'`. Neither the error nor the stack names the variable. Running `await import(process.env.ROCKETCLAW_PLAYWRIGHT_MODULE!)` with the variable unset reproduces the exact message on Bun 1.4.4.

CI does both steps itself. `.github/workflows/test.yml:123` runs `bun install --frozen-lockfile` in `internal/rocketclaw/web`. The "Install Playwright and Chromium for browser tests" step (`test.yml:132`–`139`) installs `playwright-core@1.63.0` and Chromium and exports both variables. Locally, `make test` covers only the first step: root `make test` runs `make generate lint` in `internal/rocketclaw`, and both targets depend on `web-assets`, which runs `bun install --frozen-lockfile` (`internal/rocketclaw/Makefile:15`–`24`). Nothing local sets the Playwright variables. Plain `go test ./...` does neither step.

The component READMEs do list the variables (`internal/rocketclaw/web/README.md` under "Local checks", `internal/rocketclaw/frontend/rpc/README.md`). Neither maps the two error messages back to them. The web README also says the variables "enable" the browser tests, which suggests the tests skip without them. They do not skip. They fail.

While building session write replies (PR #765), this cost three misdiagnoses in one run. The U1 worker saw seven `@sentry/react` failures and called them "an environment problem, not this change". The U3 worker said the same. After the orchestrator installed the web packages, the U7 worker saw six `Cannot find package 'undefined'` failures and blamed "the local `bun` setup". U7 had edited code inside `TestSessionEntries`, so that edit was compiled but never ran. The orchestrator's own full `go test ./...` then failed the same way. It took a grep for `ROCKETCLAW_PLAYWRIGHT_MODULE` and a read of the CI step to make `frontend/rpc` pass (44.7 s). After that, `go test ./...` exited 0.

## Guidance

These failures are a setup gap, and they block verification. They are not an unrelated failure to report and move past. Set up once per workspace, before the first full test run:

```sh
# From the workspace root. The main checkout contains .tmp/workspaces/.
main=${PWD%%/.tmp/workspaces/*}

# 1. Web packages for this workspace (make test does this too; go test ./... does not).
(cd internal/rocketclaw/web && bun install --frozen-lockfile)

# 2. playwright-core and Chromium, shared by every workspace, at the version CI pins
#    in .github/workflows/test.yml (1.63.0 as of this writing).
dir="$main/.tmp/playwright-core"
[ -d "$dir/node_modules/playwright-core" ] || (mkdir -p "$dir" && cd "$dir" && bun add playwright-core@1.63.0)
(cd "$dir" && ./node_modules/.bin/playwright-core install --no-shell chromium)

# 3. Keep the variables in one sourced file next to the database URL.
cat > .tmp/test-env.sh <<EOF
export ROCKETCLAW_PLAYWRIGHT_MODULE="$dir/node_modules/playwright-core"
export ROCKETCLAW_CHROMIUM="$(cd "$dir" && bun -e 'console.log(require("playwright-core").chromium.executablePath())')"
EOF
. .tmp/test-env.sh
go test ./internal/rocketclaw/frontend/rpc/ -count=1
```

Source `.tmp/test-env.sh` before every `go test ./...` and `make test`. Add `ROCKETCLAW_TEST_DATABASE_URL` to it when you run against a long-lived database. When a coordinator hands work to subagents, put the `. .tmp/test-env.sh` line in the worker rules. Each worker starts a fresh shell and will not inherit the variables.

Read the two messages this way:

| Error inside a `frontend/rpc` `bun test` step | Missing piece |
|---|---|
| `Could not resolve "@sentry/react"` (or any other web package) | `bun install --frozen-lockfile` in `internal/rocketclaw/web` |
| `Cannot find package 'undefined'` | `ROCKETCLAW_PLAYWRIGHT_MODULE` unset in the environment `go test` ran with |

## Why This Matters

Neither message points at its cause. Both show up in a package the change probably did not touch, and every agent run starts in a fresh workspace, so the failure comes back every time. "Environment, not this change" is literally true, which is why it keeps passing review. But it leaves the real-browser tests unrun: a change that touches `TestSessionEntries` or the Web transport code gets no browser check at all, and the required `go test ./...` and `make test` are not actually green.

## When to Apply

- Right after `ce-worktree` or any new JJ workspace, before the first `go test ./...` or `make test`.
- Whenever a worker report blames "the environment", "local bun setup" or "web packages not installed" for `frontend/rpc` failures.
- When writing worker rules or a verification step for a run that will execute Go tests.

## Examples

Before. A fresh workspace with `ROCKETCLAW_TEST_DATABASE_URL` set and nothing else:

```text
--- FAIL: TestPublicProgressRealBrowser (1.59s)
    live_test.go:249: ... bun test v1.4.4
        src/public-progress-transport.test.ts:
        error: Cannot find package 'undefined' from '.../internal/rocketclaw/web/src/public-progress-transport.test.ts'
```

After `bun install` and sourcing the variables:

```text
ok  	github.com/Rocketable/platform/internal/rocketclaw/frontend/rpc	44.684s
```

## Related

- `docs/solutions/best-practices/unix-socket-tests-need-short-tmpdir-in-jj-workspaces.md`: the other workspace setup step these same `frontend/rpc` tests need on macOS (a short `TMPDIR`).
- `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`: these tests serve the prebuilt `dist` bundle, so a stale build is a separate trap.
- `docs/solutions/best-practices/test-postgres-container-needs-larger-shm.md`: another local limit behind `make test` failures that look like test bugs.
