# Workflow Agent Progress Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show one attributed, replaceable Slack activity task for each workflow `agent()` call while preserving aggregate phase progress and private worker results.

**Architecture:** Add a connector-neutral `AgentUpdate` beside `PhaseUpdate`. The workflow engine owns stable call identity, attribution, lifecycle status, and serialization; the isolated RocketCode runner forwards only ordinary observable thinking through the existing formatter; the bridge publishes updates; Slack replaces one task per call across both streaming and `chat.update` fallback transports.

**Tech Stack:** Go 1.26, Starlark workflows, RocketCode `ChatResponse`, RocketClaw outbound events, slack-go stream chunks and plan blocks, Testify, Go testing.

## Global Constraints

- Show only the latest activity for each workflow agent call.
- Attribute activity by explicit Starlark label, then worker name, then deterministic phase call label.
- Keep worker activity tasks separate from aggregate phase cards.
- Forward commentary, reasoning summaries, tool activity, and nested-agent activity through `rocketcodeThinkingText`.
- Never publish `ChatResponseAssistantMessage`, worker prompts, schemas, structured values, or provider errors as activity.
- Preserve task ID, title, status, order, and sources across `chat.appendStream` and fallback `chat.update`.
- Preserve existing terminal titles, final-answer separation, workflow summaries, managed-history privacy, fallback serialization, and queued-reply promotion.
- Progress publication failures cancel and fail the workflow.
- Do not add verbosity configuration, persistence, exported APIs outside existing internal packages, packages, retries, logging, goroutines, timers, or mutexes.
- Use `jj diff --git`; do not use Git or create a new Jujutsu change unless explicitly requested.

---

### Task 1: Add Workflow Agent Activity Lifecycle

**Files:**
- Modify: `internal/rocketclaw/workflow/engine.go:50-85`
- Modify: `internal/rocketclaw/workflow/engine.go:118-161`
- Modify: `internal/rocketclaw/workflow/engine.go:490-613`
- Test: `internal/rocketclaw/workflow/engine_test.go`

**Interfaces:**
- Produces: `workflow.AgentUpdate`, `workflow.AgentProgressFunc`, and the revised `workflow.AgentRunFunc` and `workflow.Run` signatures.
- `AgentRunFunc`: `func(context.Context, AgentRequest, AgentThinkingFunc) (json.RawMessage, error)`.
- `AgentThinkingFunc`: `func(context.Context, string) error` receives already formatted latest activity text.
- `AgentProgressFunc`: `func(context.Context, AgentUpdate) error` receives serialized call lifecycle updates.
- `AgentUpdate`: `{CallID, Label, Activity string; Status PhaseStatus}`.
- Consumed by: Task 2 bridge/runner plumbing and Task 3 Slack rendering.

- [ ] **Step 1: Write failing lifecycle and attribution tests**

Add table-driven engine tests covering:

```go
tests := []struct {
	name, label, workerName, wantLabel string
}{
	{name: "explicit label", label: "failure-trace", workerName: "trace-investigator", wantLabel: "failure-trace"},
	{name: "worker fallback", workerName: "trace-investigator", wantLabel: "trace-investigator"},
	{name: "phase call fallback", wantLabel: "investigate call 1"},
}
```

The fake `AgentRunFunc` must invoke its `AgentThinkingFunc` twice with `read: prompt.md` and `grep: turn limit`, then return `"result"`. Assert exact call updates:

```go
[]workflow.AgentUpdate{
	{CallID: "run/agent/000000", Label: wantLabel, Status: workflow.PhaseInProgress},
	{CallID: "run/agent/000000", Label: wantLabel, Activity: "read: prompt.md", Status: workflow.PhaseInProgress},
	{CallID: "run/agent/000000", Label: wantLabel, Activity: "grep: turn limit", Status: workflow.PhaseInProgress},
	{CallID: "run/agent/000000", Label: wantLabel, Activity: "grep: turn limit", Status: workflow.PhaseComplete},
}
```

Assert the existing phase updates remain separate and reach `Complete == 1` only after the worker complete update.

- [ ] **Step 2: Write failing parallel replacement and publication-error tests**

Add one parallel workflow test with two labeled calls. Block both fake runners, emit activity from each, and assert:

