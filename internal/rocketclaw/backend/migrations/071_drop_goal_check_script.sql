-- +migrate Up
ALTER TABLE conversations DROP COLUMN goal_check_script;

-- +migrate Down
ALTER TABLE conversations ADD COLUMN goal_check_script text;
