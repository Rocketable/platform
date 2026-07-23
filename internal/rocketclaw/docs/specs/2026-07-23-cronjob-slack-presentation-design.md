# Cronjob Slack Presentation

## Goal

Change only how scheduled cronjob results appear in Slack. Keep cron execution, result selection, thread registration, replies, attachments, and reaction-driven behavior unchanged.

## Presentation

The first Slack message is the managed thread root and uses Slack Blocks:

- Header: `🔁 filename.md | agentname | RFC3339 timestamp`
- Divider
- Body: the cronjob's human-visible output

`filename.md` is the basename of the cron definition. The header remains a native Slack HeaderBlock; headers exceeding Slack's 150-character limit are truncated to exactly 150 characters and end in `...`. The top-level Slack text remains the existing machine-readable metadata sentence: ``Cronjob `relative/path.md` ran at `RFC3339 timestamp` with agent `agentname`.`` The human-visible body appears only in Blocks, not in top-level text.

If the output exceeds one Slack message, the root contains the first body content and remaining content continues in replies to that root. Attachments remain in the same thread. An attachment-only result produces the framed root followed by its threaded attachments.

## Behavior

The root timestamp remains the managed conversation's thread timestamp, so users can open the thread and continue talking exactly as they do today. Reaction and one-off cronjob production code remain untouched because the existing top-level metadata text remains unchanged. No cronjob execution, target parsing, or internal result representation changes.

## Testing

Update the focused Slack connector test to verify:

- one root contains the exact header and first body content;
- oversized native headers truncate to 150 characters and end in `...`;
- the root is not a thread reply;
- the root retains the first 48 body chunks, and remaining overflow and attachments target the root timestamp;
- cron thread registration still uses the root timestamp and configured agent;
- the top-level text remains the exact existing metadata sentence and excludes the body.
