-- +migrate Up
-- A person's Top Shelf, as the Web's TopShelf message in protobuf JSON: tabs of any RocketClaw, in their order, and
-- whether it is collapsed.
CREATE TABLE web_top_shelves (
    username TEXT PRIMARY KEY,
    shelf JSONB NOT NULL
);

-- +migrate Down
DROP TABLE web_top_shelves;
