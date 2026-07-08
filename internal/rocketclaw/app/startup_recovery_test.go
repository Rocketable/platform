package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"testing"

	"github.com/Rocketable/platform/internal/rocketclaw/harnessbridge"
	"github.com/Rocketable/platform/internal/rocketcode"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
)

type fakeStartupRecoveryStore struct {
	turns   []harnessbridge.ActiveTurnState
	deleted []string
}

func (f *fakeStartupRecoveryStore) RecoverableActiveTurns(context.Context) ([]harnessbridge.ActiveTurnState, error) {
	return slices.Clone(f.turns), nil
}

func (f *fakeStartupRecoveryStore) ClearActiveTurn(_ context.Context, turnID string) error {
	f.deleted = append(f.deleted, turnID)

	return nil
}

func TestRecoverStartupActiveTurnsSelectsAtMostOnePerConversation(t *testing.T) {
	replay := startupRecoveryReplayInput(t)
	store := &fakeStartupRecoveryStore{turns: []harnessbridge.ActiveTurnState{
		startupRecoveryTurn("turn-new", "conversation-1", replay),
		startupRecoveryTurn("turn-old", "conversation-1", replay),
		startupRecoveryTurn("turn-other", "conversation-2", replay),
	}}

	var handed []harnessbridge.ActiveTurnState

	err := recoverStartupActiveTurns(context.Background(), store, func(_ context.Context, turn *harnessbridge.ActiveTurnState) error {
		handed = append(handed, *turn)

		return nil
	}, slog.New(slog.DiscardHandler))

	require.NoError(t, err)
	require.Equal(t, []string{"turn-old"}, store.deleted)
	require.Len(t, handed, 2)
	require.Equal(t, "turn-new", handed[0].Checkpoint.TurnID)
	require.Equal(t, "turn-other", handed[1].Checkpoint.TurnID)
}

func TestRecoverStartupActiveTurnsDeletesCompetingRowsAfterCorruptSelectedRow(t *testing.T) {
	replay := startupRecoveryReplayInput(t)
	store := &fakeStartupRecoveryStore{turns: []harnessbridge.ActiveTurnState{
		startupRecoveryTurn("turn-corrupt", "conversation-1", []json.RawMessage{json.RawMessage(`{`)}),
		startupRecoveryTurn("turn-old", "conversation-1", replay),
	}}

	handoffCalled := false
	err := recoverStartupActiveTurns(context.Background(), store, func(context.Context, *harnessbridge.ActiveTurnState) error {
		handoffCalled = true

		return nil
	}, slog.New(slog.DiscardHandler))

	require.NoError(t, err)
	require.True(t, handoffCalled)
	require.Equal(t, []string{"turn-corrupt"}, store.deleted)
}

func TestRecoverStartupActiveTurnsDeletesCorruptRows(t *testing.T) {
	store := &fakeStartupRecoveryStore{turns: []harnessbridge.ActiveTurnState{
		startupRecoveryTurn("turn-corrupt", "conversation-1", []json.RawMessage{json.RawMessage(`{`)}),
	}}

	handoffCalled := false
	err := recoverStartupActiveTurns(context.Background(), store, func(context.Context, *harnessbridge.ActiveTurnState) error {
		handoffCalled = true

		return nil
	}, slog.New(slog.DiscardHandler))

	require.NoError(t, err)
	require.False(t, handoffCalled)
	require.Equal(t, []string{"turn-corrupt"}, store.deleted)
}

func TestRecoverStartupActiveTurnsDeletesInvalidRows(t *testing.T) {
	replay := startupRecoveryReplayInput(t)
	store := &fakeStartupRecoveryStore{turns: []harnessbridge.ActiveTurnState{startupRecoveryTurn("turn-invalid", " ", replay)}}

	handoffCalled := false
	err := recoverStartupActiveTurns(context.Background(), store, func(context.Context, *harnessbridge.ActiveTurnState) error {
		handoffCalled = true

		return nil
	}, slog.New(slog.DiscardHandler))

	require.NoError(t, err)
	require.False(t, handoffCalled)
	require.Equal(t, []string{"turn-invalid"}, store.deleted)
}

