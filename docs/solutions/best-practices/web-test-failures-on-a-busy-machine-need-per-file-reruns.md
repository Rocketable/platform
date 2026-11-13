---
title: Web make test Failures on a Busy Machine Need the Right File, an Isolated Rerun and an Undisturbed Run
date: 2026-11-13
category: docs/solutions/best-practices/
module: internal/rocketclaw/web
problem_type: developer_experience
component: development_workflow
severity: medium
resolution_type: workflow_improvement
applies_when:
  - The Web package's make test fails in the rocketclaw-linux container and you must decide whether the change broke it
  - Several unrelated browser tests time out together while other agents' containers load the machine
  - Mapping a (fail) line in the Web test log back to its test file
  - Stopping, or editing files during, a background verification run that rebuilds dist or oc-dist
  - OpenCode browser tests fail with ENOENT on an oc-dist _assets chunk on a branch that changed no OpenCode code
symptoms:
  - "A (fail) line sits under a src/<file>.test.ts: header of a file that does not contain that test"
  - "Tests end with `^ this test timed out after 30000ms` (or 60000ms or 180000ms), followed by `waitFor: Target page, context or browser has been closed`"
  - Different unrelated tests fail in each full run of the same code, and one run of that code passes
  - "`ENOENT: no such file or directory, open '.../internal/web/oc-dist/_assets/preload-helper-<hash>.js'`"
  - "`jj status` lists about a thousand deleted or changed files under internal/rocketclaw/internal/web/oc-dist"
related_components:
  - internal/rocketclaw/Makefile
  - internal/rocketclaw/oc
  - internal/rocketclaw/internal/web/oc-dist
tags:
  - web
  - browser-tests
  - test-timeouts
  - machine-load
  - xargs
  - oc-dist
  - linux-container
  - verification
---

# Web make test Failures on a Busy Machine Need the Right File, an Isolated Rerun and an Undisturbed Run

## Context

`make test` in `internal/rocketclaw/web` builds the bundle, then runs every test file except the `*-transport.test.ts` ones, **four files at a time**: `xargs -0 -n 1 -P 4 bun test` (`internal/rocketclaw/web/Makefile:43`–`48`). Each `bun test <file>` prints its own `src/<file>.test.ts:` header, its results and its summary. All four write to the same output, so their lines interleave.

That Makefile has no Mac rule, so on a Mac it runs natively. To test on Linux, PR #1115 (the Web typing indicator) ran it inside the `rocketclaw-linux` image with `docker --context colima-rocketclaw-linux run ... rocketclaw-linux sh -c '... make lint; make test'`, from `internal/rocketclaw/web`. The machine was shared with other agents' test containers. Load averages were 11 to 17 (`uptime` at 17:24: `13.47 16.82 18.63`; at 19:52: `12.23 11.29 11.48`).

Four things went wrong while checking those failures. Paths starting with `src/` are relative to `internal/rocketclaw/web`.

**1. The nearest header named the wrong file, twice.** The log's (fail) lines were mapped to files with `awk '/^src\/.*\.test\.ts:$/{f=$0} /^\(fail\)/{print f}'`. In the 19:41 run, `(fail) long chats open at the newest turns…` came right after the `src/notifications.browser.test.ts:` header, but that test is in `src/long-chat.browser.test.ts`. `actual App restores, merges…` was listed under `src/web-update.browser.test.ts:`, but it is in `src/session-list.browser.test.ts`. That happened twice:

- After the first failed run (17:24), a native Mac rerun of the six files the headers named passed 82 of 82. Four of the seven failing tests were in other files (`long-chat`, `delegations`, `sentry`, `session-list.browser`), so that rerun said nothing about them.
- At 19:52, a container rerun included `tabs` and `web-update` because of the headers. Neither held a failing test. Searching for the test titles found the real files: `long-chat`, `session-commands` and `session-list.browser`.

Searching for a title can also miss, because some titles are template literals: `` `fork and handoff at ${width}px` `` (`src/session-commands.browser.test.ts:9`), `` `shadow refresh survives ${mode} history reads` `` (`src/shadow-refresh.browser.test.ts:9`) and `` `actual App command hints match existing bindings on ${platform}` `` (`src/session-list.browser.test.ts:231`). Searching for `fork and handoff at 320px` or `on Linux x86_64` finds nothing.

**2. The failures were real time-outs, spread across unrelated files.** In the two full runs of the final code, all 10 failures printed `^ this test timed out after <limit>ms`. The limits were 30 s (`src/long-chat.browser.test.ts:127`), 60 s (`src/session-commands.browser.test.ts:567`) and 180 s (`src/session-list.browser.test.ts:3235`). `Target page, context or browser has been closed` is what happens after the time-out: the test's `finally` closes the browser, and the `waitFor` that was still waiting fails. In `verify-web.log` it appears as `# Unhandled error between tests` at `long-chat.browser.test.ts:98`, just above the test's `(fail)` line. The two runs failed different sets, and an earlier run on the code before the last round of edits passed completely. Durations from the logs:

