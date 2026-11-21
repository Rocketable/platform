package harness

import (
	"encoding/json"
	"errors"

	"go.starlark.net/starlark"

	"github.com/Rocketable/platform/internal/rocketclaw/codemode"
)

type bashStarlarkResult struct {
	output    string
	errorCode string
}

func (b *bashStarlarkResult) String() string       { return b.output }
func (b *bashStarlarkResult) Text() string         { return b.output }
func (b *bashStarlarkResult) Type() string         { return "bash_result" }
func (b *bashStarlarkResult) Freeze()              {}
func (b *bashStarlarkResult) Truth() starlark.Bool { return starlark.Bool(b.Ok()) }
func (b *bashStarlarkResult) Ok() bool             { return b.errorCode == "" }
func (b *bashStarlarkResult) Hash() (uint32, error) {
	return 0, errors.New("unhashable type: bash_result")
}

func (b *bashStarlarkResult) MarshalJSON() ([]byte, error) {
	output, _ := json.Marshal(b.output)

	return output, nil
}

func (b *bashStarlarkResult) Attr(name string) (starlark.Value, error) {
	var failure *codemode.Error
	if !b.Ok() {
		failure = codemode.NewError("bash", b.output)
	}

	if v, ok := codemode.ResultAttr(b, failure, name); ok {
		return v, nil
	}

	if name == "error_code" {
		return starlark.String(b.errorCode), nil
	}

	method, _ := starlark.String(b.output).Attr(name)

	return method, nil
}

func (b *bashStarlarkResult) AttrNames() []string {
	return codemode.ResultAttrNames(append(starlark.String("").AttrNames(), "error_code")...)
}

var (
	_ codemode.Result   = (*bashStarlarkResult)(nil)
	_ codemode.Text     = (*bashStarlarkResult)(nil)
	_ starlark.HasAttrs = (*bashStarlarkResult)(nil)
)
