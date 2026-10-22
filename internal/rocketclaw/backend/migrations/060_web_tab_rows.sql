-- +migrate Up
-- One person's saved Web tab state, unpacked: revision counts saves; space and placement are the
-- current space and tab strip side; viewing/viewed_at is the session their focused window last showed.
-- state holds a protobuf blob saved before this migration until startup unpacks it; a later migration drops it.
ALTER TABLE web_tab_states ALTER COLUMN state DROP NOT NULL,
    ADD COLUMN space TEXT NOT NULL DEFAULT '',
    ADD COLUMN placement TEXT NOT NULL DEFAULT 'TOP',
    ADD COLUMN viewing TEXT NOT NULL DEFAULT '',
    ADD COLUMN viewed_at TIMESTAMPTZ;

-- A person's tab groups, in strip order.
CREATE TABLE web_tab_groups (
    username TEXT NOT NULL,
    id TEXT NOT NULL,
    position INTEGER NOT NULL,
    name TEXT NOT NULL,
    color TEXT NOT NULL,
    collapsed BOOLEAN NOT NULL,
    PRIMARY KEY (username, id)
);

-- A person's tab, open at position, or closed but still some space's last tab or unread. A session's
-- tab names its conversation, so deleting the session deletes its tabs. unread_at is when a turn the
-- person sent into ended unseen, NULL once read.
CREATE TABLE web_tabs (
    username TEXT NOT NULL,
    path TEXT NOT NULL,
    conversation_id TEXT REFERENCES conversations (conversation_id) ON DELETE CASCADE,
    position INTEGER,
    pinned BOOLEAN NOT NULL DEFAULT FALSE,
    preview BOOLEAN NOT NULL DEFAULT FALSE,
    group_id TEXT NOT NULL DEFAULT '',
    space TEXT NOT NULL DEFAULT '',
    last BOOLEAN NOT NULL DEFAULT FALSE,
    unread_at TIMESTAMPTZ,
    PRIMARY KEY (username, path)
);
CREATE INDEX web_tabs_conversation ON web_tabs (conversation_id);

-- A remote OpenCode session's newest handled idle marker, in OpenCode milliseconds; NULL before its first.
ALTER TABLE conversations ADD COLUMN read_through BIGINT;

-- +migrate Down
ALTER TABLE conversations DROP COLUMN read_through;
DROP TABLE web_tabs;
DROP TABLE web_tab_groups;
DELETE FROM web_tab_states WHERE state IS NULL;
ALTER TABLE web_tab_states ALTER COLUMN state SET NOT NULL,
    DROP COLUMN space, DROP COLUMN placement, DROP COLUMN viewing, DROP COLUMN viewed_at;
