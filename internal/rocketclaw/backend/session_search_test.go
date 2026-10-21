package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	harness "github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/require"
)

const (
	searchChatA = "slack-thread:C1:1.1"
	searchChatB = "slack-thread:C9:2.2"
	searchChatW = "web-session:quarter"
	searchChatD = "web:cron:cron/daily.md:20260102T030405.000000000Z:run"
	searchChatE = "web:one-off-cron:cron/Weekly report.md:20260103T030405.000000000Z:run"
	searchChatH = "human"
	searchChatX = "mcp-dest"
)

type searchTestMatch struct {
	ID    string
	Field SessionField
	Text  string
}

// newSessionSearchFixture records the chats of the session-list.test.ts cases,
// a cron producer, an External MCP private chat and a Delegation History, which
// Web never shows.
func newSessionSearchFixture(t *testing.T) (*SessionService, *slackFrontendMock) {
	t.Helper()

	s := newTestSessionService(t)
	ctx := t.Context()

	for _, chat := range []struct {
		id, agent, user, preview string
		day                      int
	}{
		{searchChatA, "main", "first question", "Outage", 5},
		{searchChatB, "two words", "ping <@U7> on support billing", "plain", 4},
		{searchChatW, "other", "billing question", "notes", 3},
		{searchChatD, "main", "daily", "digest", 2},
		{searchChatE, "main", "weekly", "summary", 1},
		{searchChatH, "cron", "human", "cron daily report", 6},
		{searchChatX, "main", "ticket", "handled", 7},
		{"cron:cron/daily.md:20260102T030405.000000000Z:run", "main", "billing producer", "done", 8},
		{"external_mcp:private", "producer", "billing secret", "done", 9},
	} {
		require.NoError(t, s.UpsertThread(chat.id, ThreadState{Agent: chat.agent}))

		entry := testSessionEntry(chat.user, chat.preview)
		entry.Timestamp = time.Date(2026, 1, chat.day, 0, 0, 0, 0, time.UTC)
		_, err := s.AppendEntryID(ctx, chat.id, entry)
		require.NoError(t, err)
	}

	// A Delegation History has entries but no managed_conversations row; Web never shows it.
	_, err := s.AppendEntryID(ctx, searchChatD+"/call-1", testSessionEntry("billing delegated", "support child"))
	require.NoError(t, err)

	require.NoError(t, s.UpsertExternalMCPSession("ext-1", &ExternalMCPSessionState{Agent: "producer", PrivateConversationID: "external_mcp:private", ManagedConversationID: searchChatX, OriginPairs: map[string]string{"Ticket": "T-1"}}))

	_, err = s.UpdateConversationDetails(ctx, searchChatA, new(true), nil)
	require.NoError(t, err)
	_, err = s.UpdateConversationDetails(ctx, searchChatW, nil, new("support"))
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `UPDATE managed_conversations SET forked_from = 'original' WHERE conversation_id = $1`, searchChatA)
	require.NoError(t, err)

	for _, tag := range []string{"customer", "Needs review", `say "hello"`, "is:pinned", "*"} {
		_, err := s.toggleSessionTag(ctx, searchChatA, tag, []string{tag})
		require.NoError(t, err)
	}

	return s, &slackFrontendMock{
		SidebarChannelAgentChoicesFunc: func(_ context.Context, channel string) (string, []string, error) {
			if channel == "C1" {
				return "support", []string{"main"}, nil
			}

			return channel, nil, nil // An unresolved room shows its channel ID.
		},
		SlackTagsMatchingFunc: func(_ context.Context, needle string) []string {
			if needle == "ada" {
				return []string{"U7"}
			}

			return nil
		},
	}
}

func searchTestSessions(t *testing.T, s *SessionService, slack slackLookup, query string, scope SearchScope) (*SessionSearch, []searchTestMatch) {
	t.Helper()

	search, err := s.SearchSessions(t.Context(), query, scope, slack)
	require.NoError(t, err)

	matches := make([]searchTestMatch, 0, len(search.Matches))

	for i := range search.Matches {
		match := &search.Matches[i]
		matches = append(matches, searchTestMatch{match.Chat.Session.Conversation.ID, match.Field, match.Text})
	}

	return search, matches
}

