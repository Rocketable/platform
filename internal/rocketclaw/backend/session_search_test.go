package backend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
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
func newSessionSearchFixture(t *testing.T) (*SessionService, *slackLookupMock) {
	t.Helper()

	s := newTestSessionService(t)
	ctx := t.Context()

	daily, weekly := strings.TrimPrefix(searchChatD, "web:"), strings.TrimPrefix(searchChatE, "web:")
	for id, facts := range map[string]conversationFacts{
		searchChatA: {kind: KindSlackThread, slackChannel: "C1", slackThread: "1.1"}, searchChatB: {kind: KindSlackThread, slackChannel: "C9", slackThread: "2.2"},
		searchChatW: {kind: KindWebChat},
		searchChatD: {kind: KindWebChat, cronRun: daily}, searchChatE: {kind: KindWebChat, cronRun: weekly}, "external_mcp:private": {kind: KindExternalMCPRun},
	} {
		require.NoError(t, s.recordConversation(ctx, protocol.Conversation{ID: id, Space: map[string]string{searchChatA: "Blue garden", searchChatW: "Blue garden", searchChatB: "Amber"}[id]}, facts))
	}

	for id, run := range map[string]*protocol.CronRun{
		daily:  {Path: "cron/daily.md", At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		weekly: {Path: "cron/Weekly report.md", At: time.Date(2026, 1, 3, 3, 4, 5, 0, time.UTC), OneOff: true},
	} {
		require.NoError(t, s.recordConversation(ctx, protocol.Conversation{ID: id, Run: run}, conversationFacts{}))
	}

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

	// A Delegation History has entries but no conversations row; Web never shows it.
	_, err := s.AppendEntryID(ctx, searchChatD+"/call-1", testSessionEntry("billing delegated", "support child"))
	require.NoError(t, err)

	require.NoError(t, s.UpsertExternalMCPSession("ext-1", &ExternalMCPSessionState{Agent: "producer", PrivateConversationID: "external_mcp:private", ManagedConversationID: searchChatX, OriginPairs: map[string]string{"Ticket": "T-1"}}))

	_, err = s.UpdateConversationDetails(ctx, searchChatA, new(true), nil)
	require.NoError(t, err)
	_, err = s.UpdateConversationDetails(ctx, searchChatW, nil, new("support"))
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `UPDATE conversations SET forked_from = 'original' WHERE conversation_id = $1`, searchChatA)
	require.NoError(t, err)

	for _, tag := range []string{"customer", "Needs review", `say "hello"`, "is:pinned", "*"} {
		_, err := s.toggleSessionTag(ctx, searchChatA, tag, []string{tag})
		require.NoError(t, err)
	}

	return s, &slackLookupMock{
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

	search, err := s.SearchSessions(t.Context(), "", query, scope, slack)
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
	require.NoError(t, s.startSubagent(ctx, "written-web", harness.ChildKey{Path: "/call-1", Turn: "turn-1", Call: "call-1"}, "main", true), "an unindexed subagent session leaves search complete")

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
		_, matches := searchTestSessions(t, s, slack, tc.query, SearchMessages)

		ids := make([]string, 0, len(matches))
		for _, match := range matches {
			ids = append(ids, match.ID)
		}

		require.True(t, slices.Equal(tc.ids, ids), "%s: chats %q", tc.query, ids)
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
}

// Ported from session-list.test.ts; its unfinished words are read as free text.
func TestSearchSessionsReadsTermsAndFilters(t *testing.T) {
	t.Parallel()

	s, slack := newSessionSearchFixture(t)
	all := []string{searchChatA, searchChatX, searchChatH, searchChatB, searchChatW, searchChatD, searchChatE}

	// Bind the hidden runs as production does: the External MCP run reports to its chat and the
	// daily chat continues its run, which another agent ran.
	daily := strings.TrimPrefix(searchChatD, "web:")
	_, err := s.db.ExecContext(t.Context(), `UPDATE conversations SET reports_to = $1 WHERE conversation_id = 'external_mcp:private'`, searchChatX)
	require.NoError(t, err)
	_, err = s.db.ExecContext(t.Context(), `UPDATE conversations SET continues = $1 WHERE conversation_id = $2`, daily, searchChatD)
	require.NoError(t, err)
	_, err = s.db.ExecContext(t.Context(), `UPDATE conversations SET agent = 'scribe' WHERE conversation_id = $1`, daily)
	require.NoError(t, err)

	for _, tc := range []struct {
		query string
		terms []string
		text  string
		ids   []string
	}{
		{"", nil, "", all},
		{`space:"Blue garden"`, []string{"space=Blue garden"}, "", []string{searchChatA, searchChatW}},
		{`SPACE:"bLuE GaRdEn" space:"Blue garden"`, []string{"space=bLuE GaRdEn", "space=Blue garden"}, "", []string{searchChatA, searchChatW}},
		{`space:"Blue garden" agent:main room:C9 room:support`, []string{"space=Blue garden", "agent=main", "room=C9", "room=support"}, "", []string{searchChatA}},
		{`space:"Blue garden" space:Amber`, []string{"space=Blue garden", "space=Amber"}, "", nil},
		{"space:missing", []string{"space=missing"}, "", nil},
		{"space:", nil, "space:", nil},
		{`space:""`, nil, `space:""`, nil},
		{`space:"bad\q"`, nil, `space:"bad\q"`, nil},
		{`space:"Blue garden`, nil, `space:"Blue garden`, nil},
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
		{"agent:main agent:other", []string{"agent=main", "agent=other"}, "", []string{searchChatA, searchChatX, searchChatW, searchChatD, searchChatE}},
		{"agent:unknown agent:other", []string{"agent=unknown", "agent=other"}, "", []string{searchChatW}},
		{`agent:other AGENT:"two words" agent:other`, []string{"agent=other", "agent=two words", "agent=other"}, "", []string{searchChatB, searchChatW}},
		{`agent:"two words"`, []string{"agent=two words"}, "", []string{searchChatB}},
		{"agent:producer", []string{"agent=producer"}, "", []string{searchChatX}},
		{"agent:scribe agent:other", []string{"agent=scribe", "agent=other"}, "", []string{searchChatW, searchChatD}},
		{"agent:Producer", []string{"agent=Producer"}, "", nil},
		{"room:support", []string{"room=support"}, "", []string{searchChatA}},
		{"room:C9", []string{"room=C9"}, "", []string{searchChatB}},
		{"room:C1", []string{"room=C1"}, "", nil},
		{"room:support room:C9", []string{"room=support", "room=C9"}, "", []string{searchChatA, searchChatB}},
		{`room:"support" ROOM:"C9"`, []string{"room=support", "room=C9"}, "", []string{searchChatA, searchChatB}},
		{"room:unknown room:C9", []string{"room=unknown", "room=C9"}, "", []string{searchChatB}},
		{"room:Support room:unknown", []string{"room=Support", "room=unknown"}, "", nil},
		{"room:support tag:customer room:C9 agent:main is:forked", []string{"room=support", "tag=customer", "room=C9", "agent=main", "is=forked"}, "", []string{searchChatA}},
		{"room:support room:C9 agent:main agent:other", []string{"room=support", "room=C9", "agent=main", "agent=other"}, "", []string{searchChatA}},
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

	for _, query := range []string{"tag:customer", "agent:other", "is:cron", `space:"Blue garden"`} {
		_, bare := searchTestSessions(t, s, slack, query, SearchFilters)
		require.NotEmpty(t, bare, query)

		for _, text := range []string{" billing nothing-holds-this", " nothing-holds-this"} {
			search, typed := searchTestSessions(t, s, slack, query+text, SearchFilters)
			require.Equal(t, bare, typed, query+text)
			require.Empty(t, search.Hits, query+text)
		}
	}
}

func TestSearchSessionsBirthSpace(t *testing.T) {
	t.Parallel()
	s, slack := newSessionSearchFixture(t)
	ctx := t.Context()
	require.NoError(t, s.definitions.Do(func(cfg *config.Config) error {
		cfg.OpenCode = map[string]config.OpenCodeConfig{"box": {}}
		return nil
	}))
	spaces, err := s.Spaces(ctx)
	require.NoError(t, err)

	blue := spaces[slices.IndexFunc(spaces, func(space Space) bool { return space.Name == "Blue garden" })]
	_, err = s.db.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, retention, server, remote_session, title, directory, last_updated, running, synced_at, space_id)
VALUES ('opencode:box:assigned', '', '', 'remote', FALSE, 'remote', 'box', 'assigned', 'remote needle', '/repo', now(), FALSE, now(), $1),
    ('opencode:box:unassigned', '', '', 'remote', FALSE, 'remote', 'box', 'unassigned', 'remote needle', '/repo', now(), FALSE, now(), NULL)`, blue.ID)
	require.NoError(t, err)
	saved, err := s.SaveTabState(ctx, "alice", &TabState{Space: "main", Tabs: []Tab{
		{Path: "/s/" + base64.RawURLEncoding.EncodeToString([]byte(searchChatW)), Space: "main"},
		{Path: "/s/" + base64.RawURLEncoding.EncodeToString([]byte(searchChatB)), Space: blue.ID},
	}}, 0)
	require.NoError(t, err)
	require.True(t, saved)

	search, err := s.SearchSessions(ctx, "alice", `space:"Blue garden"`, SearchFields, slack)
	require.NoError(t, err)
	require.Len(t, search.Matches, 3, "birth space includes its remote session, not an unassigned one or a tab moved into it")
	require.Equal(t, searchChatW, search.Matches[2].Chat.Session.Conversation.ID, "moving a tab out does not change its birth space")
	_, matches := searchTestSessions(t, s, slack, "remote needle", SearchFields)
	require.Len(t, matches, 2, "without a space filter both remote sessions match")
	require.NoError(t, s.RenameSpace(ctx, blue.ID, `Blüe "garden"\`))
	_, matches = searchTestSessions(t, s, slack, `space:"Blüe \"garden\"\\"`, SearchFields)
	require.Len(t, matches, 3)
	_, matches = searchTestSessions(t, s, slack, `space:"Blue garden"`, SearchFields)
	require.Empty(t, matches)
	require.NoError(t, s.DeleteSpace(ctx, blue.ID))
	_, matches = searchTestSessions(t, s, slack, `space:"Blüe \"garden\"\\"`, SearchFields)
	require.Empty(t, matches)
}

// Turn ends that mark sessions unread are written directly; alice's view of searchChatW clears its mark.
// The fixture opens no tabs, so every marked session's tab is closed.
func TestSearchSessionsUnread(t *testing.T) {
	t.Parallel()

	s, slack := newSessionSearchFixture(t)
	ctx := t.Context()
	_, err := s.db.ExecContext(ctx, `INSERT INTO web_tabs (username, path, conversation_id, unread_at) VALUES ('alice', $1, $1, now()), ('alice', $2, $2, now()), ('alice', $3, $3, now()), ('alice', $4, $4, now()), ('bob', $5, $5, now())`,
		searchChatA, searchChatH, searchChatD, searchChatW, searchChatB)
	require.NoError(t, err)
	require.NoError(t, s.ViewSession(ctx, "alice", searchChatW))

	for _, tc := range []struct {
		username, query string
		ids             []string
	}{
		{"alice", "is:unread", []string{searchChatA, searchChatH, searchChatD}},
		{"alice", "IS:UNREAD agent:main", []string{searchChatA, searchChatD}},
		{"alice", "is:unread support", []string{searchChatA}},
		{"bob", "is:unread", []string{searchChatB}},
	} {
		search, err := s.SearchSessions(ctx, tc.username, tc.query, SearchFields, slack)
		require.NoError(t, err)

		ids := make([]string, 0, len(search.Matches))
		for i := range search.Matches {
			ids = append(ids, search.Matches[i].Chat.Session.Conversation.ID)
		}

		require.Equal(t, tc.ids, ids, "%s %s", tc.username, tc.query)
	}

	search, criteria, err := s.searchSessionsWithin(ctx, "is:unread", &sessionListParams{}, slack)
	require.NoError(t, err)
	require.Empty(t, search.Text, "is:unread is a filter for the agent tool too")
	require.Empty(t, criteria, "the agent tool searches for no person")
}

// Work terms count a chat's hidden runs as its own: W asks a question, B's question is answered, the
// External MCP run reporting to X runs, A and W hold queued items, and H, named "external mcp notes",
// only a reminder.
func TestSearchSessionsWorkTerms(t *testing.T) {
	t.Parallel()

	s, slack := newSessionSearchFixture(t)
	ctx := t.Context()
	_, err := s.db.ExecContext(ctx, `UPDATE conversations SET reports_to = $1 WHERE conversation_id = 'external_mcp:private'`, searchChatX)
	require.NoError(t, err)
	_, err = s.UpdateConversationDetails(ctx, searchChatH, nil, new("external mcp notes"))
	require.NoError(t, err)

	for id, turn := range map[string]string{searchChatW: "turn-w", searchChatB: "turn-b", "external_mcp:private": "turn-x"} {
		seedActiveTurn(t, s, id, turn, nil)
	}

	for id, key := range map[string]string{searchChatW: "turn-w/call/q" + webQuestionStep, searchChatB: "turn-b/call/q" + webQuestionStep, "external_mcp:private": "turn-x/call/q" + webQuestionStep} {
		require.NoError(t, s.SaveTurnStep(ctx, id, key, json.RawMessage(`{}`)))
	}

	require.NoError(t, s.SaveTurnStep(ctx, searchChatB, "turn-b/call/q/answer", json.RawMessage(`{}`)))

	for id, chat := range map[string]string{"queued-a": searchChatA, "queued-w": searchChatW} {
		require.NoError(t, s.PutThreadQueueItem(id, &protocol.ThreadQueueItem{ConversationID: chat, Message: "later", StashAt: time.Now()}))
	}

	require.NoError(t, s.PutScheduledMessage("reminder", &protocol.ScheduledMessageState{ConversationID: searchChatH, Agent: "cron", Message: "remind", DueAt: time.Now().Add(time.Hour)}))

	for query, want := range map[string][]string{
		"is:waiting":           {searchChatW},
		"is:queued":            {searchChatA, searchChatW},
		"is:queued agent:main": {searchChatA},
		"is:running":           {searchChatX, searchChatB, searchChatW},
		"is:external":          {searchChatX},
	} {
		_, matches := searchTestSessions(t, s, slack, query, SearchFields)

		ids := make([]string, 0, len(matches))
		for _, match := range matches {
			ids = append(ids, match.ID)
		}

		require.Equal(t, want, ids, query)
	}

	_, criteria, err := s.searchSessionsWithin(ctx, "is:waiting", &sessionListParams{}, slack)
	require.NoError(t, err)
	require.Len(t, criteria, 1, "the agent tool reads the same terms")
	require.Equal(t, searchChatW, criteria[0].Chat.Session.Conversation.ID)

	// The fixture's chats were last updated in January; work arriving now makes A, W and X recent,
	// while idle D and E stay out.
	since := strings.TrimSuffix(time.Now().Add(-24*time.Hour).UTC().Format(time.RFC3339Nano), "Z")
	for query, want := range map[string][]string{"is:queued": {searchChatA, searchChatW}, "agent:main": {searchChatX, searchChatA}} {
		_, criteria, err := s.searchSessionsWithin(ctx, query, &sessionListParams{Since: since}, slack)
		require.NoError(t, err)

		ids := make([]string, 0, len(criteria))
		for i := range criteria {
			ids = append(ids, criteria[i].Chat.Session.Conversation.ID)
		}

		require.Equal(t, want, ids, "%s since 24h ago", query)
	}
}

// An ASCII needle reads only the chats whose words may hold it, so each word a match can come from
// must keep its chat: a cron run's time and kind, a pair spanning its "=", and names whose İ
// and K (Kelvin sign) lowercase into ASCII.
func TestSearchSessionsReadsEveryMatchingWord(t *testing.T) {
	t.Parallel()

	s, slack := newSessionSearchFixture(t)

	for id, name := range map[string]string{searchChatH: "İSTANBUL ȒOAD", searchChatB: "\u212aELVIN"} {
		_, err := s.UpdateConversationDetails(t.Context(), id, nil, new(name))
		require.NoError(t, err)
	}

	for _, tc := range []struct {
		query string
		want  []searchTestMatch
	}{
		{"03:04:05z", []searchTestMatch{{searchChatD, fieldOrigin, ""}, {searchChatE, fieldOrigin, ""}}},
		{"scheduled", []searchTestMatch{{searchChatD, fieldOrigin, ""}}},
		{"ticket=t-1", []searchTestMatch{{searchChatX, fieldOrigin, ""}}},
		{"istanbul", []searchTestMatch{{searchChatH, fieldName, "İSTANBUL ȒOAD"}}},
		{"ȓoad", []searchTestMatch{{searchChatH, fieldName, "İSTANBUL ȒOAD"}}},
		{"kelvin", []searchTestMatch{{searchChatB, fieldName, "\u212aELVIN"}}},
	} {
		_, matches := searchTestSessions(t, s, slack, tc.query, SearchFields)
		for i := range matches {
			if matches[i].Field == fieldOrigin {
				matches[i].Text = ""
			}
		}

		require.Equal(t, tc.want, matches, tc.query)
	}
}

// searchCandidates reads every rune Go lowercases into a non-ASCII rune among its simple case folds.
func TestSimpleFoldsHoldWhatLowercasesIntoRune(t *testing.T) {
	t.Parallel()

	for r := range unicode.MaxRune + 1 {
		lower := unicode.ToLower(r)
		if lower < utf8.RuneSelf || lower == r {
			continue
		}

		fold := unicode.SimpleFold(lower)
		for fold != lower && fold != r {
			fold = unicode.SimpleFold(fold)
		}

		require.Equal(t, r, fold, "%U lowercases into %U", r, lower)
	}
}

// A narrow search reads each candidate and chat with hits once, or every chat when they are many.
func TestReadChats(t *testing.T) {
	t.Parallel()

	hits := []MessageSearchHit{{ConversationID: "b"}, {ConversationID: "b"}, {ConversationID: "c"}}
	require.Equal(t, []string{"a", "b", "c"}, readChats([]string{"b", "a"}, hits))
	require.Equal(t, []string{}, readChats([]string{}, nil))

	many := make([]string, 2001)
	for i := range many {
		many[i] = strconv.Itoa(i)
	}

	require.Nil(t, readChats(many, nil))
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
		{`space:"Blue garden" billing`, []string{"billing question"}, nil, []searchTestMatch{{searchChatW, "", ""}}},
		{`space:Amber billing`, []string{hitB}, nil, []searchTestMatch{{searchChatB, "", ""}}},
		{`agent:other agent:"two words" billing`, []string{hitB, "billing question"}, nil, []searchTestMatch{{searchChatB, "", ""}, {searchChatW, "", ""}}},
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
	}

	require.Equal(t, "ada", slack.SlackTagsMatchingCalls()[len(slack.SlackTagsMatchingCalls())-1].S)
}

func TestSearchSessionsSortOrder(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()

	for _, id := range []string{"c", "e", "d", "b", "a"} {
		require.NoError(t, s.UpsertThread(id, ThreadState{Agent: "main"}))
	}

	insertSessionEntryWithoutSummary(t, s, "c", testSessionEntry("old", "answer"))
	// A summary missing since before migration 052, which still stored that the chat has entries.
	_, err := s.db.ExecContext(ctx, `UPDATE conversations SET preview = NULL, last_updated = NULL, has_entries = TRUE WHERE conversation_id = 'c'`)
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
		_, matches := searchTestSessions(t, s, noSlackLookups{}, query, SearchFields)

		ids := make([]string, 0, len(matches))
		for _, match := range matches {
			ids = append(ids, match.ID)
		}

		require.Equal(t, want, ids, query)
		require.Contains(t, matches, searchTestMatch{"c", fieldPreview, "c"}, "an empty preview shows the session label")
	}
}

// A board shows a space's chats: those born there, those without a Birth Space whose agent's space has
// its name ignoring case, and those with neither, each once per Dashboard in its first matching lane,
// newest arrival first. Lane text matches fields, not message bodies.
func TestSessionBoard(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	writeAgent(t, s.definitions.Clone().Workspace, "ops", "---\nmodel: gpt-6.1-sol\nspace: OPS\n---\nHelp.")

	for _, name := range []string{"Ops", "Bulk"} {
		require.NoError(t, s.CreateSpace(ctx, name))
	}

	spaces, err := s.Spaces(ctx)
	require.NoError(t, err)

	ids := make(map[string]string)
	for _, space := range spaces {
		ids[space.Name] = space.ID
	}

	require.NoError(t, s.RegisterExternalMCPConversation("ticket", "ops", &ExternalMCPSessionState{Agent: "ops", PrivateConversationID: "mcp-run", ManagedConversationID: "mcp"}))

	for day, chat := range []struct{ id, agent, space string }{{"loose", "main", ""}, {"mcp", "ops", ""}, {"web-ops", "main", ids["Ops"]}, {"web-main", "main", ids["Main"]}} {
		require.NoError(t, s.UpsertThread(chat.id, ThreadState{Agent: chat.agent}))
		_, err := s.AppendEntryID(ctx, chat.id, testSessionEntryAt(time.Date(2026, 1, day+1, 0, 0, 0, 0, time.UTC), "secret body words"))
		require.NoError(t, err)
		_, err = s.db.ExecContext(ctx, `UPDATE conversations SET space_id = NULLIF($2, '') WHERE conversation_id = $1`, chat.id, chat.space)
		require.NoError(t, err)
	}

	_, err = s.UpdateConversationDetails(ctx, "web-ops", nil, new("Invoice run"))
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, preview, has_entries, last_updated, space_id)
SELECT 'bulk-' || lpad(n::text, 3, '0'), 'main', '', 'web_chat', TRUE, 'preview'::bytea, TRUE, now() - n * interval '1 second', $1 FROM generate_series(0, 249) n`, ids["Bulk"])
	require.NoError(t, err)

	bulk := make([]string, 200)
	for i := range bulk {
		bulk[i] = fmt.Sprintf("bulk-%03d", i)
	}

	// A card is the chat as the session list has it, even when a model: lane matched its agent's model.
	listed := make(map[string]SidebarChat)

	for chat, err := range s.SidebarChats(ctx, "", noSlackLookups{}, nil) {
		require.NoError(t, err)

		listed[chat.Session.Conversation.ID] = chat
	}

	type lane struct {
		IDs   []string
		Total int
	}

	for _, tc := range []struct {
		space      string
		dashboards [][]string
		want       [][]lane
	}{
		{ids["Ops"], [][]string{{"is:external", "INVOICE", ""}, {"", "is:external"}, {"secret"}, {"model:sol"}}, [][]lane{
			{{IDs: []string{"mcp"}, Total: 1}, {IDs: []string{"web-ops"}, Total: 1}, {IDs: []string{"loose"}, Total: 1}},
			{{IDs: []string{"web-ops", "mcp", "loose"}, Total: 3}, {}},
			{{}},
			{{IDs: []string{"mcp"}, Total: 1}},
		}},
		{ids["Main"], [][]string{{""}}, [][]lane{{{IDs: []string{"web-main", "loose"}, Total: 2}}}},
		{ids["Bulk"], [][]string{{"bulk"}}, [][]lane{{{IDs: bulk, Total: 250}}}},
		{"no-such-space", [][]string{{"", "is:external"}}, [][]lane{{{}, {}}}},
	} {
		board, err := s.Board(ctx, "", tc.space, tc.dashboards, noSlackLookups{})
		require.NoError(t, err)

		got := make([][]lane, len(board))
		for i, lanes := range board {
			got[i] = make([]lane, len(lanes))
			for j, found := range lanes {
				got[i][j].Total = found.Total
				for _, chat := range found.Chats {
					got[i][j].IDs = append(got[i][j].IDs, chat.Session.Conversation.ID)
					require.Equal(t, listed[chat.Session.Conversation.ID], chat)
				}
			}
		}

		require.Equal(t, tc.want, got, "%s %q", tc.space, tc.dashboards)
	}
}

