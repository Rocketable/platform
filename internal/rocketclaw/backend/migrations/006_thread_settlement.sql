-- +migrate Up
ALTER TABLE managed_conversations ADD COLUMN settled_override TEXT NOT NULL DEFAULT '';
ALTER TABLE managed_conversations ADD COLUMN bumped_at_unix_ns BIGINT NOT NULL DEFAULT 0;

-- +migrate Down
ALTER TABLE managed_conversations DROP COLUMN bumped_at_unix_ns;
ALTER TABLE managed_conversations DROP COLUMN settled_override;
