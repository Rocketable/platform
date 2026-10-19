-- +migrate Up
-- The release's last check: every entry keeps its bytes, every conversation's view keeps its entries
-- in order, and Web lists and shows the same chats as 049's manifest recorded. Row counts are not
-- compared: 053 split turn records into item rows, and 049 itself applied pending schedule effects.
-- +migrate StatementBegin
DO $$
DECLARE
    mismatches BIGINT;
BEGIN
    WITH listed AS (
        SELECT conversation_id FROM conversations WHERE listed
    ), listed_diff AS (
        (SELECT * FROM listed EXCEPT SELECT * FROM legacy_listed)
        UNION ALL (SELECT * FROM legacy_listed EXCEPT SELECT * FROM listed)
    ), own AS (
        SELECT e.conversation_id AS viewer, e.id FROM entries e JOIN conversations c USING (conversation_id)
        WHERE e.audience <> CASE WHEN c.producer_inbound_json IS NULL THEN 'sandbox' ELSE 'visible' END
    ), reported AS (
        SELECT c.reports_to AS viewer, e.id FROM conversations c JOIN entries e USING (conversation_id)
        WHERE c.reports_to IS NOT NULL AND e.audience <> 'sandbox'
        UNION ALL SELECT c.conversation_id, e.id FROM conversations c JOIN entries e ON e.conversation_id = c.continues WHERE e.audience <> 'sandbox'
    ), viewed AS (
        SELECT * FROM own UNION ALL SELECT * FROM reported
    ), history AS (
        SELECT viewer AS conversation_id, row_number() OVER (PARTITION BY viewer ORDER BY id) AS position, id AS entry_id FROM viewed
    ), history_diff AS (
        (SELECT * FROM history EXCEPT SELECT * FROM legacy_history)
        UNION ALL (SELECT * FROM legacy_history EXCEPT SELECT * FROM history)
    ), md5s AS (
        SELECT id AS entry_id, md5(entry_json::text) AS md5 FROM entries
    ), md5_diff AS (
        (SELECT * FROM md5s EXCEPT SELECT * FROM legacy_entry_md5)
        UNION ALL (SELECT * FROM legacy_entry_md5 EXCEPT SELECT * FROM md5s)
    ), viewers AS (
        SELECT DISTINCT viewer AS conversation_id FROM viewed
    ), running AS (
        SELECT DISTINCT conversation_id FROM turns WHERE phase NOT IN ('waiting', 'done')
    ), shown AS (
        SELECT l.conversation_id FROM listed l LEFT JOIN viewers v USING (conversation_id) LEFT JOIN running r USING (conversation_id)
        WHERE v.conversation_id IS NOT NULL OR r.conversation_id IS NOT NULL
    ), shown_diff AS (
        (SELECT * FROM shown EXCEPT SELECT * FROM legacy_shown)
        UNION ALL (SELECT * FROM legacy_shown EXCEPT SELECT * FROM shown)
    ), diffs AS (
        SELECT 1 FROM listed_diff UNION ALL SELECT 1 FROM history_diff UNION ALL SELECT 1 FROM md5_diff UNION ALL SELECT 1 FROM shown_diff
    )
    SELECT count(*) INTO mismatches FROM diffs;
    IF mismatches > 0 THEN
        RAISE EXCEPTION '059_legacy_handoff: % rows differ from the baseline manifest', mismatches;
    END IF;
END $$;
-- +migrate StatementEnd

-- No code reads or writes the source tables any more. They keep their rows under a legacy_ name,
-- without the foreign keys that tied them to live rows; their triggers went with their moves.
ALTER TABLE message_search DROP CONSTRAINT message_search_entry_id_fkey, DROP CONSTRAINT message_search_turn_id_fkey;
ALTER TABLE code_mode_calls DROP CONSTRAINT code_mode_calls_turn_id_fkey;
ALTER TABLE shadow_bindings DROP CONSTRAINT shadow_bindings_child_conversation_id_fkey, DROP CONSTRAINT shadow_bindings_parent_conversation_id_fkey;
ALTER TABLE thread_queue RENAME TO legacy_thread_queue;
ALTER TABLE background_jobs RENAME TO legacy_background_jobs;
ALTER TABLE scheduled_messages RENAME TO legacy_scheduled_messages;
ALTER TABLE cron_schedules RENAME TO legacy_cron_schedules;
ALTER TABLE cron_schedule_runs RENAME TO legacy_cron_schedule_runs;
ALTER TABLE session_summaries RENAME TO legacy_session_summaries;
ALTER TABLE session_tags RENAME TO legacy_session_tags;
ALTER TABLE conversation_goals RENAME TO legacy_conversation_goals;
ALTER TABLE external_mcp_sessions RENAME TO legacy_external_mcp_sessions;
ALTER TABLE shadow_bindings RENAME TO legacy_shadow_bindings;
ALTER TABLE opencode_sessions RENAME TO legacy_opencode_sessions;
ALTER TABLE message_search RENAME TO legacy_message_search;
ALTER TABLE message_search_indexed RENAME TO legacy_message_search_indexed;
ALTER TABLE code_mode_calls RENAME TO legacy_code_mode_calls;

