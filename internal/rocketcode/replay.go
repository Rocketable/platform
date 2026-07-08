package rocketcode

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

const recoveryReplayMessageText = "The previous runtime was interrupted and has now restarted. Some tool calls or delegated subagent tasks from the interrupted turn may have partially completed without their results being recorded. Evaluate the current environment and conversation state before retrying actions, continuing work, or reporting completion."

const genericAbortedToolOutputText = "tool call aborted because the runtime stopped before the call completed. Side effects may have partially occurred. Inspect the current environment before deciding whether to retry or continue."

const taskAbortedToolOutputText = "subagent task aborted because the runtime stopped before the child result was delivered. The subagent may have partially completed work or spawned nested work. Inspect the environment and conversation state before deciding whether to retry, continue, or summarize uncertainty."

func projectReplayForOpenAI(items []responses.ResponseInputItemUnionParam) []responses.ResponseInputItemUnionParam {
	projected := make([]responses.ResponseInputItemUnionParam, 0, len(items))
	for i := range items {
		item := items[i]
		if item.OfCompaction == nil {
			projected = append(projected, item)
			continue
		}

		compaction := responses.ResponseCompactionItemParam{
			EncryptedContent: item.OfCompaction.EncryptedContent,
			ID:               item.OfCompaction.ID,
			Type:             item.OfCompaction.Type,
		}
		projected = append(projected, responses.ResponseInputItemUnionParam{OfCompaction: &compaction})
	}

	return projected
}

// RecoveredReplayInput converts an interrupted active-turn checkpoint into replay input for a recovered model turn.
func RecoveredReplayInput(checkpoint *ActiveTurnCheckpoint) ([]json.RawMessage, error) {
	items, err := ReplayInputToParams(checkpoint.ReplayInput)
	if err != nil {
		return nil, err
	}

	for i := range checkpoint.CompletedFunctionOutputs {
		if slices.ContainsFunc(items, func(item responses.ResponseInputItemUnionParam) bool {
			output := item.OfFunctionCallOutput

			return output != nil && output.CallID == checkpoint.CompletedFunctionOutputs[i].CallID
		}) {
			continue
		}

		outputItems, err := ReplayInputToParams(checkpoint.CompletedFunctionOutputs[i].ReplayInput)
		if err != nil {
			return nil, err
		}

		items = append(items, outputItems...)
	}

	items = append(items, abortedFunctionCallOutputs(items, checkpoint.OpenFunctionCalls)...)
	if !hasRecoveryReplayMessage(items) {
		items = appendRecoveryReplayMessage(items)
	}

	return ReplayInputFromParams(items)
}

func abortedFunctionCallOutputs(items []responses.ResponseInputItemUnionParam, openCalls []FunctionCallCheckpoint) []responses.ResponseInputItemUnionParam {
	completed := map[string]bool{}

	for i := range items {
		output := items[i].OfFunctionCallOutput
		if output == nil {
			continue
		}

		completed[output.CallID] = true
	}

	outputs := make([]responses.ResponseInputItemUnionParam, 0, len(openCalls))
	for _, call := range openCalls {
		if completed[call.CallID] {
			continue
		}

		outputText := genericAbortedToolOutputText
		if call.Name == "task" {
			outputText = taskAbortedToolOutputText
		}

		outputs = append(outputs, responses.ResponseInputItemUnionParam{OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
			CallID: call.CallID,
			Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: openai.String(outputText)},
			Type:   "function_call_output",
		}})
		completed[call.CallID] = true
	}

	return outputs
}

func appendRecoveryReplayMessage(items []responses.ResponseInputItemUnionParam) []responses.ResponseInputItemUnionParam {
	message := inputMessageParam(responses.EasyInputMessageRoleDeveloper, easyInputStringContent(recoveryReplayMessageText))

	return append(items, message)
}

func hasRecoveryReplayMessage(items []responses.ResponseInputItemUnionParam) bool {
	return slices.ContainsFunc(items, func(item responses.ResponseInputItemUnionParam) bool {
		message := item.OfMessage

		return message != nil && message.Role == responses.EasyInputMessageRoleDeveloper && message.Content.OfString.Valid() && message.Content.OfString.Value == recoveryReplayMessageText
	})
}

// CompactedOutputToReplayInput converts provider compaction output into durable replay input.
func CompactedOutputToReplayInput(items []responses.ResponseOutputItemUnion) ([]json.RawMessage, error) {
	input := make([]responses.ResponseInputItemUnionParam, 0, len(items))
	for i := range items {
		item, ok := compactedOutputItemToReplayInput(&items[i])
		if !ok {
			return nil, fmt.Errorf("unsupported compacted output item kind %q", items[i].Type)
		}

		input = append(input, item)
	}

	raw, err := ReplayInputFromParams(input)
	if err != nil {
		return nil, fmt.Errorf("encode compacted replay input: %w", err)
	}

	return raw, nil
}

