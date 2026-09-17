-- +migrate Up
ALTER TABLE managed_conversations
    ADD COLUMN pinned BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN name TEXT NOT NULL DEFAULT '';

-- +migrate Down
ALTER TABLE managed_conversations DROP COLUMN pinned, DROP COLUMN name;
