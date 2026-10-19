-- +migrate Up
-- Each fact Web or an agent reads about a conversation lives on its row, written with the change
-- that causes it: the session list's preview and time, tags, the goal, an External MCP session's
-- binding (on its Slack thread, with the run it drives), a shadow's parent and agents, and the
-- remote OpenCode sessions Web lists, as rows of their own kind that nothing prunes, with their selected
-- agent and model and whether background work can wake them. A conversation is running while a turn
-- of it has not ended.
ALTER TABLE conversations ADD COLUMN preview BYTEA, ADD COLUMN last_updated TIMESTAMPTZ, ADD COLUMN tags JSONB,
    ADD COLUMN goal_objective TEXT, ADD COLUMN goal_check_script TEXT, ADD COLUMN goal_max_turns INTEGER, ADD COLUMN goal_turns_used INTEGER,
    ADD COLUMN goal_status TEXT, ADD COLUMN goal_note TEXT, ADD COLUMN goal_created_at_unix_ns BIGINT, ADD COLUMN goal_updated_at_unix_ns BIGINT,
    ADD COLUMN goal_principal_name TEXT, ADD COLUMN goal_principal_email TEXT,
    ADD COLUMN external_id TEXT UNIQUE, ADD COLUMN external_run TEXT UNIQUE, ADD COLUMN external_agent TEXT, ADD COLUMN external_channel TEXT, ADD COLUMN origin_pairs JSONB,
    ADD COLUMN shadow_parent TEXT REFERENCES conversations (conversation_id) ON DELETE SET NULL, ADD COLUMN parent_agent TEXT, ADD COLUMN shadow_agent TEXT,
    ADD COLUMN server TEXT, ADD COLUMN directory TEXT, ADD COLUMN title TEXT, ADD COLUMN running BOOLEAN NOT NULL DEFAULT FALSE, ADD COLUMN synced_at TIMESTAMPTZ, ADD COLUMN reply TEXT,
    ADD COLUMN background BOOLEAN NOT NULL DEFAULT FALSE, ADD COLUMN model TEXT, ADD COLUMN remote_session TEXT,
    DROP CONSTRAINT conversations_kind_check,
    ADD CONSTRAINT conversations_kind_check CHECK (kind IN ('web_chat', 'slack_thread', 'cron_run', 'one_off_cron_run', 'external_mcp_run', 'subagent', 'review', 'shadow', 'remote')),
    DROP CONSTRAINT conversations_retention_check, ADD CONSTRAINT conversations_retention_check CHECK (retention IN ('record', 'private', 'nested', 'remote'));
CREATE UNIQUE INDEX conversations_shadow ON conversations (shadow_parent, parent_agent, shadow_agent) WHERE shadow_parent IS NOT NULL;
CREATE INDEX conversations_server ON conversations (server) WHERE server IS NOT NULL;

-- A remote session's own ID, on its server, was the rest of its conversation ID; one that is not
-- fails the check below.
INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, retention, server, directory, title, last_updated, running, synced_at, pinned, name, reply,
    background, model, remote_session)
SELECT conversation_id, agent, '', 'remote', FALSE, 'remote', server, directory, title, updated_at, running, synced_at, pinned, name, reply, background, model,
    CASE WHEN starts_with(conversation_id, 'opencode:' || server || ':') THEN NULLIF(substr(conversation_id, length('opencode:' || server || ':') + 1), '') END
FROM opencode_sessions;
ALTER TABLE conversations ADD CONSTRAINT conversations_remote_session_check CHECK ((kind = 'remote') = (remote_session IS NOT NULL));
UPDATE conversations c SET running = TRUE FROM active_turns a WHERE a.conversation_id = c.conversation_id AND a.phase <> 'done';

