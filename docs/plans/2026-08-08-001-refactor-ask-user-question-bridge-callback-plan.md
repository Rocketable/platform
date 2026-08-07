---
title: "Ask-User-Question Bridge Callback - Plan"
type: "refactor"
date: "2026-08-08"
topic: "ask-user-question-bridge-callback"
artifact_contract: "ce-unified-plan/v1"
artifact_readiness: "implementation-ready"
product_contract_source: "ce-brainstorm"
execution: "code"
---

# Ask-User-Question Bridge Callback - Plan

## Goal Capsule

- **Objective:** Make `ask_user_question` ownership obvious: the bridge that starts a turn says whether that turn can ask a human, and Slack owns the full wait-for-answer path, without changing who can ask humans or how Slack questions look and behave.
- **Authority:** Current RocketClaw behavior for tool visibility and Slack questions is the bar. This plan revises only the ownership path that the two-channel work used for questions (question on the response stream, answer as a special request).
- **Open blockers:** None.
- **Product Contract preservation:** Product Contract unchanged in meaning and R/F/AE/D IDs; planning resolves the three deferred carrier/broker/test questions into KTDs and units.
- **Execution profile:** Characterization-first on current exposure and Slack question tests, then replace the path by deletion of the detour rather than layering a parallel one.
- **Tail ownership:** Implementer owns focused package tests, `gofmt`, `gopls check` on touched files, `go test ./...`, `make lint`, `make test`, and final `jj diff --git` review. Do not edit `GO_SOURCE_CLOC_BUDGET`.

## Product Contract

### Summary

When a turn starts successfully, the origin bridge attaches how human questions work for that turn.
Slack can ask a human and waits until they answer inside Slack.
External MCP and cron do not offer `ask_user_question` to the model.
Agent settings can still turn the tool off even when Slack could ask.
Answers no longer travel as a special app request kind.

### Problem Frame

The two-channel wiring works, but `ask_user_question` is hard to follow: the RocketCode turn blocks in one place, the question UI is posted by a detached response consumer, and the answer returns through another request path and a central pending map.
The user accepts that the model turn must wait for a human.
They reject the split ownership and hidden callbacks.
Behavior must stay the same as today; only the path should get simpler.

### Requirements

**Capability on turn start**

- R1. When a bridge Request succeeds and starts a turn, the origin bridge supplies that turn's `AskUserQuestion` behavior on the success path.
- R2. Child work in that turn uses the same `AskUserQuestion` behavior the origin bridge supplied.
- R3. Bridges declare their own behavior. Interactive origins attach a real `AskUserQuestion` callback. Non-interactive origins attach no callback (`nil`), which means the tool is omitted.

**Who may ask a human**

- R4. Slack-originated human turns that already qualify for native questions today keep interactive `AskUserQuestion` (post UI, wait, return answer).
- R5. External MCP turns attach no ask callback (`nil`) and must not include `ask_user_question` in the model tool list.
- R6. Cron and other non-interactive runs attach no ask callback (`nil`) and must not include `ask_user_question` in the model tool list (same as today).
- R7. If agent configuration disables `ask_user_question`, the tool stays unavailable even when the origin bridge is interactive Slack.
- R8. User-visible exposure must match today's rules. This refactor must not newly enable or disable the tool for a turn class that already had a settled rule.

**Blocking ask**

- R9. Calling interactive `AskUserQuestion` blocks until a human answer is available, the wait is canceled, or the call fails.
- R10. The RocketCode `ask_user_question` tool calls that behavior and returns the answer or error. It does not drive a separate app-level question response stream.

**Slack owns the human path**

- R11. Slack posts the question UI, accepts the human answer, cleans up the question UI, and completes the blocked wait inside the Slack bridge.
- R12. Remove `RequestTextAnswerQuestion` (and equivalent answer-as-request routing). Answers do not re-enter the app as a special request kind.
- R13. Remove sending questions as `AskUserQuestionResponse` (or equivalent) on the active Request response stream for the happy path.
- R14. Slack question presentation, options, custom answer, delete-on-answer, and delete-on-cancel stay behaviorally the same as today unless a later approved change says otherwise.

