package backend

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

// attentionOf describes username's attention row at path: none without one, else its mark (read, an old or a new
// one, and whether it is quiet), whether it is parked, its timer (soon, due, or done for a park until it is done) and
// its note.
func attentionOf(t *testing.T, s *SessionService, username, path string) string {
	t.Helper()

	got, err := queryStrings(t.Context(), s.db, `SELECT concat_ws(' ',
    CASE WHEN unread_at IS NULL THEN 'read' WHEN unread_at < now() - interval '1 hour' THEN 'old' ELSE 'new' END || CASE WHEN unread_at IS NOT NULL AND quiet THEN ' quiet' ELSE '' END,
    CASE WHEN parked THEN 'parked' END, CASE WHEN until = 'infinity' THEN 'until=done' WHEN until > now() THEN 'until=soon' WHEN until IS NOT NULL THEN 'until=due' END, NULLIF('note=' || note, 'note='))
FROM web_tab_attention WHERE username = $1 AND path = $2`, "attention", username, path)
	require.NoError(t, err)

	if len(got) == 0 {
		return "none"
	}

	return got[0]
}

// markTab writes something new on path for username through the marking statement, as one reader of each of
// readers: a question or not.
func markTab(t *testing.T, db stateStoreDB, username, path string, readers ...reader) {
	t.Helper()

	waking := make([]bool, 0, len(readers))
	for _, r := range readers {
		waking = append(waking, r == question)
	}

	_, err := db.ExecContext(t.Context(), `WITH readers AS (
    SELECT $1::text AS username, $2::text AS path, NULLIF($3::text, '') AS conversation_id, r.waking, 'news'::text AS note
    FROM unnest($4::boolean[]) AS r(waking)
)`+markTabsSQL+` SELECT count(*) FROM marked`, username, path, TabConversation(path), waking)
	require.NoError(t, err)
}

// reader is one reader of something new: a question or not.
type reader uint8

const (
	turnEnd reader = iota
	question
)

