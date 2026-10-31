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

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"
)

// SearchKey is the lowercased key of a session search filter term.
type SearchKey string

const (
	keyTag       SearchKey = "tag"
	keyCron      SearchKey = "cron"
	keyAgent     SearchKey = "agent"
	keyRoom      SearchKey = "room"
	keyIs        SearchKey = "is"
	keySort      SearchKey = "sort"
	keyPrincipal SearchKey = "principal"
	keyServer    SearchKey = "server"
	keyModel     SearchKey = "model"
	keySpace     SearchKey = "space"
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

// SidebarChat is a sidebar row with the room and cron Web shows for it.
type SidebarChat struct {
	Session SidebarSession
	// Room is a Slack thread's stored channel name, or its channel ID when unknown; empty for other chats.
	Room string
	// Cron reports a chat a cron run created; CronName is that job's stem.
	Cron     bool
	CronName string
}

// SearchScope is what a session search's free text matches.
type SearchScope int

const (
	// SearchFields matches names, rooms, previews, agents, session labels,
	// servers, folders and origins.
	SearchFields SearchScope = iota
	// SearchMessages matches the fields and message text.
	SearchMessages
	// SearchFilters applies only the filters and leaves free text to the
	// caller, which matches it against the titles it shows.
	SearchFilters
)

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
	MentionIDs []string
	Principals []string
}

// parseSessionQuery reads query as Web's search regex
// /(?:^|\s)(is:(?:pinned|forked|cron|unread|running|queued|waiting|external)|sort:(?:newest|oldest)|(?:tag|cron|agent|room|principal|server|model|space):("(?:[^"\\]|\\.)*"|\S+))(?=\s|$)/gi
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
			found = found && slices.Contains([]string{"pinned", "forked", "cron", "unread", "running", "queued", "waiting", "external"}, term.Value)
		case keySort:
			found = found && (term.Value == "newest" || term.Value == "oldest")
		case keyTag, keyCron, keyAgent, keyRoom, keyPrincipal, keyServer, keyModel, keySpace:
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

// SidebarChats yields the sidebar's chats, or only those of chats when not nil, with their room,
// resolved through slack once per channel, cron and username's unread marks.
func (s *SessionService) SidebarChats(ctx context.Context, username string, slack slackLookup, chats []string) iter.Seq2[SidebarChat, error] {
	return func(yield func(SidebarChat, error) bool) {
		rooms := make(map[string]SidebarChat)

		for row, err := range s.sidebarSessions(ctx, username, chats) {
			var chat SidebarChat
			if err == nil {
				chat, err = sidebarChat(ctx, slack, rooms, &row)
			}

			if !yield(chat, err) || err != nil {
				return
			}
		}
	}
}

// sidebarChat is row with its cron and room (sidebarRoom).
func sidebarChat(ctx context.Context, slack slackLookup, rooms map[string]SidebarChat, row *SidebarSession) (SidebarChat, error) {
	chat := SidebarChat{Session: *row, Cron: row.Origin.Run != nil && row.Origin.Binding.ManagedConversationID != row.Origin.ConversationID}
	if chat.Cron {
		// A routine at agents/<agent>.md#<schedule> is named by its agent.
		name, _, _ := strings.Cut(row.Origin.Run.Path, ".md#")
		chat.CronName = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(name, "cron/"), "agents/"), ".md")
	}

	if row.SlackChannel != "" {
		room, err := sidebarRoom(ctx, slack, rooms, row.SlackChannel)
		if err != nil {
			return SidebarChat{}, err
		}

		chat.Room = room.Room
	}

	return chat, nil
}

// sidebarRoom is the name of channel, which rooms holds once resolved through slack.
func sidebarRoom(ctx context.Context, slack slackLookup, rooms map[string]SidebarChat, channel string) (SidebarChat, error) {
	room, loaded := rooms[channel]
	if !loaded {
		var err error
		if room.Room, _, err = slack.SidebarChannelAgentChoices(ctx, channel); err != nil {
			return SidebarChat{}, fmt.Errorf("resolve sidebar room: %w", err)
		}

		rooms[channel] = room
	}

	return room, nil
}

