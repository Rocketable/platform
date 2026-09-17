-- +migrate Up
-- Rebuild the derived user-only previews with the latest user or assistant text.
-- The existing background backfill preserves history and handles concurrent writes.
DELETE FROM session_summaries;

-- +migrate Down
DELETE FROM session_summaries;
