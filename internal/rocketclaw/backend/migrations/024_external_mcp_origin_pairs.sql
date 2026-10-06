-- +migrate Up
ALTER TABLE external_mcp_sessions ADD COLUMN origin_pairs JSONB NOT NULL DEFAULT 'null';
UPDATE external_mcp_sessions s SET origin_pairs = COALESCE(e.pairs, 'null'::jsonb)
FROM (
    SELECT DISTINCT ON (conversation_id) conversation_id,
        entry_json::jsonb->'output_trace'->0->'pairs' AS pairs
    FROM session_entries
    WHERE entry_json::jsonb->>'type' = 'mcp_external_origin_pairs'
    ORDER BY conversation_id, id
) e WHERE e.conversation_id = s.private_conversation_id;
DELETE FROM session_entries
WHERE entry_json::jsonb->>'type' = 'mcp_external_origin_pairs'
  AND conversation_id IN (
    SELECT private_conversation_id FROM external_mcp_sessions
    UNION SELECT managed_conversation_id FROM external_mcp_sessions
  );

-- +migrate Down
INSERT INTO session_entries (conversation_id, entry_json, entry_timestamp)
SELECT conversation_id, jsonb_build_object('version', 1, 'type', 'mcp_external_origin_pairs',
    'output_trace', jsonb_build_array(jsonb_build_object('pairs', origin_pairs))), ''
FROM external_mcp_sessions s
CROSS JOIN LATERAL (VALUES (s.private_conversation_id), (s.managed_conversation_id)) c(conversation_id)
WHERE conversation_id IS NOT NULL AND origin_pairs <> 'null'::jsonb;
ALTER TABLE external_mcp_sessions DROP COLUMN origin_pairs;
