package rocketcode

import (
	"encoding/json"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
)

func TestAnthropicParamsMapsTextAndTools(t *testing.T) {
	looper := &looper{Model: "claude-sonnet", DisplayModel: "anthropic/claude-sonnet"}
	params := looper.buildParams([]responses.ResponseInputItemUnionParam{inputMessageParam(responses.EasyInputMessageRoleUser, easyInputStringContent("hello"))})
	params.Tools = []responses.ToolUnionParam{{OfFunction: &responses.FunctionToolParam{Name: "read", Description: openai.String("read files"), Parameters: map[string]any{"type": "object", "properties": map[string]any{"filePath": map[string]any{"type": "string"}}, "required": []string{"filePath"}}}}}

	body, err := looper.anthropicParams(&params)

	require.NoError(t, err)
	require.Equal(t, anthropic.Model("claude-sonnet"), body.Model)
	require.Len(t, body.Messages, 1)
	require.Len(t, body.Tools, 1)
	require.Equal(t, "read", *body.Tools[0].GetName())
}

func TestAnthropicResponseMapsToolUse(t *testing.T) {
	input := json.RawMessage(`{"filePath":"README.md"}`)
	message := &anthropic.Message{ID: "msg_1", Content: []anthropic.ContentBlockUnion{{Type: "tool_use", ID: "toolu_1", Name: "read", Input: input}}}

	response := anthropicResponse(message)

	require.Equal(t, "msg_1", response.ID)
	require.Len(t, response.Output, 1)
	require.Equal(t, "function_call", response.Output[0].Type)
	require.Equal(t, "toolu_1", response.Output[0].CallID)
	require.Equal(t, "read", response.Output[0].Name)
	require.Equal(t, string(input), response.Output[0].Arguments.OfString)
}