-- Facts of an ID without a row, and goals' never-read Slack recipients, stay here as they were.
CREATE TABLE legacy_unmoved_facts AS
SELECT 'session_summaries' AS source, s.conversation_id, to_jsonb(s) AS row FROM session_summaries s
LEFT JOIN conversations c USING (conversation_id) WHERE c.conversation_id IS NULL OR c.kind = 'remote'
UNION ALL SELECT 'session_tags', t.conversation_id, to_jsonb(t) FROM session_tags t
LEFT JOIN conversations c USING (conversation_id) WHERE c.conversation_id IS NULL
UNION ALL SELECT 'conversation_goals', g.conversation_id, to_jsonb(g) FROM conversation_goals g
LEFT JOIN conversations c USING (conversation_id) WHERE c.conversation_id IS NULL OR g.slack_recipient_team_id <> '' OR g.slack_recipient_user_id <> ''
UNION ALL SELECT 'external_mcp_sessions', m.managed_conversation_id, to_jsonb(m) FROM external_mcp_sessions m
LEFT JOIN conversations c ON c.conversation_id = m.managed_conversation_id WHERE c.conversation_id IS NULL;

UPDATE conversations c SET preview = s.preview, last_updated = s.last_updated FROM session_summaries s
WHERE c.conversation_id = s.conversation_id AND c.kind <> 'remote';
UPDATE conversations c SET tags = t.tags FROM session_tags t WHERE c.conversation_id = t.conversation_id;
UPDATE conversations c SET goal_objective = g.objective, goal_check_script = g.check_script, goal_max_turns = g.max_turns, goal_turns_used = g.turns_used,
    goal_status = g.status, goal_note = g.note, goal_created_at_unix_ns = g.created_at_unix_ns, goal_updated_at_unix_ns = g.updated_at_unix_ns,
    goal_principal_name = g.principal_name, goal_principal_email = g.principal_email
FROM conversation_goals g WHERE c.conversation_id = g.conversation_id;
UPDATE conversations c SET external_id = m.external_conversation_id, external_run = m.private_conversation_id, external_agent = m.agent,
    external_channel = m.slack_channel, origin_pairs = m.origin_pairs
FROM external_mcp_sessions m WHERE c.conversation_id = m.managed_conversation_id;
UPDATE conversations c SET shadow_parent = b.parent_conversation_id, parent_agent = b.parent_agent, shadow_agent = b.shadow_agent
FROM shadow_bindings b WHERE c.conversation_id = b.child_conversation_id;

-- The self-check: every moved row's values are on its conversation, and nothing else is.
-- +migrate StatementBegin
DO $$
DECLARE
    mismatches BIGINT;
