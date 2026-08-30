package backend

import (
	"encoding/json"
	"iter"
	"slices"
	"testing"
	"time"

	"github.com/Rocketable/platform/internal/rocketcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	providerReplayReadable = "portable-readable"
	providerReplayPrivate  = "provider-private-sentinel"
)

func TestProviderForModel(t *testing.T) {
	for model, want := range map[string]string{
		"":           "openai",
		"gpt-5.5":    "openai",
		"openai/gpt": "openai",
		"work/gpt":   "work",
	} {
		assert.Equal(t, want, providerForModel(model), model)
	}
}

func TestSessionEntryForProviderSameProviderIsUnchanged(t *testing.T) {
	entry := rocketcode.SessionEntry{
		Version:     1,
		Type:        "turn",
		Timestamp:   time.Unix(1, 2).UTC(),
		ResponseID:  providerReplayPrivate,
		Model:       "work/gpt",
		ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"unknown-provider-item","private":"provider-private-sentinel"}`)},
		OutputTrace: []json.RawMessage{json.RawMessage(`{"private":"provider-private-sentinel"}`)},
	}
	want, err := json.Marshal(entry)
	require.NoError(t, err)

	got, err := sessionEntryForProvider(&entry, "work")
	require.NoError(t, err)
	data, err := json.Marshal(got)
	require.NoError(t, err)
	assert.Equal(t, want, data)
}

func TestSessionEntryForProviderDifferentProviderProjectsReplay(t *testing.T) {
	entry := rocketcode.SessionEntry{
		Version:    1,
		Type:       "turn",
		Timestamp:  time.Unix(1, 0).UTC(),
		ResponseID: providerReplayPrivate,
		Model:      "openai/gpt",
		ReplayInput: []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"user","phase":"commentary","id":"provider-private-sentinel","content":[{"type":"input_text","text":"portable-readable","private":"provider-private-sentinel"},{"type":"input_image","file_id":"provider-private-sentinel","image_url":"data:image/png;base64,portable-image","detail":"high"},{"type":"input_file","file_id":"provider-private-sentinel","file_url":"https://example.test/portable-file","file_data":"portable-data","filename":"portable.txt","detail":"low"}]}`),
			json.RawMessage(`{"type":"function_call","id":"provider-private-sentinel","call_id":"portable-call","name":"read","arguments":"{\"path\":\"portable.txt\"}","status":"completed","private":"provider-private-sentinel"}`),
			json.RawMessage(`{"type":"function_call_output","id":"provider-private-sentinel","call_id":"portable-call","status":"completed","output":[{"type":"input_text","text":"portable-tool-output"},{"type":"input_file","file_id":"provider-private-sentinel","file_data":"portable-output-data","filename":"output.txt"}],"private":"provider-private-sentinel"}`),
			json.RawMessage(`{"type":"reasoning","id":"provider-private-sentinel","encrypted_content":"provider-private-sentinel","summary":[{"type":"summary_text","text":"portable-summary-one"},{"type":"summary_text","text":"  "},{"type":"summary_text","text":"portable-summary-two"}],"content":[{"type":"reasoning_text","text":"provider-private-sentinel"}],"status":"completed"}`),
			json.RawMessage(`{"type":"reasoning","encrypted_content":"provider-private-sentinel","summary":[]}`),
			json.RawMessage(`{"type":"compaction","id":"provider-private-sentinel","encrypted_content":"provider-private-sentinel","content":"portable-compaction","recent":["provider-private-sentinel"]}`),
			json.RawMessage(`{"type":"compaction","encrypted_content":"provider-private-sentinel"}`),
			json.RawMessage(`{"type":"provider_hosted","value":"provider-private-sentinel"}`),
		},
		OutputTrace: []json.RawMessage{json.RawMessage(`{"private":"provider-private-sentinel"}`)},
	}

	got, err := sessionEntryForProvider(&entry, "work")
	require.NoError(t, err)
	assert.Empty(t, got.ResponseID)
	assert.Empty(t, got.OutputTrace)
	assert.Equal(t, entry.Model, got.Model)

	data, err := json.Marshal(got.ReplayInput)
	require.NoError(t, err)

	text := string(data)
	for _, want := range []string{providerReplayReadable, "portable-image", "portable-file", "portable-data", "portable.txt", "portable-call", "read", "portable-tool-output", "portable-output-data", "output.txt", "portable-summary-one", "portable-summary-two", "portable-compaction", "commentary"} {
		assert.Contains(t, text, want)
	}

	assert.NotContains(t, text, providerReplayPrivate)
	assert.NotContains(t, text, "provider_hosted")
	assert.Len(t, got.ReplayInput, 6)

	original, err := json.Marshal(entry)
	require.NoError(t, err)
	assert.Contains(t, string(original), providerReplayPrivate)
}

