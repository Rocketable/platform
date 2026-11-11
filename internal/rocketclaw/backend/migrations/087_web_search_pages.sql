-- +migrate Up
-- A Search tab's page as rows: its name, the style id of the Dashboard it is, and the id of the search it shows, with
-- its searches in order, keyed by position because a page's search ids may repeat. Every tab, Shared tab and closed tab
-- with a page points at its own copy, which is deleted with the row or when the row points elsewhere.
CREATE TABLE web_search_pages (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    board TEXT NOT NULL,
    active TEXT NOT NULL
);
CREATE TABLE web_searches (
    page UUID NOT NULL REFERENCES web_search_pages (id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    id TEXT NOT NULL,
    name TEXT NOT NULL,
    query TEXT NOT NULL,
    notify BOOLEAN NOT NULL,
    PRIMARY KEY (page, position)
);
ALTER TABLE web_tabs ADD COLUMN search_page UUID UNIQUE REFERENCES web_search_pages (id);
ALTER TABLE web_shared_tabs ADD COLUMN search_page UUID UNIQUE REFERENCES web_search_pages (id);
ALTER TABLE web_closed_tabs ADD COLUMN search_page UUID UNIQUE REFERENCES web_search_pages (id);

-- The pages were the Web's SearchPage in protobuf JSON, which leaves out empty fields; one an older Web saved may lack
-- any field, and its active search may name none of its searches.
WITH tabs AS (
    UPDATE web_tabs SET search_page = gen_random_uuid() WHERE search IS NOT NULL RETURNING search_page, search
), shared AS (
    UPDATE web_shared_tabs SET search_page = gen_random_uuid() WHERE search IS NOT NULL RETURNING search_page, search
), closed AS (
    UPDATE web_closed_tabs SET search_page = gen_random_uuid() WHERE search IS NOT NULL RETURNING search_page, search
), written AS (
    SELECT search_page, search FROM tabs UNION ALL SELECT search_page, search FROM shared UNION ALL SELECT search_page, search FROM closed
), pages AS (
    INSERT INTO web_search_pages (id, name, board, active)
    SELECT search_page, COALESCE(search->>'name', ''), COALESCE(search->>'board', ''), COALESCE(search->>'active', '') FROM written
)
INSERT INTO web_searches (page, position, id, name, query, notify)
SELECT w.search_page, s.n, COALESCE(s.search->>'id', ''), COALESCE(s.search->>'name', ''), COALESCE(s.search->>'query', ''), COALESCE((s.search->>'notify')::boolean, FALSE)
FROM written w CROSS JOIN jsonb_array_elements(w.search->'searches') WITH ORDINALITY AS s(search, n);

ALTER TABLE web_tabs DROP COLUMN search;
ALTER TABLE web_shared_tabs DROP COLUMN search;
ALTER TABLE web_closed_tabs DROP COLUMN search;

-- Deletes the page a row held. Any table whose rows hold pages in a search_page column attaches it.
-- +migrate StatementBegin
CREATE FUNCTION delete_search_page() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path FROM CURRENT
    AS $$
BEGIN
    DELETE FROM web_search_pages WHERE id = OLD.search_page;
    RETURN NULL;
END;
$$;
-- +migrate StatementEnd
CREATE TRIGGER web_tabs_search_page_delete AFTER DELETE ON web_tabs FOR EACH ROW WHEN (OLD.search_page IS NOT NULL) EXECUTE FUNCTION delete_search_page();
CREATE TRIGGER web_tabs_search_page_change AFTER UPDATE OF search_page ON web_tabs FOR EACH ROW WHEN (OLD.search_page IS NOT NULL AND OLD.search_page IS DISTINCT FROM NEW.search_page) EXECUTE FUNCTION delete_search_page();
CREATE TRIGGER web_shared_tabs_search_page_delete AFTER DELETE ON web_shared_tabs FOR EACH ROW WHEN (OLD.search_page IS NOT NULL) EXECUTE FUNCTION delete_search_page();
CREATE TRIGGER web_shared_tabs_search_page_change AFTER UPDATE OF search_page ON web_shared_tabs FOR EACH ROW WHEN (OLD.search_page IS NOT NULL AND OLD.search_page IS DISTINCT FROM NEW.search_page) EXECUTE FUNCTION delete_search_page();
CREATE TRIGGER web_closed_tabs_search_page_delete AFTER DELETE ON web_closed_tabs FOR EACH ROW WHEN (OLD.search_page IS NOT NULL) EXECUTE FUNCTION delete_search_page();
CREATE TRIGGER web_closed_tabs_search_page_change AFTER UPDATE OF search_page ON web_closed_tabs FOR EACH ROW WHEN (OLD.search_page IS NOT NULL AND OLD.search_page IS DISTINCT FROM NEW.search_page) EXECUTE FUNCTION delete_search_page();

-- +migrate Down
DROP TRIGGER web_closed_tabs_search_page_change ON web_closed_tabs;
DROP TRIGGER web_closed_tabs_search_page_delete ON web_closed_tabs;
DROP TRIGGER web_shared_tabs_search_page_change ON web_shared_tabs;
DROP TRIGGER web_shared_tabs_search_page_delete ON web_shared_tabs;
DROP TRIGGER web_tabs_search_page_change ON web_tabs;
DROP TRIGGER web_tabs_search_page_delete ON web_tabs;
DROP FUNCTION delete_search_page();
ALTER TABLE web_closed_tabs DROP COLUMN search_page, ADD COLUMN search JSONB;
ALTER TABLE web_shared_tabs DROP COLUMN search_page, ADD COLUMN search JSONB;
ALTER TABLE web_tabs DROP COLUMN search_page, ADD COLUMN search JSONB;
DROP TABLE web_searches;
DROP TABLE web_search_pages;