BEGIN
    WITH moved_summaries AS (
        SELECT s.conversation_id, s.preview, s.last_updated FROM session_summaries s
        LEFT JOIN legacy_unmoved_facts u ON u.source = 'session_summaries' AND u.conversation_id = s.conversation_id WHERE u.source IS NULL
    ), summaries AS (
        SELECT conversation_id, preview, last_updated FROM conversations WHERE last_updated IS NOT NULL AND kind <> 'remote'
    ), moved_tags AS (
        SELECT t.conversation_id, t.tags FROM session_tags t
        LEFT JOIN legacy_unmoved_facts u ON u.source = 'session_tags' AND u.conversation_id = t.conversation_id WHERE u.source IS NULL
    ), tags AS (
        SELECT conversation_id, tags FROM conversations WHERE tags IS NOT NULL
    ), moved_goals AS (
        SELECT g.conversation_id, g.objective, g.check_script, g.max_turns, g.turns_used, g.status, g.note, g.created_at_unix_ns, g.updated_at_unix_ns,
            g.principal_name, g.principal_email
        FROM conversation_goals g JOIN conversations c USING (conversation_id)
    ), goals AS (
        SELECT conversation_id, goal_objective, goal_check_script, goal_max_turns, goal_turns_used, goal_status, goal_note, goal_created_at_unix_ns, goal_updated_at_unix_ns,
            goal_principal_name, goal_principal_email
        FROM conversations WHERE goal_objective IS NOT NULL
    ), moved_bindings AS (
        SELECT m.managed_conversation_id, m.external_conversation_id, m.private_conversation_id, m.agent, m.slack_channel, m.origin_pairs
        FROM external_mcp_sessions m JOIN conversations c ON c.conversation_id = m.managed_conversation_id
    ), bindings AS (
        SELECT conversation_id, external_id, external_run, external_agent, external_channel, origin_pairs FROM conversations WHERE external_id IS NOT NULL
    ), moved_shadows AS (
        SELECT child_conversation_id, parent_conversation_id, parent_agent, shadow_agent FROM shadow_bindings
    ), shadows AS (
        SELECT conversation_id, shadow_parent, parent_agent, shadow_agent FROM conversations WHERE shadow_parent IS NOT NULL
    ), moved_remote AS (
        SELECT conversation_id, server, directory, title, updated_at, running, synced_at, pinned, name, reply, background, agent, model FROM opencode_sessions
    ), remote AS (
        SELECT conversation_id, server, directory, title, last_updated, running, synced_at, pinned, name, reply, background, agent, model
        FROM conversations WHERE kind = 'remote'
    ), summary_diff AS (
        (SELECT * FROM moved_summaries EXCEPT SELECT * FROM summaries) UNION ALL (SELECT * FROM summaries EXCEPT SELECT * FROM moved_summaries)
    ), tag_diff AS (
        (SELECT * FROM moved_tags EXCEPT SELECT * FROM tags) UNION ALL (SELECT * FROM tags EXCEPT SELECT * FROM moved_tags)
    ), goal_diff AS (
        (SELECT * FROM moved_goals EXCEPT SELECT * FROM goals) UNION ALL (SELECT * FROM goals EXCEPT SELECT * FROM moved_goals)
    ), binding_diff AS (
        (SELECT * FROM moved_bindings EXCEPT SELECT * FROM bindings) UNION ALL (SELECT * FROM bindings EXCEPT SELECT * FROM moved_bindings)
    ), shadow_diff AS (
        (SELECT * FROM moved_shadows EXCEPT SELECT * FROM shadows) UNION ALL (SELECT * FROM shadows EXCEPT SELECT * FROM moved_shadows)
    ), remote_diff AS (
        (SELECT * FROM moved_remote EXCEPT SELECT * FROM remote) UNION ALL (SELECT * FROM remote EXCEPT SELECT * FROM moved_remote)
    ), diffs AS (
        SELECT 1 FROM summary_diff UNION ALL SELECT 1 FROM tag_diff UNION ALL SELECT 1 FROM goal_diff
        UNION ALL SELECT 1 FROM binding_diff UNION ALL SELECT 1 FROM shadow_diff UNION ALL SELECT 1 FROM remote_diff
    )
    SELECT count(*) INTO mismatches FROM diffs;
    IF mismatches > 0 THEN
        RAISE EXCEPTION '052_conversation_facts: % rows differ from their source tables', mismatches;
    END IF;
END $$;
-- +migrate StatementEnd

-- With the summary (saveSessionSummary): whether a conversation has entries of its own, and the
-- conversation its earliest shown entry came from, a run reporting to it or a copied entry's
-- source, NULL when that entry is its own. Web lists a chat and decides its origin from them.
ALTER TABLE conversations ADD COLUMN has_entries BOOLEAN NOT NULL DEFAULT FALSE, ADD COLUMN first_source TEXT;
-- The sidebar and session search read the listed chats, the chats continuing a run, and the facts
-- of the cron runs that created chats, without reading every conversation.
CREATE INDEX conversations_listed ON conversations (conversation_id) WHERE listed;
CREATE INDEX conversations_continues ON conversations (continues) WHERE continues IS NOT NULL;
CREATE INDEX conversations_cron_runs ON conversations (conversation_id) INCLUDE (agent, cron_path, cron_run_at_unix_ns, kind) WHERE cron_run_at_unix_ns IS NOT NULL;
-- Session search resolves the rooms of listed chats' Slack channels; without this it scans every conversation.
CREATE INDEX conversations_slack_channels ON conversations (slack_channel) WHERE listed AND slack_channel IS NOT NULL;
WITH firsts AS (
    SELECT c.conversation_id, e.id, COALESCE(e.source, s.conversation_id) AS source, TRUE AS own FROM conversations c
    -- Exception to the no-subquery rule: stop at each conversation's first entry.
    CROSS JOIN LATERAL (
        SELECT id, f.sync_source_conversation_id AS source,
            CASE WHEN f.sync_source_conversation_id IS NULL THEN f.sync_source_entry_id::bigint END AS source_entry_id
        FROM session_entries, json_to_record(entry_json) AS f(sync_source_conversation_id text, sync_source_entry_id text)
        WHERE conversation_id = c.conversation_id ORDER BY id LIMIT 1
    ) e
    LEFT JOIN session_entries s ON s.id = e.source_entry_id
    UNION ALL
    SELECT m.reports_to, e.id, m.conversation_id, FALSE FROM conversations m
    -- The same exception, per run.
    CROSS JOIN LATERAL (SELECT id FROM session_entries WHERE conversation_id = m.conversation_id AND audience <> 'sandbox' ORDER BY id LIMIT 1) e
    WHERE m.reports_to IS NOT NULL
), facts AS (
    SELECT conversation_id, bool_or(own) AS has_entries, (array_agg(source ORDER BY id))[1] AS first_source FROM firsts GROUP BY conversation_id
)
UPDATE conversations c SET has_entries = f.has_entries, first_source = f.first_source FROM facts f WHERE c.conversation_id = f.conversation_id;

