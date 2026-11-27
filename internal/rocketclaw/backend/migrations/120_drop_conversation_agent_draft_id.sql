-- +migrate Up
ALTER TABLE conversations DROP COLUMN agent_draft_id;

-- +migrate Down
ALTER TABLE conversations ADD COLUMN agent_draft_id TEXT REFERENCES agent_drafts (id);
