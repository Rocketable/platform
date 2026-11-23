package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestRevertQueueAdmissionAndRetry(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
	id, err := s.AppendEntryID(ctx, "main", testSessionEntry("abandoned", "answer"))
	require.NoError(t, err)
	require.NoError(t, s.PutThreadQueueItem("stashed-before", &protocol.ThreadQueueItem{ID: "stashed-before", ConversationID: "main", Source: protocol.SourceWeb, Principal: "alice", Kind: protocol.InboundKindHeld, Message: "for later"}))

	marker := fmt.Sprintf("%d:0", id)
	_, _, err = stageRevertDB(ctx, s.db, "main", marker, protocol.RevertBefore)
	require.NoError(t, err)

	bridge := &Bridge{config: Config{ConversationID: "main", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	manager := newThreadBridgeManager(nil, s, bridge.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return bridge }})
	rt := &Runtime{Sessions: s, threads: manager}
	item := protocol.ThreadQueueItem{ID: "replacement", Source: protocol.SourceWeb, Principal: "alice", Kind: protocol.InboundKindHeld, Message: "edited"}
	require.NoError(t, rt.StashQueueItem(ctx, "main", &item))
	current, _, _, err := s.RevertState(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, marker, current, "admission leaves the current point where the move put it")
	require.NoError(t, rt.StashQueueItem(ctx, "main", &item))

	queue, err := s.ThreadQueueForConversation("main")
	require.NoError(t, err)
	require.Len(t, queue, 2, "rolling back keeps earlier stashes")
	require.Equal(t, "stashed-before", queue[0].ID)

	item.Message = "conflicting"
	require.ErrorContains(t, rt.StashQueueItem(ctx, "main", &item), "different content")

	bridge.historyMutation = true
	item.ID = "racing"
	require.ErrorContains(t, rt.StashQueueItem(ctx, "main", &item), "busy")

	bridge.historyMutation = false
	bridge.waiting = []bridgeRequest{{queueItemID: "replacement", completion: &turnCompletion{done: make(chan struct{})}}}
	_, err = rt.DeleteQueueItem(context.Background(), "main", "replacement")
	require.NoError(t, err)
}

func TestRevertSettlesAndPreservesWaitingWork(t *testing.T) {
	t.Parallel()

	newBridge, s, requests, finals := newResumeTestBridges(t, true)
	b := newBridge()
	b.config.ConversationID = "main"

	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "primary"}))
	id, err := s.AppendEntryID(t.Context(), "main", testSessionEntry("replace", "abandoned"))
	require.NoError(t, err)

	manager := newThreadBridgeManager(b.runtime, s, b.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return b }})
	manager.bridges["main"] = b
	rt := &Runtime{Sessions: s, threads: manager, Cfg: b.runtime}
	runTestBridge(t, b)

	active := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "running"}, true)
	active.ConversationID, active.Metadata["web_message_id"], active.Metadata[protocol.InboundPrincipalMetadataKey] = "main", "active", "alice"

	done := make(chan error, 1)
	go func() { done <- rt.RunTurn(t.Context(), active) }()

	<-requests
	require.NoError(t, s.BeginGoal("main", &GoalState{Objective: "continue forever", MaxTurns: 0}))

	for _, id := range []string{"queued-1", "queued-2"} {
		require.NoError(t, s.PutThreadQueueItem(id, &protocol.ThreadQueueItem{ID: id, ConversationID: "main", Message: id, Source: protocol.SourceWeb, Position: 10}))
	}

	steer := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "waiting steer"}, true)
	steer.ConversationID, steer.Metadata["web_message_id"], steer.Metadata[protocol.InboundPrincipalMetadataKey] = "main", "steer", "alice"
	fresh, err := rt.admitWeb(t.Context(), b, steer, protocol.GoalRequest{})
	require.NoError(t, err)
	require.True(t, fresh)

	completion := &turnCompletion{done: make(chan struct{})}
	require.NoError(t, b.enqueue(t.Context(), &bridgeRequest{inbound: steer, queueItemID: "steer", completion: completion}, "test steer"))

	channel := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindEnqueue, &protocol.InboundContent{Text: "waiting channel"}, true)
	channel.ConversationID, channel.Metadata["web_message_id"] = "main", "channel"
	channelDone := &turnCompletion{done: make(chan struct{})}
	channelRequest := bridgeRequest{inbound: channel, completion: channelDone}
	fresh, err = rt.admitWeb(t.Context(), b, channel, protocol.GoalRequest{})
	require.NoError(t, err)
	require.True(t, fresh)

	channelRequest.queueItemID = "channel"
	require.NoError(t, b.enqueue(t.Context(), &channelRequest, "waiting channel"))
	marker, prompt, err := rt.StageRevert(t.Context(), "main", fmt.Sprintf("%d:0", id), protocol.RevertBefore)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%d:0", id), marker)
	require.Equal(t, "replace", prompt)
	require.NoError(t, <-done)
	require.Equal(t, "main", readFinal(t, finals).ConversationID)

	goal, found, err := s.Goal("main")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, GoalStatusStopped, goal.Status, "a move stops the goal")
	readFinal(t, finals)
	readFinal(t, finals)

	steered := <-requests
	require.Contains(t, steered, "waiting steer", "an unconsumed steer keeps its place ahead of later work")
	require.NotContains(t, steered, "abandoned", "queued work runs from the new current point")
	require.Contains(t, <-requests, "waiting channel")

	select {
	case <-completion.done:
		require.NoError(t, completion.err)
	case <-time.After(10 * time.Second):
		t.Fatal("preserved steer completion did not return after the move")
	}

	<-channelDone.done
	require.NoError(t, channelDone.err)
}

func TestRevertWaitsForDeliverySettlement(t *testing.T) {
	t.Parallel()

	for _, errDelivery := range []error{nil, errors.New("delivery failed")} {
		t.Run(fmt.Sprint(errDelivery), func(t *testing.T) {
			newBridge, s, _, _ := newResumeTestBridges(t, false)
			b := newBridge()
			b.config.ConversationID = "main"

			require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "primary"}))
			id, err := s.AppendEntryID(t.Context(), "main", testSessionEntry("old", "answer"))
			require.NoError(t, err)

			manager := newThreadBridgeManager(b.runtime, s, b.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return b }})
			manager.bridges["main"] = b
			rt := &Runtime{Sessions: s, threads: manager, Cfg: b.runtime}
			b.bus = rt
			events := rt.Subscribe(t.Context())
			runTestBridge(t, b)

			inbound := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindSteer, "new", true)
			inbound.ConversationID = "main"

			turnDone, staged := make(chan error, 1), make(chan error, 1)
			go func() { turnDone <- rt.RunTurn(t.Context(), inbound) }()

			for event := range events {
				if !event.Message.Complete {
					event.Acknowledgement <- nil
					continue
				}

				go func() {
					_, _, err := rt.StageRevert(t.Context(), "main", fmt.Sprintf("%d:0", id), protocol.RevertBefore)
					staged <- err
				}()

				require.Eventually(t, func() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.historyMutation }, time.Second, time.Millisecond)

				select {
				case err := <-staged:
					t.Fatal("staged before delivery settled", err)
				default:
				}

				event.Acknowledgement <- errDelivery

				break
			}

			errTurn, errStage := <-turnDone, <-staged
			marker, _, _, err := s.RevertState(t.Context(), "main")
			require.NoError(t, err)

			if errDelivery == nil {
				require.NoError(t, errTurn)
				require.NoError(t, errStage)
				require.Equal(t, fmt.Sprintf("%d:0", id), marker)
			} else {
				require.ErrorIs(t, errTurn, errDelivery)
				require.ErrorIs(t, errStage, errDelivery)
				require.Empty(t, marker)
			}
		})
	}
}

