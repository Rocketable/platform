package backend

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/jackc/pgx/v5"
)

func messageSearchText(ctx context.Context, db stateStoreDB, producer string, replay []json.RawMessage) (searchText string, principals []string, err error) {
	items, err := harness.ReplayInputToParams(replay)
	if err != nil {
		return "", nil, fmt.Errorf("decode message search replay input: %w", err)
	}

	var lines []string

	for i := range items {
		role, text, ok, err := ReplayInputMessageRoleText(&items[i], replay[i])
		if err != nil {
			return "", nil, &harness.ReplayDecodeError{EntryIndex: -1, ItemIndex: i, Cause: err}
		}

		text = strings.ReplaceAll(text, "\x00", "")

		var principal string

		if role == "user" && items[i].OfMessage != nil {
			header, _ := items[i].OfMessage.ExtraFields()["prompt_header"].(string)
			principal = PromptPrincipal(header)
		}

		if !ok || role != "user" && role != "assistant" || strings.TrimSpace(text) == "" && principal == "" {
			continue
		}

		parts := []struct{ Type, Text string }{{Type: "output_text", Text: text}}

		if role == "user" {
			parts[0].Text, _, err = inputAttachmentsDB(ctx, db, producer, text)
			if err != nil {
				return "", nil, err
			}
		} else {
			var identity struct {
				ID      string          `json:"id"`
				Content json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(replay[i], &identity); err != nil {
				return "", nil, &harness.ReplayDecodeError{EntryIndex: -1, ItemIndex: i, Cause: fmt.Errorf("decode message search identity: %w", err)}
			}

			if identity.ID != "" && len(identity.Content) > 0 && identity.Content[0] == '[' {
				parts = nil
				if err := json.Unmarshal(identity.Content, &parts); err != nil {
					return "", nil, &harness.ReplayDecodeError{EntryIndex: -1, ItemIndex: i, Cause: fmt.Errorf("decode message search output text: %w", err)}
				}
			}
		}

		for part, content := range parts {
			if content.Type != "output_text" {
				continue
			}

			text := strings.ReplaceAll(content.Text, "\x00", "")
			if strings.TrimSpace(text) != "" || principal != "" {
				lines = append(lines, fmt.Sprintf("%d %d %s", i, part, searchJSON([4]string{role, principal, text, strings.ToLower(text)})))
			}
		}

		if principal != "" {
			principals = append(principals, principal)
		}
	}

	return strings.Join(lines, "\n"), principals, nil
}

func searchJSON(v any) string {
	var encoded strings.Builder

	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(v)

	return strings.TrimSuffix(encoded.String(), "\n")
}

func PromptPrincipal(header string) string {
	inside, ok := strings.CutPrefix(header, "[")
	if !ok || !strings.HasSuffix(inside, "]") {
		return ""
	}

	_, attributes, _ := strings.Cut(strings.TrimSuffix(inside, "]"), " ")
	for attributes != "" {
		name, value, ok := strings.Cut(attributes, "=")
		if !ok {
			return ""
		}

		if strings.HasPrefix(value, `"`) {
			quoted, err := strconv.QuotedPrefix(value)
			if err != nil {
				return ""
			}

			attributes = value[len(quoted):]
			if attributes != "" && !strings.HasPrefix(attributes, " ") {
				return ""
			}

			if name == "principal" {
				principal, _ := strconv.Unquote(quoted)
				return principal
			}
		} else {
			_, attributes, _ = strings.Cut(value, " ")
		}

		attributes = strings.TrimPrefix(attributes, " ")
	}

	return ""
}

const searchableSQL = `, chats AS (
    SELECT c.conversation_id, c.reports_to, c.continues, NULLIF(split_part(c.revert_message_id, ':', 1), '')::bigint AS cut_entry,
        NULLIF(split_part(c.revert_message_id, ':', 2), '')::integer AS cut_index
    FROM conversations c JOIN searched USING (conversation_id)
), shown AS MATERIALIZED (
    SELECT c.conversation_id AS source, c.* FROM chats c WHERE c.reports_to IS NULL
    UNION ALL SELECT r.conversation_id, c.* FROM chats c JOIN conversations r ON r.reports_to = c.conversation_id
    UNION ALL SELECT r.conversation_id, c.* FROM chats c JOIN conversations r ON r.conversation_id = c.continues
    WHERE r.reports_to IS DISTINCT FROM c.conversation_id
), shown_rows AS (
    SELECT s.conversation_id, e.id AS entry_id, e.id AS position, NULL::bigint AS created_at_unix_ns, '' AS turn_id, s.cut_entry, s.cut_index, e.search_text
    FROM shown s JOIN entries e ON e.conversation_id = s.source COLLATE "C"
    UNION ALL SELECT c.conversation_id, 0, a.history_anchor_id, a.created_at_unix_ns, a.id, c.cut_entry, c.cut_index, a.search_text
    FROM chats c JOIN turns a USING (conversation_id)
), searchable AS (
    SELECT conversation_id, entry_id, position, created_at_unix_ns, turn_id, search_text, CASE WHEN entry_id = cut_entry THEN cut_index ELSE -1 END AS cut
    FROM shown_rows WHERE cut_entry IS NULL OR position < cut_entry OR entry_id = cut_entry
)`

func refreshPrincipals(ctx context.Context, db stateStoreDB, conversationIDs []string) error {
	_, err := execState(ctx, db, "refresh message principals", `WITH searched AS (
    SELECT conversation_id FROM conversations WHERE conversation_id = ANY($1)
    UNION SELECT reports_to FROM conversations WHERE conversation_id = ANY($1) AND reports_to IS NOT NULL
    UNION SELECT conversation_id FROM conversations WHERE continues = ANY($1)
)`+searchableSQL+`, principals AS (
    SELECT s.conversation_id, array_agg(DISTINCT p.m[2]::json #>> '{}') AS principals FROM searchable s
    CROSS JOIN LATERAL string_to_table(s.search_text, E'\n') AS l(line)
    CROSS JOIN LATERAL regexp_matches(l.line, '^(\d+) \d+ \["\w+",("(?:[^"\\]|\\.)+")') AS p(m)
    WHERE s.cut < 0 OR p.m[1]::integer < s.cut GROUP BY s.conversation_id
)
UPDATE conversations c SET principals = COALESCE(p.principals, '{}') FROM chats h LEFT JOIN principals p USING (conversation_id)
WHERE c.conversation_id = h.conversation_id`, conversationIDs)

	return err
}

func (s *SessionService) messagePrincipals(ctx context.Context) (principals map[string][]string, listed []string, err error) {
	principals = make(map[string][]string)
	err = readRows(ctx, s.db, `WITH `+visibleChatsSQL+chatOriginSQL+shownChatsSQL+`
SELECT c.conversation_id, p.principal, c.found OR c.running FROM shown c CROSS JOIN LATERAL unnest(c.principals) AS p(principal)
WHERE c.principals <> '{}' ORDER BY c.conversation_id COLLATE "C", p.principal COLLATE "C"`, "message principals", func(row rowScanner) error {
		var (
			id, principal string
			sidebar       bool
		)
		if err := row.Scan(&id, &principal, &sidebar); err != nil {
			return fmt.Errorf("scan message principal: %w", err)
		}

		principals[id] = append(principals[id], principal)
		if sidebar {
			listed = append(listed, principal)
		}

		return nil
	}, "", ThreadCreatedByCron, KindOneOffCronRun)

	slices.Sort(listed)

	return principals, slices.Compact(listed), err
}

func (s *SessionService) indexTurnMessages(ctx context.Context, db stateStoreDB, conversationID, turnID string) error {
	replay, err := queryRows(ctx, db, `WITH saved AS (
    SELECT TRUE AS found FROM entries WHERE conversation_id = $1 AND entry_json->>'turn_id' = $2 LIMIT 1
)
SELECT i.item FROM turns a CROSS JOIN LATERAL json_array_elements(a.replay) WITH ORDINALITY AS i(item, n)
LEFT JOIN saved ON TRUE WHERE a.id = $2 AND a.terminal <> '' AND saved.found IS NULL ORDER BY i.n`, "turn replay", scanJSON, conversationID, turnID)
	if err != nil {
		return err
	}

	text, _, err := messageSearchText(ctx, db, conversationID, replay)
	if _, ok := errors.AsType[*harness.ReplayDecodeError](err); ok {
		s.log.Error("leave undecodable turn record out of message search", "conversation_id", conversationID, "turn_id", turnID, "error", err)
	} else if err != nil {
		return err
	}

	changed, err := execRows(ctx, db, "index turn message search", `UPDATE turns SET search_text = NULLIF($2, '')
WHERE id = $1 AND search_text IS DISTINCT FROM NULLIF($2, '')`, turnID, text)
	if err != nil || changed == 0 {
		return err
	}

	return refreshPrincipals(ctx, db, []string{conversationID})
}

const visibleChatsSQL = `visible_chats AS NOT MATERIALIZED (SELECT * FROM conversations WHERE listed AND ($1 = '' OR conversation_id = $1))`

const searchMessagesSQL = `WITH found AS (
    SELECT e.conversation_id, e.id, e.search_text, r.reports_to FROM entries e LEFT JOIN conversations r ON r.conversation_id = e.conversation_id
    WHERE e.search_text LIKE ANY ($1::text[])
), matching AS (
    SELECT COALESCE(reports_to, conversation_id) AS conversation_id, id AS entry_id, id AS position, NULL::bigint AS created_at_unix_ns, '' AS turn_id, search_text FROM found
    UNION ALL SELECT w.conversation_id, f.id AS entry_id, f.id AS position, NULL::bigint AS created_at_unix_ns, '' AS turn_id, f.search_text FROM found f JOIN conversations w ON w.continues = f.conversation_id
    WHERE f.reports_to IS DISTINCT FROM w.conversation_id
    UNION ALL SELECT conversation_id, 0, history_anchor_id, created_at_unix_ns, id, search_text FROM turns WHERE search_text LIKE ANY ($1::text[])
), cuts AS (
    SELECT m.*, NULLIF(split_part(c.revert_message_id, ':', 1), '')::bigint AS cut_entry,
        NULLIF(split_part(c.revert_message_id, ':', 2), '')::integer AS cut_index
    FROM matching m JOIN conversations c ON c.conversation_id = m.conversation_id AND c.listed
)
SELECT conversation_id, entry_id, CASE WHEN entry_id = cut_entry THEN cut_index ELSE -1 END, search_text FROM cuts
WHERE cut_entry IS NULL OR position < cut_entry OR entry_id = cut_entry
ORDER BY conversation_id, position, created_at_unix_ns NULLS FIRST, turn_id`

func containsPattern(text string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text) + "%"
}

