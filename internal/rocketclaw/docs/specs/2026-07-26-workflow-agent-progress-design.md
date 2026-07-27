# Workflow Agent Progress

## Goal

Show useful live activity from each workflow `agent()` call while retaining the aggregate workflow phase cards and the privacy boundary around worker results.

The Slack plan should no longer remain at a silent state such as `investigate · 0/2` while both workers are actively reasoning or using tools.

## User Experience

Each workflow phase owns one stable Slack task. The task's details show the latest attributed activity from each worker in that phase:

```text
● investigate · 0/2
  failure-trace: grep: turn limit
  canonical-owner: read: prompt-improver.md
```

Only the latest activity for each call is shown. A new activity replaces that worker's line in the owning phase details rather than adding another task.

When a worker completes, its latest activity remains visible while the phase counter advances:

```text
● investigate · 1/2
  failure-trace: bash: Run focused tests
  canonical-owner: read: prompt-improver.md
```

The phase title and status report aggregate progress. Its details answer what each worker is doing.

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

The stable call ID controls replacement within the phase details. Duplicate labels therefore remain independent worker entries.

## Connector-Neutral Model

Workflow call activity is a separate connector-neutral progress concept. It must not be encoded in `PhaseUpdate.Details` or generic cumulative `ProgressText`.

Each activity update carries:

- the stable workflow run and call identity;
- the owning stable phase ID;
- the attribution label;
- the latest formatted activity text.

The workflow engine owns phase identity, call identity, and status. The isolated RocketCode runner owns the raw activity stream. The bridge publishes the connector-neutral update. Slack maps the update into the owning phase task's details.

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
→ owning Slack phase task details replacement
```

Parallel workers may produce activity concurrently. The workflow layer serializes activity publication so connector state never observes concurrent updates for the same turn.

Progress publication errors cancel and fail the workflow through the existing workflow error path. The system must not silently continue after losing the promised visibility contract.

## Slack Rendering

Slack retains declared pending phase tasks. It stores the latest activity by stable call ID and groups those entries by the owning phase ID.

For a healthy stream, Slack updates the owning phase through `chat.appendStream` using one stable `task_update`. The phase title and status keep their aggregate values; `details` contains one latest attributed line per worker, ordered by stable call ID.

For a stream that has permanently ended, Slack sends the same phase through the existing `chat.update` Plan-block fallback. The stream details string becomes the task card's rich-text `details` object.

The phase task ID, title, status, details, order, and sources must be identical across transports. Switching transport must not create worker task cards or turn latest-only replacement into a chronological feed.

Slack derives URL sources from the latest formatted activity using the existing activity-link extraction.

Combined details are limited to Slack's 256-character task-update limit. Existing terminal titles, pending/skipped phase visibility, final-answer separation, fallback serialization, and queued-reply promotion remain unchanged.

## Completion Semantics

The engine emits an in-progress worker activity update when the call begins. Its initial text is the attribution label alone; after the first diagnostic, it uses `<label>: <latest activity>`.

Each observable activity replaces that worker's phase-detail line. Worker lifecycle status is represented by the owning phase counter and status, not by separate worker updates. When the agent call returns successfully, the aggregate phase `Complete` count advances while the latest activity remains retained.

If the call fails, its latest activity remains in the phase details and the existing workflow failure path remains authoritative. No private provider error is added to phase details; the workflow terminal response reports failure through the existing final-answer path.

## Testing

Tests must verify:

- two parallel workers maintain separate latest lines in one phase task;
- pending phase tasks remain visible without worker details;
- repeated activity from one worker replaces its phase-detail line rather than accumulating lines;
- explicit label, worker-name fallback, and deterministic fallback attribution;
- commentary, reasoning summaries, tool activity, and nested-agent activity are forwarded through the existing formatter;
- routine tool results and private assistant messages are absent;
- completion retains the latest activity while the phase counter advances;
- worker completion and aggregate phase completion advance independently;
- publication failure cancels the workflow;
- `chat.appendStream` and fallback `chat.update` render identical phase details;
- concurrent worker updates are serialized and race-clean;
- final answer delivery, terminal plan titles, and queued-turn promotion remain unchanged.

## Non-Goals

- Retaining a chronological transcript of every worker activity.
- Displaying worker prompts or private final results.
- Replacing aggregate workflow phase cards.
- Adding user configuration for progress verbosity or retention.
- Persisting worker activity into managed conversation history or durable workflow summaries.
