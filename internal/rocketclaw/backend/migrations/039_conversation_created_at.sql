-- +migrate Up
-- Existing conversations count as old; new ones record when they were created.
ALTER TABLE managed_conversations ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT '0001-01-01 00:00:00+00';
ALTER TABLE managed_conversations ALTER COLUMN created_at SET DEFAULT now();

-- +migrate Down
ALTER TABLE managed_conversations DROP COLUMN created_at;
