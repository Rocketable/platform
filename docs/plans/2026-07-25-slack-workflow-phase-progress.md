# Slack Workflow Phase Progress Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render current workflow fan-out progress in replaceable Slack task titles without accumulating obsolete details.

**Architecture:** Keep the existing stable task IDs, status mapping, buffering, and stream transport. Change only phase chunk presentation: plain titles for zero/one scheduled call, `name · complete/scheduled` for fan-out, and no `details` payload.

**Tech Stack:** Go 1.26, slack-go streaming chunks, Testify, Go testing.

## Global Constraints

- Preserve `task_update.id`, status mapping, task ordering, flush timing, and terminal `plan_update` behavior.
- Do not change workflow engine counters or add Slack connector state.
- Do not use `details`, `output`, or `sources` for routine phase progress.
- Use `jj diff --git`; do not create a commit unless explicitly requested.

---

### Task 1: Move Fan-Out Counts Into Slack Task Titles

**Files:**
- Modify: `internal/rocketclaw/slackconnector/connector.go:1130-1156`
- Test: `internal/rocketclaw/slackconnector/connector_test.go:5944-6010`

**Interfaces:**
- Consumes: `workflow.PhaseUpdate{Name, Status, Scheduled, Complete}`.
- Produces: the existing `[]slack.StreamChunk` from `slackWorkflowPhaseChunks`, with unchanged IDs/statuses and revised titles.

- [x] **Step 1: Write failing rendering tests**

Update `TestWorkflowPhaseUsesStableTaskUpdateAndTerminalPlan` to expect this append payload:

```json
[{"type":"task_update","id":"run-1/phase/audit","title":"audit · 2/3","status":"in_progress"}]
```

Extend `TestWorkflowPhaseChunksPreserveOrder` with exact assertions that zero-call and one-call phases retain plain names and omit `details`.

Add a focused case that renders two snapshots for the same phase ID, first `summarize · 0/8` and then `summarize · 3/8`, asserting both chunks omit `details` and preserve the same ID.

- [x] **Step 2: Run focused tests and verify RED**

Run:

```sh
go test ./internal/rocketclaw/slackconnector -run 'TestWorkflowPhase(UsesStableTaskUpdateAndTerminalPlan|ChunksPreserveOrder|TitlesReplaceProgress)' -count=1
```

Expected: FAIL because current chunks use the plain title and populate `details` with counter snapshots.

- [x] **Step 3: Implement minimal title rendering**

In `slackWorkflowPhaseChunks`, construct the title before creating the chunk:

```go
title := update.Name
if update.Scheduled > 1 {
	title += fmt.Sprintf(" · %d/%d", update.Complete, update.Scheduled)
}

chunk := slack.NewTaskUpdateChunk(id, title)
```

Keep the existing status switch. Delete the `chunk.Details` construction and truncation; do not replace it with another field.

- [x] **Step 4: Run focused tests and verify GREEN**

Run:

```sh
gofmt -w internal/rocketclaw/slackconnector/connector.go internal/rocketclaw/slackconnector/connector_test.go
go test ./internal/rocketclaw/slackconnector -run 'TestWorkflowPhase(UsesStableTaskUpdateAndTerminalPlan|ChunksPreserveOrder|TitlesReplaceProgress)' -count=1
```

Expected: PASS.

- [x] **Step 5: Run repository verification**

Run sequentially:

```sh
go test ./...
make lint
make test
```

Expected: all commands pass, including race, coverage, CLOC, and metric checks.

- [x] **Step 6: Inspect final changes**

Run:

```sh
go run golang.org/x/tools/gopls@latest check internal/rocketclaw/slackconnector/connector.go internal/rocketclaw/slackconnector/connector_test.go
jj diff --git
jj status
```

Expected: no diagnostics and only the approved Slack phase rendering, tests, spec, and plan are changed.
