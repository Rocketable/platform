# Workflow Agent Progress

## Goal

Show useful live activity from each workflow `agent()` call while retaining the aggregate workflow phase cards and the privacy boundary around worker results.

The Slack plan should no longer remain at a silent state such as `investigate · 0/2` while both workers are actively reasoning or using tools.

## User Experience

Each workflow phase owns one stable Slack task. The task's details accumulate attributed worker activity in serialized callback order:

```text
● investigate · 0/2
  failure-trace: grep: turn limit
  canonical-owner: read: prompt-improver.md
  canonical-owner: bash: inspect workspace tools
  failure-trace: bash: read the referenced Slack thread
```

Every observable activity is retained as one attributed line. RocketClaw never resends the full details snapshot through `chat.appendStream`; it sends only newly queued lines so Slack's accumulation behavior produces one correctly ordered transcript.

When a worker completes, its accumulated activity remains visible while the phase counter advances:

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

The stable call ID preserves attribution while the serialized callback sequence controls transcript order. Duplicate labels remain distinguishable by their call identity even when their display labels match.

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
→ owning Slack phase task details delta append
```

Parallel workers may produce activity concurrently. The workflow layer serializes activity publication so connector state never observes concurrent updates for the same turn.

Progress publication errors cancel and fail the workflow through the existing workflow error path. The system must not silently continue after losing the promised visibility contract.

## Slack Rendering

Slack retains declared pending phase tasks. RocketClaw queues each newly observed worker event with its owning phase ID and retains successfully delivered lines as ordered phase history.

For a healthy stream, Slack updates the owning phase through `chat.appendStream` using its stable `task_update` ID. Phase title/status changes are sent without details. Worker events are sent as separate task-update chunks in callback order, each containing exactly one attributed details delta. The first delivered line has no prefix; every later line begins with `\n` because Slack concatenates streamed details verbatim.

For a stream that has permanently ended, Slack sends the same phase through the existing `chat.update` Plan-block fallback. The complete retained history plus pending events becomes the task card's rich-text `details` object. Later fallback updates replace the full Plan block.

The phase task ID, title, status, chronological details, and sources must be equivalent across transports. Switching transport must not create worker task cards, duplicate delivered lines, lose pending lines, or reorder events.

Slack derives cumulative URL sources from the retained phase history and current pending lines using the existing activity-link extraction.

Each attributed line is normalized to one line and limited to 255 characters, leaving room for the optional leading newline within Slack's 256-character task-update limit. Retained fallback history stores those same bounded lines. Existing terminal titles, pending/skipped phase visibility, final-answer separation, fallback serialization, and queued-reply promotion remain unchanged.

## Completion Semantics

The engine emits a worker activity update only when the runner produces observable activity. It does not emit a label-only lifecycle line. Each details line uses `<label>: <activity>`.

Each observable activity appends one worker-attributed phase-detail line. Worker lifecycle status is represented by the owning phase counter and status, not by separate worker updates. When the agent call returns successfully, the aggregate phase `Complete` count advances while all delivered activity remains retained.

If the call fails, its latest activity remains in the phase details and the existing workflow failure path remains authoritative. No private provider error is added to phase details; the workflow terminal response reports failure through the existing final-answer path.

## Testing

Tests must verify:

- two parallel workers accumulate attributed lines in serialized callback order;
- pending phase tasks remain visible without worker details;
- repeated activity from one worker adds ordered lines without resending prior lines;
- explicit label, worker-name fallback, and deterministic fallback attribution;
- commentary, reasoning summaries, tool activity, and nested-agent activity are forwarded through the existing formatter;
- routine tool results and private assistant messages are absent;
- completion retains the accumulated activity while the phase counter advances;
- worker completion and aggregate phase completion advance independently;
- publication failure cancels the workflow;
- the first streamed line has no prefix and subsequent lines begin with `\n`;
- `chat.appendStream` deltas and fallback `chat.update` history render equivalent phase details;
- failed and in-flight requests preserve pending event order without duplication;
- concurrent worker updates are serialized and race-clean;
- final answer delivery, terminal plan titles, and queued-turn promotion remain unchanged.

## Non-Goals

- Displaying worker prompts or private final results.
- Replacing aggregate workflow phase cards.
- Adding user configuration for progress verbosity or retention.
- Persisting worker activity into managed conversation history or durable workflow summaries.
