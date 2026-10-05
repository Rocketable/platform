-- +migrate Up
CREATE INDEX session_entries_conversation_id_c ON session_entries (conversation_id COLLATE "C");
CREATE INDEX session_summaries_conversation_id_c ON session_summaries (conversation_id COLLATE "C");

-- +migrate Down
DROP INDEX session_summaries_conversation_id_c;
DROP INDEX session_entries_conversation_id_c;
