-- +migrate Up
-- A history lock locks its conversation's row, so history may only exist for a conversation that has one.
-- History left without its conversation is deleted first; it cannot be shown or locked.
WITH orphans AS (
    SELECT e.id FROM entries e LEFT JOIN conversations c ON c.conversation_id = e.conversation_id WHERE c.conversation_id IS NULL
)
DELETE FROM entries e USING orphans o WHERE e.id = o.id;

WITH orphans AS (
    SELECT t.id FROM turns t LEFT JOIN conversations c ON c.conversation_id = t.conversation_id WHERE c.conversation_id IS NULL
)
DELETE FROM turns t USING orphans o WHERE t.id = o.id;

WITH orphans AS (
    SELECT s.conversation_id, s.key FROM steps s LEFT JOIN conversations c ON c.conversation_id = s.conversation_id WHERE c.conversation_id IS NULL
)
DELETE FROM steps s USING orphans o WHERE s.conversation_id = o.conversation_id AND s.key = o.key;

ALTER TABLE entries ADD CONSTRAINT entries_conversation_fkey FOREIGN KEY (conversation_id) REFERENCES conversations (conversation_id);
ALTER TABLE turns ADD CONSTRAINT turns_conversation_fkey FOREIGN KEY (conversation_id) REFERENCES conversations (conversation_id);
ALTER TABLE steps ADD CONSTRAINT steps_conversation_fkey FOREIGN KEY (conversation_id) REFERENCES conversations (conversation_id);

-- +migrate Down
-- Deleted history is not restored.
ALTER TABLE steps DROP CONSTRAINT steps_conversation_fkey;
ALTER TABLE turns DROP CONSTRAINT turns_conversation_fkey;
ALTER TABLE entries DROP CONSTRAINT entries_conversation_fkey;
