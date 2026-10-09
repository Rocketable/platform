package backend

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
)

// SearchKey is the lowercased key of a session search filter term.
type SearchKey string

const (
	keyTag   SearchKey = "tag"
	keyCron  SearchKey = "cron"
	keyAgent SearchKey = "agent"
	keyRoom  SearchKey = "room"
	keyIs    SearchKey = "is"
	keySort  SearchKey = "sort"
)

// SearchTerm is a filter term of a session search query: its text, decoded value
// (lowercased for is: and sort:), and UTF-16 offsets in the query.
type SearchTerm struct {
	Key         SearchKey
	Text, Value string
	Start, End  int
}

// SessionField names the field of a found chat whose text holds the needle.
type SessionField string

const (
	fieldName    SessionField = "Name"
	fieldRoom    SessionField = "Room"
	fieldAgent   SessionField = "Agent"
	fieldSession SessionField = "Session"
	fieldOrigin  SessionField = "Origin"
	fieldPreview SessionField = "Preview"
)

// SidebarChat is a sidebar row with the room, cron and origin Web shows for it.
type SidebarChat struct {
	Session SidebarSession
	// Room is a Slack thread's stored channel name, or its channel ID when unknown;
	// RoomAgents are the agents that room allows. Both are empty for other chats.
	Room       string
	RoomAgents []string
	// Cron reports a chat a cron run created; CronName is that job's stem.
	Cron     bool
	CronName string
	// Origin is the lowercased origin text, empty without an origin.
	Origin string
}

// SessionMatch is a chat a session search found and the field whose text holds
// the needle; an empty Field means only message text did.
type SessionMatch struct {
	Chat  SidebarChat
	Field SessionField
	Text  string
}

// SessionSearch is how a session search read its query and what it found.
type SessionSearch struct {
	Terms []SearchTerm
	// Text is the query without its terms; Needle is Text lowercased.
	Text, Needle string
	Matches      []SessionMatch
	Hits         []MessageSearchHit
	// MentionIDs are the Slack user and group IDs whose mention a hit holds.
	MentionIDs                       []string
	IndexComplete, SummariesComplete bool
}

// parseSessionQuery reads query as Web's search regex
// /(?:^|\s)(is:(?:pinned|forked|cron)|sort:(?:newest|oldest)|(?:tag|cron|agent|room):("(?:[^"\\]|\\.)*"|\S+))(?=\s|$)/gi
// did, decoding quoted values as JSON; a value that is empty or does not decode
// leaves its term as text. text is query without each term and the one
// whitespace character before it, trimmed.
func parseSessionQuery(query string) (terms []SearchTerm, text string) {
	var rest strings.Builder

	kept, units := 0, 0 // units counts query[:kept] in UTF-16.

	for pos := 0; pos < len(query); {
		end := strings.IndexFunc(query[pos:], unicode.IsSpace)

		switch {
		case end == 0:
			_, size := utf8.DecodeRuneInString(query[pos:])
			pos += size

			continue
		case end < 0:
			end = len(query)
		default:
			end += pos
		}

		key, value, found := strings.Cut(query[pos:end], ":")
		term := SearchTerm{Key: SearchKey(strings.ToLower(key)), Value: strings.ToLower(value)}

		switch term.Key {
		case keyIs:
			found = found && (term.Value == "pinned" || term.Value == "forked" || term.Value == "cron")
		case keySort:
			found = found && (term.Value == "newest" || term.Value == "oldest")
		case keyTag, keyCron, keyAgent, keyRoom:
			term.Value = value
			if strings.HasPrefix(value, `"`) {
				start := pos + len(key) + 1
				// A closed quote followed by whitespace or the end may span whitespace.
				for i := start + 1; i < len(query); i++ {
					if query[i] == '\\' {
						i++
					} else if query[i] == '"' {
						if after := query[i+1:]; after == "" || strings.IndexFunc(after, unicode.IsSpace) == 0 {
							end = i + 1
						}

						break
					}
				}

				found = json.Unmarshal([]byte(query[start:end]), &term.Value) == nil
			}

			found = found && term.Value != ""
		default:
			found = false
		}

		if found {
			cut := pos
			if pos > 0 {
				_, size := utf8.DecodeLastRuneInString(query[:pos])
				cut -= size
			}

			rest.WriteString(query[kept:cut])

			for i, r := range query[kept:end] {
				if kept+i == pos {
					term.Start = units
				}

				units += utf16.RuneLen(r)
			}

			kept, term.End = end, units
			term.Text = query[pos:end]
			terms = append(terms, term)
		}

		pos = end
	}

	rest.WriteString(query[kept:])

	return terms, strings.TrimSpace(rest.String())
}

// SidebarChats yields SidebarSessions with their room, resolved through slack
// once per channel, cron and origin.
func (s *SessionService) SidebarChats(ctx context.Context, slack slackLookup) iter.Seq2[SidebarChat, error] {
	return func(yield func(SidebarChat, error) bool) {
		var origins map[string]SidebarChat

		rooms := make(map[string]SidebarChat)

		for row, err := range s.SidebarSessions(ctx) {
			if err != nil {
				yield(SidebarChat{}, err)
				return
			}

			// Read origins after the row snapshot: a first cron sync must not add
			// a listed chat newer than its origin facts.
			if origins == nil {
				origins = make(map[string]SidebarChat)

				for facts, err := range s.ChatOriginFacts(ctx, "") {
					if err != nil {
						yield(SidebarChat{}, err)
						return
					}

					origin, text := DecideOrigin(&facts)
					cron, ok := origin.(cronOrigin)
					origins[facts.ConversationID] = SidebarChat{Cron: ok, CronName: cron.Stem, Origin: text}
				}
			}

			chat := origins[row.Conversation.ID]
			chat.Session = row

			if channel, _, ok := protocol.SlackThreadTarget(row.Conversation.ID); ok {
				room, loaded := rooms[channel]
				if !loaded {
					if room.Room, room.RoomAgents, err = slack.SidebarChannelAgentChoices(ctx, channel); err != nil {
						yield(SidebarChat{}, fmt.Errorf("resolve sidebar room: %w", err))
						return
					}

					rooms[channel] = room
				}

				chat.Room, chat.RoomAgents = room.Room, room.RoomAgents
			}

			if !yield(chat, nil) {
				return
			}
		}
	}
}

