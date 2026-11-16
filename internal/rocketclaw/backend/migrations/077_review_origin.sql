-- +migrate Up
ALTER TABLE conversations ADD COLUMN goal_origin text NOT NULL DEFAULT '';
ALTER TABLE schedules ADD COLUMN origin text NOT NULL DEFAULT '';
ALTER TABLE turns ADD COLUMN origin text NOT NULL DEFAULT '';

-- +migrate Down
ALTER TABLE turns DROP COLUMN origin;
ALTER TABLE schedules DROP COLUMN origin;
ALTER TABLE conversations DROP COLUMN goal_origin;
