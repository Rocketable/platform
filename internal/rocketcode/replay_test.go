package rocketcode

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
)

func TestProjectPortableReplayKeepsMessagesPhasesToolsAndAttachments(t *testing.T) {
	raw := []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","content":[{"type":"input_text","text":"inspect"},{"type":"input_image","image_url":"data:image/png;base64,aW1hZ2U="},{"type":"input_file","filename":"note.txt","file_data":"data:text/plain;base64,bm90ZQ=="}]}`),
		json.RawMessage(`{"type":"message","role":"assistant","phase":"commentary","content":"working"}`),
		json.RawMessage(`{"type":"function_call","id":"provider-call","call_id":"call-1","name":"read","arguments":"{\"filePath\":\"README.md\"}"}`),
		json.RawMessage(`{"type":"function_call_output","id":"provider-output","call_id":"call-1","output":"contents"}`),
	}
	items, err := ReplayInputToParams(raw)
	require.NoError(t, err)

	got, err := projectPortableReplay(items)
	require.NoError(t, err)
	require.Len(t, got, 4)
	require.JSONEq(t, string(raw[0]), marshalReplayJSON(t, got[0]))
	require.JSONEq(t, string(raw[1]), marshalReplayJSON(t, got[1]))
	require.JSONEq(t, `{"type":"function_call","call_id":"call-1","name":"read","arguments":"{\"filePath\":\"README.md\"}"}`, marshalReplayJSON(t, got[2]))
	require.JSONEq(t, `{"type":"function_call_output","call_id":"call-1","output":"contents"}`, marshalReplayJSON(t, got[3]))
}

func TestProjectPortableReplayRebuildsOutputMessageWithoutProviderFields(t *testing.T) {
	var message responses.ResponseOutputMessageParam

	err := json.Unmarshal([]byte(`{"type":"message","id":"provider-message","status":"completed","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"first","annotations":[]},{"type":"refusal","refusal":"cannot disclose"},{"type":"output_text","text":"last","annotations":[]}]}`), &message)
	require.NoError(t, err)

	items := []responses.ResponseInputItemUnionParam{{OfOutputMessage: &message}}
	require.True(t, hasOpaqueReplay(items))

	got, err := projectPortableReplay(items)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotNil(t, got[0].OfMessage)
	require.JSONEq(t, `{"type":"message","role":"assistant","phase":"commentary","content":"firstcannot discloselast"}`, marshalReplayJSON(t, got[0]))
}

func TestProjectPortableReplayLowersReasoningSummaryToAssistantMessage(t *testing.T) {
	items := []responses.ResponseInputItemUnionParam{
		testInputMessage(responses.EasyInputMessageRoleUser, "before", ""),
		testInputReasoning("reasoning-1", "readable thought", "sealed"),
		testInputReasoning("reasoning-2", "", "sealed-only"),
		testInputMessage(responses.EasyInputMessageRoleAssistant, "after", "final_answer"),
	}

	got, err := projectPortableReplay(items)
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.JSONEq(t, `{"content":"readable thought","role":"assistant","type":"message"}`, marshalReplayJSON(t, got[1]))
	require.JSONEq(t, `{"content":"after","phase":"final_answer","role":"assistant","type":"message"}`, marshalReplayJSON(t, got[2]))
}

func TestProjectPortableReplayUsesReadableCompactionCheckpointAndTail(t *testing.T) {
	items := []responses.ResponseInputItemUnionParam{
		testInputMessage(responses.EasyInputMessageRoleUser, "old prefix must disappear", ""),
		compactionReplayInput("compaction-1", "sealed", "portable summary"),
		testInputMessage(responses.EasyInputMessageRoleUser, "post-compaction tail one", ""),
		testInputMessage(responses.EasyInputMessageRoleAssistant, "post-compaction tail two", "final_answer"),
	}

	got, err := projectPortableReplay(items)
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.JSONEq(t, `{"content":`+strconv.Quote(CompactionCheckpointText(items[1].OfCompaction))+`,"role":"assistant","type":"message"}`, marshalReplayJSON(t, got[0]))
	require.JSONEq(t, `{"content":"post-compaction tail one","role":"user","type":"message"}`, marshalReplayJSON(t, got[1]))
	require.JSONEq(t, `{"content":"post-compaction tail two","phase":"final_answer","role":"assistant","type":"message"}`, marshalReplayJSON(t, got[2]))
	serialized := marshalReplayJSON(t, got)
	require.NotContains(t, serialized, "old prefix must disappear")
	require.Equal(t, 1, strings.Count(serialized, "portable summary"))
}

