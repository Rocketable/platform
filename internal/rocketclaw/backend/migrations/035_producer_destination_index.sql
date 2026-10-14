-- +migrate Up
CREATE INDEX managed_conversations_sync_destination
    ON managed_conversations ((producer_inbound_json->>'SyncDestination'))
    WHERE producer_inbound_json->>'SyncDestination' IS NOT NULL;

-- +migrate Down
DROP INDEX managed_conversations_sync_destination;
