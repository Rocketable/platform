-- +migrate Up
UPDATE session_entries SET entry_json = (
    SELECT string_agg(replace(part, $$\u0000$$, ''), $$\\$$ ORDER BY ordinal)
    FROM unnest(string_to_array(entry_json, $$\\$$)) WITH ORDINALITY AS parts(part, ordinal)
);
ALTER TABLE session_entries ALTER COLUMN entry_json TYPE json
    USING entry_json::json;
ALTER TABLE session_entries ADD CONSTRAINT session_entry_unicode CHECK (entry_json::jsonb IS NOT NULL);
DELETE FROM session_summaries WHERE position(decode('00', 'hex') IN preview) > 0;

-- +migrate Down
ALTER TABLE session_entries DROP CONSTRAINT session_entry_unicode;
ALTER TABLE session_entries ALTER COLUMN entry_json TYPE text USING entry_json::text;
