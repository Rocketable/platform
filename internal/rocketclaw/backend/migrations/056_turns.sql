-- +migrate Up
-- One table holds a conversation's turns: waiting, running, delivering, or done. A waiting turn is
-- an input the lane has not started: queued, stashed, or held for a stopped parent; delivery is how
-- it reaches the lane (enqueue, steer, prompt, or held), and created_at_unix_ns is when it was saved.
-- The lane's worker starts it by updating the row. Waiting turns move from thread_queue with their
-- input IDs unchanged; thread_queue stays, empty, until the release's last migration retires it.
-- Bulk moves must not notify per row; the triggers come back below, silent for waiting turns, as
-- thread_queue was.
DROP TRIGGER active_turns_transcript_change ON active_turns;
DROP TRIGGER shadow_active_change ON active_turns;
DROP TRIGGER active_turns_sync_destination_change ON active_turns;
ALTER TABLE active_turns RENAME TO turns;
ALTER INDEX active_turns_pkey RENAME TO turns_pkey;
ALTER INDEX active_turns_conversation_updated RENAME TO turns_conversation_updated;
ALTER INDEX active_turns_search_text RENAME TO turns_search_text;
ALTER TABLE turns ALTER COLUMN id SET DEFAULT 'waiting-' || gen_random_uuid(), ALTER COLUMN inbound_json DROP NOT NULL,
    ALTER COLUMN output_trace_json SET DEFAULT '[]',
    ADD COLUMN reason TEXT, ADD COLUMN delivery TEXT, ADD COLUMN input_id TEXT, ADD COLUMN position INTEGER, ADD COLUMN park_after TEXT,
    ADD COLUMN message TEXT, ADD COLUMN principal TEXT, ADD COLUMN source TEXT, ADD COLUMN content JSONB, ADD COLUMN slack_reply JSONB,
    ADD COLUMN slack_channel TEXT, ADD COLUMN slack_ts TEXT,
    ADD COLUMN takes_lane BOOLEAN NOT NULL DEFAULT TRUE, ADD COLUMN kind TEXT NOT NULL DEFAULT 'model',
    ADD CONSTRAINT turns_phase_check CHECK (phase IN ('waiting', 'running', 'delivering', 'done')),
    ADD CONSTRAINT turns_reason_check CHECK ((phase = 'waiting') = (reason IS NOT NULL) AND reason IN ('queued', 'stashed', 'held')
        AND (reason = 'queued') = (delivery <> 'held')),
    ADD CONSTRAINT turns_kind_check CHECK (kind IN ('model', 'script', 'note'));
-- One waiting turn per input; as thread_queue's key did, an input ID waits in one conversation only.
CREATE UNIQUE INDEX turns_waiting_input ON turns (input_id) WHERE phase = 'waiting';

INSERT INTO turns (conversation_id, phase, reason, delivery, input_id, position, park_after, message, principal, source, content, slack_reply,
    slack_channel, slack_ts, inbound_json, created_at_unix_ns, updated_at_unix_ns)
SELECT conversation_id, 'waiting', CASE WHEN kind <> 'held' THEN 'queued' WHEN inbound_json->>'Kind' = 'enqueue' THEN 'held' ELSE 'stashed' END,
    kind, queue_item_id, position, park_after, message, principal, source, content, slack_reply, slack_channel, slack_ts, inbound_json, stash_at_unix_ns, stash_at_unix_ns
FROM thread_queue;

-- The self-check: every lane keeps 049's waiting inputs with their deliveries, in the order its
-- queue had them here (049 itself unparked the inputs a pending schedule reset released, after
-- recording its manifest), and every moved value is on its waiting turn.
-- +migrate StatementBegin
DO $$
DECLARE
    mismatches BIGINT;
