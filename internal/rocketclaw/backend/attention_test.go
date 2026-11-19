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

func attentionOf(t *testing.T, s *SessionService, username, path string) string {
	t.Helper()

	got, err := queryStrings(t.Context(), s.db, `SELECT concat_ws(' ',
    CASE WHEN unread_at IS NULL THEN 'read' WHEN unread_at < now() - interval '1 hour' THEN 'old' ELSE 'new' END || CASE WHEN unread_at IS NOT NULL AND quiet THEN ' quiet' ELSE '' END,
    CASE WHEN parked THEN 'parked' END, CASE WHEN until = 'infinity' THEN 'until=done' WHEN until > now() THEN 'until=soon' WHEN until IS NOT NULL THEN 'until=due' END,
    CASE WHEN worked THEN 'worked' END,
    CASE WHEN NOT resumes THEN NULL WHEN resume_until IS NULL THEN 'resume=unpark' WHEN resume_until = 'infinity' THEN 'resume=done' WHEN resume_until > now() THEN 'resume=soon' ELSE 'resume=past' END,
    NULLIF('note=' || note, 'note='), CASE WHEN unfollowed THEN 'unfollowed' END)
FROM web_tab_attention WHERE username = $1 AND path = $2`, "attention", username, path)
	require.NoError(t, err)

	if len(got) == 0 {
		return "none"
	}

	return got[0]
}

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

type reader uint8

const (
	turnEnd reader = iota
	question
)

