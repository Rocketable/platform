-- +migrate Up
-- Saved history lives in entries, and running turns' scratch in steps, which every turn end clears.
-- A stopped or failed turn's record and applied inputs move from its steps onto its done turn row,
-- where History reads them: its replay items as json, which keeps their bytes, since jsonb cannot
-- hold a large turn's whole replay, and the rest of its record as History built it. turn_steps
-- becomes legacy_turn_steps; its rows of turns not ended, and the Code Mode records of those turns,
-- move into steps; the ended turns' rows stay there as they were.
-- Bulk moves must not notify per row; the triggers come back on the renamed tables below.
DROP TRIGGER turn_steps_transcript_change ON turn_steps;
DROP TRIGGER shadow_steps_change ON turn_steps;
DROP TRIGGER session_entries_transcript_change ON session_entries;
DROP TRIGGER shadow_entries_change ON session_entries;
ALTER TABLE session_entries RENAME TO entries;

ALTER TABLE active_turns ADD COLUMN replay JSON, ADD COLUMN record JSON, ADD COLUMN applied_input_ids JSONB;
WITH replays AS (
    SELECT a.id, json_agg(s.value ORDER BY s.key) AS replay FROM active_turns a
    JOIN turn_steps s ON s.conversation_id = a.conversation_id AND starts_with(s.key, a.id || '/replay_input/')
    WHERE a.phase = 'done' AND a.terminal <> '' GROUP BY a.id
)
UPDATE active_turns a SET replay = r.replay, applied_input_ids = h.value::jsonb->'applied_input_ids',
    record = (COALESCE(h.value::jsonb->'record', '{}'::jsonb) || jsonb_build_object(
        'version', 1, 'type', 'turn', 'turn_id', a.id, 'output_trace', turn_trace(a.id, a.output_trace_json)))::json
FROM turn_steps h LEFT JOIN replays r ON r.id = h.key
WHERE h.conversation_id = a.conversation_id AND h.key = a.id AND a.phase = 'done' AND a.terminal <> '';

-- The steps of ended turns, except those running Background Jobs own.
CREATE TEMP TABLE ended_steps ON COMMIT DROP AS
WITH ended AS (
    SELECT conversation_id, id FROM active_turns WHERE phase = 'done'
), owned AS (
    SELECT conversation_id, job_id FROM background_jobs WHERE status = 'running'
)
SELECT DISTINCT s.conversation_id, s.key FROM turn_steps s
JOIN ended a ON a.conversation_id = s.conversation_id AND (s.key = a.id OR starts_with(s.key, a.id || '/'))
LEFT JOIN owned j ON j.conversation_id = s.conversation_id AND starts_with(s.key, j.job_id || '/')
WHERE j.job_id IS NULL;

ALTER TABLE turn_steps RENAME TO legacy_turn_steps;
-- id orders a turn's Code Mode records as first saved.
CREATE TABLE steps (conversation_id TEXT NOT NULL, key TEXT NOT NULL, id BIGINT GENERATED ALWAYS AS IDENTITY, value JSON NOT NULL, PRIMARY KEY (conversation_id, key));
INSERT INTO steps (conversation_id, key, value)
SELECT s.conversation_id, s.key, s.value FROM legacy_turn_steps s LEFT JOIN ended_steps e USING (conversation_id, key) WHERE e.key IS NULL;
-- A Code Mode record is a step under its turn row's key; the finished turn keeps its records in its entry.
INSERT INTO steps (conversation_id, key, id, value) OVERRIDING SYSTEM VALUE
SELECT a.conversation_id, a.id || '/code_mode/' || c.key, c.id, c.record FROM code_mode_calls c JOIN active_turns a ON a.id = c.turn_id WHERE a.phase <> 'done';
SELECT setval(pg_get_serial_sequence('steps', 'id'), max(id)) FROM steps;

