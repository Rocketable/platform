---
title: Remove the ValidateAssets Hook - Plan
type: refactor
date: 2026-11-26
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Remove the ValidateAssets Hook - Plan

## Goal Capsule

- **Objective:** Starting, reloading, saving Behavior Settings and saving an agent file accept and reject exactly what they do today, while each runtime directory they validate has its agent files read once instead of twice.
- **Means:** Delete `FrontendAssembler.ValidateAssets`, its implementation and `routinefrontend.ValidateRuntimeDefinitions`, and move the tests that used the hook as a timing seam onto seams that still exist (KTD1, KTD2, KTD3).
- **Authority:** The settled decision (KTD1) outranks this plan's other choices. The repository's `AGENTS.md` outranks the plan on style, tests and verification.
- **Stop conditions:** Stop and report if any call site's two checks are found to read a different config or runtime directory in a way that lets the second fail on its own, or if a removed test turns out to be the only cover for behavior that is still reachable in production, other than the commit-failure branch KTD5 names.
- **Execution profile:** One pull request, one commit.
- **Who finishes:** The implementing agent builds, verifies and opens the PR. A person merges.

## Product Contract

### Summary

Remove the second, redundant agent-file check that runs right after `validateRuntimeAssets` on start, reload, Behavior Settings save and agent-file save. Keep every test that proves real save, reload and startup behavior by giving it a seam other than the deleted hook.

### Problem Frame

RocketClaw validates the runtime agent files twice in a row at four call sites. The first check, `validateRuntimeAssets` in `internal/rocketclaw/backend/app.go`, runs `readRocketCodeDefinitionsIn`. The second, `FrontendAssembler.ValidateAssets`, ends in `routinefrontend.ValidateRuntimeDefinitions`, then `loadDefinitionsIn`, then `backend.LoadRuntimeAgents`, which runs the same `readRocketCodeDefinitionsIn` with the same workspace root, config and runtime directory. It existed to reject bad cron files. Cron files were retired, and bad agents and routines are now dropped with a warning, so the second check cannot fail when the first passed. It costs a full definitions read on every check and an interface method every assembler and test double must carry.

Re-verification of the claim, per call site:

- `startWithBehavior` (`backend/app.go`): the first check reads `sessions.definitions`, stored from `cfg` a few lines earlier, and the second gets `cfg`. Both use `runtimeDir`. `placePendingPages`, which runs between them, only writes database rows.
- `Runtime.Reload` (`backend/runtime.go`): the first check reads `r.Cfg` and the second a clone of `r.Cfg` taken under `reloadMu`. `SaveBehavior`, the only production writer of `r.Cfg` after start, also holds `reloadMu`, so the two configs are equal. Both use the staged `runtimeDir`.
- `Runtime.SaveBehavior` (`backend/runtime.go`): both checks get the same `candidate` and staged `runtimeDir`.
- `Runtime.ValidateAgentFile` (`backend/agent_draft_save.go`): the first check reads `r.Cfg` and the second a clone taken just before, without `reloadMu`. A concurrent `SaveBehavior` can change only Behavior fields between them. `readRocketCodeDefinitionsIn` reads no Behavior field that can produce an error: the embedded main agent's model is a warning, and providers, decision providers, MCP facets and OpenCode servers are not Behavior fields. Both use the staged `runtimeDir`.

No site lets the second check fail on its own.

### Requirements

**Behavior**

- R1. Start, reload, Behavior Settings save and agent-file save accept and reject exactly the inputs they accept and reject today.
- R2. Each of those paths runs `validateRuntimeAssets` once per staged or live runtime directory and no longer runs a second definitions read for validation.

**Code**

- R3. `FrontendAssembler` has only `Gate` and `Assemble`. `processAssembler.ValidateAssets`, `routinefrontend.ValidateRuntimeDefinitions` and every caller are gone, and the mocks are regenerated with `make mocks`.
- R4. Tests that only exercised the hook are deleted. Tests that used the hook as a timing or failure seam for real behavior keep that coverage through another seam.
- R5. Source CLOC goes down, and lint and the touched packages' tests pass.

### Key Decisions

- **Delete the hook instead of keeping it as an extension point.** Governs R2, R3. (session-settled: user-approved — chosen over keeping `ValidateAssets` as the frontend-specific validation step: since cron files were retired and bad agents and routines are dropped with a warning, it re-runs the same read and can never fail on its own.)

### Scope Boundaries

- `placePendingPages` and `prepareSandbox` also call `LoadRuntimeAgents` during start and reload. Those reads feed real work, not validation, and stay.
- Historical plans under `docs/plans/` that mention `ValidateAssets` are records and stay unchanged.
- Not built: a replacement validation step. Nothing it could reject reaches it.

## Planning Contract

### Key Technical Decisions