func TestAttentionMatrix(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	soon := time.Now().Add(time.Hour)

	type step func(t *testing.T, service *SessionService, username, path string)

	mark := func(r reader) step {
		return func(t *testing.T, service *SessionService, username, path string) {
			t.Helper()
			markTab(t, service.db, username, path, r)
		}
	}
	act := func(action AttentionAction, until *time.Time) step {
		return func(t *testing.T, service *SessionService, username, path string) {
			t.Helper()
			require.NoError(t, service.ChangeAttention(ctx, username, []string{path}, action, until))
		}
	}
	view := func(hold bool) step {
		return func(t *testing.T, service *SessionService, username, path string) {
			t.Helper()

			_, err := service.PollAttention(ctx, username, path, hold)
			require.NoError(t, err)
		}
	}
	tick := func(t *testing.T, service *SessionService, _, _ string) {
		t.Helper()
		require.NoError(t, service.tickParks(ctx))
	}
	asked := func(t *testing.T, service *SessionService, username, path string) {
		t.Helper()
		markTab(t, service.db, username, path, question)
		tick(t, service, username, path)
	}
	askedTwice := func(t *testing.T, service *SessionService, username, path string) {
		t.Helper()
		markTab(t, service.db, username, path, question)
		markTab(t, service.db, username, path, question)
	}
	answered := func(t *testing.T, service *SessionService, username, path string) {
		t.Helper()
		tick(t, service, username, path)
		tick(t, service, username, path)
	}
	then := func(steps ...step) step {
		return func(t *testing.T, service *SessionService, username, path string) {
			t.Helper()

			for _, step := range steps {
				step(t, service, username, path)
			}
		}
	}
	save := func(t *testing.T, service *SessionService, username, path string, open bool, revision int64) {
		t.Helper()

		var tabs []Tab
		if open {
			tabs = []Tab{{Path: path, Space: "main"}}
		}

		saved, err := service.SaveTabState(ctx, username, &TabState{Tabs: tabs, Space: "main"}, revision)
		require.NoError(t, err)
		require.True(t, saved)
	}
	closeTab := func(t *testing.T, service *SessionService, username, path string) {
		t.Helper()
		save(t, service, username, path, false, 1)
	}
	reopenTab := func(t *testing.T, service *SessionService, username, path string) {
		t.Helper()
		closeTab(t, service, username, path)
		save(t, service, username, path, true, 2)
	}

	type seeded struct {
		mark       string
		parked     bool
		timer      time.Duration
		unfollowed bool
	}

	for name, tc := range map[string]struct {
		tab    bool
		seeded seeded
		viewed time.Duration
		do     step
		want   string
	}{
		"turn end viewed 3s ago":           {viewed: 3 * time.Second, do: mark(turnEnd), want: "none"},
		"turn end viewed 11s ago":          {viewed: 11 * time.Second, do: mark(turnEnd), want: "new note=news"},
		"question viewed 3s ago on a park": {seeded: seeded{mark: "loud", parked: true, timer: time.Hour}, viewed: 3 * time.Second, do: mark(question), want: "old parked until=due resume=soon"},
		"question viewed 5s ago":           {viewed: 5 * time.Second, do: mark(question), want: "new note=news"},
		"park time due in view":            {seeded: seeded{parked: true, timer: -time.Minute}, viewed: 2 * time.Second, do: tick, want: "read"},
		"a view, then a due park time":     {seeded: seeded{parked: true, timer: -time.Minute}, do: then(view(false), tick), want: "read"},
		"park time due":                    {seeded: seeded{mark: "quiet", parked: true, timer: -time.Minute}, do: tick, want: "new note=Park ended"},
		"turn end replaces a quiet mark":   {seeded: seeded{mark: "quiet"}, do: mark(turnEnd), want: "new note=news"},
		"turn end on a park":               {seeded: seeded{parked: true, timer: time.Hour}, do: mark(turnEnd), want: "new quiet parked until=soon note=news"},
		"question on a park":               {seeded: seeded{mark: "quiet", parked: true, timer: time.Hour}, do: mark(question), want: "new quiet parked until=due resume=soon note=news"},
		"question on a park, then a tick":  {seeded: seeded{mark: "quiet", parked: true, timer: time.Hour}, do: then(mark(question), tick), want: "new resume=soon note=news"},
		"question on a park, answered":     {seeded: seeded{mark: "quiet", parked: true, timer: time.Hour}, do: then(mark(question), answered), want: "new quiet parked until=soon note=news"},
		"two questions on a park":          {seeded: seeded{mark: "quiet", parked: true, timer: time.Hour}, do: askedTwice, want: "new quiet parked until=due resume=soon note=news"},
		"a question, then marked read":     {seeded: seeded{mark: "quiet", parked: true, timer: time.Hour}, do: then(asked, act(AttentionRead, nil), tick), want: "none"},
		"a view does not wake a park":      {seeded: seeded{parked: true, timer: -time.Minute}, do: view(false), want: "read parked until=due"},
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
		"unfollow":                         {do: act(AttentionUnfollow, nil), want: "read unfollowed"},
		"unfollow clears a mark":           {seeded: seeded{mark: "loud"}, do: act(AttentionUnfollow, nil), want: "read unfollowed"},
		"unfollow keeps a timed park":      {seeded: seeded{mark: "quiet", parked: true, timer: time.Hour}, do: act(AttentionUnfollow, nil), want: "read parked until=soon unfollowed"},
		"unfollow keeps a question's park": {seeded: seeded{mark: "quiet", parked: true, timer: time.Hour}, do: then(mark(question), tick, act(AttentionUnfollow, nil)), want: "read resume=soon unfollowed"},
		"turn end while unfollowed":        {seeded: seeded{unfollowed: true}, do: mark(turnEnd), want: "read unfollowed"},
		"question while unfollowed":        {seeded: seeded{unfollowed: true}, do: mark(question), want: "read unfollowed"},
		"question on an unfollowed park":   {seeded: seeded{parked: true, timer: time.Hour, unfollowed: true}, do: mark(question), want: "read parked until=soon unfollowed"},
		"park time due while unfollowed":   {seeded: seeded{parked: true, timer: -time.Minute, unfollowed: true}, do: tick, want: "read unfollowed"},
		"mark as unread while unfollowed":  {seeded: seeded{unfollowed: true}, do: act(AttentionUnread, nil), want: "new quiet unfollowed"},
		"follow":                           {seeded: seeded{unfollowed: true}, do: act(AttentionFollow, nil), want: "none"},
		"follow keeps a park":              {seeded: seeded{parked: true, timer: time.Hour, unfollowed: true}, do: act(AttentionFollow, nil), want: "read parked until=soon"},
		"follow, then a turn end":          {seeded: seeded{unfollowed: true}, do: then(act(AttentionFollow, nil), mark(turnEnd)), want: "new note=news"},
	} {
		for location, path := range map[string]string{"session": "/s/" + base64.RawURLEncoding.EncodeToString([]byte("web:attention")), "page": "/cron"} {
			t.Run(name+" on a "+location, func(t *testing.T) {
				t.Parallel()

				service := newTestSessionService(t)
				recordTestConversations(t, service, "main", map[string]ConversationKind{"web:attention": KindWebChat})

				username := name + " " + location
				_, err := service.db.ExecContext(ctx, `INSERT INTO web_tab_states (username, revision, viewing, viewed_at)
VALUES ($1, 0, CASE WHEN $3::float8 > 0 THEN $2 ELSE '' END, now() - make_interval(secs => $3::float8))`, username, path, tc.viewed.Seconds())
				require.NoError(t, err)

				if tc.tab {
					save(t, service, username, path, true, 0)
				}

				if tc.seeded != (seeded{}) {
					_, err := service.db.ExecContext(ctx, `INSERT INTO web_tab_attention (username, path, conversation_id, unread_at, quiet, parked, until, unfollowed)
VALUES ($1, $2, NULLIF($3, ''), CASE WHEN $4 <> '' THEN now() - interval '1 day' END, $4 = 'quiet', $5, CASE WHEN $6::float8 <> 0 THEN now() + make_interval(secs => $6::float8) END, $7)`,
						username, path, TabConversation(path), tc.seeded.mark, tc.seeded.parked, tc.seeded.timer.Seconds(), tc.seeded.unfollowed)
					require.NoError(t, err)
				}

				tc.do(t, service, username, path)
				assert.Equal(t, tc.want, attentionOf(t, service, username, path))
			})
		}
	}
}

