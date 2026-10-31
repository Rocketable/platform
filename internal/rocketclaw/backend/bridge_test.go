package backend

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/harness"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
	"github.com/gorilla/websocket"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

type discardPublisher struct{}

// PublishOutbound completes delivery at once, as Runtime.PublishOutbound does with no consumers.
func (discardPublisher) PublishOutbound(context.Context, *protocol.OutboundMessage) error {
	return nil
}

type testBus struct {
	outbound chan *protocol.OutboundMessage
	closed   chan struct{}
	once     sync.Once
}

func newTestBus() *testBus {
	return &testBus{outbound: make(chan *protocol.OutboundMessage, 128), closed: make(chan struct{})}
}

func (b *testBus) PublishOutbound(ctx context.Context, message *protocol.OutboundMessage) error {
	select {
	case <-b.closed:
		return errors.New("test publisher closed")
	default:
	}

	select {
	case b.outbound <- message:
		return nil
	case <-b.closed:
		return errors.New("test publisher closed")
	case <-ctx.Done():
		return fmt.Errorf("publish test outbound: %w", ctx.Err())
	}
}

func (b *testBus) Outbound(ctx context.Context) iter.Seq[*protocol.OutboundMessage] {
	return func(yield func(*protocol.OutboundMessage) bool) {
		for {
			select {
			case message := <-b.outbound:
				if !yield(message) {
					return
				}
			case <-b.closed:
				return
			case <-ctx.Done():
				return
			}
		}
	}
}

func (b *testBus) Close() { b.once.Do(func() { close(b.closed) }) }

func TestReplayInputForMessagePreservesSDKSerialization(t *testing.T) {
	for _, role := range []string{"user", "developer", "assistant"} {
		for _, text := range []string{"", " \n", "quotes: \" and \\ and <html>", "日本\x00\xff\u2028"} {
			message := responses.EasyInputMessageParam{Role: responses.EasyInputMessageRole(role), Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(text)}, Type: "message"}
			want, err := harness.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{{OfMessage: &message}})
			require.NoError(t, err, "string-only replay construction cannot fail")
			require.Equal(t, want, replayInputForMessage(role, text))
		}
	}
}

func TestBridgeSwitchAgentTrimsAndStoresAgent(t *testing.T) {
	t.Parallel()

	bridge := &Bridge{config: Config{Agent: "old"}}
	bridge.SwitchAgent("  next  ")
	assert.Equal(t, "next", bridge.agentSnapshot())
}

func TestBridgeConsumedInputsRetainIdentityAndOrder(t *testing.T) {
	t.Parallel()

	bus := newTestBus()
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), bus: bus, config: Config{ConversationID: "web-conversation", SessionService: newTestSessionService(t)}, inputOpen: true, requestCh: make(chan bridgeRequest, 1)}
	content := protocol.InboundContent{Text: "same text"}
	initial := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &content, true)
	initial.Metadata["web_message_id"] = "initial"
	admitted, err := bridge.activateInbound(t.Context(), &bridgeRequest{inbound: initial})
	require.NoError(t, err)
	require.True(t, admitted)
	require.Empty(t, bus.outbound)

	header, _, _ := strings.Cut(buildPrompt(initial, nil, false), "\n\n")
	bridge.publishConsumed(t.Context(), initial, header)

	for _, id := range []string{"steer-first", "steer-second"} {
		inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &content, true)
		inbound.Metadata["web_message_id"] = id
		require.NoError(t, bridge.Submit(t.Context(), inbound))
	}

	require.Equal(t, "steer-first", bridge.steers[0].queueItemID)
	require.Equal(t, "steer-second", bridge.steers[1].queueItemID)
	require.Len(t, bus.outbound, 1, "waiting steers are not consumed at submission")

	bridge.activeAttribution = harness.ReplayAttribution{Agent: "planner", Model: "work/model-a", ReasoningEffort: new("high")}
	bridge.SwitchAgent("reviewer")
	inputs := testDrainInputs(t, bridge, harness.TurnPhaseFinalAnswer)
	require.Len(t, inputs, 2)

	for i := range inputs {
		require.Equal(t, buildPrompt(initial, nil, false), inputs[i].Texts[0], "identity metadata must not change prompt framing")
		require.NotContains(t, inputs[i].Texts[0], "steer-")
	}

	require.Len(t, bus.outbound, 3)

	for _, id := range []string{"initial", "steer-first", "steer-second"} {
		message := <-bus.outbound
		require.Equal(t, "web-conversation", message.ConversationID)
		require.Equal(t, id, message.ConsumedID)
		require.Equal(t, "same text", message.ConsumedText)
		require.Equal(t, protocol.SourceWeb, message.ConsumedSource)
		require.Equal(t, "web-conversation", message.SourceConversationID)

		if id == "initial" {
			require.Nil(t, message.ReasoningEffort)
		} else {
			require.Equal(t, "planner", message.Agent)
			require.Equal(t, "work/model-a", message.Model)
			require.Equal(t, new("high"), message.ReasoningEffort)
		}

		require.Empty(t, message.Text)
		require.False(t, message.Complete)
	}

	require.Empty(t, testDrainInputs(t, bridge, harness.TurnPhaseFinalAnswer))
	require.Empty(t, bus.outbound, "a second drain must not repeat consumption")

	// A disconnected observer must not drop or replay an admitted steer.
	var logged bytes.Buffer

	bridge.log = slog.New(slog.NewTextHandler(&logged, nil))

	bus.Close()

	bridge.inputOpen, bridge.activeTurnID = true, "next-turn"
	bridge.steers, bridge.steersRead = nil, 0
	disconnected := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &content, true)
	disconnected.Metadata["web_message_id"] = "steer-disconnected"
	require.NoError(t, bridge.Submit(t.Context(), disconnected))
	inputs = testDrainInputs(t, bridge, harness.TurnPhaseFinalAnswer)
	require.Len(t, inputs, 1)
	require.Equal(t, buildPrompt(initial, nil, false), inputs[0].Texts[0])
	require.Contains(t, logged.String(), "publish consumed web input")
	require.Contains(t, logged.String(), "test publisher closed")
	require.Empty(t, testDrainInputs(t, bridge, harness.TurnPhaseFinalAnswer))
}

func TestPromotedQueueConsumptionKeepsQueueIdentity(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)

	const conversationID = "web-queue"
	require.NoError(t, store.UpsertThread(conversationID, ThreadState{Agent: "main"}))

	bus := newTestBus()
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), bus: bus, config: Config{ConversationID: conversationID, SessionService: store}, inputOpen: true, requestCh: make(chan bridgeRequest, 1)}

	manager := &threadBridgeManager{log: slog.New(slog.DiscardHandler), store: store, bridges: map[string]directBridge{conversationID: bridge}}
	for i, id := range []string{"queue-first", "queue-second"} {
		require.NoError(t, store.PutThreadQueueItem(id, &protocol.ThreadQueueItem{
			ConversationID: conversationID, Source: protocol.SourceWeb, Kind: protocol.InboundKindEnqueue,
			Message: "same text", Principal: "alice", Position: i,
		}))
		promoted, err := manager.promoteQueueItem(t.Context(), conversationID, id)
		require.NoError(t, err)
		require.True(t, promoted)
	}

	items, err := manager.queueItems(conversationID)
	require.NoError(t, err)
	require.Len(t, items, 2)

	for i, id := range []string{"queue-first", "queue-second"} {
		require.Equal(t, id, items[i].ID)
		require.Equal(t, protocol.InboundKindSteer, items[i].Kind)
		require.Equal(t, "alice", items[i].Principal)
	}

	require.Empty(t, bus.outbound)
	require.Len(t, testDrainInputs(t, bridge, harness.TurnPhaseFinalAnswer), 2)
	require.Len(t, bus.outbound, 2)

	for _, id := range []string{"queue-first", "queue-second"} {
		message := <-bus.outbound
		require.Equal(t, id, message.ConsumedID)
		require.Equal(t, "same text", message.ConsumedText)
		require.Equal(t, protocol.SourceWeb, message.ConsumedSource)
	}

	items, err = manager.queueItems(conversationID)
	require.NoError(t, err)
	require.Empty(t, items)

	queued := protocol.ThreadQueueItem{ID: "queue-next", ConversationID: conversationID, Source: protocol.SourceWeb, Kind: protocol.InboundKindEnqueue, Message: "next turn", Principal: "alice"}
	require.NoError(t, store.PutThreadQueueItem(queued.ID, &queued))
	require.NoError(t, bridge.submitEnqueuedItem(t.Context(), &queued))
	require.Empty(t, bus.outbound, "queued input is consumed only when its model prompt is built")

	request := <-bridge.requestCh
	admitted, err := bridge.activateInbound(t.Context(), &request)
	require.NoError(t, err)
	require.True(t, admitted)
	require.Empty(t, bus.outbound)

	slackQueued := protocol.ThreadQueueItem{ID: "slack-next", ConversationID: conversationID, Source: protocol.SourceSlack, Kind: protocol.InboundKindEnqueue, Message: "already visible", Principal: "bob"}
	require.NoError(t, store.PutThreadQueueItem(slackQueued.ID, &slackQueued))
	require.NoError(t, bridge.submitEnqueuedItem(t.Context(), &slackQueued))
	request = <-bridge.requestCh
	admitted, err = bridge.activateInbound(t.Context(), &request)
	require.NoError(t, err)
	require.True(t, admitted)

	require.Empty(t, bus.outbound)

	remaining, err := store.ThreadQueueForConversation(conversationID)
	require.NoError(t, err)
	require.Empty(t, remaining)
}

func TestBridgeConsumedInputKeepsRawWebText(t *testing.T) {
	t.Parallel()

	bus := newTestBus()
	bridge := &Bridge{bus: bus, config: Config{ConversationID: "slack-thread:C123:111.0", SessionService: newTestSessionService(t)}}
	inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{
		Text: "hello", TextAttachments: []string{`attachment:id "file.txt" (workspace path "artifacts/uploads/id/file.txt")`},
	}, true)
	inbound.Metadata["web_message_id"] = "web-input"
	bridge.publishConsumed(t.Context(), inbound, `[Web principal="alice"]`)

	message := <-bus.outbound
	require.Contains(t, message.ConsumedText, "attachment:id")
	require.Equal(t, "hello", message.ConsumedRawText)
	require.Equal(t, `[Web principal="alice"]`, message.ConsumedHeader)
}

func TestRestartToolScopesDescriptionToRuntimeConfig(t *testing.T) {
	t.Parallel()

	tool := restartTool(&runtimeRestarterMock{restartFunc: func(string) string { return "" }})

	assert.Contains(t, tool.Description, "explicitly requested runtime configuration change")
	assert.Contains(t, tool.Description, "rocketclaw.json")
	assert.Contains(t, tool.Description, "femtoclaw.json")
	assert.Contains(t, tool.Description, "configured overlay entries")
	assert.Contains(t, tool.Description, "Use rocketclaw_reload instead")
	assert.Contains(t, tool.Description, "agents/")
	assert.Contains(t, tool.Description, "skills/")
	assert.Contains(t, tool.Description, "cron/")
	assert.Contains(t, tool.Description, "reason field")
	assert.Contains(t, tool.Description, "memory, ledger, audit, report")
	assert.Contains(t, tool.Description, "source-code")
	assert.Contains(t, tool.Description, "data-file edits")
	assert.NotContains(t, tool.Description, "file changes")
	assert.Equal(t, []string{"reason"}, tool.Parameters["required"])
}

func TestRestartToolCallsConfiguredRestart(t *testing.T) {
	t.Parallel()

	order := []string{}

	tool := restartTool(&runtimeRestarterMock{restartFunc: func(reason string) string {
		order = append(order, "restart:"+reason)
		return "custom restart output"
	}})

	result, err := tool.Call(t.Context(), []byte(`{"reason":"rocketclaw.json changed and runtime config must reload"}`), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"restart:rocketclaw.json changed and runtime config must reload"}, order)
	assert.Equal(t, "custom restart output", result.Output)
}

func TestReloadToolReturnsModelVisibleFailure(t *testing.T) {
	t.Parallel()

	tool := reloadTool(&runtimeReloaderMock{ReloadFunc: func(string) (string, error) {
		return "", errors.New("invalid staged cron")
	}})

	result, err := tool.Call(t.Context(), []byte(`{"reason":"cron changed"}`), nil)
	require.NoError(t, err)
	assert.Equal(t, "rocketclaw_reload failed:\n\ninvalid staged cron", result.Output)

	_, err = tool.Call(t.Context(), []byte(`{"reason":"  "}`), nil)
	require.ErrorContains(t, err, "reason is required")
}

func TestUpdateGoalToolRecordsProgressNoteWithoutEndingGoal(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	recordTestConversations(t, store, "main", map[string]ConversationKind{"thread-1": KindWebChat})
	require.NoError(t, store.BeginGoal("thread-1", &GoalState{Objective: "fix lint", MaxTurns: 3}))

	tool := updateGoalTool(store, "thread-1")
	assert.Contains(t, fmt.Sprint(tool.Parameters), "what you are thinking")

	result, err := tool.Call(t.Context(), []byte(`{"status":"progress","note":"patched parser; tests next"}`), nil)
	require.NoError(t, err)
	assert.Equal(t, "goal progress recorded", result.Output)

	goal, ok, err := store.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, GoalStatusActive, goal.Status)
	assert.Equal(t, "patched parser; tests next", goal.Note)
}

func TestFinishGoalTurnAccountsKickoffAndContinuation(t *testing.T) {
	t.Parallel()

	bridge := newGoalAccountingTestBridge(t)
	require.NoError(t, beginGoalDB(t.Context(), bridge.config.SessionService.db, "thread-1", &GoalState{Objective: "ship it", MaxTurns: 3, PrincipalName: "Alice"}))

	msg := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "ship it", false)
	msg.GoalAction = protocol.GoalActionKickoff
	msg.ConversationID = "thread-1"
	finishTestGoalTurn(t, bridge, msg)

	goal, ok, err := bridge.config.SessionService.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 1, goal.TurnsUsed)
	require.Len(t, bridge.requestCh, 1)
	continuation := (<-bridge.requestCh).inbound
	assert.Equal(t, protocol.GoalActionContinue, continuation.GoalAction)
	assert.Equal(t, &protocol.SlackReplyTarget{}, continuation.SlackReply)
	assert.Equal(t, map[string]string{protocol.InboundPrincipalNameMetadataKey: "Alice", protocol.InboundPrincipalEmailMetadataKey: ""}, continuation.Metadata, "the continuation keeps the goal's setter")

	msg = protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "continue", false)
	msg.GoalAction = protocol.GoalActionContinue
	msg.ConversationID = "thread-1"
	finishTestGoalTurn(t, bridge, msg)
	goal, ok, err = bridge.config.SessionService.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 2, goal.TurnsUsed)
}

func TestFinishGoalTurnHumanResteeringDoesNotConsumeBudget(t *testing.T) {
	t.Parallel()

	bridge := newGoalAccountingTestBridge(t)
	require.NoError(t, beginGoalDB(t.Context(), bridge.config.SessionService.db, "thread-1", &GoalState{Objective: "ship it", MaxTurns: 3}))

	msg := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "try this angle", false)
	msg.ConversationID = "thread-1"
	finishTestGoalTurn(t, bridge, msg)

	goal, ok, err := bridge.config.SessionService.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 0, goal.TurnsUsed)
	require.Len(t, bridge.requestCh, 1)
	continuation := (<-bridge.requestCh).inbound
	assert.Equal(t, protocol.GoalActionContinue, continuation.GoalAction)
}

func TestGoalSteeringPromptRequiresProgressSummaryAndNote(t *testing.T) {
	t.Parallel()

	prompt := goalSteeringPrompt(&GoalState{Objective: "ship it", MaxTurns: 5, TurnsUsed: 1})

	assert.Contains(t, prompt, "Progress summary:")
	assert.Contains(t, prompt, "status progress")
	assert.Contains(t, prompt, "status complete")
	assert.Contains(t, prompt, "status blocked")
	assert.Contains(t, prompt, "note")
}

func TestInterruptActiveTurnSignalsAndPreservesQueue(t *testing.T) {
	t.Parallel()

	interrupts := make(chan os.Signal, 1)
	marker := &protocol.SlackReplyTarget{ChannelID: "D123", MessageTS: "222.333", ThreadTS: "111.222"}

	bridge := &Bridge{requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{}), activeReply: &protocol.InboundMessage{SlackReply: marker}, activeTurnInterrupts: interrupts}
	queued := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "queued", false)

	queued.ConversationID = "thread-1"
	bridge.requestCh <- bridgeRequest{inbound: queued}

	result := bridge.InterruptActiveTurn()

	assert.Equal(t, marker, result.SlackReply)
	require.Len(t, bridge.requestCh, 1)
	assert.Same(t, queued, (<-bridge.requestCh).inbound)
	assert.True(t, bridge.activeTurnInterrupted)
	assert.Equal(t, os.Interrupt, <-interrupts)
}

func TestInterruptActiveTurnDoesNotDeleteThreadQueueOrScheduled(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, store.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ConversationID: conversationID, Message: "changelog", Principal: "U1", StashAt: time.Date(2000, 1, 1, 3, 0, 0, 0, time.UTC), Position: 0}))
	require.NoError(t, store.PutScheduledMessage("s1", &protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "due later", DueAt: time.Date(2000, 1, 1, 4, 0, 0, 0, time.UTC)}))

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}

	queued := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "live", false)
	bridge.requestCh <- bridgeRequest{inbound: queued}

	bridge.InterruptActiveTurn()

	assert.Len(t, bridge.requestCh, 1)

	items, err := store.ThreadQueueForConversation(conversationID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "changelog", items[0].Message)

	messages, err := store.ScheduledMessagesForConversation(conversationID)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, "due later", messages["s1"].Message)
}

func TestPickLaterWorkPrefersEarlierStashOverLaterDue(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	stashAt := time.Date(2000, 1, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ID: "q1", ConversationID: conversationID, Message: "A", Principal: "U1", StashAt: stashAt, Position: 0, SlackChannel: "C123", SlackTS: "111.222"}))
	require.NoError(t, store.PutScheduledMessage("s1", &protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "scheduled", DueAt: time.Date(2000, 1, 1, 12, 5, 0, 0, time.UTC)}))

	bridge := &Bridge{bus: discardPublisher{}, log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}

	require.NoError(t, bridge.pickLaterWork(t.Context(), false))
	require.Len(t, bridge.requestCh, 1)
	request := <-bridge.requestCh
	require.NotNil(t, request.inbound)
	assert.Equal(t, "A", request.inbound.Text)
	assert.Equal(t, "q1", request.queueItemID)
	admitted, err := bridge.activateInbound(t.Context(), &request)
	require.NoError(t, err)
	require.True(t, admitted)

	messages, err := store.ScheduledMessagesForConversation(conversationID)
	require.NoError(t, err)
	require.Len(t, messages, 1)
}

func TestPickLaterWorkWaitsWhenParkedAfterFutureScheduled(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, store.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ID: "q1", ConversationID: conversationID, Message: "Ship README", Principal: "U1", StashAt: time.Date(2000, 1, 1, 3, 0, 0, 0, time.UTC), Position: 0, ParkAfter: "s1"}))
	require.NoError(t, store.PutScheduledMessage("s1", &protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "scheduled", DueAt: time.Now().UTC().Add(time.Hour)}))

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}

	require.NoError(t, bridge.pickLaterWork(t.Context(), false))
	assert.Empty(t, bridge.requestCh)
}

// A one-time reminder waits as a turn of its conversation, which neither the lane's later work nor
// the waiting-turn claim starts before its run-at time.
func TestScheduledTurnWaitsForRunAt(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, store.PutScheduledMessage("s1", &protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "later", DueAt: time.Now().UTC().Add(time.Hour)}))

	reasons, err := queryStrings(t.Context(), store.db, `SELECT reason FROM turns WHERE conversation_id = $1 AND input_id = 's1' AND phase = 'waiting'`, "scheduled turn", conversationID)
	require.NoError(t, err)
	require.Equal(t, []string{"scheduled"}, reasons)

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
	require.NoError(t, bridge.pickLaterWork(t.Context(), false))
	assert.Empty(t, bridge.requestCh)

	admitted, err := bridge.activateInbound(t.Context(), &bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "later", false), queueItemID: "s1"})
	require.NoError(t, err)
	assert.False(t, admitted, "the queued-input claim does not start a scheduled turn")
	assert.Empty(t, runningTestTurns(t, store))
}

func TestPickLaterWorkStartsParkedQueueAfterScheduledCancel(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, store.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ID: "q1", ConversationID: conversationID, Message: "Ship README", Principal: "U1", StashAt: time.Date(2000, 1, 1, 3, 0, 0, 0, time.UTC), Position: 0, ParkAfter: "s1", SlackChannel: "C123", SlackTS: "111.222"}))
	require.NoError(t, store.PutScheduledMessage("s1", &protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "scheduled", DueAt: time.Now().UTC().Add(time.Hour)}))
	require.NoError(t, store.ResetScheduledMessages(conversationID))

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}

	require.NoError(t, bridge.pickLaterWork(t.Context(), false))
	require.Len(t, bridge.requestCh, 1)
	assert.Equal(t, "Ship README", (<-bridge.requestCh).inbound.Text)
}

func TestActivateInboundReturnsStartedScheduleDeleteError(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	require.NoError(t, store.Stop())
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: protocol.SlackThreadConversationID("C123", "111.222"), SessionService: store}}
	admitted, err := bridge.activateInbound(t.Context(), &bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "later", false), scheduledMessageID: "s1"})
	require.Error(t, err)
	assert.False(t, admitted)
}

func TestOneShotScheduleActivatesOnce(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: "thread-1", SessionService: store}, requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
	now := time.Now().UTC()
	scheduled := protocol.ScheduledMessageState{ConversationID: "thread-1", Agent: "main", Message: "follow up", DueAt: now}
	require.NoError(t, store.PutScheduledMessage("s1", &scheduled))

	// Both timers can enqueue before either request consumes the one-shot.
	for range 2 {
		require.NoError(t, bridge.submitDueScheduled(t.Context(), "s1", &scheduled, now))
	}

	first := <-bridge.requestCh
	admitted, err := bridge.activateInbound(t.Context(), &first)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NoError(t, store.closeTurn(t.Context(), first.turnID))

	second := <-bridge.requestCh
	admitted, err = bridge.activateInbound(t.Context(), &second)
	require.NoError(t, err)
	assert.False(t, admitted, "a consumed schedule cannot start another turn")
	assert.Empty(t, runningTestTurns(t, store))
}

// A timer that saw the bridge running can reach its schedule after the bridge stopped; it must not
// claim a run it cannot submit, or a recurring schedule skips that run.
func TestStoppedBridgeLeavesDueScheduleUnclaimed(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: "thread-1", SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
	now := time.Now().UTC()
	scheduled := protocol.ScheduledMessageState{ConversationID: "thread-1", Agent: "main", Message: "hourly", DueAt: now, Recurring: true, Interval: time.Hour}
	require.NoError(t, store.PutScheduledMessage("s1", &scheduled))
	bridge.Stop()

	require.ErrorIs(t, bridge.submitDueScheduled(t.Context(), "s1", &scheduled, now), protocol.ErrBridgeStopped)

	pending, err := store.ScheduledMessagesForConversation("thread-1")
	require.NoError(t, err)
	assert.True(t, pending["s1"].DueAt.Equal(now), "the next start still finds the run due")
}

func TestPickLaterWorkSkipsWhenGoalStillActive(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	recordTestConversations(t, store, "main", map[string]ConversationKind{conversationID: KindSlackThread})
	require.NoError(t, store.BeginGoal(conversationID, &GoalState{Objective: "ship it", MaxTurns: 3}))
	require.NoError(t, store.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ID: "q1", ConversationID: conversationID, Message: "changelog", Principal: "U1", StashAt: time.Date(2000, 1, 1, 3, 0, 0, 0, time.UTC), Position: 0}))

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}

	require.NoError(t, bridge.pickLaterWork(t.Context(), false))
	assert.Empty(t, bridge.requestCh)

	items, err := store.ThreadQueueForConversation(conversationID)
	require.NoError(t, err)
	require.Len(t, items, 1)

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		bridge.requestCh = make(chan bridgeRequest, 1)
		bridge.stopCh = make(chan struct{})
		require.NoError(t, bridge.submitEnqueuedItem(ctx, &items[0]))

		var group errgroup.Group
		group.Go(func() error {
			bridge.loop(ctx)
			return nil
		})
		synctest.Wait()
		require.Empty(t, bridge.requestCh)

		remaining, err := store.ThreadQueueForConversation(conversationID)
		require.NoError(t, err)
		require.Equal(t, items, remaining, "admitted enqueue stays persisted while the goal is active")
		cancel()
		require.NoError(t, group.Wait())
	})
}

func TestPickLaterWorkAfterStopGoalStartsLaterWork(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	recordTestConversations(t, store, "main", map[string]ConversationKind{conversationID: KindSlackThread})
	require.NoError(t, store.BeginGoal(conversationID, &GoalState{Objective: "ship it", MaxTurns: 3}))
	require.NoError(t, store.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ID: "q1", ConversationID: conversationID, Message: "changelog", Principal: "U1", StashAt: time.Date(2000, 1, 1, 3, 0, 0, 0, time.UTC), Position: 0, SlackChannel: "C123", SlackTS: "111.222"}))
	require.NoError(t, store.StopGoal(conversationID))

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}

	require.NoError(t, bridge.pickLaterWork(t.Context(), false))
	require.Len(t, bridge.requestCh, 1)
	assert.Equal(t, "changelog", (<-bridge.requestCh).inbound.Text)
}

func TestPickLaterWorkClaimsDueRecurringSchedule(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	dueAt := time.Now().UTC().Add(-time.Second)
	require.NoError(t, store.PutScheduledMessage("repeat", &protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "again", DueAt: dueAt, Recurring: true, Interval: time.Minute}))

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
	require.NoError(t, bridge.pickLaterWork(t.Context(), false))
	require.Len(t, bridge.requestCh, 1)
	request := <-bridge.requestCh
	assert.Equal(t, "again", request.inbound.Text)
	assert.True(t, request.scheduledMessageRecurring)

	messages, err := store.ScheduledMessagesForConversation(conversationID)
	require.NoError(t, err)
	assert.True(t, messages["repeat"].DueAt.After(dueAt))
}

func TestPickLaterWorkDoesNothingWhenIdleAndEmpty(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
	require.NoError(t, bridge.pickLaterWork(t.Context(), false))
	assert.Empty(t, bridge.requestCh)

	bridge.requestCh <- bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "queued", false)}

	require.NoError(t, bridge.pickLaterWork(t.Context(), false))
	assert.Len(t, bridge.requestCh, 1)
	close(bridge.stopCh)
	bridge.stopped = true
	require.ErrorIs(t, bridge.PickLaterWork(t.Context()), protocol.ErrBridgeStopped)
}

func TestPickLaterWorkDoesNotClaimScheduledWhenTurnBusy(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		store := newTestSessionService(t)
		conversationID := protocol.SlackThreadConversationID("C123", "111.222")
		due := protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "now", DueAt: time.Now().UTC().Add(-time.Second), Recurring: true, Interval: time.Minute}
		require.NoError(t, store.PutScheduledMessage("due", &due))

		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{}), handling: true}
		bridge.armScheduledMessage("due", &due)
		synctest.Wait()

		assert.Empty(t, bridge.requestCh)

		messages, err := store.ScheduledMessagesForConversation(conversationID)
		require.NoError(t, err)
		assert.Equal(t, due.DueAt, messages["due"].DueAt)
	})
}

func TestBridgeStopDoesNotClassifyOrdinaryTurnAsInterrupted(t *testing.T) {
	t.Parallel()

	// The turn ends with its bridge context's cause even when Stop comes first, so a shutdown lets its running calls
	// finish and a lost run lock still stops them.
	for _, cause := range []error{errShutdown, context.Canceled} {
		bridgeCtx, cancelBridge := context.WithCancelCause(t.Context())
		turn, cancel := context.WithCancel(bridgeCtx)
		bridge := &Bridge{stopCh: make(chan struct{}), activeReply: protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "ordinary", true), activeTurnCancel: cancel}
		bridge.Stop()
		cancelBridge(cause)
		assert.False(t, bridge.activeTurnInterrupted)
		assert.ErrorIs(t, context.Cause(turn), cause)
	}
}

