package rocketcode

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	openairespjson "github.com/openai/openai-go/v3/packages/respjson"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

type chatCompletionServiceClient struct {
	service *openai.ChatCompletionService
}

// Summary sections follow OpenCode's structured compaction template:
// /Users/ucirello/Projects/opencode/packages/core/src/session/compaction.ts.
const chatCompletionCompactionInstructions = `Summarize the conversation so a later assistant can continue correctly. Do not answer the user.

Output only the compacted summary using these sections:

## Goal
The user's objective and the current task.

## Constraints & Preferences
Explicit user requirements, project rules, provider constraints, and style preferences.

## Progress
What has been completed and what remains in progress.

## Key Decisions
Important choices, accepted tradeoffs, and behavior contracts established so far.

## Next Steps
Concrete remaining actions in order.

## Critical Context
Details that are easy to lose but necessary to continue correctly, including tool results, errors, and unresolved risks.

## Relevant Files
Files, symbols, commands, and code references that matter for continuation.`

func (c chatCompletionServiceClient) New(ctx context.Context, params *responses.ResponseNewParams, opts ...option.RequestOption) (*responses.Response, error) {
	body, err := chatCompletionParams(params)
	if err != nil {
		return nil, err
	}

	completion, err := c.service.New(ctx, body, opts...)
	if err != nil {
		return nil, fmt.Errorf("create chat completion: %w", err)
	}

	return chatCompletionResponse(completion), nil
}

func (c chatCompletionServiceClient) Compact(ctx context.Context, params *responses.ResponseCompactParams, opts ...option.RequestOption) (*responses.CompactedResponse, error) {
	body, err := chatCompletionCompactParams(params)
	if err != nil {
		return nil, err
	}

	completion, err := c.service.New(ctx, body, opts...)
	if err != nil {
		return nil, fmt.Errorf("compact chat completion: %w", err)
	}

	return chatCompletionCompactedResponse(completion), nil
}

func chatCompletionParams(params *responses.ResponseNewParams) (openai.ChatCompletionNewParams, error) {
	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(params.Input.OfInputItemList)+1)
	if params.Instructions.Valid() && strings.TrimSpace(params.Instructions.Value) != "" {
		messages = append(messages, openai.DeveloperMessage(params.Instructions.Value))
	}

	inputMessages, err := chatCompletionMessages(params.Input.OfInputItemList)
	if err != nil {
		return openai.ChatCompletionNewParams{}, err
	}

	messages = append(messages, inputMessages...)
	body := openai.ChatCompletionNewParams{
		Messages:          messages,
		Model:             params.Model,
		Store:             params.Store,
		ParallelToolCalls: params.ParallelToolCalls,
	}

	if params.Reasoning.Effort != "" {
		body.ReasoningEffort = params.Reasoning.Effort
	}

	if params.Text.Verbosity != "" {
		body.Verbosity = openai.ChatCompletionNewParamsVerbosity(params.Text.Verbosity)
	}

	if params.Text.Format.GetType() != nil {
		format, err := chatCompletionResponseFormat(params.Text.Format)
		if err != nil {
			return openai.ChatCompletionNewParams{}, err
		}

		body.ResponseFormat = format
	}

	if len(params.Tools) > 0 {
		tools, err := chatCompletionTools(params.Tools)
		if err != nil {
			return openai.ChatCompletionNewParams{}, err
		}

		body.Tools = tools
	}

	return body, nil
}

func chatCompletionMessages(items []responses.ResponseInputItemUnionParam) ([]openai.ChatCompletionMessageParamUnion, error) {
	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(items))
	for i := 0; i < len(items); i++ {
		item := &items[i]
		switch {
		case item.OfMessage != nil:
			text, parts, err := chatCompletionContent(item.OfMessage.Content)
			if err != nil {
				return nil, err
			}

			switch item.OfMessage.Role {
			case responses.EasyInputMessageRoleSystem:
				if len(parts) > 0 {
					return nil, errors.New("chat completions provider does not support system prompt attachments")
				}

				messages = append(messages, openai.SystemMessage(text))
			case responses.EasyInputMessageRoleDeveloper:
				if len(parts) > 0 {
					return nil, errors.New("chat completions provider does not support developer prompt attachments")
				}

				messages = append(messages, openai.DeveloperMessage(text))
			case responses.EasyInputMessageRoleAssistant:
				if len(parts) > 0 {
					return nil, errors.New("chat completions provider does not support assistant prompt attachments")
				}

				messages = append(messages, openai.AssistantMessage(text))
			case responses.EasyInputMessageRoleUser:
				if len(parts) > 0 {
					messages = append(messages, openai.UserMessage(parts))
				} else {
					messages = append(messages, openai.UserMessage(text))
				}
			default:
				return nil, errors.New("chat completions provider does not support this message role")
			}
		case item.OfFunctionCall != nil:
			toolCalls := make([]openai.ChatCompletionMessageToolCallUnionParam, 0, 1)

			for ; i < len(items) && items[i].OfFunctionCall != nil; i++ {
				call := items[i].OfFunctionCall
				toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallUnionParam{OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
					ID: call.CallID,
					Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
						Name:      call.Name,
						Arguments: call.Arguments,
					},
				}})
			}

			i--

			messages = append(messages, openai.ChatCompletionMessageParamUnion{OfAssistant: &openai.ChatCompletionAssistantMessageParam{
				ToolCalls: toolCalls,
			}})
		case item.OfFunctionCallOutput != nil:
			text, parts, err := chatCompletionToolResult(item.OfFunctionCallOutput)
			if err != nil {
				return nil, err
			}

			messages = append(messages, openai.ToolMessage(text, item.OfFunctionCallOutput.CallID))
			if len(parts) > 0 {
				messages = append(messages, openai.UserMessage(parts))
			}
		case item.OfCompaction != nil:
			messages = append(messages, openai.UserMessage(CompactionCheckpointText(item.OfCompaction)))
		case item.OfReasoning != nil:
			continue
		default:
			return nil, errors.New("chat completions provider does not support this replay item")
		}
	}

	return messages, nil
}

