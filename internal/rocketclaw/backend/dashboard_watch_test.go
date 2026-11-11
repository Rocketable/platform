package backend

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// boardChat lists the Web chat id, named id, arriving at the time given and tagged tags, or moves it there.
func boardChat(t *testing.T, s *SessionService, id string, at time.Time, tags ...string) {
	t.Helper()

	_, err := s.db.ExecContext(t.Context(), `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, preview, has_entries, last_updated, name, tags)
VALUES ($1, 'main', '', 'web_chat', TRUE, 'preview'::bytea, TRUE, $2, $1, to_jsonb($3::text[]))
ON CONFLICT (conversation_id) DO UPDATE SET last_updated = excluded.last_updated, tags = excluded.tags`, id, at, tags)
	require.NoError(t, err)
}

// watchPass runs one Dashboard watch pass over seen, then lists and reads every mark: its person, path, whether it is
// quiet and parked, and its note.
func watchPass(t *testing.T, s *SessionService, slack slackLookup, seen *dashboardsSeen) []string {
	t.Helper()

	var err error

	*seen, err = s.watchDashboards(t.Context(), slack, *seen)
	require.NoError(t, err)

	marks, err := queryStrings(t.Context(), s.db, `SELECT format('%s %s %s: %s', username, path, concat_ws(' ', CASE WHEN quiet THEN 'quiet' ELSE 'loud' END, CASE WHEN parked THEN 'parked' END), note)
FROM web_tab_attention WHERE unread_at IS NOT NULL`, "dashboard marks")
	require.NoError(t, err)

	_, err = s.db.ExecContext(t.Context(), `UPDATE web_tab_attention SET unread_at = NULL`)
	require.NoError(t, err)

	return marks
}

// A session entering a flagged lane of a Shared Dashboard marks everyone who can see its space, loud, and leaves a
// parked Dashboard parked with a quiet mark; not the person viewing it, nor one limited to another space.
func TestDashboardWatchMarksArrivals(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()

	const (
		carol = "carol@example.com" // limited to Ops
		dave  = "dave@example.com"  // views the Dashboard
		path  = "/search/team"
	)

	require.NoError(t, s.definitions.Do(func(cfg *config.Config) error {
		cfg.Permissions = map[string][]string{carol: {"Ops"}}
		return nil
	}))
	require.NoError(t, s.CreateLimitSpaces(ctx, []string{"Ops"}))

	spaces, err := s.Spaces(ctx)
	require.NoError(t, err)

	mainID := spaces[0].ID

	for _, person := range []string{alice, bob, carol, dave} {
		saved, err := s.SaveTabState(ctx, person, &TabState{Space: mainID}, 0)
		require.NoError(t, err)
		require.True(t, saved)
	}

	require.NoError(t, s.ShareTab(ctx, mainID, path, &SearchPage{Board: "kanban", Searches: []SavedSearch{
		{ID: "a", Name: "Needs you", Query: "tag:a", Notify: true}, {ID: "b", Query: "tag:b", Notify: true}, {ID: "c", Name: "Later", Query: "tag:c"},
	}}))
	require.NoError(t, s.ChangeAttention(ctx, bob, []string{path}, AttentionPark, nil))

	at := time.Now().Add(-time.Hour)
	for i, id := range []string{"one", "two", "three", "four", "five"} {
		boardChat(t, s, id, at.Add(time.Duration(i)*time.Minute))
	}

	var seen dashboardsSeen

	boardChat(t, s, "two", at.Add(time.Minute), "a")
	assert.Empty(t, watchPass(t, s, noSlackLookups{}, &seen), "the first pass only records")

	_, err = s.PollAttention(ctx, dave, path, false)
	require.NoError(t, err)
	boardChat(t, s, "one", at, "a")
	assert.ElementsMatch(t, []string{alice + " " + path + " loud: Needs you: one", bob + " " + path + " quiet parked: Needs you: one"}, watchPass(t, s, noSlackLookups{}, &seen))

	_, err = s.PollAttention(ctx, dave, "/cron", false)
	require.NoError(t, err)
	boardChat(t, s, "one", at, "b")

	want := func(note string) []string {
		return []string{alice + " " + path + " loud: " + note, bob + " " + path + " quiet parked: " + note, dave + " " + path + " loud: " + note}
	}

	assert.ElementsMatch(t, want("tag:b: one"), watchPass(t, s, noSlackLookups{}, &seen), "a session moving between flagged lanes marks for the lane it entered")

	boardChat(t, s, "two", at.Add(time.Minute), "c")
	assert.Empty(t, watchPass(t, s, noSlackLookups{}, &seen), "an unflagged lane marks nobody")

	for i, id := range []string{"three", "four", "five"} {
		boardChat(t, s, id, at.Add(time.Duration(i+2)*time.Minute), "a")
	}

	assert.ElementsMatch(t, want("Needs you: five and 2 more"), watchPass(t, s, noSlackLookups{}, &seen))
}