func TestSearchSessionsPrincipalCriteria(t *testing.T) {
	t.Parallel()
	s, slack := newSessionSearchFixture(t)
	ctx := t.Context()
	require.NoError(t, s.UpsertThread("written-web", ThreadState{Agent: "main"}))
	require.NoError(t, s.UpsertThread(searchChatW, ThreadState{Agent: "other", CreatedBy: ThreadCreator(`Writer "One"`)}))
	require.NoError(t, s.startSubagent(ctx, "written-web/call-1", "written-web", "main", true), "an unindexed subagent session leaves search complete")

	var authored int64

	for _, row := range []struct{ id, role, principal, text string }{
		{searchChatA, "user", `Writer "One"`, "needle"},
		{searchChatB, "user", `Writer "One"`, "other prompt"},
		{searchChatB, "user", "Writer Attachment", ""},
		{"written-web", "user", "Writer Cut", "hidden by cutoff"},
		{searchChatW, "assistant", `Writer "One"`, "needle"},
		{searchChatH, "user", "", `[Web principal="Writer \"One\""] needle`},
		{"external_mcp:private", "user", `Writer "One"`, "needle"},
		{searchChatD + "/call-1", "user", `Writer "One"`, "needle"},
		{"cron:daily", "user", `Writer "One"`, "needle"},
	} {
		entry := testReplayEntry(fmt.Sprintf(`{"type":"message","role":%q,"prompt_header":%s,"content":%q}`, row.role, jsonString(fmt.Sprintf("[Web principal=%q]", row.principal)), row.text))
		if row.id == searchChatA {
			entry.ReplayInput = append(entry.ReplayInput, json.RawMessage(`{"type":"message","role":"user","prompt_header":"[Web principal=\"Writer Two\"]","content":"follow-up"}`))
		}

		id, err := s.AppendEntryID(ctx, row.id, entry)
		require.NoError(t, err)

		if row.id == "written-web" {
			authored = id
		}
	}

	for _, tc := range []struct {
		query string
		ids   []string
	}{
		{`principal:"Writer \"One\""`, []string{searchChatA, searchChatB}},
		{`PRINCIPAL:"Writer \"One\"" tag:customer room:support needle`, []string{searchChatA}},
		{`principal:"Writer \"One\"" principal:"Writer Two"`, []string{searchChatA}},
		{`principal:"writer \"One\""`, nil},
		{`principal:unknown`, nil},
		{`principal:"Writer Cut"`, []string{"written-web"}},
		{`principal:"Writer Attachment"`, []string{searchChatB}},
		{`principal:""`, nil},
		{`principal:"bad\q"`, nil},
	} {
		search, matches := searchTestSessions(t, s, slack, tc.query, SearchMessages)

		ids := make([]string, 0, len(matches))
		for _, match := range matches {
			ids = append(ids, match.ID)
		}

		require.True(t, slices.Equal(tc.ids, ids), "%s: chats %q", tc.query, ids)
		require.True(t, search.IndexComplete)
	}

	_, _, err := stageRevertDB(ctx, s.db, "written-web", fmt.Sprintf("%d:0", authored))
	require.NoError(t, err)
	filtered, matches := searchTestSessions(t, s, slack, `principal:"Writer Cut"`, SearchFields)
	require.Empty(t, matches)
	require.Equal(t, []string{`Writer "One"`, "Writer Attachment", "Writer Two"}, filtered.Principals)

	_, criteria, err := s.searchSessionsWithin(ctx, `principal:"Writer \"One\"" principal:"Writer Two"`, &sessionListParams{}, slack)
	require.NoError(t, err)
	require.Len(t, criteria, 1)
	require.Equal(t, searchChatA, criteria[0].Chat.Session.Conversation.ID)
	unindexMessageSearch(t, s)
	search, matches := searchTestSessions(t, s, slack, `principal:"Writer \"One\""`, SearchFields)
	require.Empty(t, matches)
	require.False(t, search.IndexComplete)
	require.NoError(t, s.backfillMessageSearch(ctx))
	search, matches = searchTestSessions(t, s, slack, `principal:"Writer \"One\""`, SearchFields)
	require.Len(t, matches, 2)
	require.True(t, search.IndexComplete)
}

