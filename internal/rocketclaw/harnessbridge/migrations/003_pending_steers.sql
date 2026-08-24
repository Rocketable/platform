-- +migrate Up
ALTER TABLE active_turns ADD COLUMN pending_steers_json TEXT NOT NULL DEFAULT '[]';

-- +migrate Down
ALTER TABLE active_turns DROP COLUMN pending_steers_json;