**Compatibility**

- R15. Existing RocketCode execution, persistence, recovery, conversation IDs, workflows, goals, follow-up buffering, and Slack thread behavior stay unchanged except for the ownership path above.
- R16. External MCP stays turn-only for broadcasts and does not gain a public push or subscription transport in this work.
- R17. `AutoAskUserQuestion` (auto best-judgement without a human) is out of scope.

### Key Decisions

- D1. Origin bridge supplies `AskUserQuestion` on Request success over app-global post/answer wiring. (session-settled: user-directed — chosen over app policy map or always-Slack: ownership follows who started the turn)
- D2. Callback blocks until answer over start-only plus later answer delivery. (session-settled: user-directed — chosen over split start/answer paths: one owner for the wait)
- D3. Slack keeps answer handling private; drop answer-as-request. (session-settled: user-directed — chosen over keeping `RequestTextAnswerQuestion`: removes the detour)
- D4. Ship interactive Slack callback and nil-means-omit only; defer Auto. (session-settled: user-directed — chosen over shipping Auto now: not needed soon)
- D5. For non-interactive origins, required contract is omit the tool from the model tool list via nil callback, not "expose but short-circuit if called." (session-settled: user-directed — chosen over exposed short-circuit and over a named No func after Go could not compare funcs: matches today's perceived behavior with the simplest gate)
- D6. Behavioral parity is the acceptance bar. Architectural cleanup must not change who sees the tool or how Slack questions work for users. (session-settled: user-directed — chosen over behavior changes in the name of simplification)
- D7. This deliberately revises the two-channel plan's question path (question on response channel, answer as Request) while keeping the rest of that architecture.

### Key Flows

- F1. Slack human turn with a question
  - **Trigger:** Model calls `ask_user_question` on a qualifying Slack turn.
  - **Steps:** Origin Slack success path already attached interactive ask; tool calls it; Slack posts UI and waits; human answers in Slack; Slack finishes cleanup and returns the answer to the tool.
  - **Outcome:** Turn continues with the answer. No answer request kind crosses the app request channel.
  - **Covered by:** R1, R2, R4, R9-R14.

- F2. External MCP turn
  - **Trigger:** External MCP starts a turn.
  - **Steps:** Origin leaves ask callback nil; harness builds tools without `ask_user_question`.
  - **Outcome:** Model never sees the tool for that turn.
  - **Covered by:** R1, R3, R5, R8.

- F3. Cron / non-interactive run
  - **Trigger:** Scheduled or other non-interactive work runs.
  - **Steps:** Same as today: no `ask_user_question` in the tool list.
  - **Outcome:** No human question path.
  - **Covered by:** R6, R8.

- F4. Slack-capable origin but agent disables the tool
  - **Trigger:** Qualifying Slack turn where agent config disables `ask_user_question`.
  - **Steps:** Bridge may still be interactive; agent gate keeps the tool unavailable.
  - **Outcome:** Model does not get the tool.
  - **Covered by:** R7, R8.

### Acceptance Examples

- AE1. Slack question happy path
  - **Covers:** R4, R9-R14
  - **Given:** A qualifying Slack human turn where the tool is allowed
  - **When:** The model asks a question and a human picks an option
  - **Then:** The tool receives that answer, the Slack question UI is cleaned up as today, and no answer-as-request kind is used

- AE2. External MCP never sees the tool
  - **Covers:** R5, R8
  - **Given:** An External MCP turn
  - **When:** Tools are built for the model
  - **Then:** `ask_user_question` is absent from the tool list

- AE3. Cron never sees the tool
  - **Covers:** R6, R8
  - **Given:** A cron/raw non-interactive run
  - **When:** Tools are built for the model
  - **Then:** `ask_user_question` is absent from the tool list

- AE4. Agent disable still wins on Slack
  - **Covers:** R7
  - **Given:** A Slack turn that would otherwise allow questions, with agent config disabling the tool
  - **When:** Tools are built for the model
  - **Then:** `ask_user_question` is unavailable

- AE5. Cancel cleans up
  - **Covers:** R11, R14
  - **Given:** A posted unanswered Slack question
  - **When:** The wait is canceled
  - **Then:** The question UI is removed as today and the tool call ends with cancel/failure as today

### Scope Boundaries

**In scope**

- Origin-bridge `AskUserQuestion` on turn-start success
- Interactive Slack block-until-answer ownership
- Nil ask callback meaning omit tool from the model list
- Removing question-on-response-stream and answer-as-request paths
- Keeping agent-level disable behavior

**Out of scope**

- `AutoAskUserQuestion`
- Redesigning Slack question UI or copy
- Changing External MCP network API or adding push/subscribe
- Changing cron job semantics beyond preserving "no ask tool"
- Broad event-store, replay, or new multi-bridge interaction protocol work
- Raising or rearranging CLOC budget as part of this feature (budget remains a separate concern)

**Deferred**

- Named auto / best-judgement ask mode if a non-interactive origin ever needs model-visible asking without a human

### Success Criteria

- A reader can trace one Slack question in one path: origin attach → tool call → Slack wait → human answer → return.
- Tool visibility for Slack, External MCP, cron, and agent-disabled cases matches pre-change behavior.
- Answer-as-request and question-as-response-stream paths are gone for this feature.
- Existing Slack question UX and cleanup still pass their behavioral tests.

### Dependencies and Assumptions

- Assumes the two-channel Request/Broadcast shape remains the surrounding transport.
- Assumes current `nativeQuestionTurn`-class rules describe today's visibility bar and must be preserved in outcome even if the mechanism is replaced by origin-supplied behavior.
- Assumes Slack remains the only interactive human question surface in this work.
- Related plan: `docs/plans/2026-08-07-001-refactor-two-channel-clockwork-plan.md` (question path entries are superseded by this contract where they conflict).
- Read before coding: `CONCEPTS.md` (Managed Slack Thread, Buffered Follow-Up) and `docs/solutions/logic-errors/slack-root-app-mention-redelivery-cleared-buffered-follow-ups.md`.

### Outstanding Questions

**Resolve before planning**

- None.

**Deferred to planning**

- Resolved below as KTD1–KTD3.

## Planning Contract

### Technical Design

Today the path is:

1. Slack submits a text Request; app starts a harness turn with a global `questionBroker.ask`.
2. Tool call sends `AskUserQuestionResponse` on the Request response channel.
3. Detached `consumeOutput` / `handleInteraction` posts the Slack UI.
4. Human answer becomes `RequestTextAnswerQuestion` back into the app broker.
5. Broker unblocks the tool.

Target path:

1. Origin attaches turn-local `AskUserQuestion` when the turn is built from a successful origin Request (or equivalent non-Request start such as cron/raw).
2. Qualifying Slack human turns get Slack's interactive implementation.
3. External MCP and all non-interactive starts leave the ask callback nil.
4. Harness tool list: if callback is nil, omit the tool; if non-nil, add it when agent permission allows, and `Call` invokes that callback.
5. Slack posts UI, waits on private pending state, answers from its own interaction handlers, deletes UI, returns answer.
6. Delete broker, answer Request kind, and question Response interaction.

`start_new_thread` may keep using the response-stream interaction path; this plan does not redesign it.

### Key Technical Decisions

- KTD1. **Carrier is turn-local on the harness config / inbound turn inputs, not a new Request OK payload type.**
  When app builds a turn from Slack human inbound with a Slack reply target, set `AskUserQuestion` to Slack's interactive method.
  When app builds External MCP, cron/raw, system, recovery, or other non-interactive turns, leave ask callback nil.
  Children of a turn copy the parent turn's attached behavior (R2).
  Do not invent a second parallel policy table in app.
  **Governs R1–R3, R6.**

- KTD2. **Nil callback means omit the tool; non-nil means call it.**
  Tool list rule: if `AskUserQuestion == nil`, omit `ask_user_question` from the model tool list.
  If non-nil, include the tool when agent permission allows, and tool `Call` invokes that callback.
  Qualifying Slack human turns attach Slack's interactive method. External MCP, cron/raw, system, recovery, and other non-interactive turns leave it nil.
  Do not add a named `NoAskUserQuestion` func, reflect pointer compare, or second bool flag.
  **Exception (user-directed):** this field intentionally uses nil as "disabled/absent" even though repo AGENTS usually forbid nil behavior deps. Do not generalize that pattern elsewhere in this change.
  Keep agent permission / visibility as a second gate when the callback is non-nil (R7).
  Keep `nativeQuestionTurn` (or equivalent) for `start_new_thread` gating unless a later plan changes it.
  **Governs R4–R8, R17.**

- KTD3. **Move pending-question state into Slack; delete the app broker and detour types.**
  Slack `AskUserQuestion` becomes block-until-answer: post → wait → answer/cancel → delete → return.
  Remove: `askUserQuestionBroker`, `RequestTextAnswerQuestion`, `TextRequest.QuestionID` / `QuestionAnswer`, `AskUserQuestionResponse`, router `question` / `answerQuestion` wiring, `AskUserQuestionRequest.Response` if unused, and Slack `New(... answerQuestion ...)` external callback once answers stay internal.
  Keep `StartNewThreadResponse` handling on the response stream.
  **Governs R9–R14.**

- KTD4. **Delete detour code in the same change stack as the new path; no long-lived dual path.**
  Prefer one coherent JJ change or a short ordered stack that never leaves both answer transports live in mainline behavior.
  Net source should shrink or stay near-flat: broker deletion funds Slack private pending.
  Do not edit `GO_SOURCE_CLOC_BUDGET`.
  **Governs R12–R13, D6.**

- KTD5. **Do not touch Slack follow-up stack lifecycle while moving questions.**
  Preserve `beginSlackStack` idempotency and redelivery behavior from `docs/solutions/logic-errors/slack-root-app-mention-redelivery-cleared-buffered-follow-ups.md`.
  **Governs R15.**

### Implementation Units

### U1. Contract cleanup for ask path

- **Goal:** Keep the ask func signature; delete answer/question detour types when call sites allow.
- **Files:** `internal/rocketclaw/events/types.go`, `internal/rocketclaw/events/clockwork.go`, tests under `internal/rocketclaw/events/`
- **Approach:** No named `NoAskUserQuestion` symbol. Nil on the turn-local ask field is the omit signal. Remove `RequestTextAnswerQuestion`, question fields on `TextRequest`, and `AskUserQuestionResponse` once call sites are gone (may land with U3/U4 if needed for compile stages). Prefer deleting unused types rather than leaving stubs.
- **Patterns:** Existing `events` request/response types.
- **Test scenarios:**
  - Deleted symbols have no remaining references after U4.
- **Verification:** `go test ./internal/rocketclaw/events/...`
- **Depends on:** None
- **Requirements:** R3, R5, R12, R13

### U2. Slack private block-until-answer

- **Goal:** Slack owns post, wait, answer, delete, cancel.
- **Files:** `internal/rocketclaw/slackconnector/connector.go`, `internal/rocketclaw/slackconnector/connector_test.go`
- **Approach:** Change `AskUserQuestion` to block until answer or cancel. Hold pending state inside the connector. Wire button/custom handlers to complete that wait directly. Delete external `answerQuestion` callback constructor dependency once unused. Preserve current UI blocks, custom modal, delete-on-answer, delete-on-cancel. Do not change stack begin/buffer/promote behavior.
- **Patterns:** Existing `AskUserQuestion` / `DeleteUserQuestion` and interaction handlers; mutex-owned connector state for stacks (keep question pending under the same careful locking discipline without inventing extra mutexes if one already covers connector maps).
- **Test scenarios:**
  - AE1: choice answer unblocks and returns selected values; question deleted.
  - Custom text answer unblocks and returns custom; question deleted.
  - AE5: cancel/context done deletes unanswered question and returns error.
  - Existing unique action IDs / always-custom-button tests still pass.
  - `TestHandleAppMentionEventPreservesBufferedReplyAcrossRootRedelivery` still passes unchanged in intent.
- **Verification:** `go test ./internal/rocketclaw/slackconnector/...`
- **Depends on:** U1 type if signature changes; otherwise can lead and adapt in U3
- **Requirements:** R4, R9, R11, R12, R14, R15
- **Execution direction:** Characterization-first on existing Slack question tests before changing wait ownership.

### U3. Harness tool gate + turn-local attach

- **Goal:** Tool list and tool call use origin-attached behavior.
- **Files:** `internal/rocketclaw/harnessbridge/bridge.go`, `internal/rocketclaw/harnessbridge/bridge_test.go`, `internal/rocketclaw/harnessbridge/definitions_test.go` as needed, `internal/rocketclaw/app/thread_bridges.go`, `internal/rocketclaw/app/app.go`
- **Approach:** Stop global `bridgeConfig.AskUserQuestion = questionBroker.ask`. Qualifying Slack human turns set Slack's interactive callback; all other turns leave it nil. Tool builder: `if ask == nil { /* omit */ } else if agent allows { add tool; Call invokes ask }`. Do not set `req.Response` for questions. Preserve `start_new_thread` gating and response-stream interaction.
- **Patterns:** Current `askUserQuestionTool`, `nativeQuestionTurn`, permission evaluation, per-turn config construction in `thread_bridges.go`.
- **Test scenarios:**
  - AE2: External MCP / non-Slack inbound → ask nil → tool absent.
  - AE3: cron/raw path → ask nil → tool absent.
  - Qualifying Slack human + allow → non-nil ask → tool present and Call blocks on it.
  - AE4: qualifying Slack + agent deny/disable → tool unavailable.
  - Existing option filter / empty options / description tests still pass.
  - Child/start-new-thread path does not reintroduce question response stream.
- **Verification:** `go test ./internal/rocketclaw/harnessbridge/... ./internal/rocketclaw/app/...`
- **Depends on:** U1, U2
- **Requirements:** R1, R2, R4–R10, R13

### U4. Delete app detour and wire runtime

- **Goal:** Remove broker and answer Request path; finish app/clockwork cleanup.
- **Files:** `internal/rocketclaw/app/ask_user_question.go`, `internal/rocketclaw/app/ask_user_question_test.go`, `internal/rocketclaw/app/clockwork.go`, `internal/rocketclaw/app/clockwork_test.go`, `internal/rocketclaw/app/app.go`, `internal/rocketclaw/app/app_test.go`, `internal/rocketclaw/events/clockwork.go`, `cmd/rocketclaw/CHEATSHEET.md` only if exposure wording drifts
- **Approach:** Delete broker and tests that only covered response-channel/answer-request. Strip `requestTextRouter.question`, `answerQuestion`, `handleInteraction` question arm, and `dispatchTextRequest` answer case. Ensure Slack connector construction no longer needs answer callback into app. Grep for deleted symbols. Keep Broadcast/Request two-channel behavior otherwise intact.
- **Patterns:** Surgical deletion; two-channel plan remains for non-question paths.
- **Test scenarios:**
  - No remaining references to `RequestTextAnswerQuestion` / `AskUserQuestionResponse` / broker.
  - Clockwork still handles progress and `StartNewThreadResponse`.
  - App wiring starts Slack + External MCP without question broker.
  - Integration-level app/slack tests that exercised ask still pass under new ownership.
- **Verification:** `go test ./internal/rocketclaw/app/... ./internal/rocketclaw/events/... ./internal/rocketclaw/slackconnector/...`
- **Depends on:** U2, U3
- **Requirements:** R12, R13, R15, R16

### U5. Repository verification

- **Goal:** Full gates green without budget edits.
- **Files:** touched packages only; no budget file edits
- **Approach:** Run format/lint/tests. Confirm CLOC not raised via feature change. Spot-check one end-to-end mental trace against F1.
- **Test scenarios:**
  - `gofmt` clean on touched Go files
  - `gopls check` clean on touched Go files
  - `go test ./...`
  - `make lint`
  - `make test`
- **Verification:** commands above
- **Depends on:** U1–U4
- **Requirements:** Success criteria, R15

### Risks and Dependencies

| Risk | Mitigation |
|------|------------|
| Dual answer transports left half-live | KTD4: cut answer Request only after Slack private wait works; delete in same stack |
| Exposure regression (MCP/cron suddenly see tool) | U3 tests AE2/AE3; keep agent gate; compare to `nativeQuestionTurn` outcome |
| Slack follow-up redelivery broken while editing connector | KTD5; do not touch stack begin reset; keep existing redelivery test green |
| `start_new_thread` broken while removing question interaction | Leave `StartNewThreadResponse` path intact; targeted clockwork tests |
| CLOC growth | Delete broker/detour; measure after U4; simplify before any budget discussion |
| Child turns lose interactive ask | R2/KTD1: copy parent attached behavior into child turn config |

### Assumptions

- Current working copy already contains the two-channel connector wiring feature change; this plan builds on that code shape.
- Bookmark `refactor-two-channel-clockwork` may not point at `@`; implementer must inspect JJ state before mutating bookmarks.
- Separate user-owned CLOC budget raise stays separate from this feature work.

### Documentation Plan

- Update `cmd/rocketclaw/CHEATSHEET.md` only if exposure wording becomes wrong.
- No README update expected.
- Two-channel plan question bullets are superseded where they conflict; no mandatory rewrite of that historical plan.

### System-Wide Impact

- **Interaction graph:** Request response stream loses question interactions; Slack connector becomes wait owner.
- **Unchanged:** Broadcast fan-out, External MCP turn-only drops, cron broadcast reporting, follow-up buffering contract.
- **Failure modes:** Cancel mid-question must still delete UI; failed post must error the tool without leaking pending entries.

## Verification Contract

| Gate | Command | Expected |
|------|---------|----------|
| Package tests | `go test ./internal/rocketclaw/events/... ./internal/rocketclaw/slackconnector/... ./internal/rocketclaw/harnessbridge/... ./internal/rocketclaw/app/...` | Pass |
| Full tests | `go test ./...` | Pass |
| Lint | `make lint` | Zero issues, no new suppressions |
| Repo gate | `make test` | Pass including coverage/CLOC as configured |
| Format | `gofmt` on touched Go files | Clean |
| Types | `gopls check` on touched Go files | Clean |
| Diff review | `jj diff --git` | Detour symbols gone; no budget edit; no unrelated refactors |

## Definition of Done

- R1–R17 satisfied with behavioral parity for exposure and Slack UX.
- F1–F4 and AE1–AE5 covered by tests or preserved existing tests.
- App question broker and answer-as-request / question-as-response paths removed.
- `StartNewThreadResponse` path still works.
- Slack follow-up redelivery test still passes.
- Verification Contract commands pass.
- README impact considered: no update needed unless CHEATSHEET exposure text drifts.
- No `GO_SOURCE_CLOC_BUDGET` edit in this feature change.
