-- +migrate Up
ALTER TABLE managed_conversations
    ADD COLUMN producer_inbound_json JSON,
    ADD COLUMN producer_effects_through_id BIGINT NOT NULL DEFAULT 0;

-- Completed legacy history has no reliable delivery/consumption facts. Baseline
-- original effects only; executable schedules are left intact.
UPDATE managed_conversations c SET producer_effects_through_id = COALESCE((
    SELECT MAX(e.id) FROM session_entries e WHERE e.conversation_id = c.conversation_id
        AND e.entry_json->>'type' IN ('producer_schedule', 'producer_reset_schedules')
        AND NOT e.entry_json::jsonb ? 'sync_source_entry_id'
), 0);

-- Old writers must be stopped before cutover. Unfinished private requests retain
-- their routing and all provably pending effects after their history anchor.
-- Only copies in the authoritative owner count as already projected effects.
WITH recoverable AS (
    SELECT DISTINCT ON (a.conversation_id) a.conversation_id, a.history_anchor_id,
        (a.inbound_json::jsonb || jsonb_build_object('SyncDestination', COALESCE(
            NULLIF(a.inbound_json->>'SyncDestination', ''),
            CASE WHEN s.value->>'ChannelID' <> '' AND s.value->>'MessageID' <> ''
                THEN 'slack-thread:' || (s.value->>'ChannelID') || ':' || (s.value->>'MessageID') END,
            ''
        ))) AS inbound
    FROM active_turns a
    LEFT JOIN turn_steps s ON s.conversation_id = a.conversation_id AND s.key = a.id || '/cron-root'
    WHERE a.phase <> 'done' AND (COALESCE(a.inbound_json->>'SyncDestination', '') <> ''
        OR a.inbound_json->>'RequireOutputDecision' = 'true')
    ORDER BY a.conversation_id, a.created_at_unix_ns, a.id
), pending AS (
    SELECT r.conversation_id, MIN(e.id) AS first_id
    FROM recoverable r JOIN session_entries e ON e.conversation_id = r.conversation_id
        AND e.id > r.history_anchor_id
        AND e.entry_json->>'type' IN ('producer_schedule', 'producer_reset_schedules')
        AND NOT e.entry_json::jsonb ? 'sync_source_entry_id'
    WHERE NOT EXISTS (
        SELECT 1 FROM session_entries owner WHERE owner.conversation_id = r.inbound->>'SyncDestination'
            AND owner.entry_json->>'sync_source_entry_id' = e.id::text
            AND COALESCE(owner.entry_json->>'sync_source_conversation_id', e.conversation_id) = r.conversation_id
    )
    GROUP BY r.conversation_id
)
UPDATE managed_conversations c SET producer_inbound_json = r.inbound::json,
    producer_effects_through_id = COALESCE((
        SELECT MAX(e.id) FROM session_entries e WHERE e.conversation_id = c.conversation_id
            AND e.entry_json->>'type' IN ('producer_schedule', 'producer_reset_schedules')
            AND NOT e.entry_json::jsonb ? 'sync_source_entry_id'
            AND (p.first_id IS NULL OR e.id < p.first_id)
    ), 0)
FROM recoverable r LEFT JOIN pending p ON p.conversation_id = r.conversation_id
WHERE c.conversation_id = r.conversation_id;

-- +migrate Down
ALTER TABLE managed_conversations
    DROP COLUMN producer_inbound_json,
    DROP COLUMN producer_effects_through_id;
