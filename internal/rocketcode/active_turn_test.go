package rocketcode

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func TestTurnObservationsSurviveStaleCheckpoints(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sink := recordingCheckpointSink()
		owner := turnObservations{sink: sink}
		legacy := json.RawMessage(`{"type":"unknown_provider_item","text":"PRIVATE"}`)
		checkpoint := ActiveTurnCheckpoint{TurnID: "turn-1", ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"hello"}`)}, OutputTrace: []json.RawMessage{legacy}}
		replay, err := RecoveredReplayInput(&checkpoint)
		require.NoError(t, err)
		require.NoError(t, owner.write(t.Context(), &checkpoint, checkpointStart))

		held := make(chan struct{})

		defer close(held)
		go func() { <-held }()

		var workers errgroup.Group
		for _, id := range []string{"item-a", "item-b"} {
			workers.Go(func() error {
				return owner.observe(t.Context(), &PublicProgress{ID: id, Kind: PublicProgressText, State: PublicProgressWorking, Text: id, Agent: "main", Model: "model"})
			})
		}

		synctest.Wait()
		require.NoError(t, workers.Wait())
		require.Len(t, sink.RecordOutputTraceCalls(), 2, "writes must finish while work remains held")
		require.NoError(t, owner.write(t.Context(), &checkpoint, checkpointProvider))

		persisted := sink.RecordProviderResponseCalls()[0].ActiveTurnCheckpoint
		require.Len(t, persisted.OutputTrace, 3, "a stale full snapshot must keep both siblings and legacy trace")
		require.JSONEq(t, string(legacy), string(persisted.OutputTrace[0]))
		require.Len(t, PublicProgressFromTrace(persisted.OutputTrace), 2)
		gotReplay, err := RecoveredReplayInput(persisted)
		require.NoError(t, err)
		require.Equal(t, replay, gotReplay)
		require.NoError(t, owner.observe(t.Context(), &PublicProgress{ID: "item-a", Kind: PublicProgressText, State: PublicProgressWorking, Text: "item-a", Agent: "main", Model: "model"}))
		require.Len(t, sink.RecordOutputTraceCalls(), 2, "identical public state must not write")

		replacement := PublicProgress{ID: "item-a", Kind: PublicProgressText, State: PublicProgressCompleted, Agent: "main", Model: "model"}
		require.NoError(t, owner.observe(t.Context(), &replacement))
		require.Contains(t, PublicProgressFromTrace(sink.RecordOutputTraceCalls()[2].RawMessages), replacement, "empty text replaces rather than appends to the previous text")
		replacement.Agent = ""
		require.NoError(t, owner.finishCall(t.Context(), &replacement, PublicProgressCompleted))
		require.Empty(t, replacement.Agent, "completion must not mutate the worker's snapshot")
		require.Equal(t, "main", PublicProgressFromTrace(sink.RecordOutputTraceCalls()[2].RawMessages)[0].Agent, "later caller mutations must not alter stored attribution")

		checkpoint.OutputTrace = append(checkpoint.OutputTrace, legacy, json.RawMessage(`{"type":"new_private_item","text":"ALSO PRIVATE"}`))
		require.NoError(t, owner.write(t.Context(), &checkpoint, checkpointTool))
		require.Len(t, sink.RecordCompletedToolOutputCalls()[0].ActiveTurnCheckpoint.OutputTrace, 5)
		require.NoError(t, owner.write(t.Context(), &ActiveTurnCheckpoint{TurnID: checkpoint.TurnID}, checkpointRecovered))
		require.Len(t, sink.RecordRecoveredReplayCalls()[0].ActiveTurnCheckpoint.OutputTrace, 5, "new and duplicate legacy records survive later stale snapshots")
		checkpoint.OutputTrace = sink.RecordCompletedToolOutputCalls()[0].ActiveTurnCheckpoint.OutputTrace

		require.NoError(t, owner.replaceResponse(t.Context(), "", nil))
		require.NoError(t, owner.write(t.Context(), &checkpoint, checkpointProvider))
		require.Empty(t, PublicProgressFromTrace(sink.RecordProviderResponseCalls()[1].ActiveTurnCheckpoint.OutputTrace), "stale full snapshots cannot revive removed provider attempts")
		require.Len(t, sink.RecordProviderResponseCalls()[1].ActiveTurnCheckpoint.OutputTrace, 3, "attempt removal preserves private legacy records")
		require.NoError(t, owner.close(t.Context(), PublicProgressStopped))
		require.NoError(t, owner.observe(t.Context(), &PublicProgress{ID: "late", Kind: PublicProgressText, State: PublicProgressWorking, Text: "too late"}))
		require.NoError(t, owner.write(t.Context(), &checkpoint, checkpointProvider))
		require.Len(t, sink.RecordOutputTraceCalls(), 4)
		require.Len(t, sink.RecordProviderResponseCalls(), 2)
	})
}

func TestTurnObservationPersistenceFailure(t *testing.T) {
	errPersist := errors.New("storage unavailable")
	sink := recordingCheckpointSink()
	sink.RecordOutputTraceFunc = func(context.Context, string, []json.RawMessage) error { return errPersist }
	owner := turnObservations{sink: sink}
	checkpoint := ActiveTurnCheckpoint{TurnID: "turn-1"}
	require.NoError(t, owner.write(t.Context(), &checkpoint, checkpointStart))

	progress := PublicProgress{ID: "item-1", Kind: PublicProgressText, State: PublicProgressWorking, Text: "hello"}
	err := owner.observe(t.Context(), &progress)
	require.ErrorIs(t, err, errPersist)
	_, ok := errors.AsType[progressPersistenceError](err)
	require.True(t, ok, "U2/U3 must distinguish persistence failure from provider/tool errors")
	require.ErrorIs(t, owner.observe(t.Context(), &progress), errPersist)
	require.ErrorIs(t, owner.write(t.Context(), &checkpoint, checkpointProvider), errPersist)
	require.Empty(t, sink.RecordProviderResponseCalls())
	sink.CloseActiveTurnFunc = func(context.Context, string, PublicProgressState) error { return errPersist }

	require.ErrorIs(t, owner.close(t.Context(), PublicProgressFailed), errPersist)
	require.NoError(t, owner.observe(t.Context(), &progress), "closed lifetime ignores late workers even after a write failure")
}

func TestPublicProgressTraceAllowlist(t *testing.T) {
	traces := []json.RawMessage{
		json.RawMessage(`{"type":"reasoning","text":"PRIVATE"}`),
		json.RawMessage(`{"type":"unknown","text":"PRIVATE"}`),
		json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"unknown-kind","kind":"reasoning","state":"working","text":"PRIVATE"}}`),
		json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"unknown-state","kind":"text","state":"private","text":"PRIVATE"}}`),
		json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"item-1","kind":"text","state":"working","text":"hello","agent":"main","model":"model"}}`),
	}
	require.Equal(t, []PublicProgress{{ID: "item-1", Kind: PublicProgressText, State: PublicProgressWorking, Text: "hello", Agent: "main", Model: "model"}}, PublicProgressFromTrace(traces))

	sink := recordingCheckpointSink()
	owner := turnObservations{sink: sink}
	require.NoError(t, owner.write(t.Context(), &ActiveTurnCheckpoint{TurnID: "recovered", OutputTrace: traces}, checkpointStart))
	require.NoError(t, owner.observe(t.Context(), new(PublicProgressFromTrace(traces)[0])))
	require.Empty(t, sink.RecordOutputTraceCalls(), "seeding recognizes equivalent public JSON without rewriting it")
}

