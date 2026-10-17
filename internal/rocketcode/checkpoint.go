package rocketcode

import (
	"context"
	"encoding/json"
	"fmt"
)

// Journal durably records completed steps of conversation work so an interrupted turn can resume.
type Journal interface {
	Load(ctx context.Context, key string) (json.RawMessage, bool, error)
	Save(ctx context.Context, key string, value json.RawMessage) error
	// SaveTrace persists display-only public progress for the root turn.
	SaveTrace(ctx context.Context, turnID string, trace []json.RawMessage) error
	// SaveCodeModeCall persists one Code Mode record of the root turn under its key, apart from the
	// trace, so a record is written when its call starts and finishes and never with other progress.
	SaveCodeModeCall(ctx context.Context, turnID, key string, record json.RawMessage) error
	// CodeModeCalls returns the root turn's saved Code Mode records in the order they were first saved.
	CodeModeCalls(ctx context.Context, turnID string) ([]json.RawMessage, error)
}

// InertJournal records nothing and never finds a recorded step.
type InertJournal struct{}

// Load never finds a recorded step.
func (InertJournal) Load(context.Context, string) (value json.RawMessage, found bool, err error) {
	return nil, false, nil
}

// Save discards one step.
func (InertJournal) Save(context.Context, string, json.RawMessage) error {
	return nil
}

// SaveTrace discards display-only progress.
func (InertJournal) SaveTrace(context.Context, string, []json.RawMessage) error {
	return nil
}

// SaveCodeModeCall discards a Code Mode record.
func (InertJournal) SaveCodeModeCall(context.Context, string, string, json.RawMessage) error {
	return nil
}

// CodeModeCalls never finds a Code Mode record.
func (InertJournal) CodeModeCalls(context.Context, string) ([]json.RawMessage, error) {
	return nil, nil
}

// TracelessJournal records steps in Parent but drops traces, so nested work
// (Task children, workflow workers) never replaces its root turn's progress.
type TracelessJournal struct {
	Parent Journal
}

// Load forwards to Parent.
func (j TracelessJournal) Load(ctx context.Context, key string) (value json.RawMessage, found bool, err error) {
	if value, found, err = j.Parent.Load(ctx, key); err != nil {
		return nil, false, fmt.Errorf("load parent journal step: %w", err)
	}

	return value, found, nil
}

// Save forwards to Parent.
func (j TracelessJournal) Save(ctx context.Context, key string, value json.RawMessage) error {
	if err := j.Parent.Save(ctx, key, value); err != nil {
		return fmt.Errorf("save parent journal step: %w", err)
	}

	return nil
}

// SaveTrace drops display-only progress.
func (TracelessJournal) SaveTrace(context.Context, string, []json.RawMessage) error {
	return nil
}

// SaveCodeModeCall drops a Code Mode record, which is display-only like the trace.
func (TracelessJournal) SaveCodeModeCall(context.Context, string, string, json.RawMessage) error {
	return nil
}

// CodeModeCalls finds no Code Mode record, as SaveCodeModeCall keeps none.
func (TracelessJournal) CodeModeCalls(context.Context, string) ([]json.RawMessage, error) {
	return nil, nil
}

// turnStep is a turn's recorded items under its turn ID. Response holds the latest
// provider response while its calls or final answer are not yet appended.
type turnStep struct {
	AppliedInputIDs []string          `json:"applied_input_ids,omitempty"`
	Record          SessionEntry      `json:"record"`
	Trace           []json.RawMessage `json:"trace,omitempty"`
	Response        json.RawMessage   `json:"response,omitempty"`
}

// callStep is a tool call's started marker; Output holds its function_call_output
// and replay items once the call finished.
type callStep struct {
	Output []json.RawMessage `json:"output,omitempty"`
}

// hostCallStep is a Code Mode host or MCP call's marker or result.
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
