-- +migrate Up
DROP TABLE voice_settings;

DELETE FROM web_tabs WHERE path = '/voice';
DELETE FROM web_shared_tabs WHERE path = '/voice';
DELETE FROM web_closed_tabs WHERE path = '/voice';
DELETE FROM web_tab_attention WHERE path = '/voice';
DELETE FROM web_tab_lists WHERE path = '/voice';
UPDATE web_tab_states SET navigation = '' WHERE navigation = '/voice';
UPDATE web_tab_states SET shared_order = shared_order - '/voice' WHERE shared_order ? '/voice';

WITH voice_lasts AS (
    SELECT s.username, array_agg(l.key) AS spaces
    FROM web_tab_states s CROSS JOIN LATERAL jsonb_each_text(s.last) AS l
    WHERE l.value = '/voice'
    GROUP BY s.username
)
UPDATE web_tab_states s SET last = s.last - v.spaces FROM voice_lasts v WHERE s.username = v.username;

WITH cards AS (
    SELECT s.username, COALESCE(jsonb_agg(c.key ORDER BY c.position) FILTER (WHERE c.key !~ '(^|\n)/voice$'), '[]') AS card_order
    FROM web_tab_states s CROSS JOIN LATERAL jsonb_array_elements_text(s.card_order) WITH ORDINALITY AS c(key, position)
    GROUP BY s.username
    HAVING bool_or(c.key ~ '(^|\n)/voice$')
)
UPDATE web_tab_states s SET card_order = c.card_order FROM cards c WHERE s.username = c.username;

UPDATE behavior_settings SET document = document #- '{embedded_agents,voice}' WHERE document -> 'embedded_agents' ? 'voice';

-- +migrate Down
CREATE TABLE voice_settings (
    username TEXT NOT NULL,
    agent TEXT NOT NULL DEFAULT '',
    voice TEXT NOT NULL DEFAULT '',
    transcript BOOLEAN NOT NULL DEFAULT FALSE,
    CONSTRAINT voice_settings_pkey PRIMARY KEY (username)
);
