-- +migrate Up
-- The State Store as 001_init.sql through 059_legacy_handoff.sql built it, without the legacy_ tables.
-- Startup adopts a store those migrations built; see initializeSessionDB.
-- Schemas sharing one database must not race to create the extension.
SELECT pg_advisory_xact_lock(hashtext('rocketclaw pg_trgm extension'));
CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;

-- +migrate StatementBegin
CREATE FUNCTION notify_shadow_change() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path FROM CURRENT
    AS $$
DECLARE
    children TEXT[] := '{}';
    parents TEXT[] := '{}';
    destination TEXT;
    channel TEXT := 'rocketclaw_transcript_' || md5(TG_TABLE_SCHEMA);
    revision TEXT := pg_current_xact_id()::text;
BEGIN
    -- Read only ownership keys; transcript payloads can be large.
    IF TG_TABLE_NAME = 'conversations' THEN
        IF TG_OP <> 'INSERT' THEN parents := array_append(parents, OLD.shadow_parent); END IF;
        IF TG_OP <> 'DELETE' THEN parents := array_append(parents, NEW.shadow_parent); END IF;
    ELSIF TG_TABLE_NAME = 'shadow_turns' THEN
        IF TG_OP <> 'INSERT' THEN children := array_append(children, OLD.child_conversation_id); END IF;
        IF TG_OP <> 'DELETE' THEN children := array_append(children, NEW.child_conversation_id); END IF;
    ELSE
        IF TG_OP <> 'INSERT' THEN children := array_append(children, OLD.conversation_id); END IF;
        IF TG_OP <> 'DELETE' THEN children := array_append(children, NEW.conversation_id); END IF;
    END IF;
    FOR destination IN
        WITH RECURSIVE changed AS (
            SELECT unnest(children) AS id
            UNION SELECT c.parent_conversation_id FROM changed ch JOIN conversations c ON c.conversation_id = ch.id WHERE c.parent_conversation_id IS NOT NULL
        ), shadows AS (
            SELECT conversation_id, shadow_parent FROM conversations WHERE shadow_parent IS NOT NULL
        ), bound_parents AS (
            SELECT DISTINCT b.shadow_parent AS id FROM shadows b JOIN changed c ON c.id = b.conversation_id
            UNION SELECT unnest(parents)
        ), targets AS (
            SELECT id FROM bound_parents UNION SELECT m.reports_to FROM bound_parents p JOIN conversations m ON m.conversation_id = p.id
        )
        SELECT id FROM targets WHERE COALESCE(id, '') <> ''
    LOOP
        PERFORM pg_notify(channel, json_build_object('conversationId', destination, 'revision', revision)::text);
    END LOOP;
    RETURN NULL;
END;
$$;
-- +migrate StatementEnd

-- +migrate StatementBegin
CREATE FUNCTION notify_sync_destination_change() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path FROM CURRENT
    AS $$
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
        SELECT DISTINCT m.reports_to FROM owners o JOIN conversations m ON m.conversation_id = o.id WHERE m.reports_to IS NOT NULL
    LOOP
        PERFORM pg_notify(channel, json_build_object('conversationId', destination, 'revision', revision)::text);
    END LOOP;
    RETURN NULL;
END;
$$;
-- +migrate StatementEnd

-- +migrate StatementBegin
CREATE FUNCTION notify_transcript_change() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path FROM CURRENT
    AS $$
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

-- +migrate StatementBegin
CREATE FUNCTION readable_replay(bigint) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE
    AS $_$
BEGIN
    IF $1 > 268435455 THEN
        RAISE EXCEPTION 'string too long to represent as jsonb string' USING ERRCODE = 'program_limit_exceeded',
            DETAIL = 'Due to an implementation restriction, jsonb strings cannot exceed 268435455 bytes.';
    END IF;
    RETURN TRUE;
END $_$;
-- +migrate StatementEnd

-- +migrate StatementBegin
CREATE FUNCTION readable_replay(json) RETURNS json
    LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE
    AS $_$
BEGIN
    PERFORM readable_replay(octet_length($1::text)::bigint);
    RETURN $1;
