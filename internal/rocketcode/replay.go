package rocketcode

import (
	"encoding/json"
	"fmt"
	"maps"

	"github.com/openai/openai-go/v3/responses"
)

const genericAbortedToolOutputText = "tool call aborted because the runtime stopped before the call completed. Side effects may have partially occurred. Inspect the current environment before deciding whether to retry or continue."

func projectReplayForOpenAI(items []responses.ResponseInputItemUnionParam) []responses.ResponseInputItemUnionParam {
	projected := make([]responses.ResponseInputItemUnionParam, 0, len(items))
	for i := range items {
		item := items[i]
		if item.OfMessage != nil {
			message := *item.OfMessage
			extra := maps.Clone(message.ExtraFields())
			delete(extra, "prompt_header")
			delete(extra, "input_id")
			delete(extra, "id")
			message.SetExtraFields(extra)
			item.OfMessage = &message
		}

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

		var storedMessage struct {
			Header string `json:"prompt_header"`
			ID     string `json:"input_id"`
			ItemID string `json:"id"`
		}
		if err := json.Unmarshal(raw[i], &storedMessage); err != nil {
			return nil, &ReplayDecodeError{EntryIndex: -1, ItemIndex: i, Kind: replayInputRawKind(raw[i]), Cause: fmt.Errorf("decode replay message metadata: %w", err)}
		}

		if item.OfMessage != nil {
			extra := map[string]any{}
			if storedMessage.Header != "" {
				extra["prompt_header"] = storedMessage.Header
			}

			if storedMessage.ID != "" {
				extra["input_id"] = storedMessage.ID
			}

			if storedMessage.ItemID != "" {
				extra["id"] = storedMessage.ItemID
			}

			if len(extra) > 0 {
				item.OfMessage.SetExtraFields(extra)
			}
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
