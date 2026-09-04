-- +migrate Up
ALTER TABLE managed_conversations ADD COLUMN settled BOOLEAN NOT NULL DEFAULT FALSE;

-- +migrate Down
ALTER TABLE managed_conversations DROP COLUMN settled;
