-- +migrate Up
CREATE TABLE harness_feedback_issues (
    id BIGSERIAL PRIMARY KEY,
    summary TEXT NOT NULL,
    slack_channel_id TEXT NOT NULL,
    slack_ts TEXT NOT NULL,
    reported_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +migrate Down
DROP TABLE harness_feedback_issues;
