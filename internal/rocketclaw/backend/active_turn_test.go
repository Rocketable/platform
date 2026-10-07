package backend

import (
	"bytes"
	"context"
	"encoding/base64"
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
	migrate "github.com/rubenv/sql-migrate"
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

	queued := []protocol.OutboundAttachment{{ID: "queued-report", Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}}
	require.NoError(t, service.SaveAttachment(t.Context(), conversationID, &queued[0], false))
	data, err := json.Marshal(queued)
	require.NoError(t, err)
	require.NotContains(t, string(data), base64.StdEncoding.EncodeToString(queued[0].Data), "journaled attachments keep only their stored ID")
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
	outbound.Attachments = []protocol.OutboundAttachment{{ID: "finished-report", Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}}
	require.NoError(t, service.SaveAttachment(t.Context(), conversationID, &outbound.Attachments[0], false))
	outbound.ReplyState = json.RawMessage(`{"ChannelID":"C456","MessageTS":"777.2"}`)

	require.NoError(t, service.SaveTurnStep(t.Context(), conversationID, protocol.ReplyStepKey("turn-finished"), []byte(`{"ChannelID":"C123","MessageTS":"555.1"}`)))
	_, err := service.finishTurn(t.Context(), "turn-finished", &turnFinish{store: newSessionStore(conversationID, service), accountGoal: true, outbound: outbound})
	require.NoError(t, err)

	var persisted string
	require.NoError(t, service.db.QueryRowContext(t.Context(), `SELECT outbound_json::text FROM active_turns WHERE id = 'turn-finished'`).Scan(&persisted))
	assert.NotContains(t, persisted, base64.StdEncoding.EncodeToString(outbound.Attachments[0].Data), "a finished turn keeps only attachment IDs, so large files fit")

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

func TestFinishTurnKeepsLargeAttachment(t *testing.T) {
	service := newTestSessionService(t)
	inbound := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "make a file", true)
	inbound.ConversationID = "web:attachment"
	require.NoError(t, startTurnDB(t.Context(), service.db, "attachment-turn", inbound.ConversationID, inbound))

	outbound := protocol.NewOutboundMessage(inbound.ConversationID, "file ready")
	// 192 MiB in base64 exceeds jsonb's string limit; the finished turn stores the attachment ID and reloads the bytes.
	outbound.Attachments = []protocol.OutboundAttachment{{ID: "large-file", Name: "file.bin", Data: bytes.Repeat([]byte{0x61}, 192<<20)}}
	require.NoError(t, service.SaveAttachment(t.Context(), inbound.ConversationID, &outbound.Attachments[0], false))

	require.NoError(t, service.SaveTurnStep(t.Context(), inbound.ConversationID, protocol.ReplyStepKey("attachment-turn"), []byte(`{"ChannelID":"C789","MessageTS":"888.3"}`)))

	_, err := service.finishTurn(t.Context(), "attachment-turn", &turnFinish{store: newSessionStore(inbound.ConversationID, service), outbound: outbound})
	require.NoError(t, err)
	stored, found, err := service.headTurn(t.Context(), inbound.ConversationID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, turnDelivering, stored.phase)
	assert.Equal(t, outbound.Text, stored.outbound.Text)
	require.Len(t, stored.outbound.Attachments, 1)
	assert.Equal(t, outbound.Attachments[0].Name, stored.outbound.Attachments[0].Name)
	assert.True(t, bytes.Equal(outbound.Attachments[0].Data, stored.outbound.Attachments[0].Data))
	assert.JSONEq(t, `{"ChannelID":"C789","MessageTS":"888.3"}`, string(stored.outbound.ReplyState))
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
		return NewConversation(cfg, finalsPublisher{finals: finals}, &bridgeCfg, slog.New(slog.DiscardHandler))
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
	owner, _, err := (stateDAO{db: service.db}).producer(t.Context(), "cron:daily")
	require.NoError(t, err)
	assert.Equal(t, destination, owner.SyncDestination, "the delivered root survives turn close")
	_, err = manager.switchConversationAgent(destination, "selected")
	require.NoError(t, err)
	data, err := json.Marshal(protocol.ScheduledMessageState{ConversationID: "cron:daily", Agent: "job", Message: "later", DueAt: time.Now().Add(time.Hour)})
	require.NoError(t, err)
	_, err = service.AppendEntryID(t.Context(), "cron:daily", &rocketcode.SessionEntry{Version: 1, Type: producerScheduleEntryType, Timestamp: time.Now(), OutputTrace: []json.RawMessage{data}})
	require.NoError(t, err)
	require.NoError(t, manager.StartPendingScheduledMessages())

	pending, err := service.ScheduledMessagesForConversation(destination)
	require.NoError(t, err)
	require.Len(t, pending, 1)

	for _, message := range pending {
		assert.Equal(t, "selected", message.Agent, "follow-ups reuse the selected original report conversation")
	}

	assert.Empty(t, roots, "recovery reuses the delivered root")
}

