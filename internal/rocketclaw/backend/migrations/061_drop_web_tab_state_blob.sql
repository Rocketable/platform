-- +migrate Up
-- The release with 060 unpacked every saved tab blob into rows at startup, so the blob column goes.
-- A blob still here was never unpacked: refuse, so its tabs are not lost, until that release has started once.
-- +migrate StatementBegin
DO $$
BEGIN
    PERFORM 1 FROM web_tab_states WHERE state IS NOT NULL LIMIT 1;
    IF FOUND THEN
        RAISE EXCEPTION 'web_tab_states holds saved tabs not yet unpacked into rows; start the release with migration 060 once first';
    END IF;
END $$;
-- +migrate StatementEnd
ALTER TABLE web_tab_states DROP COLUMN state;

-- +migrate Down
ALTER TABLE web_tab_states ADD COLUMN state BYTEA;
