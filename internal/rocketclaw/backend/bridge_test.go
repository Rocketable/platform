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
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/gorilla/websocket"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

type discardPublisher struct{}

// PublishOutbound completes delivery at once, as Runtime.PublishOutbound does with no consumers.
func (discardPublisher) PublishOutbound(_ context.Context, message *protocol.OutboundMessage) error {
	message.MarkDelivered(nil)
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

func TestBridgeSwitchAgentTrimsAndStoresAgent(t *testing.T) {
	bridge := &Bridge{config: Config{Agent: "old"}}
	bridge.SwitchAgent("  next  ")
	assert.Equal(t, "next", bridge.agentSnapshot())
}

func TestBridgeConsumedInputsRetainIdentityAndOrder(t *testing.T) {
	bus := newTestBus()
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), bus: bus, config: Config{ConversationID: "web-conversation", SessionService: newTestSessionService(t)}, inputOpen: true, requestCh: make(chan bridgeRequest, 1)}
	content := protocol.InboundContent{Text: "same text"}
	initial := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &content, true)
	initial.Metadata["web_message_id"] = "initial"
	admitted, err := bridge.activateInbound(t.Context(), &bridgeRequest{inbound: initial})
	require.NoError(t, err)
	require.True(t, admitted)
	require.Empty(t, bus.outbound)

	header, _, _ := strings.Cut(buildPrompt(initial, nil), "\n\n")
	bridge.publishConsumed(t.Context(), initial, header)

	for _, id := range []string{"steer-first", "steer-second"} {
		inbound := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &content, true)
		inbound.Metadata["web_message_id"] = id
		require.NoError(t, bridge.Submit(t.Context(), inbound))
	}

	require.Equal(t, "steer-first", bridge.steers[0].queueItemID)
	require.Equal(t, "steer-second", bridge.steers[1].queueItemID)
	require.Len(t, bus.outbound, 1, "waiting steers are not consumed at submission")

	bridge.activeAttribution = rocketcode.ReplayAttribution{Agent: "planner", Model: "work/model-a", ReasoningEffort: new("high")}
	bridge.SwitchAgent("reviewer")
	inputs := bridge.drainSteers(t.Context(), rocketcode.TurnPhaseFinalAnswer)
	require.Len(t, inputs, 2)

	for i := range inputs {
		require.Equal(t, buildPrompt(initial, nil), inputs[i].Text, "identity metadata must not change prompt framing")
		require.NotContains(t, inputs[i].Text, "steer-")
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

	require.Empty(t, bridge.drainSteers(t.Context(), rocketcode.TurnPhaseFinalAnswer))
	require.Empty(t, bus.outbound, "a second drain must not repeat consumption")

	// A disconnected observer must not drop or replay an admitted steer.
	var logged bytes.Buffer

	bridge.log = slog.New(slog.NewTextHandler(&logged, nil))

	bus.Close()

	bridge.inputOpen = true
	disconnected := protocol.NewInboundMessageFromContent(protocol.SourceWeb, protocol.InboundKindSteer, &content, true)
	disconnected.Metadata["web_message_id"] = "steer-disconnected"
	require.NoError(t, bridge.Submit(t.Context(), disconnected))
	inputs = bridge.drainSteers(t.Context(), rocketcode.TurnPhaseFinalAnswer)
	require.Len(t, inputs, 1)
	require.Equal(t, buildPrompt(initial, nil), inputs[0].Text)
	require.Contains(t, logged.String(), "publish consumed web input")
	require.Contains(t, logged.String(), "test publisher closed")
	require.Empty(t, bridge.drainSteers(t.Context(), rocketcode.TurnPhaseFinalAnswer))
}

func TestPromotedQueueConsumptionKeepsQueueIdentity(t *testing.T) {
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
		promoted, err := manager.promoteQueueItem(t.Context(), conversationID, id, "")
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
	require.Len(t, bridge.drainSteers(t.Context(), rocketcode.TurnPhaseFinalAnswer), 2)
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
	tool := restartTool(testNoopRestart)

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
	order := []string{}

	tool := restartTool(func(reason string) (string, error) {
		order = append(order, "restart:"+reason)
		return "custom restart output", nil
	})

	result, err := tool.Call(t.Context(), []byte(`{"reason":"rocketclaw.json changed and runtime config must reload"}`), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"restart:rocketclaw.json changed and runtime config must reload"}, order)
	assert.Equal(t, "custom restart output", result.Output)
}

func TestReloadToolReturnsModelVisibleFailure(t *testing.T) {
	tool := reloadTool(func(string) (string, error) {
		return "", errors.New("invalid staged cron")
	})

	result, err := tool.Call(t.Context(), []byte(`{"reason":"cron changed"}`), nil)
	require.NoError(t, err)
	assert.Equal(t, "rocketclaw_reload failed; live runtime assets were not changed:\n\ninvalid staged cron", result.Output)

	_, err = tool.Call(t.Context(), []byte(`{"reason":"  "}`), nil)
	require.ErrorContains(t, err, "reason is required")
}

func TestUpdateGoalToolRunsSuccessfulCheckBeforeComplete(t *testing.T) {
	bridge := newGoalCheckTestBridge(t, `---
description: Main
model: gpt-5.4
permission:
  bash:
    "./scripts/check.sh": allow
---
Prompt
`, "#!/bin/sh\nprintf passed\n")
	require.NoError(t, bridge.config.SessionService.BeginGoal("thread-1", "fix lint", "./scripts/check.sh", 3, "", ""))

	result, err := updateGoalTool(bridge).Call(t.Context(), []byte(`{"status":"complete","note":"finished lint"}`), nil)
	require.NoError(t, err)
	assert.Equal(t, "goal marked complete", result.Output)

	goal, ok, err := bridge.config.SessionService.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, GoalStatusComplete, goal.Status)
	assert.Equal(t, "finished lint", goal.Note)
}

func TestUpdateGoalToolRecordsProgressNoteWithoutEndingGoal(t *testing.T) {
	bridge := newGoalCheckTestBridge(t, `---
description: Main
model: gpt-5.4
permission: {}
---
Prompt
`, "#!/bin/sh\nexit 7\n")
	require.NoError(t, bridge.config.SessionService.BeginGoal("thread-1", "fix lint", "./scripts/check.sh", 3, "", ""))

	tool := updateGoalTool(bridge)
	assert.Contains(t, fmt.Sprint(tool.Parameters), "what you are thinking")

	result, err := tool.Call(t.Context(), []byte(`{"status":"progress","note":"patched parser; tests next"}`), nil)
	require.NoError(t, err)
	assert.Equal(t, "goal progress recorded", result.Output)

	goal, ok, err := bridge.config.SessionService.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, GoalStatusActive, goal.Status)
	assert.Equal(t, "patched parser; tests next", goal.Note)
}

func TestUpdateGoalToolKeepsGoalActiveWhenCheckFails(t *testing.T) {
	bridge := newGoalCheckTestBridge(t, `---
description: Main
model: gpt-5.4
permission:
  bash:
    "./scripts/check.sh": allow
---
Prompt
`, "#!/bin/sh\nprintf failed\nexit 7\n")
	require.NoError(t, bridge.config.SessionService.BeginGoal("thread-1", "fix lint", "./scripts/check.sh", 3, "", ""))

	result, err := updateGoalTool(bridge).Call(t.Context(), []byte(`{"status":"complete"}`), nil)
	require.NoError(t, err)
	assert.Contains(t, result.Output, "goal check did not pass")
	assert.Contains(t, result.Output, "failed")

	goal, ok, err := bridge.config.SessionService.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, GoalStatusActive, goal.Status)
}

func TestUpdateGoalToolKeepsGoalActiveWhenCheckPermissionDenied(t *testing.T) {
	bridge := newGoalCheckTestBridge(t, `---
description: Main
model: gpt-5.4
permission:
  bash:
    "./scripts/check.sh --safe": allow
---
Prompt
`, "#!/bin/sh\nexit 0\n")
	require.NoError(t, bridge.config.SessionService.BeginGoal("thread-1", "fix lint", "./scripts/check.sh --dangerous", 3, "", ""))

	result, err := updateGoalTool(bridge).Call(t.Context(), []byte(`{"status":"complete"}`), nil)
	require.NoError(t, err)
	assert.Contains(t, result.Output, "not allowed by agent bash permission")

	goal, ok, err := bridge.config.SessionService.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, GoalStatusActive, goal.Status)
}

func TestUpdateGoalToolDoesNotRunCheckForBlocked(t *testing.T) {
	bridge := newGoalCheckTestBridge(t, `---
description: Main
model: gpt-5.4
permission:
  bash:
    "./scripts/check.sh": allow
---
Prompt
`, "#!/bin/sh\nexit 7\n")
	require.NoError(t, bridge.config.SessionService.BeginGoal("thread-1", "fix lint", "./scripts/check.sh", 3, "", ""))

	result, err := updateGoalTool(bridge).Call(t.Context(), []byte(`{"status":"blocked","note":"need credentials"}`), nil)
	require.NoError(t, err)
	assert.Equal(t, "goal marked blocked", result.Output)

	goal, ok, err := bridge.config.SessionService.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "need credentials", goal.Note)
}

func TestFinishGoalTurnAccountsKickoffAndContinuation(t *testing.T) {
	bridge := newGoalAccountingTestBridge(t)
	require.NoError(t, bridge.config.SessionService.BeginGoal("thread-1", "ship it", "", 3, "T123", "U456"))

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
	assert.Equal(t, &protocol.SlackReplyTarget{RecipientTeamID: "T123", RecipientUserID: "U456"}, continuation.SlackReply)

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
	bridge := newGoalAccountingTestBridge(t)
	require.NoError(t, bridge.config.SessionService.BeginGoal("thread-1", "ship it", "", 3, "starter-team", "starter-user"))

	msg := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "try this angle", false)
	msg.ConversationID = "thread-1"
	msg.SlackReply = &protocol.SlackReplyTarget{RecipientTeamID: "resteer-team", RecipientUserID: "resteer-user"}
	finishTestGoalTurn(t, bridge, msg)

	goal, ok, err := bridge.config.SessionService.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 0, goal.TurnsUsed)
	assert.Equal(t, "starter-team", goal.SlackRecipientTeamID)
	assert.Equal(t, "starter-user", goal.SlackRecipientUserID)
	require.Len(t, bridge.requestCh, 1)
	continuation := (<-bridge.requestCh).inbound
	assert.Equal(t, protocol.GoalActionContinue, continuation.GoalAction)
	assert.Equal(t, "starter-team", continuation.SlackReply.RecipientTeamID)
	assert.Equal(t, "starter-user", continuation.SlackReply.RecipientUserID)
}

func TestGoalSteeringPromptRequiresProgressSummaryAndNote(t *testing.T) {
	prompt := goalSteeringPrompt(&GoalState{Objective: "ship it", MaxTurns: 5, TurnsUsed: 1})

	assert.Contains(t, prompt, "Progress summary:")
	assert.Contains(t, prompt, "status progress")
	assert.Contains(t, prompt, "status complete")
	assert.Contains(t, prompt, "status blocked")
	assert.Contains(t, prompt, "note")
}

func TestInterruptActiveTurnSignalsAndPreservesQueue(t *testing.T) {
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
	store := newTestSessionService(t)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	stashAt := time.Date(2000, 1, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ID: "q1", ConversationID: conversationID, Message: "A", Principal: "U1", StashAt: stashAt, Position: 0, SlackChannel: "C123", SlackTS: "111.222"}))
	require.NoError(t, store.PutScheduledMessage("s1", &protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "scheduled", DueAt: time.Date(2000, 1, 1, 12, 5, 0, 0, time.UTC)}))

	var popped []string

	bridge := &Bridge{bus: discardPublisher{}, log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store, EnqueueActivation: EnqueueActivation{Fn: func(_ context.Context, item *protocol.ThreadQueueItem, _ *protocol.InboundMessage) error {
		popped = append(popped, item.Message)
		return nil
	}}}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}

	require.NoError(t, bridge.pickLaterWork(t.Context(), false))
	require.Len(t, bridge.requestCh, 1)
	request := <-bridge.requestCh
	require.NotNil(t, request.inbound)
	assert.Equal(t, "A", request.inbound.Text)
	assert.Equal(t, "q1", request.queueItemID)
	admitted, err := bridge.activateInbound(t.Context(), &request)
	require.NoError(t, err)
	require.True(t, admitted)
	assert.Equal(t, []string{"A"}, popped)

	messages, err := store.ScheduledMessagesForConversation(conversationID)
	require.NoError(t, err)
	require.Len(t, messages, 1)
}

func TestPickLaterWorkWaitsWhenParkedAfterFutureScheduled(t *testing.T) {
	store := newTestSessionService(t)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, store.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ID: "q1", ConversationID: conversationID, Message: "Ship README", Principal: "U1", StashAt: time.Date(2000, 1, 1, 3, 0, 0, 0, time.UTC), Position: 0, ParkAfter: "s1"}))
	require.NoError(t, store.PutScheduledMessage("s1", &protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "scheduled", DueAt: time.Now().UTC().Add(time.Hour)}))

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}

	require.NoError(t, bridge.pickLaterWork(t.Context(), false))
	assert.Empty(t, bridge.requestCh)
}

func TestPickLaterWorkStartsParkedQueueAfterScheduledCancel(t *testing.T) {
	store := newTestSessionService(t)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, store.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ID: "q1", ConversationID: conversationID, Message: "Ship README", Principal: "U1", StashAt: time.Date(2000, 1, 1, 3, 0, 0, 0, time.UTC), Position: 0, ParkAfter: "s1", SlackChannel: "C123", SlackTS: "111.222"}))
	require.NoError(t, store.PutScheduledMessage("s1", &protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "scheduled", DueAt: time.Now().UTC().Add(time.Hour)}))
	require.NoError(t, (stateDAO{db: store.db}).deleteScheduledMessage(t.Context(), "s1"))

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}

	require.NoError(t, bridge.pickLaterWork(t.Context(), false))
	require.Len(t, bridge.requestCh, 1)
	assert.Equal(t, "Ship README", (<-bridge.requestCh).inbound.Text)
}