// Ported from session-list.test.ts; its unfinished words are read as free text.
func TestSearchSessionsReadsTermsAndFilters(t *testing.T) {
	t.Parallel()

	s, slack := newSessionSearchFixture(t)
	all := []string{searchChatA, searchChatX, searchChatH, searchChatB, searchChatW, searchChatD, searchChatE}

	for _, tc := range []struct {
		query string
		terms []string
		text  string
		ids   []string
	}{
		{"", nil, "", all},
		{"tag:customer", []string{"tag=customer"}, "", []string{searchChatA}},
		{"tag:customer is:pinned tag:customer is:forked outage", []string{"tag=customer", "is=pinned", "tag=customer", "is=forked"}, "outage", []string{searchChatA}},
		{`tag:"Needs review" tag:"say \"hello\""`, []string{"tag=Needs review", `tag=say "hello"`}, "", []string{searchChatA}},
		{`tag:"is:pinned"`, []string{"tag=is:pinned"}, "", []string{searchChatA}},
		{"tag:*", []string{"tag=*"}, "", []string{searchChatA}},
		{"tag:Customer", []string{"tag=Customer"}, "", nil},
		{"tag:unknown", []string{"tag=unknown"}, "", nil},
		{"tag:customer tag:internal", []string{"tag=customer", "tag=internal"}, "", nil},
		{"prefix-tag:customer", nil, "prefix-tag:customer", nil},
		{"tag:", nil, "tag:", nil},
		{`tag:"Needs review`, nil, `tag:"Needs review`, nil},
		{`tag:"bad\q"`, nil, `tag:"bad\q"`, nil},
		{`tag:""`, nil, `tag:""`, nil},
		{`tag:"customer"suffix`, nil, `tag:"customer"suffix`, nil},
		{`tag:"Needs is:pinned review" agent:ma`, []string{"tag=Needs is:pinned review", "agent=ma"}, "", nil},
		{"is:pinned tag:x outage", []string{"is=pinned", "tag=x"}, "outage", nil},
		{"is:pinned tag:customer outage", []string{"is=pinned", "tag=customer"}, "outage", []string{searchChatA}},
		{"IS:PINNED SORT:OLDEST", []string{"is=pinned", "sort=oldest"}, "", []string{searchChatA}},
		{"is:forked", []string{"is=forked"}, "", []string{searchChatA}},
		{"sort:oldest sort:newest", []string{"sort=oldest", "sort=newest"}, "", []string{searchChatX, searchChatH, searchChatA, searchChatB, searchChatW, searchChatD, searchChatE}},
		{"is:", nil, "is:", nil},
		{"is:pin", nil, "is:pin", nil},
		{"sort:", nil, "sort:", nil},
		{"sort:ne", nil, "sort:ne", nil},
		{"outage is:pin", nil, "outage is:pin", nil},
		{"is:foo", nil, "is:foo", nil},
		{"sort:bogus", nil, "sort:bogus", nil},
		{"is:cron", []string{"is=cron"}, "", []string{searchChatD, searchChatE}},
		{"IS:CRON", []string{"is=cron"}, "", []string{searchChatD, searchChatE}},
		{"is:cr", nil, "is:cr", nil},
		{"is:cron is:pinned", []string{"is=cron", "is=pinned"}, "", nil},
		{"cron:daily", []string{"cron=daily"}, "", []string{searchChatD}},
		{"is:cron CRON:daily", []string{"is=cron", "cron=daily"}, "", []string{searchChatD}},
		{`cron:"Weekly report"`, []string{"cron=Weekly report"}, "", []string{searchChatE}},
		{"cron:dai", []string{"cron=dai"}, "", nil},
		{"cron:Daily", []string{"cron=Daily"}, "", nil},
		{"cron:unknown", []string{"cron=unknown"}, "", nil},
		{"cron:daily sort:newest cron:daily", []string{"cron=daily", "sort=newest", "cron=daily"}, "", []string{searchChatD}},
		{`cron:daily cron:"Weekly report"`, []string{"cron=daily", "cron=Weekly report"}, "", nil},
		{"cron:daily report", []string{"cron=daily"}, "report", nil},
		{"cron:", nil, "cron:", []string{searchChatD, searchChatE}},
		{`cron:""`, nil, `cron:""`, nil},
		{`cron:"bad\q"`, nil, `cron:"bad\q"`, nil},
		{"agent:main", []string{"agent=main"}, "", []string{searchChatA, searchChatX, searchChatD, searchChatE}},
		{"AGENT:other", []string{"agent=other"}, "", []string{searchChatW}},
		{"agent:main agent:other", []string{"agent=main", "agent=other"}, "", nil},
		{`agent:"two words"`, []string{"agent=two words"}, "", []string{searchChatB}},
		{"room:support", []string{"room=support"}, "", []string{searchChatA}},
		{"room:C9", []string{"room=C9"}, "", []string{searchChatB}},
		{"room:C1", []string{"room=C1"}, "", nil},
		{"room:support room:C9", []string{"room=support", "room=C9"}, "", []string{searchChatA, searchChatB}},
		{`room:"support" ROOM:"C9"`, []string{"room=support", "room=C9"}, "", []string{searchChatA, searchChatB}},
		{"room:unknown room:C9", []string{"room=unknown", "room=C9"}, "", []string{searchChatB}},
		{"room:Support room:unknown", []string{"room=Support", "room=unknown"}, "", nil},
		{"room:support tag:customer room:C9 agent:main is:forked", []string{"room=support", "tag=customer", "room=C9", "agent=main", "is=forked"}, "", []string{searchChatA}},
		{"room:support room:C9 agent:main agent:other", []string{"room=support", "room=C9", "agent=main", "agent=other"}, "", nil},
		{"room:support room:C9 sort:oldest", []string{"room=support", "room=C9", "sort=oldest"}, "", []string{searchChatB, searchChatA}},
	} {
		calls := len(slack.SidebarChannelAgentChoicesCalls())
		search, matches := searchTestSessions(t, s, slack, tc.query, SearchFields)

		terms, ids := make([]string, 0, len(search.Terms)), make([]string, 0, len(matches))
		for _, term := range search.Terms {
			terms = append(terms, string(term.Key)+"="+term.Value)
		}

		for _, match := range matches {
			ids = append(ids, match.ID)
		}

		require.True(t, slices.Equal(tc.terms, terms), "%s: terms %q", tc.query, terms)
		require.Equal(t, tc.text, search.Text, tc.query)
		require.Equal(t, strings.ToLower(tc.text), search.Needle, tc.query)
		require.True(t, slices.Equal(tc.ids, ids), "%s: chats %q", tc.query, ids)
		require.Len(t, slack.SidebarChannelAgentChoicesCalls(), calls+2, "one room lookup per channel")
		require.True(t, search.SummariesComplete)
		require.True(t, search.IndexComplete)
	}
}