-- The self-check: both facts read again from every entry, in one pass rather than per conversation.
-- +migrate StatementBegin
DO $$
DECLARE
    mismatches BIGINT;
BEGIN
    WITH shown AS (
        SELECT conversation_id AS viewer, id, NULL AS run FROM session_entries
        UNION ALL SELECT m.reports_to, e.id, m.conversation_id FROM conversations m JOIN session_entries e USING (conversation_id)
        WHERE m.reports_to IS NOT NULL AND e.audience <> 'sandbox'
    ), earliest AS (
        SELECT DISTINCT ON (viewer) viewer, id, run FROM shown ORDER BY viewer, id
    ), owners AS (
        SELECT DISTINCT conversation_id FROM session_entries
    ), derived AS (
        SELECT c.conversation_id, o.conversation_id IS NOT NULL AS has_entries,
            COALESCE(f.run, e.entry_json::jsonb->>'sync_source_conversation_id', s.conversation_id) AS first_source
        FROM conversations c LEFT JOIN earliest f ON f.viewer = c.conversation_id LEFT JOIN owners o USING (conversation_id)
        LEFT JOIN session_entries e ON e.id = f.id AND f.run IS NULL
        LEFT JOIN session_entries s ON s.id = (e.entry_json::jsonb->>'sync_source_entry_id')::bigint
    ), diffs AS (
        SELECT * FROM derived EXCEPT SELECT conversation_id, has_entries, first_source FROM conversations
    )
    SELECT count(*) INTO mismatches FROM diffs;
    IF mismatches > 0 THEN
        RAISE EXCEPTION '052_conversation_facts: % conversations'' entry facts differ from their entries', mismatches;
    END IF;
END $$;
-- +migrate StatementEnd

-- Session search reads the words a needle may be in through a trigram index: of a chat Web lists or
-- a remote session, its fields and origin words, and of a cron run, its agent, path, and time as its
-- origin shows it. They are lowercased as Go lowercases ASCII letters, with İ and K, the only other
-- letters Go lowercases into ASCII, lowercased first. convert_from is only stable because it depends
-- on the database encoding, which never changes.
CREATE FUNCTION utf8_text(bytea) RETURNS text LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$ SELECT convert_from($1, 'UTF8') $$;
ALTER TABLE conversations ADD COLUMN search_words TEXT GENERATED ALWAYS AS (lower(replace(replace(
    CASE WHEN listed OR server IS NOT NULL THEN name || ' ' || COALESCE(utf8_text(preview), '') || ' ' || agent || ' slack ' || COALESCE(slack_channel, '') || ' ' || conversation_id
        || ' ' || COALESCE(external_id, '') || ' ' || COALESCE(external_agent, '') || ' ' || COALESCE(origin_pairs::text, 'null') || ' ' || COALESCE(cron_run, '')
        || ' ' || COALESCE(first_source, '') || ' ' || COALESCE(title, '') || ' ' || COALESCE(server, '') || ' ' || COALESCE(directory, '') ELSE '' END
    || CASE WHEN cron_run_at_unix_ns IS NOT NULL THEN ' ' || agent || ' ' || COALESCE(cron_path, '')
        || ' ' || lpad(extract(year FROM timezone('UTC', to_timestamp(cron_run_at_unix_ns / 1000000000)))::text, 4, '0')
        || '-' || lpad(extract(month FROM timezone('UTC', to_timestamp(cron_run_at_unix_ns / 1000000000)))::text, 2, '0')
        || '-' || lpad(extract(day FROM timezone('UTC', to_timestamp(cron_run_at_unix_ns / 1000000000)))::text, 2, '0')
        || 't' || lpad(extract(hour FROM timezone('UTC', to_timestamp(cron_run_at_unix_ns / 1000000000)))::text, 2, '0')
        || ':' || lpad(extract(minute FROM timezone('UTC', to_timestamp(cron_run_at_unix_ns / 1000000000)))::text, 2, '0')
        || ':' || lpad(floor(extract(second FROM timezone('UTC', to_timestamp(cron_run_at_unix_ns / 1000000000))))::text, 2, '0')
        || COALESCE('.' || NULLIF(rtrim(lpad(mod(cron_run_at_unix_ns, 1000000000)::text, 9, '0'), '0'), ''), '') || 'z' ELSE '' END,
    U&'\0130', 'i'), U&'\212A', 'k') COLLATE "C")) STORED;
