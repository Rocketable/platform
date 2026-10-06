---
title: Coverage Baseline Is the Working-Copy Parent
date: 2026-10-06
category: docs/solutions/best-practices/
module: internal/rocketclaw
problem_type: workflow_issue
component: development_workflow
severity: medium
applies_when:
  - Running make test in internal/rocketclaw after committing the change under test
  - The working-copy change @ is empty and @- is your own last commit
  - Several commits sit between trunk and @
symptoms:
  - "make test prints coverage budget ok with current equal to base"
  - Coverage drops introduced by the change never fail the budget
related_components:
  - internal/rocketclaw/Makefile
tags:
  - coverage
  - make-test
  - jj
  - baseline
  - check-coverage
---

# Coverage Baseline Is the Working-Copy Parent

## Context

`make test` in `internal/rocketclaw` ends with `check-coverage`, which fails when coverage drops below both 90% and a baseline. The baseline is the coverage of `@-`, the working-copy change's parent:

```make
base_revision=$$(jj log -r @- --no-graph -T commit_id); \
...
jj --quiet workspace add --revision @- "$$base_dir"; \
```

The check assumes your change is still in `@` and that `@-` is where you started. RocketClaw work often breaks that assumption. `ce-work` commits each unit as it finishes, so by the time `make test` runs, `@` is empty and `@-` is the last commit of your own change. The baseline then already includes your change, and the check compares the change with itself.

This happened while moving `rocketclaw_start_new_thread` to Web sessions (bookmark `rocketclaw-new-thread-tool`). Four commits sat on top of trunk with an empty `@`, so a plain `make test` would have measured coverage against the stack's own tip.

## Guidance

Before `make test`, make `@-` the revision the change started from and put the whole change in `@`:

```sh
tip=$(jj log -r @- --no-graph -T commit_id)   # your finished stack, @ empty
jj new <base-commit> -m "scratch: make test against base"
jj restore --from "$tip"                      # @ now holds the whole change
jj diff --from @ --to "$tip" --stat           # must report 0 files changed
make test
jj abandon @                                  # drop the scratch change
jj new "$tip"                                 # return to the stack
```

Use the commit the bookmark was actually based on, not local `main`. Local `main` can move while you work (other sessions push), and restoring onto a newer `main` reverses their commits inside your scratch diff. `jj log -r 'heads(::@ & ::main@origin)'` gives the merge base.

## Why This Matters

With the wrong baseline, `check-coverage` always prints `coverage budget ok` and a real coverage drop ships unnoticed. The baseline result is also cached in `.tmp/` under the base commit ID, so a run against your own tip stores a misleading number for that commit.

## When to Apply

- Any `make test` run in `internal/rocketclaw` after incremental commits, including `ce-work` and LFG pipelines.
- Not needed when the change is uncommitted in `@` directly on its base.
