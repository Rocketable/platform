-- +migrate Up
-- The model, as the easy agent's subagents entry, and the tool list (NULL: all of the agent's tools) an easy
-- agent's subagent session runs with; NULL model for other sessions.
ALTER TABLE conversations ADD COLUMN subagent_model text;
ALTER TABLE conversations ADD COLUMN subagent_tools jsonb;

-- +migrate Down
ALTER TABLE conversations DROP COLUMN subagent_tools;
ALTER TABLE conversations DROP COLUMN subagent_model;
