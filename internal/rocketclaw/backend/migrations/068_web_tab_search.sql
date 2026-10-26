-- +migrate Up
-- A Search tab's page, its name and searches, as the Web's SearchPage message in protobuf JSON; NULL for
-- other tabs. A person's tab holds their own page; a Shared tab holds the one page everyone in its space sees
-- and edits.
ALTER TABLE web_tabs ADD COLUMN search JSONB;
ALTER TABLE web_shared_tabs ADD COLUMN search JSONB;

-- +migrate Down
ALTER TABLE web_shared_tabs DROP COLUMN search;
ALTER TABLE web_tabs DROP COLUMN search;