func TestRevertStorageLifecycle(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	_, err := s.db.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed) VALUES ('main', 'planner', '', 'web_chat', TRUE)`)
	require.NoError(t, err)
	first, err := s.AppendEntryID(ctx, "main", testSessionEntry("first", "reply"))
	require.NoError(t, err)
	second, err := s.AppendEntryID(ctx, "main", testSessionEntry("second", "reply"))
	require.NoError(t, err)
	marker, eligible, previous, err := s.RevertState(ctx, "main")
	require.NoError(t, err)
	require.Empty(t, marker)
	require.True(t, eligible)
	require.Equal(t, fmt.Sprintf("%d:0", second), previous)

	for _, invalid := range []string{"bad", "1:delivery", "01:0", "1:-1", fmt.Sprintf("%d:1", first), fmt.Sprintf("%d:99", first)} {
		_, _, err := stageRevertDB(ctx, s.db, "main", invalid, protocol.RevertBefore)
		require.Error(t, err)
	}

	marker, text, err := stageRevertDB(ctx, s.db, "main", "", protocol.RevertBefore)
	require.NoError(t, err)
	require.Equal(t, "second", text)
	require.Equal(t, fmt.Sprintf("%d:0", second), marker)
	marker, text, err = stageRevertDB(ctx, s.db, "main", "", protocol.RevertBefore)
	require.NoError(t, err)
	require.Equal(t, "first", text)
	require.Equal(t, fmt.Sprintf("%d:0", first), marker)

	marker, text, err = stageRevertDB(ctx, s.db, "main", fmt.Sprintf("%d:1", second), protocol.RevertThrough)
	require.NoError(t, err)
	require.Empty(t, marker, "a point the path no longer shows is still a node to move to")
	require.Empty(t, text, "moving through a reply leaves the message box alone")

	marker, _, err = stageRevertDB(ctx, s.db, "main", fmt.Sprintf("%d:0", first), protocol.RevertBefore)
	require.NoError(t, err)

	entries, err := s.ObserveEntries(ctx, "main")
	require.NoError(t, err)
	require.Empty(t, entries)

	_, err = s.db.ExecContext(ctx, `UPDATE conversations SET created_at = 'epoch' WHERE conversation_id = 'main'`)
	require.NoError(t, err)
	prune, err := shouldPruneThreadConversation(ctx, s.db, "main", time.Unix(1, 0))
	require.NoError(t, err)
	require.False(t, prune, "a first-message cutoff must not let retention discard the stored suffix: hidden entries still date the chat")

	_, eligible, previous, err = s.RevertState(ctx, "main")
	require.NoError(t, err)
	require.True(t, eligible)
	require.Empty(t, previous)

	_, text, err = stageRevertDB(ctx, s.db, "main", "", protocol.RevertBefore)
	require.NoError(t, err)
	require.Empty(t, text, "Undo at the beginning does not replace the draft")

	current, _, _, err := s.RevertState(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, marker, current)

	_, err = s.db.ExecContext(ctx, `UPDATE conversations SET revert_message_id = '' WHERE conversation_id = 'main'`)
	require.NoError(t, err)
	entries, err = s.ObserveEntries(ctx, "main")
	require.NoError(t, err)
	require.Len(t, entries, 2)
}

func TestMoveKeepsSubagentsWithTheirBranch(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	recordTestConversations(t, s, "main", map[string]ConversationKind{"main": KindWebChat})

	turn := func(id, user, call, reply string) *harness.SessionEntry {
		entry := testReplayEntry(`{"type":"message","role":"user","content":"`+user+`"}`, `{"type":"function_call","call_id":"`+call+`","name":"task","arguments":"{}"}`,
			`{"type":"message","role":"assistant","content":"`+reply+`"}`)
		entry.TurnID = id

		return entry
	}

	_, err := s.AppendEntryID(ctx, "main", turn("turn-1", "first", "call_k", "one"))
	require.NoError(t, err)
	second, err := s.AppendEntryID(ctx, "main", turn("turn-2", "second", "call_r", "two"))
	require.NoError(t, err)
	recordLegacyDelegations(t, s, "main", "call_k", "call_r")

	for _, child := range []string{"main/call_k", "main/call_r"} {
		_, err := s.AppendEntryID(ctx, child, testSessionEntry("work", "done"))
		require.NoError(t, err)
	}

	delegations := func() []string {
		listed, err := s.Delegations(ctx, "main", "", 0, 0)
		require.NoError(t, err)

		ids := make([]string, 0, len(listed))
		for _, delegation := range listed {
			ids = append(ids, delegation.ID)
		}

		return ids
	}

	_, _, err = stageRevertDB(ctx, s.db, "main", fmt.Sprintf("%d:0", second), protocol.RevertBefore)
	require.NoError(t, err)
	require.Equal(t, []string{"main/call_k"}, delegations(), "a subagent whose call is off the path is not listed")

	hidden, err := s.ObserveEntries(ctx, "main/call_r")
	require.NoError(t, err)
	require.Len(t, hidden, 1, "a move never prunes a subagent session")

	marker, _, err := stageRevertDB(ctx, s.db, "main", fmt.Sprintf("%d:2", second), protocol.RevertThrough)
	require.NoError(t, err)
	require.Empty(t, marker, "moving through the newest reply returns to the newest point")
	require.Equal(t, []string{"main/call_k", "main/call_r"}, delegations(), "moving back shows the same session again")
}

func TestRevertEligibility(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)

	ctx := t.Context()
	for _, c := range []struct{ id, kind, cronRun string }{
		{"slack-thread:C1:1.1", "slack_thread", ""}, {"cron:job:1", "cron_run", ""}, {"web:cron:job:1", "web_chat", "cron:job:1"}, {"child/call", "subagent", ""},
		{"copied", "web_chat", ""}, {"valid", "web_chat", ""},
	} {
		_, err := s.db.ExecContext(ctx, `INSERT INTO conversations (conversation_id, agent, created_by, kind, listed, cron_run) VALUES ($1, 'planner', '', $2, TRUE, NULLIF($3, ''))`, c.id, c.kind, c.cronRun)
		require.NoError(t, err)
		_, err = s.AppendEntryID(ctx, c.id, testSessionEntry("request", "reply"))
		require.NoError(t, err)
	}

	require.NoError(t, s.RegisterExternalMCPConversation("binding", "planner", &ExternalMCPSessionState{ManagedConversationID: "external", PrivateConversationID: "private", Agent: "planner"}))

	log := slog.New(slog.DiscardHandler)
	rt := &Runtime{Sessions: s, threads: newThreadBridgeManager(nil, s, log, &bridgeFactoryMock{newBridgeFunc: func(c *Config) directBridge { return &Bridge{config: *c, log: log} }})}

	for id, want := range map[string]bool{"slack-thread:C1:1.1": true, "web:cron:job:1": true, "external": true, "copied": true, "valid": true, "cron:job:1": false, "child/call": false, "private": false, "missing": false} {
		_, allowed, _, err := s.RevertState(ctx, id)
		require.NoError(t, err)
		require.Equal(t, want, allowed, id)

		if !want {
			_, _, err = stageRevertDB(ctx, s.db, id, "", protocol.RevertBefore)
			require.ErrorIs(t, err, errRevertConversation, id)
			_, _, err = rt.StageRevert(ctx, id, "", protocol.RevertBefore)
			require.ErrorIs(t, err, errRevertConversation, id)
		}
	}
}

func TestRevertFloor(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	message := func(header, text string) *harness.SessionEntry {
		return testReplayEntry(fmt.Sprintf(`{"type":"message","role":"user"%s,"content":%q}`, header, text), `{"type":"message","role":"assistant","content":"reply"}`)
	}

	for _, tc := range []struct {
		name            string
		kind            ConversationKind
		steps           []string
		undoable, ahead []string
	}{
		{"cron report in a Web chat", KindWebChat, []string{"web:human1", "run:report", "web:human2"}, []string{"human2"}, []string{"human1", "report"}},
		{"External MCP thread", KindSlackThread, []string{"run:mcp1", "run:mcp2", "web:human1", "web:human2", "run:mcp3", "web:human4"}, []string{"human4"}, []string{"human1", "human2", "mcp3"}},
		{"Slack thread", KindSlackThread, []string{"run:mcp1", "slack:slack1", "web:human1", "slack:slack2", "web:human2"}, []string{"human2"}, []string{"human1", "slack2"}},
		{"unlabeled Slack message", KindSlackThread, []string{"web:human1", "old:slack1", "web:human2"}, []string{"human2"}, []string{"human1", "slack1"}},
		{"unlabeled Web message", KindWebChat, []string{"old:human1", "web:human2"}, []string{"human1", "human2"}, nil},
	} {
		chat, run := strings.ReplaceAll(tc.name, " ", "-"), "run-"+strings.ReplaceAll(tc.name, " ", "-")
		recordTestConversations(t, s, "main", map[string]ConversationKind{chat: tc.kind, run: KindExternalMCPRun})

		ids := map[string]string{}

		for i, step := range tc.steps {
			source, text, _ := strings.Cut(step, ":")
			if source == "run" {
				finishTestRunTurn(t, s, run, chat, fmt.Sprintf("%s-%d", run, i), message("", text))

				var id string
				require.NoError(t, queryScalar(ctx, s.db, "run entry", `SELECT max(id)::text || ':0' FROM entries WHERE conversation_id = $1`, &id, run))
				ids[text] = id

				continue
			}

			header := map[string]string{"web": `,"prompt_header":"[Web principal=\"Ann\"]"`, "slack": `,"prompt_header":"[Slack]"`}[source]
			id, err := s.AppendEntryID(ctx, chat, message(header, text))
			require.NoError(t, err)

			ids[text] = fmt.Sprintf("%d:0", id)
		}

		_, _, predecessor, err := s.RevertState(ctx, chat)
		require.NoError(t, err)
		require.Equal(t, ids[tc.undoable[len(tc.undoable)-1]], predecessor, tc.name)

		for _, text := range append(slices.Clone(tc.undoable), tc.ahead...) {
			tx, err := s.db.BeginTx(ctx, nil)
			require.NoError(t, err)
			_, _, err = stageRevertDB(ctx, tx, chat, ids[text], protocol.RevertBefore)
			require.Equal(t, slices.Contains(tc.undoable, text), err == nil, "%s: undo %s: %v", tc.name, text, err)
			require.NoError(t, tx.Rollback())
		}
	}

	const thread = "Slack-thread"

	_, prompt, err := stageRevertDB(ctx, s.db, thread, "", protocol.RevertBefore)
	require.NoError(t, err)
	require.Equal(t, "human2", prompt)

	_, err = s.AppendEntryID(ctx, thread, message(`,"prompt_header":"[Slack]"`, "slack3"))
	require.NoError(t, err)

	bridge := &Bridge{config: Config{ConversationID: thread, SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	rt := &Runtime{Sessions: s, threads: newThreadBridgeManager(nil, s, bridge.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return bridge }})}
	require.NoError(t, rt.StashQueueItem(ctx, thread, &protocol.ThreadQueueItem{ID: "next", Source: protocol.SourceWeb, Principal: "Ann", Kind: protocol.InboundKindHeld, Message: "next"}))

	marker, _, _, err := s.RevertState(ctx, thread)
	require.NoError(t, err)
	require.Empty(t, marker)
	require.Equal(t, []string{"message:mcp1", "message:reply", "message:slack1", "message:reply", "message:human1", "message:reply", "message:slack2", "message:reply", "message:slack3", "message:reply"},
		observedEntryTexts(t, s, thread), "the arrival continues from the current point")

	var kept bool
	require.NoError(t, queryScalar(ctx, s.db, "undone entry", `SELECT count(*) = 1 FROM entries WHERE conversation_id = $1 AND entry_json::text LIKE '%human2%'`, &kept, thread))
	require.True(t, kept, "the undone message stays stored")

	recordTestConversations(t, s, "main", map[string]ConversationKind{"bound-chat": KindWebChat, "bound-run": KindCronRun})
	_, err = s.AppendEntryID(ctx, "bound-run", message("", "older report"))
	require.NoError(t, err)
	before, err := s.AppendEntryID(ctx, "bound-chat", message(`,"prompt_header":"[Web principal=\"Ann\"]"`, "before the report"))
	require.NoError(t, err)
	_, _, err = (stateDAO{db: s.db}).bindRun(ctx, "bound-run", "bound-chat")
	require.NoError(t, err)
	_, _, err = stageRevertDB(ctx, s.db, "bound-chat", fmt.Sprintf("%d:1", before), protocol.RevertThrough)
	require.ErrorContains(t, err, "past a message from a cron run", "the report's older ID still sits after the point on the path")
}

func TestPauseGatesTurnAdmission(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))

	bridge := &Bridge{config: Config{ConversationID: "main", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	rt := &Runtime{Sessions: s, threads: newThreadBridgeManager(nil, s, bridge.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return bridge }})}

	require.ErrorContains(t, rt.SetAgentPaused(ctx, "missing", "Bob", true), "not recorded")
	require.ErrorContains(t, rt.PostPausedMessage(ctx, pausedWebMessage("missing", "Ann", &protocol.InboundContent{Text: "lunch?"})), "not recorded")
	require.NoError(t, rt.SetAgentPaused(ctx, "main", "Ann", false), "resuming a live chat changes nothing")

	bridge.historyMutation = true

	require.ErrorContains(t, rt.SetAgentPaused(ctx, "main", "Ann", true), "history is busy")
	require.ErrorContains(t, rt.PostPausedMessage(ctx, pausedWebMessage("main", "Ann", &protocol.InboundContent{Text: "lunch?"})), "history is busy")

	bridge.historyMutation = false

	require.NoError(t, rt.SetAgentPaused(ctx, "main", "Ann", true))
	require.NoError(t, rt.SetAgentPaused(ctx, "main", "Ann", false))
	assert.Empty(t, bridge.requestCh, "resuming with nothing waiting starts no turn")

	require.NoError(t, s.BeginGoal("main", &GoalState{Objective: "ship it"}))
	seedActiveTurn(t, s, "main", "turn-mid", nil)

	bridge.inputOpen = true

	for range 2 {
		require.NoError(t, rt.SetAgentPaused(ctx, "main", "Bob", true))
	}

	assert.False(t, bridge.inputOpen, "pausing closes the running reply's input")

	continuation := bridgeRequest{inbound: goalContinuation("main", &GoalState{}), completion: &turnCompletion{done: make(chan struct{})}}
	bridge.handle(ctx, &continuation)
	<-continuation.completion.done
	_, refused := errors.AsType[AgentPausedError](continuation.completion.err)
	assert.True(t, refused, "the gate closes a continuation's completion with the paused error")

	goal, _, err := s.Goal("main")
	require.NoError(t, err)
	assert.Equal(t, GoalStatusActive, goal.Status)

	inbound := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindEnqueue, "racing", true)
	inbound.ConversationID, inbound.Metadata = "main", map[string]string{"web_message_id": "racing"}
	human := bridgeRequest{inbound: inbound, completion: &turnCompletion{done: make(chan struct{})}}
	bridge.handle(ctx, &human)
	require.Len(t, bridge.waiting, 1, "a person's request waits for resume with its completion")
	assert.Equal(t, []string{"racing"}, queueIDs(t, s, "main"))

	endTestTurn(t, s, "main", "turn-mid", protocol.TerminalComplete)
	require.NoError(t, rt.SetAgentPaused(ctx, "main", "Ann", false))

	resumed := <-bridge.requestCh
	assert.Equal(t, protocol.GoalActionContinue, resumed.inbound.GoalAction, "resume re-submits the goal's continuation")
	assert.Empty(t, bridge.requestCh, "the goal goes before the waiting request")
	assert.Equal(t, []string{
		"message:\"Ann\" paused the agent. Until it is resumed, messages here are people talking to each other, not requests to you.", "message:\"Ann\" resumed the agent.",
		"message:\"Bob\" paused the agent. Until it is resumed, messages here are people talking to each other, not requests to you.", "message:\"Ann\" resumed the agent.",
	}, observedEntryTexts(t, s, "main"))

	select {
	case <-human.completion.done:
		t.Fatal("the kept request was reported done")
	default:
	}
}

func TestResumeSubmitsRefusedGoalContinuation(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	rt := pausedTestChat(t, s, "main")
	bridge, err := rt.threads.recordedBridge(ctx, "main")
	require.NoError(t, err)
	require.NoError(t, s.BeginGoal("main", &GoalState{Objective: "ship it"}))

	bridge.handle(ctx, &bridgeRequest{inbound: goalContinuation("main", &GoalState{})})

	bridge.requestCh <- bridgeRequest{inbound: pausedWebMessage("main", "Carol", &protocol.InboundContent{Text: "later"}), queueItemID: "later"}

	require.NoError(t, rt.SetAgentPaused(ctx, "main", "Ann", false))

	var actions []protocol.GoalAction
	for len(bridge.requestCh) > 0 {
		actions = append(actions, (<-bridge.requestCh).inbound.GoalAction)
	}

	assert.Equal(t, []protocol.GoalAction{"", protocol.GoalActionContinue}, actions, "the refused continuation follows the queued item")

	require.NoError(t, rt.SetAgentPaused(ctx, "main", "Ann", true))
	require.NoError(t, rt.SetAgentPaused(ctx, "main", "Ann", false))
	assert.Empty(t, bridge.requestCh, "a continuation the gate did not refuse is not submitted again")
}

func TestPauseHandsCallerToHeldRequest(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	rt := pausedTestChat(t, s, "main")
	bridge, err := rt.threads.recordedBridge(ctx, "main")
	require.NoError(t, err)

	caller := &turnCompletion{done: make(chan struct{})}
	for _, completion := range []*turnCompletion{nil, caller} {
		bridge.handle(ctx, &bridgeRequest{inbound: pausedWebMessage("main", "Ann", &protocol.InboundContent{Text: "lunch?"}), queueItemID: "lunch?", completion: completion})
	}

	require.NoError(t, rt.SetAgentPaused(ctx, "main", "Ann", false))
	require.Len(t, bridge.requestCh, 1)
	assert.Same(t, caller, (<-bridge.requestCh).completion, "resume runs the held item with its caller")
}

func TestClaimReleasesHeldRequestCaller(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	rt := pausedTestChat(t, s, "main")
	bridge, err := rt.threads.recordedBridge(ctx, "main")
	require.NoError(t, err)

	callers := map[string]*turnCompletion{"lunch?": {done: make(chan struct{})}, "dinner?": {done: make(chan struct{})}}
	for _, text := range []string{"lunch?", "dinner?"} {
		bridge.handle(ctx, &bridgeRequest{inbound: pausedWebMessage("main", "Ann", &protocol.InboundContent{Text: text}), queueItemID: text, completion: callers[text]})
	}

	stale := bridgeRequest{inbound: pausedWebMessage("main", "Ann", &protocol.InboundContent{Text: "lunch?"}), queueItemID: "lunch?"}
	bridge.requestCh <- stale

	require.NoError(t, rt.SetAgentPaused(ctx, "main", "Bob", false))

	stale = <-bridge.requestCh
	claimed, err := bridge.activateInbound(ctx, &stale)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Same(t, callers["lunch?"], stale.completion, "the claiming turn answers the held request's caller")
	endTestTurn(t, s, "main", stale.turnID, protocol.TerminalComplete)

	own := bridgeRequest{inbound: pausedWebMessage("main", "Ann", &protocol.InboundContent{Text: "dinner?"}), queueItemID: "dinner?", completion: &turnCompletion{done: make(chan struct{})}}
	claimed, err = bridge.activateInbound(ctx, &own)
	require.NoError(t, err)
	require.True(t, claimed)

	select {
	case <-callers["dinner?"].done:
		require.NoError(t, callers["dinner?"].err)
	default:
		t.Fatal("the held request's caller waits for a row another turn claimed")
	}

	assert.Empty(t, bridge.waiting, "no request stays held for a claimed row")
}

func TestAgentPauseStorageFailures(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)

	const reject = `CREATE FUNCTION reject_pause() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'pause store failed'; END $$; `

	for _, tt := range []struct {
		name, failure, restore, want string
		calls                        []string
	}{
		{"pause column", `ALTER TABLE conversations RENAME COLUMN paused TO unavailable_paused`, `ALTER TABLE conversations RENAME COLUMN unavailable_paused TO paused`,
			`column "paused" does not exist`, []string{"read", "resume", "post", "promote", "open input"}},
		{"run history", `ALTER TABLE conversations RENAME COLUMN continues TO unavailable_continues`, `ALTER TABLE conversations RENAME COLUMN unavailable_continues TO continues`,
			"continues", []string{"resume", "post"}},
		{"pause write", reject + `CREATE TRIGGER reject_pause BEFORE UPDATE ON conversations FOR EACH ROW EXECUTE FUNCTION reject_pause()`, `DROP FUNCTION reject_pause() CASCADE`,
			"pause store failed", []string{"resume"}},
		{"line write", reject + `CREATE TRIGGER reject_pause BEFORE INSERT ON entries FOR EACH ROW EXECUTE FUNCTION reject_pause()`, `DROP FUNCTION reject_pause() CASCADE`,
			"pause store failed", []string{"resume", "post"}},
		{"commit", reject + `CREATE CONSTRAINT TRIGGER reject_pause AFTER INSERT ON entries DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_pause()`, `DROP FUNCTION reject_pause() CASCADE`,
			"pause store failed", []string{"resume", "post"}},
		{"goal", `ALTER TABLE conversations RENAME COLUMN goal_objective TO unavailable_goal_objective`, `ALTER TABLE conversations RENAME COLUMN unavailable_goal_objective TO goal_objective`,
			"load resumed goal", []string{"resume"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			_, err := s.db.ExecContext(ctx, `TRUNCATE entries, turns, steps, conversations RESTART IDENTITY CASCADE`)
			require.NoError(t, err)

			rt := pausedTestChat(t, s, "main")
			b, err := rt.threads.recordedBridge(ctx, "main")
			require.NoError(t, err)

			_, err = s.db.ExecContext(ctx, tt.failure)
			require.NoError(t, err)
			t.Cleanup(func() {
				_, err := s.db.Exec(tt.restore)
				require.NoError(t, err)
			})

			message := pausedWebMessage("main", "Ann", &protocol.InboundContent{Text: "lunch at noon?"})
			calls := map[string]func() error{
				"read":    func() error { _, err := rt.AgentPaused(ctx, "main"); return err },
				"resume":  func() error { return rt.SetAgentPaused(ctx, "main", "Ann", false) },
				"post":    func() error { return rt.PostPausedMessage(ctx, message) },
				"promote": func() error { _, err := rt.PromoteQueueItem(ctx, "main", "queued"); return err },
				"open input": func() error {
					_, err := b.openInputWindow(ctx, &bridgeRequest{inbound: message, turnID: "turn"})
					return err
				},
			}

			for _, call := range tt.calls {
				require.ErrorContains(t, calls[call](), tt.want, call)
			}
		})
	}
}

func TestRevertProviderPrefixAndCompletedRetry(t *testing.T) {
	t.Parallel()

	newBridge, s, requests, finals := newResumeTestBridges(t, false)
	b := newBridge()
	b.config.ConversationID = "main"

	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "primary"}))

	entry := testSessionEntry("retained", "retained answer")
	entry.ReplayInput = append(entry.ReplayInput, testSessionEntry("abandoned", "abandoned answer").ReplayInput...)
	entry.ReplayInput = append(entry.ReplayInput, json.RawMessage(`{"type":"compaction","content":"abandoned summary"}`))
	id, err := s.AppendEntryID(t.Context(), "main", entry)
	require.NoError(t, err)

	manager := newThreadBridgeManager(b.runtime, s, b.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return b }})
	manager.bridges["main"] = b
	rt := &Runtime{Sessions: s, threads: manager, Cfg: b.runtime}
	runTestBridge(t, b)
	_, _, err = rt.StageRevert(t.Context(), "main", fmt.Sprintf("%d:2", id), protocol.RevertBefore)
	require.NoError(t, err)

	inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "replacement"}, true)
	inbound.ConversationID, inbound.Metadata["web_message_id"], inbound.Metadata[protocol.InboundPrincipalMetadataKey] = "main", "replacement", "alice"
	require.NoError(t, rt.RunTurn(t.Context(), inbound))
	readFinal(t, finals)

	request := <-requests
	require.Contains(t, request, "retained answer")
	require.Contains(t, request, "replacement")
	require.Contains(t, request, "alice")
	require.NotContains(t, request, "abandoned")
	entries, err := s.ObserveEntries(t.Context(), "main")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	marker := fmt.Sprintf("%d:0", entries[1].ID)
	_, _, err = rt.StageRevert(t.Context(), "main", marker, protocol.RevertBefore)
	require.NoError(t, err)
	require.NoError(t, rt.RunTurn(t.Context(), inbound))
	current, _, _, err := s.RevertState(t.Context(), "main")
	require.NoError(t, err)
	require.Equal(t, marker, current, "completed retry must not commit a later cutoff")
	require.Empty(t, requests)

	inbound.Attachments = []protocol.InboundAttachment{{Name: "new.png", MIMEType: "image/png", Data: []byte("new")}}
	require.ErrorContains(t, rt.RunTurn(t.Context(), inbound), "different content")
}

func TestRevertCompletedFallbackAndPreparationFailure(t *testing.T) {
	t.Parallel()

	newBridge, s, requests, finals := newResumeTestBridges(t, false)
	b := newBridge()
	b.config.ConversationID = "main"

	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "primary"}))
	id, err := s.AppendEntryID(t.Context(), "main", testSessionEntry("earlier", "answer"))
	require.NoError(t, err)

	manager := newThreadBridgeManager(b.runtime, s, b.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return b }})
	manager.bridges["main"] = b
	rt := &Runtime{Sessions: s, threads: manager, Cfg: b.runtime}
	runTestBridge(t, b)

	inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "", AttachmentPresence: protocol.AttachmentPresenceUnsupported}, true)
	inbound.ConversationID, inbound.Metadata["web_message_id"], inbound.Metadata[protocol.InboundPrincipalMetadataKey] = "main", "fallback", "alice"
	require.NoError(t, rt.RunTurn(t.Context(), inbound))
	readFinal(t, finals)
	require.Empty(t, requests)
	active, err := s.HasActiveTurn(t.Context(), "main")
	require.NoError(t, err)
	require.False(t, active)

	marker := fmt.Sprintf("%d:0", id)
	_, _, err = rt.StageRevert(t.Context(), "main", marker, protocol.RevertBefore)
	require.NoError(t, err)
	require.NoError(t, rt.RunTurn(t.Context(), inbound))
	inbound.AttachmentPresence = protocol.AttachmentPresenceImages
	require.ErrorContains(t, rt.RunTurn(t.Context(), inbound), "different content")
	require.Empty(t, requests)
}

func TestRevertHistorySnapshot(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
	id, err := s.AppendEntryID(t.Context(), "main", testSessionEntry("first", "answer"))
	require.NoError(t, err)
	view, err := s.ObserveHistory(t.Context(), "main", "", 0, 0, 1, &HistoryInventory{})
	require.NoError(t, err)
	require.True(t, view.Eligible)
	require.NotEmpty(t, view.Predecessor)

	marker := fmt.Sprintf("%d:0", id)
	_, _, err = stageRevertDB(t.Context(), s.db, "main", marker, protocol.RevertBefore)
	require.NoError(t, err)
	view, err = s.ObserveHistory(t.Context(), "main", "", 0, 0, 1, &view.Inventory)
	require.NoError(t, err)
	require.Equal(t, marker, view.Inventory.Marker)
	require.True(t, view.Reset)
	require.True(t, view.Eligible)
	require.Empty(t, view.Predecessor)
	require.Empty(t, view.Entries)
	require.Zero(t, view.Inventory.From)
}

func TestRevertAdmissionRollbackAndConcurrentIdentity(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
	id, err := s.AppendEntryID(t.Context(), "main", testSessionEntry("old", "answer"))
	require.NoError(t, err)

	marker := fmt.Sprintf("%d:0", id)
	_, _, err = stageRevertDB(t.Context(), s.db, "main", marker, protocol.RevertBefore)
	require.NoError(t, err)

	b := &Bridge{config: Config{ConversationID: "main", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	manager := newThreadBridgeManager(nil, s, b.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return b }})
	manager.bridges["main"] = b
	rt := &Runtime{Sessions: s, threads: manager}
	_, err = s.db.ExecContext(t.Context(), `CREATE FUNCTION reject_admission() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'admission failed'; END $$;
        CREATE TRIGGER reject_admission BEFORE INSERT ON turns FOR EACH ROW EXECUTE FUNCTION reject_admission()`)
	require.NoError(t, err)

	item := protocol.ThreadQueueItem{ID: "new", Kind: protocol.InboundKindHeld, Source: protocol.SourceWeb, Principal: "alice", Message: "edited"}
	require.ErrorContains(t, rt.StashQueueItem(t.Context(), "main", &item), "admission failed")
	current, _, _, err := s.RevertState(t.Context(), "main")
	require.NoError(t, err)
	require.Equal(t, marker, current)

	var raw string
	require.NoError(t, s.db.QueryRowContext(t.Context(), `SELECT entry_json::text FROM entries WHERE id = $1`, id).Scan(&raw))
	require.Contains(t, raw, "answer")
	_, err = s.db.ExecContext(t.Context(), `DROP TRIGGER reject_admission ON turns; DROP FUNCTION reject_admission()`)
	require.NoError(t, err)

	var admissions errgroup.Group
	for range 2 {
		admissions.Go(func() error { candidate := item; return rt.StashQueueItem(t.Context(), "main", &candidate) })
	}

	require.NoError(t, admissions.Wait())

	queue, err := s.ThreadQueueForConversation("main")
	require.NoError(t, err)
	require.Len(t, queue, 1)
	require.Equal(t, "new", queue[0].ID)
	require.NoError(t, s.UpsertThread("other", ThreadState{Agent: "main"}))
	other, err := s.AppendEntryID(t.Context(), "other", testSessionEntry("other history", "kept"))
	require.NoError(t, err)
	_, _, err = stageRevertDB(t.Context(), s.db, "other", fmt.Sprintf("%d:0", other), protocol.RevertBefore)
	require.NoError(t, err)

	otherBridge := &Bridge{config: Config{ConversationID: "other", SessionService: s}, log: b.log, requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	manager.bridges["other"] = otherBridge

	require.ErrorContains(t, rt.StashQueueItem(t.Context(), "other", &item), "another conversation")
	current, _, _, err = s.RevertState(t.Context(), "other")
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%d:0", other), current)
}

func TestRevertGoalIdentityAndPrompt(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))

	inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindPrompt, &protocol.InboundContent{Text: "objective"}, true)
	inbound.ConversationID, inbound.GoalTurn, inbound.GoalAction = "main", true, protocol.GoalActionKickoff
	inbound.Metadata["web_message_id"], inbound.Metadata["web_goal"], inbound.Metadata["web_goal_prompt"] = "goal", `{"Objective":"objective","MaxTurns":3}`, "/goal max-turns=3 objective"
	require.NoError(t, startTurnDB(t.Context(), s.db, "goal-turn", "main", inbound))

	replay := testSessionEntry("objective with generated goal instructions", "answer")
	replay.TurnID = "goal-turn"
	replay.ReplayInput[0] = json.RawMessage(`{"type":"message","role":"user","input_id":"goal","content":"objective with generated goal instructions"}`)
	_, err := s.finishTurn(t.Context(), "goal-turn", &turnFinish{conversationID: strings.TrimSpace("main"), entries: []harness.SessionEntry{*replay}, outbound: &protocol.OutboundMessage{ConversationID: "main"}})
	require.NoError(t, err)
	require.NoError(t, s.closeTurn(t.Context(), "goal-turn"))
	_, prompt, err := stageRevertDB(t.Context(), s.db, "main", "", protocol.RevertBefore)
	require.NoError(t, err)
	require.Equal(t, "/goal max-turns=3 objective", prompt)
	accepted, err := reconcileWebDB(t.Context(), s.db, inbound)
	require.NoError(t, err)
	require.True(t, accepted)

	inbound.Metadata["web_goal"] = `{"Objective":"objective","MaxTurns":5}`
	_, err = reconcileWebDB(t.Context(), s.db, inbound)
	require.ErrorContains(t, err, "different content")
}

func TestRevertBusyAdmissionAndRecordedDrain(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))

	inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "waiting"}, true)
	inbound.ConversationID, inbound.Metadata["web_message_id"] = "main", "waiting"
	b := &Bridge{config: Config{ConversationID: "main", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{}), historyMutation: true}
	manager := newThreadBridgeManager(nil, s, b.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return b }})
	manager.bridges["main"] = b
	rt := &Runtime{Sessions: s, threads: manager}

	require.ErrorContains(t, b.enqueue(ctx, &bridgeRequest{inbound: inbound}, "fresh racing input"), "busy")
	_, _, err := rt.StageRevert(ctx, "main", "bad", protocol.RevertBefore)
	require.Error(t, err)
	_, err = rt.PromoteQueueItem(ctx, "main", "waiting")
	require.ErrorContains(t, err, "busy")
	require.Empty(t, testDrainInputs(t, b, harness.TurnPhaseFinalAnswer))

	completion := &turnCompletion{done: make(chan struct{})}
	request := bridgeRequest{inbound: inbound, queueItemID: "waiting", completion: completion}
	activated, err := b.activateInbound(ctx, &request)
	require.NoError(t, err)
	require.False(t, activated)
	require.NoError(t, b.enqueue(ctx, &request, "already accepted input"))

	queue, err := s.ThreadQueueForConversation("main")
	require.NoError(t, err)
	require.Len(t, queue, 1, "activation and enqueue must not create two owners")

	entry := testSessionEntry("waiting", "answer")
	entry.TurnID = "drained-turn"
	entry.ReplayInput[0] = json.RawMessage(`{"type":"message","role":"user","input_id":"waiting","content":"waiting"}`)
	require.NoError(t, startTurnDB(ctx, s.db, entry.TurnID, "main", inbound))
	_, err = s.finishTurn(ctx, entry.TurnID, &turnFinish{conversationID: strings.TrimSpace("main"), entries: []harness.SessionEntry{*entry}, outbound: &protocol.OutboundMessage{ConversationID: "main"}})
	require.NoError(t, err)
	require.NoError(t, s.closeTurn(ctx, entry.TurnID))
	entries, err := s.ObserveEntries(ctx, "main")
	require.NoError(t, err)

	id := entries[0].ID
	b.waiting = nil
	b.steers, b.steersRead = []bridgeRequest{{inbound: inbound, queueItemID: "waiting", completion: completion}}, 1
	require.NoError(t, b.settleSteers(ctx, nil))
	<-completion.done
	require.NoError(t, completion.err)
	require.Empty(t, b.waiting)

	queue, err = s.ThreadQueueForConversation("main")
	require.NoError(t, err)
	require.Empty(t, queue, "the recorded input no longer has a queue owner")

	_, _, err = rt.StageRevert(ctx, "main", fmt.Sprintf("%d:0", id), protocol.RevertBefore)
	require.ErrorContains(t, err, "busy")

	b.historyMutation = false
	_, _, err = rt.StageRevert(ctx, "main", fmt.Sprintf("%d:0", id), protocol.RevertBefore)
	require.NoError(t, err)
	marker, prompt, err := rt.StageRevert(ctx, "main", "", protocol.RevertBefore)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%d:0", id), marker)
	require.Empty(t, prompt, "Undo at the beginning is a no-op")
}

func TestRevertLatestUnsavedTurn(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
	_, err := s.AppendEntryID(ctx, "main", testSessionEntry("kept", "answer"))
	require.NoError(t, err)

	for _, turnID := range []string{"older", "latest"} {
		require.NoError(t, upsertTestTurn(ctx, s, &testCheckpoint{TurnID: turnID, ConversationKey: "main", ResponseID: "resp-" + turnID, ReplayInput: []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"user","input_id":"` + turnID + `-input","content":"redo ` + turnID + `"}`),
			json.RawMessage(`{"type":"message","id":"msg-` + turnID + `","role":"assistant","status":"completed","content":[{"type":"output_text","text":"partial","annotations":[]}]}`),
		}}))
		require.NoError(t, terminateTestTurn(ctx, s, turnID, protocol.TerminalStopped))
	}

	b := &Bridge{config: Config{ConversationID: "main", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	manager := newThreadBridgeManager(nil, s, b.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return b }})
	manager.bridges["main"] = b
	rt := &Runtime{Sessions: s, threads: manager}

	_, _, err = rt.StageRevert(ctx, "main", "older-input", protocol.RevertBefore)
	require.ErrorContains(t, err, "not in the latest turn", "a saved entry cannot be ordered before later turns")

	_, prompt, err := rt.StageRevert(ctx, "main", "latest-input", protocol.RevertBefore)
	require.NoError(t, err)
	require.Equal(t, "redo latest", prompt)

	keys := func() map[string]ObservedSessionEntry {
		transcript := testHistoryEntries(t, s, "main")

		keys := make(map[string]ObservedSessionEntry)
		for _, entry := range transcript {
			keys[entry.Key] = entry
		}

		return keys
	}
	require.Contains(t, keys(), "turn:main:older")
	require.NotContains(t, keys(), "turn:main:latest", "Revert hides the latest turn")
}

