package codemode

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
)

type Result interface {
	String() string
	Type() string
	Freeze()
	Truth() starlark.Bool
	Hash() (uint32, error)
	Ok() bool
}

type Text interface {
	String() string
	Type() string
	Freeze()
	Truth() starlark.Bool
	Hash() (uint32, error)
	Text() string
}

func ResultAttrNames(extra ...string) []string {
	names := append(slices.Clone(extra), "error", "ok", "or_else", "unwrap")
	slices.Sort(names)

	return names
}

func ResultAttr(v starlark.Value, failure *Error, name string) (starlark.Value, bool) {
	switch name {
	case "ok":
		return starlark.Bool(failure == nil), true
	case "error":
		if failure == nil {
			return starlark.None, true
		}

		return starlark.String(failure.message), true
	case "unwrap":
		return noArgsMethod("unwrap", func() (starlark.Value, error) {
			if failure != nil {
				return nil, fmt.Errorf("%s: %s", failure.tool, failure.message)
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
		return NewError(tool, err.Error())
	}

	switch v := value.(type) {
	case starlark.String:
		return NewText(string(v), PlainText{})
	case *starlark.Dict:
		return NewDictResult(v)
	default:
		return value
	}
}

type Error struct {
	tool, message string
}

func NewError(tool, message string) *Error {
	return &Error{tool: tool, message: message}
}

func (e *Error) Tool() string         { return e.tool }
func (e *Error) Message() string      { return e.message }
func (e *Error) String() string       { return e.message }
func (e *Error) Text() string         { return e.message }
func (e *Error) Type() string         { return "error" }
func (e *Error) Freeze()              {}
func (e *Error) Truth() starlark.Bool { return false }
func (e *Error) Ok() bool             { return false }
func (e *Error) Name() string         { return "error" }

func (e *Error) Hash() (uint32, error) {
	return 0, errors.New("unhashable type: error")
}

func (e *Error) MarshalJSON() ([]byte, error) {
	encoded, err := json.Marshal(struct {
		Error string `json:"error"`
	}{e.message})
	if err != nil {
		return nil, fmt.Errorf("encode error value: %w", err)
	}

	return encoded, nil
}

func (e *Error) Attr(name string) (starlark.Value, error) {
	if v, ok := ResultAttr(e, e, name); ok {
		return v, nil
	}

	return e, nil
}

func (e *Error) AttrNames() []string { return ResultAttrNames() }

func (e *Error) CallInternal(*starlark.Thread, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return e, nil
}

type DictResult struct {
	dict *starlark.Dict
}

func NewDictResult(d *starlark.Dict) *DictResult {
	d.Freeze()

	return &DictResult{dict: d}
}

func (d *DictResult) String() string       { return d.dict.String() }
func (d *DictResult) Type() string         { return "dict_result" }
func (d *DictResult) Freeze()              {}
func (d *DictResult) Truth() starlark.Bool { return d.dict.Len() > 0 }
func (d *DictResult) Ok() bool             { return true }
func (d *DictResult) Len() int             { return d.dict.Len() }
func (d *DictResult) Iterate() starlark.Iterator {
	return d.dict.Iterate()
}
func (d *DictResult) Items() []starlark.Tuple { return d.dict.Items() }

func (d *DictResult) Get(k starlark.Value) (starlark.Value, bool, error) {
	v, found, err := d.dict.Get(k)
	if err != nil {
		return nil, false, fmt.Errorf("dict_result: %w", err)
	}

	return v, found, nil
}

func (d *DictResult) Hash() (uint32, error) {
	return 0, errors.New("unhashable type: dict_result")
}

func (d *DictResult) MarshalJSON() ([]byte, error) {
	encoded, err := encodeJSON(d.dict)

	return []byte(encoded), err
}

func (d *DictResult) Attr(name string) (starlark.Value, error) {
	if v, ok := ResultAttr(d, nil, name); ok {
		return v, nil
	}

	switch name {
	case "get", "items", "keys", "values":
		method, err := d.dict.Attr(name)
		if err != nil {
			return nil, fmt.Errorf("dict_result.%s: %w", name, err)
		}

		return method, nil
	default:
		return nil, nil
	}
}

func (d *DictResult) AttrNames() []string {
	return ResultAttrNames("get", "items", "keys", "values")
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
	switch v := v.(type) {
	case starlark.String:
		return v, false
	case *Error:
		return starlark.String(v.message), true
	case Text:
		return starlark.String(v.Text()), true
	case *DictResult:
		return v.dict, true
	default:
		return v, false
	}
}

func isFailure(v starlark.Value) bool {
	r, ok := v.(Result)

	return ok && !r.Ok()
}

var (
	_ Result                   = (*Error)(nil)
	_ Text                     = (*Error)(nil)
	_ starlark.HasAttrs        = (*Error)(nil)
	_ starlark.Callable        = (*Error)(nil)
	_ starlark.IterableMapping = (*DictResult)(nil)
	_ starlark.Sequence        = (*DictResult)(nil)
	_ starlark.HasAttrs        = (*Match)(nil)
)
