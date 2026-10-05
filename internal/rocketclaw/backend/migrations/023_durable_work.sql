-- +migrate Up
DROP TABLE pending_restart_notifications;
DROP TRIGGER active_turns_transcript_change ON active_turns;
DELETE FROM active_turns;
ALTER TABLE active_turns
    DROP COLUMN agent, DROP COLUMN model, DROP COLUMN display_model, DROP COLUMN replay_input_json,
    DROP COLUMN token_usage_json, DROP COLUMN response_id, DROP COLUMN open_function_calls_json,
    DROP COLUMN completed_function_outputs_json, DROP COLUMN restart_notice_json, DROP COLUMN source_metadata_json,
    DROP COLUMN pending_steers_json, DROP COLUMN replay_attribution_json, DROP COLUMN reasoning_effort_json,
    ADD COLUMN inbound_json JSON NOT NULL,
    ADD COLUMN phase TEXT NOT NULL DEFAULT 'running',
    ADD COLUMN outbound_json JSON;
CREATE TRIGGER active_turns_transcript_change AFTER INSERT OR UPDATE OF conversation_id, output_trace_json, terminal, phase, created_at_unix_ns, history_anchor_id OR DELETE ON active_turns FOR EACH ROW EXECUTE FUNCTION notify_transcript_change();

CREATE TABLE turn_steps (conversation_id TEXT NOT NULL, key TEXT NOT NULL, value JSON NOT NULL, PRIMARY KEY (conversation_id, key));
CREATE TRIGGER turn_steps_transcript_change AFTER INSERT OR UPDATE ON turn_steps FOR EACH ROW WHEN (strpos(NEW.key, '/') = 0) EXECUTE FUNCTION notify_transcript_change();

ALTER TABLE thread_queue ADD COLUMN inbound_json JSON;

-- +migrate Down
ALTER TABLE thread_queue DROP COLUMN inbound_json;
DROP TABLE turn_steps;
DROP TRIGGER active_turns_transcript_change ON active_turns;
DELETE FROM active_turns;
ALTER TABLE active_turns
    DROP COLUMN inbound_json, DROP COLUMN phase, DROP COLUMN outbound_json,
    ADD COLUMN agent TEXT NOT NULL, ADD COLUMN model TEXT NOT NULL, ADD COLUMN display_model TEXT NOT NULL,
    ADD COLUMN replay_input_json TEXT NOT NULL, ADD COLUMN token_usage_json TEXT NOT NULL, ADD COLUMN response_id TEXT NOT NULL,
    ADD COLUMN open_function_calls_json TEXT NOT NULL, ADD COLUMN completed_function_outputs_json TEXT NOT NULL,
    ADD COLUMN restart_notice_json TEXT NOT NULL, ADD COLUMN source_metadata_json TEXT NOT NULL,
    ADD COLUMN pending_steers_json TEXT NOT NULL DEFAULT '[]', ADD COLUMN replay_attribution_json TEXT NOT NULL DEFAULT '[]',
    ADD COLUMN reasoning_effort_json TEXT NOT NULL DEFAULT 'null';
CREATE TRIGGER active_turns_transcript_change AFTER INSERT OR UPDATE OF conversation_id, agent, display_model, reasoning_effort_json, replay_input_json, replay_attribution_json, output_trace_json, token_usage_json, response_id, terminal, created_at_unix_ns, history_anchor_id OR DELETE ON active_turns FOR EACH ROW EXECUTE FUNCTION notify_transcript_change();
CREATE TABLE pending_restart_notifications (conversation_id TEXT PRIMARY KEY);