- unique IDs `run/agent/000000` and `run/agent/000001`;
- labels remain attached to the correct call;
- `AgentProgressFunc` is never invoked concurrently;
- each completion retains that call's latest activity;
- phase updates still report scheduled `2`, running `2`, then complete `1/2` and `2/2`.

Add a test whose `AgentProgressFunc` returns `errors.New("activity unavailable")`. Assert `Run` returns that error and the fake runner either never starts when the initial update fails or observes cancellation when a thinking update fails.

- [ ] **Step 3: Run focused workflow tests and verify RED**

Run:

```sh
go test ./internal/rocketclaw/workflow -run 'TestAgent(ActivityLifecycle|ActivityParallel|ActivityProgressFailure)' -count=1
```

Expected: FAIL because the workflow API has no agent thinking callback or connector-neutral call update.

- [ ] **Step 4: Add the connector-neutral types and signatures**

Add beside `AgentRequest` and `AgentRunFunc`:

```go
// AgentThinkingFunc receives the latest observable activity from one isolated agent call.
type AgentThinkingFunc func(context.Context, string) error

// AgentRunFunc runs one isolated agent call.
type AgentRunFunc func(context.Context, AgentRequest, AgentThinkingFunc) (json.RawMessage, error)

// AgentUpdate reports one workflow agent call's latest observable activity.
type AgentUpdate struct {
	CallID, Label, Activity string
	Status                  PhaseStatus
}

// AgentProgressFunc receives serialized workflow agent activity updates.
type AgentProgressFunc func(context.Context, AgentUpdate) error
```

Revise `Run` to accept both the existing phase `ProgressFunc` and the required `AgentProgressFunc`. Store both callbacks on `engine`; neither callback is optional or nil-disabled.

- [ ] **Step 5: Emit one stable call lifecycle**

In the `agent()` builtin, capture a zero-based call sequence while enforcing the existing 1,000-call limit. Derive:

```go
callID := fmt.Sprintf("%s/agent/%06d", e.runID, callSequence)
callLabel := strings.TrimSpace(label)
if callLabel == "" {
	callLabel = strings.TrimSpace(request.Worker.Name)
}
if callLabel == "" {
	callLabel = fmt.Sprintf("%s call %d", phase, callSequence+1)
}
```

After existing scheduled/running phase updates, emit the initial in-progress `AgentUpdate`. Pass a real `AgentThinkingFunc` to `e.agent` that replaces `latestActivity` and emits the same call ID/label with in-progress status.

On success, emit complete status retaining `latestActivity`, then advance aggregate phase completion. On agent failure, emit error status retaining `latestActivity`, join a publication error if present, cancel with the combined error, and return it. Do not put the agent error text in `Activity`.

Serialize agent and phase publication with the existing engine mutex. Add a private method with a real body, parallel to `phaseCount`, that invokes `AgentProgressFunc`, cancels on publication failure, and returns the error.

- [ ] **Step 6: Update existing workflow test runners and run GREEN**

Update all workflow package `AgentRunFunc` test doubles to accept the required `AgentThinkingFunc`; pass an explicit inert `AgentProgressFunc` in tests that do not inspect activity:

```go
func(context.Context, workflow.AgentUpdate) error { return nil }
```

Run:

```sh
gofmt -w internal/rocketclaw/workflow/engine.go internal/rocketclaw/workflow/engine_test.go
go test ./internal/rocketclaw/workflow -count=1
```

Expected: PASS.

### Task 2: Forward Safe RocketCode Thinking And Publish It

**Files:**
- Modify: `internal/rocketclaw/harnessbridge/raw_run.go:134-245`
- Modify: `internal/rocketclaw/harnessbridge/bridge.go:843-879`
- Modify: `internal/rocketclaw/events/types.go:162-177`
- Test: `internal/rocketclaw/harnessbridge/raw_run_test.go`
- Test: `internal/rocketclaw/harnessbridge/bridge_test.go`

**Interfaces:**
- Consumes: Task 1's required `workflow.AgentThinkingFunc`, `workflow.AgentProgressFunc`, and `workflow.AgentUpdate`.
- Produces: `events.OutboundMessage.WorkflowAgent *workflow.AgentUpdate`.
- Preserves: private worker assistant messages as the only values returned to Starlark.

- [ ] **Step 1: Write a failing runner filtering test**

Extend the workflow runner provider fixture to emit, in order:

- a reasoning summary;
- assistant commentary;
- a tool start diagnostic with a link;
- a routine tool result;
- a final assistant message containing `PRIVATE WORKER RESULT`.

