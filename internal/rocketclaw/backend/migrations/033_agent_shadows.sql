-- +migrate Up
CREATE TABLE shadow_bindings (
    child_conversation_id TEXT PRIMARY KEY REFERENCES managed_conversations (conversation_id) ON DELETE CASCADE,
    parent_conversation_id TEXT NOT NULL REFERENCES managed_conversations (conversation_id) ON DELETE CASCADE,
    parent_agent TEXT NOT NULL,
    shadow_agent TEXT NOT NULL,
    UNIQUE (parent_conversation_id, parent_agent, shadow_agent)
);
CREATE TABLE shadow_turns (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    child_conversation_id TEXT NOT NULL REFERENCES shadow_bindings (child_conversation_id) ON DELETE CASCADE,
    parent_source_conversation_id TEXT NOT NULL,
    parent_turn_id TEXT NOT NULL,
    child_turn_id TEXT NOT NULL DEFAULT '',
    inbound_json JSON NOT NULL,
    inputs JSONB NOT NULL DEFAULT '[]',
    context_entries JSONB NOT NULL DEFAULT '[]',
    environment JSONB NOT NULL DEFAULT '{}',
    input_closed BOOLEAN NOT NULL DEFAULT FALSE,
    terminal TEXT NOT NULL DEFAULT '',
    UNIQUE (child_conversation_id, parent_source_conversation_id, parent_turn_id)
);
CREATE INDEX shadow_turns_pending ON shadow_turns (child_conversation_id, id) WHERE child_turn_id = '' AND terminal = '';
CREATE INDEX shadow_turns_parent ON shadow_turns (parent_source_conversation_id, parent_turn_id);
CREATE UNIQUE INDEX shadow_turns_child_turn ON shadow_turns (child_turn_id) WHERE child_turn_id <> '';

-- SQL ownership and blob storage cannot commit together. Keep deletion ownership
-- until the original bytes are gone, including across process restarts.
CREATE TABLE pending_attachment_deletions (id TEXT PRIMARY KEY);

-- Keep comparison live through the parent's existing stream. A delegation's
-- path is scoped to an explicitly bound child, not an arbitrary slash-containing ID.
-- +migrate StatementBegin
CREATE FUNCTION notify_shadow_change() RETURNS trigger LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
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
CREATE TRIGGER shadow_bindings_change AFTER INSERT OR UPDATE OR DELETE ON shadow_bindings
    FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();
CREATE TRIGGER shadow_turns_change AFTER INSERT OR UPDATE OF child_turn_id, input_closed, terminal OR DELETE ON shadow_turns
    FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();
CREATE TRIGGER shadow_entries_change AFTER INSERT OR UPDATE OR DELETE ON session_entries
    FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();
CREATE TRIGGER shadow_active_change AFTER INSERT OR UPDATE OF output_trace_json, terminal, phase OR DELETE ON active_turns
    FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();
CREATE TRIGGER shadow_steps_change AFTER INSERT OR UPDATE OR DELETE ON turn_steps
    FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();

-- +migrate Down
DROP TABLE pending_attachment_deletions;
DROP FUNCTION notify_shadow_change() CASCADE;
DROP TABLE shadow_turns;
DROP TABLE shadow_bindings;
