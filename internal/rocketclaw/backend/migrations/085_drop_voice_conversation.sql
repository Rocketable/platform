-- +migrate Up
ALTER TABLE voice_settings DROP COLUMN conversation_id;

-- +migrate Down
ALTER TABLE voice_settings ADD COLUMN conversation_id TEXT REFERENCES conversations(conversation_id) ON DELETE SET NULL;