func TestAttentionMarkTakesStrongestReader(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	markTab(t, service.db, alice, "/cron", turnEnd, turnEnd)
	assert.Equal(t, "new note=news", attentionOf(t, service, alice, "/cron"))

	_, err := service.db.ExecContext(t.Context(), `UPDATE web_tab_attention SET parked = TRUE`)
	require.NoError(t, err)
	markTab(t, service.db, alice, "/cron", turnEnd, question)
	assert.Equal(t, "new quiet parked until=due resume=unpark note=news", attentionOf(t, service, alice, "/cron"), "a question among them makes the park due")
}

func TestAttentionDecidesOnLockedRow(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	ctx := t.Context()
	soon := time.Now().Add(time.Hour)

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

	require.NoError(t, service.ChangeAttention(ctx, alice, []string{"/cron"}, AttentionPark, nil))
	held(func(tx *sql.Tx) { markTab(t, tx, alice, "/cron", turnEnd) }, func() error {
		return service.ChangeAttention(ctx, alice, []string{"/cron"}, AttentionUnpark, nil)
	})
	assert.Equal(t, "new quiet note=news", attentionOf(t, service, alice, "/cron"))

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

func cronTabPath(stem string) string {
	raw := make([]byte, 0, len(stem))
	for _, r := range stem {
		raw = append(raw, byte(r))
	}

	return "/cron/" + base64.RawURLEncoding.EncodeToString(raw)
}

func locationMarks(t *testing.T, s *SessionService, note string) []string {
	t.Helper()

	got, err := queryStrings(t.Context(), s.db, `SELECT format('%s %s %s', username, path, CASE WHEN quiet THEN 'quiet' ELSE 'loud' END)
FROM web_tab_attention WHERE unread_at IS NOT NULL AND note = $1`, "location marks", note)
	require.NoError(t, err)

	_, err = s.db.ExecContext(t.Context(), `DELETE FROM web_tab_attention`)
	require.NoError(t, err)

	return got
}

func TestCronRunEndMarksCronTabs(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	ctx := t.Context()

	const (
		carol = "carol@example.com"
		dave  = "dave@example.com"
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

	assert.ElementsMatch(t, []string{
		alice + " /cron loud", alice + " " + shared + " loud", alice + " " + routine + " loud",
		bob + " " + shared + " loud", bob + " " + typed + " loud",
		carol + " " + routine + " loud",
	}, runEnds("cron:marketing", "agents/marketing.md#30m", "Ops"))

	assert.ElementsMatch(t, []string{alice + " /cron loud", alice + " " + cronTabPath("daily") + " loud", bob + " /cron loud"},
		runEnds("cron:daily", "cron/daily.md", ""))

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

func TestMarkSearchFinished(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	ctx := t.Context()

	const carol = "carol@example.com"

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
	require.NoError(t, service.ShareTab(ctx, mainID, "/search/shared", nil))

	for _, tc := range []struct{ username, path, want string }{
		{alice, "/search/own", "new note=Search finished"},
		{bob, "/search/own", "none"},
		{bob, "/search/shared", "new note=Search finished"},
		{carol, "/search/shared", "none"},
	} {
		require.NoError(t, service.MarkSearchFinished(ctx, tc.username, tc.path))
		assert.Equal(t, tc.want, attentionOf(t, service, tc.username, tc.path), "%s %s", tc.username, tc.path)
	}
}

func TestAttentionParkUntilDone(t *testing.T) {
	t.Parallel()

	const carol = "carol@example.com"

	service := newTestSessionService(t)
	ctx := t.Context()
	soon := time.Now().Add(time.Hour)

	tick := func(t *testing.T) {
		t.Helper()
		require.NoError(t, service.tickParks(ctx))
	}
	start := func(t *testing.T, chat, turnID string) {
		t.Helper()
		require.NoError(t, startTurnDB(ctx, service.db, turnID, chat, protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "run", false)))
	}
	end := func(t *testing.T, chat, turnID string, terminal protocol.Terminal) {
		t.Helper()

		_, err := service.finishTurn(ctx, turnID, &turnFinish{conversationID: chat, outbound: protocol.NewOutboundMessage(chat, "done"), terminal: terminal})
		require.NoError(t, err)
	}
	turn := func(t *testing.T, chat, turnID string, terminal protocol.Terminal) {
		t.Helper()
		start(t, chat, turnID)
		end(t, chat, turnID, terminal)
		require.NoError(t, service.closeTurn(ctx, turnID))
	}
	queue := func(t *testing.T, chat string) {
		t.Helper()
		require.NoError(t, service.PutThreadQueueItem(chat+":next", &protocol.ThreadQueueItem{ConversationID: chat, Message: "again"}))
	}

	t.Run("waits for the session to be idle", func(t *testing.T) {
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
		tick(t)
		assert.Equal(t, "read parked until=done", attentionOf(t, service, alice, session.Path), "closing keeps it, and an idle session has not worked yet")

		queue(t, "web:done")
		start(t, "web:done", "turn:first")
		end(t, "web:done", "turn:first", "")
		tick(t)
		assert.Equal(t, "new quiet parked until=done worked", attentionOf(t, service, alice, session.Path), "a turn still delivering is work")

		require.NoError(t, service.closeTurn(ctx, "turn:first"))
		tick(t)
		assert.Equal(t, "new quiet parked until=done worked", attentionOf(t, service, alice, session.Path), "a queued message is work")

		removed, _, err := service.DeleteThreadQueueItem(ctx, "web:done", "web:done:next")
		require.NoError(t, err)
		require.True(t, removed)

		turn(t, "web:done", "turn:job", protocol.TerminalStopped)

		_, err = service.db.ExecContext(ctx, `UPDATE turns SET kind = 'script', takes_lane = FALSE, note = 'pending', wake = TRUE WHERE id = 'turn:job'`)
		require.NoError(t, err)
		tick(t)
		assert.Equal(t, "new quiet parked until=done worked", attentionOf(t, service, alice, session.Path), "a background job's result waiting to wake the agent is work")

		_, err = service.db.ExecContext(ctx, `UPDATE turns SET wake = FALSE WHERE id = 'turn:job';
UPDATE conversations SET goal_objective = 'ship', goal_status = 'active' WHERE conversation_id = 'web:done'`)
		require.NoError(t, err)
		tick(t)
		assert.Equal(t, "new quiet parked until=done worked", attentionOf(t, service, alice, session.Path), "an active goal is work between its turns")

		_, err = service.db.ExecContext(ctx, `UPDATE conversations SET goal_status = 'complete' WHERE conversation_id = 'web:done'`)
		require.NoError(t, err)
		tick(t)
		assert.Equal(t, "new", attentionOf(t, service, alice, session.Path), "unparked and loud, with no note of its own: the reply speaks")

		state, revision, err := service.TabState(ctx, alice)
		require.NoError(t, err)
		assert.Equal(t, []Tab{session}, state.Tabs, "the closed tab is back with no window asking")
		assert.Equal(t, int64(3), revision)
	})

	for name, tc := range map[string]struct {
		do   func(t *testing.T, chat, path string)
		want string
	}{
		"a failed turn ends it with work queued": {do: func(t *testing.T, chat, _ string) {
			t.Helper()
			queue(t, chat)
			turn(t, chat, chat+":turn", protocol.TerminalFailed)
		}, want: "new quiet parked until=due worked"},
		"a stopped turn only records work": {do: func(t *testing.T, chat, _ string) {
			t.Helper()
			queue(t, chat)
			turn(t, chat, chat+":turn", protocol.TerminalStopped)
			tick(t)
		}, want: "new quiet parked until=done worked"},
		"a question waits for its answer, then puts the park back": {do: func(t *testing.T, chat, path string) {
			t.Helper()
			start(t, chat, chat+":turn")
			_, err := service.db.ExecContext(ctx, `INSERT INTO steps (conversation_id, key, value) VALUES ($1, $2, '{}')`, chat, chat+":turn/ask"+webQuestionStep)
			require.NoError(t, err)
			markTab(t, service.db, bob, path, question)
			tick(t)
			tick(t)
			assert.Equal(t, "new resume=done note=news", attentionOf(t, service, bob, path))

			_, err = service.db.ExecContext(ctx, `INSERT INTO steps (conversation_id, key, value) VALUES ($1, $2, '{}')`, chat, chat+":turn/ask/answer")
			require.NoError(t, err)
			tick(t)
		}, want: "new quiet parked until=done note=news"},
		"a question on a finished session ends the park it puts back": {do: func(t *testing.T, chat, path string) {
			t.Helper()
			start(t, chat, chat+":turn")
			markTab(t, service.db, bob, path, question)
			tick(t)
			end(t, chat, chat+":turn", "")
			require.NoError(t, service.closeTurn(ctx, chat+":turn"))
			tick(t)
			assert.Equal(t, "new quiet parked until=done worked note=news", attentionOf(t, service, bob, path))
			tick(t)
		}, want: "new note=news"},
		"a run reporting to the session is work": {do: func(t *testing.T, chat, _ string) {
			t.Helper()

			_, err := service.db.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, reports_to) VALUES ($1, 'job', '', 'cron_run', FALSE, $2)`, chat+":run", chat)
			require.NoError(t, err)
			turn(t, chat, chat+":turn", "")
			start(t, chat+":run", chat+":run-turn")
			tick(t)
			assert.Equal(t, "new quiet parked until=done worked", attentionOf(t, service, bob, sessionPath(chat)))
			end(t, chat+":run", chat+":run-turn", "")
			require.NoError(t, service.closeTurn(ctx, chat+":run-turn"))
			tick(t)
		}, want: "new"},
		"parking again waits for new work": {do: func(t *testing.T, chat, path string) {
			t.Helper()
			turn(t, chat, chat+":turn", "")
			require.NoError(t, service.ChangeAttention(ctx, bob, []string{path}, AttentionUnpark, nil))
			require.NoError(t, service.ChangeAttention(ctx, bob, []string{path}, AttentionParkUntilDone, nil))
			tick(t)
		}, want: "new quiet parked until=done"},
		"a failed turn after a question drops the park": {do: func(t *testing.T, chat, path string) {
			t.Helper()
			markTab(t, service.db, bob, path, question)
			tick(t)
			queue(t, chat)
			turn(t, chat, chat+":turn", protocol.TerminalFailed)
			tick(t)
		}, want: "new note=news"},
		"closing keeps a remembered park until it is done": {do: func(t *testing.T, chat, path string) {
			t.Helper()

			saved, err := service.SaveTabState(ctx, bob, &TabState{Tabs: []Tab{{Path: path, Space: "main"}}, Space: "main"}, 0)
			require.NoError(t, err)
			require.True(t, saved)
			queue(t, chat)
			markTab(t, service.db, bob, path, question)
			tick(t)

			saved, err = service.SaveTabState(ctx, bob, &TabState{Space: "main"}, 1)
			require.NoError(t, err)
			require.True(t, saved)
			tick(t)
		}, want: "new quiet parked until=done note=news"},
	} {
		t.Run(name, func(t *testing.T) {
			chat := "web:" + name
			path := sessionPath(chat)

			recordTestConversations(t, service, "main", map[string]ConversationKind{chat: KindWebChat})
			require.NoError(t, service.ChangeAttention(ctx, bob, []string{path}, AttentionParkUntilDone, nil))
			tc.do(t, chat, path)
			assert.Equal(t, tc.want, attentionOf(t, service, bob, path))
		})
	}

	for name, tc := range map[string]struct {
		until *time.Time
		do    func(t *testing.T, path string)
		want  string
	}{
		"a timed park comes back": {until: &soon, want: "new quiet parked until=soon note=news"},
		"a timed park whose time passed does not": {until: &soon, do: func(t *testing.T, path string) {
			t.Helper()
			markTab(t, service.db, carol, path, question)
			_, err := service.db.ExecContext(ctx, `UPDATE web_tab_attention SET resume_until = now() - interval '1 minute' WHERE path = $1`, path)
			require.NoError(t, err)
			tick(t)
			tick(t)
		}, want: "new note=news"},
		"a park until unparked comes back": {want: "new quiet parked note=news"},
		"closing drops a park until unparked": {do: func(t *testing.T, path string) {
			t.Helper()

			saved, err := service.SaveTabState(ctx, carol, &TabState{Tabs: []Tab{{Path: path, Space: "main"}}, Space: "main"}, 0)
			require.NoError(t, err)
			require.True(t, saved)
			markTab(t, service.db, carol, path, question)
			tick(t)

			saved, err = service.SaveTabState(ctx, carol, &TabState{Space: "main"}, 1)
			require.NoError(t, err)
			require.True(t, saved)
			tick(t)
		}, want: "new note=news"},
		"parking again replaces it": {until: &soon, do: func(t *testing.T, path string) {
			t.Helper()
			markTab(t, service.db, carol, path, question)
			tick(t)
			require.NoError(t, service.ChangeAttention(ctx, carol, []string{path}, AttentionPark, nil))
			tick(t)
		}, want: "new quiet parked note=news"},
	} {
		t.Run(name, func(t *testing.T) {
			chat := "web:" + name
			path := sessionPath(chat)

			recordTestConversations(t, service, "main", map[string]ConversationKind{chat: KindWebChat})
			require.NoError(t, service.ChangeAttention(ctx, carol, []string{path}, AttentionPark, tc.until))

			if tc.do == nil {
				tc.do = func(t *testing.T, path string) {
					t.Helper()
					markTab(t, service.db, carol, path, question)
					tick(t)
					tick(t)
				}
			}

			tc.do(t, path)
			assert.Equal(t, tc.want, attentionOf(t, service, carol, path))
		})
	}
}

func TestPausedMessageMarksOtherReaders(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	ctx := t.Context()
	rt := pausedTestChat(t, service, "web:lunch")
	path := sessionPath("web:lunch")

	for _, person := range []string{alice, bob} {
		saved, err := service.SaveTabState(ctx, person, &TabState{Tabs: []Tab{{Path: path, Space: "main"}}, Space: "main"}, 0)
		require.NoError(t, err)
		require.True(t, saved)
	}

	require.NoError(t, service.ChangeAttention(ctx, "carol@example.com", []string{path}, AttentionParkUntilDone, nil))
	require.NoError(t, rt.PostPausedMessage(ctx, pausedWebMessage("web:lunch", "Alice", &protocol.InboundContent{Text: "lunch at noon?"})))
	require.NoError(t, service.tickParks(ctx))

	assert.Equal(t, []string{
		"message:\"Bob\" paused the agent. Until it is resumed, messages here are people talking to each other, not requests to you.",
		"message:[Web principal=\"Alice\"]\n\nlunch at noon?",
	}, observedEntryTexts(t, service, "web:lunch"))
	assert.Equal(t, "new", attentionOf(t, service, bob, path))
	assert.Equal(t, "none", attentionOf(t, service, alice, path), "the sender gets no mark, and her tab is not parked")
	assert.Equal(t, "new quiet parked until=done", attentionOf(t, service, "carol@example.com", path), "people talking is not the agent being done")

	turns, err := queryStrings(ctx, service.db, `SELECT id FROM turns WHERE conversation_id = $1`, "chat turns", "web:lunch")
	require.NoError(t, err)
	assert.Empty(t, turns)
}

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
	assert.True(t, on)
}

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
	tick := func() int64 {
		t.Helper()
		require.NoError(t, service.tickParks(ctx))

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

	tx, err := service.db.BeginTx(ctx, nil)
	require.NoError(t, err)

	defer func() { _ = tx.Rollback() }()

	var holder int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT pg_backend_pid() FROM web_tab_states WHERE username = $1 FOR UPDATE`, alice).Scan(&holder))

	var ticks errgroup.Group

	for range 2 {
		ticks.Go(func() error { return service.tickParks(ctx) })
	}

	require.Eventually(t, func() bool {
		var waiting int
		require.NoError(t, service.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))`, holder).Scan(&waiting))

		return waiting > 0
	}, 5*time.Second, time.Millisecond)
	require.NoError(t, tx.Commit())
	require.NoError(t, ticks.Wait())

	state, got, err := service.TabState(ctx, alice)
	require.NoError(t, err)
	assert.Equal(t, revision+1, got, "the revision rises once")
	assert.Equal(t, []Tab{a, search, cron}, state.Tabs, "back in their space, group and position, a Search tab with its page")
	assertNoOrphanSearchPages(t, service)

	closed, err := service.ClosedTabs(ctx, alice)
	require.NoError(t, err)
	assert.Empty(t, closed)
	assert.Equal(t, "new note=Park ended", attentionOf(t, service, alice, "/search/x"))
	assert.Equal(t, "new note=Park ended", attentionOf(t, service, alice, "/cron"))
	assert.Equal(t, "new note=Park ended", attentionOf(t, service, alice, a.Path), "a park on an open tab ends in place")
	assert.Equal(t, "new note=Park ended", attentionOf(t, service, alice, "/agents"), "one on a location never a tab ends alone")
	assert.Equal(t, revision+1, tick(), "a second tick wakes nothing")

	revision++

	require.NoError(t, service.ChangeAttention(ctx, alice, []string{"/search/x"}, AttentionPark, &soon))
	save(&TabState{Tabs: []Tab{a, cron}, Groups: group, Space: "main"})
	save(&TabState{Tabs: []Tab{{Path: a.Path, Space: "main"}}, Space: "main"})
	due()
	assert.Equal(t, revision+1, tick())

	state, _, err = service.TabState(ctx, alice)
	require.NoError(t, err)
	assert.Equal(t, []Tab{{Path: a.Path, Space: "main"}, {Path: "/search/x", Space: "main", Search: page}}, state.Tabs)

	revision++

	require.NoError(t, service.ChangeAttention(ctx, alice, []string{a.Path}, AttentionPark, &soon))
	save(&TabState{Space: "main"})
	markTab(t, service.db, alice, a.Path, question)
	markTab(t, service.db, alice, "/search/x", question)
	assert.Equal(t, revision+1, tick())

	state, _, err = service.TabState(ctx, alice)
	require.NoError(t, err)
	assert.Equal(t, []Tab{{Path: a.Path, Space: "main"}}, state.Tabs)
	assert.Equal(t, "new resume=soon note=news", attentionOf(t, service, alice, a.Path), "a question remembers the park it ends")
	assert.Equal(t, "new note=news", attentionOf(t, service, alice, "/search/x"))
}
