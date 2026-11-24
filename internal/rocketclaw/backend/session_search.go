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

type SearchKey string

const (
	keyTag       SearchKey = "tag"
	keyCron      SearchKey = "routine"
	keyAgent     SearchKey = "agent"
	keyRoom      SearchKey = "room"
	keyIs        SearchKey = "is"
	keySort      SearchKey = "sort"
	keyPrincipal SearchKey = "principal"
	keyServer    SearchKey = "server"
	keyModel     SearchKey = "model"
	keySpace     SearchKey = "space"
)

type SearchTerm struct {
	Key         SearchKey
	Text, Value string
	Start, End  int
}

type SessionField string

const (
	fieldName    SessionField = "Name"
	fieldRoom    SessionField = "Room"
	fieldAgent   SessionField = "Agent"
	fieldSession SessionField = "Session"
	fieldOrigin  SessionField = "Origin"
	fieldPreview SessionField = "Preview"
)

type SidebarChat struct {
	Session  SidebarSession
	Room     string
	Cron     bool
	CronName string
}

type SearchScope int

const (
	SearchFields SearchScope = iota
	SearchMessages
	SearchFilters
)

type SessionMatch struct {
	Chat  SidebarChat
	Field SessionField
	Text  string
}

type SessionSearch struct {
	Terms        []SearchTerm
	Text, Needle string
	Matches      []SessionMatch
	Hits         []MessageSearchHit
	MentionIDs   []string
	Principals   []string
}