func TestProjectPortableReplayReadableCompactionSupersedesEncryptedOnlyCompaction(t *testing.T) {
	items := []responses.ResponseInputItemUnionParam{
		compactionReplayInput("encrypted-only", "sealed-old", ""),
		compactionReplayInput("readable", "sealed-new", "checkpoint with retained recent context"),
		testInputMessage(responses.EasyInputMessageRoleAssistant, "post-compaction tail", "final_answer"),
	}

	got, err := projectPortableReplay(items)
	require.NoError(t, err)
	serialized := marshalReplayJSON(t, got)
	require.NotContains(t, serialized, "encrypted-only")
	require.Equal(t, 1, strings.Count(serialized, "checkpoint with retained recent context"))
	require.Equal(t, 1, strings.Count(serialized, "post-compaction tail"))
	require.Less(t, strings.Index(serialized, "checkpoint with retained recent context"), strings.Index(serialized, "post-compaction tail"))
}

func TestProjectPortableReplayRejectsEncryptedOnlyCompaction(t *testing.T) {
	items := []responses.ResponseInputItemUnionParam{compactionReplayInput("compaction-1", "sealed", "")}

	_, err := projectPortableReplay(items)

	var missing *MissingPortableContextError
	require.ErrorAs(t, err, &missing)
	require.Equal(t, "compaction-1", missing.CompactionID)
	require.EqualError(t, err, `compaction "compaction-1" has no readable context checkpoint`)
}

func TestProjectPortableReplayCollectsItemsAroundUnreadableCompaction(t *testing.T) {
	items := []responses.ResponseInputItemUnionParam{
		testInputMessage(responses.EasyInputMessageRoleUser, "before", ""),
		compactionReplayInput("compaction-1", "sealed", ""),
		testInputMessage(responses.EasyInputMessageRoleAssistant, "tail", "final_answer"),
	}

	got, err := projectPortableReplay(items)

	var missing *MissingPortableContextError
	require.ErrorAs(t, err, &missing)
	require.Len(t, got, 2)
	require.JSONEq(t, `{"content":"before","role":"user","type":"message"}`, marshalReplayJSON(t, got[0]))
	require.JSONEq(t, `{"content":"tail","phase":"final_answer","role":"assistant","type":"message"}`, marshalReplayJSON(t, got[1]))
}

