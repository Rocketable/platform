package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

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

func (e *SessionEntry) forProvider(provider string) error {
	replay := make([]json.RawMessage, 0, len(e.ReplayInput))

	boundaries := make([]int, len(e.ReplayInput)+1)
	for i, raw := range e.ReplayInput {
		boundaries[i] = len(replay)
		if providerForModel(e.AttributionAt(i).Model) == provider {
			replay = append(replay, raw)
			continue
		}

		var kind struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &kind); err != nil {
			return fmt.Errorf("decode replay item %d type: %w", i, err)
		}

		var (
			items        []responses.ResponseInputItemUnionParam
			summaryTexts []string
		)

		switch kind.Type {
		case "message":
			var item replayMessage
			if err := json.Unmarshal(raw, &item); err != nil {
				return fmt.Errorf("decode replay item %d message: %w", i, err)
			}

			message, keep, err := portableMessage(item.Role, item.Phase, item.Content)
			if err != nil {
				return fmt.Errorf("decode replay item %d message %w", i, err)
			}

			if keep {
				fields := make(map[string]any)

				if item.Role != "assistant" {
					item.ID = ""
				}

				for key, value := range map[string]string{"prompt_header": item.Header, "input_id": item.InputID, "id": item.ID} {
					if value != "" {
						fields[key] = value
					}
				}

				message.OfMessage.SetExtraFields(fields)
				items = append(items, message)
			}
		case "function_call":
			call, err := portableFunctionCall(raw)
			if err != nil {
				return fmt.Errorf("decode replay item %d function call %w", i, err)
			}

			items = append(items, call)
		case "function_call_output":
			var item replayFunctionOutput
			if err := json.Unmarshal(raw, &item); err != nil {
				return fmt.Errorf("decode replay item %d function output: %w", i, err)
			}

			output, keep, err := portableFunctionOutput(item.CallID, item.Output)
			if err != nil {
				return fmt.Errorf("decode replay item %d function output %w", i, err)
			}

			if keep {
				items = append(items, output)
			}
		case "reasoning":
			var item struct{ Summary []struct{ Text string } }
			if json.Unmarshal(raw, &item) == nil {
				for _, part := range item.Summary {
					summaryTexts = append(summaryTexts, part.Text)
				}
			}
		case "compaction":
			var item struct{ Content string }
			if json.Unmarshal(raw, &item) == nil {
				summaryTexts = []string{item.Content}
			}
		}

		for _, text := range summaryTexts {
			if strings.TrimSpace(text) == "" {
				continue
			}

			message := responses.ResponseInputItemParamOfMessage(text, responses.EasyInputMessageRoleAssistant)
			message.OfMessage.Type = "message"
			items = append(items, message)
		}

		portable, err := ReplayInputFromParams(items)
		if err != nil {
			return fmt.Errorf("encode projected replay: %w", err)
		}

		replay = append(replay, portable...)
	}

	boundaries[len(e.ReplayInput)] = len(replay)
	e.ReplayAttribution = slices.Clone(e.ReplayAttribution)
	RemapReplayAttribution(e.ReplayAttribution, boundaries)
	e.ReplayInput = replay

	return nil
}

type replayMessage struct {
	Role, Phase string
	Content     json.RawMessage
	ID          string `json:"id"`
	Header      string `json:"prompt_header"`
	InputID     string `json:"input_id"`
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

func decodePortablePayload(raw json.RawMessage, missing, expected, decodeString, decodeArray string) (text string, content []portableContent, err error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", nil, errors.New(missing)
	}

	switch raw[0] {
	case '"':
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", nil, fmt.Errorf("%s: %w", decodeString, err)
		}

		return text, nil, nil
	case '[':
		if err := json.Unmarshal(raw, &content); err != nil {
			return "", nil, fmt.Errorf("%s: %w", decodeArray, err)
		}

		return "", slices.DeleteFunc(content, func(part portableContent) bool {
			portable := part.Type == "input_text" || part.Type == "output_text" || part.Type == "input_image" && strings.TrimSpace(part.ImageURL) != "" || part.Type == "input_file" && (strings.TrimSpace(part.FileData) != "" || strings.TrimSpace(part.FileURL) != "")
			return !portable
		}), nil
	default:
		return "", nil, errors.New(expected)
	}
}