Invoke the runner with a collecting `AgentThinkingFunc`. Assert collected text equals the non-empty results of `rocketcodeThinkingText` for reasoning, commentary, and tool activity. Assert it excludes the routine result and `PRIVATE WORKER RESULT`. Assert the returned JSON still contains only the private final assistant result.

- [ ] **Step 2: Write a failing progress-cancellation test**

Make `AgentThinkingFunc` return `errors.New("publish activity")` on the first observable item. Assert the runner cancels its RocketCode loop and returns an error wrapping `publish workflow agent thinking: publish activity` rather than continuing to a successful private result.

- [ ] **Step 3: Write a failing bridge projection test**

Run a workflow through `Bridge.runWorkflow` with a fake agent runner that emits one thinking update. Read outbound events and assert:

```go
require.NotNil(t, outbound.WorkflowAgent)
assert.Equal(t, "failure-trace", outbound.WorkflowAgent.Label)
assert.Equal(t, "grep: turn limit", outbound.WorkflowAgent.Activity)
assert.Empty(t, outbound.ProgressText)
assert.Nil(t, outbound.WorkflowPhase)
```

Keep the existing phase event assertion separate. Add a publication failure test that proves an outbound activity bus error fails the workflow.

- [ ] **Step 4: Run focused harnessbridge tests and verify RED**

Run:

```sh
go test ./internal/rocketclaw/harnessbridge -run 'TestWorkflow(AgentRunnerForwardsSafeThinking|AgentRunnerCancelsOnThinkingFailure|PublishesAgentActivity)' -count=1
```

Expected: FAIL because the runner discards non-final responses and outbound messages have no workflow-agent field.

- [ ] **Step 5: Forward observable runner responses**

Set `Diagnostics: true` in the workflow runner's `rocketcode.Config`.

Run RocketCode with a child context and cancellation function. In the output loop:

```go
switch item.Kind {
case rocketcode.ChatResponseAssistantCommentary,
	rocketcode.ChatResponseAssistantTool,
	rocketcode.ChatResponseReasoningSummary:
	if thinking := rocketcodeThinkingText(item); thinking != "" {
		if err := thinkingProgress(runCtx, thinking); err != nil {
			errProgress = fmt.Errorf("publish workflow agent thinking: %w", err)
			cancel()
		}
	}
case rocketcode.ChatResponseAssistantMessage:
	// Preserve the existing private result accumulation.
}
```

After a progress error, continue draining output, wait for the loop, and return `errProgress`. Do not publish assistant messages, prompts, schemas, tool results suppressed by `rocketcodeThinkingText`, or provider errors.

- [ ] **Step 6: Publish connector-neutral activity from the bridge**

Add to `events.OutboundMessage`:

```go
WorkflowAgent *workflow.AgentUpdate
```

In `runWorkflow`, create one shared outbound sequence. Keep the existing phase callback and add an agent callback that creates an outbound message, sets only `WorkflowAgent`, and publishes it. Pass both required callbacks to `workflow.Run`.

The workflow engine guarantees the two callbacks are serialized, so sequence mutation remains single-threaded and requires no new mutex.

- [ ] **Step 7: Update call sites and run GREEN**

Update direct `newWorkflowAgentRunner` and `workflow.Run` test call sites with explicit inert callbacks where observation is not needed.

Run:

```sh
gofmt -w internal/rocketclaw/harnessbridge/raw_run.go internal/rocketclaw/harnessbridge/raw_run_test.go internal/rocketclaw/harnessbridge/bridge.go internal/rocketclaw/harnessbridge/bridge_test.go internal/rocketclaw/events/types.go
go test ./internal/rocketclaw/harnessbridge ./internal/rocketclaw/events -count=1
```

Expected: PASS.

### Task 3: Render Latest Worker Activity In Slack

**Files:**
- Modify: `internal/rocketclaw/slackconnector/connector.go:124-136`
- Modify: `internal/rocketclaw/slackconnector/connector.go:224-326`
- Modify: `internal/rocketclaw/slackconnector/connector.go:717-833`
- Modify: `internal/rocketclaw/slackconnector/connector.go:995-1235`
- Modify: `internal/rocketclaw/slackconnector/connector.go:1249-1308`
- Test: `internal/rocketclaw/slackconnector/connector_test.go`

