---
title: Dupword Fix Rewrites Repeated Test Strings
date: 2026-10-05
category: docs/solutions/best-practices/
module: internal/rocketclaw/backend
problem_type: best_practice
component: development_workflow
severity: medium
resolution_type: test_fix
applies_when:
  - A Go test embeds a program, script, or prompt as a string literal
  - The literal repeats the same word or call back to back, such as toggling a tag twice
  - make lint or make test runs golangci-lint with --fix in internal/rocketclaw
symptoms:
  - A test passes alone but fails after make lint or make test
  - The working copy shows an edit to a test string nobody made
  - Expected output has more entries than the program under test now produces
related_components:
  - internal/rocketclaw/.golangci.yml
  - internal/rocketclaw/Makefile
tags:
  - dupword
  - golangci-lint
  - lint-fix
  - test-fixture
  - make-lint
---

# Dupword Fix Rewrites Repeated Test Strings

## Context

`internal/rocketclaw/.golangci.yml` enables `dupword`, and `internal/rocketclaw/Makefile` runs `golangci-lint run --fix ./...` in both `lint` and `test`. `dupword` flags consecutive duplicate words in comments and string literals, and its fix deletes the duplicate. Inside a test fixture that is code, not prose, the deletion silently changes behavior.

It happened while adding producer-turn tag coverage to `TestRuntimeProducerKeepsDestinationUntilSync` (`internal/rocketclaw/backend/runtime_test.go`), pending on the `alex-desiderata` change. The fake model returned a Starlark program as a JSON string that toggled a tag off and on:

```text
rocketclaw_set_tag(tag=\"yellow\"), rocketclaw_set_tag(tag=\"yellow\"), rocketclaw_set_tag(tag=\"yellow\")
```

`--fix` collapsed the repeats, so the program made fewer calls while the expected output still listed every toggle. The test passed under a focused `go test` and failed only inside `make test`. Because the edit lands in the working copy mid-run, it looked like another process had touched the file.

## Guidance

- Keep repeated calls in a fixture string from appearing back to back. Interleave a different call, for example a `rocketclaw_get_tags()` read between toggles, which also asserts each intermediate state.
- After `make lint` or `make test`, inspect `jj diff` before committing. An unexpected edit inside a string literal is this linter, not a concurrent writer.
- Do not suppress `dupword` with `//nolint` without approval (`AGENTS.md`); restructure the fixture instead.

## Applicability

Applies to any Go package linted by the `internal/rocketclaw` Makefile targets where a string literal holds executable content: Starlark or shell programs, JSON tool arguments, model prompts, or SQL.
