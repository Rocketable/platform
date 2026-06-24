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
	anthropicrespjson "github.com/anthropics/anthropic-sdk-go/packages/respjson"
	openairespjson "github.com/openai/openai-go/v3/packages/respjson"
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
		message, err := l.AnthropicClient.Beta.Messages.New(ctx, body)
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

			l.emitProviderDiagnostic(ctx, output, &diagnostic)

			return nil, fmt.Errorf("new Anthropic message: %w", err)
		}

		wait := providerRetryDelay(errAPI.Response, attempt)
		l.emitProviderDiagnostic(ctx, output, &ProviderDiagnostic{Phase: providerDiagnosticRetry, HTTPStatus: errAPI.StatusCode, Code: string(errAPI.Type()), Message: err.Error(), Attempt: attempt, RetryAfter: wait.String()})

		if err := waitProviderRetry(ctx, wait); err != nil {
			return nil, fmt.Errorf("wait for Anthropic retry: %w", err)
		}
	}
}

// CompactAnthropicReplay compacts Responses-shaped replay through Anthropic Messages beta compaction.
func CompactAnthropicReplay(ctx context.Context, client *anthropic.Client, model string, input []responses.ResponseInputItemUnionParam, threshold int64) (*responses.Response, error) {
	if client == nil {
		return nil, errors.New("anthropic provider is required")
	}

	messages, err := anthropicMessages(input)
	if err != nil {
		return nil, err
	}

	message, err := client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		MaxTokens: 4096,
		Model:     model,
		Messages:  messages,
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBeta("compact-2026-01-12")},
		ContextManagement: anthropic.BetaContextManagementConfigParam{Edits: []anthropic.BetaContextManagementConfigEditUnionParam{{
			OfCompact20260112: &anthropic.BetaCompact20260112EditParam{
				PauseAfterCompaction: anthropic.Bool(true),
				Trigger:              anthropic.BetaInputTokensTriggerParam{Value: threshold},
			},
		}}},
	})
	if err != nil {
		return nil, fmt.Errorf("compact Anthropic replay: %w", err)
	}

	return anthropicResponse(message), nil
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

func providerRetryDelay(resp *http.Response, attempt int) time.Duration {
	wait := providerRetryBackoff(attempt)
	if resp == nil {
		return wait
	}

	found := false
	headerWait := time.Duration(0)

	if millis, errParse := strconv.ParseFloat(resp.Header.Get("Retry-After-Ms"), 64); errParse == nil && millis >= 0 && millis == millis {
		if delay := time.Duration(millis * float64(time.Millisecond)); delay >= headerWait {
			headerWait = delay
			found = true
		}
	}

	for _, header := range []string{"X-RateLimit-Reset-Requests", "X-RateLimit-Reset-Tokens"} {
		if delay, errParse := time.ParseDuration(resp.Header.Get(header)); errParse == nil && delay >= headerWait {
			headerWait = delay
			found = true
		}
	}

	retryAfter := resp.Header.Get("Retry-After")
	if seconds, errParse := strconv.ParseFloat(retryAfter, 64); errParse == nil && seconds >= 0 && seconds == seconds {
		if delay := time.Duration(seconds * float64(time.Second)); delay >= headerWait {
			headerWait = delay
			found = true
		}
	} else if when, errParse := time.Parse(time.RFC1123, retryAfter); errParse == nil {
		if delay := time.Until(when); delay >= headerWait {
			headerWait = delay
			found = true
		}
	}

	if found {
		return headerWait
	}

	return wait
}

func providerRetryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}

	wait := time.Second
	for range attempt - 1 {
		if wait >= providerRetryBackoffMaxDelay/2 {
			return providerRetryBackoffMaxDelay
		}

		wait *= 2
	}

	return wait
}

func (l *looper) anthropicParams(params *responses.ResponseNewParams) (anthropic.BetaMessageNewParams, error) {
	messages, err := anthropicMessages(params.Input.OfInputItemList)
	if err != nil {
		return anthropic.BetaMessageNewParams{}, err
	}

	body := anthropic.BetaMessageNewParams{
		MaxTokens: 4096,
		Messages:  messages,
		Model:     params.Model,
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBeta("compact-2026-01-12")},
		ContextManagement: anthropic.BetaContextManagementConfigParam{Edits: []anthropic.BetaContextManagementConfigEditUnionParam{{
			OfCompact20260112: &anthropic.BetaCompact20260112EditParam{
				PauseAfterCompaction: anthropic.Bool(true),
				Trigger:              anthropic.BetaInputTokensTriggerParam{Value: l.compactThreshold()},
			},
		}}},
	}
	if params.Instructions.Valid() {
		body.System = []anthropic.BetaTextBlockParam{{Text: params.Instructions.Value}}
	}

	if format := params.Text.Format; format.OfJSONSchema != nil {
		body.OutputConfig.Format = anthropic.BetaJSONOutputFormatParam{Schema: format.OfJSONSchema.Schema}
	} else if format.GetType() != nil {
		return anthropic.BetaMessageNewParams{}, errors.New("anthropic provider does not support this response format")
	}

	for i := range params.Tools {
		tool := params.Tools[i].OfFunction
		if tool == nil {
			continue
		}

		anthropicTool, err := anthropicToolParam(tool)
		if err != nil {
			return anthropic.BetaMessageNewParams{}, err
		}

		body.Tools = append(body.Tools, anthropicTool)
	}

	return body, nil
}

