package harness

import (
	"context"
	"encoding/json"
	"fmt"
)

type Journal interface {
	Load(ctx context.Context, key string) (json.RawMessage, bool, error)
	Save(ctx context.Context, key string, value json.RawMessage) error
	SaveTurn(ctx context.Context, turnID string, step json.RawMessage, lists []TurnItems) error
	Items(ctx context.Context, turnID string, list TurnList) ([]json.RawMessage, error)
	SaveTrace(ctx context.Context, turnID string, trace []json.RawMessage) error
	SaveCodeModeCall(ctx context.Context, turnID, key string, record json.RawMessage) error
	CodeModeCalls(ctx context.Context, turnID string) ([]json.RawMessage, error)
}

type TurnList string

const (
	TurnReplayInput TurnList = "replay_input"
	TurnOutputTrace TurnList = "output_trace"
	TurnTrace       TurnList = "trace"
)

type TurnItems struct {
	List  TurnList
	From  int
	Items []json.RawMessage
}

type InertJournal struct{}

func (InertJournal) Load(context.Context, string) (value json.RawMessage, found bool, err error) {
	return nil, false, nil
}

func (InertJournal) Save(context.Context, string, json.RawMessage) error {
	return nil
}

func (InertJournal) SaveTurn(context.Context, string, json.RawMessage, []TurnItems) error {
	return nil
}

func (InertJournal) Items(context.Context, string, TurnList) ([]json.RawMessage, error) {
	return nil, nil
}

func (InertJournal) SaveTrace(context.Context, string, []json.RawMessage) error {
	return nil
}

func (InertJournal) SaveCodeModeCall(context.Context, string, string, json.RawMessage) error {
	return nil
}

func (InertJournal) CodeModeCalls(context.Context, string) ([]json.RawMessage, error) {
	return nil, nil
}

type TracelessJournal struct {
	Parent Journal
}

func (j TracelessJournal) Load(ctx context.Context, key string) (value json.RawMessage, found bool, err error) {
	if value, found, err = j.Parent.Load(ctx, key); err != nil {
		return nil, false, fmt.Errorf("load parent journal step: %w", err)
	}

	return value, found, nil
}

func (j TracelessJournal) Save(ctx context.Context, key string, value json.RawMessage) error {
	if err := j.Parent.Save(ctx, key, value); err != nil {
		return fmt.Errorf("save parent journal step: %w", err)
	}

	return nil
}

func (j TracelessJournal) SaveTurn(ctx context.Context, turnID string, step json.RawMessage, lists []TurnItems) error {
	if err := j.Parent.SaveTurn(ctx, turnID, step, lists); err != nil {
		return fmt.Errorf("save parent journal turn: %w", err)
	}

	return nil
}

func (j TracelessJournal) Items(ctx context.Context, turnID string, list TurnList) ([]json.RawMessage, error) {
	items, err := j.Parent.Items(ctx, turnID, list)
	if err != nil {
		return nil, fmt.Errorf("load parent journal turn items: %w", err)
	}

	return items, nil
}

func (TracelessJournal) SaveTrace(context.Context, string, []json.RawMessage) error {
	return nil
}

func (TracelessJournal) SaveCodeModeCall(context.Context, string, string, json.RawMessage) error {
	return nil
}

func (TracelessJournal) CodeModeCalls(context.Context, string) ([]json.RawMessage, error) {
	return nil, nil
}

type turnStep struct {
	AppliedInputIDs []string          `json:"applied_input_ids,omitempty"`
	Record          SessionEntry      `json:"record"`
	Trace           []json.RawMessage `json:"trace,omitempty"`
	Response        json.RawMessage   `json:"response,omitempty"`
	Revision        int               `json:"revision,omitempty"`
}

type callStep struct {
	Output []json.RawMessage `json:"output,omitempty"`
}

type hostCallStep struct {
	Name   string      `json:"name"`
	Hash   string      `json:"hash"`
	Done   bool        `json:"done,omitempty"`
	Output string      `json:"output,omitempty"`
	Bash   *BashResult `json:"bash,omitempty"`
	Err    string      `json:"error,omitempty"`
}

func loadStep(ctx context.Context, journal Journal, key string, step any) (bool, error) {
	raw, ok, err := journal.Load(context.WithoutCancel(ctx), key)
	if err != nil {
		return false, fmt.Errorf("load journal step %q: %w", key, err)
	}

	if !ok {
		return false, nil
	}

	if err := json.Unmarshal(raw, step); err != nil {
		return false, fmt.Errorf("decode journal step %q: %w", key, err)
	}

	return true, nil
}

func saveStep(ctx context.Context, journal Journal, key string, step any) error {
	raw, err := json.Marshal(step)
	if err != nil {
		return fmt.Errorf("encode journal step %q: %w", key, err)
	}

	if err := journal.Save(context.WithoutCancel(ctx), key, raw); err != nil {
		return fmt.Errorf("save journal step %q: %w", key, err)
	}

	return nil
}
