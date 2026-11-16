-- +migrate Up
CREATE TABLE web_shared_tabs (
    space TEXT NOT NULL REFERENCES web_spaces (id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    conversation_id TEXT REFERENCES conversations (conversation_id) ON DELETE CASCADE,
    added_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (space, path)
);
CREATE INDEX web_shared_tabs_conversation ON web_shared_tabs (conversation_id);

ALTER TABLE web_tab_states ADD COLUMN last JSONB NOT NULL DEFAULT '{}';
WITH lasts AS (
    SELECT username, jsonb_object_agg(space, path) AS last FROM web_tabs WHERE last GROUP BY username
)
UPDATE web_tab_states s SET last = lasts.last FROM lasts WHERE s.username = lasts.username;
DELETE FROM web_tabs WHERE position IS NULL AND unread_at IS NULL;
ALTER TABLE web_tabs DROP COLUMN last;

-- +migrate Down
ALTER TABLE web_tabs ADD COLUMN last BOOLEAN NOT NULL DEFAULT FALSE;
WITH lasts AS (
    SELECT s.username, l.key AS space, l.value AS path FROM web_tab_states s CROSS JOIN LATERAL jsonb_each_text(s.last) AS l
)
UPDATE web_tabs t SET last = TRUE FROM lasts WHERE t.username = lasts.username AND t.path = lasts.path AND t.space = lasts.space;
ALTER TABLE web_tab_states DROP COLUMN last;
DROP TABLE web_shared_tabs;
