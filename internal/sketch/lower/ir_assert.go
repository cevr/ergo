package lower

// IR builder: test bodies and their asserts (G6, IRG6 r3). A test lowers
// to a Go function of its body in the test build (testDecl): its
// environment, its parameters, and an error result, the body a failure
// target. An assert is the emitter's `if !(cond) { ergoAssertFail(at,
// text) }` (assert); a comparison whose operands the report shows
// (assertShown) evaluates each operand once into a local, and its
// failure text is built by a helper of the generated part only when the
// assert fails. A law (IRG6 r4) lowers to `func ergoLaw…(env, params…)
// (bool, error)` whose every returned value is checked where it is
// returned (lawResult). See ir.go.

import (
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// testBody gives the test d, the Go function name, an IR body: true when
// it did, and the test's declaration is then written without one.
func (st *irState) testBody(fc *funcCtx, d *syntax.TestDecl, name string, params []*check.Var, needsFrame bool) (ok bool) {
	l := fc.l
	b := st.newBuilder(fc, d)
	defer st.finish(b, l.pkg.Path, name, d, false, &ok)
	if needsFrame && !irFrame(fc) {
		b.gate("body that needs a frame") // frameScopeLowering=off: the reference
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
	if needsFrame {
		// The test's frame scope, before the entry poll, as a function's
		// (the emitter's testDecl: openFrame, then entryPoll).
		b.part(func() {
			b.fn.Body = append(b.fn.Body, b.openScopeOf(d.Body, d, d.Name, params, b.frameErrArg(b.pos(d, d.Pos())), false)...)
		}) // ir_own.go
	}
	if frameFootprint(l.info, d).EntryPoll && fc.frame.polls {
		b.fn.Body = append(b.fn.Body, b.poll(d))
	}
	b.fn.Body = append(b.fn.Body, b.bodyStmts(d.Body.List)...) // ir_fail.go
	b.fn.Body = append(b.fn.Body, b.bodyEnd(b.fn.Body, b.fn.End)...)
	if n := len(d.Body.List); needsFrame && n > 0 && !irListTerminates(b.fn.Body) {
		// The emitter's test ends in a return at its last statement's
		// line, where its exit actions run: a defect in one names that
		// line, not the closing brace (runtime.md, Fork-tree defects 3).
		last := d.Body.List[n-1]
		b.fn.Body = append(b.fn.Body, b.ret(nil, b.pos(last, last.Pos()))...)
	}
	b.fn.Body = irScopesIncrease(irUseLocals(b.fn.Body), b.fn.Pos) // ir_end.go
	if st.census != nil {
		return false
	}
	st.out.Funcs = append(st.out.Funcs, *b.fn)
	st.report.IR = append(st.report.IR, name)
	return true
}

// lawBody gives the law, the Go function name, an IR body (lawDecl):
// `func ergoLaw…(env, params…) (bool, error)`, each value it returns
// checked where it is returned (lawResult). A law of a generic operation
// type's copy (mono.go) also takes the provider under test after the
// environment (lawOwn).
func (st *irState) lawBody(fc *funcCtx, law *check.Law, name string, needsFrame bool) (ok bool) {
	l, d := fc.l, law.Decl
	b := st.newBuilder(fc, d)
	b.result, b.law = fc.frame.result, law
	if law.Owner != nil {
		if own, ok := law.Owner.Type.(*check.Named); ok {
			b.lawOwner = own
		}
	}
	defer st.finish(b, law.Pkg.Path, name, d, law.Pkg.Std(), &ok)
	if needsFrame && !irFrame(fc) {
		b.gate("body that needs a frame") // frameScopeLowering=off: the reference
	}
	b.env = b.next
	b.next++
	if own := lawGenericOwner(law); own != nil {
		if !fc.mono {
			// The stencil/reference is shared across instances. The
			// normal IR path has a monomorphic body at each binding.
			b.gate("law of a generic operation type")
		}
		// A copy of a law of a generic operation type at one instance
		// (mono.go lawMonoFor; lawDecl writes its header: the provider
		// under test `own` after the environment); its operation calls
		// call own (ir_call.go).
		b.lawOwn = b.next
		b.next++
	}
	for _, v := range law.Params {
		if v.Name != "_" {
			b.locals[v] = b.next
			b.part(func() { b.goType(v.Type, d) })
		}
		b.next++
	}
	b.fn = &irFunc{Name: name, Pos: b.pos(d, d.Pos()), End: b.pos(d, d.Body.Rbrace)}
	b.names = b.tempNames(d.Body) // ir_fail.go
	if needsFrame {
		// The law's frame scope, before the entry poll, as a function's
		// (the emitter's lawDecl: openFrame, then entryPoll); its End
		// takes the error result, the second of (bool, error).
		b.part(func() {
			b.fn.Body = append(b.fn.Body, b.openScopeOf(d.Body, d, d.Name, law.Params, b.frameErrArg(b.pos(d, d.Pos())), false)...)
		}) // ir_own.go
	}
	if frameFootprint(l.info, d).EntryPoll && fc.frame.polls {
		b.fn.Body = append(b.fn.Body, b.poll(d))
	}
	b.fn.Body = append(b.fn.Body, b.bodyStmts(d.Body.List)...) // ir_fail.go
	b.fn.Body = append(b.fn.Body, b.bodyEnd(b.fn.Body, b.fn.End)...)
	b.fn.Body = irScopesIncrease(irUseLocals(b.fn.Body), b.fn.Pos) // ir_end.go
	if st.census != nil {
		return false
	}
	st.out.Funcs = append(st.out.Funcs, *b.fn)
	st.report.IR = append(st.report.IR, name)
	return true
}

// lawResult checks the value x a law returns, out its return (the
// emitter's lawResult): held once, refuted at x's site when false. A
// failure is no result; an untried fallible call's pair stays the
// emitter's.
func (b *irBuilder) lawResult(x syntax.Expr, out []*irStmt) []*irStmt {
	last := out[len(out)-1]
	if x == nil || last.K != "ret" || last.X == nil || b.law.Admits != nil {
		// An admission's false only skips its case (lawnear.go).
		return out
	}
	if b.fallibleCall(x) != nil {
		if _, implicit := b.l.info.Implicit[x]; !implicit {
			irNo("law result of an untried fallible call")
		}
	}
	l, p := b.l, last.Pos
	at := x
	if t, ok := unparen(x).(*syntax.TryExpr); ok {
		at = t.X
	}
	held := b.temp("held", "bool", p)
	site := &irExpr{K: "const", Pos: p, T: "string", V: l.siteText(unparen(at))}
	decl := &irExpr{K: "const", Pos: p, T: "string", V: l.siteText(b.law.Decl)}
	if b.fc.claim != nil {
		decl.V += b.fc.claim.ClaimBlame()
	}
	refute := &irExpr{K: "call", Pos: p, Name: l.refutedFunc(), Args: []*irExpr{site, decl}}
	h := &irExpr{K: "local", Pos: p, I: held}
	return append(out[:len(out)-1],
		&irStmt{K: "def", Pos: p, Local: held, X: last.X},
		&irStmt{K: "if", Pos: p, End: p, X: &irExpr{K: "un", Pos: p, Op: "!", X: h}, Then: []*irStmt{{K: "expr", Pos: p, X: refute}}},
		&irStmt{K: "ret", Pos: p, X: h})
}

// lawOpCap is the capability a law's bare operation call c calls
// (CallLawOp): the operation type that declares the law, which the law's
// run binds to the provider under test (calls.go). Nil for any other call.
func (b *irBuilder) lawOpCap(c *check.Call) *check.Named {
	if c.Kind != check.CallLawOp {
		return nil
	}
	if b.lawOwner == nil {
		irNo("law operation call outside its law's body")
	}
	return b.lawOwner
}

// refutedFunc is the harness's Refuted with the law's declaration as one
// argument: the IR calls no variadic function. The naming pass reserves
// its name under LowerIR and CensusIR only (nameTests).
func (l *lowerer) refutedFunc() string {
	name := l.pkgName(refutedKey, "ergoLawRefuted")
	if !l.tests.lits[refutedKey] {
		l.tests.lits[refutedKey] = true
		l.gen.WriteString("func " + name + "(at, declaration string) {\n\t" + l.helper("Refuted") + "(at, declaration)\n}\n\n")
	}
	return name
}

// refutedKey is the package-scope key of refutedFunc.
var refutedKey = pkgKey{"law refuted", nil}

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
		var vals []*irExpr
		build := func() []*irExpr {
			// A try that starts the assert covers the whole comparison:
			// its operands are built under it (the emitter's assert, q105).
			inTry := tried(s.X) != s.X
			if inTry {
				b.tries++
			}
			pre, vals = b.operands([]syntax.Expr{x.X, x.Y}) // ir_fail.go
			if inTry {
				b.tries--
			}
			for k, e := range []syntax.Expr{x.X, x.Y} {
				if vals[k].K == "const" || vals[k].K == "local" {
					continue
				}
				t := b.temp("arg", b.goType(b.operandType(e), e), vals[k].Pos)
				pre = append(pre, &irStmt{K: "def", Pos: vals[k].Pos, Local: t, X: vals[k]})
				vals[k] = &irExpr{K: "local", Pos: vals[k].Pos, I: t}
			}
			return vals
		}
		// == and != on a type that is not basic: the emitter's binaryOf
		// on the spilled operands, as any comparison (ir_equal.go).
		if cond = b.equalOn(x, build); cond == nil {
			b.operator(op, l.info.Types[x.X], x)
			build()
			cond = &irExpr{K: "bin", Pos: b.pos(x, x.OpPos), Op: op, X: vals[0], Y: vals[1]}
		}
		// The spilled operands hold their implicit conversions (a variant
		// compared with its union is the union's value): the helper takes
		// them at those types and prints the value it holds.
		lt, rt = b.operandType(x.X), b.operandType(x.Y)
		if !l.printable(lt) || !l.printable(rt) {
			irNo("assert of a converted operand that does not print")
		}
		show := &irExpr{K: "call", Pos: p, Name: l.assertShowFunc(s, lt, rt, x), Args: vals}
		text = &irExpr{K: "bin", Pos: p, Op: "+", X: text, Y: show}
	} else {
		pre, cond = b.value(s.X)
	}
	site := &irExpr{K: "const", Pos: p, T: "string", V: l.siteText(s)}
	fail := &irExpr{K: "call", Pos: p, Name: l.helper("AssertFail"), Args: []*irExpr{site, text}}
	return append(pre, &irStmt{K: "if", Pos: p, End: p, X: &irExpr{K: "un", Pos: p, Op: "!", X: cond}, Then: []*irStmt{{K: "expr", Pos: p, X: fail}}})
}

