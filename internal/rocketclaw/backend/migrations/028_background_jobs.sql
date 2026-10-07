-- +migrate Up
CREATE TABLE background_jobs (
    conversation_id TEXT NOT NULL,
    job_id TEXT NOT NULL,
    child_key TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('execute', 'task', 'subagent_wake')),
    status TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed', 'stopped', 'killed')),
    agent TEXT NOT NULL,
    label TEXT NOT NULL,
    call_id TEXT NOT NULL,
    subagent_key TEXT NOT NULL,
    origin_json JSON NOT NULL,
    sync_destination TEXT NOT NULL,
    runner_id TEXT NOT NULL,
    note_state TEXT NOT NULL CHECK (note_state IN ('none', 'pending', 'consumed')),
    wake BOOLEAN NOT NULL,
    wake_attempts INT NOT NULL DEFAULT 0,
    wake_after_unix_ns BIGINT NOT NULL DEFAULT 0,
    claimed_turn_id TEXT NOT NULL,
    created_at_unix_ns BIGINT NOT NULL,
    finished_at_unix_ns BIGINT NOT NULL,
    result TEXT NOT NULL,
    PRIMARY KEY (conversation_id, job_id)
);
CREATE INDEX background_jobs_pending_notes ON background_jobs (conversation_id, child_key) WHERE note_state = 'pending';
CREATE INDEX background_jobs_running ON background_jobs (conversation_id) WHERE status = 'running';
CREATE UNIQUE INDEX background_jobs_running_subagent ON background_jobs (conversation_id, subagent_key) WHERE status = 'running' AND kind <> 'execute';
CREATE INDEX background_jobs_sync_destination ON background_jobs (sync_destination) WHERE sync_destination <> '';

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
            -- Ponytail: this scans saved rows per mutation; index the sync source ID if history size warrants it.
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

CREATE TRIGGER background_jobs_transcript_change AFTER INSERT OR UPDATE OF status, note_state, label OR DELETE ON background_jobs FOR EACH ROW EXECUTE FUNCTION notify_transcript_change();

-- +migrate Down
DROP TABLE background_jobs;

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
