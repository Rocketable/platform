-- +migrate Up
-- An agent being written in the Web editor. draft holds its name, mode, variants and settings as of
-- revision; every change commits as the next revision.
CREATE TABLE agent_drafts (
    id TEXT PRIMARY KEY,
    revision BIGINT NOT NULL,
    draft JSONB NOT NULL,
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

-- Each committed change, in revision order, with the draft it left: the catch-up feed for co-editors,
-- the draft's History, and the base an edit made at an older revision is read from.
CREATE TABLE agent_draft_ops (
    draft_id TEXT NOT NULL REFERENCES agent_drafts (id) ON DELETE CASCADE,
    revision BIGINT NOT NULL,
    op JSONB NOT NULL,
    draft JSONB NOT NULL,
    at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (draft_id, revision)
);

-- Where each open editor is in a draft: its variant and its place, a setting key or a file offset.
CREATE TABLE agent_draft_presence (
    draft_id TEXT NOT NULL REFERENCES agent_drafts (id) ON DELETE CASCADE,
    client TEXT NOT NULL,
    username TEXT NOT NULL,
    variant TEXT NOT NULL,
    place TEXT NOT NULL,
    seen_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (draft_id, client)
);

-- The assistant's proposed changes to a draft, made on base_revision, until someone accepts or rejects them.
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

-- A draft's test chat is a conversation of its own kind, bound to the draft.
ALTER TABLE conversations ADD COLUMN agent_draft_id TEXT REFERENCES agent_drafts (id);
ALTER TABLE conversations DROP CONSTRAINT conversations_kind_check;
ALTER TABLE conversations ADD CONSTRAINT conversations_kind_check CHECK ((kind = ANY (ARRAY['web_chat'::text, 'slack_thread'::text, 'cron_run'::text, 'one_off_cron_run'::text, 'external_mcp_run'::text, 'subagent'::text, 'review'::text, 'shadow'::text, 'remote'::text, 'agent_draft'::text])));

-- +migrate Down
DELETE FROM conversations WHERE kind = 'agent_draft';
ALTER TABLE conversations DROP CONSTRAINT conversations_kind_check;
ALTER TABLE conversations ADD CONSTRAINT conversations_kind_check CHECK ((kind = ANY (ARRAY['web_chat'::text, 'slack_thread'::text, 'cron_run'::text, 'one_off_cron_run'::text, 'external_mcp_run'::text, 'subagent'::text, 'review'::text, 'shadow'::text, 'remote'::text])));
ALTER TABLE conversations DROP COLUMN agent_draft_id;
DROP TABLE agent_draft_proposals;
DROP TABLE agent_draft_presence;
DROP TABLE agent_draft_ops;
DROP TABLE agent_drafts;
