package harnessbridge

import (
	"bytes"
	"context"
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
	"maps"
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
	"github.com/Rocketable/platform/internal/rocketclaw/events"
	"github.com/Rocketable/platform/internal/rocketclaw/workflow"
	"github.com/Rocketable/platform/internal/rocketcode"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

type testBus struct {
	outbound chan *events.OutboundMessage
	closed   chan struct{}
	once     sync.Once
}

func newTestBus() *testBus {
	return &testBus{outbound: make(chan *events.OutboundMessage, 128), closed: make(chan struct{})}
}

func (b *testBus) PublishOutbound(ctx context.Context, message *events.OutboundMessage) error {
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

func (b *testBus) Outbound(ctx context.Context) iter.Seq[*events.OutboundMessage] {
	return func(yield func(*events.OutboundMessage) bool) {
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

func TestRestartToolScopesDescriptionToRuntimeConfig(t *testing.T) {
	tool := restartTool(testNoopRestart, testNoopRestartRecorder)

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

	tool := restartTool(func(_ context.Context, reason string) (string, error) {
		order = append(order, "restart:"+reason)
		return "custom restart output", nil
	}, func(context.Context) error {
		order = append(order, "record")

		return nil
	})

	result, err := tool.Call(t.Context(), []byte(`{"reason":"rocketclaw.json changed and runtime config must reload"}`), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"record", "restart:rocketclaw.json changed and runtime config must reload"}, order)
	assert.Equal(t, "custom restart output", result.Output)
}

func TestReloadToolReturnsModelVisibleFailure(t *testing.T) {
	tool := reloadTool(func(context.Context, string) (string, error) {
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

	msg := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, goalKickoffLabel, "ship it", false)
	msg.ConversationID = "thread-1"
	require.NoError(t, bridge.finishGoalTurn(t.Context(), msg))

	goal, ok, err := bridge.config.SessionService.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 1, goal.TurnsUsed)
	require.Len(t, bridge.requestCh, 1)
	continuation := (<-bridge.requestCh).inbound
	assert.Equal(t, goalContinuationLabel, continuation.Label)
	assert.Equal(t, &events.SlackReplyTarget{RecipientTeamID: "T123", RecipientUserID: "U456"}, continuation.SlackReply)

	msg = events.NewInboundMessage(events.SourceSystem, events.InboundKindPrompt, goalContinuationLabel, "continue", false)
	msg.ConversationID = "thread-1"
	require.NoError(t, bridge.finishGoalTurn(t.Context(), msg))
	goal, ok, err = bridge.config.SessionService.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 2, goal.TurnsUsed)
}

func TestBridgeTurnPhaseUnclassifiedWithoutLooper(t *testing.T) {
	assert.Equal(t, ThreadTurnUnclassified, (&Bridge{}).TurnPhase())
}

func TestFinishGoalTurnHumanResteeringDoesNotConsumeBudget(t *testing.T) {
	bridge := newGoalAccountingTestBridge(t)
	require.NoError(t, bridge.config.SessionService.BeginGoal("thread-1", "ship it", "", 3, "starter-team", "starter-user"))

	msg := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "try this angle", false)
	msg.ConversationID = "thread-1"
	msg.SlackReply = &events.SlackReplyTarget{RecipientTeamID: "resteer-team", RecipientUserID: "resteer-user"}
	require.NoError(t, bridge.finishGoalTurn(t.Context(), msg))

	goal, ok, err := bridge.config.SessionService.Goal("thread-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 0, goal.TurnsUsed)
	assert.Equal(t, "starter-team", goal.SlackRecipientTeamID)
	assert.Equal(t, "starter-user", goal.SlackRecipientUserID)
	require.Len(t, bridge.requestCh, 1)
	continuation := (<-bridge.requestCh).inbound
	assert.Equal(t, goalContinuationLabel, continuation.Label)
	assert.Equal(t, "starter-team", continuation.SlackReply.RecipientTeamID)
	assert.Equal(t, "starter-user", continuation.SlackReply.RecipientUserID)
}

func TestRecoveredGoalTurnPreservesAccountingSemantics(t *testing.T) {
	for _, tt := range []struct {
		name       string
		metadata   map[string]string
		wantTurns  int
		wantQueued bool
	}{
		{
			name:       "kickoff counts",
			metadata:   map[string]string{activeTurnGoalTurnKey: "true", activeTurnGoalAccountingKey: goalKickoffLabel},
			wantTurns:  1,
			wantQueued: true,
		},
		{
			name:       "continuation counts",
			metadata:   map[string]string{activeTurnGoalTurnKey: "true", activeTurnGoalAccountingKey: goalContinuationLabel},
			wantTurns:  1,
			wantQueued: true,
		},
		{
			name:       "human re-steer is budget-neutral",
			metadata:   map[string]string{activeTurnGoalTurnKey: "true"},
			wantTurns:  0,
			wantQueued: true,
		},
		{
			name:       "legacy missing metadata does not over-count",
			metadata:   nil,
			wantTurns:  0,
			wantQueued: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bridge := newGoalAccountingTestBridge(t)
			require.NoError(t, bridge.config.SessionService.BeginGoal("thread-1", "ship it", "", 3, "", ""))

			turn := ActiveTurnState{SourceMetadata: tt.metadata}
			require.NoError(t, bridge.finishGoalTurn(t.Context(), recoveredGoalTurnMessage(&turn, nil)))

			goal, ok, err := bridge.config.SessionService.Goal("thread-1")
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, tt.wantTurns, goal.TurnsUsed)
			assert.Equal(t, tt.wantQueued, len(bridge.requestCh) == 1)
		})
	}
}

func TestActiveTurnSourceMetadataRecordsGoalAccounting(t *testing.T) {
	bridge := newGoalAccountingTestBridge(t)
	require.NoError(t, bridge.config.SessionService.BeginGoal("thread-1", "ship it", "", 3, "", ""))

	kickoff := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, goalKickoffLabel, "ship it", true)
	kickoff.ConversationID = "thread-1"
	metadata := bridge.activeTurnSourceMetadata(kickoff)
	assert.Equal(t, "true", metadata[activeTurnGoalTurnKey])
	assert.Equal(t, goalKickoffLabel, metadata[activeTurnGoalAccountingKey])

	resteer := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "try this angle", true)
	resteer.ConversationID = "thread-1"
	metadata = bridge.activeTurnSourceMetadata(resteer)
	assert.Equal(t, "true", metadata[activeTurnGoalTurnKey])
	assert.Empty(t, metadata[activeTurnGoalAccountingKey])
}

func TestRecoveredExternalMCPActiveTurnSecondCheckpointPreservesSourceMetadata(t *testing.T) {
	store := newTestSessionService(t)
	bridge := &Bridge{config: Config{ConversationID: "external_mcp:planner:private", SessionService: store}}
	msg := events.NewInboundMessage(events.SourceSystem, events.InboundKindPrompt, "restart_recovery", "recover", false)
	msg.ConversationID = "external_mcp:planner:private"
	msg.Metadata = map[string]string{
		"source":                        string(events.SourceExternalMCP),
		"external_conversation_id":      "public-1",
		"later-key":                     "fresh",
		events.InboundOriginMetadataKey: "System",
		events.InboundMediaMetadataKey:  "Text",
		recoveredTurnMetadataKey:        "true",
	}

	metadata := bridge.activeTurnSourceMetadata(msg)
	sink := activeTurnCheckpointSink{store: store, conversationID: "external_mcp:planner:private", sourceMetadata: metadata}
	checkpoint := &rocketcode.ActiveTurnCheckpoint{TurnID: "turn-1", Agent: "planner", Model: "gpt-5.5", DisplayModel: "gpt-5.5"}
	require.NoError(t, sink.StartActiveTurn(context.Background(), checkpoint))
	checkpoint.ResponseID = "resp-1"
	require.NoError(t, sink.RecordProviderResponse(context.Background(), checkpoint))

	turns, err := store.RecoverableActiveTurns(context.Background())
	require.NoError(t, err)
	require.Len(t, turns, 1)
	assert.Equal(t, string(events.SourceExternalMCP), turns[0].SourceMetadata["source"])
	assert.Equal(t, "public-1", turns[0].SourceMetadata["external_conversation_id"])
	assert.Equal(t, "fresh", turns[0].SourceMetadata["later-key"])
	assert.Empty(t, turns[0].SourceMetadata[events.InboundOriginMetadataKey])
	assert.Empty(t, turns[0].SourceMetadata[events.InboundMediaMetadataKey])
	assert.Empty(t, turns[0].SourceMetadata[recoveredTurnMetadataKey])
}

func TestRecoveredGoalActiveTurnSecondCheckpointPreservesAccountingLabel(t *testing.T) {
	for _, tt := range []struct {
		name      string
		metadata  map[string]string
		wantLabel string
	}{
		{name: "kickoff", metadata: map[string]string{activeTurnGoalTurnKey: "true", activeTurnGoalAccountingKey: goalKickoffLabel}, wantLabel: goalKickoffLabel},
		{name: "continuation", metadata: map[string]string{activeTurnGoalTurnKey: "true", activeTurnGoalAccountingKey: goalContinuationLabel}, wantLabel: goalContinuationLabel},
		{name: "human re-steer", metadata: map[string]string{activeTurnGoalTurnKey: "true"}},
		{name: "legacy missing label", metadata: nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestSessionService(t)
			bridge := &Bridge{config: Config{ConversationID: "thread-1", SessionService: store}}
			msg := events.NewInboundMessage(events.SourceSystem, events.InboundKindPrompt, "restart_recovery", "recover", false)
			msg.ConversationID = "thread-1"

			msg.Metadata = maps.Clone(tt.metadata)
			if msg.Metadata == nil {
				msg.Metadata = map[string]string{}
			}

			msg.Metadata[recoveredTurnMetadataKey] = "true"

			metadata := bridge.activeTurnSourceMetadata(msg)
			sink := activeTurnCheckpointSink{store: store, conversationID: "thread-1", sourceMetadata: metadata}
			checkpoint := &rocketcode.ActiveTurnCheckpoint{TurnID: "turn-1", Agent: "planner", Model: "gpt-5.5", DisplayModel: "gpt-5.5"}
			require.NoError(t, sink.StartActiveTurn(context.Background(), checkpoint))
			checkpoint.ResponseID = "resp-1"
			require.NoError(t, sink.RecordProviderResponse(context.Background(), checkpoint))

			turns, err := store.RecoverableActiveTurns(context.Background())
			require.NoError(t, err)
			require.Len(t, turns, 1)
			assert.Equal(t, tt.metadata[activeTurnGoalTurnKey], turns[0].SourceMetadata[activeTurnGoalTurnKey])
			assert.Equal(t, tt.wantLabel, turns[0].SourceMetadata[activeTurnGoalAccountingKey])
		})
	}
}

func TestGoalSteeringPromptRequiresProgressSummaryAndNote(t *testing.T) {
	prompt := goalSteeringPrompt(&GoalState{Objective: "ship it", MaxTurns: 5, TurnsUsed: 1})

	assert.Contains(t, prompt, "Progress summary:")
	assert.Contains(t, prompt, "status progress")
	assert.Contains(t, prompt, "status complete")
	assert.Contains(t, prompt, "status blocked")
	assert.Contains(t, prompt, "note")
}

func TestInterruptActiveTurnSignalsAndClearsQueue(t *testing.T) {
	interrupts := make(chan os.Signal, 1)
	marker := &events.SlackReplyTarget{ChannelID: "D123", MessageTS: "222.333", ThreadTS: "111.222"}

	bridge := &Bridge{requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{}), activeReply: &events.InboundMessage{SlackReply: marker}, activeTurnInterrupts: interrupts}
	queued := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "queued", false)

	queued.ConversationID = "thread-1"
	bridge.requestCh <- bridgeRequest{inbound: queued, activation: NoopActivationHook}

	result := bridge.InterruptActiveTurn()

	assert.Equal(t, marker, result.SlackReply)
	assert.Empty(t, bridge.requestCh)
	assert.True(t, bridge.activeTurnInterrupted)
	assert.Equal(t, os.Interrupt, <-interrupts)
}

func TestInterruptActiveTurnDoesNotDeleteThreadQueueOrScheduled(t *testing.T) {
	store := newTestSessionService(t)
	conversationID := SlackThreadConversationID("C123", "111.222")
	require.NoError(t, store.PutThreadQueueItem("q1", &ThreadQueueItem{ConversationID: conversationID, Message: "changelog", Principal: "U1", StashAt: time.Date(2000, 1, 1, 3, 0, 0, 0, time.UTC), Position: 0}))
	require.NoError(t, store.PutScheduledMessage("s1", &ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "due later", DueAt: time.Date(2000, 1, 1, 4, 0, 0, 0, time.UTC)}))

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}

	queued := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "live", false)
	bridge.requestCh <- bridgeRequest{inbound: queued, activation: NoopActivationHook}

	bridge.InterruptActiveTurn()

	assert.Empty(t, bridge.requestCh)

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
	conversationID := SlackThreadConversationID("C123", "111.222")
	stashAt := time.Date(2000, 1, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.PutThreadQueueItem("q1", &ThreadQueueItem{ID: "q1", ConversationID: conversationID, Message: "A", Principal: "U1", StashAt: stashAt, Position: 0, SlackChannel: "C123", SlackTS: "111.222"}))
	require.NoError(t, store.PutScheduledMessage("s1", &ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "scheduled", DueAt: time.Date(2000, 1, 1, 12, 5, 0, 0, time.UTC)}))

	var popped []string

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store, EnqueueActivation: EnqueueActivation{Fn: func(_ context.Context, item *ThreadQueueItem, _ *events.InboundMessage) error {
		popped = append(popped, item.Message)
		return nil
	}}}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}

	require.NoError(t, bridge.PickLaterWork(t.Context()))
	require.Len(t, bridge.requestCh, 1)
	request := <-bridge.requestCh
	require.NotNil(t, request.inbound)
	assert.Equal(t, "A", request.inbound.Text)
	assert.Equal(t, "q1", request.queueItemID)
	require.NoError(t, request.activation(t.Context(), request.inbound))
	assert.Equal(t, []string{"A"}, popped)

	messages, err := store.ScheduledMessagesForConversation(conversationID)
	require.NoError(t, err)
	require.Len(t, messages, 1)
}

func TestPickLaterWorkPrefersEarlierDueScheduleOverLaterStash(t *testing.T) {
	store := newTestSessionService(t)
	conversationID := SlackThreadConversationID("C123", "111.222")
	require.NoError(t, store.PutThreadQueueItem("q1", &ThreadQueueItem{ID: "q1", ConversationID: conversationID, Message: "changelog", Principal: "U1", StashAt: time.Date(2000, 1, 1, 3, 0, 0, 0, time.UTC), Position: 0}))
	require.NoError(t, store.PutScheduledMessage("s1", &ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "scheduled", DueAt: time.Date(2000, 1, 1, 2, 58, 0, 0, time.UTC)}))

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}

	require.NoError(t, bridge.PickLaterWork(t.Context()))
	require.Len(t, bridge.requestCh, 1)
	request := <-bridge.requestCh
	require.NotNil(t, request.inbound)
	assert.Equal(t, "scheduled", request.inbound.Text)
	assert.Equal(t, "s1", request.scheduledMessageID)

	items, err := store.ThreadQueueForConversation(conversationID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "changelog", items[0].Message)
}

func TestPickLaterWorkSkipsWhenGoalStillActive(t *testing.T) {
	store := newTestSessionService(t)
	conversationID := SlackThreadConversationID("C123", "111.222")
	require.NoError(t, store.BeginGoal(conversationID, "ship it", "", 3, "", ""))
	require.NoError(t, store.PutThreadQueueItem("q1", &ThreadQueueItem{ID: "q1", ConversationID: conversationID, Message: "changelog", Principal: "U1", StashAt: time.Date(2000, 1, 1, 3, 0, 0, 0, time.UTC), Position: 0}))

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}

	require.NoError(t, bridge.PickLaterWork(t.Context()))
	assert.Empty(t, bridge.requestCh)

	items, err := store.ThreadQueueForConversation(conversationID)
	require.NoError(t, err)
	require.Len(t, items, 1)
}

func TestPickLaterWorkAfterStopGoalStartsLaterWork(t *testing.T) {
	store := newTestSessionService(t)
	conversationID := SlackThreadConversationID("C123", "111.222")
	require.NoError(t, store.BeginGoal(conversationID, "ship it", "", 3, "", ""))
	require.NoError(t, store.PutThreadQueueItem("q1", &ThreadQueueItem{ID: "q1", ConversationID: conversationID, Message: "changelog", Principal: "U1", StashAt: time.Date(2000, 1, 1, 3, 0, 0, 0, time.UTC), Position: 0, SlackChannel: "C123", SlackTS: "111.222"}))
	require.NoError(t, store.StopGoal(conversationID))

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}

	require.NoError(t, bridge.PickLaterWork(t.Context()))
	require.Len(t, bridge.requestCh, 1)
	assert.Equal(t, "changelog", (<-bridge.requestCh).inbound.Text)
}

