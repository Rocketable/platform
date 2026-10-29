-- +migrate Up
-- A session write's reply is a Background Job of its caller that the target session's turn consuming
-- the write settles; reply_target is that session.
ALTER TABLE turns ADD COLUMN reply_target text;
ALTER TABLE turns DROP CONSTRAINT turns_kind_check;
ALTER TABLE turns ADD CONSTRAINT turns_kind_check CHECK ((kind = ANY (ARRAY['model'::text, 'script'::text, 'note'::text, 'reply'::text])) AND ((kind = 'reply'::text) = (reply_target IS NOT NULL)));
CREATE INDEX turns_running_replies ON turns USING btree (reply_target) WHERE ((kind = 'reply'::text) AND (phase = 'running'::text));

-- +migrate Down
DELETE FROM turns WHERE kind = 'reply';
DROP INDEX turns_running_replies;
ALTER TABLE turns DROP CONSTRAINT turns_kind_check;
ALTER TABLE turns ADD CONSTRAINT turns_kind_check CHECK ((kind = ANY (ARRAY['model'::text, 'script'::text, 'note'::text])));
ALTER TABLE turns DROP COLUMN reply_target;
