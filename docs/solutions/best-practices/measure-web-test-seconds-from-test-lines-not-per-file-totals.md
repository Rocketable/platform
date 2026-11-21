---
title: Measure Web Test Seconds per File from Test Lines, Not from bun's Per-File Totals
date: 2026-11-20
category: docs/solutions/best-practices/
module: internal/rocketclaw/web
problem_type: developer_experience
component: development_workflow
severity: medium
applies_when:
  - Refreshing .github/web-test-seconds.tsv, the weights the web CI job uses to split test files across shards
  - A web shard finishes much later or much sooner than the others
  - Reading per-file durations out of a GitHub Actions log where xargs runs two bun test processes at once
  - Deciding which test file or test bounds a shard's wall time
symptoms:
  - web (1) finished in about a minute while web (2) took over three, with every shard given about the same listed weight
  - Matching each file's start to a "Ran N tests across 1 file. [Xs]" line said session-commands.browser.test.ts took 167 s; its tests add up to about 60 s
related_components:
  - .github/workflows/test.yml
  - .github/web-test-seconds.tsv
tags:
  - ci
  - web-shards
  - bun-test
  - browser-tests
  - merge-queue
  - test-timing
---

# Measure Web Test Seconds per File from Test Lines, Not from bun's Per-File Totals

## Context

The `web` job in `.github/workflows/test.yml` splits the web test files across four shards. Each shard
weighs every file by its CI seconds from `.github/web-test-seconds.tsv` (an unlisted file weighs 1), hands
the heaviest file to the least-loaded shard, then runs its files with `xargs -n 1 -P 2 bun test`, two at a
time. A stale list only unbalances the shards, so nothing fails when it drifts. By November 2026 it had
drifted a long way: `notifications.browser.test.ts` was listed at 128 s and took about 20, `tabs.browser.test.ts`
was listed at 121 and took about 165, and `github-pr.browser.test.ts` (about 29 s) and `composer.browser.test.ts`
(about 17 s) were not listed at all. One shard finished in about a minute while another took over three.

## Guidance

Measure each file's seconds by adding up its tests' own durations, taken from several recent merge queue runs:

1. Download the four `web (N)` job logs of a few recent `merge_group` runs into `.tmp/`:
   `GIT_DIR=$(jj git root) gh run view --job <job-id> --log > .tmp/ci/web-<job-id>.log`.
2. Collect every literal test title from the files the job runs: the string passed to `test(...)`, cut
   at the first `${` for titles built in a loop.
3. For every `(pass) <title> [<N>ms]` line in the logs, add N to the file whose title is the longest
   prefix of that line's title.
4. Divide each file's total by the number of runs, round to whole seconds, and write `<seconds><TAB>src/<file>`
   lines, heaviest first. Files under about 2 s can be left out; they weigh 1 anyway.

Before replacing the list, run the job's own split (the `find | awk | sort | awk` pipeline from the
workflow) against both the old and the new list, and check that every file is assigned to exactly one
shard. Feeding the measured seconds through the old list's split should reproduce the shard times
the logs show, which shows the measurement is right.

## Why This Matters

The obvious measurement is wrong. Each file's output starts with a `##[group]src/<file>:` header and ends
with `Ran N tests across 1 file. [Xs]`, so pairing a header with the `Ran` line whose duration ends at the
right time looks reliable. But two `bun test` processes write to the same log, and the first two files of a
shard start in the same instant, so their headers carry the same timestamp and either `Ran` line fits
both. That pairing reported `session-commands.browser.test.ts` at 167 s, while its five tests take
about 26 + 16 + 16 + 2 + 2 s. Test lines name their test, and a test title belongs to exactly one file, so
summing them cannot attribute time to the wrong file.

Balancing also has a limit the weights cannot fix: a file runs in one process, so a shard can finish no
sooner than its heaviest file. `tabs.browser.test.ts` (about 165 s over some 60 short tests) and the
single test "actual App restores, merges, isolates and keeps composer independent" in
`session-list.browser.test.ts` (104 to 124 s by itself) set the floor for the web job, whatever the list
says. Making the web job faster than that means splitting those files or that test, not reweighting.

## When to Apply

- A web shard's time drifts far from the others in the job timings
  (`gh run view <run-id> --json jobs`).
- Test files were added, removed, or grew a lot since the list was last measured.
- Before splitting a big browser test file, to see which file actually bounds the job.

## Examples

A merge queue run before the refresh (run 44847803122): web (1) 65 s, web (3) 145 s, web (0) 164 s,
web (2) 188 s. Simulating the split with measured seconds:

| Weights | Shard sums of measured seconds | Slowest shard (2 processes) |
|---|---|---|
| Old list | about 214, 108, 234, 211 | about 165 s (tabs) |
| Measured list | about 192 each | about 165 s (tabs) |

The measured list evens out the shards, and the slowest shard is still bound by `tabs.browser.test.ts`.

## Related

- `docs/solutions/best-practices/web-test-failures-on-a-busy-machine-need-per-file-reruns.md`: the same
  interleaving misleads local `make test` failure triage, where four `bun test` processes share one output.
  It finds a failing test's file by its title, as this doc does for durations.
- `docs/solutions/best-practices/race-built-test-binary-helpers-sleep-a-second-on-clean-exit.md`: another
  cut to a CI job the merge queue waited on.
- How long a pull request waits in the merge queue also depends on the `merge-queue` ruleset's
  `max_entries_to_build`. At 1, entries are tested one at a time, so each pull request waits for every
  entry ahead of it to finish its whole run, and that wait grows with the queue's length far more than any
  one job's time does. It was 1 until November 2026, when it was raised to 3 so the queue tests up to
  three entries at once.