// Each row of the marking matrix, on a session's tab and on a tab that is not a session.
func TestAttentionMatrix(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	ctx := t.Context()
	recordTestConversations(t, service, "main", map[string]ConversationKind{"web:attention": KindWebChat})

	soon := time.Now().Add(time.Hour)
	mark := func(r reader) func(t *testing.T, username, path string) {
		return func(t *testing.T, username, path string) {
			t.Helper()
			markTab(t, service.db, username, path, r)
		}
	}
	act := func(action AttentionAction, until *time.Time) func(t *testing.T, username, path string) {
		return func(t *testing.T, username, path string) {
			t.Helper()
			require.NoError(t, service.ChangeAttention(ctx, username, []string{path}, action, until))
		}
	}
	view := func(hold bool) func(t *testing.T, username, path string) {
		return func(t *testing.T, username, path string) {
			t.Helper()

			_, err := service.PollAttention(ctx, username, path, hold)
			require.NoError(t, err)
		}
	}
	poll := func(t *testing.T, username, _ string) {
		t.Helper()

		_, err := service.PollAttention(ctx, username, "", false)
		require.NoError(t, err)
	}
	then := func(steps ...func(t *testing.T, username, path string)) func(t *testing.T, username, path string) {
		return func(t *testing.T, username, path string) {
			t.Helper()

			for _, step := range steps {
				step(t, username, path)
			}
		}
	}
	// save saves the person's tabs as only the tab at path, or none, at revision.
	save := func(t *testing.T, username, path string, open bool, revision int64) {
		t.Helper()

		var tabs []Tab
		if open {
			tabs = []Tab{{Path: path, Space: "main"}}
		}

		saved, err := service.SaveTabState(ctx, username, &TabState{Tabs: tabs, Space: "main"}, revision)
		require.NoError(t, err)
		require.True(t, saved)
	}
	closeTab := func(t *testing.T, username, path string) {
		t.Helper()
		save(t, username, path, false, 1)
	}
	reopenTab := func(t *testing.T, username, path string) {
		t.Helper()
		closeTab(t, username, path)
		save(t, username, path, true, 2)
	}

	// seeded is a row's state before the event: an old mark, loud or quiet, a park, and a timer this far from now.
	type seeded struct {
		mark   string
		parked bool
		timer  time.Duration
	}

	for name, tc := range map[string]struct {
		tab    bool // the person has the location as a tab before it is seeded
		seeded seeded
		viewed time.Duration // the person viewed the location this long ago; zero is not at all
		do     func(t *testing.T, username, path string)
		want   string
	}{
		"turn end viewed 3s ago":           {viewed: 3 * time.Second, do: mark(turnEnd), want: "none"},
		"turn end viewed 11s ago":          {viewed: 11 * time.Second, do: mark(turnEnd), want: "new note=news"},
		"question viewed 3s ago on a park": {seeded: seeded{mark: "loud", parked: true, timer: time.Hour}, viewed: 3 * time.Second, do: mark(question), want: "old parked until=due"},
		"question viewed 5s ago":           {viewed: 5 * time.Second, do: mark(question), want: "new note=news"},
		"park time due in view":            {seeded: seeded{parked: true, timer: -time.Minute}, viewed: 2 * time.Second, do: poll, want: "read"},
		"poll views a due park time":       {seeded: seeded{parked: true, timer: -time.Minute}, do: view(false), want: "read"},
		"park time due":                    {seeded: seeded{mark: "quiet", parked: true, timer: -time.Minute}, do: poll, want: "new note=Park ended"},
		"turn end replaces a quiet mark":   {seeded: seeded{mark: "quiet"}, do: mark(turnEnd), want: "new note=news"},
		"turn end on a park":               {seeded: seeded{parked: true, timer: time.Hour}, do: mark(turnEnd), want: "new quiet parked until=soon note=news"},
		"question on a park":               {seeded: seeded{mark: "quiet", parked: true, timer: time.Hour}, do: mark(question), want: "new quiet parked until=due note=news"},
		"question on a park, then a poll":  {seeded: seeded{mark: "quiet", parked: true, timer: time.Hour}, do: then(mark(question), poll), want: "new note=news"},
		"mark as unread":                   {do: act(AttentionUnread, nil), want: "new quiet"},
		"mark as unread on a mark":         {seeded: seeded{mark: "loud"}, do: act(AttentionUnread, nil), want: "old"},
		"mark as unread on a park":         {seeded: seeded{parked: true}, do: act(AttentionUnread, nil), want: "read parked"},
		"mark as read":                     {seeded: seeded{mark: "loud"}, do: act(AttentionRead, nil), want: "none"},
		"mark as read on a park":           {seeded: seeded{mark: "loud", parked: true}, do: act(AttentionRead, nil), want: "read parked"},
		"view":                             {seeded: seeded{mark: "loud"}, do: view(false), want: "read"},
		"held view of a quiet mark":        {seeded: seeded{mark: "quiet"}, do: view(true), want: "old quiet"},
		"held view of a loud mark":         {seeded: seeded{mark: "loud"}, do: view(true), want: "read"},
		"park with a time":                 {seeded: seeded{mark: "loud"}, do: act(AttentionPark, &soon), want: "old quiet parked until=soon"},
		"unpark":                           {seeded: seeded{mark: "quiet", parked: true, timer: time.Hour}, do: act(AttentionUnpark, nil), want: "old quiet"},
		"unpark with no mark":              {seeded: seeded{parked: true}, do: act(AttentionUnpark, nil), want: "none"},
		"tab closes parked without a time": {tab: true, seeded: seeded{mark: "loud", parked: true}, do: closeTab, want: "old"},
		"tab closes parked with a time":    {tab: true, seeded: seeded{parked: true, timer: time.Hour}, do: closeTab, want: "read parked until=soon"},
		"tab opens again with its mark":    {tab: true, seeded: seeded{mark: "loud"}, do: reopenTab, want: "old quiet"},
	} {
		for location, path := range map[string]string{"session": "/s/" + base64.RawURLEncoding.EncodeToString([]byte("web:attention")), "page": "/cron"} {
			t.Run(name+" on a "+location, func(t *testing.T) {
				t.Parallel()

				username := name + " " + location
				_, err := service.db.ExecContext(ctx, `INSERT INTO web_tab_states (username, revision, viewing, viewed_at)
VALUES ($1, 0, CASE WHEN $3::float8 > 0 THEN $2 ELSE '' END, now() - make_interval(secs => $3::float8))`, username, path, tc.viewed.Seconds())
				require.NoError(t, err)

				if tc.tab {
					save(t, username, path, true, 0)
				}

				if tc.seeded != (seeded{}) {
					_, err := service.db.ExecContext(ctx, `INSERT INTO web_tab_attention (username, path, conversation_id, unread_at, quiet, parked, until)
VALUES ($1, $2, NULLIF($3, ''), CASE WHEN $4 <> '' THEN now() - interval '1 day' END, $4 = 'quiet', $5, CASE WHEN $6::float8 <> 0 THEN now() + make_interval(secs => $6::float8) END)`,
						username, path, TabConversation(path), tc.seeded.mark, tc.seeded.parked, tc.seeded.timer.Seconds())
					require.NoError(t, err)
				}

				tc.do(t, username, path)
				assert.Equal(t, tc.want, attentionOf(t, service, username, path))
			})
		}
	}
}

