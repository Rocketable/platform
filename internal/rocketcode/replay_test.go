package rocketcode

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReplayInputPreservesCompactionMetadata(t *testing.T) {
	raw := []json.RawMessage{json.RawMessage(`{"content":"summary","summary":{"text":"summary"},"recent":[{"id":"msg-1"}],"encrypted_content":"encrypted","id":"cmp-1","origin_compatible_provider":"local","origin_mode":"responses","origin_provider":"openai-compatible","type":"compaction"}`)}

	params, err := ReplayInputToParams(raw)
	require.NoError(t, err)

	got, err := ReplayInputFromParams(params)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.JSONEq(t, string(raw[0]), string(got[0]))
}