func TestInterruptActiveWorkflowCancelsWithoutSignalChannel(t *testing.T) {
	t.Parallel()

	canceled := make(chan struct{})
	bridge := &Bridge{requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{}), activeReply: new(protocol.InboundMessage)}
	ctx, cancel := context.WithCancel(t.Context())
	bridge.activeTurnCancel = cancel

	context.AfterFunc(ctx, func() { close(canceled) })

	bridge.InterruptActiveTurn()
	<-canceled
	assert.True(t, bridge.activeTurnInterrupted)
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	n, _ := b.b.Write(p)

	return n, nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.b.String()
}

func newGoalAccountingTestBridge(t *testing.T) *Bridge {
	t.Helper()

	store := newTestSessionService(t)
	recordTestConversations(t, store, "main", map[string]ConversationKind{"thread-1": KindWebChat})

	return &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: "thread-1", SessionService: store}, requestCh: make(chan bridgeRequest, 4), stopCh: make(chan struct{})}
}

func TestRestartToolAcceptsEmptyOutputAndPropagatesErrors(t *testing.T) {
	t.Parallel()

	tool := restartTool(&runtimeRestarterMock{restartFunc: func(string) string { return "" }})
	result, err := tool.Call(t.Context(), []byte(`{"reason":"cron changed"}`), nil)
	require.NoError(t, err)
	assert.Empty(t, result.Output)

	_, err = tool.Call(t.Context(), []byte(`{"reason":" "}`), nil)
	require.EqualError(t, err, "reason is required")

	_, err = tool.Call(t.Context(), []byte(`{`), nil)
	require.ErrorContains(t, err, "parse restart request")
}

func testNoopRestart(string) (string, error) { return "", nil }

// finishTestGoalTurn finishes a turn for msg as handleInbound does: accounting in the finish transaction, then continuation.
func finishTestGoalTurn(t *testing.T, b *Bridge, msg *protocol.InboundMessage) {
	t.Helper()

	turnID := "turn-" + rand.Text()
	require.NoError(t, startTurnDB(t.Context(), b.config.SessionService.db, turnID, b.config.ConversationID, msg))
	_, err := b.config.SessionService.finishTurn(t.Context(), turnID, &turnFinish{conversationID: strings.TrimSpace(b.config.ConversationID), accountGoal: msg.GoalAction != protocol.GoalActionNone, outbound: protocol.NewOutboundMessage(b.config.ConversationID, "")})
	require.NoError(t, err)
	require.NoError(t, b.finishGoalTurn(t.Context(), &bridgeRequest{inbound: msg}))
}

// runTestTurn runs one turn and appends its staged history as a request finish does.
func runTestTurn(ctx context.Context, b *Bridge, msg *protocol.InboundMessage, turnID string) (runResult, error) {
	finish := turnFinish{conversationID: strings.TrimSpace(b.config.ConversationID)}
	b.mu.Lock()
	b.activeTurnID = turnID
	b.mu.Unlock()
	result, err := b.runTurn(ctx, msg, turnID, &finish)

	for i := range finish.entries {
		if _, errAppend := appendSessionEntryDB(context.WithoutCancel(ctx), b.config.SessionService.db, finish.conversationID, &finish.entries[i]); errAppend != nil {
			return result, errAppend
		}
	}

	return result, err
}

// startTestBridge arms schedules and starts the request loop as Run does.
func startTestBridge(ctx context.Context, b *Bridge) error {
	if b.requestCh == nil {
		b.requestCh, b.stopCh = make(chan bridgeRequest, defaultQueueSize), make(chan struct{})
	}

	if err := b.armPendingScheduledMessages(); err != nil {
		return err
	}

	go b.loop(ctx)

	return nil
}

// handleTestInbound takes a request as the bridge loop does, then handles it.
func handleTestInbound(ctx context.Context, b *Bridge, request *bridgeRequest) error {
	if _, err := b.activateInbound(ctx, request); err != nil {
		return err
	}

	return b.handleInbound(ctx, request)
}

func publishTestFinal(ctx context.Context, b *Bridge, inbound *protocol.InboundMessage, result *runResult) error {
	if err := startTurnDB(ctx, b.config.SessionService.db, result.turnID, b.config.ConversationID, inbound); err != nil {
		return err
	}

	return b.finish(ctx, &bridgeRequest{inbound: inbound, turnID: result.turnID}, &turnFinish{conversationID: strings.TrimSpace(b.config.ConversationID)}, result, "")
}

func workflowSummaryPayloadFromEntry(t *testing.T, entry *harness.SessionEntry) string {
	t.Helper()

	messages, err := replayInputMessages(entry.ReplayInput)
	require.NoError(t, err)

	for _, message := range messages {
		if payload, found := strings.CutPrefix(message.text, workflowRunSummaryPrefix); found {
			require.Equal(t, "developer", message.role)
			return payload
		}
	}

	t.Fatal("workflow run summary developer message not found")

	return ""
}

func workflowSummaryFromEntry(t *testing.T, entry *harness.SessionEntry) workflowRunSummary {
	t.Helper()

	var summary workflowRunSummary
	require.NoError(t, json.Unmarshal([]byte(workflowSummaryPayloadFromEntry(t, entry)), &summary))

	return summary
}

func TestPublishFinalPreservesTurnID(t *testing.T) {
	t.Parallel()

	bus := newTestBus()
	defer bus.Close()

	bridge := new(Bridge)
	bridge.bus = bus
	bridge.log = slog.New(slog.DiscardHandler)
	bridge.config = Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", RequestRestart: &runtimeRestarterMock{restartFunc: func(string) string { return "" }}, SessionService: newTestSessionService(t)}
	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "hello", true)
	inbound.ConversationID = bridge.config.ConversationID
	result := runResult{turnID: "turn-1", text: "hello back"}

	var group errgroup.Group

	group.Go(func() error { return publishTestFinal(context.Background(), bridge, inbound, &result) })

	final := readRocketCodeOutbound(t, bus)
	assert.Equal(t, "turn-1", final.TurnID)
	assert.Equal(t, "hello back", final.Text)
	assert.True(t, final.Complete)
	assert.Equal(t, protocol.TerminalComplete, final.Terminal)
	assert.Equal(t, protocol.SourceSlack, final.Source, "the final names the source that started its turn")
	require.NoError(t, group.Wait())
}

func TestPublishFinalMarksCurrentGoalCompletion(t *testing.T) {
	t.Parallel()

	bus := &outboundPublisherMock{PublishOutboundFunc: func(_ context.Context, outbound *protocol.OutboundMessage) error {
		assert.True(t, outbound.GoalComplete)

		return nil
	}}

	bridge := new(Bridge)
	bridge.bus = bus
	bridge.log = slog.New(slog.DiscardHandler)
	bridge.config = Config{ConversationID: "thread-1", Agent: "main", RequestRestart: &runtimeRestarterMock{restartFunc: func(string) string { return "" }}, SessionService: newTestSessionService(t)}
	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "done", true)
	inbound.ConversationID = "thread-1"
	result := runResult{turnID: "turn-1", text: "done", goalCompleted: true}

	require.NoError(t, publishTestFinal(t.Context(), bridge, inbound, &result))
	require.Len(t, bus.PublishOutboundCalls(), 1)
}

func TestPublishFinalDoesNotReuseCompletedGoal(t *testing.T) {
	t.Parallel()

	bus := &outboundPublisherMock{PublishOutboundFunc: func(_ context.Context, outbound *protocol.OutboundMessage) error {
		assert.False(t, outbound.GoalComplete)

		return nil
	}}

	store := newTestSessionService(t)
	recordTestConversations(t, store, "main", map[string]ConversationKind{"thread-1": KindWebChat})
	require.NoError(t, store.BeginGoal("thread-1", &GoalState{Objective: "ship it", MaxTurns: 3}))
	_, err := store.UpdateGoalStatus("thread-1", GoalStatusComplete, "done")
	require.NoError(t, err)

	bridge := new(Bridge)
	bridge.bus = bus
	bridge.log = slog.New(slog.DiscardHandler)
	bridge.config = Config{ConversationID: "thread-1", Agent: "main", RequestRestart: &runtimeRestarterMock{restartFunc: func(string) string { return "" }}, SessionService: store}
	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "trailing message", true)
	inbound.ConversationID = "thread-1"
	result := runResult{turnID: "turn-2", text: "normal reply"}

	require.NoError(t, publishTestFinal(t.Context(), bridge, inbound, &result))
	require.Len(t, bus.PublishOutboundCalls(), 1)
}

func TestPublishFinalCarriesMainResponseAttachments(t *testing.T) {
	t.Parallel()

	bridge := new(Bridge)
	bridge.log = slog.New(slog.DiscardHandler)
	bridge.config = Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", RequestRestart: &runtimeRestarterMock{restartFunc: func(string) string { return "" }}, SessionService: newTestSessionService(t)}
	result := runResult{turnID: "turn-1", attachments: []protocol.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}}}

	for _, errDelivery := range []error{nil, errors.New("delivery failed")} {
		synctest.Test(t, func(t *testing.T) {
			rt := new(Runtime)
			bridge.bus = rt
			events := rt.Subscribe(t.Context())
			inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "hello", true)
			inbound.ConversationID = bridge.config.ConversationID
			resultCh := inbound.EnableResponseWait()

			var group errgroup.Group

			bridge.inputOpen = true

			group.Go(func() error { return publishTestFinal(t.Context(), bridge, inbound, &result) })

			for event := range events {
				assert.True(t, event.Message.Complete)
				assert.Empty(t, event.Message.Text)
				assert.Equal(t, result.attachments, event.Message.Attachments)
				synctest.Wait()
				assert.Empty(t, resultCh, "caller must wait for final delivery")
				assert.False(t, bridge.inputOpen, "late steer must not join during final delivery")

				event.Acknowledgement <- errDelivery

				break
			}

			require.ErrorIs(t, group.Wait(), errDelivery)

			response := <-resultCh
			require.ErrorIs(t, response.Err, errDelivery)

			if errDelivery == nil {
				assert.Equal(t, result.attachments, response.Attachments)
			} else {
				assert.Empty(t, response.Attachments)
			}
		})
	}
}

func TestHandleInboundReportsRocketCodeErrorDetail(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, root.MkdirAll(".rocketclaw/agents", 0o755))
	require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))

	bus := newTestBus()
	defer bus.Close()

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })

	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	bridge := NewConversation(config.NewLockedConfig(&config.Config{Workspace: workspace}), bus, &Config{ConversationID: conversationID, Agent: "main", RequestRestart: &runtimeRestarterMock{restartFunc: func(string) string { return "" }}, SessionService: service}, slog.New(slog.DiscardHandler))
	continuation := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "continue", false)
	continuation.GoalAction = protocol.GoalActionContinue
	require.NoError(t, handleTestInbound(t.Context(), bridge, &bridgeRequest{inbound: continuation}))
	assert.Empty(t, (<-continuation.EnableResponseWait()).Text, "explicit continuation skips when no goal is active")

	inbound := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "hello", true)
	inbound.Metadata = map[string]string{"web_message_id": "failed-input"}
	inbound.ConversationID = conversationID

	var group errgroup.Group
	group.Go(func() error { return handleTestInbound(context.Background(), bridge, &bridgeRequest{inbound: inbound}) })

	consumed := readRocketCodeOutbound(t, bus)
	require.Equal(t, "failed-input", consumed.ConsumedID)
	require.Equal(t, "hello", consumed.ConsumedText)
	require.Equal(t, protocol.SourceWeb, consumed.ConsumedSource)
	require.Empty(t, consumed.ConsumedHeader, "no model prompt was generated")
	outbound := readRocketCodeOutbound(t, bus)
	assert.True(t, outbound.Complete)
	assert.Contains(t, outbound.Text, internalErrorResponse)
	assert.Contains(t, outbound.Text, `missing required default agent "main"`)
	require.ErrorContains(t, group.Wait(), `prepare rocketcode turn: missing required default agent "main"`)

	response := <-inbound.EnableResponseWait()
	assert.Equal(t, outbound.Text, response.Text)
	require.NoError(t, response.Err)
}

func TestRocketCodeConfigEnablesDiagnostics(t *testing.T) {
	t.Parallel()

	bridge := &Bridge{runtime: config.NewLockedConfig(&config.Config{AutoApproverModel: "gpt-5.4-mini"}), config: Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", RequestRestart: &runtimeRestarterMock{restartFunc: func(string) string { return "" }}, RequestReload: &runtimeReloaderMock{ReloadFunc: func(string) (string, error) {
		return "rocketclaw runtime assets reloaded", nil
	}}, SessionService: newTestSessionService(t)}}
	cfg := bridge.rocketcodeConfig(t.TempDir(), nil, harness.Tool{Name: attachFilesToolName})

	toolNames := make([]string, 0, len(cfg.CustomTools))
	for i := range cfg.CustomTools {
		toolNames = append(toolNames, cfg.CustomTools[i].Name)
	}

	assert.True(t, cfg.Diagnostics)
	assert.True(t, cfg.ExperimentalStrongerSkills)
	assert.True(t, cfg.AutoApprovePermissions)
	assert.Equal(t, "gpt-5.4-mini", cfg.AutoApproverModel)
	assert.Equal(t, 16, cfg.ParallelToolCalls)
	assert.Equal(t, harness.PromptShellCommandExpansion{PrimaryPrompts: true, SubagentPrompts: true, SkillPrompts: true, InputPrompts: false}, cfg.ExpandPromptShellCommands)
	assert.NotContains(t, toolNames, restartToolName)
	assert.NotContains(t, toolNames, scheduleMessageToolName, "each turn builds its schedule tools")
	assert.Contains(t, toolNames, reloadToolName)
	assert.Contains(t, toolNames, stopBackgroundJobToolName)
	assert.Contains(t, toolNames, attachFilesToolName)
	assert.Contains(t, toolNames, sessionsListToolName)
	assert.Contains(t, toolNames, sessionsGetToolName)
	assert.Contains(t, toolNames, sessionsGetURLToolName)
	assert.NotContains(t, toolNames, sessionsCurrentIDToolName, "each turn builds its current session tool")
	assert.Equal(t, map[string]string{"A": "B"}, bridge.rocketcodeConfig(t.TempDir(), map[string]string{"A": "B"}).ShellEnv)
}

func TestAppendOverlayPromptToAgentIncludesConfiguredOverlayPrompt(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	agents := harness.Agents{Items: map[string]harness.Agent{"main": {Name: "main", Description: "", Model: "", ReasoningEffort: "", Verbosity: "", Prompt: "base prompt", Location: "", Permission: harness.PermissionSet{Buckets: nil}, Frontmatter: nil, FileMode: 0}}}

	appendOverlayPromptToAgent(agents, "main", &config.Config{Workspace: workspace, Overlays: []string{"github.com/rocketable/overlay@main"}})

	prompt := agents.Items["main"].Prompt
	assert.Contains(t, prompt, "base prompt\n\n## Runtime Overlays")
	assert.Contains(t, prompt, "Configured overlays, in application order:")
	assert.Contains(t, prompt, "- github.com/rocketable/overlay@main")
	assert.Contains(t, prompt, "Git URL: https://github.com/rocketable/overlay")
	assert.Contains(t, prompt, "Ref: main")
	assert.Contains(t, prompt, filepath.Join(workspace, ".rocketclaw", "overlays", "github.com-rocketable-overlay-main"))
	assert.Contains(t, prompt, "Uncommitted, untracked, or unconfigured files")
}

// A submission during shutdown is stored in the Thread Queue with its full
// inbound, so it survives the process exit and starts after the restart.
func TestBridgeSubmitAfterStopStoresRequestDurably(t *testing.T) {
	t.Parallel()

	bus := newTestBus()
	defer bus.Close()

	service := newTestSessionService(t)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	bridge := NewConversation(config.NewLockedConfig(&config.Config{Workspace: t.TempDir()}), bus, &Config{ConversationID: conversationID, Agent: "main", SessionService: service}, slog.New(slog.DiscardHandler))
	require.NoError(t, startTestBridge(context.Background(), bridge))
	bridge.Stop()

	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindSteer, "hello", true)
	inbound.Metadata = map[string]string{protocol.InboundPrincipalMetadataKey: "U1"}
	inbound.SlackReply = &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.333", ThreadTS: "111.222"}
	require.NoError(t, bridge.Submit(context.Background(), inbound))

	continuation := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "continue", false)
	continuation.GoalAction = protocol.GoalActionContinue
	require.ErrorIs(t, bridge.Submit(context.Background(), continuation), protocol.ErrBridgeStopped, "goal continuations restart from the goal itself")

	items, err := service.ThreadQueueForConversation(conversationID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NotNil(t, items[0].Inbound)
	assert.Equal(t, "hello", items[0].Inbound.Text)
	assert.Equal(t, protocol.InboundKindSteer, items[0].Inbound.Kind)
	assert.Equal(t, inbound.SlackReply, items[0].Inbound.SlackReply)
	assert.Equal(t, conversationID, items[0].Inbound.ConversationID)

	visible, err := (&threadBridgeManager{store: service, bridges: map[string]directBridge{}}).queueItems(conversationID)
	require.NoError(t, err)
	assert.Len(t, visible, 1, "stored human input stays visible in $queue")
}

func TestBridgeStartReportsStateLoadError(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	require.NoError(t, service.Stop())

	bus := newTestBus()
	defer bus.Close()

	bridge := NewConversation(config.NewLockedConfig(&config.Config{Workspace: t.TempDir()}), bus, &Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", SessionService: service}, slog.New(slog.DiscardHandler))
	err := startTestBridge(context.Background(), bridge)
	require.ErrorContains(t, err, "load scheduled messages")
}

func TestBridgeEnqueueReturnsContextOrStopErrors(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	bridge := &Bridge{requestCh: make(chan bridgeRequest), stopCh: make(chan struct{})}
	err := bridge.enqueue(ctx, &bridgeRequest{}, "submit test")
	require.ErrorIs(t, err, context.Canceled)

	bridge = &Bridge{requestCh: make(chan bridgeRequest), stopCh: make(chan struct{})}
	close(bridge.stopCh)
	err = bridge.enqueue(context.Background(), &bridgeRequest{}, "submit test")
	require.ErrorIs(t, err, protocol.ErrBridgeStopped)
}

func TestBridgeRunsTaskSubagentOnItsProvider(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: '{{ model \"main\" }}'\npermission:\n  task:\n    helper: allow\n---\nPrompt\n")
	writeAgent(t, workspace, "helper", "---\ndescription: Helper\nmodel: '{{ model \"helper\" }}'\n---\nHelper prompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	requests := 0
	newServer := func(provider string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/responses" {
				http.NotFound(w, r)

				return
			}

			requests++

			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				http.Error(w, err.Error(), http.StatusBadRequest)

				return
			}

			w.Header().Set("Content-Type", "application/json")

			switch requests {
			case 1:
				assert.Equal(t, "openai", provider)
				assert.Equal(t, "main-model", body["model"])
				writeRawRunFunctionCall(t, w, "resp_1", "task", map[string]string{"description": "delegate", "prompt": "delegated prompt", "subagent_type": "helper"})
			case 2:
				assert.Equal(t, "child", provider)
				assert.Equal(t, "helper-model", body["model"])
				assert.Contains(t, fmt.Sprint(body["instructions"]), "Helper prompt")
				assert.Contains(t, fmt.Sprint(body), "delegated prompt")
				writeRawRunMessage(t, w, "resp_2", "msg_2", "child response")
			case 3:
				assert.Equal(t, "openai", provider)
				assert.Equal(t, "main-model", body["model"])
				assert.Contains(t, fmt.Sprint(body), "<task_result>\nchild response\n</task_result>")
				writeRawRunMessage(t, w, "resp_3", "msg_3", "persistent done")
			default:
				t.Fatalf("unexpected response request %d", requests)
			}
		}))
	}
	server := newServer("openai")
	t.Cleanup(server.Close)

	childServer := newServer("child")
	t.Cleanup(childServer.Close)

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })

	bus := newTestBus()
	defer bus.Close()

	conversationID := protocol.SlackThreadConversationID("C123", "111.222")

	var logs lockedBuffer

	locked := config.NewLockedConfig(&config.Config{Workspace: workspace, Models: map[string]string{"main": "main-model", "helper": "child/helper-model"}, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}, Providers: map[string]config.OpenAIConfig{"child": {APIBaseURL: childServer.URL}}})

	var (
		manager  *threadBridgeManager
		registry *backgroundRegistry
	)

	// The subagent's session runs on its own bridge.
	manager = newThreadBridgeManager(locked, service, slog.New(slog.DiscardHandler), &bridgeFactoryMock{newBridgeFunc: func(cfg *Config) directBridge {
		cfg.SessionService = service
		child := NewConversation(locked, bus, cfg, slog.New(slog.DiscardHandler))
		child.background = registry

		return child
	}})
	registry = newBackgroundRegistry(service, manager, testLogger())
	service.jobs = registry

	runTestManager(t, manager)
	require.NoError(t, service.UpsertThread(conversationID, ThreadState{Agent: "main"}))

	bridge := NewConversation(locked, bus, &Config{ConversationID: conversationID, Agent: "main", SessionService: service}, slog.New(slog.NewJSONHandler(&logs, nil)))
	bridge.threads, bridge.background = manager, registry

	// The subagent finds its top-level session's bridge through the manager, as every bridge is found.
	manager.mu.Lock()
	manager.bridges[conversationID] = bridge
	manager.mu.Unlock()

	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "hello", true)
	inbound.ConversationID = conversationID

	group, ctx := errgroup.WithContext(t.Context())
	group.Go(func() error { return handleTestInbound(ctx, bridge, &bridgeRequest{inbound: inbound}) })

	var outbound *protocol.OutboundMessage

	for msg := range bus.Outbound(ctx) {
		if msg.Complete {
			outbound = msg

			break
		}
	}

	require.NoError(t, group.Wait())
	require.NotNil(t, outbound)
	require.Equal(t, "persistent done", outbound.Text)

	decoder := json.NewDecoder(strings.NewReader(logs.String()))
	for decoder.More() {
		var event struct {
			Event string `json:"event"`
			Kind  string `json:"kind"`
		}
		require.NoError(t, decoder.Decode(&event))

		if event.Event == "first_response_item" {
			assert.NotEqual(t, harness.ChatResponseAssistantMessage, event.Kind)
		}
	}

	assert.Contains(t, logs.String(), `"event":"first_assistant_text"`)
	assert.Contains(t, logs.String(), `"event":"operation_snapshot"`)
	assert.NotContains(t, logs.String(), "delegated prompt")
	require.Equal(t, 3, requests)

	entries, err := service.ObserveEntries(context.Background(), conversationID)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "openai/main-model", entries[0].Entry.Model)
}

func TestBridgeStopAfterStartContextCanceledIsIdempotent(t *testing.T) {
	t.Parallel()

	bus := newTestBus()
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	bridge := NewConversation(config.NewLockedConfig(&config.Config{Workspace: t.TempDir()}), bus, &Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", SessionService: newTestSessionService(t)}, slog.New(slog.DiscardHandler))
	require.NoError(t, startTestBridge(ctx, bridge))

	cancel()

	select {
	case <-bridge.stopCh:
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop after context cancellation")
	}

	bridge.Stop()
}

func TestScheduleMessageToolValidatesAndPreservesMessage(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer

	service := newTestSessionService(t)
	bridge := &Bridge{log: slog.New(slog.NewJSONHandler(&logs, nil)), config: Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", SessionService: service}}
	tool := bridge.scheduleMessageTool(new(protocol.InboundMessage))
	assert.ElementsMatch(t, []string{"message", "send_this_in", "recurring"}, tool.Parameters["required"])

	result, err := tool.Call(context.Background(), []byte(`{"message":"  keep spaces  ","send_this_in":"1h"}`), nil)
	require.NoError(t, err)
	assert.Equal(t, "scheduled message in 1h0m0s", result.Output)
	assert.Contains(t, logs.String(), "rocketclaw schedule message tool called")

	result, err = tool.Call(context.Background(), []byte(`{"message":"again","send_this_in":"1h","recurring":true}`), nil)
	require.NoError(t, err)
	assert.Equal(t, "scheduled recurring message every 1h0m0s", result.Output)

	messages, err := service.ScheduledMessagesForConversation(bridge.config.ConversationID)
	require.NoError(t, err)

	recurring := map[string]bool{}
	for _, message := range messages {
		recurring[message.Message] = message.Recurring
	}

	assert.Equal(t, map[string]bool{"  keep spaces  ": false, "again": true}, recurring)

	for _, delay := range []time.Duration{12 * time.Hour, 2160 * time.Hour} {
		for _, recurring := range []bool{false, true} {
			t.Run(fmt.Sprintf("accepted%s/recurring=%t", delay, recurring), func(t *testing.T) {
				message := fmt.Sprintf("  follow up after %s recurring=%t  ", delay, recurring)
				before := time.Now().UTC().Truncate(time.Microsecond)
				result, err := tool.Call(t.Context(), fmt.Appendf(nil, `{"message":%q,"send_this_in":%q,"recurring":%t}`, message, delay, recurring), nil)
				require.NoError(t, err)

				after := time.Now().UTC()

				if recurring {
					assert.Equal(t, "scheduled recurring message every "+delay.String(), result.Output)
				} else {
					assert.Equal(t, "scheduled message in "+delay.String(), result.Output)
				}

				messages, err := service.ScheduledMessagesForConversation(bridge.config.ConversationID)
				require.NoError(t, err)

				byMessage := make(map[string]protocol.ScheduledMessageState)
				for _, scheduled := range messages {
					byMessage[scheduled.Message] = scheduled
				}

				scheduled, ok := byMessage[message]
				require.True(t, ok, "scheduled message preserves exact text")
				assert.WithinRange(t, scheduled.DueAt, before.Add(delay), after.Add(delay))
				assert.Equal(t, recurring, scheduled.Recurring)

				interval := time.Duration(0)
				if recurring {
					interval = delay
				}

				assert.Equal(t, interval, scheduled.Interval)
			})
		}
	}

	for _, raw := range []string{
		`{`,
		`{"message":"  ","send_this_in":"5m"}`,
		`{"message":"hello","send_this_in":"nope"}`,
		`{"message":"hello","send_this_in":"0s"}`,
		`{"message":"hello","send_this_in":"-1h"}`,
		`{"message":"hello","send_this_in":"2160h1ns"}`,
		`{"message":"hello","send_this_in":"30s","recurring":true}`,
	} {
		_, err := tool.Call(context.Background(), []byte(raw), nil)
		require.Error(t, err, raw)
	}

	require.NoError(t, service.Stop())

	_, err = tool.Call(context.Background(), []byte(`{"message":"hello","send_this_in":"5m"}`), nil)
	require.ErrorContains(t, err, "persist scheduled message")
	assert.Contains(t, logs.String(), "rocketclaw schedule message tool failed")
}

func TestResetScheduledMessagesToolUsesScheduleSubject(t *testing.T) {
	t.Parallel()

	service := newTestSessionService(t)
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: "slack-thread:C123:111.222", SessionService: service}}
	require.NoError(t, service.PutScheduledMessage("later", &protocol.ScheduledMessageState{ConversationID: bridge.config.ConversationID, Agent: "main", Message: "later", DueAt: time.Now().Add(time.Hour)}))

	tool := bridge.resetScheduledMessagesTool(new(protocol.InboundMessage))
	assert.Equal(t, resetScheduledMessagesToolName, tool.Name)
	assert.Equal(t, []string{scheduleMessageToolName}, tool.VisibilitySubjects)
	subjects, err := tool.Subjects(nil)
	require.NoError(t, err)
	assert.Equal(t, []string{scheduleMessageToolName}, subjects)

	result, err := tool.Call(context.Background(), nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "scheduled messages reset", result.Output)

	messages, err := service.ScheduledMessagesForConversation(bridge.config.ConversationID)
	require.NoError(t, err)
	assert.Empty(t, messages)

	require.NoError(t, service.Stop())

	_, err = tool.Call(context.Background(), nil, nil)
	require.ErrorContains(t, err, "reset scheduled messages")
}

