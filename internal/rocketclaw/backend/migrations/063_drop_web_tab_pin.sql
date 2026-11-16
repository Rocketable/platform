-- +migrate Up
ALTER TABLE web_tabs DROP COLUMN pinned;

-- +migrate Down
ALTER TABLE web_tabs ADD COLUMN pinned BOOLEAN NOT NULL DEFAULT FALSE;
