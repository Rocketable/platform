-- +migrate Up
DROP INDEX session_entries_sync_source_id;
ALTER TABLE entries DROP COLUMN audience;

-- +migrate Down
ALTER TABLE entries ADD COLUMN audience text NOT NULL DEFAULT 'both' CONSTRAINT session_entries_audience_check CHECK (audience IN ('both', 'sandbox', 'visible'));
CREATE INDEX session_entries_sync_source_id ON entries USING btree (((entry_json::jsonb ->> 'sync_source_entry_id'))) WHERE ((entry_json::jsonb ->> 'sync_source_entry_id') IS NOT NULL);