func TestEnqueueActivationZeroValueIsInert(t *testing.T) {
	require.NoError(t, (EnqueueActivation{}).Activate(t.Context(), &ThreadQueueItem{}, nil))
	(PendingSteersSink{}).Persist("slack-thread:C123:111.222", nil)
}

func TestPickLaterWorkClaimsDueRecurringSchedule(t *testing.T) {
	store := newTestSessionService(t)
	conversationID := SlackThreadConversationID("C123", "111.222")
	dueAt := time.Now().UTC().Add(-time.Second)
	require.NoError(t, store.PutScheduledMessage("repeat", &ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "again", DueAt: dueAt, Recurring: true, Interval: time.Minute}))

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
	require.NoError(t, bridge.PickLaterWork(t.Context()))
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
	conversationID := SlackThreadConversationID("C123", "111.222")
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: store}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
	require.NoError(t, bridge.PickLaterWork(t.Context()))
	assert.Empty(t, bridge.requestCh)
	bridge.requestCh <- bridgeRequest{inbound: events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "queued", false), activation: NoopActivationHook}
	require.NoError(t, bridge.PickLaterWork(t.Context()))
	assert.Len(t, bridge.requestCh, 1)
	close(bridge.stopCh)
	bridge.stopped = true
	require.ErrorIs(t, bridge.PickLaterWork(t.Context()), errBridgeStopped)
}

func TestPickLaterWorkDoesNotClaimScheduledWhenTurnBusy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newTestSessionService(t)
		conversationID := SlackThreadConversationID("C123", "111.222")
		due := ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "now", DueAt: time.Now().UTC().Add(-time.Second), Recurring: true, Interval: time.Minute}
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
	bridge := &Bridge{stopCh: make(chan struct{}), activeReply: events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "ordinary", true)}
	_, bridge.activeTurnCancel = context.WithCancel(t.Context())
	require.NoError(t, bridge.Stop())
	assert.False(t, bridge.activeTurnInterrupted)
}

func TestInterruptActiveWorkflowCancelsWithoutSignalChannel(t *testing.T) {
	canceled := make(chan struct{})
	bridge := &Bridge{requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{}), activeReply: new(events.InboundMessage)}
	ctx, cancel := context.WithCancel(t.Context())
	bridge.activeTurnCancel = cancel

	context.AfterFunc(ctx, func() { close(canceled) })

	bridge.InterruptActiveTurn()
	<-canceled
	assert.True(t, bridge.activeTurnInterrupted)
}

func TestActiveTurnCheckpointSinkMapsLifecycleToSessionService(t *testing.T) {
	store := newTestSessionService(t)
	sink := activeTurnCheckpointSink{
		store:          store,
		conversationID: "external_mcp:planner:private",
		sourceMetadata: map[string]string{"source": "external_mcp", "external_conversation_id": "public-1"},
	}
	checkpoint := &rocketcode.ActiveTurnCheckpoint{TurnID: "turn-1", Agent: "planner", Model: "gpt-5.5", DisplayModel: "gpt-5.5"}

	require.NoError(t, sink.StartActiveTurn(context.Background(), checkpoint))
	checkpoint.ResponseID = "resp-1"
	require.NoError(t, sink.RecordProviderResponse(context.Background(), checkpoint))
	require.NoError(t, sink.RecordRecoveredReplay(context.Background(), checkpoint))

	turns, err := store.RecoverableActiveTurns(context.Background())
	require.NoError(t, err)
	require.Len(t, turns, 1)
	assert.Equal(t, "external_mcp:planner:private", turns[0].Checkpoint.ConversationKey)
	assert.Equal(t, "public-1", turns[0].SourceMetadata["external_conversation_id"])
	assert.Equal(t, "resp-1", turns[0].Checkpoint.ResponseID)

	require.NoError(t, sink.ClearCompletedTurn(context.Background(), "turn-1"))
	turns, err = store.RecoverableActiveTurns(context.Background())
	require.NoError(t, err)
	assert.Empty(t, turns)
}

