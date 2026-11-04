-- +migrate Up
-- marked_unread is whether the person marked the session unread themselves, which raises no notification; a
-- turn's mark sets it back.
ALTER TABLE web_tabs ADD COLUMN marked_unread BOOLEAN NOT NULL DEFAULT FALSE;

-- +migrate Down
ALTER TABLE web_tabs DROP COLUMN marked_unread;
