package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// finalsPublisher completes delivery at once and reports every final outbound.
type finalsPublisher struct {
	finals chan *protocol.OutboundMessage
}

func (p finalsPublisher) PublishOutbound(_ context.Context, message *protocol.OutboundMessage) error {
	message.MarkDelivered(nil)

	if message.Complete {
		p.finals <- message
	}

	return nil
}

// newResumeTestBridges returns a factory for bridges of one conversation that
// survive a simulated restart, a provider whose request bodies are reported, and
// the published finals. A blocked provider holds its first request until its context ends.
func newResumeTestBridges(t *testing.T, blocked bool) (newBridge func() *Bridge, service *SessionService, requests chan string, finals chan *protocol.OutboundMessage) {
	t.Helper()

	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	service = newTestSessionServiceAt(t, workspace)
	requests, finals = make(chan string, 16), make(chan *protocol.OutboundMessage, 16)

	release, first := make(chan struct{}), make(chan struct{}, 1)
	if blocked {
		first <- struct{}{}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- string(body)

		select {
		case <-first:
			select {
			case <-r.Context().Done():
			case <-release:
			}

			return
		default:
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"answer","annotations":[]}]}]}`))
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })

	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, service.UpsertThread(conversationID, ThreadState{Agent: "main"}))

	cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIKey: "test", APIBaseURL: server.URL}}
	newBridge = func() *Bridge {
		return NewConversation(cfg, finalsPublisher{finals: finals}, &Config{ConversationID: conversationID, Agent: "main", RequestRestart: testNoopRestart, StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.DiscardHandler))
	}

	return newBridge, service, requests, finals
}

func runTestBridge(t *testing.T, bridge *Bridge) context.CancelFunc {
	t.Helper()

	ctx, cancelCause := context.WithCancelCause(t.Context())
	cancel := func() { cancelCause(errShutdown) }
	done := make(chan error, 1)

	go func() { done <- bridge.Run(ctx) }()

	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})

	return cancel
}

func readFinal(t *testing.T, finals chan *protocol.OutboundMessage) *protocol.OutboundMessage {
	t.Helper()

	select {
	case final := <-finals:
		return final
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for final outbound")
		return nil
	}
}

// AE5: a crash after the claim and before any step leaves a row that runs exactly once after restart.
func TestClaimedQueueItemRunsExactlyOnceAfterCrash(t *testing.T) {
	newBridge, service, requests, finals := newResumeTestBridges(t, false)
	crashed := newBridge()
	conversationID := crashed.config.ConversationID
	require.NoError(t, service.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ID: "q1", ConversationID: conversationID, Message: "queued work", Principal: "U1", Source: protocol.SourceWeb}))

	require.NoError(t, crashed.pickLaterWork(t.Context(), false))
	request := <-crashed.requestCh
	admitted, err := crashed.activateInbound(t.Context(), &request)
	require.NoError(t, err)
	require.True(t, admitted)

	queue, err := service.ThreadQueueForConversation(conversationID)
	require.NoError(t, err)
	require.Empty(t, queue, "the claim and the row commit together")
	require.Equal(t, []string{request.turnID}, runningTestTurns(t, service))

	queued := []protocol.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}}
	data, err := json.Marshal(queued)
	require.NoError(t, err)
	require.NoError(t, service.SaveTurnStep(t.Context(), conversationID, request.turnID+"/attachments", data))

	runTestBridge(t, newBridge())
	final := readFinal(t, finals)
	assert.Equal(t, "answer", final.Text)
	assert.Equal(t, queued, final.Attachments)
	assert.Equal(t, request.turnID, final.TurnID, "the resumed request keeps its stable turn ID")
	assert.Len(t, requests, 1)

	require.Eventually(t, func() bool { return len(runningTestTurns(t, service)) == 0 }, 5*time.Second, 10*time.Millisecond)
	entries, err := service.ObserveEntries(t.Context(), conversationID)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, request.turnID, entries[0].Entry.TurnID)
	assert.Empty(t, testTurnStepKeys(t, service, conversationID))
}

// AE6: a crash in the delivering phase re-delivers from the row with no model call
// and no second goal accounting; the goal then continues exactly once.
func TestDeliveringRowRedeliversWithoutRunningAgain(t *testing.T) {
	newBridge, service, requests, finals := newResumeTestBridges(t, false)
	crashed := newBridge()
	conversationID := crashed.config.ConversationID
	require.NoError(t, service.BeginGoal(conversationID, "ship it", "", 5, "T1", "U1"))

	msg := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "continue", false)
	msg.GoalAction, msg.ConversationID = protocol.GoalActionContinue, conversationID
	require.NoError(t, startTurnDB(t.Context(), service.db, "turn-finished", conversationID, msg))

	outbound := crashed.newOutboundMessage(msg, "turn-finished", "finished answer", true)
	outbound.Attachments = []protocol.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}}

	require.NoError(t, service.SaveTurnStep(t.Context(), conversationID, protocol.ReplyStepKey("turn-finished"), []byte(`{"ChannelID":"C123","MessageTS":"555.1"}`)))
	_, err := service.finishTurn(t.Context(), "turn-finished", &turnFinish{store: newSessionStore(conversationID, service), accountGoal: true, outbound: outbound})
	require.NoError(t, err)

	late := []rocketcode.SessionEntry{{Version: 1, Type: "turn", Timestamp: time.Now(), TurnID: "turn-finished"}}
	stored, err := service.finishTurn(t.Context(), "turn-finished", &turnFinish{store: newSessionStore(conversationID, service), accountGoal: true, entries: late, outbound: crashed.newOutboundMessage(msg, "turn-finished", "late answer", true)})
	require.NoError(t, err)
	assert.Equal(t, "finished answer", stored.Text, "a second finish returns the stored outbound")
	assert.Equal(t, outbound.Attachments, stored.Attachments)
	entries, err := service.ObserveEntries(t.Context(), conversationID)
	require.NoError(t, err)
	assert.Empty(t, entries, "a second finish appends no history")

	restarted := newBridge()
	runTestBridge(t, restarted)
	final := readFinal(t, finals)
	assert.Equal(t, "finished answer", final.Text)
	assert.Equal(t, outbound.Attachments, final.Attachments)
	assert.Equal(t, "turn-finished", final.TurnID)
	assert.JSONEq(t, `{"ChannelID":"C123","MessageTS":"555.1"}`, string(final.ReplyState), "delivery edits the turn's recorded placeholder")

	goal, _, err := service.Goal(conversationID)
	require.NoError(t, err)
	assert.Equal(t, 1, goal.TurnsUsed, "redelivery does not account the goal again")

	continued := readFinal(t, finals)
	assert.Equal(t, "answer", continued.Text, "the goal continues once after the redelivered turn")
	assert.Len(t, requests, 1, "only the continuation calls the model")
}

// AE9: $stop on a workflow row waiting to resume posts one stopped final and never runs it.
func TestStopOnWaitingRowPostsStoppedFinalAndDoesNotRun(t *testing.T) {
	newBridge, service, requests, finals := newResumeTestBridges(t, false)
	idle := newBridge()
	conversationID := idle.config.ConversationID
	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "seed", true)
	inbound.ConversationID, inbound.Workflow = conversationID, protocol.WorkflowInvocation{Name: "audit"}
	require.NoError(t, startTurnDB(t.Context(), service.db, "turn-waiting", conversationID, inbound))

	stopped := idle.InterruptActiveTurn()
	require.NotNil(t, stopped)
	final := readFinal(t, finals)
	assert.Empty(t, final.Text)
	assert.Equal(t, "turn-waiting", final.TurnID)
	assert.Equal(t, protocol.TerminalStopped, final.WorkflowTerminal, "the workflow card shows the run stopped")
	require.Eventually(t, func() bool { return len(runningTestTurns(t, service)) == 0 }, 5*time.Second, 10*time.Millisecond)

	runTestBridge(t, newBridge())
	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, requests, "a stopped row does not resume")
	assert.Empty(t, finals, "the stopped final is posted once")

	entries, err := service.ObserveTranscript(t.Context(), conversationID, 0, 0, nil)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, protocol.TerminalStopped, entries[0].Terminal, "the web transcript shows the stopped turn")
}

// AE2: shutdown cancels a turn blocked on the model at once, publishes nothing,
// and leaves its row running for the next start.
func TestShutdownCancelsTurnWithoutPublishing(t *testing.T) {
	newBridge, service, requests, finals := newResumeTestBridges(t, true)
	bridge := newBridge()

	msg := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "hello", true)
	response := msg.EnableResponseWait()
	cancel := runTestBridge(t, bridge)
	require.NoError(t, bridge.Submit(t.Context(), msg))
	<-requests

	cancel()
	require.Eventually(t, func() bool { return !bridge.handlingSnapshot() }, 5*time.Second, 10*time.Millisecond)
	assert.Empty(t, finals, "no internal-error or stopped final is posted")
	assert.Empty(t, response, "the waiter is not completed")
	require.Len(t, runningTestTurns(t, service), 1, "the row stays running for resume")
	assert.NotEmpty(t, testTurnStepKeys(t, service, bridge.config.ConversationID), "recorded steps survive")
}

// A Slack redelivery while a row waits must be swallowed, not started as a second turn.
func TestThreadBusyWhileActiveTurnRowWaits(t *testing.T) {
	service := newTestSessionService(t)
	manager := newThreadBridgeManager(nil, service, slog.New(slog.DiscardHandler), func(Config) directBridge { return newDirectBridgeMock() })
	runTestManager(t, manager)

	target := protocol.TextConversationTarget{ChannelID: "C123", ThreadID: "111.222"}
	require.False(t, manager.ThreadBusy(target))

	seedActiveTurn(t, service, protocol.SlackThreadConversationID("C123", "111.222"), "turn-waiting", nil)
	assert.True(t, manager.ThreadBusy(target))

	reserved := protocol.TextConversationTarget{ChannelID: "C123", ThreadID: "333.444"}
	service.reserveTurnPair(protocol.SlackThreadConversationID(reserved.ChannelID, reserved.ThreadID), "external_mcp:private")
	assert.True(t, manager.ThreadBusy(reserved), "a pair reserved by its private session is busy")
}

// A request waiting for its paired session is kept for the next start on
// shutdown, and a waiting row is finished as stopped by $stop.
func TestWaitingForPairedSession(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "shutdown", true: "stop"}[stop], func(t *testing.T) {
			service := newTestSessionService(t)
			pairID := protocol.SlackThreadConversationID("C123", "111.222")
			service.reserveTurnPair(pairID, "external_mcp:private")

			finals := make(chan *protocol.OutboundMessage, 4)
			bridge := NewConversation(&config.Config{Workspace: t.TempDir()}, finalsPublisher{finals: finals}, &Config{ConversationID: pairID, Agent: "main", ManagedConversationID: pairID, RequestRestart: testNoopRestart, StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.DiscardHandler))

			if stop {
				seedActiveTurn(t, service, pairID, "turn-waiting", nil)
			}

			cancel := runTestBridge(t, bridge)

			if !stop {
				msg := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "wait", true)
				msg.ConversationID = pairID
				require.NoError(t, bridge.Submit(t.Context(), msg))
			}

			require.Eventually(t, func() bool {
				bridge.mu.Lock()
				defer bridge.mu.Unlock()

				return bridge.waitingTurnCancel != nil
			}, 5*time.Second, 10*time.Millisecond)

			if stop {
				require.NotNil(t, bridge.InterruptActiveTurn())

				final := readFinal(t, finals)
				assert.Empty(t, final.Text)
				assert.Equal(t, "turn-waiting", final.TurnID)
				require.Eventually(t, func() bool { return len(runningTestTurns(t, service)) == 0 }, 5*time.Second, 10*time.Millisecond)

				return
			}

			cancel()
			require.Eventually(t, func() bool {
				queue, err := service.ThreadQueueForConversation(pairID)
				require.NoError(t, err)

				return len(queue) == 1 && queue[0].Message == "wait"
			}, 5*time.Second, 10*time.Millisecond)
			assert.Empty(t, finals)
		})
	}
}

// Startup wakes every row's worker; a private External MCP row runs on its destination.
func TestStartActiveTurnsStartsRowWorkers(t *testing.T) {
	service := newTestSessionService(t)

	managedID, privateID := protocol.SlackThreadConversationID("C123", "111.222"), "external_mcp:planner:private"
	for _, id := range []string{managedID, privateID} {
		require.NoError(t, service.UpsertThread(id, ThreadState{Agent: "main"}))
	}

	msg := protocol.NewInboundMessage(protocol.SourceExternalMCP, protocol.InboundKindPrompt, "work", true)
	msg.SyncDestination = managedID
	require.NoError(t, startTurnDB(t.Context(), service.db, "turn-private", privateID, msg))

	unrecordedID := protocol.SlackThreadConversationID("C999", "999.000")
	require.NoError(t, startTurnDB(t.Context(), service.db, "turn-unrecorded", unrecordedID, protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "orphan", true)))

	var (
		started []string
		logs    strings.Builder
	)

	manager := newThreadBridgeManager(nil, service, slog.New(slog.NewTextHandler(&logs, nil)), func(cfg Config) directBridge {
		started = append(started, cfg.ConversationID)
		return newDirectBridgeMock()
	})
	runTestManager(t, manager)
	require.NoError(t, manager.StartActiveTurns(t.Context()))
	assert.Equal(t, []string{managedID}, started, "the destination worker owns the private row; an unrecorded conversation gets no worker")
	assert.Contains(t, logs.String(), `msg="active turn worker conversation is not recorded" component=thread_bridges conversation_id=`+unrecordedID)
}

// newTestBridgeManager returns a running manager of real bridges that publish to
// finals, and the shutdown that stops its loops.
func newTestBridgeManager(t *testing.T, cfg *config.Config, service *SessionService, finals chan *protocol.OutboundMessage) (manager *threadBridgeManager, shutdown func() error) {
	t.Helper()

	manager = newThreadBridgeManager(cfg, service, slog.New(slog.DiscardHandler), func(bridgeCfg Config) directBridge {
		bridgeCfg.SessionService, bridgeCfg.RequestRestart, bridgeCfg.StartNewThread = service, testNoopRestart, testNoopStartNewThread
		bridge := NewConversation(cfg, finalsPublisher{finals: finals}, &bridgeCfg, slog.New(slog.DiscardHandler))
		bridge.threads = manager

		return bridge
	})

	return manager, runTestManager(t, manager)
}

// newCronTestManager returns a manager for one #ops channel whose cron roots
// are recorded, plus the published finals.
func newCronTestManager(t *testing.T, service *SessionService) (manager *threadBridgeManager, cfg *config.Config, roots, finals chan *protocol.OutboundMessage) {
	t.Helper()

	cfg = &config.Config{Workspace: t.TempDir(), Slack: config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"channel-agent"}}}}}
	roots, finals = make(chan *protocol.OutboundMessage, 4), make(chan *protocol.OutboundMessage, 4)
	manager, _ = newTestBridgeManager(t, cfg, service, finals)
	manager.cronRoots = &slackFrontendMock{SendCronjobRootFunc: func(_ context.Context, message *protocol.OutboundMessage) (protocol.TextConversationTarget, error) {
		roots <- message
		return protocol.TextConversationTarget{ChannelID: "C1", MessageID: "1.2", ThreadID: "1.2"}, nil
	}}

	return manager, cfg, roots, finals
}

func seedCronRun(t *testing.T, service *SessionService, runID string) *protocol.InboundMessage {
	t.Helper()

	require.NoError(t, service.UpsertThread(runID, ThreadState{Agent: "job", CreatedBy: ThreadCreatedByCron}))

	msg := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "job prompt", false)
	msg.ConversationID, msg.RequireOutputDecision = runID, true
	msg.Cronjob = &protocol.CronjobMessage{RelativePath: "cron/daily.md", Agent: "job", RanAt: "2000-01-02T03:04:05Z"}
	msg.SlackReply = &protocol.SlackReplyTarget{ChannelID: "#ops"}
	require.NoError(t, startTurnDB(t.Context(), service.db, "turn-cron", runID, msg))

	return msg
}

// AE2: a scheduled cron row resumed after restart posts its report root once in
// the configured channel, and the new thread receives the run's history.
func TestResumedCronRowPostsRootOnce(t *testing.T) {
	service := newTestSessionService(t)
	manager, cfg, roots, finals := newCronTestManager(t, service)
	msg := seedCronRun(t, service, "cron:daily")

	crashed := NewConversation(cfg, finalsPublisher{finals: finals}, &Config{ConversationID: "cron:daily", Agent: "job", SessionService: service}, slog.New(slog.DiscardHandler))
	replay, err := replayInputForMessage("user", "job prompt")
	require.NoError(t, err)
	_, err = service.finishTurn(t.Context(), "turn-cron", &turnFinish{store: newSessionStore("cron:daily", service), entries: []rocketcode.SessionEntry{{Version: 1, Type: "turn", Timestamp: time.Now(), ReplayInput: replay}}, outbound: crashed.newOutboundMessage(msg, "turn-cron", "exact report", true)})
	require.NoError(t, err)

	require.NoError(t, manager.StartActiveTurns(t.Context()))
	assert.Equal(t, "exact report", readFinal(t, finals).Text)

	root := readFinal(t, roots)
	assert.Equal(t, "exact report", root.Text)
	assert.Equal(t, "#ops", root.SlackReply.ChannelID)
	assert.Equal(t, msg.Cronjob, root.Cronjob)

	destination := protocol.SlackThreadConversationID("C1", "1.2")

	require.Eventually(t, func() bool { return len(runningTestTurns(t, service)) == 0 }, 5*time.Second, 10*time.Millisecond)

	thread, recorded, err := service.Thread(destination)
	require.NoError(t, err)
	require.True(t, recorded)
	assert.Equal(t, ThreadState{Agent: "channel-agent", CreatedBy: ThreadCreatedByCron}, thread)

	entries, err := service.ObserveEntries(t.Context(), destination)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "the run's history is copied into the report thread")
	assert.Empty(t, roots, "the root is posted once")
}

// A failed scheduled cron run posts no report root, live or resumed.
func TestFailedCronRunPostsNoRoot(t *testing.T) {
	service := newTestSessionService(t)
	manager, cfg, roots, finals := newCronTestManager(t, service)
	msg := seedCronRun(t, service, "cron:daily")

	bridge := NewConversation(cfg, finalsPublisher{finals: finals}, &Config{ConversationID: "cron:daily", Agent: "job", SessionService: service}, slog.New(slog.DiscardHandler))
	bridge.threads = manager
	require.NoError(t, bridge.finish(t.Context(), &bridgeRequest{inbound: msg, turnID: "turn-cron"}, &turnFinish{store: newSessionStore("cron:daily", service)}, &runResult{turnID: "turn-cron", text: internalErrorResponse}, protocol.TerminalFailed))

	assert.Equal(t, internalErrorResponse, readFinal(t, finals).Text)
	assert.Empty(t, roots)
}

// AE3: an interrupted private External MCP turn, and an External MCP request
// still queued behind work, each finish after restart on the destination worker,
// and their result reaches the paired thread once.
func TestPrivateExternalMCPWorkResumesAndSyncsOnce(t *testing.T) {
	for _, stored := range []string{"row", "queued"} {
		t.Run(stored, func(t *testing.T) {
			newBridge, service, requests, finals := newResumeTestBridges(t, false)
			managedID, privateID := newBridge().config.ConversationID, "external_mcp:main:private"
			require.NoError(t, service.UpsertThread(privateID, ThreadState{Agent: "main"}))

			msg := protocol.NewInboundMessage(protocol.SourceExternalMCP, protocol.InboundKindPrompt, "support ticket", true)
			msg.ConversationID, msg.SyncDestination = privateID, managedID
			msg.Metadata = map[string]string{"external_conversation_id": "ext-1"}

			manager, _ := newTestBridgeManager(t, newBridge().runtime, service, finals)
			if stored == "row" {
				require.NoError(t, startTurnDB(t.Context(), service.db, "turn-private", privateID, msg))
				require.NoError(t, manager.StartActiveTurns(t.Context()))
			} else {
				require.NoError(t, service.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ID: "q1", ConversationID: privateID, Kind: protocol.InboundKindEnqueue, Message: msg.Text, Source: msg.Source, Inbound: msg}))
				require.NoError(t, manager.StartQueuedConversations())
			}

			byConversation := map[string]int{}

			for range 2 {
				final := readFinal(t, finals)
				assert.Equal(t, "answer", final.Text)
				byConversation[final.ConversationID]++
			}

			assert.Equal(t, map[string]int{privateID: 1, managedID: 1}, byConversation, "the result reaches the paired thread once")
			assert.Len(t, requests, 1)
			require.Eventually(t, func() bool { return len(runningTestTurns(t, service)) == 0 }, 5*time.Second, 10*time.Millisecond)

			entries, err := service.ObserveEntries(t.Context(), managedID)
			require.NoError(t, err)

			synced := 0

			for _, entry := range entries {
				if entry.Entry.Type == "turn" {
					synced++
				}
			}

			assert.Equal(t, 1, synced, "the turn is recorded once in the paired conversation")
			assert.Empty(t, finals)
		})
	}
}

// AE7: a turn waiting on ask_user_question at shutdown asks the same question
// again after restart, the answer continues the turn, and a later turn can still ask.
func TestPendingQuestionContinuesAfterRestart(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	service := newTestSessionServiceAt(t, workspace)

	bodies := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies <- string(body)

		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(string(body), "function_call_output") || !strings.Contains(string(body), "ship it?") {
			_, _ = w.Write([]byte(`{"id":"resp_2","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"answer","annotations":[]}]}]}`))
			return
		}

		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_q","name":"ask_user_question","arguments":"{\"question\":\"Ship?\",\"details\":\"\",\"options\":[],\"multiple\":false}"}]}`))
	}))
	t.Cleanup(server.Close)

	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, service.UpsertThread(conversationID, ThreadState{Agent: "main"}))

	asked, answers := make(chan string, 4), make(chan protocol.AskUserQuestionAnswer, 1)
	asker := protocol.InteractiveUserQuestionAsker(func(ctx context.Context, req *protocol.AskUserQuestionRequest) (protocol.AskUserQuestionAnswer, error) {
		asked <- req.ID

		select {
		case answer := <-answers:
			return answer, nil
		case <-ctx.Done():
			return protocol.AskUserQuestionAnswer{}, ctx.Err()
		}
	})
	finals := make(chan *protocol.OutboundMessage, 4)
	cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIKey: "test", APIBaseURL: server.URL}}
	newBridge := func() *Bridge {
		return NewConversation(cfg, finalsPublisher{finals: finals}, &Config{ConversationID: conversationID, Agent: "main", RequestRestart: testNoopRestart, StartNewThread: testNoopStartNewThread, SessionService: service, UserQuestionAsker: asker}, slog.New(slog.DiscardHandler))
	}

	first := newBridge()
	shutdown := runTestBridge(t, first)
	msg := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "ship it?", true)
	msg.SlackReply = &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.222", ThreadTS: "111.222"}
	require.NoError(t, first.Submit(t.Context(), msg))

	questionID := <-asked
	turns := runningTestTurns(t, service)
	require.Len(t, turns, 1)
	assert.Equal(t, turns[0]+"/call/call_q", questionID, "the question ID is the call's journal key")

	shutdown()
	require.Eventually(t, func() bool { return !first.handlingSnapshot() }, 5*time.Second, 10*time.Millisecond)
	assert.Empty(t, finals)

	runTestBridge(t, newBridge())
	assert.Equal(t, questionID, <-asked, "the resumed turn waits on the same question")

	answers <- protocol.AskUserQuestionAnswer{Selected: []string{"yes"}, Source: protocol.SourceSlack}

	assert.Equal(t, "answer", readFinal(t, finals).Text)
	assert.Len(t, bodies, 2, "the recorded model call is not repeated")

	for len(bodies) > 0 {
		<-bodies
	}

	later := newBridge()
	runTestBridge(t, later)

	next := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "anything else", true)
	next.SlackReply = &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.333", ThreadTS: "111.222"}
	require.NoError(t, later.Submit(t.Context(), next))
	assert.Contains(t, <-bodies, `"name":"ask_user_question"`, "a later turn can still ask")
	assert.Equal(t, "answer", readFinal(t, finals).Text)
}

// KTD10: a steer accepted but not yet injected before a restart is injected
// exactly once by the resumed turn.
func TestSteerAcceptedBeforeRestartIsInjectedOnce(t *testing.T) {
	newBridge, service, requests, finals := newResumeTestBridges(t, true)
	first := newBridge()
	conversationID := first.config.ConversationID
	shutdown := runTestBridge(t, first)

	msg := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "hello", true)
	require.NoError(t, first.Submit(t.Context(), msg))
	<-requests

	steer := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindSteer, "also check the logs", true)
	require.NoError(t, first.Submit(t.Context(), steer))
	turns := runningTestTurns(t, service)
	require.Len(t, turns, 1)
	assert.Contains(t, testTurnStepKeys(t, service, conversationID), turns[0]+"/steers", "the accepted steer is recorded under its turn")

	shutdown()
	require.Eventually(t, func() bool { return !first.handlingSnapshot() }, 5*time.Second, 10*time.Millisecond)

	late := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindSteer, "and the metrics", true)
	require.NoError(t, first.Submit(t.Context(), late))

	queued, err := service.ThreadQueueForConversation(conversationID)
	require.NoError(t, err)
	assert.Empty(t, queued, "the late steer is not a new queued turn")

	runTestBridge(t, newBridge())
	assert.Contains(t, readFinal(t, finals).Text, "answer")

	injected := map[string]int{}

	for len(requests) > 0 {
		body := <-requests
		for _, text := range []string{"also check the logs", "and the metrics"} {
			if strings.Contains(body, text) {
				injected[text]++

				assert.Equal(t, 1, strings.Count(body, text))
			}
		}
	}

	assert.Positive(t, injected["also check the logs"], "the resumed turn injects the steer")
	assert.Positive(t, injected["and the metrics"], "the resumed turn injects the steer sent during shutdown")
	entries, err := service.ObserveEntries(t.Context(), conversationID)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	recorded, err := json.Marshal(entries[0].Entry.ReplayInput)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(recorded), "also check the logs"), "the steer is in history once")
	assert.Equal(t, 1, strings.Count(string(recorded), "and the metrics"), "the shutdown steer is in history once")
}

// R21, R19: a resumed turn that fails posts the normal internal-error final,
// and nothing it posts mentions the restart.
func TestResumedTurnFailurePostsInternalErrorFinal(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	service := newTestSessionServiceAt(t, workspace)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request","type":"invalid_request_error"}}`))
	}))
	t.Cleanup(server.Close)

	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, service.UpsertThread(conversationID, ThreadState{Agent: "main"}))

	msg := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "hello", true)
	msg.ConversationID, msg.SlackReply = conversationID, &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.222", ThreadTS: "111.222"}
	require.NoError(t, startTurnDB(t.Context(), service.db, "turn-resumed", conversationID, msg))

	outbound := make(chan *protocol.OutboundMessage, 16)
	cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIKey: "test", APIBaseURL: server.URL}}
	publisher := &outboundPublisherMock{PublishOutboundFunc: func(_ context.Context, message *protocol.OutboundMessage) error {
		message.MarkDelivered(nil)

		outbound <- message

		return nil
	}}
	runTestBridge(t, NewConversation(cfg, publisher, &Config{ConversationID: conversationID, Agent: "main", RequestRestart: testNoopRestart, StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.DiscardHandler)))

	for {
		message := readFinal(t, outbound)
		assert.NotContains(t, strings.ToLower(message.Text), "restart")

		if message.Complete {
			assert.True(t, strings.HasPrefix(message.Text, internalErrorResponse), message.Text)
			assert.Equal(t, "turn-resumed", message.TurnID)

			break
		}
	}
}