func TestActiveTurnCheckpointJSONRoundTrip(t *testing.T) {
	checkpoint := ActiveTurnCheckpoint{
		TurnID:          "turn-1",
		ConversationKey: "conversation-1",
		Agent:           "main",
		Model:           "gpt-5.4",
		DisplayModel:    "GPT-5.4",
		ReplayInput:     []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"hello"}`)},
		OutputTrace:     []json.RawMessage{json.RawMessage(`{"type":"unknown_provider_item"}`)},
		TokenUsage:      &TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		ResponseID:      "resp-1",
		OpenFunctionCalls: []FunctionCallCheckpoint{{
			CallID:    "call-1",
			Name:      "read",
			Arguments: json.RawMessage(`{"filePath":"README.md"}`),
		}},
		CompletedFunctionOutputs: []FunctionOutputCheckpoint{{
			CallID:      "call-2",
			Name:        "read",
			ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"function_call_output","call_id":"call-2","output":"done"}`)},
		}},
	}

	data, err := json.Marshal(checkpoint)
	require.NoError(t, err)

	var got ActiveTurnCheckpoint
	require.NoError(t, json.Unmarshal(data, &got))

	require.Equal(t, checkpoint.TurnID, got.TurnID)
	require.Equal(t, checkpoint.ConversationKey, got.ConversationKey)
	require.Equal(t, checkpoint.Agent, got.Agent)
	require.Equal(t, checkpoint.Model, got.Model)
	require.Equal(t, checkpoint.DisplayModel, got.DisplayModel)
	require.Equal(t, checkpoint.ResponseID, got.ResponseID)
	require.Equal(t, checkpoint.TokenUsage, got.TokenUsage)
	require.JSONEq(t, string(checkpoint.ReplayInput[0]), string(got.ReplayInput[0]))
	require.JSONEq(t, string(checkpoint.OutputTrace[0]), string(got.OutputTrace[0]))
	require.JSONEq(t, string(checkpoint.OpenFunctionCalls[0].Arguments), string(got.OpenFunctionCalls[0].Arguments))
	require.JSONEq(t, string(checkpoint.CompletedFunctionOutputs[0].ReplayInput[0]), string(got.CompletedFunctionOutputs[0].ReplayInput[0]))
	require.NotContains(t, string(data), "status")
}

