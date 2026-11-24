package backend

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

const (
	alice = "alice@example.com"
	bob   = "bob@example.com"
)

func webMessage(kind protocol.InboundKind, text, person string) *protocol.InboundMessage {
	msg := protocol.NewInboundMessage(protocol.SourceWeb, kind, text, true)
	msg.Metadata = map[string]string{protocol.InboundPrincipalEmailMetadataKey: person}

	return msg
}

func unreadBy(t *testing.T, s *SessionService, conversationID string) []string {
	t.Helper()

	people, err := queryStrings(t.Context(), s.db, `SELECT username FROM web_tab_attention WHERE conversation_id = $1 AND unread_at IS NOT NULL ORDER BY username`, "unread people", conversationID)
	require.NoError(t, err)

	return people
}

func attentionOn(t *testing.T, s *SessionService, conversationID string) []string {
	t.Helper()

	rows, err := queryStrings(t.Context(), s.db, `SELECT format('%s %s%s', username, CASE WHEN unread_at IS NULL THEN 'read' WHEN quiet THEN 'quiet' ELSE 'loud' END,
    CASE WHEN parked THEN ' parked' ELSE '' END) FROM web_tab_attention WHERE conversation_id = $1 ORDER BY username`, "attention", conversationID)
	require.NoError(t, err)

	return rows
}

func sessionPath(conversationID string) string {
	return "/s/" + base64.RawURLEncoding.EncodeToString([]byte(conversationID))
}

func viewedAgo(t *testing.T, s *SessionService, person, conversationID string, ago time.Duration) {
	t.Helper()

	_, _, err := s.PollAttention(t.Context(), person, sessionPath(conversationID), false)
	require.NoError(t, err)
	_, err = s.db.ExecContext(t.Context(), `UPDATE web_tab_states SET viewed_at = viewed_at - make_interval(secs => $3) WHERE username = $1 AND viewing = $2`, person, sessionPath(conversationID), ago.Seconds())
	require.NoError(t, err)
}

