-- +migrate Up
-- An External MCP caller's metadata moves from an inbound's Metadata, which RocketClaw reads for routing,
-- to ExternalMCPMetadata, which only becomes ROCKETCLAW_METADATA_* variables. Every stored External MCP
-- inbound keeps in Metadata only the keys RocketClaw set or never turned into variables (origin, media,
-- principal and its name and email, source) and moves every other key, so its variables stay the same.
-- rocketclaw_raw_text is removed, not moved: such a row's Metadata was the caller's map, so its raw text
-- was planted by the caller or written by a Web queue edit, and it would otherwise drive the Web queue's
-- edit text. Its variable is the one that goes away. Rows with nothing to move or remove stay as they are.
WITH external AS (
    SELECT id, inbound_json::jsonb->'Metadata' AS metadata FROM turns
    WHERE inbound_json->>'Source' = 'external_mcp' AND json_typeof(inbound_json->'Metadata') = 'object'
), split AS (
    SELECT id, metadata, jsonb_strip_nulls(jsonb_build_object('rocketclaw_origin', metadata->'rocketclaw_origin', 'rocketclaw_media', metadata->'rocketclaw_media', 'rocketclaw_principal', metadata->'rocketclaw_principal', 'rocketclaw_principal_name', metadata->'rocketclaw_principal_name', 'rocketclaw_principal_email', metadata->'rocketclaw_principal_email', 'source', metadata->'source')) AS kept
    FROM external
)
UPDATE turns t SET inbound_json = (t.inbound_json::jsonb || jsonb_build_object('Metadata', s.kept, 'ExternalMCPMetadata', s.metadata - '{rocketclaw_origin,rocketclaw_media,rocketclaw_principal,rocketclaw_principal_name,rocketclaw_principal_email,source,rocketclaw_raw_text}'::text[]))::json
FROM split s WHERE t.id = s.id AND s.kept <> s.metadata;

WITH external AS (
    SELECT id, inbound_json::jsonb->'Metadata' AS metadata FROM shadow_turns
    WHERE inbound_json->>'Source' = 'external_mcp' AND json_typeof(inbound_json->'Metadata') = 'object'
), split AS (
    SELECT id, metadata, jsonb_strip_nulls(jsonb_build_object('rocketclaw_origin', metadata->'rocketclaw_origin', 'rocketclaw_media', metadata->'rocketclaw_media', 'rocketclaw_principal', metadata->'rocketclaw_principal', 'rocketclaw_principal_name', metadata->'rocketclaw_principal_name', 'rocketclaw_principal_email', metadata->'rocketclaw_principal_email', 'source', metadata->'source')) AS kept
    FROM external
)
UPDATE shadow_turns t SET inbound_json = (t.inbound_json::jsonb || jsonb_build_object('Metadata', s.kept, 'ExternalMCPMetadata', s.metadata - '{rocketclaw_origin,rocketclaw_media,rocketclaw_principal,rocketclaw_principal_name,rocketclaw_principal_email,source,rocketclaw_raw_text}'::text[]))::json
FROM split s WHERE t.id = s.id AND s.kept <> s.metadata;

WITH external AS (
    SELECT conversation_id, producer_inbound_json::jsonb->'Metadata' AS metadata FROM conversations
    WHERE producer_inbound_json->>'Source' = 'external_mcp' AND json_typeof(producer_inbound_json->'Metadata') = 'object'
), split AS (
    SELECT conversation_id, metadata, jsonb_strip_nulls(jsonb_build_object('rocketclaw_origin', metadata->'rocketclaw_origin', 'rocketclaw_media', metadata->'rocketclaw_media', 'rocketclaw_principal', metadata->'rocketclaw_principal', 'rocketclaw_principal_name', metadata->'rocketclaw_principal_name', 'rocketclaw_principal_email', metadata->'rocketclaw_principal_email', 'source', metadata->'source')) AS kept
    FROM external
)
UPDATE conversations c SET producer_inbound_json = (c.producer_inbound_json::jsonb || jsonb_build_object('Metadata', s.kept, 'ExternalMCPMetadata', s.metadata - '{rocketclaw_origin,rocketclaw_media,rocketclaw_principal,rocketclaw_principal_name,rocketclaw_principal_email,source,rocketclaw_raw_text}'::text[]))::json
FROM split s WHERE c.conversation_id = s.conversation_id AND s.kept <> s.metadata;