func TestLegacyCronScheduleResumesIntoCanonical(t *testing.T) {
	for _, owner := range []string{"", "slack-thread:C1:1.2"} {
		t.Run(owner, func(t *testing.T) {
			newBridge, store, calls, finals := newResumeTestBridges(t, false)
			runtime := newBridge().runtime
			writeAgent(t, runtime.Workspace, "a-selected", "---\ndescription: Selected\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nSelected canonical instructions\n")
			writeAgent(t, runtime.Workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission:\n  rocketclaw: allow\n---\nProducer instructions\n")

			destination := owner
			if destination == "" {
				destination = "web:cron:legacy"
			}

			if owner != "" {
				require.NoError(t, (&Runtime{Sessions: store}).CreateConversation(t.Context(), protocol.Conversation{ID: destination, Agent: "a-selected"}))
				_, err := store.AppendEntryID(t.Context(), destination, testSessionEntry("canonical history", "canonical answer"))
				require.NoError(t, err)
			}

			// Back to before 027_producer_handoff and the two migrations that follow it.
			_, err := (migrate.MigrationSet{TableName: "pg_migrations"}).ExecMaxContext(t.Context(), store.db, "postgres", migrate.EmbedFileSystemMigrationSource{FileSystem: sessionDBMigrations, Root: "migrations"}, migrate.Down, 3)
			require.NoError(t, err)
			require.NoError(t, store.UpsertThread("cron:legacy", ThreadState{Agent: "main", CreatedBy: ThreadCreatedByCron}))

			inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "original legacy job", false)
			inbound.ConversationID, inbound.RequireOutputDecision = "cron:legacy", true
			inbound.Cronjob = &protocol.CronjobMessage{RelativePath: "cron/missing.md", Agent: "main"}
			data, err := json.Marshal(inbound)
			require.NoError(t, err)
			_, err = store.db.ExecContext(t.Context(), `INSERT INTO active_turns (id, conversation_id, inbound_json, output_trace_json, history_anchor_id, created_at_unix_ns, updated_at_unix_ns, phase) VALUES ('legacy-turn', 'cron:legacy', $1, '[]', 0, 1, 1, 'running')`, string(data))
			require.NoError(t, err)

			if owner != "" {
				// This legacy delivery fact authorizes the existing destination.
				require.NoError(t, store.SaveTurnStep(t.Context(), "cron:legacy", "legacy-turn/cron-root", []byte(`{"ChannelID":"C1","MessageID":"1.2"}`)))
			}

			code := "def main():\n    return rocketclaw_schedule_message(message=\"legacy follow up\", send_this_in=\"1h\", recurring=True)\n"
			arguments, err := json.Marshal(struct {
				Code string `json:"code"`
			}{Code: code})
			require.NoError(t, err)
			call, err := json.Marshal(struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Type: "function_call", ID: "fc_legacy", CallID: "legacy_schedule", Name: "execute", Arguments: string(arguments)})
			require.NoError(t, err)
			// Seed the actual provider/tool journal shape, not a producer effect.
			replay, err := replayInputForMessage("user", "original legacy job")
			require.NoError(t, err)

			record := rocketcode.SessionEntry{Version: 1, Type: "turn", TurnID: "legacy-turn", Timestamp: time.Now(), ReplayInput: append(replay, call), OutputTrace: []json.RawMessage{call}}
			data, err = json.Marshal(struct {
				Record   rocketcode.SessionEntry `json:"record"`
				Response json.RawMessage         `json:"response"`
			}{Record: record, Response: json.RawMessage(`{"id":"resp_legacy","object":"response","status":"completed","output":[` + string(call) + `]}`)})
			require.NoError(t, err)
			require.NoError(t, store.SaveTurnStep(t.Context(), "cron:legacy", "legacy-turn", data))
			require.NoError(t, store.SaveTurnStep(t.Context(), "cron:legacy", "legacy-turn/call/legacy_schedule", []byte(`{"output":[{"type":"function_call_output","call_id":"legacy_schedule","output":"legacy scheduling result"}]}`)))

			legacy := protocol.ScheduledMessageState{ConversationID: "cron:legacy", Agent: "main", Message: "legacy follow up", DueAt: time.Now().Add(time.Hour).UTC(), Recurring: true, Interval: time.Hour}
			require.NoError(t, store.PutScheduledMessage("legacy-id", &legacy))
			require.NoError(t, initializeSessionDB(t.Context(), store.db, slog.New(slog.DiscardHandler)))

			// Finish the resumed job with an empty (silent) reply; scheduling must replay.
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				w.Header().Set("Content-Type", "application/json")

				calls <- string(body)

				switch {
				case strings.Contains(string(body), "Selected canonical instructions"):
					writeRawRunMessage(t, w, "followup", "followup", "canonical answer")
				default:
					writeRawRunMessage(t, w, "done", "done", "")
				}
			}))
			t.Cleanup(server.Close)
			runtime.OpenAI.APIBaseURL = server.URL
			manager, _ := newTestBridgeManager(t, runtime, store, finals)
			roots := make(chan *protocol.OutboundMessage, 1)
			manager.cronRoots = &slackFrontendMock{SendCronjobRootFunc: func(_ context.Context, message *protocol.OutboundMessage) (protocol.TextConversationTarget, error) {
				roots <- message
				return protocol.TextConversationTarget{ChannelID: "C1", ThreadID: "1.2"}, nil
			}}
			require.NoError(t, manager.StartActiveTurns(t.Context()))
			original := readFinal(t, finals)
			assert.Equal(t, "cron:legacy", original.ConversationID)
			assert.Empty(t, original.Text, "the original silent report remains private")
			require.Eventually(t, func() bool { return len(runningTestTurns(t, store)) == 0 }, 5*time.Second, 10*time.Millisecond)
			// Finish the completion handoff before inspecting its pre-admission state.
			require.NoError(t, manager.PickLaterWork(t.Context(), "cron:legacy"))

			if owner == "" {
				_, recorded, err := store.Thread(destination)
				require.NoError(t, err)
				assert.False(t, recorded, "legacy silent work creates Web only when due")
			} else {
				legacy.ConversationID, legacy.Agent = destination, "a-selected"
			}

			messages, err := store.ScheduledMessagesForConversation(legacy.ConversationID)
			require.NoError(t, err)
			require.Equal(t, map[string]protocol.ScheduledMessageState{"legacy-id": legacy}, messages, "handoff preserves the row and timestamps exactly")
			legacy.DueAt = time.Now().Add(-time.Minute).UTC()
			require.NoError(t, store.PutScheduledMessage("legacy-id", &legacy))
			require.NoError(t, manager.StartPendingScheduledMessages())

			if owner != "" {
				require.NoError(t, manager.PickLaterWork(t.Context(), destination))
			}

			followup := readFinal(t, finals)
			assert.Equal(t, destination, followup.ConversationID)
			assert.Equal(t, "a-selected", followup.Agent)
			assert.Nil(t, followup.SlackReply)
			assert.Nil(t, followup.Cronjob)
			require.Eventually(t, func() bool { return len(runningTestTurns(t, store)) == 0 }, 5*time.Second, 10*time.Millisecond)

			var canonicalBody string

			for len(calls) > 0 {
				body := <-calls
				if strings.Contains(body, "Selected canonical instructions") {
					canonicalBody = body
				}
			}

			if owner != "" {
				assert.Contains(t, canonicalBody, "canonical history")
			}

			assert.Contains(t, canonicalBody, "original legacy job")

			var request struct {
				Input []json.RawMessage `json:"input"`
			}
			require.NoError(t, json.Unmarshal([]byte(canonicalBody), &request))
			prompts, err := replayInputMessages(request.Input)
			require.NoError(t, err)
			require.NotEmpty(t, prompts)
			assert.Equal(t, "[System additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\nlegacy follow up", prompts[len(prompts)-1].text)

			messages, err = store.ScheduledMessagesForConversation(destination)
			require.NoError(t, err)
			require.Len(t, messages, 1)
			assert.True(t, messages["legacy-id"].DueAt.After(legacy.DueAt), "ordinary recurring admission advances the preserved schedule")
			legacy.ConversationID, legacy.Agent, legacy.DueAt = destination, "a-selected", messages["legacy-id"].DueAt
			assert.Equal(t, legacy, messages["legacy-id"], "ID and recurring cadence survive rehoming")

			private, err := store.ScheduledMessagesForConversation("cron:legacy")
			require.NoError(t, err)
			assert.Empty(t, private)
			effects, err := (stateDAO{db: store.db}).producerEffects(t.Context(), "cron:legacy", 0)
			require.NoError(t, err)
			assert.Empty(t, effects, "replayed scheduling output must not create a new intent")
			assert.Empty(t, roots, "neither silent Web nor retained delivery creates another Slack root")
		})
	}
}

