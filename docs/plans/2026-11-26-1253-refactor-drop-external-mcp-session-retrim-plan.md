---
title: Drop External MCP Session Re-Trim - Plan
type: refactor
date: 2026-11-26
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Drop External MCP Session Re-Trim - Plan

## Goal Capsule

- **Objective:** An External MCP caller that continues an existing conversation gets the same answer, agent and chat as today, from a server with less code to maintain.
- **Means:** delete the re-trim of the loaded session and the "incomplete persisted state" error in `cmd/rocketclaw/mcp.go` (KTD1).
- **Authority:** the owner-approved decision > this plan's Requirements > KTDs > unit Approach. `AGENTS.md` governs style, the Defensive Code and Simplification rules, and verification.
- **Stop conditions:** stop and report if a test outside the removed behavior fails, or if a second writer of the External MCP session columns turns up.
- **Execution profile:** one PR with one commit.
- **Finishing:** `lfg` implements, reviews and opens the PR. Merging stays with the user.

## Product Contract

### Summary

Remove the second normalization of a loaded External MCP session and its "incomplete persisted state" error from the External MCP prompt handler. The mismatched-agent warning, the exposed-agent check and the registration path stay as they are.

### Problem Frame

When a prompt arrives for an External MCP conversation that already exists, the handler trims the stored agent, private run ID and Web chat ID again and then refuses the call if the agent or chat ID is empty. That guards against stored values that cannot occur. `SessionService.RegisterExternalMCPConversation` in `internal/rocketclaw/backend/store.go` is the only code that writes those columns, and it trims all three before writing. Its only caller registers an exposed, non-empty agent with fresh random IDs. A check of a deployment's stored sessions found none with an empty or whitespace-padded agent, run or chat ID. `AGENTS.md` treats this kind of guard as defensive code to delete.

### Requirements

- R1. Loading an existing External MCP session uses the stored agent, private run ID and Web chat ID as read, with no second trim in the handler.
- R2. The handler no longer returns the "incomplete persisted state" error.
- R3. A stored agent that is not exposed, including an empty one, is still refused by the exposed-agent check.
- R4. The warning for a requested agent that differs from the stored one is unchanged.

### Key Decisions

- **Delete the guard instead of keeping it as defense.** (session-settled: user-approved — chosen over keeping the re-trim and the incomplete-state error: the only writer already trims, its only caller writes non-empty values, and the exposed-agent check still refuses an empty agent.) Governs R1, R2.

### Scope Boundaries

- The trimming inside `RegisterExternalMCPConversation` and its store test stay; this change depends on them.
- No new tests. A test that only asserted the removed error or re-trim would be deleted, and none exists today.
- Not built: a replacement check for an empty private run ID or Web chat ID. No code path writes one, so a guard would protect an impossible state.

## Planning Contract

### Key Technical Decisions

- KTD1. **Delete the three trim assignments and the empty-value error inside the `if ok` branch of `startExternalMCPServer`; keep the branch with its warning.** The `strings` import stays because the file still uses it for attachments. Implements the settled decision governing R1, R2.

### Assumptions

- No test in the repository asserts the "incomplete persisted state" text or the handler's re-trim; a search found the string only in `cmd/rocketclaw/mcp.go`.

## Implementation Units

### U1. Remove the re-trim and incomplete-state error

- **Goal:** the existing-session branch keeps only the mismatched-agent warning.
- **Requirements:** R1, R2, R3, R4; KTD1.
- **Dependencies:** none.
- **Files:** `cmd/rocketclaw/mcp.go`; existing coverage in `cmd/rocketclaw/mcp_test.go`.
- **Approach:**
  1. In the `if ok` branch, delete the three `strings.TrimSpace` reassignments of `session.Agent`, `session.PrivateConversationID` and `session.ManagedConversationID`.
  2. Delete the `if session.Agent == "" || session.ManagedConversationID == ""` block and its error.
  3. Leave the `else` branch, the exposed-agent check and registration untouched.
- **Patterns to follow:** the Defensive Code section of `AGENTS.md`.
- **Test scenarios:** Test expectation: none -- removes an unreachable guard. Existing tests keep covering the kept behavior:
  - `TestExternalMCPRepeatedIDKeepsLockedAgent` covers R4 and the reuse of the stored agent and run.
  - `TestExternalMCPRefusesUnexposedAgents` covers R3.
- **Verification:** the `cmd/rocketclaw` package tests pass, and the diff only deletes lines.

## Verification Contract

| Gate | Command |
|---|---|
| Format | `gofmt -l cmd/rocketclaw/mcp.go` prints nothing |
| Lint | `make -C internal/rocketclaw lint` |
| Package tests | `make -C internal/rocketclaw coverage-profile GOTESTPKGS=github.com/Rocketable/platform/cmd/rocketclaw` |
| Size budget | `make -C internal/rocketclaw check-cloc-budget` |
| Size report | `make -C internal/rocketclaw cloc` before and after |

## Definition of Done

- R1 to R4 hold and every gate in the Verification Contract passes.
- Source CLOC after the change is lower than before.
- The diff has no added lines in `cmd/rocketclaw/mcp.go` and no leftover experiments.