| Test (time limit) | Earlier passing full runs | Final code, full run 19:41 | Final code, full run 19:51 | Final code, alone in the container |
|---|---|---|---|---|
| long chats open at the newest turns (30 s) | 14.0 s, 6.9 s | timed out | timed out | 10.3 s pass |
| actual App restores, merges… (180 s) | 121.2 s, 123.0 s | timed out | timed out | 150.1 s pass |
| fork and handoff at 390px (60 s) | 13.5 s, 10.7 s | 36.0 s pass | timed out | 28.6 s, then 14.2 s pass |
| fork and handoff at 320px (60 s) | 11.2 s, 11.1 s | 32.8 s pass | 43.4 s pass | **39.7 s fail**, then 11.9 s, 12.5 s, 14.2 s pass |
| remote sessions list, tag, go offline, and start (60 s) | 5.3 s, 5.8 s | timed out | 43.9 s pass | not rerun alone |

Under load the same test ran 3 to 8 times slower. The one failure in an isolated run, `fork and handoff at 320px` at 39.7 s, failed **under** its 60 s limit, so it was not a time-out. Its error was lost, because the rerun piped `bun test` through `grep -E "^\((pass|fail)\)|…"`. It then passed three times: twice alone and once with its whole file. The cause of that one failure is still unknown.

**3. Editing files during a background run invalidated that run.** Verification started at 19:03. At 19:05, while it was still running, `typing.tsx` and `ui.tsx` were edited, and a Mac `bun run build` and `make lint` ran in `internal/rocketclaw/web`. Each step of that run reads the tree when it gets there. Its Go tests, its build and its Web tests could each have seen a different version, and nothing in its result would say which one.

**4. Stopping the wrapper script did not stop the run, and the half-stopped run broke OpenCode tests.** `pkill -f verify-rebased.sh` (a scratch wrapper around `make test`) ended the script with exit 143. Its repository-root `make test` (host PID 21935) and the container that make had started (`infallible_wing`, working directory `internal/rocketclaw`) kept running until they were killed with `kill` and `docker rm -f`. On a Mac, `make test` in `internal/rocketclaw` runs `docker run ... rocketclaw-linux make test` (`internal/rocketclaw/Makefile:20`–`24`), and `test` depends on `generate`, which runs `oc-assets` (`:122`, `:55`, `:52`–`53`). That is a Vite build whose output directory is the committed `internal/rocketclaw/internal/web/oc-dist`, with `emptyOutDir: true` (`internal/rocketclaw/oc/vite.config.ts:49`–`50`). The build was stopped partway, which left `oc-dist` partly rewritten: `jj status` counted 1,085 `oc-dist` entries, and `ls oc-dist/_assets` listed 573 entries, where the commit has 1,657 files. The OpenCode browser tests serve that directory (`src/opencode.browser.test.ts:9`). The next one failed after 1 s with `ENOENT … oc-dist/_assets/preload-helper-u7OfE_hN.js`, then with the same `Target page, context or browser has been closed` message the time-outs print. The first check had missed this damage: `jj status | grep -v dist/` hides `oc-dist/` too.

The branch changed no OpenCode code, so `jj restore --from @- internal/rocketclaw/internal/web/oc-dist` was correct. It printed `Added 1084 files, modified 1 files`, and the 93 focused tests passed.

## Guidance

**Find the failing file from the test title, never from the nearest header.**

```bash
cd internal/rocketclaw/web
grep -rlF 'long chats open at the newest turns' src
```

For titles built from a template literal, search for a fixed piece of the title, such as `fork and handoff at` or `command hints match existing bindings`. Rerun only the files this search finds.

**Count a time-out as passing only after its file passes alone, in the same container.** For each file that failed in the latest full run, run that file by itself in `rocketclaw-linux` and keep the full output:

```bash
W=$PWD   # workspace root
docker --context colima-rocketclaw-linux run --rm --privileged --network host -v $HOME:$HOME \
  -w $W/internal/rocketclaw/web -v /var/run/docker.sock:/var/run/docker.sock \
  -v rocketclaw-linux-home:/home/dev rocketclaw-linux sh -c \
  'ulimit -c 0; bun install --frozen-lockfile >/dev/null; for f in src/long-chat.browser.test.ts src/session-list.browser.test.ts; do bun test "$f"; done' \
  > .tmp/rerun.log 2>&1
grep -nE '^\((pass|fail)\)|timed out after|^error:' .tmp/rerun.log
```

Filter the log after it is saved, not the live output. That way, a failure still leaves its error behind. A native Mac rerun is weaker evidence: it uses another Chromium build on another OS. `ulimit -c 0` keeps a crashing renderer from leaving a `core` file in `internal/rocketclaw/web` (`crash-dumps-in-web-change-the-built-stylesheet.md`).

Read each failure before you call it load:

