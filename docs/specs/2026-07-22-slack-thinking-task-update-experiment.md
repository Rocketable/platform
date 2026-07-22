# Slack Thinking Task-Update Experiment

## Goal

Test whether Slack's native `task_update` stream preserves a user's folded or unfolded thinking-card state while keeping the established thinking-card appearance and all answer-message behavior unchanged.

## Baseline

Commit `65eaa20440ce89aa20e754ce4dc8b54c12d34397` is the verified control. It has the required thinking-card appearance and content, but `chat.update` collapses the card after every update.

## Variant A

Variant A changes only the transport for eligible thinking messages:

1. `chat.startStream` creates the thinking message with one `task_update` chunk.
2. The existing zero-width answer placeholder is posted second with `chat.postMessage`.
3. The existing two-second thinking debounce remains.
4. `chat.appendStream` sends each newly received thinking activity once as one or more completed `task_update` entries inside the same thinking message.
5. Before final answer delivery, RocketClaw synchronously flushes every queued thinking activity. A failed final flush returns before the answer is delivered.
6. The existing answer implementation consumes the answer placeholder without modification.
7. `chat.stopStream` sends the final `Complete` task update after successful answer delivery.

No `markdown_text` is used for thinking. Turns without a natural Slack recipient retain the verified non-streaming task-card path.

## Fixed Behavior

- The answer placeholder, answer delivery, answer chunking, and answer attachments are unchanged.
- Thinking remains the first reserved message and answer remains the second.
- Running title remains `_Thinking..._` or the existing goal-progress phrase.
- Running status remains `in_progress`.
- Completion title remains `Complete` and status remains `complete`.
- Activities remain oldest-first and literal, including `glob: **/APPLE.md`.
- Prior activities are never resent. The connector extracts each new activity from the cumulative snapshots it receives before debounce.
- Each turn has one flush-completion signal. Cleanup waits for an active append to finish before it stops or deletes the thinking stream.
- Each activity chunk is a task title of at most 256 Unicode code points with a stable activity and continuation ID.
- Oversized activities split after the latest newline, sentence boundary, or whitespace at or before 256 code points, in that order; otherwise they split exactly at 256. Joining the titles recreates the activity exactly.
- Recognized labeled HTTP(S) links use their label as source text. Recognized unlabeled HTTP(S) links use the URL itself as source text.
- Routing, queues, stacking, reactions, interruption, MCP framing, cron behavior, and recovery remain unchanged.

## Live Acceptance

After the Variant A commit is deployed to the testing environment:

1. Start a turn that produces at least two thinking updates.
2. Unfold the thinking card after the first update.
3. Wait for the next update.
4. Confirm whether the card remains unfolded.
5. Let the turn complete and provide the Slack thread URL for raw inspection.

Variant A passes only if there is one thinking message containing one overall status task and ordered activity entries, one unchanged separate answer message, preserved native UI state, literal and complete activities, no duplicate cumulative content, no truncation or API error, and final `Complete` state.

## Failure Path

If Variant A fails any criterion, do not release it. Variant B may replace only the thinking stream chunks with full `task_card` Block chunks. If both fail, restore Commit 0 behavior before release.
