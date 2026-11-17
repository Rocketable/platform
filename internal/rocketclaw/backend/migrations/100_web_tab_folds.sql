-- +migrate Up
ALTER TABLE web_tab_states ADD COLUMN folds JSONB NOT NULL DEFAULT '{}';

-- +migrate Down
ALTER TABLE web_tab_states DROP COLUMN folds;
