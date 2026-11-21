package codemode

import (
	"errors"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func TestComparisonRewriteUnboxes(t *testing.T) {
	dict := starlark.NewDict(1)
	if err := dict.SetKey(starlark.String("a"), starlark.MakeInt(1)); err != nil {
		t.Fatal(err)
	}

	runScriptCases(t, map[string]starlark.Value{
		"t":     starlark.String("x"),
		"u":     starlark.String("y"),
		"e":     NewFailure("e", "missing", nil),
		"same":  NewFailure("e", "missing", nil),
		"other": NewFailure("o", "missing", nil),
		"gone":  NewFailure("e", "gone", nil),
		"d":     dict,
	}, []scriptCase{
		{expression: `t() == "x"`, want: "true"},
		{expression: `"x" == t()`, want: "true"},
		{expression: `t() != "x"`, want: "false"},
		{expression: `"y" != t()`, want: "true"},
		{expression: `t() == t()`, want: "true"},
		{expression: `t() == u()`, want: "false"},
		{expression: `t() < "y"`, want: "true"},
		{expression: `"y" < t()`, want: "false"},
		{expression: `"w" <= t()`, want: "true"},
		{expression: `t() > u()`, want: "false"},
		{expression: `t() >= "x"`, want: "true"},
		{expression: `"x" >= t()`, want: "true"},
		{expression: `e() == "missing"`, want: "false"},
		{expression: `"missing" != e()`, want: "true"},
		{expression: `e() == same()`, want: "true"},
		{expression: `e() == other()`, want: "false"},
		{expression: `e() != gone()`, want: "true"},
		{expression: `e() in ["missing"]`, want: "false"},
		{expression: `e() not in ("missing",)`, want: "true"},
		{expression: `e() in {"missing": 1}`, want: "false"},
		{expression: `e() in [same()]`, want: "false"},
		{expression: `e() in [other()]`, want: "false"},
		{expression: `e() in set(["missing"])`, want: "false"},
		{expression: `e() not in set(["e: missing"])`, want: "true"},
		{expression: `e() < "z"`, wantErr: "error < string not implemented"},
		{expression: `e() <= other()`, wantErr: "error <= error not implemented"},
		{expression: `"missing" in e()`, wantErr: "unknown binary op: string in error"},
		{expression: `e() in "a missing file"`, wantErr: "'in <string>' requires string as left operand, not error"},
		{expression: `d() == {"a": 1}`, want: "true"},
		{expression: `{"a": 1} != d()`, want: "false"},
		{expression: `t() in ["a", "x"]`, want: "true"},
		{expression: `"x" in [t()]`, want: "true"},
		{expression: `"x" in (u(), t())`, want: "true"},
		{expression: `t() not in ["a"]`, want: "true"},
		{expression: `"z" not in [t()]`, want: "true"},
		{expression: `t() in {"x": 1}`, want: "true"},
		{expression: `"x" in {t(): 1}`, want: "true"},
		{expression: `t() in {t(): 1}`, want: "true"},
		{expression: `"z" in {t(): 1}`, want: "false"},
		{expression: `"x" in set([t()])`, want: "true"},
		{expression: `t() in set(["x"])`, want: "true"},
		{expression: `"a" in d()`, want: "true"},
		{expression: `box(s="b") in "abc"`, want: "true"},
		{expression: `box(s="z") in "abc"`, want: "false"},
		{expression: `"b" in box(s="abc")`, want: "true"},
		{expression: `(lambda: t() == "x")()`, want: "true"},
		{expression: `[v for v in ["x", "y"] if v == t()]`, want: `["x"]`},
		{expression: `[v for v in ["x", "y"] if v in [t()]]`, want: `["x"]`},
		{expression: `1 == 1.0`, want: "true"},
		{expression: `[1, 2] == [1, 2]`, want: "true"},
		{expression: `(1, "a") < (1, "b")`, want: "true"},
		{expression: `2 in [1, 2]`, want: "true"},
		{expression: `3 not in (1, 2)`, want: "true"},
		{expression: `"a" not in "abc"`, want: "false"},
		{expression: `"k" in {"k": None}`, want: "true"},
		{expression: `[] in {}`, want: "false"},
		{expression: `1 in range(3)`, want: "true"},
		{expression: `1 in 2`, wantErr: "unknown binary op: int in int"},
		{expression: `1 in "abc"`, wantErr: "'in <string>' requires string as left operand, not int"},
		{expression: `[] in set()`, wantErr: "unhashable type: list"},
		{expression: `1 < "a"`, wantErr: "int < string not implemented"},
		{expression: `t() < 1`, wantErr: "string < int not implemented"},
	})
}

func TestComparisonRewriteWrapsEveryComparison(t *testing.T) {
	const source = `
def helper(xs, y):
    def inner(z):
        return z == y or (z != 1 and not (z < 3))
    while len(xs) > 0 and xs[0] in y:
        xs = xs[1:]
    return [x for x in xs if x not in y and (x <= 2) == (x >= 1)] + [inner(x) for x in xs]

def main():
    f = lambda a, b: a == b if a in [b] else {"k": a > b}
    return helper([1, 2], [2]) if f(1, 2) != None else None
`

	f, err := sourceOptions().Parse("codemode.star", source, 0)
	if err != nil {
		t.Fatal(err)
	}

	rewriteComparisons(f)

	wrapped := func(e syntax.Expr, name string) bool {
		call, ok := e.(*syntax.CallExpr)
		if !ok || len(call.Args) != 1 {
			return false
		}

		ident, ok := call.Fn.(*syntax.Ident)

		return ok && ident.Name == name
	}

	comparisons := 0

	syntax.Walk(f, func(n syntax.Node) bool {
		e, ok := n.(*syntax.BinaryExpr)
		if !ok {
			return true
		}

		switch {
		case isComparison(e.Op):
			comparisons++

			if !wrapped(e.X, unboxBuiltin) || !wrapped(e.Y, unboxBuiltin) {
				t.Errorf("comparison at %s is not wrapped", e.OpPos)
			}
		case e.Op == syntax.IN || e.Op == syntax.NOT_IN:
			comparisons++

			if !wrapped(e.X, unboxBuiltin) || !wrapped(e.Y, membersBuiltin) {
				t.Errorf("membership at %s is not wrapped", e.OpPos)
			}
		}

		return true
	})

	if comparisons != 13 {
		t.Fatalf("found %d comparisons, want 13", comparisons)
	}

	got, err := runToString(t.Context(), source, nil, allowAll, nilCall, nil)
	if err != nil || got != "[1,false,false]" {
		t.Fatalf("runToString() = %q, %v", got, err)
	}
}

func TestComparisonRewriteKeepsErrorPositions(t *testing.T) {
	const source = "def main():\n    x = 1\n    if x == 1:\n        return [x] + [x  <  \"a\"]\n"

	_, prog, err := starlark.SourceProgramOptions(sourceOptions(), "codemode.star", source, starlark.StringDict{}.Has)
	if err != nil {
		t.Fatal(err)
	}

	thread := &starlark.Thread{}

	globals, err := prog.Init(thread, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, errWant := starlark.Call(thread, globals["main"], nil, nil)
	_, errGot := Run(t.Context(), source, nil, allowAll, nilCall, nil)

	want, okWant := errors.AsType[*starlark.EvalError](errWant)
	got, okGot := errors.AsType[*starlark.EvalError](errGot)

	if !okWant || !okGot {
		t.Fatalf("errors = %v, %v, want evaluation errors", errWant, errGot)
	}

	if got.Msg != want.Msg || got.CallStack.At(0).Pos.String() != want.CallStack.At(0).Pos.String() || want.CallStack.At(0).Pos.String() != "codemode.star:4:26" {
		t.Fatalf("error = %q at %s, want %q at %s", got.Msg, got.CallStack.At(0).Pos, want.Msg, want.CallStack.At(0).Pos)
	}
}

func TestComparisonRewriteNamesAreNotScriptNames(t *testing.T) {
	for _, source := range []string{
		"def main():\n    return $unbox(1)\n",
		"def main():\n    $unbox = 1\n    return 1\n",
		"def main():\n    return " + membersBuiltin + "(1)\n",
	} {
		if _, err := Run(t.Context(), source, nil, allowAll, nilCall, nil); err == nil {
			t.Fatalf("Run(%q) succeeded, want a parse error", source)
		}
	}
}
