-- +migrate Up
-- The OpenCode session that owns a page it stored through its coding server's MCP endpoint, NULL for
-- an agent's page. Deleting the session deletes its pages.
ALTER TABLE hosted_pages ADD COLUMN conversation_id TEXT REFERENCES conversations (conversation_id) ON DELETE CASCADE;
CREATE INDEX hosted_pages_conversation ON hosted_pages (conversation_id) WHERE conversation_id IS NOT NULL;

-- +migrate Down
ALTER TABLE hosted_pages DROP COLUMN conversation_id;
