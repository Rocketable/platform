# Slack Thinking Task-Update Experiment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Produce one deployable `main` commit that tests whether Slack `task_update` streaming preserves thinking-card fold state without changing answer-message behavior.

**Architecture:** Eligible human Slack turns create the thinking message with `chat.startStream` and one overall status task, then post the existing answer placeholder second. Before the existing debounce, the connector extracts each new activity from cumulative thinking snapshots. `chat.appendStream` appends each activity once as bounded, uniquely identified completed task entries inside the same thinking message. Before final answer delivery, RocketClaw synchronously flushes queued activities. Final answer delivery remains untouched, after which `chat.stopStream` completes only the overall status task. Recipient-less turns retain the verified non-streaming task-card path.

**Tech Stack:** Go 1.26.2+, `github.com/slack-go/slack`, Slack Web API stream chunks, `httptest`, `testify`, Jujutsu.

## Global Constraints

- Modify only thinking-message transport and state.
- Do not change answer placeholder creation, final answer update/post behavior, answer chunking, answer Blocks, answer attachments, or answer error behavior.
- Preserve thinking-first and answer-second reservation order.
- Preserve the two-second thinking debounce.
- Do not use `markdown_text` for thinking.
- Preserve literal diagnostic text, including `glob: **/APPLE.md`.
- Emit each new thinking activity once; never resend cumulative prior activities.
- Split an activity over 256 Unicode code points using the approved newline, sentence, whitespace, then hard-boundary priority while preserving every character.
- Preserve MCP Markdown/mention safety and task-card HTTP(S) link behavior on the non-streaming path.
- Add no configuration, persistence, dependency, package, exported symbol, or unrelated refactor.
- Variant A is a test-environment commit and is not release-ready until live acceptance and explicit selection.
- Use Jujutsu only; never use Git.

---

### Task 1: Reserve The Streamed Thinking Card

**Files:**
- Modify: `internal/rocketclaw/slackconnector/connector.go`
- Test: `internal/rocketclaw/slackconnector/connector_test.go`

**Interfaces:**
- Consumes: `slack.Client.StartStreamContext`, `slack.NewTaskUpdateChunk`, `slack.MsgOptionChunks`, existing `slackReplySlots`, and existing placeholder reservation call sites.
- Produces: private slot fields `thinkingStream bool` and `thinkingTaskID string`; placeholder helpers accept the originating recipient user ID.

- [ ] **Step 1: Write the failing reservation test**

Add a focused test that sets workspace team `T123`, reserves a human turn for `U123`, and requires this exact operation order:

```text
POST /chat.startStream
POST /chat.postMessage text=\u200B
```

Decode `chunks` from `chat.startStream` and assert one task update:

```json
{
  "type": "task_update",
  "id": "111.222",
  "title": "_Thinking..._",
  "status": "in_progress"
}
```

Assert `task_display_mode=timeline`, `thread_ts=111.222`, and recipient team/user IDs. Assert the answer request retains the baseline fields exactly: channel, thread timestamp, and zero-width `text`, with no stream chunks.

- [ ] **Step 2: Run RED**

```sh
go test ./internal/rocketclaw/slackconnector -run TestCreateReplyPlaceholdersStartsThinkingTaskStreamBeforeUnchangedAnswer -count=1
```

Expected: FAIL because the baseline uses two `chat.postMessage` calls.

- [ ] **Step 3: Implement minimal thinking reservation**

Store `auth.TeamID` in the connector. Pass the originating Slack user ID only at human message, goal, app-mention, and buffered-human reservation sites. For a known team and recipient, use the triggering message timestamp as the stable task ID, call `StartStreamContext` with one `TaskUpdateChunk` in `in_progress` state, then execute the existing answer-placeholder `PostMessageContext` call unchanged. Recipient-less call sites continue to post the existing thinking placeholder.

- [ ] **Step 4: Cover cleanup and recipient-less paths**

Add focused tests proving answer-placeholder failure stops and deletes the partial thinking stream with the established bounded cleanup context, and proving authenticated MCP/cron/automatic paths still use ordinary thinking task cards when no recipient exists.

- [ ] **Step 5: Run GREEN**

```sh
go test ./internal/rocketclaw/slackconnector -run 'Test(CreateReplyPlaceholdersStartsThinkingTaskStreamBeforeUnchangedAnswer|CreateReplyPlaceholdersCleansTaskStreamAfterAnswerFailure|RecipientlessThinkingKeepsTaskCard)' -count=1
```

Expected: PASS.

---

### Task 2: Stream Debounced Thinking Updates

**Files:**
- Modify: `internal/rocketclaw/slackconnector/connector.go`
- Test: `internal/rocketclaw/slackconnector/connector_test.go`

**Interfaces:**
- Consumes: Task 1 slot metadata and existing `slackThinkingState` timer/debounce lifecycle.
- Produces: stream metadata plus queued new activities, stable activity sequence, and one per-turn flush-completion signal in `slackThinkingState`; `flushProgressText` sends new bounded `task_update` entries for stream slots and retains `chat.update` for recipient-less slots.

- [ ] **Step 1: Write the failing thinking-update test**

Reserve a human thinking stream, publish cumulative progress containing:

```text
reasoning: **Finding instructions**
glob: **/APPLE.md
```

Send two cumulative snapshots, where the second adds a new activity, before flushing through the existing debounce function. Require one `/chat.appendStream` request, no `/chat.update`, no `markdown_text`, and exactly two completed `task_update` activity entries with unique ordered IDs and exact literal titles. Assert prior activity text appears only once. The overall `_Thinking..._` task remains the initial stream task and is not resent.

- [ ] **Step 2: Run RED**

