-- +migrate Up
ALTER TABLE managed_conversations DROP COLUMN web_unread;
ALTER TABLE managed_conversations ADD COLUMN snoozed_until timestamptz;

-- +migrate Down
ALTER TABLE managed_conversations DROP COLUMN snoozed_until;
ALTER TABLE managed_conversations ADD COLUMN web_unread boolean NOT NULL DEFAULT false;
CREATE INDEX managed_conversations_web_unread ON managed_conversations (web_unread);
