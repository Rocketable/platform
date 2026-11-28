---
title: Drop Old Replay History Shapes - Plan
type: refactor
date: 2026-11-27
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Drop Old Replay History Shapes - Plan

## Goal Capsule

- **Objective:** a chat that switches between OpenAI and another provider carries the same readable history as today. The harness keeps less code for history shapes it no longer writes.
- **Means:** delete the readers for old compaction and reasoning-summary shapes in the harness replay code. Read only the shapes the current writer produces (KTD1, KTD2).
- **Authority:** the owner's decisions (Product Contract Key Decisions) win, then this plan, then `AGENTS.md`.
- **Stop conditions:** stop if the change must touch how live provider output is read (`responseOutputToReplayInput`, the compacted-output reader in `internal/rocketclaw/harness/looper.go`, or `internal/rocketclaw/oai/oauth.go`), if an old-shape item would make loading a session fail, or if source CLOC does not go down.
- **Execution profile:** one PR, one commit.
- **Finishing:** the implementing agent finishes, verifies, and opens the PR. The owner merges.

## Product Contract

### Summary

The harness stores compaction items as `type:"compaction"` with a string `content`, and reasoning summaries as an array of `{text}` parts. Delete the code that also reads `compaction_summary` items, a compaction `summary` or `recent` field, and reasoning summaries stored as a bare string or a single object. Old items in those shapes are skipped when history is converted for another provider.

### Problem Frame

Since the 2026-10-29 fold, `compactionReplayInput` and `reasoningReplayInput` in `internal/rocketclaw/harness/looper.go` are the only writers of stored compaction and reasoning rows. The replay readers still accept four older shapes, which keeps a summary fallback, a three-way text decoder, and several tests alive. Stored history older than 90 days is deleted, so these readers serve a shrinking set of old chats.

### Requirements

**Current history converts exactly as today**

- R1. When a turn written by another provider is converted, a `compaction` item with string `content` becomes one assistant message with that text, and a blank or missing `content` yields nothing.
- R2. When a turn written by another provider is converted, a `reasoning` item's summary array becomes one assistant message per non-blank part, in order.
- R3. Same-provider replay keeps a compaction item's `content` and `kept` fields. The context checkpoint text built from a compaction item is unchanged for current items.

**Old shapes are skipped, never fatal**

- R4. A `compaction_summary` item, a compaction item whose text is only in `summary`, and a reasoning item whose summary is a string or a single object convert to nothing. Loading the session does not fail and does not panic.
- R5. Live provider output is read as today, including `compaction_summary` items from live responses.
- R6. Store handling is untouched. Owner rule, word for word: "Store must always be false."

### Key Decisions

- **Remove the old-shape readers instead of keeping them.** (session-settled: user-approved — chosen over keeping backward-compatible readers: the owner said "most changes are forward looking. I'm OK with some odd behavior with old data because in 90 days it will be gone.") Governs R1, R2, R4.
- **Skip old items rather than fail.** (session-settled: user-approved — chosen over returning a decode error: an error from conversion fails the whole session load, and the accepted effect is that old items are skipped.) Governs R4.
- **Leave live-output normalization alone.** (session-settled: user-directed — chosen over also removing `compaction_summary` from the live readers: those read fresh provider responses, not stored rows.) Governs R5.

### Scope Boundaries

- Live-output readers stay as they are: `responseOutputToReplayInput` and `compactedOutputToReplayParams` in `internal/rocketclaw/harness/looper.go`, and the Codex normalization in `internal/rocketclaw/oai/oauth.go`. Their tests stay too, including the `compaction_summary` cases in `TestResponseOutputReplayText`.
- No migration or rewrite of stored rows.
- Not built: a decode error for current-shape items with the wrong field types. The only writer produces the right types, and an error would fail the whole session load. Revisit only if a second writer appears.
- Same-provider replay of a compaction whose `content` is not a string already fails in `ReplayInputToParams`. This plan does not change that.
- README does not describe stored history shapes, so it needs no change.

## Planning Contract

### Key Technical Decisions

