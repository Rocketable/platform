-- +migrate Up
-- The hidden run a Web chat continues: the chat shows the run's visible entries before its own.
-- Every existing chat starts with none; one that copied a run's history keeps its copies.
ALTER TABLE managed_conversations ADD COLUMN continues TEXT;

-- +migrate Down
ALTER TABLE managed_conversations DROP COLUMN continues;
