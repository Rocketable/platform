package backend

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/stretchr/testify/require"
)

func branchTestRuntime(s *SessionService) *Runtime {
	b := &Bridge{config: Config{ConversationID: "chat", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	manager := newThreadBridgeManager(nil, s, b.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return b }})
	manager.bridges["chat"] = b

	return &Runtime{Sessions: s, threads: manager}
}

func branchTestRows(t *testing.T, s *SessionService) [4]int64 {
	t.Helper()

	var rows [4]int64
	require.NoError(t, s.db.QueryRowContext(t.Context(), `WITH e AS (SELECT count(*) AS n FROM entries), l AS (SELECT count(*) AS n FROM history_links),
    t AS (SELECT count(*) AS n FROM turns), c AS (SELECT count(*) AS n FROM conversations)
SELECT e.n, l.n, t.n, c.n FROM e, l, t, c`).Scan(&rows[0], &rows[1], &rows[2], &rows[3]))

	return rows
}

func moveBefore(t *testing.T, s *SessionService, conversationID string, entry int64, index int) {
	t.Helper()

	_, _, err := stageRevertDB(t.Context(), s.db, conversationID, fmt.Sprintf("%d:%d", entry, index), protocol.RevertBefore)
	require.NoError(t, err)
}

func TestDeleteCurrentBranchStopsItsTurnAndRunsTheQueueFromTheBranchPoint(t *testing.T) {
	t.Parallel()

	newBridge, s, requests, _ := newResumeTestBridges(t, true)
	b := newBridge()
	b.config.ConversationID = "main"
	ctx := t.Context()
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))

	for _, text := range []string{"A text", "B text"} {
		_, err := s.AppendEntryID(ctx, "main", testSessionEntry(text, "reply"))
		require.NoError(t, err)
	}

	c, err := s.AppendEntryID(ctx, "main", testSessionEntry("C text", "c reply"))
	require.NoError(t, err)
	moveBefore(t, s, "main", c, 0)

	require.NoError(t, s.startSubagent(ctx, "main", &harness.SubagentWrite{Key: harness.ChildKey{Path: "/call-x", Turn: "turn-c2", Call: "call-x"}, Agent: "helper"}))
	_, err = s.AppendEntryID(ctx, "main/call-x", testSessionEntry("work", "done"))
	require.NoError(t, err)

	entry := testReplayEntry(`{"type":"message","role":"user","content":"C prime"}`, `{"type":"function_call","call_id":"call-x","name":"task","arguments":"{}"}`, `{"type":"message","role":"assistant","content":"c prime reply"}`)
	entry.TurnID = "turn-c2"
	cPrime, err := s.AppendEntryID(ctx, "main", entry)
	require.NoError(t, err)

	manager := newThreadBridgeManager(b.runtime, s, b.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return b }})
	manager.bridges["main"] = b
	rt := &Runtime{Sessions: s, threads: manager, Cfg: b.runtime}
	runTestBridge(t, b)

	active := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "running on the branch"}, true)
	active.ConversationID, active.Metadata["web_message_id"] = "main", "active"
	done := make(chan error, 1)

	go func() { done <- rt.RunTurn(ctx, active) }()

	require.Contains(t, <-requests, "C prime")

	for i, id := range []string{"queued-1", "queued-2"} {
		require.NoError(t, s.PutThreadQueueItem(id, &protocol.ThreadQueueItem{ID: id, ConversationID: "main", Message: id, Source: protocol.SourceWeb, Position: i}))
	}

	require.NoError(t, s.PutThreadQueueItem("stashed", &protocol.ThreadQueueItem{ID: "stashed", ConversationID: "main", Message: "for later", Source: protocol.SourceWeb, Kind: protocol.InboundKindHeld, Position: 2}))

	for _, dryRun := range []bool{true, false} {
		deletion, err := rt.DeleteBranch(ctx, "main", fmt.Sprintf("%d:0", cPrime), dryRun)
		require.NoError(t, err)
		require.Equal(t, protocol.BranchDeletion{Messages: 2, SubagentSessions: 1, QueuedMessages: 2, Current: true}, deletion)
	}

	require.NoError(t, <-done)

	var ran string
	for !strings.Contains(ran, "queued-1") || !strings.Contains(ran, "queued-2") {
		select {
		case body := <-requests:
			require.Contains(t, body, "B text")
			require.NotContains(t, body, "C prime", "the queue runs from the branch point")

			ran += body
		case <-time.After(10 * time.Second):
			t.Fatal("the queued messages did not run after the delete")
		}
	}

	left, err := queryStrings(ctx, s.db, `SELECT id::text FROM entries WHERE id = $1 UNION ALL SELECT conversation_id FROM conversations WHERE conversation_id = 'main/call-x'
UNION ALL SELECT id FROM turns WHERE conversation_id = 'main' AND history_anchor_id = $1`, "deleted branch", cPrime)
	require.NoError(t, err)
	require.Empty(t, left, "the branch, its subagent session and its interrupted turn are gone")

	for _, link := range chatHistoryLinks(t, s, "main") {
		require.NotEqual(t, cPrime, link.parent, "nothing continues from the deleted branch")
	}

	require.Subset(t, observedEntryTexts(t, s, "main"), []string{"message:A text", "message:B text"})

	var sibling int
	require.NoError(t, queryScalar(ctx, s.db, "sibling", `SELECT count(*) FROM entries WHERE id = $1`, &sibling, c))
	require.Equal(t, 1, sibling, "the sibling branch stays")
}