func chatCompletionContent(content responses.EasyInputMessageContentUnionParam) (string, []openai.ChatCompletionContentPartUnionParam, error) {
	if content.OfString.Valid() {
		return content.OfString.Value, nil, nil
	}

	texts := make([]string, 0, len(content.OfInputItemContentList))

	parts := make([]openai.ChatCompletionContentPartUnionParam, 0, len(content.OfInputItemContentList))
	for i := range content.OfInputItemContentList {
		part, text, err := chatCompletionContentPart(content.OfInputItemContentList[i])
		if err != nil {
			return "", nil, err
		}

		if text != "" {
			texts = append(texts, text)
		}

		parts = append(parts, part)
	}

	return strings.Join(texts, "\n"), parts, nil
}

func chatCompletionToolResult(output *responses.ResponseInputItemFunctionCallOutputParam) (string, []openai.ChatCompletionContentPartUnionParam, error) {
	if output.Output.OfString.Valid() {
		return output.Output.OfString.Value, nil, nil
	}

	texts := make([]string, 0, len(output.Output.OfResponseFunctionCallOutputItemArray))

	var attachments []openai.ChatCompletionContentPartUnionParam

	for i := range output.Output.OfResponseFunctionCallOutputItemArray {
		item := output.Output.OfResponseFunctionCallOutputItemArray[i]
		switch {
		case item.OfInputText != nil:
			texts = append(texts, item.OfInputText.Text)
		case item.OfInputImage != nil:
			attachments = append(attachments, openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{URL: item.OfInputImage.ImageURL.Value, Detail: string(item.OfInputImage.Detail)}))
		case item.OfInputFile != nil:
			attachments = append(attachments, openai.FileContentPart(openai.ChatCompletionContentPartFileFileParam{Filename: item.OfInputFile.Filename, FileData: item.OfInputFile.FileData}))
		default:
			return "", nil, errors.New("chat completions provider does not support this tool result attachment")
		}
	}

	if len(attachments) > 0 {
		attachments = append([]openai.ChatCompletionContentPartUnionParam{openai.TextContentPart("Tool result attachments:")}, attachments...)
	}

	return strings.Join(texts, "\n"), attachments, nil
}

func chatCompletionContentPart(item responses.ResponseInputContentUnionParam) (openai.ChatCompletionContentPartUnionParam, string, error) {
	switch {
	case item.OfInputText != nil:
		return openai.TextContentPart(item.OfInputText.Text), item.OfInputText.Text, nil
	case item.OfInputImage != nil:
		return openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{URL: item.OfInputImage.ImageURL.Value, Detail: string(item.OfInputImage.Detail)}), "", nil
	case item.OfInputFile != nil:
		return openai.FileContentPart(openai.ChatCompletionContentPartFileFileParam{Filename: item.OfInputFile.Filename, FileData: item.OfInputFile.FileData}), "", nil
	default:
		return openai.ChatCompletionContentPartUnionParam{}, "", errors.New("chat completions provider does not support this prompt attachment")
	}
}

func chatCompletionTools(tools []responses.ToolUnionParam) ([]openai.ChatCompletionToolUnionParam, error) {
	chatTools := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for i := range tools {
		tool := tools[i].OfFunction
		if tool == nil {
			return nil, errors.New("chat completions provider does not support hosted tools")
		}

		definition := openai.FunctionDefinitionParam{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  openai.FunctionParameters(tool.Parameters),
			Strict:      tool.Strict,
		}
		chatTools = append(chatTools, openai.ChatCompletionFunctionTool(definition))
	}

	return chatTools, nil
}

