-- +migrate Up
-- A session whose desktop notifications a person muted: until a time when until is set, else until
-- unmuted. Deleting the session clears its mutes.
CREATE TABLE web_tab_mutes (
    username TEXT NOT NULL,
    conversation_id TEXT NOT NULL REFERENCES conversations (conversation_id) ON DELETE CASCADE,
    until TIMESTAMPTZ,
    PRIMARY KEY (username, conversation_id)
);
CREATE INDEX web_tab_mutes_conversation ON web_tab_mutes (conversation_id);

-- +migrate Down
DROP TABLE web_tab_mutes;
