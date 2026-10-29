-- +migrate Up
-- A remote OpenCode session waits on a person: it or a session under it has a pending form, such as a question, or
-- a pending permission request.
ALTER TABLE conversations ADD COLUMN asking BOOLEAN NOT NULL DEFAULT FALSE;

-- +migrate Down
ALTER TABLE conversations DROP COLUMN asking;
