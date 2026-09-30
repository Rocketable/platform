package rocketcode

import (
	"encoding/json"
	"errors"
	"fmt"

	"go.starlark.net/starlark"
)

// bashStarlarkResult is the Starlark value returned by the bash host tool.
type bashStarlarkResult struct {
	output    string
	errorCode string
}

func newBashStarlarkResult(result BashResult) *bashStarlarkResult {
	return &bashStarlarkResult{
		output:    result.Output,
		errorCode: result.ErrorCode,
	}
}

func (b *bashStarlarkResult) String() string       { return b.output }
func (b *bashStarlarkResult) Type() string         { return "bash_result" }
func (b *bashStarlarkResult) Freeze()              {}
func (b *bashStarlarkResult) Truth() starlark.Bool { return b.errorCode == "" }
func (b *bashStarlarkResult) Hash() (uint32, error) {
	return 0, errors.New("unhashable type: bash_result")
}

// MarshalJSON preserves the output when the result is nested in a list or dict.
func (b *bashStarlarkResult) MarshalJSON() ([]byte, error) {
	output, err := json.Marshal(b.output)
	if err != nil {
		return nil, fmt.Errorf("marshal bash output: %w", err)
	}

	return output, nil
}

func (b *bashStarlarkResult) Attr(name string) (starlark.Value, error) {
	switch name {
	case "error_code":
		return starlark.String(b.errorCode), nil
	default:
		return nil, nil
	}
}

func (b *bashStarlarkResult) AttrNames() []string {
	return []string{"error_code"}
}

var (
	_ starlark.Value    = (*bashStarlarkResult)(nil)
	_ starlark.HasAttrs = (*bashStarlarkResult)(nil)
)
