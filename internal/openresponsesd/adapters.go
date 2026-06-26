package openresponsesd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	openai "github.com/openai/openai-go/v3"
	openaioption "github.com/openai/openai-go/v3/option"
	openai_param "github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

type responseRequest struct {
	Model              string          `json:"model"`
	Instructions       string          `json:"instructions,omitempty"`
	Input              json.RawMessage `json:"input,omitempty"`
	Tools              []tool          `json:"tools,omitempty"`
	Text               json.RawMessage `json:"text,omitempty"`
	Reasoning          json.RawMessage `json:"reasoning,omitempty"`
	ParallelToolCalls  *bool           `json:"parallel_tool_calls,omitempty"`
	Store              *bool           `json:"store,omitempty"`
	Stream             bool            `json:"stream,omitempty"`
	Generate           *bool           `json:"generate,omitempty"`
	PreviousResponseID string          `json:"previous_response_id,omitempty"`
	Unsupported        json.RawMessage `json:"stream_options,omitempty"`
	unsupportedFields  []string
	raw                json.RawMessage
}

type tool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      *bool           `json:"strict"`
}

type canonicalResponse struct {
	ID     string               `json:"id"`
	Object string               `json:"object"`
	Status string               `json:"status"`
	Model  string               `json:"model"`
	Output []responseOutputItem `json:"output"`
	Usage  json.RawMessage      `json:"usage"`
	raw    json.RawMessage
}

type responseObject struct {
	ID     string               `json:"id"`
	Object string               `json:"object"`
	Status string               `json:"status"`
	Model  string               `json:"model"`
	Output []responseOutputItem `json:"output,omitempty"`
	Usage  json.RawMessage      `json:"usage,omitempty"`
}

type responseOutputItem struct {
	ID        string                `json:"id,omitempty"`
	Type      string                `json:"type"`
	Role      string                `json:"role,omitempty"`
	Status    string                `json:"status,omitempty"`
	Content   []responseContentPart `json:"content,omitempty"`
	CallID    string                `json:"call_id,omitempty"`
	Name      string                `json:"name,omitempty"`
	Arguments string                `json:"arguments,omitempty"`
}

type responseContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (r canonicalResponse) object() responseObject {
	return responseObject{ID: r.ID, Object: r.Object, Status: r.Status, Model: r.Model, Output: r.Output, Usage: r.Usage}
}

func (r canonicalResponse) summary(status string) responseObject {
	return responseObject{ID: r.ID, Object: r.Object, Status: status, Model: r.Model}
}

func (s *server) openAIResponses(ctx context.Context, provider providerConfig, req responseRequest) (canonicalResponse, error) {
	var params responses.ResponseNewParams
	if err := json.Unmarshal(req.raw, &params); err != nil {
		return canonicalResponse{}, err
	}
	params.Model = shared.ResponsesModel(req.Model)
	params.PreviousResponseID = openai_param.Opt[string]{}
	service := responses.NewResponseService(openaioption.WithAPIKey(provider.APIKey), openaioption.WithBaseURL(provider.BaseURL), openaioption.WithHTTPClient(s.client))
	upstream, err := service.New(ctx, params)
	if err != nil {
		return canonicalResponse{}, providerSDKError(err)
	}

	return parseCanonicalResponse(json.RawMessage(upstream.RawJSON()))
}

func (s *server) openAIResponsesCompact(ctx context.Context, provider providerConfig, req responseRequest) (canonicalResponse, error) {
	var params responses.ResponseCompactParams
	if err := json.Unmarshal(req.raw, &params); err != nil {
		return canonicalResponse{}, err
	}
	params.Model = responses.ResponseCompactParamsModel(req.Model)
	service := responses.NewResponseService(openaioption.WithAPIKey(provider.APIKey), openaioption.WithBaseURL(provider.BaseURL), openaioption.WithHTTPClient(s.client))
	upstream, err := service.Compact(ctx, params)
	if err != nil {
		return canonicalResponse{}, providerSDKError(err)
	}

	return parseCompactResponse(json.RawMessage(upstream.RawJSON()), req.Model)
}

