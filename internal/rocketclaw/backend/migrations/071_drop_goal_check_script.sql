-- +migrate Up
-- Goals no longer carry a check script; an active goal that had one continues without it.
ALTER TABLE conversations DROP COLUMN goal_check_script;

-- +migrate Down
ALTER TABLE conversations ADD COLUMN goal_check_script text;