// searchCandidates returns the chats Web lists and the remote sessions whose fields or origin may
// hold needle, a text without spaces lowercased: those whose search words (migration 052) or running
// prompt hold it, or whose producer's do, or whose room does, which it resolves into rooms. A needle
// with a non-ASCII rune is only in texts holding a rune Go lowercases into it, one of its simple
// case folds, which lowercasing ASCII letters keeps.
func (s *SessionService) searchCandidates(ctx context.Context, slack slackLookup, rooms map[string]SidebarChat, needle string) ([]string, error) {
	patterns := []string{containsPattern(needle)}

	if i := strings.IndexFunc(needle, func(r rune) bool { return r >= utf8.RuneSelf }); i >= 0 {
		r, _ := utf8.DecodeRuneInString(needle[i:])
		patterns = []string{containsPattern(string(r))}

		for fold := unicode.SimpleFold(r); fold != r; fold = unicode.SimpleFold(fold) {
			patterns = append(patterns, containsPattern(string(fold)))
		}
	}

	channels, err := queryStrings(ctx, s.db, `SELECT DISTINCT slack_channel FROM conversations WHERE listed AND slack_channel IS NOT NULL`, "sidebar Slack channels")
	if err != nil {
		return nil, err
	}

	matched := []string{}

	for _, channel := range channels {
		room, err := sidebarRoom(ctx, slack, rooms, channel)
		if err != nil {
			return nil, err
		}

		if strings.Contains(strings.ToLower(room.Room), needle) {
			matched = append(matched, channel)
		}
	}

	candidates := []string{} // Not nil, which would read every chat.
	err = s.readParallelRows(ctx, `WITH own AS (
    SELECT conversation_id, listed OR server IS NOT NULL AS shown, cron_run_at_unix_ns IS NOT NULL AS run FROM conversations WHERE search_words LIKE ANY ($6)
), `+visibleChatsSQL+chatOriginSQL+`, found AS (
    SELECT conversation_id FROM own WHERE shown
    UNION ALL SELECT c.conversation_id FROM creating c JOIN own p ON p.conversation_id = c.creating_source AND p.run
    UNION ALL SELECT c.conversation_id FROM visible_chats c JOIN turns t ON t.conversation_id = c.conversation_id AND t.phase NOT IN ('waiting', 'done') AND t.takes_lane
    WHERE lower(replace(replace(t.inbound_json->>'Text', U&'\0130', 'i'), U&'\212A', 'k') COLLATE "C") LIKE ANY ($6)
    -- A needle holding what JSON escapes, or spanning a pair, can only be read from the decoded pairs.
    UNION ALL SELECT conversation_id FROM origins c WHERE $5::text[] <> '{}' AND c.slack_channel = ANY($5) OR $4 ~ '[="\\[:cntrl:]]' AND c.pairs <> 'null'
        OR (c.managed = c.conversation_id) <> (c.run_at IS NOT NULL) AND strpos('cron source: stem: run kind: scheduled one-off id: agent: ran at: external mcp conversation:', $4) > 0
)
SELECT DISTINCT conversation_id FROM found`, "session search candidates", func(row rowScanner) error {
		candidates = append(candidates, "")
		return row.Scan(&candidates[len(candidates)-1])
	}, pgx.QueryExecModeCacheDescribe, "", ThreadCreatedByCron, KindOneOffCronRun, needle, matched, patterns)

	return candidates, err
}

// passes requires any room term to match, any agent term to match, and every other
// filter term. An agent term matches the chat's agent or one of sandboxed, the agents
// of the hidden runs it shows. A model term matches part of the chat's model, ignoring case.
func (c *SidebarChat) passes(terms []SearchTerm, principals, sandboxed []string, spaces []Space) bool {
	hasRoom, roomMatch, hasAgent, agentMatch := false, false, false, false

	for _, term := range terms {
		var ok bool

		switch term.Key {
		case keyTag:
			ok = slices.Contains(c.Session.Tags, term.Value)
		case keyCron:
			ok = c.CronName == term.Value
		case keyAgent:
			hasAgent, agentMatch = true, agentMatch || c.Session.Conversation.Agent == term.Value || slices.Contains(sandboxed, term.Value)
			continue
		case keyPrincipal:
			ok = slices.Contains(principals, term.Value)
		case keyServer:
			ok = c.Session.Server == term.Value
		case keySpace:
			ok = slices.ContainsFunc(spaces, func(space Space) bool {
				return space.ID == c.Session.SpaceID && strings.EqualFold(space.Name, term.Value)
			})
		case keyModel:
			ok = strings.Contains(strings.ToLower(c.Session.Model), strings.ToLower(term.Value))
		case keyRoom:
			hasRoom, roomMatch = true, roomMatch || c.Room == term.Value
			continue
		case keyIs:
			ok = c.is(term.Value)
		case keySort:
			ok = true
		}

		if !ok {
			return false
		}
	}

	return (!hasRoom || roomMatch) && (!hasAgent || agentMatch)
}

