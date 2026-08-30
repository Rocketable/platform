-- +migrate Up
DROP TABLE store_bootstrap;

-- +migrate Down
CREATE TABLE store_bootstrap (id INTEGER PRIMARY KEY CHECK (id = 1), imported_at_unix_ns BIGINT NOT NULL);
