package codemode

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"go.starlark.net/syntax"
)

func formatCases() []struct{ name, source, want string } {
	return []struct{ name, source, want string }{
		{"squeezed statements", "def f(): return 1\ndef main(): a = f(); return str(a)", `def f():
    return 1

def main():
    a = f()
    return str(a)`},
		{"long call", `def main():
    return github_create_pull_request(owner="Rocketable", repo="platform", title="show the script first", draft=False)`, `def main():
    return github_create_pull_request(
        owner="Rocketable",
        repo="platform",
        title="show the script first",
        draft=False,
    )`},
		{"single argument hugs its list", `def main():
    return gather([lambda: bash(command=r'''make test 2>&1 | tail -50'''), lambda: bash(command=r'''make lint''')])`, `def main():
    return gather([
        lambda: bash(command=r'''make test 2>&1 | tail -50'''),
        lambda: bash(command=r'''make lint'''),
    ])`},
		{"comments", `# leading
def main():
    x = [1,  # one
        # before two
        2]
    return str(x)  # trailing
# after`, `# leading
def main():
    x = [
        1,  # one
        # before two
        2,
    ]
    return str(x)  # trailing
# after`},
		{"parameter list opening comment", "def f( # note\n a): return a", `def f(  # note
    a,
):
    return a`},
		{"literals as written", "def main():\n\ti = 0\n\twhile i < 2:\n\t\ti += 1\n\treturn '''é — \\u00e9''' + r\"\\d\" + str(b'\\xff') + str(0 .real)", `def main():
    i = 0
    while i < 2:
        i += 1
    return '''é — \u00e9''' + r"\d" + str(b'\xff') + str(0 .real)`},
		{"branches", `def main():
    for i in range(3):
        if i == 0: continue
        elif i == 1: pass
        else:
            if i: break
    return "done"`, `def main():
    for i in range(3):
        if i == 0:
            continue
        elif i == 1:
            pass
        else:
            if i:
                break
    return "done"`},
		{"remaining syntax", `load("m.star", "a", b="c")
def main():
    x = y[1:2:3] + y[:] + y[::2]
    t = 1, 2
    u = (1,)
    v = (first_long_name, second_long_name, third_long_name, fourth_long_name_here)
    f = lambda a, *args, **kw: a if not args else -a
    s = [item * 2 for item in range(100) if item % 3 if item % 5 if item % 7 if item]
    w = [r'''two
lines''', 1]
    z = (x[
        # inside
        0])

    return t, u, (), {k: v for k, v in kw.items()}  # end
# after one
# after two`, `load("m.star", "a", b="c")

def main():
    x = y[1:2:3] + y[:] + y[::2]
    t = 1, 2
    u = (1,)
    v = (
        first_long_name,
        second_long_name,
        third_long_name,
        fourth_long_name_here,
    )
    f = lambda a, *args, **kw: a if not args else -a
    s = [
        item * 2
        for item in range(100)
        if item % 3
        if item % 5
        if item % 7
        if item
    ]
    w = [r'''two
lines''', 1]
    z = (
        x[0]  # inside
    )

    return t, u, (), {k: v for k, v in kw.items()}  # end
# after one
# after two`},
	}
}

func TestFormat(t *testing.T) {
	for _, tc := range formatCases() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Format(tc.source)
			if err != nil {
				t.Fatalf("Format() error = %v", err)
			}

			if got != tc.want {
				t.Fatalf("Format() =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}

	if _, err := Format("def main(:"); err == nil {
		t.Fatal("Format() of invalid source error = nil")
	}
}

func FuzzFormat(f *testing.F) {
	for _, tc := range formatCases() {
		f.Add(tc.source)
	}

	f.Fuzz(func(t *testing.T, source string) {
		original, err := sourceOptions().Parse("codemode.star", source, syntax.RetainComments)
		if err != nil {
			return
		}

		formatted, err := Format(source)
		if err != nil {
			t.Fatalf("Format() error = %v", err)
		}

		syntax.Walk(original, func(node syntax.Node) bool {
			if node == nil || node.Comments() == nil {
				return true
			}

			for _, comment := range slices.Concat(node.Comments().Before, node.Comments().Suffix, node.Comments().After) {
				if !strings.Contains(formatted, comment.Text) {
					t.Fatalf("formatted source lost comment %q:\n%s", comment.Text, formatted)
				}
			}

			return true
		})

		reparsed, err := sourceOptions().Parse("codemode.star", formatted, 0)
		if err != nil {
			t.Fatalf("formatted source does not parse: %v\n%s", err, formatted)
		}

		if got, want := syntaxTree(reparsed), syntaxTree(original); got != want {
			t.Fatalf("formatted source means something else:\n%s", formatted)
		}

		if again, _ := Format(formatted); again != formatted {
			t.Fatalf("Format() is not stable:\n%s\nthen\n%s", formatted, again)
		}
	})
}

func syntaxTree(file *syntax.File) string {
	var b strings.Builder

	syntax.Walk(file, func(node syntax.Node) bool {
		if node == nil {
			b.WriteString(")")

			return true
		}

		fmt.Fprintf(&b, "(%T", node)

		switch n := node.(type) {
		case *syntax.Ident:
			b.WriteString(" " + n.Name)
		case *syntax.Literal:
			fmt.Fprintf(&b, " %v %#v", n.Token, n.Value)
		case *syntax.UnaryExpr:
			b.WriteString(" " + n.Op.String())
		case *syntax.BinaryExpr:
			b.WriteString(" " + n.Op.String())
		case *syntax.AssignStmt:
			b.WriteString(" " + n.Op.String())
		case *syntax.BranchStmt:
			b.WriteString(" " + n.Token.String())
		case *syntax.Comprehension:
			fmt.Fprintf(&b, " %t", n.Curly)
		case *syntax.IfStmt:
			fmt.Fprintf(&b, " %d", len(n.True))
		case *syntax.SliceExpr:
			fmt.Fprintf(&b, " %t %t", n.Lo != nil, n.Hi != nil)
		case *syntax.DefStmt:
			fmt.Fprintf(&b, " %d", len(n.Params))
		case *syntax.LambdaExpr:
			fmt.Fprintf(&b, " %d", len(n.Params))
		}

		return true
	})

	return b.String()
}