func portableMessage(role, phase string, raw json.RawMessage) (responses.ResponseInputItemUnionParam, bool, error) {
	if strings.TrimSpace(role) == "" {
		return responses.ResponseInputItemUnionParam{}, false, errors.New("role: required")
	}

	text, content, err := decodePortablePayload(raw, "content: missing", "content: expected string or content array", "content: decode string", "content: decode array")
	if err != nil {
		return responses.ResponseInputItemUnionParam{}, false, err
	}

	var message responses.ResponseInputItemUnionParam
	if content == nil {
		message = responses.ResponseInputItemParamOfMessage(text, responses.EasyInputMessageRole(role))
	} else {
		parts := make(responses.ResponseInputMessageContentListParam, 0, len(content))
		for _, part := range content {
			switch part.Type {
			case "input_text", "output_text":
				parts = append(parts, responses.ResponseInputContentUnionParam{OfInputText: &responses.ResponseInputTextParam{Text: part.Text, Type: "input_text"}})
			case "input_image":
				parts = append(parts, responses.ResponseInputContentUnionParam{OfInputImage: withImageName(responses.ResponseInputImageParam{ImageURL: openai.String(part.ImageURL), Detail: responses.ResponseInputImageDetail(part.Detail), Type: "input_image"}, part.Filename)})
			case "input_file":
				parts = append(parts, responses.ResponseInputContentUnionParam{OfInputFile: &responses.ResponseInputFileParam{FileData: openai.String(part.FileData), FileURL: openai.String(part.FileURL), Filename: openai.String(part.Filename), Detail: responses.ResponseInputFileDetail(part.Detail), Type: "input_file"}})
			}
		}

		if len(parts) == 0 {
			return responses.ResponseInputItemUnionParam{}, false, nil
		}

		message = responses.ResponseInputItemParamOfMessage(parts, responses.EasyInputMessageRole(role))
	}

	message.OfMessage.Phase = responses.EasyInputMessagePhase(phase)
	message.OfMessage.Type = "message"

	return message, true, nil
}

func portableFunctionOutput(callID string, raw json.RawMessage) (responses.ResponseInputItemUnionParam, bool, error) {
	if strings.TrimSpace(callID) == "" {
		return responses.ResponseInputItemUnionParam{}, false, errors.New("call_id: required")
	}

	text, content, err := decodePortablePayload(raw, "missing output", "expected string or content array", "decode string", "decode function output content")
	if err != nil {
		return responses.ResponseInputItemUnionParam{}, false, err
	}

	var output responses.ResponseInputItemUnionParam
	if content == nil {
		output = responses.ResponseInputItemParamOfFunctionCallOutput(text)
	} else {
		parts := make(responses.ResponseFunctionCallOutputItemListParam, 0, len(content))
		for _, part := range content {
			switch part.Type {
			case "input_text", "output_text":
				parts = append(parts, responses.ResponseFunctionCallOutputItemUnionParam{OfInputText: &responses.ResponseInputTextContentParam{Text: part.Text, Type: "input_text"}})
			case "input_image":
				parts = append(parts, responses.ResponseFunctionCallOutputItemUnionParam{OfInputImage: withImageName(responses.ResponseInputImageContentParam{ImageURL: openai.String(part.ImageURL), Detail: responses.ResponseInputImageContentDetail(part.Detail), Type: "input_image"}, part.Filename)})
			case "input_file":
				parts = append(parts, responses.ResponseFunctionCallOutputItemUnionParam{OfInputFile: &responses.ResponseInputFileContentParam{FileData: openai.String(part.FileData), FileURL: openai.String(part.FileURL), Filename: openai.String(part.Filename), Detail: responses.ResponseInputFileContentDetail(part.Detail), Type: "input_file"}})
			}
		}

		if len(parts) == 0 {
			return responses.ResponseInputItemUnionParam{}, false, nil
		}

		output = responses.ResponseInputItemParamOfFunctionCallOutput(parts)
	}

	output.OfFunctionCallOutput.CallID = openai.String(callID)
	output.OfFunctionCallOutput.Type = "function_call_output"

	return output, true, nil
}
