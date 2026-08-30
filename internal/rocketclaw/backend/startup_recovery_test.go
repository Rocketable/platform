package backend

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"log/slog"
	"slices"
	"testing"

	"github.com/Rocketable/platform/internal/rocketcode"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeStartupRecoveryStore struct {
	turns    []ActiveTurnState
	deleted  []string
	stopped  []string
	errClear error
	errStop  error
}

func (f *fakeStartupRecoveryStore) RecoverableActiveTurns(context.Context) ([]ActiveTurnState, error) {
	return slices.Clone(f.turns), nil
}

func (f *fakeStartupRecoveryStore) ClearActiveTurn(_ context.Context, turnID string) error {
	if f.errClear != nil {
		return f.errClear
	}

	f.deleted = append(f.deleted, turnID)

	return nil
}

func (f *fakeStartupRecoveryStore) StopGoal(conversationID string) error {
	if f.errStop != nil {
		return f.errStop
	}

	f.stopped = append(f.stopped, conversationID)

	return nil
}

func (f *fakeStartupRecoveryStore) Thread(conversationID string) (ThreadState, bool, error) {
	return ThreadState{Agent: "main"}, conversationID != "unknown", nil
}

func (f *fakeStartupRecoveryStore) ExternalMCPSessionByConversationID(conversationID string) (externalConversationID string, session ExternalMCPSessionState, ok bool, err error) {
	if conversationID == "external_mcp:planner:private" {
		return "public-1", ExternalMCPSessionState{Agent: "planner", PrivateConversationID: conversationID, ManagedConversationID: "slack-thread:C1:1.1", SlackChannel: "#ops"}, true, nil
	}

	return "", ExternalMCPSessionState{}, false, nil
}

func TestRecoverStartupActiveTurnsSelectsAtMostOnePerConversation(t *testing.T) {
	replay := startupRecoveryReplayInput(t)
	store := &fakeStartupRecoveryStore{turns: []ActiveTurnState{
		startupRecoveryTurn("turn-new", "conversation-1", replay),
		startupRecoveryTurn("turn-old", "conversation-1", replay),
		startupRecoveryTurn("turn-other", "conversation-2", replay),
	}}

	var handed []ActiveTurnState

	err := recoverStartupActiveTurns(context.Background(), store, func(_ context.Context, turn *ActiveTurnState) error {
		handed = append(handed, *turn)

		return nil
	}, func(string, []protocol.PendingSteer) {}, slog.New(slog.DiscardHandler))

	require.NoError(t, err)
	require.Equal(t, []string{"turn-old"}, store.deleted)
	require.Len(t, handed, 2)
	require.Equal(t, "turn-new", handed[0].Checkpoint.TurnID)
	require.Equal(t, "turn-other", handed[1].Checkpoint.TurnID)
}

func TestRecoverStartupActiveTurnsAcceptsPrivateExternalMCPConversation(t *testing.T) {
	replay := startupRecoveryReplayInput(t)
	store := &fakeStartupRecoveryStore{turns: []ActiveTurnState{startupRecoveryTurn("turn-mcp", "external_mcp:planner:private", replay)}}

	var handed []ActiveTurnState

	require.NoError(t, recoverStartupActiveTurns(t.Context(), store, func(_ context.Context, turn *ActiveTurnState) error {
		handed = append(handed, *turn)
		return nil
	}, func(string, []protocol.PendingSteer) {}, slog.New(slog.DiscardHandler)))

	require.Len(t, handed, 1)
	assert.Equal(t, "external_mcp:planner:private", handed[0].Checkpoint.ConversationKey)
	assert.Empty(t, store.deleted)
}

