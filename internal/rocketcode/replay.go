package rocketcode

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
)

// ReplayProjectionTarget identifies the provider surface that will consume
// durable Responses-shaped replay.
type ReplayProjectionTarget struct {
	provider           string
	compatibleProvider string
	mode               string
}

// NewReplayProjectionTarget validates a replay projection target.
func NewReplayProjectionTarget(provider, compatibleProvider, mode string) (ReplayProjectionTarget, error) {
	target := ReplayProjectionTarget{provider: strings.TrimSpace(provider), compatibleProvider: strings.TrimSpace(compatibleProvider), mode: strings.TrimSpace(mode)}
	if err := target.validate(); err != nil {
		return ReplayProjectionTarget{}, err
	}

	return target, nil
}

func (target ReplayProjectionTarget) validate() error {
	switch target.provider {
	case modelProviderOpenAI:
		if target.compatibleProvider != "" || target.mode != string(OpenAICompatibleModeResponses) {
			return errors.New("invalid OpenAI replay projection target")
		}
	case modelProviderOpenAICompatible:
		if target.compatibleProvider == "" || (target.mode != string(OpenAICompatibleModeResponses) && target.mode != string(OpenAICompatibleModeChatCompletions)) {
			return errors.New("invalid OpenAI-compatible replay projection target")
		}
	case modelProviderAnthropic:
		if target.compatibleProvider != "" || target.mode != string(providerSurfaceAnthropic) {
			return errors.New("invalid Anthropic replay projection target")
		}
	default:
		return fmt.Errorf("unsupported replay projection provider %q", target.provider)
	}

	return nil
}

func replayProjectionTargetFromProviderTarget(target providerTarget) ReplayProjectionTarget {
	mode := string(OpenAICompatibleModeResponses)

	switch target.surface {
	case providerSurfaceResponses:
	case providerSurfaceChatCompletions:
		mode = string(OpenAICompatibleModeChatCompletions)
	case providerSurfaceAnthropic:
		mode = string(providerSurfaceAnthropic)
	}

	return ReplayProjectionTarget{provider: target.modelRef.provider, compatibleProvider: target.modelRef.compatibleProvider, mode: mode}
}

// ProjectReplayForTarget translates durable replay for a provider surface.
func ProjectReplayForTarget(items []responses.ResponseInputItemUnionParam, target ReplayProjectionTarget) ([]responses.ResponseInputItemUnionParam, error) {
	if err := target.validate(); err != nil {
		return nil, err
	}

	return projectReplayForTarget(items, target), nil
}

func projectReplayForTarget(items []responses.ResponseInputItemUnionParam, target ReplayProjectionTarget) []responses.ResponseInputItemUnionParam {
	projected := make([]responses.ResponseInputItemUnionParam, 0, len(items))
	for i := range items {
		item := items[i]
		if item.OfReasoning != nil && target.provider == modelProviderAnthropic {
			continue
		}

		if item.OfCompaction == nil {
			projected = append(projected, item)
			continue
		}

		if compactionMatchesTarget(item.OfCompaction, target) {
			compaction := responses.ResponseCompactionItemParam{
				EncryptedContent: item.OfCompaction.EncryptedContent,
				ID:               item.OfCompaction.ID,
				Type:             item.OfCompaction.Type,
			}
			projected = append(projected, responses.ResponseInputItemUnionParam{OfCompaction: &compaction})

			continue
		}

		message := responses.EasyInputMessageParam{Role: responses.EasyInputMessageRoleUser, Content: responses.EasyInputMessageContentUnionParam{OfString: openai.String(CompactionCheckpointText(item.OfCompaction))}, Type: "message"}
		projected = append(projected, responses.ResponseInputItemUnionParam{OfMessage: &message})
	}

	return projected
}

func compactionMatchesTarget(compaction *responses.ResponseCompactionItemParam, target ReplayProjectionTarget) bool {
	stored := compactionReplayFields(compaction)

	if stored.OriginProvider != target.provider || stored.OriginMode != target.mode {
		return false
	}

	if target.provider == modelProviderOpenAICompatible {
		return stored.OriginCompatibleProvider == target.compatibleProvider
	}

	return true
}

// CompactedOutputToReplayInput converts provider compaction output into durable replay input.
func CompactedOutputToReplayInput(items []responses.ResponseOutputItemUnion, originProvider, originCompatibleProvider, originMode string) ([]json.RawMessage, error) {
	input := make([]responses.ResponseInputItemUnionParam, 0, len(items))
	for i := range items {
		item, ok := compactedOutputItemToReplayInput(&items[i], originProvider, originCompatibleProvider, originMode)
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

func compactedOutputItemToReplayInput(item *responses.ResponseOutputItemUnion, originProvider, originCompatibleProvider, originMode string) (responses.ResponseInputItemUnionParam, bool) {
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

		extra := map[string]any{"origin_provider": originProvider, "origin_mode": originMode}
		if originCompatibleProvider != "" {
			extra["origin_compatible_provider"] = originCompatibleProvider
		}

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
				Content                  *string          `json:"content"`
				Summary                  *json.RawMessage `json:"summary"`
				Recent                   *json.RawMessage `json:"recent"`
				OriginProvider           *string          `json:"origin_provider"`
				OriginCompatibleProvider *string          `json:"origin_compatible_provider"`
				OriginMode               *string          `json:"origin_mode"`
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

			if stored.OriginProvider != nil {
				extra["origin_provider"] = *stored.OriginProvider
			}

			if stored.OriginCompatibleProvider != nil {
				extra["origin_compatible_provider"] = *stored.OriginCompatibleProvider
			}

			if stored.OriginMode != nil {
				extra["origin_mode"] = *stored.OriginMode
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