type MessageSearchHit struct {
	ConversationID, Role, Text string
	MessageID                  string
}

type messageSearchDecoder struct {
	input  strings.Reader
	values jsontext.Decoder
}

func (d *messageSearchDecoder) decode(line string) (replay int, fields [4]string, err error) {
	var part int

	d.input.Reset(line)
	d.values.Reset(&d.input, json.DefaultOptionsV1())
	err = errors.Join(jsonv2.UnmarshalDecode(&d.values, &replay), jsonv2.UnmarshalDecode(&d.values, &part), jsonv2.UnmarshalDecode(&d.values, &fields))
	d.input.Reset("")

	if err != nil {
		replay, part, fields = 0, 0, [4]string{}
		values := json.NewDecoder(strings.NewReader(line))
		err = errors.Join(values.Decode(&replay), values.Decode(&part), values.Decode(&fields))
	}

	return replay, fields, err
}

func (s *SessionService) SearchMessages(ctx context.Context, needle string, tagPrefixes []string) ([]MessageSearchHit, error) {
	needles := append([]string{needle}, tagPrefixes...)

	encoded, patterns := make([]string, len(needles)), make([]string, len(needles))
	for i, text := range needles {
		quoted := searchJSON(text)
		encoded[i] = quoted[1 : len(quoted)-1]
		patterns[i] = containsPattern(encoded[i])
	}

	var (
		hits    []MessageSearchHit
		decoder messageSearchDecoder
	)

	err := s.readParallelRows(ctx, searchMessagesSQL, "message search", func(row rowScanner) error {
		var (
			conversationID, text string
			entryID              int64
			cut                  int
		)
		if err := row.Scan(&conversationID, &entryID, &cut, &text); err != nil {
			return fmt.Errorf("scan message search row: %w", err)
		}

		for line := range strings.Lines(text) {
			if !slices.ContainsFunc(encoded, func(needle string) bool { return strings.Contains(line, needle) }) {
				continue
			}

			replay, fields, err := decoder.decode(line)
			if err != nil {
				return fmt.Errorf("decode message search text: %w", err)
			}

			if cut >= 0 && replay >= cut || !slices.ContainsFunc(needles, func(needle string) bool { return strings.Contains(fields[3], needle) }) {
				continue
			}

			hit := MessageSearchHit{ConversationID: conversationID, Role: fields[0], Text: fields[2]}
			if entryID != 0 {
				hit.MessageID = fmt.Sprintf("%d:%d", entryID, replay)
			}

			hits = append(hits, hit)
		}

		return nil
	}, pgx.QueryExecModeCacheDescribe, patterns)

	return hits, err
}

func (s *SessionService) SearchMessagesMentioning(ctx context.Context, needle string, slack slackLookup) (hits []MessageSearchHit, mentions map[string]string, err error) {
	mentions = make(map[string]string)

	for _, id := range slack.SlackTagsMatching(ctx, needle) {
		mentions[id] = "<@" + strings.ToLower(id)
		if strings.HasPrefix(id, "S") {
			mentions[id] = "<!subteam^" + strings.ToLower(id)
		}
	}

	hits, err = s.SearchMessages(ctx, needle, slices.Collect(maps.Values(mentions)))

	return hits, mentions, err
}

func MentionedIDs(hits []MessageSearchHit, mentions map[string]string) []string {
	if len(mentions) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(mentions))

	for _, hit := range hits {
		text := strings.ToLower(hit.Text)
		for id, prefix := range mentions {
			if strings.Contains(text, prefix) {
				seen[id] = struct{}{}
			}
		}
	}

	return slices.Sorted(maps.Keys(seen))
}
