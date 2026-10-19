-- +migrate Up
-- One table holds every conversation. What each one is, and whether Web lists it, are facts its
-- creating code stores; after this migration derives them once from ID shapes, nothing parses an ID.
ALTER TABLE managed_conversations RENAME TO conversations;
-- cron_path and cron_run_at_unix_ns locate a cron run; cron_run names the run a Web chat continues,
-- kept when the chat's history is cleared; the Slack channel and thread locate a Slack thread.
-- retention keeps the pruning a conversation had when only managed conversations had rows: a
-- record expires as a thread; a private history without a record (once a slack-thread:,
-- external_mcp:, cron:, or one-off-cron: ID) expires by age while unreferenced; any other
-- history without a record goes only with its family.
ALTER TABLE conversations ADD COLUMN kind TEXT, ADD COLUMN listed BOOLEAN, ADD COLUMN slack_channel TEXT, ADD COLUMN slack_thread TEXT,
    ADD COLUMN cron_path TEXT, ADD COLUMN cron_run_at_unix_ns BIGINT, ADD COLUMN cron_run TEXT,
    ADD COLUMN retention TEXT NOT NULL DEFAULT 'record' CHECK (retention IN ('record', 'private', 'nested')),
    ALTER COLUMN parent_conversation_id DROP NOT NULL, ALTER COLUMN parent_conversation_id DROP DEFAULT;

-- Recorded conversations: listed is exactly what visibleChatsSQL returned.
WITH facts AS (
    SELECT c.conversation_id,
        CASE WHEN b.child_conversation_id IS NOT NULL THEN 'shadow'
            WHEN p.private_conversation_id IS NOT NULL THEN 'external_mcp_run'
            WHEN c.parent_conversation_id <> '' THEN 'subagent'
            WHEN c.conversation_id LIKE 'cron:%' THEN 'cron_run'
            WHEN c.conversation_id LIKE 'one-off-cron:%' THEN 'one_off_cron_run'
            WHEN c.conversation_id LIKE 'slack-thread:%' THEN 'slack_thread'
            ELSE 'web_chat' END AS kind,
        c.conversation_id NOT LIKE 'cron:%' AND c.conversation_id NOT LIKE 'one-off-cron:%' AND b.child_conversation_id IS NULL
            AND p.private_conversation_id IS NULL AND parent.conversation_id IS NULL AS listed,
        parent.conversation_id AS parent
    FROM conversations c
    LEFT JOIN shadow_bindings b ON b.child_conversation_id = c.conversation_id
    LEFT JOIN external_mcp_sessions p ON p.private_conversation_id = c.conversation_id
    LEFT JOIN conversations parent ON parent.conversation_id = c.parent_conversation_id
)
UPDATE conversations c SET kind = f.kind, listed = f.listed, parent_conversation_id = f.parent FROM facts f WHERE c.conversation_id = f.conversation_id;