func TestRecoverStartupActiveTurnsHandsOffRecoveredReplay(t *testing.T) {
	replay := startupRecoveryOpenCallReplayInput(t, "call-1", "task")
	store := &fakeStartupRecoveryStore{turns: []harnessbridge.ActiveTurnState{startupRecoveryTurn("turn-1", "conversation-1", replay)}}
	store.turns[0].Checkpoint.OpenFunctionCalls = []rocketcode.FunctionCallCheckpoint{{CallID: "call-1", Name: "task"}}

	var handed harnessbridge.ActiveTurnState

	err := recoverStartupActiveTurns(context.Background(), store, func(_ context.Context, turn *harnessbridge.ActiveTurnState) error {
		handed = *turn

		return nil
	}, slog.New(slog.DiscardHandler))

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
	store := &fakeStartupRecoveryStore{turns: []harnessbridge.ActiveTurnState{startupRecoveryTurn("turn-1", "conversation-1", replay)}}

	err := recoverStartupActiveTurns(context.Background(), store, func(context.Context, *harnessbridge.ActiveTurnState) error {
		return errHandoff
	}, slog.New(slog.DiscardHandler))

	require.NoError(t, err)
	require.Equal(t, []string{"turn-1"}, store.deleted)
}

func TestRecoverStartupActiveTurnsLeavesRowsOnCanceledHandoff(t *testing.T) {
	replay := startupRecoveryReplayInput(t)

	for _, errHandoff := range []error{context.Canceled, context.DeadlineExceeded} {
		store := &fakeStartupRecoveryStore{turns: []harnessbridge.ActiveTurnState{startupRecoveryTurn("turn-1", "conversation-1", replay)}}

		err := recoverStartupActiveTurns(context.Background(), store, func(context.Context, *harnessbridge.ActiveTurnState) error {
			return errHandoff
		}, slog.New(slog.DiscardHandler))

		require.ErrorIs(t, err, errHandoff)
		require.Empty(t, store.deleted)
	}
}

func TestRecoverStartupActiveTurnsLeavesRowsWhenBridgeStopped(t *testing.T) {
	replay := startupRecoveryReplayInput(t)
	store := &fakeStartupRecoveryStore{turns: []harnessbridge.ActiveTurnState{startupRecoveryTurn("turn-1", "conversation-1", replay)}}
	bridge := harnessbridge.NewConversation(nil, nil, &harnessbridge.Config{ConversationID: "conversation-1", RecoveringActiveTurn: true}, slog.New(slog.DiscardHandler))

	require.NoError(t, bridge.Start(context.Background()))
	require.NoError(t, bridge.Stop())

	err := recoverStartupActiveTurns(context.Background(), store, func(ctx context.Context, turn *harnessbridge.ActiveTurnState) error {
		return bridge.RecoverActiveTurn(ctx, turn)
	}, slog.New(slog.DiscardHandler))

	require.Error(t, err)
	require.True(t, harnessbridge.IsBridgeStopped(err))
	require.Empty(t, store.deleted)
}

func TestRecoverStartupActiveTurnsDeletesRawCronRows(t *testing.T) {
	replay := startupRecoveryReplayInput(t)
	store := &fakeStartupRecoveryStore{turns: []harnessbridge.ActiveTurnState{
		startupRecoveryTurn("turn-cron", "cron:cron/daily.md:20000102T030405.000000006Z:abc", replay),
		startupRecoveryTurn("turn-one-off", "one-off-cron:cron/daily.md:20000102T030405.000000006Z:def", replay),
		startupRecoveryTurn("turn-external", "external_mcp:planner:private", replay),
	}}

	var handed []harnessbridge.ActiveTurnState

	err := recoverStartupActiveTurns(context.Background(), store, func(_ context.Context, turn *harnessbridge.ActiveTurnState) error {
		handed = append(handed, *turn)

		return nil
	}, slog.New(slog.DiscardHandler))

	require.NoError(t, err)
	require.Equal(t, []string{"turn-cron", "turn-one-off"}, store.deleted)
	require.Len(t, handed, 1)
	require.Equal(t, "turn-external", handed[0].Checkpoint.TurnID)
}

func startupRecoveryTurn(turnID, conversationID string, replay []json.RawMessage) harnessbridge.ActiveTurnState {
	return harnessbridge.ActiveTurnState{Checkpoint: rocketcode.ActiveTurnCheckpoint{
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
