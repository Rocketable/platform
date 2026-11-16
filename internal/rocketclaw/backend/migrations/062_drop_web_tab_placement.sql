-- +migrate Up
ALTER TABLE web_tab_states DROP COLUMN placement;

-- +migrate Down
ALTER TABLE web_tab_states ADD COLUMN placement TEXT NOT NULL DEFAULT 'TOP';
