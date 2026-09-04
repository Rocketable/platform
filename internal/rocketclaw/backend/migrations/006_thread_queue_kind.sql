-- +migrate Up
ALTER TABLE thread_queue ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'enqueue';

-- +migrate Down
ALTER TABLE thread_queue DROP COLUMN IF EXISTS kind;
