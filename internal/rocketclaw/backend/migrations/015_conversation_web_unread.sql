-- +migrate Up
ALTER TABLE managed_conversations ADD COLUMN web_unread BOOLEAN NOT NULL DEFAULT FALSE;

-- +migrate Down
ALTER TABLE managed_conversations DROP COLUMN web_unread;
