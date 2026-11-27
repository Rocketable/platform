---
title: Drop message fields that are set but never read - Plan
type: refactor
date: 2026-11-27
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Drop message fields that are set but never read - Plan

## Goal Capsule

- Objective: People reading the RocketClaw message types no longer find fields that look meaningful but feed nothing, and every chat, Slack thread, routine run, and goal turn behaves exactly as before.
- Means: Delete three write-only data fields (`OutboundMessage.ConsumedHeader`, `OutboundMessage.GoalActive`, and `RoutineMessage.RelativePath` with `RanAt`), along with their writes and the assertions that pin them (KTD1 to KTD4).
- Authority: Product Contract R-IDs win on behavior. KTDs win on mechanism.
- Stop conditions: Stop and report if a reader of any of these fields turns up in Go, TS, proto, SQL, or a JSON contract a client consumes. Also stop if source CLOC does not go down.
- Execution profile: one PR, one commit. Lightweight. Do not edit `internal/rocketclaw/harness/{agents,shell,emulated,attachments}.go`, because another change is working in them.
- Finishing: the implementer verifies locally, then the change ships as a pull request.

## Product Contract

### Summary

Remove the struct fields that are written but never read, and their writes. Behavior does not change.

### Problem Frame

Three groups of fields on the in-process message types are assigned on every turn or routine run, and no code, client, or query ever reads them:

- `protocol.OutboundMessage.ConsumedHeader` is set in `Bridge.publishConsumed`. No frontend reads it. The Web and Slack frontends read `ConsumedID`, `ConsumedText`, `ConsumedRawText`, and `ConsumedSource`. The prompt header already reaches the transcript through `harness.PromptInput.Header`, and from there the replay item's `prompt_header`.
- `protocol.OutboundMessage.GoalActive` is computed in `Bridge.newOutboundMessage` and never consulted.
- `protocol.RoutineMessage.RelativePath` and `RanAt` are set by the routine manager. Only `Agent` and `Space` are read, in Go and in SQL (`inbound_json->'Routine'->>'Agent'` and `->>'Space'`). The routine origin card's "Ran at" comes from `RoutineRun.At` through `backend.DecideOrigin`, not from `RoutineMessage.RanAt`.

Each field suggests a contract that does not exist, and costs a line at every write.

### Requirements

- R1. `OutboundMessage` no longer has `ConsumedHeader`. `publishConsumed` no longer takes a header. Consumed-input events carry the same ID, text, raw text, source, and attribution as before.
- R2. `OutboundMessage` no longer has `GoalActive`. `GoalTurn`, `GoalTurnNumber`, `GoalMaxTurns`, and `ChannelQuiet` keep their current values.
- R3. `RoutineMessage` keeps only `Agent` and `Space`. Routine runs keep the same trace conversation IDs, logs (including `ran_at`), report chats, and spaces.
- R4. Turns, queue items, and producer rows stored before this change still decode, even though their JSON carries the removed keys. `encoding/json` ignores unknown object keys.

### Key Decisions

- Delete the fields, not only their writes. (session-settled: user-approved — chosen over keeping the fields for possible future readers: nothing reads them, so they are dead state.) Governs R1, R2, R3.
- Remove `ValidateAgentFile` from the `frontend.Backend` interface only when no frontend code uses it through that interface. (session-settled: user-approved — chosen over leaving the interface unchanged: an unused method widens every mock.) Governs nothing here. Re-verification found a use, so this item is skipped (see Scope Boundaries).

### Scope Boundaries

- Skipped: removing `ValidateAgentFile` from `frontend.Backend`. `frontend/rpc/drafts.go` passes `s.backend`, a `frontend.Backend`, to `SessionService.CheckAgentDraftMerge` and `SessionService.SaveAgentDraft`. Both take backend's unexported `agentDraftRuntime` interface, which requires `ValidateAgentFile`. Removing the method would break the build. `frontend/rpc/agent_editor_live_test.go` also relies on the call going through the mock. The owner's rule was to skip an item that is still used, so the interface and the mocks stay unchanged.
- Not built: a JSON compatibility shim, migration, or rejection path for the removed keys. Old rows decode as they are (R4).
- Unchanged: `store_schema_test.go`'s migration fixture JSON keeps `RelativePath` and `RanAt`, because it stands for old stored data. Only its expected decoded struct changes.

## Planning Contract

### Key Technical Decisions