func TestSessionEntriesForProviderTreatsMissingModelAsOpenAI(t *testing.T) {
	entries := func(yield func(rocketcode.SessionEntry, error) bool) {
		yield(rocketcode.SessionEntry{Model: "", ResponseID: providerReplayPrivate, ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"portable-readable"}`)}}, nil)
	}

	got := slices.Collect(func(yield func(rocketcode.SessionEntry) bool) {
		for entry, err := range sessionEntriesForProvider(iter.Seq2[rocketcode.SessionEntry, error](entries), "work") {
			require.NoError(t, err)
			yield(entry)
		}
	})
	require.Len(t, got, 1)
	assert.Empty(t, got[0].ResponseID)
	assert.Contains(t, string(got[0].ReplayInput[0]), providerReplayReadable)
}

func TestActiveTurnForProviderProjectsCompletedOutputsWithoutMutation(t *testing.T) {
	checkpoint := rocketcode.ActiveTurnCheckpoint{
		TurnID:       "turn-1",
		DisplayModel: "openai/gpt",
		ResponseID:   providerReplayPrivate,
		ReplayInput: []json.RawMessage{
			json.RawMessage(`{"type":"function_call","id":"provider-private-sentinel","call_id":"portable-call","name":"read","arguments":"{}","status":"completed"}`),
		},
		OutputTrace:       []json.RawMessage{json.RawMessage(`{"private":"provider-private-sentinel"}`)},
		OpenFunctionCalls: []rocketcode.FunctionCallCheckpoint{{CallID: "open-call", Name: "bash", Arguments: json.RawMessage(`{"command":"printf portable"}`)}},
		CompletedFunctionOutputs: []rocketcode.FunctionOutputCheckpoint{{CallID: "portable-call", Name: "read", ReplayInput: []json.RawMessage{
			json.RawMessage(`{"type":"function_call_output","id":"provider-private-sentinel","call_id":"portable-call","status":"completed","output":"portable-tool-output"}`),
		}}},
	}
	want, err := json.Marshal(checkpoint)
	require.NoError(t, err)

	got, err := activeTurnForProvider(&checkpoint, "work")
	require.NoError(t, err)
	assert.Equal(t, checkpoint.DisplayModel, got.DisplayModel)
	assert.Empty(t, got.ResponseID)
	assert.Empty(t, got.OutputTrace)
	require.Len(t, got.CompletedFunctionOutputs, 1)
	assert.Contains(t, string(got.CompletedFunctionOutputs[0].ReplayInput[0]), "portable-tool-output")
	assert.NotContains(t, string(got.CompletedFunctionOutputs[0].ReplayInput[0]), providerReplayPrivate)
	assert.Equal(t, checkpoint.OpenFunctionCalls, got.OpenFunctionCalls)

	after, err := json.Marshal(checkpoint)
	require.NoError(t, err)
	assert.Equal(t, want, after)
}

func TestReplayForProviderDropsUnknownBeforePayloadDecode(t *testing.T) {
	replay, err := replayForProvider([]json.RawMessage{json.RawMessage(`{"type":"provider_hosted","role":{},"content":false,"output":42}`)})

	require.NoError(t, err)
	assert.Empty(t, replay)
}

func TestReplayForProviderReadsExactSummaryShapesAndPrefersCompactionContent(t *testing.T) {
	replay, err := replayForProvider([]json.RawMessage{
		json.RawMessage(`{"type":"reasoning","summary":"summary-string"}`),
		json.RawMessage(`{"type":"reasoning","summary":{"text":"summary-object"}}`),
		json.RawMessage(`{"type":"reasoning","summary":[{"text":"summary-array-one"},{"text":"summary-array-two"}]}`),
		json.RawMessage(`{"type":"compaction","content":{"text":"compaction-content"},"summary":"must-not-appear"}`),
		json.RawMessage(`{"type":"compaction","summary":{"text":"compaction-fallback"}}`),
	})
	require.NoError(t, err)

	items, err := rocketcode.ReplayInputToParams(replay)
	require.NoError(t, err)

	got := make([]string, len(items))
	for i := range items {
		got[i] = items[i].OfMessage.Content.OfString.Value
	}

	assert.Equal(t, []string{"summary-string", "summary-object", "summary-array-one", "summary-array-two", "compaction-content", "compaction-fallback"}, got)
}

func TestReplayForProviderRejectsMalformedKnownReadableData(t *testing.T) {
	for _, test := range []struct {
		name, raw, want string
	}{
		{name: "message null", raw: `{"type":"message","role":"user","content":null}`, want: "message content"},
		{name: "message object", raw: `{"type":"message","role":"user","content":{"text":"wrong container"}}`, want: "message content"},
		{name: "output null", raw: `{"type":"function_call_output","call_id":"call","output":null}`, want: "function output"},
		{name: "output object", raw: `{"type":"function_call_output","call_id":"call","output":{"text":"wrong container"}}`, want: "function output"},
		{name: "reasoning null", raw: `{"type":"reasoning","summary":null}`, want: "reasoning summary"},
		{name: "reasoning wrong text", raw: `{"type":"reasoning","summary":{"text":7}}`, want: "reasoning summary"},
		{name: "compaction null content", raw: `{"type":"compaction","content":null,"summary":"fallback"}`, want: "compaction content"},
		{name: "compaction null summary", raw: `{"type":"compaction","summary":null}`, want: "compaction summary"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := replayForProvider([]json.RawMessage{json.RawMessage(test.raw)})
			require.ErrorContains(t, err, test.want)
		})
	}

	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"type":"reasoning","encrypted_content":"private"}`),
		json.RawMessage(`{"type":"compaction","encrypted_content":"private"}`),
	} {
		replay, err := replayForProvider([]json.RawMessage{raw})
		require.NoError(t, err)
		assert.Empty(t, replay)
	}
}

