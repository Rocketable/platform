package codemode

import (
	"fmt"
	"iter"
	"regexp"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

type TextAttrs interface {
	Attr(name string) (starlark.Value, error)
	AttrNames() []string
}

type PlainText struct{}

func (PlainText) Attr(name string) (starlark.Value, error) {
	switch name {
	case "path", "name":
		return starlark.None, nil
	default:
		return nil, nil
	}
}

func (PlainText) AttrNames() []string { return []string{"name", "path"} }

type TextValue struct {
	content string
	attrs   TextAttrs
}

func NewText(content string, attrs TextAttrs) *TextValue {
	return &TextValue{content: content, attrs: attrs}
}

func (t *TextValue) String() string       { return t.content }
func (t *TextValue) Text() string         { return t.content }
func (t *TextValue) Type() string         { return "text" }
func (t *TextValue) Freeze()              {}
func (t *TextValue) Truth() starlark.Bool { return t.content != "" }
func (t *TextValue) Ok() bool             { return true }
func (t *TextValue) Len() int             { return len(t.content) }

func (t *TextValue) Hash() (uint32, error) {
	h, _ := starlark.String(t.content).Hash()

	return h, nil
}

func (t *TextValue) Index(i int) starlark.Value { return TextIndex(t, i) }

func (t *TextValue) Slice(start, end, step int) starlark.Value {
	return TextSlice(t, start, end, step)
}

func (t *TextValue) MarshalJSON() ([]byte, error) {
	encoded, err := encodeJSON(starlark.String(t.content))

	return []byte(encoded), err
}

func (t *TextValue) CompareSameType(op syntax.Token, y starlark.Value, depth int) (bool, error) {
	found, _ := starlark.String(t.content).CompareSameType(op, starlark.String(y.(*TextValue).content), depth)

	return found, nil
}

func (t *TextValue) Binary(op syntax.Token, y starlark.Value, side starlark.Side) (starlark.Value, error) {
	return TextBinary(t, op, y, side)
}

func TextIndex(t Text, i int) starlark.Value {
	return starlark.String(t.Text()).Index(i)
}

func TextSlice(t Text, start, end, step int) starlark.Value {
	return starlark.String(t.Text()).Slice(start, end, step)
}

func TextBinary(t Text, op syntax.Token, y starlark.Value, side starlark.Side) (starlark.Value, error) {
	if op != syntax.PLUS && op != syntax.STAR && op != syntax.PERCENT && op != syntax.IN {
		return nil, nil
	}

	return binaryUnboxed(op, starlark.String(t.Text()), y, side)
}

func (t *TextValue) Attr(name string) (starlark.Value, error) {
	if v, ok := ResultAttr(t, nil, name); ok {
		return v, nil
	}

	v, err := t.attrs.Attr(name)
	if err != nil {
		return nil, fmt.Errorf("text.%s: %w", name, err)
	}

	if v != nil {
		return v, nil
	}

	switch name {
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

	method, err := starlark.String(t.content).Attr(name)
	if err != nil {
		return nil, fmt.Errorf("text.%s: %w", name, err)
	}

	return method, nil
}

func (t *TextValue) AttrNames() []string {
	return ResultAttrNames(append(append(t.attrs.AttrNames(), "bytes", "lines", "search"), starlark.String("").AttrNames()...)...)
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

	var path string
	if p, errPath := t.attrs.Attr("path"); errPath == nil && p != nil {
		path, _ = starlark.AsString(p)
	}

	n := 0

	if start, errStart := t.attrs.Attr("start_line"); errStart == nil && start != nil {
		if first, errInt := starlark.AsInt32(start); errInt == nil {
			n = first - 1
		}
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
	_ Text                = (*TextValue)(nil)
	_ starlark.HasAttrs   = (*TextValue)(nil)
	_ starlark.Sliceable  = (*TextValue)(nil)
	_ starlark.HasBinary  = (*TextValue)(nil)
	_ starlark.Comparable = (*TextValue)(nil)
)