func (c *SidebarChat) is(value string) bool {
	return value == "pinned" && c.Session.Pinned || value == "forked" && c.Session.ForkedFrom != "" || value == "cron" && c.Cron ||
		value == "unread" && !c.Session.UnreadAt.IsZero() || value == "running" && (c.Session.Running || c.Session.Background) ||
		value == "queued" && c.Session.Queued || value == "waiting" && c.Session.Asking || value == "external" && c.Session.External
}

func (c *SidebarChat) holds(needle string) bool {
	row := &c.Session

	var preview string
	if row.Summary != nil {
		preview = row.Summary.LastMessage
	}

	if needle == "" || strings.Contains(strings.ToLower(row.Name+" "+c.Room+" "+preview+" "+row.Conversation.Agent+" "+row.Label()+" "+row.Server+" "+row.Directory), needle) {
		return true
	}

	_, origin := DecideOrigin(&row.Origin)

	return strings.Contains(origin, needle)
}

// Label is what Web calls the chat when it has no name or preview.
func (row *SidebarSession) Label() string {
	if row.SlackChannel != "" {
		return "slack " + row.SlackChannel
	}

	return strings.TrimPrefix(row.Conversation.ID, "web-session:")
}

// SearchSessions reads query in Web's session search language and finds, among
// SidebarChats, those passing the filters whose scope's text holds the needle;
// SearchMessages also finds those with message hits. Without a sort: term they keep
// sidebar order, chats with hits first; with one, they follow summary time.
// Either way, chats whose whole model is a model: value come first. Rows carry username's
// unread marks; an empty username is no person.
func (s *SessionService) SearchSessions(ctx context.Context, username, query string, scope SearchScope, slack slackLookup) (*SessionSearch, error) {
	search := &SessionSearch{}
	search.Terms, search.Text = parseSessionQuery(query)
	search.Needle = strings.ToLower(search.Text)

	var (
		principals map[string][]string
		found      []MessageSearchHit
		mentions   map[string]string
		chats      []string // nil reads every chat.
		sandboxed  = make(map[string][]string)
	)

	rooms := make(map[string]SidebarChat)
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() (err error) {
		principals, search.Principals, err = s.messagePrincipals(groupCtx)
		return err
	})

	group.Go(func() error { return s.sandboxedAgents(groupCtx, search.Terms, sandboxed) })

	if scope == SearchMessages && search.Needle != "" {
		group.Go(func() (err error) {
			found, mentions, err = s.SearchMessagesMentioning(groupCtx, search.Needle, slack)
			return err
		})
	}

	// A needle without spaces reads only the chats that may match it, and those with hits;
	// any other search, and one leaving free text to the caller, reads every chat, alongside
	// the reads above.
	var rows []SidebarSession

	narrow := scope != SearchFilters && search.Needle != "" && !strings.Contains(search.Needle, " ")
	if narrow {
		group.Go(func() (err error) {
			chats, err = s.searchCandidates(groupCtx, slack, rooms, search.Needle)
			return err
		})
	} else {
		group.Go(func() (err error) {
			rows, err = s.sidebarRows(groupCtx, username, nil)
			return err
		})
	}

	if err := group.Wait(); err != nil {
		return nil, fmt.Errorf("search sessions: %w", err)
	}

	if narrow {
		var err error
		if rows, err = s.sidebarRows(ctx, username, readChats(chats, found)); err != nil {
			return nil, err
		}
	}

	agents, spaces, err := s.searchDefinitions(ctx, search.Terms)
	if err != nil {
		return nil, err
	}

	var listed []SidebarChat

	hits := make(map[string]bool) // Whether each chat that passed the filters has hits.

	for i := range rows {
		chat, err := sidebarChat(ctx, slack, rooms, &rows[i])
		if err != nil {
			return nil, err
		}

		if chat.Session.Server == "" {
			chat.Session.Model = agents.Items[chat.Session.Conversation.Agent].Model
		}

		if chat.passes(search.Terms, principals[chat.Session.Conversation.ID], sandboxed[chat.Session.Conversation.ID], spaces) {
			listed = append(listed, chat)
			hits[chat.Session.Conversation.ID] = false
		}
	}

	for _, hit := range found {
		if _, passed := hits[hit.ConversationID]; passed {
			hits[hit.ConversationID] = true
			search.Hits = append(search.Hits, hit)
		}
	}

	search.MentionIDs = MentionedIDs(search.Hits, mentions)

	for _, withHits := range []bool{true, false} {
		for i := range listed {
			chat := &listed[i]

			row := &chat.Session
			if hits[row.Conversation.ID] != withHits {
				continue
			}

			if scope == SearchFilters {
				search.Matches = append(search.Matches, SessionMatch{Chat: *chat})
				continue
			}

			if !withHits && !chat.holds(search.Needle) {
				continue
			}

			label := row.Label()
			_, origin := DecideOrigin(&row.Origin)

			var preview string
			if row.Summary != nil {
				preview = row.Summary.LastMessage
			}

			match := SessionMatch{Chat: *chat}

			for _, field := range []struct {
				name SessionField
				text string
			}{{fieldName, row.Name}, {fieldRoom, chat.Room}, {fieldAgent, row.Conversation.Agent}, {fieldSession, label}, {fieldOrigin, origin}} {
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

	search.sortMatches()

	return search, nil
}

// readChats are the chats a narrow search reads: candidates and those with hits, each once, or nil
// for every chat when they are many, as reading a long list costs more than reading every chat,
// which SearchSessions filters the same way.
func readChats(candidates []string, hits []MessageSearchHit) []string {
	for _, hit := range hits {
		candidates = append(candidates, hit.ConversationID)
	}

	slices.Sort(candidates)

	if candidates = slices.Compact(candidates); len(candidates) > 2000 {
		return nil
	}

	return candidates
}

// BoardLane is a Dashboard lane: up to 200 of its chats, newest arrival first, and how many chats
// it holds.
type BoardLane struct {
	Chats []SidebarChat
	Total int
}

// Board sorts the chats of space spaceID into the lanes of each Dashboard, given in lane order as
// session search queries whose free text matches fields only. The space holds the chats born there,
// those without a Birth Space whose agent's space has its name ignoring case, and those with neither;
// an unknown space holds none. Each chat goes to its Dashboard's first lane it matches. Chats are
// as SidebarChats yields them, with username's unread marks.
func (s *SessionService) Board(ctx context.Context, username, spaceID string, dashboards [][]string, slack slackLookup) ([][]BoardLane, error) {
	type lane struct {
		terms  []SearchTerm
		needle string
	}

	var every []SearchTerm

	lanes, board := make([][]lane, len(dashboards)), make([][]BoardLane, len(dashboards))
	for i, queries := range dashboards {
		board[i] = make([]BoardLane, len(queries))

		for _, query := range queries {
			terms, text := parseSessionQuery(query)
			lanes[i] = append(lanes[i], lane{terms, strings.ToLower(text)})
			every = append(every, terms...)
		}
	}

	var (
		spaces     []Space
		rows       []SidebarSession
		agents     harness.Agents
		principals map[string][]string
		sandboxed  = make(map[string][]string)
	)

	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() (err error) {
		spaces, err = s.Spaces(groupCtx)
		return err
	})
	group.Go(func() (err error) {
		rows, err = s.sidebarRows(groupCtx, username, nil)
		return err
	})
	group.Go(func() (err error) {
		agents, _, err = LoadRuntimeDefinitions(s.definitions, s.definitions.Clone().RuntimeDirName())
		return err
	})
	group.Go(func() error { return s.sandboxedAgents(groupCtx, every, sandboxed) })

	if slices.ContainsFunc(every, func(term SearchTerm) bool { return term.Key == keyPrincipal }) {
		group.Go(func() (err error) {
			principals, _, err = s.messagePrincipals(groupCtx)
			return err
		})
	}

	if err := group.Wait(); err != nil {
		return nil, fmt.Errorf("sort board: %w", err)
	}

	space := slices.IndexFunc(spaces, func(space Space) bool { return space.ID == spaceID })
	if space < 0 {
		return board, nil
	}

	rows = slices.DeleteFunc(rows, func(row SidebarSession) bool {
		agent := agents.Items[row.Conversation.Agent]
		return row.SpaceID != spaceID && (row.SpaceID != "" || agent.Space != "" && !strings.EqualFold(agent.Space, spaces[space].Name))
	})
	slices.SortFunc(rows, func(a, b SidebarSession) int {
		return cmp.Or(b.ArrivedAt.Compare(a.ArrivedAt), strings.Compare(a.Conversation.ID, b.Conversation.ID))
	})

	rooms := make(map[string]SidebarChat)

	for n := range rows {
		row := &rows[n]

		chat, err := sidebarChat(ctx, slack, rooms, row)
		if err != nil {
			return nil, err
		}

		// A model: term matches a RocketClaw chat's agent model; its card keeps the stored one.
		matched := chat
		if row.Server == "" {
			matched.Session.Model = agents.Items[row.Conversation.Agent].Model
		}

		id := row.Conversation.ID

		for i := range lanes {
			first := slices.IndexFunc(lanes[i], func(lane lane) bool {
				return matched.passes(lane.terms, principals[id], sandboxed[id], spaces) && matched.holds(lane.needle)
			})
			if first < 0 {
				continue
			}

			found := &board[i][first]

			found.Total++
			if found.Total <= 200 {
				found.Chats = append(found.Chats, chat)
			}
		}
	}

	return board, nil
}

func (s *SessionService) sidebarRows(ctx context.Context, username string, chats []string) ([]SidebarSession, error) {
	var rows []SidebarSession

	for row, err := range s.sidebarSessions(ctx, username, chats) {
		if err != nil {
			return nil, err
		}

		rows = append(rows, row)
	}

	return rows, nil
}

// searchDefinitions loads only the definitions the query's model: and space: terms need.
func (s *SessionService) searchDefinitions(ctx context.Context, terms []SearchTerm) (agents harness.Agents, spaces []Space, err error) {
	if slices.ContainsFunc(terms, func(term SearchTerm) bool { return term.Key == keySpace }) {
		spaces, err = s.Spaces(ctx)
		if err != nil {
			return agents, nil, err
		}
	}

	if !slices.ContainsFunc(terms, func(term SearchTerm) bool { return term.Key == keyModel }) {
		return agents, spaces, nil
	}

	agents, _, err = LoadRuntimeDefinitions(s.definitions, s.definitions.Clone().RuntimeDirName())
	if err != nil {
		return agents, spaces, fmt.Errorf("load agents for model search: %w", err)
	}

	return agents, spaces, nil
}

// sandboxedAgents adds to sandboxed, for an agent: term, the agents of the hidden runs each
// chat shows: those reporting to it and the cron run it continues.
func (s *SessionService) sandboxedAgents(ctx context.Context, terms []SearchTerm, sandboxed map[string][]string) error {
	if !slices.ContainsFunc(terms, func(term SearchTerm) bool { return term.Key == keyAgent }) {
		return nil
	}

	return readRows(ctx, s.db, `SELECT reports_to, agent FROM conversations WHERE reports_to IS NOT NULL UNION SELECT c.conversation_id, r.agent FROM conversations c JOIN conversations r ON r.conversation_id = c.continues`, "sandboxed agents", func(row rowScanner) error {
		var chat, agent string
		if err := row.Scan(&chat, &agent); err != nil {
			return fmt.Errorf("scan sandboxed agent: %w", err)
		}

		sandboxed[chat] = append(sandboxed[chat], agent)

		return nil
	})
}

// sortMatches puts chats whose whole model is a model: value, ignoring case,
// ahead of partial model matches, then orders each part by summary time for a
// sort: term.
func (search *SessionSearch) sortMatches() {
	var sort string

	for _, term := range search.Terms {
		if term.Key == keySort {
			sort = term.Value
		}
	}

	whole := func(match *SessionMatch) int {
		if slices.ContainsFunc(search.Terms, func(term SearchTerm) bool {
			return term.Key == keyModel && strings.EqualFold(term.Value, match.Chat.Session.Model)
		}) {
			return 1
		}

		return 0
	}

	slices.SortStableFunc(search.Matches, func(a, b SessionMatch) int {
		if order := whole(&b) - whole(&a); order != 0 || sort == "" {
			return order
		}

		// Missing times sort last, then everything by bytewise ID.
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