- KTD1. **Read stored compaction and reasoning rows with typed structs, and skip on decode failure.** In `forProvider`, the `reasoning` case decodes the summary as a list of `{text}` parts. The `compaction` case decodes `content` as a string. If either decode fails, the item yields no text. The item's JSON already parsed when its `type` was read, so a decode failure here can only be a field of an old or unexpected type. `readableText`, `compactionText`, and `nonblankText` go away. The blank-text filter moves into the loop that turns texts into messages. Governs R1, R2, R4.
- KTD2. **Keep only `content` and `kept` as compaction extras in `ReplayInputToParams`.** Drop the `summary` and `recent` fields. An old row that has them still decodes, and only those fields are lost. Governs R3, R4.
- KTD3. **The checkpoint text reads `content` from the compaction's extras directly.** `CompactionCheckpointText` no longer marshals the item and calls `compactionText`. It reads the `content` string from `ExtraFields()`, trims it, and uses the same two messages as today. Governs R3.

### Assumptions

- Current reasoning rows always have an array summary, because `reasoningReplayInput` writes at least one part. Current compaction rows have a string `content` or none.
- `TestReplayInputPreservesCompactionPayload` also covers the current `content` and `kept` round trip. Its fixture is trimmed to the current shape instead of being deleted.

## Implementation Units

### U1. Remove old-shape readers and their tests

- **Goal:** delete the old-shape branches and keep current shapes converting as today.
- **Requirements:** R1, R2, R3, R4, R5; KTD1, KTD2, KTD3.
- **Dependencies:** none.
- **Files:**
  - `internal/rocketclaw/harness/provider_replay.go`
  - `internal/rocketclaw/harness/replay.go`
  - `internal/rocketclaw/harness/looper.go` (`CompactionCheckpointText` only)
  - `internal/rocketclaw/harness/provider_replay_test.go`
  - `internal/rocketclaw/harness/replay_test.go`
  - `internal/rocketclaw/harness/models_test.go`
- **Approach:**
  1. In `forProvider`, narrow the compaction case to `compaction` and apply KTD1 to both cases. Delete `compactionText`, `readableText`, `nonblankText`, and `replayReasoning` once nothing uses them.
  2. In `ReplayInputToParams`, drop the `Summary` and `Recent` fields and their extras (KTD2).
  3. Rewrite `CompactionCheckpointText` per KTD3.
  4. Tests: delete `TestReplayForProviderReadsExactSummaryShapesAndPrefersCompactionContent` and `TestReplayForProviderCompactionFallsBackAfterBlankContent`. Remove the reasoning and compaction rows from the error table in `TestReplayForProviderRejectsMalformedKnownReadableData`, and add the old shapes to the no-error loop at its end. Trim the fixtures in `TestReplayInputPreservesCompactionPayload`, `TestSessionEntryForProviderDifferentProviderProjectsReplay`, and the OpenAI private-field test in `models_test.go` to current shapes, and drop assertions that only made sense for the removed fields.
- **Patterns to follow:** the existing typed-struct decodes in `forProvider` (`replayMessage`, `replayFunctionOutput`).
- **Test scenarios:**
  - Covered by `TestSessionEntryForProviderDifferentProviderProjectsReplay`: a reasoning array with a blank middle part yields two messages, a compaction with string `content` yields its text, and empty reasoning and compaction yield nothing.
  - In the no-error loop of `TestReplayForProviderRejectsMalformedKnownReadableData`: `{"type":"reasoning","summary":"old"}`, `{"type":"compaction","summary":{"text":"old"}}`, and `{"type":"compaction_summary","content":"old"}` each convert to an empty replay with no error.
  - `TestReplayInputPreservesCompactionPayload`, with a fixture holding only `content`, `kept`, `encrypted_content`, `id`, and `type`, round-trips byte for byte and exposes `kept` as 2.
  - Existing permission-review and bridge tests that call `CompactionCheckpointText` still see the compaction text.
- **Verification:** the harness and backend packages pass, and no remaining code mentions the removed readers.

## Verification Contract

| Check | Command |
|---|---|
| Format | `gofmt -l` on touched files prints nothing |
| Lint | `make -C internal/rocketclaw lint` |
| Tests | `make -C internal/rocketclaw coverage-profile GOTESTPKGS="./harness ./backend"` |
| Size | `make -C internal/rocketclaw check-cloc-budget`, with source CLOC lower than before |
| Panic safety | the old-shape rows in the no-error loop pass through `forProvider` without error |

CI runs the full suite on the PR.

## Definition of Done

- R1 to R6 hold, and each Verification Contract check passes.
- Source CLOC is lower than at `main@origin`.
- The diff has no abandoned code, no new helpers, and no comments.
- The PR text names no internal deployments, people, or systems.
