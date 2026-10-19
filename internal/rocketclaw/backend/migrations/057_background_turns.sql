-- +migrate Up
-- 049 refused running Background Jobs at the start of the release; one started since would lose its
-- runner here. A refusal commits nothing.
-- +migrate StatementBegin
DO $$
DECLARE
    running BIGINT;
BEGIN
    SELECT count(*) INTO running FROM background_jobs WHERE status = 'running';
    IF running > 0 THEN
        RAISE EXCEPTION '057_background_turns: % Background Jobs are running; stop them first', running;
    END IF;
END $$;
-- +migrate StatementEnd

-- A Background Job is a turn of its conversation that does not take the lane: a script, or a task's
-- note, which waits for its subagent session's turns. Its row keeps the job ID as input ID, its origin
-- as inbound, a task's prompt as message, its result, its finish time as updated_at, and its Completion
-- Note: its state, whether it may wake the conversation, its failed wakes, the run-at time of the next
-- wake, and the turn that claimed it. A job reports where its conversation reports (reports_to).
-- Bulk moves must not notify per row; the triggers come back below, those of the lane's turns only for them.
DROP TRIGGER background_jobs_transcript_change ON background_jobs;
DROP TRIGGER turns_transcript_change ON turns;
DROP TRIGGER shadow_turns_row_change ON turns;
DROP TRIGGER shadow_turns_row_delete ON turns;
DROP TRIGGER turns_sync_destination_change ON turns;
DROP TRIGGER turns_sync_destination_delete ON turns;
ALTER TABLE turns ADD COLUMN agent TEXT, ADD COLUMN label TEXT, ADD COLUMN call_id TEXT, ADD COLUMN subagent_key TEXT, ADD COLUMN runner_id TEXT,
    ADD COLUMN note TEXT, ADD COLUMN wake BOOLEAN, ADD COLUMN wake_attempts INTEGER, ADD COLUMN run_at_unix_ns BIGINT, ADD COLUMN claimed_by TEXT, ADD COLUMN result TEXT,
    ADD CONSTRAINT turns_note_check CHECK (takes_lane = (kind = 'model') AND takes_lane = (note IS NULL) AND note IN ('none', 'pending', 'consumed'));
-- As background_jobs' key did, a job ID names one job of its conversation.
CREATE UNIQUE INDEX turns_job ON turns (conversation_id, input_id) WHERE NOT takes_lane;
CREATE INDEX turns_pending_notes ON turns (conversation_id) WHERE note = 'pending';

INSERT INTO turns (conversation_id, phase, terminal, kind, takes_lane, input_id, agent, label, call_id, subagent_key, inbound_json, runner_id, note, wake,
    wake_attempts, run_at_unix_ns, claimed_by, created_at_unix_ns, updated_at_unix_ns, result, message)
SELECT conversation_id, 'done', CASE WHEN status = 'completed' THEN '' ELSE status END, CASE WHEN kind = 'execute' THEN 'script' ELSE 'note' END, FALSE, job_id,
    agent, label, call_id, subagent_key, origin_json, runner_id, note_state, wake, wake_attempts, wake_after_unix_ns, claimed_turn_id, created_at_unix_ns,
    finished_at_unix_ns, result, input
FROM background_jobs;

-- The self-check: every job keeps 049's count per state and note, every moved value is on its turn,
-- and every pending note reports where its job's destination was.
-- +migrate StatementBegin
DO $$
DECLARE
    mismatches BIGINT;
BEGIN
    WITH moved AS (
        SELECT conversation_id, job_id, CASE WHEN kind = 'execute' THEN 'script' ELSE 'note' END, CASE WHEN status = 'completed' THEN '' ELSE status END,
            agent, label, call_id, subagent_key, md5(origin_json::text), runner_id, note_state, wake, wake_attempts, wake_after_unix_ns, claimed_turn_id,
            created_at_unix_ns, finished_at_unix_ns, md5(result), md5(input) FROM background_jobs
    ), turned AS (
        SELECT conversation_id, input_id, kind, terminal, agent, label, call_id, subagent_key, md5(inbound_json::text), runner_id, note, wake, wake_attempts,
            run_at_unix_ns, claimed_by, created_at_unix_ns, updated_at_unix_ns, md5(result), md5(message) FROM turns WHERE NOT takes_lane AND phase = 'done'
    ), states AS (
        SELECT 'background_jobs' AS table_name, CASE WHEN terminal = '' THEN 'completed' ELSE terminal END || '/' || note AS state, count(*) AS n
        FROM turns WHERE NOT takes_lane GROUP BY 2
    ), manifest AS (
        SELECT table_name, state, n FROM legacy_row_counts WHERE table_name = 'background_jobs'
    ), destinations AS (
        SELECT j.conversation_id || ' ' || j.job_id AS job FROM background_jobs j LEFT JOIN conversations c USING (conversation_id)
        WHERE j.note_state = 'pending' AND j.sync_destination <> COALESCE(c.reports_to, '')
    ), diffs AS (
        (SELECT md5(moved::text) FROM moved EXCEPT SELECT md5(turned::text) FROM turned)
        UNION ALL (SELECT md5(turned::text) FROM turned EXCEPT SELECT md5(moved::text) FROM moved)
        UNION ALL (SELECT md5(states::text) FROM states EXCEPT SELECT md5(manifest::text) FROM manifest)
        UNION ALL (SELECT md5(manifest::text) FROM manifest EXCEPT SELECT md5(states::text) FROM states)
        UNION ALL SELECT job FROM destinations
    )
    SELECT count(*) INTO mismatches FROM diffs;
    IF mismatches > 0 THEN
        RAISE EXCEPTION '057_background_turns: % Background Jobs differ from the baseline manifest or background_jobs', mismatches;
    END IF;