// passes reports whether c passes every filter term.
func (c *SidebarChat) passes(terms []SearchTerm) bool {
	for _, term := range terms {
		var ok bool

		switch term.Key {
		case keyTag:
			ok = slices.Contains(c.Session.Tags, term.Value)
		case keyCron:
			ok = c.CronName == term.Value
		case keyAgent:
			ok = c.Session.Conversation.Agent == term.Value
		case keyRoom:
			ok = c.Room == term.Value
		case keyIs:
			ok = term.Value == "pinned" && c.Session.Pinned || term.Value == "forked" && c.Session.ForkedFrom != "" || term.Value == "cron" && c.Cron
		case keySort:
			ok = true
		}

		if !ok {
			return false
		}
	}

	return true
}

// sessionLabel is the label Web shows for a chat without a name or preview.
func sessionLabel(id string) string {
	if rest, ok := strings.CutPrefix(id, "slack-thread:"); ok {
		if channel, _, _ := strings.Cut(rest, ":"); channel != "" {
			return "slack " + channel
		}
	}

	return strings.TrimPrefix(id, "web-session:")
}

// SearchSessions reads query in Web's session search language and finds, among
// SidebarChats, those passing every filter term whose fields hold the needle and,
// when messages is set, those with message hits. Without a sort: term they keep
// sidebar order, chats with hits first; with one, they follow summary time.
func (s *SessionService) SearchSessions(ctx context.Context, query string, messages bool, slack slackLookup) (*SessionSearch, error) {
	search := &SessionSearch{IndexComplete: true, SummariesComplete: true}
	search.Terms, search.Text = parseSessionQuery(query)
	search.Needle = strings.ToLower(search.Text)

	var chats []SidebarChat

	hits := make(map[string]bool) // Whether each chat that passed the filters has hits.

	for chat, err := range s.SidebarChats(ctx, slack) {
		if err != nil {
			return nil, err
		}

		search.SummariesComplete = search.SummariesComplete && chat.Session.Summary != nil

		if chat.passes(search.Terms) {
			chats = append(chats, chat)
			hits[chat.Session.Conversation.ID] = false
		}
	}

	if messages && search.Needle != "" {
		found, mentions, complete, err := s.SearchMessagesMentioning(ctx, search.Needle, slack)
		if err != nil {
			return nil, err
		}

		for _, hit := range found {
			if _, passed := hits[hit.ConversationID]; passed {
				hits[hit.ConversationID] = true
				search.Hits = append(search.Hits, hit)
			}
		}

		search.MentionIDs = MentionedIDs(search.Hits, mentions)
		search.IndexComplete = complete
	}

	for _, withHits := range []bool{true, false} {
		for i := range chats {
			chat := &chats[i]

			row := &chat.Session
			if hits[row.Conversation.ID] != withHits {
				continue
			}

			label := sessionLabel(row.Conversation.ID)

			var preview string
			if row.Summary != nil {
				preview = row.Summary.LastMessage
			}

			if !withHits && !strings.Contains(strings.ToLower(row.Name+" "+chat.Room+" "+preview+" "+row.Conversation.Agent+" "+label), search.Needle) && !strings.Contains(chat.Origin, search.Needle) {
				continue
			}

			match := SessionMatch{Chat: *chat}

			for _, field := range []struct {
				name SessionField
				text string
			}{{fieldName, row.Name}, {fieldRoom, chat.Room}, {fieldAgent, row.Conversation.Agent}, {fieldSession, label}, {fieldOrigin, chat.Origin}} {
				if search.Needle != "" && strings.Contains(strings.ToLower(field.text), search.Needle) {
					match.Field, match.Text = field.name, field.text
					break
				}
			}

			if match.Field == "" && !withHits {
				match.Field, match.Text = fieldPreview, cmp.Or(preview, label)
			}

			search.Matches = append(search.Matches, match)
		}
	}

	var sort string

	for _, term := range search.Terms {
		if term.Key == keySort {
			sort = term.Value
		}
	}

	if sort != "" {
		// Missing times sort last, then everything by bytewise ID.
		slices.SortFunc(search.Matches, func(a, b SessionMatch) int {
			var at, bt time.Time
			if a.Chat.Session.Summary != nil {
				at = a.Chat.Session.Summary.LastUpdated
			}

			if b.Chat.Session.Summary != nil {
				bt = b.Chat.Session.Summary.LastUpdated
			}

			order := at.Compare(bt)

			switch {
			case at.IsZero() || bt.IsZero():
				order = bt.Compare(at)
			case sort == "newest":
				order = -order
			}

			return cmp.Or(order, strings.Compare(a.Chat.Session.Conversation.ID, b.Chat.Session.Conversation.ID))
		})
	}

	return search, nil
}
