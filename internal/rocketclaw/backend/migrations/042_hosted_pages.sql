-- +migrate Up
-- Hosted Page files and Page Data entries, owned by the agent that published them.
CREATE TABLE hosted_pages (
    agent TEXT NOT NULL,
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    content_type TEXT NOT NULL,
    body BYTEA NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (agent, kind, name)
);

-- +migrate Down
DROP TABLE hosted_pages;
