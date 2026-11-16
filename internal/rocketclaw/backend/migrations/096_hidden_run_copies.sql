-- +migrate Up
-- Copies made before v0.2.0 become single rows, so no reader needs entries.audience or the
-- sync_source_* keys. A copy in a hidden run (049 moved it there, visible-only) and the original
-- (sandbox-only) become one row: the original's bytes, which the run's own context replays, under the
-- copy's ID and search text, which its chat shows and finds; the chat strips the original's compaction
-- items as it does for every reported entry. A copy a Web chat holds of a run's entry goes, and the run
-- reports to that chat instead, so the chat shows the run's entry at the copy's place. Anything else
-- holding a copy is refused, and a refusal commits nothing, so the previous binary still starts.
CREATE TEMP TABLE copied_entries ON COMMIT DROP AS
WITH copies AS (
    -- The predicate matches session_entries_sync_source_id, which 097 drops.
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

-- Some External MCP chats also hold an older mirror of a run's entry: their own row, with the entry's
-- exact content and time but no source key. Such a chat shows the message twice, the mirror in place
-- and the copy at the end; once bound it shows the run's entry once, in place, and the mirror goes too.
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
        -- A chat bound below must show nothing of the run it did not show already.
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
        -- The run's own view keeps its order only if no other entry of it lies between an original and its copy.
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

-- What each affected conversation shows and replays now, for the self-check below: its own entries
-- but the other audience's, and the reporting and continued runs' entries but sandbox ones; each as a
-- reader projects it (compaction items stripped from a synced entry), attributed as revert.go does.
-- Two intended changes are taken out here: a copy in a chat its source's run already reports to, and a
-- mirror, are each a second showing of a message, and a chat holding mirrors shows its messages in
-- the order they were written (the run's entry IDs) rather than with the copies at the end.
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
    -- Where each row lands: a pair's original under its copy's ID, a chat's copy at its source.
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

-- Bulk merging must not notify per row; the triggers come back below.
ALTER TABLE entries DISABLE TRIGGER entries_transcript_change, DISABLE TRIGGER shadow_entries_change;

-- Pairs: the copy's row goes, and the original takes its ID and search text.
DELETE FROM entries e USING copied_entries c WHERE c.shape = 'pair' AND e.id = c.copy_id;
UPDATE entries e SET id = c.copy_id, audience = 'both', search_text = c.search_text FROM copied_entries c WHERE c.shape = 'pair' AND e.id = c.source_id;

-- Chats' copies: the run reports to the chat, its entry takes the copy's search text, and what pointed
-- at the copy points at the entry.
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

-- Each chat's entry facts as saveSessionSummary now reads them: whether it has entries of its own, and
-- the run its earliest shown entry came from, NULL when that entry is its own.
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

-- The self-check: every affected conversation shows and replays the same entries, in the same order,
-- with the same projected content and attribution, at the IDs computed above; no entry is anything
-- but both; no copy is left; and every chat keeps the origin it had.
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
        -- A chat left with no entries of its own is still found, by the time of its runs' latest entry.
        -- An External MCP chat's origin is its binding, whatever its first entry's source (DecideOrigin).
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
-- Merged copies are not restored; readers with the audience filters read the merged rows the same.
