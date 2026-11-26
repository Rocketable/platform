---
title: Parse oai login Arguments with flag.FlagSet - Plan
type: refactor
date: 2026-11-26
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Parse oai login Arguments with flag.FlagSet - Plan

## Goal Capsule

- **Objective:** `rocketclaw oai login` keeps accepting every invocation it accepts today (provider and options in any order, `openai` by default, `-h`/`--help`, one provider at most, unknown flags rejected), and its argument handling is less code that reads like `oai list` and `oai logout`.
- **Means:** replace the hand-written `parseOAILogin` with a standard `flag.FlagSet` parsed in two passes around the positional provider (KTD1, KTD2).
- **Authority:** the owner's approved decision > this plan's Requirements > KTDs > unit Approach. `AGENTS.md` governs style, tests, verification and the CLOC budget.
- **Stop conditions:** stop and report, without opening a PR, if the change does not lower the source CLOC (`make -C internal/rocketclaw cloc`). Stop and send the question back if building raises a product question the decision does not answer.
- **Execution profile:** one PR with one commit.
- **Finishing:** `lfg` implements, reviews and opens the PR. Merging stays with the user.

---

## Product Contract

### Summary

Delete `parseOAILogin` in `cmd/rocketclaw/oai.go` and let `runOAILogin` parse its arguments with `flag.FlagSet`, as `runOAIList` and `runOAILogout` do. A second parse after the provider keeps options valid on either side of it. Duplicate options stop being errors, and the tests shrink to tables over the kept behaviors.

### Problem Frame

`oai login` is the only `oai` subcommand with its own argument parser. It re-implements value flags, `=` forms, help and unknown-flag detection, and adds duplicate-option errors that no other command has. That is about 30 lines of code and a set of tests that exist only to cover the parser itself.

### Requirements

**Kept behavior**

- R1. The provider and the `--headless` / `--aws-secrets-manager-arn` options may appear in any order: `oai login openai --headless` and `oai login --headless openai` both work.
- R2. With no provider argument, login uses `openai`.
- R3. `-h` or `--help` prints the `oai` help text and exits without logging in, wherever it appears, unless an earlier argument is already an error (an unknown flag, a missing value, or a second provider).
- R4. More than one provider is an error ("oai login accepts no more than one provider").
- R5. An unknown flag is an error, and so is `--aws-secrets-manager-arn` with no value.

**Accepted change**

- R6. Giving the same option twice is no longer an error; the last value wins, as with the standard `flag` package.

**Size**

- R7. Non-test source lines go down.

### Key Decisions

- **Duplicate options follow standard flag behavior.** Owner-approved; the custom duplicate errors and their tests go. Governs R6. (session-settled: user-approved — chosen over keeping duplicate-option rejection: standard flag behavior is simpler and matches the other `oai` commands)
- **Keep the listed behaviors.** Any order, default provider, help, one provider, unknown-flag rejection stay. Governs R1, R2, R3, R4, R5. (session-settled: user-directed — chosen over a single FlagSet pass that would require options before the provider: both orders must keep working)
- **The change must shrink the code.** Governs R7. (session-settled: user-directed — chosen over a same-size rewrite: the point of the change is less code)

### Scope Boundaries

- `runOAIList`, `runOAILogout`, the help text and the top-level `helpText` stay unchanged.
- Considered and not built: a shared parser helper for all three `oai` commands. Only login needs a second pass and help printing, so a helper would add a concept without removing lines.

---

## Planning Contract

### Key Technical Decisions

- KTD1. **One `flag.FlagSet` parsed twice.** Parse the arguments once; if a positional argument remains, take it as the provider and parse what follows it again with the same FlagSet. Anything still left after the second pass is a second provider (R4). Flags set in either pass land in the same variables, which keeps R1 and gives R6 for free. (session-settled: user-approved — chosen over keeping the hand-written loop: the stdlib parser already handles value flags, `=` forms and unknown flags. Governs R1, R4, R6)
- KTD2. **Help is `flag.ErrHelp`.** With output discarded, the FlagSet returns `flag.ErrHelp` for `-h`/`--help`; `runOAILogin` prints `oaiHelpText` for that error and wraps any other parse error as `parse oai login flags: …`, matching `oai logout`. Governs R3, R5.
- KTD3. **Tests go through `runOAILogin`.** With `parseOAILogin` gone, `secrets_cli_test.go` drops its direct parser assertions and keeps calling `runOAILogin`. Near-duplicate argument tests in `oai_test.go` become two table tests: one for help and rejection cases, one for which provider gets logged in.

