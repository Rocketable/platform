-- +migrate Up
-- Repeating rules share one table: a cron file's schedule line (cron_path), or a recurring reminder
-- of a conversation, each with its next due time. A one-time reminder is a waiting turn of its
-- conversation, scheduled for its run-at time, with the reminder's ID as input ID; it reaches the
-- lane as a prompt. A cron line skips while a turn of a run of its file is running, which
-- replaces cron_schedule_runs. Startup rebuilds cron lines from their files, so no cron state moves
-- and no recurring cron run missed during the downtime is made up. Waiting turns do not notify, so
-- the turns triggers stay.
CREATE TABLE schedules (
    id TEXT PRIMARY KEY, cron_path TEXT, conversation_id TEXT, agent TEXT, message TEXT, interval_ns BIGINT, next_due_unix_ns BIGINT NOT NULL,
    CHECK ((cron_path IS NULL) = (conversation_id IS NOT NULL AND agent IS NOT NULL AND message IS NOT NULL AND interval_ns IS NOT NULL))
);
ALTER TABLE turns DROP CONSTRAINT turns_reason_check, ADD CONSTRAINT turns_reason_check CHECK ((phase = 'waiting') = (reason IS NOT NULL)
    AND reason IN ('queued', 'stashed', 'held', 'scheduled') AND (reason IN ('queued', 'scheduled')) = (delivery <> 'held'));

INSERT INTO schedules (id, conversation_id, agent, message, interval_ns, next_due_unix_ns)
SELECT scheduled_message_id, conversation_id, agent, message, interval_ns, due_at_unix_ns FROM scheduled_messages WHERE recurring <> 0;
INSERT INTO turns (conversation_id, phase, reason, delivery, input_id, agent, message, run_at_unix_ns, created_at_unix_ns, updated_at_unix_ns)
SELECT conversation_id, 'waiting', 'scheduled', 'prompt', scheduled_message_id, agent, message, due_at_unix_ns, due_at_unix_ns, due_at_unix_ns
FROM scheduled_messages WHERE recurring = 0;

-- The self-check: every reminder keeps every value, and a one-time reminder has no interval. 049's
-- manifest counted reminders before 049 itself applied pending schedule effects, so the source is
-- scheduled_messages as it is here.
-- +migrate StatementBegin
DO $$
DECLARE
    mismatches BIGINT;
BEGIN
    WITH moved AS (
        SELECT scheduled_message_id, conversation_id, agent, md5(message), due_at_unix_ns, recurring <> 0, interval_ns FROM scheduled_messages
    ), kept AS (
        SELECT id, conversation_id, agent, md5(message), next_due_unix_ns, TRUE, interval_ns FROM schedules
        UNION ALL SELECT input_id, conversation_id, agent, md5(message), run_at_unix_ns, FALSE, 0 FROM turns WHERE phase = 'waiting' AND reason = 'scheduled'
    ), diffs AS (
        (SELECT md5(moved::text) FROM moved EXCEPT SELECT md5(kept::text) FROM kept)
        UNION ALL (SELECT md5(kept::text) FROM kept EXCEPT SELECT md5(moved::text) FROM moved)
    )
    SELECT count(*) INTO mismatches FROM diffs;
    IF mismatches > 0 THEN
        RAISE EXCEPTION '058_schedules: % reminders differ from scheduled_messages', mismatches;
    END IF;
END $$;
-- +migrate StatementEnd

DELETE FROM scheduled_messages;
DELETE FROM cron_schedules;
DELETE FROM cron_schedule_runs;

-- +migrate Down
INSERT INTO scheduled_messages (scheduled_message_id, conversation_id, agent, message, due_at_unix_ns, recurring, interval_ns)
SELECT id, conversation_id, agent, message, next_due_unix_ns, 1, interval_ns FROM schedules WHERE cron_path IS NULL
UNION ALL SELECT input_id, conversation_id, agent, message, run_at_unix_ns, 0, 0 FROM turns WHERE phase = 'waiting' AND reason = 'scheduled';
DELETE FROM turns WHERE phase = 'waiting' AND reason = 'scheduled';
ALTER TABLE turns DROP CONSTRAINT turns_reason_check, ADD CONSTRAINT turns_reason_check CHECK ((phase = 'waiting') = (reason IS NOT NULL)
    AND reason IN ('queued', 'stashed', 'held') AND (reason = 'queued') = (delivery <> 'held'));
DROP TABLE schedules;