func TestTurnEndMarksWebSenders(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	ctx := t.Context()

	goal := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "continue", false)
	goal.GoalAction, goal.Metadata = protocol.GoalActionContinue, map[string]string{protocol.InboundPrincipalEmailMetadataKey: alice}
	slack := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "ask", true)
	slack.Metadata = map[string]string{protocol.InboundPrincipalEmailMetadataKey: alice}

	finish := func(conversationID, turnID string, terminal protocol.Terminal) {
		outbound := &protocol.OutboundMessage{ConversationID: conversationID, TurnID: turnID, Text: "done", Terminal: terminal}
		_, err := service.finishTurn(ctx, turnID, &turnFinish{conversationID: strings.TrimSpace(conversationID), outbound: outbound, terminal: terminal})
		require.NoError(t, err)
	}

	require.NoError(t, service.UpsertThread("web:elsewhere", ThreadState{Agent: "main"}))

	for name, tc := range map[string]struct {
		inbound  *protocol.InboundMessage
		steers   []journaledSteer
		applied  []string
		terminal protocol.Terminal
		viewed   time.Duration
		away     bool
		want     []string
	}{
		"finished":                    {inbound: webMessage(protocol.InboundKindPrompt, "ask", alice), want: []string{alice}},
		"failed":                      {inbound: webMessage(protocol.InboundKindPrompt, "ask", alice), terminal: protocol.TerminalFailed, want: []string{alice}},
		"stopped":                     {inbound: webMessage(protocol.InboundKindPrompt, "ask", alice), terminal: protocol.TerminalStopped, want: []string{alice}},
		"another person's turn":       {inbound: webMessage(protocol.InboundKindPrompt, "ask", bob), want: []string{bob}},
		"applied steers":              {inbound: webMessage(protocol.InboundKindPrompt, "ask", alice), steers: []journaledSteer{{ID: "s-bob", Inbound: webMessage(protocol.InboundKindSteer, "also", bob)}, {ID: "s-alice", Inbound: webMessage(protocol.InboundKindSteer, "and", alice)}}, applied: []string{"s-bob", "s-alice"}, want: []string{alice, bob}},
		"journaled steer not applied": {inbound: goal, steers: []journaledSteer{{ID: "s-bob", Inbound: webMessage(protocol.InboundKindSteer, "also", bob)}}, applied: []string{"other"}},
		"viewed 3 seconds ago":        {inbound: webMessage(protocol.InboundKindPrompt, "ask", alice), viewed: 3 * time.Second},
		"viewed 30 seconds ago":       {inbound: webMessage(protocol.InboundKindPrompt, "ask", alice), viewed: 30 * time.Second, want: []string{alice}},
		"viewed then looked away":     {inbound: webMessage(protocol.InboundKindPrompt, "ask", alice), viewed: 3 * time.Second, away: true, want: []string{alice}},
		"goal continuation":           {inbound: goal},
		"slack":                       {inbound: slack},
	} {
		t.Run(name, func(t *testing.T) {
			conversationID, turnID := "web:"+name, "turn:"+name
			require.NoError(t, service.UpsertThread(conversationID, ThreadState{Agent: "main"}))
			require.NoError(t, startTurnDB(ctx, service.db, turnID, conversationID, tc.inbound))
			require.NoError(t, saveTestTurn(ctx, service, conversationID, turnID, tc.applied, &harness.SessionEntry{Version: 1, Type: "turn", TurnID: turnID}))

			if tc.steers != nil {
				data, err := json.Marshal(tc.steers)
				require.NoError(t, err)
				require.NoError(t, service.SaveTurnStep(ctx, conversationID, turnID+"/steers", data))
			}

			if tc.viewed > 0 {
				viewedAgo(t, service, alice, conversationID, tc.viewed)
			}

			if tc.away {
				viewedAgo(t, service, alice, "web:elsewhere", 0)
			}

			finish(conversationID, turnID, tc.terminal)
			assert.Equal(t, tc.want, unreadBy(t, service, conversationID))
		})
	}

	require.NoError(t, service.UpsertThread("web:late", ThreadState{Agent: "main"}))
	require.NoError(t, startTurnDB(ctx, service.db, "turn:late", "web:late", webMessage(protocol.InboundKindPrompt, "ask", alice)))

	path := sessionPath("web:late")
	saved, err := service.SaveTabState(ctx, alice, &TabState{Tabs: []Tab{{Path: path}}}, 0)
	require.NoError(t, err)
	require.True(t, saved)
	finish("web:late", "turn:late", protocol.TerminalStopped)
	require.Equal(t, []string{alice}, unreadBy(t, service, "web:late"))

	paths, err := queryStrings(ctx, service.db, `SELECT format('%s %s', path, position) FROM web_tabs WHERE conversation_id = 'web:late'
UNION ALL SELECT path FROM web_tab_attention WHERE conversation_id = 'web:late'`, "late tabs")
	require.NoError(t, err)
	require.Equal(t, []string{path + " 0", path}, paths)
	viewedAgo(t, service, alice, "web:late", 30*time.Second)
	require.Empty(t, unreadBy(t, service, "web:late"))
	finish("web:late", "turn:late", protocol.TerminalComplete)
	assert.Empty(t, unreadBy(t, service, "web:late"), "the late finish writes no second mark")

	require.NoError(t, service.UpsertThread("web:shared", ThreadState{Agent: "main"}))
	require.NoError(t, service.ShareTab(ctx, "main", sessionPath("web:shared"), nil))
	require.NoError(t, startTurnDB(ctx, service.db, "turn:shared", "web:shared", slack))
	require.NoError(t, saveTestTurn(ctx, service, "web:shared", "turn:shared", nil, &harness.SessionEntry{Version: 1, Type: "turn", TurnID: "turn:shared"}))
	viewedAgo(t, service, alice, "web:elsewhere", 0)
	viewedAgo(t, service, bob, "web:shared", 3*time.Second)
	finish("web:shared", "turn:shared", protocol.TerminalComplete)
	assert.Equal(t, []string{alice + " loud"}, attentionOn(t, service, "web:shared"))
}

