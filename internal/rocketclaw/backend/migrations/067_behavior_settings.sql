-- +migrate Up
-- The Behavior Settings document, as the JSON of config.Behavior. The id check keeps it to one row.
CREATE TABLE behavior_settings (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    document JSONB NOT NULL
);

-- +migrate Down
DROP TABLE behavior_settings;
