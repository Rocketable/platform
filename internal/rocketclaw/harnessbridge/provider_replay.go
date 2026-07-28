package harnessbridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"

	"github.com/Rocketable/platform/internal/rocketcode"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

func providerForModel(model string) string {
	model = strings.TrimSpace(model)
	if provider, _, ok := strings.Cut(model, "/"); ok && provider != "" {
		return provider
	}

	return "openai"
}

func sessionEntryForProvider(entry *rocketcode.SessionEntry, provider string) (rocketcode.SessionEntry, error) {
	if providerForModel(entry.Model) == provider {
		return *entry, nil
	}

	replay, err := replayForProvider(entry.ReplayInput)
	if err != nil {
		return rocketcode.SessionEntry{}, err
	}

	projected := *entry
	projected.ResponseID = ""
	projected.ReplayInput = replay
	projected.OutputTrace = nil

	return projected, nil
}

func sessionEntriesForProvider(entries iter.Seq2[rocketcode.SessionEntry, error], provider string) iter.Seq2[rocketcode.SessionEntry, error] {
	return func(yield func(rocketcode.SessionEntry, error) bool) {
		for entry, err := range entries {
			if err == nil {
				entry, err = sessionEntryForProvider(&entry, provider)
			}

			if !yield(entry, err) {
				return
			}
		}
	}
}

func activeTurnForProvider(checkpoint *rocketcode.ActiveTurnCheckpoint, provider string) (rocketcode.ActiveTurnCheckpoint, error) {
	if providerForModel(checkpoint.DisplayModel) == provider {
		return *checkpoint, nil
	}

	replay, err := replayForProvider(checkpoint.ReplayInput)
	if err != nil {
		return rocketcode.ActiveTurnCheckpoint{}, err
	}

	outputs := make([]rocketcode.FunctionOutputCheckpoint, len(checkpoint.CompletedFunctionOutputs))
	for i, output := range checkpoint.CompletedFunctionOutputs {
		outputs[i] = output

		outputs[i].ReplayInput, err = replayForProvider(output.ReplayInput)
		if err != nil {
			return rocketcode.ActiveTurnCheckpoint{}, err
		}
	}

	projected := *checkpoint
	projected.ResponseID = ""
	projected.ReplayInput = replay
	projected.OutputTrace = nil

	calls := make([]rocketcode.FunctionCallCheckpoint, len(checkpoint.OpenFunctionCalls))
	for i, call := range checkpoint.OpenFunctionCalls {
		calls[i] = call
		calls[i].Arguments = json.RawMessage(append([]byte(nil), call.Arguments...))
	}

	projected.OpenFunctionCalls = calls
	projected.CompletedFunctionOutputs = outputs

	return projected, nil
}

func replayForProvider(rawItems []json.RawMessage) ([]json.RawMessage, error) {
	items := make([]responses.ResponseInputItemUnionParam, 0, len(rawItems))
	for i, raw := range rawItems {
		var kind struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &kind); err != nil {
			return nil, fmt.Errorf("decode replay item %d type: %w", i, err)
		}

		switch kind.Type {
		case "message":
			var item replayMessage
			if err := json.Unmarshal(raw, &item); err != nil {
				return nil, fmt.Errorf("decode replay item %d message: %w", i, err)
			}

			message, keep, err := portableMessage(item.Role, item.Phase, item.Content)
			if err != nil {
				return nil, fmt.Errorf("decode replay item %d message %w", i, err)
			}

			if keep {
				items = append(items, message)
			}
		case "function_call":
			call, err := portableFunctionCall(raw)
			if err != nil {
				return nil, fmt.Errorf("decode replay item %d function call %w", i, err)
			}

			items = append(items, call)
		case "function_call_output":
			var item replayFunctionOutput
			if err := json.Unmarshal(raw, &item); err != nil {
				return nil, fmt.Errorf("decode replay item %d function output: %w", i, err)
			}

			output, keep, err := portableFunctionOutput(item.CallID, item.Output)
			if err != nil {
				return nil, fmt.Errorf("decode replay item %d function output %w", i, err)
			}

			if keep {
				items = append(items, output)
			}
		case "reasoning":
			var item replayReasoning
			if err := json.Unmarshal(raw, &item); err != nil {
				return nil, fmt.Errorf("decode replay item %d reasoning: %w", i, err)
			}

			texts, _, err := readableText(item.Summary)
			if err != nil {
				return nil, fmt.Errorf("decode replay item %d reasoning summary: %w", i, err)
			}

			for _, text := range texts {
				if strings.TrimSpace(text) != "" {
					message := responses.ResponseInputItemParamOfMessage(text, responses.EasyInputMessageRoleAssistant)
					message.OfMessage.Type = "message"
					items = append(items, message)
				}
			}
		case "compaction", "compaction_summary":
			var item replayCompaction
			if err := json.Unmarshal(raw, &item); err != nil {
				return nil, fmt.Errorf("decode replay item %d compaction: %w", i, err)
			}

			texts, present, err := readableText(item.Content)
			if err != nil {
				return nil, fmt.Errorf("decode replay item %d compaction content: %w", i, err)
			}

			texts = nonblankText(texts)
			if !present || len(texts) == 0 {
				texts, _, err = readableText(item.Summary)
				if err != nil {
					return nil, fmt.Errorf("decode replay item %d compaction summary: %w", i, err)
				}

				texts = nonblankText(texts)
			}

			for _, text := range texts {
				message := responses.ResponseInputItemParamOfMessage(text, responses.EasyInputMessageRoleAssistant)
				message.OfMessage.Type = "message"
				items = append(items, message)
			}
		}
	}

	replay, err := rocketcode.ReplayInputFromParams(items)
	if err != nil {
		return nil, fmt.Errorf("encode projected replay: %w", err)
	}

	return replay, nil
}

