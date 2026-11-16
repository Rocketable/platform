-- +migrate Up
ALTER TABLE web_tabs ADD COLUMN search JSONB;
ALTER TABLE web_shared_tabs ADD COLUMN search JSONB;

-- +migrate Down
ALTER TABLE web_shared_tabs DROP COLUMN search;
ALTER TABLE web_tabs DROP COLUMN search;
