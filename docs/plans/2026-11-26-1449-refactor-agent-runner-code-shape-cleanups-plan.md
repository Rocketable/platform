---
title: Agent Runner Code-Shape Cleanups - Plan
type: refactor
date: 2026-11-26
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Agent Runner Code-Shape Cleanups - Plan

## Goal Capsule

- **Objective:** The code that runs agent turns is smaller and has one copy of each concept, and every agent turn, one-shot call, and prompt header behaves exactly as it did before.
- **Means:** Five deletions and merges in `internal/rocketclaw` (KTD1 to KTD5), each skipped when its claim proves false or the linter requires the code.
- **Authority:** The owner's approved decision (five items, one PR, no behavior change) wins over this plan. `AGENTS.md` wins on style, verification, and PR text.
- **Execution profile:** One PR. Five small units, any order. Fewer non-test source lines afterwards.
- **Stop conditions:** Stop and send the question back if a product question comes up that the decision does not answer. Skip an item, and report why, if its claim is false or lint requires the code it would delete.
- **Who finishes:** `ce-work` implements and verifies. The calling pipeline reviews, opens the PR, and waits for CI without merging.

## Product Contract

### Summary

Delete the copied MCP server settings type, the no-op prompt-label sanitizer, the three copies of the folder-list response type (one shared type instead), the three-way prompt `!`command expansion switch, and a duplicate error branch. Nothing users or agents see changes.

### Problem Frame

These five spots in the code that runs agents each keep two copies of one concept, or keep code that can never change the result. Every copy is one more thing to keep in sync, and the source size budget is tight.

### Requirements

**Behavior kept**

- R1. Every prompt header, MCP server connection, OpenCode sync, prompt expansion, and agent draft error stays exactly as before.
- R2. Prompt `!`command expansion stays on for primary, subagent, and skill prompts in the real turn, and off in the handoff and agent draft assistant one-shot calls.

**Size and checks**

- R3. Non-test source lines go down (`make -C internal/rocketclaw cloc`; 45077 before).
- R4. gofmt, `make -C internal/rocketclaw lint`, the touched packages' tests, and `make -C internal/rocketclaw check-cloc-budget` pass.

### Key Decisions

- One PR with all five items, no behavior change. (session-settled: user-approved — chosen over separate PRs per item: they are small, independent, and in the same area.) Governs R1, R3.

### Scope Boundaries

- The two OpenCode parent-session walks stay as they are. They differ.
- `openCodeSession.Location` and the `CreateOpenCodeSession` request body declare the same `{directory}` shape, but they are not folder-list calls and stay untouched.

## Planning Contract

### Key Technical Decisions

- KTD1. **`mcpclient` and `harness.Config.MCPServers` use `config.MCPServerConfig` directly.** Delete `mcpclient.ServerConfig` and `toMCPClientServers`, and pass `runtimeCfg.MCPServers` through. `config` depends only on `protocol` and `harness` already imports `config`, so there is no import cycle. `mcpclient.New` still copies `Args` and rebuilds `Env` and `Headers`. `Tools` rides along unread. The old nil-on-empty return made no difference because `New` always builds its own map. (session-settled: user-approved — chosen over keeping a separate mcpclient type: it is a field-for-field copy.)
- KTD2. **Delete `provenanceToken` and its `cmp.Or(..., "System")` fallback.** Every write to `promptProvenance.origin` is a fixed word: `provenanceFromInbound` (one of four Source words, or a metadata value it accepts only from the fixed list "Slack", "Routine", "ExternalMCP", "System"), `reviewOriginHeader` ("Routine"), and `background.go` ("System"). Delete the `bridge_test.go` assertions that test sanitizing arbitrary origins. Also delete `case protocol.SourceSystem` from `provenanceFromInbound`, but only if `exhaustive` passes without it. The switch has no `default:` and currently lists every `protocol.Source` value, so the linter will probably require the case. (session-settled: user-approved — chosen over keeping the sanitizer: origin is never free text.)
- KTD3. **Share one named `openCodeLocation` type across the three `/api/debug/location` calls instead of a shared function.** A function would be net +1 line: the `server.connected` site exits the event loop on error, so all three call sites still need their own error branch. A named type replaces three 3-line anonymous structs with one-line declarations, which is net −3. Each call keeps its own error handling. (session-settled: user-approved — chosen over leaving three copies: a shared helper is allowed only if the result is net shorter.) Using a shared type instead of a shared function is this plan's choice and is flagged to the owner.
- KTD4. **Replace `PromptShellCommandExpansion` with one `ExpandPromptShellCommands bool` on `harness.Config`.** The factory stores the bool. `tasks.go`, `permission_review.go`, and the skill path in `tools.go` all read it. The real turn sets it true. The handoff and agent draft assistant still leave it false. Tests that set only one sub-switch now set the one bool. (session-settled: user-approved — chosen over three switches: only all-on and all-off are ever used.)
- KTD5. **Delete `case AgentDraftCreate, AgentDraftEditFile, AgentDraftRestore` in `applyAgentDraftOp` if `exhaustive` allows it.** It returns the same error as `default:`. `exhaustive` uses its default settings here, and `default-signifies-exhaustive` is off by default, so the linter will probably require the case. In that case skip the item and report it. (session-settled: user-approved — chosen over keeping both branches, unless lint requires the case.)

