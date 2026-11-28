---
title: Remove Stored Report Thread Handling - Plan
type: refactor
date: 2026-11-27
artifact_contract: ce-unified-plan/v1
product_contract_source: ce-plan-bootstrap
execution: code
---

# Remove Stored Report Thread Handling - Plan

## Goal Capsule

- **Objective:** RocketClaw no longer carries code, queries, tests or docs for the stored conversations of Slack threads that cron reports and External MCP sessions used before they became Web chats. How Slack answers today's threads does not change.
- **Means:** Delete the report flag from `MentionThread`, the connector branches that read it, the routine-creator clause in the shared chat-origin SQL, and the stored-record part of the Report Thread docs (KTD1, KTD2, KTD3).
- **Authority:** the owner's decision, carried as KTD1 and KTD2, then this plan, then the repository's `AGENTS.md`.
- **Stop conditions:** stop and report if a current code path still creates `slack_thread` conversations with `created_by = 'routine'`. Also stop if removing the flag would change what Slack does with a thread under an unmarked bot root.
- **Execution profile:** one PR, Lightweight. No migration and no schema change.
- **Finish:** `ce-work` implements and verifies. The calling pipeline reviews, ships and watches CI. It does not merge.

## Product Contract

### Summary

Delete the stored-record half of Report Thread handling: the `report` result of `MentionThread`, both connector checks of it, the `OR created_by = $2` clause, the tests that only covered them, and the docs that describe stored report threads. Keep the check that ignores a thread under a bot-posted root without the Channel Session mark, and document it as a general Slack rule.

### Problem Frame

Before migration `069_external_mcp_web_chats.sql`, cron reports and External MCP sessions posted into Slack threads, and RocketClaw kept those threads read-only. Migration 069 turned their conversations into Web chats, many of which keep their `slack-thread:<channel>:<ts>` IDs until retention deletes them 90 days after their last message. The code still checks every Slack mention and ambient message for a routine creator or an External MCP binding on the thread's conversation. It also still filters chat origins on routine-created `slack_thread` rows, which no code path writes any more, though older ones remain.

### Requirements

**Removal**

- R1. `PrimaryTextRouter.MentionThread` returns only whether the thread's conversation is recorded, and the backend no longer looks up External MCP bindings or the thread creator to answer it.
- R2. The Slack connector's mention and ambient paths no longer read or act on a report flag.
- R3. The chat-origin SQL no longer treats routine-created `slack_thread` rows specially, and every query sharing it keeps working.
- R4. Tests that only covered stored report threads are deleted; tests that stub `MentionThread` follow the new signature.
- R5. `CONCEPTS.md` and the Slack section of `README.md` no longer describe stored report threads. Release notes keep their history.

**Kept behavior**

- R6. A mention or ambient message in an unrecorded thread whose root RocketClaw's bot posted without the Channel Session mark is still ignored.
- R7. The pre-`sticky: slack` continuation, where `slackSessionID` resolves a managed `slack-thread:<channel>:<ts>` conversation, is untouched, and so are old Slack button acks.

### Key Decisions

- **Remove the stored report flag and its query clause.** (session-settled: user-approved — chosen over keeping the read-only handling for pre-069 threads: new work never creates those threads, and odd behavior on old data is accepted until retention removes it.) Governs R1, R2, R3, R5.
- **Keep the managed-thread continuation and old button acks.** (session-settled: user-directed — chosen over removing them with Report Threads: the continuation is still heavily used.) Governs R7.

### Scope Boundaries

- The unrecorded bot-root check stays. Removing it would make mentions in surviving Slack threads under unmarked bot roots start Thread Sessions. Those threads include issues `cmd/harnessfeedback` posts with RocketClaw's own bot (`README.md`, harnessfeedback section) and older cron and MCP threads still visible in Slack. Changing that is a product decision for the owner (Open Questions).
- `docs/verification/slack-sticky/` keeps the bot-rooted thread that R6 keeps. Its classify step ignores that thread only while it has no conversation, as the code does, and its README records the older chats the model does not start from.
- Historical plans, solutions and investigations under `docs/` stay as written.
- README release notes stay as written. They describe past releases, and the threads they call read-only are still ignored under R6.