func TestAttachFilesToolReadsWorkspacePath(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, root.Mkdir("reports", 0o755))

	data := bytes.Repeat([]byte("report body"), 400000)
	require.NoError(t, root.WriteFile("reports/latest.txt", data, 0o644))

	sessions := newTestSessionService(t)
	tool := attachFilesTool(root, sessions, "attachment-test", "turn-1")
	parameters := tool.Parameters
	properties := parameters["properties"].(map[string]any)
	attachmentsSchema := properties["attachments"].(map[string]any)
	items := attachmentsSchema["items"].(map[string]any)
	assert.ElementsMatch(t, []string{"path", "name", "mime_type", "content", "content_base64"}, items["required"])

	result, err := tool.Call(t.Context(), []byte(`{"attachments":[{"path":"reports/latest.txt","name":"","mime_type":"","content":"","content_base64":""}]}`), nil)
	require.NoError(t, err)
	require.NotEqual(t, "queued attachments for final response", result.Output, "successful tool replay must carry immutable attachment IDs")
	require.Less(t, len(result.Output), 100, "attachment bytes must not enter tool output")

	got, err := sessions.TurnAttachments(t.Context(), "attachment-test", "turn-1")
	require.NoError(t, err)
	require.Len(t, got, 1, "the turn's reply, resumed or not, sends the saved file")
	require.Contains(t, result.Output, got[0].ID)
	require.NoError(t, root.WriteFile("reports/latest.txt", []byte("changed"), 0o644))
	stored, err := sessions.LoadAttachment(t.Context(), "attachment-test", got[0].ID, false)
	require.NoError(t, err)
	assert.Equal(t, protocol.OutboundAttachment{ID: got[0].ID, Name: "latest.txt", MIMEType: "text/plain", Data: data, Size: int64(len(data))}, stored)

	calls := make(map[string]string)
	_, err = sessions.ReplayAttachments(t.Context(), "attachment-test", root, json.RawMessage(`{"type":"function_call","call_id":"capture","name":"rocketclaw_attach_files_to_response","arguments":"{}"}`), calls)
	require.NoError(t, err)
	replayed, err := sessions.ReplayAttachments(t.Context(), "attachment-test", root, json.RawMessage(fmt.Sprintf(`{"type":"function_call_output","call_id":"capture","output":%q}`, result.Output)), calls)
	require.NoError(t, err)
	require.Equal(t, []protocol.OutboundAttachment{{ID: got[0].ID, Name: "latest.txt", MIMEType: "text/plain", Size: int64(len(data))}}, replayed)
	require.False(t, replayed[0].OriginalUnverified)
	require.NoError(t, root.WriteFile("reports/empty.txt", []byte{}, 0o600))
	_, err = tool.Call(t.Context(), []byte(`{"attachments":[{"path":"reports/empty.txt"}]}`), nil)
	require.NoError(t, err)
	got, err = sessions.TurnAttachments(t.Context(), "attachment-test", "turn-1")
	require.NoError(t, err)
	require.Len(t, got, 2)
	stored, err = sessions.LoadAttachment(t.Context(), "attachment-test", got[1].ID, false)
	require.NoError(t, err)
	require.Empty(t, stored.Data)
}

func TestOutboundAttachmentSources(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	got, err := outboundAttachment(root, &outboundAttachmentInput{Name: "note.txt", Content: "hello"})
	require.NoError(t, err)
	assert.Equal(t, protocol.OutboundAttachment{Name: "note.txt", MIMEType: "text/plain", Data: []byte("hello")}, got)

	got, err = outboundAttachment(root, &outboundAttachmentInput{Name: "", MIMEType: "Text/Plain; Charset=UTF-8", ContentBase64: "aGVsbG8="})
	require.NoError(t, err)
	assert.Equal(t, protocol.OutboundAttachment{Name: "attachment", MIMEType: "text/plain", Data: []byte("hello")}, got)

	got, err = outboundAttachment(root, &outboundAttachmentInput{Name: "blob", Content: "hello"})
	require.NoError(t, err)
	assert.Equal(t, protocol.OutboundAttachment{Name: "blob", MIMEType: "text/plain", Data: []byte("hello")}, got)

	_, err = outboundAttachment(root, &outboundAttachmentInput{Name: "bad", ContentBase64: "%%%"})
	require.ErrorContains(t, err, `decode attachment "bad"`)

	_, err = outboundAttachment(root, &outboundAttachmentInput{Path: "missing.txt"})
	require.ErrorContains(t, err, `read attachment "missing.txt"`)

	_, err = outboundAttachment(root, &outboundAttachmentInput{Name: "empty"})
	require.ErrorContains(t, err, `attachment "empty" has no content or path`)
}

func TestAttachFilesToolReportsInvalidInput(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	tool := attachFilesTool(root, newTestSessionService(t), "attachment-test", "turn-1")
	_, err = tool.Call(t.Context(), []byte(`{`), nil)
	require.ErrorContains(t, err, "parse response attachments")

	raw := []byte(`{"attachments":[{"path":"missing.txt","name":"","mime_type":"","content":"","content_base64":""}]}`)
	_, err = tool.Call(t.Context(), raw, nil)
	require.ErrorContains(t, err, `read attachment "missing.txt"`)
}

func TestAttachmentFallbackAndImageAttachments(t *testing.T) {
	t.Parallel()

	assert.Empty(t, attachmentFallback(&protocol.InboundMessage{Attachments: []protocol.InboundAttachment{{Name: "image.png"}}}))
	assert.Equal(t, unsupportedFileFallback, attachmentFallback(&protocol.InboundMessage{AttachmentPresence: protocol.AttachmentPresenceUnsupported}))
	assert.Contains(t, attachmentFallback(&protocol.InboundMessage{AttachmentPresence: protocol.AttachmentPresenceImages, AttachmentWarnings: []string{" first ", " ", "second"}}), "- first\n- second")

	attachments := attachmentsFromInbound([]protocol.InboundAttachment{
		{Name: "photo.jpg", MIMEType: "image/jpeg", Data: []byte("jpg")},
		{Name: "unknown", MIMEType: "image/webp", Data: []byte("webp")},
	})
	require.Len(t, attachments, 2)
	assert.Equal(t, "image/jpeg", attachments[0].MIME)
	assert.Equal(t, "data:image/jpeg;base64,anBn", attachments[0].URL)
	assert.Equal(t, "image/webp", attachments[1].MIME)
	assert.Equal(t, "data:image/webp;base64,d2VicA==", attachments[1].URL)
	assert.Equal(t, "new", appendText("", " new "))
	assert.Equal(t, "old\nnew", appendText("old", " new "))
	assert.Equal(t, "old", appendText("old", " "))

	bus := newTestBus()
	t.Cleanup(bus.Close)
	bridge := &Bridge{bus: bus, config: Config{ConversationID: "web-fallback", SessionService: newTestSessionService(t)}, log: slog.New(slog.DiscardHandler)}
	inbound := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "", true)
	inbound.AttachmentPresence = protocol.AttachmentPresenceUnsupported
	inbound.Metadata = map[string]string{"web_message_id": "attachment-input"}

	// The buffered bus needs no reader while the fallback handles its database work.
	require.NoError(t, handleTestInbound(t.Context(), bridge, &bridgeRequest{inbound: inbound}))
	require.Len(t, bus.outbound, 2, "fallback must publish consumed input followed by its final response")

	consumed := readRocketCodeOutbound(t, bus)
	require.Equal(t, "attachment-input", consumed.ConsumedID)
	require.Empty(t, consumed.ConsumedHeader, "no model prompt was generated")
	final := readRocketCodeOutbound(t, bus)
	require.Equal(t, unsupportedFileFallback, final.Text)
}

func TestNormalizeInboundAttachmentsCentralizesModelPolicy(t *testing.T) {
	t.Parallel()

	msg := &protocol.InboundMessage{Attachments: []protocol.InboundAttachment{
		{Name: " tiny.png ", MIMEType: "application/octet-stream", Data: tinyPNG()},
		{Name: "not-image.png", MIMEType: "image/png", Data: []byte("not an image")},
		{Name: "empty.png", MIMEType: "image/png"},
		{Name: "photo.jpg", MIMEType: " Image/PNG; Charset=UTF-8 "},
		{Name: "photo.jpg", MIMEType: " "},
		{Name: "photo", MIMEType: " "},
		{Name: " ", MIMEType: "image/png"},
		{Name: "second.png", MIMEType: "image/jpeg", Data: tinyPNG()},
	}}

	normalizeInboundAttachments(msg)

	assert.Equal(t, protocol.AttachmentPresenceImages, msg.AttachmentPresence)
	assert.Equal(t, []protocol.InboundAttachment{
		{Name: "tiny.png", MIMEType: "image/png", Data: tinyPNG()},
		{Name: "second.png", MIMEType: "image/png", Data: tinyPNG()},
	}, msg.Attachments)
	assert.Equal(t, []string{
		"Skipped attachment not-image.png because text/plain is not supported.",
		"Skipped attachment empty.png because it was empty.",
		"Skipped attachment photo.jpg because it was empty.",
		"Skipped attachment photo.jpg because it was empty.",
		"Skipped attachment photo because it was empty.",
		"Skipped attachment attachment-7 because it was empty.",
	}, msg.AttachmentWarnings)
}

func TestNormalizeInboundAttachmentsRejectsAttachmentsTooLargeToReduce(t *testing.T) {
	t.Parallel()

	msg := &protocol.InboundMessage{Attachments: []protocol.InboundAttachment{
		{Name: "large.png", MIMEType: "image/png", Data: append(tinyPNG(), make([]byte, maxInboundAttachmentResizeInput)...)},
	}}

	normalizeInboundAttachments(msg)

	assert.Empty(t, msg.Attachments)
	assert.Equal(t, protocol.AttachmentPresenceImages, msg.AttachmentPresence)
	assert.Equal(t, []string{"Skipped attachment large.png because it was too large to attempt size reduction."}, msg.AttachmentWarnings)
}

func TestReduceResizedImageWithinLimitReportsEncoderErrorsAndExhaustion(t *testing.T) {
	t.Parallel()

	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))

	_, err := reduceResizedImageWithinLimit(img, 1000, 1, &inboundImageEncoderMock{encodeWithinLimitFunc: func(image.Image, int) ([]byte, int, error) {
		return nil, 0, assert.AnError
	}})
	require.ErrorIs(t, err, assert.AnError)

	_, err = reduceResizedImageWithinLimit(img, 1000, 1, &inboundImageEncoderMock{encodeWithinLimitFunc: func(image.Image, int) ([]byte, int, error) {
		return nil, 1000, nil
	}})
	require.ErrorIs(t, err, errInboundAttachmentReductionNotEnough)
}

func TestFitInboundImageWithinLimitLeavesSmallImageUnchanged(t *testing.T) {
	t.Parallel()

	data := encodeAttachmentTestPNG(t, newAttachmentTestImage(1, 1), png.BestCompression)

	transformed, transformedMIMEType, changed, err := fitInboundImageWithinLimit(" Image/PNG; Charset=UTF-8 ", data, len(data))
	require.NoError(t, err)
	assert.Equal(t, data, transformed)
	assert.Equal(t, "image/png", transformedMIMEType)
	assert.False(t, changed)
}

func TestFitInboundImageWithinLimitUsesLosslessPNGFirst(t *testing.T) {
	t.Parallel()

	img := newAttachmentTestImage(160, 160)
	original := encodeAttachmentTestPNG(t, img, png.NoCompression)
	target := len(encodeAttachmentTestPNG(t, img, png.BestCompression))
	require.Greater(t, len(original), target)

	transformed, transformedMIMEType, changed, err := fitInboundImageWithinLimit("image/png", original, target)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "image/png", transformedMIMEType)
	assert.LessOrEqual(t, len(transformed), target)

	originalConfig := decodeAttachmentTestImageConfig(t, original)
	transformedConfig := decodeAttachmentTestImageConfig(t, transformed)
	assert.Equal(t, originalConfig.Width, transformedConfig.Width)
	assert.Equal(t, originalConfig.Height, transformedConfig.Height)
}

func TestFitInboundImageWithinLimitUsesSmallestSuccessfulJPEGChangeFirst(t *testing.T) {
	t.Parallel()

	img := newAttachmentTestImage(256, 256)
	original := encodeAttachmentTestJPEG(t, img, 100)
	target := 0

	for quality := 95; quality >= 50; quality -= 5 {
		candidateSize := len(encodeAttachmentTestJPEG(t, img, quality))
		if candidateSize < len(original) {
			target = candidateSize
			break
		}
	}

	require.NotZero(t, target)

	transformed, transformedMIMEType, changed, err := fitInboundImageWithinLimit("image/jpeg", original, target)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "image/jpeg", transformedMIMEType)
	assert.LessOrEqual(t, len(transformed), target)

	originalConfig := decodeAttachmentTestImageConfig(t, original)
	transformedConfig := decodeAttachmentTestImageConfig(t, transformed)
	assert.Equal(t, originalConfig.Width, transformedConfig.Width)
	assert.Equal(t, originalConfig.Height, transformedConfig.Height)
}

func TestFitInboundImageWithinLimitResizesPNG(t *testing.T) {
	t.Parallel()

	original := encodeAttachmentTestPNG(t, newAttachmentTestImage(400, 400), png.NoCompression)
	target := 4096
	require.Greater(t, len(original), target)

	transformed, transformedMIMEType, changed, err := fitInboundImageWithinLimit("image/png", original, target)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "image/png", transformedMIMEType)
	assert.LessOrEqual(t, len(transformed), target)

	originalConfig := decodeAttachmentTestImageConfig(t, original)
	transformedConfig := decodeAttachmentTestImageConfig(t, transformed)
	assert.Less(t, transformedConfig.Width, originalConfig.Width)
	assert.Less(t, transformedConfig.Height, originalConfig.Height)
}

func TestFitInboundImageWithinLimitFallsBackToJPEG(t *testing.T) {
	t.Parallel()

	original := encodeAttachmentTestPNG(t, newAttachmentTestImage(80, 80), png.NoCompression)
	target := 1024
	require.Greater(t, len(original), target)

	transformed, transformedMIMEType, changed, err := fitInboundImageWithinLimit("image/webp", original, target)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "image/jpeg", transformedMIMEType)
	assert.LessOrEqual(t, len(transformed), target)

	cfg := decodeAttachmentTestImageConfig(t, transformed)
	assert.NotZero(t, cfg.Width)
	assert.NotZero(t, cfg.Height)

	oversized := encodeAttachmentTestPNG(t, newAttachmentTestImage(65536, 1), png.NoCompression)
	transformed, transformedMIMEType, changed, err = fitInboundImageWithinLimit("image/webp", oversized, 1)
	assert.Nil(t, transformed)
	assert.Empty(t, transformedMIMEType)
	assert.False(t, changed)
	require.ErrorIs(t, err, errInboundAttachmentReductionFailed)

	errJPEGText := "jpeg: image is too large to encode"
	require.EqualError(t, err, errInboundAttachmentReductionFailed.Error()+": "+strings.Repeat("encode jpeg: ", 2)+errJPEGText)
	chain, ok := errors.AsType[interface {
		Error() string
		Unwrap() []error
	}](err)
	require.True(t, ok)
	require.Len(t, chain.Unwrap(), 2)
	assert.Same(t, errInboundAttachmentReductionFailed, chain.Unwrap()[0])
	require.EqualError(t, chain.Unwrap()[1], "encode jpeg: "+errJPEGText)
	require.EqualError(t, errors.Unwrap(chain.Unwrap()[1]), errJPEGText)
}

func TestFitInboundImageWithinLimitReportsDecodeFailure(t *testing.T) {
	t.Parallel()

	transformed, transformedMIMEType, changed, err := fitInboundImageWithinLimit("image/webp", []byte("not an image"), 1)
	assert.Nil(t, transformed)
	assert.Empty(t, transformedMIMEType)
	assert.False(t, changed)
	assert.ErrorIs(t, err, errInboundAttachmentReductionFailed)
}

func TestFitInboundImageWithinLimitReportsPNGDecodeFailure(t *testing.T) {
	t.Parallel()

	transformed, transformedMIMEType, changed, err := fitInboundImageWithinLimit("image/png", []byte("not a png"), 1)
	assert.Nil(t, transformed)
	assert.Empty(t, transformedMIMEType)
	assert.False(t, changed)
	assert.ErrorIs(t, err, errInboundAttachmentReductionFailed)
}

func TestResizePNGWithinLimitRejectsSinglePixelImageTooLargeForTarget(t *testing.T) {
	t.Parallel()

	original := encodeAttachmentTestPNG(t, newAttachmentTestImage(1, 1), png.NoCompression)

	transformed, changed, err := resizePNGWithinLimit(original, 1)
	assert.Nil(t, transformed)
	assert.False(t, changed)
	assert.ErrorIs(t, err, errInboundAttachmentReductionNotEnough)
}

func TestNextImageResizeDimensionsStillShrinksWhenEstimateWouldGrow(t *testing.T) {
	t.Parallel()

	nextWidth, nextHeight := nextImageResizeDimensions(2, 2, 100, 10000)

	assert.Equal(t, 1, nextWidth)
	assert.Equal(t, 1, nextHeight)
}

func TestInboundAttachmentReductionFailureReason(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "image reduction failed", inboundAttachmentReductionFailureReason(errInboundAttachmentReductionFailed, maxInboundAttachmentBytes))
	assert.Equal(t, "it still exceeded the remaining attachment budget after reduction", inboundAttachmentReductionFailureReason(errInboundAttachmentReductionNotEnough, 1))
	assert.Equal(t, "it still exceeded the per-file size limit after reduction", inboundAttachmentReductionFailureReason(errInboundAttachmentReductionNotEnough, maxInboundAttachmentBytes))
}

func TestNormalizeInboundAttachmentsRejectsTotalBudgetOverflow(t *testing.T) {
	t.Parallel()

	data := append(tinyPNG(), make([]byte, maxInboundAttachmentBytes-len(tinyPNG()))...)
	msg := &protocol.InboundMessage{Attachments: []protocol.InboundAttachment{
		{Name: "one.png", MIMEType: "image/png", Data: data},
		{Name: "two.png", MIMEType: "image/png", Data: data},
		{Name: "three.png", MIMEType: "image/png", Data: data},
		{Name: "four.png", MIMEType: "image/png", Data: data},
		{MIMEType: "image/png", Data: data},
	}}

	normalizeInboundAttachments(msg)

	require.Len(t, msg.Attachments, 4)
	assert.Equal(t, []string{"Skipped attachment attachment-5 because the message exceeded the attachment size budget."}, msg.AttachmentWarnings)
}

func tinyPNG() []byte {
	return []byte("\x89PNG\r\n\x1a\n")
}

func newAttachmentTestImage(width, height int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, color.NRGBA{R: uint8((x*31 + y*17) % 256), G: uint8((x*13 + y*29) % 256), B: uint8((x*7 + y*19) % 256), A: 0xff})
		}
	}

	return img
}

func encodeAttachmentTestPNG(t *testing.T, img image.Image, level png.CompressionLevel) []byte {
	t.Helper()

	var buffer bytes.Buffer

	encoder := png.Encoder{CompressionLevel: level, BufferPool: nil}
	require.NoError(t, encoder.Encode(&buffer, img))

	return buffer.Bytes()
}

func encodeAttachmentTestJPEG(t *testing.T, img image.Image, quality int) []byte {
	t.Helper()

	var buffer bytes.Buffer

	options := jpeg.Options{Quality: quality}
	require.NoError(t, jpeg.Encode(&buffer, img, &options))

	return buffer.Bytes()
}

func decodeAttachmentTestImageConfig(t *testing.T, data []byte) image.Config {
	t.Helper()

	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	require.NoError(t, err)

	return cfg
}

func TestModelResolverConfiguresOpenAI(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "main", `---
description: Main
model: gpt-6.1-sol
---
Prompt
`)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	agents, skills, err := loadRocketCodeDefinitions(root, workspace, toolModePersistent)
	require.NoError(t, err)

	resolver := newModelResolver(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIKey: "test-key", Auth: "api_key"}}, slog.New(slog.DiscardHandler))
	client, origin, err := resolver.Resolve("gpt-6.1-sol")
	require.NoError(t, err)
	require.NotNil(t, client)
	require.Equal(t, harness.ProviderOrigin{Provider: "openai", Model: "gpt-6.1-sol"}, origin)

	shellTempDir := filepath.Join(workspace, "shell-tmp")
	require.NoError(t, os.Mkdir(shellTempDir, 0o755))
	_, err = harness.NewWithModelResolver(resolver, &harness.Config{ShellTempDir: shellTempDir, RetainedResultDir: "retained", ChildSessions: harness.InertChildSessions{}, Journal: harness.InertJournal{}, ShellCommand: harness.DefaultShellCommand{}}, root, agents, skills, "main", io.Discard)
	require.NoError(t, err)
}

func TestReplayInputMessageRoleTextCoversMessageShapes(t *testing.T) {
	t.Parallel()

	msg := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindSteer, "[literal]\n\nuser text", true)
	msg.Metadata = map[string]string{protocol.InboundPrincipalMetadataKey: `alice "quoted" ]`}
	framed := buildPrompt(msg, nil, false)
	header, _, _ := strings.Cut(framed, "\n\n")

	for _, tc := range []struct{ role, input, want string }{
		{"user", framed, msg.Text},
		{"developer", framed, msg.Text},
		{"user", header + "\n\n" + framed, framed},
		{"assistant", framed, framed},
		{"user", "[literal]\n\nuser text", "[literal]\n\nuser text"},
		{"user", "[Web media=Text principal=alice]\n\nkeep", "[Web media=Text principal=alice]\n\nkeep"},
		{"user", "prefix\n" + framed, "prefix\n" + framed},
		{"user", strings.Replace(framed, "\n\n", "\n", 1), strings.Replace(framed, "\n\n", "\n", 1)},
	} {
		message := responses.ResponseInputItemUnionParam{OfMessage: &responses.EasyInputMessageParam{Role: responses.EasyInputMessageRole(tc.role), Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(tc.input)}, Type: "message"}}
		message.OfMessage.SetExtraFields(map[string]any{"prompt_header": header})
		role, text, ok, err := ReplayInputMessageRoleText(&message, nil)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, tc.role, role)
		require.Equal(t, tc.want, text)
		require.Equal(t, tc.input, message.OfMessage.Content.OfString.Value)
	}

	old := responses.ResponseInputItemUnionParam{OfMessage: &responses.EasyInputMessageParam{Role: "user", Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(framed)}, Type: "message"}}
	_, textOld, _, errOld := ReplayInputMessageRoleText(&old, nil)
	require.NoError(t, errOld)
	require.Equal(t, framed, textOld)

	plain := responses.ResponseInputItemUnionParam{OfMessage: &responses.EasyInputMessageParam{Role: "assistant", Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("plain")}, Type: "message"}}
	role, text, ok, err := ReplayInputMessageRoleText(&plain, nil)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "assistant", role)
	assert.Equal(t, "plain", text)

	withContent := responses.ResponseInputItemUnionParam{OfInputMessage: &responses.ResponseInputItemMessageParam{Role: "user", Content: responses.ResponseInputMessageContentListParam{responses.ResponseInputContentParamOfInputText("look")}, Type: "message"}}
	role, text, ok, err = ReplayInputMessageRoleText(&withContent, nil)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "user", role)
	assert.Equal(t, "look", text)

	output := responses.ResponseInputItemUnionParam{OfOutputMessage: &responses.ResponseOutputMessageParam{Content: []responses.ResponseOutputMessageContentUnionParam{{OfOutputText: &responses.ResponseOutputTextParam{Text: "answer"}}}}}
	role, text, ok, err = ReplayInputMessageRoleText(&output, nil)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "assistant", role)
	assert.Equal(t, "answer", text)

	_, _, ok, err = ReplayInputMessageRoleText(&responses.ResponseInputItemUnionParam{}, nil)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestReplayInputMessagesFiltersBlankMessages(t *testing.T) {
	t.Parallel()

	raw, err := harness.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		{OfMessage: &responses.EasyInputMessageParam{Role: "user", Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(" ")}, Type: "message"}},
		{OfMessage: &responses.EasyInputMessageParam{Role: "assistant", Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("answer")}, Type: "message"}},
	})
	require.NoError(t, err)

	messages, err := replayInputMessages(raw)
	require.NoError(t, err)
	assert.Equal(t, []replayInputMessage{{role: "assistant", text: "answer"}}, messages)
}

func TestReplayInputMessagesReportsBadJSON(t *testing.T) {
	t.Parallel()

	_, err := replayInputMessages([]json.RawMessage{json.RawMessage("{")})
	require.ErrorContains(t, err, "decode replay input messages")
}

func TestSeedReplayTextIncludesWebSearchContext(t *testing.T) {
	t.Parallel()

	text := seedReplayText([]responses.ResponseInputItemUnionParam{{OfWebSearchCall: &responses.ResponseFunctionWebSearchParam{Action: responses.ResponseFunctionWebSearchActionUnionParam{OfSearch: &responses.ResponseFunctionWebSearchActionSearchParam{Queries: []string{"golang release"}}}, Status: "completed"}}})

	assert.Contains(t, text, "web search completed")
	assert.Contains(t, text, "golang release")
}

func TestReplayInputRawKindReportsInvalidJSON(t *testing.T) {
	t.Parallel()

	assert.Empty(t, harness.ReplayInputRawKind(json.RawMessage("{")))
}

func TestBuildPromptCoversAttachments(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		source protocol.Source
		origin string
	}{
		{protocol.SourceSlack, "Slack"},
		{protocol.SourceWeb, "Web"},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			assert.Equal(t, "["+tc.origin+" principal=\"Alice\" additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\nhello\n\nAttachment notes:\n- skipped image", buildPrompt(&protocol.InboundMessage{Source: tc.source, Human: true, Text: "  hello  ", AttachmentWarnings: []string{" skipped image ", " "}, Metadata: map[string]string{protocol.InboundPrincipalMetadataKey: "Alice"}}, nil, false))
		})
	}

	assert.Equal(t, "[System additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\nAttachment notes:\n- unsupported PDF", buildPrompt(&protocol.InboundMessage{AttachmentWarnings: []string{" unsupported PDF "}}, nil, false))
}

func TestBuildPromptAdditionalInstructionsFrontmatter(t *testing.T) {
	t.Parallel()

	msg := &protocol.InboundMessage{Source: protocol.SourceSlack, Human: true, Text: "hello", Metadata: map[string]string{protocol.InboundPrincipalMetadataKey: "Alice"}}

	assert.Equal(t, "[Slack principal=\"Alice\" additional_instructions=\"Reply in one sentence.\"]\n\nhello", buildPrompt(msg, map[string]any{"additionalInstructions": "Reply in one sentence."}, false))
	assert.Equal(t, "[Slack principal=\"Alice\" additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\nhello", buildPrompt(msg, map[string]any{"additionalInstructions": " "}, false))
	assert.Equal(t, "[Slack principal=\"Alice\" additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\nhello", buildPrompt(msg, map[string]any{"additionalInstructions": 7}, false))
	assert.Equal(t, "[System additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\n task \n", buildPrompt(&protocol.InboundMessage{Source: protocol.SourceSystem, PreserveWhitespace: true, Text: " task \n", Metadata: map[string]string{protocol.InboundOriginMetadataKey: "System", protocol.InboundMediaMetadataKey: "Text"}}, nil, false))
}

