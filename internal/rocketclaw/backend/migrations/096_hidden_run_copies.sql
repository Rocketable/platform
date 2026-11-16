-- +migrate Up
CREATE TEMP TABLE copied_entries ON COMMIT DROP AS
WITH copies AS (
    SELECT id, conversation_id, audience, entry_timestamp, search_text, entry_json::jsonb->>'sync_source_conversation_id' AS named,
        (entry_json::jsonb->>'sync_source_entry_id')::bigint AS source_id, entry_json::jsonb - 'sync_source_entry_id' - 'sync_source_conversation_id' AS content
    FROM entries WHERE (entry_json::jsonb->>'sync_source_entry_id') IS NOT NULL
), sources AS (
    SELECT c.*, s.conversation_id AS run, s.audience AS source_audience, s.entry_timestamp AS source_timestamp,
        CASE WHEN jsonb_typeof(s.entry_json::jsonb->'replay_input') = 'array'
            THEN jsonb_set(s.entry_json::jsonb, '{replay_input}', jsonb_path_query_array(s.entry_json::jsonb->'replay_input', '$[*] ? (!(@.type == "compaction" || @.type == "compaction_summary"))'))
            ELSE s.entry_json::jsonb END AS shown_source
    FROM copies c LEFT JOIN entries s ON s.id = c.source_id
)
SELECT s.id AS copy_id, s.conversation_id AS holder, s.audience, s.search_text, s.source_id, s.run, h.kind AS holder_kind, r.reports_to,
    CASE WHEN s.audience = 'visible' AND s.source_audience = 'sandbox' AND s.run = s.conversation_id AND r.reports_to IS NOT NULL THEN 'pair'
        WHEN s.audience = 'both' AND s.source_audience = 'both' AND h.kind = 'web_chat' AND h.listed AND r.reports_to IS NULL THEN 'bind'
        WHEN s.audience = 'both' AND s.source_audience = 'both' AND h.kind = 'web_chat' AND h.listed AND r.reports_to = s.conversation_id THEN 'duplicate'
    END AS shape,
    s.source_timestamp = s.entry_timestamp AND s.shown_source = s.content AND COALESCE(s.named, s.run) = s.run AS same_content
FROM sources s JOIN conversations h ON h.conversation_id = s.conversation_id LEFT JOIN conversations r ON r.conversation_id = s.run;
CREATE INDEX ON copied_entries (copy_id);
CREATE INDEX ON copied_entries (source_id);
ANALYZE copied_entries;

CREATE TEMP TABLE mirrored_entries ON COMMIT DROP AS
WITH bound AS (
    SELECT DISTINCT holder, run FROM copied_entries WHERE shape = 'bind'
), run_entries AS (
    SELECT b.holder, e.id, e.entry_timestamp, e.entry_json::jsonb AS content FROM bound b JOIN entries e ON e.conversation_id = b.run
)
SELECT o.id AS mirror_id, r.id AS source_id, r.holder FROM run_entries r
JOIN entries o ON o.conversation_id = r.holder AND o.entry_timestamp = r.entry_timestamp
WHERE (o.entry_json::jsonb->>'sync_source_entry_id') IS NULL AND o.entry_json::jsonb = r.content;

-- +migrate StatementBegin
DO $$
DECLARE
    refused BIGINT;
    example TEXT;
