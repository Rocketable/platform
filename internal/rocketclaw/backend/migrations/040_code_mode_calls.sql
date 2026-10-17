-- +migrate Up
-- A Code Mode record, one call an execute script made or the approver's decision on the whole script,
-- has a row of its own while its turn runs: it is written when its call starts and when it finishes,
-- so the turn's trace saves never rewrite it. The finished turn keeps its records in its entry.
CREATE TABLE code_mode_calls (
    turn_id TEXT NOT NULL REFERENCES active_turns (id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    id BIGINT GENERATED ALWAYS AS IDENTITY,
    record JSON NOT NULL,
    PRIMARY KEY (turn_id, key)
);
-- turn_trace is an unfinished turn's trace ($2) followed by its Code Mode records in the order first saved.
-- +migrate StatementBegin
CREATE FUNCTION turn_trace(TEXT, TEXT) RETURNS jsonb LANGUAGE sql STABLE SET search_path FROM CURRENT AS $$
    SELECT $2::jsonb || COALESCE(jsonb_agg(record::jsonb ORDER BY id), '[]'::jsonb) FROM code_mode_calls WHERE turn_id = $1
$$;
-- +migrate StatementEnd

-- +migrate Down
DROP FUNCTION turn_trace(TEXT, TEXT);
DROP TABLE code_mode_calls;