func TestRecoverStartupActiveTurnsDeletesCompetingRowsAfterCorruptSelectedRow(t *testing.T) {
	replay := startupRecoveryReplayInput(t)
	store := &fakeStartupRecoveryStore{turns: []ActiveTurnState{
		startupRecoveryTurn("turn-corrupt", "conversation-1", []json.RawMessage{json.RawMessage(`{`)}),
		startupRecoveryTurn("turn-old", "conversation-1", replay),
	}}

	handoffCalled := false
	err := recoverStartupActiveTurns(context.Background(), store, func(context.Context, *ActiveTurnState) error {
		handoffCalled = true

		return nil
	}, func(string, []protocol.PendingSteer) {}, slog.New(slog.DiscardHandler))

	require.NoError(t, err)
	require.True(t, handoffCalled)
	require.Equal(t, []string{"turn-corrupt"}, store.deleted)
}

func TestRecoverStartupActiveTurnsDeletesCorruptRows(t *testing.T) {
	store := &fakeStartupRecoveryStore{turns: []ActiveTurnState{
		startupRecoveryTurn("turn-corrupt", "conversation-1", []json.RawMessage{json.RawMessage(`{`)}),
	}}

	handoffCalled := false
	err := recoverStartupActiveTurns(context.Background(), store, func(context.Context, *ActiveTurnState) error {
		handoffCalled = true

		return nil
	}, func(string, []protocol.PendingSteer) {}, slog.New(slog.DiscardHandler))

	require.NoError(t, err)
	require.False(t, handoffCalled)
	require.Equal(t, []string{"turn-corrupt"}, store.deleted)
}

func TestRecoverStartupActiveTurnsDeletesInvalidRows(t *testing.T) {
	replay := startupRecoveryReplayInput(t)
	store := &fakeStartupRecoveryStore{turns: []ActiveTurnState{startupRecoveryTurn("turn-invalid", " ", replay)}}

	handoffCalled := false
	err := recoverStartupActiveTurns(context.Background(), store, func(context.Context, *ActiveTurnState) error {
		handoffCalled = true

		return nil
	}, func(string, []protocol.PendingSteer) {}, slog.New(slog.DiscardHandler))

	require.NoError(t, err)
	require.False(t, handoffCalled)
	require.Equal(t, []string{"turn-invalid"}, store.deleted)
}

func TestRecoverStartupActiveTurnsHandsOffRecoveredReplay(t *testing.T) {
	replay := startupRecoveryOpenCallReplayInput(t, "call-1", "task")
	store := &fakeStartupRecoveryStore{turns: []ActiveTurnState{startupRecoveryTurn("turn-1", "conversation-1", replay)}}
	store.turns[0].Checkpoint.OpenFunctionCalls = []rocketcode.FunctionCallCheckpoint{{CallID: "call-1", Name: "task"}}

	var handed ActiveTurnState

	err := recoverStartupActiveTurns(context.Background(), store, func(_ context.Context, turn *ActiveTurnState) error {
		handed = *turn

		return nil
	}, func(string, []protocol.PendingSteer) {}, slog.New(slog.DiscardHandler))

	require.NoError(t, err)
	require.Empty(t, store.deleted)

	items, err := rocketcode.ReplayInputToParams(handed.Checkpoint.ReplayInput)
	require.NoError(t, err)
	require.Len(t, items, 4)
	require.Equal(t, "function_call_output", *items[2].GetType())
	require.Equal(t, "call-1", *items[2].GetCallID())
	require.Contains(t, startupRecoveryReplayJSON(t, items[2]), "subagent task aborted")
	require.Equal(t, "developer", *items[3].GetRole())
	require.Contains(t, startupRecoveryReplayJSON(t, items[3]), "previous runtime was interrupted")
}

func TestRecoverStartupActiveTurnsDeletesPermanentHandoffFailures(t *testing.T) {
	errHandoff := errors.New("enqueue failed")
	replay := startupRecoveryReplayInput(t)
	store := &fakeStartupRecoveryStore{turns: []ActiveTurnState{startupRecoveryTurn("turn-1", "conversation-1", replay)}}

	err := recoverStartupActiveTurns(context.Background(), store, func(context.Context, *ActiveTurnState) error {
		return errHandoff
	}, func(string, []protocol.PendingSteer) {}, slog.New(slog.DiscardHandler))

	require.NoError(t, err)
	require.Equal(t, []string{"turn-1"}, store.deleted)
	require.Equal(t, []string{"conversation-1"}, store.stopped)
}

