-- +migrate Up
CREATE TABLE session_tags (
    conversation_id TEXT PRIMARY KEY,
    tags JSONB NOT NULL
);

-- +migrate Down
DROP TABLE session_tags;
