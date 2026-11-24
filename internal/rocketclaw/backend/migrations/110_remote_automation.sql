-- +migrate Up
ALTER TABLE conversations ADD COLUMN automation BOOLEAN NOT NULL DEFAULT FALSE;

-- +migrate Down
ALTER TABLE conversations DROP COLUMN automation;
