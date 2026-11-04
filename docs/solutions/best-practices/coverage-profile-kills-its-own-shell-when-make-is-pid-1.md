---
title: make coverage-profile Kills Its Own Shell When Run Directly on a Mac
date: 2026-11-03
category: docs/solutions/best-practices/
module: internal/rocketclaw
problem_type: developer_experience
component: development_workflow
severity: medium
root_cause: logic_error
resolution_type: workflow_improvement
applies_when:
  - Running one RocketClaw test, or a few, on a Mac with make -C internal/rocketclaw coverage-profile GOTESTRUN=...
  - Running make -C internal/rocketclaw check-coverage on a Mac
  - "A RocketClaw make target on a Mac stops with `Terminated` and prints no test output"
  - Changing the pkill lines in the coverage-profile recipe, or how the Darwin rule starts the Linux container
symptoms:
  - "`make -C internal/rocketclaw coverage-profile GOTESTRUN=...` on a Mac ends with `make: *** [Makefile:146: coverage-profile] Terminated`"
  - No go test output at all; the run dies before the test database starts
  - "`make test` and `make check-code-metrics` run the same recipe without trouble"
related_components:
  - internal/rocketclaw/linux.Containerfile
  - .github/workflows/test.yml
tags:
  - make
  - coverage-profile
  - gotestrun
  - pkill
  - pid-1
  - linux-container
  - colima
  - macos
---

# make coverage-profile Kills Its Own Shell When Run Directly on a Mac

## Context

On a Mac, every `make` target in `internal/rocketclaw` runs again inside the `rocketclaw-linux` container. The Darwin `%` rule runs `docker run ... rocketclaw-linux make $(MAKEOVERRIDES) $@` (`internal/rocketclaw/Makefile:14`–`18`). The image's `ENTRYPOINT` is `/bin/sh -c "... exec setpriv ... \"$@\""` (`internal/rocketclaw/linux.Containerfile:43`). Each `exec` keeps the same process, so inside the container **`make` is PID 1**.

The `coverage-profile` recipe clears leftover test browsers before and after its run with `pkill -P 1 -f 'ms-playwright/chromium'` (`internal/rocketclaw/Makefile:150` and `:152`). The comment above it (`:143`–`144`) explains the idea: a browser whose test run was killed is left as an orphan, orphans get PID 1 as their parent, so `-P 1` should match only orphans.

That idea fails when `make` itself is PID 1:

- `make` runs the recipe as `/bin/sh -c '<the whole recipe text>'`. That shell's parent is `make`, which is PID 1.
- `pkill -f` matches the full command line, and the recipe text contains `ms-playwright/chromium`.
- `pkill` never matches itself, but it does match its parent shell. That shell gets SIGTERM, and `make` reports `[Makefile:146: coverage-profile] Terminated`. Line 146 is the first line of the recipe.

`make test` does not hit this. There, `coverage-profile` runs as a sub-make: `test` runs `$(MAKE) check-code-metrics`, which runs `$(MAKE) coverage-profile` (`Makefile:113`–`120`). So the recipe shell's parent is a sub-make, not PID 1. CI does not hit it either. It runs `make coverage-profile ...` straight on a Linux runner (`.github/workflows/test.yml:147`), where `make` is not PID 1.

The goals that do hit it are the ones that make PID 1 run the recipe itself. `coverage-profile` was seen to fail on the space-permissions run (2026-11-03). `check-coverage` should fail the same way, because it lists `coverage-profile` as a prerequisite (`Makefile:136`). That case was not run.

