-- +migrate Up
ALTER TABLE conversations ADD COLUMN generated_name TEXT;

-- +migrate Down
ALTER TABLE conversations DROP COLUMN generated_name;