// Readers that name one person and location twice write one mark, a question's when one of them is.
func TestAttentionMarkTakesStrongestReader(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	markTab(t, service.db, alice, "/cron", turnEnd, turnEnd)
	assert.Equal(t, "new note=news", attentionOf(t, service, alice, "/cron"))

	_, err := service.db.ExecContext(t.Context(), `UPDATE web_tab_attention SET parked = TRUE`)
	require.NoError(t, err)
	markTab(t, service.db, alice, "/cron", turnEnd, question)
	assert.Equal(t, "new quiet parked until=due note=news", attentionOf(t, service, alice, "/cron"), "a question among them makes the park due")
}

// Every decision that depends on the stored row is made on the locked row, so a change that commits while a mark is
// being written, or a mark that commits while a change waits, is never lost.
func TestAttentionDecidesOnLockedRow(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	ctx := t.Context()
	soon := time.Now().Add(time.Hour)

	// held runs write in a transaction it keeps open until change, run beside it, waits on the row lock.
	held := func(write func(tx *sql.Tx), change func() error) {
		t.Helper()

		tx, err := service.db.BeginTx(ctx, nil)
		require.NoError(t, err)

		defer func() { _ = tx.Rollback() }()

		var holder int
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&holder))
		write(tx)

		var changes errgroup.Group
		changes.Go(change)
		require.Eventually(t, func() bool {
			var blocked bool
			require.NoError(t, service.db.QueryRowContext(ctx, `SELECT count(*) > 0 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))`, holder).Scan(&blocked))

			return blocked
		}, 5*time.Second, time.Millisecond)
		require.NoError(t, tx.Commit())
		require.NoError(t, changes.Wait())
	}

	// Unpark of a parked tab with no mark while a turn end commits a mark on it: unparked and bold.
	require.NoError(t, service.ChangeAttention(ctx, alice, []string{"/cron"}, AttentionPark, nil))
	held(func(tx *sql.Tx) { markTab(t, tx, alice, "/cron", turnEnd) }, func() error {
		return service.ChangeAttention(ctx, alice, []string{"/cron"}, AttentionUnpark, nil)
	})
	assert.Equal(t, "new quiet note=news", attentionOf(t, service, alice, "/cron"))

	// A park commits while a loud mark waits to be written: parked with a quiet mark.
	held(func(tx *sql.Tx) {
		_, err := tx.ExecContext(ctx, `INSERT INTO web_tab_attention (username, path, parked, until) VALUES ($1, '/cron', TRUE, now() + interval '1 hour')`, bob)
		require.NoError(t, err)
	}, func() error {
		if _, err := service.db.ExecContext(ctx, `WITH readers AS (SELECT $1::text AS username, '/cron'::text AS path, NULL::text AS conversation_id, FALSE AS waking, ''::text AS note)`+markTabsSQL+` SELECT count(*) FROM marked`, bob); err != nil {
			return fmt.Errorf("mark: %w", err)
		}

		return nil
	})
	assert.Equal(t, "new quiet parked until=soon", attentionOf(t, service, bob, "/cron"))

	// A turn end and the person's Park create a session's first row at once: both succeed, and one row results.
	recordTestConversations(t, service, "main", map[string]ConversationKind{"web:first": KindWebChat})

	path := "/s/" + base64.RawURLEncoding.EncodeToString([]byte("web:first"))

	held(func(tx *sql.Tx) { markTab(t, tx, alice, path, turnEnd) }, func() error {
		return service.ChangeAttention(ctx, alice, []string{path}, AttentionPark, &soon)
	})
	assert.Equal(t, "new quiet parked until=soon note=news", attentionOf(t, service, alice, path))

	_, err := service.db.ExecContext(ctx, `DELETE FROM conversations WHERE conversation_id = 'web:first'`)
	require.NoError(t, err)
	assert.Equal(t, "none", attentionOf(t, service, alice, path))
}