// Editing a lane's query, reordering lanes or turning a flag on only records; the next session to enter marks.
func TestDashboardWatchRecordsChangedLanes(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()

	spaces, err := s.Spaces(ctx)
	require.NoError(t, err)

	const path = "/search/mine"

	var (
		seen     dashboardsSeen
		revision int64
	)

	save := func(lanes []SavedSearch) {
		t.Helper()

		saved, err := s.SaveTabState(ctx, alice, &TabState{Tabs: []Tab{{Path: path, Space: spaces[0].ID, Search: &SearchPage{Board: "kanban", Searches: lanes}}}}, revision)
		require.NoError(t, err)
		require.True(t, saved)

		revision++
	}

	a, b := SavedSearch{ID: "a", Query: "tag:a", Notify: true}, SavedSearch{ID: "b", Query: "tag:b", Notify: true}

	for _, tc := range []struct {
		name          string
		before, after []SavedSearch
		enter         string
	}{
		{"query edited", []SavedSearch{a, b}, []SavedSearch{{ID: "a", Query: "tag:x", Notify: true}, b}, "x"},
		{"lanes reordered", []SavedSearch{a, b}, []SavedSearch{b, a}, "b"},
		{"flag turned on", []SavedSearch{{ID: "a", Query: "tag:a"}}, []SavedSearch{a}, "a"},
	} {
		save(tc.before)
		assert.Empty(t, watchPass(t, s, noSlackLookups{}, &seen), tc.name)

		save(tc.after)
		boardChat(t, s, tc.name+" 1", time.Now(), tc.enter)
		assert.Empty(t, watchPass(t, s, noSlackLookups{}, &seen), tc.name)

		boardChat(t, s, tc.name+" 2", time.Now(), tc.enter)
		assert.Equal(t, []string{alice + " " + path + " loud: tag:" + tc.enter + ": " + tc.name + " 2"}, watchPass(t, s, noSlackLookups{}, &seen), tc.name)
	}
}

// A lane listing 200 of its 250 sessions: one sliding into the 200 as another leaves is not new; one arriving is.
func TestDashboardWatchIgnoresSessionsSlidingIntoTheCap(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()

	spaces, err := s.Spaces(ctx)
	require.NoError(t, err)

	const path = "/search/bulk"

	saved, err := s.SaveTabState(ctx, alice, &TabState{Tabs: []Tab{{Path: path, Space: spaces[0].ID, Search: &SearchPage{Board: "kanban", Searches: []SavedSearch{{ID: "bulk", Query: "tag:bulk", Notify: true}}}}}}, 0)
	require.NoError(t, err)
	require.True(t, saved)

	_, err = s.db.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, preview, has_entries, last_updated, tags)