func TestCheckpointMalformedFunctionArguments(t *testing.T) {
	for _, arguments := range []string{`{"filePath":`, "", " "} {
		t.Run(arguments, func(t *testing.T) {
			response := responseWithFunctionCalls("resp-1", []responses.ResponseFunctionToolCall{testFunctionCall("tool-1", "call-1", "read", arguments)})
			item, ok := responseOutputToReplayInput(&response.Output[0])
			require.True(t, ok)

			replay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{item})
			require.NoError(t, err)

			checkpoint := ActiveTurnCheckpoint{ReplayInput: replay, OpenFunctionCalls: openFunctionCallCheckpoints(response.Output)}
			data, err := json.Marshal(checkpoint)
			require.NoError(t, err)

			var saved ActiveTurnCheckpoint
			require.NoError(t, json.Unmarshal(data, &saved))
			require.Equal(t, []FunctionCallCheckpoint{{CallID: "call-1", Name: "read"}}, saved.OpenFunctionCalls)
			recovered, err := RecoveredReplayInput(&saved)
			require.NoError(t, err)
			items, err := ReplayInputToParams(recovered)
			require.NoError(t, err)
			require.Equal(t, arguments, items[0].OfFunctionCall.Arguments)
			require.Equal(t, "call-1", items[1].OfFunctionCallOutput.CallID.Value)
		})
	}
}

func TestSteerCheckpointPreservesCallMetadata(t *testing.T) {
	errPersist := errors.New("checkpoint write failed")
	for _, tt := range []struct {
		name   string
		inputs []PromptInput
		err    error
	}{
		{name: "no steers"},
		{name: "persist", inputs: []PromptInput{{Text: "continue"}}},
		{name: "write failure", inputs: []PromptInput{{Text: "continue"}}, err: errPersist},
	} {
		t.Run(tt.name, func(t *testing.T) {
			looper := emptyTestLooper()
			sink := recordingCheckpointSink()
			sink.RecordProviderResponseFunc = func(_ context.Context, _ *ActiveTurnCheckpoint) error { return tt.err }
			looper.CheckpointSink = sink
			looper.SteerDrain = SteerDrain{Fn: func(context.Context, TurnPhase) []PromptInput { return tt.inputs }}
			record := SessionEntry{TurnID: "turn-1", Timestamp: time.Unix(1, 0)}
			open := []FunctionCallCheckpoint{{CallID: "open-call", Name: "read"}}
			completed := []FunctionOutputCheckpoint{{CallID: "done-call", Name: "read", ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"function_call_output","call_id":"done-call","output":"contents"}`)}}}
			checkpoint := looper.activeTurnCheckpoint(&record, open, completed)

			var items []responses.ResponseInputItemUnionParam

			looper.observations = &turnObservations{sink: sink}
			injected, err := looper.appendSteers(t.Context(), &record, &items, &checkpoint, TurnPhaseToolLoop)
			require.ErrorIs(t, err, tt.err)
			require.Equal(t, len(tt.inputs) > 0 && tt.err == nil, injected)
			require.Equal(t, open, checkpoint.OpenFunctionCalls)
			require.Equal(t, completed, checkpoint.CompletedFunctionOutputs)
			require.Len(t, checkpoint.ReplayInput, len(tt.inputs))
			require.Len(t, sink.RecordProviderResponseCalls(), len(tt.inputs))
		})
	}
}