func TestRecoveredActiveTurnCheckpointSinkPreservesRecoveredReplay(t *testing.T) {
	recoveredReplay := []json.RawMessage{json.RawMessage(`{"type":"message","role":"developer","content":"interrupted transcript"}`)}
	sink := &captureCheckpointSink{}
	wrapper := recoveredActiveTurnCheckpointSink{sink: sink, recoveredReplay: recoveredReplay}

	checkpoint := &rocketcode.ActiveTurnCheckpoint{
		TurnID:      "turn-2",
		ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"continue"}`)},
	}
	require.NoError(t, wrapper.RecordProviderResponse(context.Background(), checkpoint))

	require.Len(t, sink.checkpoints, 1)
	assert.JSONEq(t, `{"type":"message","role":"developer","content":"interrupted transcript"}`, string(sink.checkpoints[0].ReplayInput[0]))
	assert.JSONEq(t, `{"type":"message","role":"user","content":"continue"}`, string(sink.checkpoints[0].ReplayInput[1]))
	assert.JSONEq(t, `{"type":"message","role":"user","content":"continue"}`, string(checkpoint.ReplayInput[0]))

	require.NoError(t, wrapper.RecordCompletedToolOutput(context.Background(), sink.checkpoints[0]))
	require.Len(t, sink.checkpoints, 2)
	assert.Len(t, sink.checkpoints[1].ReplayInput, 2)
}

type captureCheckpointSink struct {
	checkpoints []*rocketcode.ActiveTurnCheckpoint
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

func (s *captureCheckpointSink) StartActiveTurn(_ context.Context, checkpoint *rocketcode.ActiveTurnCheckpoint) error {
	s.checkpoints = append(s.checkpoints, checkpoint)
	return nil
}

func (s *captureCheckpointSink) RecordProviderResponse(_ context.Context, checkpoint *rocketcode.ActiveTurnCheckpoint) error {
	s.checkpoints = append(s.checkpoints, checkpoint)
	return nil
}

func (s *captureCheckpointSink) RecordCompletedToolOutput(_ context.Context, checkpoint *rocketcode.ActiveTurnCheckpoint) error {
	s.checkpoints = append(s.checkpoints, checkpoint)
	return nil
}

func (s *captureCheckpointSink) RecordRecoveredReplay(_ context.Context, checkpoint *rocketcode.ActiveTurnCheckpoint) error {
	s.checkpoints = append(s.checkpoints, checkpoint)
	return nil
}

func (s *captureCheckpointSink) ClearCompletedTurn(context.Context, string) error {
	return nil
}

func TestSubmitWhenActiveDefersActivationUntilRequestDequeued(t *testing.T) {
	bridge := &Bridge{
		config:    Config{ConversationID: "external_mcp:planner:private"},
		requestCh: make(chan bridgeRequest, 1),
		stopCh:    make(chan struct{}),
	}
	activated := false
	inbound := events.NewInboundMessage(events.SourceExternalMCP, events.InboundKindPrompt, "", "follow up", true)
	inbound.ConversationID = "external_mcp:planner:private"

	require.NoError(t, bridge.SubmitWhenActive(context.Background(), inbound, func(context.Context, *events.InboundMessage) error {
		activated = true

		return nil
	}))
	assert.False(t, activated)

	request := <-bridge.requestCh
	require.NoError(t, request.activation(context.Background(), request.inbound))
	assert.True(t, activated)
	assert.Equal(t, "external_mcp:planner:private", request.inbound.ConversationID)
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
	tool := restartTool(testNoopRestart, testNoopRestartRecorder)
	result, err := tool.Call(t.Context(), []byte(`{"reason":"cron changed"}`), nil)
	require.NoError(t, err)
	assert.Empty(t, result.Output)

	_, err = tool.Call(t.Context(), []byte(`{"reason":" "}`), nil)
	require.EqualError(t, err, "reason is required")

	_, err = tool.Call(t.Context(), []byte(`{`), nil)
	require.ErrorContains(t, err, "parse restart request")

	tool = restartTool(testNoopRestart, func(context.Context) error { return assert.AnError })
	_, err = tool.Call(t.Context(), []byte(`{"reason":"cron changed"}`), nil)
	require.ErrorIs(t, err, assert.AnError)

	tool = restartTool(func(context.Context, string) (string, error) { return "", assert.AnError }, testNoopRestartRecorder)
	_, err = tool.Call(t.Context(), []byte(`{"reason":"cron changed"}`), nil)
	assert.ErrorIs(t, err, assert.AnError)
}

func testNoopRestart(context.Context, string) (string, error) { return "", nil }

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

func testNoopStartNewThread(context.Context, *events.StartNewThreadRequest) (events.StartNewThreadResult, error) {
	return events.StartNewThreadResult{}, errors.New("start new thread is inert in this test")
}

func testNoopRestartRecorder(context.Context) error { return nil }

func TestProcessResponseAndFinalShareTurnID(t *testing.T) {
	bus := newTestBus()
	defer bus.Close()

	bridge := new(Bridge)
	bridge.bus = bus
	bridge.log = slog.New(slog.DiscardHandler)
	bridge.config = Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, RequestRestart: testNoopRestart, SessionService: newTestSessionService(t)}
	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)
	inbound.ConversationID = bridge.config.ConversationID
	result := runResult{turnID: "turn-1", text: "", thinking: "", sequence: 0, sessionEntryID: 0, responseID: "", model: ""}

	var reply rocketcode.ChatResponse

	reply.Kind = rocketcode.ChatResponseAssistantMessage
	reply.Text = "hello back"
	require.NoError(t, bridge.processResponse(context.Background(), inbound, &result, reply))
	partial := readRocketCodeOutbound(t, bus)
	assert.Equal(t, "turn-1", partial.TurnID)
	assert.False(t, partial.Complete)

	var group errgroup.Group

	group.Go(func() error { return bridge.publishFinal(context.Background(), inbound, result, true) })

	final := readRocketCodeOutbound(t, bus)
	assert.Equal(t, "turn-1", final.TurnID)
	assert.True(t, final.Complete)
	final.MarkDelivered(nil)
	require.NoError(t, group.Wait())
}

func TestProcessResponseSkipsRecoveredProgress(t *testing.T) {
	bus := newTestBus()
	defer bus.Close()

	bridge := new(Bridge)
	bridge.bus = bus
	bridge.log = slog.New(slog.DiscardHandler)
	bridge.config = Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, RequestRestart: testNoopRestart, SessionService: newTestSessionService(t)}
	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "restart_recovery", "recover", false)
	inbound.ConversationID = bridge.config.ConversationID
	inbound.Metadata = map[string]string{recoveredTurnMetadataKey: "true"}
	result := runResult{turnID: "turn-1"}

	reply := rocketcode.ChatResponse{Kind: rocketcode.ChatResponseAssistantTool, Tool: &rocketcode.ToolDiagnostic{Phase: "call", Name: "bash", Status: "started", Arguments: json.RawMessage(`{"description":"old progress"}`)}}
	require.NoError(t, bridge.processResponse(context.Background(), inbound, &result, reply))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	for range bus.Outbound(ctx) {
		t.Fatal("recovered progress produced outbound message")
	}
}

func TestPublishFinalMarksCurrentGoalCompletion(t *testing.T) {
	bus := newTestBus()
	defer bus.Close()

	bridge := new(Bridge)
	bridge.bus = bus
	bridge.log = slog.New(slog.DiscardHandler)
	bridge.config = Config{ConversationID: "thread-1", Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, RequestRestart: testNoopRestart, SessionService: newTestSessionService(t)}
	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "done", true)
	inbound.ConversationID = "thread-1"
	result := runResult{turnID: "turn-1", text: "done", goalCompleted: true}

	var group errgroup.Group
	group.Go(func() error { return bridge.publishFinal(context.Background(), inbound, result, true) })

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
	bridge.config = Config{ConversationID: "thread-1", Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, RequestRestart: testNoopRestart, SessionService: store}
	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "trailing message", true)
	inbound.ConversationID = "thread-1"
	result := runResult{turnID: "turn-2", text: "normal reply"}

	var group errgroup.Group
	group.Go(func() error { return bridge.publishFinal(context.Background(), inbound, result, true) })

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
	bridge.config = Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, RequestRestart: testNoopRestart, SessionService: newTestSessionService(t)}
	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)
	inbound.ConversationID = bridge.config.ConversationID
	resultCh := inbound.EnableResponseWait()
	result := runResult{turnID: "turn-1", text: "", thinking: "", sequence: 0, sessionEntryID: 0, responseID: "", model: "", attachments: []events.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}}}

	var group errgroup.Group
	group.Go(func() error { return bridge.publishFinal(context.Background(), inbound, result, true) })

	outbound := readRocketCodeOutbound(t, bus)
	assert.True(t, outbound.Complete)
	assert.Empty(t, outbound.Text)
	assert.Equal(t, []events.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}}, outbound.Attachments)

	response := <-resultCh
	assert.Equal(t, []events.OutboundAttachment{{Name: "report.txt", MIMEType: "text/plain", Data: []byte("report")}}, response.Attachments)
	outbound.MarkDelivered(nil)
	require.NoError(t, group.Wait())
}

func TestPublishFinalStampsSessionEntryID(t *testing.T) {
	bus := newTestBus()
	defer bus.Close()

	bridge := new(Bridge)
	bridge.bus = bus
	bridge.log = slog.New(slog.DiscardHandler)
	bridge.config = Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, RequestRestart: testNoopRestart}
	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)
	inbound.ConversationID = bridge.config.ConversationID
	inbound.Workflow = new(workflow.RunRequest)
	result := runResult{turnID: "turn-1", text: "Final answer", sequence: 1, sessionEntryID: 42}

	var group errgroup.Group
	group.Go(func() error { return bridge.publishFinal(context.Background(), inbound, result, true) })

	outbound := readRocketCodeOutbound(t, bus)
	assert.True(t, outbound.Complete)
	assert.NotZero(t, outbound.SessionEntryID)
	assert.Equal(t, int64(42), outbound.SessionEntryID)
	outbound.MarkDelivered(nil)
	require.NoError(t, group.Wait())
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
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	conversationID := SlackThreadConversationID("C123", "111.222")
	bridge := NewConversation(&config.Config{Workspace: workspace}, bus, &Config{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, RequestRestart: testNoopRestart, StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.DiscardHandler))
	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)
	inbound.ConversationID = conversationID

	var group errgroup.Group
	group.Go(func() error { return bridge.handleInbound(context.Background(), inbound) })

	outbound := readRocketCodeOutbound(t, bus)
	assert.True(t, outbound.Complete)
	assert.Contains(t, outbound.Text, internalErrorResponse)
	assert.Contains(t, outbound.Text, `missing required default agent "main"`)
	outbound.MarkDelivered(nil)
	require.NoError(t, group.Wait())

	response := <-inbound.EnableResponseWait()
	assert.Equal(t, outbound.Text, response.Text)
	require.NoError(t, response.Err)
}

func TestRocketCodeConfigEnablesDiagnosticsForThinkingUpdates(t *testing.T) {
	bridge := &Bridge{runtime: &config.Config{AutoApproverModel: "gpt-5.4-mini"}, config: Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", RequestRestart: testNoopRestart, RequestReload: func(context.Context, string) (string, error) {
		return "rocketclaw runtime assets reloaded", nil
	}, SessionService: newTestSessionService(t)}}
	cfg := bridge.rocketcodeConfig(t.TempDir(), nil, nil, rocketcode.Tool{Name: attachFilesToolName})

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
	assert.Equal(t, map[string]string{"A": "B"}, bridge.rocketcodeConfig(t.TempDir(), map[string]string{"A": "B"}, nil).ShellEnv)
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
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	bus := newTestBus()
	defer bus.Close()

	bridge := NewConversation(new(config.Config), bus, &Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, RequestRestart: testNoopRestart, StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.DiscardHandler))
	assert.Same(t, service, bridge.config.SessionService)
}

func TestBridgeSubmitReturnsErrorAfterStop(t *testing.T) {
	bus := newTestBus()
	defer bus.Close()

	conversationID := SlackThreadConversationID("C123", "111.222")
	bridge := NewConversation(&config.Config{Workspace: t.TempDir()}, bus, &Config{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, StartNewThread: testNoopStartNewThread, SessionService: newTestSessionService(t)}, slog.New(slog.DiscardHandler))
	require.NoError(t, bridge.Start(context.Background()))
	require.NoError(t, bridge.Stop())

	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)
	inbound.ConversationID = conversationID
	err := bridge.Submit(context.Background(), inbound)
	require.ErrorIs(t, err, errBridgeStopped)
}

func TestBridgeStartReportsStateLoadError(t *testing.T) {
	service := newTestSessionService(t)
	require.NoError(t, service.Stop(context.Background()))

	bus := newTestBus()
	defer bus.Close()

	bridge := NewConversation(&config.Config{Workspace: t.TempDir()}, bus, &Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.DiscardHandler))
	err := bridge.Start(context.Background())
	require.ErrorContains(t, err, "load scheduled messages")
}

func TestBridgeEnqueueReturnsContextOrStopErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	bridge := &Bridge{requestCh: make(chan bridgeRequest), stopCh: make(chan struct{})}
	err := bridge.enqueue(ctx, bridgeRequest{}, "submit test")
	require.ErrorIs(t, err, context.Canceled)

	bridge = &Bridge{requestCh: make(chan bridgeRequest), stopCh: make(chan struct{})}
	close(bridge.stopCh)
	err = bridge.enqueue(context.Background(), bridgeRequest{}, "submit test")
	require.ErrorIs(t, err, errBridgeStopped)
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
				writeRawRunFunctionCall(t, w, "resp_1", "call_1", "task", map[string]string{"description": "delegate", "prompt": "delegated prompt", "subagent_type": "helper"})
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
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	bus := newTestBus()
	defer bus.Close()

	conversationID := SlackThreadConversationID("C123", "111.222")
	bridge := NewConversation(&config.Config{Workspace: workspace, Models: map[string]string{"main": "main-model", "helper": "child/helper-model", "guardrail": "guard/guardrail-model"}, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}, Providers: map[string]config.OpenAIConfig{"child": {APIBaseURL: childServer.URL}, "guard": {APIBaseURL: guardServer.URL}}}, bus, &Config{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.DiscardHandler))
	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)
	inbound.ConversationID = conversationID

	var group errgroup.Group
	group.Go(func() error { return bridge.handleInbound(context.Background(), inbound) })

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var outbound *events.OutboundMessage

	for msg := range bus.Outbound(ctx) {
		msg.MarkDelivered(nil)

		if msg.Complete {
			outbound = msg

			break
		}
	}

	require.NotNil(t, outbound)
	require.Equal(t, "persistent done", outbound.Text)
	require.NoError(t, group.Wait())
	require.Equal(t, 5, requests)

	entries, err := service.ObserveEntries(context.Background(), conversationID, 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "main-model", entries[0].Entry.Model)
}

func TestBridgeStopAfterStartContextCanceledIsIdempotent(t *testing.T) {
	bus := newTestBus()
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	bridge := NewConversation(&config.Config{Workspace: t.TempDir()}, bus, &Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, StartNewThread: testNoopStartNewThread, SessionService: newTestSessionService(t)}, slog.New(slog.DiscardHandler))
	require.NoError(t, bridge.Start(ctx))

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
	require.NoError(t, root.WriteFile("reports/latest.txt", []byte("report body"), 0o644))

	attachments := new(outboundAttachmentCollector)
	tool := attachments.Tool(root)
	parameters := tool.Parameters
	properties := parameters["properties"].(map[string]any)
	attachmentsSchema := properties["attachments"].(map[string]any)
	items := attachmentsSchema["items"].(map[string]any)
	assert.ElementsMatch(t, []string{"path", "name", "mime_type", "content", "content_base64"}, items["required"])

	_, err = tool.Call(t.Context(), []byte(`{"attachments":[{"path":"reports/latest.txt","name":"","mime_type":"","content":"","content_base64":""}]}`), nil)
	require.NoError(t, err)

	assert.Equal(t, []events.OutboundAttachment{{Name: "latest.txt", MIMEType: "text/plain", Data: []byte("report body")}}, attachments.Attachments())
}

func TestOutboundAttachmentSources(t *testing.T) {
	workspace := t.TempDir()
	root, err := os.OpenRoot(workspace)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	got, err := outboundAttachment(root, &outboundAttachmentInput{Name: "note.txt", Content: "hello"})
	require.NoError(t, err)
	assert.Equal(t, events.OutboundAttachment{Name: "note.txt", MIMEType: "text/plain", Data: []byte("hello")}, got)

	got, err = outboundAttachment(root, &outboundAttachmentInput{Name: "", MIMEType: "Text/Plain; Charset=UTF-8", ContentBase64: "aGVsbG8="})
	require.NoError(t, err)
	assert.Equal(t, events.OutboundAttachment{Name: "attachment", MIMEType: "text/plain", Data: []byte("hello")}, got)

	got, err = outboundAttachment(root, &outboundAttachmentInput{Name: "blob", Content: "hello"})
	require.NoError(t, err)
	assert.Equal(t, events.OutboundAttachment{Name: "blob", MIMEType: "text/plain", Data: []byte("hello")}, got)

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

	tool := new(outboundAttachmentCollector).Tool(root)
	_, err = tool.Call(t.Context(), []byte(`{`), nil)
	require.ErrorContains(t, err, "parse response attachments")

	raw := []byte(`{"attachments":[{"path":"missing.txt","name":"","mime_type":"","content":"","content_base64":""}]}`)
	_, err = tool.Call(t.Context(), raw, nil)
	require.ErrorContains(t, err, `read attachment "missing.txt"`)
}

func TestAttachmentFallbackAndImageAttachments(t *testing.T) {
	assert.Empty(t, attachmentFallback(&events.InboundMessage{Attachments: []events.InboundAttachment{{Name: "image.png"}}}))
	assert.Equal(t, unsupportedFileFallback, attachmentFallback(&events.InboundMessage{HadNonImageAttachments: true}))
	assert.Contains(t, attachmentFallback(&events.InboundMessage{HadAttachments: true, AttachmentWarnings: []string{" first ", " ", "second"}}), "- first\n- second")

	attachments := attachmentsFromInbound([]events.InboundAttachment{
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
}

func TestNormalizeInboundAttachmentsCentralizesModelPolicy(t *testing.T) {
	msg := &events.InboundMessage{Attachments: []events.InboundAttachment{
		{Name: "tiny.png", MIMEType: "application/octet-stream", Data: tinyPNG()},
		{Name: "not-image.png", MIMEType: "image/png", Data: []byte("not an image")},
		{Name: "empty.png", MIMEType: "image/png"},
	}}

	normalizeInboundAttachments(msg)

	require.Len(t, msg.Attachments, 1)
	assert.True(t, msg.HadAttachments)
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
	msg := &events.InboundMessage{Attachments: []events.InboundAttachment{
		{Name: "large.png", MIMEType: "image/png", Data: append(tinyPNG(), make([]byte, maxInboundAttachmentResizeInput)...)},
	}}

	normalizeInboundAttachments(msg)

	assert.Empty(t, msg.Attachments)
	assert.True(t, msg.HadAttachments)
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
	msg := &events.InboundMessage{Attachments: []events.InboundAttachment{
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
		{Type: "compaction_summary", ID: "cmp_2", EncryptedContent: "chatgpt-sealed"},
		{Type: "reasoning", ID: "rsn_1", Summary: []responses.ResponseReasoningItemSummary{{Text: "summary"}}, EncryptedContent: "reasoning-sealed"},
		{Type: "reasoning", ID: "rsn_2"},
	}

	got, err := rocketcode.CompactedOutputToReplayInput(items)
	require.NoError(t, err)
	params, err := rocketcode.ReplayInputToParams(got)
	require.NoError(t, err)
	require.Len(t, params, len(items))

	assert.Equal(t, "hello world", params[0].OfMessage.Content.OfString.Value)
	assert.Equal(t, responses.EasyInputMessagePhase("final_answer"), params[0].OfMessage.Phase)
	assert.Equal(t, "assistant", params[1].OfMessage.Content.OfString.Value)
	assert.Equal(t, "sealed", params[2].OfCompaction.EncryptedContent)
	assert.Equal(t, "chatgpt-sealed", params[3].OfCompaction.EncryptedContent)
	assert.JSONEq(t, `{"encrypted_content":"sealed","id":"cmp_1","type":"compaction"}`, string(got[2]))
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
	_, err = rocketcode.NewWithModelResolver(resolver, &rocketcode.Config{ShellTempDir: shellTempDir, ChildRunLogger: rocketcode.DiscardChildRunLog, CheckpointSink: rocketcode.InertCheckpointSink{}, ShellCommand: rocketcode.DefaultShellCommand}, root, agents, skills, "main", io.Discard)
	require.NoError(t, err)
}

func TestReplayInputMessageRoleTextCoversMessageShapes(t *testing.T) {
	plain := responses.ResponseInputItemUnionParam{OfMessage: &responses.EasyInputMessageParam{Role: "assistant", Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("plain")}, Type: "message"}}
	role, text, ok := replayInputMessageRoleText(&plain)
	require.True(t, ok)
	assert.Equal(t, "assistant", role)
	assert.Equal(t, "plain", text)

	withContent := responses.ResponseInputItemUnionParam{OfInputMessage: &responses.ResponseInputItemMessageParam{Role: "user", Content: responses.ResponseInputMessageContentListParam{responses.ResponseInputContentParamOfInputText("look")}, Type: "message"}}
	role, text, ok = replayInputMessageRoleText(&withContent)
	require.True(t, ok)
	assert.Equal(t, "user", role)
	assert.Equal(t, "look", text)

	_, _, ok = replayInputMessageRoleText(&responses.ResponseInputItemUnionParam{})
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
	assert.Equal(t, "[Slack media=Text principal=\"Alice\" additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\nhello\n\nAttachment notes:\n- skipped image", buildPrompt(&events.InboundMessage{Source: events.SourceSlack, Human: true, Text: "  hello  ", AttachmentWarnings: []string{" skipped image ", " "}, Metadata: map[string]string{events.InboundPrincipalMetadataKey: "Alice"}}, nil))

	assert.Equal(t, "[System media=Text additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\nAttachment notes:\n- unsupported PDF", buildPrompt(&events.InboundMessage{AttachmentWarnings: []string{" unsupported PDF "}}, nil))
}

func TestBuildPromptAdditionalInstructionsFrontmatter(t *testing.T) {
	msg := &events.InboundMessage{Source: events.SourceSlack, Human: true, Text: "hello", Metadata: map[string]string{events.InboundPrincipalMetadataKey: "Alice"}}

	assert.Equal(t, "[Slack media=Text principal=\"Alice\" additional_instructions=\"Reply in one sentence.\"]\n\nhello", buildPrompt(msg, map[string]any{"additionalInstructions": "Reply in one sentence."}))
	assert.Equal(t, "[Slack media=Text principal=\"Alice\" additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\nhello", buildPrompt(msg, map[string]any{"additionalInstructions": " "}))
	assert.Equal(t, "[Slack media=Text principal=\"Alice\" additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\nhello", buildPrompt(msg, map[string]any{"additionalInstructions": 7}))
	assert.Equal(t, "[System media=Text additional_instructions=\"Reply in plain text suitable for Slack. Avoid markdown unless it is necessary.\"]\n\n task \n", buildPrompt(&events.InboundMessage{Source: events.SourceSystem, Label: startNewThreadToolName, Text: " task \n", Metadata: map[string]string{events.InboundOriginMetadataKey: "System", events.InboundMediaMetadataKey: "Text"}}, nil))
}

func TestParseSlackDirectSkillTrigger(t *testing.T) {
	for _, text := range []string{"💡 docs-helper write docs", ":light_bulb: docs-helper write docs", ":electric_light_bulb: docs-helper write docs"} {
		directSkill, ok := parseSlackDirectSkillTrigger(text)

		require.True(t, ok)
		assert.Equal(t, rocketcode.PromptInputDirectSkill{Name: "docs-helper", Arguments: "write docs"}, directSkill)
	}

	directSkill, ok := parseSlackDirectSkillTrigger("💡   ")
	require.True(t, ok)
	assert.Empty(t, directSkill.Name)

	_, ok = parseSlackDirectSkillTrigger("hello 💡 docs-helper")
	assert.False(t, ok)
}

func TestProvenanceHeaderSanitizesAmbiguousTokens(t *testing.T) {
	assert.Equal(t, "[ExternalMCP media=Text principal=\"Alice [ops]=lead\" additional_instructions=\"line \\\"one\\\"\\nnext\"]", provenanceHeader(promptProvenance{origin: "ExternalMCP", media: "Text", principal: " Alice [ops]=lead ", additionalInstructions: "line \"one\"\nnext"}))
	assert.Equal(t, `[Slack media=Text principal="a\"b\\c"]`, provenanceHeader(promptProvenance{origin: "Slack", media: "Text", principal: "a\"b\\c"}))
	assert.Equal(t, "[Slack media=Text]", provenanceHeader(promptProvenance{origin: "Slack", media: "Text", principal: "   "}))
	assert.Equal(t, "[External_(MCP)-x media=Voice_(note)-clip]", provenanceHeader(promptProvenance{origin: "External [MCP]=x", media: "Voice [note]=clip"}))
	assert.Equal(t, promptProvenance{origin: "System", media: "Text"}, provenanceFromInbound(&events.InboundMessage{Source: events.SourceSystem, Metadata: map[string]string{events.InboundOriginMetadataKey: "Mallory", events.InboundMediaMetadataKey: "Dance"}}))
}

func TestBridgeScheduleMessageSubmitsAfterDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: SlackThreadConversationID("D123", "111.222"), SessionService: newTestSessionService(t)}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
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
			require.NotNil(t, request.inbound.SlackReply)
			assert.Equal(t, events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: "111.222"}, *request.inbound.SlackReply)
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
	require.NoError(t, store.Stop(context.Background()))

	var logs bytes.Buffer

	bridge := &Bridge{log: slog.New(slog.NewJSONHandler(&logs, nil)), config: Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", SessionService: store}}

	require.Error(t, bridge.ScheduleMessage(time.Minute, "later", false))
	assert.Contains(t, logs.String(), "scheduled message persist failed")
}

func TestBridgeLogsRocketCodeHiddenChildRunOutput(t *testing.T) {
	var logs bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	bridge := &Bridge{log: logger, config: Config{ConversationID: "slack-thread:C123:111.222"}}

	bridge.logRocketCodeChildRun(&rocketcode.ChildRunEvent{
		Kind:  rocketcode.ChildRunKindGuardrail,
		Stage: rocketcode.ChildRunStageResponse,
		Agent: "safety",
		Item:  rocketcode.ChatResponse{Kind: rocketcode.ChatResponseReasoningSummary, Text: "checking response"},
	})

	got := logs.String()
	assert.Contains(t, got, "rocketcode hidden child run output")
	assert.Contains(t, got, `"component":"rocketcode_child_run"`)
	assert.Contains(t, got, `"conversation_id":"slack-thread:C123:111.222"`)
	assert.Contains(t, got, `"child_run_kind":"guardrail"`)
	assert.Contains(t, got, `"child_run_stage":"response"`)
	assert.Contains(t, got, `"agent":"safety"`)
	assert.Contains(t, got, `"item_kind":"reasoning_summary"`)
	assert.Contains(t, got, `"text":"checking response"`)
}

func TestBridgeScheduleMessageSubmitsExternalMCPInPersistedSlackThread(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		threadKey := SlackThreadConversationID("D123", "111.222")
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
			require.NotNil(t, request.inbound.SlackReply)
			assert.Equal(t, events.SlackReplyTarget{ChannelID: "D123", MessageTS: "111.222", ThreadTS: "111.222"}, *request.inbound.SlackReply)
		case <-time.After(time.Nanosecond):
			t.Fatal("scheduled external MCP message was not submitted")
		}
	})
}

func TestBridgeInterruptCancelsTurnWaitingForPairedSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		pairID, privateID := SlackThreadConversationID("C123", "111.222"), "external_mcp:private"
		service.reserveTurnPair(pairID, privateID)

		bridge := &Bridge{runtime: &config.Config{}, config: Config{ConversationID: pairID, Agent: "main", ManagedConversationID: pairID, SessionService: service}, log: slog.New(slog.DiscardHandler)}
		require.NoError(t, bridge.Start(t.Context()))
		t.Cleanup(func() { require.NoError(t, bridge.Stop()) })

		activated := false
		inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "wait", true)
		result := inbound.EnableResponseWait()
		require.NoError(t, bridge.SubmitWhenActive(t.Context(), inbound, func(context.Context, *events.InboundMessage) error {
			activated = true
			return nil
		}))
		synctest.Wait()

		assert.Same(t, inbound, bridge.InterruptActiveTurn())
		service.completeTurnPairReservation(pairID, privateID)
		synctest.Wait()
		assert.False(t, activated)

		response := <-result
		require.ErrorIs(t, response.Err, context.Canceled)
		assert.Nil(t, bridge.InterruptActiveTurn())
	})
}

func TestBridgePermanentFirstTurnFailureReleasesManagedSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		pairID, privateID := SlackThreadConversationID("C123", "111.222"), "external_mcp:private"
		service.reserveTurnPair(pairID, privateID)

		bridge := &Bridge{runtime: &config.Config{}, config: Config{ConversationID: privateID, Agent: "planner", ManagedConversationID: pairID, ExternalConversationID: "public-1", SessionService: service}, log: slog.New(slog.DiscardHandler)}
		require.NoError(t, bridge.Start(t.Context()))
		t.Cleanup(func() { require.NoError(t, bridge.Stop()) })

		inbound := events.NewInboundMessage(events.SourceExternalMCP, events.InboundKindPrompt, "", "fail", true)
		result := inbound.EnableResponseWait()
		errActivation := errors.New("relay failed")

		require.NoError(t, bridge.SubmitWhenActive(t.Context(), inbound, func(context.Context, *events.InboundMessage) error { return errActivation }))
		synctest.Wait()
		require.ErrorIs(t, (<-result).Err, errActivation)

		unlock, err := service.lockTurnPair(t.Context(), pairID, pairID)
		require.NoError(t, err)
		unlock()
	})
}

func TestBridgeSuccessfulManagedWorkflowReleasesPairedReservation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		workspace := t.TempDir()
		writeAgent(t, workspace, "main", "---\ndescription: Main\nmodel: gpt-5.5\n---\nMain prompt\n")

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"checking workflow","annotations":[]}]},{"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"finished","annotations":[]}]}]}`))
			assert.NoError(t, err)
		}))
		t.Cleanup(server.Close)

		root, err := os.OpenRoot(workspace)
		require.NoError(t, err)
		require.NoError(t, root.MkdirAll(".rocketclaw/skills", 0o755))
		require.NoError(t, root.MkdirAll(".rocketclaw/workflows", 0o755))
		require.NoError(t, root.WriteFile(".rocketclaw/workflows/audit.star", []byte("meta = {\"name\": \"audit\", \"description\": \"Audit\", \"phases\": [\"work\", \"later\"]}\ndef main(args): return phase(\"work\", lambda: agent(\"prompt\", label=\"worker\"))\n"), 0o600))
		definitions, err := workflow.Load(root, ".rocketclaw")
		require.NoError(t, err)
		require.NoError(t, root.Close())

		service := newTestSessionServiceAt(t, workspace)
		pairID, privateID := SlackThreadConversationID("C123", "111.222"), "external_mcp:private"
		require.NoError(t, service.UpsertExternalMCPSession("public-1", &ExternalMCPSessionState{PrivateConversationID: privateID, ManagedConversationID: pairID}))
		releaseWorkflow, reserved, err := service.ReserveWorkflowTurn(pairID)
		require.NoError(t, err)
		require.True(t, reserved)

		bus := newTestBus()
		t.Cleanup(bus.Close)
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, bus: bus, config: Config{ConversationID: pairID, ManagedConversationID: pairID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, SessionService: service}}
		require.NoError(t, bridge.Start(t.Context()))
		t.Cleanup(func() { require.NoError(t, bridge.Stop()) })

		delivered := make(chan struct{})
		workflowTurnID := ""

		var agentUpdates []workflow.AgentUpdate

		go func() {
			for outbound := range bus.Outbound(t.Context()) {
				workflowTurnID = outbound.TurnID
				if outbound.WorkflowAgent != nil {
					agentUpdates = append(agentUpdates, *outbound.WorkflowAgent)
				}

				outbound.MarkDelivered(nil)

				if outbound.Complete {
					close(delivered)
					return
				}
			}
		}()

		inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "workflow", "$workflow audit", true)
		inbound.Workflow = &workflow.RunRequest{RunID: "run-1", Definition: definitions["audit"]}
		response := inbound.EnableResponseWait()
		require.NoError(t, bridge.Submit(t.Context(), inbound))
		require.NoError(t, (<-response).Err)
		<-delivered
		server.Close()
		synctest.Wait()

		entries, err := service.ObserveEntries(t.Context(), pairID, 0)
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
		require.Len(t, agentUpdates, 1)
		assert.Equal(t, "worker", agentUpdates[0].Label)
		assert.Contains(t, agentUpdates[0].PhaseID, "/phase/000000/work")
		assert.Equal(t, "checking workflow", agentUpdates[0].Activity)

		privateAcquired := false

		var unlockPrivate func()
		go func() {
			unlockPrivate, err = service.lockTurnPair(t.Context(), pairID, privateID)
			privateAcquired = err == nil
		}()

		synctest.Wait()

		if !privateAcquired {
			releaseWorkflow()
			synctest.Wait()
			t.Fatal("private turn remained blocked after managed workflow completed")
		}

		unlockPrivate()

		releaseWorkflow, reserved, err = service.ReserveWorkflowTurn(pairID)
		require.NoError(t, err)
		assert.True(t, reserved)
		releaseWorkflow()
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
		definitions, err := workflow.Load(root, ".rocketclaw")
		require.NoError(t, err)
		require.NoError(t, root.Close())

		service := newTestSessionServiceAt(t, workspace)
		conversationID := SlackThreadConversationID("C123", "111.222")
		bus := newTestBus()
		t.Cleanup(bus.Close)
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), runtime: &config.Config{Workspace: workspace}, bus: bus, config: Config{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, SessionService: service}}
		require.NoError(t, bridge.Start(t.Context()))
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

		inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "workflow", "$workflow fail", true)
		inbound.Workflow = &workflow.RunRequest{Definition: definitions["fail"]}
		response := inbound.EnableResponseWait()
		require.NoError(t, bridge.Submit(t.Context(), inbound))
		require.NoError(t, (<-response).Err)
		<-delivered
		synctest.Wait()

		entries, err := service.ObserveEntries(t.Context(), conversationID, 0)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, workflowRunEntryType, entries[0].Entry.Type)
		summary := workflowSummaryFromEntry(t, &entries[0].Entry)
		assert.Equal(t, workflow.TerminalFailed, summary.Terminal)
		assert.Equal(t, []workflowRunPhaseSummary{{Name: "work", Status: workflow.PhaseError}, {Name: "later", Status: workflow.PhaseSkipped}}, summary.Phases)
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
	definitions, err := workflow.Load(root, ".rocketclaw")
	require.NoError(t, err)
	require.NoError(t, root.Close())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, privateError, http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	service := newTestSessionServiceAt(t, workspace)
	conversationID := SlackThreadConversationID("C123", "111.222")
	bus := newTestBus()
	t.Cleanup(bus.Close)
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, bus: bus, config: Config{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, SessionService: service}}
	require.NoError(t, bridge.Start(t.Context()))
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

	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "workflow", "$workflow fail-worker", true)
	inbound.Workflow = &workflow.RunRequest{Definition: definitions["fail-worker"]}
	response := inbound.EnableResponseWait()
	require.NoError(t, bridge.Submit(t.Context(), inbound))
	require.NoError(t, (<-response).Err)
	<-delivered

	entries, err := service.ObserveEntries(t.Context(), conversationID, 0)
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
	definitions, err := workflow.Load(root, ".rocketclaw")
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
	conversationID := SlackThreadConversationID("C123", "111.222")
	bus := newTestBus()
	t.Cleanup(bus.Close)
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, bus: bus, config: Config{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, SessionService: service}}
	require.NoError(t, bridge.Start(t.Context()))
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

	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "workflow", "$workflow stop", true)
	inbound.Workflow = &workflow.RunRequest{Definition: definitions["stop"]}
	response := inbound.EnableResponseWait()
	require.NoError(t, bridge.Submit(t.Context(), inbound))
	<-requestArrived
	assert.Same(t, inbound, bridge.InterruptActiveTurn())
	close(releaseRequest)
	require.NoError(t, (<-response).Err)
	<-delivered

	entries, err := service.ObserveEntries(t.Context(), conversationID, 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	summary := workflowSummaryFromEntry(t, &entries[0].Entry)
	assert.Equal(t, workflow.TerminalStopped, summary.Terminal)
	assert.Equal(t, []workflowRunPhaseSummary{{Name: "work", Status: workflow.PhaseError, Scheduled: 1}, {Name: "later", Status: workflow.PhaseSkipped}}, summary.Phases)
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
	definitions, err := workflow.Load(root, ".rocketclaw")
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
	conversationID := SlackThreadConversationID("C123", "111.222")
	bus := newTestBus()
	t.Cleanup(bus.Close)
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, bus: bus, config: Config{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, SessionService: service}}
	require.NoError(t, bridge.Start(t.Context()))
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

	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "workflow", "$workflow private", true)
	inbound.Workflow = &workflow.RunRequest{Definition: definitions["private"]}
	response := inbound.EnableResponseWait()
	require.NoError(t, bridge.Submit(t.Context(), inbound))
	require.NoError(t, (<-response).Err)
	assert.Equal(t, "public result", <-completed)

	entries, err := service.ObserveEntries(t.Context(), conversationID, 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	encodedEntry, err := json.Marshal(entries[0].Entry)
	require.NoError(t, err)
	assert.NotContains(t, string(encodedEntry), intermediate)

	followUp := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "What happened?", true)
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
	entries, err = service.ObserveEntries(t.Context(), conversationID, 0)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "turn", entries[1].Entry.Type)
}

