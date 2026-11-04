package format

import (
	"ergo.local/prototype/internal/sketch/syntax"
)

// ---------------------------------------------------------------- file and declarations

func (p *printer) printFile(f *syntax.File) {
	for i, d := range f.Decls {
		if i > 0 {
			p.newline()
		}
		p.decl(d)
	}
}

func (p *printer) ident(x *syntax.Ident) { p.token(x.NamePos, x.Name) }

// tok prints a token at pos, or a synthesized one when the tree has no
// position for it.
func (p *printer) tok(pos syntax.Pos, text string) {
	if pos.IsValid() {
		p.token(pos, text)
		return
	}
	p.synth(text)
}

func (p *printer) decl(d syntax.Decl) {
	switch d := d.(type) {
	case *syntax.ImportDecl:
		p.importDecl(d)
	case *syntax.VarDecl:
		p.letDecl(d)
	case *syntax.TypeDecl:
		p.token(d.Type, "type")
		p.space()
		p.ident(d.Name)
		if d.TypeParams != nil {
			p.fieldList(d.TypeParams, "[", "]")
		}
		p.space()
		switch t := d.Spec.(type) {
		case *syntax.InterfaceType:
			if t.Union != nil {
				p.union(t)
				return
			}
		}
		p.expr(d.Spec)
	case *syntax.FuncDecl:
		if x := d.Extern; x != nil {
			p.token(x.Extern, "extern")
			p.space()
			p.token(x.Go, "go")
			p.space()
			p.basicLit(x.Target)
			p.space()
		}
		p.token(d.Func, "func")
		p.space()
		p.ident(d.Name)
		if d.TypeParams != nil {
			p.fieldList(d.TypeParams, "[", "]")
		}
		p.signature(d.Type)
		if d.Body != nil {
			if p.blockOneLine(d.Body) {
				p.sep() // consecutive one-line functions align their bodies (gofmt)
			} else {
				p.space()
			}
			p.block(d.Body, true)
		}
	case *syntax.ContextDecl:
		p.tok(d.Let, "let")
		p.space()
		p.ident(d.Name)
		p.space()
		p.synth("=")
		p.space()
		p.context(d.Context, d.Provides, d.Needs, d.Body)
	case *syntax.TestDecl:
		p.token(d.Test, "test")
		p.space()
		p.ident(d.Name)
		if d.Params != nil {
			p.fieldList(d.Params, "(", ")")
		}
		p.space()
		p.block(d.Body, true)
	case *syntax.LawDecl:
		// A top-level law prints as a top-level test does (law-top).
		p.token(d.Law, "law")
		p.space()
		p.ident(d.Name)
		p.fieldList(d.Params, "(", ")")
		p.space()
		p.block(d.Body, true)
	}
}

// closeBody ends a multi-line body: comments before the closer at the
// body's indentation, then the closer on its own line.
func (p *printer) closeBody(closePos syntax.Pos, close string) {
	p.newline()
	p.flushComments(closePos)
	p.indent--
	p.newlineNoBlank()
	p.token(closePos, close)
}

// importDecl prints `import [go] [alias] path`.
func (p *printer) importDecl(d *syntax.ImportDecl) {
	p.token(d.Import, "import")
	s := d.Specs[0]
	p.space()
	if s.Kind == syntax.ImportGo {
		p.token(s.NamePos, "go")
		p.space()
	}
	if s.Name != nil {
		p.ident(s.Name)
		p.space()
	}
	p.basicLit(s.Path)
}

// letDecl prints `let [mut] patterns [Type] [= value]`.
func (p *printer) letDecl(d *syntax.VarDecl) {
	p.token(d.Var, "let")
	p.space()
	if d.Mut.IsValid() {
		p.token(d.Mut, "mut")
		p.space()
	}
	s := d.Specs[0]
	if s.Pattern != nil {
		p.expr(s.Pattern)
	} else {
		p.identList(s.Names, true)
	}
	if s.Type != nil {
		p.space()
		p.typ(s.Type)
	}
	if len(s.Values) > 0 {
		p.space()
		p.token(s.Assign, "=")
		p.assignRHS(s.Values)
	}
}