CREATE INDEX conversations_search_words ON conversations USING gin (search_words public.gin_trgm_ops);

-- The self-check: each cron run's words hold its time as its origin shows it.
-- +migrate StatementBegin
DO $$
DECLARE
    mismatches BIGINT;
BEGIN
    WITH runs AS (
        SELECT search_words, to_char(to_timestamp(div(cron_run_at_unix_ns, 1000000000)) AT TIME ZONE 'UTC', 'YYYY-MM-DD"t"HH24:MI:SS')
            || COALESCE('.' || NULLIF(rtrim(lpad(mod(cron_run_at_unix_ns, 1000000000)::text, 9, '0'), '0'), ''), '') || 'z' AS run_time
        FROM conversations WHERE cron_run_at_unix_ns IS NOT NULL
    )
    SELECT count(*) INTO mismatches FROM runs WHERE strpos(search_words, run_time) = 0;
    IF mismatches > 0 THEN
        RAISE EXCEPTION '052_conversation_facts: % cron runs'' search words miss their time', mismatches;
    END IF;
END $$;
-- +migrate StatementEnd

-- The folded tables stay, empty, until the release's last migration retires them. A shadow's
-- comparison turns now go with its conversation, and binding changes reach its parent from the row.
ALTER TABLE shadow_turns DROP CONSTRAINT shadow_turns_child_conversation_id_fkey,
    ADD CONSTRAINT shadow_turns_child_conversation_id_fkey FOREIGN KEY (child_conversation_id) REFERENCES conversations (conversation_id) ON DELETE CASCADE;
DROP TRIGGER shadow_bindings_change ON shadow_bindings;
DELETE FROM session_summaries;
DELETE FROM session_tags;
DELETE FROM conversation_goals;
DELETE FROM external_mcp_sessions;
DELETE FROM shadow_bindings;
DELETE FROM opencode_sessions;

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
CREATE TRIGGER conversations_shadow_change AFTER INSERT OR UPDATE OF shadow_parent, parent_agent, shadow_agent OR DELETE ON conversations
    FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();

-- +migrate Down
DROP TRIGGER conversations_shadow_change ON conversations;
INSERT INTO session_summaries SELECT conversation_id, preview, last_updated FROM conversations WHERE last_updated IS NOT NULL AND kind <> 'remote';
INSERT INTO session_tags SELECT conversation_id, tags FROM conversations WHERE tags IS NOT NULL;
INSERT INTO conversation_goals SELECT (jsonb_populate_record(NULL::conversation_goals, row)).* FROM legacy_unmoved_facts WHERE source = 'conversation_goals';
INSERT INTO conversation_goals (conversation_id, objective, check_script, max_turns, turns_used, status, note, created_at_unix_ns, updated_at_unix_ns, principal_name, principal_email)
SELECT conversation_id, goal_objective, goal_check_script, goal_max_turns, goal_turns_used, goal_status, goal_note, goal_created_at_unix_ns, goal_updated_at_unix_ns,
    goal_principal_name, goal_principal_email
