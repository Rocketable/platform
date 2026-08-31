package rocketcode

import (
	"errors"

	"go.starlark.net/starlark"
)

// shellStarlarkResult is the Starlark value returned by shell and python3 host tools.
type shellStarlarkResult struct {
	output    string
	errorCode string
	kind      string
}

func newHostCommandStarlarkResult(kind string, result ShellResult) *shellStarlarkResult {
	return &shellStarlarkResult{
		output:    result.Output,
		errorCode: result.ErrorCode,
		kind:      kind,
	}
}

func (b *shellStarlarkResult) String() string       { return b.output }
func (b *shellStarlarkResult) Type() string         { return b.kind }
func (b *shellStarlarkResult) Freeze()              {}
func (b *shellStarlarkResult) Truth() starlark.Bool { return b.errorCode == "" }
func (b *shellStarlarkResult) Hash() (uint32, error) {
	return 0, errors.New("unhashable type: " + b.kind)
}

func (b *shellStarlarkResult) Attr(name string) (starlark.Value, error) {
	switch name {
	case "error_code":
		return starlark.String(b.errorCode), nil
	default:
		return nil, nil
	}
}

func (b *shellStarlarkResult) AttrNames() []string {
	return []string{"error_code"}
}

var (
	_ starlark.Value    = (*shellStarlarkResult)(nil)
	_ starlark.HasAttrs = (*shellStarlarkResult)(nil)
)