-- The self-check: every moved replay item keeps its bytes and place, every moved record its step's
-- fields, trace, and Code Mode records, a stopped or failed turn without a step had no replay item or
-- Code Mode record to show, and every other step and Code Mode record of a turn not ended is a step
-- with its bytes. 049's turn_steps row count is not compared: 053 split each record into item rows.
-- +migrate StatementBegin
DO $$
DECLARE
    mismatches BIGINT;
BEGIN
    WITH moved AS (
        SELECT a.id, a.conversation_id, a.replay, a.record, a.applied_input_ids, a.output_trace_json, h.value AS head
        FROM active_turns a JOIN legacy_turn_steps h ON h.conversation_id = a.conversation_id AND h.key = a.id
        WHERE a.phase = 'done' AND a.terminal <> ''
    ), source_items AS (
        SELECT t.id, row_number() OVER (PARTITION BY t.id ORDER BY s.key) AS n, md5(s.value::text) AS md5 FROM moved t
        JOIN legacy_turn_steps s ON s.conversation_id = t.conversation_id AND starts_with(s.key, t.id || '/replay_input/')
    ), items AS (
        SELECT t.id, i.n, md5(i.item::text) AS md5 FROM moved t CROSS JOIN LATERAL json_array_elements(t.replay) WITH ORDINALITY AS i(item, n)
    ), source_fields AS (
        SELECT t.id, f.key, md5(f.value::jsonb::text) AS md5 FROM moved t CROSS JOIN LATERAL json_each(t.head->'record') AS f
        WHERE f.key NOT IN ('version', 'type', 'turn_id', 'replay_input', 'output_trace')
        UNION ALL SELECT id, 'version', md5('1') FROM moved
        UNION ALL SELECT id, 'type', md5('"turn"') FROM moved
        UNION ALL SELECT id, 'turn_id', md5(to_jsonb(id)::text) FROM moved
        UNION ALL SELECT id, 'applied_input_ids', md5(COALESCE(head::jsonb->'applied_input_ids', 'null')::text) FROM moved
    ), fields AS (
        SELECT t.id, f.key, md5(f.value::jsonb::text) AS md5 FROM moved t CROSS JOIN LATERAL json_each(t.record) AS f WHERE f.key <> 'output_trace'
        UNION ALL SELECT id, 'applied_input_ids', md5(COALESCE(applied_input_ids, 'null')::text) FROM moved
    ), source_trace AS (
        SELECT t.id, i.n, md5(i.item::jsonb::text) AS md5 FROM moved t CROSS JOIN LATERAL json_array_elements(t.output_trace_json::json) WITH ORDINALITY AS i(item, n)
        UNION ALL SELECT t.id, json_array_length(t.output_trace_json::json) + row_number() OVER (PARTITION BY t.id ORDER BY c.id), md5(c.record::jsonb::text)
        FROM moved t JOIN code_mode_calls c ON c.turn_id = t.id
    ), trace AS (
        SELECT t.id, i.n, md5(i.item::jsonb::text) AS md5 FROM moved t CROSS JOIN LATERAL json_array_elements(t.record->'output_trace') WITH ORDINALITY AS i(item, n)
    ), source_steps AS (
        SELECT s.conversation_id, s.key, md5(s.value::text) AS md5 FROM legacy_turn_steps s LEFT JOIN ended_steps e USING (conversation_id, key) WHERE e.key IS NULL
        UNION ALL SELECT a.conversation_id, a.id || '/code_mode/' || c.key, md5(c.record::text) FROM code_mode_calls c JOIN active_turns a ON a.id = c.turn_id WHERE a.phase <> 'done'
    ), moved_steps AS (
        SELECT conversation_id, key, md5(value::text) AS md5 FROM steps
    ), unrecorded AS (
        SELECT 1 FROM code_mode_calls c JOIN active_turns a ON a.id = c.turn_id WHERE a.phase = 'done' AND a.terminal <> '' AND a.record IS NULL
        UNION ALL SELECT 1 FROM legacy_turn_steps s JOIN active_turns a ON a.conversation_id = s.conversation_id AND starts_with(s.key, a.id || '/replay_input/')
        WHERE a.phase = 'done' AND a.terminal <> '' AND a.record IS NULL
    ), diffs AS (
        (SELECT * FROM source_items EXCEPT SELECT * FROM items) UNION ALL (SELECT * FROM items EXCEPT SELECT * FROM source_items)
        UNION ALL (SELECT id, 0, key || md5 FROM source_fields EXCEPT SELECT id, 0, key || md5 FROM fields)
        UNION ALL (SELECT id, 0, key || md5 FROM fields EXCEPT SELECT id, 0, key || md5 FROM source_fields)
        UNION ALL (SELECT * FROM source_trace EXCEPT SELECT * FROM trace) UNION ALL (SELECT * FROM trace EXCEPT SELECT * FROM source_trace)
        UNION ALL (SELECT conversation_id, 0, key || md5 FROM source_steps EXCEPT SELECT conversation_id, 0, key || md5 FROM moved_steps)
        UNION ALL (SELECT conversation_id, 0, key || md5 FROM moved_steps EXCEPT SELECT conversation_id, 0, key || md5 FROM source_steps)
        UNION ALL SELECT '', 0, '' FROM unrecorded
    )
    SELECT count(*) INTO mismatches FROM diffs;
    IF mismatches > 0 THEN
        RAISE EXCEPTION '054_entries_steps: % moved records or steps differ from their source', mismatches;
    END IF;
