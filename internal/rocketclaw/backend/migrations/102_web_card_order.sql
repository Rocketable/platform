-- +migrate Up
ALTER TABLE web_tab_states ADD COLUMN card_order JSONB NOT NULL DEFAULT '[]';

-- +migrate Down
ALTER TABLE web_tab_states DROP COLUMN card_order;
