package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/openai/openai-go/v3/responses"
)

const genericAbortedToolOutputText = "tool call aborted because the runtime stopped before the call completed. Side effects may have partially occurred. Inspect the current environment before deciding whether to retry or continue."

func projectReplayForOpenAI(items []responses.ResponseInputItemUnionParam) []responses.ResponseInputItemUnionParam {
	projected := make([]responses.ResponseInputItemUnionParam, len(items))
	for i := range items {
		item := items[i]
		if compaction := item.OfCompaction; compaction != nil {
			item = responses.ResponseInputItemUnionParam{OfCompaction: &responses.ResponseCompactionItemParam{
				EncryptedContent: compaction.EncryptedContent,
				ID:               compaction.ID,
				Type:             compaction.Type,
			}}
		} else if item.OfMessage != nil {
			message := *item.OfMessage
			extra := maps.Clone(message.ExtraFields())
			delete(extra, "prompt_header")
			delete(extra, "input_id")
			delete(extra, "id")
			message.SetExtraFields(extra)

			message.Content.OfInputItemContentList = slices.Clone(message.Content.OfInputItemContentList)
			for j, part := range message.Content.OfInputItemContentList {
				if part.OfInputImage != nil {
					message.Content.OfInputItemContentList[j].OfInputImage = withImageName(*part.OfInputImage, "")
				}
			}

			item.OfMessage = &message
		} else if item.OfFunctionCallOutput != nil {
			output := *item.OfFunctionCallOutput
			output.Output.OfResponseFunctionCallOutputItemArray = slices.Clone(output.Output.OfResponseFunctionCallOutputItemArray)

			for j, part := range output.Output.OfResponseFunctionCallOutputItemArray {
				if part.OfInputImage != nil {
					output.Output.OfResponseFunctionCallOutputItemArray[j].OfInputImage = withImageName(*part.OfInputImage, "")
				}
			}

			item.OfFunctionCallOutput = &output
		}

		projected[i] = item
	}

	return projected
}

func withImageName[T any, P interface {
	*T
	SetExtraFields(map[string]any)
}](image T, name string) *T {
	extra := map[string]any{"filename": name}
	if name == "" {
		extra = nil
	}

	P(&image).SetExtraFields(extra)

	return &image
}

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

func ReplayInputFromParams(items []responses.ResponseInputItemUnionParam) ([]json.RawMessage, error) {
	raw := make([]json.RawMessage, len(items))
	for i := range items {
		data, err := json.Marshal(items[i])
		if err != nil {
			return nil, &ReplayDecodeError{EntryIndex: -1, ItemIndex: i, Kind: responseInputKind(&items[i]), Cause: fmt.Errorf("marshal SDK replay input: %w", err)}
		}

		var decoded responses.ResponseInputItemUnionParam
		if err := json.Unmarshal(data, &decoded); err != nil {
			return nil, &ReplayDecodeError{EntryIndex: -1, ItemIndex: i, Kind: responseInputKind(&items[i]), Cause: fmt.Errorf("unmarshal SDK replay input: %w", err)}
		}

		raw[i] = data
	}

	return raw, nil
}

func ReplayInputToParams(raw []json.RawMessage) ([]responses.ResponseInputItemUnionParam, error) {
	items := make([]responses.ResponseInputItemUnionParam, len(raw))
	for i := range raw {
		item := &items[i]
		if err := json.Unmarshal(raw[i], item); err != nil {
			return nil, &ReplayDecodeError{EntryIndex: -1, ItemIndex: i, Kind: ReplayInputRawKind(raw[i]), Cause: fmt.Errorf("unmarshal SDK replay input: %w", err)}
		}

		var storedMessage struct {
			Header          string `json:"prompt_header"`
			ID              string `json:"input_id"`
			ItemID          string `json:"id"`
			Content, Output json.RawMessage
		}
		if err := json.Unmarshal(raw[i], &storedMessage); err != nil {
			return nil, &ReplayDecodeError{EntryIndex: -1, ItemIndex: i, Kind: ReplayInputRawKind(raw[i]), Cause: fmt.Errorf("decode replay message metadata: %w", err)}
		}

		parted := storedMessage.Output
		if item.OfMessage != nil {
			parted = storedMessage.Content
		}

		var parts []struct{ Filename string }
		if (item.OfMessage != nil || item.OfFunctionCallOutput != nil) && bytes.HasPrefix(parted, []byte("[")) {
			if err := json.Unmarshal(parted, &parts); err != nil {
				return nil, &ReplayDecodeError{EntryIndex: -1, ItemIndex: i, Kind: ReplayInputRawKind(raw[i]), Cause: fmt.Errorf("decode replay image names: %w", err)}
			}
		}

		for j, part := range parts {
			switch {
			case item.OfMessage != nil && item.OfMessage.Content.OfInputItemContentList[j].OfInputImage != nil:
				item.OfMessage.Content.OfInputItemContentList[j].OfInputImage = withImageName(*item.OfMessage.Content.OfInputItemContentList[j].OfInputImage, part.Filename)
			case item.OfFunctionCallOutput != nil && item.OfFunctionCallOutput.Output.OfResponseFunctionCallOutputItemArray[j].OfInputImage != nil:
				item.OfFunctionCallOutput.Output.OfResponseFunctionCallOutputItemArray[j].OfInputImage = withImageName(*item.OfFunctionCallOutput.Output.OfResponseFunctionCallOutputItemArray[j].OfInputImage, part.Filename)
			}
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
				Kept    *int             `json:"kept"`
			}
			if err := json.Unmarshal(raw[i], &stored); err != nil {
				return nil, &ReplayDecodeError{EntryIndex: -1, ItemIndex: i, Kind: ReplayInputRawKind(raw[i]), Cause: fmt.Errorf("decode compaction replay extras: %w", err)}
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

			if stored.Kept != nil {
				extra["kept"] = *stored.Kept
			}

			if len(extra) > 0 {
				item.OfCompaction.SetExtraFields(extra)
			}
		}

		data, err := json.Marshal(*item)
		if err != nil {
			return nil, &ReplayDecodeError{EntryIndex: -1, ItemIndex: i, Kind: ReplayInputRawKind(raw[i]), Cause: fmt.Errorf("marshal SDK replay input: %w", err)}
		}

		if !bytes.Equal(data, raw[i]) {
			var roundTrip responses.ResponseInputItemUnionParam
			if err := json.Unmarshal(data, &roundTrip); err != nil {
				return nil, &ReplayDecodeError{EntryIndex: -1, ItemIndex: i, Kind: ReplayInputRawKind(raw[i]), Cause: fmt.Errorf("round-trip SDK replay input: %w", err)}
			}
		}
	}

	return items, nil
}

func responseInputKind(item *responses.ResponseInputItemUnionParam) string {
	if typ := item.GetType(); typ != nil {
		return *typ
	}

	return ""
}

func ReplayInputRawKind(raw json.RawMessage) string {
	var object struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}

	return object.Type
}
