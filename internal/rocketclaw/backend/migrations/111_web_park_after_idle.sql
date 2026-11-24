-- +migrate Up
ALTER TABLE web_tab_attention ADD COLUMN after_idle BIGINT;

UPDATE web_tab_attention a SET after_idle = COALESCE(c.read_through, 0) - CASE WHEN a.worked THEN 1 ELSE 0 END
FROM conversations c
WHERE c.conversation_id = a.conversation_id AND c.kind = 'remote' AND (a.parked AND a.until = 'infinity' OR a.resumes AND a.resume_until = 'infinity');

-- +migrate Down
ALTER TABLE web_tab_attention DROP COLUMN after_idle;
