-- +migrate Up
-- A turn's step keeps the rest of its turn, and each item of its record's replay input and output trace,
-- and of its progress, is a step of its own, keyed by the turn's key, the list, and the item's zero-padded
-- index: a running turn writes each item once (rocketcode.Journal SaveTurn). Saved turn steps move to that
-- shape. Items stay json, which keeps their bytes; jsonb cannot hold a large turn's whole record.
-- Bulk re-keying must not notify per row; the triggers come back below.
DROP TRIGGER turn_steps_transcript_change ON turn_steps;
DROP TRIGGER shadow_steps_change ON turn_steps;

CREATE TEMP TABLE turn_records ON COMMIT DROP AS
SELECT conversation_id, key, value FROM turn_steps WHERE json_typeof(value->'record') = 'object';
CREATE TEMP TABLE turn_record_items ON COMMIT DROP AS
SELECT r.conversation_id, r.key || '/' || l.list || '/' || lpad((i.ordinal - 1)::text, 10, '0') AS key, i.item AS value FROM turn_records r
CROSS JOIN LATERAL (VALUES ('replay_input', r.value->'record'->'replay_input'), ('output_trace', r.value->'record'->'output_trace'), ('trace', r.value->'trace')) AS l(list, items)
CROSS JOIN LATERAL json_array_elements(CASE WHEN json_typeof(l.items) = 'array' THEN l.items END) WITH ORDINALITY AS i(item, ordinal);

INSERT INTO turn_steps (conversation_id, key, value) SELECT conversation_id, key, value FROM turn_record_items;
WITH records AS (
    SELECT r.conversation_id, r.key, json_object_agg(f.key, f.value) AS record FROM turn_records r
    CROSS JOIN LATERAL json_each(r.value->'record') AS f WHERE f.key NOT IN ('replay_input', 'output_trace') GROUP BY r.conversation_id, r.key
), steps AS (
    SELECT r.conversation_id, r.key, json_object_agg(f.key, CASE WHEN f.key = 'record' THEN c.record ELSE f.value END) AS value FROM turn_records r
    JOIN records c ON c.conversation_id = r.conversation_id AND c.key = r.key CROSS JOIN LATERAL json_each(r.value) AS f
    WHERE f.key <> 'trace' GROUP BY r.conversation_id, r.key
)
UPDATE turn_steps s SET value = t.value FROM steps t WHERE s.conversation_id = t.conversation_id AND s.key = t.key;

-- The self-check: every item is a step of its own with its text, and every step keeps its other fields as they were.
-- +migrate StatementBegin
DO $$
DECLARE
    mismatches BIGINT;
BEGIN
    WITH moved_items AS (
        SELECT conversation_id, key, md5(value::text) AS md5 FROM turn_record_items
    ), items AS (
        SELECT conversation_id, key, md5(value::text) AS md5 FROM turn_steps WHERE key ~ '/(replay_input|output_trace|trace)/[0-9]{10}$'
    ), moved_fields AS (
        SELECT r.conversation_id, r.key, 'record.' || f.key AS field, md5(f.value::text) AS md5 FROM turn_records r
        CROSS JOIN LATERAL json_each(r.value->'record') AS f WHERE f.key NOT IN ('replay_input', 'output_trace')
        UNION ALL SELECT r.conversation_id, r.key, f.key, md5(f.value::text) FROM turn_records r CROSS JOIN LATERAL json_each(r.value) AS f WHERE f.key NOT IN ('record', 'trace')
    ), steps AS (
        SELECT s.conversation_id, s.key, s.value FROM turn_steps s JOIN turn_records r ON r.conversation_id = s.conversation_id AND r.key = s.key
    ), fields AS (
        SELECT s.conversation_id, s.key, 'record.' || f.key AS field, md5(f.value::text) AS md5 FROM steps s CROSS JOIN LATERAL json_each(s.value->'record') AS f
        UNION ALL SELECT s.conversation_id, s.key, f.key, md5(f.value::text) FROM steps s CROSS JOIN LATERAL json_each(s.value) AS f WHERE f.key <> 'record'
    ), diffs AS (
        (SELECT * FROM moved_items EXCEPT SELECT * FROM items) UNION ALL (SELECT * FROM items EXCEPT SELECT * FROM moved_items)
        UNION ALL (SELECT conversation_id, key || ' ' || field, md5 FROM moved_fields EXCEPT SELECT conversation_id, key || ' ' || field, md5 FROM fields)
        UNION ALL (SELECT conversation_id, key || ' ' || field, md5 FROM fields EXCEPT SELECT conversation_id, key || ' ' || field, md5 FROM moved_fields)
    )
    SELECT count(*) INTO mismatches FROM diffs;
    IF mismatches > 0 THEN
        RAISE EXCEPTION '053_turn_items: % turn step values differ from their source steps', mismatches;
    END IF;