- KTD1. **Delete the hook outright.** Remove the interface method, the `processAssembler` method, `ValidateRuntimeDefinitions` and the four call sites. The call sites keep their existing error wrapping of `validateRuntimeAssets`. (session-settled: user-approved — chosen over keeping `ValidateAssets` as the frontend-specific validation step: it re-runs the same read and can never fail on its own.)
- KTD2. **Agent-draft save tests get an in-package mockery mock of `agentDraftRuntime`.** `SaveAgentDraft` and `CheckAgentDraftMerge` already take this two-method interface (`ValidateAgentFile`, `Reload`). The tests pass a generated `agentDraftRuntimeMock` whose functions delegate to the real `*Runtime`, and a test swaps in a function that runs its injected action and then delegates. Injections that ran on the first validation move to `ValidateAgentFile`. Injections that ran on the reload's validation move to `Reload`, before or after delegating. This keeps the race and refusal tests at the same point in the save. Considered: the `Routines` refresher seam, which runs only after the live swap and cannot reach the window between the two stale-file checks.
- KTD3. **The rpc agent-editor live test holds the save through a delegating `mockBackend`.** `rpc.New` takes `frontend.Backend`, and the server passes it to `SaveAgentDraft`. The test builds the existing generated `mockBackend` with every function set to the matching `*backend.Runtime` method value. `ValidateAgentFileFunc` waits for `/test/release-save` when the agent is `hold`, then delegates. `server_test.go` already delegates `mockBackend` functions to a `Runtime` this way. Considered: dropping the concurrent-save step, which would leave the Web rendering of "alice is saving this draft already." without a test.
- KTD4. **Runs that used a `ValidateAssets` error to stop after a stage now stop at `Assemble`.** `TestRunTakesTheLockThenGatesThenMigrates` and `TestRunStartsPastAnInvalidAgent` return their sentinel error from `AssembleFunc`. `Run` wraps it with `%w`, so `require.ErrorIs` still holds. Reaching `Assemble` proves the gate passed and the migrations ran, and that definitions validation passed with the invalid agent left out.
- KTD5. **Rows that injected a hook failure are deleted, with nothing in their place.** `TestRunStopsOnInvalidFrontendAssets`, the `invalid-frontend` scenario of `TestRuntimeReload` and the `frontend assets rejected` row of `TestRuntimeSaveBehavior` only proved the hook's error propagated. The `swap` row of `TestRuntimeSaveBehavior` used the hook to plant `attachments/kept` in the random stage directory so the commit would fail. No other seam reaches that directory before the commit. The rollback it checks, `restore` in `skel.ReplaceRuntimeAssetsAfterValidation`, still runs in the `refresh`, `cron` and `commit` rows. Only the commit-failure branch that calls it loses its test.

### Assumptions

- The live web test `src/agent-editor-transport.test.ts` uses no `frontend.Backend` method beyond the ones the delegating `mockBackend` sets. Setting all of them makes this hold by construction.
- The `.mockery.yml` `matryer` template generates an in-package mock for an unexported interface, as it already does for `runtimeRefresher`.

## Implementation Units

### U1. Delete the hook and regenerate mocks

- **Goal:** Remove the second check from production code.
- **Requirements:** R1, R2, R3. KTD1.
- **Dependencies:** None.
- **Files:** `internal/rocketclaw/backend/app.go`, `internal/rocketclaw/backend/runtime.go`, `internal/rocketclaw/backend/agent_draft_save.go`, `cmd/rocketclaw/routine.go`, `internal/rocketclaw/frontend/routine/manager.go`, `.mockery.yml`, regenerated `*frontendassembler_mocks_test.go` in `internal/rocketclaw/backend`, `cmd/rocketclaw` and `internal/rocketclaw/frontend/rpc`, and the new `internal/rocketclaw/backend/agentdraftruntime_mocks_test.go`.
- **Approach:**
  1. Drop `ValidateAssets` from `FrontendAssembler`.
  2. In `startWithBehavior`, delete the call after `placePendingPages`.
  3. In `Reload` and `ValidateAgentFile`, the validate callback returns `validateRuntimeAssets`'s error alone. `ValidateAgentFile` no longer needs its `cfg` clone except for `cfg.Workspace` and `cfg.Overlays`.
  4. In `SaveBehavior`, the `validate` closure keeps only the `validateRuntimeAssets` call.
  5. Delete `processAssembler.ValidateAssets` and `ValidateRuntimeDefinitions`, then prune unused imports.
  6. Add `agentDraftRuntime` with structname `agentDraftRuntimeMock` under the backend package in `.mockery.yml` and run `make mocks`.
- **Patterns to follow:** The `runtimeRefresher` entry in `.mockery.yml`.
- **Test expectation:** none -- behavior is unchanged. U2 and U3 rewire the existing tests that cover it.
- **Verification:** `rg 'ValidateAssets|ValidateRuntimeDefinitions' --type go` returns nothing, and the packages build.

### U2. Rewire backend and cmd tests

