-- +migrate Up
-- Each model response of a shadow turn's parent (child = FALSE) or of the shadow itself, once per
-- response even when a restart repeats it. segment counts the person-sent steers the turn had
-- applied before the response: a shadow may take floor(1.5 x the parent's steps) in each segment.
CREATE TABLE shadow_steps (
    shadow_turn_id BIGINT NOT NULL REFERENCES shadow_turns (id) ON DELETE CASCADE,
    child BOOLEAN NOT NULL,
    response_id TEXT NOT NULL,
    segment INTEGER NOT NULL,
    PRIMARY KEY (shadow_turn_id, child, response_id)
);
-- What Web shows for a shadow turn stopped at its step limit.
ALTER TABLE shadow_turns ADD COLUMN limit_note TEXT NOT NULL DEFAULT '';

-- +migrate Down
ALTER TABLE shadow_turns DROP COLUMN limit_note;
DROP TABLE shadow_steps;
