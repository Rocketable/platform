-- +migrate Up
ALTER TABLE thread_queue ADD COLUMN content JSONB NOT NULL DEFAULT '{}';
ALTER TABLE thread_queue ADD COLUMN source TEXT NOT NULL DEFAULT '';
ALTER TABLE thread_queue ADD COLUMN slack_reply JSONB NOT NULL DEFAULT 'null';

-- +migrate Down
ALTER TABLE thread_queue DROP COLUMN slack_reply;
ALTER TABLE thread_queue DROP COLUMN source;
ALTER TABLE thread_queue DROP COLUMN content;