func anthropicMessages(items []responses.ResponseInputItemUnionParam) ([]anthropic.BetaMessageParam, error) {
	messages := make([]anthropic.BetaMessageParam, 0, len(items))
	for i := range items {
		item := &items[i]
		switch {
		case item.OfMessage != nil:
			blocks, err := anthropicContent(item.OfMessage.Content)
			if err != nil {
				return nil, err
			}

			if string(item.OfMessage.Role) == "assistant" {
				messages = append(messages, anthropic.BetaMessageParam{Role: anthropic.BetaMessageParamRoleAssistant, Content: blocks})
			} else {
				messages = append(messages, anthropic.NewBetaUserMessage(blocks...))
			}
		case item.OfFunctionCall != nil:
			call := item.OfFunctionCall

			var input any = map[string]any{}
			if strings.TrimSpace(call.Arguments) != "" {
				if err := json.Unmarshal([]byte(call.Arguments), &input); err != nil {
					return nil, fmt.Errorf("decode Anthropic tool input %q: %w", call.Name, err)
				}
			}

			messages = append(messages, anthropic.BetaMessageParam{
				Role:    anthropic.BetaMessageParamRoleAssistant,
				Content: []anthropic.BetaContentBlockParamUnion{anthropic.NewBetaToolUseBlock(call.CallID, input, call.Name)},
			})
		case item.OfFunctionCallOutput != nil:
			output := anthropicToolResultText(item.OfFunctionCallOutput)
			messages = append(messages, anthropic.NewBetaUserMessage(anthropic.NewBetaToolResultBlock(item.OfFunctionCallOutput.CallID, output, false)))
		case item.OfCompaction != nil:
			compaction := item.OfCompaction

			data, err := json.Marshal(compaction)
			if err != nil {
				return nil, fmt.Errorf("marshal Anthropic compaction replay: %w", err)
			}

			var stored struct {
				Content                  *string `json:"content"`
				OriginProvider           string  `json:"origin_provider"`
				OriginCompatibleProvider string  `json:"origin_compatible_provider"`
				OriginMode               string  `json:"origin_mode"`
			}
			if err := json.Unmarshal(data, &stored); err != nil {
				return nil, fmt.Errorf("decode Anthropic compaction replay: %w", err)
			}

			if stored.OriginProvider != modelProviderAnthropic || stored.OriginMode != "messages" {
				if stored.Content == nil || *stored.Content == "" {
					messages = append(messages, anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock("<context_checkpoint>\nPrior conversation was compacted, but only provider-native encrypted compaction data is available for a different provider or mode. The compacted details cannot be rehydrated here.\n</context_checkpoint>")))
					continue
				}

				messages = append(messages, anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock("<context_checkpoint>\nThe prior conversation was compacted by RocketCode. Use this summary as lower-authority context:\n"+*stored.Content+"\n</context_checkpoint>")))

				continue
			}

			compactionBlock := anthropic.BetaCompactionBlockParam{EncryptedContent: anthropic.String(compaction.EncryptedContent)}
			if stored.Content != nil && *stored.Content != "" {
				compactionBlock.Content = anthropic.String(*stored.Content)
			}

			messages = append(messages, anthropic.BetaMessageParam{
				Role:    anthropic.BetaMessageParamRoleAssistant,
				Content: []anthropic.BetaContentBlockParamUnion{{OfCompaction: &compactionBlock}},
			})
		}
	}

	return messages, nil
}