-- Every history without a record becomes a hidden conversation: nested under the deepest known
-- conversation its ID extends with '/', else classified by its ID shape. No rule, no migration.
-- So do the tags and goals of private IDs without one, which pruning expired.
CREATE TABLE legacy_unrecorded_conversations AS
WITH private_leftovers AS (
    SELECT conversation_id FROM session_tags UNION SELECT conversation_id FROM conversation_goals
), histories AS (
    SELECT conversation_id FROM session_entries UNION SELECT conversation_id FROM active_turns
    UNION SELECT private_conversation_id FROM external_mcp_sessions WHERE private_conversation_id IS NOT NULL
    UNION SELECT conversation_id FROM private_leftovers WHERE conversation_id LIKE 'slack-thread:%' OR conversation_id LIKE 'external_mcp:%'
        OR conversation_id LIKE 'cron:%' OR conversation_id LIKE 'one-off-cron:%'
), unrecorded AS (
    SELECT h.conversation_id FROM histories h LEFT JOIN conversations c USING (conversation_id) WHERE c.conversation_id IS NULL
), known AS (
    SELECT conversation_id FROM conversations UNION SELECT conversation_id FROM histories
), prefixes AS (
    SELECT u.conversation_id, left(u.conversation_id, i - 1) AS prefix, i FROM unrecorded u
    CROSS JOIN LATERAL generate_series(2, length(u.conversation_id)) AS i WHERE substr(u.conversation_id, i, 1) = '/'
), parents AS (
    SELECT DISTINCT ON (p.conversation_id) p.conversation_id, p.prefix FROM prefixes p JOIN known k ON k.conversation_id = p.prefix
    ORDER BY p.conversation_id, p.i DESC
)
SELECT u.conversation_id, n.prefix AS parent_conversation_id, COALESCE(m.agent, '') AS agent,
    CASE WHEN u.conversation_id LIKE 'slack-thread:%' OR u.conversation_id LIKE 'external_mcp:%' OR u.conversation_id LIKE 'cron:%'
        OR u.conversation_id LIKE 'one-off-cron:%' THEN 'private' ELSE 'nested' END AS retention,
    CASE WHEN n.prefix IS NOT NULL AND u.conversation_id ~ '-review-[^/]*$' THEN 'review'
        WHEN n.prefix IS NOT NULL THEN 'subagent'
        WHEN m.private_conversation_id IS NOT NULL THEN 'external_mcp_run'
        WHEN u.conversation_id ~ '/[^/]*-review-[^/]*$' THEN 'review'
        WHEN u.conversation_id LIKE 'cron:%' THEN 'cron_run'
        WHEN u.conversation_id LIKE 'one-off-cron:%' THEN 'one_off_cron_run'
        WHEN u.conversation_id LIKE 'slack-thread:%' THEN 'slack_thread'
        WHEN u.conversation_id LIKE 'external_mcp:%' THEN 'external_mcp_run'
        WHEN u.conversation_id ~ '^(web:|web-session:|[A-Z2-7]{26}$)' THEN 'web_chat'
    END AS kind
FROM unrecorded u LEFT JOIN parents n USING (conversation_id) LEFT JOIN external_mcp_sessions m ON m.private_conversation_id = u.conversation_id;

-- +migrate StatementBegin
DO $$
DECLARE
    unclassified BIGINT;
    example TEXT;
BEGIN
    SELECT count(*), min(conversation_id) INTO unclassified, example FROM legacy_unrecorded_conversations WHERE kind IS NULL;
    IF unclassified > 0 THEN
        RAISE EXCEPTION '051_conversations: % histories match no conversation kind, such as %', unclassified, example;
    END IF;
END $$;
-- +migrate StatementEnd

INSERT INTO conversations (conversation_id, agent, created_by, parent_conversation_id, created_at, kind, listed, retention)
SELECT conversation_id, agent, '', parent_conversation_id, '0001-01-01 00:00:00+00', kind, FALSE, retention FROM legacy_unrecorded_conversations;

-- A delegation's call: the call of its parent's turn that started it, matched once against the rules
-- that named it (rocketcode.delegationName): the call ID itself, as before delegations had their own
-- key, which records no turn; else the call ID and a hash of the call's key, with -review- for a
-- permission review. A turn that never saved holds its calls in its trace. No match, no migration.
ALTER TABLE conversations ADD COLUMN call_turn TEXT, ADD COLUMN call_id TEXT;
WITH children AS (
    SELECT conversation_id, parent_conversation_id AS parent, substr(conversation_id, length(parent_conversation_id) + 2) AS name
    FROM conversations WHERE starts_with(conversation_id, parent_conversation_id || '/')
), parents AS (
    SELECT DISTINCT parent FROM children
), calls AS (
    SELECT p.parent, e.entry_json::jsonb->>'turn_id' AS turn_id, item->>'call_id' AS call_id FROM parents p
    JOIN session_entries e ON e.conversation_id = p.parent
    CROSS JOIN LATERAL jsonb_array_elements(NULLIF(e.entry_json::jsonb->'replay_input', 'null'::jsonb)) AS items(item)
    WHERE item->>'type' = 'function_call'
    UNION SELECT p.parent, t.id, progress->>'id' FROM parents p JOIN active_turns t ON t.conversation_id = p.parent
    CROSS JOIN LATERAL jsonb_path_query(t.output_trace_json::jsonb, '$[*] ? (@.type == "rocketcode_public_progress").progress') AS progress
), keyed AS (
    SELECT c.conversation_id, c.name, k.turn_id, k.call_id, left(encode(sha256(convert_to(k.turn_id || '/call/' || k.call_id, 'UTF8')), 'hex'), 8) AS hash
    FROM children c JOIN calls k USING (parent) WHERE starts_with(c.name, k.call_id)
), matched AS (
    SELECT DISTINCT ON (conversation_id) conversation_id, CASE WHEN name <> call_id THEN turn_id END AS call_turn, call_id
    FROM keyed WHERE name IN (call_id, call_id || '-review-' || hash, call_id || '-' || hash)
    ORDER BY conversation_id
)
UPDATE conversations c SET call_turn = m.call_turn, call_id = m.call_id FROM matched m WHERE c.conversation_id = m.conversation_id;

