-- +migrate Up
CREATE INDEX session_entries_sync_source_id ON session_entries ((entry_json::jsonb->>'sync_source_entry_id'))
WHERE entry_json::jsonb->>'sync_source_entry_id' IS NOT NULL;

-- +migrate Down
DROP INDEX session_entries_sync_source_id;