func TestTurnEndReachesKeepersAndSharedViewers(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	ctx := t.Context()

	const (
		session = "web:kept"
		erin    = "erin@example.com"
	)

	require.NoError(t, service.UpsertThread(session, ThreadState{Agent: "main"}))

	for person, state := range map[string]*TabState{
		alice: {Tabs: []Tab{{Path: sessionPath(session), Space: "main"}}},
		bob:   {},
		erin:  {Tabs: []Tab{{Path: "group:g", Group: "g", Space: "main"}}, Groups: []TabGroup{{ID: "g", Name: "Kept"}}},
	} {
		saved, err := service.SaveTabState(ctx, person, state, 0)
		require.NoError(t, err)
		require.True(t, saved)
	}

	turnEnds := func(turnID string, inbound *protocol.InboundMessage) []string {
		t.Helper()

		require.NoError(t, startTurnDB(ctx, service.db, turnID, session, inbound))
		require.NoError(t, saveTestTurn(ctx, service, session, turnID, nil, &harness.SessionEntry{Version: 1, Type: "turn", TurnID: turnID}))
		_, err := service.finishTurn(ctx, turnID, &turnFinish{conversationID: session, outbound: protocol.NewOutboundMessage(session, "done")})
		require.NoError(t, err)

		return attentionOn(t, service, session)
	}

	cron := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "report", false)
	assert.Equal(t, []string{alice + " loud"}, turnEnds("turn:cron", cron), "a cron turn reaches the keepers, not Bob or Erin")

	require.NoError(t, service.ShareTab(ctx, "main", sessionPath(session), nil))
	_, err := service.db.ExecContext(ctx, `DELETE FROM web_tab_attention`)
	require.NoError(t, err)
	assert.Equal(t, []string{alice + " loud", bob + " loud", erin + " loud"},
		turnEnds("turn:bob", webMessage(protocol.InboundKindPrompt, "ask", bob)))

	_, err = service.db.ExecContext(ctx, `DELETE FROM web_tab_attention`)
	require.NoError(t, err)
	require.NoError(t, service.ChangeAttention(ctx, alice, []string{sessionPath(session)}, AttentionPark, nil, 0))
	assert.Equal(t, []string{alice + " quiet parked", bob + " loud", erin + " loud"},
		turnEnds("turn:alice", webMessage(protocol.InboundKindPrompt, "ask", alice)))

	require.NoError(t, service.ChangeAttention(ctx, alice, []string{sessionPath(session)}, AttentionUnpark, nil, 0))
	assert.Equal(t, []string{alice + " quiet", bob + " loud", erin + " loud"}, attentionOn(t, service, session))
}

func TestMarkUnread(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	ctx := t.Context()
	marks := func(conversationID string) []string {
		t.Helper()

		got, err := queryStrings(ctx, service.db, `SELECT format('%s quiet=%s', username, quiet) FROM web_tab_attention WHERE conversation_id = $1 AND unread_at IS NOT NULL ORDER BY username`, "marks", conversationID)
		require.NoError(t, err)

		return got
	}
	markUnread := func(conversationIDs ...string) {
		t.Helper()

		paths := make([]string, 0, len(conversationIDs))
		for _, id := range conversationIDs {
			paths = append(paths, sessionPath(id))
		}

		require.NoError(t, service.ChangeAttention(ctx, alice, paths, AttentionUnread, nil, 0))
	}
	heldView := func(conversationID string) {
		t.Helper()

		_, _, err := service.PollAttention(ctx, alice, sessionPath(conversationID), true)
		require.NoError(t, err)
	}
	turnEnds := func(conversationID, turnID string) {
		t.Helper()
		require.NoError(t, startTurnDB(ctx, service.db, turnID, conversationID, webMessage(protocol.InboundKindPrompt, "ask", alice)))
		require.NoError(t, saveTestTurn(ctx, service, conversationID, turnID, nil, &harness.SessionEntry{Version: 1, Type: "turn", TurnID: turnID}))
		_, err := service.finishTurn(ctx, turnID, &turnFinish{conversationID: conversationID, outbound: &protocol.OutboundMessage{ConversationID: conversationID, TurnID: turnID, Text: "done"}})
		require.NoError(t, err)
	}

	turnMark := func() []string {
		t.Helper()

		got, err := queryStrings(ctx, service.db, `SELECT unread_at::text FROM web_tab_attention WHERE conversation_id = 'web:turn'`, "turn mark")
		require.NoError(t, err)

		return got
	}

	for _, id := range []string{"web:shared", "web:turn", "web:held"} {
		require.NoError(t, service.UpsertThread(id, ThreadState{Agent: "main"}))
	}

	viewedAgo(t, service, bob, "web:turn", 0)
	require.NoError(t, service.ShareTab(ctx, "main", sessionPath("web:shared"), nil))
	turnEnds("web:turn", "turn:turn")

	before := turnMark()

	markUnread("web:shared", "web:turn")
	assert.Equal(t, []string{alice + " quiet=t"}, marks("web:shared"), "a Shared session's mark is alice's alone")
	assert.Equal(t, before, turnMark(), "a turn's mark keeps its time")
	assert.Equal(t, []string{alice + " quiet=f"}, marks("web:turn"))

	markUnread("web:held")
	heldView("web:held")
	turnEnds("web:held", "turn:held")
	assert.Equal(t, []string{alice + " quiet=t"}, marks("web:held"), "a held view keeps the mark, and a turn ending in view marks nothing")

	_, err := service.db.ExecContext(ctx, `UPDATE web_tab_states SET viewed_at = viewed_at - interval '30 seconds' WHERE username = $1`, alice)
	require.NoError(t, err)
	turnEnds("web:held", "turn:held-later")
	assert.Equal(t, []string{alice + " quiet=f"}, marks("web:held"), "a turn ending once the view went stale leaves a turn's mark")
	heldView("web:held")
	assert.Empty(t, marks("web:held"), "a held view clears a turn's mark")

	markUnread("web:held")
	assert.Equal(t, []string{alice + " quiet=t"}, marks("web:held"), "marking a read tab that has a row makes the mark alice's own")
}

