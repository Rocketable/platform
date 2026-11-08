-- +migrate Up
-- sticky is how the chat was born: off, or the sticky chat of its agent for one person (user) or for
-- everyone (global). It never changes, so a chat born off never becomes a sticky chat.
ALTER TABLE conversations ADD COLUMN sticky TEXT NOT NULL DEFAULT 'off' CHECK (sticky IN ('off', 'user', 'global'));
CREATE UNIQUE INDEX conversations_sticky_user ON conversations (agent, created_by) WHERE sticky = 'user';
CREATE UNIQUE INDEX conversations_sticky_global ON conversations (agent) WHERE sticky = 'global';

-- +migrate Down
DROP INDEX conversations_sticky_global;
DROP INDEX conversations_sticky_user;
ALTER TABLE conversations DROP COLUMN sticky;