// identList prints grouped names; a line break after a comma follows the
// source. With indent, the names after the break take one continuation
// level; without it they take the level the enclosing list or block gives,
// as go/printer's identList(…, indent).
func (p *printer) identList(list []*syntax.Ident, indent bool) {
	broken := false
	for i, x := range list {
		if i > 0 {
			p.synth(",")
			if p.gap(x.Pos(), true) && !broken && indent {
				broken = true
				p.indent++
			}
		}
		p.ident(x)
	}
	if broken {
		p.indent--
	}
}

// assignRHS prints the right side of an assignment or let after its
// operator; a line break after the operator follows the source.
func (p *printer) assignRHS(values []syntax.Expr) {
	broken := p.gap(values[0].Pos(), true)
	if broken {
		p.indent++
	}
	p.exprList(values, 1)
	if broken {
		p.indent--
	}
}

// ---------------------------------------------------------------- lists

// list prints n comma-separated items between an opener and a closer. The
// line breaks after the opener, between items and before the closer follow
// the source; with any of them, the items indent one level and a closer on
// its own line follows a comma.
func (p *printer) list(openPos syntax.Pos, open string, items []syntax.Node, item func(i int), closePos syntax.Pos, close string) {
	p.tok(openPos, open)
	n := len(items)
	if n == 0 {
		p.tok(closePos, close)
		return
	}
	// The items indent at the list's first line break, not before it, as
	// go/printer's exprList and parameters do.
	indented := false
	savedIndented := p.listIndented
	for i := range items {
		if i == 0 {
			if p.gap(items[0].Pos(), false) {
				p.noBlank = true
				p.indent++
				indented = true
			}
		} else {
			p.synth(",")
			if p.gap(items[i].Pos(), true) && !indented {
				p.indent++
				indented = true
			}
		}
		p.listIndented = indented
		item(i)
	}
	p.listIndented = savedIndented
	if closePos.IsValid() && p.breakBefore(closePos) {
		if !indented {
			p.indent++
		}
		p.synth(",")
		p.closeBody(closePos, close)
		return
	}
	if indented {
		p.indent--
	}
	p.tok(closePos, close)
}

// exprList prints an unbracketed expression list; a line break after a
// comma follows the source and indents the rest one level.
func (p *printer) exprList(list []syntax.Expr, depth int) {
	broken := false
	for i, x := range list {
		if i > 0 {
			p.synth(",")
			if p.gap(x.Pos(), true) && !broken {
				broken = true
				p.indent++
			}
		}
		p.expr1(x, depth)
	}
	if broken {
		p.indent--
	}
}

func nodes[T syntax.Node](list []T) []syntax.Node {
	out := make([]syntax.Node, len(list))
	for i, x := range list {
		out[i] = x
	}
	return out
}

// ---------------------------------------------------------------- signatures and fields

// fieldList prints a parameter or type parameter list.
func (p *printer) fieldList(l *syntax.FieldList, open, close string) {
	p.list(l.Opening, open, nodes(l.List), func(i int) {
		f := l.List[i]
		p.identList(f.Names, !p.listIndented)
		if f.Type != nil {
			if len(f.Names) > 0 {
				p.space()
			}
			p.typ(f.Type)
		}
	}, l.Closing, close)
}

// signature prints `(params) [Result] [fails …] [needs …]`.
func (p *printer) signature(t *syntax.FuncType) {
	p.fieldList(t.Params, "(", ")")
	if r := t.Results; r != nil {
		for _, f := range r.List {
			p.space()
			p.typ(f.Type)
		}
	}
	if t.Needs != nil {
		p.space()
		p.needs(t.Needs)
	}
}

func (p *printer) needs(u *syntax.NeedsClause) {
	p.token(u.Needs, "needs")
	p.row(u.Names)
}

// row prints the names of an error row or a capability row, ` A | B`: both
// rows are unions and are spelled alike (196n).
func (p *printer) row(names []syntax.Expr) {
	for i, x := range names {
		p.space()
		if i > 0 {
			p.synth("|")
			p.space()
		}
		p.typ(x)
	}
}

// ---------------------------------------------------------------- types

func (p *printer) typ(x syntax.Expr) { p.expr1(x, 1) }

// member is one member of a braced type body, in source order.
type member struct {
	node  syntax.Node
	print func(aligned bool)
}

