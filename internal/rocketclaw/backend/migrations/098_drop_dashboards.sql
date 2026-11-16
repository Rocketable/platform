-- +migrate Up
ALTER TABLE web_search_pages DROP COLUMN board;
ALTER TABLE web_searches DROP COLUMN notify;

-- +migrate Down
ALTER TABLE web_searches ADD COLUMN notify boolean NOT NULL DEFAULT FALSE;
ALTER TABLE web_search_pages ADD COLUMN board text NOT NULL DEFAULT '';
