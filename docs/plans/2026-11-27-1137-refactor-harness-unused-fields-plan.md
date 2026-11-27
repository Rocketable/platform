---
title: Delete Unused Harness Fields - Plan
type: refactor
date: 2026-11-27
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Delete Unused Harness Fields - Plan

## Goal Capsule

- **Objective:** RocketClaw behaves exactly as it does today, including what the model and the permission reviewer see, while the harness carries less code nobody uses.
- **Means:** delete `Agent.FileMode`, `BashResult.Success`, and the developer option for prompt inputs (KTD1, KTD2, KTD3).
- **Authority:** the owner's decision in the invoking request, then this plan, then the repository's `AGENTS.md`.
- **Execution profile:** one PR, three small units, no behavior change.
- **Stop conditions:** stop and report if any deletion would change model input, reviewer input, saved checkpoint decoding, or bash exit reporting.
- **Finishing:** the implementing agent builds, verifies, and opens the PR. The owner merges.

---

## Product Contract

### Summary

Delete three things in `internal/rocketclaw/harness` that no feature uses: the agent file mode, the bash success flag, and the developer role for incoming prompts. The `Role` field on prompt inputs stays, because the permission reviewer sees it.

### Problem Frame

The harness carries fields that nothing reads and an input option nothing sets. Each one costs lines, tests, and reader attention. The developer option was only set by command-line tools that have since been removed.

### Requirements

**Agent file mode**
- R1. Agents no longer record the file mode of their definition file, and loading an agent no longer stats that file.

**Bash result**
- R2. Bash results no longer carry a separate success flag. `Output` and `ErrorCode` stay exactly as today, and an empty `ErrorCode` means the command succeeded.
- R3. The goal check still fails a goal when its command fails, using `ErrorCode`.

**Prompt input role**
- R4. Prompt inputs can no longer ask to be sent as developer messages. Every prompt input is sent with the user role.
- R5. The JSON the permission reviewer receives for `current_turn_inputs` is unchanged, including its `Role` key.
- R6. Every other developer message stays as it is: the system prompt, goal steering, replayed developer items, and looper developer messages.

### Key Decisions

- **Delete all three in one PR with no behavior change.** (session-settled: user-approved — chosen over leaving the dead code in place: it costs lines and reader attention and serves no caller.) Governs R1, R2, R4.
- **Keep the prompt input `Role` field, its type, the user value, and the three call sites that set it.** The permission review request marshals `current_turn_inputs` with a `Role` key, and the reviewer's origin rule mentions "Role and Header fields". The owner's instruction was to report rather than change reviewer output in that case. Governs R5.

### Scope Boundaries

- Not changing the reviewer origin rule text or the hash-pinned guardian prompt.
- Not changing the `testPromptInput` helper's signature. Its callers all pass the user role, and changing it is test churn across about 100 call sites with no source line saved.
- Not changing any developer message other than prompt inputs (R6).

---

## Planning Contract

### Key Technical Decisions

- KTD1. **Delete `Agent.FileMode` and the stat in `loadAgent`.** The `info` from that stat feeds only `FileMode`. The other `info` in `LoadAgents` is a separate variable for shadow directories and stays. (session-settled: user-approved — chosen over keeping the field: nothing outside tests reads it.)
- KTD2. **Delete `BashResult.Success` and replace its one production read with `ErrorCode != ""`.** The request said the field is never read, but `backend/bridge.go` reads `!check.Success` in the goal check. The swap does not change behavior, because every builder sets `ErrorCode` to empty exactly when it sets success: `shell.go` sets `"timeout"`, the exit code, or `"error"` whenever the run timed out or returned an error, `bashFailure` always sets `"error"`, and `emulatedBashResult` derives success from an empty code. Tests that asserted `Success` assert on `ErrorCode` instead. (session-settled: user-approved — chosen over keeping the field: it duplicates `ErrorCode`.) Conflict call-out: the request's "never read" premise is wrong for `backend/bridge.go:3470`; this does not change the decision.
- KTD3. **Delete `PromptInputRoleDeveloper` and its branch in `PromptInputMessage`, and keep the rest of the role.** See the Key Decision on keeping the `Role` field (R5). `PromptInputMessage` always builds a user message. (session-settled: user-approved — chosen over keeping the option: no RocketClaw code sets it.)

### Saved Data

