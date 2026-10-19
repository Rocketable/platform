-- +migrate Up
-- A refusal here commits nothing, so the previous binary still starts.
-- +migrate StatementBegin
DO $$
DECLARE
    busy BIGINT;
    unindexed BIGINT;
    uncopied BIGINT;
BEGIN
    WITH unfinished AS (
        SELECT 1 FROM active_turns WHERE phase IN ('running', 'delivering')
        UNION ALL SELECT 1 FROM background_jobs WHERE status = 'running'
        UNION ALL SELECT 1 FROM shadow_turns WHERE child_turn_id = '' AND terminal = ''
    )
    SELECT count(*) INTO busy FROM unfinished;
    IF busy > 0 THEN
        RAISE EXCEPTION '049_hidden_run_entries: % turns, Background Jobs, or shadow turns are unfinished; stop them first', busy;
    END IF;
    -- 055 moves search text from message_search, so every visible chat must be in it.
    WITH visible_chats AS (
        SELECT c.conversation_id FROM managed_conversations c
        LEFT JOIN shadow_bindings b ON b.child_conversation_id = c.conversation_id
        LEFT JOIN external_mcp_sessions p ON p.private_conversation_id = c.conversation_id
        LEFT JOIN managed_conversations parent ON parent.conversation_id = c.parent_conversation_id
        WHERE c.conversation_id NOT LIKE 'cron:%' AND c.conversation_id NOT LIKE 'one-off-cron:%'
            AND b.child_conversation_id IS NULL AND p.private_conversation_id IS NULL AND parent.conversation_id IS NULL
    )
    SELECT count(*) INTO unindexed FROM visible_chats c LEFT JOIN message_search_indexed i USING (conversation_id) WHERE i.conversation_id IS NULL;
    IF unindexed > 0 THEN
        RAISE EXCEPTION '049_hidden_run_entries: % visible chats are not fully in message_search; run the previous release until its startup backfill finishes', unindexed;
    END IF;
    -- A chat holds everything its hidden runs' context does: below, each original of a run bound to
    -- a chat becomes that context only, so its copy must already be in the chat.
    WITH bound AS (
        SELECT conversation_id AS run, producer_inbound_json->>'SyncDestination' AS chat FROM managed_conversations
        WHERE NULLIF(producer_inbound_json->>'SyncDestination', '') IS NOT NULL
    ), originals AS (
        SELECT e.id, b.chat FROM bound b JOIN session_entries e ON e.conversation_id = b.run WHERE NOT e.entry_json::jsonb ? 'sync_source_entry_id'
    ), copies AS (
        SELECT DISTINCT conversation_id AS chat, (entry_json->>'sync_source_entry_id')::bigint AS id FROM session_entries WHERE entry_json::jsonb ? 'sync_source_entry_id'
    )
    SELECT count(*) INTO uncopied FROM originals o LEFT JOIN copies c USING (chat, id) WHERE c.id IS NULL;
    IF uncopied > 0 THEN
        RAISE EXCEPTION '049_hidden_run_entries: % entries of hidden runs have no copy in the chat they report to, which must hold all of them', uncopied;
    END IF;
END $$;
-- +migrate StatementEnd

-- The baseline manifest every later migration checks itself against. It lives in
-- this State Store's own schema, since several State Store schemas can share one database.
CREATE TABLE legacy_entry_md5 AS SELECT id AS entry_id, md5(entry_json::text) AS md5 FROM session_entries;
CREATE TABLE legacy_history AS
SELECT conversation_id, row_number() OVER (PARTITION BY conversation_id ORDER BY id) AS position, id AS entry_id FROM session_entries;
CREATE TABLE legacy_listed AS
SELECT c.conversation_id FROM managed_conversations c
LEFT JOIN shadow_bindings b ON b.child_conversation_id = c.conversation_id
LEFT JOIN external_mcp_sessions p ON p.private_conversation_id = c.conversation_id
LEFT JOIN managed_conversations parent ON parent.conversation_id = c.parent_conversation_id
WHERE c.conversation_id NOT LIKE 'cron:%' AND c.conversation_id NOT LIKE 'one-off-cron:%'
    AND b.child_conversation_id IS NULL AND p.private_conversation_id IS NULL AND parent.conversation_id IS NULL;