END $_$;
-- +migrate StatementEnd

-- +migrate StatementBegin
CREATE FUNCTION utf8_text(bytea) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
    AS $_$ SELECT convert_from($1, 'UTF8') $_$;
-- +migrate StatementEnd

CREATE TABLE attachments (
    id text NOT NULL,
    conversation_id text NOT NULL,
    name text NOT NULL,
    mime_type text NOT NULL,
    size bigint NOT NULL,
    original_unverified boolean NOT NULL,
    upload boolean NOT NULL,
    turn_id text DEFAULT ''::text NOT NULL,
    seq bigint NOT NULL
);
ALTER TABLE attachments ALTER COLUMN seq ADD GENERATED BY DEFAULT AS IDENTITY (
    SEQUENCE NAME attachments_seq_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

CREATE TABLE conversations (
    conversation_id text CONSTRAINT managed_conversations_conversation_id_not_null NOT NULL,
    agent text CONSTRAINT managed_conversations_agent_not_null NOT NULL,
    created_by text CONSTRAINT managed_conversations_created_by_not_null NOT NULL,
    settled boolean DEFAULT false CONSTRAINT managed_conversations_settled_not_null NOT NULL,
    reopened_at timestamp with time zone DEFAULT '0001-01-01 00:00:00+00'::timestamp with time zone CONSTRAINT managed_conversations_reopened_at_not_null NOT NULL,
    pinned boolean DEFAULT false CONSTRAINT managed_conversations_pinned_not_null NOT NULL,
    name text DEFAULT ''::text CONSTRAINT managed_conversations_name_not_null NOT NULL,
    forked_from text DEFAULT ''::text CONSTRAINT managed_conversations_forked_from_not_null NOT NULL,
    snoozed_until timestamp with time zone,
    revert_message_id text DEFAULT ''::text CONSTRAINT managed_conversations_revert_message_id_not_null NOT NULL,
    producer_inbound_json json,
    producer_effects_through_id bigint DEFAULT 0 CONSTRAINT managed_conversations_producer_effects_through_id_not_null NOT NULL,
    spawned_by text DEFAULT ''::text CONSTRAINT managed_conversations_spawned_by_not_null NOT NULL,
    parent_conversation_id text,
    created_at timestamp with time zone DEFAULT now() CONSTRAINT managed_conversations_created_at_not_null NOT NULL,
    space_id text,
    reports_to text,
    continues text,
    kind text NOT NULL,
    listed boolean NOT NULL,
    slack_channel text,
    slack_thread text,
    cron_path text,
    cron_run_at_unix_ns bigint,
    cron_run text,
    retention text DEFAULT 'record'::text NOT NULL,
    call_turn text,
    call_id text,
    preview bytea,
    last_updated timestamp with time zone,
    tags jsonb,
    goal_objective text,
    goal_check_script text,
    goal_max_turns integer,
    goal_turns_used integer,
    goal_status text,
    goal_note text,
    goal_created_at_unix_ns bigint,
    goal_updated_at_unix_ns bigint,
    goal_principal_name text,
    goal_principal_email text,
    external_id text,
    external_run text,
    external_agent text,
    external_channel text,
    origin_pairs jsonb,
    shadow_parent text,
    parent_agent text,
    shadow_agent text,
    server text,
    directory text,
    title text,
    running boolean DEFAULT false NOT NULL,
    synced_at timestamp with time zone,
    reply text,
    background boolean DEFAULT false NOT NULL,
    model text,
    remote_session text,
    has_entries boolean DEFAULT false NOT NULL,
    first_source text,
    search_words text GENERATED ALWAYS AS (lower((replace(replace((
CASE
    WHEN (listed OR (server IS NOT NULL)) THEN ((((((((((((((((((((((((name || ' '::text) || COALESCE(utf8_text(preview), ''::text)) || ' '::text) || agent) || ' slack '::text) || COALESCE(slack_channel, ''::text)) || ' '::text) || conversation_id) || ' '::text) || COALESCE(external_id, ''::text)) || ' '::text) || COALESCE(external_agent, ''::text)) || ' '::text) || COALESCE((origin_pairs)::text, 'null'::text)) || ' '::text) || COALESCE(cron_run, ''::text)) || ' '::text) || COALESCE(first_source, ''::text)) || ' '::text) || COALESCE(title, ''::text)) || ' '::text) || COALESCE(server, ''::text)) || ' '::text) || COALESCE(directory, ''::text))
    ELSE ''::text
END ||
CASE
    WHEN (cron_run_at_unix_ns IS NOT NULL) THEN (((((((((((((((((' '::text || agent) || ' '::text) || COALESCE(cron_path, ''::text)) || ' '::text) || lpad((EXTRACT(year FROM timezone('UTC'::text, to_timestamp(((cron_run_at_unix_ns / 1000000000))::double precision))))::text, 4, '0'::text)) || '-'::text) || lpad((EXTRACT(month FROM timezone('UTC'::text, to_timestamp(((cron_run_at_unix_ns / 1000000000))::double precision))))::text, 2, '0'::text)) || '-'::text) || lpad((EXTRACT(day FROM timezone('UTC'::text, to_timestamp(((cron_run_at_unix_ns / 1000000000))::double precision))))::text, 2, '0'::text)) || 't'::text) || lpad((EXTRACT(hour FROM timezone('UTC'::text, to_timestamp(((cron_run_at_unix_ns / 1000000000))::double precision))))::text, 2, '0'::text)) || ':'::text) || lpad((EXTRACT(minute FROM timezone('UTC'::text, to_timestamp(((cron_run_at_unix_ns / 1000000000))::double precision))))::text, 2, '0'::text)) || ':'::text) || lpad((floor(EXTRACT(second FROM timezone('UTC'::text, to_timestamp(((cron_run_at_unix_ns / 1000000000))::double precision)))))::text, 2, '0'::text)) || COALESCE(('.'::text || NULLIF(rtrim(lpad((mod(cron_run_at_unix_ns, (1000000000)::bigint))::text, 9, '0'::text), '0'::text), ''::text)), ''::text)) || 'z'::text)
    ELSE ''::text
END), 'İ'::text, 'i'::text), 'K'::text, 'k'::text) COLLATE "C"))) STORED,
    principals text[] DEFAULT '{}'::text[] NOT NULL,
    CONSTRAINT conversations_call_check CHECK ((((parent_conversation_id IS NULL) = (call_id IS NULL)) AND ((call_turn IS NULL) OR (call_id IS NOT NULL)))),
    CONSTRAINT conversations_kind_check CHECK ((kind = ANY (ARRAY['web_chat'::text, 'slack_thread'::text, 'cron_run'::text, 'one_off_cron_run'::text, 'external_mcp_run'::text, 'subagent'::text, 'review'::text, 'shadow'::text, 'remote'::text]))),
    CONSTRAINT conversations_remote_session_check CHECK (((kind = 'remote'::text) = (remote_session IS NOT NULL))),
    CONSTRAINT conversations_retention_check CHECK ((retention = ANY (ARRAY['record'::text, 'private'::text, 'nested'::text, 'remote'::text])))
);

CREATE TABLE entries (
    id bigint CONSTRAINT session_entries_id_not_null NOT NULL,
    conversation_id text CONSTRAINT session_entries_conversation_id_not_null NOT NULL,
    entry_json json CONSTRAINT session_entries_entry_json_not_null NOT NULL,
    entry_timestamp text CONSTRAINT session_entries_entry_timestamp_not_null NOT NULL,
    audience text DEFAULT 'both'::text CONSTRAINT session_entries_audience_not_null NOT NULL,
    search_text text,
    CONSTRAINT session_entries_audience_check CHECK ((audience = ANY (ARRAY['both'::text, 'sandbox'::text, 'visible'::text]))),
    CONSTRAINT session_entry_unicode CHECK (((entry_json)::jsonb IS NOT NULL))
);

CREATE TABLE hosted_pages (
    agent text NOT NULL,
    kind text NOT NULL,
    name text NOT NULL,
    content_type text NOT NULL,
    body bytea NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE pending_attachment_deletions (
    id text NOT NULL
);

CREATE TABLE schedules (
    id text NOT NULL,
    cron_path text,
    conversation_id text,
    agent text,
    message text,
    interval_ns bigint,
    next_due_unix_ns bigint NOT NULL,
    CONSTRAINT schedules_check CHECK (((cron_path IS NULL) = ((conversation_id IS NOT NULL) AND (agent IS NOT NULL) AND (message IS NOT NULL) AND (interval_ns IS NOT NULL))))
);
ALTER TABLE entries ALTER COLUMN id ADD GENERATED BY DEFAULT AS IDENTITY (
    SEQUENCE NAME session_entries_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

CREATE TABLE shadow_steps (
    shadow_turn_id bigint NOT NULL,
    child boolean NOT NULL,
    response_id text NOT NULL,
    segment integer NOT NULL
);

CREATE TABLE shadow_turns (
    id bigint NOT NULL,
    child_conversation_id text NOT NULL,
    parent_source_conversation_id text NOT NULL,
    parent_turn_id text NOT NULL,
    child_turn_id text DEFAULT ''::text NOT NULL,
    inbound_json json NOT NULL,
    inputs jsonb DEFAULT '[]'::jsonb NOT NULL,
    context_entries jsonb DEFAULT '[]'::jsonb NOT NULL,
    environment jsonb DEFAULT '{}'::jsonb NOT NULL,
    input_closed boolean DEFAULT false NOT NULL,
    terminal text DEFAULT ''::text NOT NULL,
    limit_note text DEFAULT ''::text NOT NULL
);
ALTER TABLE shadow_turns ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME shadow_turns_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

CREATE TABLE slack_channel_facts (
    workspace_id text NOT NULL,
    channel_id text NOT NULL,
    name text NOT NULL,
    observed_at bigint NOT NULL
);

CREATE TABLE steps (
    conversation_id text NOT NULL,
    key text NOT NULL,
    id bigint NOT NULL,
    value json NOT NULL
);
ALTER TABLE steps ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME steps_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

CREATE TABLE turns (
    id text DEFAULT ('waiting-'::text || gen_random_uuid()) CONSTRAINT active_turns_id_not_null NOT NULL,
    conversation_id text CONSTRAINT active_turns_conversation_id_not_null NOT NULL,
    output_trace_json text DEFAULT '[]'::text CONSTRAINT active_turns_output_trace_json_not_null NOT NULL,
    created_at_unix_ns bigint CONSTRAINT active_turns_created_at_unix_ns_not_null NOT NULL,
    updated_at_unix_ns bigint CONSTRAINT active_turns_updated_at_unix_ns_not_null NOT NULL,
    terminal text DEFAULT ''::text CONSTRAINT active_turns_terminal_not_null NOT NULL,
    history_anchor_id bigint DEFAULT 0 CONSTRAINT active_turns_history_anchor_id_not_null NOT NULL,
    inbound_json json,
    phase text DEFAULT 'running'::text CONSTRAINT active_turns_phase_not_null NOT NULL,
    outbound_json json,
    replay json,
    record json,
    applied_input_ids jsonb,
    search_text text,
    reason text,
    delivery text,
    input_id text,
    "position" integer,
    park_after text,
    message text,
    principal text,
    source text,
    content jsonb,
    slack_reply jsonb,
    slack_channel text,
    slack_ts text,
    takes_lane boolean DEFAULT true NOT NULL,
    kind text DEFAULT 'model'::text NOT NULL,
    agent text,
    label text,
    call_id text,
    subagent_key text,
    runner_id text,
    note text,
    wake boolean,
    wake_attempts integer,
    run_at_unix_ns bigint,
    claimed_by text,
    result text,
    CONSTRAINT turns_kind_check CHECK ((kind = ANY (ARRAY['model'::text, 'script'::text, 'note'::text]))),
    CONSTRAINT turns_note_check CHECK (((takes_lane = (kind = 'model'::text)) AND (takes_lane = (note IS NULL)) AND (note = ANY (ARRAY['none'::text, 'pending'::text, 'consumed'::text])))),
    CONSTRAINT turns_phase_check CHECK ((phase = ANY (ARRAY['waiting'::text, 'running'::text, 'delivering'::text, 'done'::text]))),
    CONSTRAINT turns_reason_check CHECK ((((phase = 'waiting'::text) = (reason IS NOT NULL)) AND (reason = ANY (ARRAY['queued'::text, 'stashed'::text, 'held'::text, 'scheduled'::text])) AND ((reason = ANY (ARRAY['queued'::text, 'scheduled'::text])) = (delivery <> 'held'::text))))
);

CREATE TABLE voice_settings (
    username text NOT NULL,
    conversation_id text,
    agent text DEFAULT ''::text NOT NULL,
    voice text DEFAULT ''::text NOT NULL,
    transcript boolean DEFAULT false NOT NULL
);

CREATE TABLE web_spaces (
    id text DEFAULT (gen_random_uuid())::text NOT NULL,
    name text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE web_tab_states (
    username text NOT NULL,
    state bytea NOT NULL,
    revision bigint NOT NULL
);

-- A SQL function's body is checked when it is created, so this one follows the tables it reads.
-- +migrate StatementBegin
CREATE FUNCTION turn_trace(text, text) RETURNS jsonb
    LANGUAGE sql STABLE
    SET search_path FROM CURRENT
    AS $_$
    WITH turn AS (
        SELECT conversation_id FROM turns WHERE id = $1
    )
    SELECT $2::jsonb || COALESCE(jsonb_agg(s.value::jsonb ORDER BY s.id), '[]'::jsonb) FROM turn t
    JOIN steps s ON s.conversation_id = t.conversation_id AND starts_with(s.key, $1 || '/code_mode/')
$_$;
-- +migrate StatementEnd

ALTER TABLE ONLY attachments
    ADD CONSTRAINT attachments_pkey PRIMARY KEY (id);
ALTER TABLE ONLY conversations
    ADD CONSTRAINT conversations_external_id_key UNIQUE (external_id);
ALTER TABLE ONLY conversations
    ADD CONSTRAINT conversations_external_run_key UNIQUE (external_run);
ALTER TABLE ONLY hosted_pages
    ADD CONSTRAINT hosted_pages_pkey PRIMARY KEY (agent, kind, name);
ALTER TABLE ONLY conversations
    ADD CONSTRAINT managed_conversations_pkey PRIMARY KEY (conversation_id);
ALTER TABLE ONLY pending_attachment_deletions
    ADD CONSTRAINT pending_attachment_deletions_pkey PRIMARY KEY (id);
ALTER TABLE ONLY schedules
    ADD CONSTRAINT schedules_pkey PRIMARY KEY (id);
ALTER TABLE ONLY entries
    ADD CONSTRAINT session_entries_pkey PRIMARY KEY (id);
ALTER TABLE ONLY shadow_steps
    ADD CONSTRAINT shadow_steps_pkey PRIMARY KEY (shadow_turn_id, child, response_id);
ALTER TABLE ONLY shadow_turns
    ADD CONSTRAINT shadow_turns_child_conversation_id_parent_source_conversati_key UNIQUE (child_conversation_id, parent_source_conversation_id, parent_turn_id);
ALTER TABLE ONLY shadow_turns
    ADD CONSTRAINT shadow_turns_pkey PRIMARY KEY (id);
ALTER TABLE ONLY slack_channel_facts
    ADD CONSTRAINT slack_channel_facts_pkey PRIMARY KEY (workspace_id, channel_id);
ALTER TABLE ONLY steps
    ADD CONSTRAINT steps_pkey PRIMARY KEY (conversation_id, key);
ALTER TABLE ONLY turns
    ADD CONSTRAINT turns_pkey PRIMARY KEY (id);
ALTER TABLE ONLY voice_settings
    ADD CONSTRAINT voice_settings_pkey PRIMARY KEY (username);
ALTER TABLE ONLY web_spaces
    ADD CONSTRAINT web_spaces_pkey PRIMARY KEY (id);
ALTER TABLE ONLY web_tab_states
    ADD CONSTRAINT web_tab_states_pkey PRIMARY KEY (username);

CREATE INDEX conversations_continues ON conversations USING btree (continues) WHERE (continues IS NOT NULL);
CREATE INDEX conversations_cron_runs ON conversations USING btree (conversation_id) INCLUDE (agent, cron_path, cron_run_at_unix_ns, kind) WHERE (cron_run_at_unix_ns IS NOT NULL);
CREATE INDEX conversations_listed ON conversations USING btree (conversation_id) WHERE listed;
CREATE INDEX conversations_parent ON conversations USING btree (parent_conversation_id) WHERE (parent_conversation_id IS NOT NULL);
CREATE INDEX conversations_search_words ON conversations USING gin (search_words public.gin_trgm_ops);
CREATE INDEX conversations_server ON conversations USING btree (server) WHERE (server IS NOT NULL);
CREATE UNIQUE INDEX conversations_shadow ON conversations USING btree (shadow_parent, parent_agent, shadow_agent) WHERE (shadow_parent IS NOT NULL);
CREATE INDEX conversations_slack_channels ON conversations USING btree (slack_channel) WHERE (listed AND (slack_channel IS NOT NULL));
CREATE INDEX entries_search_text ON entries USING gin (search_text public.gin_trgm_ops);
CREATE INDEX managed_conversations_reports_to ON conversations USING btree (reports_to) WHERE (reports_to IS NOT NULL);
CREATE INDEX managed_conversations_reverted ON conversations USING btree (conversation_id) WHERE (revert_message_id <> ''::text);
CREATE INDEX session_entries_conversation_id_c ON entries USING btree (conversation_id COLLATE "C");
CREATE INDEX session_entries_conversation_id_id ON entries USING btree (conversation_id, id);
CREATE INDEX session_entries_conversation_id_timestamp ON entries USING btree (conversation_id, entry_timestamp);
CREATE INDEX session_entries_mcp_metadata ON entries USING btree (conversation_id, id DESC) WHERE (((entry_json)::jsonb ->> 'type'::text) = 'mcp_external_metadata'::text);
CREATE INDEX session_entries_sync_source_id ON entries USING btree ((((entry_json)::jsonb ->> 'sync_source_entry_id'::text))) WHERE (((entry_json)::jsonb ->> 'sync_source_entry_id'::text) IS NOT NULL);
CREATE UNIQUE INDEX shadow_turns_child_turn ON shadow_turns USING btree (child_turn_id) WHERE (child_turn_id <> ''::text);
CREATE INDEX shadow_turns_parent ON shadow_turns USING btree (parent_source_conversation_id, parent_turn_id);
CREATE INDEX shadow_turns_pending ON shadow_turns USING btree (child_conversation_id, id) WHERE ((child_turn_id = ''::text) AND (terminal = ''::text));
CREATE INDEX turns_conversation_updated ON turns USING btree (conversation_id, updated_at_unix_ns);
CREATE UNIQUE INDEX turns_job ON turns USING btree (conversation_id, input_id) WHERE (NOT takes_lane);
CREATE INDEX turns_pending_notes ON turns USING btree (conversation_id) WHERE (note = 'pending'::text);
CREATE INDEX turns_search_text ON turns USING gin (search_text public.gin_trgm_ops);
CREATE UNIQUE INDEX turns_waiting_input ON turns USING btree (input_id) WHERE (phase = 'waiting'::text);
CREATE UNIQUE INDEX web_spaces_name ON web_spaces USING btree (lower(name));

CREATE TRIGGER conversations_shadow_change AFTER INSERT OR DELETE OR UPDATE OF shadow_parent, parent_agent, shadow_agent ON conversations FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();
CREATE TRIGGER entries_transcript_change AFTER INSERT OR DELETE OR UPDATE ON entries FOR EACH ROW EXECUTE FUNCTION notify_transcript_change();
CREATE TRIGGER managed_conversations_revert_change AFTER UPDATE OF revert_message_id ON conversations FOR EACH ROW WHEN ((old.revert_message_id IS DISTINCT FROM new.revert_message_id)) EXECUTE FUNCTION notify_transcript_change();
CREATE TRIGGER shadow_entries_change AFTER INSERT OR DELETE OR UPDATE ON entries FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();
CREATE TRIGGER shadow_steps_change AFTER INSERT OR DELETE OR UPDATE ON steps FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();
CREATE TRIGGER shadow_turns_change AFTER INSERT OR DELETE OR UPDATE OF child_turn_id, input_closed, terminal ON shadow_turns FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();
CREATE TRIGGER shadow_turns_row_change AFTER INSERT OR UPDATE OF output_trace_json, terminal, phase, replay, record ON turns FOR EACH ROW WHEN (((new.phase <> 'waiting'::text) AND new.takes_lane)) EXECUTE FUNCTION notify_shadow_change();
CREATE TRIGGER shadow_turns_row_delete AFTER DELETE ON turns FOR EACH ROW WHEN (((old.phase <> 'waiting'::text) AND old.takes_lane)) EXECUTE FUNCTION notify_shadow_change();
CREATE TRIGGER steps_transcript_change AFTER INSERT OR UPDATE ON steps FOR EACH ROW WHEN ((strpos(new.key, '/'::text) = 0)) EXECUTE FUNCTION notify_transcript_change();
CREATE TRIGGER turns_sync_destination_change AFTER INSERT OR UPDATE OF output_trace_json, terminal, phase ON turns FOR EACH ROW WHEN (((new.phase <> 'waiting'::text) AND new.takes_lane)) EXECUTE FUNCTION notify_sync_destination_change();
CREATE TRIGGER turns_sync_destination_delete AFTER DELETE ON turns FOR EACH ROW WHEN (((old.phase <> 'waiting'::text) AND old.takes_lane)) EXECUTE FUNCTION notify_sync_destination_change();
CREATE TRIGGER turns_transcript_change AFTER INSERT OR UPDATE OF conversation_id, output_trace_json, terminal, phase, created_at_unix_ns, history_anchor_id, replay, record, note, label ON turns FOR EACH ROW WHEN ((new.phase <> 'waiting'::text)) EXECUTE FUNCTION notify_transcript_change();
CREATE TRIGGER turns_transcript_delete AFTER DELETE ON turns FOR EACH ROW WHEN ((old.phase <> 'waiting'::text)) EXECUTE FUNCTION notify_transcript_change();
ALTER TABLE ONLY conversations
    ADD CONSTRAINT conversations_parent_fkey FOREIGN KEY (parent_conversation_id) REFERENCES conversations(conversation_id) ON DELETE RESTRICT;
ALTER TABLE ONLY conversations
    ADD CONSTRAINT conversations_reports_to_fkey FOREIGN KEY (reports_to) REFERENCES conversations(conversation_id) ON DELETE SET NULL;
ALTER TABLE ONLY conversations
    ADD CONSTRAINT conversations_shadow_parent_fkey FOREIGN KEY (shadow_parent) REFERENCES conversations(conversation_id) ON DELETE SET NULL;
ALTER TABLE ONLY conversations
    ADD CONSTRAINT managed_conversations_space_id_fkey FOREIGN KEY (space_id) REFERENCES web_spaces(id) ON DELETE SET NULL;
ALTER TABLE ONLY shadow_steps
    ADD CONSTRAINT shadow_steps_shadow_turn_id_fkey FOREIGN KEY (shadow_turn_id) REFERENCES shadow_turns(id) ON DELETE CASCADE;
ALTER TABLE ONLY shadow_turns
    ADD CONSTRAINT shadow_turns_child_conversation_id_fkey FOREIGN KEY (child_conversation_id) REFERENCES conversations(conversation_id) ON DELETE CASCADE;
ALTER TABLE ONLY voice_settings
    ADD CONSTRAINT voice_settings_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES conversations(conversation_id) ON DELETE SET NULL;

INSERT INTO web_spaces (id, name) VALUES ('main', 'Main');

-- 059 ended with these statistics, so a new store plans its first queries as a migrated one did.
ANALYZE conversations, turns, entries, steps, schedules;