**Interfaces:**
- Consumes: `events.OutboundMessage.WorkflowAgent` and `workflow.AgentUpdate` from Task 2.
- Produces: stable Slack worker task updates with identical stream and plan-block fallback rendering.
- Preserves: existing generic activity, workflow phase, terminal, fallback, and queue-order behavior.

- [ ] **Step 1: Write failing exact worker-task rendering tests**

Add table-driven tests for a private `slackWorkflowAgentChunks` renderer:

```go
tests := []struct {
	name string
	update workflow.AgentUpdate
	want string
}{
	{name: "initial", update: workflow.AgentUpdate{CallID: "run/agent/000000", Label: "failure-trace", Status: workflow.PhaseInProgress}, want: `[{"type":"task_update","id":"run/agent/000000","title":"failure-trace","status":"in_progress"}]`},
	{name: "latest", update: workflow.AgentUpdate{CallID: "run/agent/000000", Label: "failure-trace", Activity: "grep: turn limit", Status: workflow.PhaseInProgress}, want: `[{"type":"task_update","id":"run/agent/000000","title":"failure-trace: grep: turn limit","status":"in_progress"}]`},
	{name: "complete", update: workflow.AgentUpdate{CallID: "run/agent/000000", Label: "failure-trace", Activity: "bash: Run focused tests", Status: workflow.PhaseComplete}, want: `[{"type":"task_update","id":"run/agent/000000","title":"failure-trace: bash: Run focused tests","status":"complete"}]`},
	{name: "error", update: workflow.AgentUpdate{CallID: "run/agent/000000", Label: "failure-trace", Activity: "read: prompt.md", Status: workflow.PhaseError}, want: `[{"type":"task_update","id":"run/agent/000000","title":"failure-trace: read: prompt.md","status":"error"}]`},
}
```

Add a link case asserting sources are extracted from the latest activity. Add a long-title case asserting the complete attributed title is limited to Slack's 256-character task-title limit without splitting into extra task IDs.

- [ ] **Step 2: Write failing latest-only stream integration test**

Send initial, first-activity, second-activity, and complete `WorkflowAgent` messages for one call, plus one phase update. Flush after each snapshot. Assert every worker chunk uses the same ID and the titles replace in order; no generic `-activity-` IDs are created. Assert the phase task remains independent.

Add two parallel calls and assert deterministic first-seen ordering and no cross-call replacement.

- [ ] **Step 3: Write failing stream-to-update parity test**

Start with streamed worker and phase tasks, make the next append return `message_not_in_streaming_state`, and inspect fallback `chat.update`. Assert the plan block contains exactly the latest worker tasks and phase task with identical IDs, titles, statuses, order, and sources. Continue one worker through another update and completion; assert only its existing task changes.

Complete the workflow and assert worker tasks remain under `Workflow complete`, the final answer stays separate, and queued reply promotion remains after terminal delivery.

- [ ] **Step 4: Run focused Slack tests and verify RED**

Run:

```sh
go test ./internal/rocketclaw/slackconnector -run 'TestWorkflowAgent|TestSendResponseWorkflowAgent' -count=1
```

Expected: FAIL because Slack ignores `WorkflowAgent` and has no stable pending call-activity state.

- [ ] **Step 5: Buffer latest workflow-agent snapshots**

Add one pending map to `slackThinkingState`:

```go
workflowAgents map[string]workflow.AgentUpdate
```

In `SendResponse`, handle `msg.WorkflowAgent` before generic `ProgressText`: buffer a value copy keyed by `CallID`, set the existing Slack reply state and stream metadata, and arm the existing non-postponing workflow flush timer. Do not route worker activity through cumulative `ProgressText`.

- [ ] **Step 6: Render stable worker task chunks**

Add `slackWorkflowAgentChunks` that sorts map keys, creates one `slack.TaskUpdateChunk` per call ID, renders the initial label or `<label>: <activity>`, truncates the attributed title to 256 runes, maps in-progress/complete/error status, and extracts URL sources from the latest activity.

Extract the existing activity link scan into a reused private `slackTaskSources(text string) []slack.TaskCardSource` helper. Use it from both generic activity chunks and workflow-agent chunks; do not change generic activity output.

- [ ] **Step 7: Integrate worker snapshots into every transport path**

Where progress snapshots are cloned, add `maps.Clone(pending.workflowAgents)`. Build chunks in this order:

