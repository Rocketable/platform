-- +migrate Up
ALTER TABLE managed_conversations ADD COLUMN IF NOT EXISTS settled BOOLEAN NOT NULL DEFAULT FALSE;

-- +migrate Down
ALTER TABLE managed_conversations DROP COLUMN IF EXISTS settled;
