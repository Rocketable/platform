package harness

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	providerReplayReadable = "portable-readable"
	providerReplayPrivate  = "provider-private-sentinel"
)

func TestSessionEntryForProviderSameProviderIsUnchanged(t *testing.T) {
	t.Parallel()

	entry := SessionEntry{
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

	require.NoError(t, entry.forProvider("work"))
	data, err := json.Marshal(entry)
	require.NoError(t, err)
	assert.Equal(t, want, data)
}

func TestSessionEntryForProviderDifferentProviderProjectsReplay(t *testing.T) {
	t.Parallel()

	entry := SessionEntry{
		Version:    1,
		Type:       "turn",
		Timestamp:  time.Unix(1, 0).UTC(),
		ResponseID: providerReplayPrivate,
		Model:      "openai/gpt",
		ReplayInput: []json.RawMessage{
			json.RawMessage(`{"type":"message","role":"user","phase":"commentary","id":"provider-private-sentinel","content":[{"type":"input_text","text":"portable-readable","private":"provider-private-sentinel"},{"type":"input_image","file_id":"provider-private-sentinel","image_url":"data:image/png;base64,portable-image","detail":"high"},{"type":"input_file","file_id":"provider-private-sentinel","file_url":"https://example.test/portable-file","file_data":"portable-data","filename":"portable.txt","detail":"low"}]}`),
			json.RawMessage(`{"type":"function_call","id":"provider-private-sentinel","call_id":"portable-call","name":"read","arguments":"{\"path\":\"portable.txt\"}","status":"completed","private":"provider-private-sentinel"}`),
			json.RawMessage(`{"type":"function_call_output","id":"provider-private-sentinel","call_id":"portable-call","status":"completed","output":[{"type":"input_text","text":"portable-tool-output"},{"type":"input_image","file_id":"provider-private-sentinel","image_url":"data:image/png;base64,portable-output-image"},{"type":"input_file","file_id":"provider-private-sentinel","file_data":"portable-output-data","filename":"output.txt"}],"private":"provider-private-sentinel"}`),
			json.RawMessage(`{"type":"function_call_output","id":"provider-private-sentinel","call_id":"portable-string-call","output":"portable-string-output"}`),
			json.RawMessage(`{"type":"reasoning","id":"provider-private-sentinel","encrypted_content":"provider-private-sentinel","summary":[{"type":"summary_text","text":"portable-summary-one"},{"type":"summary_text","text":"  "},{"type":"summary_text","text":"portable-summary-two"}],"content":[{"type":"reasoning_text","text":"provider-private-sentinel"}],"status":"completed"}`),
			json.RawMessage(`{"type":"reasoning","encrypted_content":"provider-private-sentinel","summary":[]}`),
			json.RawMessage(`{"type":"compaction","id":"provider-private-sentinel","encrypted_content":"provider-private-sentinel","content":"portable-compaction"}`),
			json.RawMessage(`{"type":"compaction","encrypted_content":"provider-private-sentinel"}`),
			json.RawMessage(`{"type":"provider_hosted","value":"provider-private-sentinel"}`),
		},
		OutputTrace: []json.RawMessage{json.RawMessage(`{"private":"provider-private-sentinel"}`), json.RawMessage(`{"type":"rocketcode_public_progress","progress":{"id":"item/0","parent_id":"turn/response","kind":"text","state":"completed","text":"early-public"}}`)},
	}

	original, err := json.Marshal(entry)
	require.NoError(t, err)

	got := entry
	require.NoError(t, got.forProvider("work"))
	assert.Equal(t, entry.Model, got.Model)

	data, err := json.Marshal(got.ReplayInput)
	require.NoError(t, err)

	text := string(data)
	for _, want := range []string{providerReplayReadable, "portable-image", "portable-file", "portable-data", "portable.txt", "portable-call", "read", "portable-tool-output", "portable-output-image", "portable-output-data", "output.txt", "portable-string-call", "portable-string-output", "portable-summary-one", "portable-summary-two", "portable-compaction", "commentary"} {
		assert.Contains(t, text, want)
	}

	assert.NotContains(t, text, providerReplayPrivate)
	assert.NotContains(t, text, "provider_hosted")
	assert.Len(t, got.ReplayInput, 7)

	unchanged, err := json.Marshal(entry)
	require.NoError(t, err)
	assert.Equal(t, original, unchanged)
}

func TestSessionEntryForProviderTreatsMissingModelAsOpenAI(t *testing.T) {
	t.Parallel()

	entry := SessionEntry{Model: "", ReplayInput: []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","id":"provider-private-sentinel","content":"portable-readable"}`)}}
	require.NoError(t, entry.forProvider("work"))
	assert.Contains(t, string(entry.ReplayInput[0]), providerReplayReadable)
	assert.NotContains(t, string(entry.ReplayInput[0]), providerReplayPrivate)
}

func TestSessionEntryForProviderConvertsOnlyAnotherProvidersRanges(t *testing.T) {
	t.Parallel()

	native := json.RawMessage(`{"type":"reasoning","encrypted_content":"work-native","summary":[]}`)
	entry := SessionEntry{Model: "work/gpt", ReplayInput: []json.RawMessage{
		native,
		json.RawMessage(`{"type":"reasoning","encrypted_content":"provider-private-sentinel","summary":[{"type":"summary_text","text":"portable-readable"},{"type":"summary_text","text":"portable-second"}]}`),
		native,
	}, ReplayAttribution: []ReplayAttribution{{Start: 1, End: 2, Model: "openai/gpt"}}}
	require.NoError(t, entry.forProvider("work"))
	require.Len(t, entry.ReplayInput, 4)
	assert.Equal(t, native, entry.ReplayInput[0])
	assert.Contains(t, string(entry.ReplayInput[1]), providerReplayReadable)
	assert.NotContains(t, string(entry.ReplayInput[1]), providerReplayPrivate)
	assert.Contains(t, string(entry.ReplayInput[2]), "portable-second")
	assert.Equal(t, native, entry.ReplayInput[3])
	assert.Equal(t, []ReplayAttribution{{Start: 1, End: 3, Model: "openai/gpt"}}, entry.ReplayAttribution)
}

func TestReplayForProviderDropsUnknownBeforePayloadDecode(t *testing.T) {
	t.Parallel()

	replay, err := replayForWork(json.RawMessage(`{"type":"provider_hosted","role":{},"content":false,"output":42}`))

	require.NoError(t, err)
	assert.Empty(t, replay)
}

func TestReplayForProviderRejectsMalformedKnownReadableData(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, raw, want string
	}{
		{name: "invalid JSON", raw: `{"type":`, want: "replay item 0 type: unexpected end of JSON input"},
		{name: "wrong type field", raw: `{"type":7}`, want: "replay item 0 type: json: cannot unmarshal number"},
		{name: "wrong message role", raw: `{"type":"message","role":{},"content":"text"}`, want: "replay item 0 message: json: cannot unmarshal object"},
		{name: "wrong call id", raw: `{"type":"function_call","call_id":7,"name":"read","arguments":"{}"}`, want: "function call decode fields: json: cannot unmarshal number"},
		{name: "wrong output call id", raw: `{"type":"function_call_output","call_id":7,"output":"done"}`, want: "function output: json: cannot unmarshal number"},
		{name: "message content missing", raw: `{"type":"message","role":"user"}`, want: "message content: missing"},
		{name: "message content wrong text", raw: `{"type":"message","role":"user","content":[{"type":"input_text","text":7}]}`, want: "message content: decode array: json: cannot unmarshal number"},
		{name: "message null", raw: `{"type":"message","role":"user","content":null}`, want: "message content"},
		{name: "message object", raw: `{"type":"message","role":"user","content":{"text":"wrong container"}}`, want: "message content"},
		{name: "output null", raw: `{"type":"function_call_output","call_id":"call","output":null}`, want: "function output"},
		{name: "output object", raw: `{"type":"function_call_output","call_id":"call","output":{"text":"wrong container"}}`, want: "function output"},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry := SessionEntry{Model: "openai/model", ReplayInput: []json.RawMessage{json.RawMessage(test.raw)}}
			require.ErrorContains(t, entry.forProvider("other"), test.want)
			require.Equal(t, []json.RawMessage{json.RawMessage(test.raw)}, entry.ReplayInput)
		})
	}

	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"type":"reasoning","encrypted_content":"private"}`),
		json.RawMessage(`{"type":"compaction","encrypted_content":"private"}`),
		json.RawMessage(`{"type":"reasoning","summary":"old"}`),
		json.RawMessage(`{"type":"compaction","summary":{"text":"old"}}`),
		json.RawMessage(`{"type":"compaction_summary","content":"old"}`),
	} {
		replay, err := replayForWork(raw)
		require.NoError(t, err)
		assert.Empty(t, replay)
	}
}

func TestReplayForProviderDropsContentArrayItemsWithoutPortableParts(t *testing.T) {
	t.Parallel()

	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","content":[]}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"call","output":[]}`),
		json.RawMessage(`{"type":"message","role":"user","content":[{"type":"input_image","file_id":"hosted-image"},{"type":"input_file","file_id":"hosted-file"}]}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"call","output":[{"type":"input_image","file_id":"hosted-image"},{"type":"input_file","file_id":"hosted-file"}]}`),
	} {
		replay, err := replayForWork(raw)
		require.NoError(t, err)
		assert.Empty(t, replay)
	}

	replay, err := replayForWork(
		json.RawMessage(`{"type":"message","role":"user","content":[{"type":"input_image","file_id":"drop"},{"type":"input_image","image_url":"data:image/png;base64,keep"},{"type":"input_file","file_id":"drop"},{"type":"input_file","file_data":"keep-data"}]}`),
	)
	require.NoError(t, err)
	require.Len(t, replay, 1)
	assert.NotContains(t, string(replay[0]), `file_id`)
	assert.Contains(t, string(replay[0]), "base64,keep")
	assert.Contains(t, string(replay[0]), "keep-data")

	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"assistant","phase":"final_answer","content":""}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"call","output":""}`),
	} {
		replay, err := replayForWork(raw)
		require.NoError(t, err)
		require.Len(t, replay, 1, "empty strings survive even though empty content arrays do not")
		require.JSONEq(t, string(raw), string(replay[0]))
	}
}

func TestReplayForProviderValidatesRequiredKnownFields(t *testing.T) {
	t.Parallel()

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
			_, err := replayForWork(json.RawMessage(test.raw))
			require.ErrorContains(t, err, test.want)
		})
	}

	replay, err := replayForWork(json.RawMessage(`{"type":"function_call","call_id":"call","name":"read","arguments":""}`))
	require.NoError(t, err)
	require.Len(t, replay, 1)
	assert.Contains(t, string(replay[0]), `"arguments":""`)
}

func replayForWork(raw ...json.RawMessage) ([]json.RawMessage, error) {
	entry := SessionEntry{ReplayInput: raw}
	err := entry.forProvider("work")

	return entry.ReplayInput, err
}
