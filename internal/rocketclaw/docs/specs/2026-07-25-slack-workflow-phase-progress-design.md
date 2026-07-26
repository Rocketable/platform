# Slack Workflow Phase Progress

## Goal

Keep workflow phase progress current and readable in Slack without accumulating obsolete counter snapshots in task-card details.

## Rendering Contract

Each workflow phase continues to use one stable `task_update.id` and its existing Slack status mapping.

For phases with zero or one scheduled call, the title remains the phase name:

```text
find
```

For fan-out phases with more than one scheduled call, the title includes replaceable completion progress:

```text
summarize · 3/8
```

The numerator is `Complete` and the denominator is `Scheduled`. The title changes as Slack receives updates for the same task ID. The status separately communicates pending, in-progress, complete, or error, so the title does not repeat a running count.

Routine workflow phase updates do not set `details`, `output`, or `sources`. Workflow errors remain in the final answer path instead of being accumulated in task details.

## Implementation

Change only `slackWorkflowPhaseChunks` in the Slack connector. Preserve task IDs, ordering, buffering, flush timing, terminal plan updates, and workflow engine counters.

Do not change Slack stream lifecycle, add connector state, retain previously flushed snapshots, or switch back to `chat.update`.

## Tests

Update the Slack phase chunk tests to assert:

- zero-call and one-call phases keep the plain phase title;
- fan-out titles render `name · complete/scheduled`;
- task IDs and statuses remain unchanged;
- generated chunks omit `details`, including across successive snapshots of the same phase;
- terminal `plan_update` behavior remains unchanged.
