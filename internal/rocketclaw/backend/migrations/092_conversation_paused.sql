-- +migrate Up
-- A Web chat whose agent is paused: people talk there without the agent taking a turn until someone resumes it.
ALTER TABLE conversations ADD COLUMN paused BOOLEAN NOT NULL DEFAULT FALSE;

-- +migrate Down
ALTER TABLE conversations DROP COLUMN paused;