// members prints `{ m, m }` on one line when the source has the braces on
// one line and no member prints on several; otherwise one member per line
// with a trailing comma, field types aligned in a column (gofmt's struct
// rule). tight drops the blanks inside one-line braces (a union variant).
func (p *printer) members(lbrace, rbrace syntax.Pos, list []member, tight bool) {
	oneLine := p.line(lbrace) == p.line(rbrace)
	for _, m := range list {
		oneLine = oneLine && !forcesBreak(m.node)
	}
	if oneLine {
		p.token(lbrace, "{")
		for i, m := range list {
			if i > 0 {
				p.synth(",")
				p.space()
			} else if !tight {
				p.space()
			}
			m.print(false)
		}
		if len(list) > 0 && !tight {
			p.space()
		}
		p.token(rbrace, "}")
		return
	}
	p.openBody(lbrace)
	for i, m := range list {
		p.bodyLine(i)
		m.print(true)
		p.synth(",")
	}
	p.closeBody(rbrace, "}")
}

func (p *printer) fieldMember(f *syntax.Field) member {
	return member{f, func(aligned bool) {
		p.identList(f.Names, false)
		if aligned {
			p.sep()
		} else {
			p.space()
		}
		p.typ(f.Type)
	}}
}

func (p *printer) fieldMembers(fl *syntax.FieldList) []member {
	var list []member
	for _, f := range fl.List {
		list = append(list, p.fieldMember(f))
	}
	return list
}

func (p *printer) structType(t *syntax.StructType) {
	p.members(t.Fields.Opening, t.Fields.Closing, p.fieldMembers(t.Fields), false)
}

// operationType prints the operations and laws of an operation type in
// source order.
func (p *printer) operationType(t *syntax.InterfaceType) {
	var list []member
	i, j := 0, 0
	for i < len(t.Methods) || j < len(t.Laws) {
		if j == len(t.Laws) || i < len(t.Methods) && t.Methods[i].Pos() < t.Laws[j].Pos() {
			m := t.Methods[i]
			list = append(list, member{m, func(bool) {
				p.ident(m.Names[0])
				p.signature(m.Type.(*syntax.FuncType))
			}})
			i++
			continue
		}
		l := t.Laws[j]
		list = append(list, member{l, func(bool) {
			p.token(l.Law, "law")
			p.space()
			p.ident(l.Name)
			p.fieldList(l.Params, "(", ")")
			p.space()
			p.lawBody(l.Body)
		}})
		j++
	}
	p.members(t.Lbrace, t.Rbrace, list, false)
}

// lawBody prints a law's block. A law is usually one equation after its
// setup, so a body written on one line stays on one line with its
// statements separated by `;` when nothing in it breaks.
func (p *printer) lawBody(b *syntax.BlockStmt) {
	list := b.List
	oneLine := p.line(b.Lbrace) == p.line(b.Rbrace)
	for _, s := range list {
		oneLine = oneLine && !forcesBreak(s)
	}
	if !oneLine || len(list) < 2 {
		p.block(b, true)
		return
	}
	p.token(b.Lbrace, "{")
	for i, s := range list {
		if i > 0 {
			p.synth(";")
		}
		p.space()
		p.stmt(s)
	}
	p.space()
	p.token(b.Rbrace, "}")
}

// union prints `= A | B{…}`: on one line when the variants are on one line
// in the source, else one variant per line, each after a leading `|`
// (#196, D4).
func (p *printer) union(t *syntax.InterfaceType) {
	p.synth("=")
	first := t.Union[0]
	multi := false
	for _, v := range t.Union {
		multi = multi || p.line(v.Pos()) != p.line(first.Pos()) || p.line(v.End()) != p.line(v.Pos())
	}
	if !multi {
		for i, v := range t.Union {
			p.space()
			if i > 0 {
				p.synth("|")
				p.space()
			}
			p.variant(v)
		}
		return
	}
	p.indent++
	for _, v := range t.Union {
		p.newlineNoBlank()
		p.synth("|")
		p.space()
		p.variant(v)
	}
	p.indent--
}