BEGIN
    WITH waiting AS (
        SELECT conversation_id, input_id, delivery, row_number() OVER (PARTITION BY conversation_id ORDER BY park_after <> '', position, created_at_unix_ns, input_id) AS n
        FROM turns WHERE phase = 'waiting'
    ), queued AS (
        SELECT conversation_id, queue_item_id, kind, row_number() OVER (PARTITION BY conversation_id ORDER BY park_after <> '', position, stash_at_unix_ns, queue_item_id) AS n
        FROM thread_queue
    ), manifest AS (
        SELECT conversation_id, queue_item_id, kind FROM legacy_lane_work
    ), moved AS (
        SELECT queue_item_id, conversation_id, message, principal, stash_at_unix_ns, position, park_after, slack_channel, slack_ts, kind, content, source, slack_reply,
            md5(inbound_json::text) AS inbound FROM thread_queue
    ), turned AS (
        SELECT input_id, conversation_id, message, principal, created_at_unix_ns, position, park_after, slack_channel, slack_ts, delivery, content, source, slack_reply,
            md5(inbound_json::text) FROM turns WHERE phase = 'waiting'
    ), diffs AS (
        (SELECT conversation_id, input_id, delivery FROM waiting EXCEPT SELECT * FROM manifest)
        UNION ALL (SELECT * FROM manifest EXCEPT SELECT conversation_id, input_id, delivery FROM waiting)
        UNION ALL (SELECT conversation_id, input_id, n::text FROM waiting EXCEPT SELECT conversation_id, queue_item_id, n::text FROM queued)
        UNION ALL (SELECT conversation_id, queue_item_id, n::text FROM queued EXCEPT SELECT conversation_id, input_id, n::text FROM waiting)
        UNION ALL (SELECT conversation_id, queue_item_id, md5(moved::text) FROM moved EXCEPT SELECT conversation_id, input_id, md5(turned::text) FROM turned)
        UNION ALL (SELECT conversation_id, input_id, md5(turned::text) FROM turned EXCEPT SELECT conversation_id, queue_item_id, md5(moved::text) FROM moved)
    )
    SELECT count(*) INTO mismatches FROM diffs;
    IF mismatches > 0 THEN
        RAISE EXCEPTION '056_turns: % waiting inputs differ from the baseline manifest or thread_queue', mismatches;
    END IF;
END $$;
-- +migrate StatementEnd

DELETE FROM thread_queue;

-- +migrate StatementBegin
CREATE OR REPLACE FUNCTION turn_trace(TEXT, TEXT) RETURNS jsonb LANGUAGE sql STABLE SET search_path FROM CURRENT AS $$
    WITH turn AS (
        SELECT conversation_id FROM turns WHERE id = $1
    )
    SELECT $2::jsonb || COALESCE(jsonb_agg(s.value::jsonb ORDER BY s.id), '[]'::jsonb) FROM turn t
    JOIN steps s ON s.conversation_id = t.conversation_id AND starts_with(s.key, $1 || '/code_mode/')
$$;
-- +migrate StatementEnd

CREATE TRIGGER turns_transcript_change AFTER INSERT OR UPDATE OF conversation_id, output_trace_json, terminal, phase, created_at_unix_ns, history_anchor_id, replay, record
    ON turns FOR EACH ROW WHEN (NEW.phase <> 'waiting') EXECUTE FUNCTION notify_transcript_change();
CREATE TRIGGER turns_transcript_delete AFTER DELETE ON turns FOR EACH ROW WHEN (OLD.phase <> 'waiting') EXECUTE FUNCTION notify_transcript_change();
CREATE TRIGGER shadow_turns_row_change AFTER INSERT OR UPDATE OF output_trace_json, terminal, phase, replay, record
    ON turns FOR EACH ROW WHEN (NEW.phase <> 'waiting') EXECUTE FUNCTION notify_shadow_change();
