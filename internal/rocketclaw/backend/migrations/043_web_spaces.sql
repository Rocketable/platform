-- +migrate Up
-- Web spaces are shared by everyone; each person's tabs and groups in them follow
-- that person across devices. A session remembers the space it was born in.
CREATE TABLE web_spaces (
    id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX web_spaces_name ON web_spaces (lower(name));
INSERT INTO web_spaces (id, name) VALUES ('main', 'Main');

-- revision counts saves; a save made from an older revision is refused.
CREATE TABLE web_tab_states (
    username TEXT PRIMARY KEY,
    state BYTEA NOT NULL,
    revision BIGINT NOT NULL
);

ALTER TABLE managed_conversations ADD COLUMN space_id TEXT REFERENCES web_spaces (id) ON DELETE SET NULL;

-- +migrate Down
ALTER TABLE managed_conversations DROP COLUMN space_id;
DROP TABLE web_tab_states;
DROP TABLE web_spaces;