### Open Questions

- Should RocketClaw stop ignoring threads under bot-posted roots that lack the Channel Session mark? Doing so deletes the root check in `handleAppMentionEvent` and the `root.User == c.botUserID` clause in `handleMessageEvent`, along with their test cases and README sentence. It would let mentions in harnessfeedback issue threads and in old cron and MCP Slack threads start Thread Sessions. This does not block the plan: it decides a possible follow-up commit.

## Planning Contract

### Key Technical Decisions

- KTD1. **`MentionThread` returns `(recorded bool, err error)`.** It keeps the `slackSessionID` lookup and the `store.Thread` read of the resolved conversation. The `store.Thread(managed)` read and `ExternalMCPSessionByConversationID` only fed the flag, so both go. The interface, the backend implementation, the inert test router and the generated mock change together. Implements the first Key Decision, so R1, R2.
- KTD2. **Drop `OR created_by = $2` and renumber the shared parameters.** `$2` is `ThreadCreatedByRoutine`. The clause only matters for `slack_thread` rows with `created_by = 'routine'`. Migration 069 converted every cron-created `slack_thread` row to `web_chat`, migration 116 renamed `cron` to `routine` afterwards, and today `slack_thread` rows are created with an empty creator only (`threadBridgeManager.SubmitMention`, `Bridge` turn moves). PostgreSQL rejects an unreferenced positional parameter, so the routine-creator argument has to leave the queries that no longer use it. Implements the first Key Decision, so R3.
  1. `chatOriginSQL` reads the one-off kind from `$2` instead of `$3`.
  2. `ChatOriginFacts` and `messagePrincipals` pass `(filter, KindOneOffRoutineRun)`.
  3. `searchCandidates` drops `ThreadCreatedByRoutine` and renumbers `$4..$6` to `$3..$5`.
  4. `sidebarRows` still needs the routine creator for `pausable`, so it passes `KindOneOffRoutineRun` second and `ThreadCreatedByRoutine` third, and `pausable` reads `$3`.
- KTD3. **Leave the unrecorded bot-root check in place (R6).** Its `slices.IndexFunc` root lookup also feeds `recentRootMention`, so only the `report` lines around it change.

### Assumptions

- Older report chats still stored as `slack-thread:<channel>:<ts>`, routine-created or bound to an External MCP session, are continued by a mention or reply in that Slack thread when their agent is the channel's agent: the message queues into that Web chat and its answer does not reach Slack. Older routine-created Slack thread rows lose their routine origin. Both are accepted for old data until retention deletes it.
- The concurrent goal-status edits in `internal/rocketclaw/backend/store.go` do not touch the sidebar `pausable` line. The `goal` line just above it may change, which would mean resolving a rebase conflict by hand.

## Implementation Units

### U1. Backend and protocol: drop the report flag

- **Goal:** `MentionThread` answers only `recorded` (KTD1).
- **Requirements:** R1, R4, R7.
- **Dependencies:** none.
- **Files:** `internal/rocketclaw/protocol/conversation.go`, `internal/rocketclaw/backend/thread_bridges.go`, `internal/rocketclaw/backend/thread_bridges_test.go`, `internal/rocketclaw/frontend/slack/primarytextrouter_mocks_test.go` (regenerated with `make mocks`), `internal/rocketclaw/frontend/slack/inert_test.go`, `.github/rocketclaw-test-seconds.tsv`.
- **Approach:**
  1. Change the interface signature, then the backend method: delete the managed-thread read, the External MCP lookup and the flag.
  2. Delete `TestThreadBridgeManagerMentionThreadRecognizesReportThreads` and its timing line. Update the remaining `MentionThread` caller in `thread_bridges_test.go` to drop its `report` assertion.
  3. Regenerate mocks. Update `inertThreadRouter`.