func TestBridgeInterruptReleasesDrainedWorkflowReservation(t *testing.T) {
	service := newTestSessionService(t)
	pairID, privateID := SlackThreadConversationID("C123", "111.222"), "external_mcp:private"
	require.NoError(t, service.UpsertExternalMCPSession("public-1", &ExternalMCPSessionState{PrivateConversationID: privateID, ManagedConversationID: pairID}))
	release, reserved, err := service.ReserveWorkflowTurn(pairID)
	require.NoError(t, err)
	require.True(t, reserved)
	t.Cleanup(release)

	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "workflow", "$workflow audit", true)
	inbound.Workflow = &workflow.RunRequest{}

	bridge := &Bridge{requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{}), config: Config{ConversationID: pairID, ManagedConversationID: pairID, SessionService: service}}
	bridge.requestCh <- bridgeRequest{inbound: inbound, activation: NoopActivationHook}

	bridge.InterruptActiveTurn()

	releaseAgain, reserved, err := service.ReserveWorkflowTurn(pairID)
	require.NoError(t, err)
	assert.True(t, reserved)
	releaseAgain()
}

func TestBridgePairLockFailureReleasesWorkflowReservation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		pairID, privateID := SlackThreadConversationID("C123", "111.222"), "external_mcp:private"
		require.NoError(t, service.UpsertExternalMCPSession("public-1", &ExternalMCPSessionState{PrivateConversationID: privateID, ManagedConversationID: pairID}))
		release, reserved, err := service.ReserveWorkflowTurn(pairID)
		require.NoError(t, err)
		require.True(t, reserved)
		t.Cleanup(release)

		unlock, err := service.lockTurnPair(t.Context(), pairID, pairID)
		require.NoError(t, err)

		bridge := &Bridge{runtime: &config.Config{}, config: Config{ConversationID: pairID, ManagedConversationID: pairID, SessionService: service}, log: slog.New(slog.DiscardHandler)}
		require.NoError(t, bridge.Start(t.Context()))
		t.Cleanup(func() { require.NoError(t, bridge.Stop()) })

		inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "workflow", "$workflow audit", true)
		inbound.Workflow = &workflow.RunRequest{}
		response := inbound.EnableResponseWait()
		require.NoError(t, bridge.Submit(t.Context(), inbound))
		synctest.Wait()
		assert.Same(t, inbound, bridge.InterruptActiveTurn())
		synctest.Wait()
		require.ErrorIs(t, (<-response).Err, context.Canceled)
		unlock()
		synctest.Wait()

		releaseAgain, reserved, err := service.ReserveWorkflowTurn(pairID)
		require.NoError(t, err)
		assert.True(t, reserved)
		releaseAgain()
	})
}

func TestBridgeUnrelatedManagedTurnDoesNotReleaseWorkflowReservation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		pairID, privateID := SlackThreadConversationID("C123", "111.222"), "external_mcp:private"
		require.NoError(t, service.UpsertExternalMCPSession("public-1", &ExternalMCPSessionState{PrivateConversationID: privateID, ManagedConversationID: pairID}))
		release, reserved, err := service.ReserveWorkflowTurn(pairID)
		require.NoError(t, err)
		require.True(t, reserved)
		t.Cleanup(release)

		bridge := &Bridge{runtime: &config.Config{}, config: Config{ConversationID: pairID, ManagedConversationID: pairID, SessionService: service}, log: slog.New(slog.DiscardHandler)}
		require.NoError(t, bridge.Start(t.Context()))
		t.Cleanup(func() { require.NoError(t, bridge.Stop()) })

		inbound := events.NewInboundMessage(events.SourceSystem, events.InboundKindPrompt, "scheduled_message", "scheduled", false)
		response := inbound.EnableResponseWait()
		errActivation := errors.New("scheduled turn stopped")

		require.NoError(t, bridge.SubmitWhenActive(t.Context(), inbound, func(context.Context, *events.InboundMessage) error { return errActivation }))
		require.ErrorIs(t, (<-response).Err, errActivation)
		synctest.Wait()

		releaseAgain, reserved, err := service.ReserveWorkflowTurn(pairID)
		require.NoError(t, err)
		assert.False(t, reserved)
		releaseAgain()
	})
}

func TestBridgeRequestReservationOwnershipPreservesManagedRecovery(t *testing.T) {
	service := newTestSessionService(t)
	pairID, privateID := SlackThreadConversationID("C123", "111.222"), "external_mcp:private"

	service.reserveTurnPair(pairID, privateID)
	private := &Bridge{config: Config{ConversationID: privateID, ManagedConversationID: pairID, SessionService: service}}
	private.completeRequestTurnPairReservation(bridgeRequest{activeTurn: new(ActiveTurnState)})

	unlocked, err := service.lockTurnPair(t.Context(), pairID, pairID)
	require.NoError(t, err)
	unlocked()

	require.NoError(t, service.UpsertExternalMCPSession("public-1", &ExternalMCPSessionState{PrivateConversationID: privateID, ManagedConversationID: pairID}))
	release, reserved, err := service.ReserveWorkflowTurn(pairID)
	require.NoError(t, err)
	require.True(t, reserved)
	t.Cleanup(release)

	managed := &Bridge{config: Config{ConversationID: pairID, ManagedConversationID: pairID, SessionService: service}}
	managed.completeRequestTurnPairReservation(bridgeRequest{activeTurn: new(ActiveTurnState)})

	releaseAgain, reserved, err := service.ReserveWorkflowTurn(pairID)
	require.NoError(t, err)
	assert.False(t, reserved)
	releaseAgain()
}

func TestBridgeScheduleMessageUsesOwningSlackThread(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		conversationID := SlackThreadConversationID("D456", "222.333")

		require.NoError(t, service.UpsertThread(SlackThreadConversationID("D123", "111.222"), ThreadState{Agent: "planner"}))

		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, Agent: "planner", SessionService: service}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
		require.NoError(t, bridge.ScheduleMessage(5*time.Second, "later", false))

		time.Sleep(5 * time.Second)
		synctest.Wait()

		select {
		case request := <-bridge.requestCh:
			require.NotNil(t, request.inbound)
			assert.Equal(t, "later", request.inbound.Text)
			assert.Equal(t, conversationID, request.inbound.ConversationID)
			require.NotNil(t, request.inbound.SlackReply)
			assert.Equal(t, events.SlackReplyTarget{ChannelID: "D456", MessageTS: "222.333", ThreadTS: "222.333"}, *request.inbound.SlackReply)
		case <-time.After(time.Nanosecond):
			t.Fatal("scheduled external MCP message was not submitted")
		}
	})
}

func TestBridgeDeletesScheduledMessageAfterSuccessfulHandling(t *testing.T) {
	workspace := t.TempDir()
	service := newTestSessionServiceAt(t, workspace)
	conversationID := SlackThreadConversationID("C123", "111.222")
	require.NoError(t, service.PutScheduledMessage("schedule-1", &ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "later", DueAt: time.Now().UTC()}))

	bus := newTestBus()
	defer bus.Close()

	bridge := NewConversation(&config.Config{Workspace: workspace}, bus, &Config{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, RequestRestart: testNoopRestart, StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.DiscardHandler))
	bridge.requestCh = make(chan bridgeRequest, 1)
	bridge.stopCh = make(chan struct{})

	go bridge.loop(t.Context())

	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "scheduled_message", "later", false)
	inbound.ConversationID = conversationID
	inbound.HadNonImageAttachments = true
	responseCh := inbound.EnableResponseWait()
	require.NoError(t, bridge.enqueue(context.Background(), bridgeRequest{inbound: inbound, scheduledMessageID: "schedule-1", activation: NoopActivationHook}, "submit scheduled message"))

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

