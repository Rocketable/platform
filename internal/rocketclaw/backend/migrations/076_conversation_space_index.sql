-- +migrate Up
CREATE INDEX conversations_space ON conversations (space_id) WHERE space_id IS NOT NULL;

-- +migrate Down
DROP INDEX conversations_space;
