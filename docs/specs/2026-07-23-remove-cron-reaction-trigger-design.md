# Remove Cron Reaction Trigger

## Goal

Remove the Slack emoji-reaction entry point for one-off cronjobs while preserving text-command one-off cron requests and allowing them from any configured Slack channel.

## Behavior

Adding the Slack `repeat_one` reaction to a message no longer triggers any RocketClaw behavior. RocketClaw continues to accept message commands such as `:repeat_one: daily` and `🔂 daily` through the existing message-event path.

A text-command one-off request may start any top-level cronjob from any configured Slack channel, regardless of the cron definition's `channel`. Scheduled cron runs continue delivering to their configured `channel`.

Stop reactions remain unchanged:

- `octagonal_sign`
- `stop_button`

One-off cron loading, user authorization, execution, progress, output, and managed-thread registration remain unchanged.

## Implementation

Remove the reaction constant, reaction dispatch branch, reaction handler, reaction-specific tests, and reaction documentation. Remove the one-off request's cron-definition channel check and its rejection path. Do not add a feature flag, compatibility handler, rejection response, or replacement trigger.

## Testing

Keep the existing text-command tests. Add coverage proving a command can start a cronjob whose configured channel differs from the requesting Slack channel. Update reaction dispatch tests so `repeat_one` is unsupported and causes no API calls or cron execution. Retain stop-reaction coverage.