```go
chunks := slackThinkingActivityChunks(&pending, activities)
chunks = append(chunks, slackWorkflowAgentChunks(workflowAgents)...)
chunks = append(chunks, slackWorkflowPhaseChunks(phases)...)
```

Use this order in streaming flush, established `chat.update` flush, permanent append fallback, and terminal completion. Merge chunks into the existing retained `tasks` slice so stable IDs replace in place across transports.

Extend `slackConsumeThinkingSnapshots` to delete only workflow-agent snapshots whose current value still equals the sent snapshot, matching existing phase compare-before-delete semantics. This preserves updates arriving during an in-flight Slack request.

- [ ] **Step 8: Run focused and package tests GREEN**

Run:

```sh
gofmt -w internal/rocketclaw/slackconnector/connector.go internal/rocketclaw/slackconnector/connector_test.go
go test ./internal/rocketclaw/slackconnector -run 'TestWorkflowAgent|TestSendResponseWorkflowAgent|TestWorkflowPhase|TestFlushProgress|TestSendResponseSerializes' -count=1
go test -race ./internal/rocketclaw/slackconnector -run 'TestWorkflowAgent|TestSendResponseWorkflowAgent' -count=1
go test ./internal/rocketclaw/slackconnector -count=1
```

Expected: PASS.

### Task 4: Verify End-To-End Contracts

**Files:**
- Inspect: `internal/rocketclaw/workflow/engine.go`
- Inspect: `internal/rocketclaw/harnessbridge/raw_run.go`
- Inspect: `internal/rocketclaw/harnessbridge/bridge.go`
- Inspect: `internal/rocketclaw/events/types.go`
- Inspect: `internal/rocketclaw/slackconnector/connector.go`
- Inspect: corresponding touched tests
- Inspect: `README.md`

**Interfaces:**
- Consumes: Tasks 1-3 complete implementation.
- Produces: verification evidence only.

- [ ] **Step 1: Run the mandatory touched-diff Go standards pass**

Inspect `jj diff --git` and confirm:

- every error variable begins with `err` and every error type ends with `Error`;
- no single-use single-line helper or delegating wrapper was added;
- no nil-disabled injected callback exists;
- no defensive guard, exported API beyond internal package needs, context field, goroutine, timer, mutex, atomic operation, retry, logging, or unrelated refactor was added;
- worker assistant messages, prompts, schemas, structured values, and provider errors cannot reach outbound activity;
- phase counters, call statuses, callback serialization, latest-only replacement, stream fallback, terminal order, and queued reply promotion match the approved spec;
- source CLOC and coverage budgets were not changed.

- [ ] **Step 2: Run required repository verification**

Run sequentially:

```sh
gofmt -w internal/rocketclaw/workflow/engine.go internal/rocketclaw/workflow/engine_test.go internal/rocketclaw/harnessbridge/raw_run.go internal/rocketclaw/harnessbridge/raw_run_test.go internal/rocketclaw/harnessbridge/bridge.go internal/rocketclaw/harnessbridge/bridge_test.go internal/rocketclaw/events/types.go internal/rocketclaw/slackconnector/connector.go internal/rocketclaw/slackconnector/connector_test.go
go test ./...
make lint
make test
```

Expected: all commands pass, including race, coverage, CLOC, and metric checks.

- [ ] **Step 3: Run final diagnostics and inspect repository state**

Run:

```sh
go run golang.org/x/tools/gopls@latest check internal/rocketclaw/workflow/engine.go internal/rocketclaw/workflow/engine_test.go internal/rocketclaw/harnessbridge/raw_run.go internal/rocketclaw/harnessbridge/raw_run_test.go internal/rocketclaw/harnessbridge/bridge.go internal/rocketclaw/harnessbridge/bridge_test.go internal/rocketclaw/events/types.go internal/rocketclaw/slackconnector/connector.go internal/rocketclaw/slackconnector/connector_test.go
jj diff --git
jj status
```

Expected: no diagnostics and only the approved spec, plan, workflow engine, bridge/runner, event, Slack connector, and focused test files are changed.

- [ ] **Step 4: Update and verify README impact**

Update `README.md` to say saved workflows show both Slack phase progress and each worker's latest attributed activity. Do not add command or configuration documentation because those surfaces are unchanged. Record the conclusion in the final response.