func (p *printer) variant(x syntax.Expr) {
	v, ok := x.(*syntax.VariantSpec)
	if !ok {
		p.typ(x)
		return
	}
	p.ident(v.Name)
	if v.TypeArgs != nil {
		p.fieldList(v.TypeArgs, "[", "]")
	}
	p.members(v.Fields.Opening, v.Fields.Closing, p.fieldMembers(v.Fields), true)
}

func (p *printer) machineType(t *syntax.MachineType) {
	p.token(t.Machine, "machine")
	p.list(t.Lbrack, "[", []syntax.Node{t.State, t.Signal}, func(i int) {
		p.typ([]syntax.Expr{t.State, t.Signal}[i])
	}, t.Rbrack, "]")
	p.space()
	var list []member
	for _, e := range t.Entries {
		list = append(list, member{e, func(aligned bool) {
			p.token(e.NamePos, e.Name)
			p.tok(e.Colon, ":")
			if aligned {
				p.sep()
			} else {
				p.space()
			}
			p.expr(e.Value)
		}})
	}
	p.members(t.Lbrace, t.Rbrace, list, false)
}

// context prints `context [provides …] [needs …] { entries }`.
func (p *printer) context(pos syntax.Pos, provides *syntax.ProvidesClause, needs *syntax.NeedsClause, b *syntax.ContextBody) {
	p.token(pos, "context")
	if provides != nil {
		p.space()
		p.token(provides.Provides, "provides")
		p.row(provides.Names)
	}
	if needs != nil {
		p.space()
		p.needs(needs)
	}
	p.space()
	var list []member
	for _, e := range b.Entries {
		list = append(list, member{e, func(aligned bool) {
			if e.Kind == syntax.ContextReplace {
				p.token(e.Replace, "replace")
				p.space()
			}
			p.typ(e.Key)
			for _, k := range e.Keys {
				p.synth(",")
				p.space()
				p.typ(k)
			}
			if e.Kind == syntax.ContextEmbed {
				return
			}
			p.tok(e.Assign, ":")
			if aligned {
				p.sep()
			} else {
				p.space()
			}
			p.expr(e.Value)
		}})
	}
	p.members(b.Lbrace, b.Rbrace, list, false)
}

// oneLine reports whether a braced body stays on one line: the source has
// it on one line with at most one element (an explicit `;` between two
// elements becomes a line break), and nothing inside it prints on several
// lines.
func (p *printer) oneLine(lbrace, rbrace syntax.Pos, contents ...syntax.Node) bool {
	if p.line(lbrace) != p.line(rbrace) || len(contents) > 1 {
		return false
	}
	for _, n := range contents {
		if forcesBreak(n) {
			return false
		}
	}
	return true
}

// forcesBreak reports whether n prints on several lines even when its
// source is on one line: clauses (match, catch) and statement blocks with
// more than one statement.
func forcesBreak(n syntax.Node) bool {
	forced := false
	syntax.Inspect(n, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.SwitchStmt, *syntax.TypeSwitchStmt, *syntax.CatchExpr:
			forced = true
		case *syntax.BlockStmt:
			forced = forced || len(n.List) > 1
		}
		return !forced
	})
	return forced
}

func (p *printer) openBody(lbrace syntax.Pos) {
	p.token(lbrace, "{")
	p.indent++
	p.noBlank = true
}

func (p *printer) bodyLine(i int) {
	if i == 0 {
		p.newlineNoBlank()
	} else {
		p.newline()
	}
}

// ---------------------------------------------------------------- statements

// block prints a braced statement list. With oneLineOK, a block the source
// has on one line with at most one statement stays on one line (gofmt's
// function-body rule).
func (p *printer) block(b *syntax.BlockStmt, oneLineOK bool) {
	list := b.List
	if oneLineOK && p.oneLine(b.Lbrace, b.Rbrace, nodes(list)...) {
		p.token(b.Lbrace, "{")
		if len(list) == 1 {
			p.space()
			p.stmt(list[0])
			p.space()
		}
		p.token(b.Rbrace, "}")
		return
	}
	p.openBody(b.Lbrace)
	p.stmtList(list)
	p.closeBody(b.Rbrace, "}")
}

// blockOneLine reports whether a function-body-like block prints on one line.
func (p *printer) blockOneLine(b *syntax.BlockStmt) bool {
	return p.oneLine(b.Lbrace, b.Rbrace, nodes(b.List)...)
}

