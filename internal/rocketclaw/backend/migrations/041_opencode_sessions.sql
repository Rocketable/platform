-- +migrate Up
-- Remote OpenCode root sessions the Web lists. RocketClaw keeps their pins and names here,
-- outside managed_conversations, so startup pruning and message search never see them.
CREATE TABLE opencode_sessions (
    conversation_id TEXT PRIMARY KEY,
    server TEXT NOT NULL,
    title TEXT NOT NULL,
    directory TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    running BOOLEAN NOT NULL,
    synced_at TIMESTAMPTZ NOT NULL,
    pinned BOOLEAN NOT NULL DEFAULT FALSE,
    name TEXT NOT NULL DEFAULT ''
);
CREATE INDEX opencode_sessions_server ON opencode_sessions (server);

-- +migrate Down
DROP TABLE opencode_sessions;