type fakeSteerSurface struct {
	restored  []string
	discarded []int
}

func (f *fakeSteerSurface) RestorePendingSteers(conversationID string, _ []protocol.PendingSteer) {
	f.restored = append(f.restored, conversationID)
}

func (f *fakeSteerSurface) DiscardPendingSteers(context.Context, []protocol.PendingSteer) {
	f.discarded = append(f.discarded, 1)
}

func TestApplyStartupSteerRecoveryRestoresThenPicks(t *testing.T) {
	slack := &fakeSteerSurface{}

	var picked []string

	err := applyStartupSteerRecovery(t.Context(), slack, func(_ context.Context, conversationID string) error {
		picked = append(picked, conversationID)
		return nil
	}, []ActiveTurnState{startupRecoveryTurn("turn-1", "conversation-1", nil)}, []cannotResumeItem{{conversationID: "conversation-2", steers: []protocol.PendingSteer{{Text: "later"}}}})
	require.NoError(t, err)
	assert.Equal(t, []string{"conversation-1"}, slack.restored)
	assert.Equal(t, []int{1}, slack.discarded)
	assert.Equal(t, []string{"conversation-2"}, picked)
	err = applyStartupSteerRecovery(t.Context(), slack, func(context.Context, string) error {
		return errors.New("pick failed")
	}, nil, []cannotResumeItem{{conversationID: "conversation-2"}})
	require.ErrorContains(t, err, "pick later work after unresumable turn")
}

func TestCannotResumeActiveTurnWrapsStoreErrors(t *testing.T) {
	turn := startupRecoveryTurn("turn-1", "conversation-1", nil)
	err := cannotResumeActiveTurn(t.Context(), &fakeStartupRecoveryStore{errClear: errors.New("clear failed")}, &turn, func(string, []protocol.PendingSteer) {})
	require.ErrorContains(t, err, "clear unresumable active turn")
	err = cannotResumeActiveTurn(t.Context(), &fakeStartupRecoveryStore{errStop: errors.New("stop failed")}, &turn, func(string, []protocol.PendingSteer) {})
	require.ErrorContains(t, err, "stop goal after unresumable turn")
}

func TestRecoverStartupActiveTurnsCannotResumeStopsGoalAndReportsSteers(t *testing.T) {
	store := &fakeStartupRecoveryStore{turns: []ActiveTurnState{startupRecoveryTurn("turn-1", "conversation-1", []json.RawMessage{json.RawMessage(`{`)})}}
	store.turns[0].PendingSteers = []protocol.PendingSteer{{Text: "don't touch the database", SlackChannel: "C123", SlackTS: "222.333", SlackThreadTS: "111.222"}}

	var (
		gotID     string
		gotSteers []protocol.PendingSteer
	)

	err := recoverStartupActiveTurns(context.Background(), store, func(context.Context, *ActiveTurnState) error {
		t.Fatal("handoff should not run")
		return nil
	}, func(conversationID string, steers []protocol.PendingSteer) {
		gotID = conversationID
		gotSteers = steers
	}, slog.New(slog.DiscardHandler))

	require.NoError(t, err)
	assert.Equal(t, "conversation-1", gotID)
	assert.Equal(t, store.turns[0].PendingSteers, gotSteers)
	assert.Equal(t, []string{"conversation-1"}, store.stopped)
}

func TestRecoverStartupActiveTurnsLeavesRowsOnCanceledHandoff(t *testing.T) {
	replay := startupRecoveryReplayInput(t)

	for _, errHandoff := range []error{context.Canceled, context.DeadlineExceeded} {
		store := &fakeStartupRecoveryStore{turns: []ActiveTurnState{startupRecoveryTurn("turn-1", "conversation-1", replay)}}

		err := recoverStartupActiveTurns(context.Background(), store, func(context.Context, *ActiveTurnState) error {
			return errHandoff
		}, func(string, []protocol.PendingSteer) {}, slog.New(slog.DiscardHandler))

		require.ErrorIs(t, err, errHandoff)
		require.Empty(t, store.deleted)
	}
}

