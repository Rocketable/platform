-- +migrate Up
ALTER TABLE thread_queue ADD COLUMN IF NOT EXISTS content JSONB NOT NULL DEFAULT '{}';
ALTER TABLE thread_queue ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT '';
ALTER TABLE thread_queue ADD COLUMN IF NOT EXISTS slack_reply JSONB NOT NULL DEFAULT 'null';

-- +migrate Down
ALTER TABLE thread_queue DROP COLUMN IF EXISTS slack_reply;
ALTER TABLE thread_queue DROP COLUMN IF EXISTS source;
ALTER TABLE thread_queue DROP COLUMN IF EXISTS content;