- KTD1. **`publishConsumed(ctx, inbound)`.** Drop the `header` parameter. Its four callers stop passing it. In `conversations.go`, the drain loop calls `publishConsumed(ctx, pending[i].inbound)`. In `bridge.go`, `runTurn` keeps its local `header` variable, because the deferred "publish consumed if no prompt was built" check still reads it (`if header == ""`). Only the argument goes away, which keeps that control flow exactly as it is.
- KTD2. **`GoalActive`.** Delete the field and its single assignment. `statusActive` and `accounted` still feed `GoalTurn`, so they stay.
- KTD3. **`RoutineMessage{Agent, Space}`.** Delete `RelativePath` and `RanAt`, and drop them from both writes in `frontend/routine/manager.go`. `executeJob` keeps `ranAt` for its log line. `RunOneOffRoutine` loses a second `m.now()` call that only fed `RanAt`.
- KTD4. **Tests follow the types.** Delete assertions on the removed fields. Drop the removed keys from test struct literals. Where a test used `ConsumedHeader` only to compare the prompt header with the replay item's `prompt_header`, compare against the literal header string instead, so the prompt-header contract stays covered. Where a test used `Routine.RelativePath` to prove a deep clone, use `Routine.Agent`.

### Assumptions

- Re-verification before planning: `rg` over Go, `internal/rocketclaw/web/src`, `web/proto/web.proto`, SQL, and migrations finds no reader. Stored `turns.outbound_json`, `turns.inbound_json`, `shadow_turns.inbound_json`, and `conversations.producer_inbound_json` are decoded with `encoding/json`, which ignores unknown keys. The only `DisallowUnknownFields` decoder is for tag tool arguments. Outbound events reach frontends in process as cloned Go structs through `Runtime.PublishOutbound`. No relay JSON-encodes them for a client.
- Attachment retention matches attachment IDs as substrings of `inbound_json` and `outbound_json` text. None of the removed fields can hold an attachment ID: the header is a principal and instructions line, and routine paths and timestamps are not IDs. So retention does not change.

## Implementation Units

### U1. Delete the write-only message fields

- Goal: R1 to R4.
- Dependencies: none.
- Files:
  - `internal/rocketclaw/protocol/types.go`
  - `internal/rocketclaw/backend/conversations.go`
  - `internal/rocketclaw/backend/bridge.go`
  - `internal/rocketclaw/frontend/routine/manager.go`
  - Tests: `internal/rocketclaw/protocol/clockwork_test.go`, `internal/rocketclaw/backend/{bridge_test,shadows_test,active_turn_test,thread_bridges_test,page_tools_test,attention_test,store_schema_test}.go`, `internal/rocketclaw/frontend/routine/manager_test.go`, `cmd/rocketclaw/routine_test.go`
- Approach:
  1. Remove the fields from `protocol/types.go` (KTD2, KTD3).
  2. Apply KTD1 to `publishConsumed` and its callers.
  3. Remove the `GoalActive` assignment in `newOutboundMessage`.
  4. Remove `RelativePath` and `RanAt` from the two `RoutineMessage` literals in `manager.go`.
  5. Update the tests per KTD4.
- Test scenarios:
  - Existing `TestBridgeConsumedInputsRetainIdentityAndOrder`, `TestBridgeConsumedInputKeepsRawWebText`, and the queued-input attribution test still pass. The last one still proves `prompt_header` on replay items equals the literal Alice and Bob headers.
  - Existing `TestNewOutboundMessageMarksGoalTurns` still proves the `GoalTurn` and turn-number values.
  - Existing `store_schema_test` migration case decodes old JSON carrying `RelativePath` and `RanAt` into `RoutineMessage{Agent: "a", Space: "Ops"}`. This covers R4 with no new test.
  - Existing routine manager tests still prove the trace IDs and the routine's `Agent` and `Space`.
- Verification: the build compiles with no reference to the removed names, and the touched packages' tests pass.

## Verification Contract

- `gofmt -l` on touched Go files prints nothing.
- `make -C internal/rocketclaw lint` passes.
- `make -C internal/rocketclaw coverage-profile GOTESTPKGS="./protocol ./backend ./frontend/routine ./frontend/rpc ./frontend/slack"` passes, plus `go test ./cmd/rocketclaw`. `frontend/rpc` and `frontend/slack` are not edited, but they consume `OutboundMessage` and the requester asked for them. `frontend/rpc` needs `bun install` in `internal/rocketclaw/web` first.
- `make -C internal/rocketclaw check-cloc-budget` passes, and source CLOC (the same `cloc` query the target uses) is below the baseline of 45366.
- `rg 'ConsumedHeader|GoalActive|RanAt:'` over `internal/rocketclaw` and `cmd/rocketclaw` finds only `backend/origin.go`'s unrelated `routineOrigin.RanAt`.

## Definition of Done

- R1 to R4 hold. Item 4 is skipped and the reason is recorded in the PR description.
- Every Verification Contract check passes.
- No dead locals, imports, or abandoned edits remain in the diff.