func (s *server) chatCompletions(ctx context.Context, provider providerConfig, req responseRequest) (canonicalResponse, error) {
	messages, err := chatMessages(req)
	if err != nil {
		return canonicalResponse{}, err
	}

	params := openai.ChatCompletionNewParams{Model: shared.ChatModel(req.Model), Messages: messages}
	if len(req.Tools) > 0 {
		tools, err := chatTools(req.Tools)
		if err != nil {
			return canonicalResponse{}, err
		}
		params.Tools = tools
	}
	if req.ParallelToolCalls != nil {
		params.ParallelToolCalls = openai.Bool(*req.ParallelToolCalls)
	}
	if req.Store != nil {
		params.Store = openai.Bool(*req.Store)
	}

	service := openai.NewChatCompletionService(openaioption.WithAPIKey(provider.APIKey), openaioption.WithBaseURL(provider.BaseURL), openaioption.WithHTTPClient(s.client))
	upstream, err := service.New(ctx, params)
	if err != nil {
		return canonicalResponse{}, providerSDKError(err)
	}

	return chatCompletionToResponse(*upstream), nil
}

func (s *server) anthropicMessages(ctx context.Context, provider providerConfig, req responseRequest) (canonicalResponse, error) {
	messages, err := anthropicRequestMessages(req)
	if err != nil {
		return canonicalResponse{}, err
	}

	params := anthropic.MessageNewParams{Model: anthropic.Model(req.Model), MaxTokens: 4096, Messages: messages}
	if strings.TrimSpace(req.Instructions) != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.Instructions}}
	}
	if len(req.Tools) > 0 {
		tools, err := anthropicTools(req.Tools)
		if err != nil {
			return canonicalResponse{}, err
		}
		params.Tools = tools
	}

	client := anthropic.NewClient(anthropicoption.WithoutEnvironmentDefaults(), anthropicoption.WithAPIKey(provider.APIKey), anthropicoption.WithBaseURL(provider.BaseURL), anthropicoption.WithHTTPClient(s.client))
	upstream, err := client.Messages.New(ctx, params)
	if err != nil {
		return canonicalResponse{}, providerSDKError(err)
	}

	return anthropicMessageToResponse(*upstream)
}

func providerSDKError(err error) error {
	if errOpenAI, ok := errors.AsType[*openai.Error](err); ok {
		return upstreamError(errOpenAI.StatusCode, errOpenAI.Message)
	}
	if errAnthropic, ok := errors.AsType[*anthropic.Error](err); ok {
		return upstreamError(errAnthropic.StatusCode, errAnthropic.Error())
	}

	return err
}

func upstreamError(status int, upstreamMessage string) error {
	code := "upstream_error"
	message := "upstream provider returned an error"
	if strings.TrimSpace(upstreamMessage) != "" {
		message = upstreamMessage
	}

	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return &adapterError{status: http.StatusBadRequest, code: code, message: message}
	case http.StatusUnauthorized, http.StatusForbidden:
		return &adapterError{status: status, code: "provider_authentication_failed", message: "upstream provider rejected credentials"}
	case http.StatusNotFound:
		return &adapterError{status: http.StatusBadRequest, code: "model_not_found", message: message}
	case http.StatusTooManyRequests:
		return &adapterError{status: http.StatusTooManyRequests, code: "rate_limit_exceeded", message: message}
	default:
		return &adapterError{status: http.StatusBadGateway, code: "upstream_error", message: message}
	}
}

func chatMessages(req responseRequest) ([]openai.ChatCompletionMessageParamUnion, error) {
	messages, err := mustChatInputMessages(req.Input)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Instructions) != "" {
		messages = append([]openai.ChatCompletionMessageParamUnion{openai.DeveloperMessage(req.Instructions)}, messages...)
	}

	return messages, nil
}

