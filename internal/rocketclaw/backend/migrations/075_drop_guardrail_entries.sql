-- +migrate Up
DELETE FROM entries WHERE entry_json::jsonb->>'type' = 'guardrail';

-- +migrate Down