func TestParseDirectSkillTrigger(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"$docs-helper write docs", "$skill docs-helper write docs"} {
		directSkill := parseDirectSkillTrigger(text)

		require.NotNil(t, directSkill)
		assert.Equal(t, &harness.PromptInputDirectSkill{Name: "docs-helper", Arguments: "write docs"}, directSkill)
	}

	directSkill := parseDirectSkillTrigger("$skill   ")
	require.NotNil(t, directSkill)
	assert.Empty(t, directSkill.Name)

	assert.Nil(t, parseDirectSkillTrigger("hello $docs-helper"))

	for _, name := range []string{"", "agent", "cron", "workflow", "stop", "enqueue", "queue", "goal"} {
		assert.Nil(t, parseDirectSkillTrigger("$"+name))
		assert.Equal(t, &harness.PromptInputDirectSkill{Name: name, Arguments: ""}, parseDirectSkillTrigger("$skill "+name))
	}

	assert.Equal(t, &harness.PromptInputDirectSkill{Name: "Docs", Arguments: "\"API guide\"  Next\tLast "}, parseDirectSkillTrigger("  $Docs \"API guide\"  Next\tLast "))

	for _, source := range []protocol.Source{protocol.SourceSlack, protocol.SourceWeb, protocol.SourceSystem, protocol.SourceExternalMCP} {
		for _, kind := range []protocol.InboundKind{protocol.InboundKindPrompt, protocol.InboundKindSteer, protocol.InboundKindEnqueue, protocol.InboundKindCancel} {
			for _, text := range []string{"$docs-helper typed", ""} {
				msg := protocol.NewInboundMessageFromContent(source, kind, &protocol.InboundContent{Text: text, TextAttachments: []string{"$docs-helper attachment"}}, true)
				if text != "" && source == protocol.SourceWeb && kind != protocol.InboundKindCancel {
					assert.Equal(t, &harness.PromptInputDirectSkill{Name: "docs-helper", Arguments: "typed"}, inboundDirectSkill(msg))
				} else {
					assert.Nil(t, inboundDirectSkill(msg))
				}

				msg.Human = false
				assert.Nil(t, inboundDirectSkill(msg))
			}
		}
	}
}

func TestInboundWorkflowReadsWebCommands(t *testing.T) {
	t.Parallel()

	web := func(kind protocol.InboundKind, text string) *protocol.InboundMessage {
		return protocol.NewInboundMessageFromContent(protocol.SourceWeb, kind, &protocol.InboundContent{Text: text, TextAttachments: []string{"attached"}}, true)
	}

	for text, want := range map[string]protocol.WorkflowInvocation{
		"$workflow audit":                        {Name: "audit"},
		"  $Workflow audit   src/routes  extra ": {Name: "audit", Args: "src/routes  extra"},
		"$workflow\taudit\nsrc":                  {Name: "audit", Args: "src"},
		"$workflow":                              {},
		"$workflow   ":                           {},
		"$workflows audit":                       {},
		"please $workflow audit":                 {},
		"audit":                                  {},
	} {
		assert.Equal(t, want, inboundWorkflow(web(protocol.InboundKindSteer, text)), text)
	}

	for _, kind := range []protocol.InboundKind{protocol.InboundKindPrompt, protocol.InboundKindSteer, protocol.InboundKindEnqueue} {
		assert.Equal(t, protocol.WorkflowInvocation{Name: "audit", Args: "src"}, inboundWorkflow(web(kind, "$workflow audit src")), kind)
	}

	assert.Zero(t, inboundWorkflow(web(protocol.InboundKindCancel, "$workflow audit")))

	automated := web(protocol.InboundKindSteer, "$workflow audit")
	automated.Human = false
	assert.Zero(t, inboundWorkflow(automated))

	for _, source := range []protocol.Source{protocol.SourceSlack, protocol.SourceSystem, protocol.SourceExternalMCP} {
		msg := protocol.NewInboundMessageFromContent(source, protocol.InboundKindSteer, &protocol.InboundContent{Text: "$workflow audit"}, true)
		assert.Zero(t, inboundWorkflow(msg), source)
	}
}

func TestVoiceInboundRunsNoDollarCommands(t *testing.T) {
	t.Parallel()

	skill := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "$docs-helper do it"}, true)
	command := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "$workflow deploy"}, true)

	require.Equal(t, &harness.PromptInputDirectSkill{Name: "docs-helper", Arguments: "do it"}, inboundDirectSkill(skill))
	require.Equal(t, protocol.WorkflowInvocation{Name: "deploy"}, inboundWorkflow(command))

	for _, msg := range []*protocol.InboundMessage{skill, command} {
		msg.Metadata[protocol.InboundMediaMetadataKey] = "Voice"
		assert.Nil(t, inboundDirectSkill(msg), msg.Text)
		assert.Zero(t, inboundWorkflow(msg), msg.Text)
		assert.Equal(t, msg.Text, msg.Metadata[protocol.InboundRawTextMetadataKey])
	}
}

func TestBridgeQueuesWebWorkflowInsteadOfSteering(t *testing.T) {
	t.Parallel()

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), stopCh: make(chan struct{}), inputOpen: true, requestCh: make(chan bridgeRequest, 1), config: Config{SessionService: newTestSessionService(t)}}

	workflowInbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "$workflow audit src"}, true)
	require.NoError(t, bridge.enqueue(t.Context(), &bridgeRequest{inbound: workflowInbound}, "test"))
	assert.Empty(t, bridge.steers)
	require.Len(t, bridge.requestCh, 1)
	assert.Equal(t, protocol.WorkflowInvocation{Name: "audit", Args: "src"}, (<-bridge.requestCh).inbound.Workflow)

	steer := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "keep going"}, true)
	require.NoError(t, bridge.enqueue(t.Context(), &bridgeRequest{inbound: steer}, "test"))
	assert.Len(t, bridge.steers, 1)
	assert.Empty(t, bridge.requestCh)

	agentSteer := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindSteer, "also check 2025", false)
	require.NoError(t, bridge.enqueue(t.Context(), &bridgeRequest{inbound: agentSteer}, "test"))
	assert.Len(t, bridge.steers, 2, "a non-human steer joins an open turn")
	assert.Empty(t, bridge.requestCh)
}

// A steer submitted during shutdown joins the interrupted turn unless that turn is hidden or a $workflow.
func TestStoppedSteerJoinsOnlyOpenTurns(t *testing.T) {
	for name, tc := range map[string]struct {
		turn  *protocol.InboundMessage
		joins bool
	}{
		"person":    {turn: &protocol.InboundMessage{Source: protocol.SourceWeb, Human: true, Text: "start"}, joins: true},
		"system":    {turn: &protocol.InboundMessage{Source: protocol.SourceSystem, Text: "goal continuation"}, joins: true},
		"cron":      {turn: &protocol.InboundMessage{Source: protocol.SourceSystem, Text: "cron", RequireOutputDecision: true}},
		"$workflow": {turn: &protocol.InboundMessage{Source: protocol.SourceWeb, Human: true, Text: "$workflow audit", Workflow: protocol.WorkflowInvocation{Name: "audit"}}},
	} {
		t.Run(name, func(t *testing.T) {
			service := newTestSessionService(t)

			const conversationID = "web:stopped"
			require.NoError(t, startTurnDB(t.Context(), service.db, "turn-1", conversationID, tc.turn))

			bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: service}}
			steer := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindSteer, "also this", false)
			require.NoError(t, bridge.storeStopped(&bridgeRequest{inbound: steer}, "test"))

			_, journaled, err := service.LoadTurnStep(t.Context(), conversationID, "turn-1/steers")
			require.NoError(t, err)
			assert.Equal(t, tc.joins, journaled)

			queued, err := service.ThreadQueueForConversation(conversationID)
			require.NoError(t, err)

			if tc.joins {
				assert.Empty(t, queued)
			} else {
				assert.Len(t, queued, 1, "a steer that misses runs next, once")
			}
		})
	}
}

func TestProvenanceHeaderSanitizesAmbiguousTokens(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "[System]", provenanceHeader(&promptProvenance{origin: " \t\n ", media: " \n "}))
	assert.Equal(t, "[ExternalMCP principal=\"Alice [ops]=lead\" additional_instructions=\"line \\\"one\\\"\\nnext\"]", provenanceHeader(&promptProvenance{origin: "ExternalMCP", media: "Text", principal: " Alice [ops]=lead ", additionalInstructions: "line \"one\"\nnext"}))
	assert.Equal(t, `[Slack principal="a\"b\\c"]`, provenanceHeader(&promptProvenance{origin: "Slack", media: "Text", principal: "a\"b\\c"}))
	assert.Equal(t, "[Slack]", provenanceHeader(&promptProvenance{origin: "Slack", media: "Text", principal: "   "}))
	assert.Equal(t, "[External_(MCP)-x media=Voice_(note)-clip]", provenanceHeader(&promptProvenance{origin: "External [MCP]=x", media: "Voice [note]=clip"}))
	assert.Equal(t, promptProvenance{origin: "System", media: "Text"}, provenanceFromInbound(&protocol.InboundMessage{Source: protocol.SourceSystem, Metadata: map[string]string{protocol.InboundOriginMetadataKey: "Mallory", protocol.InboundMediaMetadataKey: "Dance"}}))
	voice := provenanceFromInbound(&protocol.InboundMessage{Source: protocol.SourceWeb, Human: true, Metadata: map[string]string{protocol.InboundMediaMetadataKey: "Voice", protocol.InboundPrincipalMetadataKey: "Alice"}})
	assert.Equal(t, `[Web media=Voice principal="Alice"]`, provenanceHeader(&voice))
}

// R17: the builder names an agent sender and picks the verb by how the message reaches the turn. An
// agent's write tells the receiver its answer returns on its own; a task prompt's answer is the task
// call's result, so its header does not.
func TestBuildPromptNamesAgentSender(t *testing.T) {
	msg := &protocol.InboundMessage{Source: protocol.SourceSystem, Text: "hi", Metadata: map[string]string{protocol.InboundSenderSessionMetadataKey: `web:"a"`, protocol.InboundSenderAgentMetadataKey: "planner"}}
	instructions := ` additional_instructions="Reply in plain text suitable for Slack. Avoid markdown unless it is necessary."]` + "\n\nhi"
	returns := `; your final answer returns to that session automatically, so do not write back to it"`

	assert.Equal(t, `[System sender="steer from session web:\"a\" (agent planner)`+returns+instructions, buildPrompt(msg, nil, true))
	assert.Equal(t, `[System sender="message from session web:\"a\" (agent planner)`+returns+instructions, buildPrompt(msg, nil, false))

	msg.Metadata[protocol.InboundSenderActionMetadataKey] = protocol.SenderActionStopped
	assert.Equal(t, `[System sender="session web:\"a\" (agent planner) stopped your previous turn and sent this`+returns+instructions, buildPrompt(msg, nil, false))

	task := &protocol.InboundMessage{Source: protocol.SourceSystem, Text: "hi", Metadata: map[string]string{protocol.InboundSenderSessionMetadataKey: `web:"a"`, protocol.InboundSenderAgentMetadataKey: "planner", inboundAgentMetadataKey: "researcher"}}
	assert.Equal(t, `[System sender="message from session web:\"a\" (agent planner)"`+instructions, buildPrompt(task, nil, false))
}

func TestBridgeScheduleMessageSubmitsAfterDelay(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: protocol.SlackThreadConversationID("D123", "111.222"), SessionService: newTestSessionService(t)}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
		require.NoError(t, bridge.ScheduleMessage(new(protocol.InboundMessage), 5*time.Second, "later", false))
		synctest.Wait()

		select {
		case <-bridge.requestCh:
			t.Fatal("scheduled message submitted before delay")
		default:
		}

		time.Sleep(5 * time.Second)
		synctest.Wait()

		select {
		case request := <-bridge.requestCh:
			require.NotNil(t, request.inbound)
			assert.NotEmpty(t, request.scheduledMessageID)
			assert.Equal(t, "later", request.inbound.Text)
			assert.Equal(t, bridge.config.ConversationID, request.inbound.ConversationID)
			assert.Nil(t, request.inbound.SlackReply)
		case <-time.After(time.Nanosecond):
			t.Fatal("scheduled message was not submitted")
		}

		messages, err := bridge.config.SessionService.ScheduledMessages()
		require.NoError(t, err)
		require.Len(t, messages, 1)
	})
}

func TestBridgeScheduleMessageSubmitsExternalMCPInPersistedSlackThread(t *testing.T) {
	t.Parallel()

	newBridge, service, requests, finals := newResumeTestBridges(t, false)
	cfg := newBridge().runtime
	threadKey := newBridge().config.ConversationID
	privateConversationID := "external_mcp:planner:private"

	writeAgent(t, cfg.Clone().Workspace, "selected", "---\ndescription: Selected\nmode: primary\nmodel: gpt-6.1-sol\npermission: {}\n---\nSelected canonical instructions\n")
	require.NoError(t, service.UpsertThread(threadKey, ThreadState{Agent: "selected"}))
	require.NoError(t, service.UpsertThread(privateConversationID, ThreadState{Agent: "planner"}))
	require.NoError(t, service.UpsertExternalMCPSession("public-1", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: privateConversationID, ManagedConversationID: threadKey}))
	_, err := service.AppendEntryID(t.Context(), threadKey, testSessionEntry("canonical context", "canonical history"))
	require.NoError(t, err)

	inbound := protocol.NewInboundMessage(protocol.SourceExternalMCP, protocol.InboundKindPrompt, "producer context", true)
	inbound.ConversationID, inbound.SyncDestination = privateConversationID, threadKey
	require.NoError(t, startTurnDB(t.Context(), service.db, "producer", privateConversationID, inbound))
	bridge := NewConversation(cfg, finalsPublisher{finals: finals}, &Config{ConversationID: privateConversationID, Agent: "planner", SessionService: service}, slog.New(slog.DiscardHandler))
	bridge.activeTurnID = "producer" // The producer turn is running.
	require.NoError(t, bridge.ScheduleMessage(inbound, 100*time.Millisecond, "later", false))
	_, err = service.finishTurn(t.Context(), "producer", &turnFinish{conversationID: strings.TrimSpace(privateConversationID), entries: []harness.SessionEntry{*testSessionEntry("producer context", "producer answer")}, outbound: protocol.NewOutboundMessage(privateConversationID, "")})
	require.NoError(t, err)
	require.NoError(t, service.closeTurn(t.Context(), "producer"))

	manager, _ := newTestBridgeManager(t, cfg, service, finals)
	require.NoError(t, manager.StartPendingScheduledMessages(t.Context()))
	final := readFinal(t, finals)
	assert.Equal(t, threadKey, final.ConversationID)
	assert.Equal(t, "selected", final.Agent)
	assert.Nil(t, final.SlackReply, "routing comes from the canonical Slack ID, not an inherited reply")
	assert.Nil(t, final.Cronjob)

	body := <-requests
	assert.Contains(t, body, "Selected canonical instructions")
	assert.Contains(t, body, "canonical context")
	assert.Contains(t, body, "producer context")

	var request struct {
		Input []struct{ Role, Content string }
	}

	require.NoError(t, json.Unmarshal([]byte(body), &request))
	assert.Equal(t, "user", request.Input[len(request.Input)-1].Role)
	assert.Equal(t, "[System additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\nlater", request.Input[len(request.Input)-1].Content)
	entries, err := service.ObserveEntries(t.Context(), privateConversationID)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "the canonical scheduled turn does not enter private history")
}

func TestBridgeSuccessfulWorkflowPersistsRunSummary(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		workspace := t.TempDir()
		writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-6.1-sol\npermission:\n  rocketclaw:\n    rocketclaw_set_tag: [[customer, internal]]\n---\nMain prompt\n")

		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Input []struct{ Type, Output string }
			}
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
				return
			}

			w.Header().Set("Content-Type", "application/json")

			requests++
			if requests == 1 {
				writeRawRunFunctionCall(t, w, "set-tag", "execute", struct {
					Code string `json:"code"`
				}{"def main():\n    return rocketclaw_set_tag(tag=\"customer\")\n"})

				return
			}

			var outputs []string

			for _, item := range body.Input {
				if item.Type == "function_call_output" {
					outputs = append(outputs, item.Output)
				}
			}

			assert.Equal(t, []string{`{"tags":["customer"]}`}, outputs)

			_, err := w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"checking workflow","annotations":[]}]},{"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"finished","annotations":[]}]}]}`))
			assert.NoError(t, err)
		}))
		t.Cleanup(server.Close)

		root, err := os.OpenRoot(workspace)
		require.NoError(t, err)
		require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))
		require.NoError(t, root.MkdirAll(".rocketclaw/workflows", 0o755))
		require.NoError(t, root.WriteFile(".rocketclaw/workflows/audit.star", []byte("meta = {\"name\": \"audit\", \"description\": \"Audit\", \"phases\": [\"work\", \"later\"]}\ndef main(args): return phase(\"work\", lambda: agent(\"prompt\", label=\"worker\"))\n"), 0o600))
		_, err = workflow.Load(root, ".rocketclaw")
		require.NoError(t, err)
		require.NoError(t, root.Close())

		service := newTestSessionServiceAt(t, workspace)
		pairID := protocol.SlackThreadConversationID("C123", "111.222")
		recordTestConversations(t, service, "main", map[string]ConversationKind{pairID: KindSlackThread})

		bus := newTestBus()
		t.Cleanup(bus.Close)
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), runtime: config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}), bus: bus, config: Config{ConversationID: pairID, Agent: "main", SessionService: service}}
		require.NoError(t, startTestBridge(t.Context(), bridge))
		t.Cleanup(bridge.Stop)

		delivered := make(chan struct{})
		workflowTurnID := ""
		starts := 0

		go func() {
			for outbound := range bus.Outbound(t.Context()) {
				workflowTurnID = outbound.TurnID
				if !outbound.Complete {
					starts++

					assert.Empty(t, outbound.Text)
					assert.Empty(t, outbound.ConsumedID)
					assert.Empty(t, outbound.Attachments)
					assert.Nil(t, outbound.ReasoningEffort)
					assert.Empty(t, outbound.Terminal)
				}

				if outbound.Complete {
					assert.Equal(t, "finished", outbound.Text)
					close(delivered)

					return
				}
			}
		}()

		inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "$workflow audit", true)
		inbound.Workflow = protocol.WorkflowInvocation{Name: "audit"}
		response := inbound.EnableResponseWait()
		require.NoError(t, bridge.Submit(t.Context(), inbound))
		require.NoError(t, (<-response).Err)
		<-delivered
		require.Equal(t, 1, starts, "only one content-free start may precede the final answer")
		server.Close()
		synctest.Wait()

		tags, err := sessionTags(t.Context(), service.db, pairID)
		require.NoError(t, err)
		require.Equal(t, []string{"customer"}, tags)

		entries, err := service.ObserveEntries(t.Context(), pairID)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, workflowRunEntryType, entries[0].Entry.Type)
		messages, err := replayInputMessages(entries[0].Entry.ReplayInput)
		require.NoError(t, err)
		require.Len(t, messages, 3)
		assert.Equal(t, []replayInputMessage{{role: "user", text: "$workflow audit"}, {role: "assistant", text: "finished"}}, messages[:2])
		payload := workflowSummaryPayloadFromEntry(t, &entries[0].Entry)

		var summary workflowRunSummary
		require.NoError(t, json.Unmarshal([]byte(payload), &summary))
		assert.Equal(t, workflowTurnID, summary.RunID)
		assert.JSONEq(t, fmt.Sprintf(`{"workflow":"audit","run_id":%q,"terminal":"complete","phases":[{"name":"work","status":"complete","scheduled":1,"complete":1},{"name":"later","status":"skipped","scheduled":0,"complete":0}]}`, workflowTurnID), payload)
		require.Equal(t, 2, requests, "the tag call must complete before the workflow answer")
	})
}

func TestBridgeFailedManagedWorkflowPersistsRunSummary(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		workspace := t.TempDir()
		writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-6.1-sol\n---\nMain prompt\n")
		root, err := os.OpenRoot(workspace)
		require.NoError(t, err)
		require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))
		require.NoError(t, root.MkdirAll(".rocketclaw/workflows", 0o755))
		require.NoError(t, root.WriteFile(".rocketclaw/workflows/fail.star", []byte("meta = {\"name\": \"fail\", \"description\": \"Fail\", \"phases\": [\"work\", \"later\"]}\ndef main(args): return phase(\"work\", lambda: 1 // 0)\n"), 0o600))
		_, err = workflow.Load(root, ".rocketclaw")
		require.NoError(t, err)
		require.NoError(t, root.Close())

		service := newTestSessionServiceAt(t, workspace)
		conversationID := protocol.SlackThreadConversationID("C123", "111.222")
		bus := newTestBus()
		t.Cleanup(bus.Close)
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), runtime: config.NewLockedConfig(&config.Config{Workspace: workspace}), bus: bus, config: Config{ConversationID: conversationID, Agent: "main", SessionService: service}}
		require.NoError(t, startTestBridge(t.Context(), bridge))
		t.Cleanup(bridge.Stop)

		delivered := make(chan struct{})

		go func() {
			for outbound := range bus.Outbound(t.Context()) {
				if outbound.Complete {
					close(delivered)
					return
				}
			}
		}()

		inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "$workflow fail", true)
		inbound.Workflow = protocol.WorkflowInvocation{Name: "fail"}
		response := inbound.EnableResponseWait()
		require.NoError(t, bridge.Submit(t.Context(), inbound))
		require.NoError(t, (<-response).Err)
		<-delivered
		synctest.Wait()

		entries, err := service.ObserveEntries(t.Context(), conversationID)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, workflowRunEntryType, entries[0].Entry.Type)
		summary := workflowSummaryFromEntry(t, &entries[0].Entry)
		assert.Equal(t, protocol.TerminalFailed, summary.Terminal)
		assert.Equal(t, []workflowRunPhaseSummary{{Name: "work", Status: protocol.PhaseError}, {Name: "later", Status: protocol.PhaseSkipped}}, summary.Phases)
		assert.Equal(t, `phase "work" failed`, summary.Error)
	})
}

func TestBridgeFailedWorkerErrorIsNotPersisted(t *testing.T) {
	t.Parallel()

	const privateError = "PRIVATE_WORKER_ERROR"

	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-6.1-sol\n---\nMain prompt\n")
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))
	require.NoError(t, root.MkdirAll(".rocketclaw/workflows", 0o755))
	require.NoError(t, root.WriteFile(".rocketclaw/workflows/fail-worker.star", []byte("meta = {\"name\": \"fail-worker\", \"description\": \"Fail worker\", \"phases\": [\"work\", \"later\"]}\ndef main(args): return phase(\"work\", lambda: agent(\"fail\"))\n"), 0o600))
	_, err = workflow.Load(root, ".rocketclaw")
	require.NoError(t, err)
	require.NoError(t, root.Close())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, privateError, http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	service := newTestSessionServiceAt(t, workspace)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	bus := newTestBus()
	t.Cleanup(bus.Close)
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), runtime: config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}), bus: bus, config: Config{ConversationID: conversationID, Agent: "main", SessionService: service}}
	require.NoError(t, startTestBridge(t.Context(), bridge))
	t.Cleanup(bridge.Stop)

	delivered := make(chan struct{})

	go func() {
		for outbound := range bus.Outbound(t.Context()) {
			if outbound.Complete {
				close(delivered)
				return
			}
		}
	}()

	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "$workflow fail-worker", true)
	inbound.Workflow = protocol.WorkflowInvocation{Name: "fail-worker"}
	response := inbound.EnableResponseWait()
	require.NoError(t, bridge.Submit(t.Context(), inbound))
	require.NoError(t, (<-response).Err)
	<-delivered

	entries, err := service.ObserveEntries(t.Context(), conversationID)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	encodedEntry, err := json.Marshal(entries[0].Entry)
	require.NoError(t, err)
	assert.NotContains(t, string(encodedEntry), privateError)
	summary := workflowSummaryFromEntry(t, &entries[0].Entry)
	assert.Equal(t, `phase "work" failed`, summary.Error)
}

func TestBridgeStoppedManagedWorkflowPersistsRunSummary(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-6.1-sol\n---\nMain prompt\n")
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))
	require.NoError(t, root.MkdirAll(".rocketclaw/workflows", 0o755))
	require.NoError(t, root.WriteFile(".rocketclaw/workflows/stop.star", []byte("meta = {\"name\": \"stop\", \"description\": \"Stop\", \"phases\": [\"work\", \"later\"]}\ndef main(args): return phase(\"work\", lambda: agent(\"wait\"))\n"), 0o600))
	_, err = workflow.Load(root, ".rocketclaw")
	require.NoError(t, err)
	require.NoError(t, root.Close())

	requestArrived, releaseRequest := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(requestArrived)

		select {
		case <-request.Context().Done():
		case <-releaseRequest:
		}
	}))
	t.Cleanup(server.Close)

	service := newTestSessionServiceAt(t, workspace)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	bus := newTestBus()
	t.Cleanup(bus.Close)
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), runtime: config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}), bus: bus, config: Config{ConversationID: conversationID, Agent: "main", SessionService: service}}
	require.NoError(t, startTestBridge(t.Context(), bridge))
	t.Cleanup(bridge.Stop)

	delivered := make(chan struct{})

	go func() {
		for outbound := range bus.Outbound(t.Context()) {
			if outbound.Complete {
				close(delivered)
				return
			}
		}
	}()

	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "$workflow stop", true)
	inbound.Workflow = protocol.WorkflowInvocation{Name: "stop"}
	response := inbound.EnableResponseWait()
	require.NoError(t, bridge.Submit(t.Context(), inbound))
	<-requestArrived
	assert.Same(t, inbound, bridge.InterruptActiveTurn())
	close(releaseRequest)
	require.NoError(t, (<-response).Err)
	<-delivered

	entries, err := service.ObserveEntries(t.Context(), conversationID)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	summary := workflowSummaryFromEntry(t, &entries[0].Entry)
	assert.Equal(t, protocol.TerminalStopped, summary.Terminal)
	assert.Equal(t, []workflowRunPhaseSummary{{Name: "work", Status: protocol.PhaseError, Scheduled: 1}, {Name: "later", Status: protocol.PhaseSkipped}}, summary.Phases)
	assert.Equal(t, "workflow stopped by user", summary.Error)
	transcript, err := service.ObserveTranscript(t.Context(), conversationID, 0, 0, nil)
	require.NoError(t, err)
	require.Len(t, transcript, 1)
	assert.Empty(t, transcript[0].Terminal, "saved workflow summaries retain their existing transcript metadata")
}

func TestWorkflowRunSummaryIsVisibleWithoutIntermediateOutput(t *testing.T) {
	t.Parallel()

	const intermediate = "PRIVATE_INTERMEDIATE_WORKFLOW_OUTPUT"

	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-6.1-sol\n---\nMain prompt\n")
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))
	require.NoError(t, root.MkdirAll(".rocketclaw/workflows", 0o755))
	require.NoError(t, root.WriteFile(".rocketclaw/workflows/private.star", []byte("meta = {\"name\": \"private\", \"description\": \"Private\", \"phases\": [\"work\", \"later\"]}\ndef main(args):\n    phase(\"work\", lambda: agent(\"produce intermediate\"))\n    return \"public result\"\n"), 0o600))
	_, err = workflow.Load(root, ".rocketclaw")
	require.NoError(t, err)
	require.NoError(t, root.Close())

	requests := 0

	var followUpInput string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++

		var body map[string]any
		if !assert.NoError(t, json.NewDecoder(request.Body).Decode(&body)) {
			http.Error(w, "decode request", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		if requests == 1 {
			writeRawRunMessage(t, w, "workflow-response", "workflow-message", intermediate)
			return
		}

		encoded, err := json.Marshal(body["input"])
		if !assert.NoError(t, err) {
			http.Error(w, "encode request input", http.StatusInternalServerError)
			return
		}

		followUpInput = string(encoded)

		writeRawRunMessage(t, w, "follow-up-response", "follow-up-message", "follow-up answer")
	}))
	t.Cleanup(server.Close)

	service := newTestSessionServiceAt(t, workspace)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	bus := newTestBus()
	t.Cleanup(bus.Close)
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), runtime: config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}), bus: bus, config: Config{ConversationID: conversationID, Agent: "main", SessionService: service}}
	require.NoError(t, startTestBridge(t.Context(), bridge))
	t.Cleanup(bridge.Stop)

	completed := make(chan string, 2)

	go func() {
		for outbound := range bus.Outbound(t.Context()) {
			if outbound.Complete {
				completed <- outbound.Text
			}
		}
	}()

	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "$workflow private", true)
	inbound.Workflow = protocol.WorkflowInvocation{Name: "private"}
	response := inbound.EnableResponseWait()
	require.NoError(t, bridge.Submit(t.Context(), inbound))
	require.NoError(t, (<-response).Err)
	assert.Equal(t, "public result", <-completed)

	entries, err := service.ObserveEntries(t.Context(), conversationID)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	encodedEntry, err := json.Marshal(entries[0].Entry)
	require.NoError(t, err)
	assert.NotContains(t, string(encodedEntry), intermediate)

	followUp := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "What happened?", true)
	followUpResponse := followUp.EnableResponseWait()
	require.NoError(t, bridge.Submit(t.Context(), followUp))
	require.NoError(t, (<-followUpResponse).Err)
	assert.Equal(t, "follow-up answer", <-completed)
	assert.Contains(t, followUpInput, "Workflow run summary.")
	assert.Contains(t, followUpInput, `\"workflow\":\"private\"`)
	assert.Contains(t, followUpInput, `\"name\":\"work\",\"status\":\"complete\"`)
	assert.Contains(t, followUpInput, `\"name\":\"later\",\"status\":\"skipped\"`)
	assert.NotContains(t, followUpInput, intermediate)
	assert.NotContains(t, followUpInput, "produce intermediate")
	assert.NotContains(t, followUpInput, "function_call")
	assert.NotContains(t, followUpInput, "reasoning")
	entries, err = service.ObserveEntries(t.Context(), conversationID)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "turn", entries[1].Entry.Type)
}

