-- +migrate Up
CREATE INDEX session_entries_mcp_metadata ON session_entries (conversation_id, id DESC)
WHERE entry_json::jsonb->>'type' = 'mcp_external_metadata';

-- +migrate Down
DROP INDEX session_entries_mcp_metadata;