CREATE TABLE legacy_shown AS
WITH with_history AS (
    SELECT DISTINCT conversation_id FROM session_entries
), running AS (
    SELECT DISTINCT conversation_id FROM active_turns WHERE phase <> 'done'
)
SELECT l.conversation_id FROM legacy_listed l
LEFT JOIN with_history h USING (conversation_id) LEFT JOIN running r USING (conversation_id)
WHERE h.conversation_id IS NOT NULL OR r.conversation_id IS NOT NULL;
CREATE TABLE legacy_lane_work AS
SELECT conversation_id, row_number() OVER (PARTITION BY conversation_id ORDER BY park_after <> '', position, stash_at_unix_ns, queue_item_id) AS position,
    queue_item_id, kind FROM thread_queue;
CREATE TABLE legacy_row_counts AS
SELECT 'session_entries' AS table_name, '' AS state, count(*) AS n FROM session_entries
UNION ALL SELECT 'managed_conversations', CASE WHEN producer_inbound_json IS NULL THEN '' ELSE 'hidden run' END, count(*) FROM managed_conversations GROUP BY 2
UNION ALL SELECT 'active_turns', phase || '/' || terminal, count(*) FROM active_turns GROUP BY 2
UNION ALL SELECT 'turn_steps', '', count(*) FROM turn_steps
UNION ALL SELECT 'thread_queue', kind, count(*) FROM thread_queue GROUP BY 2
UNION ALL SELECT 'background_jobs', status || '/' || note_state, count(*) FROM background_jobs GROUP BY 2
UNION ALL SELECT 'scheduled_messages', '', count(*) FROM scheduled_messages
UNION ALL SELECT 'shadow_bindings', '', count(*) FROM shadow_bindings
UNION ALL SELECT 'shadow_turns', terminal, count(*) FROM shadow_turns GROUP BY 2
UNION ALL SELECT 'session_summaries', '', count(*) FROM session_summaries
UNION ALL SELECT 'message_search', '', count(*) FROM message_search
UNION ALL SELECT 'message_search_indexed', '', count(*) FROM message_search_indexed
UNION ALL SELECT 'external_mcp_sessions', '', count(*) FROM external_mcp_sessions
UNION ALL SELECT 'conversation_goals', status, count(*) FROM conversation_goals GROUP BY 2
UNION ALL SELECT 'code_mode_calls', '', count(*) FROM code_mode_calls
UNION ALL SELECT 'attachments', '', count(*) FROM attachments
UNION ALL SELECT 'session_tags', '', count(*) FROM session_tags;

-- A hidden run reports to one visible conversation, set once by its first destination.
-- The index comes before the backfill, so the backfill's updates cannot leave it unusable to older snapshots.
ALTER TABLE managed_conversations ADD COLUMN reports_to TEXT;
CREATE INDEX managed_conversations_reports_to ON managed_conversations (reports_to) WHERE reports_to IS NOT NULL;
UPDATE managed_conversations SET reports_to = NULLIF(producer_inbound_json->>'SyncDestination', '') WHERE producer_inbound_json IS NOT NULL;
DROP INDEX managed_conversations_sync_destination;

-- both: every view; sandbox: only the run's own context; visible: only the chat it reports to.
ALTER TABLE session_entries ADD COLUMN audience TEXT NOT NULL DEFAULT 'both' CHECK (audience IN ('both', 'sandbox', 'visible'));