func TestSubmitEnqueuedItemPreservesSource(t *testing.T) {
	t.Parallel()

	bridge := &Bridge{
		log:       slog.New(slog.DiscardHandler),
		requestCh: make(chan bridgeRequest, 1),
		stopCh:    make(chan struct{}),
		config:    Config{ConversationID: protocol.SlackThreadConversationID("C123", "111.0"), SessionService: newTestSessionService(t)},
	}

	require.NoError(t, bridge.submitEnqueuedItem(t.Context(), &protocol.ThreadQueueItem{ID: "q1", Source: protocol.SourceSlack, Message: "wait 4s and say alpha", Principal: "U1", SlackChannel: "C123", SlackTS: "111.2", SlackReply: &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.2", ThreadTS: "111.0"}}))

	req := <-bridge.requestCh
	require.NotNil(t, req.inbound)
	assert.Equal(t, protocol.SourceSlack, req.inbound.Source)
	assert.Equal(t, protocol.InboundKindEnqueue, req.inbound.Kind)
	assert.Equal(t, "wait 4s and say alpha", req.inbound.Text)
	require.NotNil(t, req.inbound.SlackReply)
	assert.Equal(t, "C123", req.inbound.SlackReply.ChannelID)
	assert.Equal(t, "111.2", req.inbound.SlackReply.MessageTS)
	assert.Equal(t, "111.0", req.inbound.SlackReply.ThreadTS)
	assert.Equal(t, "U1", req.inbound.Metadata[protocol.InboundPrincipalMetadataKey])

	item := &protocol.ThreadQueueItem{ID: "q2", Kind: protocol.InboundKindSteer, Message: "keep this instruction", Principal: "U2"}
	require.NoError(t, bridge.submitEnqueuedItem(t.Context(), item))
	req = <-bridge.requestCh
	assert.Equal(t, item.Kind, req.inbound.Kind)
	assert.Equal(t, item.Message, req.inbound.Text)
	assert.Equal(t, item.Principal, req.inbound.Metadata[protocol.InboundPrincipalMetadataKey])

	for _, text := range []string{"$docs-helper \"typed args\"  Next", "$skill stop \"typed args\"  Next", ""} {
		item := &protocol.ThreadQueueItem{ID: "raw-queue", ConversationID: bridge.config.ConversationID, Source: protocol.SourceWeb, Message: text, Principal: "author", Content: protocol.InboundContent{Text: text, TextAttachments: []string{"$docs-helper attachment-only"}, AttachmentWarnings: []string{"warning"}}}
		require.NoError(t, bridge.config.SessionService.PutThreadQueueItem(item.ID, item))
		items, err := bridge.config.SessionService.ThreadQueueForConversation(bridge.config.ConversationID)
		require.NoError(t, err)
		require.Len(t, items, 1)
		require.Equal(t, item.Content, items[0].Content)
		require.NoError(t, bridge.submitEnqueuedItem(t.Context(), &items[0]))
		inbound := (<-bridge.requestCh).inbound
		require.Equal(t, text, inbound.Metadata[protocol.InboundRawTextMetadataKey])
		require.Equal(t, "author", inbound.Metadata[protocol.InboundPrincipalMetadataKey])
		require.Contains(t, inbound.Text, "$docs-helper attachment-only")
		require.Equal(t, []string{"warning"}, inbound.AttachmentWarnings)
		require.Equal(t, parseDirectSkillTrigger(text), inboundDirectSkill(inbound))
		_, claimed, err := (stateDAO{db: bridge.config.SessionService.db}).claimThreadQueueItem(t.Context(), bridge.config.ConversationID, item.ID)
		require.NoError(t, err)
		require.True(t, claimed)
	}

	waiting := protocol.NewInboundMessageFromContent(protocol.SourceExternalMCP, protocol.InboundKindPrompt, &protocol.InboundContent{Text: "source text", Attachments: []protocol.InboundAttachment{{Name: "image.png", MIMEType: "image/png", Data: []byte("image")}}}, true)
	waiting.Metadata = map[string]string{"external_conversation_id": "external-1", protocol.InboundPrincipalMetadataKey: "U3"}
	item = &protocol.ThreadQueueItem{ID: "q3", ConversationID: bridge.config.ConversationID, Message: "queue display", Principal: "U3", Inbound: waiting}
	require.NoError(t, bridge.config.SessionService.PutThreadQueueItem(item.ID, item))

	require.NoError(t, bridge.submitEnqueuedItem(t.Context(), item))

	inbound := (<-bridge.requestCh).inbound
	assert.Same(t, waiting, inbound)
	assert.Equal(t, protocol.InboundKindPrompt, inbound.Kind)
	assert.Equal(t, "source text", inbound.Text)
	assert.Equal(t, "external-1", inbound.Metadata["external_conversation_id"])
	assert.Equal(t, []byte("image"), inbound.Attachments[0].Data)
}

func TestBridgeDeletesEnqueueItemWhenTurnStarts(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	service := newTestSessionServiceAt(t, workspace)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, service.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ID: "q1", ConversationID: conversationID, Message: "changelog", Principal: "U1", StashAt: time.Now().UTC(), Position: 0}))

	bus := newTestBus()
	bus.Close()

	bridge := NewConversation(config.NewLockedConfig(&config.Config{Workspace: workspace}), bus, &Config{ConversationID: conversationID, Agent: "main", RequestRestart: &runtimeRestarterMock{restartFunc: func(string) string { return "" }}, SessionService: service}, slog.New(slog.DiscardHandler))
	bridge.requestCh = make(chan bridgeRequest, 1)

	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "changelog", false)
	inbound.ConversationID = conversationID
	inbound.AttachmentPresence = protocol.AttachmentPresenceUnsupported
	responseCh := inbound.EnableResponseWait()
	require.NoError(t, bridge.enqueue(context.Background(), &bridgeRequest{inbound: inbound, queueItemID: "q1"}, "submit enqueued message"))
	request := <-bridge.requestCh
	bridge.handle(t.Context(), &request)

	select {
	case response := <-responseCh:
		require.Error(t, response.Err)
	default:
		t.Fatal("enqueued message response was not completed")
	}

	items, err := service.ThreadQueueForConversation(conversationID)
	require.NoError(t, err)
	require.Empty(t, items)
}

func TestBridgeDeletesScheduledMessageWhenTurnStarts(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	service := newTestSessionServiceAt(t, workspace)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, service.PutScheduledMessage("schedule-1", &protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "later", DueAt: time.Now().UTC()}))

	bus := newTestBus()
	bus.Close()

	bridge := NewConversation(config.NewLockedConfig(&config.Config{Workspace: workspace}), bus, &Config{ConversationID: conversationID, Agent: "main", RequestRestart: &runtimeRestarterMock{restartFunc: func(string) string { return "" }}, SessionService: service}, slog.New(slog.DiscardHandler))
	bridge.requestCh = make(chan bridgeRequest, 1)

	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "later", false)
	inbound.ConversationID = conversationID
	inbound.AttachmentPresence = protocol.AttachmentPresenceUnsupported
	responseCh := inbound.EnableResponseWait()
	require.NoError(t, bridge.enqueue(context.Background(), &bridgeRequest{inbound: inbound, scheduledMessageID: "schedule-1"}, "submit scheduled message"))
	request := <-bridge.requestCh
	bridge.handle(t.Context(), &request)

	select {
	case response := <-responseCh:
		require.Error(t, response.Err)
	default:
		t.Fatal("scheduled message response was not completed")
	}

	messages, err := service.ScheduledMessages()
	require.NoError(t, err)
	require.Empty(t, messages)
}

func TestBridgeKeepsRecurringScheduledMessageAfterSuccessfulHandling(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		workspace := t.TempDir()
		service := newTestSessionServiceAt(t, workspace)
		conversationID := protocol.SlackThreadConversationID("C123", "111.222")
		require.NoError(t, service.PutScheduledMessage("schedule-1", &protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "later", DueAt: time.Now().UTC(), Recurring: true, Interval: time.Minute}))

		bus := newTestBus()
		defer bus.Close()

		bridge := NewConversation(config.NewLockedConfig(&config.Config{Workspace: workspace}), bus, &Config{ConversationID: conversationID, Agent: "main", RequestRestart: &runtimeRestarterMock{restartFunc: func(string) string { return "" }}, SessionService: service}, slog.New(slog.DiscardHandler))
		bridge.requestCh = make(chan bridgeRequest, 1)
		bridge.stopCh = make(chan struct{})

		go bridge.loop(t.Context())

		inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "later", false)
		inbound.ConversationID = conversationID
		inbound.AttachmentPresence = protocol.AttachmentPresenceUnsupported
		responseCh := inbound.EnableResponseWait()
		require.NoError(t, bridge.enqueue(context.Background(), &bridgeRequest{inbound: inbound, scheduledMessageID: "schedule-1", scheduledMessageRecurring: true}, "submit scheduled message"))

		outbound := readRocketCodeOutbound(t, bus)
		assert.Equal(t, unsupportedFileFallback, outbound.Text)

		select {
		case response := <-responseCh:
			require.NoError(t, response.Err)
		case <-time.After(time.Second):
			t.Fatal("scheduled message response was not completed")
		}

		require.Eventually(t, func() bool {
			messages, err := service.ScheduledMessages()
			require.NoError(t, err)

			_, ok := messages["schedule-1"]

			return ok
		}, time.Second, time.Millisecond)
		// Let the loop finish its later-work pass before the test context is canceled.
		synctest.Wait()
	})
}

func TestBridgeStopDisarmsScheduledMessage(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var logs lockedBuffer

		bridge := &Bridge{log: slog.New(slog.NewJSONHandler(&logs, nil)), config: Config{ConversationID: "slack-thread:C123:111.222", SessionService: newTestSessionService(t)}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
		require.NoError(t, bridge.ScheduleMessage(new(protocol.InboundMessage), 5*time.Second, "later", false))
		bridge.Stop()

		time.Sleep(5 * time.Second)
		synctest.Wait()

		select {
		case <-bridge.requestCh:
			t.Fatal("scheduled message submitted after bridge stop")
		default:
		}

		assert.Contains(t, logs.String(), "scheduled message enqueue failed")
	})
}

func TestBridgeResetScheduledMessagesDeletesPersistedAndCancelsArmed(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		workspace := t.TempDir()
		store, err := NewSessionService(workspace)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Stop()) })

		var logs lockedBuffer

		logger := slog.New(slog.NewJSONHandler(&logs, nil))
		conversationID := protocol.SlackThreadConversationID("C123", "111.222")
		bridge := NewConversation(config.NewLockedConfig(&config.Config{Workspace: workspace}), discardPublisher{}, &Config{ConversationID: conversationID, Agent: "main", SessionService: store}, logger)
		bridge.requestCh = make(chan bridgeRequest, 1)
		bridge.stopCh = make(chan struct{})

		require.NoError(t, bridge.ScheduleMessage(new(protocol.InboundMessage), 5*time.Second, "later", false))
		require.NoError(t, bridge.ScheduleMessage(new(protocol.InboundMessage), 5*time.Second, "every", true))
		require.NoError(t, store.PutScheduledMessage("other", &protocol.ScheduledMessageState{ConversationID: "other", Agent: "main", Message: "keep", DueAt: time.Now().UTC().Add(time.Hour)}))

		require.NoError(t, bridge.ResetScheduledMessages(new(protocol.InboundMessage)))
		synctest.Wait()
		time.Sleep(5 * time.Second)
		synctest.Wait()

		select {
		case <-bridge.requestCh:
			t.Fatal("scheduled message submitted after reset")
		default:
		}

		messages, err := store.ScheduledMessages()
		require.NoError(t, err)
		require.Len(t, messages, 1)
		assert.Equal(t, "other", messages["other"].ConversationID)
		assert.Equal(t, "keep", messages["other"].Message)
		assert.Contains(t, logs.String(), "scheduled message persisted")
		assert.Contains(t, logs.String(), "scheduled messages reset")
	})
}

func TestBridgeRecurringScheduledMessageAdvancesAndRearms(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		conversationID := protocol.SlackThreadConversationID("C123", "111.222")
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: service}, requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
		due := protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "again", DueAt: time.Now().UTC().Add(5 * time.Second), Recurring: true, Interval: time.Minute}

		require.NoError(t, service.PutScheduledMessage("repeat", &due))
		bridge.armScheduledMessage("repeat", &due)

		time.Sleep(5 * time.Second)
		synctest.Wait()

		select {
		case request := <-bridge.requestCh:
			require.NotNil(t, request.inbound)
			assert.Equal(t, "again", request.inbound.Text)
			assert.True(t, request.scheduledMessageRecurring)
		case <-time.After(time.Nanosecond):
			t.Fatal("recurring scheduled message was not submitted")
		}

		messages, err := service.ScheduledMessages()
		require.NoError(t, err)

		advanced := messages["repeat"]
		assert.True(t, advanced.Recurring)
		assert.Equal(t, time.Minute, advanced.Interval)
		assert.True(t, advanced.DueAt.After(due.DueAt))

		time.Sleep(time.Minute)
		synctest.Wait()

		select {
		case request := <-bridge.requestCh:
			require.NotNil(t, request.inbound)
			assert.Equal(t, "again", request.inbound.Text)
			assert.True(t, request.scheduledMessageRecurring)
		case <-time.After(time.Nanosecond):
			t.Fatal("recurring scheduled message was not rearmed")
		}
	})
}

func TestBridgeScheduledMessageTimerStopsOnStoreError(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		conversationID := protocol.SlackThreadConversationID("C123", "111.222")
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: service}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
		due := protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "later", DueAt: time.Now().UTC().Add(5 * time.Second)}

		require.NoError(t, service.PutScheduledMessage("broken", &due))
		require.NoError(t, service.Stop())
		bridge.armScheduledMessage("broken", &due)

		time.Sleep(5 * time.Second)
		synctest.Wait()

		select {
		case <-bridge.requestCh:
			t.Fatal("scheduled message submitted after store error")
		default:
		}
	})
}

func TestOpenAIClientLogsProviderRequestsOnError(t *testing.T) {
	t.Parallel()

	status := http.StatusTooManyRequests
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status == http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))

			return
		}

		w.Header().Set("Retry-After", "2")
		w.Header().Set("X-Request-ID", "req-rate")
		http.Error(w, `{"error":{"message":"blocked"}}`, status)
	}))
	t.Cleanup(server.Close)

	var logs bytes.Buffer

	cfg := new(config.Config)
	cfg.OpenAI.APIBaseURL = server.URL

	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	var params responses.ResponseNewParams

	logger = logger.With("conversation_id", "main", "turn_id", "turn-1", "agent", "main", "source", string(protocol.SourceSlack), "kind", string(protocol.InboundKindPrompt), "human", true, "goal_turn", true, "publish", true, "attachment_count", 2, "web_session_id", "browser-session-1")
	resolver := newModelResolver(cfg, logger)
	client, _, err := resolver.Resolve("gpt-6.1-sol")
	require.NoError(t, err)

	_, err = client.Responses.New(context.Background(), params)
	require.Error(t, err)
	assert.Contains(t, logs.String(), "provider request failed")
	assert.Contains(t, logs.String(), `"status":429`)
	assert.Contains(t, logs.String(), `"event":"provider_sdk_attempt"`)
	assert.Contains(t, logs.String(), `"outcome":"http_error"`)
	assert.Contains(t, logs.String(), `"duration_ms":`)
	assert.Contains(t, logs.String(), `"conversation_id":"main"`)
	assert.Contains(t, logs.String(), `"turn_id":"turn-1"`)
	assert.Contains(t, logs.String(), `"agent":"main"`)
	assert.Contains(t, logs.String(), `"source":"slack"`)
	assert.Contains(t, logs.String(), `"kind":"prompt"`)
	assert.Contains(t, logs.String(), `"human":true`)
	assert.Contains(t, logs.String(), `"goal_turn":true`)
	assert.Contains(t, logs.String(), `"publish":true`)
	assert.Contains(t, logs.String(), `"attachment_count":2`)
	assert.Contains(t, logs.String(), `"web_session_id":"browser-session-1"`)
	assert.Contains(t, logs.String(), `"provider_request_id":"req-rate"`)
	assert.Contains(t, logs.String(), `"retry_after":"2"`)
	logs.Reset()

	status = http.StatusOK
	client, _, err = resolver.Resolve("gpt-6.1-sol")
	require.NoError(t, err)

	_, _ = client.Responses.New(context.Background(), params)

	assert.Contains(t, logs.String(), "provider request completed")
	assert.Contains(t, logs.String(), `"status":200`)
	assert.Contains(t, logs.String(), `"outcome":"success"`)
}

func TestAskUserQuestionToolAllowsEmptyOptions(t *testing.T) {
	t.Parallel()

	tool := askUserQuestionTool(&userQuestionAskerMock{AskUserQuestionFunc: func(_ context.Context, req *protocol.AskUserQuestionRequest) (protocol.AskUserQuestionAnswer, error) {
		assert.Equal(t, "Approve?", req.Question)
		assert.Empty(t, req.Options)

		return protocol.AskUserQuestionAnswer{Custom: "approved", Source: protocol.SourceWeb}, nil
	}}, &protocol.InboundMessage{Source: protocol.SourceWeb, Human: true})

	result, err := tool.Call(t.Context(), []byte(`{"question":"Approve?","details":"","options":[],"multiple":false}`), nil)

	require.NoError(t, err)
	assert.JSONEq(t, `{"selected":null,"custom":"approved","source":"web"}`, result.Output)
}

func TestAskUserQuestionToolDescriptionRejectsCatchAllOptions(t *testing.T) {
	t.Parallel()

	tool := askUserQuestionTool(&userQuestionAskerMock{AskUserQuestionFunc: func(context.Context, *protocol.AskUserQuestionRequest) (protocol.AskUserQuestionAnswer, error) {
		return protocol.AskUserQuestionAnswer{}, nil
	}}, &protocol.InboundMessage{Source: protocol.SourceWeb, Human: true})

	assert.Contains(t, tool.Description, "only for concrete predefined choices")
	assert.Contains(t, tool.Description, "do not include catch-all choices")
}

func TestAskUserQuestionToolFiltersRedundantCustomOptions(t *testing.T) {
	t.Parallel()

	tool := askUserQuestionTool(&userQuestionAskerMock{AskUserQuestionFunc: func(_ context.Context, req *protocol.AskUserQuestionRequest) (protocol.AskUserQuestionAnswer, error) {
		require.Len(t, req.Options, 1)
		assert.Equal(t, "High priority", req.Options[0].Label)

		return protocol.AskUserQuestionAnswer{Selected: []string{"high"}, Source: protocol.SourceWeb}, nil
	}}, &protocol.InboundMessage{Source: protocol.SourceWeb, Human: true})

	result, err := tool.Call(t.Context(), []byte(`{"question":"Approve?","details":"","options":[{"label":"Custom answer","value":"custom_answer","description":"free text"},{"label":"High priority","value":"high","description":"Prioritize now"}],"multiple":false}`), nil)

	require.NoError(t, err)
	assert.JSONEq(t, `{"selected":["high"],"custom":"","source":"web"}`, result.Output)
}

func TestAskUserQuestionToolOmitsResponseChannel(t *testing.T) {
	t.Parallel()

	var got *protocol.AskUserQuestionRequest

	tool := askUserQuestionTool(&userQuestionAskerMock{AskUserQuestionFunc: func(_ context.Context, req *protocol.AskUserQuestionRequest) (protocol.AskUserQuestionAnswer, error) {
		got = req

		return protocol.AskUserQuestionAnswer{Custom: "ok", Source: protocol.SourceWeb}, nil
	}}, &protocol.InboundMessage{Source: protocol.SourceWeb, Human: true, ConversationID: "slack-thread:C1:1"})

	_, err := tool.Call(t.Context(), []byte(`{"question":"Q?","details":"","options":[],"multiple":false}`), nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "slack-thread:C1:1", got.ConversationID)
}

func TestRunTurnExpandsOnlyCronDefinitionShellCommands(t *testing.T) {
	t.Parallel()

	const prompt = "!`printf x >> expansion-count; printf sample-value`"

	for _, tc := range []struct {
		name   string
		source protocol.Source
		cron   bool
		wake   bool
		want   string
	}{
		{"cron", protocol.SourceSystem, true, false, "sample-value"},
		{"cron completion", protocol.SourceSystem, true, true, prompt},
		{"system", protocol.SourceSystem, false, false, prompt},
		{"web", protocol.SourceWeb, false, false, prompt},
		{"channel", protocol.SourceSlack, false, false, prompt},
		{"external", protocol.SourceExternalMCP, false, false, prompt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newBridge, _, requests, _ := newResumeTestBridges(t, false)
			bridge := newBridge()

			msg := protocol.NewInboundMessage(tc.source, protocol.InboundKindPrompt, prompt, false)
			if tc.cron {
				msg.Cronjob = &protocol.CronjobMessage{RelativePath: "cron/report.md", Agent: "main"}
			}

			if tc.wake {
				msg.Metadata = map[string]string{backgroundNotesMetadataKey: "finished-job"}
			}

			result, err := runTestTurn(t.Context(), bridge, msg, "turn-input")
			require.NoError(t, err)
			assert.Equal(t, "answer", result.text)

			var request struct {
				Input []json.RawMessage `json:"input"`
			}
			require.NoError(t, json.Unmarshal([]byte(<-requests), &request))
			messages, err := replayInputMessages(request.Input)
			require.NoError(t, err)
			require.NotEmpty(t, messages)
			assert.Equal(t, "user", messages[len(messages)-1].role)

			prefix := map[protocol.Source]string{protocol.SourceSystem: "System", protocol.SourceWeb: "Web", protocol.SourceSlack: "Slack", protocol.SourceExternalMCP: "ExternalMCP"}[tc.source]
			assert.Equal(t, "["+prefix+" additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\n"+tc.want, messages[len(messages)-1].text)
			assert.Equal(t, prompt, msg.Text, "the admitted input stays unchanged")

			snapshot, captured, err := bridge.turnInputs(t.Context(), "turn-input")
			require.NoError(t, err)
			require.True(t, captured)
			require.Equal(t, tc.cron && !tc.wake, snapshot.Inputs[0].ExpandShellCommands, "the consumed input retains its expansion policy")

			root, err := os.OpenRoot(bridge.runtime.Clone().Workspace)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, root.Close()) })

			if tc.cron && !tc.wake {
				_, err = runTestTurn(t.Context(), bridge, msg, "turn-input")
				require.NoError(t, err)
				count, err := root.ReadFile("expansion-count")
				require.NoError(t, err)
				assert.Equal(t, "x", string(count), "resuming reuses the journaled expansion")
			} else {
				_, err := root.Stat("expansion-count")
				assert.True(t, os.IsNotExist(err), "untrusted input must not run a shell command")
			}
		})
	}
}

func TestRunTurnExposesPrincipalEnv(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "planner", "---\ndescription: Planner\nmode: primary\nmodel: gpt-6.1-sol\npermission:\n  read: allow\n  bash: allow\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	var (
		requestBody struct {
			Input []struct {
				Content json.RawMessage `json:"content"`
				Output  any             `json:"output"`
			} `json:"input"`
		}
		requests int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBody.Input = nil
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&requestBody))
		w.Header().Set("Content-Type", "application/json")

		requests++
		if requests%2 == 1 {
			writeRawRunFunctionCall(t, w, "resp_call", "execute", executeBashScript(`printf '%s|%s|%s|%s' "${ROCKETCLAW_PRINCIPAL_NAME-unset}" "${ROCKETCLAW_PRINCIPAL_EMAIL-unset}" "${ROCKETCLAW_PRINCIPAL_ID-unset}" "${ROCKETCLAW_CONVERSATION_ID-unset}"`))

			return
		}

		writeRawRunMessage(t, w, "resp_1", "msg_1", "ok")
	}))
	t.Cleanup(server.Close)

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })

	runtime := config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}})

	require.NoError(t, service.RegisterExternalMCPConversation("public-1", "planner", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: "external_mcp:planner:private", ManagedConversationID: protocol.SlackThreadConversationID("C123", "111.222"), OriginPairs: map[string]string{"a": "first"}}))

	for _, tc := range []struct {
		name, conversationID string
		metadata             map[string]string
		want                 string
	}{
		{"name and email", "web-1", map[string]string{protocol.InboundPrincipalNameMetadataKey: "Alice Smith", protocol.InboundPrincipalEmailMetadataKey: "Alice@Example.com "}, "Alice Smith|Alice@Example.com |ff8d9819fc0e12bf0d24892e45987e249a28dce836a85cad60e28eaaa8c6d976|unset"},
		{"name only", "web-2", map[string]string{protocol.InboundPrincipalNameMetadataKey: "Bob"}, "Bob|unset|unset|unset"},
		{"no person", "web-3", nil, "unset|unset|unset|unset"},
		{"person in an External MCP thread", "external_mcp:planner:private", map[string]string{protocol.InboundPrincipalNameMetadataKey: "Alice Smith", protocol.InboundPrincipalEmailMetadataKey: "alice@example.com"}, "Alice Smith|alice@example.com|ff8d9819fc0e12bf0d24892e45987e249a28dce836a85cad60e28eaaa8c6d976|external_mcp:planner:private"},
	} {
		bridge := &Bridge{runtime: runtime, config: Config{ConversationID: tc.conversationID, Agent: "planner", SessionService: service}, bus: discardPublisher{}, log: slog.New(slog.DiscardHandler)}

		if strings.HasPrefix(tc.conversationID, "external_mcp:") {
			// The thread's metadata is saved once an External MCP turn ran.
			seed := protocol.NewInboundMessage(protocol.SourceExternalMCP, protocol.InboundKindPrompt, "hello", true)
			seed.ConversationID, seed.Metadata = tc.conversationID, map[string]string{"a": "first"}
			_, err := runTestTurn(t.Context(), bridge, seed, "seed-"+tc.conversationID)
			require.NoError(t, err)
		}

		msg := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "hello", true)
		msg.ConversationID, msg.Metadata = tc.conversationID, tc.metadata

		_, err := runTestTurn(t.Context(), bridge, msg, "turn-"+tc.conversationID)
		require.NoError(t, err, tc.name)
		require.NotEmpty(t, requestBody.Input, tc.name)
		assert.Equal(t, tc.want, requestBody.Input[len(requestBody.Input)-1].Output, tc.name)

		for i := range requestBody.Input {
			assert.NotContains(t, string(requestBody.Input[i].Content), "PRINCIPAL", "%s: the model never sees the person's keys", tc.name)

			if email := tc.metadata[protocol.InboundPrincipalEmailMetadataKey]; email != "" {
				assert.NotContains(t, strings.ToLower(string(requestBody.Input[i].Content)), strings.TrimSpace(strings.ToLower(email)), "%s: nor the email, unless a command prints it", tc.name)
			}
		}
	}
}