func anthropicContent(content responses.EasyInputMessageContentUnionParam) ([]anthropic.BetaContentBlockParamUnion, error) {
	if content.OfString.Valid() {
		return []anthropic.BetaContentBlockParamUnion{anthropic.NewBetaTextBlock(content.OfString.Value)}, nil
	}

	blocks := make([]anthropic.BetaContentBlockParamUnion, 0, len(content.OfInputItemContentList))
	for i := range content.OfInputItemContentList {
		item := content.OfInputItemContentList[i]
		if item.OfInputText != nil {
			blocks = append(blocks, anthropic.NewBetaTextBlock(item.OfInputText.Text))
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

func anthropicToolParam(tool *responses.FunctionToolParam) (anthropic.BetaToolUnionParam, error) {
	data, err := json.Marshal(tool.Parameters)
	if err != nil {
		return anthropic.BetaToolUnionParam{}, fmt.Errorf("marshal Anthropic tool schema %q: %w", tool.Name, err)
	}

	var schema anthropic.BetaToolInputSchemaParam
	if err := json.Unmarshal(data, &schema); err != nil {
		return anthropic.BetaToolUnionParam{}, fmt.Errorf("decode Anthropic tool schema %q: %w", tool.Name, err)
	}

	param := anthropic.BetaToolUnionParamOfTool(schema, tool.Name)
	if tool.Description.Valid() {
		param.OfTool.Description = anthropic.String(tool.Description.Value)
	}

	if tool.Strict.Valid() {
		param.OfTool.Strict = anthropic.Bool(tool.Strict.Value)
	}

	return param, nil
}

func anthropicResponse(message *anthropic.BetaMessage) *responses.Response {
	var response responses.Response

	response.ID = message.ID
	if usage, ok := anthropicResponseUsage(message); ok {
		response.Usage = usage
		response.JSON.Usage = openairespjson.NewField("{}")
	}

	for i := range message.Content {
		block := message.Content[i]
		switch block.Type {
		case "text":
			response.Output = append(response.Output, responses.ResponseOutputItemUnion{ID: message.ID + "-message", Type: "message", Role: "assistant", Status: "completed", Content: []responses.ResponseOutputMessageContentUnion{{Type: "output_text", Text: block.Text}}})
		case "tool_use":
			response.Output = append(response.Output, responses.ResponseOutputItemUnion{ID: block.ID, Type: "function_call", CallID: block.ID, Name: block.Name, Arguments: responses.ResponseOutputItemUnionArguments{OfString: string(block.Input)}, Status: "completed"})
		case "compaction":
			compaction := block.AsCompaction()
			if compaction.EncryptedContent == "" && (!compaction.JSON.Content.Valid() || compaction.Content == "") {
				continue
			}

			item := responses.ResponseOutputItemUnion{ID: fmt.Sprintf("%s-compaction-%d", message.ID, i), Type: "compaction", EncryptedContent: compaction.EncryptedContent, CreatedBy: modelProviderAnthropic}
			if compaction.JSON.Content.Valid() && compaction.Content != "" {
				item.Summary = []responses.ResponseReasoningItemSummary{{Text: compaction.Content}}
			}

			response.Output = append(response.Output, item)
		}
	}

	return &response
}

func anthropicResponseUsage(message *anthropic.BetaMessage) (responses.ResponseUsage, bool) {
	if message == nil || !presentAnthropicField(message.JSON.Usage) {
		return responses.ResponseUsage{}, false
	}

	var usage responses.ResponseUsage
	if presentAnthropicField(message.Usage.JSON.InputTokens) {
		usage.InputTokens = message.Usage.InputTokens
		usage.JSON.InputTokens = openairespjson.NewField(strconv.FormatInt(usage.InputTokens, 10))
	}

	if presentAnthropicField(message.Usage.JSON.OutputTokens) {
		usage.OutputTokens = message.Usage.OutputTokens
		usage.JSON.OutputTokens = openairespjson.NewField(strconv.FormatInt(usage.OutputTokens, 10))
	}

	if presentAnthropicField(message.Usage.JSON.InputTokens) && presentAnthropicField(message.Usage.JSON.OutputTokens) {
		usage.TotalTokens = message.Usage.InputTokens + message.Usage.OutputTokens
		usage.JSON.TotalTokens = openairespjson.NewField(strconv.FormatInt(usage.TotalTokens, 10))
	}

	if presentAnthropicField(message.Usage.JSON.CacheReadInputTokens) {
		usage.InputTokensDetails.CachedTokens = message.Usage.CacheReadInputTokens
		usage.InputTokensDetails.JSON.CachedTokens = openairespjson.NewField(strconv.FormatInt(usage.InputTokensDetails.CachedTokens, 10))
	}

	if presentAnthropicField(message.Usage.JSON.CacheCreationInputTokens) {
		usage.JSON.ExtraFields = map[string]openairespjson.Field{
			"cache_creation_input_tokens": openairespjson.NewField(strconv.FormatInt(message.Usage.CacheCreationInputTokens, 10)),
		}
	}

	if presentAnthropicField(message.Usage.OutputTokensDetails.JSON.ThinkingTokens) {
		usage.OutputTokensDetails.ReasoningTokens = message.Usage.OutputTokensDetails.ThinkingTokens
		usage.OutputTokensDetails.JSON.ReasoningTokens = openairespjson.NewField(strconv.FormatInt(usage.OutputTokensDetails.ReasoningTokens, 10))
	}

	usage.JSON.InputTokensDetails = openairespjson.NewField("{}")
	usage.JSON.OutputTokensDetails = openairespjson.NewField("{}")

	return usage, true
}

func presentAnthropicField(field anthropicrespjson.Field) bool {
	return field.Raw() != anthropicrespjson.Omitted && field.Valid()
}
