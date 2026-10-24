-- +migrate Up
-- Web tabs always sit in the left sidebar, so the saved strip side goes.
ALTER TABLE web_tab_states DROP COLUMN placement;

-- +migrate Down
ALTER TABLE web_tab_states ADD COLUMN placement TEXT NOT NULL DEFAULT 'TOP';