FROM conversations WHERE goal_objective IS NOT NULL ON CONFLICT (conversation_id) DO NOTHING;
INSERT INTO external_mcp_sessions (external_conversation_id, private_conversation_id, managed_conversation_id, agent, slack_channel, origin_pairs)
SELECT external_id, external_run, conversation_id, external_agent, external_channel, origin_pairs FROM conversations WHERE external_id IS NOT NULL;
INSERT INTO session_summaries SELECT (jsonb_populate_record(NULL::session_summaries, row)).* FROM legacy_unmoved_facts WHERE source = 'session_summaries';
INSERT INTO session_tags SELECT (jsonb_populate_record(NULL::session_tags, row)).* FROM legacy_unmoved_facts WHERE source = 'session_tags';
INSERT INTO external_mcp_sessions SELECT (jsonb_populate_record(NULL::external_mcp_sessions, row)).* FROM legacy_unmoved_facts WHERE source = 'external_mcp_sessions';
INSERT INTO shadow_bindings (child_conversation_id, parent_conversation_id, parent_agent, shadow_agent)
SELECT conversation_id, shadow_parent, parent_agent, shadow_agent FROM conversations WHERE shadow_parent IS NOT NULL;
INSERT INTO opencode_sessions (conversation_id, server, title, directory, updated_at, running, synced_at, pinned, name, reply, background, agent, model)
SELECT conversation_id, server, title, directory, last_updated, running, synced_at, pinned, name, reply, background, agent, model FROM conversations WHERE kind = 'remote';
DELETE FROM conversations WHERE kind = 'remote';
DROP TABLE legacy_unmoved_facts;
ALTER TABLE shadow_turns DROP CONSTRAINT shadow_turns_child_conversation_id_fkey,
    ADD CONSTRAINT shadow_turns_child_conversation_id_fkey FOREIGN KEY (child_conversation_id) REFERENCES shadow_bindings (child_conversation_id) ON DELETE CASCADE;
CREATE TRIGGER shadow_bindings_change AFTER INSERT OR UPDATE OR DELETE ON shadow_bindings
    FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();
DROP INDEX conversations_shadow, conversations_server, conversations_listed, conversations_continues, conversations_cron_runs, conversations_slack_channels, conversations_search_words;
ALTER TABLE conversations DROP COLUMN search_words;
DROP FUNCTION utf8_text(bytea);
ALTER TABLE conversations DROP COLUMN has_entries, DROP COLUMN first_source, DROP COLUMN preview, DROP COLUMN last_updated, DROP COLUMN tags,
    DROP COLUMN goal_objective, DROP COLUMN goal_check_script, DROP COLUMN goal_max_turns, DROP COLUMN goal_turns_used,
    DROP COLUMN goal_status, DROP COLUMN goal_note, DROP COLUMN goal_created_at_unix_ns, DROP COLUMN goal_updated_at_unix_ns,
    DROP COLUMN goal_principal_name, DROP COLUMN goal_principal_email,
    DROP COLUMN external_id, DROP COLUMN external_run, DROP COLUMN external_agent, DROP COLUMN external_channel, DROP COLUMN origin_pairs,
    DROP COLUMN shadow_parent, DROP COLUMN parent_agent, DROP COLUMN shadow_agent,
    DROP COLUMN server, DROP COLUMN directory, DROP COLUMN title, DROP COLUMN running, DROP COLUMN synced_at, DROP COLUMN reply,
    DROP COLUMN background, DROP COLUMN model, DROP COLUMN remote_session,
    DROP CONSTRAINT conversations_kind_check,
    ADD CONSTRAINT conversations_kind_check CHECK (kind IN ('web_chat', 'slack_thread', 'cron_run', 'one_off_cron_run', 'external_mcp_run', 'subagent', 'review', 'shadow')),
    DROP CONSTRAINT conversations_retention_check, ADD CONSTRAINT conversations_retention_check CHECK (retention IN ('record', 'private', 'nested'));

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
