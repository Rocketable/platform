-- +migrate Up
-- The trusted origin header of the turn that set a goal or scheduled a message, which permission reviews of
-- the goal's continuations and the message's turn read.
ALTER TABLE conversations ADD COLUMN goal_origin text NOT NULL DEFAULT '';
ALTER TABLE schedules ADD COLUMN origin text NOT NULL DEFAULT '';
ALTER TABLE turns ADD COLUMN origin text NOT NULL DEFAULT '';

-- +migrate Down
ALTER TABLE turns DROP COLUMN origin;
ALTER TABLE schedules DROP COLUMN origin;
ALTER TABLE conversations DROP COLUMN goal_origin;
