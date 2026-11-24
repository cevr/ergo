package lower

// IR pass: rewrites over functions (types.md, Laws 14; LAWFFI r2 §3.6,
// LF6c; on IRF-0's forms, LF6p). See ir.go.
//
// A top-level rewrite law `law L(params) { lhs } == { rhs }` in the
// rewrite's scope (check.FuncRewrite) matches a call tree of the body's IR
// that is its left side's IR: the same Go calls, conversions and len, the
// same constants, and, at each parameter, any operand of the parameter's
// Go type (a parameter that occurs twice, the same local or constant).
// The builder lowers every call as written; the pass (rewritePass,
// ir_rewrite_pass.go) runs on the body before its forms are erased. One
// decision, rewriteAt, takes three forms:
//
//   - the run build, under -rewrites=on (the default) or proved, lowers
//     the right side where the evidence admits the law (AdmitFunc: a
//     current entry at a level admitLevel admits, and, under proved, a
//     proved one);
//   - the reference (rewriteLowering=reference) and -rewrites=off lower
//     the left side, as written;
//   - under -rewrites=canary[=N] the run build's rewritten site also
//     evaluates its left side on each N-th call and compares (canary);
//   - the test build lowers shadow.Shadow(left, right, …) at every matched
//     site, admitted or not, a stale entry's and a dependency's site too:
//     the site's value is its left side's, and a right side that key
//     equality tells apart raises a defect naming the law and the site.
//
// Each matched operand is evaluated once, in source order, into a
// temporary (a local or a constant is read as it is), before either side:
// a rewrite never drops, repeats or reorders the evaluation of a user
// expression. The temporaries go into the statement slot before the
// statement that holds the site, only where nothing the statement
// evaluates before the site can fail (rwAt). A call tree inside a call of
// a function marked `// ergo:aswritten` (the prelude's AsWritten, or a
// user's; check.AsWritten) is never the
// root or a part of a match; its matched sites keep their left side, and
// the report says why.
//
// The matcher reads the law's sides as the builder lowers them, never a
// function's or a package's name. Only the IR path
// rewrites; a body that keeps its source body keeps its left sides (the
// text emitter is frozen, IR-ONLY), and its sites say so.

import (
	"fmt"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketch/testplan"
	v1 "ergo.local/prototype/runtime/v1"
)

// funcRewrites is a lowering's rewrites over functions.
type funcRewrites struct {
	// mode is -rewrites (on, proved, off, canary); test shadows every
	// site. every is the canary's N: a check on each N-th call at a site.
	mode  string
	every uint32
	test  bool
	ev    RewriteEvidence
	// laws are the laws in the rewrite's scope, read once; shipped the
	// shipped ledger's evidence by package.
	laws    []*check.Law
	shipped map[*check.Package]*shippedEvidence
	// fallible are the fallible laws unfoldAt places (ir_unfold.go).
	fallible []*check.Law
	// canarySites are the canary's site indexes by position: a lowering
	// that runs again from the start (lowerAgain) meets a site again.
	canarySites map[string]int
}

func newFuncRewrites(u Unit, test bool) *funcRewrites {
	mode, every, _ := ParseRewrites(u.Rewrites) // Validate checked it
	return &funcRewrites{mode: mode, every: every, test: test, ev: u.Rewrite}
}

// CanaryEvery is the canary's N when -rewrites=canary names none (LAWFFI
// r2 §3.7).
const CanaryEvery = 1024

// ParseRewrites reads ergo build's -rewrites value: "" or on, proved,
// off, or canary[=N] (N from 1 to 2^31, CanaryEvery when not given). It
// returns the mode and the canary's N, 0 for any other mode.
func ParseRewrites(s string) (string, uint32, error) {
	switch s {
	case "", "on":
		return "on", 0, nil
	case "proved", "off":
		return s, 0, nil
	case "canary":
		return "canary", CanaryEvery, nil
	}
	if n, ok := strings.CutPrefix(s, "canary="); ok {
		v, err := strconv.ParseUint(n, 10, 32)
		if err == nil && v >= 1 && v <= 1<<31 {
			return "canary", uint32(v), nil
		}
	}
	return "", 0, fmt.Errorf("-rewrites takes on, proved, off or canary[=N] (N from 1 to 2147483648), not %q", s)
}