// A resumed turn does not post its web input's consume card a second time.
func TestResumedTurnPostsConsumeCardOnce(t *testing.T) {
	newBridge, service, requests, _ := newResumeTestBridges(t, true)
	template := newBridge()

	consumed, finals := make(chan string, 4), make(chan *protocol.OutboundMessage, 4)
	publisher := &outboundPublisherMock{PublishOutboundFunc: func(_ context.Context, message *protocol.OutboundMessage) error {
		message.MarkDelivered(nil)

		if message.ConsumedID != "" {
			consumed <- message.ConsumedID
		}

		if message.Complete {
			finals <- message
		}

		return nil
	}}
	bridge := func() *Bridge {
		return NewConversation(template.runtime, publisher, &template.config, slog.New(slog.DiscardHandler))
	}

	first := bridge()
	shutdown := runTestBridge(t, first)
	msg := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindPrompt, &protocol.InboundContent{Text: "hello"}, true)
	msg.Metadata["web_message_id"] = "web-1"
	require.NoError(t, first.Submit(t.Context(), msg))
	<-requests
	assert.Equal(t, "web-1", <-consumed)

	shutdown()
	require.Eventually(t, func() bool { return !first.handlingSnapshot() }, 5*time.Second, 10*time.Millisecond)
	require.Len(t, runningTestTurns(t, service), 1)

	runTestBridge(t, bridge())
	assert.Equal(t, "answer", readFinal(t, finals).Text)
	assert.Empty(t, consumed, "the consume card is posted once")
}