BEGIN
    WITH bound_runs AS (
        SELECT run, count(DISTINCT holder) AS holders FROM copied_entries WHERE shape IN ('bind', 'duplicate') GROUP BY run
    ), copied_ids AS (
        SELECT source_id AS id FROM copied_entries
    ), uncopied_run_entries AS (
        SELECT e.id, e.conversation_id FROM bound_runs b JOIN entries e ON e.conversation_id = b.run LEFT JOIN copied_ids c USING (id)
        JOIN copied_entries k ON k.run = b.run AND k.shape = 'bind' WHERE c.id IS NULL
    ), copy_ids AS (
        SELECT copy_id AS id FROM copied_entries WHERE shape = 'pair'
    ), pair_sources AS (
        SELECT source_id AS id FROM copied_entries WHERE shape = 'pair'
    ), stray AS (
        SELECT e.conversation_id || ' entry ' || e.id AS why FROM entries e LEFT JOIN copy_ids c USING (id) WHERE e.audience = 'visible' AND c.id IS NULL
        UNION ALL SELECT e.conversation_id || ' entry ' || e.id FROM entries e LEFT JOIN pair_sources p USING (id) WHERE e.audience = 'sandbox' AND p.id IS NULL
    ), interleaved AS (
        SELECT p.run || ' entry ' || e.id AS why FROM copied_entries p JOIN entries e ON e.conversation_id = p.run AND e.id > p.source_id AND e.id < p.copy_id AND e.audience = 'both'
        WHERE p.shape = 'pair'
    ), ambiguous_mirrors AS (
        SELECT holder || ' entry ' || source_id AS why FROM mirrored_entries GROUP BY holder, source_id HAVING count(*) > 1
        UNION ALL SELECT holder || ' entry ' || mirror_id FROM mirrored_entries GROUP BY holder, mirror_id HAVING count(*) > 1
    ), refusals AS (
        SELECT holder || ' copy ' || copy_id || ' has no known shape' AS why FROM copied_entries WHERE shape IS NULL
        UNION ALL SELECT why || ' has more than one mirror' FROM ambiguous_mirrors
        UNION ALL SELECT holder || ' copy ' || copy_id || ' differs from its source' FROM copied_entries WHERE NOT same_content
        UNION ALL SELECT run || ' is copied into ' || holders || ' chats' FROM bound_runs WHERE holders > 1
        UNION ALL SELECT conversation_id || ' entry ' || id || ' has no copy in the chat it would report to' FROM uncopied_run_entries
        UNION ALL SELECT why || ' is neither original nor copy' FROM stray
        UNION ALL SELECT why || ' lies between an original and its copy' FROM interleaved
    )
    SELECT count(*), min(why) INTO refused, example FROM refusals;
    IF refused > 0 THEN
        RAISE EXCEPTION '096_hidden_run_copies: % copies cannot be merged, e.g. %', refused, example;
    END IF;
END $$;
-- +migrate StatementEnd

