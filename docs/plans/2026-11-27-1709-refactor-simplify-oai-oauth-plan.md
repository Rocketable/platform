---
title: Simplify OpenAI OAuth Token Storage and Request Cleanup - Plan
type: refactor
date: 2026-11-27
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Simplify OpenAI OAuth Token Storage and Request Cleanup - Plan

## Goal Capsule

- **Objective:** ChatGPT sign-in, token refresh, and model requests in ChatGPT mode work exactly as they do today, and the token file stays as safe on disk. The code behind them is smaller.
- **Means:** delete the "committed" bookkeeping and the directory-sync seam, the `MissingTokenError` type, and the unreachable `store=true` branch in `internal/rocketclaw/oai/oauth.go` (KTD1, KTD2, KTD3).
- **Authority:** the owner's decisions in the invoking request, then this plan, then the repository's `AGENTS.md`.
- **Execution profile:** one PR with three small units. The only visible change is the accepted one in R2.
- **Stop conditions:** stop and report if production code type-checks `MissingTokenError`, if any request can reach the Codex transport with `store` set to true, or if `rocketclaw oai login` depends on the committed flag.
- **Finishing:** the implementing agent builds, verifies, and opens the PR. The owner merges.

## Product Contract

### Summary

Three pieces of `internal/rocketclaw/oai/oauth.go` exist only for one rare error prefix, for a test-only error type, or for requests nobody sends. Delete them and their dedicated tests and mock.

### Problem Frame

Saving a token threads a `committed` flag through two functions and an injected directory syncer. It exists only so that a directory-sync failure after the rename reads "committed OpenAI OAuth token: …". `MissingTokenError` is exported, but only a test checks its type. The request cleanup reads `store` to decide whether to keep input IDs. Every caller sends `store: false`, and the ChatGPT Codex backend requires it.

### Requirements

**Token file writes**
- R1. Saving or removing a token writes the same temporary file in the same folder with the same permissions, syncs it, renames it into place, then fsyncs the folder, exactly as today.
- R2. If the folder fsync fails after the rename, the error no longer starts with `committed OpenAI OAuth token: `. Every other error message is unchanged.
- R3. `rocketclaw oai login` still restores the config only when the stored token differs from the new one. It re-reads the stored token and never reads the committed flag.

**Missing token**
- R4. When a refresh finds no refresh token for the provider, the error text is unchanged: `OpenAI OAuth token for provider "<name>" is missing refresh token`.

**Request cleanup**
- R5. Requests to the Codex transport always have their input item IDs stripped, as they are today for every real caller. Compaction items keep only `encrypted_content` and `type`.
- R6. When a compaction item is replayed on the retry after a context-length error, it still keeps its ID, as it does today.

### Key Decisions

- **Accept the loss of the "committed" prefix on the rare post-rename sync failure.** (session-settled: user-approved — chosen over keeping the committed flag and syncer seam: they exist for one error prefix nobody reads.) Governs R1, R2.
- **Keep the missing-token message text.** (session-settled: user-approved — chosen over keeping the exported `MissingTokenError` type: only tests check its type.) Governs R4.
- **Always strip input IDs.** (session-settled: user-approved — chosen over keeping the `store=true` branch: no caller sends `store: true`.) Governs R5.

### Scope Boundaries

- Not changing the token lock, the auth file format, or how `LoadTokenIn` reports a missing token.
- Not changing `cleanCodexInputItem`'s keep-ID mode, which the compaction retry uses (R6).
- Not adding a new test seam for the folder fsync failure. That path loses its only test along with the seam. Considered and not built: the failure is rare, still returned to the caller, and only its prefix changed. A reported fsync failure the caller cannot see would change this call.
- Not applying the formal-modeling pack. This removes error bookkeeping from one locked write and adds no state, actor, or retry (pack: formal-modeling, stateful-design.md).

## Planning Contract

### Key Technical Decisions

