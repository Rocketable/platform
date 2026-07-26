# Workflow Agent Progress

## Goal

Show useful live activity from each workflow `agent()` call while retaining the aggregate workflow phase cards and the privacy boundary around worker results.

The Slack plan should no longer remain at a silent state such as `investigate · 0/2` while both workers are actively reasoning or using tools.

## User Experience

Each workflow agent call owns one stable activity task. Parallel calls update independently:

```text
● failure-trace: grep: turn limit
● canonical-owner: read: prompt-improver.md
● investigate · 0/2
```

Only the latest activity for each call is shown. A new activity replaces that call's previous title rather than adding another task.

When a worker completes, its latest activity remains visible and its task status changes to complete:

```text
✓ failure-trace: bash: Run focused tests
● canonical-owner: read: prompt-improver.md
● investigate · 1/2
```

The worker activity tasks and aggregate phase task remain separate. Activity answers what each worker is doing; the phase card reports scheduled and completed calls.

## Attribution

Each agent call receives a stable call ID and display label.

The display label is selected in this order:

1. The explicit Starlark `agent(..., label = "...")` value.
2. The configured worker name.
3. A deterministic label derived from the phase and call sequence.

The label is fixed for the lifetime of the call. Activity titles use:

```text
<label>: <latest activity>
```

The stable call ID, not the title, controls replacement. Duplicate labels therefore remain independent tasks.

## Connector-Neutral Model

Workflow call activity is a separate connector-neutral progress concept. It must not be encoded in `PhaseUpdate.Details` or generic cumulative `ProgressText`.

Each activity update carries:

- the stable workflow run and call identity;
- the attribution label;
- the latest formatted activity text;
- in-progress, complete, or error status.

The workflow engine owns call identity and status. The isolated RocketCode runner owns the raw activity stream. The bridge publishes the connector-neutral update. Slack maps the update to a stable task.

## Progress Sources

The isolated workflow runner enables the same diagnostics used by ordinary observable RocketCode turns. It forwards these response kinds through the existing `rocketcodeThinkingText` formatter:

- assistant commentary;
- reasoning summaries;
- tool activity;
- nested-agent activity.

The existing formatter remains responsible for the user-visible wording and for suppressing routine tool results that ordinary thinking messages do not show.

## Privacy Boundary

Worker final output remains private workflow data.

The runner must never publish `ChatResponseAssistantMessage` as activity. Those messages continue to form the private value returned by `agent()` to Starlark and may only affect the workflow's eventual explicit result.

Worker prompts, structured return values, schemas, and provider errors are not activity. Existing workflow persistence and managed-history exclusions remain unchanged.

## Data Flow

The progress path is:

```text
RocketCode ChatResponse
→ filter non-final observable response kinds
→ rocketcodeThinkingText
→ workflow call progress callback
→ serialized connector-neutral activity update
→ outbound event
→ Slack task replacement
```

Parallel workers may produce activity concurrently. The workflow layer serializes activity publication so connector state never observes concurrent updates for the same turn.

Progress publication errors cancel and fail the workflow through the existing workflow error path. The system must not silently continue after losing the promised visibility contract.

## Slack Rendering

Slack stores the latest activity update by stable call ID alongside the existing phase state.

For a healthy stream, Slack sends each activity through `chat.appendStream` as a stable `task_update`. For a stream that has permanently ended, Slack sends the same retained task through the existing `chat.update` plan-block fallback.

The task ID, title, status, order, and sources must be identical across transports. Switching transport must not duplicate the worker task or turn latest-only replacement into a chronological feed.

Slack derives URL sources from the latest formatted activity using the existing activity-link extraction.

Worker tasks remain in first-seen call order. Phase tasks retain their declared phase order. Existing terminal titles, final-answer separation, fallback serialization, and queued-reply promotion remain unchanged.

## Completion Semantics

The engine emits an in-progress worker activity task when the call begins. Its initial title is the attribution label alone; after the first diagnostic, the title uses `<label>: <latest activity>`.

Each observable activity replaces that task's title. When the agent call returns successfully, the engine emits a complete update retaining the latest activity title. The aggregate phase `Complete` count then advances independently.

If the call fails, its latest activity remains visible with error status and the existing workflow failure path remains authoritative. No private provider error is added to the worker activity task; the workflow terminal response reports failure through the existing final-answer path.

## Testing

Tests must verify:

- two parallel workers maintain separate stable tasks;
- repeated activity from one worker replaces its title rather than accumulating tasks;
- explicit label, worker-name fallback, and deterministic fallback attribution;
- commentary, reasoning summaries, tool activity, and nested-agent activity are forwarded through the existing formatter;
- routine tool results and private assistant messages are absent;
- completion retains the latest title and changes status to complete;
- worker completion and aggregate phase completion advance independently;
- publication failure cancels the workflow;
- `chat.appendStream` and fallback `chat.update` render identical worker tasks;
- concurrent worker updates are serialized and race-clean;
- final answer delivery, terminal plan titles, and queued-turn promotion remain unchanged.

## Non-Goals

- Retaining a chronological transcript of every worker activity.
- Displaying worker prompts or private final results.
- Replacing aggregate workflow phase cards.
- Adding user configuration for progress verbosity or retention.
- Persisting worker activity into managed conversation history or durable workflow summaries.
