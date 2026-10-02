-- +migrate Up
ALTER TABLE active_turns ADD COLUMN replay_attribution_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE active_turns ADD COLUMN reasoning_effort_json TEXT NOT NULL DEFAULT 'null';
ALTER TABLE active_turns ADD COLUMN terminal TEXT NOT NULL DEFAULT '';
ALTER TABLE active_turns ADD COLUMN history_anchor_id BIGINT NOT NULL DEFAULT 0;
UPDATE active_turns a SET history_anchor_id = COALESCE((SELECT MAX(id) FROM session_entries WHERE conversation_id = a.conversation_id), 0);

-- +migrate StatementBegin
CREATE FUNCTION notify_transcript_change() RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
DECLARE
    channel TEXT := 'rocketclaw_transcript_' || md5(TG_TABLE_SCHEMA);
    revision TEXT := pg_current_xact_id()::text;
    destination TEXT;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        PERFORM pg_notify(channel, json_build_object('conversationId', OLD.conversation_id, 'revision', revision)::text);
        IF TG_TABLE_NAME = 'session_entries' THEN
            -- Ponytail: this scans saved rows per mutation; index the sync source ID if history size warrants it.
            FOR destination IN
                SELECT DISTINCT conversation_id FROM session_entries
                WHERE entry_json::jsonb->>'sync_source_entry_id' = OLD.id::text
            LOOP
                PERFORM pg_notify(channel, json_build_object('conversationId', destination, 'revision', revision)::text);
            END LOOP;
        END IF;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        PERFORM pg_notify(channel, json_build_object('conversationId', NEW.conversation_id, 'revision', revision)::text);
    END IF;
    RETURN NULL;
END;
$$;
-- +migrate StatementEnd

CREATE TRIGGER session_entries_transcript_change AFTER INSERT OR UPDATE OR DELETE ON session_entries FOR EACH ROW EXECUTE FUNCTION notify_transcript_change();
CREATE TRIGGER active_turns_transcript_change AFTER INSERT OR UPDATE OF conversation_id, agent, display_model, reasoning_effort_json, replay_input_json, replay_attribution_json, output_trace_json, token_usage_json, response_id, terminal, created_at_unix_ns, history_anchor_id OR DELETE ON active_turns FOR EACH ROW EXECUTE FUNCTION notify_transcript_change();

-- +migrate Down
DROP TRIGGER active_turns_transcript_change ON active_turns;
DROP TRIGGER session_entries_transcript_change ON session_entries;
DROP FUNCTION notify_transcript_change();
ALTER TABLE active_turns DROP COLUMN history_anchor_id;
ALTER TABLE active_turns DROP COLUMN terminal;
ALTER TABLE active_turns DROP COLUMN reasoning_effort_json;
ALTER TABLE active_turns DROP COLUMN replay_attribution_json;
