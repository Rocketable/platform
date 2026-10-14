-- +migrate Up
-- Each person's Web voice state follows them across devices: their current voice
-- conversation, the agent for their next one, their voice, and the transcript view.
CREATE TABLE voice_settings (
    username TEXT PRIMARY KEY,
    conversation_id TEXT REFERENCES managed_conversations (conversation_id) ON DELETE SET NULL,
    agent TEXT NOT NULL DEFAULT '',
    voice TEXT NOT NULL DEFAULT '',
    transcript BOOLEAN NOT NULL DEFAULT FALSE
);

-- +migrate Down
DROP TABLE voice_settings;
