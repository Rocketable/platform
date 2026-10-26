---
title: Unix-Socket Tests Need a Short TMPDIR Inside JJ Workspaces
date: 2026-10-26
category: docs/solutions/best-practices/
module: internal/rocketclaw/frontend/rpc
problem_type: developer_experience
component: development_workflow
severity: medium
resolution_type: environment_setup
applies_when:
  - Running root or internal/rocketclaw make test on macOS from a JJ workspace under the repository's .tmp/workspaces/
  - Setting TMPDIR inside .tmp/ as the repository requires, at a path deeper than the main checkout's .tmp/
  - internal/rocketclaw/frontend/rpc tests fail with bind invalid argument while the rest of the suite passes
symptoms:
  - "make test fails about 22 internal/rocketclaw/frontend/rpc tests, such as TestHTTPStreams, TestPages and TestSessionReads"
  - "Errors read listen unix <TMPDIR>/rpc-NNNNNNNNNN/s: bind: invalid argument"
  - The failures have nothing to do with the change under test and go away with a shorter TMPDIR
related_components:
  - internal/rocketclaw/Makefile
  - internal/rocketclaw/frontend/rpc/README.md
  - internal/rocketclaw/web/README.md
tags:
  - tmpdir
  - unix-socket
  - macos
  - jj-workspace
  - make-test
  - frontend-rpc
  - path-length
---

# Unix-Socket Tests Need a Short TMPDIR Inside JJ Workspaces

## Context

The repository requires every temporary file to live under `<repo-root>/.tmp/`, so tests run with `TMPDIR` set there. Agent runs work in JJ workspaces nested inside that same directory, at `<main-checkout>/.tmp/workspaces/<date>-<slug>/`. Setting `TMPDIR` to that workspace's own `.tmp/` follows the rule, but on macOS it breaks `make test`.

The `internal/rocketclaw/frontend/rpc` tests start real gRPC servers on Unix sockets. Every one of them gets its socket path from `testSocketPath` (`internal/rocketclaw/frontend/rpc/server_test.go:3888`):

```go
dir, err := os.MkdirTemp(os.Getenv("TMPDIR"), "rpc-")
...
socketPath, err := filepath.Abs(filepath.Join(dir, "s"))
```

`make test` reaches these tests through the `coverage-profile` target in `internal/rocketclaw/Makefile`, which runs `go test ... ./...`.

On Darwin, a Unix socket path must fit in a 104-byte buffer that also holds a terminating NUL. Go's `syscall` package returns `EINVAL` before calling `bind` when the path is 104 bytes or longer (`SockaddrUnix.sockaddr` in Go's `syscall_bsd.go`), so the longest usable path is 103 bytes. The test sees this as `bind: invalid argument`, which looks nothing like a path-length error.

`os.MkdirTemp` names the directory `rpc-` plus a random unsigned 32-bit number in decimal. That number is usually 10 digits long. So the socket path is `len(TMPDIR) + 17` bytes at most (`/rpc-` is 5, the number is up to 10, `/s` is 2). Any `TMPDIR` longer than 86 bytes can fail.

On the search-agent-filter run (2026-10-26), the workspace root was `/Users/ucirello/Projects/platform/.tmp/workspaces/20261026-search-agent-filter-any`, which is 82 bytes:

| `TMPDIR` | Length | Longest socket path | Result |
|---|---|---|---|
| workspace `.tmp` | 87 | 104 | Fails whenever the random number has 10 digits, which is about 77% of the time |
| workspace `.tmp/test-tmp` (the pattern in `internal/rocketclaw/frontend/rpc/README.md`) | 96 | 113 | Always fails |
| main checkout `.tmp` | 38 | 55 | Always fits |

During that run, root `make test` failed 22 tests, all in `frontend/rpc`, with 52 `bind: invalid argument` errors. The rerun used a short `TMPDIR` that was still inside the repository's `.tmp/`, and the socket failures went away. The session record does not keep the exact short path. The same limit had already come up in `docs/plans/2026-09-24-1402-feat-settle-snooze-plan.md`, which used `.tmp/st` at the repository root for a socket test.

## Guidance

When you run tests on macOS from a JJ workspace, point `TMPDIR` at a short directory under the **main checkout's** `.tmp/`, not the workspace's own `.tmp/`:

```sh
# From the workspace root. The main checkout is the directory that contains .tmp/workspaces/.
main=${PWD%%/.tmp/workspaces/*}
mkdir -p "$main/.tmp/t"
export TMPDIR="$main/.tmp/t"
make test
```

This stays inside `<repo-root>/.tmp/` as the temporary-directory rule requires. Before a long run, check the budget: `printf %s "$TMPDIR" | wc -c` must print 86 or less.

If you see `bind: invalid argument` from `listen unix`, count the path length before you debug the test. Do not add a fallback to `/tmp`. That breaks the repository's temporary-directory rule.

## Why This Matters

The error does not mention path length. It shows up in a package the change probably did not touch, and it can look random because the length depends on the random directory name. All of that points an agent toward debugging the wrong thing or deciding the suite is flaky. The comment in `testSocketPath` says `TMPDIR` should be short, but it does not say what "short" means. The README examples (`$PWD/.tmp`, `$PWD/.tmp/test-tmp`) are only safe from the main checkout, because `$PWD` there is short. Every agent run starts in a deeply nested workspace, so this will keep coming back. Longer workspace slugs make it worse.

## When to Apply

- Before running root `make test`, `internal/rocketclaw` `make test`, or `go test ./internal/rocketclaw/frontend/rpc` on macOS from anywhere under `.tmp/workspaces/`.
- When writing a verification contract or plan step that sets `TMPDIR` for runs in an agent workspace.
- When adding a new test that binds a Unix socket under `TMPDIR`. Reuse `testSocketPath` and keep the added path short.

## Examples

Before: from the workspace root, `TMPDIR` is set to the workspace's `.tmp/` (87 bytes). Root `make test` fails 22 `frontend/rpc` tests with `listen unix .../.tmp/rpc-NNNNNNNNNN/s: bind: invalid argument`.

After: `TMPDIR` is a short directory under the main checkout's `.tmp/`. The `frontend/rpc` tests bind their sockets, and the socket errors are gone.

## Related

- `docs/solutions/best-practices/test-postgres-container-needs-larger-shm.md`: another local-environment limit behind `make test` failures that look like test bugs.
- `docs/plans/2026-09-24-1402-feat-settle-snooze-plan.md`: an earlier run that hit the same socket-path limit and used `.tmp/st`.
