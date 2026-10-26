-- +migrate Up
-- Slack now only answers mentions. The Slack threads of External MCP sessions and cron reports
-- become Web chats: they keep their IDs, history, bindings, and creator, and nothing posts to
-- Slack for them again. Mention threads stay Slack threads.
UPDATE conversations SET kind = 'web_chat', slack_channel = NULL, slack_thread = NULL
WHERE kind = 'slack_thread' AND (external_id IS NOT NULL OR created_by = 'cron');
ALTER TABLE conversations DROP COLUMN external_channel;

-- +migrate Down
ALTER TABLE conversations ADD COLUMN external_channel text;