func TestActivateInboundReturnsStartedScheduleDeleteError(t *testing.T) {
	store := newTestSessionService(t)
	require.NoError(t, store.Stop())
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: protocol.SlackThreadConversationID("C123", "111.222"), SessionService: store}}
	admitted, err := bridge.activateInbound(t.Context(), &bridgeRequest{inbound: protocol.NewInboundMessage(protocol.SourceSystem, protocol.InboundKindPrompt, "later", false), scheduledMessageID: "s1"})
	require.Error(t, err)
	assert.False(t, admitted)
}

func TestPickLaterWorkSkipsWhenGoalStillActive(t *testing.T) {
	store := newTestSessionService(t)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, store.BeginGoal(conversationID, "ship it", "", 3, "", ""))
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
	store := newTestSessionService(t)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, store.BeginGoal(conversationID, "ship it", "", 3, "", ""))
	require.NoError(t, store.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ID: "q1", ConversationID: conversationID, Message: "changelog", Principal: "U1", StashAt: time.Date(2000, 1, 1, 3, 0, 0, 0, time.UTC), Position: 0, SlackChannel: "C123", SlackTS: "111.222"}))
	require.NoError(t, store.StopGoal(conversationID))

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}

	require.NoError(t, bridge.pickLaterWork(t.Context(), false))
	require.Len(t, bridge.requestCh, 1)
	assert.Equal(t, "changelog", (<-bridge.requestCh).inbound.Text)
}

func TestEnqueueActivationZeroValueIsInert(t *testing.T) {
	require.NoError(t, (EnqueueActivation{}).Activate(t.Context(), &protocol.ThreadQueueItem{}, nil))
}

func TestPickLaterWorkClaimsDueRecurringSchedule(t *testing.T) {
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
	bridge := &Bridge{stopCh: make(chan struct{}), activeReply: protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "ordinary", true)}
	_, bridge.activeTurnCancel = context.WithCancel(t.Context())
	require.NoError(t, bridge.Stop())
	assert.False(t, bridge.activeTurnInterrupted)
}

func TestInterruptActiveWorkflowCancelsWithoutSignalChannel(t *testing.T) {
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

	return &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: "thread-1", SessionService: store}, requestCh: make(chan bridgeRequest, 4), stopCh: make(chan struct{})}
}

func newGoalCheckTestBridge(t *testing.T, agent, script string) *Bridge {
	t.Helper()

	workspace := t.TempDir()
	writeAgent(t, workspace, "main", agent)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	require.NoError(t, root.MkdirAll("scripts", 0o755))
	require.NoError(t, root.WriteFile("scripts/check.sh", []byte(script), 0o755))
	require.NoError(t, root.Chmod("scripts/check.sh", 0o755))
	require.NoError(t, root.Close())

	store := newTestSessionServiceAt(t, workspace)

	return &Bridge{
		log:     slog.New(slog.DiscardHandler),
		runtime: &config.Config{Workspace: workspace},
		config:  Config{ConversationID: "thread-1", Agent: "main", SessionService: store},
	}
}

func TestRestartToolAcceptsEmptyOutputAndPropagatesErrors(t *testing.T) {
	tool := restartTool(testNoopRestart)
	result, err := tool.Call(t.Context(), []byte(`{"reason":"cron changed"}`), nil)
	require.NoError(t, err)
	assert.Empty(t, result.Output)

	_, err = tool.Call(t.Context(), []byte(`{"reason":" "}`), nil)
	require.EqualError(t, err, "reason is required")

	_, err = tool.Call(t.Context(), []byte(`{`), nil)
	require.ErrorContains(t, err, "parse restart request")

	tool = restartTool(func(string) (string, error) { return "", assert.AnError })
	_, err = tool.Call(t.Context(), []byte(`{"reason":"cron changed"}`), nil)
	assert.ErrorIs(t, err, assert.AnError)
}

func testNoopRestart(string) (string, error) { return "", nil }

// finishTestGoalTurn finishes a turn for msg as handleInbound does: accounting in the finish transaction, then continuation.
func finishTestGoalTurn(t *testing.T, b *Bridge, msg *protocol.InboundMessage) {
	t.Helper()

	turnID := "turn-" + rand.Text()
	require.NoError(t, startTurnDB(t.Context(), b.config.SessionService.db, turnID, b.config.ConversationID, msg))
	_, err := b.config.SessionService.finishTurn(t.Context(), turnID, &turnFinish{store: newSessionStore(b.config.ConversationID, b.config.SessionService), accountGoal: msg.GoalAction != protocol.GoalActionNone, outbound: protocol.NewOutboundMessage(b.config.ConversationID, "")})
	require.NoError(t, err)
	require.NoError(t, b.finishGoalTurn(t.Context(), &bridgeRequest{inbound: msg}))
}

// runTestTurn runs one turn and appends its staged history as a request finish does.
func runTestTurn(ctx context.Context, b *Bridge, msg *protocol.InboundMessage, turnID string) (runResult, error) {
	finish := turnFinish{store: newSessionStore(b.config.ConversationID, b.config.SessionService)}
	b.mu.Lock()
	b.activeTurnID = turnID
	b.mu.Unlock()
	result, err := b.runTurn(ctx, msg, turnID, turnID, &finish)

	for i := range finish.entries {
		if _, errAppend := finish.store.appendDB(context.WithoutCancel(ctx), b.config.SessionService.db, &finish.entries[i]); errAppend != nil {
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

	return b.finish(ctx, &bridgeRequest{inbound: inbound, turnID: result.turnID}, &turnFinish{store: newSessionStore(b.config.ConversationID, b.config.SessionService)}, result, "")
}

func workflowSummaryPayloadFromEntry(t *testing.T, entry *rocketcode.SessionEntry) string {
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

func workflowSummaryFromEntry(t *testing.T, entry *rocketcode.SessionEntry) workflowRunSummary {
	t.Helper()

	var summary workflowRunSummary
	require.NoError(t, json.Unmarshal([]byte(workflowSummaryPayloadFromEntry(t, entry)), &summary))

	return summary
}

func testNoopStartNewThread(context.Context, *protocol.StartNewThreadRequest) (protocol.StartNewThreadResult, error) {
	return protocol.StartNewThreadResult{}, errors.New("start new thread is inert in this test")
}

func TestPublishFinalPreservesTurnID(t *testing.T) {
	bus := newTestBus()
	defer bus.Close()

	bridge := new(Bridge)
	bridge.bus = bus
	bridge.log = slog.New(slog.DiscardHandler)
	bridge.config = Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", RequestRestart: testNoopRestart, SessionService: newTestSessionService(t)}
	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "hello", true)
	inbound.ConversationID = bridge.config.ConversationID
	result := runResult{turnID: "turn-1", text: "hello back"}

	var group errgroup.Group

	group.Go(func() error { return publishTestFinal(context.Background(), bridge, inbound, &result) })

	final := readRocketCodeOutbound(t, bus)
	assert.Equal(t, "turn-1", final.TurnID)
	assert.Equal(t, "hello back", final.Text)
	assert.True(t, final.Complete)
	final.MarkDelivered(nil)
	require.NoError(t, group.Wait())
}

func TestPublishFinalMarksCurrentGoalCompletion(t *testing.T) {
	bus := newTestBus()
	defer bus.Close()

	bridge := new(Bridge)
	bridge.bus = bus
	bridge.log = slog.New(slog.DiscardHandler)
	bridge.config = Config{ConversationID: "thread-1", Agent: "main", RequestRestart: testNoopRestart, SessionService: newTestSessionService(t)}
	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "done", true)
	inbound.ConversationID = "thread-1"
	result := runResult{turnID: "turn-1", text: "done", goalCompleted: true}

	var group errgroup.Group
	group.Go(func() error { return publishTestFinal(context.Background(), bridge, inbound, &result) })

	outbound := readRocketCodeOutbound(t, bus)
	assert.True(t, outbound.GoalComplete)
	outbound.MarkDelivered(nil)
	require.NoError(t, group.Wait())
}

func TestPublishFinalDoesNotReuseCompletedGoal(t *testing.T) {
	bus := newTestBus()
	defer bus.Close()

	store := newTestSessionService(t)
	require.NoError(t, store.BeginGoal("thread-1", "ship it", "", 3, "", ""))
	_, err := store.UpdateGoalStatus("thread-1", GoalStatusComplete, "done")
	require.NoError(t, err)

	bridge := new(Bridge)
	bridge.bus = bus
	bridge.log = slog.New(slog.DiscardHandler)
	bridge.config = Config{ConversationID: "thread-1", Agent: "main", RequestRestart: testNoopRestart, SessionService: store}
	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "trailing message", true)
	inbound.ConversationID = "thread-1"
	result := runResult{turnID: "turn-2", text: "normal reply"}

	var group errgroup.Group
	group.Go(func() error { return publishTestFinal(context.Background(), bridge, inbound, &result) })

	outbound := readRocketCodeOutbound(t, bus)
	assert.False(t, outbound.GoalComplete)
	outbound.MarkDelivered(nil)
	require.NoError(t, group.Wait())
}

func TestPublishFinalCarriesMainResponseAttachments(t *testing.T) {
	bus := newTestBus()
	defer bus.Close()

	bridge := new(Bridge)
	bridge.bus = bus
	bridge.log = slog.New(slog.DiscardHandler)
	bridge.config = Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", RequestRestart: testNoopRestart, SessionService: newTestSessionService(t)}
	result := runResult{turnID: "turn-1", attachments: []protocol.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}}}

	for _, errDelivery := range []error{nil, errors.New("delivery failed")} {
		synctest.Test(t, func(t *testing.T) {
			inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "hello", true)
			inbound.ConversationID = bridge.config.ConversationID
			resultCh := inbound.EnableResponseWait()

			var group errgroup.Group

			bridge.inputOpen = true

			group.Go(func() error { return publishTestFinal(t.Context(), bridge, inbound, &result) })

			outbound := readRocketCodeOutbound(t, bus)
			assert.True(t, outbound.Complete)
			assert.Empty(t, outbound.Text)
			assert.Equal(t, result.attachments, outbound.Attachments)
			synctest.Wait()
			assert.Empty(t, resultCh, "caller must wait for final delivery")
			assert.False(t, bridge.inputOpen, "late steer must not join during final delivery")

			outbound.MarkDelivered(errDelivery)
			require.ErrorIs(t, group.Wait(), errDelivery)

			response := <-resultCh
			require.ErrorIs(t, response.Err, errDelivery)
			assert.Equal(t, result.attachments, response.Attachments)
		})
	}
}

func TestHandleInboundReportsRocketCodeErrorDetail(t *testing.T) {
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
	bridge := NewConversation(&config.Config{Workspace: workspace}, bus, &Config{ConversationID: conversationID, Agent: "main", RequestRestart: testNoopRestart, StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.DiscardHandler))
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
	outbound.MarkDelivered(nil)
	require.ErrorContains(t, group.Wait(), `prepare rocketcode turn: missing required default agent "main"`)

	response := <-inbound.EnableResponseWait()
	assert.Equal(t, outbound.Text, response.Text)
	require.NoError(t, response.Err)
}

func TestRocketCodeConfigEnablesDiagnostics(t *testing.T) {
	bridge := &Bridge{runtime: &config.Config{AutoApproverModel: "gpt-5.4-mini"}, config: Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", RequestRestart: testNoopRestart, RequestReload: func(string) (string, error) {
		return "rocketclaw runtime assets reloaded", nil
	}, SessionService: newTestSessionService(t)}}
	cfg := bridge.rocketcodeConfig(t.TempDir(), nil, rocketcode.Tool{Name: attachFilesToolName})

	toolNames := make([]string, 0, len(cfg.CustomTools))
	for i := range cfg.CustomTools {
		toolNames = append(toolNames, cfg.CustomTools[i].Name)
	}

	assert.True(t, cfg.Diagnostics)
	assert.True(t, cfg.ExperimentalStrongerSkills)
	assert.True(t, cfg.AutoApprovePermissions)
	assert.Equal(t, "gpt-5.4-mini", cfg.AutoApproverModel)
	assert.Equal(t, 16, cfg.ParallelToolCalls)
	assert.Equal(t, rocketcode.PromptShellCommandExpansion{PrimaryPrompts: true, SubagentPrompts: true, SkillPrompts: true, InputPrompts: false}, cfg.ExpandPromptShellCommands)
	assert.NotContains(t, toolNames, restartToolName)
	assert.Contains(t, toolNames, scheduleMessageToolName)
	assert.Contains(t, toolNames, reloadToolName)
	assert.Contains(t, toolNames, resetScheduledMessagesToolName)
	assert.Contains(t, toolNames, attachFilesToolName)
	assert.Contains(t, toolNames, listSessionsToolName)
	assert.Contains(t, toolNames, getSessionToolName)
	assert.Contains(t, toolNames, currentSessionIDToolName)
	assert.Equal(t, map[string]string{"A": "B"}, bridge.rocketcodeConfig(t.TempDir(), map[string]string{"A": "B"}).ShellEnv)
}