func TestBridgeDeletesEnqueueItemWhenTurnStarts(t *testing.T) {
	workspace := t.TempDir()
	service := newTestSessionServiceAt(t, workspace)
	conversationID := SlackThreadConversationID("C123", "111.222")
	require.NoError(t, service.PutThreadQueueItem("q1", &ThreadQueueItem{ID: "q1", ConversationID: conversationID, Message: "changelog", Principal: "U1", StashAt: time.Now().UTC(), Position: 0}))

	bus := newTestBus()
	bus.Close()

	bridge := NewConversation(&config.Config{Workspace: workspace}, bus, &Config{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, RequestRestart: testNoopRestart, StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.DiscardHandler))
	bridge.requestCh = make(chan bridgeRequest, 1)
	bridge.stopCh = make(chan struct{})
	go bridge.loop(t.Context())

	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "enqueued_message", "changelog", false)
	inbound.ConversationID = conversationID
	inbound.HadNonImageAttachments = true
	responseCh := inbound.EnableResponseWait()
	require.NoError(t, bridge.enqueue(context.Background(), bridgeRequest{inbound: inbound, queueItemID: "q1", activation: NoopActivationHook}, "submit enqueued message"))

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
	conversationID := SlackThreadConversationID("C123", "111.222")
	require.NoError(t, service.PutScheduledMessage("schedule-1", &ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "later", DueAt: time.Now().UTC()}))

	bus := newTestBus()
	bus.Close()

	bridge := NewConversation(&config.Config{Workspace: workspace}, bus, &Config{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, RequestRestart: testNoopRestart, StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.DiscardHandler))
	bridge.requestCh = make(chan bridgeRequest, 1)
	bridge.stopCh = make(chan struct{})

	go bridge.loop(t.Context())

	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "scheduled_message", "later", false)
	inbound.ConversationID = conversationID
	inbound.HadNonImageAttachments = true
	responseCh := inbound.EnableResponseWait()
	require.NoError(t, bridge.enqueue(context.Background(), bridgeRequest{inbound: inbound, scheduledMessageID: "schedule-1", activation: NoopActivationHook}, "submit scheduled message"))

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
	workspace := t.TempDir()
	service := newTestSessionServiceAt(t, workspace)
	conversationID := SlackThreadConversationID("C123", "111.222")
	require.NoError(t, service.PutScheduledMessage("schedule-1", &ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "later", DueAt: time.Now().UTC(), Recurring: true, Interval: time.Minute}))

	bus := newTestBus()
	defer bus.Close()

	bridge := NewConversation(&config.Config{Workspace: workspace}, bus, &Config{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, RequestRestart: testNoopRestart, StartNewThread: testNoopStartNewThread, SessionService: service}, slog.New(slog.DiscardHandler))
	bridge.requestCh = make(chan bridgeRequest, 1)
	bridge.stopCh = make(chan struct{})

	go bridge.loop(t.Context())

	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "scheduled_message", "later", false)
	inbound.ConversationID = conversationID
	inbound.HadNonImageAttachments = true
	responseCh := inbound.EnableResponseWait()
	require.NoError(t, bridge.enqueue(context.Background(), bridgeRequest{inbound: inbound, scheduledMessageID: "schedule-1", scheduledMessageRecurring: true, activation: NoopActivationHook}, "submit scheduled message"))

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
		t.Cleanup(func() { require.NoError(t, store.Stop(context.Background())) })

		var logs lockedBuffer

		logger := slog.New(slog.NewJSONHandler(&logs, nil))
		conversationID := SlackThreadConversationID("C123", "111.222")
		bridge := NewConversation(&config.Config{Workspace: workspace}, nil, &Config{ConversationID: conversationID, Agent: "main", StartNewThread: testNoopStartNewThread, SessionService: store}, logger)
		bridge.requestCh = make(chan bridgeRequest, 1)
		bridge.stopCh = make(chan struct{})

		require.NoError(t, bridge.ScheduleMessage(5*time.Second, "later", false))
		require.NoError(t, store.PutScheduledMessage("other", &ScheduledMessageState{ConversationID: "other", Agent: "main", Message: "keep", DueAt: time.Now().UTC().Add(time.Hour)}))

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
	require.NoError(t, store.Stop(context.Background()))

	bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: "slack-thread:C123:111.222", SessionService: store}}
	require.Error(t, bridge.ResetScheduledMessages())
}

func TestBridgeArmsOverdueScheduledMessage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := newTestSessionService(t)
		conversationID := SlackThreadConversationID("C123", "111.222")
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: service}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
		due := ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "now", DueAt: time.Now().UTC().Add(-time.Second)}

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
		conversationID := SlackThreadConversationID("C123", "111.222")
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: service}, requestCh: make(chan bridgeRequest, 2), stopCh: make(chan struct{})}
		due := ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "again", DueAt: time.Now().UTC().Add(5 * time.Second), Recurring: true, Interval: time.Minute}

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
		conversationID := SlackThreadConversationID("C123", "111.222")
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: service}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
		oldDue := ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "old", DueAt: time.Now().UTC().Add(5 * time.Second)}
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
		conversationID := SlackThreadConversationID("C123", "111.222")
		bridge := &Bridge{log: slog.New(slog.DiscardHandler), config: Config{ConversationID: conversationID, SessionService: service}, requestCh: make(chan bridgeRequest, 1), stopCh: make(chan struct{})}
		due := ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "later", DueAt: time.Now().UTC().Add(5 * time.Second)}

		require.NoError(t, service.PutScheduledMessage("broken", &due))
		require.NoError(t, service.Stop(context.Background()))
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
		t.Cleanup(func() { require.NoError(t, store.Stop(context.Background())) })

		conversationID := SlackThreadConversationID("C123", "111.222")

		first := NewConversation(&config.Config{Workspace: workspace}, nil, &Config{ConversationID: conversationID, Agent: "main", StartNewThread: testNoopStartNewThread, SessionService: store}, slog.New(slog.DiscardHandler))
		require.NoError(t, first.Start(t.Context()))
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
	t.Cleanup(func() { require.NoError(t, store.Stop(context.Background())) })

	conversationID := SlackThreadConversationID("C123", "111.222")

	require.NoError(t, store.PutScheduledMessage("schedule-1", &ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "later", DueAt: time.Now().UTC().Add(time.Hour)}))

	var logs bytes.Buffer

	bus := newTestBus()
	t.Cleanup(bus.Close)
	bridge := NewConversation(&config.Config{Workspace: workspace}, bus, &Config{ConversationID: conversationID, Agent: "main", StartNewThread: testNoopStartNewThread, SessionService: store}, slog.New(slog.NewJSONHandler(&logs, nil)))
	require.NoError(t, bridge.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, bridge.Stop()) })

	assert.Contains(t, logs.String(), "scheduled message restored")
}

func TestBridgeRearmsScheduledMessagesAfterRecoveredTurnFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		workspace := t.TempDir()
		store, err := NewSessionService(workspace)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Stop(context.Background())) })

		conversationID := SlackThreadConversationID("C123", "111.222")

		require.NoError(t, store.PutScheduledMessage("schedule-1", &ScheduledMessageState{ConversationID: conversationID, Agent: "main", Message: "later", DueAt: time.Now().UTC().Add(5 * time.Second)}))

		var logs lockedBuffer

		bus := newTestBus()
		t.Cleanup(bus.Close)
		bridge := NewConversation(&config.Config{Workspace: workspace}, bus, &Config{ConversationID: conversationID, Agent: "main", RecoveringActiveTurn: true, StartNewThread: testNoopStartNewThread, SessionService: store}, slog.New(slog.NewJSONHandler(&logs, nil)))
		require.NoError(t, bridge.Start(t.Context()))
		t.Cleanup(func() { require.NoError(t, bridge.Stop()) })
		synctest.Wait()
		assert.NotContains(t, logs.String(), "scheduled message restored")

		require.NoError(t, bridge.RecoverActiveTurn(context.Background(), &ActiveTurnState{Checkpoint: rocketcode.ActiveTurnCheckpoint{TurnID: "old-turn", ConversationKey: conversationID, Agent: "main", ReplayInput: []json.RawMessage{json.RawMessage("{")}}}))
		synctest.Wait()
		assert.Contains(t, logs.String(), "handle recovered active turn")
		assert.Contains(t, logs.String(), "scheduled message restored")

		time.Sleep(5 * time.Second)
		synctest.Wait()
		assert.Contains(t, logs.String(), "scheduled message enqueued")
	})
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

	logger = logger.With("conversation_id", "main", "turn_id", "turn-1", "agent", "main", "source", string(events.SourceSlack), "kind", string(events.InboundKindPrompt), "label", "goal", "human", true, "goal_turn", true, "publish", true, "attachment_count", 2, "web_session_id", "browser-session-1")
	resolver := newModelResolver(cfg, logger)
	client, _, err := resolver.Resolve("gpt-5.5")
	require.NoError(t, err)

	_, err = client.Responses.New(context.Background(), params)
	require.Error(t, err)
	assert.Contains(t, logs.String(), "provider request failed")
	assert.Contains(t, logs.String(), `"path":"/responses"`)
	assert.Contains(t, logs.String(), `"status":429`)
	assert.Contains(t, logs.String(), `"error":"provider returned status 429"`)
	assert.Contains(t, logs.String(), `"conversation_id":"main"`)
	assert.Contains(t, logs.String(), `"turn_id":"turn-1"`)
	assert.Contains(t, logs.String(), `"agent":"main"`)
	assert.Contains(t, logs.String(), `"source":"slack"`)
	assert.Contains(t, logs.String(), `"kind":"prompt"`)
	assert.Contains(t, logs.String(), `"label":"goal"`)
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

	assert.NotContains(t, logs.String(), "provider request completed")
}

func TestRocketCodeThinkingTextHandlesStructuredToolDiagnostics(t *testing.T) {
	var call, status, hosted, hostedQueries, raw, result rocketcode.ToolDiagnostic

	call.Phase = "call"
	call.Name = "bash"
	call.Arguments = []byte(`{"command":"cat /tmp/file","description":"Read the file"}`)
	status.Phase = "call"
	status.Name = "bash"
	hosted.Phase = "call"
	hosted.Name = "websearch"
	hosted.Status = "started"
	hosted.Action = []byte(`{"type":"search","query":"Google DeepMind blog"}`)
	hostedQueries.Phase = "call"
	hostedQueries.Name = "websearch"
	hostedQueries.Status = "started"
	hostedQueries.Action = []byte(`{"type":"search","queries":["OpenAI news","Google AI blog"]}`)
	raw.Phase = "call"
	raw.Name = "custom"
	raw.Status = "started"
	raw.Arguments = []byte(`plain text`)
	result.Phase = "result"
	result.Name = "bash"
	result.Result = "file contents"

	assert.Equal(t, "Bash\nRead the file", rocketcodeThinkingText(toolResponse(&call)))
	assert.Equal(t, "Bash", rocketcodeThinkingText(toolResponse(&status)))
	assert.Equal(t, "Websearch\nGoogle DeepMind blog", rocketcodeThinkingText(toolResponse(&hosted)))
	assert.Equal(t, "Websearch\nOpenAI news, Google AI blog", rocketcodeThinkingText(toolResponse(&hostedQueries)))
	assert.Equal(t, "Custom\nplain text", rocketcodeThinkingText(toolResponse(&raw)))
	assert.Empty(t, rocketcodeThinkingText(toolResponse(&result)))

	nested := rocketcode.ToolDiagnostic{Phase: "call", Name: "execute → read", Arguments: []byte(`{"filePath":"README.md"}`)}
	assert.Equal(t, "Execute → Read: README.md", rocketcodeThinkingText(toolResponse(&nested)))

	nestedGather := rocketcode.ToolDiagnostic{Phase: "call", Name: "execute → gather → read", Arguments: []byte(`{"filePath":"README.md"}`)}
	assert.Equal(t, "Execute → Gather → Read: README.md", rocketcodeThinkingText(toolResponse(&nestedGather)))

	nestedBare := rocketcode.ToolDiagnostic{Phase: "call", Name: "execute → bash"}
	assert.Equal(t, "Execute → Bash", rocketcodeThinkingText(toolResponse(&nestedBare)))

	nestedSearch := rocketcode.ToolDiagnostic{Phase: "call", Name: "execute → search", Arguments: []byte(`{"query":"context7"}`)}
	assert.Equal(t, "Execute → Search: context7", rocketcodeThinkingText(toolResponse(&nestedSearch)))

	findSkills := rocketcode.ToolDiagnostic{Phase: "call", Name: "find_skills", Arguments: []byte(`{"query":"context7"}`)}
	assert.Equal(t, "Find Skills\ncontext7", rocketcodeThinkingText(toolResponse(&findSkills)))

	task := rocketcode.ToolDiagnostic{Phase: "call", Name: "task", Arguments: []byte(`{"description":"Run the heartbeat sweep","prompt":"do it","subagent_type":"hally-google-workspace"}`)}
	assert.Equal(t, "Task hally-google-workspace\nRun the heartbeat sweep", rocketcodeThinkingText(toolResponse(&task)))

	taskAgentOnly := rocketcode.ToolDiagnostic{Phase: "call", Name: "task", Arguments: []byte(`{"subagent_type":"helper"}`)}
	assert.Equal(t, "Task helper", rocketcodeThinkingText(toolResponse(&taskAgentOnly)))

	result.Result = `tool call denied: permission "bash" has no matching allow rule for subject "pwd". Choose a different action.`
	assert.Equal(t, result.Result, rocketcodeThinkingText(toolResponse(&result)))

	failed := rocketcode.ToolDiagnostic{
		Phase:  "result",
		Name:   "execute",
		Result: `tool call failed: execute: run code mode: execute codemode: codemode.star:2:25: invalid escape sequence \(. Choose a different action.`,
	}
	assert.Equal(t, "Execute failed\ninvalid escape sequence \\(", rocketcodeThinkingText(toolResponse(&failed)))

	mcpEOF := rocketcode.ToolDiagnostic{
		Phase:  "result",
		Name:   "execute",
		Result: `tool call failed: execute: list mcp tools: server memory: connect mcp server "memory": connection closed: calling "initialize": client is closing: EOF. Choose a different action.`,
	}
	assert.Equal(t, "Execute failed\n\"memory\" errored: \"client is closing: EOF\"", rocketcodeThinkingText(toolResponse(&mcpEOF)))

	// Successful tool bodies must not leak into thinking even if they mention denial/failure phrases.
	leaky := rocketcode.ToolDiagnostic{
		Phase:  "result",
		Name:   "execute",
		Result: `["<path>agents/cron.md</path>\ncontent mentions tool call denied: and tool call failed: in docs\n"]`,
	}
	assert.Empty(t, rocketcodeThinkingText(toolResponse(&leaky)))
}

func TestRocketCodeThinkingTextHandlesToolDiagnosticFallbacks(t *testing.T) {
	assert.Equal(t, "Tool", rocketcodeThinkingText(toolResponse(&rocketcode.ToolDiagnostic{Phase: "call"})))
	assert.Equal(t, "Custom", rocketcodeThinkingText(toolResponse(&rocketcode.ToolDiagnostic{Phase: "unknown", Name: " custom "})))
	assert.Equal(t, "Tool queued", rocketcodeThinkingText(toolResponse(&rocketcode.ToolDiagnostic{Phase: "call", Status: "queued"})))
	assert.Equal(t, "plain thought", rocketcodeThinkingText(rocketcode.ChatResponse{Text: " plain thought "}))
}

func TestRocketCodeThinkingTextHandlesSubagentToolDiagnostics(t *testing.T) {
	call := rocketcode.ToolDiagnostic{Phase: "call", Name: "bash", Arguments: []byte(`{"command":"cat /tmp/file","description":"Read the file"}`)}
	result := rocketcode.ToolDiagnostic{Phase: "result", Name: "bash", Result: "file contents"}

	assert.Equal(t, "subagent(1/20) → hally-google-workspace → tool: Bash\nRead the file", rocketcodeThinkingText(subagentToolResponse(&call)))
	assert.Empty(t, rocketcodeThinkingText(subagentToolResponse(&result)))
}

func TestRocketCodeThinkingTextSuppressesEmptyNestedSubagentDiagnostics(t *testing.T) {
	response := rocketcode.ChatResponse{
		Kind: rocketcode.ChatResponseAssistantTool,
		Subagent: &rocketcode.SubagentDiagnostic{
			Name:  "alitu-scenario-manager",
			Label: "assistant tool",
			Subagent: &rocketcode.SubagentDiagnostic{
				Name:  "alitu-scenario-manager",
				Label: "assistant tool",
				Tool:  &rocketcode.ToolDiagnostic{Phase: "result", Name: "bash", Result: "file contents"},
			},
		},
	}

	assert.Empty(t, rocketcodeThinkingText(response))
}

func TestRocketCodeThinkingTextSuppressesProviderOnlySubagentDiagnostics(t *testing.T) {
	response := rocketcode.ChatResponse{
		Kind: rocketcode.ChatResponseAssistantTool,
		Subagent: &rocketcode.SubagentDiagnostic{
			Name:  "alitu-scenario-manager",
			Label: "assistant tool",
			Provider: &rocketcode.ProviderDiagnostic{
				Phase:   "retry",
				Attempt: 2,
			},
		},
	}

	assert.Empty(t, rocketcodeThinkingText(response))
}