func TestSearchSessionsFreeTextFields(t *testing.T) {
	t.Parallel()

	s, slack := newSessionSearchFixture(t)

	for _, tc := range []struct {
		query string
		want  []searchTestMatch
	}{
		{"outage", []searchTestMatch{{searchChatA, fieldPreview, "Outage"}}},
		{"outage main", []searchTestMatch{{searchChatA, fieldPreview, "Outage"}}},
		{"support", []searchTestMatch{{searchChatA, fieldRoom, "support"}, {searchChatW, fieldName, "support"}}},
		{"two", []searchTestMatch{{searchChatB, fieldAgent, "two words"}}},
		{"QUARTER", []searchTestMatch{{searchChatW, fieldSession, "quarter"}}},
		{"slack c9", []searchTestMatch{{searchChatB, fieldSession, "slack C9"}}},
		{"t-1", []searchTestMatch{{searchChatX, fieldOrigin, "external mcp external conversation: ext-1 agent: producer ticket=t-1"}}},
		{"notes", []searchTestMatch{{searchChatW, fieldPreview, "notes"}}},
		{"billing", nil},
		{"tag:customer", []searchTestMatch{{searchChatA, fieldPreview, "Outage"}}},
	} {
		search, matches := searchTestSessions(t, s, slack, tc.query, SearchFields)
		require.True(t, slices.Equal(tc.want, matches), "%s: %q", tc.query, matches)
		require.Empty(t, search.Hits, tc.query)
	}
}

