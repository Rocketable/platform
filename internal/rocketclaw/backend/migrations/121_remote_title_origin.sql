-- +migrate Up
ALTER TABLE conversations ADD COLUMN typed_titles text[] NOT NULL DEFAULT '{}';
UPDATE conversations SET generated_name = name WHERE kind = 'remote' AND name IS NOT DISTINCT FROM title;

-- +migrate Down
UPDATE conversations SET generated_name = NULL WHERE kind = 'remote';
ALTER TABLE conversations DROP COLUMN typed_titles;
