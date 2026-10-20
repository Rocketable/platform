-- +migrate Up
-- A remote OpenCode session's selected agent and model ("<provider>/<model>"), empty until it has one.
ALTER TABLE opencode_sessions ADD COLUMN agent TEXT NOT NULL DEFAULT '', ADD COLUMN model TEXT NOT NULL DEFAULT '';

-- +migrate Down
ALTER TABLE opencode_sessions DROP COLUMN model, DROP COLUMN agent;
