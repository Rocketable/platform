-- +migrate Up
-- Bound waits for the new foreign keys' locks on tables the previous binary may still write.
SET LOCAL lock_timeout = '5s';
-- Schemas sharing one database must not race to create the extension.
SELECT pg_advisory_xact_lock(hashtext('rocketclaw pg_trgm extension'));
CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;

CREATE TABLE message_search (
    conversation_id TEXT NOT NULL,
    entry_id BIGINT REFERENCES session_entries (id) ON DELETE CASCADE,
    turn_id TEXT REFERENCES active_turns (id) ON DELETE CASCADE,
    replay_index INTEGER NOT NULL,
    part_index INTEGER NOT NULL,
    role TEXT NOT NULL,
    text TEXT NOT NULL,
    text_lower TEXT NOT NULL,
    CHECK ((entry_id IS NULL) <> (turn_id IS NULL))
);
CREATE UNIQUE INDEX message_search_entry ON message_search (entry_id, replay_index, part_index) WHERE entry_id IS NOT NULL;
CREATE UNIQUE INDEX message_search_turn ON message_search (turn_id, replay_index, part_index) WHERE turn_id IS NOT NULL;
CREATE INDEX message_search_conversation ON message_search (conversation_id);
CREATE INDEX message_search_text ON message_search USING gin (text_lower public.gin_trgm_ops);

CREATE TABLE message_search_indexed (conversation_id TEXT PRIMARY KEY);

-- +migrate Down
DROP TABLE message_search_indexed;
DROP TABLE message_search;
