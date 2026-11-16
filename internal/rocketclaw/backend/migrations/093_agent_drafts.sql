-- +migrate Up
CREATE TABLE agent_drafts (
    id TEXT PRIMARY KEY,
    revision BIGINT NOT NULL,
    draft JSONB NOT NULL,
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE agent_draft_ops (
    draft_id TEXT NOT NULL REFERENCES agent_drafts (id) ON DELETE CASCADE,
    revision BIGINT NOT NULL,
    op JSONB NOT NULL,
    draft JSONB NOT NULL,
    at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (draft_id, revision)
);

CREATE TABLE agent_draft_presence (
    draft_id TEXT NOT NULL REFERENCES agent_drafts (id) ON DELETE CASCADE,
    client TEXT NOT NULL,
    username TEXT NOT NULL,
    variant TEXT NOT NULL,
    place TEXT NOT NULL,
    seen_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (draft_id, client)
);

CREATE TABLE agent_draft_proposals (
    id TEXT PRIMARY KEY,
    draft_id TEXT NOT NULL REFERENCES agent_drafts (id) ON DELETE CASCADE,
    base_revision BIGINT NOT NULL,
    proposal JSONB NOT NULL,
    state TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'accepted', 'rejected')),
    created_by TEXT NOT NULL,
    decided_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX agent_draft_proposals_draft ON agent_draft_proposals (draft_id);

ALTER TABLE conversations ADD COLUMN agent_draft_id TEXT REFERENCES agent_drafts (id);
ALTER TABLE conversations DROP CONSTRAINT conversations_kind_check;
ALTER TABLE conversations ADD CONSTRAINT conversations_kind_check CHECK ((kind = ANY (ARRAY['web_chat'::text, 'slack_thread'::text, 'cron_run'::text, 'one_off_cron_run'::text, 'external_mcp_run'::text, 'subagent'::text, 'review'::text, 'shadow'::text, 'remote'::text, 'agent_draft'::text])));

-- +migrate Down
WITH RECURSIVE gone AS (
    SELECT conversation_id FROM conversations WHERE kind = 'agent_draft'
    UNION
    SELECT c.conversation_id FROM conversations c JOIN gone g ON g.conversation_id IN (c.parent_conversation_id, c.shadow_parent)
), gone_entries AS (
    DELETE FROM entries e USING gone g WHERE e.conversation_id = g.conversation_id
), gone_turns AS (
    DELETE FROM turns t USING gone g WHERE t.conversation_id = g.conversation_id
), gone_steps AS (
    DELETE FROM steps s USING gone g WHERE s.conversation_id = g.conversation_id
)
DELETE FROM conversations c USING gone g WHERE c.conversation_id = g.conversation_id;
ALTER TABLE conversations DROP CONSTRAINT conversations_kind_check;
ALTER TABLE conversations ADD CONSTRAINT conversations_kind_check CHECK ((kind = ANY (ARRAY['web_chat'::text, 'slack_thread'::text, 'cron_run'::text, 'one_off_cron_run'::text, 'external_mcp_run'::text, 'subagent'::text, 'review'::text, 'shadow'::text, 'remote'::text])));
ALTER TABLE conversations DROP COLUMN agent_draft_id;
DROP TABLE agent_draft_proposals;
DROP TABLE agent_draft_presence;
DROP TABLE agent_draft_ops;
DROP TABLE agent_drafts;
