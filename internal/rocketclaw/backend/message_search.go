package backend

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	harness "github.com/Rocketable/platform/internal/rocketcode"
	"github.com/jackc/pgx/v5"
)

// indexEntryMessages indexes the user and assistant text History shows for a saved
// entry (entryID) or an ended turn's record (turnID) of conversationID. producer
// owns the attachments its prompts reference. History hides an entry without a
// producer, and search never reads cron producers, so those get no rows. A '/'
// does not mark a Delegation History, since Web chats of cron handoffs have
// one; Delegation Histories are indexed and kept out by search visibility.
// Replay that cannot be decoded fails with a *harness.ReplayDecodeError before
// any row is written.
func indexEntryMessages(ctx context.Context, db stateStoreDB, conversationID, producer string, entryID int64, turnID string, replay []json.RawMessage) error {
	if producer == "" || strings.HasPrefix(conversationID, "cron:") || strings.HasPrefix(conversationID, "one-off-cron:") {
		return nil
	}

	items, err := harness.ReplayInputToParams(replay)
	if err != nil {
		return fmt.Errorf("decode message search replay input: %w", err)
	}

	var (
		positions, parts     []int
		roles, texts, lowers []string
	)

	for i := range items {
		role, text, ok, err := ReplayInputMessageRoleText(&items[i], replay[i])
		if err != nil {
			return &harness.ReplayDecodeError{EntryIndex: -1, ItemIndex: i, Cause: err}
		}

		text = strings.ReplaceAll(text, "\x00", "")
		if !ok || role != "user" && role != "assistant" || strings.TrimSpace(text) == "" {
			continue
		}

		shown := []string{text}

		if role == "user" {
			shown[0], _, err = inputAttachmentsDB(ctx, db, producer, text)
			if err != nil {
				return err
			}
		} else {
			var identity struct {
				ID      string          `json:"id"`
				Content json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(replay[i], &identity); err != nil {
				return &harness.ReplayDecodeError{EntryIndex: -1, ItemIndex: i, Cause: fmt.Errorf("decode message search identity: %w", err)}
			}
			// History shows each output_text part of an assistant item with an ID
			// on its own (frontend/rpc/server.go publicText).
			if identity.ID != "" && len(identity.Content) > 0 && identity.Content[0] == '[' {
				var content []struct{ Type, Text string }
				if err := json.Unmarshal(identity.Content, &content); err != nil {
					return &harness.ReplayDecodeError{EntryIndex: -1, ItemIndex: i, Cause: fmt.Errorf("decode message search output text: %w", err)}
				}

				shown = make([]string, len(content))
				for part := range content {
					if content[part].Type == "output_text" {
						shown[part] = content[part].Text
					}
				}
			}
		}

		for part, text := range shown {
			text = strings.ReplaceAll(text, "\x00", "")
			if strings.TrimSpace(text) != "" {
				positions, parts, roles, texts, lowers = append(positions, i), append(parts, part), append(roles, role), append(texts, text), append(lowers, strings.ToLower(text))
			}
		}
	}

	if len(positions) == 0 {
		return nil
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO message_search (conversation_id, entry_id, turn_id, replay_index, part_index, role, text, text_lower)
SELECT $1, NULLIF($2::bigint, 0), NULLIF($3::text, ''), * FROM unnest($4::integer[], $5::integer[], $6::text[], $7::text[], $8::text[])`, conversationID, entryID, turnID, positions, parts, roles, texts, lowers); err != nil {
		return fmt.Errorf("index message search: %w", err)
	}

	return nil
}

// indexTurnMessages replaces the rows of turnID, a turn of conversationID, with
// its record's text while History shows the ended turn: until a saved entry of
// the turn hides it (observeTranscriptDB). The caller holds the history lock. A
// record that cannot be decoded is logged and gets no rows, so the turn still ends.
func (s *SessionService) indexTurnMessages(ctx context.Context, db stateStoreDB, conversationID, turnID string) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM message_search WHERE turn_id = $1`, turnID); err != nil {
		return fmt.Errorf("clear turn message search: %w", err)
	}

	var raw []byte

	err := db.QueryRowContext(ctx, `WITH `+sessionHistorySQL+`
SELECT COALESCE(s.value::jsonb->'record', '{}') FROM active_turns a JOIN turn_steps s ON s.conversation_id = a.conversation_id AND s.key = a.id
WHERE a.id = $2 AND a.conversation_id = $1 AND a.terminal <> ''
    AND NOT EXISTS (SELECT 1 FROM physical_entries WHERE entry->>'turn_id' = a.id AND (NOT synced OR source_conversation_id = $1))`, conversationID, turnID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("load turn record: %w", err)
	}

	var record struct {
		ReplayInput []json.RawMessage `json:"replay_input"`
	}

	err = json.Unmarshal(raw, &record)
	if err == nil {
		err = indexEntryMessages(ctx, db, conversationID, conversationID, 0, turnID, record.ReplayInput)
		if _, ok := errors.AsType[*harness.ReplayDecodeError](err); !ok {
			return err
		}
	}

	s.log.Error("leave undecodable turn record out of message search", "conversation_id", conversationID, "turn_id", turnID, "error", err)

	return nil
}

// markNewChatIndexed marks a chat the caller is about to create under its
// history lock, unless it already exists or has history an older binary may
// have saved unindexed. Every later write to the chat indexes itself.
func markNewChatIndexed(ctx context.Context, db stateStoreDB, conversationID string) error {
	if _, err := db.ExecContext(ctx, `INSERT INTO message_search_indexed (conversation_id) SELECT $1::text
WHERE NOT EXISTS (SELECT 1 FROM managed_conversations WHERE conversation_id = $1) AND NOT EXISTS (SELECT 1 FROM session_entries WHERE conversation_id = $1)
    AND NOT EXISTS (SELECT 1 FROM active_turns WHERE conversation_id = $1)
ON CONFLICT DO NOTHING`, conversationID); err != nil {
		return fmt.Errorf("mark new chat message search: %w", err)
	}

	return nil
}

// visibleChatsSQL keeps the managed_conversations rows c that Web shows
// (ChatOriginFacts); a nonempty $1 restricts it to that chat.
const visibleChatsSQL = `($1 = '' OR c.conversation_id = $1) AND c.conversation_id NOT LIKE 'cron:%' AND c.conversation_id NOT LIKE 'one-off-cron:%'
    AND NOT EXISTS (SELECT 1 FROM external_mcp_sessions p WHERE p.private_conversation_id = c.conversation_id)`

// unindexedChatsSQL selects the chats Web shows that have no message search marker.
const unindexedChatsSQL = `SELECT c.conversation_id FROM managed_conversations c WHERE ` + visibleChatsSQL + `
    AND NOT EXISTS (SELECT 1 FROM message_search_indexed m WHERE m.conversation_id = c.conversation_id)`

// searchMessagesSQL applies the undo cutoff of effective_entries and
// observeTranscriptDB, and orders hits as ListConversations orders chats and
// History orders messages. $1 is empty and $2 holds the LIKE patterns.
const searchMessagesSQL = `SELECT NOT EXISTS (` + unindexedChatsSQL + `),
    COALESCE(json_agg(json_build_object('ConversationID', m.conversation_id, 'Role', m.role, 'Text', m.text, 'MessageID', COALESCE(m.entry_id || ':' || m.replay_index, ''))
        ORDER BY c.conversation_id, COALESCE(m.entry_id, a.history_anchor_id), a.created_at_unix_ns NULLS FIRST, m.turn_id, m.replay_index, m.part_index), 'null')
FROM managed_conversations c JOIN message_search m ON m.conversation_id = c.conversation_id LEFT JOIN active_turns a ON a.id = m.turn_id
WHERE ` + visibleChatsSQL + ` AND m.text_lower LIKE ANY ($2::text[])
    AND (c.revert_message_id = '' OR COALESCE(m.entry_id, a.history_anchor_id) < split_part(c.revert_message_id, ':', 1)::bigint
        OR m.entry_id = split_part(c.revert_message_id, ':', 1)::bigint AND m.replay_index < split_part(c.revert_message_id, ':', 2)::integer)`

// MessageSearchHit is a user or assistant message as History shows it.
type MessageSearchHit struct {
	ConversationID, Role, Text string
	// MessageID is History's "<entry ID>:<replay index>", empty for a stopped or failed turn.
	MessageID string
}

// SearchMessages returns, in chat then history order, the messages of chats Web
// shows whose lowercased text contains needle or one of tagPrefixes, which the
// caller lowercased with strings.ToLower. Messages a pending undo hides are left
// out. complete reports that every chat Web shows is indexed.
func (s *SessionService) SearchMessages(ctx context.Context, needle string, tagPrefixes []string) (hits []MessageSearchHit, complete bool, err error) {
	escape := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

	patterns := make([]string, 0, 1+len(tagPrefixes))
	for _, text := range append([]string{needle}, tagPrefixes...) {
		patterns = append(patterns, "%"+escape.Replace(text)+"%")
	}

	var raw []byte
	// An unnamed statement is planned for its patterns: a generic plan would
	// trust the trigram index even for needles too short to use it.
	if err := s.db.QueryRowContext(ctx, searchMessagesSQL, pgx.QueryExecModeExec, "", patterns).Scan(&complete, &raw); err != nil {
		return nil, false, fmt.Errorf("search messages: %w", err)
	}

	if err := json.Unmarshal(raw, &hits); err != nil {
		return nil, false, fmt.Errorf("decode message search hits: %w", err)
	}

	return hits, complete, nil
}

// SearchMessagesMentioning is SearchMessages for needle and the mentions of the
// Slack users and groups slack finds for it; mentions maps their IDs to the
// lowercased mention prefixes.
func (s *SessionService) SearchMessagesMentioning(ctx context.Context, needle string, slack slackLookup) (hits []MessageSearchHit, mentions map[string]string, complete bool, err error) {
	mentions = make(map[string]string)

	for _, id := range slack.SlackTagsMatching(ctx, needle) {
		mentions[id] = "<@" + strings.ToLower(id)
		if strings.HasPrefix(id, "S") {
			mentions[id] = "<!subteam^" + strings.ToLower(id)
		}
	}

	hits, complete, err = s.SearchMessages(ctx, needle, slices.Collect(maps.Values(mentions)))

	return hits, mentions, complete, err
}

// MentionedIDs returns, sorted, the IDs of mentions whose prefix a hit's
// lowercased text holds.
func MentionedIDs(hits []MessageSearchHit, mentions map[string]string) []string {
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

func (s *SessionService) backfillMessageSearch(ctx context.Context) error {
	for {
		conversationIDs, err := queryStrings(ctx, s.db, unindexedChatsSQL+` ORDER BY c.conversation_id COLLATE "C" LIMIT 100`, "unindexed message search chats", "")
		if err != nil {
			return err
		}

		if len(conversationIDs) == 0 {
			if _, err := s.db.ExecContext(ctx, `SELECT gin_clean_pending_list('message_search_text'::regclass)`); err != nil {
				return fmt.Errorf("clean message search pending list: %w", err)
			}

			return nil
		}

		for _, conversationID := range conversationIDs {
			if err := s.backfillChatMessageSearch(ctx, conversationID); err != nil {
				return err
			}
		}
	}
}

// backfillChatMessageSearch indexes every physical entry and ended turn of a
// chat, leaving out and logging entries it cannot decode, and marks the chat.
func (s *SessionService) backfillChatMessageSearch(ctx context.Context, conversationID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin message search backfill: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := lockSessionHistory(ctx, tx, conversationID); err != nil {
		return err
	}

	var unindexed bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (`+unindexedChatsSQL+`)`, conversationID).Scan(&unindexed); err != nil {
		return fmt.Errorf("check message search backfill: %w", err)
	}

	if !unindexed {
		return nil
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM message_search WHERE conversation_id = $1`, conversationID); err != nil {
		return fmt.Errorf("clear message search backfill: %w", err)
	}

	type physicalEntry struct {
		id            int64
		raw, producer string
	}

	entries, err := queryRows(ctx, tx, `WITH `+sessionHistorySQL+`
SELECT id, entry_json, CASE WHEN synced THEN source_conversation_id ELSE $1 END FROM physical_entries ORDER BY id`, "message search backfill entries", func(row rowScanner) (physicalEntry, error) {
		var entry physicalEntry
		if err := row.Scan(&entry.id, &entry.raw, &entry.producer); err != nil {
			return entry, fmt.Errorf("scan message search backfill entry: %w", err)
		}

		return entry, nil
	}, conversationID)
	if err != nil {
		return err
	}

	for _, physical := range entries {
		var entry struct {
			ReplayInput []json.RawMessage `json:"replay_input"`
		}

		err := json.Unmarshal([]byte(physical.raw), &entry)
		if err == nil {
			err = indexEntryMessages(ctx, tx, conversationID, physical.producer, physical.id, "", entry.ReplayInput)
			if _, ok := errors.AsType[*harness.ReplayDecodeError](err); err != nil && !ok {
				return err
			}
		}

		if err != nil {
			s.log.Error("leave damaged entry out of message search", "conversation_id", conversationID, "entry_id", physical.id, "error", err)
		}
	}

	turnIDs, err := queryStrings(ctx, tx, `SELECT id FROM active_turns WHERE conversation_id = $1 AND terminal <> '' ORDER BY id`, "message search backfill turns", conversationID)
	if err != nil {
		return err
	}

	for _, turnID := range turnIDs {
		if err := s.indexTurnMessages(ctx, tx, conversationID, turnID); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO message_search_indexed (conversation_id) VALUES ($1)`, conversationID); err != nil {
		return fmt.Errorf("mark message search backfill: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit message search backfill: %w", err)
	}

	return nil
}
