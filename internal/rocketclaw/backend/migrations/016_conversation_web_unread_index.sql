-- +migrate Up
CREATE INDEX managed_conversations_web_unread ON managed_conversations (web_unread);

-- +migrate Down
DROP INDEX managed_conversations_web_unread;