END $$;
-- +migrate StatementEnd

CREATE TRIGGER turn_steps_transcript_change AFTER INSERT OR UPDATE ON turn_steps FOR EACH ROW WHEN (strpos(NEW.key, '/') = 0) EXECUTE FUNCTION notify_transcript_change();
CREATE TRIGGER shadow_steps_change AFTER INSERT OR UPDATE OR DELETE ON turn_steps FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();

-- +migrate Down
DROP TRIGGER turn_steps_transcript_change ON turn_steps;
DROP TRIGGER shadow_steps_change ON turn_steps;

CREATE TEMP TABLE turn_record_items ON COMMIT DROP AS
SELECT conversation_id, substring(key FROM '^(.*)/[a-z_]+/[0-9]{10}$') AS turn_key, substring(key FROM '/([a-z_]+)/[0-9]{10}$') AS list, key, value
FROM turn_steps WHERE key ~ '/(replay_input|output_trace|trace)/[0-9]{10}$';
WITH lists AS (
    SELECT conversation_id, turn_key, list, json_agg(value ORDER BY key) AS items FROM turn_record_items GROUP BY conversation_id, turn_key, list
), record_fields AS (
    SELECT s.conversation_id, s.key, f.key AS field, f.value FROM turn_steps s CROSS JOIN LATERAL json_each(s.value->'record') AS f
    WHERE json_typeof(s.value->'record') = 'object'
    UNION ALL SELECT conversation_id, turn_key, list, items FROM lists WHERE list <> 'trace'
), records AS (
    SELECT conversation_id, key, json_object_agg(field, value) AS record FROM record_fields GROUP BY conversation_id, key
), step_fields AS (
    SELECT s.conversation_id, s.key, f.key AS field, CASE WHEN f.key = 'record' THEN r.record ELSE f.value END AS value FROM turn_steps s
    JOIN records r ON r.conversation_id = s.conversation_id AND r.key = s.key CROSS JOIN LATERAL json_each(s.value) AS f
    UNION ALL SELECT l.conversation_id, l.turn_key, 'trace', l.items FROM lists l WHERE l.list = 'trace'
), steps AS (
    SELECT conversation_id, key, json_object_agg(field, value) AS value FROM step_fields GROUP BY conversation_id, key
)
UPDATE turn_steps s SET value = t.value FROM steps t WHERE s.conversation_id = t.conversation_id AND s.key = t.key AND json_typeof(s.value->'record') = 'object';
DELETE FROM turn_steps s USING turn_record_items i WHERE s.conversation_id = i.conversation_id AND s.key = i.key;

CREATE TRIGGER turn_steps_transcript_change AFTER INSERT OR UPDATE ON turn_steps FOR EACH ROW WHEN (strpos(NEW.key, '/') = 0) EXECUTE FUNCTION notify_transcript_change();
CREATE TRIGGER shadow_steps_change AFTER INSERT OR UPDATE OR DELETE ON turn_steps FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();
