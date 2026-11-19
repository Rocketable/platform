package codemode

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"go.starlark.net/syntax"
)

const formatWidth = 80

type closing int

const (
	commaWhenBroken closing = iota
	commaWhenSingle
	noComma
)

type printer struct {
	out     strings.Builder
	indent  int
	col     int
	atStart bool
	pending []string
	flat    bool
	stop    bool
	comment bool
}

func Format(source string) (string, error) {
	f, err := sourceOptions().Parse("codemode.star", source, syntax.RetainComments)
	if err != nil {
		return "", fmt.Errorf("parse codemode source: %w", err)
	}

	p := printer{atStart: true}
	p.stmts(f.Stmts)

	if c := f.Comments(); c != nil {
		for _, line := range c.After {
			if p.out.Len() > 0 {
				p.newline()
			}

			p.write(line.Text)
		}
	}

	if len(p.pending) > 0 {
		p.newline()
	}

	return strings.TrimSuffix(p.out.String(), "\n"), nil
}

func (p *printer) write(s string) {
	if p.flat {
		if p.stop {
			return
		}

		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s, p.stop = s[:i], true
		}

		p.col += utf8.RuneCountInString(s)
		if p.col > formatWidth {
			p.stop = true
		}

		return
	}

	if p.atStart {
		p.out.WriteString(strings.Repeat("    ", p.indent))
		p.col, p.atStart = 4*p.indent, false
	}

	p.out.WriteString(s)

	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		p.col = utf8.RuneCountInString(s[i+1:])
	} else {
		p.col += utf8.RuneCountInString(s)
	}
}

func (p *printer) newline() {
	if len(p.pending) > 0 {
		p.write("  " + strings.Join(p.pending, "  "))
		p.pending = nil
	}

	p.out.WriteByte('\n')
	p.col, p.atStart = 0, true
}

func (p *printer) before(n syntax.Node) {
	c := n.Comments()
	if c == nil || len(c.Before) == 0 {
		return
	}

	if p.flat {
		p.comment, p.stop = true, true

		return
	}

	for _, line := range c.Before {
		if !p.atStart {
			p.pending = append(p.pending, line.Text)

			continue
		}

		p.write(line.Text)
		p.newline()
	}
}

func (p *printer) suffix(n syntax.Node) {
	c := n.Comments()
	if c == nil || len(c.Suffix) == 0 {
		return
	}

	if p.flat {
		p.comment, p.stop = true, true

		return
	}

	for _, line := range c.Suffix {
		p.pending = append(p.pending, line.Text)
	}
}

func (p *printer) measure(render func(*printer)) (wide, comment bool) {
	if p.flat {
		return false, false
	}

	q := printer{flat: true, col: p.col}
	if p.atStart {
		q.col = 4 * p.indent
	}

	render(&q)

	return q.col > formatWidth, q.comment
}

func (p *printer) stmts(list []syntax.Stmt) {
	for i, stmt := range list {
		if i > 0 {
			p.newline()

			start, _ := stmt.Span()
			if c := stmt.Comments(); c != nil && len(c.Before) > 0 {
				start = c.Before[0].Start
			}

			_, isDef := stmt.(*syntax.DefStmt)
			_, wasDef := list[i-1].(*syntax.DefStmt)

			if start.Line-syntax.End(list[i-1]).Line > 1 || p.indent == 0 && (isDef || wasDef) {
				p.newline()
			}
		}

		p.node(stmt)
	}
}

func (p *printer) body(list []syntax.Stmt) {
	p.write(":")
	p.indent++
	p.newline()
	p.stmts(list)
	p.indent--
}

func (p *printer) list(left string, items []syntax.Expr, right string, kind closing) {
	wide, comment := p.measure(func(q *printer) { q.list(left, items, right, kind) })
	broken := wide && len(items) > 1 || comment

	p.write(left)

	if !broken {
		for i, item := range items {
			if i > 0 {
				p.write(", ")
			}

			p.node(item)
		}

		if kind == commaWhenSingle && len(items) == 1 {
			p.write(",")
		}

		p.write(right)

		return
	}

	p.indent++
	for _, item := range items {
		p.newline()
		p.node(item)

		if kind != noComma {
			p.write(",")
		}
	}

	p.indent--
	p.newline()
	p.write(right)
}

func (p *printer) ifStmt(stmt *syntax.IfStmt, keyword string) {
	p.write(keyword)
	p.node(stmt.Cond)
	p.body(stmt.True)

	if len(stmt.False) == 0 {
		return
	}

	p.newline()

	if elif, ok := stmt.False[0].(*syntax.IfStmt); ok && len(stmt.False) == 1 && elif.If == stmt.ElsePos {
		p.before(elif)
		p.ifStmt(elif, "elif ")
		p.suffix(elif)

		return
	}

	p.write("else")
	p.body(stmt.False)
}

func (p *printer) node(n syntax.Node) {
	if n == nil || p.stop {
		return
	}

	p.before(n)

	switch n := n.(type) {
	case syntax.Stmt:
		p.stmt(n)
	case syntax.Expr:
		p.expr(n)
	}

	p.suffix(n)
}

