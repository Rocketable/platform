-- +migrate Up
-- A person's own order of Shared tabs, as a JSON array of tab paths. Shared tabs it does not name follow, oldest
-- added first, so the order of a new person, and of a tab shared since, needs nothing stored.
ALTER TABLE web_tab_states ADD COLUMN shared_order JSONB NOT NULL DEFAULT '[]';

-- +migrate Down
ALTER TABLE web_tab_states DROP COLUMN shared_order;
