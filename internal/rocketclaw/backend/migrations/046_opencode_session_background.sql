-- +migrate Up
-- A remote OpenCode root session's running child session or shell command, which can wake it
-- after its turn ends.
ALTER TABLE opencode_sessions ADD COLUMN background BOOLEAN NOT NULL DEFAULT FALSE;

-- +migrate Down
ALTER TABLE opencode_sessions DROP COLUMN background;