type replayMessage struct {
	Role, Phase string
	Content     json.RawMessage
}

type replayFunctionCall struct {
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type replayFunctionOutput struct {
	CallID string `json:"call_id"`
	Output json.RawMessage
}

type replayReasoning struct{ Summary json.RawMessage }

type replayCompaction struct{ Content, Summary json.RawMessage }

type portableContent struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Detail   string `json:"detail"`
	ImageURL string `json:"image_url"`
	FileData string `json:"file_data"`
	FileURL  string `json:"file_url"`
	Filename string `json:"filename"`
}

func portableFunctionCall(raw json.RawMessage) (responses.ResponseInputItemUnionParam, error) {
	var item replayFunctionCall
	if err := json.Unmarshal(raw, &item); err != nil {
		return responses.ResponseInputItemUnionParam{}, fmt.Errorf("decode fields: %w", err)
	}

	if strings.TrimSpace(item.CallID) == "" {
		return responses.ResponseInputItemUnionParam{}, errors.New("call_id: required")
	}

	if strings.TrimSpace(item.Name) == "" {
		return responses.ResponseInputItemUnionParam{}, errors.New("name: required")
	}

	arguments := bytes.TrimSpace(item.Arguments)
	if len(arguments) == 0 || arguments[0] != '"' {
		return responses.ResponseInputItemUnionParam{}, errors.New("arguments: required string")
	}

	var argumentsText string
	if err := json.Unmarshal(arguments, &argumentsText); err != nil {
		return responses.ResponseInputItemUnionParam{}, fmt.Errorf("arguments: %w", err)
	}

	call := responses.ResponseInputItemParamOfFunctionCall(argumentsText, item.CallID, item.Name)
	call.OfFunctionCall.Type = "function_call"

	return call, nil
}

func portableMessage(role, phase string, raw json.RawMessage) (responses.ResponseInputItemUnionParam, bool, error) {
	if strings.TrimSpace(role) == "" {
		return responses.ResponseInputItemUnionParam{}, false, errors.New("role: required")
	}

	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return responses.ResponseInputItemUnionParam{}, false, errors.New("content: missing")
	}

	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return responses.ResponseInputItemUnionParam{}, false, fmt.Errorf("content: decode string: %w", err)
		}

		message := responses.ResponseInputItemParamOfMessage(text, responses.EasyInputMessageRole(role))
		message.OfMessage.Phase = responses.EasyInputMessagePhase(phase)
		message.OfMessage.Type = "message"

		return message, true, nil
	}

	if raw[0] != '[' {
		return responses.ResponseInputItemUnionParam{}, false, errors.New("content: expected string or content array")
	}

	var content []portableContent
	if err := json.Unmarshal(raw, &content); err != nil {
		return responses.ResponseInputItemUnionParam{}, false, fmt.Errorf("content: decode array: %w", err)
	}

	content = portableContentParts(content)

	parts := make(responses.ResponseInputMessageContentListParam, 0, len(content))
	for _, part := range content {
		switch part.Type {
		case "input_text", "output_text":
			parts = append(parts, responses.ResponseInputContentUnionParam{OfInputText: &responses.ResponseInputTextParam{Text: part.Text, Type: "input_text"}})
		case "input_image":
			parts = append(parts, responses.ResponseInputContentUnionParam{OfInputImage: &responses.ResponseInputImageParam{ImageURL: openai.String(part.ImageURL), Detail: responses.ResponseInputImageDetail(part.Detail), Type: "input_image"}})
		case "input_file":
			parts = append(parts, responses.ResponseInputContentUnionParam{OfInputFile: &responses.ResponseInputFileParam{FileData: openai.String(part.FileData), FileURL: openai.String(part.FileURL), Filename: openai.String(part.Filename), Detail: responses.ResponseInputFileDetail(part.Detail), Type: "input_file"}})
		}
	}

	if len(parts) == 0 {
		return responses.ResponseInputItemUnionParam{}, false, nil
	}

	message := responses.ResponseInputItemParamOfMessage(parts, responses.EasyInputMessageRole(role))
	message.OfMessage.Phase = responses.EasyInputMessagePhase(phase)
	message.OfMessage.Type = "message"

	return message, true, nil
}

