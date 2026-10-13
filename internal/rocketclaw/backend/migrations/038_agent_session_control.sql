-- +migrate Up
ALTER TABLE managed_conversations ADD COLUMN spawned_by TEXT NOT NULL DEFAULT '', ADD COLUMN parent_conversation_id TEXT NOT NULL DEFAULT '';
-- A subagent's turns read only its task turns, as they did before it had a session row: its history,
-- with no row yet, nests under another session's; its other entries are skipped as guardrail runs.
-- MATERIALIZED keeps the turn ID read off entries outside subagent histories.
WITH histories AS (
    SELECT DISTINCT conversation_id FROM session_entries
), unmanaged AS (
    SELECT h.conversation_id FROM histories h LEFT JOIN managed_conversations m USING (conversation_id) WHERE m.conversation_id IS NULL
), known AS (
    SELECT conversation_id FROM managed_conversations UNION SELECT conversation_id FROM histories
), parents AS (
    SELECT u.conversation_id, left(u.conversation_id, i - 1) AS parent_id FROM unmanaged u
    CROSS JOIN LATERAL generate_series(2, length(u.conversation_id)) AS i WHERE substr(u.conversation_id, i, 1) = '/'
), subagents AS (
    SELECT DISTINCT p.conversation_id FROM parents p JOIN known k ON k.conversation_id = p.parent_id
), subagent_entries AS MATERIALIZED (
    SELECT e.id, COALESCE(e.entry_json->>'turn_id', '') AS turn_id FROM session_entries e JOIN subagents s USING (conversation_id)
)
UPDATE session_entries e SET entry_json = jsonb_set(e.entry_json::jsonb, '{type}', '"guardrail"')::json FROM subagent_entries s
WHERE e.id = s.id AND s.turn_id NOT LIKE '%/task';
-- A subagent is a session that owns its Background Jobs, with the session row task creates for it,
-- under the deepest known session it nests in.
DELETE FROM background_jobs WHERE kind = 'subagent_wake';
WITH subagents AS (
    SELECT DISTINCT ON (conversation_id || child_key) conversation_id || child_key AS id, conversation_id AS root, agent FROM background_jobs
    WHERE child_key <> '' ORDER BY conversation_id || child_key, created_at_unix_ns DESC
), known AS (
    SELECT conversation_id FROM managed_conversations UNION SELECT conversation_id FROM session_entries
    UNION SELECT id FROM subagents UNION SELECT root FROM subagents
), parents AS (
    SELECT DISTINCT ON (s.id) s.id, s.agent, left(s.id, i - 1) AS parent_id FROM subagents s
    CROSS JOIN LATERAL generate_series(2, length(s.id)) AS i JOIN known k ON k.conversation_id = left(s.id, i - 1)
    WHERE substr(s.id, i, 1) = '/' ORDER BY s.id, i DESC
)
INSERT INTO managed_conversations (conversation_id, agent, created_by, parent_conversation_id)
SELECT id, agent, '', parent_id FROM parents
ON CONFLICT (conversation_id) DO NOTHING;
UPDATE background_jobs SET conversation_id = conversation_id || child_key WHERE child_key <> '';
DROP INDEX background_jobs_running_subagent;
DROP INDEX background_jobs_pending_notes;
ALTER TABLE background_jobs DROP COLUMN child_key, ADD COLUMN input TEXT NOT NULL DEFAULT '', DROP CONSTRAINT background_jobs_kind_check,
    ADD CONSTRAINT background_jobs_kind_check CHECK (kind IN ('execute', 'task'));
CREATE INDEX background_jobs_pending_notes ON background_jobs (conversation_id) WHERE note_state = 'pending';

-- +migrate Down
DROP INDEX background_jobs_pending_notes;
ALTER TABLE background_jobs DROP COLUMN input, ADD COLUMN child_key TEXT NOT NULL DEFAULT '', DROP CONSTRAINT background_jobs_kind_check,
    ADD CONSTRAINT background_jobs_kind_check CHECK (kind IN ('execute', 'task', 'subagent_wake'));
CREATE INDEX background_jobs_pending_notes ON background_jobs (conversation_id, child_key) WHERE note_state = 'pending';
WITH waits AS (
    SELECT conversation_id, job_id, row_number() OVER (PARTITION BY conversation_id, subagent_key ORDER BY created_at_unix_ns, job_id) AS n
    FROM background_jobs WHERE status = 'running' AND kind <> 'execute'
)
UPDATE background_jobs j SET status = 'killed', note_state = 'pending' FROM waits w WHERE (j.conversation_id, j.job_id) = (w.conversation_id, w.job_id) AND w.n > 1;
CREATE UNIQUE INDEX background_jobs_running_subagent ON background_jobs (conversation_id, subagent_key) WHERE status = 'running' AND kind <> 'execute';
DELETE FROM managed_conversations WHERE parent_conversation_id <> '';
ALTER TABLE managed_conversations DROP COLUMN parent_conversation_id, DROP COLUMN spawned_by;