func parseSessionQuery(query string) (terms []SearchTerm, text string) {
	if !strings.ContainsRune(query, ':') {
		return nil, strings.TrimSpace(query)
	}

	var rest strings.Builder

	kept, units := 0, 0

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
		if !found {
			pos = end

			continue
		}

		term := SearchTerm{Key: SearchKey(strings.ToLower(key)), Value: value}

		switch term.Key {
		case keyIs:
			term.Value = strings.ToLower(value)
			found = slices.Contains([]string{"forked", "routine", "unread", "running", "queued", "waiting", "external"}, term.Value)
		case keySort:
			term.Value = strings.ToLower(value)
			found = term.Value == "newest" || term.Value == "oldest"
		case keyTag, keyCron, keyAgent, keyRoom, keyPrincipal, keyServer, keyModel, keySpace:
			if strings.HasPrefix(value, `"`) {
				start := pos + len(key) + 1
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

func (s *SessionService) SidebarChats(ctx context.Context, username string, slack slackLookup, chats []string) iter.Seq2[SidebarChat, error] {
	return func(yield func(SidebarChat, error) bool) {
		rooms := make(map[string]string)

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

func sidebarChat(ctx context.Context, slack slackLookup, rooms map[string]string, row *SidebarSession) (SidebarChat, error) {
	chat := SidebarChat{Session: *row, Cron: row.Origin.Run != nil && row.Origin.Binding.ManagedConversationID != row.Origin.ConversationID}
	if chat.Cron {
		name, _, _ := strings.Cut(row.Origin.Run.Path, ".md#")
		chat.CronName = strings.TrimPrefix(name, "agents/")
	}

	if row.SlackChannel != "" {
		room, err := sidebarRoom(ctx, slack, rooms, row.SlackChannel)
		if err != nil {
			return SidebarChat{}, err
		}

		chat.Room = room
	}

	return chat, nil
}

func sidebarRoom(ctx context.Context, slack slackLookup, rooms map[string]string, channel string) (string, error) {
	if room, loaded := rooms[channel]; loaded {
		return room, nil
	}

	room, _, err := slack.SidebarChannelAgentChoices(ctx, channel)
	if err != nil {
		return "", fmt.Errorf("resolve sidebar room: %w", err)
	}

	rooms[channel] = room

	return room, nil
}

func (s *SessionService) searchCandidates(ctx context.Context, slack slackLookup, rooms map[string]string, needle string) ([]string, error) {
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

		if strings.Contains(strings.ToLower(room), needle) {
			matched = append(matched, channel)
		}
	}

	candidates := []string{}
	err = s.readParallelRows(ctx, `WITH own AS (
    SELECT conversation_id, listed OR server IS NOT NULL AS shown, cron_run_at_unix_ns IS NOT NULL AS run FROM conversations WHERE search_words LIKE ANY ($6)
), `+visibleChatsSQL+chatOriginSQL+`, found AS (
    SELECT conversation_id FROM own WHERE shown
    UNION ALL SELECT c.conversation_id FROM creating c JOIN own p ON p.conversation_id = c.creating_source AND p.run
    UNION ALL SELECT c.conversation_id FROM visible_chats c JOIN turns t ON t.conversation_id = c.conversation_id AND t.phase NOT IN ('waiting', 'done') AND t.takes_lane
    WHERE lower(replace(replace(t.inbound_json->>'Text', U&'\0130', 'i'), U&'\212A', 'k') COLLATE "C") LIKE ANY ($6)
    UNION ALL SELECT conversation_id FROM origins c WHERE $5::text[] <> '{}' AND c.slack_channel = ANY($5) OR $4 ~ '[="\\[:cntrl:]]' AND c.pairs <> 'null'
        OR (c.managed = c.conversation_id) <> (c.run_at IS NOT NULL) AND strpos('cron source: stem: run kind: scheduled one-off id: agent: ran at: external mcp conversation:', $4) > 0
)
SELECT DISTINCT conversation_id FROM found`, "session search candidates", func(row rowScanner) error {
		candidates = append(candidates, "")
		return row.Scan(&candidates[len(candidates)-1])
	}, pgx.QueryExecModeCacheDescribe, "", ThreadCreatedByCron, KindOneOffCronRun, needle, matched, patterns)

	return candidates, err
}

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
	return value == "forked" && c.Session.ForkedFrom != "" || value == "routine" && c.Cron ||
		value == "unread" && !c.Session.Attention.UnreadAt.IsZero() && !c.Session.Attention.Parked || value == "running" && (c.Session.Running || c.Session.Background) ||
		value == "queued" && c.Session.Queued || value == "waiting" && c.Session.Asking || value == "external" && c.Session.External
}

func (row *SidebarSession) Label() string {
	if row.SlackChannel != "" {
		return "slack " + row.SlackChannel
	}

	return strings.TrimPrefix(row.Conversation.ID, "web-session:")
}

func (s *SessionService) SearchSessions(ctx context.Context, username, query string, scope SearchScope, slack slackLookup) (*SessionSearch, error) {
	search := &SessionSearch{}
	search.Terms, search.Text = parseSessionQuery(query)
	search.Needle = strings.ToLower(search.Text)

	var (
		principals map[string][]string
		found      []MessageSearchHit
		mentions   map[string]string
		chats      []string
		sandboxed  map[string][]string
	)

	rooms := make(map[string]string)
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() (err error) {
		principals, search.Principals, err = s.messagePrincipals(groupCtx)
		return err
	})

	if slices.ContainsFunc(search.Terms, func(term SearchTerm) bool { return term.Key == keyAgent }) {
		sandboxed = make(map[string][]string)

		group.Go(func() error {
			return readRows(groupCtx, s.db, `SELECT reports_to, agent FROM conversations WHERE reports_to IS NOT NULL UNION SELECT c.conversation_id, r.agent FROM conversations c JOIN conversations r ON r.conversation_id = c.continues`, "sandboxed agents", func(row rowScanner) error {
				var chat, agent string
				if err := row.Scan(&chat, &agent); err != nil {
					return fmt.Errorf("scan sandboxed agent: %w", err)
				}

				sandboxed[chat] = append(sandboxed[chat], agent)

				return nil
			})
		})
	}

	if scope == SearchMessages && search.Needle != "" {
		group.Go(func() (err error) {
			found, mentions, err = s.SearchMessagesMentioning(groupCtx, search.Needle, slack)
			return err
		})
	}

	var rows []*SidebarSession

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

	if s.definitions.PermittedSpaces(username) != nil {
		search.Principals = nil
		for _, row := range rows {
			search.Principals = append(search.Principals, principals[row.Conversation.ID]...)
		}

		slices.Sort(search.Principals)
		search.Principals = slices.Compact(search.Principals)
	}

	agents, spaces, err := s.searchDefinitions(ctx, search.Terms)
	if err != nil {
		return nil, err
	}

	var listed []SidebarChat

	hits := make(map[string]bool)

	for _, row := range rows {
		chat, err := sidebarChat(ctx, slack, rooms, row)
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

	search.Hits = passingHits(found, hits)
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
				if !strings.Contains(strings.ToLower(row.Name+" "+chat.Room+" "+preview+" "+row.Conversation.Agent+" "+label+" "+row.Server+" "+row.Directory), search.Needle) {
					continue
				}

				match.Field, match.Text = fieldPreview, cmp.Or(preview, label)
			}

			search.Matches = append(search.Matches, match)
		}
	}

	search.sortMatches()

	return search, nil
}

func passingHits(found []MessageSearchHit, hits map[string]bool) []MessageSearchHit {
	kept := slices.DeleteFunc(found, func(hit MessageSearchHit) bool {
		if _, passed := hits[hit.ConversationID]; !passed {
			return true
		}

		hits[hit.ConversationID] = true

		return false
	})
	if len(kept) != len(found) {
		kept = append([]MessageSearchHit(nil), kept...)
	}

	return kept
}

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

	agents, err = LoadRuntimeAgents(s.definitions, s.definitions.Clone().RuntimeDirName())
	if err != nil {
		return agents, spaces, fmt.Errorf("load agents for model search: %w", err)
	}

	return agents, spaces, nil
}

func (search *SessionSearch) sortMatches() {
	if len(search.Terms) == 0 {
		return
	}

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

		var at, bt time.Time
		if a.Chat.Session.Summary != nil {
			at = a.Chat.Session.Summary.LastUpdated
		}

		if b.Chat.Session.Summary != nil {
			bt = b.Chat.Session.Summary.LastUpdated
		}

		order := at.Compare(bt)

		if at.IsZero() || bt.IsZero() || sort == "newest" {
			order = -order
		}

		return cmp.Or(order, strings.Compare(a.Chat.Session.Conversation.ID, b.Chat.Session.Conversation.ID))
	})
}
