-- +migrate Up
UPDATE web_tabs SET path = regexp_replace(path, '^/cron(/|$)', '/routines\1') WHERE path ~ '^/cron(/|$)';
UPDATE web_shared_tabs SET path = regexp_replace(path, '^/cron(/|$)', '/routines\1') WHERE path ~ '^/cron(/|$)';
UPDATE web_closed_tabs SET path = regexp_replace(path, '^/cron(/|$)', '/routines\1') WHERE path ~ '^/cron(/|$)';
UPDATE web_tab_attention SET path = regexp_replace(path, '^/cron(/|$)', '/routines\1') WHERE path ~ '^/cron(/|$)';
UPDATE web_tab_states SET navigation = regexp_replace(navigation, '^/cron(/|$)', '/routines\1') WHERE navigation ~ '^/cron(/|$)';

WITH lasts AS (
    SELECT s.username, jsonb_object_agg(l.key, regexp_replace(l.value, '^/cron(/|$)', '/routines\1')) AS last
    FROM web_tab_states s CROSS JOIN LATERAL jsonb_each_text(s.last) AS l
    WHERE s.last::text ~ '"/cron(/|")'
    GROUP BY s.username
)
UPDATE web_tab_states s SET last = l.last FROM lasts l WHERE s.username = l.username;

WITH orders AS (
    SELECT s.username, jsonb_agg(regexp_replace(o.path, '^/cron(/|$)', '/routines\1') ORDER BY o.position) AS shared_order
    FROM web_tab_states s CROSS JOIN LATERAL jsonb_array_elements_text(s.shared_order) WITH ORDINALITY AS o(path, position)
    WHERE s.shared_order::text ~ '"/cron(/|")'
    GROUP BY s.username
)
UPDATE web_tab_states s SET shared_order = o.shared_order FROM orders o WHERE s.username = o.username;

WITH cards AS (
    SELECT s.username, jsonb_agg(regexp_replace(c.key, '(^|\n)/cron(/|$)', '\1/routines\2') ORDER BY c.position) AS card_order
    FROM web_tab_states s CROSS JOIN LATERAL jsonb_array_elements_text(s.card_order) WITH ORDINALITY AS c(key, position)
    WHERE s.card_order::text ~ '\\n/cron(/|")'
    GROUP BY s.username
)
UPDATE web_tab_states s SET card_order = c.card_order FROM cards c WHERE s.username = c.username;

-- +migrate Down
UPDATE web_tabs SET path = regexp_replace(path, '^/routines(/|$)', '/cron\1') WHERE path ~ '^/routines(/|$)';
UPDATE web_shared_tabs SET path = regexp_replace(path, '^/routines(/|$)', '/cron\1') WHERE path ~ '^/routines(/|$)';
UPDATE web_closed_tabs SET path = regexp_replace(path, '^/routines(/|$)', '/cron\1') WHERE path ~ '^/routines(/|$)';
UPDATE web_tab_attention SET path = regexp_replace(path, '^/routines(/|$)', '/cron\1') WHERE path ~ '^/routines(/|$)';
UPDATE web_tab_states SET navigation = regexp_replace(navigation, '^/routines(/|$)', '/cron\1') WHERE navigation ~ '^/routines(/|$)';

WITH lasts AS (
    SELECT s.username, jsonb_object_agg(l.key, regexp_replace(l.value, '^/routines(/|$)', '/cron\1')) AS last
    FROM web_tab_states s CROSS JOIN LATERAL jsonb_each_text(s.last) AS l
    WHERE s.last::text ~ '"/routines(/|")'
    GROUP BY s.username
)
UPDATE web_tab_states s SET last = l.last FROM lasts l WHERE s.username = l.username;

WITH orders AS (
    SELECT s.username, jsonb_agg(regexp_replace(o.path, '^/routines(/|$)', '/cron\1') ORDER BY o.position) AS shared_order
    FROM web_tab_states s CROSS JOIN LATERAL jsonb_array_elements_text(s.shared_order) WITH ORDINALITY AS o(path, position)
    WHERE s.shared_order::text ~ '"/routines(/|")'
    GROUP BY s.username
)
UPDATE web_tab_states s SET shared_order = o.shared_order FROM orders o WHERE s.username = o.username;

WITH cards AS (
    SELECT s.username, jsonb_agg(regexp_replace(c.key, '(^|\n)/routines(/|$)', '\1/cron\2') ORDER BY c.position) AS card_order
    FROM web_tab_states s CROSS JOIN LATERAL jsonb_array_elements_text(s.card_order) WITH ORDINALITY AS c(key, position)
    WHERE s.card_order::text ~ '\\n/routines(/|")'
    GROUP BY s.username
)
UPDATE web_tab_states s SET card_order = c.card_order FROM cards c WHERE s.username = c.username;