// scopeLaws are the laws in the rewrite's scope: of the root package and
// every package the program reaches, each package's in the order it
// declares them.
func (l *lowerer) scopeLaws() []*check.Law {
	r := l.canon.funcRw
	if r == nil {
		return nil
	}
	if r.laws == nil {
		r.laws = []*check.Law{}
		seen := map[*check.Package]bool{}
		for _, p := range append([]*check.Package{l.pkg}, l.libraries()...) {
			if seen[p] {
				continue
			}
			seen[p] = true
			for _, law := range p.FunctionRewrites() {
				if rw := law.TopRewrite(); rw.Root != nil && !lawFallible(law) {
					r.laws = append(r.laws, law)
				}
			}
		}
	}
	return r.laws
}

// lawFallible reports a law with a try in a side: the IR rewrites a call
// that cannot fail at its site only.
func lawFallible(law *check.Law) bool {
	found := false
	for _, side := range []syntax.Expr{law.TopRewrite().Lhs, law.TopRewrite().Rhs} {
		syntax.Inspect(side, func(n syntax.Node) bool {
			if _, ok := n.(*syntax.TryExpr); ok {
				found = true
			}
			return !found
		})
	}
	return found
}

// funcVerdictOf is the evidence's word on law: the build's ledger for a
// law of a package of the module, the shipped ledger for any other.
func (l *lowerer) funcVerdictOf(law *check.Law) funcVerdict {
	r := l.canon.funcRw
	p := law.RewritePackage()
	if !l.isSourced(p) {
		if r.shipped == nil {
			r.shipped = map[*check.Package]*shippedEvidence{}
		}
		e := r.shipped[p]
		if e == nil {
			e = newShippedEvidence(p)
			r.shipped[p] = e
		}
		return e.funcVerdict(law)
	}
	if e, ok := r.ev.(funcEvidence); ok {
		return e.funcVerdict(law)
	}
	key := LedgerNames(nil).funcKey(law)
	return funcVerdict{Key: key, State: "missing", Why: "the build reads no ergo.laws for " + key}
}

// asWrittenCall reports a call of a function marked `// ergo:aswritten`
// (check.AsWritten), in any package.
func (b *irBuilder) asWrittenCall(x *syntax.CallExpr) bool {
	c := b.l.info.Calls[x]
	return c != nil && c.Kind == check.CallFunc && check.AsWritten(c.Func)
}

// rewriteAt is the site m, a call tree of the body's IR that a law's left
// side matches, at the place at: its right side (run build), its shadow
// (test build), or ok false to keep it as written. pre goes into the
// statement slot before the statement that holds the site.
func (b *irBuilder) rewriteAt(m rwMatch, at *rwAt) (pre []*irStmt, e *irExpr, ok bool) {
	r := b.l.canon.funcRw
	x := m.site
	if x == nil {
		return nil, nil, false
	}
	site := b.siteText(x)
	off := rewriteLowering == "reference" || r.mode == "off" && !r.test
	defer b.withBinding(m.bind)() // a generic law's sides at the site's types (ir_fusion.go)
	over := m.law.TopRewrite().OverFuncs
	if r.test {
		if off || m.keep {
			return nil, nil, false
		}
		return b.shadow(x, m, site, at)
	}
	v, why := b.rwDecide(m)
	var rv *irExpr
	if why == "" {
		mark := b.mark()
		var temps map[*check.Var]*irExpr
		pre, temps = b.bindOperands(m)
		rpre, right := b.lawSide(m.law, m.law.TopRewrite().Rhs, temps, false)
		if over {
			// The fused order may meet a later stage's defect first: on a
			// defect, the left side runs again (v1.FuseOr).
			right, rpre = b.fuseOr(m, x, b.goType(b.lawType(m.law), x), temps, rpre, right), nil
		}
		pre, rv = append(pre, rpre...), right
		if r.every > 0 {
			cpre, cv := b.canary(x, m, site, v.Key, temps, rv)
			pre, rv = append(pre, cpre...), cv
		}
		if len(pre) > 0 && !at.placed() {
			b.unmark(mark)
			why = rwNoSlot
		}
	}
	b.siteRow(x, v, why == "", why)
	if why != "" {
		return nil, nil, false
	}
	b.rwLevel(v.Verdict)
	return pre, rv, true
}