// indexDepth is the depth of an index element: gofmt's depth+1, except for
// a union of types, which keeps its blanks as a failure row does.
func indexDepth(x syntax.Expr, depth int) int {
	if b, ok := x.(*syntax.BinaryExpr); ok && b.Op == syntax.OR {
		return 1
	}
	return depth + 1
}

func (p *printer) stmtList(list []syntax.Stmt) {
	for i, s := range list {
		p.bodyLine(i)
		p.stmt(s)
	}
}

// sameLine reports whether the source has n on one line: an if or for
// written on one line keeps its one-statement blocks on that line.
func (p *printer) sameLine(n syntax.Node) bool { return p.line(n.Pos()) == p.line(n.End()) }

func (p *printer) stmt(s syntax.Stmt) {
	switch s := s.(type) {
	case *syntax.ExprStmt:
		p.expr(s.X)
	case *syntax.DeclStmt:
		p.letDecl(s.Decl)
	case *syntax.AssignStmt:
		p.exprList(s.Lhs, 1)
		p.space()
		p.token(s.TokPos, s.Tok.String())
		p.assignRHS(s.Rhs)
	case *syntax.ReturnStmt:
		p.token(s.Return, "return")
		if len(s.Results) > 0 {
			p.space()
			p.exprList(s.Results, 1)
		}
	case *syntax.BranchStmt:
		p.token(s.TokPos, s.Tok.String())
	case *syntax.AssertStmt:
		p.token(s.Assert, "assert")
		p.space()
		p.expr(s.X)
	case *syntax.BlockStmt:
		p.block(s, false)
	case *syntax.IfStmt:
		p.ifStmt(s, p.sameLine(s))
	case *syntax.SwitchStmt:
		p.token(s.Switch, "match")
		p.space()
		p.expr(s.Tag)
		p.space()
		p.caseClauses(s.Lbrace, s.Clauses, s.Rbrace)
	case *syntax.TypeSwitchStmt:
		p.token(s.Switch, "match")
		p.space()
		if len(s.Subjects) == 1 {
			p.expr(s.Subjects[0].X)
		} else {
			p.synth("(")
			for i, g := range s.Subjects {
				if i > 0 {
					p.synth(",")
					p.space()
				}
				p.expr(g.X)
			}
			p.synth(")")
		}
		p.space()
		p.caseClauses(s.Lbrace, s.Clauses, s.Rbrace)
	case *syntax.ForStmt:
		p.token(s.For, "for")
		if s.Cond != nil {
			p.space()
			p.expr(s.Cond)
		}
		p.space()
		p.block(s.Body, p.sameLine(s))
	case *syntax.RangeStmt:
		p.token(s.For, "for")
		p.space()
		if s.Key != nil {
			p.expr(s.Key)
			p.synth(",")
			p.space()
		}
		p.expr(s.Value)
		p.space()
		p.token(s.Range, "in")
		p.space()
		p.expr(s.X)
		p.space()
		p.block(s.Body, p.sameLine(s))
	}
}

func (p *printer) ifStmt(s *syntax.IfStmt, oneLineOK bool) {
	p.token(s.If, "if")
	p.space()
	p.expr(s.Cond)
	p.space()
	p.block(s.Body, oneLineOK)
	if s.Else != nil {
		p.space()
		p.token(s.ElsePos, "else")
		p.space()
		if e, ok := s.Else.(*syntax.IfStmt); ok {
			p.ifStmt(e, oneLineOK)
		} else {
			p.block(s.Else.(*syntax.BlockStmt), oneLineOK)
		}
	}
}

// clauses prints the clause list of a match or catch: each case at the
// statement's indentation, its body one level in.
func (p *printer) clauses(lbrace syntax.Pos, n int, casePos func(i int) syntax.Pos, head func(i int), body func(i int) []syntax.Stmt, rbrace syntax.Pos) {
	base := p.indent
	p.token(lbrace, "{")
	p.noBlank = true
	for i := 0; i < n; i++ {
		if i > 0 {
			p.newline()
			p.flushClauseComments(casePos(i))
			p.indent = base
			p.newline()
		} else {
			p.newlineNoBlank()
		}
		head(i)
		p.indent = base + 1
		p.stmtList(body(i))
	}
	if n > 0 {
		p.newline()
		p.flushClauseComments(rbrace)
	}
	p.indent = base
	p.newline()
	p.flushComments(rbrace)
	p.newlineNoBlank()
	p.token(rbrace, "}")
}