func TestAppendOverlayPromptToAgentIncludesConfiguredOverlayPrompt(t *testing.T) {
	workspace := t.TempDir()
	agents := rocketcode.Agents{Items: map[string]rocketcode.Agent{"main": {Name: "main", Description: "", Model: "", ReasoningEffort: "", Verbosity: "", Prompt: "base prompt", Location: "", Permission: rocketcode.PermissionSet{Buckets: nil}, Frontmatter: nil, FileMode: 0}}}

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

func TestNewConversationKeepsInjectedSessionService(t *testing.T) {
	service, err := NewSessionService(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })

	bus := newTestBus()
	defer bus.Close()

	bridge := NewConversation(new(config.Config), bus, &Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", RequestRestart: testNoopRestart, StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.DiscardHandler))
	assert.Same(t, service, bridge.config.SessionService)
}

// A submission during shutdown is stored in the Thread Queue with its full
// inbound, so it survives the process exit and starts after the restart.
func TestBridgeSubmitAfterStopStoresRequestDurably(t *testing.T) {
	bus := newTestBus()
	defer bus.Close()

	service := newTestSessionService(t)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	bridge := NewConversation(&config.Config{Workspace: t.TempDir()}, bus, &Config{ConversationID: conversationID, Agent: "main", StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.DiscardHandler))
	require.NoError(t, startTestBridge(context.Background(), bridge))
	require.NoError(t, bridge.Stop())

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
	service := newTestSessionService(t)
	require.NoError(t, service.Stop())

	bus := newTestBus()
	defer bus.Close()

	bridge := NewConversation(&config.Config{Workspace: t.TempDir()}, bus, &Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.DiscardHandler))
	err := startTestBridge(context.Background(), bridge)
	require.ErrorContains(t, err, "load scheduled messages")
}

func TestBridgeEnqueueReturnsContextOrStopErrors(t *testing.T) {
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

func TestBridgePassesLocalGuardrailToRocketCode(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: '{{ model \"main\" }}'\npermission:\n  task:\n    helper: allow\n---\nPrompt\n")
	writeAgent(t, workspace, "helper", "---\ndescription: Helper\nmodel: '{{ model \"helper\" }}'\nguardrail: guardrail\n---\nHelper prompt\n")
	writeAgent(t, workspace, "guardrail", "---\ndescription: Guardrail\nmodel: '{{ model \"guardrail\" }}'\n---\nGuard delegated work\n")
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
				assert.Equal(t, "guard", provider)
				assert.Equal(t, "guardrail-model", body["model"])
				assert.Contains(t, fmt.Sprint(body["instructions"]), "Guard delegated work")
				assert.Contains(t, fmt.Sprint(body), "Current Action: delegation")
				assert.Contains(t, fmt.Sprint(body), "The agent main wants to delegate to helper")
				assert.Contains(t, fmt.Sprint(body), "delegated prompt")
				assert.Contains(t, fmt.Sprint(body["text"]), "json_schema")
				writeRawRunMessage(t, w, "resp_2", "msg_2", `{"approved":true,"reason":""}`)
			case 3:
				assert.Equal(t, "child", provider)
				assert.Equal(t, "helper-model", body["model"])
				assert.Contains(t, fmt.Sprint(body["instructions"]), "Helper prompt")
				assert.Contains(t, fmt.Sprint(body), "delegated prompt")
				writeRawRunMessage(t, w, "resp_3", "msg_3", "child response")
			case 4:
				assert.Equal(t, "guard", provider)
				assert.Equal(t, "guardrail-model", body["model"])
				assert.Contains(t, fmt.Sprint(body["instructions"]), "Guard delegated work")
				assert.Contains(t, fmt.Sprint(body), "Current Action: response")
				assert.Contains(t, fmt.Sprint(body), "And the response from helper to main")
				assert.Contains(t, fmt.Sprint(body), "child response")
				assert.Contains(t, fmt.Sprint(body["text"]), "json_schema")
				writeRawRunMessage(t, w, "resp_4", "msg_4", `{"approved":true,"reason":""}`)
			case 5:
				assert.Equal(t, "openai", provider)
				assert.Equal(t, "main-model", body["model"])
				assert.Contains(t, fmt.Sprint(body), "<task_result>\nchild response\n</task_result>")
				writeRawRunMessage(t, w, "resp_5", "msg_5", "persistent done")
			default:
				t.Fatalf("unexpected response request %d", requests)
			}
		}))
	}
	server := newServer("openai")
	t.Cleanup(server.Close)

	childServer := newServer("child")
	t.Cleanup(childServer.Close)

	guardServer := newServer("guard")
	t.Cleanup(guardServer.Close)

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })

	bus := newTestBus()
	defer bus.Close()

	conversationID := protocol.SlackThreadConversationID("C123", "111.222")

	var logs lockedBuffer

	bridge := NewConversation(&config.Config{Workspace: workspace, Models: map[string]string{"main": "main-model", "helper": "child/helper-model", "guardrail": "guard/guardrail-model"}, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}, Providers: map[string]config.OpenAIConfig{"child": {APIBaseURL: childServer.URL}, "guard": {APIBaseURL: guardServer.URL}}}, bus, &Config{ConversationID: conversationID, Agent: "main", StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.NewJSONHandler(&logs, nil)))
	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "hello", true)
	inbound.ConversationID = conversationID

	group, ctx := errgroup.WithContext(t.Context())
	group.Go(func() error { return handleTestInbound(ctx, bridge, &bridgeRequest{inbound: inbound}) })

	var outbound *protocol.OutboundMessage

	for msg := range bus.Outbound(ctx) {
		msg.MarkDelivered(nil)

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
			assert.NotEqual(t, rocketcode.ChatResponseAssistantMessage, event.Kind)
		}
	}

	assert.Contains(t, logs.String(), `"event":"first_assistant_text"`)
	assert.Contains(t, logs.String(), `"event":"operation_snapshot"`)
	assert.NotContains(t, logs.String(), "delegated prompt")
	require.Equal(t, 5, requests)

	entries, err := service.ObserveEntries(context.Background(), conversationID)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "openai/main-model", entries[0].Entry.Model)
}

func TestBridgeStopAfterStartContextCanceledIsIdempotent(t *testing.T) {
	bus := newTestBus()
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	bridge := NewConversation(&config.Config{Workspace: t.TempDir()}, bus, &Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", StartNewThread: testNoopStartNewThread, SessionService: newTestSessionService(t)}, slog.New(slog.DiscardHandler))
	require.NoError(t, startTestBridge(ctx, bridge))

	cancel()

	select {
	case <-bridge.stopCh:
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop after context cancellation")
	}

	require.NoError(t, bridge.Stop())
}

func TestScheduleMessageToolValidatesAndPreservesMessage(t *testing.T) {
	var (
		delay     time.Duration
		message   string
		recurring bool
		logs      bytes.Buffer
	)

	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	tool := scheduleMessageTool(func(d time.Duration, msg string, repeat bool) error {
		delay = d
		message = msg
		recurring = repeat

		return nil
	}, logger)
	assert.ElementsMatch(t, []string{"message", "send_this_in", "recurring"}, tool.Parameters["required"])

	result, err := tool.Call(context.Background(), []byte(`{"message":"  keep spaces  ","send_this_in":"5m"}`), nil)
	require.NoError(t, err)
	assert.Equal(t, "scheduled message in 5m0s", result.Output)
	assert.Equal(t, 5*time.Minute, delay)
	assert.Equal(t, "  keep spaces  ", message)
	assert.False(t, recurring)
	assert.Contains(t, logs.String(), "rocketclaw schedule message tool called")

	result, err = tool.Call(context.Background(), []byte(`{"message":"again","send_this_in":"1m","recurring":true}`), nil)
	require.NoError(t, err)
	assert.Equal(t, "scheduled recurring message every 1m0s", result.Output)
	assert.True(t, recurring)

	for _, raw := range []string{
		`{`,
		`{"message":"  ","send_this_in":"5m"}`,
		`{"message":"hello","send_this_in":"nope"}`,
		`{"message":"hello","send_this_in":"0s"}`,
		`{"message":"hello","send_this_in":"2h"}`,
		`{"message":"hello","send_this_in":"30s","recurring":true}`,
	} {
		_, err := tool.Call(context.Background(), []byte(raw), nil)
		require.Error(t, err, raw)
	}

	tool = scheduleMessageTool(func(time.Duration, string, bool) error { return assert.AnError }, logger)
	_, err = tool.Call(context.Background(), []byte(`{"message":"hello","send_this_in":"5m"}`), nil)
	require.ErrorIs(t, err, assert.AnError)
	assert.Contains(t, logs.String(), "rocketclaw schedule message tool failed")
}

func TestResetScheduledMessagesToolUsesScheduleSubject(t *testing.T) {
	reset := false
	tool := resetScheduledMessagesTool(func() error {
		reset = true

		return nil
	})

	assert.Equal(t, resetScheduledMessagesToolName, tool.Name)
	assert.Equal(t, []string{scheduleMessageToolName}, tool.VisibilitySubjects)
	subjects, err := tool.Subjects(nil)
	require.NoError(t, err)
	assert.Equal(t, []string{scheduleMessageToolName}, subjects)

	result, err := tool.Call(context.Background(), nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "scheduled messages reset", result.Output)
	assert.True(t, reset)

	tool = resetScheduledMessagesTool(func() error { return assert.AnError })
	_, err = tool.Call(context.Background(), nil, nil)
	require.ErrorIs(t, err, assert.AnError)
}

func TestAttachFilesToolReadsWorkspacePath(t *testing.T) {
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	require.NoError(t, root.Mkdir("reports", 0o755))

	data := bytes.Repeat([]byte("report body"), 400000)
	require.NoError(t, root.WriteFile("reports/latest.txt", data, 0o644))

	attachments := &outboundAttachmentCollector{key: "turn-1/attachments"}
	sessions := newTestSessionService(t)
	tool := attachments.Tool(root, sessions, "attachment-test")
	parameters := tool.Parameters
	properties := parameters["properties"].(map[string]any)
	attachmentsSchema := properties["attachments"].(map[string]any)
	items := attachmentsSchema["items"].(map[string]any)
	assert.ElementsMatch(t, []string{"path", "name", "mime_type", "content", "content_base64"}, items["required"])

	result, err := tool.Call(t.Context(), []byte(`{"attachments":[{"path":"reports/latest.txt","name":"","mime_type":"","content":"","content_base64":""}]}`), nil)
	require.NoError(t, err)
	require.NotEqual(t, "queued attachments for final response", result.Output, "successful tool replay must carry immutable attachment IDs")
	require.Less(t, len(result.Output), 100, "attachment bytes must not enter tool output")

	got := attachments.Attachments()
	require.Len(t, got, 1)
	require.Contains(t, result.Output, got[0].ID)

	recorded, found, err := sessions.LoadTurnStep(t.Context(), "attachment-test", "turn-1/attachments")
	require.NoError(t, err)
	require.True(t, found)

	var resumed []protocol.OutboundAttachment
	require.NoError(t, json.Unmarshal(recorded, &resumed))
	assert.Equal(t, got, resumed, "a resumed turn keeps attachments queued before a restart")
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
	stored, err = sessions.LoadAttachment(t.Context(), "attachment-test", attachments.Attachments()[1].ID, false)
	require.NoError(t, err)
	require.Empty(t, stored.Data)
}

func TestOutboundAttachmentSources(t *testing.T) {
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
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	tool := new(outboundAttachmentCollector).Tool(root, newTestSessionService(t), "attachment-test")
	_, err = tool.Call(t.Context(), []byte(`{`), nil)
	require.ErrorContains(t, err, "parse response attachments")

	raw := []byte(`{"attachments":[{"path":"missing.txt","name":"","mime_type":"","content":"","content_base64":""}]}`)
	_, err = tool.Call(t.Context(), raw, nil)
	require.ErrorContains(t, err, `read attachment "missing.txt"`)
}

func TestAttachmentFallbackAndImageAttachments(t *testing.T) {
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

	var group errgroup.Group
	group.Go(func() error { return handleTestInbound(t.Context(), bridge, &bridgeRequest{inbound: inbound}) })

	consumed := readRocketCodeOutbound(t, bus)
	require.Equal(t, "attachment-input", consumed.ConsumedID)
	require.Empty(t, consumed.ConsumedHeader, "no model prompt was generated")
	final := readRocketCodeOutbound(t, bus)
	require.Equal(t, unsupportedFileFallback, final.Text)
	final.MarkDelivered(nil)
	require.NoError(t, group.Wait())
}

func TestNormalizeInboundAttachmentsCentralizesModelPolicy(t *testing.T) {
	msg := &protocol.InboundMessage{Attachments: []protocol.InboundAttachment{
		{Name: "tiny.png", MIMEType: "application/octet-stream", Data: tinyPNG()},
		{Name: "not-image.png", MIMEType: "image/png", Data: []byte("not an image")},
		{Name: "empty.png", MIMEType: "image/png"},
	}}

	normalizeInboundAttachments(msg)

	require.Len(t, msg.Attachments, 1)
	assert.Equal(t, protocol.AttachmentPresenceImages, msg.AttachmentPresence)
	assert.Equal(t, "tiny.png", msg.Attachments[0].Name)
	assert.Equal(t, "image/png", msg.Attachments[0].MIMEType)
	assert.Equal(t, tinyPNG(), msg.Attachments[0].Data)
	assert.Equal(t, []string{
		"Skipped attachment not-image.png because text/plain is not supported.",
		"Skipped attachment empty.png because it was empty.",
	}, msg.AttachmentWarnings)
}

func TestModelAttachmentMIMETypePrecedence(t *testing.T) {
	assert.Equal(t, "text/plain", modelAttachmentMIMEType([]byte("plain text"), "image/png", "photo.png"))
	assert.Equal(t, "image/png", modelAttachmentMIMEType(nil, " Image/PNG; Charset=UTF-8 ", "photo.jpg"))
	assert.Equal(t, "image/jpeg", modelAttachmentMIMEType(nil, " ", "photo.jpg"))
	assert.Empty(t, modelAttachmentMIMEType(nil, " ", "photo"))
}

func TestNormalizeInboundAttachmentsRejectsAttachmentsTooLargeToReduce(t *testing.T) {
	msg := &protocol.InboundMessage{Attachments: []protocol.InboundAttachment{
		{Name: "large.png", MIMEType: "image/png", Data: append(tinyPNG(), make([]byte, maxInboundAttachmentResizeInput)...)},
	}}

	normalizeInboundAttachments(msg)

	assert.Empty(t, msg.Attachments)
	assert.Equal(t, protocol.AttachmentPresenceImages, msg.AttachmentPresence)
	assert.Equal(t, []string{"Skipped attachment large.png because it was too large to attempt size reduction."}, msg.AttachmentWarnings)
}

func TestReduceResizedImageWithinLimitReportsEncoderErrorsAndExhaustion(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))

	_, err := reduceResizedImageWithinLimit(img, 1000, 1, func(image.Image, int) ([]byte, int, error) {
		return nil, 0, assert.AnError
	})
	require.ErrorIs(t, err, assert.AnError)

	_, err = reduceResizedImageWithinLimit(img, 1000, 1, func(image.Image, int) ([]byte, int, error) {
		return nil, 1000, nil
	})
	require.ErrorIs(t, err, errInboundAttachmentReductionNotEnough)
}