-- A running turn's journaled steers are [{"id", "inbound"}] arrays; each External MCP inbound in one moves alike, in place.
WITH steers AS (
    SELECT s.conversation_id, s.key, e.steer, e.position, e.steer->'inbound'->'Metadata' AS metadata,
        e.steer->'inbound'->>'Source' = 'external_mcp' AND jsonb_typeof(e.steer->'inbound'->'Metadata') = 'object' AS external
    FROM steps s CROSS JOIN LATERAL jsonb_array_elements(s.value::jsonb) WITH ORDINALITY e(steer, position)
    WHERE s.key LIKE '%/steers'
), split AS (
    SELECT conversation_id, key, steer, position, metadata, CASE WHEN external THEN jsonb_strip_nulls(jsonb_build_object('rocketclaw_origin', metadata->'rocketclaw_origin', 'rocketclaw_media', metadata->'rocketclaw_media', 'rocketclaw_principal', metadata->'rocketclaw_principal', 'rocketclaw_principal_name', metadata->'rocketclaw_principal_name', 'rocketclaw_principal_email', metadata->'rocketclaw_principal_email', 'source', metadata->'source')) END AS kept
    FROM steers
), rebuilt AS (
    SELECT conversation_id, key, json_agg(CASE WHEN kept <> metadata THEN jsonb_set(steer, '{inbound}', steer->'inbound' || jsonb_build_object('Metadata', kept, 'ExternalMCPMetadata', metadata - '{rocketclaw_origin,rocketclaw_media,rocketclaw_principal,rocketclaw_principal_name,rocketclaw_principal_email,source,rocketclaw_raw_text}'::text[])) ELSE steer END ORDER BY position) AS value
    FROM split GROUP BY conversation_id, key HAVING bool_or(kept <> metadata)
)
UPDATE steps s SET value = r.value FROM rebuilt r WHERE s.conversation_id = r.conversation_id AND s.key = r.key;

-- +migrate Down
-- Caller keys merge back into Metadata, where the older binary reads them, except the keys it deleted
-- from caller input; Metadata wins a clash. A removed rocketclaw_raw_text is not restored.
UPDATE turns SET inbound_json = (inbound_json::jsonb - 'ExternalMCPMetadata' || CASE WHEN json_typeof(inbound_json->'ExternalMCPMetadata') = 'object' THEN jsonb_build_object('Metadata', (inbound_json::jsonb->'ExternalMCPMetadata') - '{rocketclaw_origin,rocketclaw_media,rocketclaw_principal,rocketclaw_principal_name,rocketclaw_principal_email,rocketclaw_sender_agent}'::text[] || CASE WHEN json_typeof(inbound_json->'Metadata') = 'object' THEN inbound_json::jsonb->'Metadata' ELSE '{}' END) ELSE '{}' END)::json
WHERE inbound_json->'ExternalMCPMetadata' IS NOT NULL;

UPDATE shadow_turns SET inbound_json = (inbound_json::jsonb - 'ExternalMCPMetadata' || CASE WHEN json_typeof(inbound_json->'ExternalMCPMetadata') = 'object' THEN jsonb_build_object('Metadata', (inbound_json::jsonb->'ExternalMCPMetadata') - '{rocketclaw_origin,rocketclaw_media,rocketclaw_principal,rocketclaw_principal_name,rocketclaw_principal_email,rocketclaw_sender_agent}'::text[] || CASE WHEN json_typeof(inbound_json->'Metadata') = 'object' THEN inbound_json::jsonb->'Metadata' ELSE '{}' END) ELSE '{}' END)::json
WHERE inbound_json->'ExternalMCPMetadata' IS NOT NULL;

UPDATE conversations SET producer_inbound_json = (producer_inbound_json::jsonb - 'ExternalMCPMetadata' || CASE WHEN json_typeof(producer_inbound_json->'ExternalMCPMetadata') = 'object' THEN jsonb_build_object('Metadata', (producer_inbound_json::jsonb->'ExternalMCPMetadata') - '{rocketclaw_origin,rocketclaw_media,rocketclaw_principal,rocketclaw_principal_name,rocketclaw_principal_email,rocketclaw_sender_agent}'::text[] || CASE WHEN json_typeof(producer_inbound_json->'Metadata') = 'object' THEN producer_inbound_json::jsonb->'Metadata' ELSE '{}' END) ELSE '{}' END)::json
WHERE producer_inbound_json->'ExternalMCPMetadata' IS NOT NULL;

WITH steers AS (
    SELECT s.conversation_id, s.key, e.steer, e.position, e.steer->'inbound' AS inbound
    FROM steps s CROSS JOIN LATERAL jsonb_array_elements(s.value::jsonb) WITH ORDINALITY e(steer, position)
    WHERE s.key LIKE '%/steers'
), rebuilt AS (
    SELECT conversation_id, key, json_agg(CASE WHEN inbound ? 'ExternalMCPMetadata' THEN jsonb_set(steer, '{inbound}', inbound - 'ExternalMCPMetadata' || CASE WHEN jsonb_typeof(inbound->'ExternalMCPMetadata') = 'object' THEN jsonb_build_object('Metadata', (inbound->'ExternalMCPMetadata') - '{rocketclaw_origin,rocketclaw_media,rocketclaw_principal,rocketclaw_principal_name,rocketclaw_principal_email,rocketclaw_sender_agent}'::text[] || CASE WHEN jsonb_typeof(inbound->'Metadata') = 'object' THEN inbound->'Metadata' ELSE '{}' END) ELSE '{}' END) ELSE steer END ORDER BY position) AS value
    FROM steers GROUP BY conversation_id, key HAVING bool_or(inbound ? 'ExternalMCPMetadata')
)
UPDATE steps s SET value = r.value FROM rebuilt r WHERE s.conversation_id = r.conversation_id AND s.key = r.key;
