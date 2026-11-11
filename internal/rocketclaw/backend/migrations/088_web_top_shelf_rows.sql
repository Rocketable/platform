-- +migrate Up
-- A person's Top Shelf as rows: each tab of any RocketClaw, server '' being this one, in order, with its space and
-- path there. A tab of a local session names it, so deleting the session takes the tab off the shelf; a Search tab
-- points at its own page. Whether the shelf is collapsed is kept with the person's tab state.
ALTER TABLE web_top_shelves RENAME TO web_top_shelf_blobs;
ALTER TABLE web_top_shelf_blobs RENAME CONSTRAINT web_top_shelves_pkey TO web_top_shelf_blobs_pkey;
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
ALTER TABLE web_tab_states ADD COLUMN shelf_collapsed BOOLEAN NOT NULL DEFAULT FALSE;

-- The session a local /s/ or /oc/ path names, read as Go's TabConversation reads it, legacy URI-encoded spellings
-- included; '' for any other path, and NULL for one whose text does not decode.
-- +migrate StatementBegin
CREATE FUNCTION shelf_conversation(path TEXT) RETURNS TEXT
    LANGUAGE plpgsql
    AS $$
DECLARE
    id TEXT := substr(path, strpos(substr(path, 2), '/') + 2);
    raw BYTEA := convert_to(id, 'UTF8');
BEGIN
    IF NOT (starts_with(path, '/s/') OR starts_with(path, '/oc/')) THEN
        RETURN '';
    END IF;

    IF id !~ '%(?![0-9A-Fa-f]{2})' THEN
        SELECT COALESCE(string_agg(CASE WHEN starts_with(t[1], '%') THEN decode(substr(t[1], 2), 'hex') ELSE convert_to(t[1], 'UTF8') END, ''::bytea ORDER BY n), '')
        INTO raw FROM regexp_matches(id, '%[0-9A-Fa-f]{2}|[^%]+', 'g') WITH ORDINALITY AS m(t, n);
    END IF;

    id := rtrim(translate(convert_from(raw, 'UTF8'), '-_', '+/'), '=');
    IF id ~ '^([A-Za-z0-9+/]{4})*([A-Za-z0-9+/]{2,3})?$' THEN
        RETURN convert_from(decode(rpad(id, (length(id) + 3) / 4 * 4, '='), 'base64'), 'UTF8');
    END IF;

    RETURN convert_from(raw, 'UTF8');
EXCEPTION WHEN OTHERS THEN
    RETURN NULL;
END;
$$;
-- +migrate StatementEnd

-- Each shelf was the Web's TopShelf in protobuf JSON, which leaves out empty fields. A local session's tab is kept under
-- the path the Web spells it with, one of a session that no longer exists is left out, and of tabs that repeat a server
-- and path the first is kept.
WITH entries AS (
    SELECT b.username, e.n, COALESCE(e.tab->>'server', '') AS server, COALESCE(e.tab->>'space', '') AS space, COALESCE(e.tab->>'path', '') AS path, e.tab->'search' AS search
    FROM web_top_shelf_blobs b CROSS JOIN jsonb_array_elements(COALESCE(b.shelf->'tabs', '[]')) WITH ORDINALITY AS e(tab, n)
), located AS (
    SELECT e.*, CASE WHEN e.server = '' THEN shelf_conversation(e.path) ELSE '' END AS conversation_id FROM entries e
), live AS (
    SELECT l.username, l.n, l.server, l.space, COALESCE(CASE WHEN c.kind = 'remote' THEN '/oc/' ELSE '/s/' END || rtrim(translate(replace(encode(convert_to(c.conversation_id, 'UTF8'), 'base64'), E'\n', ''), '+/', '-_'), '='), l.path) AS path, c.conversation_id, l.search
    FROM located l LEFT JOIN conversations c ON c.conversation_id = l.conversation_id
    WHERE l.conversation_id = '' OR c.conversation_id IS NOT NULL
), kept AS (
    SELECT DISTINCT ON (username, server, path) *, CASE WHEN search IS NOT NULL THEN gen_random_uuid() END AS search_page FROM live ORDER BY username, server, path, n
), shelves AS (
    INSERT INTO web_top_shelves (username, position, server, space, path, conversation_id, search_page)
    SELECT username, n - 1, server, space, path, conversation_id, search_page FROM kept
), written AS (
    SELECT search_page, search FROM kept WHERE search_page IS NOT NULL
), pages AS (
    INSERT INTO web_search_pages (id, name, board, active)
    SELECT search_page, COALESCE(search->>'name', ''), COALESCE(search->>'board', ''), COALESCE(search->>'active', '') FROM written
), searches AS (
    INSERT INTO web_searches (page, position, id, name, query, notify)
    SELECT w.search_page, s.n, COALESCE(s.search->>'id', ''), COALESCE(s.search->>'name', ''), COALESCE(s.search->>'query', ''), COALESCE((s.search->>'notify')::boolean, FALSE)
    FROM written w CROSS JOIN jsonb_array_elements(w.search->'searches') WITH ORDINALITY AS s(search, n)
)
INSERT INTO web_tab_states (username, revision, shelf_collapsed)
SELECT username, 0, COALESCE((shelf->>'collapsed')::boolean, FALSE) FROM web_top_shelf_blobs
ON CONFLICT (username) DO UPDATE SET shelf_collapsed = excluded.shelf_collapsed;

DROP FUNCTION shelf_conversation(TEXT);
DROP TABLE web_top_shelf_blobs;

-- +migrate Down
DROP TABLE web_top_shelves;
ALTER TABLE web_tab_states DROP COLUMN shelf_collapsed;
CREATE TABLE web_top_shelves (
    username TEXT PRIMARY KEY,
    shelf JSONB NOT NULL
);