### Assumptions

- `exhaustive` runs with default settings (no `settings.exhaustive` block in `internal/rocketclaw/.golangci.yml`). A real lint run decides KTD2's switch-case part and KTD5.

## Implementation Units

### U1. One MCP server settings type

- **Goal:** Delete the mirror type and its converter (KTD1).
- **Requirements:** R1, R3
- **Dependencies:** none
- **Files:** `internal/rocketclaw/mcpclient/registry.go`, `internal/rocketclaw/harness/rocketcode.go`, `internal/rocketclaw/backend/bridge.go`; tests `internal/rocketclaw/mcpclient/registry_test.go`, `internal/rocketclaw/harness/{mcp_tools_test,mcp_result_test,result_model_test}.go`, plus any backend test that names `mcpclient.ServerConfig`.
- **Approach:** Change `Registry.servers` and `New` to `config.MCPServerConfig`. Change `harness.Config.MCPServers` to `map[string]config.MCPServerConfig`. Pass `runtimeCfg.MCPServers` in `bridge.go`. Delete `toMCPClientServers`. Drop the imports this leaves unused.
- **Test scenarios:** Test expectation: none -- type substitution. The existing registry and harness MCP tests must pass unchanged in meaning (env/header merge and cloning).
- **Verification:** `grep ServerConfig` finds only `config.MCPServerConfig`. mcpclient, harness, and backend tests pass.

### U2. Prompt origin label

- **Goal:** Delete `provenanceToken`, the `cmp.Or` fallback, and, lint permitting, the repeated `SourceSystem` case (KTD2).
- **Requirements:** R1, R3
- **Dependencies:** none
- **Files:** `internal/rocketclaw/backend/bridge.go`, `internal/rocketclaw/backend/bridge_test.go`
- **Approach:** `provenanceHeader` starts with `"[" + provenance.origin`. Delete the two `bridge_test.go` assertions that feed free-text origins (whitespace-only, and `External [MCP]=x`). Keep the others.
- **Test scenarios:** The remaining `provenanceHeader`/`provenanceFromInbound` assertions still produce `[System]`, `[Slack]`, `[Web principal=...]`, `[ExternalMCP ...]`, and `[Routine ...]` exactly as before.
- **Verification:** backend tests pass. Lint decides the switch case.

### U3. OpenCode folder-list response type

- **Goal:** One named location type for the three folder-list calls (KTD3).
- **Requirements:** R1, R3
- **Dependencies:** none
- **Files:** `internal/rocketclaw/backend/opencode.go`, `internal/rocketclaw/backend/opencode_mcp.go`
- **Approach:** Declare `openCodeLocation` next to `openCodeSession` and use `[]openCodeLocation` at the three call sites. Leave the error handling and loops as they are.
- **Test scenarios:** Test expectation: none -- the declared type changes and the decoding stays the same. Existing OpenCode sync, waiting, and bridge tests cover the calls.
- **Verification:** backend tests pass. Net source lines drop.

### U4. One prompt `!`command expansion switch

- **Goal:** Collapse the three switches into one bool (KTD4).
- **Requirements:** R2, R3
- **Dependencies:** none
- **Files:** `internal/rocketclaw/harness/{rocketcode,tools,tasks,permission_review}.go`, `internal/rocketclaw/backend/bridge.go`; tests `internal/rocketclaw/harness/{main_test,skills_test,prompts_test,looper_test}.go`, `internal/rocketclaw/backend/bridge_test.go`
- **Approach:** Delete the `PromptShellCommandExpansion` type. Store the bool on the factory and read it at all three sites. Set it true in `bridge.go`. `handoff.go` and `agent_draft_assistant.go` keep leaving it unset. A test that turned on one sub-switch and relied on another staying off tested a mix production never uses. Update it to the single bool and keep what it asserts about the prompt it targets.
- **Test scenarios:** The real turn's config has `ExpandPromptShellCommands` true (update the `bridge_test.go` assertion). The existing harness expansion tests for primary, skill, and subagent prompts still pass with the single bool.
- **Verification:** harness and backend tests pass. `grep PromptShellCommandExpansion` finds nothing.

### U5. Duplicate agent draft branch

- **Goal:** Delete the case that repeats `default:` (KTD5), lint permitting.
- **Requirements:** R1, R3
- **Dependencies:** none
- **Files:** `internal/rocketclaw/backend/agent_drafts.go`
- **Approach:** Delete the case, run lint, and restore it if `exhaustive` flags the switch.
- **Test scenarios:** Test expectation: none -- the same error is returned for the same inputs.
- **Verification:** lint is the decider. backend tests pass.

## Verification Contract

| Check | Command |
|---|---|
| Format | `gofmt -l` on touched files prints nothing |
| Lint | `make -C internal/rocketclaw lint` |
| Tests | `make -C internal/rocketclaw coverage-profile GOTESTPKGS="./backend ./harness ./mcpclient ./config"` |
| Size | `make -C internal/rocketclaw cloc` (source below 45077) and `make -C internal/rocketclaw check-cloc-budget` |

## Definition of Done

- Each of U1 to U5 is either landed or skipped with the evidence that decided it (false claim or lint requirement).
- R1 and R2 hold. R3 shows fewer source lines. Every R4 check passes.
- No source comments are added, no defensive code, and no abandoned-attempt code is left in the diff.
