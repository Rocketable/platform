-- +migrate Up
CREATE INDEX conversations_external_reports ON conversations (reports_to)
WHERE kind = 'external_mcp_run' AND reports_to IS NOT NULL;

-- +migrate Down
DROP INDEX conversations_external_reports;
