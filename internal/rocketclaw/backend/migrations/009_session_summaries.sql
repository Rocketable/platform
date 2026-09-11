-- +migrate Up
CREATE TABLE session_summaries (
    conversation_id TEXT PRIMARY KEY,
    preview BYTEA NOT NULL,
    last_updated TIMESTAMPTZ NOT NULL
);
CREATE INDEX session_summaries_updated ON session_summaries (last_updated DESC, conversation_id COLLATE "C");

-- +migrate Down
DROP TABLE session_summaries;
