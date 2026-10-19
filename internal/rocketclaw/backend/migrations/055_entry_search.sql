-- +migrate Up
-- Search reads a saved entry's or ended turn's search_text, which messageSearchText writes: a line
-- "<replay index> <part index> <JSON array of role, principal, text, lowercased text>" per message part
-- History shows. The text moves from message_search, which Go computed; 049 refused unless every visible chat was in it.
SET LOCAL lock_timeout = '5s';
-- Schemas sharing one database must not race to create the extension.
SELECT pg_advisory_xact_lock(hashtext('rocketclaw pg_trgm extension'));
CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;

ALTER TABLE entries ADD COLUMN search_text TEXT;
ALTER TABLE active_turns ADD COLUMN search_text TEXT;
-- The principals of the user messages a conversation shows, as message_search held them (refreshPrincipals).
ALTER TABLE conversations ADD COLUMN principals TEXT[] NOT NULL DEFAULT '{}';

-- Bulk backfill must not notify per row; the triggers come back below.
DROP TRIGGER entries_transcript_change ON entries;
DROP TRIGGER shadow_entries_change ON entries;

-- Search finds an entry in the chat it reports to, else in its own conversation. A row held under any
-- other, which a run's entry got before the run reported anywhere, was never found and stays so; so
-- was one of a conversation neither listed nor reporting to a chat, which gets no text (appendSessionEntryDB).
CREATE TEMP TABLE indexed_rows ON COMMIT DROP AS
SELECT m.* FROM message_search m LEFT JOIN entries e ON e.id = m.entry_id LEFT JOIN conversations r ON r.conversation_id = e.conversation_id
WHERE m.turn_id IS NOT NULL OR m.conversation_id = COALESCE(r.reports_to, e.conversation_id) AND (r.reports_to IS NOT NULL OR r.listed IS NOT FALSE);

-- PostgreSQL's JSON keeps U+2028 and U+2029 raw; Go's, like messageSearchText's, escapes them.
CREATE TEMP TABLE search_lines ON COMMIT DROP AS
WITH fields AS (
    SELECT entry_id, turn_id, replay_index, part_index,
        '[' || to_json(role)::text || ',' || to_json(principal)::text || ',' || to_json(text)::text || ',' || to_json(text_lower)::text || ']' AS fields
    FROM indexed_rows
)
SELECT entry_id, turn_id, replay_index, part_index,
    replay_index || ' ' || part_index || ' ' || replace(replace(fields, U&'\2028', '\u2028'), U&'\2029', '\u2029') AS line
FROM fields;

WITH texts AS (
    SELECT entry_id, string_agg(line, E'\n' ORDER BY replay_index, part_index) AS search_text FROM search_lines WHERE entry_id IS NOT NULL GROUP BY entry_id
)
UPDATE entries e SET search_text = t.search_text FROM texts t WHERE e.id = t.entry_id;
WITH texts AS (
    SELECT turn_id, string_agg(line, E'\n' ORDER BY replay_index, part_index) AS search_text FROM search_lines WHERE turn_id IS NOT NULL GROUP BY turn_id
)
UPDATE active_turns a SET search_text = t.search_text FROM texts t WHERE a.id = t.turn_id;

-- The self-check: every line read back is its indexed row, and every such row is a line.
-- +migrate StatementBegin
DO $$
DECLARE
    mismatches BIGINT;
