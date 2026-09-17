-- +migrate Up
ALTER TABLE managed_conversations ADD COLUMN reopened_at TIMESTAMPTZ NOT NULL DEFAULT '0001-01-01 00:00:00+00';

-- +migrate Down
ALTER TABLE managed_conversations DROP COLUMN reopened_at;