END $$;
-- +migrate StatementEnd

-- legacy_turn_steps keeps only what moved nowhere else: the ended turns' steps.
DELETE FROM legacy_turn_steps s USING steps m WHERE m.conversation_id = s.conversation_id AND m.key = s.key;

-- +migrate StatementBegin
CREATE OR REPLACE FUNCTION turn_trace(TEXT, TEXT) RETURNS jsonb LANGUAGE sql STABLE SET search_path FROM CURRENT AS $$
    WITH turn AS (
        SELECT conversation_id FROM active_turns WHERE id = $1
    )
    SELECT $2::jsonb || COALESCE(jsonb_agg(s.value::jsonb ORDER BY s.id), '[]'::jsonb) FROM turn t
    JOIN steps s ON s.conversation_id = t.conversation_id AND starts_with(s.key, $1 || '/code_mode/')
$$;
-- +migrate StatementEnd

-- readable_replay(size) is whether a read may expand a turn's replay of $1 bytes: one past jsonb's
-- limit, which bounded every turn record before, fails the read with the error jsonb failed it with
-- then, rather than taking many times its size of the backend's memory. readable_replay(replay) is
-- that replay once checked.
-- +migrate StatementBegin
CREATE FUNCTION readable_replay(BIGINT) RETURNS BOOLEAN LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
BEGIN
    IF $1 > 268435455 THEN
        RAISE EXCEPTION 'string too long to represent as jsonb string' USING ERRCODE = 'program_limit_exceeded',
            DETAIL = 'Due to an implementation restriction, jsonb strings cannot exceed 268435455 bytes.';
    END IF;
    RETURN TRUE;
END $$;
-- +migrate StatementEnd
-- +migrate StatementBegin
CREATE FUNCTION readable_replay(JSON) RETURNS JSON LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
BEGIN
    PERFORM readable_replay(octet_length($1::text)::bigint);
    RETURN $1;
END $$;
-- +migrate StatementEnd

CREATE TRIGGER entries_transcript_change AFTER INSERT OR UPDATE OR DELETE ON entries FOR EACH ROW EXECUTE FUNCTION notify_transcript_change();
CREATE TRIGGER shadow_entries_change AFTER INSERT OR UPDATE OR DELETE ON entries FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();
CREATE TRIGGER steps_transcript_change AFTER INSERT OR UPDATE ON steps FOR EACH ROW WHEN (strpos(NEW.key, '/') = 0) EXECUTE FUNCTION notify_transcript_change();
CREATE TRIGGER shadow_steps_change AFTER INSERT OR UPDATE OR DELETE ON steps FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();
-- A runner arriving after its turn ended may move its record again.
DROP TRIGGER active_turns_transcript_change ON active_turns;
CREATE TRIGGER active_turns_transcript_change AFTER INSERT OR UPDATE OF conversation_id, output_trace_json, terminal, phase, created_at_unix_ns, history_anchor_id, replay, record
    OR DELETE ON active_turns FOR EACH ROW EXECUTE FUNCTION notify_transcript_change();