// rwDecide is the run build's word on the site m: the law's verdict, and
// why the site keeps its left side, "" to apply the law.
func (b *irBuilder) rwDecide(m rwMatch) (funcVerdict, string) {
	r := b.l.canon.funcRw
	v := b.l.funcVerdictOf(m.law)
	switch {
	case m.keep:
		return v, "not applied: AsWritten"
	case rewriteLowering == "reference" || r.mode == "off":
		return v, "not applied: -rewrites=off"
	case !v.OK:
		return v, v.Why
	case r.mode == "proved" && v.Verdict != "proved":
		return v, "not applied: -rewrites=proved, and law " + v.Key + " is " + v.Verdict
	case m.law.TopRewrite().OverFuncs:
		return v, b.sitePure(m)
	}
	return v, ""
}

// rwNoSlot is a site's row where its operands or its right side need
// statements that no place before the site keeps in order.
const rwNoSlot = "not applied: the site's statements would run before code that may fail, or the site is evaluated more than once"

// canary is a run build's rewritten site x under -rewrites=canary=N
// (LAWFFI r2 §3.7): right, the right side's value, is the site's, held in
// a temporary; on each N-th call at the site (shadow.Tick, a counter per
// site) the left side is evaluated too, at the same operands, and
// shadow.Canary compares the two, a defect that names the law and the
// site when key equality tells them apart.
func (b *irBuilder) canary(x *syntax.CallExpr, m rwMatch, at, key string, vals map[*check.Var]*irExpr, right *irExpr) ([]*irStmt, *irExpr) {
	p := b.pos(x, x.Pos())
	t := b.lawType(m.law)
	gt := b.goType(t, x)
	if b.namesTypeParam(gt) {
		irNo("rewrite canary in a generic body")
	}
	r := b.l.canon.funcRw
	if r.canarySites == nil {
		r.canarySites = map[string]int{}
	}
	site, seen := r.canarySites[at]
	if !seen {
		site = len(r.canarySites)
		r.canarySites[at] = site
	}
	rt := b.temp("right", gt, p)
	pre := []*irStmt{{K: "def", Pos: p, Local: rt, X: right}}
	lpre, left := b.lawSide(m.law, m.law.TopRewrite().Lhs, vals, true)
	str := func(s string) *irExpr { return irStringConst(p, "string", s) }
	b.keepImport("var _ = " + b.l.rtRef("Tick"))
	var check *irStmt
	if goEqualIsKey(t) {
		// Go's == is key equality here (no float in it): the site
		// compares, and a failure calls CanaryFail, so the check keeps
		// the site's function small enough for Go to inline it (a
		// generic call would not).
		b.keepImport("var _ = " + b.l.rtRef("CanaryFail"))
		check = &irStmt{K: "if", Pos: p, End: p, X: &irExpr{K: "bin", Pos: p, Op: "!=", X: left, Y: &irExpr{K: "local", Pos: p, I: rt}},
			Then: []*irStmt{{K: "expr", Pos: p, X: &irExpr{K: "pcall", Pos: p, T: v1.PackageOf("CanaryFail"), Name: "CanaryFail", Args: []*irExpr{str(key), str(at)}}}}}
	} else {
		b.keepImport("var _ = " + b.l.rtRef("Canary") + "[" + gt + "]")
		check = &irStmt{K: "expr", Pos: p, X: &irExpr{K: "pcall", Pos: p, T: v1.PackageOf("Canary"), Name: "Canary", V: gt, Args: []*irExpr{
			left, {K: "local", Pos: p, I: rt}, str(key), str(at),
		}}}
	}
	tick := &irExpr{K: "pcall", Pos: p, T: v1.PackageOf("Tick"), Name: "Tick", Args: []*irExpr{
		{K: "const", Pos: p, T: "int", V: strconv.Itoa(site)},
		{K: "const", Pos: p, T: "uint32", V: strconv.FormatUint(uint64(r.every), 10)},
	}}
	pre = append(pre, &irStmt{K: "if", Pos: p, End: p, X: tick, Then: append(lpre, check)})
	return pre, &irExpr{K: "local", Pos: p, I: rt}
}

// goEqualIsKey reports a type whose Go == is key equality: a bool, an
// integer or a string, through its definitions. A float's == is not (a NaN
// is not equal to itself, and -0 equals +0), and a composite's needs its
// parts compared.
func goEqualIsKey(t check.Type) bool {
	for n, ok := t.(*check.Named); ok; n, ok = t.(*check.Named) {
		t = n.Underlying()
	}
	u, ok := t.(*check.Basic)
	if !ok {
		return false
	}
	switch u.Kind {
	case check.Bool, check.String, check.Int, check.Int8, check.Int16, check.Int32, check.Int64, check.Duration,
		check.Uint, check.Uint8, check.Uint16, check.Uint32, check.Uint64, check.Uintptr:
		return true
	}
	return false
}

