---
title: Stop Re-Reading Pull Request Links From Old Bash Calls - Plan
type: refactor
date: 2026-11-27
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Stop Re-Reading Pull Request Links From Old Bash Calls - Plan

## Goal Capsule

- **Objective:** the PR pane keeps listing every pull request a bash call created or mentioned since those links started being recorded, with less code behind it.
- **Means:** delete `CodeModeCall.PullRequests` and have History read the recorded `CreatedPullRequests` and `MentionedPullRequests` fields directly (KTD1).
- **Authority:** the owner's decision in the invoking request, then this plan, then the repository's `AGENTS.md`.
- **Execution profile:** one PR, one unit.
- **Stop conditions:** stop and report if source CLOC does not go down from 45186, or if some path other than History's transcript builder reads `PullRequests()`.
- **Finishing:** the implementing agent builds, verifies, and opens the PR. The owner merges.

## Product Contract

### Summary

Remove the History-time fallback that re-parses a bash call's arguments and summary for pull request links when the stored record has none. The transcript builder copies the two recorded lists as they are. The test that only covered the fallback is deleted, and the solution note that describes the fallback is updated.

### Problem Frame

Bash calls record the pull requests they created or mentioned when they finish. Records written before that recording existed have no lists, so `CodeModeCall.PullRequests` re-reads the clipped command and the summary line on every History read. Those records age out, and the fallback costs a method, a branch with five conditions, and a five-row test.

### Requirements

- R1. History's code mode calls carry `created_pull_requests` and `mentioned_pull_requests` exactly as recorded on the call.
- R2. Bash calls keep classifying and recording pull request links when they finish.
- R3. Tests and documentation that only describe the fallback are removed or updated; coverage of recording stays.

### Key Decisions

- **No re-parsing of old records.** (session-settled: user-approved — chosen over keeping the fallback for records written before links were recorded: those records age out within 90 days and the owner accepts the PR pane missing their links.) Governs R1, R3.

### Scope Boundaries

- `PullRequestsIn` stays: `CodeModeCall.finish` records with it and `FindPullRequests` in `internal/rocketclaw/frontend/rpc/session_commands.go` classifies OpenCode runs with it.
- No migration or backfill of old records.
- The web client already reads the two fields as sent; nothing changes there.

## Planning Contract

### Key Technical Decisions

- KTD1. **Delete the method rather than keep a getter.** Without the fallback it would return two fields unchanged, a one-line delegating wrapper with one caller, which `AGENTS.md` forbids. `TranscriptEvent.codeModeCall` reads `call.CreatedPullRequests` and `call.MentionedPullRequests` inline. Implements the Key Decision; governs R1.

### Assumptions

- The stateful-design pack (pack: formal-modeling, stateful-design.md) does not apply: no queue, worker or lifecycle changes.

## Implementation Units

### U1. History reads recorded pull request links directly

- **Goal:** remove the fallback and its test.
- **Requirements:** R1, R2, R3; KTD1.
- **Dependencies:** none.
- **Files:** `internal/rocketclaw/harness/public_progress.go`, `internal/rocketclaw/harness/public_progress_test.go`, `internal/rocketclaw/frontend/rpc/server.go`, `docs/solutions/integration-issues/pr-pane-missed-prs-in-jj-workspaces-and-subagent-sessions.md`.
- **Approach:**
  1. Delete `CodeModeCall.PullRequests` in `public_progress.go`; drop any import it alone used.
  2. In `TranscriptEvent.codeModeCall` in `server.go`, fill the two proto fields from the call's recorded fields.
  3. Delete `TestCodeModeCallPullRequestsOfStoredRecords`.
  4. In the solution note, drop the "native records saved before round 2" caller, the fallback test reference, and the "old records still go through the last-line fallback" sentence, and say old records now show no links.
- **Test scenarios:**
  - `TestCodeModeCallPullRequests` and `TestCodeModeCallPullRequestsNeedSuccessfulBash` still pass: recording at finish is unchanged.
  - The live History test in `internal/rocketclaw/frontend/rpc/live_test.go` still returns the recorded created and mentioned links for a stored call.
- **Verification:** no `PullRequests()` call remains in `internal/rocketclaw`, and both packages pass.

## Verification Contract

| Gate | Command |
|---|---|
| Format | `gofmt -l` on touched Go files prints nothing |
| Lint | `make -C internal/rocketclaw lint` |
| Tests | `make -C internal/rocketclaw coverage-profile GOTESTPKGS="./harness ./frontend/rpc"`, after `bun install` in `internal/rocketclaw/web` |
| Size | `make -C internal/rocketclaw check-cloc-budget`, with source CLOC below 45186 |

## Definition of Done

- R1 to R3 hold, and the grep check in U1 finds nothing.
- All gates in the Verification Contract pass.
- No comments, migration or new tests were added.
- No abandoned experimental code remains in the diff.