func TestFitInboundImageWithinLimitLeavesSmallImageUnchanged(t *testing.T) {
	data := encodeAttachmentTestPNG(t, newAttachmentTestImage(1, 1), png.BestCompression)

	transformed, transformedMIMEType, changed, err := fitInboundImageWithinLimit(" Image/PNG; Charset=UTF-8 ", data, len(data))
	require.NoError(t, err)
	assert.Equal(t, data, transformed)
	assert.Equal(t, "image/png", transformedMIMEType)
	assert.False(t, changed)
}

func TestFitInboundImageWithinLimitUsesLosslessPNGFirst(t *testing.T) {
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
}

func TestFitInboundImageWithinLimitReportsDecodeFailure(t *testing.T) {
	transformed, transformedMIMEType, changed, err := fitInboundImageWithinLimit("image/webp", []byte("not an image"), 1)
	assert.Nil(t, transformed)
	assert.Empty(t, transformedMIMEType)
	assert.False(t, changed)
	assert.ErrorIs(t, err, errInboundAttachmentReductionFailed)
}

func TestFitInboundImageWithinLimitRejectsImpossibleTarget(t *testing.T) {
	transformed, transformedMIMEType, changed, err := fitInboundImageWithinLimit("image/png", []byte("x"), 0)
	assert.Nil(t, transformed)
	assert.Empty(t, transformedMIMEType)
	assert.False(t, changed)
	require.ErrorIs(t, err, errInboundAttachmentReductionNotEnough)

	transformed, changed, err = resizePNGWithinLimit([]byte("x"), 0)
	assert.Nil(t, transformed)
	assert.False(t, changed)
	assert.ErrorIs(t, err, errInboundAttachmentReductionNotEnough)
}

func TestFitInboundImageWithinLimitReportsPNGDecodeFailure(t *testing.T) {
	transformed, transformedMIMEType, changed, err := fitInboundImageWithinLimit("image/png", []byte("not a png"), 1)
	assert.Nil(t, transformed)
	assert.Empty(t, transformedMIMEType)
	assert.False(t, changed)
	assert.ErrorIs(t, err, errInboundAttachmentReductionFailed)
}

func TestResizePNGWithinLimitRejectsSinglePixelImageTooLargeForTarget(t *testing.T) {
	original := encodeAttachmentTestPNG(t, newAttachmentTestImage(1, 1), png.NoCompression)

	transformed, changed, err := resizePNGWithinLimit(original, 1)
	assert.Nil(t, transformed)
	assert.False(t, changed)
	assert.ErrorIs(t, err, errInboundAttachmentReductionNotEnough)
}

func TestNextImageResizeDimensionsStillShrinksWhenEstimateWouldGrow(t *testing.T) {
	nextWidth, nextHeight := nextImageResizeDimensions(2, 2, 100, 10000)

	assert.Equal(t, 1, nextWidth)
	assert.Equal(t, 1, nextHeight)
}

func TestInboundAttachmentReductionFailureReason(t *testing.T) {
	assert.Equal(t, "image reduction failed", inboundAttachmentReductionFailureReason(errInboundAttachmentReductionFailed, maxInboundAttachmentBytes))
	assert.Equal(t, "it still exceeded the remaining attachment budget after reduction", inboundAttachmentReductionFailureReason(errInboundAttachmentReductionNotEnough, 1))
	assert.Equal(t, "it still exceeded the per-file size limit after reduction", inboundAttachmentReductionFailureReason(errInboundAttachmentReductionNotEnough, maxInboundAttachmentBytes))
}

func TestNormalizeInboundAttachmentsRejectsTotalBudgetOverflow(t *testing.T) {
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

func TestCompactedOutputToReplayInputPreservesSupportedItems(t *testing.T) {
	items := []responses.ResponseOutputItemUnion{
		{
			Type: "message",
			ID:   "msg_1",
			Content: []responses.ResponseOutputMessageContentUnion{
				{Type: "output_text", Text: "hello "},
				{Type: "refusal", Refusal: "no"},
				{Type: "output_text", Text: "world"},
			},
			Phase: responses.ResponseOutputMessagePhase("final_answer"),
		},
		{
			Type:    "message",
			ID:      "msg_2",
			Role:    "assistant",
			Content: []responses.ResponseOutputMessageContentUnion{{Type: "output_text", Text: "assistant"}},
		},
		{Type: "compaction", ID: "cmp_1", EncryptedContent: "sealed"},
		{
			Type: "compaction_summary", ID: "cmp_2", EncryptedContent: "chatgpt-sealed",
			Content: []responses.ResponseOutputMessageContentUnion{
				{Type: "output_text", Text: "content "},
				{Type: "refusal", Refusal: "ignored"},
				{Type: "output_text", Text: ""},
				{Type: "output_text", Text: "first\n"},
			},
			Summary: []responses.ResponseReasoningItemSummary{{Text: "summary "}, {Text: ""}, {Text: "last"}},
		},
		{Type: "reasoning", ID: "rsn_1", Summary: []responses.ResponseReasoningItemSummary{{Text: "summary"}}, EncryptedContent: "reasoning-sealed"},
		{Type: "reasoning", ID: "rsn_2"},
	}

	got, err := rocketcode.CompactedOutputToReplayInput(items)
	require.NoError(t, err)
	params, err := rocketcode.ReplayInputToParams(got)
	require.NoError(t, err)
	require.Len(t, params, len(items))

	assert.Equal(t, "hello world", params[0].OfMessage.Content.OfString.Value)
	assert.Equal(t, responses.EasyInputMessageRoleUser, params[0].OfMessage.Role)
	assert.Equal(t, responses.EasyInputMessagePhase("final_answer"), params[0].OfMessage.Phase)
	assert.Equal(t, "assistant", params[1].OfMessage.Content.OfString.Value)
	assert.Equal(t, "sealed", params[2].OfCompaction.EncryptedContent)
	assert.Equal(t, "chatgpt-sealed", params[3].OfCompaction.EncryptedContent)
	assert.JSONEq(t, `{"encrypted_content":"sealed","id":"cmp_1","type":"compaction"}`, string(got[2]))
	assert.JSONEq(t, `{"encrypted_content":"chatgpt-sealed","id":"cmp_2","type":"compaction","content":"content first\nsummary last","summary":"content first\nsummary last"}`, string(got[3]))
	assert.Equal(t, "summary", params[4].OfReasoning.Summary[0].Text)
	assert.Equal(t, "rsn_2", params[5].OfReasoning.ID)
}

func TestCompactedOutputToReplayInputRejectsUnsupportedKind(t *testing.T) {
	_, err := rocketcode.CompactedOutputToReplayInput([]responses.ResponseOutputItemUnion{{Type: "tool_search_call"}})
	require.ErrorContains(t, err, `unsupported compacted output item kind "tool_search_call"`)
}

func TestModelResolverConfiguresOpenAI(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", `---
description: Main
model: gpt-5.5
---
Prompt
`)
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	agents, skills, err := loadRocketCodeDefinitions(root, workspace, toolModePersistent)
	require.NoError(t, err)

	resolver := newModelResolver(&config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIKey: "test-key", RocketCodeAuth: "api_key"}}, slog.New(slog.DiscardHandler))
	client, origin, err := resolver.Resolve("gpt-5.5")
	require.NoError(t, err)
	require.NotNil(t, client)
	require.Equal(t, rocketcode.ProviderOrigin{Provider: "openai", Model: "gpt-5.5"}, origin)

	shellTempDir := filepath.Join(workspace, "shell-tmp")
	require.NoError(t, os.Mkdir(shellTempDir, 0o755))
	_, err = rocketcode.NewWithModelResolver(resolver, &rocketcode.Config{ShellTempDir: shellTempDir, ChildSessions: rocketcode.InertChildSessions{}, Journal: rocketcode.InertJournal{}, ShellCommand: rocketcode.DefaultShellCommand}, root, agents, skills, "main", io.Discard)
	require.NoError(t, err)
}

func TestReplayInputMessageRoleTextCoversMessageShapes(t *testing.T) {
	msg := protocol.NewInboundMessage(protocol.SourceWeb, protocol.InboundKindSteer, "[literal]\n\nuser text", true)
	msg.Metadata = map[string]string{protocol.InboundPrincipalMetadataKey: `alice "quoted" ]`}
	framed := buildPrompt(msg, nil)
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
	raw, err := rocketcode.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		{OfMessage: &responses.EasyInputMessageParam{Role: "user", Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(" ")}, Type: "message"}},
		{OfMessage: &responses.EasyInputMessageParam{Role: "assistant", Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("answer")}, Type: "message"}},
	})
	require.NoError(t, err)

	messages, err := replayInputMessages(raw)
	require.NoError(t, err)
	assert.Equal(t, []replayInputMessage{{role: "assistant", text: "answer"}}, messages)
}

func TestReplayInputMessagesReportsBadJSON(t *testing.T) {
	_, err := replayInputMessages([]json.RawMessage{json.RawMessage("{")})
	require.ErrorContains(t, err, "decode replay input messages")
}

func TestSeedReplayTextIncludesWebSearchContext(t *testing.T) {
	text := seedReplayText([]responses.ResponseInputItemUnionParam{{OfWebSearchCall: &responses.ResponseFunctionWebSearchParam{Action: responses.ResponseFunctionWebSearchActionUnionParam{OfSearch: &responses.ResponseFunctionWebSearchActionSearchParam{Queries: []string{"golang release"}}}, Status: "completed"}}})

	assert.Contains(t, text, "web search completed")
	assert.Contains(t, text, "golang release")
}

func TestReplayInputRawKindReportsInvalidJSON(t *testing.T) {
	assert.Empty(t, replayInputRawKind(json.RawMessage("{")))
}

func TestBuildPromptCoversAttachments(t *testing.T) {
	for _, tc := range []struct {
		source protocol.Source
		origin string
	}{
		{protocol.SourceSlack, "Slack"},
		{protocol.SourceWeb, "Web"},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			assert.Equal(t, "["+tc.origin+" principal=\"Alice\" additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\nhello\n\nAttachment notes:\n- skipped image", buildPrompt(&protocol.InboundMessage{Source: tc.source, Human: true, Text: "  hello  ", AttachmentWarnings: []string{" skipped image ", " "}, Metadata: map[string]string{protocol.InboundPrincipalMetadataKey: "Alice"}}, nil))
		})
	}

	assert.Equal(t, "[System additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\nAttachment notes:\n- unsupported PDF", buildPrompt(&protocol.InboundMessage{AttachmentWarnings: []string{" unsupported PDF "}}, nil))
}

func TestBuildPromptAdditionalInstructionsFrontmatter(t *testing.T) {
	msg := &protocol.InboundMessage{Source: protocol.SourceSlack, Human: true, Text: "hello", Metadata: map[string]string{protocol.InboundPrincipalMetadataKey: "Alice"}}

	assert.Equal(t, "[Slack principal=\"Alice\" additional_instructions=\"Reply in one sentence.\"]\n\nhello", buildPrompt(msg, map[string]any{"additionalInstructions": "Reply in one sentence."}))
	assert.Equal(t, "[Slack principal=\"Alice\" additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\nhello", buildPrompt(msg, map[string]any{"additionalInstructions": " "}))
	assert.Equal(t, "[Slack principal=\"Alice\" additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\nhello", buildPrompt(msg, map[string]any{"additionalInstructions": 7}))
	assert.Equal(t, "[System additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\n task \n", buildPrompt(&protocol.InboundMessage{Source: protocol.SourceSystem, PreserveWhitespace: true, Text: " task \n", Metadata: map[string]string{protocol.InboundOriginMetadataKey: "System", protocol.InboundMediaMetadataKey: "Text"}}, nil))
}

func TestParseDirectSkillTrigger(t *testing.T) {
	for _, text := range []string{"$docs-helper write docs", "$skill docs-helper write docs"} {
		directSkill := parseDirectSkillTrigger(text)

		require.NotNil(t, directSkill)
		assert.Equal(t, &rocketcode.PromptInputDirectSkill{Name: "docs-helper", Arguments: "write docs"}, directSkill)
	}

	directSkill := parseDirectSkillTrigger("$skill   ")
	require.NotNil(t, directSkill)
	assert.Empty(t, directSkill.Name)

	assert.Nil(t, parseDirectSkillTrigger("hello $docs-helper"))

	for _, name := range []string{"", "agent", "cron", "workflow", "stop", "enqueue", "queue", "goal"} {
		assert.Nil(t, parseDirectSkillTrigger("$"+name))
		assert.Equal(t, &rocketcode.PromptInputDirectSkill{Name: name, Arguments: ""}, parseDirectSkillTrigger("$skill "+name))
	}

	assert.Equal(t, &rocketcode.PromptInputDirectSkill{Name: "Docs", Arguments: "\"API guide\"  Next\tLast "}, parseDirectSkillTrigger("  $Docs \"API guide\"  Next\tLast "))

	for _, source := range []protocol.Source{protocol.SourceSlack, protocol.SourceWeb, protocol.SourceSystem, protocol.SourceExternalMCP} {
		for _, kind := range []protocol.InboundKind{protocol.InboundKindPrompt, protocol.InboundKindSteer, protocol.InboundKindEnqueue, protocol.InboundKindCancel} {
			for _, text := range []string{"$docs-helper typed", ""} {
				msg := protocol.NewInboundMessageFromContent(source, kind, &protocol.InboundContent{Text: text, TextAttachments: []string{"$docs-helper attachment"}}, true)
				if text != "" && (source == protocol.SourceSlack || source == protocol.SourceWeb) && kind != protocol.InboundKindCancel {
					assert.Equal(t, &rocketcode.PromptInputDirectSkill{Name: "docs-helper", Arguments: "typed"}, inboundDirectSkill(msg))
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

func TestBridgeQueuesWebWorkflowInsteadOfSteering(t *testing.T) {
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
}

func TestProvenanceHeaderSanitizesAmbiguousTokens(t *testing.T) {
	assert.Equal(t, "[ExternalMCP principal=\"Alice [ops]=lead\" additional_instructions=\"line \\\"one\\\"\\nnext\"]", provenanceHeader(promptProvenance{origin: "ExternalMCP", media: "Text", principal: " Alice [ops]=lead ", additionalInstructions: "line \"one\"\nnext"}))
	assert.Equal(t, `[Slack principal="a\"b\\c"]`, provenanceHeader(promptProvenance{origin: "Slack", media: "Text", principal: "a\"b\\c"}))
	assert.Equal(t, "[Slack]", provenanceHeader(promptProvenance{origin: "Slack", media: "Text", principal: "   "}))
	assert.Equal(t, "[External_(MCP)-x media=Voice_(note)-clip]", provenanceHeader(promptProvenance{origin: "External [MCP]=x", media: "Voice [note]=clip"}))
	assert.Equal(t, promptProvenance{origin: "System", media: "Text"}, provenanceFromInbound(&protocol.InboundMessage{Source: protocol.SourceSystem, Metadata: map[string]string{protocol.InboundOriginMetadataKey: "Mallory", protocol.InboundMediaMetadataKey: "Dance"}}))
}

func TestBridgeScheduleMessageSubmitsAfterDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: protocol.SlackThreadConversationID("D123", "111.222"), SessionService: newTestSessionService(t)}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
		require.NoError(t, bridge.ScheduleMessage(5*time.Second, "later", false))
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

func TestBridgeScheduleMessagePersistsRecurringMetadata(t *testing.T) {
	service := newTestSessionService(t)
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", SessionService: service}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}

	require.NoError(t, bridge.ScheduleMessage(time.Minute, "again", true))

	messages, err := service.ScheduledMessages()
	require.NoError(t, err)
	require.Len(t, messages, 1)

	for _, scheduled := range messages {
		assert.True(t, scheduled.Recurring)
		assert.Equal(t, time.Minute, scheduled.Interval)
		assert.Equal(t, "again", scheduled.Message)
	}
}

func TestBridgeScheduleMessageLogsPersistFailure(t *testing.T) {
	store := newTestSessionService(t)
	require.NoError(t, store.Stop())

	var logs bytes.Buffer

	bridge := &Bridge{log: slog.New(slog.NewJSONHandler(&logs, nil)), config: Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", SessionService: store}}

	require.Error(t, bridge.ScheduleMessage(time.Minute, "later", false))
	assert.Contains(t, logs.String(), "scheduled message persist failed")
}

func TestBridgeScheduleMessageSubmitsExternalMCPInPersistedSlackThread(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		threadKey := protocol.SlackThreadConversationID("D123", "111.222")
		privateConversationID := "external_mcp:planner:private"

		require.NoError(t, service.UpsertThread(threadKey, ThreadState{Agent: "planner"}))
		require.NoError(t, service.UpsertExternalMCPSession("public-1", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: privateConversationID, ManagedConversationID: threadKey, SlackChannel: "ops"}))

		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: privateConversationID, Agent: "planner", ManagedConversationID: threadKey, ExternalConversationID: "public-1", SessionService: service}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
		require.NoError(t, bridge.ScheduleMessage(5*time.Second, "later", false))

		time.Sleep(5 * time.Second)
		synctest.Wait()

		select {
		case request := <-bridge.requestCh:
			require.NotNil(t, request.inbound)
			assert.Equal(t, "later", request.inbound.Text)
			assert.Equal(t, privateConversationID, request.inbound.ConversationID)
			assert.Nil(t, request.inbound.SlackReply)
		case <-time.After(time.Nanosecond):
			t.Fatal("scheduled external MCP message was not submitted")
		}
	})
}

func TestBridgeInterruptCancelsTurnWaitingForPairedSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		pairID, privateID := protocol.SlackThreadConversationID("C123", "111.222"), "external_mcp:private"
		service.reserveTurnPair(pairID, privateID)

		bridge := &Bridge{runtime: &config.Config{}, config: Config{ConversationID: pairID, Agent: "main", ManagedConversationID: pairID, SessionService: service}, bus: discardPublisher{}, log: slog.New(slog.DiscardHandler)}
		require.NoError(t, startTestBridge(t.Context(), bridge))
		t.Cleanup(func() { require.NoError(t, bridge.Stop()) })

		inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "wait", true)
		result := inbound.EnableResponseWait()
		require.NoError(t, bridge.Submit(t.Context(), inbound))
		synctest.Wait()

		assert.Same(t, inbound, bridge.InterruptActiveTurn())
		service.completeTurnPairReservation(pairID, privateID)
		synctest.Wait()

		response := <-result
		require.ErrorIs(t, response.Err, context.Canceled)
		assert.Nil(t, bridge.InterruptActiveTurn())
	})
}

func TestBridgeSuccessfulManagedWorkflowReleasesPairedTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		workspace := t.TempDir()
		writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\npermission:\n  rocketclaw:\n    rocketclaw_set_tag: [[customer, internal]]\n---\nMain prompt\n")

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
				writeRawRunFunctionCall(t, w, "set-tag", setTagToolName, json.RawMessage(`{"tag":"customer"}`))
				return
			}

			var outputs []string

			for _, item := range body.Input {
				if item.Type == "function_call_output" {
					outputs = append(outputs, item.Output)
				}
			}

			assert.Equal(t, []string{`{"tags":["customer"]}`}, outputs)

			_, err := w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"checking workflow","annotations":[]}]},{"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"finished","annotations":[]}]}]}`))
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
		pairID, privateID := protocol.SlackThreadConversationID("C123", "111.222"), "external_mcp:private"
		require.NoError(t, service.UpsertExternalMCPSession("public-1", &ExternalMCPSessionState{PrivateConversationID: privateID, ManagedConversationID: pairID}))

		bus := newTestBus()
		t.Cleanup(bus.Close)
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, bus: bus, config: Config{ConversationID: pairID, ManagedConversationID: pairID, Agent: "main", SessionService: service}}
		require.NoError(t, startTestBridge(t.Context(), bridge))
		t.Cleanup(func() { require.NoError(t, bridge.Stop()) })

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
					assert.Empty(t, outbound.WorkflowTerminal)
				}

				outbound.MarkDelivered(nil)

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

		privateAcquired := false

		var unlockPrivate func()
		go func() {
			unlockPrivate, err = service.lockTurnPair(t.Context(), pairID, privateID)
			privateAcquired = err == nil
		}()

		synctest.Wait()

		require.True(t, privateAcquired, "private turn remained blocked after managed workflow completed")
		unlockPrivate()
	})
}