func TestRevertPreservationFailureCompletesOwners(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)

	for _, path := range []string{"steer", "channel", "handle", "activation", "enqueue"} {
		t.Run(path, func(t *testing.T) {
			_, err := s.db.ExecContext(t.Context(), `TRUNCATE entries, turns, steps, conversations RESTART IDENTITY CASCADE`)
			require.NoError(t, err)
			ctx := t.Context()
			require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
			id, err := s.AppendEntryID(ctx, "main", testSessionEntry("old", "answer"))
			require.NoError(t, err)

			inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "waiting"}, true)
			inbound.ConversationID, inbound.Metadata["web_message_id"] = "main", "waiting"
			completion := &turnCompletion{done: make(chan struct{})}
			request := bridgeRequest{inbound: inbound, queueItemID: "waiting", completion: completion}
			require.NoError(t, s.PutThreadQueueItem("waiting", &protocol.ThreadQueueItem{ID: "waiting", ConversationID: "main", Source: protocol.SourceWeb, Kind: protocol.InboundKindEnqueue, Message: inbound.Text, Inbound: inbound}))
			_, err = s.db.ExecContext(ctx, `CREATE FUNCTION reject_preservation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'preservation failed'; END $$;
				CREATE TRIGGER reject_preservation BEFORE UPDATE ON turns FOR EACH ROW WHEN (OLD.phase = 'waiting') EXECUTE FUNCTION reject_preservation()`)
			require.NoError(t, err)
			t.Cleanup(func() {
				_, err := s.db.Exec(`DROP FUNCTION reject_preservation() CASCADE`)
				require.NoError(t, err)
			})

			b := &Bridge{config: Config{ConversationID: "main", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{}), historyMutation: path != "channel"}
			manager := newThreadBridgeManager(nil, s, b.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return b }})
			manager.bridges["main"] = b
			rt := &Runtime{Sessions: s, threads: manager}

			switch path {
			case "steer":
				b.steers = []bridgeRequest{request}
				err = b.settleSteers(ctx, nil)
			case "channel":
				b.requestCh <- request

				_, _, err = rt.StageRevert(ctx, "main", fmt.Sprintf("%d:0", id), protocol.RevertBefore)
			case "handle":
				b.handle(ctx, &request)

				err = completion.err
			case "activation":
				_, err = b.activateInbound(ctx, &request)
			case "enqueue":
				err = b.enqueue(ctx, &request, "waiting")
			}

			require.ErrorContains(t, err, "preservation failed")

			select {
			case <-completion.done:
				require.ErrorContains(t, completion.err, "preservation failed")
			default:
				t.Fatal("failed preservation orphaned its completion")
			}

			require.Empty(t, b.waiting)

			_, err = s.db.ExecContext(ctx, `DROP TRIGGER reject_preservation ON turns`)
			require.NoError(t, err)
			queue, err := s.ThreadQueueForConversation("main")
			require.NoError(t, err)
			require.Len(t, queue, 1)
			require.Equal(t, "waiting", queue[0].ID)
			require.Equal(t, inbound.Text, queue[0].Inbound.Text)

			b.historyMutation = true
			recovered := bridgeRequest{inbound: queue[0].Inbound, queueItemID: queue[0].ID}
			require.NoError(t, b.enqueue(ctx, &recovered, "recovery"))
			require.Len(t, b.waiting, 1)
			require.NoError(t, b.settleSteers(ctx, nil), "teardown must not complete the failed owner twice")

			queue, err = s.ThreadQueueForConversation("main")
			require.NoError(t, err)
			require.Len(t, queue, 1)
		})
	}
}