CREATE TEMP TABLE hidden_run_views ON COMMIT DROP AS
WITH affected AS (
    SELECT holder AS id FROM copied_entries UNION SELECT run FROM copied_entries
), viewers AS (
    SELECT id FROM affected
    UNION SELECT c.reports_to FROM affected a JOIN conversations c ON c.conversation_id = a.id WHERE c.reports_to IS NOT NULL
    UNION SELECT c.conversation_id FROM affected a JOIN conversations c ON c.continues = a.id
), runs AS (
    SELECT v.id AS viewer, c.conversation_id AS run FROM viewers v JOIN conversations c ON c.reports_to = v.id
    UNION SELECT v.id, c.continues FROM viewers v JOIN conversations c ON c.conversation_id = v.id WHERE c.continues IS NOT NULL
), shown AS (
    SELECT v.id AS viewer, e.id, e.conversation_id, FALSE AS reported FROM viewers v JOIN conversations c ON c.conversation_id = v.id
    JOIN entries e ON e.conversation_id = v.id WHERE e.audience <> CASE WHEN c.producer_inbound_json IS NULL THEN 'sandbox' ELSE 'visible' END
    UNION ALL SELECT r.viewer, e.id, e.conversation_id, TRUE FROM runs r JOIN entries e ON e.conversation_id = r.run WHERE e.audience <> 'sandbox'
), duplicates AS (
    SELECT s.viewer, k.copy_id AS id FROM shown s JOIN copied_entries k ON k.source_id = s.id AND k.holder = s.viewer AND k.shape = 'duplicate' WHERE s.reported
    UNION ALL SELECT holder, mirror_id FROM mirrored_entries
), reordered AS (
    SELECT DISTINCT holder AS viewer FROM mirrored_entries
), attributed AS (
    SELECT s.viewer, s.id, e.entry_json::jsonb AS entry, s.reported OR e.entry_json::jsonb ? 'sync_source_entry_id' AS synced,
        CASE WHEN s.reported THEN s.conversation_id ELSE COALESCE(e.entry_json::jsonb->>'sync_source_conversation_id', src.conversation_id, '') END AS source
    FROM shown s JOIN entries e ON e.id = s.id LEFT JOIN duplicates d ON d.viewer = s.viewer AND d.id = s.id
    LEFT JOIN entries src ON NOT s.reported AND src.id = (e.entry_json::jsonb->>'sync_source_entry_id')::bigint
    WHERE d.id IS NULL
), moved AS (
    SELECT source_id AS id, copy_id AS lands FROM copied_entries WHERE shape = 'pair'
    UNION ALL SELECT copy_id, source_id FROM copied_entries WHERE shape IN ('bind', 'duplicate')
)
SELECT a.viewer, row_number() OVER (PARTITION BY a.viewer ORDER BY CASE WHEN r.viewer IS NULL THEN a.id ELSE COALESCE(m.lands, a.id) END) AS position,
    COALESCE(m.lands, a.id) AS id, a.synced, a.source,
    md5((CASE WHEN a.synced AND jsonb_typeof(a.entry->'replay_input') = 'array'
        THEN jsonb_set(a.entry, '{replay_input}', jsonb_path_query_array(a.entry->'replay_input', '$[*] ? (!(@.type == "compaction" || @.type == "compaction_summary"))'))
        ELSE a.entry END - 'sync_source_entry_id' - 'sync_source_conversation_id')::text) AS content
FROM attributed a LEFT JOIN moved m ON m.id = a.id LEFT JOIN reordered r ON r.viewer = a.viewer;

CREATE TEMP TABLE hidden_run_origins ON COMMIT DROP AS
SELECT DISTINCT c.conversation_id, c.first_source FROM copied_entries k JOIN conversations c ON c.conversation_id = k.holder WHERE k.shape IN ('bind', 'duplicate');

ALTER TABLE entries DISABLE TRIGGER entries_transcript_change, DISABLE TRIGGER shadow_entries_change;

DELETE FROM entries e USING copied_entries c WHERE c.shape = 'pair' AND e.id = c.copy_id;
UPDATE entries e SET id = c.copy_id, audience = 'both', search_text = c.search_text FROM copied_entries c WHERE c.shape = 'pair' AND e.id = c.source_id;

WITH bindings AS (
    SELECT DISTINCT holder, run FROM copied_entries WHERE shape = 'bind'
)
UPDATE conversations r SET reports_to = b.holder FROM bindings b WHERE r.conversation_id = b.run;
UPDATE entries e SET search_text = c.search_text FROM copied_entries c
WHERE c.shape IN ('bind', 'duplicate') AND e.id = c.source_id AND e.search_text IS NULL AND c.search_text IS NOT NULL;
CREATE TEMP TABLE retired_entries ON COMMIT DROP AS
SELECT copy_id AS id, source_id FROM copied_entries WHERE shape IN ('bind', 'duplicate')
UNION ALL SELECT mirror_id, source_id FROM mirrored_entries;
UPDATE turns t SET history_anchor_id = r.source_id FROM retired_entries r WHERE t.history_anchor_id = r.id;
UPDATE conversations v SET revert_message_id = r.source_id::text || ':' || split_part(v.revert_message_id, ':', 2) FROM retired_entries r
WHERE v.revert_message_id <> '' AND split_part(v.revert_message_id, ':', 1)::bigint = r.id;
DELETE FROM entries e USING retired_entries r WHERE e.id = r.id;

