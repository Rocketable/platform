-- +migrate Up
ALTER TABLE managed_conversations ADD COLUMN forked_from TEXT NOT NULL DEFAULT '';

-- +migrate Down
ALTER TABLE managed_conversations DROP COLUMN forked_from;