func mustChatInputMessages(raw json.RawMessage) ([]openai.ChatCompletionMessageParamUnion, error) {
	var messages []openai.ChatCompletionMessageParamUnion

	items, err := inputItems(raw)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		switch item.Type {
		case "message":
			content, err := textMessageContent(item.Content)
			if err != nil {
				return nil, err
			}
			message, err := chatMessage(item.Role, content)
			if err != nil {
				return nil, err
			}
			messages = append(messages, message)
		case "function_call":
			messages = append(messages, openai.ChatCompletionMessageParamUnion{OfAssistant: &openai.ChatCompletionAssistantMessageParam{ToolCalls: []openai.ChatCompletionMessageToolCallUnionParam{{OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{ID: item.CallID, Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{Name: item.Name, Arguments: item.Arguments}}}}}})
		case "function_call_output":
			messages = append(messages, openai.ToolMessage(item.Output, item.CallID))
		default:
			return nil, &adapterError{status: http.StatusBadRequest, code: "unsupported_input", message: "unsupported input item type " + item.Type}
		}
	}

	return messages, nil
}

func chatMessage(role, content string) (openai.ChatCompletionMessageParamUnion, error) {
	switch role {
	case "user":
		return openai.UserMessage(content), nil
	case "assistant":
		return openai.AssistantMessage(content), nil
	case "system":
		return openai.SystemMessage(content), nil
	case "developer":
		return openai.DeveloperMessage(content), nil
	}

	return openai.ChatCompletionMessageParamUnion{}, &adapterError{status: http.StatusBadRequest, code: "unsupported_input_role", message: "unsupported message role " + role}
}

func chatTools(tools []tool) ([]openai.ChatCompletionToolUnionParam, error) {
	result := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		if tool.Type != "function" {
			return nil, &adapterError{status: http.StatusBadRequest, code: "unsupported_tool", message: "only function tools are supported"}
		}
		params, err := rawFunctionParameters(tool.Parameters)
		if err != nil {
			return nil, err
		}
		definition := shared.FunctionDefinitionParam{Name: tool.Name, Description: openai.String(tool.Description), Parameters: params}
		if tool.Strict != nil {
			definition.Strict = openai.Bool(*tool.Strict)
		}
		result = append(result, openai.ChatCompletionFunctionTool(definition))
	}

	return result, nil
}

func anthropicRequestMessages(req responseRequest) ([]anthropic.MessageParam, error) {
	items, err := inputItems(req.Input)
	if err != nil {
		return nil, err
	}
	var messages []anthropic.MessageParam
	for _, item := range items {
		switch item.Type {
		case "message":
			content, err := anthropicContent(item.Content)
			if err != nil {
				return nil, err
			}
			role, err := anthropicRole(item.Role)
			if err != nil {
				return nil, err
			}
			if role == anthropic.MessageParamRoleAssistant {
				messages = append(messages, anthropic.NewAssistantMessage(content...))
			} else {
				messages = append(messages, anthropic.NewUserMessage(content...))
			}
		case "function_call":
			var input any = struct{}{}
			if strings.TrimSpace(item.Arguments) != "" {
				if err := json.Unmarshal([]byte(item.Arguments), &input); err != nil {
					return nil, &adapterError{status: http.StatusBadRequest, code: "invalid_tool_arguments", message: "function_call arguments must be JSON"}
				}
			}
			messages = append(messages, anthropic.NewAssistantMessage(anthropic.NewToolUseBlock(item.CallID, input, item.Name)))
		case "function_call_output":
			messages = append(messages, anthropic.NewUserMessage(anthropic.NewToolResultBlock(item.CallID, item.Output, false)))
		default:
			return nil, &adapterError{status: http.StatusBadRequest, code: "unsupported_input", message: "unsupported input item type " + item.Type}
		}
	}

	return messages, nil
}

func anthropicTools(tools []tool) ([]anthropic.ToolUnionParam, error) {
	result := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		if tool.Type != "function" {
			return nil, &adapterError{status: http.StatusBadRequest, code: "unsupported_tool", message: "only function tools are supported"}
		}
		schema, err := rawAnthropicSchema(tool.Parameters)
		if err != nil {
			return nil, err
		}
		param := anthropic.ToolUnionParamOfTool(schema, tool.Name)
		param.OfTool.Description = anthropic.String(tool.Description)
		if tool.Strict != nil {
			param.OfTool.Strict = anthropic.Bool(*tool.Strict)
		}
		result = append(result, param)
	}

	return result, nil
}

type inputItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Output    string          `json:"output"`
}

func inputItems(raw json.RawMessage) ([]inputItem, error) {
	if len(raw) == 0 {
		return nil, &adapterError{status: http.StatusBadRequest, code: "missing_input", message: "input is required"}
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, err
		}
		return []inputItem{{Type: "message", Role: "user", Content: mustMarshal(text)}}, nil
	}
	var items []inputItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, &adapterError{status: http.StatusBadRequest, code: "invalid_input", message: "input must be a string or item array"}
	}

	return items, nil
}

func textMessageContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", err
		}
		return text, nil
	}
	if len(raw) != 0 && raw[0] == '[' {
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &parts); err != nil {
			return "", &adapterError{status: http.StatusBadRequest, code: "invalid_content", message: "message content is invalid"}
		}
		var text strings.Builder
		for _, part := range parts {
			switch part.Type {
			case "input_text", "output_text", "text":
				text.WriteString(part.Text)
			default:
				return "", &adapterError{status: http.StatusBadRequest, code: "unsupported_content", message: "this provider adapter supports text content only"}
			}
		}

		return text.String(), nil
	}

	return "", &adapterError{status: http.StatusBadRequest, code: "unsupported_content", message: "this provider adapter supports text content only"}
}

func anthropicContent(raw json.RawMessage) ([]anthropic.ContentBlockParamUnion, error) {
	text, err := textMessageContent(raw)
	if err != nil {
		return nil, err
	}

	return []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(text)}, nil
}

func anthropicRole(role string) (anthropic.MessageParamRole, error) {
	switch role {
	case "user":
		return anthropic.MessageParamRoleUser, nil
	case "assistant":
		return anthropic.MessageParamRoleAssistant, nil
	}

	return "", &adapterError{status: http.StatusBadRequest, code: "unsupported_input_role", message: "unsupported message role " + role}
}

func parseCanonicalResponse(raw json.RawMessage) (canonicalResponse, error) {
	var resp canonicalResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return canonicalResponse{}, err
	}
	if resp.Object == "" {
		resp.Object = "response"
	}
	if resp.Status == "" {
		resp.Status = "completed"
	}
	if resp.Usage == nil {
		resp.Usage = json.RawMessage(`{}`)
	}
	resp.raw = mustMarshal(resp.object())

	return resp, nil
}

func parseCompactResponse(raw json.RawMessage, model string) (canonicalResponse, error) {
	resp, err := parseCanonicalResponse(raw)
	if err != nil {
		return canonicalResponse{}, err
	}
	resp.Model = model
	resp.raw = raw

	return resp, nil
}

func chatCompletionToResponse(completion openai.ChatCompletion) canonicalResponse {
	var output []responseOutputItem
	if len(completion.Choices) > 0 {
		message := completion.Choices[0].Message
		if message.Content != "" {
			output = append(output, outputMessage(completion.ID+"-message", message.Content))
		}
		for _, call := range message.ToolCalls {
			if call.Type == "function" {
				output = append(output, responseOutputItem{ID: call.ID, Type: "function_call", CallID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments, Status: "completed"})
			}
		}
	}

	return newCanonicalResponse(completion.ID, completion.Model, output, mustMarshal(completion.Usage))
}

func anthropicMessageToResponse(message anthropic.Message) (canonicalResponse, error) {
	var output []responseOutputItem
	for _, content := range message.Content {
		switch block := content.AsAny().(type) {
		case anthropic.TextBlock:
			output = append(output, outputMessage(message.ID+"-message", block.Text))
		case anthropic.ToolUseBlock:
			arguments, err := json.Marshal(block.Input)
			if err != nil {
				return canonicalResponse{}, err
			}
			output = append(output, responseOutputItem{ID: block.ID, Type: "function_call", CallID: block.ID, Name: block.Name, Arguments: string(arguments), Status: "completed"})
		}
	}

	usage := mustMarshal(struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
		TotalTokens  int64 `json:"total_tokens"`
	}{InputTokens: message.Usage.InputTokens, OutputTokens: message.Usage.OutputTokens, TotalTokens: message.Usage.InputTokens + message.Usage.OutputTokens})

	return newCanonicalResponse(message.ID, string(message.Model), output, usage), nil
}

