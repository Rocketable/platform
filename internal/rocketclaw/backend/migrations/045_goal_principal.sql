-- +migrate Up
-- The person who set the goal, so its continuation turns keep that person's ROCKETCLAW_PRINCIPAL_*
-- variables. Empty for goals set before this migration.
ALTER TABLE conversation_goals ADD COLUMN principal_name TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_goals ADD COLUMN principal_email TEXT NOT NULL DEFAULT '';

-- +migrate Down
ALTER TABLE conversation_goals DROP COLUMN principal_email;
ALTER TABLE conversation_goals DROP COLUMN principal_name;
