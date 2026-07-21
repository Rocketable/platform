# Slack Thinking Streams Design

## Goal

Use Slack's native streaming API for human-originated thinking messages while preserving the separate thinking and answer message positions. Render links in MCP requests and task-card thinking details.

## Message Flow

For a human Slack turn with an identified Slack user and workspace:

1. `chat.startStream` creates the thinking message in the managed thread.
2. `chat.postMessage` creates the zero-width answer placeholder immediately after it.
3. The existing pending-placeholder key binds both timestamps to the later RocketCode turn ID.
4. `chat.appendStream` appends only the new suffix of cumulative thinking text as Slack Markdown, split into rune-safe requests of at most 12,000 characters. Successfully appended chunks advance the cumulative stream state independently so a retry resumes after the last successful chunk.
5. The final answer updates the answer placeholder exactly as it does now.
6. `chat.stopStream` finalizes the thinking message.
7. Connector state is cleared and buffered messages are promoted.

The answer is not streamed. Partial answer snapshots remain ignored. This preserves Slack ordering even when thinking and answer events arrive internally out of order.

Turns without an identified recipient, including External MCP, cron, automated continuations, and recovery, retain the existing task-card path. This avoids inventing a recipient identity where Slack requires `recipient_user_id` and `recipient_team_id` for channel streams.

## Recipient Data

The connector retains the workspace ID returned by `auth.test`. Human message and app-mention handlers pass the originating Slack user ID when reserving placeholders. Buffered human messages retain the same principal user ID. No recipient data is persisted and no configuration is added.

## Stream State

The existing `slackReplySlots` records whether its thinking timestamp is a stream and the cumulative thinking text already appended. Human stream progress is sent synchronously through the existing serialized outbound worker. The existing timer and task-card state remain only for recipient-less turns.

If answer-placeholder creation fails after `chat.startStream`, RocketClaw stops and deletes the partial thinking stream. Abort and pending-placeholder cleanup also stop a stream before deleting it. Successful completion stops the stream after final-answer delivery.

## MCP Rendering

MCP request bodies use Slack Markdown instead of plain text, allowing links such as `<https://example.com|label>` to render. Request body Blocks use Slack's `verbatim` setting so raw broadcast and user-group names are not automatically parsed. Every explicit direct-mention opener `<@`, plus explicit user-group, `@here`, `@channel`, and `@everyone` controls, is encoded to display literally in request Blocks, fallback text, and continuation messages without changing the complete request delivered to the MCP agent. MCP final responses already use Slack Markdown and retain their existing automatic parsing setting.

Recipient-less thinking remains a task card. Within each task-card activity, recognized Slack HTTP or HTTPS link markup is represented by native rich-text link elements. Other activity text remains literal; this change does not introduce a general Markdown parser.

## Testing

Targeted connector tests cover:

- `chat.startStream` before the answer placeholder for human turns.
- Required recipient user, recipient team, thread timestamp, and initial thinking marker.
- Appending only new cumulative thinking text.
- Rune-safe 12,000-character append chunks and retry after a partial multi-chunk failure.
- Final answer update before `chat.stopStream`.
- Empty answers and stream cleanup failures.
- Recipient-less turns retaining task cards.
- MCP request bodies using `mrkdwn`.
- MCP request U- and W-prefixed direct mentions and other explicit mention controls remaining literal in Blocks, fallback text, and continuations while links and ordinary Markdown remain intact.
- MCP request body Blocks using `verbatim`, while MCP final-response Blocks retain their existing setting.
- Task-card Slack links becoming native rich-text link elements while surrounding text remains literal.

The final verification remains `gofmt`, `go test ./...`, `make lint`, and `make test`.

## Non-Goals

- Stream final answers.
- Persist or configure stream recipients.
- Stream External MCP, cron, automatic, or recovery turns without a natural Slack recipient.
- Parse general Markdown into task-card rich text.
- Change queueing, buffering, routing, attachment, reaction, or restart behavior.
