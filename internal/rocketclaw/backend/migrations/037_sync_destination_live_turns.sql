-- +migrate Up
-- A hidden run's live turn shows in the chat it syncs to, so that chat hears its changes.
-- +migrate StatementBegin
CREATE FUNCTION notify_sync_destination_change() RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
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
CREATE TRIGGER active_turns_sync_destination_change AFTER INSERT OR UPDATE OF output_trace_json, terminal, phase OR DELETE ON active_turns
    FOR EACH ROW EXECUTE FUNCTION notify_sync_destination_change();

-- +migrate Down
DROP FUNCTION notify_sync_destination_change() CASCADE;
