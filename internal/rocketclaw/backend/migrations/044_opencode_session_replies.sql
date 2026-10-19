-- +migrate Up
-- The text of a remote OpenCode session's newest assistant message, read when a sync sees
-- its turn end. NULL until then.
ALTER TABLE opencode_sessions ADD COLUMN reply TEXT;

-- +migrate Down
ALTER TABLE opencode_sessions DROP COLUMN reply;