- **Goal:** Delete hook-only tests and move timing-seam tests onto the KTD2 and KTD4 seams.
- **Requirements:** R4. KTD2, KTD4, KTD5.
- **Dependencies:** U1.
- **Files:** `internal/rocketclaw/backend/app_test.go`, `thread_bridges_test.go`, `runtime_reload_test.go`, `agent_draft_save_test.go`, `agent_draft_overlay_test.go`, `agent_draft_turn_test.go`, `cmd/rocketclaw/assemble_test.go`, `ts_test.go`, `external_mcp_env_test.go`.
- **Approach:**
  1. Delete `ValidateAssetsFunc` lines that return nil, and `externalMCPAssembler.ValidateAssets`.
  2. Delete `TestValidateAssetsAcceptsWorkspaceWithoutRoutines`, `TestValidateAssetsLeavesABadRoutineToItsAgent` and `TestRunStopsOnInvalidFrontendAssets`.
  3. In the big `Run` test in `app_test.go`, move the "runtime dir holds `agents/primary.md`" check into `AssembleFunc` against `cfg.RuntimeDirName()`, and drop the two `validated` entries from the expected order.
  4. Apply KTD4 to `TestRunTakesTheLockThenGatesThenMigrates` and `TestRunStartsPastAnInvalidAgent`.
  5. Apply KTD5 to `TestRuntimeReload` and `TestRuntimeSaveBehavior`, collapsing the branches the deleted scenario leaves dead.
  6. In `agent_draft_save_test.go`, replace `agentDraftSave.validate` with an `agentDraftRuntimeMock` field built in `startAgentDraftSave`. `save` and the direct `SaveAgentDraft` and `CheckAgentDraftMerge` calls pass it.
  7. Move each injection per KTD2. In `TestSaveAgentDraftGoesLive`, the competing save and the context cancel run in `ValidateAgentFile`, and the "2 validations" count is dropped. The `TestSaveAgentDraftReviewsChangedFile` write and the overlay `moveBranch` hook also run in `ValidateAgentFile`. The `TestSaveAgentDraftLiveOnlyWithWhatItSaved` edit runs in `Reload` before delegating, and its rewrite goes to the live runtime file after delegating. The `TestAgentDraftMergeCheckEndsOnlyItsOwnWait` injection runs in `Reload` before delegating.
- **Patterns to follow:** `runtimeRefresherMock` use in `runtime_reload_test.go`.
- **Test scenarios:**
  - A second save started during the first save's `ValidateAgentFile` is refused with "Someone is saving this draft already."
  - A save whose request context is cancelled during `ValidateAgentFile` still goes live.
  - A workspace file changed during `ValidateAgentFile` makes the save stop for review with the new file as Live.
  - An edit made during `Reload` leaves the draft Editing with the saved file as its base.
  - A live runtime file that differs from the saved file after `Reload` leaves the draft Editing.
  - A teammate commit pushed while the overlay save validates lands under the draft's commit, and a teammate change to the same file stops the save for review.
  - A merge check whose wait is replaced during `Reload` keeps the newer wait.
  - `Run` with a gate error stops before migrations, and with a passing gate migrates and reaches `Assemble`.
  - `Run` with an invalid agent reaches `Assemble`.
- **Verification:** The backend and `cmd/rocketclaw` packages pass whole.

### U3. Rewire the rpc tests

- **Goal:** Drop the hook from rpc test assemblers and keep the concurrent-save step of the agent-editor live test.
- **Requirements:** R4. KTD3.
- **Dependencies:** U1.
- **Files:** `internal/rocketclaw/frontend/rpc/agent_editor_live_test.go`, `live_test.go`, `shadow_live_test.go`, `remote_test.go`, `pages_test.go`, `permissions_live_test.go`, `web_harness_test.go`.
- **Approach:**
  1. Delete the nil-returning `ValidateAssetsFunc` fields.
  2. In `agent_editor_live_test.go`, the assembler keeps only `GateFunc` and `AssembleFunc`.
  3. Build the delegating `mockBackend` from KTD3 and pass it to `New` in place of `rt`.
- **Patterns to follow:** The `mockBackend` built in `server_test.go` around `SubscribeFunc: rt.Subscribe`.
- **Test scenarios:**
  - Covers the existing web step: while alice's save of `hold` is held in `ValidateAgentFile`, bob's save shows "alice is saving this draft already.", and releasing it shows "Saved and reloaded: hold is live."
- **Verification:** The `./frontend/rpc` package passes whole, including the agent-editor live test.

## Verification Contract

| Gate | Command |
|---|---|
| Format | `gofmt -l` on touched Go files prints nothing |
| Lint | `make -C internal/rocketclaw lint` |
| Tests | `make -C internal/rocketclaw coverage-profile GOTESTPKGS="github.com/Rocketable/platform/cmd/rocketclaw ./backend ./frontend/routine ./frontend/rpc"` |
| Size | `make -C internal/rocketclaw check-cloc-budget`, and `make -C internal/rocketclaw cloc` source CLOC below 45148 |

## Definition of Done

- `rg 'ValidateAssets|ValidateRuntimeDefinitions' --type go` finds nothing.
- Every gate in the Verification Contract passes.
- No test lost coverage of behavior still reachable in production, except the commit-failure branch named in KTD5.
- No leftover experimental code, unused helpers or unused imports in the diff.
