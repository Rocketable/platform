-- +migrate Up
CREATE TABLE web_closed_tabs (
    username TEXT NOT NULL,
    path TEXT NOT NULL,
    conversation_id TEXT REFERENCES conversations (conversation_id) ON DELETE CASCADE,
    space TEXT NOT NULL,
    search JSONB,
    closed_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (username, path)
);
CREATE INDEX web_closed_tabs_conversation ON web_closed_tabs (conversation_id);

-- +migrate Down
DROP TABLE web_closed_tabs;