SELECT 'bulk-' || lpad(n::text, 3, '0'), 'main', '', 'web_chat', TRUE, 'preview'::bytea, TRUE, now() - interval '1 minute' - n * interval '1 second', '["bulk"]' FROM generate_series(0, 249) n`)
	require.NoError(t, err)

	var seen dashboardsSeen

	assert.Empty(t, watchPass(t, s, noSlackLookups{}, &seen))

	_, err = s.db.ExecContext(ctx, `UPDATE conversations SET tags = '[]' WHERE conversation_id = 'bulk-000'`)
	require.NoError(t, err)
	assert.Empty(t, watchPass(t, s, noSlackLookups{}, &seen), "bulk-200 slid into the listed 200")

	boardChat(t, s, "fresh", time.Now(), "bulk")
	assert.Equal(t, []string{alice + " " + path + " loud: tag:bulk: fresh"}, watchPass(t, s, noSlackLookups{}, &seen))
}

// A pass reads the session list only with a flagged lane, and while it is unchanged sorts only the Dashboards it has
// not recorded. A pass whose sort fails keeps what the Dashboard held, so the session that entered marks on the next.
func TestDashboardWatchSkipsUnchangedSessions(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()

	spaces, err := s.Spaces(ctx)
	require.NoError(t, err)

	// Each read of the session list, and each sort, resolves the Slack thread's room once.
	recordTestConversations(t, s, "main", map[string]ConversationKind{"slack-thread:C1:1.1": KindSlackThread})
	_, err = s.db.ExecContext(ctx, `UPDATE conversations SET has_entries = TRUE, last_updated = now() WHERE conversation_id = 'slack-thread:C1:1.1'`)
	require.NoError(t, err)

	slack := &slackLookupMock{SidebarChannelAgentChoicesFunc: func(context.Context, string) (string, []string, error) { return "support", nil, nil }}
	counted := 0
	reads := func() int {
		n := len(slack.SidebarChannelAgentChoicesCalls()) - counted
		counted += n

		return n
	}

	open := func(person, path string, lane SavedSearch) {
		t.Helper()

		state, revision, err := s.TabState(ctx, person)
		require.NoError(t, err)

		saved, err := s.SaveTabState(ctx, person, &TabState{Tabs: []Tab{{Path: path, Space: spaces[0].ID, Search: &SearchPage{Board: "kanban", Searches: []SavedSearch{lane}}}}, Space: state.Space}, revision)
		require.NoError(t, err)
		require.True(t, saved)
	}

	var seen dashboardsSeen

	open(alice, "/search/one", SavedSearch{ID: "a", Query: "tag:a"})
	assert.Empty(t, watchPass(t, s, slack, &seen))
	assert.Zero(t, reads(), "no flagged lane, no read")

	open(alice, "/search/one", SavedSearch{ID: "a", Query: "tag:a", Notify: true})
	assert.Empty(t, watchPass(t, s, slack, &seen))
	assert.Equal(t, 2, reads(), "the list, and Alice's Dashboard sorted for the first time")

	assert.Empty(t, watchPass(t, s, slack, &seen))
	assert.Equal(t, 1, reads(), "the list, unchanged")

	open(bob, "/search/two", SavedSearch{ID: "b", Query: "tag:b", Notify: true})
	assert.Empty(t, watchPass(t, s, slack, &seen))
	assert.Equal(t, 2, reads(), "the list, unchanged, and Bob's Dashboard, not yet recorded")

	boardChat(t, s, "first", time.Now(), "b")
	assert.Equal(t, []string{bob + " /search/two loud: tag:b: first"}, watchPass(t, s, slack, &seen))
	assert.Equal(t, 3, reads(), "the list, changed, and both Dashboards")

	workspace := s.definitions.Clone().Workspace
	require.NoError(t, s.definitions.Do(func(cfg *config.Config) error {
		cfg.Workspace = filepath.Join(workspace, "missing")
		return nil
	}))

	boardChat(t, s, "second", time.Now(), "b")

	seen, err = s.watchDashboards(ctx, slack, seen)
	require.Error(t, err, "a sort reads the agent files")
	assert.Equal(t, "read note=tag:b: first", attentionOf(t, s, bob, "/search/two"))

	require.NoError(t, s.definitions.Do(func(cfg *config.Config) error {
		cfg.Workspace = workspace
		return nil
	}))
	assert.Equal(t, []string{bob + " /search/two loud: tag:b: second"}, watchPass(t, s, slack, &seen))
}

// A session a lane of unread sessions lists for a quiet mark, such as the person's own, is no arrival; one whose mark
// notifies is, and so is the quiet one in a lane of anything else.
func TestDashboardArrivalsSkipQuietMarksInUnreadLanes(t *testing.T) {
	t.Parallel()

	lanes := []SavedSearch{{Name: "Unread", Query: "is:unread", Notify: true}, {Name: "Tagged", Query: "tag:a", Notify: true}}
	was := []seenLane{{}, {}}
	chat := func(id string, quiet bool) SidebarChat {
		var c SidebarChat

		c.Session.Conversation.ID, c.Session.Name = id, id
		c.Session.Attention = Attention{UnreadAt: time.Now(), Quiet: quiet}

		return c
	}

	_, note := dashboardArrivals(lanes, was, []BoardLane{{Chats: []SidebarChat{chat("mine", true)}, Total: 1}, {}})
	assert.Empty(t, note)

	_, note = dashboardArrivals(lanes, was, []BoardLane{{Chats: []SidebarChat{chat("mine", true), chat("theirs", false)}, Total: 2}, {Chats: []SidebarChat{chat("mine", true)}, Total: 1}})
	assert.Equal(t, "Unread: theirs and 1 more", note)
}
