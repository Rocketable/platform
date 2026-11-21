package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

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
func (b *bashStarlarkResult) Truth() starlark.Bool { return b.output != "" }
func (b *bashStarlarkResult) Ok() bool             { return true }

func (b *bashStarlarkResult) Len() int                   { return len(b.output) }
func (b *bashStarlarkResult) Index(i int) starlark.Value { return codemode.TextIndex(b, i) }
func (b *bashStarlarkResult) Slice(start, end, step int) starlark.Value {
	return codemode.TextSlice(b, start, end, step)
}

func (b *bashStarlarkResult) Binary(op syntax.Token, y starlark.Value, side starlark.Side) (starlark.Value, error) {
	z, err := codemode.TextBinary(b, op, y, side)
	if err != nil {
		return nil, fmt.Errorf("%w", err)
	}

	return z, nil
}

func (b *bashStarlarkResult) Hash() (uint32, error) {
	return 0, errors.New("unhashable type: bash_result")
}

func (b *bashStarlarkResult) Failure() *codemode.Error {
	message := "failed"

	switch _, errNumber := strconv.Atoi(b.errorCode); {
	case b.errorCode == "":
		return nil
	case errNumber == nil:
		message = "exit status " + b.errorCode
	case b.errorCode == "timeout":
		message = "timed out"
	}

	return codemode.NewFailure("bash", message, starlark.StringDict{"output": starlark.String(b.output), "error_code": starlark.String(b.errorCode)})
}

func (b *bashStarlarkResult) MarshalJSON() ([]byte, error) {
	output, _ := json.Marshal(b.output)

	return output, nil
}

func (b *bashStarlarkResult) Attr(name string) (starlark.Value, error) {
	if v, ok := codemode.ResultAttr(b, nil, name); ok {
		return v, nil
	}

	switch name {
	case "output":
		return starlark.String(b.output), nil
	case "error_code":
		return starlark.String(b.errorCode), nil
	}

	method, _ := starlark.String(b.output).Attr(name)

	return method, nil
}

func (b *bashStarlarkResult) AttrNames() []string {
	return codemode.ResultAttrNames(append(starlark.String("").AttrNames(), "error_code", "output")...)
}

var (
	_ codemode.Result    = (*bashStarlarkResult)(nil)
	_ codemode.Text      = (*bashStarlarkResult)(nil)
	_ starlark.HasAttrs  = (*bashStarlarkResult)(nil)
	_ starlark.Sliceable = (*bashStarlarkResult)(nil)
	_ starlark.HasBinary = (*bashStarlarkResult)(nil)
)