func TestRocketCodeThinkingTextKeepsExplicitSubagentProviderText(t *testing.T) {
	response := rocketcode.ChatResponse{
		Kind: rocketcode.ChatResponseAssistantTool,
		Subagent: &rocketcode.SubagentDiagnostic{
			Name:     "alitu-scenario-manager",
			Label:    "assistant tool",
			Index:    1,
			Total:    1,
			Text:     "provider retrying",
			Provider: &rocketcode.ProviderDiagnostic{Phase: "retry", Attempt: 2},
		},
	}

	assert.Equal(t, "subagent(1/1) → alitu-scenario-manager → tool: provider retrying", rocketcodeThinkingText(response))
}

func TestRocketCodeThinkingTextRendersBreadcrumbDiagnostics(t *testing.T) {
	delegationStarted := rocketcode.ChatResponse{
		Kind: rocketcode.ChatResponseAssistantTool,
		Subagent: &rocketcode.SubagentDiagnostic{
			Name:  "review",
			Label: "delegation",
			Index: 1,
			Total: 1,
			Text:  "started: Review",
		},
	}
	delegationFinished := rocketcode.ChatResponse{
		Kind: rocketcode.ChatResponseAssistantTool,
		Subagent: &rocketcode.SubagentDiagnostic{
			Name:  "review",
			Label: "delegation",
			Index: 1,
			Total: 1,
			Text:  "finished",
		},
	}
	guardrailReasoning := rocketcode.ChatResponse{
		Kind: rocketcode.ChatResponseAssistantTool,
		Subagent: &rocketcode.SubagentDiagnostic{
			Name:  "review",
			Index: 1,
			Total: 1,
			Subagent: &rocketcode.SubagentDiagnostic{
				Name:  "safety",
				Label: "guardrail(delegation)",
				Subagent: &rocketcode.SubagentDiagnostic{
					Label: "reasoning summary",
					Text:  "checking delegation",
				},
			},
		},
	}
	guardrailResult := rocketcode.ChatResponse{
		Kind: rocketcode.ChatResponseAssistantTool,
		Subagent: &rocketcode.SubagentDiagnostic{
			Name:  "review",
			Index: 1,
			Total: 1,
			Subagent: &rocketcode.SubagentDiagnostic{
				Name:  "safety",
				Label: "guardrail(response)",
				Text:  "reject: do not share",
				Subagent: &rocketcode.SubagentDiagnostic{
					Label: "result",
				},
			},
		},
	}
	autoApprover := rocketcode.ChatResponse{
		Kind: rocketcode.ChatResponseAssistantTool,
		Subagent: &rocketcode.SubagentDiagnostic{
			Name:  "guardian",
			Label: "auto-approver",
			Text:  "allow: Low-risk action.",
			Subagent: &rocketcode.SubagentDiagnostic{
				Label: "result",
			},
		},
	}
	nestedAutoApprover := rocketcode.ChatResponse{
		Kind: rocketcode.ChatResponseAssistantTool,
		Subagent: &rocketcode.SubagentDiagnostic{
			Name:  "review",
			Index: 1,
			Total: 1,
			Subagent: &rocketcode.SubagentDiagnostic{
				Name:  "guardian",
				Label: "auto-approver",
				Text:  "allow: Low-risk action.",
				Subagent: &rocketcode.SubagentDiagnostic{
					Label: "result",
				},
			},
		},
	}
	nestedSubagent := rocketcode.ChatResponse{
		Kind: rocketcode.ChatResponseAssistantTool,
		Subagent: &rocketcode.SubagentDiagnostic{
			Name:  "review",
			Index: 1,
			Total: 1,
			Subagent: &rocketcode.SubagentDiagnostic{
				Name:  "researcher",
				Index: 1,
				Total: 2,
				Label: "reasoning summary",
				Text:  "found context",
			},
		},
	}

	assert.Equal(t, "subagent(1/1) → review: started: Review", rocketcodeThinkingText(delegationStarted))
	assert.Equal(t, "subagent(1/1) → review: finished", rocketcodeThinkingText(delegationFinished))
	assert.Equal(t, "subagent(1/1) → review → guardrail(delegation) → safety → reasoning: checking delegation", rocketcodeThinkingText(guardrailReasoning))
	assert.Equal(t, "subagent(1/1) → review → guardrail(response) → safety → result: reject: do not share", rocketcodeThinkingText(guardrailResult))
	assert.Equal(t, "auto-approver → guardian → result: allow: Low-risk action.", rocketcodeThinkingText(autoApprover))
	assert.Equal(t, "subagent(1/1) → review → auto-approver → guardian → result: allow: Low-risk action.", rocketcodeThinkingText(nestedAutoApprover))
	assert.Equal(t, "subagent(1/1) → review → subagent(1/2) → researcher → reasoning: found context", rocketcodeThinkingText(nestedSubagent))
}

func toolResponse(tool *rocketcode.ToolDiagnostic) rocketcode.ChatResponse {
	var response rocketcode.ChatResponse

	response.Kind = rocketcode.ChatResponseAssistantTool
	response.Tool = tool

	return response
}

func subagentToolResponse(tool *rocketcode.ToolDiagnostic) rocketcode.ChatResponse {
	var response rocketcode.ChatResponse

	response.Kind = rocketcode.ChatResponseAssistantTool
	response.Subagent = &rocketcode.SubagentDiagnostic{Name: "hally-google-workspace", Label: "assistant tool", Index: 1, Total: 20, Tool: tool}

	return response
}

func TestProcessResponsePublishesStructuredToolDiagnosticsAsThinking(t *testing.T) {
	bus := newTestBus()
	defer bus.Close()

	bridge := new(Bridge)
	bridge.bus = bus
	bridge.log = slog.New(slog.DiscardHandler)
	bridge.config = Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, RequestRestart: testNoopRestart, SessionService: newTestSessionService(t)}
	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)
	inbound.ConversationID = bridge.config.ConversationID
	result := runResult{turnID: "turn-1", text: "", thinking: "", sequence: 0, sessionEntryID: 0, responseID: "", model: ""}

	var diagnostic rocketcode.ToolDiagnostic

	diagnostic.Phase = "call"
	diagnostic.Name = "bash"
	diagnostic.Arguments = []byte(`{"command":"cat /tmp/file","description":"Read the file"}`)

	require.NoError(t, bridge.processResponse(context.Background(), inbound, &result, toolResponse(&diagnostic)))

	outbound := readRocketCodeOutbound(t, bus)
	assert.Equal(t, "Bash\nRead the file", outbound.ProgressText)
	assert.Equal(t, "turn-1", outbound.TurnID)
}

func TestAskUserQuestionToolAllowsEmptyOptions(t *testing.T) {
	tool := askUserQuestionTool(events.InteractiveUserQuestionAsker(func(_ context.Context, req *events.AskUserQuestionRequest) (events.AskUserQuestionAnswer, error) {
		assert.Equal(t, "Approve?", req.Question)
		assert.Empty(t, req.Options)

		return events.AskUserQuestionAnswer{Custom: "approved", Source: events.SourceSlack}, nil
	}), &events.InboundMessage{Source: events.SourceSlack, Human: true, SlackReply: &events.SlackReplyTarget{ChannelID: "C1", MessageTS: "1", ThreadTS: "1"}})

	result, err := tool.Call(t.Context(), []byte(`{"question":"Approve?","details":"","options":[],"multiple":false}`), nil)

	require.NoError(t, err)
	assert.JSONEq(t, `{"selected":null,"custom":"approved","source":"slack"}`, result.Output)
}

func TestAskUserQuestionToolDescriptionRejectsCatchAllOptions(t *testing.T) {
	tool := askUserQuestionTool(events.InteractiveUserQuestionAsker(func(context.Context, *events.AskUserQuestionRequest) (events.AskUserQuestionAnswer, error) {
		return events.AskUserQuestionAnswer{}, nil
	}), &events.InboundMessage{Source: events.SourceSlack, Human: true, SlackReply: &events.SlackReplyTarget{ChannelID: "C1", MessageTS: "1", ThreadTS: "1"}})

	assert.Contains(t, tool.Description, "only for concrete predefined choices")
	assert.Contains(t, tool.Description, "do not include catch-all choices")
}

func TestAskUserQuestionToolFiltersRedundantCustomOptions(t *testing.T) {
	tool := askUserQuestionTool(events.InteractiveUserQuestionAsker(func(_ context.Context, req *events.AskUserQuestionRequest) (events.AskUserQuestionAnswer, error) {
		require.Len(t, req.Options, 1)
		assert.Equal(t, "High priority", req.Options[0].Label)

		return events.AskUserQuestionAnswer{Selected: []string{"high"}, Source: events.SourceSlack}, nil
	}), &events.InboundMessage{Source: events.SourceSlack, Human: true, SlackReply: &events.SlackReplyTarget{ChannelID: "C1", MessageTS: "1", ThreadTS: "1"}})

	result, err := tool.Call(t.Context(), []byte(`{"question":"Approve?","details":"","options":[{"label":"Custom answer","value":"custom_answer","description":"free text"},{"label":"High priority","value":"high","description":"Prioritize now"}],"multiple":false}`), nil)

	require.NoError(t, err)
	assert.JSONEq(t, `{"selected":["high"],"custom":"","source":"slack"}`, result.Output)
}

func TestStartNewThreadNativeTurnGate(t *testing.T) {
	assert.True(t, startNewThreadNativeTurn(&events.InboundMessage{Source: events.SourceSlack, Human: true, SlackReply: &events.SlackReplyTarget{ChannelID: "C1", MessageTS: "1"}}))
	assert.False(t, startNewThreadNativeTurn(&events.InboundMessage{Source: events.SourceSlack, Human: true, SlackReply: &events.SlackReplyTarget{ChannelID: "C1", MessageTS: "1"}, Metadata: map[string]string{events.InboundStartNewThreadDisabledMetadataKey: "true"}}))
	assert.False(t, startNewThreadNativeTurn(&events.InboundMessage{Source: events.SourceExternalMCP, Human: true}))
	assert.False(t, startNewThreadNativeTurn(&events.InboundMessage{Source: events.SourceSlack, Human: false, SlackReply: &events.SlackReplyTarget{ChannelID: "C1", MessageTS: "1"}}))
}

func TestNativeQuestionTurnGate(t *testing.T) {
	assert.True(t, nativeQuestionTurn(&events.InboundMessage{Source: events.SourceSlack, Human: true, SlackReply: &events.SlackReplyTarget{ChannelID: "C1", MessageTS: "1"}}))
	assert.False(t, nativeQuestionTurn(&events.InboundMessage{Source: events.SourceSlack, Human: true}))
	assert.False(t, nativeQuestionTurn(&events.InboundMessage{Source: events.SourceSlack, Human: false, SlackReply: &events.SlackReplyTarget{ChannelID: "C1", MessageTS: "1"}}))
	assert.False(t, nativeQuestionTurn(&events.InboundMessage{Source: events.SourceExternalMCP, Human: true, SlackReply: &events.SlackReplyTarget{ChannelID: "C1", MessageTS: "1"}}))
	assert.False(t, nativeQuestionTurn(&events.InboundMessage{Source: events.SourceSystem, Human: true, SlackReply: &events.SlackReplyTarget{ChannelID: "C1", MessageTS: "1"}}))
}

func TestAskUserQuestionToolOmitsResponseChannel(t *testing.T) {
	var got *events.AskUserQuestionRequest

	tool := askUserQuestionTool(events.InteractiveUserQuestionAsker(func(_ context.Context, req *events.AskUserQuestionRequest) (events.AskUserQuestionAnswer, error) {
		got = req

		return events.AskUserQuestionAnswer{Custom: "ok", Source: events.SourceSlack}, nil
	}), &events.InboundMessage{
		Source:         events.SourceSlack,
		Human:          true,
		ConversationID: "slack-thread:C1:1",
		Bridge:         events.BridgeSlack,
		Response:       make(chan events.Response, 1),
		SlackReply:     &events.SlackReplyTarget{ChannelID: "C1", MessageTS: "1", ThreadTS: "1"},
	})

	_, err := tool.Call(t.Context(), []byte(`{"question":"Q?","details":"","options":[],"multiple":false}`), nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, events.BridgeSlack, got.Bridge)
	assert.Equal(t, "slack-thread:C1:1", got.ConversationID)
	assert.Equal(t, &events.SlackReplyTarget{ChannelID: "C1", MessageTS: "1", ThreadTS: "1"}, got.SlackReply)
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
	tool := startNewThreadTool(func(_ context.Context, req *events.StartNewThreadRequest) (events.StartNewThreadResult, error) {
		assert.Equal(t, " literal $(date) ", req.Prompt)
		assert.Equal(t, "Child", req.Title)

		return events.StartNewThreadResult{ConversationID: "slack-thread:C1:2"}, nil
	}, &events.InboundMessage{Source: events.SourceSlack, Human: true, ConversationID: "slack-thread:C1:1", SlackReply: &events.SlackReplyTarget{ChannelID: "C1", MessageTS: "1", ThreadTS: "1"}}, "main")

	result, err := tool.Call(t.Context(), []byte(`{"title":" Child ","prompt":" literal $(date) "}`), nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"conversation_id":"slack-thread:C1:2"}`, result.Output)
}

func TestProcessResponseSuppressesProviderOnlySubagentDiagnostics(t *testing.T) {
	bus := newTestBus()
	defer bus.Close()

	bridge := new(Bridge)
	bridge.bus = bus
	bridge.config = Config{ConversationID: "slack-thread:C123:111.222", Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, RequestRestart: testNoopRestart, SessionService: newTestSessionService(t)}
	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)
	inbound.ConversationID = bridge.config.ConversationID
	result := runResult{turnID: "turn-1", text: "", thinking: "", sequence: 0, sessionEntryID: 0, responseID: "", model: ""}
	item := rocketcode.ChatResponse{
		Kind: rocketcode.ChatResponseAssistantTool,
		Subagent: &rocketcode.SubagentDiagnostic{
			Name:     "alitu-scenario-manager",
			Label:    "assistant tool",
			Provider: &rocketcode.ProviderDiagnostic{Phase: "retry", Attempt: 2},
		},
	}

	require.NoError(t, bridge.processResponse(context.Background(), inbound, &result, item))
	assert.Empty(t, result.thinking)
	assert.Zero(t, result.sequence)
}

func TestRunTurnSendsExternalMCPMetadataAsDeveloperMessage(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "planner", "---\ndescription: Planner\nmode: primary\nmodel: gpt-5.5\npermission:\n  bash:\n    \"*\": allow\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	var (
		requestBody struct {
			Input []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
				Output  any    `json:"output"`
			} `json:"input"`
		}
		errRequest error
		requests   int
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

		w.Header().Set("Content-Type", "application/json")

		if requests == 2 || requests == 5 {
			writeRawRunFunctionCall(t, w, "resp_2", "call_1", "execute", executeBashScript(`printf '%s|%s|%s' "$ROCKETCLAW_METADATA_A" "$ROCKETCLAW_METADATA_LATER_KEY" "$ROCKETCLAW_METADATA_Z"`))

			return
		}

		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}`))
	}))
	t.Cleanup(server.Close)

	bridge := new(Bridge)
	bridge.runtime = &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}
	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	managedConversationID := SlackThreadConversationID("C123", "111.222")
	require.NoError(t, service.RegisterExternalMCPConversation("public-1", "planner", &ExternalMCPSessionState{Agent: "planner", PrivateConversationID: "external_mcp:planner:private", ManagedConversationID: managedConversationID, SlackChannel: "#ops"}))
	bridge.config = Config{ConversationID: "external_mcp:planner:private", Agent: "planner", ManagedConversationID: managedConversationID, ExternalConversationID: "public-1", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, SessionService: service}
	bridge.log = slog.New(slog.DiscardHandler)

	msg := events.NewInboundMessage(events.SourceExternalMCP, events.InboundKindPrompt, "", "hello", true)
	msg.ConversationID = bridge.config.ConversationID
	msg.Metadata = map[string]string{"z": "last", "a": "first"}

	result, err := bridge.runTurn(context.Background(), msg, "turn-1", false)
	require.NoError(t, err)
	require.NoError(t, errRequest)
	assert.Equal(t, "ok", result.text)
	require.Len(t, requestBody.Input, 2)
	assert.Equal(t, "developer", requestBody.Input[0].Role)
	assert.Equal(t, "This external MCP thread has metadata:\nROCKETCLAW_CONVERSATION_ID=\"external_mcp:planner:private\"\nROCKETCLAW_METADATA_A=\"first\"\nROCKETCLAW_METADATA_Z=\"last\"", requestBody.Input[0].Content)
	assert.Equal(t, "user", requestBody.Input[1].Role)

	msg.Metadata = map[string]string{"a": "ignored", "later-key": "fresh"}
	_, err = bridge.runTurn(context.Background(), msg, "turn-2", false)
	require.NoError(t, err)

	developerMessages := []string{}

	for i := range requestBody.Input {
		if requestBody.Input[i].Role == "developer" {
			developerMessages = append(developerMessages, requestBody.Input[i].Content)
		}
	}

	assert.Contains(t, developerMessages, "This external MCP thread has metadata:\nROCKETCLAW_CONVERSATION_ID=\"external_mcp:planner:private\"\nROCKETCLAW_METADATA_A=\"first\"\nROCKETCLAW_METADATA_Z=\"last\"")
	assert.Contains(t, developerMessages, "This external MCP turn has additional metadata:\nROCKETCLAW_METADATA_LATER_KEY=\"fresh\"")

	require.NotEmpty(t, requestBody.Input)
	assert.Equal(t, "first|fresh|last", requestBody.Input[len(requestBody.Input)-1].Output)

	msg.Metadata = map[string]string{"a": "ignored"}
	_, err = bridge.runTurn(context.Background(), msg, "turn-3", false)
	require.NoError(t, err)

	for i := range requestBody.Input {
		assert.NotContains(t, requestBody.Input[i].Content, "LATER_KEY")
	}

	entries, err := service.ObserveEntries(context.Background(), bridge.config.ConversationID, 0)
	require.NoError(t, err)

	metadataEntries := 0

	for i := range entries {
		if entries[i].Entry.Type == externalMCPMetadataEntryType {
			metadataEntries++
		}
	}

	assert.Equal(t, 1, metadataEntries)

	managedEntries, err := service.ObserveEntries(context.Background(), managedConversationID, 0)
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

	managedBridge := &Bridge{runtime: bridge.runtime, config: Config{ConversationID: managedConversationID, Agent: "planner", ManagedConversationID: managedConversationID, OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, SessionService: service}, log: slog.New(slog.DiscardHandler)}
	managedMsg := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "check metadata", true)
	managedMsg.ConversationID = managedConversationID
	_, err = managedBridge.runTurn(context.Background(), managedMsg, "turn-managed", false)
	require.NoError(t, err)
	require.NotEmpty(t, requestBody.Input)
	assert.Equal(t, "first||last", requestBody.Input[len(requestBody.Input)-1].Output)
}

