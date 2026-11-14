---
title: Dupword Fix Rewrites Repeated Words in Test Strings and Production SQL
date: 2026-10-05
last_updated: 2026-11-14
category: docs/solutions/best-practices/
module: internal/rocketclaw/backend
problem_type: best_practice
component: development_workflow
severity: high
resolution_type: code_fix
applies_when:
  - A Go string literal holds code, such as a SQL query, a Starlark or shell program, JSON tool arguments or a prompt
  - The literal repeats the same word back to back, such as FALSE, FALSE in a UNION branch or a call repeated in a test
  - make lint or make test runs golangci-lint with --fix in internal/rocketclaw
symptoms:
  - make lint prints 0 issues and exits 0, yet the working copy now holds an edit to a string literal nobody made
  - "A query fails at run time with: each UNION query must have the same number of columns (SQLSTATE 42601)"
  - A test passes alone but fails after make lint or make test
  - Expected output has more entries than the program under test now produces
related_components:
  - internal/rocketclaw/.golangci.yml
  - internal/rocketclaw/Makefile
  - internal/rocketclaw/backend/store.go
tags:
  - dupword
  - golangci-lint
  - lint-fix
  - sql
  - union-all
  - test-fixture
  - make-lint
---

# Dupword Fix Rewrites Repeated Words in Test Strings and Production SQL

## Context

`internal/rocketclaw/.golangci.yml:12` enables `dupword`. `internal/rocketclaw/Makefile:70` runs `golangci-lint run --fix ./...` for `lint`, and `test` depends on `lint` (`Makefile:133`). `dupword` looks for the same word twice in a row, split on whitespace, in comments and in every string literal, raw backtick strings included. With `--fix`, golangci-lint applies the fix and does not report the issue, so the run still prints `0 issues`. In code held in a string, that silent edit changes behavior.

**Production SQL (PR #1117, pause agent in Web chats).** The pause work added `paused` and `pausable` columns to the `local` branch of the sidebar query in `sidebarSessions` (`internal/rocketclaw/backend/store.go`). It filled the matching slots of the `remote` branch with constants, `o.pinned, FALSE, FALSE, o.name`, plus two more `''` slots. `make -C internal/rocketclaw lint` printed `0 issues` and exited 0, but rewrote the `remote` branch:

- `FALSE, FALSE` became one `FALSE`;
- in the same literal, `'', ''` became `''` and `'', '', ''` lost its repeats.

The two halves of `SELECT * FROM local UNION ALL SELECT * FROM remote` (`store.go:1416`) no longer had the same column count. Lint did not catch it. It showed up later, when real-backend RPC tests called `ListSessions` and failed with `query sidebar sessions: database error`. The server log had the cause: `ERROR: each UNION query must have the same number of columns (SQLSTATE 42601)`.

**Test fixture (first case).** `TestRuntimeProducerReportsToDestination` (`internal/rocketclaw/backend/runtime_test.go:82`) had a fake model return a Starlark program that toggled a tag with `rocketclaw_set_tag(tag=\"yellow\"), rocketclaw_set_tag(tag=\"yellow\")`. `--fix` collapsed the repeats, so the program made fewer calls than the expected output listed. The test passed under a focused `go test` and failed only inside `make test`, and the edit looked like another process had touched the file.

## How the fix rewrites a literal

These points come from reading `dupword` v0.1.8 (the version golangci-lint v2.15.0 pins) and from running its analyzer with `-fix` on a scratch package:

- A word that starts with punctuation, a digit or a symbol, such as `''` or `0`, never triggers a finding by itself. `SELECT a, '', '', c` alone is left alone.
- When a literal has at least one real repeat (`FALSE, FALSE`), the fix rewrites the **whole literal**. That rewrite also drops the repeats of punctuation words that would never trigger alone. This is why the `''` runs went missing only once `FALSE, FALSE` was added beside them.
- The rewrite also dropped a final one-letter word: `... b FROM t` came back as `... b FROM`.
- Line breaks do not separate words. `FALSE,` at the end of one line and `FALSE,` at the start of the next still count as a repeat.

So after a `--fix` run, check the whole literal, not just the word the finding names.

## Guidance

- **SQL constants in a UNION branch:** give repeated literals an alias, `FALSE AS paused, FALSE AS pausable` (`store.go:1405`). PostgreSQL takes a UNION's column names from its first branch, so the alias changes nothing, and the two `FALSE` tokens are no longer next to each other. The same `remote` branch already does this with `FALSE AS queued, FALSE AS external_mcp` (`store.go:1408`). Once no real repeat is left, the runs of `''` stay as written.
- **Test fixtures:** keep repeated calls from sitting back to back. Put a different call between them, for example `rocketclaw_get_tags()` between toggles, which also checks each step (`runtime_test.go:111`).
- After `make lint` or `make test`, read `jj diff --git` before committing. An edit inside a string literal that you did not make comes from this linter, not from another writer. A clean `0 issues` does not mean lint left the files alone.
- If the diff shows a string you wrote collapsed, put back the **whole** literal. Then remove the repeat the way shown above, rather than restoring the original text, which the next run would collapse again.
- Do not silence `dupword` with `//nolint` or a config change without approval (`AGENTS.md`). Restructure the literal instead.

## Why This Matters

The broken query was in production code, and every `ListSessions` call would have failed, leaving the Web sidebar empty. Lint gave no warning. The breakage was caught only because tests that run the real sidebar query against PostgreSQL ran after lint. Committing straight after `make lint` would have committed the broken query, leaving CI's database tests as the only catch.

CI does not catch this either. The `checks` job in `.github/workflows/test.yml` runs the same `--fix` target (`test.yml:63`), and its check that the tree is unchanged (`test.yml:60`) runs before lint. So a repeat in a committed literal is fixed only inside the CI checkout and never fails the job. A local `make lint` or `make test`, though, edits your working copy.

A check that would stop this was not added here, because it needs a CI or Makefile change, or a linter-settings change that needs approval:

- fail when lint changes the tree, by running `git diff --exit-code` after the lint step in CI, or by running golangci-lint without `--fix` there;
- or set `dupword`'s `skip-raw-strings: true`, which stops it from checking the backtick strings that hold the SQL.

## When to Apply

This applies to any Go package linted by the `internal/rocketclaw` Makefile targets whose string literals hold code: SQL (especially constant-filled `UNION` branches), Starlark or shell programs, JSON tool arguments and model prompts.

## Examples

```sql
-- Written: two constants side by side in the remote branch
SELECT ..., o.pinned, FALSE, FALSE, o.name, ..., COALESCE(o.reply, ''), '', '', COALESCE(o.model, ''), ...
-- After make lint (--fix), still "0 issues": one FALSE and one '' gone, UNION column count broken
SELECT ..., o.pinned, FALSE, o.name, ..., COALESCE(o.reply, ''), '', COALESCE(o.model, ''), ...
-- Survives lint: aliases break the repeat, so the '' run is not rewritten either
SELECT ..., o.pinned, FALSE AS paused, FALSE AS pausable, o.name, ..., COALESCE(o.reply, ''), '', '', COALESCE(o.model, ''), ...
```

## Related

- `docs/solutions/logic-errors/remote-opencode-fork-marker-dropped-by-sidebar-remote-branch.md`: the same `remote` branch, and why each of its constants must line up by position with the `local` branch.
