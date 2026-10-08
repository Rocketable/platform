-- +migrate Up
CREATE INDEX managed_conversations_reverted ON managed_conversations (conversation_id)
WHERE revert_message_id <> '';

-- +migrate Down
DROP INDEX managed_conversations_reverted;
