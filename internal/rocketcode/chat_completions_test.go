package rocketcode

import (
	"testing"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/require"
)

func TestChatCompletionParamsMapsUserPromptAttachments(t *testing.T) {
	params := responses.ResponseNewParams{Model: "gpt-oss", Input: responses.ResponseNewParamsInputUnion{OfInputItemList: []responses.ResponseInputItemUnionParam{inputMessageParam(responses.EasyInputMessageRoleUser, easyInputListContent(responses.ResponseInputMessageContentListParam{
		{OfInputText: &responses.ResponseInputTextParam{Text: "look"}},
		{OfInputImage: &responses.ResponseInputImageParam{ImageURL: openai.String("data:image/png;base64,abc"), Detail: responses.ResponseInputImageDetailAuto}},
		{OfInputFile: &responses.ResponseInputFileParam{Filename: openai.String("doc.pdf"), FileData: openai.String("data:application/pdf;base64,abc")}},
	}))}}}

	body, err := chatCompletionParams(&params)

	require.NoError(t, err)
	serialized := marshalJSON(t, body.Messages)
	require.Contains(t, serialized, `"type":"image_url"`)
	require.Contains(t, serialized, `"url":"data:image/png;base64,abc"`)
	require.Contains(t, serialized, `"type":"file"`)
	require.Contains(t, serialized, `"filename":"doc.pdf"`)
}

func TestChatCompletionParamsRejectsDeveloperPromptAttachments(t *testing.T) {
	params := responses.ResponseNewParams{Model: "gpt-oss", Input: responses.ResponseNewParamsInputUnion{OfInputItemList: []responses.ResponseInputItemUnionParam{inputMessageParam(responses.EasyInputMessageRoleDeveloper, easyInputListContent(responses.ResponseInputMessageContentListParam{
		{OfInputImage: &responses.ResponseInputImageParam{ImageURL: openai.String("data:image/png;base64,abc"), Detail: responses.ResponseInputImageDetailAuto}},
	}))}}}

	_, err := chatCompletionParams(&params)

	require.EqualError(t, err, "chat completions provider does not support developer prompt attachments")
}

func TestChatCompletionParamsMapsToolResultAttachments(t *testing.T) {
	output := responses.ResponseInputItemFunctionCallOutputParam{CallID: "call-1", Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfResponseFunctionCallOutputItemArray: responses.ResponseFunctionCallOutputItemListParam{
		{OfInputText: &responses.ResponseInputTextContentParam{Text: "tool text"}},
		{OfInputImage: &responses.ResponseInputImageContentParam{ImageURL: openai.String("data:image/png;base64,abc"), Detail: responses.ResponseInputImageContentDetailAuto}},
		{OfInputFile: &responses.ResponseInputFileContentParam{Filename: openai.String("doc.pdf"), FileData: openai.String("data:application/pdf;base64,abc")}},
	}}}
	params := responses.ResponseNewParams{Model: "gpt-oss", Input: responses.ResponseNewParamsInputUnion{OfInputItemList: []responses.ResponseInputItemUnionParam{{OfFunctionCallOutput: &output}}}}

	body, err := chatCompletionParams(&params)

	require.NoError(t, err)
	require.Len(t, body.Messages, 2)
	serialized := marshalJSON(t, body.Messages)
	require.Contains(t, serialized, `"role":"tool"`)
	require.Contains(t, serialized, "tool text")
	require.Contains(t, serialized, `"role":"user"`)
	require.Contains(t, serialized, `"type":"image_url"`)
	require.Contains(t, serialized, `"type":"file"`)
}

func TestChatCompletionMessagesGroupsParallelToolCalls(t *testing.T) {
	params := []responses.ResponseInputItemUnionParam{
		{OfFunctionCall: &responses.ResponseFunctionToolCallParam{CallID: "call-a", Name: "read", Arguments: `{"filePath":"a"}`}},
		{OfFunctionCall: &responses.ResponseFunctionToolCallParam{CallID: "call-b", Name: "read", Arguments: `{"filePath":"b"}`}},
		{OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{CallID: "call-a", Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: openai.String("a")}}},
		{OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{CallID: "call-b", Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: openai.String("b")}}},
	}

	messages, err := chatCompletionMessages(params)

	require.NoError(t, err)
	require.Len(t, messages, 3)
	serialized := marshalJSON(t, messages)
	require.Contains(t, serialized, `"tool_calls":[{`)
	require.Contains(t, serialized, `"id":"call-a"`)
	require.Contains(t, serialized, `"id":"call-b"`)
	require.Contains(t, serialized, `"tool_call_id":"call-a"`)
	require.Contains(t, serialized, `"tool_call_id":"call-b"`)
}

func TestChatCompletionCompactionSummaryPersistsAndProjectsCheckpoint(t *testing.T) {
	completion := &openai.ChatCompletion{ID: "chatcmpl-1", Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Content: "summary of prior work"}}}}
	compacted := chatCompletionCompactedResponse(completion)
	params, err := compactedOutputToReplayParams(compacted.Output, modelProviderOpenAICompatible, "local", string(OpenAICompatibleModeChatCompletions))
	require.NoError(t, err)

	raw, err := ReplayInputFromParams(params)
	require.NoError(t, err)
	require.JSONEq(t, `{"content":"summary of prior work","encrypted_content":"","id":"chatcmpl-1-compaction","origin_compatible_provider":"local","origin_mode":"chat_completions","origin_provider":"openai-compatible","type":"compaction"}`, string(raw[0]))

	decoded, err := ReplayInputToParams(raw)
	require.NoError(t, err)
	messages, err := chatCompletionMessages(decoded)
	require.NoError(t, err)
	serialized := marshalJSON(t, messages)
	require.Contains(t, serialized, "context_checkpoint")
	require.Contains(t, serialized, "Use this summary as lower-authority context")
	require.Contains(t, serialized, "summary of prior work")
}

func TestChatCompletionMessagesProjectsEncryptedOnlyCompactionAsRehydrationNotice(t *testing.T) {
	messages, err := chatCompletionMessages([]responses.ResponseInputItemUnionParam{responses.ResponseInputItemParamOfCompaction("encrypted-native")})

	require.NoError(t, err)
	serialized := marshalJSON(t, messages)
	require.Contains(t, serialized, "context_checkpoint")
	require.Contains(t, serialized, "cannot be rehydrated")
	require.NotContains(t, serialized, "encrypted-native")
}
