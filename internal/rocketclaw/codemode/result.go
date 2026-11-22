package codemode

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

type Result interface {
	String() string
	Type() string
	Freeze()
	Truth() starlark.Bool
	Hash() (uint32, error)
	Ok() bool
}

func ResultAttrNames(extra ...string) []string {
	names := append(slices.Clone(extra), "error", "ok", "or_else", "unwrap")
	slices.Sort(names)

	return slices.Compact(names)
}

func ResultAttr(v starlark.Value, failure *Error, name string) (starlark.Value, bool) {
	switch name {
	case "ok":
		return starlark.Bool(failure == nil), true
	case "error":
		if failure == nil {
			return starlark.None, true
		}

		return starlark.String(failure.failureText()), true
	case "unwrap":
		return noArgsMethod("unwrap", func() (starlark.Value, error) {
			if failure != nil {
				return nil, errors.New(failure.failureText())
			}

			return v, nil
		}), true
	case "or_else":
		return starlark.NewBuiltin("or_else", func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var fallback starlark.Value
			if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &fallback); err != nil {
				return nil, fmt.Errorf("%s arguments: %w", b.Name(), err)
			}

			if failure != nil {
				return fallback, nil
			}

			return v, nil
		}), true
	}

	return nil, false
}

func noArgsMethod(name string, call func() (starlark.Value, error)) *starlark.Builtin {
	return starlark.NewBuiltin(name, func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
			return nil, fmt.Errorf("%s arguments: %w", b.Name(), err)
		}

		return call()
	})
}

func HostResult(tool string, value starlark.Value, err error) starlark.Value {
	if err != nil {
		return NewFailure(tool, err.Error(), nil)
	}

	switch v := value.(type) {
	case starlark.String:
		return NewText("text", string(v), nil, nil)
	case *starlark.Dict:
		v.Freeze()

		return decodedDict{v, "dict_result"}
	}

	return value
}

type Error struct {
	tool, message string
	details       starlark.StringDict
}

func NewFailure(tool, message string, details starlark.StringDict) *Error {
	return &Error{tool: tool, message: message, details: details}
}

func (e *Error) Tool() string    { return e.tool }
func (e *Error) Message() string { return e.message }

func (e *Error) String() string {
	if output, _ := starlark.AsString(e.details["output"]); output != "" {
		return e.failureText() + "\n" + output
	}

	return e.failureText()
}

func (e *Error) Type() string         { return "error" }
func (e *Error) Freeze()              {}
func (e *Error) Truth() starlark.Bool { return false }
func (e *Error) Ok() bool             { return false }
func (e *Error) Name() string         { return "error" }

func (e *Error) Hash() (uint32, error) {
	return 0, errors.New("unhashable type: error")
}

func (e *Error) MarshalJSON() ([]byte, error) {
	fields := starlark.NewDict(len(e.details) + 2)
	_ = fields.SetKey(starlark.String("ok"), starlark.False)
	_ = fields.SetKey(starlark.String("error"), starlark.String(e.failureText()))

	for name, value := range e.details {
		_ = fields.SetKey(starlark.String(name), value)
	}

	encoded, err := encodeJSON(fields)

	return []byte(encoded), err
}

func (e *Error) Attr(name string) (starlark.Value, error) {
	if v, ok := ResultAttr(e, e, name); ok {
		return v, nil
	}

	if v, ok := e.details[name]; ok {
		return v, nil
	}

	return e, nil
}

func (e *Error) AttrNames() []string { return ResultAttrNames(e.details.Keys()...) }

func (e *Error) CompareSameType(op syntax.Token, y starlark.Value, _ int) (bool, error) {
	if op != syntax.EQL && op != syntax.NEQ {
		return false, fmt.Errorf("%s %s %s not implemented", e.Type(), op, y.Type())
	}

	return (e.failureText() == y.(*Error).failureText()) == (op == syntax.EQL), nil
}

func (e *Error) CallInternal(*starlark.Thread, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return e, nil
}

func (e *Error) withDetails(details starlark.StringDict) *Error {
	merged := starlark.StringDict{}
	maps.Copy(merged, details)
	maps.Copy(merged, e.details)

	return NewFailure(e.tool, e.message, merged)
}

func (e *Error) failureText() string {
	if e.tool == "" || strings.HasPrefix(e.message, e.tool+": ") {
		return e.message
	}

	return e.tool + ": " + e.message
}

type Match struct {
	path string
	line int
	text string
}

func NewMatch(path string, line int, text string) *Match {
	return &Match{path: path, line: line, text: text}
}

func (m *Match) String() string {
	if m.path == "" {
		return fmt.Sprintf("%d: %s", m.line, m.text)
	}

	return fmt.Sprintf("%s:%d: %s", m.path, m.line, m.text)
}

func (m *Match) Type() string         { return "match" }
func (m *Match) Freeze()              {}
func (m *Match) Truth() starlark.Bool { return true }
func (m *Match) Ok() bool             { return true }

func (m *Match) Hash() (uint32, error) {
	return 0, errors.New("unhashable type: match")
}

func (m *Match) MarshalJSON() ([]byte, error) {
	var path *string
	if m.path != "" {
		path = &m.path
	}

	encoded, err := json.Marshal(struct {
		Line int     `json:"line"`
		Path *string `json:"path"`
		Text string  `json:"text"`
	}{m.line, path, m.text})
	if err != nil {
		return nil, fmt.Errorf("encode match: %w", err)
	}

	return encoded, nil
}

func (m *Match) Attr(name string) (starlark.Value, error) {
	if v, ok := ResultAttr(m, nil, name); ok {
		return v, nil
	}

	switch name {
	case "path":
		if m.path == "" {
			return starlark.None, nil
		}

		return starlark.String(m.path), nil
	case "line":
		return starlark.MakeInt(m.line), nil
	case "text":
		return starlark.String(m.text), nil
	default:
		return nil, nil
	}
}

func (m *Match) AttrNames() []string { return ResultAttrNames("line", "path", "text") }

func encodeJSON(v starlark.Value) (string, error) {
	encoded, err := starlark.Call(&starlark.Thread{Name: "json"}, starjson.Module.Members["encode"], starlark.Tuple{v}, nil)
	if err != nil {
		return "", fmt.Errorf("%w", err)
	}

	return string(encoded.(starlark.String)), nil
}

func unbox(v starlark.Value) starlark.Value {
	unboxed, _ := unboxValue(v)

	return unboxed
}

func unboxValue(v starlark.Value) (starlark.Value, bool) {
	if IsFailure(v) {
		return v, false
	}

	switch v := v.(type) {
	case starlark.String:
		return v, false
	case *TextValue:
		return starlark.String(v.content), true
	case decodedDict:
		return v.Dict, true
	case decodedList:
		return v.List, true
	default:
		return v, false
	}
}

func IsFailure(v starlark.Value) bool {
	r, ok := v.(Result)

	return ok && !r.Ok()
}

var (
	_ Result              = (*Error)(nil)
	_ starlark.HasAttrs   = (*Error)(nil)
	_ starlark.Callable   = (*Error)(nil)
	_ starlark.Comparable = (*Error)(nil)
	_ starlark.HasAttrs   = (*Match)(nil)
)