// cronTabPath is the tab path of the cron named stem as the Web spells it: base64url of one byte per character.
func cronTabPath(stem string) string {
	raw := make([]byte, 0, len(stem))
	for _, r := range stem {
		raw = append(raw, byte(r))
	}

	return "/cron/" + base64.RawURLEncoding.EncodeToString(raw)
}

// locationMarks lists, and then clears, every mark whose note is note: its person, path, and whether it is quiet.
func locationMarks(t *testing.T, s *SessionService, note string) []string {
	t.Helper()

	got, err := queryStrings(t.Context(), s.db, `SELECT format('%s %s %s', username, path, CASE WHEN quiet THEN 'quiet' ELSE 'loud' END)
FROM web_tab_attention WHERE unread_at IS NOT NULL AND note = $1`, "location marks", note)
	require.NoError(t, err)

	_, err = s.db.ExecContext(t.Context(), `DELETE FROM web_tab_attention`)
	require.NoError(t, err)

	return got
}

// A cron run's turn end reaches the people with that cron's tab under any spelling the Web reads, and with the Cron
// overview of the cron's space, or of any space for a cron without one; a person limited to other spaces gets nothing.
func TestCronRunEndMarksCronTabs(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	ctx := t.Context()

	const (
		carol = "carol@example.com" // limited to Ops
		dave  = "dave@example.com"  // limited to Main
		erin  = "erin@example.com"  // keeps the routine on her Top Shelf
	)

	require.NoError(t, service.definitions.Do(func(cfg *config.Config) error {
		cfg.Permissions = map[string][]string{carol: {"Ops"}, dave: {"Main"}}
		return nil
	}))
	require.NoError(t, service.CreateLimitSpaces(ctx, []string{"Ops"}))

	spaces, err := service.Spaces(ctx)
	require.NoError(t, err)

	mainID, ops := spaces[0].ID, spaces[1].ID
	routine, typed, shared := cronTabPath("marketing · 30m"), "/cron/marketing%20%C2%B7%2030m", "/cron/marketing · 30m"

	for person, tabs := range map[string][]Tab{
		alice: {{Path: routine, Space: mainID}, {Path: "/cron", Space: ops}, {Path: cronTabPath("daily"), Space: mainID}},
		bob:   {{Path: typed, Space: mainID}, {Path: "/cron", Space: mainID}},
		carol: {{Path: routine, Space: ops}, {Path: "/cron/daily", Space: ops}},
		dave:  {{Path: routine, Space: mainID}, {Path: "/cron", Space: mainID}},
	} {
		saved, err := service.SaveTabState(ctx, person, &TabState{Tabs: tabs, Space: mainID}, 0)
		require.NoError(t, err)
		require.True(t, saved)
	}

	require.NoError(t, service.SaveTopShelf(ctx, erin, &TopShelf{Tabs: []ShelfTab{{Space: mainID, Path: routine}, {Server: "other", Space: mainID, Path: "/cron"}}}))
	require.NoError(t, service.ShareTab(ctx, mainID, shared, nil))

	runEnds := func(runID, path, space string) []string {
		t.Helper()

		_, err := service.recordConversation(ctx, &protocol.Conversation{ID: runID, Agent: "marketing", CreatedBy: string(ThreadCreatedByCron), Run: &protocol.CronRun{Path: path}}, conversationFacts{}, harness.AgentStickyOff)
		require.NoError(t, err)

		inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "run", false)
		inbound.Cronjob = &protocol.CronjobMessage{RelativePath: path, Agent: "marketing", Space: space}
		require.NoError(t, startTurnDB(ctx, service.db, "turn:"+runID, runID, inbound))
		_, err = service.finishTurn(ctx, "turn:"+runID, &turnFinish{conversationID: runID, outbound: protocol.NewOutboundMessage(runID, "done")})
		require.NoError(t, err)

		return locationMarks(t, service, "Run finished")
	}

	// The routine of Ops: Carol's own tab reaches her, but not the tab Shared in Main; nothing reaches Dave.
	assert.ElementsMatch(t, []string{
		alice + " /cron loud", alice + " " + shared + " loud", alice + " " + routine + " loud",
		bob + " " + shared + " loud", bob + " " + typed + " loud",
		carol + " " + routine + " loud",
		erin + " " + shared + " loud", erin + " " + routine + " loud",
	}, runEnds("cron:marketing", "agents/marketing.md#30m", "Ops"))

	// A cron without a space is on the Cron overview of every space, and reaches no limited person.
	assert.ElementsMatch(t, []string{alice + " /cron loud", alice + " " + cronTabPath("daily") + " loud", bob + " /cron loud"},
		runEnds("cron:daily", "cron/daily.md", ""))

	// A run marks once, when it ends: a turn that ends while another of the run waits in its queue marks nothing.
	_, err = service.recordConversation(ctx, &protocol.Conversation{ID: "cron:twice", Agent: "marketing", CreatedBy: string(ThreadCreatedByCron), Run: &protocol.CronRun{Path: "cron/daily.md"}}, conversationFacts{}, harness.AgentStickyOff)
	require.NoError(t, err)

	turnEnds := func(turnID string) []string {
		t.Helper()

		require.NoError(t, startTurnDB(ctx, service.db, turnID, "cron:twice", protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "run", false)))
		_, err := service.finishTurn(ctx, turnID, &turnFinish{conversationID: "cron:twice", outbound: protocol.NewOutboundMessage("cron:twice", "done")})
		require.NoError(t, err)

		return locationMarks(t, service, "Run finished")
	}

	require.NoError(t, service.PutThreadQueueItem("next", &protocol.ThreadQueueItem{ConversationID: "cron:twice", Message: "again"}))
	assert.Empty(t, turnEnds("turn:first"))

	removed, _, err := service.DeleteThreadQueueItem(ctx, "cron:twice", "next")
	require.NoError(t, err)
	require.True(t, removed)
	assert.ElementsMatch(t, []string{alice + " /cron loud", alice + " " + cronTabPath("daily") + " loud", bob + " /cron loud"}, turnEnds("turn:last"))
}

