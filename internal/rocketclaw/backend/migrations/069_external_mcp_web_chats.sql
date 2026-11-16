-- +migrate Up
UPDATE conversations SET kind = 'web_chat', slack_channel = NULL, slack_thread = NULL
WHERE kind = 'slack_thread' AND (external_id IS NOT NULL OR created_by = 'cron');
ALTER TABLE conversations DROP COLUMN external_channel;

-- +migrate Down
ALTER TABLE conversations ADD COLUMN external_channel text;
