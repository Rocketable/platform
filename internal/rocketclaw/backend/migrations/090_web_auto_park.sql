-- +migrate Up
-- A person who auto-parks a session parks its tab until it is done whenever they send it a prompt. auto_park on the
-- session's attention row is their choice for that session, NULL for none; on their tab state, for every session they
-- chose nothing for.
ALTER TABLE web_tab_attention ADD COLUMN auto_park BOOLEAN;
ALTER TABLE web_tab_states ADD COLUMN auto_park BOOLEAN NOT NULL DEFAULT FALSE;

-- +migrate Down
ALTER TABLE web_tab_states DROP COLUMN auto_park;
ALTER TABLE web_tab_attention DROP COLUMN auto_park;
