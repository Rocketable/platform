-- +migrate Up
ALTER TABLE managed_conversations ADD COLUMN revert_message_id TEXT NOT NULL DEFAULT '';
CREATE TRIGGER managed_conversations_revert_change AFTER UPDATE OF revert_message_id ON managed_conversations
FOR EACH ROW WHEN (OLD.revert_message_id IS DISTINCT FROM NEW.revert_message_id) EXECUTE FUNCTION notify_transcript_change();

-- +migrate Down
DROP TRIGGER managed_conversations_revert_change ON managed_conversations;
ALTER TABLE managed_conversations DROP COLUMN revert_message_id;
