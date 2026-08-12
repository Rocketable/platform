package rocketcode

import (
	"errors"

	"go.starlark.net/starlark"
)

// bashStarlarkResult is the Starlark value returned by the bash host tool.
type bashStarlarkResult struct {
	head      string
	full      string
	errorCode string
}

func newBashStarlarkResult(result BashResult) *bashStarlarkResult {
	return &bashStarlarkResult{
		head:      result.HeadOutput,
		full:      result.FullOutput,
		errorCode: result.ErrorCode,
	}
}

func (b *bashStarlarkResult) String() string       { return b.head }
func (b *bashStarlarkResult) Type() string         { return "bash_result" }
func (b *bashStarlarkResult) Freeze()              {}
func (b *bashStarlarkResult) Truth() starlark.Bool { return b.errorCode == "" }
func (b *bashStarlarkResult) Hash() (uint32, error) {
	return 0, errors.New("unhashable type: bash_result")
}

func (b *bashStarlarkResult) Attr(name string) (starlark.Value, error) {
	switch name {
	case "head_output":
		return starlark.String(b.head), nil
	case "full_output":
		return starlark.String(b.full), nil
	case "error_code":
		return starlark.String(b.errorCode), nil
	default:
		return nil, nil
	}
}

func (b *bashStarlarkResult) AttrNames() []string {
	return []string{"error_code", "full_output", "head_output"}
}

var (
	_ starlark.Value    = (*bashStarlarkResult)(nil)
	_ starlark.HasAttrs = (*bashStarlarkResult)(nil)
)
