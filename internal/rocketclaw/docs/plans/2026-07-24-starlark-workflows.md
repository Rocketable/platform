# Starlark Workflows Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Add saved Starlark workflows that run as managed foreground RocketClaw turns with isolated workers and Slack phase cards.

**Architecture:** `internal/rocketclaw/workflow` owns the interpreter and narrow host interfaces. `harnessbridge.Bridge` owns foreground lifecycle and a generalized isolated RocketCode runner. Existing app, store, skeleton, and Slack paths provide validation, final history, assets, and delivery.

**Tech Stack:** Go 1.26.4+, `go.starlark.net`, RocketCode, SQLite, Slack Block Kit streaming, Jujutsu.

## Global Constraints

- Use TDD: observe every focused test fail before production implementation.
- Never use Git; inspect with `jj diff --git`.
- Use only `<repo-root>/.tmp/` for temporary artifacts.
- Preserve the human-approved RocketClaw CLOC budget of 16,500 and the unchanged RocketCode budget of 9,000; do not add lint suppressions or hide code in excluded paths.
- Workflow workers inherit and may only narrow the invoking agent's access.
- Workflow prompts disable primary and input prompt-shell expansion.
- Workflow workers never receive `task` or RocketClaw behavior tools.
- Nested fan-out is invalid and must fail validation or the runtime backstop.
- `$stop` is terminal; daemon interruption ends the workflow and requires a new invocation.
- Parallel workers share the checkout; overlapping writes are unsupported.
- README and cheatsheet updates are required.

---

### Task 1: Starlark Definitions And Validation

**Files:** create `internal/rocketclaw/workflow/definition.go` and `definition_test.go`; modify `go.mod`, `go.sum`, and `vendor/`.

**Produces:** compiled top-level workflow definitions with validated metadata, entry point, workers, schemas, phases, callback shapes, and safe names.

- [ ] Add failing tests for valid definitions and every rejected top-level/nested-fan-out shape.
- [ ] Run the focused tests and verify expected failures.
- [ ] Pin and vendor `go.starlark.net`.
- [ ] Implement the restricted dialect and AST validator through `os.Root`.
- [ ] Run workflow package tests and both CLOC checks.

### Task 2: Starlark Engine

**Files:** create `internal/rocketclaw/workflow/engine.go` and `engine_test.go`.

**Consumes:** compiled definitions. **Produces:** `Run`, `AgentRunFunc`, phase updates, JSON conversion, and callback/step/agent limits.

- [ ] Add failing tests for values, phases, ordered fan-out, limits, cancellation, nested backstop, and infrastructure errors.
- [ ] Run focused tests and verify expected failures.
- [ ] Implement pure workers and host builtins with separate Starlark threads.
- [ ] Implement shared limits and callback freezing.
- [ ] Run package tests with the race detector and CLOC checks.

### Task 3: RocketCode Tool Narrowing And Isolated Runner

**Files:** modify `internal/rocketcode/rocketcode.go`, `main_test.go`, `internal/rocketclaw/harnessbridge/raw_run.go`, and `raw_run_test.go`.

**Produces:** supported RocketCode tool allowlisting and a neutral isolated-agent runner reused by cron and workflows.

- [ ] Add failing RocketCode allowlist tests and workflow-runner tests.
- [ ] Implement exact tool filtering after effective skill permissions are derived.
- [ ] Generalize raw execution while preserving all cron output/provenance behavior.
- [ ] Disable workflow prompt expansion and behavior tools; support strict schema output.
- [ ] Run RocketCode and harnessbridge focused tests, race tests, and CLOC checks.

### Task 5: Bridge-Native Workflow Turns

**Files:** modify `harnessbridge/bridge.go`, `bridge_test.go`, `primary_text_router.go`, `app/thread_bridges.go`, and `thread_bridges_test.go`.

**Produces:** typed workflow submission, managed queue execution, progress publishing, terminal stop, paired final history, and final delivery.

- [ ] Add failing queue, stop, history, paired-lock, silent-result, and shutdown tests.
- [ ] Add the typed bridge request and explicit real/inert runner dependency.
- [ ] Execute workflows under the existing active-turn lifetime and publish path.
- [ ] Complete drained workflow waiters and distinguish human stop from daemon shutdown.
- [ ] Run bridge/app focused tests and CLOC checks.

### Task 6: Assets And Reload

**Files:** modify `skel/skel.go`, `skel_test.go`, `app/app.go`, and tests.

**Produces:** effective workflow assets and atomic startup/reload validation.

- [ ] Add failing local/configured overlay, reload rollback, and path-safety tests.
- [ ] Add `workflows` to every asset synchronization path.
- [ ] Validate workflows during startup and staged reload.
- [ ] Run skel/app focused tests and CLOC checks.

### Task 7: Slack Command And Phase Cards

**Files:** modify `events/types.go`, `slackconnector/connector.go`, and connector tests.

**Produces:** `$workflow`, listing/help, root/existing-thread routing, stable phase task cards, and separate direct final output.

- [ ] Add failing exact request tests for commands and plan/task stream chunks.
- [ ] Route `$workflow` through the existing dollar parser without prompt leakage.
- [ ] Add typed phase updates and stable task IDs to the existing Slack stream path.
- [ ] Implement terminal plan states and answer separation.
- [ ] Run connector tests, race tests, and CLOC checks.

### Task 8: Bundled Workflow Authoring Skill

**Files:** create `skel/.rocketclaw/skills/main-create-or-update-workflow/SKILL.md`; modify `skel_test.go`.

**Produces:** a tested embedded skill that reliably authors valid workflow overlays and reloads once.

- [ ] Run three fresh-agent baseline scenarios without the skill and record failures.
- [ ] Add failing skeleton/discovery/content tests.
- [ ] Write the minimum skill addressing observed failures.
- [ ] Re-run the scenarios with the skill loaded and close remaining gaps.
- [ ] Run focused skeleton tests.

### Task 9: Documentation And Full Verification

**Files:** modify `README.md` and `cmd/rocketclaw/CHEATSHEET.md`.

- [ ] Document assets, invocation, DSL, limits, stop/restart, Slack cards, and shared writes.
- [ ] Run `gofmt` on all touched Go files.
- [ ] Run `go test ./...`.
- [ ] Run `make lint` and `make test`.
- [ ] Run RocketClaw and RocketCode `make cloc` and stop on either hard limit.
- [ ] Run `jj diff --git` and repeat the final Go standards/semantics review.
- [ ] Dispatch a whole-change review and fix every Critical/Important finding.