func TestSendAfterRevertKeepsPreservedOwners(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
	id, err := s.AppendEntryID(ctx, "main", testSessionEntry("old", "answer"))
	require.NoError(t, err)
	_, _, err = stageRevertDB(ctx, s.db, "main", fmt.Sprintf("%d:0", id), protocol.RevertBefore)
	require.NoError(t, err)

	b := &Bridge{config: Config{ConversationID: "main", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	manager := newThreadBridgeManager(nil, s, b.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return b }})
	manager.bridges["main"] = b
	rt := &Runtime{Sessions: s, threads: manager}

	for _, id := range []string{"withdraw", "system", "discard"} {
		inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: id}, id != "system")
		inbound.ConversationID, inbound.Metadata["web_message_id"] = "main", id
		request := bridgeRequest{inbound: inbound, queueItemID: id, completion: &turnCompletion{done: make(chan struct{})}}

		b.mu.Lock()
		err = b.preserveRevertRequestLocked(ctx, &request)
		b.mu.Unlock()
		require.NoError(t, err)
	}

	require.Len(t, b.waiting, 2)

	withdrawn, discarded := b.waiting[0].completion, b.waiting[1].completion
	changed, err := rt.DeleteQueueItem(ctx, "main", "discard")
	require.NoError(t, err)
	require.True(t, changed)
	<-discarded.done
	require.ErrorIs(t, discarded.err, context.Canceled)

	item := protocol.ThreadQueueItem{ID: "replacement", Source: protocol.SourceWeb, Kind: protocol.InboundKindHeld, Message: "edited"}
	require.NoError(t, rt.StashQueueItem(ctx, "main", &item))

	select {
	case <-withdrawn.done:
		t.Fatal("a send after a move dropped a queued request")
	default:
	}

	require.Len(t, b.waiting, 1)

	queue, err := s.ThreadQueueForConversation("main")
	require.NoError(t, err)
	require.Len(t, queue, 2, "queued messages survive the move")
	require.Equal(t, []string{"withdraw", "replacement"}, []string{queue[0].ID, queue[1].ID})
}

