package lower

// IR builder: test bodies and their asserts (G6, IRG6 r3). A test lowers
// to a Go function of its body in the test build (testDecl): its
// environment, its parameters, and an error result, the body a failure
// target. An assert is the emitter's `if !(cond) { ergoAssertFail(at,
// text) }` (assert); a comparison whose operands the report shows
// (assertShown) evaluates each operand once into a local, and its
// failure text is built by a helper of the generated part only when the
// assert fails. See ir.go.

import (
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// testBody gives the test d, the Go function name, an IR body: true when
// it did, and the test's declaration is then written without one.
func (st *irState) testBody(fc *funcCtx, d *syntax.TestDecl, name string, params []*check.Var, needsFrame bool) (ok bool) {
	l := fc.l
	b := &irBuilder{fc: fc, l: l, st: st, locals: map[*check.Var]int{}, env: -1}
	defer st.finish(b, l.pkg.Path, name, d, false, &ok)
	if needsFrame {
		b.gate("body that needs a frame")
	}
	// Go parameter order: the environment, then the parameters (testDecl).
	b.env = b.next
	b.next++
	for _, v := range params {
		if v.Name != "_" {
			b.locals[v] = b.next
			b.part(func() { b.goType(v.Type, d) })
		}
		b.next++
	}
	b.fn = &irFunc{Name: name, Pos: b.pos(d, d.Pos()), End: b.pos(d, d.Body.Rbrace)}
	b.names = b.tempNames(d.Body) // ir_fail.go
	if frameFootprint(l.info, d).EntryPoll && fc.frame.polls {
		b.fn.Body = append(b.fn.Body, b.poll(d))
	}
	b.fn.Body = append(b.fn.Body, b.bodyStmts(d.Body.List)...) // ir_fail.go
	b.fn.Body = append(b.fn.Body, b.bodyEnd(b.fn.Body, b.fn.End)...)
	b.fn.Body = irUseLocals(b.fn.Body) // ir_end.go
	if st.census != nil {
		return false
	}
	st.out.Funcs = append(st.out.Funcs, *b.fn)
	st.report.IR = append(st.report.IR, name)
	return true
}

// assert is `assert cond` (the emitter's assert): the condition, and the
// failure the harness reports when it is false.
func (b *irBuilder) assert(s *syntax.AssertStmt) []*irStmt {
	l := b.l
	if l.tests == nil {
		irNo("assert outside the test build")
	}
	p := b.pos(s, s.Pos())
	i, _, _, _ := l.at(s.X, s.X.Pos())
	text := &irExpr{K: "const", Pos: p, T: "string", V: l.srcs[i].text[s.X.Pos():s.X.End()]}
	var pre []*irStmt
	var cond *irExpr
	if x, lt, rt, shown := l.assertShown(s.X); shown {
		// Each operand once, left to right, into a local (a constant
		// stays inline): the comparison and the failure text read them.
		b.plain(x)
		op := x.Op.String()
		b.operator(op, l.info.Types[x.X], x)
		var vals []*irExpr
		pre, vals = b.operands([]syntax.Expr{x.X, x.Y}) // ir_fail.go
		for k, e := range []syntax.Expr{x.X, x.Y} {
			if vals[k].K == "const" || vals[k].K == "local" {
				continue
			}
			t := b.temp("arg", b.goType(l.info.Types[e], e), vals[k].Pos)
			pre = append(pre, &irStmt{K: "def", Pos: vals[k].Pos, Local: t, X: vals[k]})
			vals[k] = &irExpr{K: "local", Pos: vals[k].Pos, I: t}
		}
		cond = &irExpr{K: "bin", Pos: b.pos(x, x.OpPos), Op: op, X: vals[0], Y: vals[1]}
		show := &irExpr{K: "call", Pos: p, Name: l.assertShowFunc(s, lt, rt, x), Args: vals}
		text = &irExpr{K: "bin", Pos: p, Op: "+", X: text, Y: show}
	} else {
		pre, cond = b.value(s.X)
	}
	site := &irExpr{K: "const", Pos: p, T: "string", V: l.siteText(s)}
	fail := &irExpr{K: "call", Pos: p, Name: l.helper("AssertFail"), Args: []*irExpr{site, text}}
	return append(pre, &irStmt{K: "if", Pos: p, End: p, X: &irExpr{K: "un", Pos: p, Op: "!", X: cond}, Then: []*irStmt{{K: "expr", Pos: p, X: fail}}})
}

// assertShowFunc is the generated helper that writes a shown assert's
// operands (assertCompare's failure text after the source text):
// `: left L, right R`, each an Ergo literal of its type, and the line
// diff of two strings. It is emitted once per assert s, whose name the
// naming pass reserves (nameTests, under LowerIR and CensusIR only).
func (l *lowerer) assertShowFunc(s *syntax.AssertStmt, lt, rt check.Type, site syntax.Node) string {
	tc := &typeCtx{l: l, site: site}
	lg, rg := tc.goType(lt), tc.goType(rt)
	key := assertShowKey(s)
	name := l.pkgName(key, "ergoAssertShow")
	if l.tests.lits[key] {
		return name
	}
	l.tests.lits[key] = true
	scope := newNamer(l.names)
	a, c := scope.alloc("left"), scope.alloc("right")
	text := strconv.Quote(": left ") + " + " + l.litExpr(lt, a, site) + " + " + strconv.Quote(", right ") + " + " + l.litExpr(rt, c, site)
	if isStringLike(lt) && isStringLike(rt) {
		text += " + " + l.helper("AssertDiff") + "(string(" + a + "), string(" + c + "))"
	}
	l.gen.WriteString("func " + name + "(" + a + " " + lg + ", " + c + " " + rg + ") string {\n\treturn " + text + "\n}\n\n")
	return name
}

// assertShowKey is the package-scope key of s's show helper.
func assertShowKey(s *syntax.AssertStmt) pkgKey { return pkgKey{"assert show", s} }