// siteText is x's position, file:line:column.
func (b *irBuilder) siteText(x syntax.Node) string {
	file, line, col := b.l.locate(x, x.Pos())
	return file + ":" + strconv.Itoa(line) + ":" + strconv.Itoa(col)
}

// siteRow records the site's row in the build's ledger evidence, for ergo
// inspect rewrites; the body's finish unapplies it when the body keeps
// its source body.
func (b *irBuilder) siteRow(x syntax.Node, v funcVerdict, applied bool, why string) {
	e, ok := b.l.canon.funcRw.ev.(*LedgerEvidence)
	if !ok {
		return
	}
	file, line, col := b.l.locate(x, x.Pos())
	key := e.funcSite(file, line, col, v, applied, why)
	if applied {
		b.rwRows = append(b.rwRows, key)
	}
}

// rwLevel counts an applied rewrite of a law at level verdict in the body.
func (b *irBuilder) rwLevel(verdict string) {
	if b.rwLevels == nil {
		b.rwLevels = map[string]int{}
	}
	b.rwLevels[verdict]++
}

// rewritesDone ends a body's rewrites: a body given IR keeps them and
// counts them; one that keeps its source body keeps its left sides.
func (st *irState) rewritesDone(b *irBuilder, ok bool, reason string) {
	if !ok {
		if e, isLedger := b.l.canon.funcRw.ev.(*LedgerEvidence); isLedger && len(b.rwRows) > 0 {
			e.funcUnapplied(b.rwRows, "not applied: the body keeps its source body ("+reason+"), which no rewrite reaches")
		}
		return
	}
	for level, n := range b.rwLevels {
		if st.funcApplied == nil {
			st.funcApplied = map[string]int{}
		}
		st.funcApplied[level] += n
	}
}

// bindOperands evaluates each matched operand once, in source order: a
// local or a constant is read where the side reads it, any other operand
// into a temporary. It returns the statements and each parameter's
// local. Each temporary has the parameter's Go type, the matched
// operand's (rwMatch.bind).
func (b *irBuilder) bindOperands(m rwMatch) ([]*irStmt, map[*check.Var]*irExpr) {
	var pre []*irStmt
	vals := map[*check.Var]*irExpr{}
	for _, i := range m.order {
		e, h := m.vals[i], m.holes[i]
		if e.K != "const" && e.K != "local" {
			t := b.temp("arg", h.T, e.Pos)
			pre = append(pre, &irStmt{K: "def", Pos: e.Pos, Local: t, X: e})
			e = &irExpr{K: "local", Pos: e.Pos, I: t}
		}
		vals[m.law.Params[i]] = e
	}
	return pre, vals
}

// lawSide lowers a side of law with its parameters bound to vals: in the
// law's package's facts and with no entry in the call tables (a std law's
// nodes are in no source of the program). A side a check reads (keep: a
// canary's or a shadow's left side) is never rewritten: the pass leaves
// its calls as they are.
func (b *irBuilder) lawSide(law *check.Law, side syntax.Expr, vals map[*check.Var]*irExpr, keep bool) ([]*irStmt, *irExpr) {
	savedInfo, savedDirectives := b.l.info, b.fc.directives
	if b.lawArgs == nil {
		b.lawArgs = map[*check.Var]*irExpr{}
	}
	for v, e := range vals {
		b.lawArgs[v] = e
	}
	b.l.info, b.fc.directives = law.RewriteInfo(), false
	// A side's own binders (a function literal's parameter, in a law over
	// function parameters) are the side's: each lowering of it declares
	// them again (two sites, or both sides under FuseOr).
	known := map[*check.Var]bool{}
	for v := range b.locals {
		known[v] = true
	}
	defer func() {
		for v := range b.locals {
			if !known[v] {
				delete(b.locals, v)
			}
		}
	}()
	b.lawDepth++
	if keep {
		b.lawKeep++
	}
	defer func() {
		b.l.info, b.fc.directives = savedInfo, savedDirectives
		b.lawDepth--
		if keep {
			b.lawKeep--
		}
		for v := range vals {
			delete(b.lawArgs, v)
		}
	}()
	return b.value(side)
}

