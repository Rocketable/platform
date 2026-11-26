-- +migrate Up
ALTER TABLE conversations ADD COLUMN goal_check text NOT NULL DEFAULT '';
ALTER TABLE conversations ADD COLUMN goal_force boolean NOT NULL DEFAULT FALSE;
ALTER TABLE conversations ADD COLUMN goal_pending boolean NOT NULL DEFAULT FALSE;

CREATE TABLE goal_events (
    id bigserial PRIMARY KEY,
    conversation_id text NOT NULL REFERENCES conversations(conversation_id) ON DELETE CASCADE,
    kind text NOT NULL,
    turn integer NOT NULL DEFAULT 0,
    status text NOT NULL DEFAULT '',
    note text NOT NULL DEFAULT '',
    check_result text NOT NULL DEFAULT '',
    check_output text NOT NULL DEFAULT '',
    entry_id bigint NOT NULL DEFAULT 0,
    principal text NOT NULL DEFAULT '',
    at_unix_ns bigint NOT NULL
);
CREATE INDEX goal_events_conversation_id ON goal_events (conversation_id, id);

-- +migrate Down
DROP TABLE goal_events;
ALTER TABLE conversations DROP COLUMN goal_pending;
ALTER TABLE conversations DROP COLUMN goal_force;
ALTER TABLE conversations DROP COLUMN goal_check;