func TestDeleteSiblingBranchKeepsSteeringTheRunningTurn(t *testing.T) {
	t.Parallel()

	newBridge, s, requests, _ := newResumeTestBridges(t, true)
	b := newBridge()
	b.config.ConversationID = "main"
	ctx := t.Context()
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))

	_, err := s.AppendEntryID(ctx, "main", testSessionEntry("A text", "a reply"))
	require.NoError(t, err)
	c, err := s.AppendEntryID(ctx, "main", testSessionEntry("C text", "c reply"))
	require.NoError(t, err)
	moveBefore(t, s, "main", c, 0)
	_, err = s.AppendEntryID(ctx, "main", testSessionEntry("C prime", "c prime reply"))
	require.NoError(t, err)

	manager := newThreadBridgeManager(b.runtime, s, b.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return b }})
	manager.bridges["main"] = b
	rt := &Runtime{Sessions: s, threads: manager, Cfg: b.runtime}
	runTestBridge(t, b)

	active := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "running on the current path"}, true)
	active.ConversationID, active.Metadata["web_message_id"] = "main", "active"

	go func() { _ = rt.RunTurn(ctx, active) }()

	require.Contains(t, <-requests, "C prime")

	deletion, err := rt.DeleteBranch(ctx, "main", fmt.Sprintf("%d:0", c), false)
	require.NoError(t, err)
	require.Equal(t, protocol.BranchDeletion{Messages: 2}, deletion)

	steer := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "steer"}, true)
	steer.Metadata["web_message_id"] = "steer"
	require.NoError(t, b.Submit(ctx, steer))

	b.mu.Lock()
	defer b.mu.Unlock()

	require.True(t, slices.ContainsFunc(b.steers, func(request bridgeRequest) bool { return request.queueItemID == "steer" }), "the running turn still takes steering")
}