// caseHead prints `case P, Q:`, or `case _:` for an empty list.
func (p *printer) caseHead(pos syntax.Pos, list []syntax.Expr, colon syntax.Pos) {
	p.token(pos, "case")
	p.space()
	if list == nil {
		p.synth("_")
	} else {
		p.exprList(list, 1)
	}
	p.token(colon, ":")
}

func (p *printer) caseClauses(lbrace syntax.Pos, list []*syntax.CaseClause, rbrace syntax.Pos) {
	p.clauses(lbrace, len(list), func(i int) syntax.Pos { return list[i].Case }, func(i int) {
		p.caseHead(list[i].Case, list[i].List, list[i].Colon)
	}, func(i int) []syntax.Stmt { return list[i].Body }, rbrace)
}

// ---------------------------------------------------------------- expressions

func (p *printer) expr(x syntax.Expr) { p.expr1(x, 1) }

func (p *printer) basicLit(x *syntax.BasicLit) { p.tokenEnd(x.ValuePos, x.Value, x.ValueEnd) }

func (p *printer) expr1(x syntax.Expr, depth int) {
	switch x := x.(type) {
	case *syntax.Ident:
		p.ident(x)
	case *syntax.BasicLit:
		p.basicLit(x)
	case *syntax.InterpolatedString:
		for i, t := range x.Texts {
			p.basicLit(t)
			if i < len(x.Exprs) {
				p.expr(x.Exprs[i])
			}
		}
	case *syntax.BinaryExpr:
		if x.Op == syntax.DOTDOT {
			// `a..b` is written without blanks, its bounds as operands
			// of a nested binary expression (Rust's rustfmt: `0..n`).
			p.binaryOperand(x.X, depth+1)
			p.token(x.OpPos, "..")
			p.binaryOperand(x.Y, depth+1)
			return
		}
		// A line break after an operator indents the rest of the binary
		// expression once (gofmt).
		indented := binaryBreaks(p, x)
		if indented {
			p.indent++
		}
		p.binary(x, depth)
		if indented {
			p.indent--
		}
	case *syntax.UnaryExpr:
		p.token(x.OpPos, x.Op.String())
		p.expr1(x.X, depth)
	case *syntax.StarExpr:
		p.token(x.Star, "*")
		p.expr1(x.X, 1)
	case *syntax.ParenExpr:
		p.token(x.Lparen, "(")
		broken := p.gap(x.X.Pos(), false)
		if broken {
			p.noBlank = true
			p.indent++
		}
		p.expr1(x.X, max(depth-1, 1))
		if broken {
			p.indent--
		}
		p.gap(x.Rparen, false)
		p.token(x.Rparen, ")")
	case *syntax.SelectorExpr:
		p.expr1(x.X, depth)
		if lit, ok := x.X.(*syntax.BasicLit); ok && (lit.Kind == syntax.INT || lit.Kind == syntax.FLOAT) {
			// `0 .A` printed as `0.A` would scan as the number `0.`; a
			// blank keeps the tokens apart (go/printer's mayCombine).
			p.space()
		}
		p.synth(".")
		if p.breakBefore(x.Sel.Pos()) {
			p.indent++
			p.newline()
			p.ident(x.Sel)
			p.indent--
		} else {
			p.ident(x.Sel)
		}
	case *syntax.IndexExpr:
		p.expr1(x.X, depth)
		p.list(x.Lbrack, "[", nodes(x.Indices), func(i int) { p.expr1(x.Indices[i], indexDepth(x.Indices[i], depth)) }, x.Rbrack, "]")
	case *syntax.SliceExpr:
		p.expr1(x.X, depth)
		p.token(x.Lbrack, "[")
		p.slice(x, depth)
		p.token(x.Rbrack, "]")
	case *syntax.TypeGuard:
		p.expr1(x.X, depth)
	case *syntax.CallExpr:
		if id, ok := x.Fun.(*syntax.Ident); ok && id.Name == "with" && !x.Lparen.IsValid() {
			// `with E, F { body }`, the call shape of the with form; one
			// argument prints without parentheses (#196, D4).
			p.token(id.NamePos, "with")
			p.space()
			p.exprList(x.Args, 1)
			p.space()
			p.block(x.Trailing, true)
			return
		}
		p.expr1(x.Fun, depth)
		argDepth := depth
		if len(x.Args) > 1 {
			argDepth++
		}
		p.list(x.Lparen, "(", nodes(x.Args), func(i int) { p.expr1(x.Args[i], argDepth) }, x.Rparen, ")")
		if x.Trailing != nil {
			p.space()
			p.block(x.Trailing, true)
		}
	case *syntax.CompositeLit:
		if x.Type != nil {
			p.expr1(x.Type, depth)
		}
		p.list(x.Lbrace, "{", nodes(x.Elts), func(i int) { p.element(x.Elts[i]) }, x.Rbrace, "}")
	case *syntax.KeyValueExpr:
		p.element(x)
	case *syntax.AsPattern:
		p.expr1(x.X, depth)
		p.space()
		p.token(x.As, "as")
		p.space()
		p.ident(x.Name)
	case *syntax.FuncLit:
		p.funcType(x.Type)
		p.space()
		p.block(x.Body, true)
	case *syntax.TryExpr:
		p.token(x.Try, "try")
		p.space()
		p.expr1(x.X, depth)
	case *syntax.CatchExpr:
		p.expr1(x.X, depth)
		p.space()
		p.token(x.Catch, "catch")
		p.space()
		p.clauses(x.Lbrace, len(x.Arms), func(i int) syntax.Pos { return x.Arms[i].Case }, func(i int) {
			a := x.Arms[i]
			p.caseHead(a.Case, a.Types, a.Colon)
		}, func(i int) []syntax.Stmt { return x.Arms[i].Body }, x.Rbrace)
	case *syntax.ContextLit:
		p.context(x.Context, x.Provides, x.Needs, x.Body)
	case *syntax.IfStmt:
		p.ifStmt(x, p.sameLine(x))
	case *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		p.stmt(x.(syntax.Stmt))
	case *syntax.ArrayType:
		p.token(x.Lbrack, "[")
		p.synth("]")
		p.typ(x.Elt)
	case *syntax.MapType:
		p.token(x.Map, "Map")
		p.list(x.Lbrack, "[", []syntax.Node{x.Key, x.Value}, func(i int) {
			p.typ([]syntax.Expr{x.Key, x.Value}[i])
		}, x.Rbrack, "]")
	case *syntax.FuncType:
		p.funcType(x)
	case *syntax.StructType:
		p.structType(x)
	case *syntax.InterfaceType:
		if x.Union != nil {
			p.union(x)
			return
		}
		p.operationType(x)
	case *syntax.MachineType:
		p.machineType(x)
	case *syntax.ErrorRow:
		p.token(x.Error, "fails")
		p.row(x.Types)
	case *syntax.TupleExpr:
		if !x.Lparen.IsValid() {
			p.exprList(x.Elts, depth)
			return
		}
		p.list(x.Lparen, "(", nodes(x.Elts), func(i int) { p.expr(x.Elts[i]) }, x.Rparen, ")")
	}
}