func TestBridgeFailedManagedWorkflowPersistsRunSummary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		workspace := t.TempDir()
		writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\n---\nMain prompt\n")
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
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), runtime: &config.Config{Workspace: workspace}, bus: bus, config: Config{ConversationID: conversationID, Agent: "main", SessionService: service}}
		require.NoError(t, startTestBridge(t.Context(), bridge))
		t.Cleanup(func() { require.NoError(t, bridge.Stop()) })

		delivered := make(chan struct{})

		go func() {
			for outbound := range bus.Outbound(t.Context()) {
				outbound.MarkDelivered(nil)

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
	const privateError = "PRIVATE_WORKER_ERROR"

	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\n---\nMain prompt\n")
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
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, bus: bus, config: Config{ConversationID: conversationID, Agent: "main", SessionService: service}}
	require.NoError(t, startTestBridge(t.Context(), bridge))
	t.Cleanup(func() { require.NoError(t, bridge.Stop()) })

	delivered := make(chan struct{})

	go func() {
		for outbound := range bus.Outbound(t.Context()) {
			outbound.MarkDelivered(nil)

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
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\n---\nMain prompt\n")
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
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, bus: bus, config: Config{ConversationID: conversationID, Agent: "main", SessionService: service}}
	require.NoError(t, startTestBridge(t.Context(), bridge))
	t.Cleanup(func() { require.NoError(t, bridge.Stop()) })

	delivered := make(chan struct{})

	go func() {
		for outbound := range bus.Outbound(t.Context()) {
			outbound.MarkDelivered(nil)

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
}

func TestWorkflowRunSummaryIsVisibleWithoutIntermediateOutput(t *testing.T) {
	const intermediate = "PRIVATE_INTERMEDIATE_WORKFLOW_OUTPUT"

	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\n---\nMain prompt\n")
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
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, bus: bus, config: Config{ConversationID: conversationID, Agent: "main", SessionService: service}}
	require.NoError(t, startTestBridge(t.Context(), bridge))
	t.Cleanup(func() { require.NoError(t, bridge.Stop()) })

	completed := make(chan string, 2)

	go func() {
		for outbound := range bus.Outbound(t.Context()) {
			outbound.MarkDelivered(nil)

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

func TestBridgeInterruptPreservesWaitingWorkflowReservation(t *testing.T) {
	service := newTestSessionService(t)
	pairID := protocol.SlackThreadConversationID("C123", "111.222")
	service.reserveTurnPair(pairID, pairID)
	t.Cleanup(func() { service.completeTurnPairReservation(pairID, pairID) })

	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "$workflow audit", true)
	inbound.Workflow = protocol.WorkflowInvocation{Name: "audit"}

	bridge := &Bridge{requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{}), config: Config{ConversationID: pairID, ManagedConversationID: pairID, SessionService: service}}
	bridge.requestCh <- bridgeRequest{inbound: inbound}

	bridge.InterruptActiveTurn()

	assert.Equal(t, pairID, turnPairReservation(service, pairID))
	assert.Len(t, bridge.requestCh, 1)
}

func turnPairReservation(service *SessionService, pairID string) string {
	service.turnGatesMu.Lock()
	defer service.turnGatesMu.Unlock()

	if gate := service.turnGates[pairID]; gate != nil {
		return gate.reservedFor
	}

	return ""
}

func TestBridgePairLockFailureReleasesWorkflowReservation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		pairID := protocol.SlackThreadConversationID("C123", "111.222")
		service.reserveTurnPair(pairID, pairID)

		unlock, err := service.lockTurnPair(t.Context(), pairID, pairID)
		require.NoError(t, err)

		bridge := &Bridge{runtime: &config.Config{}, config: Config{ConversationID: pairID, ManagedConversationID: pairID, SessionService: service}, bus: discardPublisher{}, log: slog.New(slog.DiscardHandler)}
		require.NoError(t, startTestBridge(t.Context(), bridge))
		t.Cleanup(func() { require.NoError(t, bridge.Stop()) })

		inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "$workflow audit", true)
		inbound.Workflow = protocol.WorkflowInvocation{Name: "audit"}
		response := inbound.EnableResponseWait()
		require.NoError(t, bridge.Submit(t.Context(), inbound))
		synctest.Wait()
		assert.Same(t, inbound, bridge.InterruptActiveTurn())
		synctest.Wait()
		require.ErrorIs(t, (<-response).Err, context.Canceled)
		unlock()
		synctest.Wait()

		assert.Empty(t, turnPairReservation(service, pairID))
	})
}

func TestBridgeScheduleMessageUsesOwningSlackThread(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		conversationID := protocol.SlackThreadConversationID("D456", "222.333")

		require.NoError(t, service.UpsertThread(protocol.SlackThreadConversationID("D123", "111.222"), ThreadState{Agent: "planner"}))

		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, Agent: "planner", SessionService: service}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
		require.NoError(t, bridge.ScheduleMessage(5*time.Second, "later", false))

		time.Sleep(5 * time.Second)
		synctest.Wait()

		select {
		case request := <-bridge.requestCh:
			require.NotNil(t, request.inbound)
			assert.Equal(t, "later", request.inbound.Text)
			assert.Equal(t, conversationID, request.inbound.ConversationID)
			assert.Nil(t, request.inbound.SlackReply)
		case <-time.After(time.Nanosecond):
			t.Fatal("scheduled external MCP message was not submitted")
		}
	})
}

func TestBridgeDeletesScheduledMessageAfterSuccessfulHandling(t *testing.T) {
	workspace := t.TempDir()
	service := newTestSessionServiceAt(t, workspace)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, service.PutScheduledMessage("schedule-1", &protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "later", DueAt: time.Now().UTC()}))

	bus := newTestBus()
	defer bus.Close()

	bridge := NewConversation(&config.Config{Workspace: workspace}, bus, &Config{ConversationID: conversationID, Agent: "main", RequestRestart: testNoopRestart, StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.DiscardHandler))
	bridge.requestCh = make(chan bridgeRequest, 1)
	bridge.stopCh = make(chan struct{})

	go bridge.loop(t.Context())

	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "later", false)
	inbound.ConversationID = conversationID
	inbound.AttachmentPresence = protocol.AttachmentPresenceUnsupported
	responseCh := inbound.EnableResponseWait()
	require.NoError(t, bridge.enqueue(context.Background(), &bridgeRequest{inbound: inbound, scheduledMessageID: "schedule-1"}, "submit scheduled message"))

	outbound := readRocketCodeOutbound(t, bus)
	assert.Equal(t, unsupportedFileFallback, outbound.Text)
	outbound.MarkDelivered(nil)

	select {
	case response := <-responseCh:
		require.NoError(t, response.Err)
	case <-time.After(time.Second):
		t.Fatal("scheduled message response was not completed")
	}

	require.Eventually(t, func() bool {
		messages, err := service.ScheduledMessages()
		require.NoError(t, err)

		return len(messages) == 0
	}, time.Second, time.Millisecond)
}