func TestSearchSessionsFilters(t *testing.T) {
	t.Parallel()

	s, slack := newSessionSearchFixture(t)

	// Free text is the caller's to match: it changes nothing here, and no message is read.
	_, matches := searchTestSessions(t, s, slack, "tag:customer", SearchFilters)
	require.Equal(t, []searchTestMatch{{searchChatA, "", ""}}, matches)

	for _, query := range []string{"tag:customer", "agent:other", "is:cron"} {
		_, bare := searchTestSessions(t, s, slack, query, SearchFilters)
		search, typed := searchTestSessions(t, s, slack, query+" billing nothing-holds-this", SearchFilters)
		require.NotEmpty(t, bare, query)
		require.Equal(t, bare, typed, query)
		require.Empty(t, search.Hits, query)
	}
}

func TestSearchSessionsMessageHits(t *testing.T) {
	t.Parallel()

	s, slack := newSessionSearchFixture(t)
	hitB := "ping <@U7> on support billing"

	// The Delegation History's messages also hold billing and support; it neither matches nor hits.
	for _, tc := range []struct {
		query    string
		hits     []string
		mentions []string
		want     []searchTestMatch
	}{
		{"billing", []string{hitB, "billing question"}, nil, []searchTestMatch{{searchChatB, "", ""}, {searchChatW, "", ""}}},
		{"support", []string{hitB}, nil, []searchTestMatch{{searchChatB, "", ""}, {searchChatA, fieldRoom, "support"}, {searchChatW, fieldName, "support"}}},
		{"support sort:oldest", []string{hitB}, nil, []searchTestMatch{{searchChatW, fieldName, "support"}, {searchChatB, "", ""}, {searchChatA, fieldRoom, "support"}}},
		{"tag:customer billing", nil, nil, nil},
		{"room:support room:C9 billing", []string{hitB}, nil, []searchTestMatch{{searchChatB, "", ""}}},
		{"room:support room:C9 tag:customer billing", nil, nil, nil},
		{"ada", []string{hitB}, []string{"U7"}, []searchTestMatch{{searchChatB, "", ""}}},
		{"ada agent:main", nil, nil, nil},
	} {
		search, matches := searchTestSessions(t, s, slack, tc.query, SearchMessages)

		hits := make([]string, 0, len(search.Hits))
		for _, hit := range search.Hits {
			hits = append(hits, hit.Text)
		}

		require.True(t, slices.Equal(tc.hits, hits), "%s: hits %q", tc.query, hits)
		require.Equal(t, tc.mentions, search.MentionIDs, tc.query)
		require.True(t, slices.Equal(tc.want, matches), "%s: %q", tc.query, matches)
		require.True(t, search.IndexComplete, tc.query)
	}

	require.Equal(t, "ada", slack.SlackTagsMatchingCalls()[len(slack.SlackTagsMatchingCalls())-1].S)

	unindexMessageSearch(t, s)

	search, _ := searchTestSessions(t, s, slack, "billing", SearchMessages)
	require.False(t, search.IndexComplete)

	search, _ = searchTestSessions(t, s, slack, "billing", SearchFields)
	require.True(t, search.IndexComplete, "no message search ran")
	require.True(t, search.SummariesComplete)

	require.NoError(t, s.UpsertThread("missing", ThreadState{Agent: "main"}))
	insertSessionEntryWithoutSummary(t, s, "missing", testSessionEntry("old", "answer"))
	_, err := s.db.ExecContext(t.Context(), `DELETE FROM session_summaries WHERE conversation_id = 'missing'`)
	require.NoError(t, err)

	search, _ = searchTestSessions(t, s, slack, "tag:customer", SearchFields)
	require.False(t, search.SummariesComplete, "a visible chat without a summary counts even when filtered out")
}