-- Pending schedule effects become scheduled messages of the chat the run reports
-- to, or of the run until a reminder hands it to a Web chat. Only the effects after
-- a run's last pending reset survive it, and that reset clears its target.
CREATE TEMP TABLE pending_effects ON COMMIT DROP AS
WITH runs AS (
    SELECT conversation_id AS run, COALESCE(reports_to, conversation_id) AS target, producer_effects_through_id AS through
    FROM managed_conversations WHERE producer_inbound_json IS NOT NULL
), effects AS (
    SELECT r.run, r.target, e.id, e.entry_json->>'type' AS kind, e.entry_json::jsonb->'output_trace'->0 AS schedule
    FROM runs r JOIN session_entries e ON e.conversation_id = r.run
    WHERE e.id > r.through AND e.entry_json->>'type' IN ('producer_schedule', 'producer_reset_schedules')
        AND NOT e.entry_json::jsonb ? 'sync_source_entry_id'
), resets AS (
    SELECT run, max(id) AS reset_id FROM effects WHERE kind = 'producer_reset_schedules' GROUP BY run
)
SELECT f.run, f.target, f.id, f.kind, f.schedule, COALESCE(s.reset_id, 0) AS reset_id FROM effects f LEFT JOIN resets s USING (run);

WITH bound AS (
    SELECT m.conversation_id, m.reports_to, t.agent FROM managed_conversations m
    JOIN managed_conversations t ON t.conversation_id = m.reports_to
)
UPDATE scheduled_messages s SET conversation_id = b.reports_to, agent = b.agent FROM bound b WHERE s.conversation_id = b.conversation_id;
WITH reset_targets AS (
    SELECT DISTINCT target FROM pending_effects WHERE kind = 'producer_reset_schedules'
), parks AS (
    UPDATE thread_queue q SET park_after = '' FROM reset_targets r WHERE q.conversation_id = r.target
)
DELETE FROM scheduled_messages s USING reset_targets r WHERE s.conversation_id = r.target;
WITH schedules AS (
    SELECT p.id, p.target, p.schedule FROM pending_effects p WHERE p.kind = 'producer_schedule' AND p.id > p.reset_id
)
INSERT INTO scheduled_messages (scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring, interval_ns)
SELECT 'producer-' || s.id, s.target, COALESCE(t.agent, ''), COALESCE(s.schedule->>'message', ''),
    (extract(epoch FROM (s.schedule->>'due_at')::timestamptz) * 1000000)::bigint * 1000,
    (COALESCE(s.schedule->>'recurring', 'false'))::boolean::integer, COALESCE(s.schedule->>'interval', '0')::bigint
FROM schedules s LEFT JOIN managed_conversations t ON t.conversation_id = s.target;
WITH applied AS (
    SELECT run, max(id) AS id FROM pending_effects GROUP BY run
)
UPDATE managed_conversations m SET producer_effects_through_id = a.id FROM applied a WHERE m.conversation_id = a.run;

-- Bulk re-ownership must not notify per row; the triggers come back below.
DROP TRIGGER session_entries_transcript_change ON session_entries;
DROP TRIGGER shadow_entries_change ON session_entries;

-- Every copy stays a row with its ID. The copy in its run's reports_to chat moves to
-- the run as visible-only; a copy of a run without a recorded row stays its chat's,
-- visible-only; any other copy stays its chat's ordinary entry.
CREATE TEMP TABLE run_copies ON COMMIT DROP AS
WITH copies AS (
    SELECT id, conversation_id AS chat, (entry_json->>'sync_source_entry_id')::bigint AS source_id, entry_json->>'sync_source_conversation_id' AS named_run
    FROM session_entries WHERE entry_json::jsonb ? 'sync_source_entry_id'
)
SELECT c.id, c.chat, c.source_id, m.conversation_id AS run, m.reports_to IS NOT DISTINCT FROM c.chat AS home
FROM copies c LEFT JOIN session_entries s ON s.id = c.source_id
LEFT JOIN managed_conversations m ON m.conversation_id = COALESCE(c.named_run, s.conversation_id);

UPDATE session_entries e SET conversation_id = c.run, audience = 'visible' FROM run_copies c WHERE e.id = c.id AND c.home;
UPDATE session_entries e SET audience = 'visible' FROM run_copies c WHERE e.id = c.id AND c.run IS NULL;
-- A bound run's originals stay its context but leave its chat, which showed only their copies.
UPDATE session_entries e SET audience = 'sandbox' FROM managed_conversations m
WHERE e.conversation_id = m.conversation_id AND m.reports_to IS NOT NULL AND NOT e.entry_json::jsonb ? 'sync_source_entry_id';

