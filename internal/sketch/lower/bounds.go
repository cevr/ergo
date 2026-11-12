package lower

import (
	"go/constant"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Bounds facts and unchecked loads (BCE1; owner q112, 2026-11-11;
// docs/spec/facts.md, Evidence 1 "checked" and Lowering 3). A comparison
// that dominates an index or a slice in the same body can prove the
// access in range where Go's prove pass cannot: Go reads `p+128 <=
// len(s)` as a fact about a sum that may wrap, and does not carry the
// margin over to `s[p+8:p+16]`. Ergo's integers are Go's, so they wrap
// too; the facts below are taken only where no wrap can make them false.
// The run build then reads the element or the substring through a
// reviewed body of runtime/v1 (unchecked.go) instead of Go's checked
// form. Only a proved fact changes code (q115), and the lowering reads
// the comparison's form, never a name (internal/residue,
// TestLoweringKeysOnNoStdName).
//
// A fact bounds one int local or parameter, its subject: below by a
// constant, above by a constant or by len(S)+h, h <= 0, for a string or
// slice local S. Each conjunct of an if's or a for's condition gives facts
// for its body, in order, so a later conjunct may rest on an earlier one:
//   - v <= R, v < R, R >= v, R > v and the mirrored forms for a lower
//     bound, where R is a constant or len(S) minus a constant, and v may
//     carry a constant, v+k, when the facts before it prove that v+k does
//     not wrap;
//   - uint(v+k) < uint(R) and <=, where R is a constant or len(S): Go's
//     one-compare range check, 0 <= v+k < R, which cannot wrap.
//
// `p+128 <= len(s)` alone proves nothing: for p near the largest int the
// sum wraps below zero and the comparison holds. `0 <= p && p <=
// len(s)-128` proves p+k in range for 0 <= k < 128, since len(s)-128
// cannot wrap.
//
// A fact holds in its branch until its subject is written: an assignment
// rooted at v or S kills every fact that names it, for the rest of the
// body; a loop kills, at its head, the facts over every local its body
// writes. A let mut that a function literal uses, or that is borrowed
// (&x, or the receiver of a dot call), is never a subject (unstableVars),
// since a call may write it; a let and a parameter are never written. A
// fact does not enter a function literal or an inlined body: each has a
// frame of its own (a fresh funcCtx). The test build keeps Go's checks
// (facts.md, Lowering 3), and factLowering "checked" is the reference,
// which emits no unchecked load.
//
// An access is unchecked when its index i has 0 <= i and i <= len(S)-1,
// or, for s[a:b] of a string, 0 <= a, b <= len(s) and a <= b, where a <= b
// holds when a and b are one sum of locals with constants in order, or
// when a's constant upper bound is at most b's lower one. Every sum is
// checked step by step: a step whose bounds could pass the int range
// proves nothing.
//
// TestBoundsFactsSound checks on generated guards that no site is
// unchecked whose access a state that passes the guard puts out of range,
// and that the sites the rule covers are unchecked; TestBoundsFactsAgree
// builds both lowerings and runs them.
//
// Prior art: Go's prove pass (ssa/prove.go) derives the same facts from
// dominating branches, but keeps them only where it can rule the wrap
// out, and never across a margin; HotSpot's range check elimination and
// LLVM's IRCE remove checks inside loops from ranges of the induction
// variable; SPARK's GNATprove removes a run-time check only when it proves
// it. Gleam and Elixir check every access on the BEAM, which has no
// unchecked read; ReScript compiles to JavaScript, whose reads are
// bounds-checked by the engine, and offers the trusted Array.getUnsafe.
var factLowering string

// boundLimit is the largest magnitude of a constant or a constant bound a
// fact keeps, so a sum of a few of them never wraps an int, of 32 bits or
// 64.
const boundLimit = 1 << 30

// boundFact is one fact: v >= k (lower), or v <= k (upper, of nil), or
// v <= len(of)+k (upper, k <= 0).
type boundFact struct {
	v     *check.Var
	upper bool
	k     int64
	of    *check.Var
	dead  bool
}

// span is what the facts say of an int expression: lo <= x when hasLo,
// x <= hi when hasHi, x <= len(S)+h for each S in sym, and its form as a
// sum of terms and a constant; opaque when a part of it is no term (a
// mask), so its form says nothing.
type span struct {
	lo, hi       int64
	hasLo, hasHi bool
	sym          map[*check.Var]int64
	terms        []term
	k            int64
	opaque       bool
}

// term is one local of a sum, or len of one.
type term struct {
	v     *check.Var
	isLen bool
}

// uncheckedOK reports whether this build may write unchecked loads.
func (fc *funcCtx) uncheckedOK() bool {
	return factLowering != "checked" && fc.l.tests == nil
}

// factMark is the length of the fact list, to reset to after a branch.
func (fc *funcCtx) factMark() int { return len(fc.bounds) }

// factReset drops the facts of a branch that ends.
func (fc *funcCtx) factReset(n int) { fc.bounds = fc.bounds[:n] }

// assume adds the facts cond gives where it holds.
func (fc *funcCtx) assume(cond syntax.Expr) {
	if !fc.uncheckedOK() {
		return
	}
	cond = unparen(cond)
	if b, ok := cond.(*syntax.BinaryExpr); ok && b.Op == syntax.LAND {
		fc.assume(b.X)
		fc.assume(b.Y)
		return
	}
	b, ok := cond.(*syntax.BinaryExpr)
	if !ok {
		return
	}
	x, y, op := b.X, b.Y, b.Op
	switch op {
	case syntax.GTR, syntax.GEQ:
		x, y = y, x
		if op == syntax.GTR {
			op = syntax.LSS
		} else {
			op = syntax.LEQ
		}
	case syntax.LSS, syntax.LEQ:
	default:
		return
	}
	// x op y, op one of < and <=.
	strict := int64(0)
	if op == syntax.LSS {
		strict = 1
	}
	if ux, ok := fc.uintOf(x); ok {
		if uy, ok := fc.uintOf(y); ok {
			fc.assumeRange(ux, uy, strict)
		}
		return
	}
	// An upper bound on a local: v+k <= R.
	if v, k, ok := fc.oneVar(x); ok && fc.exact(x) {
		if r, ok := fc.limit(y); ok {
			fc.addBound(&boundFact{v: v, upper: true, k: r.k - k - strict, of: r.of})
		}
	}
	// A lower bound on a local: K <= v+k.
	if v, k, ok := fc.oneVar(y); ok && fc.exact(y) {
		if c, ok := fc.constOf(x); ok {
			fc.addBound(&boundFact{v: v, k: c - k + strict})
		}
	}
}

// exact reports that x, a local or a sum v+k, is computed with no wrap:
// a local is its value, and a sum needs facts that bound it (spanOf).
func (fc *funcCtx) exact(x syntax.Expr) bool {
	if _, ok := fc.intVar(x); ok {
		return true
	}
	_, ok := fc.spanOf(x)
	return ok
}

// assumeRange adds the facts of uint(x) < uint(y) (strict 1) or <=: x is
// v+k, y a constant K >= 0 or len(S). The uint compare holds exactly when
// 0 <= v+k < K, with v+k taken as Go computes it; since K and k are small,
// v itself is in -k .. K-k-1.
func (fc *funcCtx) assumeRange(x, y syntax.Expr, strict int64) {
	v, k, ok := fc.oneVar(x)
	if !ok {
		return
	}
	if c, ok := fc.constOf(y); ok {
		if c < 0 {
			return
		}
		fc.addBound(&boundFact{v: v, k: -k})
		fc.addBound(&boundFact{v: v, upper: true, k: c - k - strict})
		return
	}
	if s, ok := fc.lenOf(y); ok {
		fc.addBound(&boundFact{v: v, k: -k})
		if h := -k - strict; h <= 0 {
			fc.addBound(&boundFact{v: v, upper: true, k: h, of: s})
		}
	}
}

// assumeLet adds the facts of a let of an int local: what the facts in
// force say of its value. The local holds that value until it is written,
// which kills them.
func (fc *funcCtx) assumeLet(v *check.Var, value syntax.Expr) {
	if !fc.uncheckedOK() || !isInt(v.Type) || !fc.stable(v) {
		return
	}
	s, ok := fc.spanOf(value)
	if !ok {
		return
	}
	if s.hasLo {
		fc.addBound(&boundFact{v: v, k: s.lo})
	}
	if s.hasHi {
		fc.addBound(&boundFact{v: v, upper: true, k: s.hi})
	}
	for of, h := range s.sym {
		fc.addBound(&boundFact{v: v, upper: true, k: h, of: of})
	}
}

// addBound adds b when its constant is within boundLimit.
func (fc *funcCtx) addBound(b *boundFact) {
	if b.k > boundLimit || b.k < -boundLimit || b.of != nil && b.k > 0 {
		return
	}
	fc.bounds = append(fc.bounds, b)
}

// kill ends every fact that names v, for the rest of the body.
func (fc *funcCtx) kill(v *check.Var) {
	for _, b := range fc.bounds {
		if b.v == v || b.of == v {
			b.dead = true
		}
	}
}

// killWritten kills the facts over every local n writes: a loop's body,
// at the loop's head.
func (fc *funcCtx) killWritten(n syntax.Node) {
	if len(fc.bounds) == 0 {
		return
	}
	syntax.Inspect(n, func(n syntax.Node) bool {
		if a, ok := n.(*syntax.AssignStmt); ok {
			for _, x := range a.Lhs {
				if v := fc.rootVar(x); v != nil {
					fc.kill(v)
				}
			}
		}
		return true
	})
}

// killAssigned kills the facts over the local an assignment writes.
func (fc *funcCtx) killAssigned(s *syntax.AssignStmt) {
	for _, x := range s.Lhs {
		if v := fc.rootVar(x); v != nil {
			fc.kill(v)
		}
	}
}

// rootVar is the local a place is rooted at, or nil.
func (fc *funcCtx) rootVar(x syntax.Expr) *check.Var { return rootOf(fc.l.info, x) }

// rootOf is the local a place is rooted at, by info's uses, or nil.
func rootOf(info *check.Info, x syntax.Expr) *check.Var {
	for {
		switch e := x.(type) {
		case *syntax.ParenExpr:
			x = e.X
		case *syntax.SelectorExpr:
			x = e.X
		case *syntax.IndexExpr:
			x = e.X
		case *syntax.SliceExpr:
			x = e.X
		case *syntax.StarExpr:
			x = e.X
		case *syntax.Ident:
			v, _ := info.Uses[e].(*check.Var)
			return v
		default:
			return nil
		}
	}
}

// unstableVars are the let mut locals of the package that a call may
// write: those a function literal uses, and those borrowed, by &x or as
// the receiver of a dot call. A bounds fact never names one.
func (l *lowerer) unstableVars() map[*check.Var]bool {
	info := l.info
	if u, ok := l.unstable[info]; ok {
		return u
	}
	u := map[*check.Var]bool{}
	mark := func(n syntax.Node) {
		syntax.Inspect(n, func(n syntax.Node) bool {
			if id, ok := n.(*syntax.Ident); ok {
				if v, ok := info.Uses[id].(*check.Var); ok && v.Mut() {
					u[v] = true
				}
			}
			return true
		})
	}
	for n := range info.Literals {
		mark(n)
	}
	root := func(x syntax.Expr) {
		if v := rootOf(info, x); v != nil && v.Mut() {
			u[v] = true
		}
	}
	for x := range info.Types {
		switch e := x.(type) {
		case *syntax.UnaryExpr:
			if e.Op == syntax.AND {
				root(e.X)
			}
		case *syntax.SelectorExpr:
			root(e.X)
		}
	}
	for call := range info.Calls {
		if sel, ok := unparen(call.Fun).(*syntax.SelectorExpr); ok {
			root(sel.X)
		}
	}
	if l.unstable == nil {
		l.unstable = map[*check.Info]map[*check.Var]bool{}
	}
	l.unstable[info] = u
	return u
}

// stable reports whether a fact may name v: a local or parameter of this
// Go function that no call can write.
func (fc *funcCtx) stable(v *check.Var) bool {
	if v == nil {
		return false
	}
	if _, local := fc.locals[v]; !local {
		return false
	}
	return !v.Mut() || !fc.l.unstableVars()[v]
}

// intVar is the stable int local x names.
func (fc *funcCtx) intVar(x syntax.Expr) (*check.Var, bool) {
	id, ok := unparen(x).(*syntax.Ident)
	if !ok {
		return nil, false
	}
	v, ok := fc.l.info.Uses[id].(*check.Var)
	if !ok || !fc.stable(v) || !isInt(v.Type) {
		return nil, false
	}
	return v, true
}

// isGoString reports a type whose Go form is string: string, or std fs's
// Text, a mapped text (types.md, Borrows 8), which lowers to string.
func isGoString(t check.Type) bool {
	if check.IsMappedText(t) {
		return true
	}
	b, ok := t.(*check.Basic)
	return ok && b.Kind == check.String
}

// isInt reports Go's int.
func isInt(t check.Type) bool {
	b, ok := t.(*check.Basic)
	return ok && b.Kind == check.Int
}

// constOf is x's value when x is an int constant within boundLimit: a
// constant expression, or a package let lowered as a Go constant.
func (fc *funcCtx) constOf(x syntax.Expr) (int64, bool) {
	if !isInt(fc.l.info.Types[x]) && !isInt(fc.typeOrNil(x)) {
		return 0, false
	}
	val, ok := fc.l.info.Values[x]
	if !ok {
		var id *syntax.Ident
		switch e := unparen(x).(type) {
		case *syntax.Ident:
			id = e
		case *syntax.SelectorExpr:
			id = e.Sel
		}
		if id == nil {
			return 0, false
		}
		v, isVar := fc.l.info.Uses[id].(*check.Var)
		if !isVar {
			return 0, false
		}
		val, ok = fc.l.constVals[v]
		if !ok || val == nil {
			return 0, false
		}
	}
	n, exact := constant.Int64Val(val)
	if !exact || n > boundLimit || n < -boundLimit {
		return 0, false
	}
	return n, true
}

// typeOrNil is x's checked type, or nil.
func (fc *funcCtx) typeOrNil(x syntax.Expr) check.Type {
	t, _ := fc.typed(x)
	return t
}

// subject is the stable string or slice local x names, the S of len(S).
func (fc *funcCtx) subject(x syntax.Expr) (*check.Var, bool) {
	id, ok := unparen(x).(*syntax.Ident)
	if !ok {
		return nil, false
	}
	v, ok := fc.l.info.Uses[id].(*check.Var)
	if !ok || !fc.stable(v) {
		return nil, false
	}
	if isGoString(v.Type) {
		return v, true
	}
	switch v.Type.(type) {
	case *check.Slice:
		return v, true
	}
	return nil, false
}

// lenOf is S when x is len(S).
func (fc *funcCtx) lenOf(x syntax.Expr) (*check.Var, bool) {
	call, ok := unparen(x).(*syntax.CallExpr)
	if !ok || len(call.Args) != 1 {
		return nil, false
	}
	if c := fc.l.info.Calls[call]; c == nil || c.Kind != check.CallLen {
		return nil, false
	}
	return fc.subject(call.Args[0])
}

// uintOf is e when x is uint(e) of an int e.
func (fc *funcCtx) uintOf(x syntax.Expr) (syntax.Expr, bool) {
	call, ok := unparen(x).(*syntax.CallExpr)
	if !ok || len(call.Args) != 1 {
		return nil, false
	}
	c := fc.l.info.Calls[call]
	if c == nil || c.Kind != check.CallConversion {
		return nil, false
	}
	if b, ok := fc.typeOrNil(x).(*check.Basic); !ok || b.Kind != check.Uint {
		return nil, false
	}
	if !isInt(fc.typeOrNil(call.Args[0])) {
		return nil, false
	}
	return call.Args[0], true
}

// limitOf is R of a fact: a constant (of nil), or len(S) minus a constant.
type limitOf struct {
	of *check.Var
	k  int64
}

// limit reads x as a limit: a constant, len(S), or len(S)-c with c >= 0,
// which cannot wrap.
func (fc *funcCtx) limit(x syntax.Expr) (limitOf, bool) {
	if c, ok := fc.constOf(x); ok {
		return limitOf{k: c}, true
	}
	if s, ok := fc.lenOf(x); ok {
		return limitOf{of: s}, true
	}
	b, ok := unparen(x).(*syntax.BinaryExpr)
	if !ok || b.Op != syntax.SUB {
		return limitOf{}, false
	}
	s, ok := fc.lenOf(b.X)
	if !ok {
		return limitOf{}, false
	}
	c, ok := fc.constOf(b.Y)
	if !ok || c < 0 {
		return limitOf{}, false
	}
	return limitOf{of: s, k: -c}, true
}

// oneVar reads x as v+k: one stable int local and a constant.
func (fc *funcCtx) oneVar(x syntax.Expr) (*check.Var, int64, bool) {
	x = unparen(x)
	if v, ok := fc.intVar(x); ok {
		return v, 0, true
	}
	b, ok := x.(*syntax.BinaryExpr)
	if !ok || b.Op != syntax.ADD && b.Op != syntax.SUB {
		return nil, 0, false
	}
	v, k, ok := fc.oneVar(b.X)
	if !ok {
		return nil, 0, false
	}
	c, ok := fc.constOf(b.Y)
	if !ok {
		return nil, 0, false
	}
	if b.Op == syntax.SUB {
		c = -c
	}
	return v, k + c, true
}

// noWrap reports a span whose bounds keep it inside the int range: a
// constant upper bound within boundLimit, or an upper bound len(S)+h with
// h <= 0, and a lower bound within boundLimit.
func (s span) noWrap() bool {
	if !s.hasLo || s.lo < -boundLimit {
		return false
	}
	if s.hasHi && s.hi <= boundLimit {
		return true
	}
	for _, h := range s.sym {
		if h <= 0 {
			return true
		}
	}
	return false
}

// spanOf is what the live facts say of x, or false when x is not a sum
// of stable int locals, len of stable locals and constants, or when a
// step of the sum could wrap.
func (fc *funcCtx) spanOf(x syntax.Expr) (span, bool) {
	x = unparen(x)
	if c, ok := fc.constOf(x); ok {
		return span{lo: c, hi: c, hasLo: true, hasHi: true, k: c}, true
	}
	if v, ok := fc.intVar(x); ok {
		s := span{terms: []term{{v: v}}}
		for _, b := range fc.bounds {
			if b.dead || b.v != v {
				continue
			}
			switch {
			case !b.upper:
				if !s.hasLo || b.k > s.lo {
					s.lo, s.hasLo = b.k, true
				}
			case b.of == nil:
				if !s.hasHi || b.k < s.hi {
					s.hi, s.hasHi = b.k, true
				}
			default:
				if h, ok := s.sym[b.of]; !ok || b.k < h {
					if s.sym == nil {
						s.sym = map[*check.Var]int64{}
					}
					s.sym[b.of] = b.k
				}
			}
		}
		return s, true
	}
	if v, ok := fc.lenOf(x); ok {
		return span{lo: 0, hasLo: true, sym: map[*check.Var]int64{v: 0}, terms: []term{{v: v, isLen: true}}}, true
	}
	b, ok := x.(*syntax.BinaryExpr)
	if !ok || !isInt(fc.typeOrNil(x)) {
		return span{}, false
	}
	switch b.Op {
	case syntax.AND:
		// x & K with K >= 0 is in 0..K whatever x is.
		for _, m := range []syntax.Expr{b.X, b.Y} {
			if c, ok := fc.constOf(m); ok && c >= 0 {
				return span{lo: 0, hi: c, hasLo: true, hasHi: true, opaque: true}, true
			}
		}
		return span{}, false
	case syntax.ADD:
		l, ok := fc.spanOf(b.X)
		if !ok {
			return span{}, false
		}
		r, ok := fc.spanOf(b.Y)
		if !ok {
			return span{}, false
		}
		s := addSpans(l, r)
		return s, s.noWrap()
	case syntax.SUB:
		l, ok := fc.spanOf(b.X)
		if !ok {
			return span{}, false
		}
		c, ok := fc.constOf(b.Y)
		if !ok {
			return span{}, false
		}
		s := addSpans(l, span{lo: -c, hi: -c, hasLo: true, hasHi: true, k: -c})
		return s, s.noWrap()
	}
	return span{}, false
}

// addSpans is the span of a sum.
func addSpans(a, b span) span {
	s := span{k: a.k + b.k, opaque: a.opaque || b.opaque}
	s.terms = append(append(s.terms, a.terms...), b.terms...)
	if a.hasLo && b.hasLo {
		s.lo, s.hasLo = a.lo+b.lo, true
	}
	if a.hasHi && b.hasHi {
		s.hi, s.hasHi = a.hi+b.hi, true
	}
	add := func(sym map[*check.Var]int64, other span) {
		if !other.hasHi {
			return
		}
		for v, h := range sym {
			if cur, ok := s.sym[v]; !ok || h+other.hi < cur {
				if s.sym == nil {
					s.sym = map[*check.Var]int64{}
				}
				s.sym[v] = h + other.hi
			}
		}
	}
	add(a.sym, b)
	add(b.sym, a)
	return s
}

// within reports 0 <= x <= len(S)+h for the span of x.
func within(s span, of *check.Var, h int64) bool {
	if !s.hasLo || s.lo < 0 {
		return false
	}
	got, ok := s.sym[of]
	return ok && got <= h
}

// ordered reports a <= b for two spans: one sum of terms whose constants
// are in order, or a's constant upper bound at most b's lower one.
func ordered(a, b span) bool {
	if a.hasHi && b.hasLo && a.hi <= b.lo {
		return true
	}
	if a.opaque || b.opaque || len(a.terms) != len(b.terms) || a.k > b.k {
		return false
	}
	count := map[term]int{}
	for _, t := range a.terms {
		count[t]++
	}
	for _, t := range b.terms {
		count[t]--
		if count[t] < 0 {
			return false
		}
	}
	return true
}

// uncheckedIndex reports whether x, a read of S[i] of a string or a slice,
// is proved in range.
func (fc *funcCtx) uncheckedIndex(x *syntax.IndexExpr) bool {
	if !fc.uncheckedOK() || len(fc.bounds) == 0 || len(x.Indices) != 1 {
		return false
	}
	s, ok := fc.subject(x.X)
	if !ok {
		return false
	}
	i, ok := fc.spanOf(x.Indices[0])
	return ok && within(i, s, -1)
}

// sliceLen is the length of s[a:b] when a and b are one sum of locals
// whose constants differ by more than 0 (b-a, which needs no evaluation of
// b), else 0.
func (fc *funcCtx) sliceLen(x *syntax.SliceExpr) int64 {
	if x.Low == nil || x.High == nil {
		return 0
	}
	lo, ok := fc.spanOf(x.Low)
	if !ok {
		return 0
	}
	hi, ok := fc.spanOf(x.High)
	if !ok || !ordered(span{terms: lo.terms, k: lo.k, opaque: lo.opaque}, span{terms: hi.terms, k: hi.k, opaque: hi.opaque}) {
		return 0
	}
	return hi.k - lo.k
}

// uncheckedSlice reports whether x, s[a:b] of a string, is proved in
// range.
func (fc *funcCtx) uncheckedSlice(x *syntax.SliceExpr) bool {
	if !fc.uncheckedOK() || len(fc.bounds) == 0 || x.Slice3 {
		return false
	}
	s, ok := fc.subject(x.X)
	if !ok {
		return false
	}
	if !isGoString(s.Type) {
		return false
	}
	zero := span{lo: 0, hi: 0, hasLo: true, hasHi: true}
	lo, hi := zero, span{lo: 0, hasLo: true, sym: map[*check.Var]int64{s: 0}, terms: []term{{v: s, isLen: true}}}
	if x.Low != nil {
		if lo, ok = fc.spanOf(x.Low); !ok {
			return false
		}
	}
	if x.High != nil {
		if hi, ok = fc.spanOf(x.High); !ok {
			return false
		}
	}
	if !within(lo, s, 0) {
		return false
	}
	// lo <= len(s) is proved, so a cut to the end, s[a:] or s[a:len(s)],
	// is in order.
	if x.High == nil || len(hi.terms) == 1 && hi.terms[0] == (term{v: s, isLen: true}) && hi.k == 0 && !hi.opaque {
		return true
	}
	return within(hi, s, 0) && ordered(lo, hi)
}
