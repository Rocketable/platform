package rocketcode

import (
	"encoding/json"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
)

func TestReplayInputPreservesCompactionPayload(t *testing.T) {
	raw := []json.RawMessage{json.RawMessage(`{"content":"summary","summary":{"text":"summary"},"recent":[{"id":"msg-1"}],"encrypted_content":"encrypted","id":"cmp-1","type":"compaction"}`)}

	params, err := ReplayInputToParams(raw)
	require.NoError(t, err)

	got, err := ReplayInputFromParams(params)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.JSONEq(t, string(raw[0]), string(got[0]))
}

func TestAbortedFunctionCallOutputsUsesGenericText(t *testing.T) {
	items := []responses.ResponseInputItemUnionParam{
		functionCallReplayInput("fc-1", "call-1", "read", `{"filePath":"README.md"}`),
	}

	outputs := abortedFunctionCallOutputs(items, []FunctionCallCheckpoint{{CallID: "call-1", Name: "read"}})

	require.Len(t, outputs, 1)
	require.Equal(t, "function_call_output", *outputs[0].GetType())
	require.Equal(t, "call-1", *outputs[0].GetCallID())
	require.Contains(t, marshalReplayJSON(t, outputs[0]), genericAbortedToolOutputText)
}

func TestAbortedFunctionCallOutputsUsesTaskText(t *testing.T) {
	items := []responses.ResponseInputItemUnionParam{
		functionCallReplayInput("fc-1", "call-1", "task", `{"description":"work"}`),
	}

	outputs := abortedFunctionCallOutputs(items, []FunctionCallCheckpoint{{CallID: "call-1", Name: "task"}})

	require.Len(t, outputs, 1)
	require.Equal(t, "call-1", *outputs[0].GetCallID())
	require.Contains(t, marshalReplayJSON(t, outputs[0]), taskAbortedToolOutputText)
}

func TestAbortedFunctionCallOutputsPreservesCompletedOutputs(t *testing.T) {
	completed := responses.ResponseInputItemFunctionCallOutputParam{
		CallID: "call-1",
		Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: openai.String("done")},
		Type:   "function_call_output",
	}
	items := []responses.ResponseInputItemUnionParam{
		functionCallReplayInput("fc-1", "call-1", "read", `{"filePath":"README.md"}`),
		{OfFunctionCallOutput: &completed},
		functionCallReplayInput("fc-2", "call-2", "task", `{"description":"work"}`),
	}

	outputs := abortedFunctionCallOutputs(items, []FunctionCallCheckpoint{
		{CallID: "call-1", Name: "read"},
		{CallID: "call-2", Name: "task"},
	})

	require.Len(t, outputs, 1)
	require.Equal(t, "call-2", *outputs[0].GetCallID())
	require.NotContains(t, marshalReplayJSON(t, outputs[0]), "call-1")
}

func TestRecoveredReplayInputDecodes(t *testing.T) {
	raw, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		testInputMessage(responses.EasyInputMessageRoleUser, "do work", ""),
		functionCallReplayInput("fc-1", "call-1", "read", `{"filePath":"README.md"}`),
	})
	require.NoError(t, err)

	checkpoint := ActiveTurnCheckpoint{
		ReplayInput:       raw,
		OpenFunctionCalls: []FunctionCallCheckpoint{{CallID: "call-1", Name: "read"}},
	}
	recovered, err := RecoveredReplayInput(&checkpoint)
	require.NoError(t, err)

	items, err := ReplayInputToParams(recovered)
	require.NoError(t, err)
	require.Len(t, items, 4)
	require.Equal(t, "function_call_output", *items[2].GetType())
	require.Equal(t, "message", *items[3].GetType())
	require.Equal(t, "developer", *items[3].GetRole())
	require.JSONEq(t, `{"content":"`+recoveryReplayMessageText+`","role":"developer","type":"message"}`, marshalReplayJSON(t, items[3]))
}

func TestRecoveredReplayInputPreservesCompletedToolOutputs(t *testing.T) {
	completed := responses.ResponseInputItemFunctionCallOutputParam{
		CallID: "call-1",
		Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: openai.String("completed before restart")},
		Type:   "function_call_output",
	}
	raw, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		testInputMessage(responses.EasyInputMessageRoleUser, "do work", ""),
		functionCallReplayInput("fc-1", "call-1", "read", `{"filePath":"README.md"}`),
		{OfFunctionCallOutput: &completed},
		functionCallReplayInput("fc-2", "call-2", "task", `{"description":"continue"}`),
	})
	require.NoError(t, err)

	checkpoint := ActiveTurnCheckpoint{
		ReplayInput: raw,
		OpenFunctionCalls: []FunctionCallCheckpoint{
			{CallID: "call-1", Name: "read"},
			{CallID: "call-2", Name: "task"},
		},
	}
	recovered, err := RecoveredReplayInput(&checkpoint)
	require.NoError(t, err)

	items, err := ReplayInputToParams(recovered)
	require.NoError(t, err)
	require.Len(t, items, 6)
	require.Equal(t, "function_call_output", *items[2].GetType())
	require.Equal(t, "call-1", *items[2].GetCallID())
	require.Contains(t, marshalReplayJSON(t, items[2]), "completed before restart")
	require.Equal(t, "function_call_output", *items[4].GetType())
	require.Equal(t, "call-2", *items[4].GetCallID())
	require.Contains(t, marshalReplayJSON(t, items[4]), taskAbortedToolOutputText)
	require.NotContains(t, marshalReplayJSON(t, items[4]), "completed before restart")
}

