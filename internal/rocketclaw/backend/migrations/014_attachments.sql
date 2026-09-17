-- +migrate Up
CREATE TABLE attachments (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    name TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    size BIGINT NOT NULL,
    original_unverified BOOLEAN NOT NULL,
    upload BOOLEAN NOT NULL
);

-- +migrate Down
DROP TABLE attachments;