func outputMessage(id, text string) responseOutputItem {
	return responseOutputItem{ID: id, Type: "message", Role: "assistant", Status: "completed", Content: []responseContentPart{{Type: "output_text", Text: text}}}
}

func newCanonicalResponse(id, model string, output []responseOutputItem, usage json.RawMessage) canonicalResponse {
	if usage == nil {
		usage = json.RawMessage(`{}`)
	}
	resp := canonicalResponse{ID: id, Object: "response", Status: "completed", Model: model, Output: output, Usage: usage}
	resp.raw = mustMarshal(resp.object())

	return resp
}

func validateToolSchemas(tools []tool) error {
	for _, tool := range tools {
		if tool.Type != "function" {
			continue
		}
		if err := validateToolSchema(tool.Parameters); err != nil {
			return err
		}
	}

	return nil
}

func validateToolSchema(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if !json.Valid([]byte(trimmed)) || !strings.HasPrefix(trimmed, "{") {
		return &adapterError{status: http.StatusBadRequest, code: "invalid_tool_schema", message: "function tool parameters must be a JSON object"}
	}

	return nil
}

func rawFunctionParameters(raw json.RawMessage) (shared.FunctionParameters, error) {
	if len(raw) == 0 {
		return shared.FunctionParameters{}, nil
	}
	var params shared.FunctionParameters
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, &adapterError{status: http.StatusBadRequest, code: "invalid_tool_schema", message: "function tool parameters must be a JSON object"}
	}

	return params, nil
}

func rawAnthropicSchema(raw json.RawMessage) (anthropic.ToolInputSchemaParam, error) {
	if len(raw) == 0 {
		return anthropic.ToolInputSchemaParam{}, nil
	}
	var schema anthropic.ToolInputSchemaParam
	if err := json.Unmarshal(raw, &schema); err != nil {
		return anthropic.ToolInputSchemaParam{}, &adapterError{status: http.StatusBadRequest, code: "invalid_tool_schema", message: "function tool parameters must be a JSON object"}
	}

	return schema, nil
}

func (r responseRequest) MarshalJSON() ([]byte, error) {
	type wire struct {
		Model              string          `json:"model"`
		Instructions       string          `json:"instructions,omitempty"`
		Input              json.RawMessage `json:"input,omitempty"`
		Tools              []tool          `json:"tools,omitempty"`
		Text               json.RawMessage `json:"text,omitempty"`
		Reasoning          json.RawMessage `json:"reasoning,omitempty"`
		ParallelToolCalls  *bool           `json:"parallel_tool_calls,omitempty"`
		Store              *bool           `json:"store,omitempty"`
		PreviousResponseID string          `json:"previous_response_id,omitempty"`
	}

	return json.Marshal(wire{
		Model:              r.Model,
		Instructions:       r.Instructions,
		Input:              r.Input,
		Tools:              r.Tools,
		Text:               r.Text,
		Reasoning:          r.Reasoning,
		ParallelToolCalls:  r.ParallelToolCalls,
		Store:              r.Store,
		PreviousResponseID: r.PreviousResponseID,
	})
}

func (r *responseRequest) UnmarshalJSON(data []byte) error {
	type plain responseRequest
	var req plain
	if err := json.Unmarshal(data, &req); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	known := map[string]bool{
		"model":                true,
		"instructions":         true,
		"input":                true,
		"tools":                true,
		"text":                 true,
		"reasoning":            true,
		"parallel_tool_calls":  true,
		"store":                true,
		"stream":               true,
		"generate":             true,
		"previous_response_id": true,
	}
	for name, value := range fields {
		if known[name] {
			continue
		}
		req.Unsupported = value
		req.unsupportedFields = append(req.unsupportedFields, name)
	}
	*r = responseRequest(req)

	return nil
}