func TestSilentCronLiveToolKeepsSchedulesPrivateUntilDue(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "job", "---\ndescription: Job\nmode: primary\nmodel: gpt-5.5\npermission:\n  rocketclaw: allow\n---\nProducer instructions\n")
	writeAgent(t, workspace, "canonical", "---\ndescription: Canonical\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nCanonical instructions\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.MkdirAll(".rocketclaw/cron", 0o755))
	require.NoError(t, root.WriteFile(".rocketclaw/cron/daily.md", []byte("---\nschedule: 1h\nagent: job\nchannel: '#current'\n---\nJob\n"), 0o600))
	service := newTestSessionServiceAt(t, workspace)
	finals := make(chan *protocol.OutboundMessage, 8)
	requests := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(string(body), "Canonical instructions") {
			entries, err := service.ObserveEntries(r.Context(), "web:cron:daily")
			assert.NoError(t, err)
			assert.NotEmpty(t, entries, "canonical history is projected before the model runs")

			requests <- string(body)

			writeRawRunMessage(t, w, "followup", "followup", "canonical answer")

			return
		}

		if strings.Contains(string(body), "function_call_output") {
			writeRawRunMessage(t, w, "done", "done", "") // A silent reply.
			return
		}

		writeRawRunFunctionCall(t, w, "schedule", "execute", struct {
			Code string `json:"code"`
		}{Code: "def main():\n    rocketclaw_schedule_message(message=\"follow up\", send_this_in=\"59m\", recurring=False)\n    rocketclaw_schedule_message(message=\"second follow up\", send_this_in=\"59m200ms\", recurring=False)\n    rocketclaw_schedule_message(message=\"recurring follow up\", send_this_in=\"1h\", recurring=True)\n    return \"\"\n"})
	}))
	t.Cleanup(server.Close)
	cfg := &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIKey: "test", APIBaseURL: server.URL}, Slack: config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"job"}}, {Channel: "#current", Agents: []string{"not-loaded", "canonical", "job"}}}}}
	manager, shutdown := newTestBridgeManager(t, cfg, service, finals)
	roots := make(chan *protocol.OutboundMessage, 4)
	manager.cronRoots = &slackFrontendMock{SendCronjobRootFunc: func(_ context.Context, message *protocol.OutboundMessage) (protocol.TextConversationTarget, error) {
		roots <- message
		return protocol.TextConversationTarget{ChannelID: "C1", ThreadID: "1.2"}, nil
	}}

	require.NoError(t, service.UpsertThread("cron:daily", ThreadState{Agent: "job", CreatedBy: ThreadCreatedByCron}))

	bridge, err := manager.recordedBridge("cron:daily")
	require.NoError(t, err)

	msg := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "run job", false)
	msg.RequireOutputDecision = true
	msg.Cronjob = &protocol.CronjobMessage{RelativePath: "cron/daily.md", Agent: "job"}
	msg.SlackReply = &protocol.SlackReplyTarget{ChannelID: "#ops"}
	require.NoError(t, bridge.Submit(t.Context(), msg))
	assert.Empty(t, readFinal(t, finals).Text)
	require.Eventually(t, func() bool { return len(runningTestTurns(t, service)) == 0 }, 5*time.Second, 10*time.Millisecond)

	scheduled, err := service.ScheduledMessagesForConversation("cron:daily")
	require.NoError(t, err)
	require.Empty(t, scheduled, "a real silent cron tool call must not create executable private work")
	effects, err := (stateDAO{db: service.db}).producerEffects(t.Context(), "cron:daily", 0)
	require.NoError(t, err)
	require.Len(t, effects, 3)

	// The live tool schedules beyond the test timeout, so HTTP/database latency
	// cannot make correct due-time Web creation fail this pre-due assertion.
	_, recorded, err := service.Thread("web:cron:daily")
	require.NoError(t, err)
	require.False(t, recorded, "Web creation waits until due")

	// Entries are append-only through SessionService. Update only the original
	// one-shot payloads here to control due data, not the production clock;
	// preserve their IDs/order and leave the recurring effect unchanged.
	due := time.Now().Add(time.Second)

	for i := range 2 {
		var scheduled protocol.ScheduledMessageState
		require.NoError(t, json.Unmarshal(effects[i].Entry.OutputTrace[0], &scheduled))
		scheduled.DueAt = due.Add(time.Duration(i) * 200 * time.Millisecond)
		data, err := json.Marshal(scheduled)
		require.NoError(t, err)
		_, err = service.db.ExecContext(t.Context(), `UPDATE session_entries SET entry_json = jsonb_set(entry_json::jsonb, '{output_trace,0}', $2::jsonb)::json WHERE id = $1`, effects[i].ID, string(data))
		require.NoError(t, err)
	}

	require.NoError(t, manager.PickLaterWork(t.Context(), "cron:daily"))

	remaining := []string{"follow up", "second follow up"}
	// Concurrent claims are deferred: two finals need not mean two distinct prompts.
	require.Eventually(t, func() bool {
		for len(finals) > 0 {
			final := readFinal(t, finals)
			assert.Equal(t, "web:cron:daily", final.ConversationID)
			assert.Equal(t, "canonical", final.Agent)
			assert.Equal(t, "canonical answer", final.Text)
			assert.Nil(t, final.SlackReply, "no inherited cron channel target")
			assert.Nil(t, final.Cronjob, "follow-ups are ordinary scheduled turns")

			body := <-requests

			var request struct {
				Input []json.RawMessage `json:"input"`
			}

			require.NoError(t, json.Unmarshal([]byte(body), &request))
			messages, err := replayInputMessages(request.Input)
			require.NoError(t, err)
			require.NotEmpty(t, messages)
			first := slices.IndexFunc(messages, func(message replayInputMessage) bool { return message.role == "user" })
			require.NotEqual(t, -1, first)
			_, original, framed := strings.Cut(messages[first].text, "\n\n")
			assert.True(t, framed)
			assert.Equal(t, "run job", original, "the scheduled model sees canonical history")

			current := messages[len(messages)-1]
			assert.Equal(t, "user", current.role)
			header, prompt, framed := strings.Cut(current.text, "\n\n")
			assert.True(t, framed)
			assert.Equal(t, `[System additional_instructions="Reply in plain text suitable for Slack. Avoid markdown unless it is necessary."]`, header)
			assert.Contains(t, []string{"follow up", "second follow up"}, prompt)
			t.Logf("scheduled prompt %q, turn %q", prompt, final.TurnID)

			remaining = slices.DeleteFunc(remaining, func(expected string) bool { return expected == prompt })
		}

		if len(remaining) > 0 {
			return false
		}

		canonical, err := manager.recordedBridge("web:cron:daily")
		require.NoError(t, err)

		return len(canonical.requestCh) == 0 && !canonical.handlingSnapshot() && len(runningTestTurns(t, service)) == 0 && len(finals) == 0
	}, 10*time.Second, 10*time.Millisecond)
	require.Empty(t, requests, "all one-shot requests and finals are consumed before restart")

	assert.Empty(t, roots, "silent fallback never creates a Slack root")
	inbound, through, err := (stateDAO{db: service.db}).producer(t.Context(), "cron:daily")
	require.NoError(t, err)
	assert.Equal(t, "web:cron:daily", inbound.SyncDestination)
	assert.Equal(t, effects[len(effects)-1].ID, through)

	pending, err := service.ScheduledMessagesForConversation("web:cron:daily")
	require.NoError(t, err)
	require.Len(t, pending, 1)

	var recurringEffect protocol.ScheduledMessageState
	require.NoError(t, json.Unmarshal(effects[2].Entry.OutputTrace[0], &recurringEffect))

	for _, message := range pending {
		assert.True(t, message.Recurring)
		assert.Equal(t, "canonical", message.Agent)
		assert.Equal(t, time.Hour, message.Interval)
		assert.True(t, message.DueAt.Equal(recurringEffect.DueAt), "handoff preserves the recurring due time")
	}

	require.NoError(t, shutdown())

	for id, message := range pending {
		message.Agent, message.DueAt = "job", time.Now().Add(-time.Second)
		require.NoError(t, service.PutScheduledMessage(id, &message))
	}

	manager, _ = newTestBridgeManager(t, cfg, service, finals)
	manager.cronRoots = &slackFrontendMock{SendCronjobRootFunc: func(_ context.Context, message *protocol.OutboundMessage) (protocol.TextConversationTarget, error) {
		roots <- message
		return protocol.TextConversationTarget{ChannelID: "C1", ThreadID: "1.2"}, nil
	}}
	require.NoError(t, manager.StartPendingScheduledMessages())
	recurring := readFinal(t, finals)
	assert.Equal(t, "web:cron:daily", recurring.ConversationID)
	assert.Equal(t, "canonical", recurring.Agent, "startup uses persisted Web selection, not the stale schedule agent")
	assert.Equal(t, "canonical answer", recurring.Text)
	assert.Nil(t, recurring.SlackReply)
	assert.Nil(t, recurring.Cronjob)

	var request struct {
		Input []json.RawMessage `json:"input"`
	}

	require.NoError(t, json.Unmarshal([]byte(<-requests), &request))
	messages, err := replayInputMessages(request.Input)
	require.NoError(t, err)
	require.NotEmpty(t, messages)
	assert.Equal(t, "user", messages[len(messages)-1].role)
	assert.Equal(t, "[System additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\nrecurring follow up", messages[len(messages)-1].text)
	assert.Empty(t, roots)
	private, err := service.ObserveEntries(t.Context(), "cron:daily")
	require.NoError(t, err)
	assert.Len(t, private, 4, "canonical turns never append to producer history")

	for _, state := range []string{"completed before Sync", "binding before projection", "existing Web", "reset cancels", "reset then future", "creation failure", "Sync failure", "live creation failure", "live Sync failure", "live read failure", "due creation failure", "due read failure"} {
		t.Run(state, func(t *testing.T) {
			newBridge, store, calls, outbounds := newResumeTestBridges(t, false)
			store.db.SetMaxOpenConns(1)

			runtime := newBridge().runtime
			if state == "completed before Sync" {
				writeAgent(t, runtime.Workspace, "a-loaded", "---\ndescription: First loaded\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nSorted fallback instructions\n")
			}

			require.NoError(t, store.UpsertThread("cron:pending", ThreadState{Agent: "main", CreatedBy: ThreadCreatedByCron}))

			inbound := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "original job", false)
			inbound.RequireOutputDecision = true
			inbound.Cronjob = &protocol.CronjobMessage{RelativePath: "cron/missing.md", Agent: "main"}
			require.NoError(t, startTurnDB(t.Context(), store.db, "pending", "cron:pending", inbound))

			data, err := json.Marshal(protocol.ScheduledMessageState{ConversationID: "cron:pending", Agent: "main", Message: "pending follow up", DueAt: time.Now().Add(-time.Minute)})
			require.NoError(t, err)
			_, err = store.AppendEntryID(t.Context(), "cron:pending", &rocketcode.SessionEntry{Version: 1, Type: producerScheduleEntryType, Timestamp: time.Now(), OutputTrace: []json.RawMessage{data}})
			require.NoError(t, err)

			if strings.HasPrefix(state, "reset") {
				require.NoError(t, store.PutScheduledMessage("legacy-before-reset", &protocol.ScheduledMessageState{ConversationID: "cron:pending", Agent: "main", Message: "legacy follow up", DueAt: time.Now().Add(-time.Minute)}))
				_, err = store.AppendEntryID(t.Context(), "cron:pending", &rocketcode.SessionEntry{Version: 1, Type: producerResetEntryType, Timestamp: time.Now()})
				require.NoError(t, err)

				if state == "reset then future" {
					data, err = json.Marshal(protocol.ScheduledMessageState{ConversationID: "cron:pending", Agent: "main", Message: "future follow up", DueAt: time.Now().Add(time.Hour)})
					require.NoError(t, err)
					_, err = store.AppendEntryID(t.Context(), "cron:pending", &rocketcode.SessionEntry{Version: 1, Type: producerScheduleEntryType, Timestamp: time.Now(), OutputTrace: []json.RawMessage{data}})
					require.NoError(t, err)
				}
			}

			delivery, err := store.finishTurn(t.Context(), "pending", &turnFinish{store: newSessionStore("cron:pending", store), entries: []rocketcode.SessionEntry{*testSessionEntry("original job", "private answer")}, outbound: protocol.NewOutboundMessage("cron:pending", "")})
			require.NoError(t, err)

			if !strings.HasPrefix(state, "live ") {
				require.NoError(t, store.closeTurn(t.Context(), "pending"))
			}

			if state == "existing Web" || state == "binding before projection" {
				writeAgent(t, runtime.Workspace, "selected", "---\ndescription: Selected\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nSelected Web instructions\n")
				require.NoError(t, (&Runtime{Sessions: store}).CreateConversation(t.Context(), protocol.Conversation{ID: "web:cron:pending", Agent: "selected"}))

				if state == "binding before projection" {
					_, err = (stateDAO{db: store.db}).bindProducer(t.Context(), "cron:pending", "web:cron:pending")
					require.NoError(t, err)
				}
			}

			manager, shutdown := newTestBridgeManager(t, runtime, store, outbounds)

			if strings.HasSuffix(state, "read failure") {
				bridge := newBridge()
				bridge.config.ConversationID, bridge.threads = "cron:pending", manager

				t.Cleanup(func() { require.NoError(t, bridge.Stop()) })
				// Fail metadata reads only after delivery closes the original turn.
				// A sequence proves the failed attempt even when its query rolls back.
				_, err = store.db.ExecContext(t.Context(), `
CREATE SEQUENCE handoff_reads;
ALTER TABLE managed_conversations RENAME TO unavailable_conversations;
CREATE FUNCTION unavailable_producer(inbound json) RETURNS json LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM active_turns WHERE conversation_id = 'cron:pending' AND phase <> 'done') THEN
        RETURN inbound;
    END IF;
    PERFORM nextval('handoff_reads');
    RAISE EXCEPTION 'producer metadata unavailable';
END $$;
CREATE VIEW managed_conversations AS
SELECT conversation_id, revert_message_id, unavailable_producer(producer_inbound_json) AS producer_inbound_json FROM unavailable_conversations`)
				require.NoError(t, err)

				if state == "live read failure" {
					bridge.handle(t.Context(), &bridgeRequest{inbound: inbound, turnID: "pending", delivery: delivery})
				} else {
					bridge.armScheduledMessage("", &protocol.ScheduledMessageState{ConversationID: "cron:pending", DueAt: time.Now().Add(-time.Minute)})
				}

				require.Eventually(t, func() bool {
					var attempted bool
					require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT is_called FROM handoff_reads`).Scan(&attempted))

					return attempted
				}, 5*time.Second, 10*time.Millisecond)

				var through int64
				require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT producer_effects_through_id FROM unavailable_conversations WHERE conversation_id = 'cron:pending'`).Scan(&through))
				assert.Zero(t, through)
				assert.Empty(t, calls)
				require.Empty(t, runningTestTurns(t, store), "delivery closed the original turn before the metadata read failed")
				_, err = store.db.ExecContext(t.Context(), `DROP VIEW managed_conversations; ALTER TABLE unavailable_conversations RENAME TO managed_conversations; DROP FUNCTION unavailable_producer(json)`)
				require.NoError(t, err)
			} else if strings.HasSuffix(state, "failure") {
				statement := `ALTER TABLE managed_conversations ADD CONSTRAINT reject_handoff CHECK (conversation_id <> 'web:cron:pending') NOT VALID`
				table := "managed_conversations"

				if strings.HasSuffix(state, "Sync failure") {
					statement = `ALTER TABLE scheduled_messages ADD CONSTRAINT reject_handoff CHECK (message <> 'pending follow up')`
					table = "scheduled_messages"
				}

				if state == "due creation failure" {
					// Sequences survive rollback, exposing a failed live timer attempt.
					_, err = store.db.ExecContext(t.Context(), `CREATE SEQUENCE handoff_attempts`)
					require.NoError(t, err)

					statement = `ALTER TABLE managed_conversations ADD CONSTRAINT reject_handoff CHECK (conversation_id <> 'web:cron:pending' OR nextval('handoff_attempts') < 0) NOT VALID`
				}

				_, err = store.db.ExecContext(t.Context(), statement)
				require.NoError(t, err)

				switch {
				case strings.HasPrefix(state, "live "):
					bridge := newBridge()
					bridge.config.ConversationID, bridge.threads = "cron:pending", manager

					t.Cleanup(func() { require.NoError(t, bridge.Stop()) })
					bridge.handle(t.Context(), &bridgeRequest{inbound: inbound, turnID: "pending", delivery: delivery})
					require.Empty(t, runningTestTurns(t, store))
				case state == "due creation failure":
					bridge, err := manager.recordedBridge("cron:pending")
					require.NoError(t, err)
					bridge.armScheduledMessage("", &protocol.ScheduledMessageState{ConversationID: "cron:pending", DueAt: time.Now().Add(-time.Minute)})
					require.Eventually(t, func() bool {
						var attempted bool
						require.NoError(t, store.db.QueryRowContext(t.Context(), `SELECT is_called FROM handoff_attempts`).Scan(&attempted))

						return attempted
					}, 5*time.Second, 10*time.Millisecond)
				default:
					require.ErrorContains(t, manager.StartPendingScheduledMessages(), "reject_handoff")
				}

				_, through, err := (stateDAO{db: store.db}).producer(t.Context(), "cron:pending")
				require.NoError(t, err)
				assert.Zero(t, through)
				assert.Empty(t, calls, "failed handoff must not run privately")
				_, err = store.db.ExecContext(t.Context(), "ALTER TABLE "+table+" DROP CONSTRAINT reject_handoff")
				require.NoError(t, err)

				if !strings.HasPrefix(state, "live ") && !strings.HasPrefix(state, "due ") {
					// Lose the failed process: recovery uses only retained routing and effects.
					require.NoError(t, shutdown())
					manager, _ = newTestBridgeManager(t, runtime, store, outbounds)
				}
			}

			if !strings.HasPrefix(state, "live ") && !strings.HasPrefix(state, "due ") {
				require.NoError(t, manager.StartPendingScheduledMessages())
			}

			if strings.HasPrefix(state, "reset") {
				_, recorded, err := store.Thread("web:cron:pending")
				require.NoError(t, err)
				assert.False(t, recorded, "cancelled due work cannot create Web ahead of a surviving future schedule")
				assert.Empty(t, calls)

				if state == "reset cancels" {
					private, err := store.ScheduledMessagesForConversation("cron:pending")
					require.NoError(t, err)
					assert.Empty(t, private, "the resumed reset also cancels legacy executable rows")
				}

				return
			}

			final := readFinal(t, outbounds)
			assert.Equal(t, "web:cron:pending", final.ConversationID)

			wantAgent := "main"
			if state == "completed before Sync" {
				wantAgent = "a-loaded"
			}

			if state == "existing Web" || state == "binding before projection" {
				wantAgent = "selected"
			}

			assert.Equal(t, wantAgent, final.Agent)
			assert.Nil(t, final.SlackReply)
			assert.Contains(t, <-calls, "original job")
			require.Eventually(t, func() bool { return len(runningTestTurns(t, store)) == 0 }, 5*time.Second, 10*time.Millisecond)

			pending, err := store.ScheduledMessages()
			require.NoError(t, err)
			require.Empty(t, pending, "the completed one-shot has no executable row")
			// Inspect prior calls before rediscovery; concurrent claims are outside
			// this contract, but rediscovery must not rearm consumed work.
			for len(calls) > 0 {
				assert.Contains(t, <-calls, "original job")
			}

			require.NoError(t, manager.StartPendingScheduledMessages())
			assert.Empty(t, calls, "consumed work stays consumed on rediscovery")
		})
	}
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
	for _, stored := range []string{"row", "queued", "completed before Sync", "binding before projection", "projection before arming", "due while producing", "projection failure"} {
		t.Run(stored, func(t *testing.T) {
			newBridge, service, requests, finals := newResumeTestBridges(t, false)
			managedID, privateID := newBridge().config.ConversationID, "external_mcp:main:private"
			require.NoError(t, service.UpsertThread(privateID, ThreadState{Agent: "main"}))

			msg := protocol.NewInboundMessage(protocol.SourceExternalMCP, protocol.InboundKindPrompt, "support ticket", true)
			msg.ConversationID, msg.SyncDestination = privateID, managedID
			msg.Metadata = map[string]string{"external_conversation_id": "ext-1"}

			manager, _ := newTestBridgeManager(t, newBridge().runtime, service, finals)
			if stored != "row" && stored != "queued" {
				writeAgent(t, newBridge().runtime.Workspace, "selected", "---\ndescription: Selected\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nSelected canonical instructions\n")
				require.NoError(t, startTurnDB(t.Context(), service.db, "turn-private", privateID, msg))
				data, err := json.Marshal(protocol.ScheduledMessageState{ConversationID: privateID, Agent: "main", Message: "scheduled follow up", DueAt: time.Now().Add(-time.Minute)})
				require.NoError(t, err)
				effectID, err := service.AppendEntryID(t.Context(), privateID, &rocketcode.SessionEntry{Version: 1, Type: producerScheduleEntryType, Timestamp: time.Now(), OutputTrace: []json.RawMessage{data}})
				require.NoError(t, err)

				if stored == "due while producing" {
					require.NoError(t, manager.StartPendingScheduledMessages())

					pending, err := service.ScheduledMessagesForConversation(managedID)
					require.NoError(t, err)
					assert.Empty(t, pending, "even overdue work waits for the producer to finish")
					require.NoError(t, manager.StartActiveTurns(t.Context()))

					for range 3 {
						readFinal(t, finals)
					}

					assert.Len(t, requests, 2, "original producer and canonical follow-up each run once")

					return
				}

				_, err = service.finishTurn(t.Context(), "turn-private", &turnFinish{store: newSessionStore(privateID, service), entries: []rocketcode.SessionEntry{*testSessionEntry("support ticket", "original answer")}, outbound: protocol.NewOutboundMessage(privateID, "original answer")})
				require.NoError(t, err)
				require.NoError(t, service.closeTurn(t.Context(), "turn-private"))
				require.Empty(t, runningTestTurns(t, service), "startup must recover without an unfinished producer row")

				if stored == "projection before arming" {
					// Persist the atomic projection but lose the process before any timer is armed.
					require.NoError(t, service.PutScheduledMessage("projected", &protocol.ScheduledMessageState{ConversationID: managedID, Agent: "main", Message: "scheduled follow up", DueAt: time.Now().Add(-time.Minute)}))
					require.NoError(t, (stateDAO{db: service.db}).advanceProducerEffects(t.Context(), privateID, effectID))
				}

				if stored == "binding before projection" {
					owner, err := (stateDAO{db: service.db}).bindProducer(t.Context(), privateID, managedID)
					require.NoError(t, err)
					assert.Equal(t, managedID, owner)
				}

				if stored == "projection failure" {
					_, err := service.db.ExecContext(t.Context(), `ALTER TABLE scheduled_messages ADD CONSTRAINT reject_handoff CHECK (message <> 'scheduled follow up')`)
					require.NoError(t, err)
					require.ErrorContains(t, manager.StartPendingScheduledMessages(), "reject_handoff")
					_, through, err := (stateDAO{db: service.db}).producer(t.Context(), privateID)
					require.NoError(t, err)
					assert.Zero(t, through, "a failed projection leaves progress pending")
					_, err = service.db.ExecContext(t.Context(), `ALTER TABLE scheduled_messages DROP CONSTRAINT reject_handoff`)
					require.NoError(t, err)
				}

				_, err = manager.switchConversationAgent(managedID, "selected")
				require.NoError(t, err)
				require.NoError(t, manager.StartPendingScheduledMessages())
				final := readFinal(t, finals)
				assert.Equal(t, managedID, final.ConversationID)
				assert.Equal(t, "selected", final.Agent)
				assert.Contains(t, <-requests, "Selected canonical instructions")
				require.Eventually(t, func() bool { return len(runningTestTurns(t, service)) == 0 }, 5*time.Second, 10*time.Millisecond)
				require.NoError(t, manager.StartPendingScheduledMessages())

				pending, err := service.ScheduledMessagesForConversation(managedID)
				require.NoError(t, err)
				assert.Empty(t, pending, "consumed one-shots stay consumed on another startup discovery")
				assert.Empty(t, finals)

				return
			}

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

		// Platform tools run inside execute.
		writeRawRunFunctionCall(t, w, "resp_1", "execute", struct {
			Code string `json:"code"`
		}{Code: "def main():\n    return ask_user_question(question=\"Ship?\", details=\"\", options=[], multiple=False)\n"})
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
	assert.True(t, strings.HasPrefix(questionID, turns[0]+"/call/call_1/host/"), "the question ID is the nested call's journal key, got %q", questionID)

	shutdown()
	require.Eventually(t, func() bool { return !first.handlingSnapshot() }, 5*time.Second, 10*time.Millisecond)
	assert.Empty(t, finals)

	runTestBridge(t, newBridge())
	assert.Equal(t, questionID, <-asked, "the resumed turn waits on the same question")

	answers <- protocol.AskUserQuestionAnswer{Selected: []string{"yes"}, Source: protocol.SourceSlack}

	assert.Equal(t, "answer", readFinal(t, finals).Text)
	assert.Len(t, bodies, 2, "the recorded model call is not repeated")

	observed, err := service.ObserveEntries(t.Context(), conversationID)
	require.NoError(t, err)

	asks := 0

	for i := range observed {
		for _, raw := range observed[i].Entry.OutputTrace {
			var call struct {
				Type, Name   string
				ParentCallID string `json:"parent_call_id"`
			}
			if json.Unmarshal(raw, &call) == nil && call.Type == "function_call" && call.ParentCallID != "" && call.Name == "ask_user_question" {
				asks++
			}
		}
	}

	assert.Equal(t, 1, asks, "the resumed script saves its question once")

	for len(bodies) > 0 {
		<-bodies
	}

	later := newBridge()
	runTestBridge(t, later)

	next := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "anything else", true)
	next.SlackReply = &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.333", ThreadTS: "111.222"}
	require.NoError(t, later.Submit(t.Context(), next))
	assert.Contains(t, <-bodies, "- ask_user_question(", "a later turn can still ask inside execute")
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

	destination, err := rt.threads.recordedBridge(template.config.ConversationID)
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

// restart starts nt's bridges the way a new process does, settling the Background Jobs a stopped
// one left running before any turn resumes.
func (nt *noteTest) restart(t *testing.T) (manager *threadBridgeManager, shutdown func() error) {
	t.Helper()

	manager, shutdown = nt.run(t, ignoreOutbound)
	resume, err := nt.bridge(t, manager, nt.conversationID).background.recoverJobs(t.Context())
	require.NoError(t, err)
	require.NoError(t, resume(t.Context()))

	return manager, shutdown
}

func killedExecuteNote(jobID string) string {
	return `<execute id="` + jobID + `" state="killed" description="tests">` + "\nThe job was killed because the server restarted.\n</execute>"
}

// The killed script's note waits for the conversation's next turn and starts no turn of its own.
func TestRestartReportsKilledScriptAtNextTurn(t *testing.T) {
	nt := newNoteTest(t, answerNoteTest)
	job := testBackgroundJob(nt.conversationID, "turn-0/call/a")
	job.origin = nt.slackPrompt("run the tests")
	createTestBackgroundJob(t, nt.service, job)

	manager, _ := nt.restart(t)
	nt.waitIdle(t, manager, nt.conversationID)
	assert.Empty(t, nt.requests, "no turn starts for the killed script")
	assert.Equal(t, []string{"turn-0/call/a pending false"}, noteStates(t, nt.service))

	require.NoError(t, nt.bridge(t, manager, nt.conversationID).Submit(t.Context(), nt.slackPrompt("next")))
	assert.NotContains(t, nt.request(t), "turn-0/call/a")
	assert.Equal(t, "[System]\n\n"+killedExecuteNote("turn-0/call/a"), lastRequestMessage(t, nt.request(t)), "the next turn reports the killed script")
	readFinal(t, nt.finals)
	nt.waitIdle(t, manager, nt.conversationID)
	assert.Equal(t, []string{"turn-0/call/a consumed false"}, noteStates(t, nt.service))
}

// A subagent cut by a restart resumes its journaled turn, then delivers its note; a second start replays nothing.
func TestRestartResumesBackgroundSubagent(t *testing.T) {
	cut := make(chan struct{}, 1)
	cut <- struct{}{}

	nt := newNoteTest(t, func(w http.ResponseWriter, r *http.Request, n int, body string) {
		switch {
		case strings.Contains(body, `"content":"research the flaky test"`):
			select {
			case <-cut:
				<-r.Context().Done() // The restart cuts the subagent's first run.
				return
			default:
			}

			_, _ = w.Write([]byte(`{"id":"resp_2","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"found it","annotations":[]}]}]}`))
		case !strings.Contains(body, "function_call_output"):
			_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_t","name":"task","arguments":"{\"description\":\"research\",\"prompt\":\"research the flaky test\",\"subagent_type\":\"researcher\",\"background\":true}"}]}`))
		default:
			answerNoteTest(w, r, n, body)
		}
	})
	writeAgent(t, nt.cfg.Workspace, "main", "---\ndescription: Agent\nmode: primary\nmodel: gpt-5.5\npermission:\n  task: allow\n  rocketclaw:\n    allow_background: allow\n---\nPrompt\n")

	first, shutdown := nt.run(t, ignoreOutbound)
	registry := nt.bridge(t, first, nt.conversationID).background
	require.NoError(t, nt.bridge(t, first, nt.conversationID).Submit(t.Context(), nt.slackPrompt("hello")))

	for range 3 { // The task call, the subagent's cut run, and the turn's answer.
		nt.request(t)
	}

	assert.Equal(t, "answer", readFinal(t, nt.finals).Text)
	nt.waitIdle(t, first, nt.conversationID)
	require.NoError(t, shutdown())
	registry.shutdown()

	jobs, err := queryStrings(t.Context(), nt.service.db, `SELECT job_id FROM background_jobs WHERE kind = 'task' AND status = 'running'`, "running tasks")
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	keys, err := queryStrings(t.Context(), nt.service.db, `SELECT subagent_key FROM background_jobs WHERE job_id = $1`, "task subagent key", jobs[0])
	require.NoError(t, err)
	require.Regexp(t, `^/call_t-[0-9a-f]{8}$`, keys[0])

	manager, shutdown := nt.restart(t)
	assert.Contains(t, nt.request(t), `"content":"research the flaky test"`, "the subagent resumes its journaled turn")
	assert.Equal(t, systemPromptHeader()+"\n\n"+`<subagent id="`+jobs[0]+`" state="completed" description="research" continue="`+keys[0]+`">`+"\n<task_result>\nfound it\n</task_result>\n</subagent>", lastRequestMessage(t, nt.request(t)))
	readFinal(t, nt.finals)
	nt.waitIdle(t, manager, nt.conversationID)
	assert.Equal(t, []string{jobs[0] + " consumed true"}, noteStates(t, nt.service))
	require.NoError(t, shutdown())

	manager, _ = nt.restart(t)
	nt.waitIdle(t, manager, nt.conversationID)
	assert.Empty(t, nt.requests, "a second start replays nothing")
}

// The killed note wakes the hidden cron run, and its follow-up is posted in the thread the run posted.
func TestRestartWakesHiddenRunForKilledScript(t *testing.T) {
	nt := newNoteTest(t, func(w http.ResponseWriter, _ *http.Request, _ int, _ string) {
		// A cron run's reply is its report.
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Report finished.","annotations":[]}]}]}`))
	})
	first, shutdown := nt.run(t, ignoreOutbound)
	report := protocol.SlackThreadConversationID("C1", "1.2")

	first.mu.Lock()
	first.cronRoots = &slackFrontendMock{SendCronjobRootFunc: func(context.Context, *protocol.OutboundMessage) (protocol.TextConversationTarget, error) {
		return protocol.TextConversationTarget{ChannelID: "C1", MessageID: "1.2", ThreadID: "1.2"}, nil
	}}
	first.mu.Unlock()

	msg := seedCronRun(t, nt.service, "cron:nightly")
	job := testBackgroundJob("cron:nightly", "turn-cron/call/a")
	job.origin = msg
	createTestBackgroundJob(t, nt.service, job)

	outbound := protocol.NewOutboundMessage("cron:nightly", "Report started")
	outbound.TurnID, outbound.Complete, outbound.Cronjob, outbound.SlackReply = "turn-cron", true, msg.Cronjob, msg.SlackReply
	_, err := nt.service.finishTurn(t.Context(), "turn-cron", &turnFinish{store: newSessionStore("cron:nightly", nt.service), entries: []rocketcode.SessionEntry{*testSessionEntry("job prompt", "Report started")}, outbound: outbound})
	require.NoError(t, err)
	require.NoError(t, first.StartActiveTurns(t.Context()))
	assert.Equal(t, "Report started", readFinalFor(t, nt.finals, "cron:nightly").Text)
	require.Eventually(t, func() bool {
		destinations, err := queryStrings(context.Background(), nt.service.db, `SELECT sync_destination FROM background_jobs`, "job destinations")
		return err == nil && slices.Equal(destinations, []string{report})
	}, 10*time.Second, 10*time.Millisecond)
	nt.waitIdle(t, first, report)
	require.NoError(t, shutdown())

	manager, _ := nt.restart(t)
	assert.Equal(t, "Report finished.", readFinalFor(t, nt.finals, report).Text, "the follow-up is posted in the report thread")
	assert.Equal(t, systemPromptHeader()+"\n\n"+killedExecuteNote(job.jobID), lastRequestMessage(t, nt.request(t)))
	nt.waitIdle(t, manager, report)
	assert.Equal(t, []string{"turn-cron/call/a consumed true"}, noteStates(t, nt.service))
}

