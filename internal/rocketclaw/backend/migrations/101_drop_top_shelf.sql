-- +migrate Up
DELETE FROM web_top_shelves;
DROP TABLE web_top_shelves;
ALTER TABLE web_tab_states DROP COLUMN shelf_collapsed;

-- +migrate Down
ALTER TABLE web_tab_states ADD COLUMN shelf_collapsed BOOLEAN NOT NULL DEFAULT FALSE;
CREATE TABLE web_top_shelves (
    username TEXT NOT NULL,
    position INTEGER NOT NULL,
    server TEXT NOT NULL,
    space TEXT NOT NULL,
    path TEXT NOT NULL,
    conversation_id TEXT REFERENCES conversations (conversation_id) ON DELETE CASCADE,
    search_page UUID REFERENCES web_search_pages (id),
    CONSTRAINT web_top_shelves_pkey PRIMARY KEY (username, server, path),
    CONSTRAINT web_top_shelves_search_page_key UNIQUE (search_page)
);
CREATE INDEX web_top_shelves_conversation ON web_top_shelves (conversation_id);
CREATE TRIGGER web_top_shelves_search_page_delete AFTER DELETE ON web_top_shelves FOR EACH ROW WHEN (OLD.search_page IS NOT NULL) EXECUTE FUNCTION delete_search_page();
CREATE TRIGGER web_top_shelves_search_page_change AFTER UPDATE OF search_page ON web_top_shelves FOR EACH ROW WHEN (OLD.search_page IS NOT NULL AND OLD.search_page IS DISTINCT FROM NEW.search_page) EXECUTE FUNCTION delete_search_page();
