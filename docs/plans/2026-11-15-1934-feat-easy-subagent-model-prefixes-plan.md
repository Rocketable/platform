---
title: Prefix entries in easy agents' subagents
type: feat
date: 2026-11-15
execution: code
---

# Prefix entries in easy agents' subagents

## Summary

An easy agent's `subagents` list matches models only exactly today, so an author must list every model and effort a subagent may run on. Let an entry end in `*`: it then allows any model whose reference, `#effort` included, starts with the text before the `*`. Entries without `*` keep exact matching.

## Decisions

- **Trailing `*` marks a prefix entry**, chosen over every entry being a prefix (changes what existing entries allow) and over provider-only entries (too coarse). The `*` may only be the last character; any other `*` fails loading.
- **The prefix covers effort too**: `anthropic/*` allows `anthropic/claude-haiku-4-5` and `anthropic/claude-haiku-4-5#high` alike, chosen over a fixed effort after the `*` (`anthropic/*#high`). Exact entries are how an author pins an effort.

## Units

1. **Matching rule, `internal/rocketclaw/harness`.** One `Agent` method reports whether a model matches `Subagents` (exactly, or by a trailing-`*` prefix). `subagentWrite` in `tasks.go` uses it in place of `slices.Contains`; `compileEasyAgent` in `easy.go` rejects an entry with a `*` that is not its last character. A requested model that contains `*`, or is only the bare prefix, is refused. The `task` description keeps listing entries as written and says once what a trailing `*` means. The existing provider check keeps reading each entry's provider, so `nope/*` still fails loading.
   - Tests: `easy_test.go` `TestLoadEasyAgentErrors` gains a misplaced-`*` row and an unconfigured prefix provider row; a matching table covers prefix entries accepting models with and without `#effort`, and refusing models outside the prefix, the entry text itself and the bare prefix, plus the description line.
2. **Docs.** README easy-mode `task` bullet and example, `cmd/rocketclaw/CHEATSHEET.md` `subagents` row and `task` paragraph, the Web editor help for Helper models, and the skel skill `main-create-or-update-agent` (`subagents` line).

## Assumptions

- A prefix entry whose text has no `/` (for example `*` or `gpt-*`) is checked against the `openai` provider at load, as unqualified names are today; a matched model from an unconfigured provider fails when the subagent's model is resolved, as an unknown model does now.
- RocketClaw has no catalog of provider models, so the agent fills in a concrete model name under a prefix; the description says so.

## Verification

`make -C internal/rocketclaw test` and `make lint`, plus `gofmt`.
