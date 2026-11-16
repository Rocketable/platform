-- +migrate Up
CREATE TABLE web_tab_mutes (
    username TEXT NOT NULL,
    conversation_id TEXT NOT NULL REFERENCES conversations (conversation_id) ON DELETE CASCADE,
    until TIMESTAMPTZ,
    PRIMARY KEY (username, conversation_id)
);
CREATE INDEX web_tab_mutes_conversation ON web_tab_mutes (conversation_id);

-- +migrate Down
DROP TABLE web_tab_mutes;