func TestRunTurnPreservesRecoveredExternalMCPReplayWithTransientMetadata(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "planner", "---\ndescription: Planner\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	conversationID := SlackThreadConversationID("C123", "111.222")
	metadataReplay, err := replayInputForMessage("developer", externalMCPMetadataDeveloperMessage("This external MCP thread has metadata:", externalMCPMetadataEnv(conversationID, map[string]string{"a": "first"})))
	require.NoError(t, err)
	_, err = service.AppendEntryID(context.Background(), conversationID, &rocketcode.SessionEntry{Version: 1, Type: externalMCPMetadataEntryType, Timestamp: time.Unix(1, 0).UTC(), ReplayInput: metadataReplay})
	require.NoError(t, err)

	recoveredReplay, err := rocketcode.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{{OfMessage: &responses.EasyInputMessageParam{Role: responses.EasyInputMessageRoleUser, Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("interrupted external turn")}, Type: "message"}}})
	require.NoError(t, err)

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
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"recovered","annotations":[]}]}]}`))
	}))
	t.Cleanup(server.Close)

	bridge := &Bridge{runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, config: Config{ConversationID: conversationID, Agent: "planner", ExternalConversationID: "public-1", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, SessionService: service}, log: slog.New(slog.DiscardHandler)}
	msg := events.NewInboundMessage(events.SourceExternalMCP, events.InboundKindPrompt, "restart_recovery", "Continue from the recovered restart handoff.", false)
	msg.ConversationID = conversationID
	msg.Metadata = map[string]string{"later-key": "fresh"}

	_, err = bridge.runTurn(context.Background(), msg, "turn-1", false, rocketcode.ActiveTurnCheckpoint{DisplayModel: "gpt-5.5", ReplayInput: recoveredReplay})
	require.NoError(t, err)
	require.NoError(t, errRequest)

	var input strings.Builder
	for i := range requestBody.Input {
		input.WriteString(requestBody.Input[i].Content)
		input.WriteByte('\n')
	}

	requestInput := input.String()
	threadMetadata := strings.Index(requestInput, "This external MCP thread has metadata:")
	recoveredTurn := strings.Index(requestInput, "interrupted external turn")
	transientMetadata := strings.Index(requestInput, "This external MCP turn has additional metadata:")
	require.NotEqual(t, -1, threadMetadata, "provider request missing stored metadata: %s", requestInput)
	require.NotEqual(t, -1, recoveredTurn, "provider request missing recovered replay: %s", requestInput)
	require.NotEqual(t, -1, transientMetadata, "provider request missing transient metadata: %s", requestInput)
	assert.Less(t, threadMetadata, recoveredTurn)
	assert.Less(t, recoveredTurn, transientMetadata)
}

func TestRecoveredExternalMCPActiveTurnUsesStoredSourceMetadata(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "planner", "---\ndescription: Planner\nmode: primary\nmodel: gpt-5.5\npermission:\n  bash:\n    \"*\": allow\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	conversationID := SlackThreadConversationID("C123", "111.222")
	metadataReplay, err := replayInputForMessage("developer", externalMCPMetadataDeveloperMessage("This external MCP thread has metadata:", externalMCPMetadataEnv(conversationID, map[string]string{"a": "first"})))
	require.NoError(t, err)
	_, err = service.AppendEntryID(context.Background(), conversationID, &rocketcode.SessionEntry{Version: 1, Type: externalMCPMetadataEntryType, Timestamp: time.Unix(1, 0).UTC(), ReplayInput: metadataReplay})
	require.NoError(t, err)

	recoveredReplay, err := rocketcode.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{{OfMessage: &responses.EasyInputMessageParam{Role: responses.EasyInputMessageRoleUser, Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("interrupted external turn")}, Type: "message"}}})
	require.NoError(t, err)

	var (
		requestBody struct {
			Input []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
				Output  any    `json:"output"`
			} `json:"input"`
		}
		errRequest error
		requests   int
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

		w.Header().Set("Content-Type", "application/json")

		if requests == 1 {
			writeRawRunFunctionCall(t, w, "resp_2", "call_1", "execute", executeBashScript(`printf '%s|%s' "$ROCKETCLAW_METADATA_A" "$ROCKETCLAW_METADATA_LATER_KEY"`))

			return
		}

		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"recovered","annotations":[]}]}]}`))
	}))
	t.Cleanup(server.Close)

	bus := newTestBus()
	t.Cleanup(bus.Close)
	bridge := &Bridge{runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, config: Config{ConversationID: conversationID, Agent: "planner", ExternalConversationID: "public-1", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, SessionService: service}, bus: bus, log: slog.New(slog.DiscardHandler)}
	turn := ActiveTurnState{Checkpoint: rocketcode.ActiveTurnCheckpoint{TurnID: "old-turn", ConversationKey: conversationID, Agent: "planner", Model: "gpt-5.5", DisplayModel: "gpt-5.5", ReplayInput: recoveredReplay}, SourceMetadata: map[string]string{"later-key": "fresh"}}

	var group errgroup.Group
	group.Go(func() error { return bridge.handleRecoveredActiveTurn(context.Background(), &turn) })

	for {
		outbound := readRocketCodeOutbound(t, bus)
		outbound.MarkDelivered(nil)

		if outbound.Complete {
			break
		}
	}

	require.NoError(t, group.Wait())
	require.NoError(t, errRequest)

	developerMessages := []string{}

	for i := range requestBody.Input {
		if requestBody.Input[i].Role == "developer" {
			developerMessages = append(developerMessages, requestBody.Input[i].Content)
		}
	}

	assert.Contains(t, developerMessages, "This external MCP turn has additional metadata:\nROCKETCLAW_METADATA_LATER_KEY=\"fresh\"")
	require.NotEmpty(t, requestBody.Input)
	assert.Equal(t, "first|fresh", requestBody.Input[len(requestBody.Input)-1].Output)
}

func TestRunTurnTranslatesSlackLightbulbToDirectSkill(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission:\n  skill:\n    docs-helper: allow\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills", "docs-helper"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".rocketclaw", "skills", "docs-helper", "SKILL.md"), []byte(`---
name: docs-helper
description: Write docs
---

Use this skill for docs.
Request: $ARGUMENTS
`), 0o644))

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
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	conversationID := SlackThreadConversationID("C123", "111.222")
	bridge := &Bridge{runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, config: Config{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, SessionService: service}, log: slog.New(slog.DiscardHandler)}
	msg := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "💡 docs-helper write API docs", true)
	msg.ConversationID = conversationID
	msg.Metadata = map[string]string{events.InboundPrincipalMetadataKey: "Alice"}

	result, err := bridge.runTurn(context.Background(), msg, "turn-1", false)

	require.NoError(t, err)
	require.NoError(t, errRequest)
	assert.Equal(t, "ok", result.text)
	require.Len(t, requestBody.Input, 2)
	assert.Equal(t, "developer", requestBody.Input[0].Role)
	assert.Contains(t, requestBody.Input[0].Content, "Use this skill for docs.")
	assert.Contains(t, requestBody.Input[0].Content, "Request: write API docs")
	assert.Equal(t, "user", requestBody.Input[1].Role)
	assert.Contains(t, requestBody.Input[1].Content, "[Slack media=Text principal=\"Alice\"")
	assert.NotContains(t, requestBody.Input[1].Content, "💡")
	assert.NotContains(t, requestBody.Input[1].Content, "docs-helper write API docs")
}

func TestRunTurnProjectsDifferentProviderHistoryBeforeRequest(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: work/gpt\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	conversationID := SlackThreadConversationID("C123", "111.222")
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

	bridge := &Bridge{runtime: &config.Config{Workspace: workspace, Providers: map[string]config.OpenAIConfig{"work": {APIBaseURL: server.URL}}}, config: Config{ConversationID: conversationID, Agent: "main", SessionService: service}, log: slog.New(slog.DiscardHandler)}
	msg := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)
	msg.ConversationID = conversationID
	_, err = bridge.runTurn(t.Context(), msg, "turn-1", false)
	require.NoError(t, err)
	assert.Contains(t, requestBody, providerReplayReadable)
	assert.NotContains(t, requestBody, providerReplayPrivate)

	entries, err := service.ObserveEntries(t.Context(), conversationID, 0)
	require.NoError(t, err)
	assert.Contains(t, string(entries[0].Entry.ReplayInput[0]), providerReplayPrivate)
}

func TestRecoveredActiveTurnProjectsDifferentProviderReplayBeforeRequest(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: work/gpt\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	conversationID := SlackThreadConversationID("C123", "111.222")
	checkpoint := rocketcode.ActiveTurnCheckpoint{TurnID: "old-turn", ConversationKey: conversationID, Agent: "main", Model: "gpt", DisplayModel: "openai/gpt", ResponseID: providerReplayPrivate, ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"portable-readable","id":"provider-private-sentinel"}`)}, OutputTrace: []json.RawMessage{json.RawMessage(`{"private":"provider-private-sentinel"}`)}}
	want, err := json.Marshal(checkpoint)
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
		writeRawRunMessage(t, w, "response", "message", "recovered")
	}))
	t.Cleanup(server.Close)

	bus := newTestBus()
	t.Cleanup(bus.Close)
	bridge := &Bridge{runtime: &config.Config{Workspace: workspace, Providers: map[string]config.OpenAIConfig{"work": {APIBaseURL: server.URL}}}, config: Config{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, SessionService: service}, bus: bus, log: slog.New(slog.DiscardHandler)}

	errRecovered := make(chan error, 1)
	go func() {
		errRecovered <- bridge.handleRecoveredActiveTurn(t.Context(), &ActiveTurnState{Checkpoint: checkpoint})
	}()

	for {
		outbound := readRocketCodeOutbound(t, bus)
		outbound.MarkDelivered(nil)

		if outbound.Complete {
			break
		}
	}

	require.NoError(t, <-errRecovered)
	assert.Contains(t, requestBody, providerReplayReadable)
	assert.NotContains(t, requestBody, providerReplayPrivate)

	after, err := json.Marshal(checkpoint)
	require.NoError(t, err)
	assert.Equal(t, want, after)
}

func TestRunTurnPreservesNamedProviderRecoveryBytesForSameProvider(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: work/new-model\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))
	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	var requestBody string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		requestBody = string(body)

		w.Header().Set("Content-Type", "application/json")
		writeRawRunMessage(t, w, "response", "message", "recovered")
	}))
	t.Cleanup(server.Close)

	conversationID := SlackThreadConversationID("C123", "111.222")
	bridge := &Bridge{runtime: &config.Config{Workspace: workspace, Providers: map[string]config.OpenAIConfig{"work": {APIBaseURL: server.URL}}}, config: Config{ConversationID: conversationID, Agent: "main", SessionService: service}, log: slog.New(slog.DiscardHandler)}
	checkpoint := rocketcode.ActiveTurnCheckpoint{DisplayModel: "work/old-model", ReplayInput: []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","content":"native-readable"}`),
		json.RawMessage(`{"type":"function_call","id":"provider-private-sentinel","call_id":"native-call","name":"read","arguments":"{}","status":"completed"}`),
	}}
	msg := events.NewInboundMessage(events.SourceSystem, events.InboundKindPrompt, "restart_recovery", "continue", false)
	msg.ConversationID = conversationID

	_, err = bridge.runTurn(t.Context(), msg, "turn-1", false, checkpoint)
	require.NoError(t, err)
	assert.Contains(t, requestBody, "native-readable")
	assert.Contains(t, requestBody, providerReplayPrivate)
}

func TestRunTurnWritesActiveTurnBeforeProviderAndClearsAfterSessionAppend(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	conversationID := SlackThreadConversationID("C123", "111.222")

	var errRequest error

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			errRequest = assert.AnError

			http.NotFound(w, r)

			return
		}

		turns, err := service.RecoverableActiveTurns(r.Context())
		if err != nil {
			errRequest = err
			http.Error(w, err.Error(), http.StatusInternalServerError)

			return
		}

		if assert.Len(t, turns, 1) {
			assert.Equal(t, conversationID, turns[0].Checkpoint.ConversationKey)
			assert.NotEmpty(t, turns[0].Checkpoint.ReplayInput)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}`))
	}))
	t.Cleanup(server.Close)

	bridge := &Bridge{runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, config: Config{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, SessionService: service}, log: slog.New(slog.DiscardHandler)}
	msg := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)
	msg.ConversationID = conversationID
	msg.Metadata = map[string]string{events.InboundPrincipalMetadataKey: "Alice"}

	result, err := bridge.runTurn(context.Background(), msg, "turn-1", false)
	require.NoError(t, err)
	require.NoError(t, errRequest)
	assert.Equal(t, "ok", result.text)

	entries, err := service.ObserveEntries(context.Background(), conversationID, 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	turns, err := service.RecoverableActiveTurns(context.Background())
	require.NoError(t, err)
	assert.Empty(t, turns)
}

func TestInterruptActiveTurnClearsRecoverableCheckpoint(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	service := newTestSessionServiceAt(t, workspace)

	conversationID := SlackThreadConversationID("C123", "111.222")
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
	bridge := &Bridge{log: slog.New(slog.DiscardHandler), runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, bus: bus, config: Config{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, SessionService: service}}
	require.NoError(t, bridge.Start(t.Context()))
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

	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)
	response := inbound.EnableResponseWait()
	require.NoError(t, bridge.Submit(t.Context(), inbound))
	<-requestArrived

	turns, err := service.RecoverableActiveTurns(t.Context())
	require.NoError(t, err)
	require.Len(t, turns, 1)

	bridge.InterruptActiveTurn()
	close(releaseRequest)
	require.NoError(t, (<-response).Err)
	<-delivered

	turns, err = service.RecoverableActiveTurns(t.Context())
	require.NoError(t, err)
	assert.Empty(t, turns)
}

func TestRecoveredActiveTurnPersistsDurableSessionEntry(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	replay, err := rocketcode.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		{OfMessage: &responses.EasyInputMessageParam{Role: responses.EasyInputMessageRoleUser, Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("interrupted")}, Type: "message"}},
		{OfFunctionCall: &responses.ResponseFunctionToolCallParam{Arguments: `{"filePath":"README.md"}`, CallID: "call-1", Name: "read", ID: openai.String("fc-1"), Type: "function_call"}},
	})
	require.NoError(t, err)

	var requestBody string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		requestBody = string(data)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"recovered","annotations":[]}]}]}`))
	}))
	t.Cleanup(server.Close)

	bus := newTestBus()
	t.Cleanup(bus.Close)

	outboundCtx, stopOutbound := context.WithCancel(context.Background())

	delivered := make(chan struct{})
	go func() {
		defer close(delivered)

		for outbound := range bus.Outbound(outboundCtx) {
			if outbound.Complete {
				outbound.MarkDelivered(nil)
				return
			}
		}
	}()

	conversationID := SlackThreadConversationID("C123", "111.222")
	bridge := &Bridge{runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, config: Config{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, SessionService: service}, bus: bus, log: slog.New(slog.DiscardHandler)}
	turn := ActiveTurnState{Checkpoint: rocketcode.ActiveTurnCheckpoint{TurnID: "old-turn", ConversationKey: conversationID, Agent: "main", Model: "gpt-5.5", DisplayModel: "gpt-5.5", ReplayInput: replay, OpenFunctionCalls: []rocketcode.FunctionCallCheckpoint{{CallID: "call-1", Name: "read"}}}}
	require.NoError(t, bridge.handleRecoveredActiveTurn(context.Background(), &turn))
	stopOutbound()
	<-delivered

	entries, err := service.ObserveEntries(context.Background(), conversationID, 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Contains(t, string(entries[0].Entry.ReplayInput[0]), "interrupted")
	assert.Contains(t, requestBody, "tool call aborted")
	assert.Contains(t, requestBody, "previous runtime was interrupted")
}