DROP TRIGGER shadow_active_change ON active_turns;
CREATE TRIGGER shadow_active_change AFTER INSERT OR UPDATE OF output_trace_json, terminal, phase, replay, record OR DELETE ON active_turns
    FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();
ANALYZE steps;

-- +migrate Down
-- Turns that ended since keep their records only in their entries, if any.
DROP TRIGGER entries_transcript_change ON entries;
DROP TRIGGER shadow_entries_change ON entries;
DROP TRIGGER steps_transcript_change ON steps;
DROP TRIGGER shadow_steps_change ON steps;
DROP TRIGGER active_turns_transcript_change ON active_turns;
DROP TRIGGER shadow_active_change ON active_turns;
ALTER TABLE entries RENAME TO session_entries;

INSERT INTO code_mode_calls (turn_id, key, record)
SELECT a.id, substr(s.key, length(a.id || '/code_mode/') + 1), s.value FROM steps s
JOIN active_turns a ON a.conversation_id = s.conversation_id AND starts_with(s.key, a.id || '/code_mode/') ORDER BY s.id
ON CONFLICT (turn_id, key) DO UPDATE SET record = excluded.record;
DELETE FROM steps WHERE strpos(key, '/code_mode/') > 0;
INSERT INTO steps (conversation_id, key, value) SELECT conversation_id, key, value FROM legacy_turn_steps ON CONFLICT (conversation_id, key) DO NOTHING;
DROP TABLE legacy_turn_steps;
ALTER TABLE steps DROP COLUMN id;
ALTER TABLE steps RENAME TO turn_steps;
ALTER INDEX steps_pkey RENAME TO turn_steps_pkey;
ALTER TABLE active_turns DROP COLUMN replay, DROP COLUMN record, DROP COLUMN applied_input_ids;
DROP FUNCTION readable_replay(JSON);
DROP FUNCTION readable_replay(BIGINT);

-- +migrate StatementBegin
CREATE OR REPLACE FUNCTION turn_trace(TEXT, TEXT) RETURNS jsonb LANGUAGE sql STABLE SET search_path FROM CURRENT AS $$
    SELECT $2::jsonb || COALESCE(jsonb_agg(record::jsonb ORDER BY id), '[]'::jsonb) FROM code_mode_calls WHERE turn_id = $1
$$;
-- +migrate StatementEnd

CREATE TRIGGER session_entries_transcript_change AFTER INSERT OR UPDATE OR DELETE ON session_entries FOR EACH ROW EXECUTE FUNCTION notify_transcript_change();
CREATE TRIGGER shadow_entries_change AFTER INSERT OR UPDATE OR DELETE ON session_entries FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();
CREATE TRIGGER turn_steps_transcript_change AFTER INSERT OR UPDATE ON turn_steps FOR EACH ROW WHEN (strpos(NEW.key, '/') = 0) EXECUTE FUNCTION notify_transcript_change();
CREATE TRIGGER shadow_steps_change AFTER INSERT OR UPDATE OR DELETE ON turn_steps FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();
CREATE TRIGGER active_turns_transcript_change AFTER INSERT OR UPDATE OF conversation_id, output_trace_json, terminal, phase, created_at_unix_ns, history_anchor_id
    OR DELETE ON active_turns FOR EACH ROW EXECUTE FUNCTION notify_transcript_change();
CREATE TRIGGER shadow_active_change AFTER INSERT OR UPDATE OF output_trace_json, terminal, phase OR DELETE ON active_turns
    FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();