func TestDeleteBranchRemovesItsComparisons(t *testing.T) {
	t.Parallel()

	s, _ := newShadowTestStore(t)
	ctx := t.Context()
	require.NoError(t, s.UpsertThread("chat", ThreadState{Agent: "primary"}))
	children, err := queryStrings(ctx, s.db, `SELECT conversation_id FROM conversations WHERE shadow_parent = 'chat' ORDER BY 1`, "shadow children")
	require.NoError(t, err)
	require.Len(t, children, 2)

	rt := branchTestRuntime(s)
	compare := func(turn string) int64 {
		entry := testSessionEntry(turn, turn+" answer")
		entry.TurnID = turn
		id, err := s.AppendEntryID(ctx, "chat", entry)
		require.NoError(t, err)

		for _, child := range children {
			entry := testSessionEntry(turn, child+" compares "+turn)
			entry.TurnID = child + "-" + turn
			_, err := s.AppendEntryID(ctx, child, entry)
			require.NoError(t, err)
			_, err = s.db.ExecContext(ctx, `INSERT INTO shadow_turns (child_conversation_id, parent_source_conversation_id, parent_turn_id, child_turn_id, inbound_json, terminal)
VALUES ($1, 'chat', $2, $3, '{}', 'complete')`, child, turn, entry.TurnID)
			require.NoError(t, err)
		}

		return id
	}

	compare("a")
	c := compare("c")
	moveBefore(t, s, "chat", c, 0)
	compare("c2")

	deletion, err := rt.DeleteBranch(ctx, "chat", fmt.Sprintf("%d:0", c), false)
	require.NoError(t, err)
	require.Equal(t, protocol.BranchDeletion{Messages: 2}, deletion)

	bindings, err := shadowHistoryDB(ctx, s.db, "chat", "", 0, 0)
	require.NoError(t, err)
	require.Len(t, bindings, len(children))

	for _, binding := range bindings {
		turns := make([]string, 0, len(binding.Turns))
		for _, turn := range binding.Turns {
			turns = append(turns, turn.ParentTurnID)
		}

		require.Equal(t, []string{"a", "c2"}, turns, "the deleted branch's comparison does not come back")
		require.Equal(t, []string{"message:a", "message:" + binding.ChildConversationID + " compares a", "message:c2", "message:" + binding.ChildConversationID + " compares c2"},
			observedEntryTexts(t, s, binding.ChildConversationID))
	}

	var rows [3]int
	require.NoError(t, s.db.QueryRowContext(ctx, `WITH e AS (SELECT count(*) AS n FROM entries WHERE conversation_id = ANY($1)), l AS (SELECT count(*) AS n FROM history_links WHERE conversation_id = ANY($1)),
    s AS (SELECT count(*) AS n FROM shadow_turns WHERE parent_turn_id = 'c')
SELECT e.n, l.n, s.n FROM e, l, s`, children).Scan(&rows[0], &rows[1], &rows[2]))
	require.Equal(t, [3]int{4, 4, 0}, rows, "c's comparison answers, their links and their records go; a's and c2's stay")
}