func TestRecoveredActiveGoalTurnUsesPersistedSlackRecipient(t *testing.T) {
	for _, tt := range []struct {
		name                string
		recipientTeamID     string
		recipientUserID     string
		wantRecipientTeamID string
		wantRecipientUserID string
	}{
		{name: "persisted recipient", recipientTeamID: "T123", recipientUserID: "U456", wantRecipientTeamID: "T123", wantRecipientUserID: "U456"},
		{name: "legacy empty recipient"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
			require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

			service, err := NewSessionService(workspace)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

			conversationID := SlackThreadConversationID("C123", "111.222")
			require.NoError(t, service.BeginGoal(conversationID, "ship it", "", 3, tt.recipientTeamID, tt.recipientUserID))

			replay, err := rocketcode.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{{OfMessage: &responses.EasyInputMessageParam{Role: responses.EasyInputMessageRoleUser, Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("interrupted")}, Type: "message"}}})
			require.NoError(t, err)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"recovered","annotations":[]}]}]}`))
			}))
			t.Cleanup(server.Close)

			bus := newTestBus()
			t.Cleanup(bus.Close)

			bridge := &Bridge{
				runtime:   &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}},
				config:    Config{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, SessionService: service},
				bus:       bus,
				log:       slog.New(slog.DiscardHandler),
				requestCh: make(chan bridgeRequest, 1),
			}
			turn := ActiveTurnState{Checkpoint: rocketcode.ActiveTurnCheckpoint{TurnID: "old-turn", ConversationKey: conversationID, Agent: "main", Model: "gpt-5.5", DisplayModel: "gpt-5.5", ReplayInput: replay}}

			errRecovered := make(chan error, 1)
			go func() { errRecovered <- bridge.handleRecoveredActiveTurn(t.Context(), &turn) }()

			for {
				outbound := readRocketCodeOutbound(t, bus)
				require.NotNil(t, outbound.SlackReply)
				assert.Equal(t, tt.wantRecipientTeamID, outbound.SlackReply.RecipientTeamID)
				assert.Equal(t, tt.wantRecipientUserID, outbound.SlackReply.RecipientUserID)
				outbound.MarkDelivered(nil)

				if outbound.Complete {
					break
				}
			}

			require.NoError(t, <-errRecovered)
		})
	}
}

func TestRecoveredActiveTurnIncludesPriorCompletedHistory(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	conversationID := SlackThreadConversationID("C123", "111.222")

	priorReplay, err := rocketcode.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		{OfMessage: &responses.EasyInputMessageParam{Role: responses.EasyInputMessageRoleUser, Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("prior question")}, Type: "message"}},
		{OfMessage: &responses.EasyInputMessageParam{Role: responses.EasyInputMessageRoleAssistant, Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("prior answer")}, Type: "message"}},
	})
	require.NoError(t, err)
	_, err = service.AppendEntryID(context.Background(), conversationID, &rocketcode.SessionEntry{Version: 1, Type: "turn", Timestamp: time.Unix(1, 0).UTC(), Model: "gpt-5.5", ReplayInput: priorReplay})
	require.NoError(t, err)

	recoveredReplay, err := rocketcode.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{{OfMessage: &responses.EasyInputMessageParam{Role: responses.EasyInputMessageRoleUser, Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("interrupted turn")}, Type: "message"}}})
	require.NoError(t, err)

	var requestInput string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)

			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		requestInput = string(body)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"gpt-5.5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"recovered","annotations":[]}]}]}`))
	}))
	t.Cleanup(server.Close)

	bridge := &Bridge{runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, config: Config{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, SessionService: service}, log: slog.New(slog.DiscardHandler)}
	msg := events.NewInboundMessage(events.SourceSystem, events.InboundKindPrompt, "restart_recovery", "Continue from the recovered restart handoff.", false)
	msg.ConversationID = conversationID
	msg.Metadata = map[string]string{events.InboundOriginMetadataKey: "System", events.InboundMediaMetadataKey: "Text", recoveredTurnMetadataKey: "true"}

	_, err = bridge.runTurn(context.Background(), msg, "turn-1", false, rocketcode.ActiveTurnCheckpoint{DisplayModel: "gpt-5.5", ReplayInput: recoveredReplay})
	require.NoError(t, err)

	priorQuestion := strings.Index(requestInput, "prior question")
	priorAnswer := strings.Index(requestInput, "prior answer")
	interruptedTurn := strings.Index(requestInput, "interrupted turn")
	require.NotEqual(t, -1, priorQuestion, "provider request missing prior completed user history: %s", requestInput)
	require.NotEqual(t, -1, priorAnswer, "provider request missing prior completed assistant history: %s", requestInput)
	require.NotEqual(t, -1, interruptedTurn, "provider request missing recovered active turn replay: %s", requestInput)
	assert.Less(t, priorQuestion, interruptedTurn)
	assert.Less(t, priorAnswer, interruptedTurn)

	entries, err := service.ObserveEntries(context.Background(), conversationID, 0)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	var savedReplay strings.Builder
	for _, raw := range entries[1].Entry.ReplayInput {
		savedReplay.Write(raw)
		savedReplay.WriteByte('\n')
	}

	assert.Contains(t, savedReplay.String(), "interrupted turn")
	assert.NotContains(t, savedReplay.String(), "prior question")
	assert.NotContains(t, savedReplay.String(), "prior answer")
}

func TestRecoveredActiveTurnCancellationBeforeReplacementLeavesOriginalRowUntouched(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	conversationID := SlackThreadConversationID("C123", "111.222")

	replay, err := rocketcode.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{{OfMessage: &responses.EasyInputMessageParam{Role: responses.EasyInputMessageRoleUser, Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("interrupted")}, Type: "message"}}})
	require.NoError(t, err)
	require.NoError(t, service.UpsertActiveTurn(context.Background(), &rocketcode.ActiveTurnCheckpoint{TurnID: "old-turn", ConversationKey: conversationID, Agent: "main", Model: "gpt-5.5", DisplayModel: "gpt-5.5", ReplayInput: replay}, nil))

	bus := newTestBus()
	t.Cleanup(bus.Close)
	bridge := &Bridge{runtime: &config.Config{Workspace: workspace}, config: Config{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, SessionService: service}, bus: bus, log: slog.New(slog.DiscardHandler)}
	turn := ActiveTurnState{Checkpoint: rocketcode.ActiveTurnCheckpoint{TurnID: "old-turn", ConversationKey: conversationID, Agent: "main", Model: "gpt-5.5", DisplayModel: "gpt-5.5", ReplayInput: replay}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = bridge.handleRecoveredActiveTurn(ctx, &turn)
	require.ErrorIs(t, err, context.Canceled)

	turn, ok, err := service.ActiveTurn(context.Background(), "old-turn")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "old-turn", turn.Checkpoint.TurnID)
}

func TestRecoveredActiveTurnPermanentFailureClearsFreshRecoveryRow(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\npermission: {}\n---\nPrompt\n")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".rocketclaw", "skills"), 0o755))

	service, err := NewSessionService(workspace)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	conversationID := SlackThreadConversationID("C123", "111.222")

	replay, err := rocketcode.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{{OfMessage: &responses.EasyInputMessageParam{Role: responses.EasyInputMessageRoleUser, Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("interrupted")}, Type: "message"}}})
	require.NoError(t, err)
	require.NoError(t, service.UpsertActiveTurn(context.Background(), &rocketcode.ActiveTurnCheckpoint{TurnID: "old-turn", ConversationKey: conversationID, Agent: "main", Model: "gpt-5.5", DisplayModel: "gpt-5.5", ReplayInput: replay}, nil))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "provider failed", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	publisher := newTestBus()
	bridge := &Bridge{runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, config: Config{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, SessionService: service}, bus: publisher, log: slog.New(slog.DiscardHandler)}
	t.Cleanup(publisher.Close)

	turn := ActiveTurnState{Checkpoint: rocketcode.ActiveTurnCheckpoint{TurnID: "old-turn", ConversationKey: conversationID, Agent: "main", Model: "gpt-5.5", DisplayModel: "gpt-5.5", ReplayInput: replay}}
	err = bridge.handleRecoveredActiveTurn(context.Background(), &turn)
	require.Error(t, err)

	turns, err := service.RecoverableActiveTurns(context.Background())
	require.NoError(t, err)
	assert.Empty(t, turns)
}

func TestRunTurnUsesSelectedAgentAdditionalInstructions(t *testing.T) {
	workspace := t.TempDir()
	writeAgent(t, workspace, "main", "---\ndescription: Main\nmode: primary\nmodel: gpt-5.5\nadditionalInstructions: Reply in one sentence.\npermission: {}\n---\nPrompt\n")
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
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })

	conversationID := SlackThreadConversationID("C123", "111.222")

	bridge := &Bridge{runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, config: Config{ConversationID: conversationID, Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, SessionService: service}, log: slog.New(slog.DiscardHandler)}
	msg := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)
	msg.ConversationID = conversationID
	msg.Metadata = map[string]string{events.InboundPrincipalMetadataKey: "Alice"}

	_, err = bridge.runTurn(context.Background(), msg, "turn-1", false)
	require.NoError(t, err)
	require.NoError(t, errRequest)

	userContent := ""

	for i := range requestBody.Input {
		if requestBody.Input[i].Role == "user" {
			userContent = requestBody.Input[i].Content
		}
	}

	assert.Equal(t, "[Slack media=Text principal=\"Alice\" additional_instructions=\"Reply in one sentence.\"]\n\nhello", userContent)
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
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })
	require.NoError(t, service.BeginGoal("thread-1", "ship it", "", 5, "", ""))
	_, err = service.UpdateGoalStatus("thread-1", GoalStatusProgress, "patched parser; checking connectors")
	require.NoError(t, err)

	bridge := &Bridge{runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, config: Config{ConversationID: "thread-1", Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, SessionService: service}, log: slog.New(slog.DiscardHandler)}
	msg := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, goalContinuationLabel, "continue", false)
	msg.ConversationID = "thread-1"

	_, err = bridge.runTurn(context.Background(), msg, "turn-1", false)
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
	t.Cleanup(func() { require.NoError(t, service.Stop(context.Background())) })
	require.NoError(t, service.BeginGoal("thread-1", "ship it", "", 5, "", ""))

	bridge := &Bridge{runtime: &config.Config{Workspace: workspace, OpenAI: config.OpenAIConfig{APIBaseURL: server.URL}}, config: Config{ConversationID: "thread-1", Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, SessionService: service}, log: slog.New(slog.DiscardHandler)}
	msg := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, goalContinuationLabel, "continue", false)
	msg.ConversationID = "thread-1"

	_, err = bridge.runTurn(context.Background(), msg, "turn-1", false)
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
	bridge.config = Config{ConversationID: "thread-1", Agent: "main", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}, RequestRestart: testNoopRestart, SessionService: store}

	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "", "hello", true)
	inbound.ConversationID = "thread-1"
	inbound.SlackReply = &events.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.2", ThreadTS: "111.1", RecipientTeamID: "T123", RecipientUserID: "U456"}
	assert.False(t, bridge.newOutboundMessage(inbound, "turn-1", 1, "reply", "", false).GoalTurn)

	require.NoError(t, store.BeginGoal("thread-1", "ship it", "", 3, "", ""))

	outbound := bridge.newOutboundMessage(inbound, "turn-2", 1, "reply", "", false)
	assert.True(t, outbound.GoalTurn)
	assert.True(t, outbound.GoalActive)
	assert.Equal(t, "main", outbound.Agent)
	assert.Equal(t, inbound.SlackReply, outbound.SlackReply)
	assert.Equal(t, 1, outbound.GoalTurnNumber)
	assert.Equal(t, 3, outbound.GoalMaxTurns)

	inbound.Label = goalContinuationLabel
	_, _, err := store.AccountGoalTurn("thread-1")
	require.NoError(t, err)

	outbound = bridge.newOutboundMessage(inbound, "turn-3", 1, "reply", "", false)
	assert.True(t, outbound.GoalTurn)
	assert.True(t, outbound.GoalActive)
	assert.Equal(t, 2, outbound.GoalTurnNumber)
	assert.Equal(t, 3, outbound.GoalMaxTurns)

	inbound.Label = ""
	outbound = bridge.newOutboundMessage(inbound, "turn-4", 1, "reply", "", false)
	assert.True(t, outbound.GoalTurn)
	assert.True(t, outbound.GoalActive)
	assert.Equal(t, 2, outbound.GoalTurnNumber)
	assert.Equal(t, 3, outbound.GoalMaxTurns)

	inbound.Label = goalContinuationLabel
	_, _, err = store.AccountGoalTurn("thread-1")
	require.NoError(t, err)

	outbound = bridge.newOutboundMessage(inbound, "turn-4b", 1, "reply", "", false)
	assert.True(t, outbound.GoalTurn)
	assert.False(t, outbound.GoalActive)
	assert.Equal(t, 3, outbound.GoalTurnNumber)

	require.NoError(t, store.BeginGoal("thread-2", "ship it forever", "", 0, "", ""))

	bridge.config.ConversationID = "thread-2"
	outbound = bridge.newOutboundMessage(inbound, "turn-5", 1, "reply", "", false)
	assert.True(t, outbound.GoalTurn)
	assert.True(t, outbound.GoalActive)
	assert.Zero(t, outbound.GoalTurnNumber)
	assert.Zero(t, outbound.GoalMaxTurns)

	_, err = store.UpdateGoalStatus("thread-2", GoalStatusBlocked, "need credentials")
	require.NoError(t, err)

	outbound = bridge.newOutboundMessage(inbound, "turn-6", 1, "reply", "", false)
	assert.True(t, outbound.GoalTurn)
	assert.False(t, outbound.GoalActive)
}

func TestWorkflowProgressOutboundDoesNotLookupGoal(t *testing.T) {
	bus := newTestBus()
	defer bus.Close()

	bridge := &Bridge{bus: bus, config: Config{ConversationID: "thread-1", OutputTargets: []events.OutputTarget{events.OutputTargetSlack}}}
	inbound := events.NewInboundMessage(events.SourceSlack, events.InboundKindPrompt, "workflow", "$workflow audit", true)
	inbound.Workflow = new(workflow.RunRequest)
	inbound.SlackReply = &events.SlackReplyTarget{ChannelID: "C123", MessageTS: "111.2", ThreadTS: "111.1", RecipientTeamID: "T123", RecipientUserID: "U456"}
	phase := workflow.PhaseUpdate{PhaseID: "turn-1/phase/audit", Name: "audit", Status: workflow.PhaseInProgress}

	outbound := bridge.newOutboundMessage(inbound, "turn-1", 1, "", "", false)
	outbound.WorkflowPhase = &phase
	require.NoError(t, bus.PublishOutbound(t.Context(), outbound))

	published := readRocketCodeOutbound(t, bus)
	assert.Equal(t, &phase, published.WorkflowPhase)
	assert.Equal(t, inbound.SlackReply, published.SlackReply)

	agent := workflow.AgentUpdate{CallID: "turn-1/agent/000000", Label: "failure-trace", Activity: "grep: turn limit"}
	outbound = bridge.newOutboundMessage(inbound, "turn-1", 2, "", "", false)
	outbound.WorkflowAgent = &agent
	require.NoError(t, bus.PublishOutbound(t.Context(), outbound))

	published = readRocketCodeOutbound(t, bus)
	assert.Equal(t, &agent, published.WorkflowAgent)
	assert.Nil(t, published.WorkflowPhase)
	assert.Empty(t, published.ProgressText)
	assert.Equal(t, inbound.SlackReply, published.SlackReply)
}

func readRocketCodeOutbound(t *testing.T, bus *testBus) *events.OutboundMessage {
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
	assert.Equal(t, ".rocketclaw/.rocketcode/tmp/slack-thread_C123_111.222", rocketcodeShellTempRel(".rocketclaw", "slack-thread:C123:111.222"))
	assert.Equal(t, "runtime/.rocketcode/tmp/cron_job", rocketcodeShellTempRel("runtime", "cron:job"))
}