- **Test scenarios:**
  - The existing mention-thread test still shows a managed `slack-thread:` conversation with the channel's agent as recorded, and a new thread as unrecorded. This covers R7.
- **Verification:** `./backend` and `./protocol` build and pass. No `report` result remains on `MentionThread`.

### U2. Slack connector: drop report branches

- **Goal:** neither Slack path reads a report flag (R2), and the bot-root check stays (KTD3).
- **Requirements:** R2, R4, R6.
- **Dependencies:** U1.
- **Files:** `internal/rocketclaw/frontend/slack/connector.go`, `internal/rocketclaw/frontend/slack/connector_test.go`.
- **Approach:**
  1. `handleAppMentionEvent`: delete the `if report { return }` block and the extra return value.
  2. `handleMessageEvent`: delete `report ||` from the early-return condition and the extra return value.
  3. Delete `TestHandleAppMentionEventIgnoresReportThread`. In the ignore table, delete the `Report Thread reply` row and the `report` field. Update the `MentionThreadFunc` stubs to the two-value signature.
- **Test scenarios:**
  - `TestHandleAppMentionEventThreadUnderBotRoot` still ignores a mention under an unmarked bot root and admits one under a marked Channel Session answer. This covers R6.
  - The ignore table still ignores a reply under an unmarked bot root in a Watched Channel. This covers R6.
- **Verification:** `./frontend/slack` passes. A grep for `report` in `connector.go` finds nothing tied to `MentionThread`.

### U3. Chat-origin SQL: drop the routine-creator clause

- **Goal:** `creating_source` no longer depends on the creator of a `slack_thread` row (R3, KTD2).
- **Requirements:** R3.
- **Dependencies:** none.
- **Files:** `internal/rocketclaw/backend/store.go`, `internal/rocketclaw/backend/session_search.go`, `internal/rocketclaw/backend/message_search.go`.
- **Approach:** follow KTD2's numbered steps. Change only the clause, the placeholders and the argument lists. Leave the `goal` line next to `pausable` alone.
- **Test scenarios:**
  - Existing sidebar, session-search, message-principal and chat-origin tests in `./backend` still pass. They run every query that shares `chatOriginSQL` against PostgreSQL, which would reject an unreferenced or mistyped parameter.
  - A routine-created Web chat still lists as pausable = false. Existing sidebar tests cover this.
- **Verification:** `./backend` passes. `OR created_by` no longer appears in `chatOriginSQL`.

### U4. Docs

- **Goal:** docs no longer describe stored report threads (R5) and still state the kept rule (R6).
- **Requirements:** R5, R6.
- **Dependencies:** U2.
- **Files:** `CONCEPTS.md`, `README.md`.
- **Approach:**
  1. `CONCEPTS.md`: delete the `Report Thread` entry and its relationship line. Add one sentence to `Thread Session` saying that a thread under a bot-posted root without the Channel Session mark has no Thread Session, and that RocketClaw ignores mentions and replies in it.
  2. `README.md`: replace the read-only cron and MCP thread paragraph in the Slack section with one sentence of the general rule. Leave release notes and the harnessfeedback sentence alone.
- **Test scenarios:** Test expectation: none -- documentation only.
- **Verification:** the only remaining `Report Thread` mentions are under `docs/`.

## Verification Contract

| Gate | Command |
|---|---|
| Format | `gofmt -l` on touched Go files prints nothing |
| Mocks | `make mocks` leaves the tree consistent with the new interface |
| Lint | `make -C internal/rocketclaw lint` |
| Tests | `make -C internal/rocketclaw coverage-profile GOTESTPKGS="./backend ./frontend/slack ./protocol"` |
| Size | `make -C internal/rocketclaw check-cloc-budget`; source CLOC after is lower than before |

## Definition of Done

- R1 through R7 hold, and each gate in the Verification Contract passes.
- Source CLOC went down. Record the before and after numbers.
- No abandoned attempt code, comments or stray scratch files remain in the diff.
- The diff does not touch `internal/rocketclaw/backend/store_schema.go` or any goal-status line.