func TestCrossProviderRepeatedRecoveryCheckpointKeepsProjectedBytes(t *testing.T) {
	original := rocketcode.ActiveTurnCheckpoint{DisplayModel: "openai/gpt", ReplayInput: []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","content":"portable-readable","id":"provider-private-sentinel"}`),
	}}
	projected, err := activeTurnForProvider(&original, "work")
	require.NoError(t, err)
	recovered, err := rocketcode.RecoveredReplayInput(&projected)
	require.NoError(t, err)

	sink := new(captureCheckpointSink)
	recheckpoint := &rocketcode.ActiveTurnCheckpoint{DisplayModel: "work/gpt", ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"continue"}`)}}
	require.NoError(t, sink.RecordRecoveredReplay(t.Context(), withRecoveredReplay(recheckpoint, recovered)))
	require.Len(t, sink.checkpoints, 1)
	want := slices.Clone(sink.checkpoints[0].ReplayInput)

	again, err := activeTurnForProvider(sink.checkpoints[0], "work")
	require.NoError(t, err)
	assert.Equal(t, want, again.ReplayInput)
	data, err := json.Marshal(again.ReplayInput)
	require.NoError(t, err)
	assert.NotContains(t, string(data), providerReplayPrivate)
}

func TestReplayForProviderDropsContentArrayItemsWithoutPortableParts(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","content":[{"type":"input_image","file_id":"hosted-image"},{"type":"input_file","file_id":"hosted-file"}]}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"call","output":[{"type":"input_image","file_id":"hosted-image"},{"type":"input_file","file_id":"hosted-file"}]}`),
	} {
		replay, err := replayForProvider([]json.RawMessage{raw})
		require.NoError(t, err)
		assert.Empty(t, replay)
	}

	replay, err := replayForProvider([]json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","content":[{"type":"input_image","file_id":"drop"},{"type":"input_image","image_url":"data:image/png;base64,keep"},{"type":"input_file","file_id":"drop"},{"type":"input_file","file_data":"keep-data"}]}`),
	})
	require.NoError(t, err)
	require.Len(t, replay, 1)
	assert.NotContains(t, string(replay[0]), `file_id`)
	assert.Contains(t, string(replay[0]), "base64,keep")
	assert.Contains(t, string(replay[0]), "keep-data")
}