// A finished search marks the person's Search tab, loud, when it is among their own tabs, on their Top Shelf of this
// server, or Shared in a space they can see.
func TestMarkSearchFinished(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	ctx := t.Context()

	const carol = "carol@example.com" // limited to Ops

	require.NoError(t, service.definitions.Do(func(cfg *config.Config) error {
		cfg.Permissions = map[string][]string{carol: {"Ops"}}
		return nil
	}))
	require.NoError(t, service.CreateLimitSpaces(ctx, []string{"Ops"}))

	spaces, err := service.Spaces(ctx)
	require.NoError(t, err)

	mainID := spaces[0].ID
	saved, err := service.SaveTabState(ctx, alice, &TabState{Tabs: []Tab{{Path: "/search/own", Space: mainID}}}, 0)
	require.NoError(t, err)
	require.True(t, saved)
	require.NoError(t, service.SaveTopShelf(ctx, bob, &TopShelf{Tabs: []ShelfTab{{Space: mainID, Path: "/search/shelf"}, {Server: "other", Space: mainID, Path: "/search/own"}}}))
	require.NoError(t, service.ShareTab(ctx, mainID, "/search/shared", nil))

	for _, tc := range []struct{ username, path, want string }{
		{alice, "/search/own", "new note=Search finished"},
		{bob, "/search/shelf", "new note=Search finished"},
		{bob, "/search/own", "none"}, // on the shelf of another server
		{bob, "/search/shared", "new note=Search finished"},
		{carol, "/search/shared", "none"},
	} {
		require.NoError(t, service.MarkSearchFinished(ctx, tc.username, tc.path))
		assert.Equal(t, tc.want, attentionOf(t, service, tc.username, tc.path), "%s %s", tc.username, tc.path)
	}
}

