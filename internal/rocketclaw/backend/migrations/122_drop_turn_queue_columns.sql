-- +migrate Up
ALTER TABLE turns DROP COLUMN principal, DROP COLUMN source, DROP COLUMN content, DROP COLUMN slack_reply, DROP COLUMN slack_channel, DROP COLUMN slack_ts;

-- +migrate Down
ALTER TABLE turns ADD COLUMN principal text, ADD COLUMN source text, ADD COLUMN content jsonb, ADD COLUMN slack_reply jsonb, ADD COLUMN slack_channel text, ADD COLUMN slack_ts text;