func TestProjectPortableReplayDropsProviderExtensions(t *testing.T) {
	items, err := ReplayInputToParams([]json.RawMessage{
		json.RawMessage(`{"type":"web_search_call","id":"web-1","status":"completed","action":{"type":"search","query":"golang"}}`),
		json.RawMessage(`{"type":"file_search_call","id":"extension-1","status":"completed","queries":["private"]}`),
	})
	require.NoError(t, err)
	require.True(t, hasOpaqueReplay(items))

	got, err := projectPortableReplay(items)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestReplayInputPreservesCompactionPayload(t *testing.T) {
	raw := []json.RawMessage{json.RawMessage(`{"content":"summary","summary":{"text":"summary"},"recent":[{"id":"msg-1"}],"encrypted_content":"encrypted","id":"cmp-1","type":"compaction"}`)}

	params, err := ReplayInputToParams(raw)
	require.NoError(t, err)

	got, err := ReplayInputFromParams(params)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.JSONEq(t, string(raw[0]), string(got[0]))
}

func TestSetReadableCompactionBackupUpdatesEveryCompactionAndPreservesExtras(t *testing.T) {
	items, err := ReplayInputToParams([]json.RawMessage{
		json.RawMessage(`{"content":"provider content","summary":{"text":"provider summary"},"recent":[{"id":"msg-1"}],"encrypted_content":"encrypted-1","id":"cmp-1","type":"compaction"}`),
		json.RawMessage(`{"content":"other content","encrypted_content":"encrypted-2","id":"cmp-2","type":"compaction"}`),
	})
	require.NoError(t, err)

	require.True(t, setReadableCompactionBackup(items, "generated backup"))
	require.JSONEq(t, `{"content":"generated backup","summary":{"text":"provider summary"},"recent":[{"id":"msg-1"}],"encrypted_content":"encrypted-1","id":"cmp-1","type":"compaction"}`, marshalReplayJSON(t, items[0]))
	require.JSONEq(t, `{"content":"generated backup","encrypted_content":"encrypted-2","id":"cmp-2","type":"compaction"}`, marshalReplayJSON(t, items[1]))
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
	recovered, err := RecoveredReplayInput(&checkpoint, ProviderOrigin{})
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
	recovered, err := RecoveredReplayInput(&checkpoint, ProviderOrigin{})
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
	recovered, err := RecoveredReplayInput(&checkpoint, ProviderOrigin{})
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
	recovered, err := RecoveredReplayInput(&checkpoint, ProviderOrigin{})
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
	first, err := RecoveredReplayInput(&checkpoint, ProviderOrigin{})
	require.NoError(t, err)

	checkpoint.ReplayInput = first
	second, err := RecoveredReplayInput(&checkpoint, ProviderOrigin{})
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
	recovered, err := RecoveredReplayInput(&checkpoint, ProviderOrigin{})
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

func TestRecoveredReplayInputProjectsNonlegacyOriginMismatch(t *testing.T) {
	source := ProviderOrigin{ProviderID: "source", Route: "source-route", ModelID: "source-model", AuthenticationEpoch: "source-epoch"}
	destination := ProviderOrigin{ProviderID: "destination", Route: "destination-route", ModelID: "destination-model", AuthenticationEpoch: "destination-epoch"}
	replay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{
		testInputReasoning("reasoning-provider-id", "readable reasoning", "sealed-reasoning"),
		functionCallReplayInput("call-provider-id", "call-1", "read", `{}`),
	})
	require.NoError(t, err)

	checkpoint := ActiveTurnCheckpoint{Origin: &source, ReplayInput: replay, OpenFunctionCalls: []FunctionCallCheckpoint{{CallID: "call-1", Name: "read"}}}

	got, err := RecoveredReplayInput(&checkpoint, destination)
	require.NoError(t, err)
	serializedJSON, err := json.Marshal(got)
	require.NoError(t, err)

	serialized := string(serializedJSON)
	require.NotContains(t, serialized, "sealed-reasoning")
	require.NotContains(t, serialized, "provider-id")
	require.Contains(t, serialized, "readable reasoning")
	require.Equal(t, 1, strings.Count(serialized, genericAbortedToolOutputText))
	require.Equal(t, 1, strings.Count(serialized, recoveryReplayMessageText))
}

func TestRecoveredReplayInputPreservesLegacyOpaqueReplay(t *testing.T) {
	replay, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{testInputReasoning("reasoning-provider-id", "readable reasoning", "sealed-reasoning")})
	require.NoError(t, err)

	checkpoint := ActiveTurnCheckpoint{ReplayInput: replay}

	got, err := RecoveredReplayInput(&checkpoint, ProviderOrigin{ProviderID: "destination"})
	require.NoError(t, err)
	serializedJSON, err := json.Marshal(got)
	require.NoError(t, err)

	serialized := string(serializedJSON)
	require.Contains(t, serialized, "sealed-reasoning")
	require.Contains(t, serialized, "reasoning-provider-id")
}

func marshalReplayJSON(t *testing.T, value any) string {
	t.Helper()

	data, err := json.Marshal(value)
	require.NoError(t, err)

	return string(data)
}
