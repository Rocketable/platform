-- +migrate Up
-- What each message to a draft's test chat was sent with, by the input id History shows for it: each
-- column's version, the live file's version, the columns that did not answer, and its External MCP mode,
-- pairs and caller. A chat's first message, the lowest seq, fixes its mode and keys.
CREATE TABLE agent_draft_messages (
    conversation_id TEXT NOT NULL REFERENCES conversations (conversation_id) ON DELETE CASCADE,
    input_id TEXT NOT NULL,
    seq BIGINT GENERATED ALWAYS AS IDENTITY,
    message JSONB NOT NULL,
    PRIMARY KEY (conversation_id, input_id)
);

-- +migrate Down
DROP TABLE agent_draft_messages;