func TestSubmitEnqueuedItemPreservesSource(t *testing.T) {
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

	for _, source := range []protocol.Source{protocol.SourceSlack, protocol.SourceWeb} {
		for _, text := range []string{"$docs-helper \"typed args\"  Next", "$skill stop \"typed args\"  Next", ""} {
			item := &protocol.ThreadQueueItem{ID: "raw-queue", ConversationID: bridge.config.ConversationID, Source: source, Message: text, Principal: "author", Content: protocol.InboundContent{Text: text, TextAttachments: []string{"$docs-helper attachment-only"}, AttachmentWarnings: []string{"warning"}}}
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
	}

	waiting := protocol.NewInboundMessageFromContent(protocol.SourceExternalMCP, protocol.InboundKindPrompt, &protocol.InboundContent{Text: "source text", Attachments: []protocol.InboundAttachment{{Name: "image.png", MIMEType: "image/png", Data: []byte("image")}}}, true)
	waiting.Metadata = map[string]string{"external_conversation_id": "external-1", protocol.InboundPrincipalMetadataKey: "U3"}
	item = &protocol.ThreadQueueItem{ID: "q3", ConversationID: bridge.config.ConversationID, Message: "queue display", Principal: "U3", Inbound: waiting}
	require.NoError(t, bridge.config.SessionService.PutThreadQueueItem(item.ID, item))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	bridge.log = slog.New(slog.DiscardHandler)
	activated := make(chan *protocol.InboundMessage, 1)
	errActivation := errors.New("stop at activation")
	bridge.config.EnqueueActivation = EnqueueActivation{Fn: func(_ context.Context, _ *protocol.ThreadQueueItem, inbound *protocol.InboundMessage) error {
		activated <- inbound

		// Stopping, unlike canceling ctx, keeps pickLaterWork from requeueing the restored item.
		assert.NoError(t, bridge.Stop())

		return errActivation
	}}
	response := waiting.EnableResponseWait()

	require.NoError(t, bridge.submitEnqueuedItem(ctx, item))

	var group errgroup.Group
	group.Go(func() error {
		bridge.loop(ctx)
		return nil
	})

	inbound := <-activated
	assert.Same(t, waiting, inbound)
	assert.Equal(t, protocol.InboundKindPrompt, inbound.Kind)
	assert.Equal(t, "source text", inbound.Text)
	assert.Equal(t, "external-1", inbound.Metadata["external_conversation_id"])
	assert.Equal(t, []byte("image"), inbound.Attachments[0].Data)
	require.ErrorIs(t, (<-response).Err, errActivation)
	require.NoError(t, group.Wait())
}

func TestBridgeDeletesEnqueueItemWhenTurnStarts(t *testing.T) {
	workspace := t.TempDir()
	service := newTestSessionServiceAt(t, workspace)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, service.PutThreadQueueItem("q1", &protocol.ThreadQueueItem{ID: "q1", ConversationID: conversationID, Message: "changelog", Principal: "U1", StashAt: time.Now().UTC(), Position: 0}))

	bus := newTestBus()
	bus.Close()

	bridge := NewConversation(&config.Config{Workspace: workspace}, bus, &Config{ConversationID: conversationID, Agent: "main", RequestRestart: testNoopRestart, StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.DiscardHandler))
	bridge.requestCh = make(chan bridgeRequest, 1)

	bridge.stopCh = make(chan struct{})
	go bridge.loop(t.Context())

	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "changelog", false)
	inbound.ConversationID = conversationID
	inbound.AttachmentPresence = protocol.AttachmentPresenceUnsupported
	responseCh := inbound.EnableResponseWait()
	require.NoError(t, bridge.enqueue(context.Background(), &bridgeRequest{inbound: inbound, queueItemID: "q1"}, "submit enqueued message"))

	select {
	case response := <-responseCh:
		require.Error(t, response.Err)
	case <-time.After(time.Second):
		t.Fatal("enqueued message response was not completed")
	}

	require.Eventually(t, func() bool {
		items, err := service.ThreadQueueForConversation(conversationID)
		require.NoError(t, err)

		return len(items) == 0
	}, time.Second, time.Millisecond)
}

func TestBridgeDeletesScheduledMessageWhenTurnStarts(t *testing.T) {
	workspace := t.TempDir()
	service := newTestSessionServiceAt(t, workspace)
	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, service.PutScheduledMessage("schedule-1", &protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "later", DueAt: time.Now().UTC()}))

	bus := newTestBus()
	bus.Close()

	bridge := NewConversation(&config.Config{Workspace: workspace}, bus, &Config{ConversationID: conversationID, Agent: "main", RequestRestart: testNoopRestart, StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.DiscardHandler))
	bridge.requestCh = make(chan bridgeRequest, 1)
	bridge.stopCh = make(chan struct{})

	go bridge.loop(t.Context())

	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "later", false)
	inbound.ConversationID = conversationID
	inbound.AttachmentPresence = protocol.AttachmentPresenceUnsupported
	responseCh := inbound.EnableResponseWait()
	require.NoError(t, bridge.enqueue(context.Background(), &bridgeRequest{inbound: inbound, scheduledMessageID: "schedule-1"}, "submit scheduled message"))

	select {
	case response := <-responseCh:
		require.Error(t, response.Err)
	case <-time.After(time.Second):
		t.Fatal("scheduled message response was not completed")
	}

	require.Eventually(t, func() bool {
		messages, err := service.ScheduledMessages()
		require.NoError(t, err)

		return len(messages) == 0
	}, time.Second, time.Millisecond)
}

func TestBridgeKeepsRecurringScheduledMessageAfterSuccessfulHandling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		workspace := t.TempDir()
		service := newTestSessionServiceAt(t, workspace)
		conversationID := protocol.SlackThreadConversationID("C123", "111.222")
		require.NoError(t, service.PutScheduledMessage("schedule-1", &protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "later", DueAt: time.Now().UTC(), Recurring: true, Interval: time.Minute}))

		bus := newTestBus()
		defer bus.Close()

		bridge := NewConversation(&config.Config{Workspace: workspace}, bus, &Config{ConversationID: conversationID, Agent: "main", RequestRestart: testNoopRestart, StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.DiscardHandler))
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
		outbound.MarkDelivered(nil)

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
	synctest.Test(t, func(t *testing.T) {
		var logs lockedBuffer

		bridge := &Bridge{log: slog.New(slog.NewJSONHandler(&logs, nil)), config: Config{ConversationID: "slack-thread:C123:111.222", SessionService: newTestSessionService(t)}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
		require.NoError(t, bridge.ScheduleMessage(5*time.Second, "later", false))
		require.NoError(t, bridge.Stop())

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
	synctest.Test(t, func(t *testing.T) {
		workspace := t.TempDir()
		store, err := NewSessionService(workspace)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Stop()) })

		var logs lockedBuffer

		logger := slog.New(slog.NewJSONHandler(&logs, nil))
		conversationID := protocol.SlackThreadConversationID("C123", "111.222")
		bridge := NewConversation(&config.Config{Workspace: workspace}, nil, &Config{ConversationID: conversationID, Agent: "main", StartNewThread: testNoopStartNewThread, SessionService: store}, logger)
		bridge.requestCh = make(chan bridgeRequest, 1)
		bridge.stopCh = make(chan struct{})

		require.NoError(t, bridge.ScheduleMessage(5*time.Second, "later", false))
		require.NoError(t, store.PutScheduledMessage("other", &protocol.ScheduledMessageState{ConversationID: "other", Agent: "main", Message: "keep", DueAt: time.Now().UTC().Add(time.Hour)}))

		require.NoError(t, bridge.ResetScheduledMessages())
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

func TestBridgeResetScheduledMessagesReportsStoreError(t *testing.T) {
	store := newTestSessionService(t)
	require.NoError(t, store.Stop())

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: "slack-thread:C123:111.222", SessionService: store}}
	require.Error(t, bridge.ResetScheduledMessages())
}

func TestBridgeArmsOverdueScheduledMessage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		conversationID := protocol.SlackThreadConversationID("C123", "111.222")
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: service}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
		due := protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "now", DueAt: time.Now().UTC().Add(-time.Second)}

		require.NoError(t, service.PutScheduledMessage("due", &due))
		bridge.armScheduledMessage("due", &due)
		synctest.Wait()

		select {
		case request := <-bridge.requestCh:
			require.NotNil(t, request.inbound)
			assert.NotEmpty(t, request.scheduledMessageID)
			assert.Equal(t, "now", request.inbound.Text)
		case <-time.After(time.Nanosecond):
			t.Fatal("overdue scheduled message was not submitted")
		}
	})
}

func TestBridgeRecurringScheduledMessageAdvancesAndRearms(t *testing.T) {
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

func TestBridgeStaleScheduledMessageTimerDoesNotSubmit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		conversationID := protocol.SlackThreadConversationID("C123", "111.222")
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: service}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
		oldDue := protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "old", DueAt: time.Now().UTC().Add(5 * time.Second)}
		newDue := oldDue
		newDue.DueAt = newDue.DueAt.Add(time.Minute)

		require.NoError(t, service.PutScheduledMessage("stale", &newDue))
		bridge.armScheduledMessage("stale", &oldDue)

		time.Sleep(5 * time.Second)
		synctest.Wait()

		select {
		case <-bridge.requestCh:
			t.Fatal("stale scheduled message was submitted")
		default:
		}
	})
}

func TestBridgeScheduledMessageTimerStopsOnStoreError(t *testing.T) {
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

func TestBridgeRestoresScheduledMessageAfterRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		workspace := t.TempDir()
		store, err := NewSessionService(workspace)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Stop()) })

		conversationID := protocol.SlackThreadConversationID("C123", "111.222")

		first := NewConversation(&config.Config{Workspace: workspace}, nil, &Config{ConversationID: conversationID, Agent: "main", StartNewThread: testNoopStartNewThread, SessionService: store}, slog.New(slog.DiscardHandler))
		require.NoError(t, startTestBridge(t.Context(), first))
		require.NoError(t, first.ScheduleMessage(5*time.Second, "later", false))
		require.NoError(t, first.Stop())

		var logs bytes.Buffer

		second := NewConversation(&config.Config{Workspace: workspace}, nil, &Config{ConversationID: conversationID, Agent: "main", StartNewThread: testNoopStartNewThread, SessionService: store}, slog.New(slog.NewJSONHandler(&logs, nil)))
		second.requestCh = make(chan bridgeRequest, 1)
		second.stopCh = make(chan struct{})
		messages, err := store.ScheduledMessages()
		require.NoError(t, err)

		for id, message := range messages {
			second.armScheduledMessage(id, &message)
		}

		synctest.Wait()

		select {
		case <-second.requestCh:
			t.Fatal("scheduled message submitted before restored delay")
		default:
		}

		time.Sleep(5 * time.Second)
		synctest.Wait()

		select {
		case request := <-second.requestCh:
			require.NotNil(t, request.inbound)
			assert.NotEmpty(t, request.scheduledMessageID)
			assert.Equal(t, "later", request.inbound.Text)
		case <-time.After(time.Nanosecond):
			t.Fatal("restored scheduled message was not submitted")
		}

		messages, err = store.ScheduledMessages()
		require.NoError(t, err)
		require.Len(t, messages, 1)
		assert.Contains(t, logs.String(), "scheduled message enqueued")
	})
}

func TestBridgeStartLogsRestoredScheduledMessage(t *testing.T) {
	workspace := t.TempDir()
	store, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Stop()) })

	conversationID := protocol.SlackThreadConversationID("C123", "111.222")

	require.NoError(t, store.PutScheduledMessage("schedule-1", &protocol.ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "later", DueAt: time.Now().UTC().Add(time.Hour)}))

	var logs bytes.Buffer

	bus := newTestBus()
	t.Cleanup(bus.Close)
	bridge := NewConversation(&config.Config{Workspace: workspace}, bus, &Config{ConversationID: conversationID, Agent: "main", StartNewThread: testNoopStartNewThread, SessionService: store}, slog.New(slog.NewJSONHandler(&logs, nil)))
	require.NoError(t, startTestBridge(t.Context(), bridge))
	t.Cleanup(func() { require.NoError(t, bridge.Stop()) })

	assert.Contains(t, logs.String(), "scheduled message restored")
}

func TestOpenAIClientLogsProviderRequestsOnError(t *testing.T) {
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
	client, _, err := resolver.Resolve("gpt-5.5")
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
	client, _, err = resolver.Resolve("gpt-5.5")
	require.NoError(t, err)

	_, _ = client.Responses.New(context.Background(), params)

	assert.Contains(t, logs.String(), "provider request completed")
	assert.Contains(t, logs.String(), `"status":200`)
	assert.Contains(t, logs.String(), `"outcome":"success"`)
}

func TestAskUserQuestionToolAllowsEmptyOptions(t *testing.T) {
	tool := askUserQuestionTool(protocol.InteractiveUserQuestionAsker(func(_ context.Context, req *protocol.AskUserQuestionRequest) (protocol.AskUserQuestionAnswer, error) {
		assert.Equal(t, "Approve?", req.Question)
		assert.Empty(t, req.Options)

		return protocol.AskUserQuestionAnswer{Custom: "approved", Source: protocol.SourceSlack}, nil
	}), &protocol.InboundMessage{Source: protocol.SourceSlack, Human: true, SlackReply: &protocol.SlackReplyTarget{ChannelID: "C1", MessageTS: "1", ThreadTS: "1"}})

	result, err := tool.Call(t.Context(), []byte(`{"question":"Approve?","details":"","options":[],"multiple":false}`), nil)

	require.NoError(t, err)
	assert.JSONEq(t, `{"selected":null,"custom":"approved","source":"slack"}`, result.Output)
}

func TestAskUserQuestionToolDescriptionRejectsCatchAllOptions(t *testing.T) {
	tool := askUserQuestionTool(protocol.InteractiveUserQuestionAsker(func(context.Context, *protocol.AskUserQuestionRequest) (protocol.AskUserQuestionAnswer, error) {
		return protocol.AskUserQuestionAnswer{}, nil
	}), &protocol.InboundMessage{Source: protocol.SourceSlack, Human: true, SlackReply: &protocol.SlackReplyTarget{ChannelID: "C1", MessageTS: "1", ThreadTS: "1"}})

	assert.Contains(t, tool.Description, "only for concrete predefined choices")
	assert.Contains(t, tool.Description, "do not include catch-all choices")
}

