-- +migrate Up
ALTER TABLE attachments DROP COLUMN original_unverified;

-- +migrate Down
ALTER TABLE attachments ADD COLUMN original_unverified boolean NOT NULL DEFAULT false;