-- Each original and copy falls into one bucket; the rehearsal records the totals.
CREATE TABLE legacy_hidden_run_buckets AS
WITH originals AS (
    SELECT e.id FROM legacy_history h JOIN session_entries e ON e.id = h.entry_id
    JOIN managed_conversations m ON m.conversation_id = h.conversation_id
    WHERE m.producer_inbound_json IS NOT NULL AND NOT e.entry_json::jsonb ? 'sync_source_entry_id'
), copied AS (
    SELECT o.id, count(c.id) AS copies FROM originals o LEFT JOIN run_copies c ON c.source_id = o.id GROUP BY o.id
), orphans AS (
    SELECT c.id FROM run_copies c LEFT JOIN originals o ON o.id = c.source_id WHERE o.id IS NULL
)
SELECT CASE copies WHEN 0 THEN 'original only' WHEN 1 THEN 'original with one copy' ELSE 'original with several copies' END AS bucket,
    count(*) AS originals, sum(copies) AS copies FROM copied GROUP BY 1
UNION ALL SELECT 'copy only', 0, count(*) FROM orphans;

-- Live updates reach the owning conversation and the chat it reports to; copies no longer fan out.
-- +migrate StatementBegin
CREATE OR REPLACE FUNCTION notify_transcript_change() RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE
    channel TEXT := 'rocketclaw_transcript_' || md5(TG_TABLE_SCHEMA);
    revision TEXT := pg_current_xact_id()::text;
    changed TEXT[] := '{}';
    destination TEXT;
BEGIN
    IF TG_OP <> 'INSERT' THEN changed := changed || OLD.conversation_id; END IF;
    IF TG_OP <> 'DELETE' THEN changed := changed || NEW.conversation_id; END IF;
    IF TG_TABLE_NAME = 'background_jobs' THEN
        IF TG_OP <> 'INSERT' THEN changed := changed || OLD.sync_destination; END IF;
        IF TG_OP <> 'DELETE' THEN changed := changed || NEW.sync_destination; END IF;
    END IF;
    FOREACH destination IN ARRAY changed LOOP
        IF destination <> '' THEN
            PERFORM pg_notify(channel, json_build_object('conversationId', destination, 'revision', revision)::text);
        END IF;
    END LOOP;
    FOR destination IN
        WITH owners AS (
            SELECT DISTINCT unnest(changed) AS id
        )
        SELECT DISTINCT m.reports_to FROM owners o JOIN managed_conversations m ON m.conversation_id = o.id WHERE m.reports_to IS NOT NULL
    LOOP
        PERFORM pg_notify(channel, json_build_object('conversationId', destination, 'revision', revision)::text);
    END LOOP;
    RETURN NULL;
END;
$$;
-- +migrate StatementEnd

-- +migrate StatementBegin
CREATE OR REPLACE FUNCTION notify_sync_destination_change() RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE
    channel TEXT := 'rocketclaw_transcript_' || md5(TG_TABLE_SCHEMA);
    revision TEXT := pg_current_xact_id()::text;
    destination TEXT;
BEGIN
    FOR destination IN
        WITH owners AS (
            SELECT OLD.conversation_id AS id WHERE TG_OP <> 'INSERT'
            UNION SELECT NEW.conversation_id WHERE TG_OP <> 'DELETE'
        )
        SELECT DISTINCT m.reports_to FROM owners o JOIN managed_conversations m ON m.conversation_id = o.id WHERE m.reports_to IS NOT NULL
    LOOP
        PERFORM pg_notify(channel, json_build_object('conversationId', destination, 'revision', revision)::text);
    END LOOP;
    RETURN NULL;
END;
$$;
-- +migrate StatementEnd

