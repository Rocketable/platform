-- +migrate Up
-- Web tabs are no longer pinned; a pinned tab stays open where it was, as an ordinary tab.
ALTER TABLE web_tabs DROP COLUMN pinned;

-- +migrate Down
ALTER TABLE web_tabs ADD COLUMN pinned BOOLEAN NOT NULL DEFAULT FALSE;