func TestReplayForProviderCompactionFallsBackAfterBlankContent(t *testing.T) {
	replay, err := replayForProvider([]json.RawMessage{
		json.RawMessage(`{"type":"compaction","content":"","summary":"fallback-string"}`),
		json.RawMessage(`{"type":"compaction","content":"  \n","summary":{"text":"fallback-whitespace"}}`),
		json.RawMessage(`{"type":"compaction","content":{"text":""},"summary":[{"text":"fallback-object"}]}`),
		json.RawMessage(`{"type":"compaction","content":[],"summary":"fallback-array"}`),
	})
	require.NoError(t, err)

	items, err := rocketcode.ReplayInputToParams(replay)
	require.NoError(t, err)

	got := make([]string, len(items))
	for i := range items {
		got[i] = items[i].OfMessage.Content.OfString.Value
	}

	assert.Equal(t, []string{"fallback-string", "fallback-whitespace", "fallback-object", "fallback-array"}, got)
}

func TestReplayForProviderValidatesRequiredKnownFields(t *testing.T) {
	for _, test := range []struct {
		name, raw, want string
	}{
		{name: "message role missing", raw: `{"type":"message","content":"text"}`, want: "message role"},
		{name: "message role blank", raw: `{"type":"message","role":"  ","content":"text"}`, want: "message role"},
		{name: "call id missing", raw: `{"type":"function_call","name":"read","arguments":"{}"}`, want: "function call call_id"},
		{name: "call id blank", raw: `{"type":"function_call","call_id":" ","name":"read","arguments":"{}"}`, want: "function call call_id"},
		{name: "call name missing", raw: `{"type":"function_call","call_id":"call","arguments":"{}"}`, want: "function call name"},
		{name: "call name blank", raw: `{"type":"function_call","call_id":"call","name":" ","arguments":"{}"}`, want: "function call name"},
		{name: "call arguments missing", raw: `{"type":"function_call","call_id":"call","name":"read"}`, want: "function call arguments"},
		{name: "call arguments null", raw: `{"type":"function_call","call_id":"call","name":"read","arguments":null}`, want: "function call arguments"},
		{name: "call arguments object", raw: `{"type":"function_call","call_id":"call","name":"read","arguments":{}}`, want: "function call arguments"},
		{name: "output call id missing", raw: `{"type":"function_call_output","output":"done"}`, want: "function output call_id"},
		{name: "output call id blank", raw: `{"type":"function_call_output","call_id":" ","output":"done"}`, want: "function output call_id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := replayForProvider([]json.RawMessage{json.RawMessage(test.raw)})
			require.ErrorContains(t, err, test.want)
		})
	}

	replay, err := replayForProvider([]json.RawMessage{json.RawMessage(`{"type":"function_call","call_id":"call","name":"read","arguments":""}`)})
	require.NoError(t, err)
	require.Len(t, replay, 1)
	assert.Contains(t, string(replay[0]), `"arguments":""`)
}
