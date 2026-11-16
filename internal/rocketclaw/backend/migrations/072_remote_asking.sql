-- +migrate Up
ALTER TABLE conversations ADD COLUMN asking BOOLEAN NOT NULL DEFAULT FALSE;

-- +migrate Down
ALTER TABLE conversations DROP COLUMN asking;