func (p *printer) funcType(t *syntax.FuncType) {
	if t.Escaping.IsValid() {
		p.token(t.Escaping, "escaping")
		p.space()
	}
	if t.Func.IsValid() {
		p.token(t.Func, "func")
	}
	p.signature(t)
}

// element prints a composite literal element. A keyed element that starts
// its line aligns its value with the keyed elements of the lines around it.
func (p *printer) element(x syntax.Expr) {
	kv, ok := x.(*syntax.KeyValueExpr)
	if !ok {
		p.expr(x)
		return
	}
	startsLine := !p.started || p.pendingNL > 0
	p.expr(kv.Key)
	p.token(kv.Colon, ":")
	if p.gap(kv.Value.Pos(), false) {
		p.indent++
		p.expr(kv.Value)
		p.indent--
		return
	}
	if startsLine {
		p.sep()
	} else {
		p.space()
	}
	p.expr(kv.Value)
}

// slice prints the indices of a slice expression with gofmt's blanks: around
// the colons when more than one index is present and one is binary.
func (p *printer) slice(x *syntax.SliceExpr, depth int) {
	indices := []syntax.Expr{x.Low, x.High}
	if x.Slice3 {
		indices = append(indices, x.Max)
	}
	blanks := false
	if depth <= 1 {
		count, binary := 0, false
		for _, e := range indices {
			if e != nil {
				count++
				_, isBinary := e.(*syntax.BinaryExpr)
				binary = binary || isBinary
			}
		}
		blanks = count > 1 && binary
	}
	for i, e := range indices {
		if i > 0 {
			if indices[i-1] != nil && blanks {
				p.space()
			}
			p.synth(":")
			if e != nil && blanks {
				p.space()
			}
		}
		if e != nil {
			p.expr1(e, depth+1)
		}
	}
}

