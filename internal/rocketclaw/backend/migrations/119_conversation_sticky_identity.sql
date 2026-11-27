-- +migrate Up
ALTER TABLE conversations DROP CONSTRAINT conversations_sticky_check;
ALTER TABLE conversations ADD CONSTRAINT conversations_sticky_check CHECK (sticky IN ('off', 'user', 'global', 'identity'));
CREATE UNIQUE INDEX conversations_sticky_identity ON conversations (agent, created_by) WHERE sticky = 'identity';

-- +migrate Down
DROP INDEX conversations_sticky_identity;
UPDATE conversations SET sticky = 'off' WHERE sticky = 'identity';
ALTER TABLE conversations DROP CONSTRAINT conversations_sticky_check;
ALTER TABLE conversations ADD CONSTRAINT conversations_sticky_check CHECK (sticky IN ('off', 'user', 'global'));