// A second start replays nothing.
func TestRestartWakesUndeliveredNote(t *testing.T) {
	nt := newNoteTest(t, answerNoteTest)
	nt.addNote(t, nt.conversationID, "turn-0/call/a", nt.slackPrompt("run the tests"))

	manager, shutdown := nt.restart(t)
	assert.Equal(t, systemPromptHeader()+"\n\n"+testExecuteNote("turn-0/call/a"), lastRequestMessage(t, nt.request(t)))
	readFinal(t, nt.finals)
	nt.waitIdle(t, manager, nt.conversationID)
	require.NoError(t, shutdown())

	manager, _ = nt.restart(t)
	nt.waitIdle(t, manager, nt.conversationID)
	assert.Empty(t, nt.requests, "a second start replays nothing")
	assert.Equal(t, []string{"turn-0/call/a consumed true"}, noteStates(t, nt.service))
}

func TestRestartDropsJobsOfDeletedConversation(t *testing.T) {
	nt := newNoteTest(t, answerNoteTest)
	task := testBackgroundJob(nt.conversationID, "turn-0/call/a")
	task.kind, task.subagentKey = backgroundTask, "/a"

	for _, job := range []*backgroundJob{task, testBackgroundJob(nt.conversationID, "turn-0/call/b")} {
		createTestBackgroundJob(t, nt.service, job)
	}

	nt.addNote(t, nt.conversationID, "turn-0/call/c", nt.slackPrompt("run the tests"))
	_, err := nt.service.DeleteSession(t.Context(), nt.conversationID)
	require.NoError(t, err)

	manager, _ := nt.restart(t)
	nt.waitIdle(t, manager, nt.conversationID)
	assert.Empty(t, nt.requests)
	assert.Empty(t, backgroundRows(t, nt.service))
}