This trap is easy to step into. `AGENTS.md` says RocketClaw tests run on a Mac only through `make`, and `coverage-profile GOTESTRUN=...` is the target that runs just a chosen subset of Go tests (the larger targets also run lint, the emulated shell's tests and the coverage minimum). The error names no cause, and the same recipe passes under `make test`.

## Guidance

Until the Makefile is fixed, run a focused test subset in the container with something other than `make` as PID 1. This is the runner the space-permissions run used (it lived at `.tmp/run-tests.sh` in that workspace):

```sh
#!/bin/sh
# Runs `make coverage-profile GOTESTRUN=$1` in the RocketClaw Linux container with make not as PID 1.
set -eu
ws=$(cd "$(dirname "$0")/.." && pwd)
make -C "$ws/internal/rocketclaw" linux-container
docker --context colima-rocketclaw-linux run --rm --privileged --network host -v "$HOME:$HOME" -w "$ws/internal/rocketclaw" \
	-v /var/run/docker.sock:/var/run/docker.sock -v rocketclaw-linux-home:/home/dev \
	-v rocketclaw-linux-sandbox:/sandbox -e SANDBOX_TEST_DIR=/sandbox -e "RUN=$1" \
	rocketclaw-linux sh -c 'make GOTESTRUN="$(printf %s "$RUN" | sed "s/\\$/\$\$/g")" coverage-profile; exit $?'
```

Some details matter:

- `sh -c '...; exit $?'` is a compound command, so `sh` stays PID 1 and does not `exec` into `make`. A bare `sh -c 'make ...'` may be exec'd and bring the problem back.
- The `docker run` flags and volumes copy the Darwin rule (`Makefile:15`–`17`). If that rule changes, copy the new flags.
- `make linux-container` first makes sure the Colima VM and the image exist, as the Darwin rule does.
- `make` expands `$` in `GOTESTRUN`, so an anchored pattern needs `$$`. CI writes `\$\$` for the same reason (`test.yml:147`). The `sed` does this, so `.tmp/run-tests.sh '^(TestFoo|TestBar)$'` works.

A full `make test` needs no workaround.

**The real fix belongs in `internal/rocketclaw/Makefile`. This doc does not make it.** Either change would stop the recipe from matching its own shell. Neither has been tested:

- Write the pattern so its own text does not match it: `pkill -P 1 -f 'ms-playwright/[c]hromium'`. The regex still matches a Chromium command line. The recipe text now contains `[c]hromium`, which the regex does not match.
- Or add `--init` to the Darwin `docker run`, so Docker's init is PID 1 and `make` is not. Check that the `ENTRYPOINT`'s cgroup moves still work with an extra process in the container.

Once the Makefile is fixed, confirm that `make -C internal/rocketclaw coverage-profile GOTESTRUN=<one test>` passes on a Mac. Then remove the workaround from this doc.

## Why This Matters

`-P 1` means "orphan" only when PID 1 is an init process. In a container, PID 1 is whatever command the container runs. The Makefile's comment states that assumption. Nothing checks it, and it broke silently when the Mac runs moved into the container. The direct targets are the ones people use for quick checks, so this lands on the person running a single test. Without this doc, they see `Terminated`, rerun it, maybe blame Colima or memory, and finally fall back to a full `make test` that takes much longer.

## When to Apply

- Before running `coverage-profile` or `check-coverage` directly on a Mac.
- When a RocketClaw target in the container ends with `Terminated` and shows no test output.
- When editing the `pkill` lines, adding another `pkill -f` or `pgrep -f` to a recipe, or changing how the Darwin rule starts the container. Any `-f` pattern that appears literally in its own recipe matches that recipe's shell whenever the shell's parent fits the other filters.

## Examples

Before, on a Mac:

```text
$ make -C internal/rocketclaw coverage-profile GOTESTRUN=<pattern>
make: *** [Makefile:146: coverage-profile] Terminated
```

After, through the runner above, `go test` runs the chosen tests and writes `.coverage.out` as usual.

## Related

- `docs/solutions/best-practices/frontend-rpc-tests-need-setup-in-fresh-jj-workspaces.md`: older setup for running RocketClaw tests from a workspace. It predates the move of Mac runs into the Linux container.
- `docs/solutions/best-practices/colima-bind-mounts-fill-vm-disk.md`: another way the Colima VM behind these runs can fail.
