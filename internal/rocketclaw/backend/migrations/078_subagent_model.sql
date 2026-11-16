-- +migrate Up
ALTER TABLE conversations ADD COLUMN subagent_model text;
ALTER TABLE conversations ADD COLUMN subagent_tools jsonb;

-- +migrate Down
ALTER TABLE conversations DROP COLUMN subagent_tools;
ALTER TABLE conversations DROP COLUMN subagent_model;
