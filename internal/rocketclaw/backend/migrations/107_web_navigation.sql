-- +migrate Up
ALTER TABLE web_tab_states ADD COLUMN navigation TEXT NOT NULL DEFAULT '',
    ADD COLUMN navigation_at TIMESTAMPTZ;

-- +migrate Down
ALTER TABLE web_tab_states DROP COLUMN navigation, DROP COLUMN navigation_at;