func TestRevertLegacyQueuePromotionAndEnqueue(t *testing.T) {
	t.Parallel()

	newBridge, s, requests, finals := newResumeTestBridges(t, false)
	b := newBridge()
	b.config.ConversationID = "main"

	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "primary"}))
	manager := newThreadBridgeManager(b.runtime, s, b.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return b }})
	manager.bridges["main"] = b
	rt := &Runtime{Sessions: s, threads: manager, Cfg: b.runtime}
	runTestBridge(t, b)
	require.NoError(t, s.PutThreadQueueItem("legacy", &protocol.ThreadQueueItem{ID: "legacy", ConversationID: "main", Source: protocol.SourceWeb, Principal: "alice", Message: "legacy request", Kind: protocol.InboundKindEnqueue}))
	changed, err := rt.PromoteQueueItem(t.Context(), "main", "legacy")
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "main", readFinal(t, finals).ConversationID)

	request := <-requests
	require.Contains(t, request, "legacy request")
	require.Contains(t, request, "alice")

	item := protocol.ThreadQueueItem{ID: "queued", Source: protocol.SourceWeb, Principal: "bob", Message: "new queue request", Kind: protocol.InboundKindEnqueue}
	require.NoError(t, rt.StashQueueItem(t.Context(), "main", &item))
	require.Equal(t, "main", readFinal(t, finals).ConversationID)
	require.Contains(t, <-requests, "new queue request")
}