func portableFunctionOutput(callID string, raw json.RawMessage) (responses.ResponseInputItemUnionParam, bool, error) {
	if strings.TrimSpace(callID) == "" {
		return responses.ResponseInputItemUnionParam{}, false, errors.New("call_id: required")
	}

	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return responses.ResponseInputItemUnionParam{}, false, errors.New("missing output")
	}

	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return responses.ResponseInputItemUnionParam{}, false, fmt.Errorf("decode string: %w", err)
		}

		output := responses.ResponseInputItemParamOfFunctionCallOutput(callID, text)
		output.OfFunctionCallOutput.Type = "function_call_output"

		return output, true, nil
	}

	if raw[0] != '[' {
		return responses.ResponseInputItemUnionParam{}, false, errors.New("expected string or content array")
	}

	var content []portableContent
	if err := json.Unmarshal(raw, &content); err != nil {
		return responses.ResponseInputItemUnionParam{}, false, fmt.Errorf("decode function output content: %w", err)
	}

	content = portableContentParts(content)

	parts := make(responses.ResponseFunctionCallOutputItemListParam, 0, len(content))
	for _, part := range content {
		switch part.Type {
		case "input_text", "output_text":
			parts = append(parts, responses.ResponseFunctionCallOutputItemUnionParam{OfInputText: &responses.ResponseInputTextContentParam{Text: part.Text, Type: "input_text"}})
		case "input_image":
			parts = append(parts, responses.ResponseFunctionCallOutputItemUnionParam{OfInputImage: &responses.ResponseInputImageContentParam{ImageURL: openai.String(part.ImageURL), Detail: responses.ResponseInputImageContentDetail(part.Detail), Type: "input_image"}})
		case "input_file":
			parts = append(parts, responses.ResponseFunctionCallOutputItemUnionParam{OfInputFile: &responses.ResponseInputFileContentParam{FileData: openai.String(part.FileData), FileURL: openai.String(part.FileURL), Filename: openai.String(part.Filename), Detail: responses.ResponseInputFileContentDetail(part.Detail), Type: "input_file"}})
		}
	}

	if len(parts) == 0 {
		return responses.ResponseInputItemUnionParam{}, false, nil
	}

	output := responses.ResponseInputItemParamOfFunctionCallOutput(callID, parts)
	output.OfFunctionCallOutput.Type = "function_call_output"

	return output, true, nil
}

func portableContentParts(parts []portableContent) []portableContent {
	n := 0

	for _, part := range parts {
		portable := part.Type == "input_text" || part.Type == "output_text" || part.Type == "input_image" && strings.TrimSpace(part.ImageURL) != "" || part.Type == "input_file" && (strings.TrimSpace(part.FileData) != "" || strings.TrimSpace(part.FileURL) != "")
		if portable {
			parts[n] = part
			n++
		}
	}

	return parts[:n]
}

func nonblankText(texts []string) []string {
	n := 0

	for _, text := range texts {
		if strings.TrimSpace(text) != "" {
			texts[n] = text
			n++
		}
	}

	return texts[:n]
}

func readableText(raw json.RawMessage) (texts []string, present bool, err error) {
	if raw == nil {
		return nil, false, nil
	}

	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, true, errors.New("empty value")
	}

	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, true, fmt.Errorf("decode string: %w", err)
		}

		return []string{text}, true, nil
	}

	decode := func(part json.RawMessage) (string, error) {
		var item struct {
			Text json.RawMessage `json:"text"`
		}
		if err := json.Unmarshal(part, &item); err != nil {
			return "", fmt.Errorf("decode object: %w", err)
		}

		var text string

		if item.Text == nil {
			return "", errors.New("missing text")
		}

		if err := json.Unmarshal(item.Text, &text); err != nil {
			return "", fmt.Errorf("decode text: %w", err)
		}

		return text, nil
	}

	switch raw[0] {
	case '{':
		text, err := decode(raw)
		return []string{text}, true, err
	case '[':
		var parts []json.RawMessage
		if err := json.Unmarshal(raw, &parts); err != nil {
			return nil, true, fmt.Errorf("decode array: %w", err)
		}

		texts := make([]string, len(parts))
		for i := range parts {
			text, err := decode(parts[i])
			if err != nil {
				return nil, true, fmt.Errorf("item %d: %w", i, err)
			}

			texts[i] = text
		}

		return texts, true, nil
	default:
		return nil, true, errors.New("expected string, text object, or text array")
	}
}