func TestAskUserQuestionToolFiltersRedundantCustomOptions(t *testing.T) {
	tool := askUserQuestionTool(protocol.InteractiveUserQuestionAsker(func(_ context.Context, req *protocol.AskUserQuestionRequest) (protocol.AskUserQuestionAnswer, error) {
		require.Len(t, req.Options, 1)
		assert.Equal(t, "High priority", req.Options[0].Label)

		return protocol.AskUserQuestionAnswer{Selected: []string{"high"}, Source: protocol.SourceSlack}, nil
	}), &protocol.InboundMessage{Source: protocol.SourceSlack, Human: true, SlackReply: &protocol.SlackReplyTarget{ChannelID: "C1", MessageTS: "1", ThreadTS: "1"}})

	result, err := tool.Call(t.Context(), []byte(`{"question":"Approve?","details":"","options":[{"label":"Custom answer","value":"custom_answer","description":"free text"},{"label":"High priority","value":"high","description":"Prioritize now"}],"multiple":false}`), nil)

	require.NoError(t, err)
	assert.JSONEq(t, `{"selected":["high"],"custom":"","source":"slack"}`, result.Output)
}

func TestStartNewThreadNativeTurnGate(t *testing.T) {
	assert.True(t, startNewThreadNativeTurn(&protocol.InboundMessage{Source: protocol.SourceSlack, Human: true, SlackReply: &protocol.SlackReplyTarget{ChannelID: "C1", MessageTS: "1"}}))
	assert.False(t, startNewThreadNativeTurn(&protocol.InboundMessage{Source: protocol.SourceSlack, Human: true, SlackReply: &protocol.SlackReplyTarget{ChannelID: "C1", MessageTS: "1"}, Metadata: map[string]string{protocol.InboundStartNewThreadDisabledMetadataKey: "true"}}))
	assert.False(t, startNewThreadNativeTurn(&protocol.InboundMessage{Source: protocol.SourceExternalMCP, Human: true}))
	assert.False(t, startNewThreadNativeTurn(&protocol.InboundMessage{Source: protocol.SourceSlack, Human: false, SlackReply: &protocol.SlackReplyTarget{ChannelID: "C1", MessageTS: "1"}}))
}

func TestNativeQuestionTurnGate(t *testing.T) {
	assert.True(t, nativeQuestionTurn(&protocol.InboundMessage{Source: protocol.SourceSlack, Human: true, SlackReply: &protocol.SlackReplyTarget{ChannelID: "C1", MessageTS: "1"}}))
	assert.False(t, nativeQuestionTurn(&protocol.InboundMessage{Source: protocol.SourceSlack, Human: true}))
	assert.False(t, nativeQuestionTurn(&protocol.InboundMessage{Source: protocol.SourceSlack, Human: false, SlackReply: &protocol.SlackReplyTarget{ChannelID: "C1", MessageTS: "1"}}))
	assert.False(t, nativeQuestionTurn(&protocol.InboundMessage{Source: protocol.SourceExternalMCP, Human: true, SlackReply: &protocol.SlackReplyTarget{ChannelID: "C1", MessageTS: "1"}}))
	assert.False(t, nativeQuestionTurn(&protocol.InboundMessage{Source: protocol.SourceSystem, Human: true, SlackReply: &protocol.SlackReplyTarget{ChannelID: "C1", MessageTS: "1"}}))
}

func TestAskUserQuestionToolOmitsResponseChannel(t *testing.T) {
	var got *protocol.AskUserQuestionRequest

	tool := askUserQuestionTool(protocol.InteractiveUserQuestionAsker(func(_ context.Context, req *protocol.AskUserQuestionRequest) (protocol.AskUserQuestionAnswer, error) {
		got = req

		return protocol.AskUserQuestionAnswer{Custom: "ok", Source: protocol.SourceSlack}, nil
	}), &protocol.InboundMessage{
		Source:         protocol.SourceSlack,
		Human:          true,
		ConversationID: "slack-thread:C1:1",
		SlackReply:     &protocol.SlackReplyTarget{ChannelID: "C1", MessageTS: "1", ThreadTS: "1"},
	})

	_, err := tool.Call(t.Context(), []byte(`{"question":"Q?","details":"","options":[],"multiple":false}`), nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, protocol.SourceSlack, got.Source)
	assert.Equal(t, "slack-thread:C1:1", got.ConversationID)
	assert.Equal(t, &protocol.SlackReplyTarget{ChannelID: "C1", MessageTS: "1", ThreadTS: "1"}, got.SlackReply)
}

func TestAgentExplicitlyAllowsRocketClawToolRequiresAllow(t *testing.T) {
	var allowed, auto, denied, missing rocketcode.PermissionSet
	require.NoError(t, allowed.Set("rocketclaw", restartToolName, rocketcode.PermissionAllow))
	require.NoError(t, auto.Set("rocketclaw", restartToolName, rocketcode.PermissionAuto))
	require.NoError(t, denied.Set("rocketclaw", restartToolName, rocketcode.PermissionDeny))

	assert.True(t, agentExplicitlyAllowsRocketClawTool(&rocketcode.Agent{Permission: allowed}, restartToolName))
	assert.False(t, agentExplicitlyAllowsRocketClawTool(&rocketcode.Agent{Permission: auto}, restartToolName))
	assert.False(t, agentExplicitlyAllowsRocketClawTool(&rocketcode.Agent{Permission: denied}, restartToolName))
	assert.False(t, agentExplicitlyAllowsRocketClawTool(&rocketcode.Agent{Permission: missing}, restartToolName))
}

func TestStartNewThreadToolPreservesLiteralPrompt(t *testing.T) {
	tool := startNewThreadTool(func(_ context.Context, req *protocol.StartNewThreadRequest) (protocol.StartNewThreadResult, error) {
		assert.Equal(t, " literal $(date) ", req.Prompt)
		assert.Equal(t, "Child", req.Title)

		return protocol.StartNewThreadResult{ConversationID: "slack-thread:C1:2"}, nil
	}, &protocol.InboundMessage{Source: protocol.SourceSlack, Human: true, ConversationID: "slack-thread:C1:1", SlackReply: &protocol.SlackReplyTarget{ChannelID: "C1", MessageTS: "1", ThreadTS: "1"}}, "main")

	result, err := tool.Call(t.Context(), []byte(`{"title":" Child ","prompt":" literal $(date) "}`), nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"conversation_id":"slack-thread:C1:2"}`, result.Output)
}

func TestRunTurnSendsExternalMCPMetadataAsDeveloperMessage(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "planner", "---\ndescription: Planner\nmode: primary\nmodel: gpt-5.5\npermission:\n  read: allow\n  bash: auto\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	var (
		requestBody struct {
			Model string `json:"model"`
			Input []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
				Output  any    `json:"output"`
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
					messages = append(messages, item.Content)
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

		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}`))
	}))
	t.Cleanup(server.Close)

	bridge := new(Bridge)
	bridge.runtime = &config.Config{Workspace: workspace, AutoApproverModel: "gpt-5.4-mini", OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}
	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })

	managedConversationID := protocol.SlackThreadConversationID("C123", "111.222")
	require.NoError(t, service.RegisterExternalMCPConversation("public-1", "planner", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: "external_mcp:planner:private", ManagedConversationID: managedConversationID, SlackChannel: "#ops", OriginPairs: map[string]string{"z": "last", "a": "first"}}))
	bridge.config = Config{ConversationID: "external_mcp:planner:private", Agent: "planner", ManagedConversationID: managedConversationID, ExternalConversationID: "public-1", SessionService: service}
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
	assert.Equal(t, "This external MCP thread has metadata:\nROCKETCLAW_CONVERSATION_ID=\"external_mcp:planner:private\"\nROCKETCLAW_METADATA_A=\"first\"\nROCKETCLAW_METADATA_Z=\"last\"", requestBody.Input[0].Content)
	assert.Equal(t, "user", requestBody.Input[1].Role)

	msg.Metadata = map[string]string{"a": "ignored", "later-key": "fresh"}
	_, err = runTestTurn(context.Background(), bridge, msg, "turn-2")
	require.NoError(t, err)

	developerMessages := []string{}

	for i := range requestBody.Input {
		if requestBody.Input[i].Role == "developer" {
			developerMessages = append(developerMessages, requestBody.Input[i].Content)
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
		assert.NotContains(t, requestBody.Input[i].Content, "LATER_KEY")
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

	managedEntries, err := service.ObserveEntries(context.Background(), managedConversationID)
	require.NoError(t, err)
	require.Len(t, managedEntries, 4)
	assert.Equal(t, externalMCPMetadataEntryType, managedEntries[0].Entry.Type)
	assert.Contains(t, string(managedEntries[2].Entry.ReplayInput[0]), "ROCKETCLAW_METADATA_LATER_KEY")

	for i := range entries {
		messages, err := replayInputMessages(entries[i].Entry.ReplayInput)
		require.NoError(t, err)

		for _, message := range messages {
			assert.NotContains(t, message.text, "ROCKETCLAW_METADATA_LATER_KEY")
		}
	}

	// Web-created bridges know only the actual conversation ID, not pairing fields.
	writeAgent(t, workspace, "planner", "---\ndescription: Planner\nmode: primary\nmodel: gpt-5.5\npermission:\n  edit:\n    '.tmp/${ROCKETCLAW_METADATA_A}/note.txt': allow\n  read: allow\n  bash: auto\n---\nPrompt\n")

	managedBridge := &Bridge{runtime: bridge.runtime, config: Config{ConversationID: managedConversationID, Agent: "planner", SessionService: service}, bus: discardPublisher{}, log: slog.New(slog.DiscardHandler)}
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
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission:\n  skill:\n    docs-helper: allow\n---\nPrompt\n")
	writeAgent(t, workspace, "denied", "---\ndescription: Denied\nmode: primary\nmodel: gpt-5.5\npermission:\n  skill:\n    docs-helper: deny\n---\nPrompt\n")
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
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}`))
	}))
	t.Cleanup(server.Close)

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })

	for _, source := range []protocol.Source{protocol.SourceSlack, protocol.SourceWeb} {
		for _, kind := range []protocol.InboundKind{protocol.InboundKindPrompt, protocol.InboundKindSteer, protocol.InboundKindEnqueue} {
			for _, invocation := range []string{"$docs-helper write API docs", "$skill docs-helper write API docs"} {
				conversationID := fmt.Sprintf("dollar-%s-%s-%s", source, kind, invocation)
				bridge := &Bridge{runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, config: Config{ConversationID: conversationID, Agent: "main", SessionService: service}, bus: discardPublisher{}, log: slog.New(slog.DiscardHandler)}
				msg := protocol.NewInboundMessageFromContent(source, kind, &protocol.InboundContent{Text: invocation, TextAttachments: []string{"attachment-only argument"}}, true)
				msg.ConversationID = conversationID
				msg.Metadata[protocol.InboundPrincipalMetadataKey] = "Alice"

				require.NoError(t, root.WriteFile("state", []byte("old"), 0o600))
				require.NoError(t, root.WriteFile("calls", nil, 0o600))

				promote := kind == protocol.InboundKindEnqueue && strings.HasPrefix(invocation, "$skill ")
				if kind == protocol.InboundKindEnqueue {
					item := protocol.ThreadQueueItem{ID: "queued", ConversationID: conversationID, Source: source, Message: invocation, Principal: "Alice", Content: protocol.InboundContent{Text: invocation, TextAttachments: []string{"attachment-only argument"}}}
					require.NoError(t, service.UpsertThread(conversationID, ThreadState{Agent: "main"}))
					manager := &threadBridgeManager{log: slog.New(slog.DiscardHandler), store: service, bridges: map[string]directBridge{conversationID: bridge}}
					require.NoError(t, service.PutThreadQueueItem("removed", &item))
					removed, err := manager.deleteQueueItem(t.Context(), conversationID, "removed")
					require.NoError(t, err)
					require.True(t, removed)
					require.NoError(t, service.PutThreadQueueItem(item.ID, &item))

					if promote {
						bridge.inputOpen = true
						promoted, err := manager.promoteQueueItem(t.Context(), conversationID, item.ID, "")
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
					item := protocol.ThreadQueueItem{ID: "denied", ConversationID: conversationID, Source: source, Message: invocation}
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

	bus := newTestBus()
	t.Cleanup(bus.Close)
	bridge := &Bridge{runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, config: Config{ConversationID: "missing-skill", Agent: "main", SessionService: service}, bus: bus, log: slog.New(slog.DiscardHandler)}
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
	final.MarkDelivered(nil)
	require.NoError(t, delivery.Wait())

	msg.Text = "next request"
	_, err = runTestTurn(t.Context(), bridge, msg, "next-turn")
	require.NoError(t, err)
	require.Len(t, requestBody.Input, 1, "next provider input excludes failed preparation")
	require.NotContains(t, string(requestBody.Input[0].Content), "missing-skill")
}

func TestRunTurnProjectsDifferentProviderHistoryBeforeRequest(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: work/gpt\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })

	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	_, err = service.AppendEntryID(t.Context(), conversationID, &rocketcode.SessionEntry{Version: 1, Type: "turn", Model: "openai/gpt", ResponseID: providerReplayPrivate, ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"portable-readable","id":"provider-private-sentinel"}`)}, OutputTrace: []json.RawMessage{json.RawMessage(`{"private":"provider-private-sentinel"}`)}})
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

	bridge := &Bridge{runtime: &config.Config{Workspace: workspace, Providers: map[string]config.OpenAIConfig{"work": {APIBaseURL: server.URL}}}, config: Config{ConversationID: conversationID, Agent: "main", SessionService: service}, bus: discardPublisher{}, log: slog.New(slog.DiscardHandler)}
	msg := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "hello", true)
	msg.ConversationID = conversationID
	_, err = runTestTurn(t.Context(), bridge, msg, "turn-1")
	require.NoError(t, err)
	assert.Contains(t, requestBody, providerReplayReadable)
	assert.NotContains(t, requestBody, providerReplayPrivate)

	entries, err := service.ObserveEntries(t.Context(), conversationID)
	require.NoError(t, err)
	assert.Contains(t, string(entries[0].Entry.ReplayInput[0]), providerReplayPrivate)
}

