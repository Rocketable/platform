# Slack Stream Update Fallback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve the same Slack plan layout and progress content by falling back from `chat.appendStream` to `chat.update` when Slack reports that a stream has permanently ended.

**Architecture:** Keep `chat.startStream`, `task_update`, and `plan_update` as the normal path. Retain the rendered task model in the existing per-turn thinking state so a terminal stream-state error can replace the same message with Slack's non-streaming `plan` block, containing the same title, task IDs, titles, statuses, order, and sources. Once switched, all later progress and terminal updates use `chat.update`; transient append errors remain on the streaming path and retry unchanged.

**Tech Stack:** Go 1.26, slack-go streaming chunks and Block Kit `plan`/`task_card` blocks, Testify, Go testing.

## Global Constraints

- Treat only Slack's `message_not_in_streaming_state` and `stopped_by_user` responses as permanent stream loss.
- Update the existing message timestamp; do not post a replacement message or start another stream.
- Preserve the current plan title and every retained task's ID, title, status, order, and sources across the transport switch.
- Continue using `chat.appendStream` for healthy streams and retry transient failures such as `ratelimited` through the existing path.
- Preserve final-answer delivery, workflow execution, reply-stack serialization, and queued-turn promotion.
- Do not add retries, logging, exported symbols, packages, or compatibility paths beyond the required fallback.
- Use `jj diff --git`; do not use Git or create a commit unless explicitly requested.

---

### Task 1: Preserve Plan Tasks Across Stream Loss

**Files:**
- Modify: `internal/rocketclaw/slackconnector/connector.go:124-136`
- Modify: `internal/rocketclaw/slackconnector/connector.go:716-817`
- Modify: `internal/rocketclaw/slackconnector/connector.go:917-1161`
- Test: `internal/rocketclaw/slackconnector/connector_test.go:2314-2370`
- Test: `internal/rocketclaw/slackconnector/connector_test.go:2886-2923`
- Test: `internal/rocketclaw/slackconnector/connector_test.go:5944-6041`

**Interfaces:**
- Consumes: `slack.SlackErrorResponse.Err`, streamed `slack.TaskUpdateChunk` values, and the existing `slackThinkingState` queues.
- Produces: a private permanent-stream-error predicate and a `slack.PlanBlock` built from retained `slack.TaskUpdateChunk` values.
- Preserves: `Connector.SendResponse`, `flushProgressText`, and all external connector interfaces.

- [ ] **Step 1: Write a failing append-fallback contract test**

Add a test that starts with a streamed workflow plan and records API calls. Make the first `/chat.appendStream` request return:

```json
{"ok":false,"error":"message_not_in_streaming_state"}
```

Require the same flush to call `/chat.update` for the original `channel` and `ts`. Decode `blocks` as a `slack.PlanBlock` and assert the exact fallback payload:

```json
{
  "type": "plan",
  "title": "Thinking...",
  "tasks": [
    {
      "type": "task_card",
      "task_id": "run/phase/000000/audit",
      "title": "audit · 2/3",
      "status": "in_progress"
    }
  ]
}
```

Then buffer `audit · 3/3` with complete status and flush again. Assert that the second progress request is `/chat.update`, not `/chat.appendStream`, and that it replaces the same task ID in place rather than adding a duplicate.

- [ ] **Step 2: Write failing retention and terminal tests**

Extend the fallback test with one successfully appended diagnostic activity before stream loss. Assert that the fallback plan contains the previously appended activity first and the current workflow phase second, preserving the streamed task IDs, titles, statuses, and sources.

Complete the response with `WorkflowTerminal == workflow.TerminalComplete`. Assert that finalization uses `/chat.update`, does not call `/chat.stopStream`, keeps all retained tasks, and changes only the plan title to:

```text
Workflow complete
```

Keep the existing answer-placeholder assertion so the final answer remains separate from progress. Seed one buffered Slack reply and assert it is promoted only after completion, preserving the existing serialized turn behavior.

- [ ] **Step 3: Write a failing stopped-by-user test**

Table-drive the permanent error behavior for both documented Slack errors:

```go
[]string{"message_not_in_streaming_state", "stopped_by_user"}
```

For each error, assert one failed `/chat.appendStream`, followed by `/chat.update`, followed by future progress through `/chat.update` only.

- [ ] **Step 4: Run focused tests and verify RED**

Run:

```sh
go test ./internal/rocketclaw/slackconnector -run 'TestFlushProgress(FallsBackToPlanUpdateWhenStreamEnded|RetainsStreamAfterAppendFailure)|TestSendResponseCompletesFallbackPlanAndPromotesQueuedReply' -count=1
```

Expected: the new tests fail because `flushProgressText` currently returns the permanent Slack error and leaves the turn in streaming mode; the existing transient-failure test passes.

- [ ] **Step 5: Retain the rendered task model**

Add one private field to `slackThinkingState`:

```go
tasks []slack.TaskUpdateChunk
```

When preparing activity and workflow-phase chunks, merge each task into this slice by stable task ID with `slices.IndexFunc`: replace an existing entry in place or append a new entry. This keeps declaration order while allowing phase counters and statuses to replace prior snapshots.

