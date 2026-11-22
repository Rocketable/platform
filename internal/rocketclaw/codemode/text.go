package codemode

import (
	"encoding/json"
	"fmt"
	"iter"
	"regexp"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

type TextValue struct {
	kind, content string
	attrs         starlark.StringDict
	data          json.RawMessage
}

func NewText(kind, content string, attrs starlark.StringDict, data json.RawMessage) *TextValue {
	return &TextValue{kind: kind, content: content, attrs: attrs, data: data}
}

func (t *TextValue) String() string       { return t.content }
func (t *TextValue) Type() string         { return t.kind }
func (t *TextValue) Freeze()              {}
func (t *TextValue) Truth() starlark.Bool { return t.content != "" }
func (t *TextValue) Ok() bool             { return true }
func (t *TextValue) Len() int             { return len(t.content) }

func (t *TextValue) Hash() (uint32, error) {
	h, _ := starlark.String(t.content).Hash()

	return h, nil
}

func (t *TextValue) Index(i int) starlark.Value { return starlark.String(t.content).Index(i) }

func (t *TextValue) Slice(start, end, step int) starlark.Value {
	return starlark.String(t.content).Slice(start, end, step)
}

func (t *TextValue) MarshalJSON() ([]byte, error) {
	if t.data != nil {
		return t.data, nil
	}

	encoded, err := encodeJSON(starlark.String(t.content))

	return []byte(encoded), err
}

func (t *TextValue) CompareSameType(op syntax.Token, y starlark.Value, depth int) (bool, error) {
	found, _ := starlark.String(t.content).CompareSameType(op, starlark.String(y.(*TextValue).content), depth)

	return found, nil
}

func (t *TextValue) Binary(op syntax.Token, y starlark.Value, side starlark.Side) (starlark.Value, error) {
	if op != syntax.PLUS && op != syntax.STAR && op != syntax.PERCENT && op != syntax.IN {
		return nil, nil
	}

	return binaryUnboxed(op, starlark.String(t.content), y, side)
}

func (t *TextValue) Attr(name string) (starlark.Value, error) {
	if v, ok := ResultAttr(t, nil, name); ok {
		return v, nil
	}

	if v, ok := t.attrs[name]; ok {
		return v, nil
	}

	switch name {
	case "path", "name":
		return starlark.None, nil
	case "lines":
		return noArgsMethod("lines", func() (starlark.Value, error) {
			var lines []starlark.Value
			for line := range t.lines() {
				lines = append(lines, starlark.String(line))
			}

			return starlark.NewList(lines), nil
		}), nil
	case "search":
		return starlark.NewBuiltin("search", t.search), nil
	case "bytes":
		return noArgsMethod("bytes", func() (starlark.Value, error) { return starlark.Bytes(t.content), nil }), nil
	}

	method, _ := starlark.String(t.content).Attr(name)

	return method, nil
}

func (t *TextValue) AttrNames() []string {
	return ResultAttrNames(append(append(t.attrs.Keys(), "bytes", "lines", "name", "path", "search"), starlark.String("").AttrNames()...)...)
}

func (t *TextValue) lines() iter.Seq[string] {
	return func(yield func(string) bool) {
		for line := range strings.Lines(t.content) {
			if !yield(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")) {
				return
			}
		}
	}
}

func (t *TextValue) search(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var pattern string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "pattern", &pattern); err != nil {
		return nil, fmt.Errorf("search arguments: %w", err)
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	path, _ := starlark.AsString(t.attrs["path"])
	n := 0

	if start, ok := t.attrs["start_line"].(starlark.Int); ok {
		first, _ := start.Int64()
		n = int(first) - 1
	}

	var matches []starlark.Value

	for line := range t.lines() {
		n++

		if re.MatchString(line) {
			matches = append(matches, NewMatch(path, n, line))
		}
	}

	return starlark.NewList(matches), nil
}

var (
	_ Result              = (*TextValue)(nil)
	_ starlark.HasAttrs   = (*TextValue)(nil)
	_ starlark.Sliceable  = (*TextValue)(nil)
	_ starlark.HasBinary  = (*TextValue)(nil)
	_ starlark.Comparable = (*TextValue)(nil)
)