### High-Level Technical Design

Directional sketch of the parse order in `runOAILogin`:

```text
parse(args)                       -> flags before the provider
provider = first leftover or "openai"
if leftovers: parse(leftovers[1:]) -> flags after the provider
ErrHelp          -> print help, return
other parse err  -> "parse oai login flags: ..."
leftovers remain -> "oai login accepts no more than one provider"
```

### Assumptions

- Standard `flag` syntax now also applies to `oai login`, as it already does for `oai list` and `oai logout`: single-dash forms (`-headless`), `--headless=false`, and `--` as an end-of-flags marker are accepted. The owner's decision to use `flag.FlagSet` "the way `oai list` and `oai logout` already parse" covers this.
- The unknown-flag message becomes the `flag` package's (`parse oai login flags: flag provided but not defined: -bad`) instead of the custom `unknown flag "--bad"`. The decision deletes the custom error messages; rejection itself stays (R5).

---

## Implementation Units

### U1. Replace parseOAILogin with flag.FlagSet

- **Goal:** `runOAILogin` parses its own arguments with `flag.FlagSet`; `parseOAILogin` is deleted.
- **Requirements:** R1–R7, KTD1, KTD2.
- **Dependencies:** none.
- **Files:** `cmd/rocketclaw/oai.go`, `cmd/rocketclaw/oai_test.go`, `cmd/rocketclaw/secrets_cli_test.go`.
- **Approach:**
  1. In `runOAILogin`, build a FlagSet named `oai login` with output discarded, a `headless` bool and the shared `secretsARNFlag` string, and parse in two passes per KTD1.
  2. Handle `flag.ErrHelp`, other parse errors and extra providers per KTD2 and R4.
  3. Delete `parseOAILogin` and any import it alone used.
  4. Rewrite the parser tests per KTD3.
- **Patterns to follow:** `runOAILogout` in `cmd/rocketclaw/oai.go`.
- **Test scenarios:**
  - `-h` prints the help text.
  - `work -h` prints the help text (help after the provider, second pass).
  - `--headless --help --bad` prints the help text (help stops parsing before the later bad flag).
  - `--bad --help` fails with the flag package's unknown-flag error.
  - `work openai -h` fails with "more than one provider".
  - `--aws-secrets-manager-arn` alone fails with "flag needs an argument: -aws-secrets-manager-arn".
  - `--headless` logs in `openai` (default provider).
  - `--headless work` logs in `work` (option before provider).
  - `help --headless` logs in the provider named `help` (option after provider; a bare word is a provider, not help).
  - `--aws-secrets-manager-arn ARN work --headless` still logs in with the ARN given before the provider in `TestCommandsAcceptSecretsARNFlag`; `TestRunOAILoginRejectsBeforeOAuth` keeps proving an ARN placed after the provider is read.
- **Verification:** the `cmd/rocketclaw` package tests pass, and the duplicate-option tests no longer exist.

---

## Verification Contract

| Check | Command | Applies |
|---|---|---|
| Format | `gofmt -l cmd/rocketclaw` prints nothing | always |
| Lint | `make -C internal/rocketclaw lint` | always |
| Package tests | `make -C internal/rocketclaw coverage-profile GOTESTPKGS=github.com/Rocketable/platform/cmd/rocketclaw` | always |
| Size budget | `make -C internal/rocketclaw check-cloc-budget` | always |
| Size drop | `make -C internal/rocketclaw cloc` source CLOC below the 45148 measured before the change | always (R7) |

## Definition of Done

- `parseOAILogin` and its duplicate-option errors are gone; `runOAILogin` uses `flag.FlagSet`.
- Every Requirement holds, checked against the U1 test scenarios.
- Source CLOC is lower than before; every check in the Verification Contract passes.
- No dead code, comments or abandoned attempts remain in the diff.
