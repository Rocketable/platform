-- +migrate Up
ALTER TABLE conversations RENAME COLUMN cron_path TO routine_path;
ALTER TABLE conversations RENAME COLUMN cron_run_at_unix_ns TO routine_run_at_unix_ns;
ALTER TABLE conversations RENAME COLUMN cron_run TO routine_run;
ALTER TABLE schedules RENAME COLUMN cron_path TO routine_path;
ALTER INDEX conversations_cron_runs RENAME TO conversations_routine_runs;

ALTER TABLE conversations DROP CONSTRAINT conversations_kind_check;
UPDATE conversations SET kind = CASE kind WHEN 'cron_run' THEN 'routine_run' ELSE 'one_off_routine_run' END WHERE kind IN ('cron_run', 'one_off_cron_run');
ALTER TABLE conversations ADD CONSTRAINT conversations_kind_check CHECK ((kind = ANY (ARRAY['web_chat'::text, 'slack_thread'::text, 'routine_run'::text, 'one_off_routine_run'::text, 'external_mcp_run'::text, 'subagent'::text, 'review'::text, 'shadow'::text, 'remote'::text, 'agent_draft'::text])));
UPDATE conversations SET created_by = 'routine' WHERE created_by = 'cron';
UPDATE conversations SET goal_origin = regexp_replace(goal_origin, '^\[(ScheduledMessage source=)?Cron(?=[] ])', '[\1Routine') WHERE goal_origin ~ '^\[(ScheduledMessage source=)?Cron[] ]';
UPDATE schedules SET origin = regexp_replace(origin, '^\[(ScheduledMessage source=)?Cron(?=[] ])', '[\1Routine') WHERE origin ~ '^\[(ScheduledMessage source=)?Cron[] ]';
UPDATE turns SET origin = regexp_replace(origin, '^\[(ScheduledMessage source=)?Cron(?=[] ])', '[\1Routine') WHERE origin ~ '^\[(ScheduledMessage source=)?Cron[] ]';

UPDATE turns SET inbound_json = (inbound_json::jsonb - 'Cronjob' || jsonb_build_object('Routine', inbound_json::jsonb->'Cronjob'))::json
WHERE json_typeof(inbound_json->'Cronjob') = 'object';
UPDATE turns SET outbound_json = (outbound_json::jsonb - 'Cronjob' || jsonb_build_object('Routine', outbound_json::jsonb->'Cronjob'))::json
WHERE json_typeof(outbound_json->'Cronjob') = 'object';
UPDATE shadow_turns SET inbound_json = (inbound_json::jsonb - 'Cronjob' || jsonb_build_object('Routine', inbound_json::jsonb->'Cronjob'))::json
WHERE json_typeof(inbound_json->'Cronjob') = 'object';
UPDATE conversations SET producer_inbound_json = (producer_inbound_json::jsonb - 'Cronjob' || jsonb_build_object('Routine', producer_inbound_json::jsonb->'Cronjob'))::json
WHERE json_typeof(producer_inbound_json->'Cronjob') = 'object';

-- +migrate Down
UPDATE conversations SET producer_inbound_json = (producer_inbound_json::jsonb - 'Routine' || jsonb_build_object('Cronjob', producer_inbound_json::jsonb->'Routine'))::json
WHERE json_typeof(producer_inbound_json->'Routine') = 'object';
UPDATE shadow_turns SET inbound_json = (inbound_json::jsonb - 'Routine' || jsonb_build_object('Cronjob', inbound_json::jsonb->'Routine'))::json
WHERE json_typeof(inbound_json->'Routine') = 'object';
UPDATE turns SET outbound_json = (outbound_json::jsonb - 'Routine' || jsonb_build_object('Cronjob', outbound_json::jsonb->'Routine'))::json
WHERE json_typeof(outbound_json->'Routine') = 'object';
UPDATE turns SET inbound_json = (inbound_json::jsonb - 'Routine' || jsonb_build_object('Cronjob', inbound_json::jsonb->'Routine'))::json
WHERE json_typeof(inbound_json->'Routine') = 'object';

UPDATE turns SET origin = regexp_replace(origin, '^\[(ScheduledMessage source=)?Routine(?=[] ])', '[\1Cron') WHERE origin ~ '^\[(ScheduledMessage source=)?Routine[] ]';
UPDATE schedules SET origin = regexp_replace(origin, '^\[(ScheduledMessage source=)?Routine(?=[] ])', '[\1Cron') WHERE origin ~ '^\[(ScheduledMessage source=)?Routine[] ]';
UPDATE conversations SET goal_origin = regexp_replace(goal_origin, '^\[(ScheduledMessage source=)?Routine(?=[] ])', '[\1Cron') WHERE goal_origin ~ '^\[(ScheduledMessage source=)?Routine[] ]';
UPDATE conversations SET created_by = 'cron' WHERE created_by = 'routine';
ALTER TABLE conversations DROP CONSTRAINT conversations_kind_check;
UPDATE conversations SET kind = CASE kind WHEN 'routine_run' THEN 'cron_run' ELSE 'one_off_cron_run' END WHERE kind IN ('routine_run', 'one_off_routine_run');
ALTER TABLE conversations ADD CONSTRAINT conversations_kind_check CHECK ((kind = ANY (ARRAY['web_chat'::text, 'slack_thread'::text, 'cron_run'::text, 'one_off_cron_run'::text, 'external_mcp_run'::text, 'subagent'::text, 'review'::text, 'shadow'::text, 'remote'::text, 'agent_draft'::text])));

ALTER INDEX conversations_routine_runs RENAME TO conversations_cron_runs;
ALTER TABLE schedules RENAME COLUMN routine_path TO cron_path;
ALTER TABLE conversations RENAME COLUMN routine_run TO cron_run;
ALTER TABLE conversations RENAME COLUMN routine_run_at_unix_ns TO cron_run_at_unix_ns;
ALTER TABLE conversations RENAME COLUMN routine_path TO cron_path;