-- Background Jobs are turns now, which notify for their own conversation.
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
    FOREACH destination IN ARRAY changed LOOP
        IF destination <> '' THEN
            PERFORM pg_notify(channel, json_build_object('conversationId', destination, 'revision', revision)::text);
        END IF;
    END LOOP;
    FOR destination IN
        WITH owners AS (
            SELECT DISTINCT unnest(changed) AS id
        )
        SELECT DISTINCT m.reports_to FROM owners o JOIN conversations m ON m.conversation_id = o.id WHERE m.reports_to IS NOT NULL
    LOOP
        PERFORM pg_notify(channel, json_build_object('conversationId', destination, 'revision', revision)::text);
    END LOOP;
    RETURN NULL;
END;
$$;
-- +migrate StatementEnd

ANALYZE conversations, turns, entries, steps, schedules;

-- +migrate Down
ALTER TABLE legacy_thread_queue RENAME TO thread_queue;
ALTER TABLE legacy_background_jobs RENAME TO background_jobs;
ALTER TABLE legacy_scheduled_messages RENAME TO scheduled_messages;
ALTER TABLE legacy_cron_schedules RENAME TO cron_schedules;
ALTER TABLE legacy_cron_schedule_runs RENAME TO cron_schedule_runs;
ALTER TABLE legacy_session_summaries RENAME TO session_summaries;
ALTER TABLE legacy_session_tags RENAME TO session_tags;
ALTER TABLE legacy_conversation_goals RENAME TO conversation_goals;
ALTER TABLE legacy_external_mcp_sessions RENAME TO external_mcp_sessions;
ALTER TABLE legacy_shadow_bindings RENAME TO shadow_bindings;
ALTER TABLE legacy_opencode_sessions RENAME TO opencode_sessions;
ALTER TABLE legacy_message_search RENAME TO message_search;
ALTER TABLE legacy_message_search_indexed RENAME TO message_search_indexed;
ALTER TABLE legacy_code_mode_calls RENAME TO code_mode_calls;

-- Rows whose entry or turn went while unbound go as their foreign keys' cascades would have taken them.
WITH orphans AS (
    SELECT m.ctid AS row FROM message_search m LEFT JOIN entries e ON e.id = m.entry_id LEFT JOIN turns t ON t.id = m.turn_id
    WHERE m.entry_id IS NOT NULL AND e.id IS NULL OR m.turn_id IS NOT NULL AND t.id IS NULL
)
DELETE FROM message_search m USING orphans o WHERE m.ctid = o.row;
WITH orphans AS (
    SELECT c.id FROM code_mode_calls c LEFT JOIN turns t ON t.id = c.turn_id WHERE t.id IS NULL
)
DELETE FROM code_mode_calls c USING orphans o WHERE c.id = o.id;
ALTER TABLE message_search ADD CONSTRAINT message_search_entry_id_fkey FOREIGN KEY (entry_id) REFERENCES entries (id) ON DELETE CASCADE,
    ADD CONSTRAINT message_search_turn_id_fkey FOREIGN KEY (turn_id) REFERENCES turns (id) ON DELETE CASCADE;
ALTER TABLE code_mode_calls ADD CONSTRAINT code_mode_calls_turn_id_fkey FOREIGN KEY (turn_id) REFERENCES turns (id) ON DELETE CASCADE;
ALTER TABLE shadow_bindings ADD CONSTRAINT shadow_bindings_child_conversation_id_fkey FOREIGN KEY (child_conversation_id) REFERENCES conversations (conversation_id) ON DELETE CASCADE,
    ADD CONSTRAINT shadow_bindings_parent_conversation_id_fkey FOREIGN KEY (parent_conversation_id) REFERENCES conversations (conversation_id) ON DELETE CASCADE;

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
        SELECT DISTINCT m.reports_to FROM owners o JOIN conversations m ON m.conversation_id = o.id WHERE m.reports_to IS NOT NULL
    LOOP
        PERFORM pg_notify(channel, json_build_object('conversationId', destination, 'revision', revision)::text);
    END LOOP;
    RETURN NULL;
END;
$$;
-- +migrate StatementEnd
