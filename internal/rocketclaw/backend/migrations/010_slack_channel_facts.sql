-- +migrate Up
CREATE TABLE slack_channel_facts (
    workspace_id TEXT NOT NULL,
    channel_id TEXT NOT NULL,
    name TEXT NOT NULL,
    observed_at BIGINT NOT NULL,
    PRIMARY KEY (workspace_id, channel_id)
);

-- +migrate Down
DROP TABLE slack_channel_facts;
