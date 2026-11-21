package codemode

import (
	"errors"
	"fmt"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

const (
	unboxBuiltin   = "$unbox"
	membersBuiltin = "$members"
)

func rewriteComparisons(f *syntax.File) {
	syntax.Walk(f, func(n syntax.Node) bool {
		e, ok := n.(*syntax.BinaryExpr)
		if !ok {
			return true
		}

		switch {
		case isComparison(e.Op):
			e.X, e.Y = wrapCall(unboxBuiltin, e.X), wrapCall(unboxBuiltin, e.Y)
		case e.Op == syntax.IN || e.Op == syntax.NOT_IN:
			e.X, e.Y = wrapCall(unboxBuiltin, e.X), wrapCall(membersBuiltin, e.Y)
		}

		return true
	})
}

func isComparison(op syntax.Token) bool {
	return op == syntax.EQL || op == syntax.NEQ || op == syntax.LT || op == syntax.LE || op == syntax.GT || op == syntax.GE
}

func wrapCall(name string, e syntax.Expr) *syntax.CallExpr {
	start, end := e.Span()

	return &syntax.CallExpr{
		Fn:     &syntax.Ident{NamePos: start, Name: name},
		Lparen: start,
		Args:   []syntax.Expr{e},
		Rparen: end,
	}
}

func comparisonBuiltins() starlark.StringDict {
	unary := func(name string, fn func(starlark.Value) starlark.Value) *starlark.Builtin {
		return starlark.NewBuiltin(name, func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
			return fn(args[0]), nil
		})
	}

	return starlark.StringDict{
		unboxBuiltin:   unary(unboxBuiltin, unbox),
		membersBuiltin: unary(membersBuiltin, func(v starlark.Value) starlark.Value { return membership{container: unbox(v)} }),
	}
}

type membership struct {
	container starlark.Value
}

func (m membership) String() string       { return m.container.String() }
func (m membership) Type() string         { return m.container.Type() }
func (m membership) Freeze()              {}
func (m membership) Truth() starlark.Bool { return m.container.Truth() }
func (m membership) Hash() (uint32, error) {
	return 0, errors.New("unhashable type: membership")
}

func (m membership) Has(needle starlark.Value) (bool, error) {
	switch c := m.container.(type) {
	case *starlark.List, starlark.Tuple:
		return containsUnboxed(c.(starlark.Iterable), needle, false)
	case *starlark.Dict, *starlark.Set:
		if IsFailure(needle) {
			return containsUnboxed(c.(starlark.Iterable), needle, true)
		}

		found, err := starlark.Binary(syntax.IN, needle, c)
		if err != nil {
			return false, fmt.Errorf("%w", err)
		}

		if found.Truth() {
			return true, nil
		}

		return containsUnboxed(c.(starlark.Iterable), needle, true)
	}

	found, err := starlark.Binary(syntax.IN, needle, m.container)
	if err != nil {
		return false, fmt.Errorf("%w", err)
	}

	return bool(found.Truth()), nil
}

func containsUnboxed(c starlark.Iterable, needle starlark.Value, boxesOnly bool) (bool, error) {
	for elem := range starlark.Elements(c) {
		unboxed, boxed := unboxValue(elem)
		if boxesOnly && !boxed || IsFailure(unboxed) {
			continue
		}

		found, err := starlark.Equal(needle, unboxed)
		if err != nil {
			return false, fmt.Errorf("%w", err)
		}

		if found {
			return true, nil
		}
	}

	return false, nil
}

var _ starlark.Container = membership{}
