-- +migrate Up
-- Agents no longer have guardrails; their saved runs would otherwise replay into subagent sessions.
DELETE FROM entries WHERE entry_json::jsonb->>'type' = 'guardrail';

-- +migrate Down
