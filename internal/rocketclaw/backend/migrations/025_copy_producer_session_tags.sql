-- +migrate Up
INSERT INTO session_tags (conversation_id, tags)
SELECT s.managed_conversation_id, t.tags
FROM external_mcp_sessions s
JOIN session_tags t ON t.conversation_id = s.private_conversation_id
WHERE t.tags <> '[]'::jsonb
ON CONFLICT (conversation_id) DO UPDATE SET tags = excluded.tags
WHERE session_tags.tags = '[]'::jsonb;

-- +migrate Down
