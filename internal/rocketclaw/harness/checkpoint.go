package harness

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

type turnStep struct {
	ReviewInputs    []PromptInput     `json:"review_inputs,omitempty"`
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
	Name     string              `json:"name"`
	Hash     string              `json:"hash"`
	Done     bool                `json:"done,omitempty"`
	Output   string              `json:"output,omitempty"`
	Bash     *BashResult         `json:"bash,omitempty"`
	Typed    *typedHostStep      `json:"typed,omitempty"`
	WebFetch *webFetchPage       `json:"webfetch,omitempty"`
	MCP      *mcp.CallToolResult `json:"mcp,omitempty"`
	Err      string              `json:"error,omitempty"`
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
