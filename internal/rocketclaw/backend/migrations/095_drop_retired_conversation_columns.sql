-- +migrate Up
ALTER TABLE conversations DROP COLUMN settled, DROP COLUMN reopened_at, DROP COLUMN snoozed_until, DROP COLUMN producer_effects_through_id;

-- +migrate Down
ALTER TABLE conversations
    ADD COLUMN settled boolean DEFAULT false CONSTRAINT managed_conversations_settled_not_null NOT NULL,
    ADD COLUMN reopened_at timestamp with time zone DEFAULT '0001-01-01 00:00:00+00'::timestamp with time zone CONSTRAINT managed_conversations_reopened_at_not_null NOT NULL,
    ADD COLUMN snoozed_until timestamp with time zone,
    ADD COLUMN producer_effects_through_id bigint DEFAULT 0 CONSTRAINT managed_conversations_producer_effects_through_id_not_null NOT NULL;