func TestRecoverStartupActiveTurnsLeavesRowsWhenBridgeStopped(t *testing.T) {
	replay := startupRecoveryReplayInput(t)
	store := &fakeStartupRecoveryStore{turns: []ActiveTurnState{startupRecoveryTurn("turn-1", "conversation-1", replay)}}
	bridge := NewConversation(nil, nil, &Config{ConversationID: "conversation-1", RecoveringActiveTurn: true}, slog.New(slog.DiscardHandler))

	require.NoError(t, bridge.Start(context.Background()))
	require.NoError(t, bridge.Stop())

	err := recoverStartupActiveTurns(context.Background(), store, func(ctx context.Context, turn *ActiveTurnState) error {
		return bridge.RecoverActiveTurn(ctx, turn)
	}, func(string, []protocol.PendingSteer) {}, slog.New(slog.DiscardHandler))

	require.Error(t, err)
	require.True(t, IsBridgeStopped(err))
	require.Empty(t, store.deleted)
}

func TestRecoverStartupActiveTurnsDeletesRawCronRows(t *testing.T) {
	replay := startupRecoveryReplayInput(t)
	store := &fakeStartupRecoveryStore{turns: []ActiveTurnState{
		startupRecoveryTurn("turn-cron", "cron:cron/daily.md:20000102T030405.000000006Z:abc", replay),
		startupRecoveryTurn("turn-one-off", "one-off-cron:cron/daily.md:20000102T030405.000000006Z:def", replay),
		startupRecoveryTurn("turn-external", "external_mcp:planner:private", replay),
	}}

	var handed []ActiveTurnState

	err := recoverStartupActiveTurns(context.Background(), store, func(_ context.Context, turn *ActiveTurnState) error {
		handed = append(handed, *turn)

		return nil
	}, func(string, []protocol.PendingSteer) {}, slog.New(slog.DiscardHandler))

	require.NoError(t, err)
	require.Equal(t, []string{"turn-cron", "turn-one-off"}, store.deleted)
	require.Len(t, handed, 1)
	require.Equal(t, "turn-external", handed[0].Checkpoint.TurnID)
}

func startupRecoveryTurn(turnID, conversationID string, replay []json.RawMessage) ActiveTurnState {
	return ActiveTurnState{Checkpoint: rocketcode.ActiveTurnCheckpoint{
		TurnID:          turnID,
		ConversationKey: conversationID,
		Agent:           "planner",
		Model:           "gpt-5.5",
		DisplayModel:    "gpt-5.5",
		ReplayInput:     replay,
	}}
}

func startupRecoveryReplayInput(t *testing.T) []json.RawMessage {
	t.Helper()

	replay, err := rocketcode.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{{OfMessage: &responses.EasyInputMessageParam{
		Role:    responses.EasyInputMessageRoleUser,
		Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("hello")},
		Type:    "message",
	}}})
	require.NoError(t, err)

	return replay
}

func startupRecoveryOpenCallReplayInput(t *testing.T, callID, name string) []json.RawMessage {
	t.Helper()

	replay, err := rocketcode.ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		{OfMessage: &responses.EasyInputMessageParam{
			Role:    responses.EasyInputMessageRoleUser,
			Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String("hello")},
			Type:    "message",
		}},
		{OfFunctionCall: &responses.ResponseFunctionToolCallParam{Arguments: `{"description":"work"}`, CallID: callID, Name: name, ID: openai.String("fc-1"), Type: "function_call"}},
	})
	require.NoError(t, err)

	return replay
}

func startupRecoveryReplayJSON(t *testing.T, value any) string {
	t.Helper()

	data, err := json.Marshal(value)
	require.NoError(t, err)

	return string(data)
}