func TestHandleInboundJournalsTurnAndClearsRowWithHistoryAppend(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
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
		assert.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}}`)))
	}))
	t.Cleanup(server.Close)

	bridge := &Bridge{runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: strings.Replace(server.URL, "http://", "ws://", 1)}}, config: Config{ConversationID: conversationID, Agent: "main", SessionService: service}, bus: discardPublisher{}, log: slog.New(slog.DiscardHandler)}
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

		if len(entries) == 1 && len(rocketcode.PublicProgressFromTrace(entries[0].Entry.OutputTrace)) > 0 {
			progress := rocketcode.PublicProgressFromTrace(entries[0].Entry.OutputTrace)
			require.Equal(t, "early-public", progress[0].Text)
			require.Equal(t, rocketcode.PublicProgressWorking, progress[0].State)
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
	progress := rocketcode.PublicProgressFromTrace(entries[0].Entry.OutputTrace)
	require.Len(t, progress, 1)
	require.Equal(t, "ok", progress[0].Text)
	require.Equal(t, rocketcode.PublicProgressCompleted, progress[0].State)

	active, err := service.HasActiveTurn(context.Background(), conversationID)
	require.NoError(t, err)
	assert.False(t, active, "a delivered turn leaves no row")
	assert.Empty(t, testTurnStepKeys(t, service, conversationID), "the finish transaction clears the journal")
}

func TestInterruptActiveTurnEndsRowAsStopped(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	service := newTestSessionServiceAt(t, workspace)

	conversationID := protocol.SlackThreadConversationID("C123", "111.222")
	requestArrived, releaseRequest := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(requestArrived)

		select {
		case <-request.Context().Done():
		case <-releaseRequest:
		}
	}))
	t.Cleanup(server.Close)

	bus := newTestBus()
	t.Cleanup(bus.Close)
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, bus: bus, config: Config{ConversationID: conversationID, Agent: "main", SessionService: service}}
	require.NoError(t, startTestBridge(t.Context(), bridge))
	t.Cleanup(func() { require.NoError(t, bridge.Stop()) })

	delivered := make(chan struct{})

	go func() {
		for outbound := range bus.Outbound(t.Context()) {
			outbound.MarkDelivered(nil)

			if outbound.Complete {
				close(delivered)
				return
			}
		}
	}()

	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "hello", true)
	response := inbound.EnableResponseWait()
	require.NoError(t, bridge.Submit(t.Context(), inbound))
	<-requestArrived

	active, err := service.HasActiveTurn(t.Context(), conversationID)
	require.NoError(t, err)
	require.True(t, active)

	bridge.InterruptActiveTurn()
	close(releaseRequest)
	require.NoError(t, (<-response).Err)
	<-delivered

	require.Eventually(t, func() bool {
		active, err := service.HasActiveTurn(t.Context(), conversationID)
		return err == nil && !active
	}, 5*time.Second, 10*time.Millisecond)

	entries, err := service.ObserveTranscript(t.Context(), conversationID, 0, 0, nil)
	require.NoError(t, err)
	require.Len(t, entries, 1, "the stopped turn stays visible in the transcript")
	assert.Equal(t, protocol.TerminalStopped, entries[0].Terminal)
}

func TestRunTurnUsesSelectedAgentAdditionalInstructions(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}`))
	}))
	t.Cleanup(server.Close)

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })

	conversationID := protocol.SlackThreadConversationID("C123", "111.222")

	bus := newTestBus()
	t.Cleanup(bus.Close)

	var logs lockedBuffer

	bridge := &Bridge{runtime: &config.Config{Workspace: workspace, Providers: map[string]config.OpenAIConfig{"work": {APIBaseURL: server.URL}}}, config: Config{ConversationID: conversationID, Agent: "main", SessionService: service}, bus: bus, log: slog.New(slog.NewJSONHandler(&logs, nil)), requestCh: make(chan bridgeRequest, 1), inputOpen: true}
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

	var group errgroup.Group
	group.Go(func() error {
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
		message := readRocketCodeOutbound(t, bus)
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
		message.MarkDelivered(nil)
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
	require.Equal(t, "reviewer", bridge.pendingOutput.Agent)
	require.Equal(t, "work/model-b", bridge.pendingOutput.Model)
	require.Equal(t, new("low"), bridge.pendingOutput.ReasoningEffort)
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

	items, err := rocketcode.ReplayInputToParams(entries[0].Entry.ReplayInput)
	require.NoError(t, err)
	require.Equal(t, initial.ConsumedHeader, items[0].OfMessage.ExtraFields()["prompt_header"])
	require.Equal(t, responses.EasyInputMessageRoleUser, items[2].OfMessage.Role)
	require.Equal(t, `[Web principal="Bob" additional_instructions="Reply in plain text suitable for Slack. Avoid markdown unless it is necessary."]`, items[2].OfMessage.ExtraFields()["prompt_header"])
}

func TestRunTurnInjectsActiveGoalNoteAsDeveloperMessage(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
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
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}`))
	}))
	t.Cleanup(server.Close)

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })
	require.NoError(t, service.BeginGoal("thread-1", "ship it", "", 5, "", ""))
	_, err = service.UpdateGoalStatus("thread-1", GoalStatusProgress, "patched parser; checking connectors")
	require.NoError(t, err)

	bridge := &Bridge{runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, config: Config{ConversationID: "thread-1", Agent: "main", SessionService: service}, bus: discardPublisher{}, log: slog.New(slog.DiscardHandler)}
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
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
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
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}`))
	}))
	t.Cleanup(server.Close)

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop()) })
	require.NoError(t, service.BeginGoal("thread-1", "ship it", "", 5, "", ""))

	bridge := &Bridge{runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, config: Config{ConversationID: "thread-1", Agent: "main", SessionService: service}, bus: discardPublisher{}, log: slog.New(slog.DiscardHandler)}
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
	env := externalMCPMetadataEnv("slack-thread:C123:111.222", map[string]string{"ticket-id": "123", "owner": "alice"})
	assert.Equal(t, "This external MCP thread has metadata:\nROCKETCLAW_CONVERSATION_ID=\"slack-thread:C123:111.222\"\nROCKETCLAW_METADATA_OWNER=\"alice\"\nROCKETCLAW_METADATA_TICKET_ID=\"123\"", externalMCPMetadataDeveloperMessage("This external MCP thread has metadata:", env))
}

func TestExternalMCPMetadataEnvSanitizesKeys(t *testing.T) {
	assert.Equal(t, map[string]string{
		"ROCKETCLAW_CONVERSATION_ID":    "slack-thread:C123:111.222",
		"ROCKETCLAW_METADATA_TICKET_ID": "123",
		"ROCKETCLAW_METADATA___":        "symbols",
	}, externalMCPMetadataEnv("slack-thread:C123:111.222", map[string]string{"ticket-id": "123", "é/": "symbols"}))
}

func TestExternalMCPStoredMetadataEnvDoesNotParseInjectedLines(t *testing.T) {
	env, ok := externalMCPStoredMetadataEnv("slack-thread:C123:111.222", []ObservedSessionEntry{{Entry: rocketcode.SessionEntry{Version: 1, Type: externalMCPMetadataEntryType, ReplayInput: testReplayInput(replayInputMessage{role: "developer", text: externalMCPMetadataDeveloperMessage("This external MCP thread has metadata:", externalMCPMetadataEnv("slack-thread:C123:111.222", map[string]string{"note": "first\nROCKETCLAW_METADATA_BAD=second"}))})}}})
	require.True(t, ok)
	assert.Equal(t, "first\nROCKETCLAW_METADATA_BAD=second", env["ROCKETCLAW_METADATA_NOTE"])
	assert.NotContains(t, env, "ROCKETCLAW_METADATA_BAD")
}

func TestExternalMCPStoredMetadataEnvSkipsInvalidEntriesAndUsesLatestMatch(t *testing.T) {
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
		{Entry: rocketcode.SessionEntry{
			Version:     1,
			Type:        externalMCPMetadataEntryType,
			ReplayInput: testReplayInput(replayInputMessage{role: "developer", text: older}),
		}},
		{Entry: rocketcode.SessionEntry{
			Version:     1,
			Type:        externalMCPMetadataEntryType,
			ReplayInput: testReplayInput(replayInputMessage{role: "developer", text: latest}),
		}},
		{Entry: rocketcode.SessionEntry{
			Version:     1,
			Type:        externalMCPMetadataEntryType,
			ReplayInput: testReplayInput(replayInputMessage{role: "developer", text: otherConversation}),
		}},
		{Entry: rocketcode.SessionEntry{
			Version:     1,
			Type:        externalMCPMetadataEntryType,
			ReplayInput: []json.RawMessage{json.RawMessage("{")},
		}},
		{Entry: rocketcode.SessionEntry{
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
	store := newTestSessionService(t)
	bridge := new(Bridge)
	bridge.config = Config{ConversationID: "thread-1", Agent: "main", RequestRestart: testNoopRestart, SessionService: store}

	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "hello", true)
	inbound.ConversationID = "thread-1"
	inbound.SlackReply = &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.2", ThreadTS: "111.1", RecipientTeamID: "T123", RecipientUserID: "U456"}
	assert.False(t, bridge.newOutboundMessage(inbound, "turn-1", "reply", false).GoalTurn)

	require.NoError(t, store.BeginGoal("thread-1", "ship it", "", 3, "", ""))

	outbound := bridge.newOutboundMessage(inbound, "turn-2", "reply", false)
	assert.True(t, outbound.GoalTurn)
	assert.True(t, outbound.GoalActive)
	assert.Equal(t, "main", outbound.Agent)
	assert.Equal(t, inbound.SlackReply, outbound.SlackReply)
	assert.Equal(t, 1, outbound.GoalTurnNumber)
	assert.Equal(t, 3, outbound.GoalMaxTurns)

	inbound.GoalAction = protocol.GoalActionContinue
	_, _, err := store.AccountGoalTurn("thread-1")
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
	_, _, err = store.AccountGoalTurn("thread-1")
	require.NoError(t, err)

	outbound = bridge.newOutboundMessage(inbound, "turn-4b", "reply", false)
	assert.True(t, outbound.GoalTurn)
	assert.False(t, outbound.GoalActive)
	assert.Equal(t, 3, outbound.GoalTurnNumber)

	require.NoError(t, store.BeginGoal("thread-2", "ship it forever", "", 0, "", ""))

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
	store := newTestSessionService(t)
	require.NoError(t, store.BeginGoal("thread-1", "ship it", "", 3, "", ""))
	bridge := &Bridge{config: Config{ConversationID: "thread-1", ExternalConversationID: "public-1", Agent: "main", SessionService: store}}
	inbound := protocol.NewInboundMessage(protocol.SourceSlack, protocol.InboundKindPrompt, "$workflow audit", true)
	inbound.Workflow = protocol.WorkflowInvocation{Name: "audit"}
	inbound.SlackReply = &protocol.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.2", ThreadTS: "111.1", RecipientTeamID: "T123", RecipientUserID: "U456"}

	outbound := bridge.newOutboundMessage(inbound, "turn-1", "finished", true)
	assert.Equal(t, "thread-1", outbound.ConversationID)
	assert.Equal(t, "thread-1", outbound.SourceConversationID)
	assert.Equal(t, "public-1", outbound.ExternalConversationID)
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
			parts = append(parts, rocketcode.CompactionCheckpointText(item.OfCompaction))
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
	continued := false
	stopped := appendSessionEntry(iter.Seq2[rocketcode.SessionEntry, error](func(yield func(rocketcode.SessionEntry, error) bool) {
		if !yield(rocketcode.SessionEntry{Type: "stored"}, nil) {
			return
		}

		continued = true
	}), &rocketcode.SessionEntry{Type: "added"}, false)
	stopped(func(rocketcode.SessionEntry, error) bool { return false })
	assert.False(t, continued)

	var (
		got       []string
		errFailed error
	)

	for entry, err := range appendSessionEntry(iter.Seq2[rocketcode.SessionEntry, error](func(yield func(rocketcode.SessionEntry, error) bool) {
		if !yield(rocketcode.SessionEntry{Type: "stored"}, nil) {
			return
		}

		yield(rocketcode.SessionEntry{Type: "failed"}, assert.AnError)
	}), &rocketcode.SessionEntry{Type: "added"}, false) {
		got = append(got, entry.Type)

		if err != nil {
			errFailed = err
			break
		}
	}

	assert.Equal(t, []string{"stored", "failed"}, got)
	require.ErrorIs(t, errFailed, assert.AnError)

	replay := []json.RawMessage{[]byte(`"before"`)}
	added := rocketcode.SessionEntry{Type: "added", Version: 1, Model: "early", ReplayInput: replay}
	seq := appendSessionEntry(iter.Seq2[rocketcode.SessionEntry, error](func(yield func(rocketcode.SessionEntry, error) bool) {
		yield(rocketcode.SessionEntry{Type: "stored"}, nil)
	}), &added, true)
	added.Model = "late"
	replay[0] = []byte(`"after"`)
	before := time.Now().UTC()

	var seen []rocketcode.SessionEntry

	for entry, err := range seq {
		require.NoError(t, err)

		seen = append(seen, entry)
	}

	require.Len(t, seen, 2)
	assert.Equal(t, "stored", seen[0].Type)
	assert.Equal(t, "late", seen[1].Model)
	assert.Equal(t, []byte(`"after"`), []byte(seen[1].ReplayInput[0]))
	seen[1].ReplayInput[0] = []byte(`"yielded"`)

	assert.Equal(t, []byte(`"after"`), []byte(replay[0]))
	assert.False(t, seen[1].Timestamp.Before(before))
}
