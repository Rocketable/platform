---
title: Create Every Agent-Started Web Chat Through The Shared Conversation Path - Plan
type: refactor
date: 2026-11-26
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Create Every Agent-Started Web Chat Through The Shared Conversation Path - Plan

## Goal Capsule

- **Objective:** A Web chat an agent starts with its session tools shows up in that agent's Space and is listed like any other Web chat.
- **Means:** Delete `SessionService.UpsertThread` and send non-sticky agents through `CreateStickyConversation` with `harness.AgentStickyOff`, the call sticky agents already use (KTD1).
- **Authority:** the settled decisions in this plan, then `AGENTS.md`, then this plan's units.
- **Stop conditions:** stop and report if any precondition in R5 turns out false, or if running `checkSessionTarget` for non-sticky agents would be needed to keep behavior.
- **Execution profile:** one PR, two units in order.
- **Who finishes:** the implementing agent, through an open PR; merging stays with the owner.

## Product Contract

### Summary

The agent session tools create a new non-sticky Web chat through the same shared conversation path that sticky agents, the Web UI, and routines use. The chat row is written before its bridge starts, for every agent. `UpsertThread` and its rollback cleanup go away, and test fixtures move to the shared path.

### Problem Frame

`createSession` in `internal/rocketclaw/backend/thread_bridges.go` has two ways to store a new chat. Sticky agents go through `CreateStickyConversation` and get the agent's Space. Non-sticky agents start the bridge first, then call `UpsertThread`, which writes a near-identical row without a Space. If that write fails, extra code stops the bridge and drops it from the manager. `UpsertThread` has no other production caller and is mostly a test fixture (198 test call sites across 36 test files).

### Requirements

**Creation path**

- R1. A new non-sticky chat started by the session tools is stored through `CreateStickyConversation` with `harness.AgentStickyOff`.
- R2. The stored row is kind `web_chat`, listed, and carries the agent, `created_by`, and the agent's Space.
- R3. The chat row is written before its bridge starts for every agent, and the cleanup that stopped and forgot the bridge after a failed save is removed.
- R4. `checkSessionTarget` still runs for sticky agents only, unchanged.
- R5. Before deleting, the implementer confirms: `createSession` is the only production caller of `UpsertThread`, its ID is always a fresh `rand.Text()`, the shared path stores the same row and session summary, and it reconciles shadows for a newly created chat.

**Removal and tests**

- R6. `SessionService.UpsertThread` is deleted.
- R7. Test fixtures that called `UpsertThread` use the shared creation path or one small private helper per test package; test lines do not grow overall.
- R8. Tests that only covered `UpsertThread`'s own behavior (its update-on-conflict branch and blank-ID error) are deleted. A test that needed an existing chat's agent changed uses the production agent-switch path, or creates the chat with its final agent when the switch is not what it checks.

### Key Decisions

- **Unify non-sticky session-tool chats onto the shared conversation path.** (session-settled: user-directed — chosen over keeping `UpsertThread` as a separate writer: one creation path is simpler and the owner accepted the visible change) Governs R1, R6.
- **New chats get the agent's Space.** (session-settled: user-directed — chosen over keeping these chats Space-less: every other chat already gets the agent's Space) Governs R2.
- **Row before bridge for all agents.** (session-settled: user-directed — chosen over keeping bridge-first for non-sticky agents: it removes the failed-save cleanup) Governs R3.

### Scope Boundaries

- No change to `checkSessionTarget`, sticky lookup, or `UpdateConversationDetails` naming.
- Historical plan documents under `docs/plans/` that mention `UpsertThread` stay as written.
- Considered and not built: a state diagram and PlusCal model for the removed failed-save cleanup (pack: formal-modeling, stateful-design.md). Saving the row and then starting the in-memory bridge is one request with no new concurrent actors, retries, or leases, in the same order sticky agents already use. A crash after the save leaves a chat whose bridge starts on its first prompt.
- Considered and not built: trimming `created_by` before storing it. `UpsertThread` trimmed it; the shared path does not, the same as sticky agents and Web UI chats today. The value comes from the authenticated principal email. A case where that email is whitespace-padded would change this call.

## Planning Contract

### Key Technical Decisions

- KTD1. **Call `CreateStickyConversation` for every agent, and keep the `checkSessionTarget` branch for sticky agents.** The call already handles `AgentStickyOff` (fresh ID, `ON CONFLICT (conversation_id) DO NOTHING`, summary preserved, shadows reconciled on creation). The conditional around the call goes away, and the sticky-only block keeps only `checkSessionTarget`. (session-settled: user-directed — chosen over a separate non-sticky write: one creation path) Governs R1, R3, R4.
- KTD2. **Fixtures use one private helper per test package.** `CreateStickyConversation` returns three values, so calling it inline needs extra lines at each of the 198 sites. A helper in `backend` and one in `frontend/rpc` keep each call on one line. The helper takes a `protocol.Conversation`, the type production passes.
- KTD3. **The one concurrent caller uses `CreateStickyConversation` directly** and sends its error on the existing channel, because a `testing.TB` helper must not fail from a non-test goroutine.

