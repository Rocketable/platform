-- +migrate Up
ALTER TABLE web_tab_attention ADD COLUMN unfollowed BOOLEAN NOT NULL DEFAULT FALSE;

-- +migrate Down
ALTER TABLE web_tab_attention DROP COLUMN unfollowed;