CREATE TRIGGER shadow_turns_row_delete AFTER DELETE ON turns FOR EACH ROW WHEN (OLD.phase <> 'waiting') EXECUTE FUNCTION notify_shadow_change();
CREATE TRIGGER turns_sync_destination_change AFTER INSERT OR UPDATE OF output_trace_json, terminal, phase
    ON turns FOR EACH ROW WHEN (NEW.phase <> 'waiting') EXECUTE FUNCTION notify_sync_destination_change();
CREATE TRIGGER turns_sync_destination_delete AFTER DELETE ON turns FOR EACH ROW WHEN (OLD.phase <> 'waiting') EXECUTE FUNCTION notify_sync_destination_change();
ANALYZE turns;

-- +migrate Down
DROP TRIGGER turns_transcript_change ON turns;
DROP TRIGGER turns_transcript_delete ON turns;
DROP TRIGGER shadow_turns_row_change ON turns;
DROP TRIGGER shadow_turns_row_delete ON turns;
DROP TRIGGER turns_sync_destination_change ON turns;
DROP TRIGGER turns_sync_destination_delete ON turns;
INSERT INTO thread_queue (queue_item_id, conversation_id, message, principal, stash_at_unix_ns, position, park_after, slack_channel, slack_ts, kind, content, source, slack_reply, inbound_json)
SELECT input_id, conversation_id, message, principal, created_at_unix_ns, position, park_after, slack_channel, slack_ts, delivery, content, source, slack_reply, inbound_json
FROM turns WHERE phase = 'waiting';
DELETE FROM turns WHERE phase = 'waiting';
DROP INDEX turns_waiting_input;
ALTER TABLE turns DROP CONSTRAINT turns_phase_check, DROP CONSTRAINT turns_reason_check, DROP CONSTRAINT turns_kind_check,
    DROP COLUMN reason, DROP COLUMN delivery, DROP COLUMN input_id, DROP COLUMN position, DROP COLUMN park_after, DROP COLUMN message,
    DROP COLUMN principal, DROP COLUMN source, DROP COLUMN content, DROP COLUMN slack_reply, DROP COLUMN slack_channel, DROP COLUMN slack_ts,
    DROP COLUMN takes_lane, DROP COLUMN kind,
    ALTER COLUMN id DROP DEFAULT, ALTER COLUMN inbound_json SET NOT NULL, ALTER COLUMN output_trace_json DROP DEFAULT;
ALTER INDEX turns_pkey RENAME TO active_turns_pkey;
ALTER INDEX turns_conversation_updated RENAME TO active_turns_conversation_updated;
ALTER INDEX turns_search_text RENAME TO active_turns_search_text;
ALTER TABLE turns RENAME TO active_turns;

-- +migrate StatementBegin
CREATE OR REPLACE FUNCTION turn_trace(TEXT, TEXT) RETURNS jsonb LANGUAGE sql STABLE SET search_path FROM CURRENT AS $$
    WITH turn AS (
        SELECT conversation_id FROM active_turns WHERE id = $1
    )
    SELECT $2::jsonb || COALESCE(jsonb_agg(s.value::jsonb ORDER BY s.id), '[]'::jsonb) FROM turn t
    JOIN steps s ON s.conversation_id = t.conversation_id AND starts_with(s.key, $1 || '/code_mode/')
$$;
-- +migrate StatementEnd

CREATE TRIGGER active_turns_transcript_change AFTER INSERT OR UPDATE OF conversation_id, output_trace_json, terminal, phase, created_at_unix_ns, history_anchor_id, replay, record
    OR DELETE ON active_turns FOR EACH ROW EXECUTE FUNCTION notify_transcript_change();
CREATE TRIGGER shadow_active_change AFTER INSERT OR UPDATE OF output_trace_json, terminal, phase, replay, record OR DELETE ON active_turns
    FOR EACH ROW EXECUTE FUNCTION notify_shadow_change();
CREATE TRIGGER active_turns_sync_destination_change AFTER INSERT OR UPDATE OF output_trace_json, terminal, phase OR DELETE ON active_turns
    FOR EACH ROW EXECUTE FUNCTION notify_sync_destination_change();
