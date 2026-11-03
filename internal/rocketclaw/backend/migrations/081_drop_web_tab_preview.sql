-- +migrate Up
ALTER TABLE web_tabs DROP COLUMN preview;

-- +migrate Down
ALTER TABLE web_tabs ADD COLUMN preview BOOLEAN NOT NULL DEFAULT FALSE;