```sh
go test ./internal/rocketclaw/slackconnector -run TestFlushProgressUsesTaskUpdateWithoutChangingDiagnostics -count=1
```

Expected: FAIL because baseline calls `chat.update` with a task-card Block.

- [ ] **Step 3: Implement the stream branch in the existing debounce**

Carry `thinkingStream`, the overall task ID, queued newly extracted activities, the next activity sequence, and one flush-completion signal into `slackThinkingState`. In `bufferProgressText`, compare each cumulative snapshot with the prior snapshot and queue only the exact newly appended activity. Split queued activities with a private Unicode-safe helper using the approved boundary priority. In `flushProgressText`, create one completed `TaskUpdateChunk` per activity part, using the exact part as `Title` and a stable ID containing activity and continuation order, then call `AppendStreamContext` once with those chunks. Do not use Markdown text. Leave the existing non-streaming `UpdateMessageContext` branch unchanged.

- [ ] **Step 4: Preserve timer and retry semantics**

Keep the existing two-second timer reset behavior. On append success, remove only the emitted activity queue and advance the stable sequence. On append failure, return the Slack error and retain the complete queue and sequence for the next update. Do not add retries, clocks, goroutines, or a second timer.

- [ ] **Step 5: Run GREEN and recipient-less regression tests**

```sh
go test ./internal/rocketclaw/slackconnector -run 'Test(FlushProgressUsesTaskUpdateWithoutChangingDiagnostics|SendResponseKeepsHumanThinkingTaskCardLifecycle|SlackThinkingBlocksRenderLinks)' -count=1
```

Expected: PASS, with the baseline lifecycle test adjusted only where the thinking endpoint differs; answer assertions remain identical.

---

### Task 3: Flush Thinking Before And Complete It After The Unchanged Answer

**Files:**
- Modify: `internal/rocketclaw/slackconnector/connector.go`
- Test: `internal/rocketclaw/slackconnector/connector_test.go`

**Interfaces:**
- Consumes: Task 1-2 stream metadata and existing `finishCompleteResponse` ordering.
- Produces: a required final flush before existing answer delivery, `chat.stopStream` that completes only the overall status task after answer delivery, and stream-aware abort/pending cleanup that waits for an active append.

- [ ] **Step 1: Write the failing complete lifecycle test**

Record all Slack operations for a human turn with two activity entries. Require:

```text
start thinking stream
post unchanged answer placeholder
append thinking task update
update unchanged answer placeholder with Final answer
stop thinking stream with the overall Complete/complete task update
```

Assert the final answer request matches the Commit 0 baseline fields and Blocks exactly. Assert no answer content appears in stream chunks.

- [ ] **Step 2: Run RED**

```sh
go test ./internal/rocketclaw/slackconnector -run TestSendResponseCompletesThinkingTaskStreamAfterUnchangedAnswer -count=1
```

Expected: FAIL because baseline completes thinking through `chat.update`.

- [ ] **Step 3: Implement stream completion**

Before entering the existing answer delivery branch, synchronously flush queued activity entries. If that flush fails, return the error before answer delivery so normal abort cleanup remains valid. After answer delivery succeeds, stop the thinking stream with one `TaskUpdateChunk` using the overall task ID, title `Complete`, and status `complete`; do not resend activity details. Keep stop failure non-fatal so an already delivered answer is never deleted.

- [ ] **Step 4: Implement cleanup behavior**

Pending-placeholder cleanup and `AbortResponse` use the turn's flush-completion signal to wait for an active append before stopping the stream or deleting thinking/answer messages. They do not start another append. Empty final answers flush queued activities, stop thinking, and delete only the existing answer placeholder. Recipient-less cleanup remains unchanged.

- [ ] **Step 5: Run lifecycle GREEN tests**

```sh
go test ./internal/rocketclaw/slackconnector -run 'Test(SendResponseCompletesThinkingTaskStreamAfterUnchangedAnswer|SendResponseKeepsDeliveredAnswerWhenTaskStreamStopFails|CleanupStopsTaskStreamBeforeDeleting)' -count=1
```

Expected: PASS.

---

### Task 4: Review, Verify, Commit, And Hand Off Deployment

**Files:**
- Review: `internal/rocketclaw/slackconnector/connector.go`
- Review: `internal/rocketclaw/slackconnector/connector_test.go`
- Review: `docs/specs/2026-07-22-slack-thinking-task-update-experiment.md`

- [ ] **Step 1: Run formatting and language-server checks**

```sh
gofmt -w internal/rocketclaw/slackconnector/connector.go internal/rocketclaw/slackconnector/connector_test.go
go run golang.org/x/tools/gopls@latest check internal/rocketclaw/slackconnector/connector.go internal/rocketclaw/slackconnector/connector_test.go
```

- [ ] **Step 2: Review the actual diff**

Use `jj diff --git`. Confirm answer-related production hunks are unchanged except for surrounding thinking-state selection, all new fields are private, no single-line wrapper or defensive fallback was added, no stream Markdown exists, and no unrelated behavior moved.

- [ ] **Step 3: Run all required verification**

```sh
go test ./...
make lint
make test
```

Expected: PASS with race, coverage, and CLOC budgets satisfied.

- [ ] **Step 4: Commit Variant A to main**

```sh
jj commit -m "internal/rocketclaw: test streamed Slack thinking tasks"
jj bookmark move main --to @-
```

- [ ] **Step 5: Deployment handoff**

Report the exact commit ID with the instruction:

```text
Deploy this Variant A commit to the testing environment. Unfold the thinking card after its first update, wait for another update, let the turn complete, and send the Slack thread URL.
```

Do not describe the commit as release-ready.