func TestRevertStashKeepsVoiceInbound(t *testing.T) {
	t.Parallel()

	newBridge, s, requests, finals := newResumeTestBridges(t, false)
	crashed := newBridge()
	crashed.config.ConversationID = "main"

	workspace := crashed.runtime.Clone().Workspace
	writeAgent(t, workspace, "primary", "---\ndescription: Primary\nmodel: gpt-6.1-sol\npermission:\n  skill:\n    docs-helper: allow\n---\nPrompt\n")
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, root.MkdirAll(".rocketclaw/skills/docs-helper", 0o755))
	require.NoError(t, root.WriteFile(".rocketclaw/skills/docs-helper/SKILL.md", []byte("---\nname: docs-helper\ndescription: Write docs\n---\n\nUse this skill for docs.\n"), 0o644))
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "primary"}))
	manager := newThreadBridgeManager(crashed.runtime, s, crashed.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return crashed }})
	manager.bridges["main"] = crashed
	rt := &Runtime{Sessions: s, threads: manager, Cfg: crashed.runtime}
	voice := func(id, text string) protocol.ThreadQueueItem {
		inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: text}, true)
		inbound.ConversationID, inbound.Metadata["web_message_id"], inbound.Metadata[protocol.InboundPrincipalMetadataKey] = "main", id, "alice"
		inbound.Metadata[protocol.InboundMediaMetadataKey] = "Voice"

		return protocol.ThreadQueueItem{ID: id, Source: protocol.SourceWeb, Principal: "alice", Kind: protocol.InboundKindSteer, Message: text, Inbound: inbound}
	}

	for range 2 {
		item := voice("stored", "deploy it")
		require.NoError(t, rt.StashQueueItem(t.Context(), "main", &item))
	}

	require.Len(t, crashed.requestCh, 1, "a re-delivered hand-off is admitted once")

	restarted := newBridge()
	restarted.config.ConversationID = "main"
	manager.bridges["main"] = restarted
	runTestBridge(t, restarted)
	require.NoError(t, restarted.PickLaterWork(t.Context()))

	header := `[Web media=Voice principal=\"alice\" additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\n`
	require.Contains(t, <-requests, header+"deploy it", "the stored voice item keeps its header across the restart")
	require.Equal(t, "answer", readFinal(t, finals).Text)

	item := voice("skill", "/docs-helper do it")
	require.NoError(t, rt.StashQueueItem(t.Context(), "main", &item))

	request := <-requests
	require.Contains(t, request, header+"/docs-helper do it")
	require.NotContains(t, request, "Use this skill for docs.", "spoken /skill text loads no skill")
	require.Equal(t, "answer", readFinal(t, finals).Text)
}