// skip is `skip "reason"`: the harness's $SkipNow with the reason, which
// ends the run ($Try reads it).
func (b *irBuilder) skip(s *syntax.SkipStmt) []*irStmt {
	l := b.l
	if l.tests == nil {
		irNo("skip outside the test build")
	}
	p := b.pos(s, s.Pos())
	why, _ := s.Text()
	call := &irExpr{K: "call", Pos: p, Name: l.helper("SkipNow"), Args: []*irExpr{{K: "const", Pos: p, T: "string", V: why}}}
	return []*irStmt{{K: "expr", Pos: p, X: call}}
}

// assertShowFunc is the generated helper that writes a shown assert's
// operands (assertCompare's failure text after the source text):
// `: left L, right R`, each an Ergo literal of its type, and the line
// diff of two strings. It is emitted once per assert s, whose name the
// naming pass reserves (nameTests).
func (l *lowerer) assertShowFunc(s syntax.Node, lt, rt check.Type, site syntax.Node) string {
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

// assertShowKey is the package-scope key of s's show helper: a shown
// assert, or the ergo·shown call of a harnessed test's rewritten assert
// (check harness.go).
func assertShowKey(s syntax.Node) pkgKey { return pkgKey{"assert show", s} }

// shownCall is ergo·shown(left, right) (check.CallTestShown): the helper
// of a shown assert on the two operands, each at the type the checker
// gave it, which gives the runner's text after the assert's source text.
func (b *irBuilder) shownCall(x *syntax.CallExpr, p irPos) ([]*irStmt, *irExpr) {
	l := b.l
	if l.tests == nil || len(x.Args) != 2 {
		irNo("ergo·shown outside a harnessed test")
	}
	lt, lok := l.typedExpr(x.Args[0])
	rt, rok := l.typedExpr(x.Args[1])
	if !lok || !rok || !l.printable(lt) || !l.printable(rt) {
		irNo("ergo·shown of an operand that does not print")
	}
	pre, vals := b.operands(x.Args) // ir_fail.go
	return pre, &irExpr{K: "call", Pos: p, Name: l.assertShowFunc(x, lt, rt, x), Args: vals}
}