-- +migrate StatementBegin
DO $$
DECLARE
    unmatched BIGINT;
    example TEXT;
BEGIN
    SELECT count(*), min(conversation_id) INTO unmatched, example FROM conversations WHERE parent_conversation_id IS NOT NULL AND call_id IS NULL;
    IF unmatched > 0 THEN
        RAISE EXCEPTION '051_conversations: % delegations match no call of their parent, such as %', unmatched, example;
    END IF;
END $$;
-- +migrate StatementEnd

-- A cron run's path and start time, and a Web chat's continued run, as ParseCronRun read them from IDs.
WITH runs AS (
    SELECT conversation_id, regexp_match(conversation_id, '^(?:cron|one-off-cron):(.*):([0-9]{4})([0-9]{2})([0-9]{2})T([0-9]{2})([0-9]{2})([0-9]{2})\.([0-9]{9})Z:[^:]*$') AS m
    FROM conversations WHERE kind IN ('cron_run', 'one_off_cron_run')
)
UPDATE conversations c SET cron_path = r.m[1], cron_run_at_unix_ns = r.m[8]::bigint
    + extract(epoch FROM make_timestamptz(r.m[2]::int, r.m[3]::int, r.m[4]::int, r.m[5]::int, r.m[6]::int, r.m[7]::int, 'UTC'))::bigint * 1000000000
FROM runs r WHERE c.conversation_id = r.conversation_id AND r.m IS NOT NULL;
UPDATE conversations SET cron_run = substr(conversation_id, 5)
WHERE kind = 'web_chat' AND (conversation_id LIKE 'web:cron:%' OR conversation_id LIKE 'web:one-off-cron:%');

-- The channel of any slack-thread: ID, as SlackThreadTarget read it; the thread only of a Slack thread itself.
WITH threads AS (
    SELECT conversation_id, kind, regexp_match(conversation_id, '^slack-thread:([^:]*):(.*)$') AS m FROM conversations WHERE conversation_id LIKE 'slack-thread:%'
)
UPDATE conversations c SET slack_channel = trim(t.m[1]), slack_thread = CASE WHEN t.kind = 'slack_thread' THEN trim(t.m[2]) END
FROM threads t WHERE c.conversation_id = t.conversation_id AND trim(t.m[1]) <> '' AND trim(t.m[2]) <> '';

-- A run whose chat is gone reports to no conversation; its own view keeps its entries (sessionHistorySQL).
WITH dangling AS (
    SELECT r.conversation_id FROM conversations r LEFT JOIN conversations t ON t.conversation_id = r.reports_to
    WHERE r.reports_to IS NOT NULL AND t.conversation_id IS NULL
)
UPDATE conversations c SET reports_to = NULL FROM dangling d WHERE c.conversation_id = d.conversation_id;

ALTER TABLE conversations ALTER COLUMN kind SET NOT NULL, ALTER COLUMN listed SET NOT NULL,
    ADD CONSTRAINT conversations_kind_check CHECK (kind IN ('web_chat', 'slack_thread', 'cron_run', 'one_off_cron_run', 'external_mcp_run', 'subagent', 'review', 'shadow')),
    ADD CONSTRAINT conversations_parent_fkey FOREIGN KEY (parent_conversation_id) REFERENCES conversations (conversation_id) ON DELETE RESTRICT,
    ADD CONSTRAINT conversations_reports_to_fkey FOREIGN KEY (reports_to) REFERENCES conversations (conversation_id) ON DELETE SET NULL,
    ADD CONSTRAINT conversations_call_check CHECK ((parent_conversation_id IS NULL) = (call_id IS NULL) AND (call_turn IS NULL OR call_id IS NOT NULL));