- Journaled host-call steps (`hostCallStep.Bash`) store `BashResult` as JSON with `encoding/json`. Older steps that carry a `Success` key still decode, because `encoding/json` ignores unknown keys. Replay reads only `Output` and `ErrorCode`.
- Checkpoint `review_inputs`, the reviewer's `current_turn_inputs`, and saved turn and shadow snapshot `inputs` keep the same `PromptInput` shape, because `Role` stays. `review_inputs` only feed the reviewer JSON. Saved snapshot inputs are drained back into the looper, but RocketClaw never wrote a developer role into them, so the user-only message changes nothing for them.

---

## Implementation Units

### U1. Delete Agent.FileMode

- **Goal:** R1.
- **Requirements:** R1, KTD1.
- **Dependencies:** none.
- **Files:** `internal/rocketclaw/harness/agents.go`, `internal/rocketclaw/harness/agents_test.go`, `internal/rocketclaw/harness/prompts_test.go`, `internal/rocketclaw/backend/bridge_test.go`.
- **Approach:**
  1. Remove the field, its assignment, and the stat in `loadAgent`.
  2. Remove `FileMode: 0` from test struct literals.
  3. Remove the mode assertion and the `testMapFileMode` helper if it has no other use.
- **Test scenarios:** Test expectation: none -- deletion of an unread field. Existing agent-loading tests keep covering load behavior.
- **Verification:** `rg FileMode internal/rocketclaw` finds no use of the agent field.

### U2. Delete BashResult.Success

- **Goal:** R2, R3.
- **Requirements:** R2, R3, KTD2.
- **Dependencies:** none.
- **Files:** `internal/rocketclaw/harness/shell.go`, `internal/rocketclaw/harness/emulated.go`, `internal/rocketclaw/backend/bridge.go`, `internal/rocketclaw/harness/shell_test.go`, `internal/rocketclaw/harness/emulated_linux_test.go`, `internal/rocketclaw/harness/emulated_security_linux_test.go`, `internal/rocketclaw/harness/permission_gate_test.go`, `internal/rocketclaw/harness/public_progress_test.go`.
- **Approach:**
  1. Remove the field and its three assignments.
  2. In the goal check, test `check.ErrorCode != ""`.
  3. In tests, a true success assertion becomes an empty `ErrorCode` assertion, and a false one becomes a non-empty `ErrorCode` assertion. Struct literals drop `Success`.
- **Test scenarios:**
  - Existing shell tests keep proving that a successful command has an empty `ErrorCode`, and that a failing, timed-out, or erroring command has a non-empty one.
  - The existing goal-check tests in `backend` keep proving that a failing check keeps the goal active.
- **Verification:** `rg '\.Success\b|Success:' internal/rocketclaw` finds nothing tied to `BashResult`.

### U3. Delete the developer option for prompt inputs

- **Goal:** R4, R5, R6.
- **Requirements:** R4, R5, R6, KTD3.
- **Dependencies:** none.
- **Files:** `internal/rocketclaw/harness/attachments.go`, `internal/rocketclaw/harness/looper_test.go`, `internal/rocketclaw/harness/permission_inputs_test.go`.
- **Approach:**
  1. Remove the developer constant and the role branch, and pass the user role straight to `inputMessageParam`.
  2. Delete `TestLooperSendsAndReplaysDeveloperPromptInput` and `TestLooperSendsAndReplaysDeveloperAttachments`, plus `testPromptInputWithAttachments` once it has no callers.
  3. In `TestPermissionReviewInputKeepsSubmittedEvidenceWithoutAttachmentContents`, use the user role. That test is about evidence copying.
- **Test scenarios:**
  - The existing user-role looper tests keep proving that prompt inputs reach the model as user messages.
  - The existing permission input tests keep proving that `current_turn_inputs` carries `Role` unchanged.
- **Verification:** `rg PromptInputRoleDeveloper internal/rocketclaw` finds nothing. The `developer` matches for system prompt and steering are unchanged.

---

## Verification Contract

| Gate | Command |
|---|---|
| Format | `gofmt -l` on touched files prints nothing |
| Lint | `make -C internal/rocketclaw lint` |
| Tests | `make -C internal/rocketclaw coverage-profile GOTESTPKGS="./harness ./backend"` |
| Size | `make -C internal/rocketclaw check-cloc-budget`, with source CLOC below the 45366 baseline |

## Definition of Done

- U1 to U3 land in one commit in one PR, with every gate above passing.
- Non-test source lines go down from 45366.
- Every requirement R1 to R6 is rechecked against the diff, and the reviewer JSON shape is unchanged.
- No leftover code from approaches that were tried and abandoned.