func TestRevertAdmissionRollbackAtEveryWrite(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)

	for _, failure := range []struct{ name, setup, want string }{
		{"waiting turns insert", `CREATE TRIGGER reject_branch BEFORE INSERT ON turns FOR EACH ROW WHEN (NEW.phase = 'waiting') EXECUTE FUNCTION reject_branch()`, "branch write failed"},
		{"unreadable admitted input", `UPDATE turns SET inbound_json = '{"Text":5,"Metadata":{"web_message_id":"replacement"}}' WHERE id = 'old-turn'`, "decode admitted Web input"},
	} {
		t.Run(failure.name, func(t *testing.T) {
			_, err := s.db.ExecContext(t.Context(), `TRUNCATE entries, turns, steps, conversations RESTART IDENTITY CASCADE`)
			require.NoError(t, err)
			ctx := t.Context()
			require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))

			entry := testSessionEntry("keep", "kept")
			entry.ReplayInput = append(entry.ReplayInput, testSessionEntry("old", "abandoned").ReplayInput...)
			entry.TurnID = "old-turn"
			id, err := s.AppendEntryID(ctx, "main", entry)
			require.NoError(t, err)
			_, err = s.AppendEntryID(ctx, "main", testSessionEntry("later", "later answer"))
			require.NoError(t, err)

			inbound := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindSteer, "old", true)
			require.NoError(t, startTurnDB(ctx, s.db, "old-turn", "main", inbound))
			_, err = s.db.ExecContext(ctx, `UPDATE turns SET phase = 'done'; INSERT INTO steps (conversation_id, key, value) VALUES ('main', 'old-turn/retry/1', '{}')`)
			require.NoError(t, err)
			require.NoError(t, s.PutThreadQueueItem("waiting", &protocol.ThreadQueueItem{ID: "waiting", ConversationID: "main", Source: protocol.SourceWeb, Message: "pending"}))

			marker := fmt.Sprintf("%d:2", id)
			_, _, err = stageRevertDB(ctx, s.db, "main", marker, protocol.RevertBefore)
			require.NoError(t, err)
			_, err = s.db.ExecContext(ctx, `CREATE FUNCTION reject_branch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'branch write failed'; END $$`)
			require.NoError(t, err)
			t.Cleanup(func() {
				_, err := s.db.Exec(`DROP FUNCTION reject_branch() CASCADE`)
				require.NoError(t, err)
			})

			_, err = s.db.ExecContext(ctx, failure.setup)
			require.NoError(t, err)

			const snapshot = `WITH entry_rows AS (
				SELECT jsonb_agg(to_jsonb(e) ORDER BY id) AS agg FROM entries e
			), turn_rows AS (
				SELECT jsonb_agg(to_jsonb(a) ORDER BY id) AS agg FROM turns a
			), step_rows AS (
				SELECT jsonb_agg(to_jsonb(j) ORDER BY key) AS agg FROM steps j
			), conversation_rows AS (
				SELECT jsonb_agg(to_jsonb(m) ORDER BY conversation_id) AS agg FROM conversations m
			)
			SELECT jsonb_build_array(e.agg, a.agg, j.agg, m.agg)::text
			FROM entry_rows e CROSS JOIN turn_rows a CROSS JOIN step_rows j CROSS JOIN conversation_rows m`

			var before, after string
			require.NoError(t, s.db.QueryRowContext(ctx, snapshot).Scan(&before))

			b := &Bridge{config: Config{ConversationID: "main", SessionService: s}, log: slog.New(slog.DiscardHandler)}
			rt := &Runtime{Sessions: s}
			replacement := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindHeld, &protocol.InboundContent{Text: "edited"}, true)
			replacement.ConversationID, replacement.Metadata["web_message_id"] = "main", "replacement"
			fresh, err := rt.admitWeb(ctx, b, replacement, protocol.GoalRequest{})
			require.ErrorContains(t, err, failure.want)
			require.False(t, fresh)
			require.NoError(t, s.db.QueryRowContext(ctx, snapshot).Scan(&after))
			require.JSONEq(t, before, after, "history, journals, pending inputs and summary must all survive failed admission")
		})
	}
}

func TestRevertStorageFailuresAreNotEmptySuccess(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
	b := &Bridge{config: Config{ConversationID: "main", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	manager := newThreadBridgeManager(nil, s, b.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return b }})
	manager.bridges["main"] = b
	rt := &Runtime{Sessions: s, threads: manager}
	inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "new"}, true)
	inbound.ConversationID, inbound.Metadata["web_message_id"] = "main", "new"

	require.NoError(t, s.Stop())
	marker, _, _, err := s.RevertState(ctx, "main")
	require.ErrorContains(t, err, "database is closed")
	require.Empty(t, marker)

	_, _, err = rt.StageRevert(ctx, "main", "1:0", protocol.RevertBefore)
	require.ErrorContains(t, err, "database is closed")
	require.ErrorContains(t, rt.RunTurn(ctx, inbound), "database is closed")
	require.ErrorContains(t, rt.StartGoal(ctx, inbound, protocol.GoalRequest{Objective: "new"}), "database is closed")
	_, err = rt.PopQueueItem(ctx, "main", "new")
	require.ErrorContains(t, err, "database is closed")
	_, err = rt.PromoteQueueItem(ctx, "main", "new")
	require.ErrorContains(t, err, "database is closed")
	require.ErrorContains(t, rt.StashQueueItem(ctx, "main", &protocol.ThreadQueueItem{Source: protocol.SourceWeb}), "database is closed")
	_, err = rt.mutateWebQueue(ctx, "main", "new", protocol.InboundKindSteer)
	require.ErrorContains(t, err, "database is closed")
	_, err = rt.admitWeb(ctx, b, inbound, protocol.GoalRequest{})
	require.ErrorContains(t, err, "database is closed")

	completion := &turnCompletion{done: make(chan struct{})}
	b.handle(ctx, &bridgeRequest{inbound: inbound, completion: completion})
	<-completion.done
	require.ErrorContains(t, completion.err, "database is closed")
	_, err = s.Delegations(ctx, "main", "", 0, 0)
	require.ErrorContains(t, err, "database is closed")
	_, _, err = stageRevertDB(ctx, s.db, "main", "1:0", protocol.RevertBefore)
	require.ErrorContains(t, err, "database is closed")
	_, err = revertTargetDB(ctx, s.db, "main", "1:0", protocol.RevertBefore)
	require.ErrorContains(t, err, "read revert boundary")
	_, err = saveRevertTurnDB(ctx, s.db, "main", "new", "turn")
	require.ErrorContains(t, err, "read reverted input")

	preserved := &turnCompletion{done: make(chan struct{})}

	b.mu.Lock()
	err = b.preserveRevertRequestLocked(ctx, &bridgeRequest{inbound: inbound, completion: preserved})
	b.mu.Unlock()
	require.ErrorContains(t, err, "classify waiting reverted input")
	<-preserved.done
	require.ErrorIs(t, preserved.err, err, "a failed transfer releases its caller")
	require.Empty(t, b.waiting)
	require.ErrorContains(t, s.closeTurn(ctx, "turn"), "database is closed")
	require.ErrorContains(t, s.PutThreadQueueItem("new", &protocol.ThreadQueueItem{}), "database is closed")
	require.Empty(t, b.requestCh, "storage failure must never wake execution")
}

func TestRevertStageFailurePreservesHistory(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)

	for _, tc := range []struct{ unavailable, want string }{
		{"conversations.goal_objective", "read goal"}, {"turns", `relation "turns" does not exist`}, {"conversations update", "stage failed"}, {"settled delivery", "revert requires settled delivery"},
		{"conversations.continues", "observe session revert"}, {"turns.inbound_json", "read reverted goal prompt"},
	} {
		unavailable := tc.unavailable
		t.Run(unavailable, func(t *testing.T) {
			_, err := s.db.ExecContext(t.Context(), `TRUNCATE entries, turns, steps, conversations RESTART IDENTITY CASCADE`)
			require.NoError(t, err)
			ctx := t.Context()
			require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
			id, err := s.AppendEntryID(ctx, "main", testSessionEntry("original", "answer"))
			require.NoError(t, err)

			b := &Bridge{config: Config{ConversationID: "main", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
			manager := newThreadBridgeManager(nil, s, b.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return b }})
			manager.bridges["main"] = b
			rt := &Runtime{Sessions: s, threads: manager}

			table, column, renamed := strings.Cut(unavailable, ".")

			switch {
			case renamed:
				_, err = s.db.ExecContext(ctx, "ALTER TABLE "+table+" RENAME COLUMN "+column+" TO unavailable_"+column)
				require.NoError(t, err)
				t.Cleanup(func() {
					_, err := s.db.Exec("ALTER TABLE " + table + " RENAME COLUMN unavailable_" + column + " TO " + column)
					require.NoError(t, err)
				})
			case unavailable == "conversations update":
				_, err = s.db.ExecContext(ctx, `CREATE FUNCTION reject_stage() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'stage failed'; END $$;
					CREATE TRIGGER reject_stage BEFORE UPDATE ON conversations FOR EACH ROW EXECUTE FUNCTION reject_stage()`)
				require.NoError(t, err)
				t.Cleanup(func() {
					_, err := s.db.Exec(`DROP FUNCTION reject_stage() CASCADE`)
					require.NoError(t, err)
				})
			case unavailable == "settled delivery":
				err = startTurnDB(ctx, s.db, "delivering", "main", protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "running", true))
				require.NoError(t, err)
				_, err = s.finishTurn(ctx, "delivering", &turnFinish{conversationID: strings.TrimSpace("main"), outbound: &protocol.OutboundMessage{ConversationID: "main"}})
			default:
				_, err = s.db.ExecContext(ctx, "ALTER TABLE "+unavailable+" RENAME TO unavailable_stage_table")
				require.NoError(t, err)
				t.Cleanup(func() {
					_, err := s.db.Exec(`ALTER TABLE unavailable_stage_table RENAME TO ` + unavailable)
					require.NoError(t, err)
				})
			}

			require.NoError(t, err)
			_, _, err = rt.StageRevert(ctx, "main", fmt.Sprintf("%d:0", id), protocol.RevertBefore)
			require.ErrorContains(t, err, tc.want)
			require.False(t, b.historyMutation, "failed staging must release the transient barrier")

			var marker string
			require.NoError(t, s.db.QueryRowContext(ctx, `SELECT revert_message_id FROM conversations WHERE conversation_id = 'main'`).Scan(&marker))
			require.Empty(t, marker)

			var raw string
			require.NoError(t, s.db.QueryRowContext(ctx, `SELECT entry_json::text FROM entries WHERE id = $1`, id).Scan(&raw))
			require.Contains(t, raw, "answer")
		})
	}
}

