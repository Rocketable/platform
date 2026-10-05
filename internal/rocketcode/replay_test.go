package rocketcode

import (
	"encoding/json"
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
)

func TestResponseOutputReplayText(t *testing.T) {
	for _, tt := range []struct {
		name, input, want string
	}{
		{
			name:  "message parts",
			input: `{"type":"message","id":"msg","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":" first\n"},{"type":"refusal","refusal":"refused"},{"type":"output_text","text":"世界 "}]}`,
			want:  `{"type":"message","id":"msg","role":"assistant","phase":"final_answer","content":" first\n世界 "}`,
		},
		{
			name:  "empty message",
			input: `{"type":"message","id":"msg"}`,
			want:  `{"type":"message","id":"msg","role":"assistant","content":""}`,
		},
		{
			name:  "compaction content",
			input: `{"type":"compaction","id":"cmp","encrypted_content":"sealed","content":[{"type":"output_text","text":" first\n"},{"type":"refusal","refusal":"refused"},{"type":"output_text","text":"世界 "}]}`,
			want:  `{"type":"compaction","id":"cmp","encrypted_content":"sealed","content":" first\n世界 "}`,
		},
		{
			name:  "empty summary uses content",
			input: `{"type":"compaction_summary","id":"cmp","encrypted_content":"sealed","summary":[{"text":""}],"content":[{"type":"output_text","text":"fallback"}]}`,
			want:  `{"type":"compaction","id":"cmp","encrypted_content":"sealed","content":"fallback"}`,
		},
		{
			name:  "summary takes precedence",
			input: `{"type":"compaction","id":"cmp","encrypted_content":"sealed","summary":[{"text":"summary"},{"text":"later"}],"content":[{"type":"output_text","text":"fallback"}]}`,
			want:  `{"type":"compaction","id":"cmp","encrypted_content":"sealed","content":"summary"}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var item responses.ResponseOutputItemUnion
			require.NoError(t, json.Unmarshal([]byte(tt.input), &item))
			got, ok := responseOutputToReplayInput(&item)
			require.True(t, ok)
			require.JSONEq(t, tt.want, marshalReplayJSON(t, got))
		})
	}
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

func TestReplayInputPreservesMessageHeaderLocally(t *testing.T) {
	const header = `[Slack principal="alice"]`

	raw := []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","input_id":"input-1","prompt_header":"[Slack principal=\"alice\"]","content":"[Slack principal=\"alice\"]\n\n[literal]\n\nbody"}`),
		json.RawMessage(`{"type":"message","role":"user","input_id":"input-2","content":"body"}`),
		json.RawMessage(`{"type":"message","id":"provider-item","role":"assistant","phase":"final_answer","content":"answer"}`),
	}
	items, err := ReplayInputToParams(raw)
	require.NoError(t, err)
	require.Equal(t, header, items[0].OfMessage.ExtraFields()["prompt_header"])
	require.Equal(t, "input-1", items[0].OfMessage.ExtraFields()["input_id"])
	stored, err := ReplayInputFromParams(items)
	require.NoError(t, err)
	require.JSONEq(t, string(raw[0]), string(stored[0]))
	require.JSONEq(t, string(raw[1]), string(stored[1]))
	require.JSONEq(t, string(raw[2]), string(stored[2]))

	projected, err := ReplayInputFromParams(projectReplayForOpenAI(items))
	require.NoError(t, err)
	require.NotContains(t, string(projected[0]), "prompt_header")
	require.NotContains(t, string(projected[0]), "input_id")
	require.JSONEq(t, `{"type":"message","role":"user","content":"body"}`, string(projected[1]))
	require.JSONEq(t, `{"type":"message","role":"assistant","phase":"final_answer","content":"answer"}`, string(projected[2]), "item identity stays local without changing provider message shape")
	require.Equal(t, header+"\n\n[literal]\n\nbody", items[0].OfMessage.Content.OfString.Value)
	require.Equal(t, header, items[0].OfMessage.ExtraFields()["prompt_header"])
	require.Equal(t, "input-1", items[0].OfMessage.ExtraFields()["input_id"])
	require.Equal(t, "input-2", items[1].OfMessage.ExtraFields()["input_id"])
}

func marshalReplayJSON(t *testing.T, value any) string {
	t.Helper()

	data, err := json.Marshal(value)
	require.NoError(t, err)

	return string(data)
}