func TestRecoveredReplayInputIncludesCheckpointCompletedOutputs(t *testing.T) {
	saved, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		testInputMessage(responses.EasyInputMessageRoleUser, "do work", ""),
		functionCallReplayInput("fc-1", "call-1", "read", `{"filePath":"README.md"}`),
		functionCallReplayInput("fc-2", "call-2", "task", `{"description":"check state"}`),
	})
	require.NoError(t, err)

	completed, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{{OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
		CallID: "call-1",
		Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: openai.String("read result")},
		Type:   "function_call_output",
	}}})
	require.NoError(t, err)

	checkpoint := ActiveTurnCheckpoint{
		ReplayInput: saved,
		OpenFunctionCalls: []FunctionCallCheckpoint{
			{CallID: "call-1", Name: "read"},
			{CallID: "call-2", Name: "task"},
		},
		CompletedFunctionOutputs: []FunctionOutputCheckpoint{{CallID: "call-1", Name: "read", ReplayInput: completed}},
	}
	recovered, err := RecoveredReplayInput(&checkpoint)
	require.NoError(t, err)

	items, err := ReplayInputToParams(recovered)
	require.NoError(t, err)
	require.Len(t, items, 6)
	require.Equal(t, "call-1", *items[3].GetCallID())
	require.Contains(t, marshalReplayJSON(t, items[3]), "read result")
	require.Equal(t, "call-2", *items[4].GetCallID())
	require.Contains(t, marshalReplayJSON(t, items[4]), taskAbortedToolOutputText)
	require.Contains(t, marshalReplayJSON(t, items[5]), recoveryReplayMessageText)
}

func TestRecoveredReplayInputDoesNotDuplicateCompletedCheckpointOutputs(t *testing.T) {
	completed, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{{OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
		CallID: "call-1",
		Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: openai.String("read result")},
		Type:   "function_call_output",
	}}})
	require.NoError(t, err)

	saved, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		testInputMessage(responses.EasyInputMessageRoleUser, "do work", ""),
		functionCallReplayInput("fc-1", "call-1", "read", `{"filePath":"README.md"}`),
	})
	require.NoError(t, err)

	saved = append(saved, completed...)

	checkpoint := ActiveTurnCheckpoint{
		ReplayInput:              saved,
		OpenFunctionCalls:        []FunctionCallCheckpoint{{CallID: "call-1", Name: "read"}},
		CompletedFunctionOutputs: []FunctionOutputCheckpoint{{CallID: "call-1", Name: "read", ReplayInput: completed}},
	}
	recovered, err := RecoveredReplayInput(&checkpoint)
	require.NoError(t, err)

	items, err := ReplayInputToParams(recovered)
	require.NoError(t, err)

	outputs := 0

	for i := range items {
		if items[i].OfFunctionCallOutput != nil && items[i].OfFunctionCallOutput.CallID == "call-1" {
			outputs++
		}
	}

	require.Equal(t, 1, outputs)
}

func TestRecoveredReplayInputDoesNotDuplicateRecoveryMessage(t *testing.T) {
	raw, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		testInputMessage(responses.EasyInputMessageRoleUser, "do work", ""),
		functionCallReplayInput("fc-1", "call-1", "read", `{"filePath":"README.md"}`),
	})
	require.NoError(t, err)

	checkpoint := ActiveTurnCheckpoint{ReplayInput: raw, OpenFunctionCalls: []FunctionCallCheckpoint{{CallID: "call-1", Name: "read"}}}
	first, err := RecoveredReplayInput(&checkpoint)
	require.NoError(t, err)

	checkpoint.ReplayInput = first
	second, err := RecoveredReplayInput(&checkpoint)
	require.NoError(t, err)

	items, err := ReplayInputToParams(second)
	require.NoError(t, err)

	messages := 0

	for i := range items {
		if strings.Contains(marshalReplayJSON(t, items[i]), recoveryReplayMessageText) {
			messages++
		}
	}

	require.Equal(t, 1, messages)
}

func TestRecoveredReplayInputBuildsProviderParams(t *testing.T) {
	saved, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		testInputMessage(responses.EasyInputMessageRoleUser, "inspect", ""),
		functionCallReplayInput("fc-1", "call-1", "read", `{"filePath":"README.md"}`),
	})
	require.NoError(t, err)

	checkpoint := ActiveTurnCheckpoint{
		ReplayInput:       saved,
		OpenFunctionCalls: []FunctionCallCheckpoint{{CallID: "call-1", Name: "read"}},
	}
	recovered, err := RecoveredReplayInput(&checkpoint)
	require.NoError(t, err)

	items, err := ReplayInputToParams(recovered)
	require.NoError(t, err)

	looper := emptyTestLooper()
	looper.Model = openai.ChatModelGPT5
	params := looper.buildParams(items)

	require.Equal(t, openai.ChatModelGPT5, params.Model)
	require.Len(t, params.Input.OfInputItemList, 4)
	require.Equal(t, "function_call_output", *params.Input.OfInputItemList[2].GetType())
	require.Equal(t, "developer", *params.Input.OfInputItemList[3].GetRole())
}

func marshalReplayJSON(t *testing.T, value any) string {
	t.Helper()

	data, err := json.Marshal(value)
	require.NoError(t, err)

	return string(data)
}
