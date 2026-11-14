---
title: Concurrent RocketClaw make Runs on a Mac Share One Go Build Cache
date: 2026-11-13
category: docs/solutions/best-practices/
module: internal/rocketclaw
problem_type: developer_experience
component: development_workflow
severity: medium
root_cause: concurrency
resolution_type: code_fix
applies_when:
  - Running a RocketClaw make target on a Mac while another JJ workspace may run one too
  - "A RocketClaw make target fails to build with `could not import <std package> (open /home/dev/.cache/go-build/...-d: no such file or directory)`"
  - Many packages report `[build failed]` at once and the same commit builds on a rerun
  - Writing worker or verification instructions that run RocketClaw make targets in parallel workspaces
  - Changing how internal/rocketclaw/Makefile starts the rocketclaw-linux container
related_components:
  - internal/rocketclaw/linux.Containerfile
tags:
  - make
  - gocache
  - go-build-cache
  - linux-container
  - docker-volume
  - colima
  - concurrent-workspaces
  - make-test
---

# Concurrent RocketClaw make Runs on a Mac Share One Go Build Cache

## Context

On a Mac, every `make` target in `internal/rocketclaw` runs again in the `rocketclaw-linux` container. Each run mounts the same named Docker volume, `rocketclaw-linux-home`, as the container user's home folder `/home/dev` (`internal/rocketclaw/Makefile:21`–`24`; `HOME=/home/dev` at `internal/rocketclaw/linux.Containerfile:38`). Go's default build cache is `$HOME/.cache/go-build`. So every RocketClaw `make` run on the machine, from every JJ workspace, uses one Go build cache.

During the agent editor build (2026-11-12 and 2026-11-13), builds broke this way three times, in three different workers' runs. The build stopped before any test ran, with errors like this:

```text
oai/oauth.go:10:2: could not import encoding/base64 (open /home/dev/.cache/go-build/8e/8eda9a5b…-d: no such file or directory)
sandbox/broker_linux.go:14:2: too many errors
FAIL	github.com/Rocketable/platform/internal/rocketclaw/agentlint [build failed]
```

The same error also came from `vet` during `coverage-profile` (`vet: … could not import fmt (open /home/dev/.cache/go-build/00/…-d: no such file or directory)`), and once from `sandbox-test`. Right after the first failure above, `docker ps` on the `colima-rocketclaw-linux` context showed another `rocketclaw-linux` container that had been running for 7 hours. The session records no such check for the other two failures.

The packages named are standard library packages (`fmt`, `errors`, `encoding/json`). The error reads like a broken Go install or a bad commit, and a plain rerun sometimes passes, so it looks flaky.

The session did not find out what deleted the cache files. `go help cache` says the cache "is safe for concurrent invocations of the go command", and also that the go command "periodically deletes cached data that has not been used recently". What is known: the failures happened only on the shared cache, and they stopped once each run used its own cache.

## Guidance

`internal/rocketclaw/Makefile`'s Darwin rule now gives each workspace its own Go build cache: it passes `-e GOCACHE=$(abspath ../../.tmp)/gocache` to `docker run`, the same path form `SANDBOX_TEST_DIR` uses. The path is the same inside the container because your home folder is bind-mounted at the same path (`-v $(HOME):$(HOME)`). Concurrent runs from different workspaces no longer share `/home/dev/.cache/go-build`.

- Keep that `-e GOCACHE` when changing the `docker run` line. Dropping it brings the shared cache, and these failures, back.
- A `GOCACHE` given on the make command line still wins, because command-line variables override the environment in the container's `make`: `make -C internal/rocketclaw test GOCACHE=/some/other/cache`.
- **An exported environment variable does not reach the container.** `GOCACHE=… make …` or `export GOCACHE=…` changes only the Mac-side `make`. Any setting meant for the container goes on the `make` command line or in the `docker run` line.

Costs: each workspace's first run compiles everything into a cold cache, and the cache grows large (11 GB in one workspace after a long build). It lives in the workspace's `.tmp/`, so it goes away with the workspace.

## Why This Matters

The repo runs many JJ workspaces in parallel, so `make` runs overlapping is normal, not rare. Nothing in the Makefile says the Go cache is shared: the volume mount looks like it only keeps a home folder between runs. The error names standard library packages, so it gets blamed on the toolchain, retried, or written off as "environment, not this change". A retry that happens to pass hides the problem until the next overlap. The obvious workaround, an exported `GOCACHE`, silently does nothing, which is why the fix lives in the Makefile.

## When to Apply

- When a RocketClaw build fails with `could not import … /home/dev/.cache/go-build/…: no such file or directory`: check that the run used a workspace with the Makefile fix, and that the `docker run` line still passes `-e GOCACHE`.
- When changing the Darwin rule in `internal/rocketclaw/Makefile`.
- For any setting you want to reach the container: put it on the `make` command line or the `docker run` line, never in the environment.

## Examples

Before the fix, with another `rocketclaw-linux` container running:

```text
$ make -C internal/rocketclaw coverage-profile GOTESTRUN='^TestAgentEditorRealBackendBrowser$'
vet: /usr/local/go/src/go/ast/commentmap.go:10:2: could not import fmt (open /home/dev/.cache/go-build/00/0079d730…-d: no such file or directory)
```

After the fix, a dry run shows each workspace's own cache:

```text
$ make -n -C internal/rocketclaw test | grep -o 'GOCACHE=[^ ]*'
GOCACHE=/Users/…/<workspace>/.tmp/gocache
```

## Related

- `docs/solutions/best-practices/concurrent-make-test-runs-share-one-test-database-container-name.md`: another resource that concurrent Mac runs share, the test database's container name on the shared Docker daemon. Its fix is separate.
- `docs/solutions/best-practices/frontend-rpc-tests-need-setup-in-fresh-jj-workspaces.md`: says that only command-line make variables reach the container. It also covers `vendor/`, which two runs in the *same* workspace share.
- `docs/solutions/best-practices/coverage-profile-kills-its-own-shell-when-make-is-pid-1.md`: how `linux_overrides` quotes command-line variables for the container.
