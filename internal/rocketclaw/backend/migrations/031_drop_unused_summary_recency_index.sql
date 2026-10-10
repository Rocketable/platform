-- +migrate Up
DROP INDEX session_summaries_updated;

-- +migrate Down
CREATE INDEX session_summaries_updated ON session_summaries (last_updated DESC, conversation_id COLLATE "C");
