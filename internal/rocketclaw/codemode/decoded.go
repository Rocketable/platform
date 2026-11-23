package codemode

import (
	"fmt"

	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

type decodedDict struct {
	*starlark.Dict

	kind string
}

type decodedList struct {
	*starlark.List

	kind string
}

func decodeJSON(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	text := false

	if len(args) > 0 {
		if IsFailure(args[0]) {
			return args[0], nil
		}

		value, boxed := unboxValue(args[0])
		if boxed {
			args = append(starlark.Tuple{value}, args[1:]...)
		}

		_, text = value.(starlark.String)
	}

	decoded, err := starlark.Call(thread, starjson.Module.Members["decode"], args, kwargs)
	switch {
	case err != nil && text:
		return NewFailure("json.decode", err.Error(), nil), nil
	case err != nil:
		return nil, fmt.Errorf("%w", err)
	}

	switch v := decoded.(type) {
	case starlark.String:
		return NewText("text", string(v), nil, nil), nil
	case *starlark.Dict:
		return decodedDict{v, "decoded_dict"}, nil
	case *starlark.List:
		return decodedList{v, "decoded_list"}, nil
	}

	return decoded, nil
}

func Decoded(v starlark.Value) starlark.Value {
	switch v := v.(type) {
	case decodedDict:
		return v.Dict
	case decodedList:
		return v.List
	}

	return v
}

func (d decodedDict) Type() string { return d.kind }
func (d decodedDict) Ok() bool     { return true }

func (d decodedDict) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: %s", d.kind)
}

func (d decodedDict) Attr(name string) (starlark.Value, error) { return decodedAttr(d, d.Dict, name) }

func (d decodedDict) AttrNames() []string { return ResultAttrNames(d.Dict.AttrNames()...) }

func (d decodedDict) CompareSameType(op syntax.Token, y starlark.Value, depth int) (bool, error) {
	return compareDecoded(op, d.Dict, y, depth)
}

func (d decodedDict) Binary(op syntax.Token, y starlark.Value, side starlark.Side) (starlark.Value, error) {
	return binaryUnboxed(op, d.Dict, y, side)
}

func (l decodedList) Type() string { return l.kind }
func (l decodedList) Ok() bool     { return true }

func (l decodedList) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: %s", l.kind)
}

func (l decodedList) Attr(name string) (starlark.Value, error) { return decodedAttr(l, l.List, name) }

func (l decodedList) AttrNames() []string { return ResultAttrNames(l.List.AttrNames()...) }

func (l decodedList) CompareSameType(op syntax.Token, y starlark.Value, depth int) (bool, error) {
	return compareDecoded(op, l.List, y, depth)
}

func (l decodedList) Binary(op syntax.Token, y starlark.Value, side starlark.Side) (starlark.Value, error) {
	return binaryUnboxed(op, l.List, y, side)
}

func decodedAttr(v starlark.Value, inner starlark.HasAttrs, name string) (starlark.Value, error) {
	if attr, ok := ResultAttr(v, nil, name); ok {
		return attr, nil
	}

	method, err := inner.Attr(name)
	if err != nil {
		return nil, fmt.Errorf("%w", err)
	}

	return method, nil
}

func compareDecoded(op syntax.Token, x, y starlark.Value, depth int) (bool, error) {
	found, err := starlark.CompareDepth(op, x, unbox(y), depth)
	if err != nil {
		return false, fmt.Errorf("%w", err)
	}

	return found, nil
}

func binaryUnboxed(op syntax.Token, x, y starlark.Value, side starlark.Side) (starlark.Value, error) {
	y = unbox(y)
	if side == starlark.Right {
		x, y = y, x
	}

	z, err := starlark.Binary(op, x, y)
	if err != nil {
		return nil, fmt.Errorf("%w", err)
	}

	return z, nil
}

var (
	_ Result                   = decodedDict{}
	_ starlark.IterableMapping = decodedDict{}
	_ starlark.HasSetKey       = decodedDict{}
	_ starlark.HasBinary       = decodedDict{}
	_ starlark.Comparable      = decodedDict{}
	_ Result                   = decodedList{}
	_ starlark.Sliceable       = decodedList{}
	_ starlark.HasSetIndex     = decodedList{}
	_ starlark.HasBinary       = decodedList{}
	_ starlark.Comparable      = decodedList{}
)