// ---------------------------------------------------------------- binary expressions (go/printer's spacing)

// binary prints x with gofmt's blanks: an operator of a precedence at or
// above the expression's cutoff is written without blanks (x*y + z).
func (p *printer) binary(x *syntax.BinaryExpr, depth int) {
	prec := x.Op.Precedence()
	blank := prec < p.cutoff(x, depth)
	p.binaryOperand(x.X, depth+diffPrec(x.X, prec))
	if blank {
		p.space()
	}
	p.token(x.OpPos, x.Op.String())
	p.gap(x.Y.Pos(), blank)
	p.binaryOperand(x.Y, depth+1)
}

func (p *printer) binaryOperand(x syntax.Expr, depth int) {
	if b, ok := x.(*syntax.BinaryExpr); ok && b.Op != syntax.DOTDOT {
		p.binary(b, depth)
		return
	}
	p.expr1(x, depth)
}

// binaryBreaks reports whether the source breaks a line after an operator
// of the binary expression x or its binary operands.
func binaryBreaks(p *printer, x *syntax.BinaryExpr) bool {
	if p.line(x.Y.Pos()) > p.line(x.OpPos) {
		return true
	}
	for _, e := range []syntax.Expr{x.X, x.Y} {
		if b, ok := e.(*syntax.BinaryExpr); ok && binaryBreaks(p, b) {
			return true
		}
	}
	return false
}

func (p *printer) cutoff(e *syntax.BinaryExpr, depth int) int {
	has4, has5, maxProblem := p.walkBinary(e)
	if maxProblem > 0 {
		return maxProblem + 1
	}
	if has4 && has5 {
		if depth == 1 {
			return 5
		}
		return 4
	}
	if depth == 1 {
		return 6
	}
	return 4
}

func diffPrec(x syntax.Expr, prec int) int {
	b, ok := x.(*syntax.BinaryExpr)
	if !ok || prec != b.Op.Precedence() {
		return 1
	}
	return 0
}

// walkBinary is go/printer's walkBinary, memoised per node: cutoff asks it for
// every operator of a chain, which would be quadratic in the chain's length.
func (p *printer) walkBinary(e *syntax.BinaryExpr) (has4, has5 bool, maxProblem int) {
	if r, ok := p.walked[e]; ok {
		return r.has4, r.has5, r.maxProblem
	}
	defer func() { p.walked[e] = walkResult{has4, has5, maxProblem} }()
	switch e.Op.Precedence() {
	case 4:
		has4 = true
	case 5:
		has5 = true
	}
	if l, ok := e.X.(*syntax.BinaryExpr); ok && l.Op.Precedence() >= e.Op.Precedence() {
		h4, h5, mp := p.walkBinary(l)
		has4, has5, maxProblem = has4 || h4, has5 || h5, max(maxProblem, mp)
	}
	switch r := e.Y.(type) {
	case *syntax.BinaryExpr:
		if r.Op.Precedence() > e.Op.Precedence() {
			h4, h5, mp := p.walkBinary(r)
			has4, has5, maxProblem = has4 || h4, has5 || h5, max(maxProblem, mp)
		}
	case *syntax.StarExpr:
		if e.Op == syntax.QUO {
			maxProblem = 5
		}
	case *syntax.UnaryExpr:
		switch e.Op.String() + r.Op.String() {
		case "/*", "&&", "&^":
			maxProblem = 5
		case "++", "--":
			maxProblem = max(maxProblem, 4)
		}
	}
	return
}
