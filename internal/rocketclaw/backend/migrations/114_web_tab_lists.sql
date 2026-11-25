-- +migrate Up
CREATE TABLE web_tab_lists (
    username TEXT NOT NULL,
    list TEXT NOT NULL,
    server TEXT NOT NULL,
    path TEXT NOT NULL,
    conversation_id TEXT REFERENCES conversations (conversation_id) ON DELETE CASCADE,
    space TEXT NOT NULL,
    position INTEGER NOT NULL,
    PRIMARY KEY (username, list, server, path)
);
CREATE INDEX web_tab_lists_conversation ON web_tab_lists (conversation_id);

-- +migrate Down
DROP TABLE web_tab_lists;