func TestRunTurnSendsExternalMCPMetadataAsDeveloperMessage(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "planner", "---\ndescription: Planner\nmode: primary\nmodel: gpt-6.1-sol\npermission:\n  read: allow\n  bash: auto\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	var (
		requestBody struct {
			Model string `json:"model"`
			Input []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
				Output  any             `json:"output"`
			} `json:"input"`
		}
		errRequest     error
		requests       int
		reviewMetadata [][]string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			errRequest = assert.AnError

			http.NotFound(w, r)

			return
		}

		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			errRequest = err
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		if requestBody.Model == "gpt-5.4-mini" {
			var messages []string

			for _, item := range requestBody.Input {
				if item.Role == "developer" {
					var content string
					if err := json.Unmarshal(item.Content, &content); err != nil {
						errRequest = err
					}

					messages = append(messages, content)
				}
			}

			reviewMetadata = append(reviewMetadata, messages)

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"review","object":"response","status":"completed","model":"gpt-5.4-mini","output":[{"id":"review-message","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"{\"risk_level\":\"low\",\"user_authorization\":\"high\",\"outcome\":\"allow\",\"rationale\":\"Read metadata.\"}","annotations":[]}]}]}`))

			return
		}

		requests++

		w.Header().Set("Content-Type", "application/json")

		if requests == 2 || requests == 5 {
			writeRawRunFunctionCall(t, w, "resp_2", "execute", executeBashScript(`printf '%s|%s|%s' "$ROCKETCLAW_METADATA_A" "$ROCKETCLAW_METADATA_LATER_KEY" "$ROCKETCLAW_METADATA_Z"`))

			return
		}

		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}`))
	}))
	t.Cleanup(server.Close)

	bridge := new(Bridge)
	bridge.runtime = config.NewLockedConfig(&config.Config{Workspace: workspace, AutoApproverModel: "gpt-5.4-mini", OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}})
	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })

	managedConversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, service.RegisterExternalMCPConversation("public-1", "planner", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: "external_mcp:planner:private", ManagedConversationID: managedConversationID, OriginPairs: map[string]string{"z": "last", "a": "first"}}))
	bridge.config = Config{ConversationID: "external_mcp:planner:private", Agent: "planner", SessionService: service}
	bridge.bus = discardPublisher{}
	bridge.log = slog.New(slog.DiscardHandler)

	msg := protocol.NewInboundMessage(protocol.SourceExternalMCP, protocol.InboundKindPrompt, "hello", true)
	msg.ConversationID = bridge.config.ConversationID
	msg.Metadata = map[string]string{"z": "last", "a": "first"}

	result, err := runTestTurn(context.Background(), bridge, msg, "turn-1")
	require.NoError(t, err)
	require.NoError(t, errRequest)
	assert.Equal(t, "ok", result.text)
	require.Len(t, requestBody.Input, 2)
	assert.Equal(t, "developer", requestBody.Input[0].Role)

	var metadata string
	require.NoError(t, json.Unmarshal(requestBody.Input[0].Content, &metadata))
	assert.Equal(t, "This external MCP thread has metadata:\nROCKETCLAW_CONVERSATION_ID=\"external_mcp:planner:private\"\nROCKETCLAW_METADATA_A=\"first\"\nROCKETCLAW_METADATA_Z=\"last\"", metadata)
	assert.Equal(t, "user", requestBody.Input[1].Role)

	msg.Metadata = map[string]string{"a": "ignored", "later-key": "fresh"}
	_, err = runTestTurn(context.Background(), bridge, msg, "turn-2")
	require.NoError(t, err)

	developerMessages := []string{}

	for i := range requestBody.Input {
		if requestBody.Input[i].Role == "developer" {
			var content string
			require.NoError(t, json.Unmarshal(requestBody.Input[i].Content, &content))
			developerMessages = append(developerMessages, content)
		}
	}

	assert.Contains(t, developerMessages, "This external MCP thread has metadata:\nROCKETCLAW_CONVERSATION_ID=\"external_mcp:planner:private\"\nROCKETCLAW_METADATA_A=\"first\"\nROCKETCLAW_METADATA_Z=\"last\"")
	assert.Contains(t, developerMessages, "This external MCP turn has additional metadata:\nROCKETCLAW_METADATA_LATER_KEY=\"fresh\"")
	require.Len(t, reviewMetadata, 1)
	assert.Equal(t, developerMessages, reviewMetadata[0])

	require.NotEmpty(t, requestBody.Input)
	assert.Equal(t, "first|fresh|last", requestBody.Input[len(requestBody.Input)-1].Output)

	msg.Metadata = map[string]string{"a": "ignored"}
	_, err = runTestTurn(context.Background(), bridge, msg, "turn-3")
	require.NoError(t, err)

	for i := range requestBody.Input {
		assert.NotContains(t, string(requestBody.Input[i].Content), "LATER_KEY")
	}

	entries, err := service.ObserveEntries(context.Background(), bridge.config.ConversationID)
	require.NoError(t, err)

	metadataEntries := 0

	for i := range entries {
		if entries[i].Entry.Type == externalMCPMetadataEntryType {
			metadataEntries++
		}
	}

	assert.Equal(t, 1, metadataEntries)
	assert.Len(t, entries, 4, "only the metadata prompt and three turns belong in history")

	binding, found, err := service.ExternalMCPSession("public-1")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, map[string]string{"a": "first", "z": "last"}, binding.OriginPairs)

	for i := range entries {
		messages, err := replayInputMessages(entries[i].Entry.ReplayInput)
		require.NoError(t, err)

		for _, message := range messages {
			assert.NotContains(t, message.text, "ROCKETCLAW_METADATA_LATER_KEY")
		}
	}

	// Web-created bridges know only the actual conversation ID, not pairing fields.
	writeAgent(t, workspace, "planner", "---\ndescription: Planner\nmode: primary\nmodel: gpt-6.1-sol\npermission:\n  edit:\n    '.tmp/${ROCKETCLAW_METADATA_A}/note.txt': allow\n  read: allow\n  bash: auto\n---\nPrompt\n")

	managedBridge := &Bridge{runtime: bridge.runtime, config: Config{ConversationID: managedConversationID, Agent: "planner", SessionService: service}, bus: discardPublisher{}, log: slog.New(slog.DiscardHandler)}
	// The run reports to its Slack thread, which shows its entries without a copy.
	_, err = service.db.ExecContext(t.Context(), `UPDATE conversations SET reports_to = $1 WHERE conversation_id = $2`, managedConversationID, bridge.config.ConversationID)
	require.NoError(t, err)

	managedEntries, err := service.ObserveEntries(context.Background(), managedConversationID)
	require.NoError(t, err)
	require.Len(t, managedEntries, 4)
	assert.Equal(t, externalMCPMetadataEntryType, managedEntries[0].Entry.Type)

	managedMsg := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindPrompt, "check metadata", true)
	managedMsg.ConversationID = managedConversationID
	_, err = runTestTurn(context.Background(), managedBridge, managedMsg, "turn-managed")
	require.NoError(t, err)
	require.NotEmpty(t, requestBody.Input)
	assert.Equal(t, "first||last", requestBody.Input[len(requestBody.Input)-1].Output)
	require.Len(t, reviewMetadata, 2)
	assert.Equal(t, []string{developerMessages[0]}, reviewMetadata[1])
}

func TestRunTurnTranslatesDollarToDirectSkill(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-6.1-sol\npermission:\n  skill:\n    docs-helper: allow\n---\nPrompt\n")
	writeAgent(t, workspace, "denied", "---\ndescription: Denied\nmode: primary\nmodel: gpt-6.1-sol\npermission:\n  skill:\n    docs-helper: deny\n---\nPrompt\n")
	require.NoError(t, root.MkdirAll(".rocketclaw/skills/docs-helper", 0o755))
	require.NoError(t, root.WriteFile(".rocketclaw/skills/docs-helper/SKILL.md", []byte(`---
name: docs-helper
description: Write docs
---

Use this skill for docs.
Request: $ARGUMENTS
`+"State: !`cat state; echo run >> calls`"), 0o644))

	var (
		requestBody struct {
			Input []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"input"`
		}
		errRequest error
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			errRequest = assert.AnError

			http.NotFound(w, r)

			return
		}

		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			errRequest = err
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}`))
	}))
	t.Cleanup(server.Close)

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })

	for _, source := range []protocol.Source{protocol.SourceWeb} {
		for _, kind := range []protocol.InboundKind{protocol.InboundKindPrompt, protocol.InboundKindSteer, protocol.InboundKindEnqueue} {
			for _, invocation := range []string{"$docs-helper write API docs", "$skill docs-helper write API docs"} {
				conversationID := fmt.Sprintf("dollar-%s-%s-%s", source, kind, invocation)
				bridge := &Bridge{runtime: config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}), config: Config{ConversationID: conversationID, Agent: "main", SessionService: service}, bus: discardPublisher{}, log: slog.New(slog.DiscardHandler)}
				msg := protocol.NewInboundMessageFromContent(source, kind, &protocol.InboundContent{Text: invocation, TextAttachments: []string{"attachment-only argument"}}, true)
				msg.ConversationID = conversationID
				msg.Metadata[protocol.InboundPrincipalMetadataKey] = "Alice"

				require.NoError(t, root.WriteFile("state", []byte("old"), 0o600))
				require.NoError(t, root.WriteFile("calls", nil, 0o600))

				promote := kind == protocol.InboundKindEnqueue && strings.HasPrefix(invocation, "$skill ")
				if kind == protocol.InboundKindEnqueue {
					item := protocol.ThreadQueueItem{ID: conversationID + "-queued", ConversationID: conversationID, Source: source, Message: invocation, Principal: "Alice", Content: protocol.InboundContent{Text: invocation, TextAttachments: []string{"attachment-only argument"}}}
					require.NoError(t, service.UpsertThread(conversationID, ThreadState{Agent: "main"}))
					manager := &threadBridgeManager{log: slog.New(slog.DiscardHandler), store: service, bridges: map[string]directBridge{conversationID: bridge}}
					require.NoError(t, service.PutThreadQueueItem("removed", &item))
					removed, err := manager.deleteQueueItem(t.Context(), conversationID, "removed")
					require.NoError(t, err)
					require.True(t, removed)
					require.NoError(t, service.PutThreadQueueItem(item.ID, &item))

					if promote {
						bridge.inputOpen = true
						promoted, err := manager.promoteQueueItem(t.Context(), conversationID, item.ID)
						require.NoError(t, err)
						require.True(t, promoted)

						msg = protocol.NewInboundMessage(source, protocol.InboundKindPrompt, "start", true)
					} else {
						items, err := service.ThreadQueueForConversation(conversationID)
						require.NoError(t, err)
						require.Len(t, items, 1)

						bridge.requestCh = make(chan bridgeRequest, 1)
						require.NoError(t, bridge.submitEnqueuedItem(t.Context(), &items[0]))
						msg = (<-bridge.requestCh).inbound
					}

					calls, err := root.ReadFile("calls")
					require.NoError(t, err)
					require.Empty(t, calls, "enqueue, removal and promotion must not run skill shell blocks")
				}

				require.NoError(t, root.WriteFile("state", []byte("fresh"), 0o600))

				result, err := runTestTurn(context.Background(), bridge, msg, "turn-1")

				require.NoError(t, err)
				require.NoError(t, errRequest)
				assert.Contains(t, result.text, "ok")

				calls, err := root.ReadFile("calls")
				require.NoError(t, err)
				require.Equal(t, "run\n", string(calls))

				if promote {
					items, err := service.ThreadQueueForConversation(conversationID)
					require.NoError(t, err)
					require.Empty(t, items, "promoted skill must not remain as later work")
					require.Len(t, requestBody.Input, 4)
					requestBody.Input = requestBody.Input[2:]
				}

				require.Len(t, requestBody.Input, 2)

				var skillContent, requestContent string
				require.NoError(t, json.Unmarshal(requestBody.Input[0].Content, &skillContent))
				require.NoError(t, json.Unmarshal(requestBody.Input[1].Content, &requestContent))
				assert.Equal(t, "developer", requestBody.Input[0].Role)
				assert.Contains(t, skillContent, "Use this skill for docs.")
				assert.Contains(t, skillContent, "Request: write API docs")
				assert.Contains(t, skillContent, "State: fresh")
				assert.NotContains(t, skillContent, "attachment-only argument")
				assert.Equal(t, "user", requestBody.Input[1].Role)
				assert.Contains(t, requestContent, "principal=\"Alice\"")
				assert.Contains(t, requestContent, "attachment-only argument")
				assert.Contains(t, requestContent, invocation)

				if kind == protocol.InboundKindEnqueue {
					item := protocol.ThreadQueueItem{ID: conversationID + "-denied", ConversationID: conversationID, Source: source, Message: invocation}
					require.NoError(t, service.PutThreadQueueItem(item.ID, &item))

					bridge.requestCh = make(chan bridgeRequest, 1)
					require.NoError(t, bridge.submitEnqueuedItem(t.Context(), &item))
					bridge.config.Agent = "denied"
					requestBody.Input = nil

					require.NoError(t, root.WriteFile("calls", nil, 0o600))
					result, err := runTestTurn(t.Context(), bridge, (<-bridge.requestCh).inbound, "denied-turn")
					require.NoError(t, err)
					require.Contains(t, result.text, "not available to the active agent")
					require.Empty(t, requestBody.Input, "agent changed after enqueue must be checked before the provider")

					calls, err := root.ReadFile("calls")
					require.NoError(t, err)
					require.Empty(t, calls)
				}
			}
		}
	}

	// A Slack mention's $<skill> reaches the agent as ordinary text.
	mention := &Bridge{runtime: config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}), config: Config{ConversationID: "dollar-slack", Agent: "main", SessionService: service}, bus: discardPublisher{}, log: slog.New(slog.DiscardHandler)}
	mentionMsg := protocol.NewInboundMessageFromContent(protocol.SourceSlack, protocol.InboundKindPrompt, &protocol.InboundContent{Text: "$docs-helper write API docs"}, true)
	mentionMsg.ConversationID = mention.config.ConversationID
	requestBody.Input = nil

	require.NoError(t, root.WriteFile("calls", nil, 0o600))
	_, err = runTestTurn(t.Context(), mention, mentionMsg, "mention-turn")
	require.NoError(t, err)
	mentionCalls, err := root.ReadFile("calls")
	require.NoError(t, err)
	require.Empty(t, mentionCalls, "a Slack mention runs no skill")
	require.Len(t, requestBody.Input, 1)
	assert.Equal(t, "user", requestBody.Input[0].Role)
	assert.Contains(t, string(requestBody.Input[0].Content), "$docs-helper write API docs")

	bus := newTestBus()
	t.Cleanup(bus.Close)
	bridge := &Bridge{runtime: config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}), config: Config{ConversationID: "missing-skill", Agent: "main", SessionService: service}, bus: bus, log: slog.New(slog.DiscardHandler)}
	msg := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindSteer, "$skill missing-skill", true)
	msg.ConversationID = bridge.config.ConversationID
	msg.Metadata = map[string]string{"web_message_id": "original-input"}
	requestBody.Input = nil
	result, err := runTestTurn(t.Context(), bridge, msg, "missing-turn")
	require.NoError(t, err)
	require.Contains(t, result.text, `subject "missing-skill"`)
	require.Empty(t, requestBody.Input)
	saved, err := service.ObserveEntries(t.Context(), msg.ConversationID)
	require.NoError(t, err)
	require.Empty(t, saved, "failed preparation must not append a replay turn")

	var delivery errgroup.Group
	delivery.Go(func() error { return publishTestFinal(t.Context(), bridge, msg, &result) })
	require.Equal(t, "original-input", readRocketCodeOutbound(t, bus).ConsumedID)
	require.False(t, readRocketCodeOutbound(t, bus).Complete)
	final := readRocketCodeOutbound(t, bus)
	require.True(t, final.Complete)
	require.Equal(t, result.text, final.Text)
	require.NoError(t, delivery.Wait())

	msg.Text = "next request"
	_, err = runTestTurn(t.Context(), bridge, msg, "next-turn")
	require.NoError(t, err)
	require.Len(t, requestBody.Input, 1, "next provider input excludes failed preparation")
	require.NotContains(t, string(requestBody.Input[0].Content), "missing-skill")
}

func TestRunTurnProjectsDifferentProviderHistoryBeforeRequest(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: work/gpt\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })

	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	_, err = service.AppendEntryID(t.Context(), conversationID, &harness.SessionEntry{Version: 1, Type: "turn", Model: "openai/gpt", ResponseID: "provider-private-sentinel", ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"portable-readable","id":"provider-private-sentinel"}`)}, OutputTrace: []json.RawMessage{json.RawMessage(`{"private":"provider-private-sentinel"}`)}})
	require.NoError(t, err)

	var requestBody string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		requestBody = string(body)

		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "response", "message", "ok")
	}))
	t.Cleanup(server.Close)

	bridge := &Bridge{runtime: config.NewLockedConfig(&config.Config{Workspace: workspace, Providers: map[string]config.OpenAIConfig{"work": {APIBaseURL: server.URL}}}), config: Config{ConversationID: conversationID, Agent: "main", SessionService: service}, bus: discardPublisher{}, log: slog.New(slog.DiscardHandler)}
	msg := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "hello", true)
	msg.ConversationID = conversationID
	_, err = runTestTurn(t.Context(), bridge, msg, "turn-1")
	require.NoError(t, err)
	assert.Contains(t, requestBody, "portable-readable")
	assert.NotContains(t, requestBody, "provider-private-sentinel")

	entries, err := service.ObserveEntries(t.Context(), conversationID)
	require.NoError(t, err)
	assert.Contains(t, string(entries[0].Entry.ReplayInput[0]), "provider-private-sentinel")
}

func TestHandleInboundJournalsTurnAndClearsRowWithHistoryAppend(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-6.1-sol\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })

	conversationID := protocol.SlackThreadConversationID("C123", "111.222")

	held := make(chan struct{})

	release := sync.OnceFunc(func() { close(held) })
	defer release()

	nextChange, stopChanges := iter.Pull2(service.Changes(t.Context(), conversationID))
	defer stopChanges()

	_, err, ok := nextChange()
	require.True(t, ok)
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if !assert.NoError(t, err) {
			return
		}
		defer func() { _ = conn.Close() }()

		_, _, err = conn.ReadMessage()
		if !assert.NoError(t, err) {
			return
		}

		active, err := service.HasActiveTurn(r.Context(), conversationID)
		assert.NoError(t, err)
		assert.True(t, active, "the row exists before the provider is called")
		assert.NotEmpty(t, testTurnStepKeys(t, service, conversationID), "the turn is journaled before the provider is called")

		for _, event := range []string{
			`{"type":"response.created","response":{"id":"resp_1"}}`,
			`{"type":"response.output_text.delta","item_id":"msg_1","content_index":0,"delta":"early-public"}`,
		} {
			if !assert.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(event))) {
				return
			}
		}

		<-held
		assert.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}}`)))
	}))
	t.Cleanup(server.Close)

	bridge := &Bridge{runtime: config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: strings.Replace(server.URL, "http://", "ws://", 1)}}), config: Config{ConversationID: conversationID, Agent: "main", SessionService: service}, bus: discardPublisher{}, log: slog.New(slog.DiscardHandler)}
	msg := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "hello", true)
	msg.ConversationID = conversationID
	msg.Metadata = map[string]string{protocol.InboundPrincipalMetadataKey: "Alice"}

	var running errgroup.Group
	running.Go(func() error {
		return handleTestInbound(t.Context(), bridge, &bridgeRequest{inbound: msg})
	})

	defer func() {
		release()
		require.NoError(t, running.Wait())
	}()

	for {
		_, err, ok := nextChange()
		require.True(t, ok)
		require.NoError(t, err)
		entries, err := service.ObserveTranscript(t.Context(), conversationID, 0, 0, nil)
		require.NoError(t, err)

		if len(entries) == 1 && len(harness.PublicProgressFromTrace(entries[0].Entry.OutputTrace)) > 0 {
			progress := harness.PublicProgressFromTrace(entries[0].Entry.OutputTrace)
			require.Equal(t, "early-public", progress[0].Text)
			require.Equal(t, harness.PublicProgressWorking, progress[0].State)
			require.True(t, entries[0].Active)
			reopened, err := service.ObserveTranscript(t.Context(), conversationID, 0, 0, nil)
			require.NoError(t, err)
			require.Equal(t, entries, reopened, "reopen observes the committed text without another signal")

			break
		}
	}

	release()
	require.NoError(t, running.Wait())
	assert.Equal(t, "ok", (<-msg.EnableResponseWait()).Text)

	entries, err := service.ObserveEntries(context.Background(), conversationID)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	progress := harness.PublicProgressFromTrace(entries[0].Entry.OutputTrace)
	require.Len(t, progress, 1)
	require.Equal(t, "ok", progress[0].Text)
	require.Equal(t, harness.PublicProgressCompleted, progress[0].State)

	active, err := service.HasActiveTurn(context.Background(), conversationID)
	require.NoError(t, err)
	assert.False(t, active, "a delivered turn leaves no row")
	assert.Empty(t, testTurnStepKeys(t, service, conversationID), "the finish transaction clears the journal")
}

func TestRunTurnUsesSelectedAgentAdditionalInstructions(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: work/model-a\nreasoningEffort: high\npermission: {}\n---\nPrompt\n")
	writeAgent(t, workspace, "reviewer", "---\ndescription: Reviewer\nmode: primary\nmodel: work/model-b\nreasoningEffort: low\nadditionalInstructions: Reply in one sentence.\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	entered, release := make(chan struct{}), make(chan struct{})
	requests := 0

	var (
		requestBody struct {
			Model     string `json:"model"`
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
			Input []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"input"`
		}
		errRequest error
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			errRequest = assert.AnError

			http.NotFound(w, r)

			return
		}

		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			errRequest = err
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		requests++
		if requests == 1 {
			close(entered)
			<-release
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}`))
	}))
	t.Cleanup(server.Close)

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })

	conversationID := protocol.SlackThreadConversationID("C123", "111.222")

	bus := newTestBus()
	t.Cleanup(bus.Close)

	var logs lockedBuffer

	bridge := &Bridge{runtime: config.NewLockedConfig(&config.Config{Workspace: workspace, Providers: map[string]config.OpenAIConfig{"work": {APIBaseURL: server.URL}}}), config: Config{ConversationID: conversationID, Agent: "main", SessionService: service}, bus: bus, log: slog.New(slog.NewJSONHandler(&logs, nil)), requestCh: make(chan bridgeRequest, 1), inputOpen: true}
	queued := protocol.ThreadQueueItem{ID: "queued-input", ConversationID: conversationID, Source: protocol.SourceWeb, Kind: protocol.InboundKindEnqueue, Message: "hello", Principal: "Alice"}
	require.NoError(t, service.PutThreadQueueItem(queued.ID, &queued))
	require.NoError(t, bridge.submitEnqueuedItem(t.Context(), &queued))
	require.Empty(t, bus.outbound)
	bridge.SwitchAgent("reviewer")
	request := <-bridge.requestCh
	admitted, err := bridge.activateInbound(t.Context(), &request)
	require.NoError(t, err)
	require.True(t, admitted)
	require.Empty(t, bus.outbound)

	request.inbound.SyncDestination = "destination"

	finished := make(chan struct{})

	var group errgroup.Group
	group.Go(func() error {
		defer close(finished)

		result, err := runTestTurn(t.Context(), bridge, request.inbound, "turn-1")
		if err != nil {
			return err
		}

		return publishTestFinal(t.Context(), bridge, request.inbound, &result)
	})
	<-entered

	initial := readRocketCodeOutbound(t, bus)
	require.Equal(t, queued.ID, initial.ConsumedID)
	require.Equal(t, "work/model-b", initial.Model)
	require.Equal(t, `[Web principal="Alice" additional_instructions="Reply in one sentence."]`, initial.ConsumedHeader)
	start := readRocketCodeOutbound(t, bus)
	require.False(t, start.Complete)
	require.Equal(t, "turn-1", start.TurnID)
	require.Equal(t, conversationID, start.ConversationID)
	require.Equal(t, conversationID, start.SourceConversationID)
	require.Empty(t, start.ConsumedID)
	require.Empty(t, start.Text)
	require.Empty(t, start.Attachments)
	require.Nil(t, start.ReasoningEffort)
	bridge.SwitchAgent("main")

	steer := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &protocol.InboundContent{Text: "also explain"}, true)
	steer.Metadata["web_message_id"] = "active-steer"
	steer.Metadata[protocol.InboundPrincipalMetadataKey] = "Bob"
	require.NoError(t, bridge.Submit(t.Context(), steer))
	close(release)

	var consumedSteer, assistant, final bool
	for !final {
		var message *protocol.OutboundMessage
		select {
		case message = <-bus.outbound:
		case <-finished:
			require.NoError(t, group.Wait())
			t.Fatal("turn finished without its final outbound message")
		}

		require.Equal(t, "reviewer", message.Agent)
		require.Equal(t, "work/model-b", message.Model)
		require.Equal(t, new("low"), message.ReasoningEffort)
		require.Equal(t, conversationID, message.SourceConversationID)
		require.Equal(t, conversationID, message.ConversationID)
		require.NotEqual(t, queued.ID, message.ConsumedID, "activated input must not be published again during the turn")

		consumedSteer = consumedSteer || message.ConsumedID == "active-steer"
		if message.ConsumedID == "active-steer" {
			require.Equal(t, `[Web principal="Bob" additional_instructions="Reply in plain text suitable for Slack. Avoid markdown unless it is necessary."]`, message.ConsumedHeader)
		} else {
			require.True(t, message.Complete, "only one blank start may precede the final answer")
		}

		if message.Text != "" {
			require.True(t, message.Complete, "delivery must contain only the final answer")

			assistant = true
		}

		final = message.Complete
	}

	require.True(t, consumedSteer)
	require.True(t, assistant)
	require.NoError(t, group.Wait())

	for _, event := range []string{"queue_admitted", "queue_started", "steer_admitted", "steer_injected", "first_response_item", "first_assistant_text", "generation_finished", "delivery_acknowledged"} {
		require.Contains(t, logs.String(), `"event":"`+event+`"`)
	}

	require.NotContains(t, logs.String(), "also explain")
	require.Less(t, strings.Index(logs.String(), `"event":"first_response_item"`), strings.Index(logs.String(), `"event":"first_assistant_text"`))
	require.Less(t, strings.Index(logs.String(), `"event":"generation_finished"`), strings.Index(logs.String(), `"event":"delivery_acknowledged"`))
	relay := readRocketCodeOutbound(t, bus)
	require.Equal(t, "destination", relay.ConversationID, "the run's chat gets its final reply")
	require.Equal(t, "reviewer", relay.Agent)
	require.Equal(t, "work/model-b", relay.Model)
	require.Equal(t, new("low"), relay.ReasoningEffort)
	entries, err := service.ObserveEntries(t.Context(), conversationID)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "reviewer", entries[0].Entry.Agent)
	require.Equal(t, "work/model-b", entries[0].Entry.Model)
	require.Equal(t, new("low"), entries[0].Entry.ReasoningEffort)
	require.NoError(t, errRequest)
	require.Equal(t, "model-b", requestBody.Model)
	require.Equal(t, "low", requestBody.Reasoning.Effort)

	userContent := ""

	for i := range requestBody.Input {
		if requestBody.Input[i].Role == "user" && strings.HasSuffix(requestBody.Input[i].Content, "\n\nhello") {
			userContent = requestBody.Input[i].Content
		}
	}

	assert.Equal(t, "[Web principal=\"Alice\" additional_instructions=\"Reply in one sentence.\"]\n\nhello", userContent)

	items, err := harness.ReplayInputToParams(entries[0].Entry.ReplayInput)
	require.NoError(t, err)
	require.Equal(t, initial.ConsumedHeader, items[0].OfMessage.ExtraFields()["prompt_header"])
	require.Equal(t, responses.EasyInputMessageRoleUser, items[2].OfMessage.Role)
	require.Equal(t, `[Web principal="Bob" additional_instructions="Reply in plain text suitable for Slack. Avoid markdown unless it is necessary."]`, items[2].OfMessage.ExtraFields()["prompt_header"])
}