func TestDeleteBranchCountsAndRemovesNestedBranches(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	recordTestConversations(t, s, "main", map[string]ConversationKind{"chat": KindWebChat})
	rt := branchTestRuntime(s)

	appendEntry := func(conversationID string, entry *harness.SessionEntry) int64 {
		id, err := s.AppendEntryID(ctx, conversationID, entry)
		require.NoError(t, err)

		return id
	}
	stopTurn := func(id string) {
		require.NoError(t, upsertTestTurn(ctx, s, &testCheckpoint{TurnID: id, ConversationKey: "chat", ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"` + id + `"}`)}}))
		require.NoError(t, terminateTestTurn(ctx, s, id, protocol.TerminalStopped))
	}

	appendEntry("chat", testSessionEntry("A", "a"))
	c := appendEntry("chat", testSessionEntry("C", "c"))
	d := appendEntry("chat", testSessionEntry("D", "d"))
	moveBefore(t, s, "chat", d, 0)
	require.NoError(t, s.startSubagent(ctx, "chat", &harness.SubagentWrite{Key: harness.ChildKey{Path: "/call-d", Turn: "d2", Call: "call-d"}, Agent: "helper"}))
	appendEntry("chat/call-d", testSessionEntry("work", "done"))
	appendEntry("chat", testReplayEntry(`{"type":"message","role":"user","content":"D'"}`, `{"type":"function_call","call_id":"call-d","name":"task","arguments":"{}"}`, `{"type":"message","role":"assistant","content":"d'"}`))
	stopTurn("stopped-on-d-prime")

	registry, notes := newTestBackgroundRegistry(s)
	rt.background = registry
	started := make(chan struct{}, 1)
	_, err := backgroundTurn{registry: registry, root: testTurnRoot(t), conversationID: "chat", origin: &protocol.InboundMessage{}}.Run(ctx,
		&harness.BackgroundJob{ID: "d2/call/call-d", Kind: harness.BackgroundKindTask, CallID: "call-d", SubagentKey: "/call-d", Detached: true}, blockedWork(started, make(chan struct{})))
	require.NoError(t, err)
	<-started

	moveBefore(t, s, "chat", c, 0)
	stopTurn("stopped-after-c-began")
	appendEntry("chat", testSessionEntry("C'", "c'"))
	stopTurn("stopped-on-c-prime")

	path, rows := observedEntryTexts(t, s, "chat"), branchTestRows(t, s)
	marker, _, _, err := s.RevertState(ctx, "chat")
	require.NoError(t, err)

	want := protocol.BranchDeletion{Messages: 6, NestedBranches: 1, SubagentSessions: 1}
	dry, err := rt.DeleteBranch(ctx, "chat", fmt.Sprintf("%d:0", c), true)
	require.NoError(t, err)
	require.Equal(t, want, dry)
	require.Equal(t, rows, branchTestRows(t, s), "a dry run deletes nothing")

	deleted, err := rt.DeleteBranch(ctx, "chat", fmt.Sprintf("%d:0", c), false)
	require.NoError(t, err)
	require.Equal(t, want, deleted)
	require.Equal(t, [4]int64{rows[0] - 4, rows[1] - 4, rows[2] - 2, rows[3] - 1}, branchTestRows(t, s), "C, D, D', the subagent session, its stopped job and the stopped turn D' shows go")
	require.Empty(t, notes, "the stopped subagent leaves no completion note")
	require.Equal(t, path, observedEntryTexts(t, s, "chat"))

	kept, err := queryStrings(ctx, s.db, `SELECT id FROM turns WHERE conversation_id = 'chat'`, "kept turns")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"stopped-after-c-began", "stopped-on-c-prime"}, kept, "a stopped turn anchored at the branch point shows on the kept path, so it stays")

	current, _, _, err := s.RevertState(ctx, "chat")
	require.NoError(t, err)
	require.Equal(t, marker, current)
}

func TestDeleteBranchThatStartsInsideAnEntryClipsIt(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	recordTestConversations(t, s, "main", map[string]ConversationKind{"chat": KindWebChat})
	rt := branchTestRuntime(s)

	split, err := s.AppendEntryID(ctx, "chat", testReplayEntry(`{"type":"message","role":"user","content":"one"}`, `{"type":"function_call","call_id":"call-1","name":"look","arguments":"{}"}`,
		`{"type":"function_call_output","call_id":"call-1","output":"seen"}`, `{"type":"message","role":"user","content":"two needle"}`, `{"type":"message","role":"assistant","content":"second answer"}`))
	require.NoError(t, err)
	after, err := s.AppendEntryID(ctx, "chat", testSessionEntry("after", "after reply"))
	require.NoError(t, err)
	moveBefore(t, s, "chat", split, 3)
	sibling, err := s.AppendEntryID(ctx, "chat", testSessionEntry("other way", "other reply"))
	require.NoError(t, err)

	siblingPath := observedEntryTexts(t, s, "chat")
	_, _, err = stageRevertDB(ctx, s.db, "chat", fmt.Sprintf("%d:1", after), protocol.RevertThrough)
	require.NoError(t, err)

	deletion, err := rt.DeleteBranch(ctx, "chat", fmt.Sprintf("%d:3", split), false)
	require.NoError(t, err)
	require.Equal(t, protocol.BranchDeletion{Messages: 4, Current: true}, deletion)
	require.Equal(t, []string{"message:one", "function_call:", "function_call_output:"}, observedEntryTexts(t, s, "chat"), "the current point moved to the branch point after the tool output")
	require.Equal(t, []historyLink{{split, 0, -1}, {sibling, split, 3}}, chatHistoryLinks(t, s, "chat"))

	var kept int
	require.NoError(t, queryScalar(ctx, s.db, "clipped entry", `SELECT json_array_length(entry_json->'replay_input') FROM entries WHERE id = $1`, &kept, split))
	require.Equal(t, 3, kept, "the entry keeps only the shared prefix")

	_, _, err = stageRevertDB(ctx, s.db, "chat", fmt.Sprintf("%d:1", sibling), protocol.RevertThrough)
	require.NoError(t, err)
	require.Equal(t, siblingPath, observedEntryTexts(t, s, "chat"), "the sibling path is unchanged")

	hits, err := s.SearchMessages(ctx, "needle", nil)
	require.NoError(t, err)
	require.Empty(t, hits, "the clipped text leaves search")
}

func TestDeleteSiblingBranchKeepsTheCurrentPoint(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	recordTestConversations(t, s, "main", map[string]ConversationKind{"chat": KindWebChat})
	rt := branchTestRuntime(s)

	a, err := s.AppendEntryID(ctx, "chat", testSessionEntry("A", "a"))
	require.NoError(t, err)
	c, err := s.AppendEntryID(ctx, "chat", testSessionEntry("C", "c"))
	require.NoError(t, err)
	moveBefore(t, s, "chat", c, 0)
	sibling, err := s.AppendEntryID(ctx, "chat", testSessionEntry("C'", "c'"))
	require.NoError(t, err)
	marker, _, err := stageRevertDB(ctx, s.db, "chat", fmt.Sprintf("%d:1", c), protocol.RevertThrough)
	require.NoError(t, err)

	deletion, err := rt.DeleteBranch(ctx, "chat", fmt.Sprintf("%d:0", sibling), false)
	require.NoError(t, err)
	require.Equal(t, protocol.BranchDeletion{Messages: 2}, deletion)

	current, _, _, err := s.RevertState(ctx, "chat")
	require.NoError(t, err)
	require.Equal(t, marker, current)
	require.Equal(t, []string{"message:A", "message:a", "message:C", "message:c"}, observedEntryTexts(t, s, "chat"))

	rows := branchTestRows(t, s)

	for message, errWant := range map[string]error{
		fmt.Sprintf("%d:0", c): errNoBranchPoint, fmt.Sprintf("%d:0", a): errNoBranchPoint, fmt.Sprintf("%d:1", a): errNoBranchPoint,
		fmt.Sprintf("%d:0", sibling): ErrRevertMessageGone, fmt.Sprintf("%d:2", c): ErrRevertMessageGone, "bad": ErrInvalidRevertMessage,
	} {
		_, err := rt.DeleteBranch(ctx, "chat", message, false)
		require.ErrorIs(t, err, errWant, message)
	}

	require.Equal(t, rows, branchTestRows(t, s), "a refused delete removes nothing")
}

func TestDeleteBranchHoldingARoutineReportIsRefused(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	recordTestConversations(t, s, "main", map[string]ConversationKind{"chat": KindWebChat, "run": KindExternalMCPRun})
	rt := branchTestRuntime(s)

	_, err := s.AppendEntryID(ctx, "chat", testSessionEntry("A", "a"))
	require.NoError(t, err)
	b, err := s.AppendEntryID(ctx, "chat", testSessionEntry("B", "b"))
	require.NoError(t, err)
	moveBefore(t, s, "chat", b, 0)
	branch, err := s.AppendEntryID(ctx, "chat", testSessionEntry("B'", "b'"))
	require.NoError(t, err)
	finishTestRunTurn(t, s, "run", "chat", "run-1", testSessionEntry("report", "done"))

	rows, path := branchTestRows(t, s), observedEntryTexts(t, s, "chat")
	_, err = rt.DeleteBranch(ctx, "chat", fmt.Sprintf("%d:0", branch), false)
	require.ErrorContains(t, err, "past a message from a routine run")
	require.Equal(t, rows, branchTestRows(t, s), "nothing is deleted")
	require.Equal(t, path, observedEntryTexts(t, s, "chat"))
}

func TestBranchSummaryLandsOnTheCurrentPathAndReachesTheNextTurn(t *testing.T) {
	t.Parallel()

	newBridge, s, requests, _ := newResumeTestBridges(t, false)
	b := newBridge()
	b.config.ConversationID = "main"
	ctx := t.Context()
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))

	appendEntry := func(user, assistant string) int64 {
		id, err := s.AppendEntryID(ctx, "main", testSessionEntry(user, assistant))
		require.NoError(t, err)

		return id
	}

	appendEntry("A text", "a reply")
	c := appendEntry("C text", "c reply")
	d := appendEntry("D text", "d reply")
	moveBefore(t, s, "main", d, 0)
	appendEntry("D newer", "d newer reply")
	moveBefore(t, s, "main", c, 0)
	appendEntry("C prime", "c prime reply")

	branch := fmt.Sprintf("%d:0", c)
	entries, err := s.BranchEntries(ctx, "main", branch)
	require.NoError(t, err)

	var texts []string

	for i := range entries {
		for _, raw := range entries[i].Entry.ReplayInput {
			var item struct{ Type, Content string }
			require.NoError(t, json.Unmarshal(raw, &item))
			texts = append(texts, item.Type+":"+item.Content)
		}
	}

	require.Equal(t, []string{"message:C text", "message:c reply", "message:D newer", "message:d newer reply"}, texts, "the branch runs from its split point along its newest leaf")

	for _, gone := range []string{"9:0", fmt.Sprintf("%d:2", c)} {
		_, err := s.BranchEntries(ctx, "main", gone)
		require.ErrorIs(t, err, ErrRevertMessageGone, gone)
	}

	manager := newThreadBridgeManager(b.runtime, s, b.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return b }})
	manager.bridges["main"] = b
	rt := &Runtime{Sessions: s, threads: manager, Cfg: b.runtime}
	runTestBridge(t, b)

	appended, err := s.AppendBranchSummary(ctx, "main", branch, "C summary")
	require.NoError(t, err)
	require.True(t, appended)

	const summary = "message:[System]\n\nSummary of another branch of this chat:\n\nC summary"
	require.Equal(t, []string{"message:A text", "message:a reply", "message:C prime", "message:c prime reply", summary}, observedEntryTexts(t, s, "main"))

	view, err := s.ObserveHistory(ctx, "main", "", 0, 0, 0, &HistoryInventory{})
	require.NoError(t, err)
	require.Empty(t, view.Floor, "the summary does not move the floor")

	next := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "next request"}, true)
	next.ConversationID, next.Metadata["web_message_id"] = "main", "next"
	require.NoError(t, rt.RunTurn(ctx, next))
	require.Contains(t, <-requests, "C summary", "the agent sees the summary from its next turn")

	_, _, err = rt.StageRevert(ctx, "main", fmt.Sprintf("%d:1", c), protocol.RevertThrough)
	require.NoError(t, err)
	require.Empty(t, requests, "moving never asks the model for a summary")

	path := observedEntryTexts(t, s, "main")
	appended, err = s.AppendBranchSummary(ctx, "main", branch, "late summary")
	require.NoError(t, err)
	require.False(t, appended, "the current path now holds the summarized branch")
	require.Equal(t, path, observedEntryTexts(t, s, "main"))
}

func TestBranchSummaryWaitsForTheRunningTurn(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	recordTestConversations(t, s, "main", map[string]ConversationKind{"chat": KindWebChat})

	_, err := s.AppendEntryID(ctx, "chat", testSessionEntry("A", "a"))
	require.NoError(t, err)
	c, err := s.AppendEntryID(ctx, "chat", testSessionEntry("C", "c"))
	require.NoError(t, err)
	moveBefore(t, s, "chat", c, 0)
	_, err = s.AppendEntryID(ctx, "chat", testSessionEntry("C'", "c'"))
	require.NoError(t, err)
	require.NoError(t, startTurnDB(ctx, s.db, "running", "chat", protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "next", true)))

	path, branch := observedEntryTexts(t, s, "chat"), fmt.Sprintf("%d:0", c)
	_, err = s.BranchEntries(ctx, "chat", branch)
	require.ErrorIs(t, err, errHistoryBusy, "no summary starts while a turn runs")

	appended, err := s.AppendBranchSummary(ctx, "chat", branch, "C summary")
	require.ErrorIs(t, err, errHistoryBusy, "a summary that finishes while a turn runs is not added")
	require.False(t, appended)
	require.Equal(t, path, observedEntryTexts(t, s, "chat"))
}
