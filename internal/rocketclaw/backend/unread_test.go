package backend

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	alice = "alice@example.com"
	bob   = "bob@example.com"
)

// webMessage is a message person sent from Web.
func webMessage(kind protocol.InboundKind, text, person string) *protocol.InboundMessage {
	msg := protocol.NewInboundMessage(protocol.SourceWeb, kind, text, true)
	msg.Metadata = map[string]string{protocol.InboundPrincipalEmailMetadataKey: person}

	return msg
}

// unreadBy lists the people who have conversationID unread.
func unreadBy(t *testing.T, s *SessionService, conversationID string) []string {
	t.Helper()

	people, err := queryStrings(t.Context(), s.db, `SELECT username FROM web_tabs WHERE conversation_id = $1 AND unread_at IS NOT NULL ORDER BY username`, "unread people", conversationID)
	require.NoError(t, err)

	return people
}

// viewedAgo records that person viewed conversationID ago.
func viewedAgo(t *testing.T, s *SessionService, person, conversationID string, ago time.Duration) {
	t.Helper()

	require.NoError(t, s.ViewSession(t.Context(), person, conversationID))
	_, err := s.db.ExecContext(t.Context(), `UPDATE web_tab_states SET viewed_at = viewed_at - make_interval(secs => $3) WHERE username = $1 AND viewing = $2`, person, conversationID, ago.Seconds())
	require.NoError(t, err)
}

// R1–R5, AE1–AE3, AE5: a turn's end marks each Web person who sent its input or an applied steer.
func TestTurnEndMarksWebSenders(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	ctx := t.Context()

	// Goal continuations and Slack messages carry the person's email too.
	goal := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "continue", false)
	goal.GoalAction, goal.Metadata = protocol.GoalActionContinue, map[string]string{protocol.InboundPrincipalEmailMetadataKey: alice}
	slack := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "ask", true)
	slack.Metadata = map[string]string{protocol.InboundPrincipalEmailMetadataKey: alice}

	finish := func(conversationID, turnID string, terminal protocol.Terminal) {
		outbound := &protocol.OutboundMessage{ConversationID: conversationID, TurnID: turnID, Text: "done", Terminal: terminal}
		_, err := service.finishTurn(ctx, turnID, &turnFinish{store: newSessionStore(conversationID, service), outbound: outbound, terminal: terminal})
		require.NoError(t, err)
	}

	require.NoError(t, service.UpsertThread("web:elsewhere", ThreadState{Agent: "main"}))

	for name, tc := range map[string]struct {
		inbound  *protocol.InboundMessage
		steers   []journaledSteer
		applied  []string
		terminal protocol.Terminal
		viewed   time.Duration // how long ago Alice last viewed the session; zero is never
		away     bool          // Alice viewed another session since, which ends her view of this one
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
			require.NoError(t, saveTestTurn(ctx, service, conversationID, turnID, tc.applied, &rocketcode.SessionEntry{Version: 1, Type: "turn", TurnID: turnID}))

			if tc.steers != nil {
				data, err := json.Marshal(tc.steers)
				require.NoError(t, err)
				require.NoError(t, service.SaveTurnStep(ctx, conversationID, turnID+"/steers", data))
			}

			if tc.viewed > 0 {
				viewedAgo(t, service, alice, conversationID, tc.viewed)
			}

			if tc.away {
				require.NoError(t, service.ViewSession(ctx, alice, "web:elsewhere"))
			}

			finish(conversationID, turnID, tc.terminal)
			assert.Equal(t, tc.want, unreadBy(t, service, conversationID))
		})
	}

	// A late finish of an ended turn marks no one again, even once Alice has looked away.
	// The mark lands on the row of the tab Alice saved, under the path the Web spells.
	require.NoError(t, service.UpsertThread("web:late", ThreadState{Agent: "main"}))
	require.NoError(t, startTurnDB(ctx, service.db, "turn:late", "web:late", webMessage(protocol.InboundKindPrompt, "ask", alice)))

	path := "/s/" + base64.RawURLEncoding.EncodeToString([]byte("web:late"))
	saved, err := service.SaveTabState(ctx, alice, &TabState{Tabs: []Tab{{Path: path}}}, 0)
	require.NoError(t, err)
	require.True(t, saved)
	finish("web:late", "turn:late", protocol.TerminalStopped)
	require.Equal(t, []string{alice}, unreadBy(t, service, "web:late"))

	paths, err := queryStrings(ctx, service.db, `SELECT format('%s %s', path, position) FROM web_tabs WHERE conversation_id = 'web:late'`, "late tabs")
	require.NoError(t, err)
	require.Equal(t, []string{path + " 0"}, paths)
	viewedAgo(t, service, alice, "web:late", 30*time.Second)
	require.Empty(t, unreadBy(t, service, "web:late"))
	finish("web:late", "turn:late", protocol.TerminalComplete)
	assert.Empty(t, unreadBy(t, service, "web:late"), "the late finish writes no second mark")
}

// R4: a message Alice queued behind Bob's turn marks her when it runs as its own turn.
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
