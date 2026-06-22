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
	looper := &looper{provider: modelProviderAnthropic, modelRef: modelRef{provider: modelProviderAnthropic, apiModel: "claude-sonnet"}, Model: "claude-sonnet", DisplayModel: "anthropic/claude-sonnet"}
	params := looper.buildParams([]responses.ResponseInputItemUnionParam{inputMessageParam(responses.EasyInputMessageRoleUser, easyInputStringContent("hello"))})
	params.Tools = []responses.ToolUnionParam{{OfFunction: &responses.FunctionToolParam{Name: "read", Description: openai.String("read files"), Parameters: map[string]any{"type": "object", "properties": map[string]any{"filePath": map[string]any{"type": "string"}}, "required": []string{"filePath"}}}}}

	body, err := looper.anthropicParams(&params)

	require.NoError(t, err)
	require.Equal(t, anthropic.Model("claude-sonnet"), body.Model)
	require.Contains(t, body.Betas, anthropic.AnthropicBeta("compact-2026-01-12"))
	require.Len(t, body.ContextManagement.Edits, 1)
	require.NotNil(t, body.ContextManagement.Edits[0].OfCompact20260112)
	require.True(t, body.ContextManagement.Edits[0].OfCompact20260112.PauseAfterCompaction.Value)
	require.Len(t, body.Messages, 1)
	require.Len(t, body.Tools, 1)
	require.Equal(t, "read", *body.Tools[0].GetName())
}

func TestAnthropicResponseMapsToolUse(t *testing.T) {
	input := json.RawMessage(`{"filePath":"README.md"}`)
	message := &anthropic.BetaMessage{ID: "msg_1", Content: []anthropic.BetaContentBlockUnion{{Type: "tool_use", ID: "toolu_1", Name: "read", Input: input}}}

	response := anthropicResponse(message)

	require.Equal(t, "msg_1", response.ID)
	require.Len(t, response.Output, 1)
	require.Equal(t, "function_call", response.Output[0].Type)
	require.Equal(t, "toolu_1", response.Output[0].CallID)
	require.Equal(t, "read", response.Output[0].Name)
	require.Equal(t, string(input), response.Output[0].Arguments.OfString)
}

func TestAnthropicResponseMapsUsage(t *testing.T) {
	var message anthropic.BetaMessage
	require.NoError(t, json.Unmarshal([]byte(`{"id":"msg_1","usage":{"input_tokens":12,"cache_read_input_tokens":3,"cache_creation_input_tokens":4,"output_tokens":5,"output_tokens_details":{"thinking_tokens":2}},"content":[{"type":"text","text":"done"}]}`), &message))

	response := anthropicResponse(&message)
	usage, ok := tokenUsageFromResponse(response)

	require.True(t, ok)
	require.Equal(t, TokenUsage{PromptTokens: 12, CompletionTokens: 5, TotalTokens: 17, PromptCacheReadTokens: 3, PromptCacheWriteTokens: 4, CompletionReasoningTokens: 2}, usage)
}

func TestAnthropicCompactionRoundTripsThroughReplay(t *testing.T) {
	var message anthropic.BetaMessage
	require.NoError(t, json.Unmarshal([]byte(`{"id":"msg_1","content":[{"type":"compaction","content":"summary of prior work","encrypted_content":"encrypted-compact"},{"type":"text","text":"continuing"}]}`), &message))

	response := anthropicResponse(&message)

	require.Equal(t, "msg_1", response.ID)
	require.Len(t, response.Output, 2)
	require.Equal(t, "compaction", response.Output[0].Type)
	require.Equal(t, "msg_1-compaction-0", response.Output[0].ID)
	require.Equal(t, "encrypted-compact", response.Output[0].EncryptedContent)
	require.Equal(t, "summary of prior work", response.Output[0].Summary[0].Text)

	replayInput, ok := responseOutputToReplayInput(&response.Output[0], modelProviderAnthropic, "", "messages")
	require.True(t, ok)

	raw, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{replayInput})
	require.NoError(t, err)
	require.JSONEq(t, `{"content":"summary of prior work","encrypted_content":"encrypted-compact","id":"msg_1-compaction-0","origin_mode":"messages","origin_provider":"anthropic","type":"compaction"}`, string(raw[0]))

	items, err := ReplayInputToParams(raw)
	require.NoError(t, err)

	looper := &looper{provider: modelProviderAnthropic, modelRef: modelRef{provider: modelProviderAnthropic, apiModel: "claude-sonnet"}, Model: "claude-sonnet", DisplayModel: "anthropic/claude-sonnet"}
	params := looper.buildParams(items)
	body, err := looper.anthropicParams(&params)
	require.NoError(t, err)

	require.Len(t, body.Messages, 1)
	require.Len(t, body.Messages[0].Content, 1)
	compaction := body.Messages[0].Content[0].OfCompaction
	require.NotNil(t, compaction)
	require.Equal(t, "summary of prior work", compaction.Content.Value)
	require.Equal(t, "encrypted-compact", compaction.EncryptedContent.Value)
}

func TestAnthropicParamsProjectsForeignCompactionAsCheckpoint(t *testing.T) {
	foreign := compactionReplayInput("cmp-foreign", "foreign-encrypted", "foreign summary", modelProviderOpenAICompatible, "local", string(OpenAICompatibleModeChatCompletions))
	raw, err := ReplayInputFromParams([]responses.ResponseInputItemUnionParam{foreign})
	require.NoError(t, err)
	items, err := ReplayInputToParams(raw)
	require.NoError(t, err)

	looper := &looper{provider: modelProviderAnthropic, modelRef: modelRef{provider: modelProviderAnthropic, apiModel: "claude-sonnet"}, Model: "claude-sonnet", DisplayModel: "anthropic/claude-sonnet"}
	params := looper.buildParams(items)
	body, err := looper.anthropicParams(&params)
	require.NoError(t, err)

	require.Len(t, body.Messages, 1)
	serialized := marshalJSON(t, body.Messages)
	require.Contains(t, serialized, "foreign summary")
	require.Contains(t, serialized, "context_checkpoint")
	require.NotContains(t, serialized, "foreign-encrypted")
}

func TestAnthropicCompactionPreservesEncryptedContentWithoutSummary(t *testing.T) {
	var message anthropic.BetaMessage
	require.NoError(t, json.Unmarshal([]byte(`{"id":"msg_1","content":[{"type":"compaction","content":null,"encrypted_content":"encrypted-compact"}]}`), &message))

	response := anthropicResponse(&message)

	require.Len(t, response.Output, 1)
	require.Equal(t, "compaction", response.Output[0].Type)
	require.Equal(t, "encrypted-compact", response.Output[0].EncryptedContent)
	require.Empty(t, response.Output[0].Summary)
}