func TestTabViewLocksStateBeforeAttention(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	ctx := t.Context()

	const conversationID = "web:view-locks"

	path := sessionPath(conversationID)
	require.NoError(t, service.UpsertThread(conversationID, ThreadState{Agent: "main"}))
	require.NoError(t, service.ChangeAttention(ctx, alice, []string{path}, AttentionUnread, nil, 0))

	tx, err := service.db.BeginTx(ctx, nil)
	require.NoError(t, err)

	defer func() { _ = tx.Rollback() }()

	var holder int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT pg_backend_pid() FROM web_tab_states WHERE username = $1 FOR UPDATE`, alice).Scan(&holder))

	var view errgroup.Group
	view.Go(func() error {
		_, _, err := service.PollAttention(ctx, alice, path, false)
		return err
	})

	defer func() { _ = tx.Rollback(); require.NoError(t, view.Wait()) }()

	require.Eventually(t, func() bool {
		var blocked bool
		require.NoError(t, service.db.QueryRowContext(ctx, `SELECT count(*) > 0 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))`, holder).Scan(&blocked))

		return blocked
	}, 5*time.Second, time.Millisecond, "view waits for the state lock")

	var locked string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT path FROM web_tab_attention WHERE username = $1 AND path = $2 FOR UPDATE NOWAIT`, alice, path).Scan(&locked))
	require.Equal(t, path, locked)
	require.NoError(t, tx.Commit())
	require.NoError(t, view.Wait())
	require.Empty(t, unreadBy(t, service, conversationID))
}

func TestQueuedWebMessageMarksItsSender(t *testing.T) {
	t.Parallel()

	newBridge, service, _, finals := newResumeTestBridges(t, false)
	bridge := newBridge()
	conversationID := bridge.config.ConversationID
	queued := webMessage(protocol.InboundKindEnqueue, "queued work", alice)
	require.NoError(t, service.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ID: "q1", ConversationID: conversationID, Kind: protocol.InboundKindEnqueue, Message: queued.Text, Principal: "alice", Source: protocol.SourceWeb, Inbound: queued}))
	require.NoError(t, bridge.Submit(t.Context(), webMessage(protocol.InboundKindPrompt, "first", bob)))

	runTestBridge(t, bridge)
	assert.Equal(t, "answer", readFinal(t, finals).Text)
	assert.Equal(t, "answer", readFinal(t, finals).Text)
	require.Eventually(t, func() bool { return len(runningTestTurns(t, service)) == 0 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{alice, bob}, unreadBy(t, service, conversationID))
}