- **It is a time-out** only when the log says `^ this test timed out after <limit>ms` and the time shown is about the limit. `Target page, context or browser has been closed` alone is not proof, because the ENOENT failure above prints it too, after 1 s.
- **A failure under the limit is not a time-out.** Get its real error by running it alone again with `-t '<title>'` and keeping the output. Report it as unexplained if it then passes.
- **Compare against earlier passing runs.** Use the durations in their logs. A test that takes much longer than it did there, with other unrelated tests in the same state, points at load. A test that fails quickly, or every time while its neighbours pass, points at the code.
- **Report what was actually shown.** For example: "the full suite never passed end to end on the final code; each file that failed in its last run passed alone in the container; CI is the confirmation". "Just load" is not a verification result (`frontend-rpc-tests-need-setup-in-fresh-jj-workspaces.md`).

**Do not touch the tree while a verification runs.** That includes source edits, `bun run build` and `make lint`. If you have to change something, stop the run completely, make the change and start again.

**Stopping a run means stopping its make and its containers, then checking the committed bundles.**

```bash
pgrep -fl 'make test|webtest|coverage-profile'
D="docker --context colima-rocketclaw-linux"
for c in $($D ps -q); do echo "$c $($D inspect -f '{{.Name}} {{.Config.WorkingDir}}' $c)"; done
# kill your make PID and remove only containers whose working directory is in your workspace
jj status | grep -c 'internal/web/oc-dist/'
jj status | grep 'internal/web/dist/'
```

Other workspaces' test containers run on the same daemon, so remove only yours. If `oc-dist` changed and your change does not touch `internal/rocketclaw/oc`, put back the committed copy from the commit your work sits on: `jj restore --from @- internal/rocketclaw/internal/web/oc-dist`. If your change does touch `internal/rocketclaw/oc`, rebuild it with `make -C internal/rocketclaw oc-assets` and let the build finish. Filter `jj status` with `grep -v 'internal/web/dist/'`, not `grep -v dist/`.

Not done here, because it changes the Makefile: labelling each line with its file would remove the wrong-header problem at its source. One option is `xargs -0 -n 1 -P 4 bash -o pipefail -c 'bun test "$1" 2>&1 | sed "s|^|$1: |"' _`. It needs `pipefail`, or `sed`'s exit status would replace `bun test`'s and failures would pass. This has not been tried. Whoever owns the Web Makefile can decide.

## Why This Matters

Each of these failures is cheap to misread. A wrong file mapping produces a clean rerun of the wrong files, which looks like proof. Calling every time-out "load" would also have hidden the one failure that was not a time-out. Requiring every full run to pass is not practical on a machine this busy either: the same code passed one full run and failed the next. The isolated rerun in the same container is the evidence that sits between those. The stopped run is the worst case, because the damage lands in a committed directory the branch never meant to change, and shows up as a test failure that looks like the time-outs.

## When to Apply

- Before reporting a Web change as verified after a `make test` run that failed only on unrelated tests.
- Whenever a (fail) line from the Web suite has to be traced to a file.
- Before stopping a background `make test`, `make generate` or a wrapper script around them, and after any run that was stopped.
- When OpenCode browser tests fail with ENOENT on an `oc-dist` asset.

## Examples

Wrong, from the 17:24 run: the file list came from headers, so the rerun covered other files.

```text
$ awk '/^src\/.*\.test\.ts:$/{f=$0} /^\(fail\)/{print f}' verify-web.log | sort -u
src/notifications.browser.test.ts:   <- held none of the failing tests
...
$ bun test <those six files>   # native Mac
 82 pass
 0 fail
```

Right, from the 19:59 rerun: the files came from searching for the titles, and each ran alone in the container.

```text
== src/long-chat.browser.test.ts
(pass) long chats open at the newest turns, … [10291.78ms]
== src/session-commands.browser.test.ts
(fail) fork and handoff at 320px [39706.41ms]   <- under its 60 s limit: rerun alone with its error kept
== src/session-list.browser.test.ts
(pass) actual App restores, merges, isolates and keeps composer independent [150138.95ms]
```

## Related

- `docs/solutions/best-practices/frontend-rpc-tests-need-setup-in-fresh-jj-workspaces.md`: failures from missing setup, and why "environment, not this change" is not a verification result.
- `docs/solutions/best-practices/crash-dumps-in-web-change-the-built-stylesheet.md`: putting back the committed Web `dist` after a crashed or stopped run.
- `docs/solutions/best-practices/web-browser-tests-run-the-last-bundle-that-built.md`: browser tests serve whatever bundle is on disk.
- `docs/solutions/best-practices/concurrent-make-test-runs-share-one-test-database-container-name.md`: other workspaces' runs share the same Docker daemon.
- `docs/solutions/best-practices/coverage-profile-kills-its-own-shell-when-make-is-pid-1.md`: the Web `test` recipe's cleanup of leftover Chromium processes.