// lawType is the type of law's sides.
func (b *irBuilder) lawType(law *check.Law) check.Type {
	t := law.RewriteInfo().Types[law.TopRewrite().Lhs]
	if t == nil {
		irNo("rewrite law side without a type")
	}
	return t
}

// shadow is the test build's site x: both sides at the matched operands,
// the left first, compared by v1.Shadow.
func (b *irBuilder) shadow(x *syntax.CallExpr, m rwMatch, at string, place *rwAt) ([]*irStmt, *irExpr, bool) {
	if !place.placed() {
		return nil, nil, false
	}
	p := b.pos(x, x.Pos())
	gt := b.goType(b.lawType(m.law), x)
	pre, vals := b.bindOperands(m)
	lpre, left := b.lawSide(m.law, m.law.TopRewrite().Lhs, vals, true)
	pre = append(pre, lpre...)
	lt := b.temp("left", gt, p)
	pre = append(pre, &irStmt{K: "def", Pos: p, Local: lt, X: left})
	rpre, right := b.lawSide(m.law, m.law.TopRewrite().Rhs, vals, true)
	pre = append(pre, rpre...)
	key := LedgerNames(nil).funcKey(m.law)
	if b.l.isSourced(m.law.RewritePackage()) {
		if e, ok := b.l.canon.funcRw.ev.(*LedgerEvidence); ok {
			key = e.names.funcKey(m.law)
		}
	}
	str := func(s string) *irExpr { return irStringConst(p, "string", s) }
	// Shadow is the runtime leaf runtime/v1/shadow's (it links fmt and
	// reflect, which no run build needs), kept as an instance (stdCall).
	if b.namesTypeParam(gt) {
		irNo("rewrite shadow in a generic body")
	}
	b.keepImport("var _ = " + b.l.rtRef("Shadow") + "[" + gt + "]")
	// The law's arguments at the site, for the defect's "for s = …" and
	// the refutation's corpus case (ergo test writes it, Tests 10).
	var names []string
	args := &irExpr{K: "lit", Pos: p, T: "[]any"}
	enc := true
	for _, v := range m.law.Params {
		names = append(names, v.Name)
		args.Elems = append(args.Elems, &irExpr{K: "toiface", Pos: p, T: "any", X: vals[v]})
		enc = enc && caseEncodable(v.Type)
	}
	call := &irExpr{K: "pcall", Pos: p, T: v1.PackageOf("Shadow"), Name: "Shadow", V: gt, Args: []*irExpr{
		{K: "local", Pos: p, I: lt}, right, str(key), str(at), str(b.l.nodeText(m.law.TopRewrite().Lhs)),
		str(strings.Join(names, ",")), {K: "const", Pos: p, T: "bool", V: strconv.FormatBool(enc)}, args,
	}}
	return pre, call, true
}

// caseEncodable reports a type whose values have an exact corpus text at
// run time (ergo.cases; eval's encodeValue): a string, an integer, a
// float, or a slice of one. A listable type's text is its index in the
// class table (testplan.Domain), which a run-time value does not tell.
func caseEncodable(t check.Type) bool {
	if vs, ok := testplan.Domain(t); ok && len(vs) > 0 {
		return false
	}
	for n, ok := t.(*check.Named); ok; n, ok = t.(*check.Named) {
		t = n.Underlying()
	}
	switch u := t.(type) {
	case *check.Basic:
		switch u.Kind {
		case check.String, check.Float32, check.Float64, check.Int, check.Int8, check.Int16, check.Int32, check.Int64, check.Duration,
			check.Uint, check.Uint8, check.Uint16, check.Uint32, check.Uint64, check.Uintptr:
			return true
		}
		return false
	case *check.Slice:
		return caseEncodable(u.Elem)
	}
	return false
}

// nodeText is n's source text, from the program's sources or std's.
func (l *lowerer) nodeText(n syntax.Node) string {
	var f *syntax.File
	if i, ok := l.sourceOf(n); ok {
		f = l.srcs[i].file
	} else if sf, ok := l.stdFileOf(n); ok {
		f = sf.file
	}
	if f != nil && n.Pos() >= 0 && int(n.End()) <= len(f.Src) {
		return f.Src[n.Pos():n.End()]
	}
	return "the left side"
}