-- +migrate StatementBegin
CREATE OR REPLACE FUNCTION notify_shadow_change() RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE
    children TEXT[] := '{}';
    parents TEXT[] := '{}';
    destination TEXT;
    channel TEXT := 'rocketclaw_transcript_' || md5(TG_TABLE_SCHEMA);
    revision TEXT := pg_current_xact_id()::text;
BEGIN
    -- Read only ownership keys; transcript payloads can be large.
    IF TG_TABLE_NAME = 'shadow_bindings' THEN
        IF TG_OP <> 'INSERT' THEN parents := array_append(parents, OLD.parent_conversation_id); END IF;
        IF TG_OP <> 'DELETE' THEN parents := array_append(parents, NEW.parent_conversation_id); END IF;
    ELSIF TG_TABLE_NAME = 'shadow_turns' THEN
        IF TG_OP <> 'INSERT' THEN children := array_append(children, OLD.child_conversation_id); END IF;
        IF TG_OP <> 'DELETE' THEN children := array_append(children, NEW.child_conversation_id); END IF;
    ELSE
        IF TG_OP <> 'INSERT' THEN children := array_append(children, OLD.conversation_id); END IF;
        IF TG_OP <> 'DELETE' THEN children := array_append(children, NEW.conversation_id); END IF;
    END IF;
    FOR destination IN
        WITH changed AS (
            SELECT unnest(children) AS id
        ), bound_parents AS (
            SELECT DISTINCT b.parent_conversation_id AS id FROM shadow_bindings b
            JOIN changed c ON c.id = b.child_conversation_id OR starts_with(c.id, b.child_conversation_id || '/')
            UNION SELECT unnest(parents)
        ), targets AS (
            SELECT id FROM bound_parents UNION SELECT m.reports_to FROM bound_parents p JOIN managed_conversations m ON m.conversation_id = p.id
        )
        SELECT id FROM targets WHERE COALESCE(id, '') <> ''
    LOOP
        PERFORM pg_notify(channel, json_build_object('conversationId', destination, 'revision', revision)::text);
    END LOOP;
    RETURN NULL;
END;
$$;
-- +migrate StatementEnd

CREATE TRIGGER session_entries_transcript_change AFTER INSERT OR UPDATE OR DELETE ON session_entries FOR EACH ROW EXECUTE FUNCTION notify_transcript_change();
CREATE TRIGGER shadow_entries_change AFTER INSERT OR UPDATE OR DELETE ON session_entries
    FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();

-- The self-check: every entry keeps its bytes, every conversation's history keeps its
-- entries in order, the shown chats stay the same, and the buckets reconcile.
-- +migrate StatementBegin
DO $$
DECLARE
    mismatches BIGINT;