CREATE INDEX conversations_parent ON conversations (parent_conversation_id) WHERE parent_conversation_id IS NOT NULL;

-- The live-update functions read the renamed table.
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
        SELECT DISTINCT m.reports_to FROM owners o JOIN conversations m ON m.conversation_id = o.id WHERE m.reports_to IS NOT NULL
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

-- The self-check: Web lists and shows the same chats, and every conversation's view keeps its entries in order.
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
        SELECT e.conversation_id AS viewer, e.id FROM session_entries e JOIN conversations c USING (conversation_id)
        WHERE e.audience <> CASE WHEN c.producer_inbound_json IS NULL THEN 'sandbox' ELSE 'visible' END
    ), reported AS (
        SELECT c.reports_to AS viewer, e.id FROM conversations c JOIN session_entries e USING (conversation_id)
        WHERE c.reports_to IS NOT NULL AND e.audience <> 'sandbox'
        UNION ALL SELECT c.conversation_id, e.id FROM conversations c JOIN session_entries e ON e.conversation_id = c.continues WHERE e.audience <> 'sandbox'
    ), viewed AS (
        SELECT * FROM own UNION ALL SELECT * FROM reported
    ), history AS (
        SELECT viewer AS conversation_id, row_number() OVER (PARTITION BY viewer ORDER BY id) AS position, id AS entry_id FROM viewed
    ), history_diff AS (
        (SELECT * FROM history EXCEPT SELECT * FROM legacy_history)
        UNION ALL (SELECT * FROM legacy_history EXCEPT SELECT * FROM history)
    ), viewers AS (
        SELECT DISTINCT viewer AS conversation_id FROM viewed
    ), running AS (
        SELECT DISTINCT conversation_id FROM active_turns WHERE phase <> 'done'
    ), shown AS (
        SELECT l.conversation_id FROM listed l LEFT JOIN viewers v USING (conversation_id) LEFT JOIN running r USING (conversation_id)
        WHERE v.conversation_id IS NOT NULL OR r.conversation_id IS NOT NULL
    ), shown_diff AS (
        (SELECT * FROM shown EXCEPT SELECT * FROM legacy_shown)
        UNION ALL (SELECT * FROM legacy_shown EXCEPT SELECT * FROM shown)
    ), diffs AS (
        SELECT 1 FROM listed_diff UNION ALL SELECT 1 FROM history_diff UNION ALL SELECT 1 FROM shown_diff
    )
    SELECT count(*) INTO mismatches FROM diffs;
    IF mismatches > 0 THEN
        RAISE EXCEPTION '051_conversations: % rows differ from the baseline manifest', mismatches;
    END IF;
END $$;
-- +migrate StatementEnd

-- +migrate Down
DROP INDEX conversations_parent;
ALTER TABLE conversations DROP CONSTRAINT conversations_parent_fkey, DROP CONSTRAINT conversations_reports_to_fkey, DROP CONSTRAINT conversations_kind_check,
    DROP CONSTRAINT conversations_call_check;
DELETE FROM conversations c USING legacy_unrecorded_conversations u WHERE c.conversation_id = u.conversation_id;
DROP TABLE legacy_unrecorded_conversations;
UPDATE conversations SET parent_conversation_id = '' WHERE parent_conversation_id IS NULL;
ALTER TABLE conversations DROP COLUMN kind, DROP COLUMN listed, DROP COLUMN slack_channel, DROP COLUMN slack_thread,
    DROP COLUMN cron_path, DROP COLUMN cron_run_at_unix_ns, DROP COLUMN cron_run, DROP COLUMN retention, DROP COLUMN call_turn, DROP COLUMN call_id,
    ALTER COLUMN parent_conversation_id SET DEFAULT '', ALTER COLUMN parent_conversation_id SET NOT NULL;
ALTER TABLE conversations RENAME TO managed_conversations;

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