func TestSearchSessionsSortOrder(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()

	for _, id := range []string{"c", "e", "d", "b", "a"} {
		require.NoError(t, s.UpsertThread(id, ThreadState{Agent: "main"}))
	}

	insertSessionEntryWithoutSummary(t, s, "c", testSessionEntry("old", "answer"))
	_, err := s.db.ExecContext(ctx, `DELETE FROM session_summaries WHERE conversation_id = 'c'`)
	require.NoError(t, err)
	_, err = s.AppendEntryID(ctx, "d", &harness.SessionEntry{})
	require.NoError(t, err)

	for id, stamp := range map[string]time.Time{"e": time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), "b": time.Date(2026, 1, 1, 0, 0, 0, 500_000_000, time.UTC), "a": time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)} {
		_, err = s.AppendEntryID(ctx, id, testSessionEntryAt(stamp, "question"))
		require.NoError(t, err)
	}

	_, err = s.UpdateConversationDetails(ctx, "b", new(true), nil)
	require.NoError(t, err)

	for query, want := range map[string][]string{"sort:oldest": {"b", "a", "e", "c", "d"}, "sort:newest": {"a", "e", "b", "c", "d"}, "": {"b", "a", "e", "c", "d"}} {
		search, matches := searchTestSessions(t, s, noSlackLookups{}, query, SearchFields)

		ids := make([]string, 0, len(matches))
		for _, match := range matches {
			ids = append(ids, match.ID)
		}

		require.Equal(t, want, ids, query)
		require.False(t, search.SummariesComplete)
		require.Contains(t, matches, searchTestMatch{"c", fieldPreview, "c"}, "an empty preview shows the session label")
	}
}

func TestSearchSessionsTermOffsets(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)

	for _, tc := range []struct {
		query, text string
		terms       []SearchTerm
	}{
		{"😀 tag:\"Needs review\" x is:pinned", "😀 x", []SearchTerm{
			{Key: keyTag, Text: `tag:"Needs review"`, Value: "Needs review", Start: 3, End: 21},
			{Key: keyIs, Text: "is:pinned", Value: "pinned", Start: 24, End: 33},
		}},
		{"a\u00a0TAG:x  b", "a  b", []SearchTerm{{Key: keyTag, Text: "TAG:x", Value: "x", Start: 2, End: 7}}},
		{`😀 room:"Blue garden" ROOM:amber x`, "😀 x", []SearchTerm{
			{Key: keyRoom, Text: `room:"Blue garden"`, Value: "Blue garden", Start: 3, End: 21},
			{Key: keyRoom, Text: "ROOM:amber", Value: "amber", Start: 22, End: 32},
		}},
	} {
		search, _ := searchTestSessions(t, s, noSlackLookups{}, tc.query, SearchFields)
		require.Equal(t, tc.terms, search.Terms, tc.query)
		require.Equal(t, tc.text, search.Text, tc.query)
	}
}

// A term's offsets do not re-encode the query before it, so a 1 MiB query parses in linear time.
func TestSearchSessionsLongQueryParse(t *testing.T) {
	t.Parallel()

	const pairs = 1 << 17

	start := time.Now()
	terms, text := parseSessionQuery(strings.Repeat("tag:a x ", pairs))
	elapsed := time.Since(start)

	require.Len(t, terms, pairs)
	require.Equal(t, SearchTerm{Key: keyTag, Text: "tag:a", Value: "a", Start: 8 * (pairs - 1), End: 8*(pairs-1) + 5}, terms[pairs-1])
	require.Len(t, text, 2*pairs-1)
	require.Less(t, elapsed, 2*time.Second)
}

func TestSearchSessionsOriginFailure(t *testing.T) {
	t.Parallel()

	s, slack := newSessionSearchFixture(t)
	ctx := t.Context()
	// A creating entry whose source entry ID is not a number fails the origin
	// query after the sidebar rows have been read.
	_, err := s.db.ExecContext(ctx, `UPDATE session_entries SET entry_json = jsonb_set(entry_json::jsonb, '{sync_source_entry_id}', '"x"')::json WHERE conversation_id = $1`, searchChatW)
	require.NoError(t, err)

	_, err = s.SearchSessions(ctx, "billing", SearchFields, slack)
	require.ErrorContains(t, err, "bigint")

	_, err = listSessionsTool(s, &threadBridgeManager{slackLookups: slack}).Call(ctx, json.RawMessage(`{"criteria":"billing","since":"1970-01-01T00:00:00Z","until":"","limit":10,"include_message_preview":false}`), nil)
	require.ErrorContains(t, err, "bigint")
}
