-- +migrate Up
-- No permission scope reads which session created another one any more.
ALTER TABLE conversations DROP COLUMN spawned_by;

-- +migrate Down
ALTER TABLE conversations ADD COLUMN spawned_by text DEFAULT ''::text CONSTRAINT managed_conversations_spawned_by_not_null NOT NULL;
