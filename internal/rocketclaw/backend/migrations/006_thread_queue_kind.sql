-- +migrate Up
ALTER TABLE thread_queue ADD COLUMN kind TEXT NOT NULL DEFAULT 'enqueue';

-- +migrate Down
ALTER TABLE thread_queue DROP COLUMN kind;
