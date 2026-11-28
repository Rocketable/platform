---
title: Refuse Single-Token auth.json - Plan
type: refactor
date: 2026-11-27
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Refuse Single-Token auth.json - Plan

## Goal Capsule

- **Objective:** RocketClaw reads ChatGPT OAuth credentials in one file format only, and a person with an old single-token `auth.json` gets a clear refusal that tells them how to recover.
- **Means:** delete the single-token fallback in the auth file reader and refuse any file without a `providers` object (KTD1).
- **Authority:** the owner's decision (Product Contract Key Decisions) wins, then this plan, then repository conventions in `AGENTS.md`.
- **Stop conditions:** stop if the change needs a new error type, touches store handling, or changes how `auth.json` is written.
- **Execution profile:** one PR, one commit.
- **Finishing:** the implementing agent finishes, verifies, and opens the PR. The owner merges.

## Product Contract

### Summary

Delete the code that reads an `auth.json` holding one bare token as the `openai` provider. A file without a `providers` object is refused with an error naming the file and the recovery step. Writing is unchanged.

### Problem Frame

Before named providers, `auth.json` held one token. Every token refresh, login, and logout since then rewrites the file as `{"providers": {...}}`, so the single-token reader only serves files nothing has touched since late July. It keeps a second parse path, a raw-field probe, and three tests alive for that one case.

### Requirements

- R1. An `auth.json` without a `providers` object is refused when read, for every caller: model requests, `oai list`, `oai login`, and `oai logout`.
- R2. The refusal names the file and says to remove it and run `rocketclaw oai login`.
- R3. Files with a `providers` object read and write exactly as before.
- R4. Store handling is untouched. Owner rule, word for word: "Store must always be false."

### Key Decisions

- **Drop single-token support instead of keeping or migrating it.** (session-settled: user-approved — chosen over keeping the fallback reader: every refresh already rewrites the file, so only untouched pre-July files are affected, and their owners can log in again.) Governs R1, R3.
- **Refusing in `oai login` and `oai logout` is acceptable.** (session-settled: user-directed — chosen over making those commands repair the file: the owner said they must not crash and refusing is fine.) Governs R1.

### Scope Boundaries

- Writing `auth.json`, locking, and atomic replace are unchanged.
- No migration, no automatic rewrite, no new error type.
- Not built: letting `oai login` overwrite an unreadable file. It would also discard every other provider in a corrupted file, and the owner accepted refusal. Revisit only if people report being stuck.
- README does not describe the old format, so it needs no change. Historical documents under `docs/plans/` and `docs/investigations/` stay as written.

## Planning Contract

### Key Technical Decisions

- KTD1. **One nil check replaces the fallback.** After the existing unmarshal, a nil `Providers` map is refused. This covers both a missing key (the old format) and `"providers": null`, so the separate raw-field probe and the second unmarshal into `Token` go away. An empty object `{}` still decodes to an empty, non-nil map and stays valid. Governs R1, R3.
- KTD2. **Reuse the existing wording.** The error keeps the `parse OpenAI OAuth token:` prefix and the `providers must be an object` phrase from the current null-providers error, adds the file path and the recovery step, and stays a plain `fmt.Errorf`. Governs R2.

### Assumptions

- The recovery step is "remove the file, then log in", not "log in" alone. `oai login` and `oai logout` read the file before writing it and refuse when the read fails, so running `oai login` on its own does not get past an old file. The decision said only "runs `rocketclaw oai login` again", so the error text spells out the extra step.
- After an old file is refused, `oai login` restores the config file it already changed, because `saveOAILogin` restores when the saved token does not read back. The person still has to complete the browser or device sign-in before seeing the refusal.

## Implementation Units

### U1. Refuse auth files without a providers object

- **Goal:** delete the single-token fallback and its tests.
- **Requirements:** R1, R2, R3, R4. KTD1, KTD2.
- **Dependencies:** none.
- **Files:**
  - `internal/rocketclaw/oai/oauth.go`
  - `internal/rocketclaw/oai/oauth_test.go`
- **Approach:**
  1. In `readAuthFile`, replace everything after the first unmarshal with one nil check on `Providers` that returns the KTD2 error, then return the file.
  2. Delete `TestLoadTokenInReadsOldTokenAsOpenAI`, `TestLoadTokenInDoesNotUseOldTokenForNamedProvider`, and `TestSaveTokenInPreservesOldOpenAITokenWhenAddingNamedProvider`.
  3. Delete the `missing refresh` row of `TestLoadTokenRejectsInvalidTokenFiles`. Its input `{"access":"access"}` is a single-token file.
- **Patterns to follow:** existing `fmt.Errorf("parse OpenAI OAuth token: %w", err)` style in the same function.
- **Test scenarios:**
  - The kept `null providers` row (`{"providers":null}`) still fails with `providers must be an object`. It passes through the same nil check an old single-token file does.
  - The kept `malformed providers` and `invalid json` rows still fail with `parse OpenAI OAuth token`.
  - Existing multi-provider tests (isolation, removal, concurrent saves, refresh) pass unchanged.
- **Verification:** the oai and cmd/rocketclaw packages pass, and source CLOC goes down.

## Verification Contract

| Check | Command |
| --- | --- |
| Format | `gofmt -l` on touched files prints nothing |
| Lint | `make -C internal/rocketclaw lint` |
| Tests | `make -C internal/rocketclaw coverage-profile GOTESTPKGS="./oai github.com/Rocketable/platform/cmd/rocketclaw"` |
| Size | `make -C internal/rocketclaw check-cloc-budget` passes, and `make -C internal/rocketclaw cloc` source count is below 45230 |

## Definition of Done

- U1 is done and every Verification Contract check passes.
- No store handling, write path, or README text changed.
- No code from abandoned attempts remains in the diff.