END $$;
-- +migrate StatementEnd

DELETE FROM background_jobs;

CREATE TRIGGER turns_transcript_change AFTER INSERT OR UPDATE OF conversation_id, output_trace_json, terminal, phase, created_at_unix_ns, history_anchor_id, replay, record, note, label
    ON turns FOR EACH ROW WHEN (NEW.phase <> 'waiting') EXECUTE FUNCTION notify_transcript_change();
CREATE TRIGGER shadow_turns_row_change AFTER INSERT OR UPDATE OF output_trace_json, terminal, phase, replay, record
    ON turns FOR EACH ROW WHEN (NEW.phase <> 'waiting' AND NEW.takes_lane) EXECUTE FUNCTION notify_shadow_change();
CREATE TRIGGER shadow_turns_row_delete AFTER DELETE ON turns FOR EACH ROW WHEN (OLD.phase <> 'waiting' AND OLD.takes_lane) EXECUTE FUNCTION notify_shadow_change();
CREATE TRIGGER turns_sync_destination_change AFTER INSERT OR UPDATE OF output_trace_json, terminal, phase
    ON turns FOR EACH ROW WHEN (NEW.phase <> 'waiting' AND NEW.takes_lane) EXECUTE FUNCTION notify_sync_destination_change();
CREATE TRIGGER turns_sync_destination_delete AFTER DELETE ON turns FOR EACH ROW WHEN (OLD.phase <> 'waiting' AND OLD.takes_lane) EXECUTE FUNCTION notify_sync_destination_change();
ANALYZE turns;

-- +migrate Down
DROP TRIGGER turns_transcript_change ON turns;
DROP TRIGGER shadow_turns_row_change ON turns;
DROP TRIGGER shadow_turns_row_delete ON turns;
DROP TRIGGER turns_sync_destination_change ON turns;
DROP TRIGGER turns_sync_destination_delete ON turns;
INSERT INTO background_jobs (conversation_id, job_id, kind, status, agent, label, call_id, subagent_key, origin_json, sync_destination, runner_id, note_state, wake,
    wake_attempts, wake_after_unix_ns, claimed_turn_id, created_at_unix_ns, finished_at_unix_ns, result, input)
SELECT t.conversation_id, t.input_id, CASE WHEN t.kind = 'script' THEN 'execute' ELSE 'task' END,
    CASE WHEN t.phase = 'running' THEN 'running' WHEN t.terminal = '' THEN 'completed' ELSE t.terminal END, t.agent, t.label, t.call_id, t.subagent_key,
    t.inbound_json, COALESCE(c.reports_to, ''), t.runner_id, t.note, t.wake, t.wake_attempts, t.run_at_unix_ns, t.claimed_by, t.created_at_unix_ns,
    CASE WHEN t.phase = 'running' THEN 0 ELSE t.updated_at_unix_ns END, t.result, t.message
FROM turns t LEFT JOIN conversations c USING (conversation_id) WHERE NOT t.takes_lane;
DELETE FROM turns WHERE NOT takes_lane;
DROP INDEX turns_job;
DROP INDEX turns_pending_notes;
ALTER TABLE turns DROP CONSTRAINT turns_note_check, DROP COLUMN agent, DROP COLUMN label, DROP COLUMN call_id, DROP COLUMN subagent_key, DROP COLUMN runner_id,
    DROP COLUMN note, DROP COLUMN wake, DROP COLUMN wake_attempts, DROP COLUMN run_at_unix_ns, DROP COLUMN claimed_by, DROP COLUMN result;
CREATE TRIGGER turns_transcript_change AFTER INSERT OR UPDATE OF conversation_id, output_trace_json, terminal, phase, created_at_unix_ns, history_anchor_id, replay, record
    ON turns FOR EACH ROW WHEN (NEW.phase <> 'waiting') EXECUTE FUNCTION notify_transcript_change();
CREATE TRIGGER shadow_turns_row_change AFTER INSERT OR UPDATE OF output_trace_json, terminal, phase, replay, record
    ON turns FOR EACH ROW WHEN (NEW.phase <> 'waiting') EXECUTE FUNCTION notify_shadow_change();
CREATE TRIGGER shadow_turns_row_delete AFTER DELETE ON turns FOR EACH ROW WHEN (OLD.phase <> 'waiting') EXECUTE FUNCTION notify_shadow_change();
CREATE TRIGGER turns_sync_destination_change AFTER INSERT OR UPDATE OF output_trace_json, terminal, phase
    ON turns FOR EACH ROW WHEN (NEW.phase <> 'waiting') EXECUTE FUNCTION notify_sync_destination_change();
CREATE TRIGGER turns_sync_destination_delete AFTER DELETE ON turns FOR EACH ROW WHEN (OLD.phase <> 'waiting') EXECUTE FUNCTION notify_sync_destination_change();
CREATE TRIGGER background_jobs_transcript_change AFTER INSERT OR UPDATE OF status, note_state, label OR DELETE ON background_jobs
    FOR EACH ROW EXECUTE FUNCTION notify_transcript_change();
