package rocketcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3/responses"
)

func (l *looper) newAnthropicResponse(ctx context.Context, params *responses.ResponseNewParams, output chan<- ChatResponse) (*responses.Response, error) {
	if l.AnthropicClient == nil {
		return nil, errors.New("anthropic provider is required")
	}

	body, err := l.anthropicParams(params)
	if err != nil {
		return nil, err
	}

	for attempt := 1; ; attempt++ {
		message, err := l.AnthropicClient.Messages.New(ctx, body)
		if err == nil {
			return anthropicResponse(message), nil
		}

		errAPI, ok := errors.AsType[*anthropic.Error](err)
		if !ok || errAPI.StatusCode != http.StatusTooManyRequests {
			diagnostic := ProviderDiagnostic{Phase: providerDiagnosticError, Message: err.Error()}
			if errAPI != nil {
				diagnostic.HTTPStatus = errAPI.StatusCode
				diagnostic.Code = string(errAPI.Type())
			}

			l.emitProviderDiagnostic(output, &diagnostic)

			return nil, fmt.Errorf("new Anthropic message: %w", err)
		}

		wait := providerRetryDelay(errAPI.Response)
		l.emitProviderDiagnostic(output, &ProviderDiagnostic{Phase: providerDiagnosticRetry, HTTPStatus: errAPI.StatusCode, Code: string(errAPI.Type()), Message: err.Error(), Attempt: attempt, RetryAfter: wait.String()})

		if err := waitProviderRetry(ctx, wait); err != nil {
			return nil, fmt.Errorf("wait for Anthropic retry: %w", err)
		}
	}
}

func waitProviderRetry(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("provider retry canceled: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

func providerRetryDelay(resp *http.Response) time.Duration {
	wait := providerRateLimitRetryMinDelay
	if resp == nil {
		return wait
	}

	if millis, errParse := strconv.ParseFloat(resp.Header.Get("Retry-After-Ms"), 64); errParse == nil && millis >= 0 && millis == millis {
		if delay := time.Duration(millis * float64(time.Millisecond)); delay > wait {
			wait = delay
		}
	}

	for _, header := range []string{"X-RateLimit-Reset-Requests", "X-RateLimit-Reset-Tokens"} {
		if delay, errParse := time.ParseDuration(resp.Header.Get(header)); errParse == nil && delay > wait {
			wait = delay
		}
	}

	retryAfter := resp.Header.Get("Retry-After")
	if seconds, errParse := strconv.ParseFloat(retryAfter, 64); errParse == nil && seconds >= 0 && seconds == seconds {
		if delay := time.Duration(seconds * float64(time.Second)); delay > wait {
			wait = delay
		}
	} else if when, errParse := time.Parse(time.RFC1123, retryAfter); errParse == nil {
		if delay := time.Until(when); delay > wait {
			wait = delay
		}
	}

	return wait
}

func (l *looper) anthropicParams(params *responses.ResponseNewParams) (anthropic.MessageNewParams, error) {
	messages, err := anthropicMessages(params.Input.OfInputItemList)
	if err != nil {
		return anthropic.MessageNewParams{}, err
	}

	body := anthropic.MessageNewParams{MaxTokens: 4096, Messages: messages, Model: params.Model}
	if params.Instructions.Valid() {
		body.System = []anthropic.TextBlockParam{{Text: params.Instructions.Value}}
	}

	for i := range params.Tools {
		tool := params.Tools[i].OfFunction
		if tool == nil {
			continue
		}

		anthropicTool, err := anthropicToolParam(tool)
		if err != nil {
			return anthropic.MessageNewParams{}, err
		}

		body.Tools = append(body.Tools, anthropicTool)
	}

	return body, nil
}

func anthropicMessages(items []responses.ResponseInputItemUnionParam) ([]anthropic.MessageParam, error) {
	messages := make([]anthropic.MessageParam, 0, len(items))
	for i := range items {
		item := &items[i]
		switch {
		case item.OfMessage != nil:
			blocks, err := anthropicContent(item.OfMessage.Content)
			if err != nil {
				return nil, err
			}

			if string(item.OfMessage.Role) == "assistant" {
				messages = append(messages, anthropic.NewAssistantMessage(blocks...))
			} else {
				messages = append(messages, anthropic.NewUserMessage(blocks...))
			}
		case item.OfFunctionCall != nil:
			call := item.OfFunctionCall

			var input any = map[string]any{}
			if strings.TrimSpace(call.Arguments) != "" {
				if err := json.Unmarshal([]byte(call.Arguments), &input); err != nil {
					return nil, fmt.Errorf("decode Anthropic tool input %q: %w", call.Name, err)
				}
			}

			messages = append(messages, anthropic.NewAssistantMessage(anthropic.NewToolUseBlock(call.CallID, input, call.Name)))
		case item.OfFunctionCallOutput != nil:
			output := anthropicToolResultText(item.OfFunctionCallOutput)
			messages = append(messages, anthropic.NewUserMessage(anthropic.NewToolResultBlock(item.OfFunctionCallOutput.CallID, output, false)))
		}
	}

	return messages, nil
}

func anthropicContent(content responses.EasyInputMessageContentUnionParam) ([]anthropic.ContentBlockParamUnion, error) {
	if content.OfString.Valid() {
		return []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(content.OfString.Value)}, nil
	}

	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(content.OfInputItemContentList))
	for i := range content.OfInputItemContentList {
		item := content.OfInputItemContentList[i]
		if item.OfInputText != nil {
			blocks = append(blocks, anthropic.NewTextBlock(item.OfInputText.Text))
		} else {
			return nil, errors.New("anthropic provider does not support prompt attachments yet")
		}
	}

	return blocks, nil
}

