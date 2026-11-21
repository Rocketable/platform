-- +migrate Up
ALTER TABLE turns ADD COLUMN result_id text;

-- +migrate Down
ALTER TABLE turns DROP COLUMN result_id;
