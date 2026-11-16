-- +migrate Up
ALTER TABLE web_tabs ADD COLUMN marked_unread BOOLEAN NOT NULL DEFAULT FALSE;

-- +migrate Down
ALTER TABLE web_tabs DROP COLUMN marked_unread;