func anthropicToolResultText(output *responses.ResponseInputItemFunctionCallOutputParam) string {
	if output.Output.OfString.Valid() {
		return output.Output.OfString.Value
	}

	parts := make([]string, 0, len(output.Output.OfResponseFunctionCallOutputItemArray))
	for i := range output.Output.OfResponseFunctionCallOutputItemArray {
		item := output.Output.OfResponseFunctionCallOutputItemArray[i]
		if item.OfInputText != nil {
			parts = append(parts, item.OfInputText.Text)
		}
	}

	return strings.Join(parts, "\n")
}

func anthropicToolParam(tool *responses.FunctionToolParam) (anthropic.ToolUnionParam, error) {
	data, err := json.Marshal(tool.Parameters)
	if err != nil {
		return anthropic.ToolUnionParam{}, fmt.Errorf("marshal Anthropic tool schema %q: %w", tool.Name, err)
	}

	var schema anthropic.ToolInputSchemaParam
	if err := json.Unmarshal(data, &schema); err != nil {
		return anthropic.ToolUnionParam{}, fmt.Errorf("decode Anthropic tool schema %q: %w", tool.Name, err)
	}

	param := anthropic.ToolUnionParamOfTool(schema, tool.Name)
	if tool.Description.Valid() {
		param.OfTool.Description = anthropic.String(tool.Description.Value)
	}

	if tool.Strict.Valid() {
		param.OfTool.Strict = anthropic.Bool(tool.Strict.Value)
	}

	return param, nil
}

func anthropicResponse(message *anthropic.Message) *responses.Response {
	var response responses.Response

	response.ID = message.ID
	for i := range message.Content {
		block := message.Content[i]
		switch block.Type {
		case "text":
			response.Output = append(response.Output, responses.ResponseOutputItemUnion{ID: message.ID + "-message", Type: "message", Role: "assistant", Status: "completed", Content: []responses.ResponseOutputMessageContentUnion{{Type: "output_text", Text: block.Text}}})
		case "tool_use":
			response.Output = append(response.Output, responses.ResponseOutputItemUnion{ID: block.ID, Type: "function_call", CallID: block.ID, Name: block.Name, Arguments: responses.ResponseOutputItemUnionArguments{OfString: string(block.Input)}, Status: "completed"})
		}
	}

	return &response
}
