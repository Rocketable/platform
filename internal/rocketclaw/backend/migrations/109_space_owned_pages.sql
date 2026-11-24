-- +migrate Up
ALTER TABLE hosted_pages RENAME TO retired_hosted_pages;
ALTER TABLE retired_hosted_pages DROP CONSTRAINT hosted_pages_conversation_id_fkey;
CREATE TABLE hosted_pages_pending AS SELECT * FROM retired_hosted_pages;

CREATE TABLE hosted_pages (
    space text NOT NULL,
    kind text NOT NULL,
    name text NOT NULL,
    content_type text NOT NULL,
    body bytea NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE UNIQUE INDEX hosted_pages_space_key ON hosted_pages (lower(space), kind, name);

-- +migrate Down
DROP TABLE hosted_pages;
DROP TABLE hosted_pages_pending;
ALTER TABLE retired_hosted_pages RENAME TO hosted_pages;

WITH gone AS (
    SELECT DISTINCT p.conversation_id FROM hosted_pages p LEFT JOIN conversations c USING (conversation_id)
    WHERE p.conversation_id IS NOT NULL AND c.conversation_id IS NULL
)
DELETE FROM hosted_pages p USING gone g WHERE p.conversation_id = g.conversation_id;

ALTER TABLE hosted_pages ADD CONSTRAINT hosted_pages_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES conversations (conversation_id) ON DELETE CASCADE;
