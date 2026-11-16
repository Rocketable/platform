-- +migrate Up
ALTER TABLE web_tab_attention ADD COLUMN auto_park BOOLEAN;
ALTER TABLE web_tab_states ADD COLUMN auto_park BOOLEAN NOT NULL DEFAULT FALSE;

-- +migrate Down
ALTER TABLE web_tab_states DROP COLUMN auto_park;
ALTER TABLE web_tab_attention DROP COLUMN auto_park;