func compactedOutputItemToReplayInput(item *responses.ResponseOutputItemUnion) (responses.ResponseInputItemUnionParam, bool) {
	switch item.Type {
	case "message":
		parts := make([]string, 0, len(item.Content))
		for i := range item.Content {
			if item.Content[i].Type == "output_text" {
				parts = append(parts, item.Content[i].Text)
			}
		}

		role := strings.TrimSpace(string(item.Role))
		if role == "" {
			role = "user"
		}

		message := responses.EasyInputMessageParam{Role: responses.EasyInputMessageRole(role), Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(strings.Join(parts, ""))}, Type: "message"}
		if item.Phase != "" {
			message.Phase = responses.EasyInputMessagePhase(item.Phase)
		}

		return responses.ResponseInputItemUnionParam{OfMessage: &message}, true
	case "compaction", "compaction_summary":
		parts := make([]string, 0, len(item.Content)+len(item.Summary))
		for i := range item.Content {
			if item.Content[i].Type == "output_text" {
				parts = append(parts, item.Content[i].Text)
			}
		}

		for i := range item.Summary {
			parts = append(parts, item.Summary[i].Text)
		}

		compaction := responses.ResponseCompactionItemParam{ID: openai.String(item.ID), EncryptedContent: item.EncryptedContent, Type: "compaction"}

		extra := map[string]any{}

		if content := strings.Join(parts, ""); content != "" {
			extra["content"] = content
			extra["summary"] = content
		}

		compaction.SetExtraFields(extra)

		return responses.ResponseInputItemUnionParam{OfCompaction: &compaction}, true
	case "reasoning":
		summary := ""
		if len(item.Summary) > 0 {
			summary = item.Summary[0].Text
		}

		reasoning := responses.ResponseReasoningItemParam{ID: item.ID, Summary: []responses.ResponseReasoningItemSummaryParam{{Text: summary}}, Type: "reasoning"}
		if item.EncryptedContent != "" {
			reasoning.EncryptedContent = openai.String(item.EncryptedContent)
		}

		return responses.ResponseInputItemUnionParam{OfReasoning: &reasoning}, true
	default:
		return responses.ResponseInputItemUnionParam{}, false
	}
}

// ReplayDecodeError describes one durable replay item that could not be decoded
// through the OpenAI SDK input union.
type ReplayDecodeError struct {
	EntryIndex int
	ItemIndex  int
	Kind       string
	Cause      error
}

func (e *ReplayDecodeError) Error() string {
	location := fmt.Sprintf("entry %d item %d", e.EntryIndex, e.ItemIndex)
	if e.Kind != "" {
		location += " kind " + e.Kind
	}

	return fmt.Sprintf("decode replay %s: %v", location, e.Cause)
}

func (e *ReplayDecodeError) Unwrap() error {
	return e.Cause
}

// ReplayInputFromParams returns SDK-native durable replay JSON after each item
// round-trips through responses.ResponseInputItemUnionParam.
func ReplayInputFromParams(items []responses.ResponseInputItemUnionParam) ([]json.RawMessage, error) {
	raw := make([]json.RawMessage, 0, len(items))
	for i := range items {
		data, err := json.Marshal(items[i])
		if err != nil {
			return nil, &ReplayDecodeError{EntryIndex: -1, ItemIndex: i, Kind: responseInputKind(&items[i]), Cause: fmt.Errorf("marshal SDK replay input: %w", err)}
		}

		var decoded responses.ResponseInputItemUnionParam
		if err := json.Unmarshal(data, &decoded); err != nil {
			return nil, &ReplayDecodeError{EntryIndex: -1, ItemIndex: i, Kind: responseInputKind(&items[i]), Cause: fmt.Errorf("unmarshal SDK replay input: %w", err)}
		}

		raw = append(raw, json.RawMessage(data))
	}

	return raw, nil
}

// ReplayInputToParams decodes SDK-native durable replay JSON through the OpenAI
// SDK input union.
func ReplayInputToParams(raw []json.RawMessage) ([]responses.ResponseInputItemUnionParam, error) {
	items := make([]responses.ResponseInputItemUnionParam, 0, len(raw))
	for i := range raw {
		var item responses.ResponseInputItemUnionParam
		if err := json.Unmarshal(raw[i], &item); err != nil {
			return nil, &ReplayDecodeError{EntryIndex: -1, ItemIndex: i, Kind: replayInputRawKind(raw[i]), Cause: fmt.Errorf("unmarshal SDK replay input: %w", err)}
		}

		if item.OfCompaction != nil {
			var stored struct {
				Content *string          `json:"content"`
				Summary *json.RawMessage `json:"summary"`
				Recent  *json.RawMessage `json:"recent"`
			}
			if err := json.Unmarshal(raw[i], &stored); err != nil {
				return nil, &ReplayDecodeError{EntryIndex: -1, ItemIndex: i, Kind: replayInputRawKind(raw[i]), Cause: fmt.Errorf("decode compaction replay extras: %w", err)}
			}

			extra := map[string]any{}
			if stored.Content != nil {
				extra["content"] = *stored.Content
			}

			if stored.Summary != nil {
				extra["summary"] = *stored.Summary
			}

			if stored.Recent != nil {
				extra["recent"] = *stored.Recent
			}

			if len(extra) > 0 {
				item.OfCompaction.SetExtraFields(extra)
			}
		}

		data, err := json.Marshal(item)
		if err != nil {
			return nil, &ReplayDecodeError{EntryIndex: -1, ItemIndex: i, Kind: replayInputRawKind(raw[i]), Cause: fmt.Errorf("marshal SDK replay input: %w", err)}
		}

		var roundTrip responses.ResponseInputItemUnionParam
		if err := json.Unmarshal(data, &roundTrip); err != nil {
			return nil, &ReplayDecodeError{EntryIndex: -1, ItemIndex: i, Kind: replayInputRawKind(raw[i]), Cause: fmt.Errorf("round-trip SDK replay input: %w", err)}
		}

		items = append(items, item)
	}

	return items, nil
}

func responseInputKind(item *responses.ResponseInputItemUnionParam) string {
	if typ := item.GetType(); typ != nil {
		return *typ
	}

	return ""
}

func replayInputRawKind(raw json.RawMessage) string {
	var object struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}

	return object.Type
}