// A row runs before a live message that reaches the conversation as soon as
// connectors accept input.
func TestRowRunsBeforeLiveMessage(t *testing.T) {
	newBridge, service, _, finals := newResumeTestBridges(t, false)
	bridge := newBridge()
	conversationID := bridge.config.ConversationID
	seedActiveTurn(t, service, conversationID, "turn-waiting", nil)

	live := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "live", true)
	require.NoError(t, bridge.Submit(t.Context(), live))
	runTestBridge(t, bridge)

	assert.Equal(t, "turn-waiting", readFinal(t, finals).TurnID, "the row is the queue head")
	assert.NotEqual(t, "turn-waiting", readFinal(t, finals).TurnID)
}

// An output-decision retry interrupted mid-way resumes that retry without
// repeating the finished first turn, and delivers once.
func TestInterruptedOutputDecisionRetryResumes(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	service := newTestSessionServiceAt(t, workspace)

	requests, blockRetry := make(chan string, 8), make(chan struct{}, 1)
	blockRetry <- struct{}{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- string(body)

		output := `{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"undecided","annotations":[]}]}`

		switch {
		case strings.Contains(string(body), "function_call_output"):
			output = `{"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"done","annotations":[]}]}`
		case strings.Contains(string(body), "You did not call the mandatory"):
			select {
			case <-blockRetry:
				<-r.Context().Done()
				return
			default:
			}

			output = `{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_d","name":"` + rawRunToolName + `","arguments":"{\"payload\":\"report\"}"}`
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[` + output + `]}`))
	}))
	t.Cleanup(server.Close)

	conversationID := "cron:daily"
	require.NoError(t, service.UpsertThread(conversationID, ThreadState{Agent: "main"}))

	finals := make(chan *protocol.OutboundMessage, 4)
	cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIKey: "test", APIBaseURL: server.URL}}
	newBridge := func() *Bridge {
		return NewConversation(cfg, finalsPublisher{finals: finals}, &Config{ConversationID: conversationID, Agent: "main", RequestRestart: testNoopRestart, StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.DiscardHandler))
	}

	first := newBridge()
	shutdown := runTestBridge(t, first)
	msg := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "run the job", false)
	msg.RequireOutputDecision = true
	require.NoError(t, first.Submit(t.Context(), msg))
	<-requests
	assert.Contains(t, <-requests, "You did not call the mandatory", "the retry is in flight")

	shutdown()
	require.Eventually(t, func() bool { return !first.handlingSnapshot() }, 5*time.Second, 10*time.Millisecond)
	assert.Empty(t, finals)

	runTestBridge(t, newBridge())
	final := readFinal(t, finals)
	assert.Equal(t, "report", final.Text)

	resumed := 0

	for len(requests) > 0 {
		assert.Contains(t, <-requests, "You did not call the mandatory", "the finished first turn is not repeated")

		resumed++
	}

	assert.Equal(t, 2, resumed, "the retry resumes: its model call and its post-decision call")
	require.Eventually(t, func() bool { return len(runningTestTurns(t, service)) == 0 }, 5*time.Second, 10*time.Millisecond)
	assert.Empty(t, finals, "the result is delivered once")
}

// A cron turn mid-flight does not hold up shutdown: once bridges stop, the cron
// runner's RunTurn and its follow-up sync return, so cron manager Stop does not block.
func TestShutdownReleasesCronRunnerWait(t *testing.T) {
	newBridge, service, requests, finals := newResumeTestBridges(t, true)
	template := newBridge()
	rt := &Runtime{Sessions: service, Cfg: template.runtime, Log: slog.New(slog.DiscardHandler)}

	var shutdown func() error

	rt.threads, shutdown = newTestBridgeManager(t, template.runtime, service, finals)
	require.NoError(t, service.UpsertThread("cron:daily", ThreadState{Agent: "main", CreatedBy: ThreadCreatedByCron}))

	inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "job prompt", false)
	inbound.ConversationID, inbound.SyncDestination, inbound.RequireOutputDecision = "cron:daily", template.config.ConversationID, true

	done := make(chan error, 1)

	go func() {
		errRun := rt.RunTurn(t.Context(), inbound)
		done <- errors.Join(errRun, rt.SyncConversation(context.WithoutCancel(t.Context()), "cron:daily", template.config.ConversationID))
	}()

	<-requests

	require.NoError(t, service.UpsertThread("cron:weekly", ThreadState{Agent: "main", CreatedBy: ThreadCreatedByCron}))

	destination, err := rt.recordedBridge(template.config.ConversationID)
	require.NoError(t, err)

	synced := make(chan error, 1)

	go func() {
		synced <- rt.SyncConversation(context.WithoutCancel(t.Context()), "cron:weekly", template.config.ConversationID)
	}()

	require.Eventually(t, func() bool { return len(destination.requestCh) == 1 }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, shutdown())

	for _, waiting := range []chan error{done, synced} {
		select {
		case err := <-waiting:
			require.ErrorIs(t, err, protocol.ErrBridgeStopped)
		case <-time.After(5 * time.Second):
			t.Fatal("the cron runner still waits after bridges stopped")
		}
	}

	assert.Len(t, runningTestTurns(t, service), 1, "the cron row resumes after restart")
	assert.Empty(t, finals)

	late := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "late job prompt", false)
	late.ConversationID, late.SyncDestination = "cron:daily", template.config.ConversationID
	require.ErrorIs(t, rt.RunTurn(t.Context(), late), protocol.ErrBridgeStopped)

	queue, err := service.ThreadQueueForConversation("cron:daily")
	require.NoError(t, err)
	require.Len(t, queue, 1)
	assert.Equal(t, "late job prompt", queue[0].Message)

	destinationQueue, err := service.ThreadQueueForConversation(template.config.ConversationID)
	require.NoError(t, err)
	assert.Empty(t, destinationQueue)
}

// U7, AE4: a saved workflow interrupted by shutdown after two finished phases and
// one of two parallel workers resumes without re-running any finished worker. Its
// summary matches an uninterrupted run, and $stop on the resumed run still stops it.
func TestInterruptedWorkflowResumesWithRecordedWorkers(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "stop"}[stop], func(t *testing.T) {
			workspace := t.TempDir()
			writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
			require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
			require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "workflows"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(workspace, ".rocketclaw", "workflows", "audit.star"), []byte(`meta = {"name": "audit", "description": "Audit", "phases": ["one", "two", "three"]}
def main(args):
    a = phase("one", lambda: agent("worker-a"))
    b = phase("two", lambda: agent("worker-b"))
    c, d = phase("three", lambda: parallel([lambda: agent("worker-c"), lambda: agent("worker-d")]))
    return a + b + c + d
`), 0o600))
			service := newTestSessionServiceAt(t, workspace)

			var mu sync.Mutex

			calls := map[string]int{}
			blockedD := make(chan struct{}, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				worker := ""

				for _, name := range []string{"a", "b", "c", "d"} {
					if strings.Contains(string(body), "worker-"+name) {
						worker = name
					}
				}

				mu.Lock()
				calls[worker]++
				block := worker == "d" && (calls[worker] == 1 || stop)
				mu.Unlock()

				if block {
					blockedD <- struct{}{}

					<-r.Context().Done()

					return
				}

				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"` + strings.ToUpper(worker) + `","annotations":[]}]}]}`))
			}))
			t.Cleanup(server.Close)

			conversationID := protocol.SlackThreadConversationID("C123", "111.222")
			require.NoError(t, service.UpsertThread(conversationID, ThreadState{Agent: "main"}))

			finals := make(chan *protocol.OutboundMessage, 4)
			cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIKey: "test", APIBaseURL: server.URL}}
			newBridge := func() *Bridge {
				return NewConversation(cfg, finalsPublisher{finals: finals}, &Config{ConversationID: conversationID, Agent: "main", RequestRestart: testNoopRestart, StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.DiscardHandler))
			}
			submit := func(bridge *Bridge) {
				msg := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "$workflow audit", true)
				msg.Workflow = protocol.WorkflowInvocation{Name: "audit"}
				require.NoError(t, bridge.Submit(t.Context(), msg))
			}
			workerCalls := func() map[string]int {
				mu.Lock()
				defer mu.Unlock()

				return maps.Clone(calls)
			}

			first := newBridge()
			shutdown := runTestBridge(t, first)
			submit(first)
			<-blockedD

			turns := runningTestTurns(t, service)
			require.Len(t, turns, 1)
			turnID := turns[0]

			require.Eventually(t, func() bool {
				return slices.Contains(testTurnStepKeys(t, service, conversationID), turnID+"/workflow/2/0/0")
			}, 5*time.Second, 10*time.Millisecond, "the finished parallel worker is recorded under its branch key")

			shutdown()
			require.Eventually(t, func() bool { return !first.handlingSnapshot() }, 5*time.Second, 10*time.Millisecond)
			assert.Empty(t, finals, "shutdown posts nothing")
			entries, err := service.ObserveEntries(t.Context(), conversationID)
			require.NoError(t, err)
			assert.Empty(t, entries, "shutdown writes no workflow summary")
			require.Equal(t, []string{turnID}, runningTestTurns(t, service))
			assert.True(t, slices.ContainsFunc(testTurnStepKeys(t, service, conversationID), func(key string) bool {
				return strings.HasPrefix(key, turnID+"/workflow/2/1/0/")
			}), "the in-flight worker's turn is journaled under its key")

			restarted := newBridge()
			runTestBridge(t, restarted)

			if stop {
				<-blockedD
				require.NotNil(t, restarted.InterruptActiveTurn())

				final := readFinal(t, finals)
				assert.Equal(t, protocol.TerminalStopped, final.WorkflowTerminal)
				assert.Equal(t, map[string]int{"a": 1, "b": 1, "c": 1, "d": 2}, workerCalls(), "finished workers are not repeated")

				require.Eventually(t, func() bool {
					entries, err = service.ObserveEntries(t.Context(), conversationID)
					return err == nil && len(entries) == 1
				}, 5*time.Second, 10*time.Millisecond)

				summary := workflowSummaryFromEntry(t, &entries[0].Entry)
				assert.Equal(t, protocol.TerminalStopped, summary.Terminal)
				assert.Equal(t, "workflow stopped by user", summary.Error)

				return
			}

			final := readFinal(t, finals)
			assert.Equal(t, "ABCD", final.Text)
			assert.Equal(t, turnID, final.TurnID, "the resumed run keeps its run ID")
			assert.Equal(t, map[string]int{"a": 1, "b": 1, "c": 1, "d": 2}, workerCalls(), "only the interrupted worker runs again")

			submit(restarted)

			uninterrupted := readFinal(t, finals)
			assert.Equal(t, final.Text, uninterrupted.Text)
			assert.Equal(t, map[string]int{"a": 2, "b": 2, "c": 2, "d": 3}, workerCalls())

			require.Eventually(t, func() bool {
				entries, err = service.ObserveEntries(t.Context(), conversationID)
				return err == nil && len(entries) == 2
			}, 5*time.Second, 10*time.Millisecond)

			resumed := workflowSummaryPayloadFromEntry(t, &entries[0].Entry)
			assert.JSONEq(t, strings.ReplaceAll(workflowSummaryPayloadFromEntry(t, &entries[1].Entry), uninterrupted.TurnID, turnID), resumed)
			assert.JSONEq(t, `{"workflow":"audit","run_id":"`+turnID+`","terminal":"complete","phases":[{"name":"one","status":"complete","scheduled":1,"complete":1},{"name":"two","status":"complete","scheduled":1,"complete":1},{"name":"three","status":"complete","scheduled":2,"complete":2}]}`, resumed)
			require.Eventually(t, func() bool { return len(testTurnStepKeys(t, service, conversationID)) == 0 }, 5*time.Second, 10*time.Millisecond, "the finished runs leave no journal steps")
		})
	}
}
