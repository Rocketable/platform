-- +migrate Up
ALTER TABLE thread_queue ADD COLUMN park_after TEXT NOT NULL DEFAULT '';

-- +migrate Down
ALTER TABLE thread_queue DROP COLUMN park_after;
