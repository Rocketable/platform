-- +migrate Up
-- Every person auto-parks every session they chose nothing for unless they turn it off, those already kept included.
ALTER TABLE web_tab_states ALTER COLUMN auto_park SET DEFAULT TRUE;
UPDATE web_tab_states SET auto_park = TRUE;

-- +migrate Down
ALTER TABLE web_tab_states ALTER COLUMN auto_park SET DEFAULT FALSE;