func TestRunTurnInjectsActiveGoalNoteAsDeveloperMessage(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-6.1-sol\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	var (
		requestBody struct {
			Input []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"input"`
		}
		errRequest error
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			errRequest = assert.AnError

			http.NotFound(w, r)

			return
		}

		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			errRequest = err
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}`))
	}))
	t.Cleanup(server.Close)

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })
	recordTestConversations(t, service, "main", map[string]ConversationKind{"thread-1": KindWebChat})
	require.NoError(t, service.BeginGoal("thread-1", &GoalState{Objective: "ship it", MaxTurns: 5}))
	_, err = service.UpdateGoalStatus("thread-1", GoalStatusProgress, "patched parser; checking connectors")
	require.NoError(t, err)

	bridge := &Bridge{runtime: config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}), config: Config{ConversationID: "thread-1", Agent: "main", SessionService: service}, bus: discardPublisher{}, log: slog.New(slog.DiscardHandler)}
	msg := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "continue", false)
	msg.GoalAction = protocol.GoalActionContinue
	msg.ConversationID = "thread-1"

	_, err = runTestTurn(context.Background(), bridge, msg, "turn-1")
	require.NoError(t, err)
	require.NoError(t, errRequest)
	require.NotEmpty(t, requestBody.Input)
	assert.Equal(t, "developer", requestBody.Input[0].Role)
	assert.Equal(t, "RocketClaw goal state:\nStatus: progress\nLast reported note:\npatched parser; checking connectors", requestBody.Input[0].Content)

	for i := range requestBody.Input {
		if requestBody.Input[i].Role == "assistant" {
			assert.NotContains(t, requestBody.Input[i].Content, "patched parser; checking connectors")
		}
	}
}

func TestRunTurnSkipsActiveGoalDeveloperMessageWithoutNote(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-6.1-sol\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	var (
		requestBody struct {
			Input []struct {
				Role string `json:"role"`
			} `json:"input"`
		}
		errRequest error
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			errRequest = assert.AnError

			http.NotFound(w, r)

			return
		}

		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			errRequest = err
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}`))
	}))
	t.Cleanup(server.Close)

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })
	recordTestConversations(t, service, "main", map[string]ConversationKind{"thread-1": KindWebChat})
	require.NoError(t, service.BeginGoal("thread-1", &GoalState{Objective: "ship it", MaxTurns: 5}))

	bridge := &Bridge{runtime: config.NewLockedConfig(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}), config: Config{ConversationID: "thread-1", Agent: "main", SessionService: service}, bus: discardPublisher{}, log: slog.New(slog.DiscardHandler)}
	msg := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "continue", false)
	msg.GoalAction = protocol.GoalActionContinue
	msg.ConversationID = "thread-1"

	_, err = runTestTurn(context.Background(), bridge, msg, "turn-1")
	require.NoError(t, err)
	require.NoError(t, errRequest)
	require.NotEmpty(t, requestBody.Input)
	assert.Equal(t, "user", requestBody.Input[0].Role)
}

func TestExternalMCPMetadataDeveloperMessageSorted(t *testing.T) {
	t.Parallel()

	env := externalMCPMetadataEnv("slack-thread:C123:111.222", map[string]string{"ticket-id": "123", "owner": "alice"})
	assert.Equal(t, "This external MCP thread has metadata:\nROCKETCLAW_CONVERSATION_ID=\"slack-thread:C123:111.222\"\nROCKETCLAW_METADATA_OWNER=\"alice\"\nROCKETCLAW_METADATA_TICKET_ID=\"123\"", externalMCPMetadataDeveloperMessage("This external MCP thread has metadata:", env))
}

func TestExternalMCPMetadataEnvSanitizesKeys(t *testing.T) {
	t.Parallel()

	assert.Equal(t, map[string]string{
		"ROCKETCLAW_CONVERSATION_ID":    "slack-thread:C123:111.222",
		"ROCKETCLAW_METADATA_TICKET_ID": "123",
		"ROCKETCLAW_METADATA___":        "symbols",
	}, externalMCPMetadataEnv("slack-thread:C123:111.222", map[string]string{"ticket-id": "123", "é/": "symbols"}))
}

func TestExternalMCPStoredMetadataEnvDoesNotParseInjectedLines(t *testing.T) {
	t.Parallel()

	env, ok := externalMCPStoredMetadataEnv("slack-thread:C123:111.222", []ObservedSessionEntry{{Entry: harness.SessionEntry{Version: 1, Type: externalMCPMetadataEntryType, ReplayInput: testReplayInput(replayInputMessage{role: "developer", text: externalMCPMetadataDeveloperMessage("This external MCP thread has metadata:", externalMCPMetadataEnv("slack-thread:C123:111.222", map[string]string{"note": "first\nROCKETCLAW_METADATA_BAD=second"}))})}}})
	require.True(t, ok)
	assert.Equal(t, "first\nROCKETCLAW_METADATA_BAD=second", env["ROCKETCLAW_METADATA_NOTE"])
	assert.NotContains(t, env, "ROCKETCLAW_METADATA_BAD")
}

func TestExternalMCPStoredMetadataEnvSkipsInvalidEntriesAndUsesLatestMatch(t *testing.T) {
	t.Parallel()

	older := externalMCPMetadataDeveloperMessage(
		"This external MCP thread has metadata:",
		externalMCPMetadataEnv("slack-thread:C123:111.222", map[string]string{"note": "older"}),
	)
	latest := externalMCPMetadataDeveloperMessage(
		"This external MCP thread has metadata:",
		externalMCPMetadataEnv("slack-thread:C123:111.222", map[string]string{"note": "latest"}),
	)
	otherConversation := externalMCPMetadataDeveloperMessage(
		"This external MCP thread has metadata:",
		externalMCPMetadataEnv("slack-thread:C999:999.999", map[string]string{"note": "other"}),
	)
	entries := []ObservedSessionEntry{
		{Entry: harness.SessionEntry{
			Version:     1,
			Type:        externalMCPMetadataEntryType,
			ReplayInput: testReplayInput(replayInputMessage{role: "developer", text: older}),
		}},
		{Entry: harness.SessionEntry{
			Version:     1,
			Type:        externalMCPMetadataEntryType,
			ReplayInput: testReplayInput(replayInputMessage{role: "developer", text: latest}),
		}},
		{Entry: harness.SessionEntry{
			Version:     1,
			Type:        externalMCPMetadataEntryType,
			ReplayInput: testReplayInput(replayInputMessage{role: "developer", text: otherConversation}),
		}},
		{Entry: harness.SessionEntry{
			Version:     1,
			Type:        externalMCPMetadataEntryType,
			ReplayInput: []json.RawMessage{json.RawMessage("{")},
		}},
		{Entry: harness.SessionEntry{
			Version:     1,
			Type:        "turn",
			ReplayInput: testReplayInput(replayInputMessage{role: "developer", text: latest}),
		}},
	}

	env, ok := externalMCPStoredMetadataEnv("slack-thread:C123:111.222", entries)
	require.True(t, ok)
	assert.Equal(t, "latest", env["ROCKETCLAW_METADATA_NOTE"])

	_, ok = externalMCPStoredMetadataEnv("slack-thread:C000:000.000", entries)
	assert.False(t, ok)
}

func TestNewOutboundMessageMarksGoalTurns(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	bridge := new(Bridge)
	bridge.config = Config{ConversationID: "thread-1", Agent: "main", RequestRestart: &runtimeRestarterMock{restartFunc: func(string) string { return "" }}, SessionService: store}

	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "hello", true)
	inbound.ConversationID = "thread-1"
	inbound.SlackReply = &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.2", ThreadTS: "111.1"}
	assert.False(t, bridge.newOutboundMessage(inbound, "turn-1", "reply", false).GoalTurn)

	recordTestConversations(t, store, "main", map[string]ConversationKind{"thread-1": KindWebChat, "thread-2": KindWebChat})
	require.NoError(t, store.BeginGoal("thread-1", &GoalState{Objective: "ship it", MaxTurns: 3}))

	outbound := bridge.newOutboundMessage(inbound, "turn-2", "reply", false)
	assert.True(t, outbound.GoalTurn)
	assert.True(t, outbound.GoalActive)
	assert.Equal(t, "main", outbound.Agent)
	assert.Equal(t, inbound.SlackReply, outbound.SlackReply)
	assert.Equal(t, 1, outbound.GoalTurnNumber)
	assert.Equal(t, 3, outbound.GoalMaxTurns)

	inbound.GoalAction = protocol.GoalActionContinue

	seedActiveTurn(t, store, "thread-1", "turn-2", nil)
	_, err := store.finishTurn(t.Context(), "turn-2", &turnFinish{conversationID: strings.TrimSpace("thread-1"), accountGoal: true, outbound: outbound})
	require.NoError(t, err)

	outbound = bridge.newOutboundMessage(inbound, "turn-3", "reply", false)
	assert.True(t, outbound.GoalTurn)
	assert.True(t, outbound.GoalActive)
	assert.Equal(t, 2, outbound.GoalTurnNumber)
	assert.Equal(t, 3, outbound.GoalMaxTurns)

	inbound.GoalAction = protocol.GoalActionNone
	outbound = bridge.newOutboundMessage(inbound, "turn-4", "reply", false)
	assert.True(t, outbound.GoalTurn)
	assert.True(t, outbound.GoalActive)
	assert.Equal(t, 2, outbound.GoalTurnNumber)
	assert.Equal(t, 3, outbound.GoalMaxTurns)

	inbound.GoalAction = protocol.GoalActionContinue

	seedActiveTurn(t, store, "thread-1", "turn-3", nil)
	_, err = store.finishTurn(t.Context(), "turn-3", &turnFinish{conversationID: strings.TrimSpace("thread-1"), accountGoal: true, outbound: outbound})
	require.NoError(t, err)

	outbound = bridge.newOutboundMessage(inbound, "turn-4b", "reply", false)
	assert.True(t, outbound.GoalTurn)
	assert.False(t, outbound.GoalActive)
	assert.Equal(t, 3, outbound.GoalTurnNumber)

	require.NoError(t, store.BeginGoal("thread-2", &GoalState{Objective: "ship it forever", MaxTurns: 0}))

	bridge.config.ConversationID = "thread-2"
	outbound = bridge.newOutboundMessage(inbound, "turn-5", "reply", false)
	assert.True(t, outbound.GoalTurn)
	assert.True(t, outbound.GoalActive)
	assert.Zero(t, outbound.GoalTurnNumber)
	assert.Zero(t, outbound.GoalMaxTurns)

	_, err = store.UpdateGoalStatus("thread-2", GoalStatusBlocked, "need credentials")
	require.NoError(t, err)

	outbound = bridge.newOutboundMessage(inbound, "turn-6", "reply", false)
	assert.True(t, outbound.GoalTurn)
	assert.False(t, outbound.GoalActive)
}

func TestWorkflowFinalOutboundPreservesMetadata(t *testing.T) {
	t.Parallel()

	store := newTestSessionService(t)
	recordTestConversations(t, store, "main", map[string]ConversationKind{"thread-1": KindWebChat})
	require.NoError(t, store.BeginGoal("thread-1", &GoalState{Objective: "ship it", MaxTurns: 3}))
	bridge := &Bridge{config: Config{ConversationID: "thread-1", Agent: "main", SessionService: store}}
	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "$workflow audit", true)
	inbound.Workflow = protocol.WorkflowInvocation{Name: "audit"}
	inbound.SlackReply = &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.2", ThreadTS: "111.1"}

	outbound := bridge.newOutboundMessage(inbound, "turn-1", "finished", true)
	assert.Equal(t, "thread-1", outbound.ConversationID)
	assert.Equal(t, "thread-1", outbound.SourceConversationID)
	assert.Equal(t, "main", outbound.Agent)
	assert.Equal(t, "turn-1", outbound.TurnID)
	assert.Equal(t, "finished", outbound.Text)
	assert.True(t, outbound.Complete)
	assert.False(t, outbound.GoalTurn)
	assert.False(t, outbound.GoalActive)
	assert.Zero(t, outbound.GoalTurnNumber)
	assert.Zero(t, outbound.GoalMaxTurns)
	assert.Equal(t, inbound.SlackReply, outbound.SlackReply)
	assert.NotSame(t, inbound.SlackReply, outbound.SlackReply)
}

func readRocketCodeOutbound(t *testing.T, bus *testBus) *protocol.OutboundMessage {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	for msg := range bus.Outbound(ctx) {
		return msg
	}

	t.Fatal("timed out waiting for outbound message")

	return nil
}

func TestRocketcodeShellTempRel(t *testing.T) {
	t.Parallel()

	assert.Equal(t, ".rocketclaw/.rocketcode/tmp/anonymous", rocketcodeShellTempRel(".rocketclaw", ""))
	assert.Equal(t, ".rocketclaw/.rocketcode/tmp/anonymous", rocketcodeShellTempRel(".rocketclaw", " \t\n\u2003"))
	assert.Equal(t, ".rocketclaw/.rocketcode/tmp/slack-thread_C123_111.222", rocketcodeShellTempRel(".rocketclaw", "slack-thread:C123:111.222"))
	assert.Equal(t, "runtime/.rocketcode/tmp/cron_job", rocketcodeShellTempRel("runtime", "cron:job"))
	assert.Equal(t, "runtime/.rocketcode/tmp/Az09-_.", rocketcodeShellTempRel("runtime", " \u2003Az09-_.\t "))
	assert.Equal(t, "runtime/.rocketcode/tmp/A______Z", rocketcodeShellTempRel("runtime", " \u2003Aé/界🙂\xff\xfeZ\t "))
}

func seedReplayText(items []responses.ResponseInputItemUnionParam) string {
	parts := make([]string, 0, len(items))
	for i := range items {
		item := items[i]
		switch {
		case item.OfMessage != nil:
			var text string
			if item.OfMessage.Content.OfString.Valid() {
				text = item.OfMessage.Content.OfString.Value
			} else {
				texts := make([]string, 0, len(item.OfMessage.Content.OfInputItemContentList))

				for j := range item.OfMessage.Content.OfInputItemContentList {
					if item.OfMessage.Content.OfInputItemContentList[j].OfInputText != nil {
						texts = append(texts, item.OfMessage.Content.OfInputItemContentList[j].OfInputText.Text)
					}
				}

				text = strings.Join(texts, "\n")
			}

			parts = append(parts, strings.TrimSpace(string(item.OfMessage.Role))+": "+strings.TrimSpace(text))
		case item.OfInputMessage != nil:
			texts := make([]string, 0, len(item.OfInputMessage.Content))

			for j := range item.OfInputMessage.Content {
				if text := item.OfInputMessage.Content[j].GetText(); text != nil {
					texts = append(texts, *text)
				}
			}

			parts = append(parts, strings.TrimSpace(item.OfInputMessage.Role)+": "+strings.TrimSpace(strings.Join(texts, "\n")))
		case item.OfCompaction != nil:
			parts = append(parts, harness.CompactionCheckpointText(item.OfCompaction))
		case item.OfFunctionCall != nil:
			parts = append(parts, "assistant tool call "+item.OfFunctionCall.Name+": "+item.OfFunctionCall.Arguments)
		case item.OfFunctionCallOutput != nil:
			parts = append(parts, "tool result "+item.OfFunctionCallOutput.CallID.Or("")+": "+seedFunctionCallOutputText(item.OfFunctionCallOutput))
		case item.OfWebSearchCall != nil:
			data, err := json.Marshal(item.OfWebSearchCall.Action)
			if err == nil {
				parts = append(parts, "web search "+string(item.OfWebSearchCall.Status)+": "+string(data))
			}
		}
	}

	return strings.Join(parts, "\n")
}

func seedFunctionCallOutputText(output *responses.ResponseInputItemFunctionCallOutputParam) string {
	if output.Output.OfString.Valid() {
		return output.Output.OfString.Value
	}

	parts := make([]string, 0, len(output.Output.OfResponseFunctionCallOutputItemArray))
	attachments := 0

	for i := range output.Output.OfResponseFunctionCallOutputItemArray {
		item := output.Output.OfResponseFunctionCallOutputItemArray[i]
		if item.OfInputText != nil {
			parts = append(parts, item.OfInputText.Text)
		} else {
			attachments++
		}
	}

	if attachments > 0 {
		parts = append(parts, "[tool result attachments omitted from seed summary input]")
	}

	return strings.Join(parts, "\n")
}

func TestAppendSessionEntryKeepsPriorEntriesThenAdded(t *testing.T) {
	t.Parallel()

	continued := false
	stopped := appendSessionEntry(iter.Seq2[harness.SessionEntry, error](func(yield func(harness.SessionEntry, error) bool) {
		if !yield(harness.SessionEntry{Type: "stored"}, nil) {
			return
		}

		continued = true
	}), &harness.SessionEntry{Type: "added"})
	stopped(func(harness.SessionEntry, error) bool { return false })
	assert.False(t, continued)

	var (
		got       []string
		errFailed error
	)

	for entry, err := range appendSessionEntry(iter.Seq2[harness.SessionEntry, error](func(yield func(harness.SessionEntry, error) bool) {
		if !yield(harness.SessionEntry{Type: "stored"}, nil) {
			return
		}

		yield(harness.SessionEntry{Type: "failed"}, assert.AnError)
	}), &harness.SessionEntry{Type: "added"}) {
		got = append(got, entry.Type)

		if err != nil {
			errFailed = err
			break
		}
	}

	assert.Equal(t, []string{"stored", "failed"}, got)
	require.ErrorIs(t, errFailed, assert.AnError)

	replay := []json.RawMessage{[]byte(`"before"`)}
	added := harness.SessionEntry{Type: "added", Version: 1, Model: "early", ReplayInput: replay}
	seq := appendSessionEntry(iter.Seq2[harness.SessionEntry, error](func(yield func(harness.SessionEntry, error) bool) {
		yield(harness.SessionEntry{Type: "stored"}, nil)
	}), &added)
	added.Model = "late"
	replay[0] = []byte(`"after"`)
	before := time.Now().UTC()

	var seen []harness.SessionEntry

	for entry, err := range seq {
		require.NoError(t, err)

		seen = append(seen, entry)
	}

	require.Len(t, seen, 2)
	assert.Equal(t, "stored", seen[0].Type)
	assert.Equal(t, "late", seen[1].Model)
	assert.Equal(t, []byte(`"after"`), []byte(seen[1].ReplayInput[0]))
	assert.False(t, seen[1].Timestamp.Before(before))
}

// noteTest runs a Slack thread conversation's bridges wired to a background registry, as app.go
// does, against a provider that reports each request body and answers through respond.
type noteTest struct {
	service        *SessionService
	cfg            *config.Config
	conversationID string
	requests       chan string
	finals         chan *protocol.OutboundMessage
}

// noteTestResponder answers the nth provider request, counted from 0.
type noteTestResponder func(w http.ResponseWriter, r *http.Request, n int, body string)

func answerNoteTest(w http.ResponseWriter, _ *http.Request, _ int, _ string) {
	_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"answer","annotations":[]}]}]}`))
}

// holdNoteTest holds the provider requests numbered in held until release closes or the request ends.
func holdNoteTest(release <-chan struct{}, held ...int) noteTestResponder {
	return func(w http.ResponseWriter, r *http.Request, n int, body string) {
		if slices.Contains(held, n) {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}

		answerNoteTest(w, r, n, body)
	}
}

func newNoteTest(t *testing.T, respond noteTestResponder) *noteTest {
	t.Helper()

	workspace := t.TempDir()
	for _, agent := range []string{"main", "job"} {
		writeAgent(t, workspace, agent, "---\ndescription: Agent\nmode: primary\nmodel: gpt-6.1-sol\npermission: {}\n---\nPrompt\n")
	}

	writeAgent(t, workspace, "researcher", "---\ndescription: Researcher\nmode: subagent\nmodel: gpt-6.1-sol\npermission: {}\n---\nResearch\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	nt := &noteTest{service: newTestSessionServiceAt(t, workspace), conversationID: protocol.SlackThreadConversationID("C123", "111.222"), requests: make(chan string, 16), finals: make(chan *protocol.OutboundMessage, 16)}

	var (
		mu    sync.Mutex
		count int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		nt.requests <- string(body)

		mu.Lock()
		n := count
		count++
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		respond(w, r, n, string(body))
	}))
	t.Cleanup(server.Close)

	nt.cfg = &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIKey: "test", APIBaseURL: server.URL}, Slack: config.SlackConfig{Channels: []config.SlackChannelConfig{{Channel: "#ops", Agents: []string{"main"}}}}}
	require.NoError(t, nt.service.UpsertThread(nt.conversationID, ThreadState{Agent: "main"}))

	return nt
}

// run starts a manager of real bridges that hands each published outbound to observe and reports
// completed ones on finals. It returns the manager and its shutdown.
func (nt *noteTest) run(t *testing.T, observe func(*protocol.OutboundMessage)) (manager *threadBridgeManager, shutdown func()) {
	t.Helper()

	var registry *backgroundRegistry

	publisher := &outboundPublisherMock{PublishOutboundFunc: func(_ context.Context, message *protocol.OutboundMessage) error {
		observe(message)

		if message.Complete {
			nt.finals <- message
		}

		return nil
	}}
	manager = newThreadBridgeManager(config.NewLockedConfig(nt.cfg), nt.service, slog.New(slog.DiscardHandler), &bridgeFactoryMock{newBridgeFunc: func(cfg *Config) directBridge {
		cfg.SessionService, cfg.RequestRestart = nt.service, &runtimeRestarterMock{restartFunc: func(string) string { return "" }}
		bridge := NewConversation(config.NewLockedConfig(nt.cfg), publisher, cfg, slog.New(slog.DiscardHandler))
		bridge.threads, bridge.background = manager, registry

		return bridge
	}})
	registry = newBackgroundRegistry(nt.service, manager, testLogger())
	nt.service.jobs = registry

	return manager, runTestManager(t, manager)
}

func ignoreOutbound(*protocol.OutboundMessage) {}

func (nt *noteTest) bridge(t *testing.T, manager *threadBridgeManager, conversationID string) *Bridge {
	t.Helper()

	bridge, err := manager.recordedBridge(t.Context(), conversationID)
	require.NoError(t, err)

	return bridge
}

// slackPrompt is a human Slack message in the conversation's thread.
func (nt *noteTest) slackPrompt(text string) *protocol.InboundMessage {
	msg := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, text, true)
	msg.ConversationID, msg.SlackReply = nt.conversationID, &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.333", ThreadTS: "111.222"}

	return msg
}

// addNote stores a completed main-agent job of conversationID, started by origin, with a wakeable note.
func (nt *noteTest) addNote(t *testing.T, conversationID, jobID string, origin *protocol.InboundMessage) {
	t.Helper()

	job := testBackgroundJob(conversationID, jobID)
	job.origin = origin
	createTestBackgroundJob(t, nt.service, job)
	finishTestBackgroundJob(t, nt.service, job, backgroundCompleted, true)
}

// waitIdle waits until conversationID's bridge runs and holds no turn.
func (nt *noteTest) waitIdle(t *testing.T, manager *threadBridgeManager, conversationID string) {
	t.Helper()

	bridge := nt.bridge(t, manager, conversationID)
	require.Eventually(t, func() bool {
		running, err := queryStrings(context.Background(), nt.service.db, `SELECT id FROM turns WHERE phase NOT IN ($1, $2) AND takes_lane`, "running turns", turnWaiting, turnDone)
		return err == nil && len(running) == 0 && !bridge.handlingSnapshot() && len(bridge.requestCh) == 0
	}, 10*time.Second, 10*time.Millisecond)
}

func (nt *noteTest) request(t *testing.T) string {
	t.Helper()

	select {
	case body := <-nt.requests:
		return body
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a provider request")
		return ""
	}
}

func readFinalFor(t *testing.T, finals chan *protocol.OutboundMessage, conversationID string) *protocol.OutboundMessage {
	t.Helper()

	for {
		if final := readFinal(t, finals); final.ConversationID == conversationID {
			return final
		}
	}
}

func noteStates(t *testing.T, service *SessionService) []string {
	t.Helper()

	states, err := queryStrings(t.Context(), service.db, `SELECT j.input_id || ' ' || j.note || ' ' || j.wake::text`+testJobsSQL+` ORDER BY j.input_id`, "note states")
	require.NoError(t, err)

	return states
}

func lastRequestMessage(t *testing.T, body string) string {
	t.Helper()

	var request struct {
		Input []struct{ Content json.RawMessage }
	}
	require.NoError(t, json.Unmarshal([]byte(body), &request))
	require.NotEmpty(t, request.Input)

	var text string
	require.NoError(t, json.Unmarshal(request.Input[len(request.Input)-1].Content, &text))

	return text
}

func systemPromptHeader() string {
	return "[System additional_instructions=" + strconv.Quote(defaultReplyInstruction) + "]"
}

func testExecuteNote(jobID string) string {
	return `<execute id="` + jobID + `" state="completed" description="tests">` + "\ncompleted\n</execute>"
}

// One system turn holds all notes in finish order and replies in the thread of the turn that started the jobs.
func TestBackgroundNotesWakeIdleConversation(t *testing.T) {
	t.Parallel()

	nt := newNoteTest(t, answerNoteTest)
	manager, _ := nt.run(t, ignoreOutbound)

	for _, jobID := range []string{"turn-1/call/b", "turn-1/call/a"} {
		nt.addNote(t, nt.conversationID, jobID, nt.slackPrompt("run the tests"))
	}

	manager.noteReady(nt.conversationID)

	final := readFinal(t, nt.finals)
	assert.Equal(t, "answer", final.Text)
	assert.Equal(t, &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.333", ThreadTS: "111.222"}, final.SlackReply)
	assert.Equal(t, systemPromptHeader()+"\n\n"+testExecuteNote("turn-1/call/b")+"\n\n"+testExecuteNote("turn-1/call/a"), lastRequestMessage(t, nt.request(t)))

	manager.noteReady(nt.conversationID)
	nt.waitIdle(t, manager, nt.conversationID)
	assert.Empty(t, nt.requests, "delivered notes start no other turn")
	assert.Equal(t, []string{"turn-1/call/a consumed true", "turn-1/call/b consumed true"}, noteStates(t, nt.service))

	entries, err := nt.service.ObserveEntries(t.Context(), nt.conversationID)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	items, err := harness.ReplayInputToParams(entries[0].Entry.ReplayInput)
	require.NoError(t, err)
	assert.Equal(t, "turn-1/call/b turn-1/call/a", items[0].OfMessage.ExtraFields()["input_id"], "the Web finds the turn's notes by their job IDs")
}

// The note enters the next step, also of a goal continuation or a final answer, and starts no turn of its own.
func TestBackgroundNoteEntersRunningTurn(t *testing.T) {
	t.Parallel()

	for _, goal := range []bool{false, true} {
		t.Run(fmt.Sprintf("goal=%t", goal), func(t *testing.T) {
			release := make(chan struct{})
			nt := newNoteTest(t, holdNoteTest(release, 0))
			manager, _ := nt.run(t, ignoreOutbound)

			msg := nt.slackPrompt("hello")
			if goal {
				require.NoError(t, nt.service.BeginGoal(nt.conversationID, &GoalState{Objective: "ship it", MaxTurns: 1}))

				msg = protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "Continue the active goal loop.", false)
				msg.GoalAction = protocol.GoalActionContinue
			}

			require.NoError(t, nt.bridge(t, manager, nt.conversationID).Submit(t.Context(), msg))
			nt.request(t)

			nt.addNote(t, nt.conversationID, "turn-0/call/a", nt.slackPrompt("run the tests"))
			manager.noteReady(nt.conversationID)
			close(release)

			assert.Equal(t, "[System]\n\n"+testExecuteNote("turn-0/call/a"), lastRequestMessage(t, nt.request(t)), "the final answer continues with the note")
			readFinal(t, nt.finals)
			nt.waitIdle(t, manager, nt.conversationID)
			assert.Empty(t, nt.requests, "no turn starts for the note")
			assert.Equal(t, []string{"turn-0/call/a consumed true"}, noteStates(t, nt.service))
		})
	}
}