func (p *printer) stmt(stmt syntax.Stmt) {
	switch n := stmt.(type) {
	case *syntax.ExprStmt:
		p.node(n.X)
	case *syntax.AssignStmt:
		p.node(n.LHS)
		p.write(" " + n.Op.String() + " ")
		p.node(n.RHS)
	case *syntax.ReturnStmt:
		p.write("return")

		if n.Result != nil {
			p.write(" ")
			p.node(n.Result)
		}
	case *syntax.BranchStmt:
		p.write(n.Token.String())
	case *syntax.LoadStmt:
		p.write("load(")
		p.node(n.Module)

		for i, from := range n.From {
			p.write(", ")

			if to := n.To[i]; to != from {
				p.node(to)
				p.write("=")
			}

			p.before(from)
			p.write(syntax.Quote(from.Name, false))
			p.suffix(from)
		}

		p.write(")")
	case *syntax.DefStmt:
		p.write("def ")
		p.node(n.Name)
		p.list("(", n.Params, ")", commaWhenBroken)
		p.body(n.Body)
	case *syntax.ForStmt:
		p.write("for ")
		p.node(n.Vars)
		p.write(" in ")
		p.node(n.X)
		p.body(n.Body)
	case *syntax.WhileStmt:
		p.write("while ")
		p.node(n.Cond)
		p.body(n.Body)
	case *syntax.IfStmt:
		p.ifStmt(n, "if ")
	}
}

func (p *printer) expr(expr syntax.Expr) {
	switch n := expr.(type) {
	case *syntax.Ident:
		p.write(n.Name)
	case *syntax.Literal:
		p.write(n.Raw)
	case *syntax.ParenExpr:
		p.paren(n)
	case *syntax.UnaryExpr:
		p.write(n.Op.String())

		if n.Op == syntax.NOT {
			p.write(" ")
		}

		p.node(n.X)
	case *syntax.BinaryExpr:
		p.node(n.X)

		if n.Op == syntax.EQ {
			p.write("=")
		} else {
			p.write(" " + n.Op.String() + " ")
		}

		p.node(n.Y)
	case *syntax.CallExpr:
		p.node(n.Fn)
		p.list("(", n.Args, ")", commaWhenBroken)
	case *syntax.DotExpr:
		p.node(n.X)

		if x, ok := n.X.(*syntax.Literal); ok && x.Token == syntax.INT {
			p.write(" ")
		}

		p.write(".")
		p.node(n.Name)
	case *syntax.IndexExpr:
		p.node(n.X)
		p.write("[")
		p.node(n.Y)
		p.write("]")
	case *syntax.SliceExpr:
		p.node(n.X)
		p.write("[")
		p.node(n.Lo)
		p.write(":")
		p.node(n.Hi)

		if n.Step != nil {
			p.write(":")
			p.node(n.Step)
		}

		p.write("]")
	case *syntax.ListExpr:
		p.list("[", n.List, "]", commaWhenBroken)
	case *syntax.DictExpr:
		p.list("{", n.List, "}", commaWhenBroken)
	case *syntax.DictEntry:
		p.node(n.Key)
		p.write(": ")
		p.node(n.Value)
	case *syntax.TupleExpr:
		p.tuple(n)
	case *syntax.CondExpr:
		p.node(n.True)
		p.write(" if ")
		p.node(n.Cond)
		p.write(" else ")
		p.node(n.False)
	case *syntax.LambdaExpr:
		p.write("lambda")

		for i, param := range n.Params {
			if i == 0 {
				p.write(" ")
			} else {
				p.write(", ")
			}

			p.node(param)
		}

		p.write(": ")
		p.node(n.Body)
	case *syntax.Comprehension:
		p.comprehension(n)
	}
}

func (p *printer) paren(n *syntax.ParenExpr) {
	tuple, ok := n.X.(*syntax.TupleExpr)
	if !ok || tuple.Lparen.IsValid() {
		p.list("(", []syntax.Expr{n.X}, ")", noComma)

		return
	}

	p.before(tuple)
	p.list("(", tuple.List, ")", commaWhenSingle)
	p.suffix(tuple)
}

func (p *printer) tuple(n *syntax.TupleExpr) {
	if n.Lparen.IsValid() {
		p.write("()")

		return
	}

	for i, item := range n.List {
		if i > 0 {
			p.write(", ")
		}

		p.node(item)
	}
}

func (p *printer) comprehension(n *syntax.Comprehension) {
	left, right := "[", "]"
	if n.Curly {
		left, right = "{", "}"
	}

	wide, comment := p.measure(func(q *printer) { q.comprehension(n) })
	broken := wide || comment

	p.write(left)

	if broken {
		p.indent++
		p.newline()
	}

	p.node(n.Body)

	for _, clause := range n.Clauses {
		if broken {
			p.newline()
		} else {
			p.write(" ")
		}

		p.before(clause)

		switch c := clause.(type) {
		case *syntax.ForClause:
			p.write("for ")
			p.node(c.Vars)
			p.write(" in ")
			p.node(c.X)
		case *syntax.IfClause:
			p.write("if ")
			p.node(c.Cond)
		}

		p.suffix(clause)
	}

	if broken {
		p.indent--
		p.newline()
	}

	p.write(right)
}
