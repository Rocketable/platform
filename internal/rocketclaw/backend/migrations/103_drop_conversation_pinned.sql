-- +migrate Up
ALTER TABLE conversations DROP COLUMN pinned;

-- +migrate Down
ALTER TABLE conversations ADD COLUMN pinned boolean DEFAULT false CONSTRAINT managed_conversations_pinned_not_null NOT NULL;