BEGIN
    WITH runs AS (
        SELECT conversation_id, reports_to FROM managed_conversations WHERE reports_to IS NOT NULL
    ), own AS (
        SELECT e.conversation_id AS viewer, e.id FROM session_entries e LEFT JOIN runs r USING (conversation_id)
        WHERE e.audience <> CASE WHEN r.reports_to IS NULL THEN 'sandbox' ELSE 'visible' END
    ), reported AS (
        SELECT r.reports_to AS viewer, e.id FROM runs r JOIN session_entries e USING (conversation_id) WHERE e.audience <> 'sandbox'
    ), viewed AS (
        SELECT * FROM own UNION ALL SELECT * FROM reported
    ), history AS (
        SELECT viewer AS conversation_id, row_number() OVER (PARTITION BY viewer ORDER BY id) AS position, id AS entry_id FROM viewed
    ), history_diff AS (
        (SELECT * FROM history EXCEPT SELECT * FROM legacy_history)
        UNION ALL (SELECT * FROM legacy_history EXCEPT SELECT * FROM history)
    ), md5s AS (
        SELECT id AS entry_id, md5(entry_json::text) AS md5 FROM session_entries
    ), md5_diff AS (
        (SELECT * FROM md5s EXCEPT SELECT * FROM legacy_entry_md5)
        UNION ALL (SELECT * FROM legacy_entry_md5 EXCEPT SELECT * FROM md5s)
    ), viewers AS (
        SELECT DISTINCT viewer AS conversation_id FROM viewed
    ), running AS (
        SELECT DISTINCT conversation_id FROM active_turns WHERE phase <> 'done'
    ), shown AS (
        SELECT l.conversation_id FROM legacy_listed l
        LEFT JOIN viewers v USING (conversation_id) LEFT JOIN running r USING (conversation_id)
        WHERE v.conversation_id IS NOT NULL OR r.conversation_id IS NOT NULL
    ), shown_diff AS (
        (SELECT * FROM shown EXCEPT SELECT * FROM legacy_shown)
        UNION ALL (SELECT * FROM legacy_shown EXCEPT SELECT * FROM shown)
    ), bucket_totals AS (
        SELECT sum(originals) AS originals, sum(copies) AS copies FROM legacy_hidden_run_buckets
    ), source_totals AS (
        SELECT count(*) AS originals FROM legacy_history h JOIN managed_conversations m ON m.conversation_id = h.conversation_id
        JOIN session_entries e ON e.id = h.entry_id WHERE m.producer_inbound_json IS NOT NULL AND NOT e.entry_json::jsonb ? 'sync_source_entry_id'
    ), copy_totals AS (
        SELECT count(*) AS copies FROM run_copies
    ), bucket_diff AS (
        SELECT 1 FROM bucket_totals b CROSS JOIN source_totals s CROSS JOIN copy_totals c
        WHERE b.originals IS DISTINCT FROM s.originals OR b.copies IS DISTINCT FROM c.copies
    ), diffs AS (
        SELECT 1 FROM history_diff UNION ALL SELECT 1 FROM md5_diff UNION ALL SELECT 1 FROM shown_diff UNION ALL SELECT 1 FROM bucket_diff
    )
    SELECT count(*) INTO mismatches FROM diffs;
    IF mismatches > 0 THEN
        RAISE EXCEPTION '049_hidden_run_entries: % rows differ from the baseline manifest', mismatches;
    END IF;
END $$;
-- +migrate StatementEnd

-- +migrate Down
DROP TRIGGER session_entries_transcript_change ON session_entries;
DROP TRIGGER shadow_entries_change ON session_entries;
UPDATE session_entries e SET conversation_id = m.reports_to FROM managed_conversations m
WHERE e.conversation_id = m.conversation_id AND e.audience = 'visible' AND m.reports_to IS NOT NULL;
UPDATE managed_conversations SET producer_inbound_json = jsonb_set(producer_inbound_json::jsonb, '{SyncDestination}', to_jsonb(COALESCE(reports_to, '')))::json
WHERE producer_inbound_json IS NOT NULL AND COALESCE(producer_inbound_json->>'SyncDestination', '') <> COALESCE(reports_to, '');
ALTER TABLE session_entries DROP COLUMN audience;
DROP INDEX managed_conversations_reports_to;
ALTER TABLE managed_conversations DROP COLUMN reports_to;
CREATE INDEX managed_conversations_sync_destination
    ON managed_conversations ((producer_inbound_json->>'SyncDestination'))
    WHERE producer_inbound_json->>'SyncDestination' IS NOT NULL;
DROP TABLE legacy_entry_md5, legacy_history, legacy_listed, legacy_shown, legacy_lane_work, legacy_row_counts, legacy_hidden_run_buckets;

-- +migrate StatementBegin
CREATE OR REPLACE FUNCTION notify_transcript_change() RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE
    channel TEXT := 'rocketclaw_transcript_' || md5(TG_TABLE_SCHEMA);
    revision TEXT := pg_current_xact_id()::text;
    destination TEXT;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        PERFORM pg_notify(channel, json_build_object('conversationId', OLD.conversation_id, 'revision', revision)::text);
        IF TG_TABLE_NAME = 'session_entries' THEN
            FOR destination IN
                SELECT DISTINCT conversation_id FROM session_entries
                WHERE entry_json::jsonb->>'sync_source_entry_id' = OLD.id::text
            LOOP
                PERFORM pg_notify(channel, json_build_object('conversationId', destination, 'revision', revision)::text);
            END LOOP;
        END IF;
        IF TG_TABLE_NAME = 'background_jobs' THEN
            IF OLD.sync_destination <> '' THEN
                PERFORM pg_notify(channel, json_build_object('conversationId', OLD.sync_destination, 'revision', revision)::text);
            END IF;
        END IF;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        PERFORM pg_notify(channel, json_build_object('conversationId', NEW.conversation_id, 'revision', revision)::text);
        IF TG_TABLE_NAME = 'background_jobs' THEN
            IF NEW.sync_destination <> '' THEN
                PERFORM pg_notify(channel, json_build_object('conversationId', NEW.sync_destination, 'revision', revision)::text);
            END IF;
        END IF;
    END IF;
    RETURN NULL;
