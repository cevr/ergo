package lower

// IR builder: rewrites over functions (types.md, Laws 14; LAWFFI r2 §3.6,
// LF6c). See ir.go.
//
// A top-level rewrite law `law L(params) { lhs } == { rhs }` in the
// rewrite's scope (check.FuncRewrite) matches a call tree of the program
// that has the left side's shape: the same functions, conversions and
// len, the same constants, and, at each parameter, any operand of the
// parameter's type (a parameter that occurs twice, the same local). One
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
// expression. A call tree inside a call of a body marked //ergo:aswritten
// (the prelude's AsWritten) is never the root or a part of a match; its
// matched sites keep their left side, and the report says why.
//
// The matcher reads checked law facts only: the law's sides and their
// call table, never a function's or a package's name. Only the IR path
// rewrites; a body that keeps its source body keeps its left sides (the
// text emitter is frozen, IR-ONLY), and its sites say so.

import (
	"fmt"
	"go/constant"
	"go/token"
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
	// laws are the laws in the rewrite's scope by root function, read
	// once; shipped the shipped ledger's evidence by package.
	laws    map[*check.Func][]*check.Law
	shipped map[*check.Package]*shippedEvidence
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

// funcLaws are the laws in the rewrite's scope whose root is f: of the
// root package and every package the program reaches.
func (l *lowerer) funcLaws(f *check.Func) []*check.Law {
	r := l.canon.funcRw
	if r == nil {
		return nil
	}
	if r.laws == nil {
		r.laws = map[*check.Func][]*check.Law{}
		seen := map[*check.Package]bool{}
		for _, p := range append([]*check.Package{l.pkg}, l.libraries()...) {
			if seen[p] {
				continue
			}
			seen[p] = true
			for _, law := range p.Laws {
				if rw := law.FuncRewrite; rw != nil && !lawFallible(law) {
					r.laws[rw.Root] = append(r.laws[rw.Root], law)
				}
			}
		}
	}
	return r.laws[f]
}

// lawFallible reports a law with a try in a side: the IR rewrites a call
// that cannot fail at its site only.
func lawFallible(law *check.Law) bool {
	found := false
	for _, side := range []syntax.Expr{law.FuncRewrite.Lhs, law.FuncRewrite.Rhs} {
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
	if !l.isSourced(law.Pkg) {
		if r.shipped == nil {
			r.shipped = map[*check.Package]*shippedEvidence{}
		}
		e := r.shipped[law.Pkg]
		if e == nil {
			e = newShippedEvidence(law.Pkg)
			r.shipped[law.Pkg] = e
		}
		return e.funcVerdict(law)
	}
	if e, ok := r.ev.(funcEvidence); ok {
		return e.funcVerdict(law)
	}
	key := LedgerNames(nil).funcLaw(law)
	return funcVerdict{Key: key, State: "missing", Why: "the build reads no ergo.laws for " + key}
}

// asWrittenCall reports a call of a body marked //ergo:aswritten.
func (b *irBuilder) asWrittenCall(x *syntax.CallExpr) bool {
	c := b.l.info.Calls[x]
	if c == nil || c.Kind != check.CallFunc || c.Func == nil || c.Func.Decl == nil || c.Func.Decl.Extern == nil {
		return false
	}
	path, name := c.Func.Decl.Extern.Split()
	if !v1.Owns(path) {
		return false
	}
	body, err := v1.BodyIn(path, name)
	return err == nil && body.AsWritten
}

// rwMatch is a match of a law's left side at a site: each parameter's
// operand, and the parameters in the order their operands are evaluated.
type rwMatch struct {
	law   *check.Law
	bind  map[*check.Var]syntax.Expr
	order []*check.Var
}

// rewriteAt is the call x where a law's left side matches it: its right
// side (run build), its shadow (test build), or ok false to lower x as
// written.
func (b *irBuilder) rewriteAt(x *syntax.CallExpr) (pre []*irStmt, e *irExpr, ok bool) {
	r := b.l.canon.funcRw
	if r == nil || b.st.census != nil || b.law != nil {
		return nil, nil, false
	}
	m, found := b.matchAt(x)
	if !found {
		return nil, nil, false
	}
	at := b.siteText(x)
	off := rewriteLowering == "reference" || r.mode == "off" && !r.test
	if r.test {
		if off || b.keep > 0 {
			return nil, nil, false
		}
		return b.shadow(x, m, at)
	}
	v := b.l.funcVerdictOf(m.law)
	why := ""
	switch {
	case b.keep > 0:
		why = "not applied: AsWritten"
	case off:
		why = "not applied: -rewrites=off"
	case !v.OK:
		why = v.Why
	case r.mode == "proved" && v.Verdict != "proved":
		why = "not applied: -rewrites=proved, and law " + v.Key + " is " + v.Verdict
	}
	b.siteRow(x, v, why == "", why)
	if why != "" {
		return nil, nil, false
	}
	pre, temps := b.bindOperands(m)
	rpre, rv := b.lawSide(m.law, m.law.FuncRewrite.Rhs, temps)
	pre = append(pre, rpre...)
	if r.every > 0 {
		cpre, cv, ok := b.canary(x, m, at, v.Key, temps, rv)
		if !ok {
			return nil, nil, false
		}
		pre, rv = append(pre, cpre...), cv
	}
	b.rwLevel(v.Verdict)
	return pre, rv, true
}

// canary is a run build's rewritten site x under -rewrites=canary=N
// (LAWFFI r2 §3.7): right, the right side's value, is the site's, held in
// a temporary; on each N-th call at the site (shadow.Tick, a counter per
// site) the left side is evaluated too, at the same operands, and
// shadow.Canary compares the two, a defect that names the law and the
// site when key equality tells them apart.
func (b *irBuilder) canary(x *syntax.CallExpr, m rwMatch, at, key string, vals map[*check.Var]*irExpr, right *irExpr) ([]*irStmt, *irExpr, bool) {
	p := b.pos(x, x.Pos())
	t := b.l.info.Types[x]
	if t == nil {
		return nil, nil, false
	}
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
	lpre, left := b.lawSide(m.law, m.law.FuncRewrite.Lhs, vals)
	str := func(s string) *irExpr { return &irExpr{K: "const", Pos: p, T: "string", V: irString(s)} }
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
	return pre, &irExpr{K: "local", Pos: p, I: rt}, true
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

// matchAt is the first law whose left side matches x, in the order the
// root's package declares its laws.
func (b *irBuilder) matchAt(x *syntax.CallExpr) (rwMatch, bool) {
	root := b.firstFunc(x)
	if root == nil {
		return rwMatch{}, false
	}
	for _, law := range b.l.funcLaws(root) {
		m := rwMatch{law: law, bind: map[*check.Var]syntax.Expr{}}
		if b.match(&m, law.FuncRewrite.Lhs, x) {
			return m, true
		}
	}
	return rwMatch{}, false
}

// firstFunc is the first declared function x calls, outermost first and
// in source order: a law's root (check.FuncRewrite.Root) is its left
// side's, so a site its left side matches has the same one.
func (b *irBuilder) firstFunc(x syntax.Expr) *check.Func {
	var f *check.Func
	syntax.Inspect(x, func(n syntax.Node) bool {
		if f != nil {
			return false
		}
		if call, ok := n.(*syntax.CallExpr); ok {
			if c := b.l.info.Calls[call]; c != nil && c.Kind == check.CallFunc && c.Func != nil {
				f = c.Func
				return false
			}
		}
		return true
	})
	return f
}

// match matches the law's side node ln against the site's node sn.
func (b *irBuilder) match(m *rwMatch, ln, sn syntax.Expr) bool {
	li, si := m.law.Pkg.Info, b.l.info
	ln, sn = unparen(ln), unparen(sn)
	if id, ok := ln.(*syntax.Ident); ok {
		if v, isVar := li.Uses[id].(*check.Var); isVar && lawParam(m.law, v) {
			return b.bindParam(m, v, sn)
		}
	}
	if _, conv := li.Implicit[ln]; conv {
		return false
	}
	if _, conv := si.Implicit[sn]; conv {
		return false
	}
	if lv, isConst := li.Values[ln]; isConst {
		sv, ok := si.Values[sn]
		return ok && lv.Kind() == sv.Kind() && constant.Compare(lv, token.EQL, sv) && b.sameGoType(li.Types[ln], si.Types[sn], sn)
	}
	lc, ok := ln.(*syntax.CallExpr)
	if !ok {
		return false
	}
	sc, ok := sn.(*syntax.CallExpr)
	if !ok || sc.Trailing != nil || lc.Trailing != nil {
		return false
	}
	lk, sk := li.Calls[lc], si.Calls[sc]
	if lk == nil || sk == nil || lk.Kind != sk.Kind || len(lk.TypeArgs) > 0 || len(sk.TypeArgs) > 0 || failsHere(sk) {
		return false
	}
	switch lk.Kind {
	case check.CallFunc:
		if lk.Func == nil || lk.Func != sk.Func || b.asWrittenCall(sc) {
			return false
		}
	case check.CallConversion:
		if !b.sameGoType(li.Types[lc], si.Types[sc], sc) {
			return false
		}
	case check.CallLen:
	default:
		return false
	}
	if (lk.Recv == nil) != (sk.Recv == nil) || len(lc.Args) != len(sc.Args) {
		return false
	}
	if lk.Recv != nil && !b.match(m, lk.Recv, sk.Recv) {
		return false
	}
	for i := range lc.Args {
		if !b.match(m, lc.Args[i], sc.Args[i]) {
			return false
		}
	}
	return true
}

// bindParam binds the parameter v to the site's operand sn: an operand of
// v's type, after its implicit conversion; a second occurrence of v takes
// only the same local.
func (b *irBuilder) bindParam(m *rwMatch, v *check.Var, sn syntax.Expr) bool {
	si := b.l.info
	if prev, bound := m.bind[v]; bound {
		pi, ok1 := unparen(prev).(*syntax.Ident)
		ni, ok2 := sn.(*syntax.Ident)
		if !ok1 || !ok2 {
			return false
		}
		pv, isVar := si.Uses[pi].(*check.Var)
		_, local := b.locals[pv]
		return isVar && local && si.Uses[ni] == pv
	}
	t := si.Types[sn]
	if conv, ok := si.Implicit[sn]; ok && conv.To != nil {
		t = conv.To
	}
	if t == nil || !b.sameGoType(v.Type, t, sn) {
		return false
	}
	m.bind[v] = sn
	m.order = append(m.order, v)
	return true
}

// sameGoType reports two types the program spells as one Go type: the
// law's package and the site's check intern their types apart.
func (b *irBuilder) sameGoType(a, c check.Type, site syntax.Node) bool {
	return a != nil && c != nil && b.goType(a, site) == b.goType(c, site)
}

// lawParam reports v, a parameter of law.
func lawParam(law *check.Law, v *check.Var) bool {
	for _, p := range law.Params {
		if p == v {
			return true
		}
	}
	return false
}

// bindOperands evaluates each matched operand once, in source order: a
// local or a constant is read where the side reads it, any other operand
// into a temporary. It returns the statements and each parameter's
// local.
func (b *irBuilder) bindOperands(m rwMatch) ([]*irStmt, map[*check.Var]*irExpr) {
	var pre []*irStmt
	vals := map[*check.Var]*irExpr{}
	for _, v := range m.order {
		sn := m.bind[v]
		vpre, e := b.operand(sn)
		pre = append(pre, vpre...)
		if e.K != "const" && e.K != "local" {
			t := b.temp("arg", b.goType(v.Type, sn), e.Pos)
			pre = append(pre, &irStmt{K: "def", Pos: e.Pos, Local: t, X: e})
			e = &irExpr{K: "local", Pos: e.Pos, I: t}
		}
		vals[v] = e
	}
	return pre, vals
}

// lawSide lowers a side of law with its parameters bound to vals: in the
// law's package's facts, with no rewrite inside it (it is the law's own
// text), and with no entry in the call tables (a std law's nodes are in
// no source of the program).
func (b *irBuilder) lawSide(law *check.Law, side syntax.Expr, vals map[*check.Var]*irExpr) ([]*irStmt, *irExpr) {
	savedInfo, savedDirectives := b.l.info, b.fc.directives
	if b.params == nil {
		b.params = map[*check.Var]*irExpr{}
	}
	for v, e := range vals {
		b.params[v] = e
	}
	b.l.info, b.fc.directives = law.Pkg.Info, false
	b.keep++
	defer func() {
		b.l.info, b.fc.directives = savedInfo, savedDirectives
		b.keep--
		for v := range vals {
			delete(b.params, v)
		}
	}()
	return b.value(side)
}

// shadow is the test build's site x: both sides at the matched operands,
// the left first, compared by v1.Shadow.
func (b *irBuilder) shadow(x *syntax.CallExpr, m rwMatch, at string) ([]*irStmt, *irExpr, bool) {
	p := b.pos(x, x.Pos())
	t := b.l.info.Types[x]
	if t == nil {
		return nil, nil, false
	}
	gt := b.goType(t, x)
	pre, vals := b.bindOperands(m)
	lpre, left := b.lawSide(m.law, m.law.FuncRewrite.Lhs, vals)
	pre = append(pre, lpre...)
	lt := b.temp("left", gt, p)
	pre = append(pre, &irStmt{K: "def", Pos: p, Local: lt, X: left})
	rpre, right := b.lawSide(m.law, m.law.FuncRewrite.Rhs, vals)
	pre = append(pre, rpre...)
	key := LedgerNames(nil).funcLaw(m.law)
	if b.l.isSourced(m.law.Pkg) {
		if e, ok := b.l.canon.funcRw.ev.(*LedgerEvidence); ok {
			key = e.names.funcLaw(m.law)
		}
	}
	str := func(s string) *irExpr { return &irExpr{K: "const", Pos: p, T: "string", V: irString(s)} }
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
		{K: "local", Pos: p, I: lt}, right, str(key), str(at), str(b.l.nodeText(m.law.FuncRewrite.Lhs)),
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