// A park until it is done takes only a session's tab, lasts when the tab closes, and ends with the session's next turn
// end: at the person's next poll the tab comes back, bold, with the turn's own notification. A turn end with the tab in
// view, a question, or Unpark ends it too.
func TestAttentionParkUntilDone(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	ctx := t.Context()

	recordTestConversations(t, service, "main", map[string]ConversationKind{"web:done": KindWebChat})

	session := Tab{Path: sessionPath("web:done"), Space: "main"}
	saved, err := service.SaveTabState(ctx, alice, &TabState{Tabs: []Tab{session}, Space: "main"}, 0)
	require.NoError(t, err)
	require.True(t, saved)

	require.NoError(t, service.ChangeAttention(ctx, alice, []string{session.Path, "/cron"}, AttentionParkUntilDone, nil))
	assert.Equal(t, "read parked until=done", attentionOf(t, service, alice, session.Path))
	assert.Equal(t, "none", attentionOf(t, service, alice, "/cron"), "a location that is no session is skipped")

	for row, err := range service.sidebarSessions(ctx, alice, []string{"web:done"}) {
		require.NoError(t, err)
		assert.True(t, row.Attention.Parked && row.Attention.UntilDone && row.Attention.Until.IsZero(), "the list says until done, with no time")
	}

	saved, err = service.SaveTabState(ctx, alice, &TabState{Space: "main"}, 1)
	require.NoError(t, err)
	require.True(t, saved)
	assert.Equal(t, "read parked until=done", attentionOf(t, service, alice, session.Path), "closing keeps it")

	// A turn nobody sent ends: it reaches Alice only through her park, and makes the park due.
	require.NoError(t, startTurnDB(ctx, service.db, "turn:done", "web:done", protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "run", false)))
	_, err = service.finishTurn(ctx, "turn:done", &turnFinish{conversationID: "web:done", outbound: protocol.NewOutboundMessage("web:done", "done")})
	require.NoError(t, err)
	assert.Equal(t, "new quiet parked until=due", attentionOf(t, service, alice, session.Path))

	_, err = service.PollAttention(ctx, alice, "", false)
	require.NoError(t, err)
	assert.Equal(t, "new", attentionOf(t, service, alice, session.Path), "unparked and loud, with no note of its own: the reply speaks")

	state, _, err := service.TabState(ctx, alice)
	require.NoError(t, err)
	assert.Equal(t, []Tab{session}, state.Tabs, "the closed tab is back")

	for name, tc := range map[string]struct {
		viewed bool
		end    reader
		unpark bool
		want   string
	}{
		"turn end in view": {viewed: true, end: turnEnd, want: "read parked until=due"},
		"question":         {end: question, want: "new quiet parked until=due note=news"},
		"unpark":           {unpark: true, want: "none"},
	} {
		username := name + "@example.com"
		_, err := service.db.ExecContext(ctx, `INSERT INTO web_tab_states (username, revision, viewing, viewed_at)
VALUES ($1, 0, CASE WHEN $3 THEN $2 ELSE '' END, now())`, username, session.Path, tc.viewed)
		require.NoError(t, err)
		require.NoError(t, service.ChangeAttention(ctx, username, []string{session.Path}, AttentionParkUntilDone, nil))

		if tc.unpark {
			require.NoError(t, service.ChangeAttention(ctx, username, []string{session.Path}, AttentionUnpark, nil))
		} else {
			markTab(t, service.db, username, session.Path, tc.end)
		}

		assert.Equal(t, tc.want, attentionOf(t, service, username, session.Path), name)
	}
}

