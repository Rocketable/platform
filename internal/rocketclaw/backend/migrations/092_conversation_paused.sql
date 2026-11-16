-- +migrate Up
ALTER TABLE conversations ADD COLUMN paused BOOLEAN NOT NULL DEFAULT FALSE;

-- +migrate Down
ALTER TABLE conversations DROP COLUMN paused;