func TestRevertCorruptHistoryCannotBeAdmitted(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)

	for _, corrupt := range []struct{ name, owner, raw, want string }{
		{"trace", "main", `{"type":"turn","output_trace":1,"replay_input":[{"role":"user","type":"message","content":"old"}]}`, "decode revert boundary"},
		{"replay", "main", `{"type":"turn","replay_input":[{"role":"user","type":"message","input_id":"old","prompt_header":42,"content":"old"}]}`, "decode revert boundary"},
		{"synced", "run", `{"type":"turn","replay_input":[42]}`, "read visible replay item type"},
	} {
		t.Run(corrupt.name, func(t *testing.T) {
			_, err := s.db.ExecContext(t.Context(), `TRUNCATE entries, turns, steps, conversations RESTART IDENTITY CASCADE`)
			require.NoError(t, err)
			ctx := t.Context()
			require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
			recordTestConversations(t, s, "main", map[string]ConversationKind{"run": KindCronRun})
			_, err = s.db.ExecContext(ctx, `UPDATE conversations SET reports_to = 'main' WHERE conversation_id = 'run'`)
			require.NoError(t, err)

			var id int64
			require.NoError(t, s.db.QueryRowContext(ctx, `INSERT INTO entries (conversation_id, entry_json, entry_timestamp) VALUES ($1, $2, '') RETURNING id`, corrupt.owner, corrupt.raw).Scan(&id))
			require.NoError(t, linkHistoryDB(ctx, s.db, "main", corrupt.owner, id))
			_, _, err = stageRevertDB(ctx, s.db, "main", fmt.Sprintf("%d:0", id), protocol.RevertBefore)
			require.ErrorContains(t, err, corrupt.want)

			_, err = s.ObserveHistory(ctx, "main", "", 0, 0, 0, &HistoryInventory{})
			if corrupt.name == "trace" {
				require.Error(t, err)
			}

			inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "old"}, true)

			inbound.ConversationID, inbound.Metadata["web_message_id"] = "main", "old"
			if corrupt.name == "replay" {
				_, err = reconcileWebDB(ctx, s.db, inbound)
				require.Error(t, err)
			}
		})
	}
}

func TestRevertSavedAttachmentRetryIdentity(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "original", Attachments: []protocol.InboundAttachment{
		{Name: "photo.png", MIMEType: "image/png", Data: append(tinyPNG(), "image bytes"...)},
		{Name: "chart.png", MIMEType: "image/png", Data: append(tinyPNG(), "chart bytes"...)},
	}}, true)
	inbound.ConversationID, inbound.Metadata["web_message_id"], inbound.Metadata[protocol.InboundPrincipalMetadataKey] = "main", "original", "alice"
	prompt := buildPrompt(inbound, nil, false)
	header, _, _ := strings.Cut(prompt, "\n\n")
	attachments := attachmentsFromInbound(inbound.Attachments)

	type portableContent struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL string `json:"image_url"`
		FileData string `json:"file_data"`
		Filename string `json:"filename"`
	}

	raw, err := json.Marshal(struct {
		Type    string            `json:"type"`
		Role    string            `json:"role"`
		Content []portableContent `json:"content"`
		Header  string            `json:"prompt_header"`
		InputID string            `json:"input_id"`
	}{Type: "message", Role: "user", Header: header, InputID: "original", Content: []portableContent{
		{Type: "input_text", Text: prompt}, {Type: "input_image", ImageURL: attachments[0].URL}, {Type: "input_image", ImageURL: attachments[1].URL},
	}})
	require.NoError(t, err)
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
	_, err = s.AppendEntryID(t.Context(), "main", &harness.SessionEntry{Type: "turn", ReplayInput: []json.RawMessage{raw}})
	require.NoError(t, err)
	accepted, err := reconcileWebDB(t.Context(), s.db, inbound)
	require.NoError(t, err)
	require.True(t, accepted)

	inbound.Attachments[1].Data = append(tinyPNG(), "different bytes"...)
	_, err = reconcileWebDB(t.Context(), s.db, inbound)
	require.ErrorContains(t, err, "different content")
}

func TestRetriedSteerWithSkippedFileIsAcceptedAfterItsTurn(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	recordTestConversations(t, s, "main", map[string]ConversationKind{"web-retry": KindWebChat})
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), bus: discardPublisher{}, inputOpen: true, requestCh: make(chan bridgeRequest, 1), config: Config{ConversationID: "web-retry", SessionService: s}}
	send := func() *protocol.InboundMessage {
		inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "see the file", Attachments: []protocol.InboundAttachment{{Name: "notes.png", MIMEType: "image/png", Data: []byte("not an image")}}}, true)
		inbound.ConversationID = "web-retry"
		inbound.Metadata["web_message_id"], inbound.Metadata[protocol.InboundPrincipalMetadataKey] = "steer-1", "alice"

		return inbound
	}

	require.NoError(t, bridge.Submit(t.Context(), send()))
	inputs := testDrainInputs(t, bridge, harness.TurnPhaseFinalAnswer)
	require.Len(t, inputs, 1)
	replay, err := harness.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{harness.PromptInputMessage(&inputs[0])})
	require.NoError(t, err)
	_, err = s.AppendEntryID(t.Context(), "web-retry", &harness.SessionEntry{Type: "turn", ReplayInput: replay})
	require.NoError(t, err)

	accepted, err := reconcileWebDB(t.Context(), s.db, send())
	require.NoError(t, err)
	require.True(t, accepted, "the same steer sent again after its turn is the one already taken")
}

func TestUndoThenSendKeepsBothBranches(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	recordTestConversations(t, s, "main", map[string]ConversationKind{"chat": KindWebChat})

	a, err := s.AppendEntryID(ctx, "chat", testSessionEntry("A", "a"))
	require.NoError(t, err)
	b, err := s.AppendEntryID(ctx, "chat", testSessionEntry("B", "b"))
	require.NoError(t, err)
	c, err := s.AppendEntryID(ctx, "chat", testReplayEntry(`{"type":"message","role":"user","input_id":"sent-c","content":"C"}`, `{"type":"message","role":"assistant","content":"c"}`))
	require.NoError(t, err)

	_, prompt, err := stageRevertDB(ctx, s.db, "chat", "", protocol.RevertBefore)
	require.NoError(t, err)
	require.Equal(t, "C", prompt)

	bridge := &Bridge{config: Config{ConversationID: "chat", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	rt := &Runtime{Sessions: s, threads: newThreadBridgeManager(nil, s, bridge.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return bridge }})}
	resent := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindEnqueue, &protocol.InboundContent{Text: "C"}, true)
	resent.ConversationID, resent.Metadata["web_message_id"] = "chat", "resent-c"
	fresh, err := rt.admitWeb(ctx, bridge, resent, protocol.GoalRequest{})
	require.NoError(t, err)
	require.True(t, fresh, "the restored text sent again carries a new message ID and is admitted")

	sibling, err := s.AppendEntryID(ctx, "chat", testSessionEntry("C'", "c'"))
	require.NoError(t, err)

	require.Equal(t, []historyLink{{a, 0, -1}, {b, a, -1}, {c, b, -1}, {sibling, b, -1}}, chatHistoryLinks(t, s, "chat"), "C and C' are siblings after B")
	require.Equal(t, []string{"message:A", "message:a", "message:B", "message:b", "message:C'", "message:c'"}, observedEntryTexts(t, s, "chat"))

	marker, prompt, err := stageRevertDB(ctx, s.db, "chat", fmt.Sprintf("%d:1", c), protocol.RevertThrough)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%d:2", c), marker)
	require.Empty(t, prompt)
	require.Equal(t, []string{"message:A", "message:a", "message:B", "message:b", "message:C", "message:c"}, observedEntryTexts(t, s, "chat"))

	entries := testHistoryEntries(t, s, "chat")

	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, entry.Key)
	}

	require.Equal(t, []string{strconv.FormatInt(a, 10), strconv.FormatInt(b, 10), strconv.FormatInt(c, 10)}, keys, "the transcript shows A, B, C")

	next, err := s.AppendEntryID(ctx, "chat", testSessionEntry("D", "d"))
	require.NoError(t, err)
	require.Equal(t, historyLink{next, c, 2}, chatHistoryLinks(t, s, "chat")[4], "the next message continues from C")
}

func TestRevertUnsavedTurnOnALowerBranch(t *testing.T) {
	t.Parallel()

	s := newTestSessionService(t)
	ctx := t.Context()
	require.NoError(t, s.UpsertThread("main", ThreadState{Agent: "main"}))
	kept, err := s.AppendEntryID(ctx, "main", testSessionEntry("kept", "answer"))
	require.NoError(t, err)
	sibling, err := s.AppendEntryID(ctx, "main", testSessionEntry("sibling", "higher ID"))
	require.NoError(t, err)
	_, _, err = stageRevertDB(ctx, s.db, "main", fmt.Sprintf("%d:0", sibling), protocol.RevertBefore)
	require.NoError(t, err)

	require.NoError(t, upsertTestTurn(ctx, s, &testCheckpoint{TurnID: "in-flight", ConversationKey: "main", ReplayInput: []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","input_id":"in-flight-input","content":"retry"}`),
		json.RawMessage(`{"type":"message","role":"assistant","content":"partial"}`),
	}}))
	require.NoError(t, terminateTestTurn(ctx, s, "in-flight", protocol.TerminalStopped))

	b := &Bridge{config: Config{ConversationID: "main", SessionService: s}, log: slog.New(slog.DiscardHandler), requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	manager := newThreadBridgeManager(nil, s, b.log, &bridgeFactoryMock{newBridgeFunc: func(*Config) directBridge { return b }})
	manager.bridges["main"] = b
	rt := &Runtime{Sessions: s, threads: manager}

	_, prompt, err := rt.StageRevert(ctx, "main", "in-flight-input", protocol.RevertBefore)
	require.NoError(t, err, "the sibling's higher ID is not on this branch")
	require.Equal(t, "retry", prompt)

	links := chatHistoryLinks(t, s, "main")
	require.Equal(t, kept, links[len(links)-1].parent, "the saved turn continues from its own branch")
}