BEGIN
    WITH texts AS (
        SELECT id AS entry_id, NULL::text AS turn_id, search_text FROM entries WHERE search_text IS NOT NULL
        UNION ALL SELECT NULL, id, search_text FROM active_turns WHERE search_text IS NOT NULL
    ), lines AS (
        SELECT t.entry_id, t.turn_id, m[1]::integer AS replay_index, m[2]::integer AS part_index, m[3]::json AS fields FROM texts t
        CROSS JOIN LATERAL string_to_table(t.search_text, E'\n') AS l(line) CROSS JOIN LATERAL regexp_match(l.line, '^(\d+) (\d+) (.*)$') AS r(m)
    ), stored AS (
        SELECT entry_id, turn_id, replay_index, part_index, fields->>0 AS role, fields->>1 AS principal, fields->>2 AS text, fields->>3 AS text_lower FROM lines
    ), source AS (
        SELECT entry_id, turn_id, replay_index, part_index, role, principal, text, text_lower FROM indexed_rows
    ), diffs AS (
        (SELECT * FROM source EXCEPT ALL SELECT * FROM stored) UNION ALL (SELECT * FROM stored EXCEPT ALL SELECT * FROM source)
    )
    SELECT count(*) INTO mismatches FROM diffs;
    IF mismatches > 0 THEN
        RAISE EXCEPTION '055_entry_search: % search text lines differ from message_search', mismatches;
    END IF;
END $$;
-- +migrate StatementEnd

-- A chat shows the principals of its indexed rows but those only a run's context holds; a staged undo
-- hides what follows its cut.
CREATE TEMP TABLE shown_principals ON COMMIT DROP AS
WITH cuts AS (
    SELECT conversation_id, NULLIF(split_part(revert_message_id, ':', 1), '')::bigint AS cut_entry,
        NULLIF(split_part(revert_message_id, ':', 2), '')::integer AS cut_index
    FROM conversations
), indexed AS (
    SELECT m.conversation_id, m.principal, COALESCE(m.entry_id, a.history_anchor_id) AS position, COALESCE(m.entry_id, 0) AS entry_id, m.replay_index
    FROM indexed_rows m LEFT JOIN entries e ON e.id = m.entry_id LEFT JOIN active_turns a ON a.id = m.turn_id
    WHERE m.principal <> '' AND COALESCE(e.audience, '') <> 'sandbox'
)
SELECT DISTINCT i.conversation_id, i.principal FROM indexed i JOIN cuts c USING (conversation_id)
WHERE c.cut_entry IS NULL OR i.position < c.cut_entry OR i.entry_id = c.cut_entry AND i.replay_index < c.cut_index;

WITH principals AS (
    SELECT conversation_id, array_agg(principal) AS principals FROM shown_principals GROUP BY conversation_id
)
UPDATE conversations c SET principals = p.principals FROM principals p WHERE c.conversation_id = p.conversation_id;

-- The self-check: every conversation holds exactly the principals it shows.
-- +migrate StatementBegin
DO $$
DECLARE
    mismatches BIGINT;
BEGIN
    WITH stored AS (
        SELECT c.conversation_id, p.principal FROM conversations c CROSS JOIN LATERAL unnest(c.principals) AS p(principal)
    ), diffs AS (
        (SELECT * FROM shown_principals EXCEPT SELECT * FROM stored) UNION ALL (SELECT * FROM stored EXCEPT SELECT * FROM shown_principals)
    )
    SELECT count(*) INTO mismatches FROM diffs;
    IF mismatches > 0 THEN
        RAISE EXCEPTION '055_entry_search: % chat principals differ from message_search', mismatches;
    END IF;
END $$;
-- +migrate StatementEnd

CREATE INDEX entries_search_text ON entries USING gin (search_text public.gin_trgm_ops);
CREATE INDEX active_turns_search_text ON active_turns USING gin (search_text public.gin_trgm_ops);
CREATE TRIGGER entries_transcript_change AFTER INSERT OR UPDATE OR DELETE ON entries FOR EACH ROW EXECUTE FUNCTION notify_transcript_change();
CREATE TRIGGER shadow_entries_change AFTER INSERT OR UPDATE OR DELETE ON entries FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();
ANALYZE entries;
ANALYZE active_turns;

-- +migrate Down
-- The previous release indexes every visible chat again at startup, this release's history included.
TRUNCATE message_search, message_search_indexed;
ALTER TABLE entries DROP COLUMN search_text;
ALTER TABLE active_turns DROP COLUMN search_text;
ALTER TABLE conversations DROP COLUMN principals;