Change `bufferProgressText` to queue activity deltas whenever `thinkingTaskID` is present, not only while `thinkingStream` is true. This lets the same task sequence continue after transport fallback without adding a second mode-specific progress model.

Do not retain `plan_update` chunks in `tasks`; the plan title remains derived from the current placeholder or workflow terminal value.

- [ ] **Step 6: Render the retained model as a plan block**

Add a private renderer used by fallback progress and fallback finalization:

```go
func slackThinkingPlanBlock(title string, tasks []slack.TaskUpdateChunk) *slack.PlanBlock {
	planTasks := make([]*slack.TaskCardBlock, len(tasks))
	for i := range tasks {
		task := slack.NewTaskCardBlock(tasks[i].ID, tasks[i].Title).WithStatus(tasks[i].Status)
		if len(tasks[i].Sources) > 0 {
			task.WithSources(tasks[i].Sources...)
		}
		planTasks[i] = task
	}

	return slack.NewPlanBlock(title).WithTasks(planTasks...)
}
```

Use the same title text as streaming:

```go
strings.TrimSuffix(strings.TrimPrefix(pending.Placeholder, "_"), "_")
```

For terminal responses, preserve the existing `Complete`, `Workflow complete`, `Workflow failed`, and `Workflow stopped` titles.

- [ ] **Step 7: Switch transport on permanent stream errors**

Add a private predicate using Go 1.26 typed extraction:

```go
func slackStreamEnded(err error) bool {
	errSlack, ok := errors.AsType[slack.SlackErrorResponse](err)
	return ok && (errSlack.Err == "message_not_in_streaming_state" || errSlack.Err == "stopped_by_user")
}
```

In `flushProgressText`, preserve the current success and transient-error behavior. On a permanent error:

1. Set `thinkingStream` to false in both `c.thinking[turnID]` and `c.replies[turnID]` while holding `c.mu`.
2. Keep the retained tasks and pending queues intact until `chat.update` succeeds.
3. Call `UpdateMessageContext` for the same channel and timestamp with notification text from `slackThinkingMessage` and one `slack.PlanBlock`.
4. On success, consume the activity and phase snapshots exactly as a successful append does.
5. Return `nil`; if `chat.update` fails, return that update error and leave the turn in non-streaming mode so the next flush retries `chat.update`, never the dead stream.

In the non-streaming branch, use the plan renderer when `thinkingTaskID` is present. Preserve `slackThinkingBlocks` unchanged for messages that were non-streaming from creation.

- [ ] **Step 8: Finalize through the active transport**

In `finishCompleteResponse`, use `chat.stopStream` only while the stored reply state remains streaming. If `chat.stopStream` itself returns a permanent stream error, switch the stored state and immediately finalize the same message through `chat.update` with the retained plan tasks and terminal title.

If the turn already fell back during progress, skip `chat.stopStream` and finalize directly through `chat.update`. Preserve current best-effort completion logging and final-answer delivery semantics.

- [ ] **Step 9: Format and run focused tests**

Run:

```sh
gofmt -w internal/rocketclaw/slackconnector/connector.go internal/rocketclaw/slackconnector/connector_test.go
go test ./internal/rocketclaw/slackconnector -run 'TestFlushProgress(FallsBackToPlanUpdateWhenStreamEnded|RetainsStreamAfterAppendFailure)|TestSendResponseCompletesFallbackPlanAndPromotesQueuedReply|TestWorkflowPhase' -count=1
```

Expected: PASS. The existing `ratelimited` regression test must still make two identical `/chat.appendStream` requests and no fallback update.

### Task 2: Verify Lifecycle and Repository Contracts

**Files:**
- Inspect: `internal/rocketclaw/slackconnector/connector.go`
- Inspect: `internal/rocketclaw/slackconnector/connector_test.go`
- Inspect: `README.md`

**Interfaces:**
- Consumes: the completed Task 1 implementation.
- Produces: verification evidence only; no additional production concepts.

- [ ] **Step 1: Run the Go standards pass before broad tests**

Inspect `jj diff --git` and confirm:

- error locals begin with `err`;
- no one-line delegating wrapper or single-use single-line helper was added;
- the only new state is the retained task slice;
- no defensive fallback, timer, goroutine, mutex, exported name, or context lifetime was added;
- permanent errors switch transport exactly once;
- transient errors preserve queued snapshots and stream retry behavior;
- activity order, phase replacement, final answer delivery, and queued reply promotion are unchanged.

- [ ] **Step 2: Run required repository verification**

Run sequentially:

```sh
go test ./...
make lint
make test
```

Expected: all commands pass, including race, coverage, CLOC, and metric checks.

- [ ] **Step 3: Run final diagnostics and diff review**

Run:

```sh
go run golang.org/x/tools/gopls@latest check internal/rocketclaw/slackconnector/connector.go internal/rocketclaw/slackconnector/connector_test.go
jj diff --git
jj status
```

Expected: no diagnostics; only the Slack connector, focused tests, and this plan are changed.

- [ ] **Step 4: Consider README impact**

Inspect `README.md` for a user-facing contract covering Slack progress transport. Expected: no README update is needed because this is recovery behavior that preserves the existing visible plan contract rather than adding configuration or commands. Record the conclusion in the final response.
