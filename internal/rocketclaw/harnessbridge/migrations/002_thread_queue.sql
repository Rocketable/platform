-- +migrate Up
CREATE TABLE thread_queue (
	queue_item_id TEXT PRIMARY KEY,
	conversation_id TEXT NOT NULL,
	message TEXT NOT NULL,
	principal TEXT NOT NULL,
	stash_at_unix_ns BIGINT NOT NULL,
	position INTEGER NOT NULL,
	slack_channel TEXT NOT NULL DEFAULT '',
	slack_ts TEXT NOT NULL DEFAULT ''
);
CREATE INDEX thread_queue_conversation_position ON thread_queue (conversation_id, position, stash_at_unix_ns);

-- +migrate Down
DROP TABLE thread_queue;
