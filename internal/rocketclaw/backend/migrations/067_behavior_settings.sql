-- +migrate Up
CREATE TABLE behavior_settings (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    document JSONB NOT NULL
);

-- +migrate Down
DROP TABLE behavior_settings;
