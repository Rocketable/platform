-- +migrate Up
ALTER TABLE web_tab_attention
    ADD COLUMN worked BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN resumes BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN resume_until TIMESTAMPTZ;
CREATE INDEX web_tab_attention_ticking ON web_tab_attention (until) WHERE until IS NOT NULL OR resumes;

-- +migrate Down
DROP INDEX web_tab_attention_ticking;
ALTER TABLE web_tab_attention DROP COLUMN worked, DROP COLUMN resumes, DROP COLUMN resume_until;