func chatCompletionResponseFormat(format responses.ResponseFormatTextConfigUnionParam) (openai.ChatCompletionNewParamsResponseFormatUnion, error) {
	switch {
	case format.OfJSONSchema != nil:
		jsonSchema := shared.ResponseFormatJSONSchemaParam{JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
			Name:        format.OfJSONSchema.Name,
			Description: format.OfJSONSchema.Description,
			Schema:      format.OfJSONSchema.Schema,
			Strict:      format.OfJSONSchema.Strict,
		}}

		return openai.ChatCompletionNewParamsResponseFormatUnion{OfJSONSchema: &jsonSchema}, nil
	case format.OfJSONObject != nil:
		return openai.ChatCompletionNewParamsResponseFormatUnion{OfJSONObject: format.OfJSONObject}, nil
	case format.OfText != nil:
		return openai.ChatCompletionNewParamsResponseFormatUnion{OfText: format.OfText}, nil
	default:
		return openai.ChatCompletionNewParamsResponseFormatUnion{}, errors.New("unsupported chat completions response format")
	}
}

func chatCompletionResponse(completion *openai.ChatCompletion) *responses.Response {
	resp := &responses.Response{ID: completion.ID, Model: completion.Model}
	if presentOpenAIField(completion.JSON.Usage) || completion.Usage.PromptTokens != 0 || completion.Usage.CompletionTokens != 0 || completion.Usage.TotalTokens != 0 {
		resp.Usage.InputTokens = completion.Usage.PromptTokens
		resp.Usage.OutputTokens = completion.Usage.CompletionTokens
		resp.Usage.TotalTokens = completion.Usage.TotalTokens
		resp.Usage.InputTokensDetails.CachedTokens = completion.Usage.PromptTokensDetails.CachedTokens
		resp.Usage.OutputTokensDetails.ReasoningTokens = completion.Usage.CompletionTokensDetails.ReasoningTokens
		resp.Usage.JSON.InputTokens = openairespjson.NewField(strconv.FormatInt(resp.Usage.InputTokens, 10))
		resp.Usage.JSON.OutputTokens = openairespjson.NewField(strconv.FormatInt(resp.Usage.OutputTokens, 10))
		resp.Usage.JSON.TotalTokens = openairespjson.NewField(strconv.FormatInt(resp.Usage.TotalTokens, 10))
		resp.Usage.InputTokensDetails.JSON.CachedTokens = openairespjson.NewField(strconv.FormatInt(resp.Usage.InputTokensDetails.CachedTokens, 10))
		resp.Usage.OutputTokensDetails.JSON.ReasoningTokens = openairespjson.NewField(strconv.FormatInt(resp.Usage.OutputTokensDetails.ReasoningTokens, 10))
		resp.Usage.JSON.InputTokensDetails = openairespjson.NewField("{}")
		resp.Usage.JSON.OutputTokensDetails = openairespjson.NewField("{}")
		resp.JSON.Usage = openairespjson.NewField("{}")
	}

	if len(completion.Choices) == 0 {
		return resp
	}

	message := completion.Choices[0].Message
	if message.Content != "" {
		resp.Output = append(resp.Output, responses.ResponseOutputItemUnion{
			ID:     completion.ID + "-message",
			Type:   "message",
			Role:   "assistant",
			Status: "completed",
			Content: []responses.ResponseOutputMessageContentUnion{{
				Type: "output_text",
				Text: message.Content,
			}},
		})
	}

	for i := range message.ToolCalls {
		call := message.ToolCalls[i]
		if call.Type != "function" {
			continue
		}

		resp.Output = append(resp.Output, responses.ResponseOutputItemUnion{
			ID:     call.ID,
			Type:   "function_call",
			CallID: call.ID,
			Name:   call.Function.Name,
			Arguments: responses.ResponseOutputItemUnionArguments{
				OfString: call.Function.Arguments,
			},
			Status: "completed",
		})
	}

	return resp
}

func chatCompletionCompactParams(params *responses.ResponseCompactParams) (openai.ChatCompletionNewParams, error) {
	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(params.Input.OfResponseInputItemArray)+2)
	if params.Instructions.Valid() && strings.TrimSpace(params.Instructions.Value) != "" {
		messages = append(messages, openai.DeveloperMessage(params.Instructions.Value))
	}

	inputMessages, err := chatCompletionMessages(params.Input.OfResponseInputItemArray)
	if err != nil {
		return openai.ChatCompletionNewParams{}, err
	}

	messages = append(messages, inputMessages...)
	messages = append(messages, openai.DeveloperMessage(chatCompletionCompactionInstructions))

	return openai.ChatCompletionNewParams{Messages: messages, Model: openai.ChatModel(params.Model)}, nil
}

func chatCompletionCompactedResponse(completion *openai.ChatCompletion) *responses.CompactedResponse {
	summary := ""
	if len(completion.Choices) > 0 {
		summary = completion.Choices[0].Message.Content
	}

	return &responses.CompactedResponse{ID: completion.ID, Output: []responses.ResponseOutputItemUnion{{ID: completion.ID + "-compaction", Type: "compaction", EncryptedContent: "", Content: []responses.ResponseOutputMessageContentUnion{{Type: "output_text", Text: summary}}}}}
}
