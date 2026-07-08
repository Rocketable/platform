package rocketcode

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

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