func TestBackgroundNoteAfterLastStepWakesOnce(t *testing.T) {
	t.Parallel()

	nt := newNoteTest(t, answerNoteTest)
	job := testBackgroundJob(nt.conversationID, "turn-0/call/a")
	job.origin = nt.slackPrompt("run the tests")
	createTestBackgroundJob(t, nt.service, job)

	var (
		manager *threadBridgeManager
		noted   bool
	)

	manager, _ = nt.run(t, func(message *protocol.OutboundMessage) {
		if !message.Complete || noted {
			return
		}

		noted = true
		end := *job

		end.status, end.noteState, end.wake, end.result = backgroundCompleted, notePending, true, "completed"
		if _, err := nt.service.finishBackgroundJob(context.Background(), &end); err != nil {
			t.Errorf("finish background job: %v", err)
		}

		manager.noteReady(nt.conversationID)
	})

	require.NoError(t, nt.bridge(t, manager, nt.conversationID).Submit(t.Context(), nt.slackPrompt("hello")))
	assert.NotContains(t, nt.request(t), "turn-0/call/a")
	readFinal(t, nt.finals)
	assert.Equal(t, systemPromptHeader()+"\n\n"+testExecuteNote("turn-0/call/a"), lastRequestMessage(t, nt.request(t)), "one later turn delivers the note")
	readFinal(t, nt.finals)
	nt.waitIdle(t, manager, nt.conversationID)
	assert.Empty(t, nt.requests)
	assert.Equal(t, []string{"turn-0/call/a consumed true"}, noteStates(t, nt.service))
}

func TestStoppedInitialTurnDoesNotReplayPendingNotes(t *testing.T) {
	t.Parallel()

	for _, human := range []bool{false, true} {
		t.Run(fmt.Sprintf("human=%t", human), func(t *testing.T) {
			release := make(chan struct{})
			nt := newNoteTest(t, holdNoteTest(release, 0))
			t.Cleanup(func() { close(release) })
			manager, _ := nt.run(t, ignoreOutbound)
			bridge := nt.bridge(t, manager, nt.conversationID)

			if human {
				require.NoError(t, bridge.Submit(t.Context(), nt.slackPrompt("Please review the draft.")))
				require.Contains(t, nt.request(t), "Please review the draft.")
			}

			nt.addNote(t, nt.conversationID, "turn-0/call/a", nt.slackPrompt("run the tests"))

			if human {
				// A previously released note waits for human work rather than waking another turn.
				_, err := nt.service.db.ExecContext(t.Context(), `UPDATE turns SET wake = FALSE WHERE conversation_id = $1 AND NOT takes_lane`, nt.conversationID)
				require.NoError(t, err)
			}

			manager.noteReady(nt.conversationID)

			if !human {
				require.Equal(t, systemPromptHeader()+"\n\n"+testExecuteNote("turn-0/call/a"), lastRequestMessage(t, nt.request(t)))
			}

			bridge.InterruptActiveTurn()
			assert.Equal(t, protocol.TerminalStopped, readFinal(t, nt.finals).Terminal)
			nt.waitIdle(t, manager, nt.conversationID)
			assert.Empty(t, nt.requests, "stopped execution must not retry")
			assert.Equal(t, []string{"turn-0/call/a pending false"}, noteStates(t, nt.service))

			entries, err := nt.service.ObserveEntries(t.Context(), nt.conversationID)
			require.NoError(t, err)

			if human {
				require.Len(t, entries, 1)
				assert.Contains(t, string(entries[0].Entry.ReplayInput[0]), "Please review the draft.")
			} else {
				assert.Empty(t, entries, "a pending Completion Note is not saved as replay input")
			}

			require.NoError(t, bridge.Submit(t.Context(), nt.slackPrompt("next")))
			assert.NotContains(t, nt.request(t), "turn-0/call/a", "only human input survives in saved replay")
			assert.Equal(t, 1, strings.Count(nt.request(t), `<execute id=\"turn-0/call/a\"`), "the next turn sees the released note once")
			readFinal(t, nt.finals)
			nt.waitIdle(t, manager, nt.conversationID)
			assert.Equal(t, []string{"turn-0/call/a consumed false"}, noteStates(t, nt.service))
		})
	}
}

// A note injected into a turn that is then stopped wakes nothing; the next turn delivers it.
func TestStoppedTurnReleasesItsNotes(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	nt := newNoteTest(t, func(w http.ResponseWriter, r *http.Request, n int, body string) {
		if n == 1 {
			<-r.Context().Done()
			return
		}

		holdNoteTest(release, 0)(w, r, n, body)
	})
	manager, _ := nt.run(t, ignoreOutbound)
	bridge := nt.bridge(t, manager, nt.conversationID)

	require.NoError(t, bridge.Submit(t.Context(), nt.slackPrompt("hello")))
	nt.request(t)
	nt.addNote(t, nt.conversationID, "turn-0/call/a", nt.slackPrompt("run the tests"))
	manager.noteReady(nt.conversationID)
	close(release)
	assert.Equal(t, "[System]\n\n"+testExecuteNote("turn-0/call/a"), lastRequestMessage(t, nt.request(t)))

	manager.InterruptConversation(nt.conversationID)
	assert.Empty(t, readFinal(t, nt.finals).Text)
	nt.waitIdle(t, manager, nt.conversationID)
	assert.Empty(t, nt.requests, "a stopped turn's note wakes nothing")
	assert.Equal(t, []string{"turn-0/call/a pending false"}, noteStates(t, nt.service))

	require.NoError(t, bridge.Submit(t.Context(), nt.slackPrompt("next")))
	assert.NotContains(t, nt.request(t), "turn-0/call/a")
	assert.Equal(t, "[System]\n\n"+testExecuteNote("turn-0/call/a"), lastRequestMessage(t, nt.request(t)), "the next turn delivers the note")
	readFinal(t, nt.finals)
	nt.waitIdle(t, manager, nt.conversationID)
	assert.Equal(t, []string{"turn-0/call/a consumed false"}, noteStates(t, nt.service))
}

// Released notes wake nothing again, and the owner's next turn delivers them.
func TestFailedWakeReleasesItsNotes(t *testing.T) {
	t.Parallel()

	nt := newNoteTest(t, func(w http.ResponseWriter, r *http.Request, n int, body string) {
		if n > 0 {
			answerNoteTest(w, r, n, body)
			return
		}

		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request","type":"invalid_request_error"}}`))
	})
	manager, _ := nt.run(t, ignoreOutbound)
	nt.addNote(t, nt.conversationID, "turn-0/call/a", nt.slackPrompt("run the tests"))

	manager.noteReady(nt.conversationID)
	assert.True(t, strings.HasPrefix(readFinal(t, nt.finals).Text, internalErrorResponse))
	assert.Equal(t, systemPromptHeader()+"\n\n"+testExecuteNote("turn-0/call/a"), lastRequestMessage(t, nt.request(t)))
	nt.waitIdle(t, manager, nt.conversationID)
	assert.Equal(t, []string{"turn-0/call/a pending false"}, noteStates(t, nt.service))

	manager.noteReady(nt.conversationID)
	nt.waitIdle(t, manager, nt.conversationID)
	assert.Empty(t, nt.requests, "a released note wakes nothing")

	require.NoError(t, nt.bridge(t, manager, nt.conversationID).Submit(t.Context(), nt.slackPrompt("next")))
	assert.NotContains(t, nt.request(t), "turn-0/call/a")
	assert.Equal(t, "[System]\n\n"+testExecuteNote("turn-0/call/a"), lastRequestMessage(t, nt.request(t)), "the next turn delivers the note")
	readFinal(t, nt.finals)
	nt.waitIdle(t, manager, nt.conversationID)
	assert.Equal(t, []string{"turn-0/call/a consumed false"}, noteStates(t, nt.service))
}

func TestResumedTurnInjectsNoteOnce(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	nt := newNoteTest(t, func(w http.ResponseWriter, r *http.Request, n int, body string) {
		if n == 1 {
			<-r.Context().Done()
			return
		}

		holdNoteTest(release, 0)(w, r, n, body)
	})
	first, shutdown := nt.run(t, ignoreOutbound)

	require.NoError(t, nt.bridge(t, first, nt.conversationID).Submit(t.Context(), nt.slackPrompt("hello")))
	nt.request(t)
	nt.addNote(t, nt.conversationID, "turn-0/call/a", nt.slackPrompt("run the tests"))
	first.noteReady(nt.conversationID)
	close(release)
	assert.Contains(t, nt.request(t), "turn-0/call/a")
	shutdown()

	second, _ := nt.run(t, ignoreOutbound)
	require.NoError(t, second.StartActiveTurns(t.Context()))
	readFinal(t, nt.finals)
	assert.Equal(t, 1, strings.Count(nt.request(t), "state="), "the resumed turn holds the note once")
	nt.waitIdle(t, second, nt.conversationID)
	assert.Empty(t, nt.requests, "the note is not injected again")

	entries, err := nt.service.ObserveEntries(t.Context(), nt.conversationID)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	recorded, err := json.Marshal(entries[0].Entry.ReplayInput)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(recorded), "state="), "the note is in history once")
	assert.Equal(t, []string{"turn-0/call/a consumed true"}, noteStates(t, nt.service))
}

// A note claimed before any step recorded it enters the resumed turn.
func TestResumedTurnInjectsNoteClaimedBeforeCrash(t *testing.T) {
	t.Parallel()

	nt := newNoteTest(t, answerNoteTest)
	nt.addNote(t, nt.conversationID, "turn-0/call/a", nt.slackPrompt("run the tests"))
	require.NoError(t, startTurnDB(t.Context(), nt.service.db, "turn-crashed", nt.conversationID, nt.slackPrompt("hello")))
	_, err := claimBackgroundNotes(t.Context(), nt.service.db, nt.conversationID, "turn-crashed", false)
	require.NoError(t, err)

	manager, _ := nt.run(t, ignoreOutbound)
	require.NoError(t, manager.StartActiveTurns(t.Context()))
	assert.NotContains(t, nt.request(t), "state=")
	assert.Equal(t, "[System]\n\n"+testExecuteNote("turn-0/call/a"), lastRequestMessage(t, nt.request(t)))
	readFinal(t, nt.finals)
	nt.waitIdle(t, manager, nt.conversationID)
	assert.Equal(t, []string{"turn-0/call/a consumed true"}, noteStates(t, nt.service))
}

// The destination is the chat a cron run reported in, subject to its output decision, or an External MCP managed chat.
func TestHiddenProducerWakeReachesDestination(t *testing.T) {
	t.Parallel()

	decide := func(w http.ResponseWriter, _ *http.Request, _ int, _ string) {
		// A hidden run's reply is its output.
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Tests passed.","annotations":[]}]}]}`))
	}

	t.Run("cron", func(t *testing.T) {
		nt := newNoteTest(t, decide)
		manager, _ := nt.run(t, ignoreOutbound)
		report := "web:cron:daily"

		// The run started a background job, then reported "Report started" in a new chat.
		msg := seedCronRun(t, nt.service, "cron:daily")
		job := testBackgroundJob("cron:daily", "turn-cron/call/a")
		job.origin = msg
		createTestBackgroundJob(t, nt.service, job)

		outbound := protocol.NewOutboundMessage("cron:daily", "Report started")
		outbound.TurnID, outbound.Complete, outbound.Cronjob = "turn-cron", true, msg.Cronjob
		_, err := nt.service.finishTurn(t.Context(), "turn-cron", &turnFinish{conversationID: strings.TrimSpace("cron:daily"), entries: []harness.SessionEntry{*testSessionEntry("job prompt", "Report started")}, outbound: outbound})
		require.NoError(t, err)
		require.NoError(t, manager.StartActiveTurns(t.Context()))
		readFinalFor(t, nt.finals, "cron:daily")
		require.Eventually(t, func() bool {
			destinations, err := queryStrings(context.Background(), nt.service.db, `SELECT COALESCE(c.reports_to, '') || ' ' || COALESCE(NULLIF(j.inbound_json->>'SyncDestination', ''), c.reports_to, '')`+testJobsSQL, "job destinations")
			return err == nil && slices.Equal(destinations, []string{report + " " + report})
		}, 10*time.Second, 10*time.Millisecond, "the job reports to the report chat")

		finishTestBackgroundJob(t, nt.service, job, backgroundCompleted, true)
		manager.noteReady("cron:daily")

		assert.Equal(t, "Tests passed.", readFinalFor(t, nt.finals, report).Text, "the decided output reaches the report chat")
		assert.Equal(t, systemPromptHeader()+"\n\n"+testExecuteNote(job.jobID), lastRequestMessage(t, nt.request(t)))
		nt.waitIdle(t, manager, report)
		assert.Equal(t, []string{"turn-cron/call/a consumed true"}, noteStates(t, nt.service))
	})

	// A wake queued from the run's origin before the run opened its report chat still reaches that chat.
	t.Run("cron wake queued before the report chat", func(t *testing.T) {
		nt := newNoteTest(t, decide)
		manager, _ := nt.run(t, ignoreOutbound)
		report := "web:cron:daily"

		msg := seedCronRun(t, nt.service, "cron:daily")
		job := testBackgroundJob("cron:daily", "turn-cron/call/a")
		job.origin = msg
		createTestBackgroundJob(t, nt.service, job)

		outbound := protocol.NewOutboundMessage("cron:daily", "Report started")
		outbound.TurnID, outbound.Complete, outbound.Cronjob = "turn-cron", true, msg.Cronjob
		_, err := nt.service.finishTurn(t.Context(), "turn-cron", &turnFinish{conversationID: strings.TrimSpace("cron:daily"), entries: []harness.SessionEntry{*testSessionEntry("job prompt", "Report started")}, outbound: outbound})
		require.NoError(t, err)
		require.NoError(t, manager.StartActiveTurns(t.Context()))
		readFinalFor(t, nt.finals, "cron:daily")
		require.Eventually(t, func() bool {
			destinations, err := queryStrings(context.Background(), nt.service.db, `SELECT COALESCE(c.reports_to, '')`+testJobsSQL, "job destinations")
			return err == nil && slices.Equal(destinations, []string{report})
		}, 10*time.Second, 10*time.Millisecond)

		finishTestBackgroundJob(t, nt.service, job, backgroundCompleted, true)

		stale := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "", false)
		stale.ConversationID, stale.RequireOutputDecision, stale.Cronjob = "cron:daily", msg.RequireOutputDecision, msg.Cronjob
		require.NoError(t, nt.bridge(t, manager, "cron:daily").enqueue(t.Context(), &bridgeRequest{inbound: stale, backgroundWake: true}, "submit background wake"))

		assert.Equal(t, "Tests passed.", readFinalFor(t, nt.finals, report).Text)
		nt.waitIdle(t, manager, report)
		assert.Equal(t, []string{"turn-cron/call/a consumed true"}, noteStates(t, nt.service))
	})

	t.Run("external MCP", func(t *testing.T) {
		nt := newNoteTest(t, decide)
		manager, _ := nt.run(t, ignoreOutbound)
		private, managed := "external_mcp:planner:private", protocol.SlackThreadConversationID("C9", "9.9")

		for _, conversationID := range []string{private, managed} {
			require.NoError(t, nt.service.UpsertThread(conversationID, ThreadState{Agent: "main"}))
		}

		origin := protocol.NewInboundMessage(protocol.SourceExternalMCP, protocol.InboundKindPrompt, "plan", true)
		origin.ConversationID, origin.SyncDestination = private, managed
		origin.SlackReply = &protocol.SlackReplyTarget{ChannelID: "C9", MessageTS: "9.10", ThreadTS: "9.9"}
		nt.addNote(t, private, "turn-mcp/call/a", origin)
		manager.noteReady(private)

		final := readFinalFor(t, nt.finals, managed)
		assert.Equal(t, "Tests passed.", final.Text)
		assert.Equal(t, "9.9", final.SlackReply.ThreadTS)
		nt.waitIdle(t, manager, managed)

		entries, err := nt.service.ObserveEntries(t.Context(), managed)
		require.NoError(t, err)
		require.NotEmpty(t, entries)

		synced, err := json.Marshal(entries)
		require.NoError(t, err)
		assert.Contains(t, string(synced), "turn-mcp/call/a", "the wake's history is synced to the managed thread")
	})
}

// A hidden run may never have a next turn, so its failed wake retries after a growing delay, also
// after a restart, until a wake delivers the note to the destination. Only the first failure
// reaches the destination, saying the wake retries.
func TestHiddenRunFailedWakeRetries(t *testing.T) {
	t.Parallel()

	nt := newNoteTest(t, func(w http.ResponseWriter, r *http.Request, n int, body string) {
		// A kept-alive connection would block on the network and stop the bubble's clock.
		w.Header().Set("Connection", "close")

		if n < 3 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"bad request","type":"invalid_request_error"}}`))

			return
		}

		answerNoteTest(w, r, n, body)
	})
	private, managed := "external_mcp:planner:private", protocol.SlackThreadConversationID("C9", "9.9")

	// The provider stays outside the bubble; the store, whose connections hold bubble timers, inside.
	synctest.Test(t, func(t *testing.T) {
		nt.service = newTestSessionServiceAt(t, nt.cfg.Workspace)
		for _, conversationID := range []string{nt.conversationID, private, managed} {
			require.NoError(t, nt.service.UpsertThread(conversationID, ThreadState{Agent: "main"}))
		}

		manager, shutdown := nt.run(t, ignoreOutbound)
		origin := protocol.NewInboundMessage(protocol.SourceExternalMCP, protocol.InboundKindPrompt, "plan", true)
		origin.ConversationID, origin.SyncDestination = private, managed
		nt.addNote(t, private, "turn-mcp/call/a", origin)

		// woken waits for the bubble to settle and returns the one wake request it sent.
		woken := func() string {
			t.Helper()

			synctest.Wait()
			require.Len(t, nt.requests, 1)

			return <-nt.requests
		}

		// posted returns the final messages the destination got.
		posted := func() []string {
			var texts []string

			for len(nt.finals) > 0 {
				if final := <-nt.finals; final.ConversationID == managed {
					texts = append(texts, final.Text)
				}
			}

			return texts
		}

		// failed checks that the wake failed and left its note to retry after delay.
		failed := func(attempts int, delay time.Duration) {
			t.Helper()

			var state string
			require.NoError(t, nt.service.db.QueryRowContext(t.Context(), `SELECT j.note || ' ' || j.wake::text || ' ' || j.wake_attempts || ' ' || j.run_at_unix_ns`+testJobsSQL).Scan(&state))
			assert.Equal(t, "pending true "+strconv.Itoa(attempts)+" "+strconv.FormatInt(time.Now().Add(delay).UnixNano(), 10), state)
		}

		manager.noteReady(private)
		woken()
		failed(1, time.Minute)

		first := posted()
		require.Len(t, first, 1, "the first failure reaches the destination once")
		assert.True(t, strings.HasPrefix(first[0], internalErrorResponse))
		assert.True(t, strings.HasSuffix(first[0], "\n\n"+wakeRetryNotice), first[0])

		manager.noteReady(private)
		synctest.Wait()
		assert.Empty(t, nt.requests, "a later look for work wakes nothing early")

		time.Sleep(time.Minute)
		woken()
		failed(2, 5*time.Minute)
		assert.Empty(t, posted(), "a failed retry posts nothing to the destination")

		shutdown()
		nt.restart(t)

		time.Sleep(5*time.Minute - time.Nanosecond)
		synctest.Wait()
		assert.Empty(t, nt.requests, "no wake before the delay ends")

		time.Sleep(time.Nanosecond)
		assert.Equal(t, systemPromptHeader()+"\n\n"+testExecuteNote("turn-mcp/call/a"), lastRequestMessage(t, woken()), "the restarted process retries the wake")
		failed(3, 30*time.Minute)
		assert.Empty(t, posted(), "a failed retry posts nothing to the destination")

		time.Sleep(30 * time.Minute)
		woken()
		assert.Equal(t, []string{"answer"}, posted(), "the answer reaches the destination")
		assert.Equal(t, []string{"turn-mcp/call/a consumed true"}, noteStates(t, nt.service))
	})
}

func TestStopLeavesBackgroundJobsRunning(t *testing.T) {
	t.Parallel()

	nt := newNoteTest(t, holdNoteTest(nil, 0))
	manager, _ := nt.run(t, ignoreOutbound)
	bridge := nt.bridge(t, manager, nt.conversationID)

	require.NoError(t, bridge.Submit(t.Context(), nt.slackPrompt("hello")))
	nt.request(t)

	started, release := make(chan struct{}, 1), make(chan struct{})
	turn := backgroundTurn{registry: bridge.background, root: testTurnRoot(t), conversationID: nt.conversationID, origin: nt.slackPrompt("run the tests")}
	_, err := turn.Run(t.Context(), &harness.BackgroundJob{ID: "turn-0/call/a", Kind: harness.BackgroundKindExecute, Label: "tests", Agent: "main", Detached: true}, blockedWork(started, release))
	require.NoError(t, err)
	<-started

	manager.InterruptConversation(nt.conversationID)
	readFinal(t, nt.finals)
	nt.waitIdle(t, manager, nt.conversationID)
	assert.Equal(t, backgroundRunning, testBackgroundRow(t, nt.service, nt.conversationID, "turn-0/call/a").status)

	close(release)
	assert.Contains(t, lastRequestMessage(t, nt.request(t)), `<execute id="turn-0/call/a" state="completed" description="tests">`+"\ndone\n</execute>", "the job finishes and reports")
	readFinal(t, nt.finals)
}

// A hidden producer's wake that finds its notes already delivered starts no turn.
func TestEmptyProducerWakeLeavesDestinationFree(t *testing.T) {
	t.Parallel()

	nt := newNoteTest(t, answerNoteTest)
	manager, _ := nt.run(t, ignoreOutbound)
	private, managed := "external_mcp:planner:private", protocol.SlackThreadConversationID("C9", "9.9")

	for _, conversationID := range []string{private, managed} {
		require.NoError(t, nt.service.UpsertThread(conversationID, ThreadState{Agent: "main"}))
	}

	wake := protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "", false)
	wake.ConversationID, wake.SyncDestination = private, managed
	destination := nt.bridge(t, manager, managed)
	destination.handle(t.Context(), &bridgeRequest{inbound: wake, backgroundWake: true, producer: nt.bridge(t, manager, private)})

	assert.Empty(t, nt.requests)
	assert.Same(t, destination, destination.stopTarget(), "a stop on the destination is not left routed to the producer")
}

// After a hidden producer's turn ends, schedules stay private, a nested workflow runs, and turn-bound tools refuse.
func TestBackgroundScriptUsesOriginTurnTools(t *testing.T) {
	t.Parallel()

	gate := ""
	nt := newNoteTest(t, func(w http.ResponseWriter, r *http.Request, n int, body string) {
		if n > 0 {
			answerNoteTest(w, r, n, body)
			return
		}

		code := "def main():\n    bash(command=r'''while [ ! -f " + gate + " ]; do sleep 0.05; done''')\n    return '|'.join([rocketclaw_attach_files_to_response(attachments=[]), rocketclaw_restart(reason='x'), rocketclaw_schedule_message(message='later', send_this_in='1h', recurring=False), rocketclaw_dynamic_workflow(name='quiet', args='')])\n"
		arguments, _ := json.Marshal(struct {
			Code        string `json:"code"`
			Description string `json:"description"`
			Background  bool   `json:"background"`
		}{code, "tools", true}) // Encoding strings and a bool cannot fail.

		_, _ = fmt.Fprintf(w, `{"id":"resp_0","object":"response","created_at":0,"status":"completed","model":"gpt-6.1-sol","output":[{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_b","name":"execute","arguments":%q}]}`, arguments)
	})
	gate = filepath.Join(nt.cfg.Workspace, "gate")
	writeWorkflowFixture(t, nt.cfg.Workspace, "quiet", "meta = {\"name\": \"quiet\", \"description\": \"Quiet\"}\ndef main(args):\n    return None\n")
	writeAgent(t, nt.cfg.Workspace, "main", "---\ndescription: Agent\nmode: primary\nmodel: gpt-6.1-sol\npermission:\n  bash: {\"*\": allow}\n  workflow: {\"*\": allow}\n  rocketclaw: {allow_background: allow, rocketclaw_restart: allow}\n---\nPrompt\n")

	manager, _ := nt.run(t, ignoreOutbound)
	private, managed := "external_mcp:planner:private", protocol.SlackThreadConversationID("C9", "9.9")

	for _, conversationID := range []string{private, managed} {
		require.NoError(t, nt.service.UpsertThread(conversationID, ThreadState{Agent: "main"}))
	}

	origin := protocol.NewInboundMessage(protocol.SourceExternalMCP, protocol.InboundKindPrompt, "plan", true)
	origin.ConversationID, origin.SyncDestination = private, managed
	require.NoError(t, nt.bridge(t, manager, managed).enqueue(t.Context(), &bridgeRequest{inbound: origin, producer: nt.bridge(t, manager, private)}, "test"))
	readFinalFor(t, nt.finals, managed)
	nt.waitIdle(t, manager, managed)
	nt.request(t)
	nt.request(t)

	require.NoError(t, os.WriteFile(gate, nil, 0o600))

	const refusal = "Refused: %[1]s did nothing, because this call now runs as background work, after the turn it acts on. Nothing was shown to the user, and nobody will answer. This text is not %[1]s's result or the user's answer. Decide without it, or call %[1]s in the turn that receives this job's result."
	assert.Contains(t, lastRequestMessage(t, nt.request(t)), fmt.Sprintf(refusal, attachFilesToolName)+"|"+fmt.Sprintf(refusal, restartToolName)+"|scheduled message in 1h0m0s|"+nestedWorkflowSilentCompleteText+"\n</execute>")
	readFinalFor(t, nt.finals, managed)
	nt.waitIdle(t, manager, managed)

	scheduled, err := nt.service.ScheduledMessagesForConversation(private)
	require.NoError(t, err)
	assert.Empty(t, scheduled, "the hidden run schedules nothing itself")

	scheduled, err = nt.service.ScheduledMessagesForConversation(managed)
	require.NoError(t, err)
	assert.Len(t, scheduled, 1, "its private schedule reaches the destination with the wake's sync")
}

// Exactly the three turn-bound platform tools refuse to run in background work; the page tools are not among them.
func TestTurnBoundPlatformTools(t *testing.T) {
	t.Parallel()

	origin := new(protocol.InboundMessage)
	bridge := &Bridge{runtime: new(config.LockedConfig), config: Config{ConversationID: "main", SessionService: newTestSessionService(t)}, log: slog.New(slog.DiscardHandler)}
	tools := slices.Concat(bridge.rocketcodeConfig(t.TempDir(), nil).CustomTools, sessionTagTools(bridge.config.SessionService, "main"), []harness.Tool{bridge.scheduleMessageTool(origin), bridge.resetScheduledMessagesTool(origin), restartTool(&runtimeRestarterMock{restartFunc: func(string) string { return "" }}), attachFilesTool(nil, bridge.config.SessionService, "main", "turn-1"), askUserQuestionTool(&userQuestionAskerMock{}, origin), sessionsWriteTool(bridge, origin, origin, "main", "main", harness.Agents{}), currentSessionIDTool("main")}, pageTools(bridge.config.SessionService, nil, nil, "main", "", ""))

	var bound []string

	for i := range tools {
		if tools[i].TurnBound {
			bound = append(bound, tools[i].Name)
		}
	}

	assert.ElementsMatch(t, []string{attachFilesToolName, askUserQuestionToolName, restartToolName}, bound)
}
