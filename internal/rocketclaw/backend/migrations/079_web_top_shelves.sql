-- +migrate Up
CREATE TABLE web_top_shelves (
    username TEXT PRIMARY KEY,
    shelf JSONB NOT NULL
);

-- +migrate Down
DROP TABLE web_top_shelves;