### Assumptions

- The `TestDeleteSessionDeletesOnlyTarget` fixture can create both its chats with agent `ops` instead of creating them as `main` and then changing `main` to `ops`. The test only checks the `main` chat's agent after deletion.
- `TestSessionServiceThreadAgentUpdatePreservesCreator` only covers `UpsertThread`'s conflict branch. Agent switching goes through `SetThreadAgentIfExists`, which only changes the agent column and is exercised through `switchConversationAgent` and the RPC server tests.

## Implementation Units

### U1. Route agent-created Web chats through the shared path

- **Goal:** `createSession` stores every new chat through `CreateStickyConversation` before starting its bridge, and `UpsertThread` is gone.
- **Requirements:** R1–R6, KTD1.
- **Dependencies:** none.
- **Files:** `internal/rocketclaw/backend/thread_bridges.go`, `internal/rocketclaw/backend/store.go`, `internal/rocketclaw/backend/thread_bridges_test.go`.
- **Approach:**
  1. Re-verify R5 against the code before editing.
  2. In `createSession`, call `CreateStickyConversation` unconditionally with `agent.Sticky` and keep `checkSessionTarget` inside the sticky-only branch.
  3. Start the bridge with `ensureThreadBridge` after the row exists, and remove the `UpsertThread` block and its stop-and-forget cleanup.
  4. Delete `UpsertThread` and any import it alone used.
- **Patterns to follow:** the existing sticky branch of `createSession`. `Runtime.CreateConversation` in `internal/rocketclaw/backend/conversations.go` already passes `AgentStickyOff`.
- **Test scenarios:**
  - Saving the new chat fails: `createSession` returns the "persist new Web session" error and no bridge was started. Rewrite `TestThreadBridgeManagerDropsBridgeWhenThreadIsNotPersisted` in `internal/rocketclaw/backend/thread_bridges_test.go` to this contract, replacing its stop-and-retry assertions.
  - An agent with `sticky` off creates a session with the session tools and the queued prompt reaches it. The existing session-tool create tests in `internal/rocketclaw/backend/session_tools_test.go` cover this.
  - The shared path stores the Space for `AgentStickyOff`. Existing tests in `internal/rocketclaw/backend/pages_test.go` and `internal/rocketclaw/backend/page_tools_test.go` cover this, so no new test.
  - A sticky agent still resumes its existing chat, and `checkSessionTarget` still rejects a target that is the caller. Existing tests cover this.
- **Verification:** no production reference to `UpsertThread` remains, and backend tests pass.

### U2. Move test fixtures off `UpsertThread`

- **Goal:** every test that used `UpsertThread` compiles and passes on the shared path, with fewer or equal test lines.
- **Requirements:** R7, R8, KTD2, KTD3.
- **Dependencies:** U1.
- **Files:** the 36 test files under `internal/rocketclaw/backend/` and `internal/rocketclaw/frontend/rpc/` that call `UpsertThread`, including `internal/rocketclaw/backend/store_test.go` and `internal/rocketclaw/frontend/rpc/turns_test.go`.
- **Approach:**
  1. Add one private helper per package that creates a Web chat from a `protocol.Conversation` with `AgentStickyOff` and fails the test on error.
  2. Rewrite each call site to the helper, converting `ThreadState{Agent, CreatedBy}` to the matching `protocol.Conversation` fields.
  3. Delete `TestSessionServiceThreadAgentUpdatePreservesCreator` and the blank-ID `UpsertThread` assertion in `TestSessionServiceRejectsBlankKeys`.
  4. In `TestDeleteSessionDeletesOnlyTarget`, create the chats with agent `ops` and drop the agent-changing call.
  5. Where a test relied on `UpsertThread` changing an existing chat's agent, switch it with `SetThreadAgentIfExists`, the production path for agent switches. `TestThreadBridgeManagerStartsPendingScheduledMessageBridges` is one such test. Any others surface as failures when the tests run.
- **Test expectation:** these are fixture changes; the behavior is checked by the existing tests passing unchanged in intent.
- **Verification:** `UpsertThread` appears nowhere under `internal/rocketclaw`, and the touched packages' tests pass.

## Verification Contract

| Gate | Command |
|---|---|
| Format | `gofmt -l` on touched Go files prints nothing |
| Lint | `make -C internal/rocketclaw lint` |
| Tests | `make -C internal/rocketclaw coverage-profile GOTESTPKGS="./backend ./frontend/rpc"` after `bun install` in `internal/rocketclaw/web` |
| Size | `make -C internal/rocketclaw check-cloc-budget`, and `make -C internal/rocketclaw cloc` source and test numbers before and after |

## Definition of Done

- U1 and U2 are complete and every gate in the Verification Contract passes.
- Source CLOC goes down and test CLOC does not go up.
- No abandoned-attempt code, comments, or unused imports remain in the diff.
- The PR text names the visible change (these chats now get the agent's Space) and the dropped `created_by` trim, with no internal names.