WITH holders AS (
    SELECT DISTINCT holder AS conversation_id FROM copied_entries WHERE shape IN ('bind', 'duplicate')
), own AS (
    SELECT h.conversation_id, min(e.id) AS id FROM holders h JOIN entries e USING (conversation_id) GROUP BY h.conversation_id
), reported AS (
    SELECT DISTINCT ON (r.reports_to) r.reports_to AS conversation_id, e.id, r.conversation_id AS source
    FROM holders h JOIN conversations r ON r.reports_to = h.conversation_id JOIN entries e ON e.conversation_id = r.conversation_id
    ORDER BY r.reports_to, e.id
), facts AS (
    SELECT h.conversation_id, o.id IS NOT NULL AS has_entries, CASE WHEN o.id IS NULL OR p.id < o.id THEN p.source END AS first_source
    FROM holders h LEFT JOIN own o USING (conversation_id) LEFT JOIN reported p USING (conversation_id)
)
UPDATE conversations c SET has_entries = f.has_entries, first_source = f.first_source FROM facts f WHERE c.conversation_id = f.conversation_id;

ALTER TABLE entries ENABLE TRIGGER entries_transcript_change, ENABLE TRIGGER shadow_entries_change;

-- +migrate StatementBegin
DO $$
DECLARE
    mismatches BIGINT;
BEGIN
    WITH viewers AS (
        SELECT DISTINCT viewer AS id FROM hidden_run_views
    ), runs AS (
        SELECT v.id AS viewer, c.conversation_id AS run FROM viewers v JOIN conversations c ON c.reports_to = v.id
        UNION SELECT v.id, c.continues FROM viewers v JOIN conversations c ON c.conversation_id = v.id WHERE c.continues IS NOT NULL
    ), shown AS (
        SELECT v.id AS viewer, e.id, e.conversation_id, FALSE AS reported FROM viewers v JOIN entries e ON e.conversation_id = v.id
        UNION ALL SELECT r.viewer, e.id, e.conversation_id, TRUE FROM runs r JOIN entries e ON e.conversation_id = r.run
    ), now_shown AS (
        SELECT s.viewer, row_number() OVER (PARTITION BY s.viewer ORDER BY s.id) AS position, s.id, s.reported AS synced, CASE WHEN s.reported THEN s.conversation_id ELSE '' END AS source,
            md5((CASE WHEN s.reported AND jsonb_typeof(e.entry_json::jsonb->'replay_input') = 'array'
                THEN jsonb_set(e.entry_json::jsonb, '{replay_input}', jsonb_path_query_array(e.entry_json::jsonb->'replay_input', '$[*] ? (!(@.type == "compaction" || @.type == "compaction_summary"))'))
                ELSE e.entry_json::jsonb END - 'sync_source_entry_id' - 'sync_source_conversation_id')::text) AS content
        FROM shown s JOIN entries e ON e.id = s.id
    ), view_diff AS (
        (SELECT * FROM now_shown EXCEPT SELECT * FROM hidden_run_views) UNION ALL (SELECT * FROM hidden_run_views EXCEPT SELECT * FROM now_shown)
    ), leftovers AS (
        SELECT 1 FROM entries WHERE audience <> 'both' UNION ALL SELECT 1 FROM entries WHERE (entry_json::jsonb->>'sync_source_entry_id') IS NOT NULL
    ), origin_diff AS (
        SELECT 1 FROM hidden_run_origins o JOIN conversations c USING (conversation_id)
        WHERE c.first_source IS DISTINCT FROM o.first_source AND NOT (c.external_id IS NOT NULL AND c.external_run = c.first_source)
            OR NOT c.has_entries AND c.last_updated > '0001-01-01' IS NOT TRUE
    ), diffs AS (
        SELECT 1 FROM view_diff UNION ALL SELECT 1 FROM leftovers UNION ALL SELECT 1 FROM origin_diff
    )
    SELECT count(*) INTO mismatches FROM diffs;
    IF mismatches > 0 THEN
        RAISE EXCEPTION '096_hidden_run_copies: % rows differ from what conversations showed before', mismatches;
    END IF;
END $$;
-- +migrate StatementEnd

-- +migrate Down
