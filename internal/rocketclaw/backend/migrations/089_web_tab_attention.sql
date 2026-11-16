-- +migrate Up
DELETE FROM web_tab_mutes;
ALTER TABLE web_tab_mutes RENAME TO web_tab_attention;
ALTER TABLE web_tab_attention DROP CONSTRAINT web_tab_mutes_pkey, DROP CONSTRAINT web_tab_mutes_conversation_id_fkey;
DROP INDEX web_tab_mutes_conversation;
ALTER TABLE web_tab_attention
    ALTER COLUMN conversation_id DROP NOT NULL,
    ADD COLUMN path TEXT NOT NULL,
    ADD COLUMN unread_at TIMESTAMPTZ,
    ADD COLUMN quiet BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN parked BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN note TEXT NOT NULL DEFAULT '',
    ADD CONSTRAINT web_tab_attention_pkey PRIMARY KEY (username, path),
    ADD CONSTRAINT web_tab_attention_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES conversations (conversation_id) ON DELETE CASCADE;
CREATE INDEX web_tab_attention_conversation ON web_tab_attention (conversation_id);

DELETE FROM web_tabs WHERE position IS NULL;
ALTER TABLE web_tabs DROP COLUMN unread_at, DROP COLUMN marked_unread, ALTER COLUMN position SET NOT NULL;

UPDATE web_tab_states SET viewing = '', viewed_at = NULL WHERE viewing <> '' OR viewed_at IS NOT NULL;

ALTER TABLE web_closed_tabs ADD COLUMN group_id TEXT NOT NULL DEFAULT '', ADD COLUMN position INTEGER NOT NULL DEFAULT 0;

-- +migrate Down
ALTER TABLE web_closed_tabs DROP COLUMN group_id, DROP COLUMN position;
ALTER TABLE web_tabs ALTER COLUMN position DROP NOT NULL, ADD COLUMN unread_at TIMESTAMPTZ, ADD COLUMN marked_unread BOOLEAN NOT NULL DEFAULT FALSE;
DROP TABLE web_tab_attention;
CREATE TABLE web_tab_mutes (
    username TEXT NOT NULL,
    conversation_id TEXT NOT NULL REFERENCES conversations (conversation_id) ON DELETE CASCADE,
    until TIMESTAMPTZ,
    PRIMARY KEY (username, conversation_id)
);
CREATE INDEX web_tab_mutes_conversation ON web_tab_mutes (conversation_id);