// BenchmarkSessionBoard sorts 1,000 chats into the default lanes of a new Dashboard.
func BenchmarkSessionBoard(b *testing.B) {
	service := sidebarWorkFixture(b)
	require.NoError(b, os.MkdirAll(filepath.Join(service.definitions.Clone().Workspace, ".rocketclaw", "agents"), 0o755))

	dashboards := [][]string{{"is:waiting", "is:running", "is:queued", "is:external"}}

	for b.Loop() {
		board, err := service.Board(b.Context(), "", "main", dashboards, noSlackLookups{})
		require.NoError(b, err)
		require.Equal(b, 50, board[0][0].Total)
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
		{`😀 space:"Blue garden" x`, "😀 x", []SearchTerm{{Key: "space", Text: `space:"Blue garden"`, Value: "Blue garden", Start: 3, End: 22}}},
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

	if os.Getenv("ROCKETCLAW_TEST_LONG_QUERY_PARSE") != "1" {
		// Measure the parser, not race detector work on atomic coverage counters.
		binary := filepath.Join(t.TempDir(), "backend.test")
		build := exec.CommandContext(t.Context(), "go", "test", "-c", "-race=false", "-cover=false", "-o", binary, ".")
		output, err := build.CombinedOutput()
		require.NoError(t, err, "%s", output)

		cmd := exec.CommandContext(t.Context(), binary, "-test.run=^"+t.Name()+"$", "-test.cpu="+strconv.Itoa(runtime.GOMAXPROCS(0)))

		cmd.Env = append(os.Environ(), "ROCKETCLAW_TEST_LONG_QUERY_PARSE=1")
		output, err = cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
		// Count the isolated parser's CPU work, not time descheduled behind parallel tests.
		require.Less(t, cmd.ProcessState.UserTime()+cmd.ProcessState.SystemTime(), 2*time.Second)

		return
	}

	const pairs = 1 << 17

	terms, text := parseSessionQuery(strings.Repeat("tag:a x ", pairs))

	require.Len(t, terms, pairs)
	require.Equal(t, SearchTerm{Key: keyTag, Text: "tag:a", Value: "a", Start: 8 * (pairs - 1), End: 8*(pairs-1) + 5}, terms[pairs-1])
	require.Len(t, text, 2*pairs-1)
}