- KTD1. **`updateAuthFileIn` and `writeAuthFile` return only `error`. The folder fsync moves inline into `writeAuthFile`.** Delete `directorySyncer`, `localDirectorySyncer`, and their method. Keep the open and fsync errors' inner text (`sync OpenAI OAuth token dir: open directory: …`, `… sync directory: …`) so R2 is the only text change. `SaveTokenIn` and `RemoveTokenIn` return the update error directly. Delete the `directorySyncer` entry in `.mockery.yml`, the generated `internal/rocketclaw/oai/directorysyncer_mocks_test.go`, and `TestWriteAuthFileReportsPostRenameFailuresAsCommitted`. `writeAuthFile` stays as its own function: its five errors share the `write OpenAI OAuth token:` wrapper in `updateAuthFileIn`, and inlining it would repeat that prefix five times. (session-settled: user-approved — chosen over keeping the committed flag and syncer seam: they exist for one error prefix.)
- KTD2. **Delete `MissingTokenError`. `freshTokenIn` returns `fmt.Errorf` with the same text and no wrapped error.** `updateAuthFileIn` returns the callback's error unwrapped, so the transport's message stays identical (R4). The old type also unwrapped to `os.ErrNotExist`. No production code checks that on transport errors: the only `os.ErrNotExist` checks near OAuth are `HasTokenIn` and `updateAuthFileIn`, which see `LoadTokenIn` and `readAuthFile` errors, never this one. Reusing `LoadTokenIn`'s error instead would append `: file does not exist` and change the text. `TestFreshTokenInReportsMissingToken` asserts the exact text instead of the type. (session-settled: user-approved — chosen over keeping the exported type: only tests check it.) Conflict call-out: the request calls this a repeat of `LoadTokenIn`'s error, but the two texts differ by that `: file does not exist` suffix. Keeping the text identical wins, so the `os.ErrNotExist` chain goes.
- KTD3. **Delete the `store` read in `cleanCodexRequest` and always ask `cleanCodexInputItem` to strip IDs.** Verified callers of the transport, which `oai.NewChatGPTClientIn` builds only from `backend/model_resolver.go`: `harness/looper.go` (Store false), `harness/title.go` (Store false), `backend/model_resolver.go` `TestModel` (Store false), the looper's `/responses/compact` call (`ResponseCompactParams` has no `store` field, so it already strips), and the transport's own compaction request (`"store": false`). The `stripIDs` parameter stays for the compaction retry's keep-ID call (R6). Delete `TestStripInputIDsKeepsIDsWhenStoreTrue`. A prior live check of the Codex backend sent its follow-up with `store:false` and the ID stripped (docs/solutions/runtime-errors/chatgpt-codex-retired-responses-compact.md). (session-settled: user-approved — chosen over keeping the branch: no caller sends `store: true`.)

## Implementation Units

### U1. Remove the committed flag and the directory syncer

- **Goal:** R1, R2, R3.
- **Requirements:** R1, R2, R3, KTD1.
- **Dependencies:** none.
- **Files:** `internal/rocketclaw/oai/oauth.go`, `internal/rocketclaw/oai/oauth_test.go`, `internal/rocketclaw/oai/directorysyncer_mocks_test.go` (delete), `.mockery.yml`.
- **Approach:**
  1. Make `updateAuthFileIn` and `writeAuthFile` return only an error. Drop the syncer parameter, and open and fsync the folder inline after the rename.
  2. Make `SaveTokenIn` return the update error directly. Update `RemoveTokenIn` and `freshTokenIn` to the new signature.
  3. Delete the syncer types, the mock, its mockery entry, and the committed test.
  4. Confirm that `cmd/rocketclaw/oai.go` `saveOAILogin` uses only `SaveTokenIn`'s error and the re-read token (R3).
- **Test scenarios:** Test expectation: none -- deletion of bookkeeping. The existing save, remove, and refresh tests keep covering the write path, and `TestSaveTokenReportsWriteError` keeps the `write OpenAI OAuth token` wrapper.
- **Verification:** `rg 'directorySyncer|committed' internal/rocketclaw/oai .mockery.yml` finds nothing, and the save path still renames before the folder fsync.

### U2. Delete MissingTokenError

- **Goal:** R4.
- **Requirements:** R4, KTD2.
- **Dependencies:** U1 (same function signature).
- **Files:** `internal/rocketclaw/oai/oauth.go`, `internal/rocketclaw/oai/oauth_test.go`.
- **Approach:** replace the type with an `fmt.Errorf` of the same text in `freshTokenIn`, and delete the type and its methods.
- **Test scenarios:**
  - `TestFreshTokenInReportsMissingToken`: a saved `openai` token and a refresh for `work` returns exactly `OpenAI OAuth token for provider "work" is missing refresh token`.
- **Verification:** `rg MissingTokenError` across `cmd` and `internal` finds nothing.

### U3. Always strip input IDs

- **Goal:** R5, R6.
- **Requirements:** R5, R6, KTD3.
- **Dependencies:** none.
- **Files:** `internal/rocketclaw/oai/oauth.go`, `internal/rocketclaw/oai/oauth_test.go`.
- **Approach:** delete the `store` variable and its unmarshal, and pass the strip mode to `cleanCodexInputItem` in the input loop. Delete `TestStripInputIDsKeepsIDsWhenStoreTrue`.
- **Test scenarios:**
  - The existing `cleanCodexRequest` tests keep proving that message and function-call IDs are removed and compaction items are reduced to `encrypted_content` and `type`.
  - The existing compaction-retry test keeps proving that the replayed compaction item keeps its ID.
- **Verification:** `rg '"store"' internal/rocketclaw/oai/oauth.go` finds only the transport's own compaction request body.

## Verification Contract

| Gate | Command |
|---|---|
| Format | `gofmt -l` on touched files prints nothing |
| Lint | `make -C internal/rocketclaw lint` |
| Tests | `make -C internal/rocketclaw coverage-profile GOTESTPKGS="./oai github.com/Rocketable/platform/cmd/rocketclaw"` |
| Size | `make -C internal/rocketclaw check-cloc-budget`, with source CLOC below the 45269 baseline |

## Definition of Done

- U1 to U3 land in one commit in one PR, with every gate above passing.
- Non-test source lines go down from 45269.
- R1 to R6 are rechecked against the diff. The temporary-file permissions, rename, and folder fsync are unchanged, and R2 is the only message text change.
- No leftover code from approaches that were tried and abandoned.