END;
$$;
-- +migrate StatementEnd

-- +migrate StatementBegin
CREATE OR REPLACE FUNCTION notify_sync_destination_change() RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE
    channel TEXT := 'rocketclaw_transcript_' || md5(TG_TABLE_SCHEMA);
    revision TEXT := pg_current_xact_id()::text;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        IF COALESCE(OLD.inbound_json->>'SyncDestination', '') <> '' THEN
            PERFORM pg_notify(channel, json_build_object('conversationId', OLD.inbound_json->>'SyncDestination', 'revision', revision)::text);
        END IF;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        IF COALESCE(NEW.inbound_json->>'SyncDestination', '') <> '' THEN
            PERFORM pg_notify(channel, json_build_object('conversationId', NEW.inbound_json->>'SyncDestination', 'revision', revision)::text);
        END IF;
    END IF;
    RETURN NULL;
END;
$$;
-- +migrate StatementEnd

-- +migrate StatementBegin
CREATE OR REPLACE FUNCTION notify_shadow_change() RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE
    children TEXT[] := '{}';
    parents TEXT[] := '{}';
    destination TEXT;
    channel TEXT := 'rocketclaw_transcript_' || md5(TG_TABLE_SCHEMA);
    revision TEXT := pg_current_xact_id()::text;
BEGIN
    IF TG_TABLE_NAME = 'shadow_bindings' THEN
        IF TG_OP <> 'INSERT' THEN parents := array_append(parents, OLD.parent_conversation_id); END IF;
        IF TG_OP <> 'DELETE' THEN parents := array_append(parents, NEW.parent_conversation_id); END IF;
    ELSIF TG_TABLE_NAME = 'shadow_turns' THEN
        IF TG_OP <> 'INSERT' THEN children := array_append(children, OLD.child_conversation_id); END IF;
        IF TG_OP <> 'DELETE' THEN children := array_append(children, NEW.child_conversation_id); END IF;
    ELSE
        IF TG_OP <> 'INSERT' THEN children := array_append(children, OLD.conversation_id); END IF;
        IF TG_OP <> 'DELETE' THEN children := array_append(children, NEW.conversation_id); END IF;
    END IF;
    FOR destination IN
        SELECT DISTINCT target.id FROM (
            SELECT b.parent_conversation_id AS id FROM shadow_bindings b
            WHERE EXISTS (SELECT 1 FROM unnest(children) child(id)
                WHERE child.id = b.child_conversation_id OR starts_with(child.id, b.child_conversation_id || '/'))
            UNION SELECT unnest(parents)
        ) parent
        LEFT JOIN managed_conversations m ON m.conversation_id = parent.id
        CROSS JOIN LATERAL (VALUES (parent.id), (m.producer_inbound_json->>'SyncDestination')) target(id)
        WHERE target.id <> ''
    LOOP
        PERFORM pg_notify(channel, json_build_object('conversationId', destination, 'revision', revision)::text);
    END LOOP;
    RETURN NULL;
END;
$$;
-- +migrate StatementEnd

CREATE TRIGGER session_entries_transcript_change AFTER INSERT OR UPDATE OR DELETE ON session_entries FOR EACH ROW EXECUTE FUNCTION notify_transcript_change();
CREATE TRIGGER shadow_entries_change AFTER INSERT OR UPDATE OR DELETE ON session_entries
    FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();
