-- +migrate Up
SET LOCAL lock_timeout = '5s';
ALTER TABLE message_search ADD COLUMN principal TEXT NOT NULL DEFAULT '';
CREATE INDEX message_search_principal ON message_search (principal, conversation_id) WHERE principal <> '';
DELETE FROM message_search_indexed;

-- +migrate Down
DROP INDEX message_search_principal;
ALTER TABLE message_search DROP COLUMN principal;