// Auto-park is a choice kept on the session's attention row: it leaves the row's park and timer alone, keeps the row
// through an unpark, and skips a location that is no session. The choice for every session starts off.
func TestAttentionAutoPark(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	ctx := t.Context()
	soon := time.Now().Add(time.Hour)
	path := sessionPath("web:auto")

	recordTestConversations(t, service, "main", map[string]ConversationKind{"web:auto": KindWebChat})

	require.NoError(t, service.ChangeAttention(ctx, alice, []string{path}, AttentionPark, &soon))
	require.NoError(t, service.ChangeAttention(ctx, alice, []string{path, "/cron"}, AttentionAutoPark, nil))
	assert.Equal(t, "read parked until=soon", attentionOf(t, service, alice, path))
	assert.Equal(t, "none", attentionOf(t, service, alice, "/cron"))

	require.NoError(t, service.ChangeAttention(ctx, alice, []string{path}, AttentionUnpark, nil))
	assert.Equal(t, "read", attentionOf(t, service, alice, path), "the choice keeps the row")

	on, err := service.AutoParkAll(ctx, alice)
	require.NoError(t, err)
	assert.False(t, on)
}

// A timer that comes due on a closed tab brings it back where it was, once, with its Search page.
func TestAttentionWakeRestoresClosedTabs(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	ctx := t.Context()
	soon := time.Now().Add(time.Hour)

	recordTestConversations(t, service, "main", map[string]ConversationKind{"a": KindWebChat})

	revision := int64(0)
	save := func(state *TabState) {
		t.Helper()

		saved, err := service.SaveTabState(ctx, alice, state, revision)
		require.NoError(t, err)
		require.True(t, saved)

		revision++
	}
	due := func() {
		t.Helper()

		_, err := service.db.ExecContext(ctx, `UPDATE web_tab_attention SET until = now() - interval '1 second' WHERE until IS NOT NULL`)
		require.NoError(t, err)
	}
	poll := func() int64 {
		t.Helper()

		got, err := service.PollAttention(ctx, alice, "", false)
		require.NoError(t, err)

		return got
	}

	page := &SearchPage{Name: "Triage", Searches: []SavedSearch{{ID: "1", Query: "is:unread"}}}
	a, search, cron := Tab{Path: "/s/YQ", Group: "g", Space: "main"}, Tab{Path: "/search/x", Group: "g", Space: "main", Search: page}, Tab{Path: "/cron", Space: "ops"}
	group := []TabGroup{{ID: "g", Name: "Work"}}
	save(&TabState{Tabs: []Tab{a, search, cron}, Groups: group, Space: "main"})
	require.NoError(t, service.ChangeAttention(ctx, alice, []string{"/search/x"}, AttentionPark, &soon))
	require.NoError(t, service.ChangeAttention(ctx, alice, []string{"/cron"}, AttentionPark, &soon))
	require.NoError(t, service.ChangeAttention(ctx, alice, []string{a.Path, "/agents"}, AttentionPark, &soon))
	save(&TabState{Tabs: []Tab{a}, Groups: group, Space: "main"})
	due()

	// Two polls at once: whichever locks the person's state first wakes and restores, and the other finds nothing due.
	tx, err := service.db.BeginTx(ctx, nil)
	require.NoError(t, err)

	defer func() { _ = tx.Rollback() }()

	var holder int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT pg_backend_pid() FROM web_tab_states WHERE username = $1 FOR UPDATE`, alice).Scan(&holder))

	revisions := make(chan int64, 2)

	for range 2 {
		go func() {
			got, err := service.PollAttention(ctx, alice, "", false)
			assert.NoError(t, err)

			revisions <- got
		}()
	}

	require.Eventually(t, func() bool {
		var waiting int
		require.NoError(t, service.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))`, holder).Scan(&waiting))

		return waiting > 0
	}, 5*time.Second, time.Millisecond)
	require.NoError(t, tx.Commit())
	assert.Equal(t, []int64{revision + 1, revision + 1}, []int64{<-revisions, <-revisions}, "the revision rises once")

	state, got, err := service.TabState(ctx, alice)
	require.NoError(t, err)
	assert.Equal(t, revision+1, got)
	assert.Equal(t, []Tab{a, search, cron}, state.Tabs, "back in their space, group and position, a Search tab with its page")
	assertNoOrphanSearchPages(t, service)

	closed, err := service.ClosedTabs(ctx, alice)
	require.NoError(t, err)
	assert.Empty(t, closed)
	assert.Equal(t, "new note=Park ended", attentionOf(t, service, alice, "/search/x"))
	assert.Equal(t, "new note=Park ended", attentionOf(t, service, alice, "/cron"))
	assert.Equal(t, "new note=Park ended", attentionOf(t, service, alice, a.Path), "a park on an open tab ends in place")
	assert.Equal(t, "new note=Park ended", attentionOf(t, service, alice, "/agents"), "one on a location never a tab ends alone")
	assert.Equal(t, revision+1, poll(), "a second poll wakes nothing")

	// The group is gone by the time the park comes due, and the strip is shorter: the tab comes back ungrouped, last.
	revision++

	require.NoError(t, service.ChangeAttention(ctx, alice, []string{"/search/x"}, AttentionPark, &soon))
	save(&TabState{Tabs: []Tab{a, cron}, Groups: group, Space: "main"})
	save(&TabState{Tabs: []Tab{{Path: a.Path, Space: "main"}}, Space: "main"})
	due()
	assert.Equal(t, revision+1, poll())

	state, _, err = service.TabState(ctx, alice)
	require.NoError(t, err)
	assert.Equal(t, []Tab{{Path: a.Path, Space: "main"}, {Path: "/search/x", Space: "main", Search: page}}, state.Tabs)

	// An agent's question on a closed tab parked with a time brings it back at the next poll, marked with the question;
	// on a closed tab that is not parked it only marks.
	revision++

	require.NoError(t, service.ChangeAttention(ctx, alice, []string{a.Path}, AttentionPark, &soon))
	save(&TabState{Space: "main"})
	markTab(t, service.db, alice, a.Path, question)
	markTab(t, service.db, alice, "/search/x", question)
	assert.Equal(t, revision+1, poll())

	state, _, err = service.TabState(ctx, alice)
	require.NoError(t, err)
	assert.Equal(t, []Tab{{Path: a.Path, Space: "main"}}, state.Tabs)
	assert.Equal(t, "new note=news", attentionOf(t, service, alice, a.Path))
	assert.Equal(t, "new note=news", attentionOf(t, service, alice, "/search/x"))
}
