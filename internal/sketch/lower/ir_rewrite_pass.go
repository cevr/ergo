package lower

// IR pass: the rewrite over functions on the body's IR (LF6p; IR-FORMS
// §3.6, call facts and a statement slot). See ir_rewrite.go for the rule
// and the decision.
//
// The builder lowers every call as written and gives each call node its
// facts (irCallFacts): the source call it lowers and whether it is kept as
// written. rewritePass then walks the body before its forms are erased:
// every statement list, a function literal's too, and in each statement
// its expressions in Go's evaluation order. At each call node, outermost
// first, it tries the laws whose left side's IR has the node's head
// (rwPattern, built once per body by lowering the side with each
// parameter a typed hole). A match goes to rewriteAt.
//
// The statement slot. A site's statements (its operands' temporaries, a
// right side's own statements, a canary or a shadow) go right before the
// statement that holds it. That keeps the program's order only where the
// statement evaluates the site once (not a loop's condition or a case's
// value), the site is not under the right operand of && or ||, and
// nothing the statement evaluates before the site can fail (rwAt.total):
// so no defect moves. Anywhere else a site rewrites only when it needs no
// statement: its operands are locals or constants and its right side is a
// value.
//
// A right side the run build placed is walked again, so a law that
// matches it rewrites it too: a law's right side makes fewer calls than
// its left, so this ends.

import (
	"slices"
	"sync"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// irCallFacts are a call node's facts, in memory only.
type irCallFacts struct {
	// Site is the program's call the node lowers; nil for a call of a
	// law's side.
	Site *syntax.CallExpr
	// Keep says why the node is kept as written: keepAsWritten inside
	// AsWritten, keepCheck in a side a canary or a shadow compares.
	Keep string
	// Stmt is set for a call in statement position: a window's call
	// (ir_window.go), never a site of a law over functions: its right side
	// is a value, often no call, and the IR reader takes only a call as an
	// expression statement (toolchain/ir: "an expression statement is a
	// call").
	Stmt bool
}

const (
	keepAsWritten = "AsWritten"
	keepCheck     = "check"
)

// callFacts gives e, the call x as the builder lowered it, its facts;
// stmt reports a call in statement position.
func (b *irBuilder) callFacts(x *syntax.CallExpr, e *irExpr, stmt bool) {
	if !b.rwFuncs() && !b.rwWindowsOn() {
		return
	}
	f := &irCallFacts{Stmt: stmt}
	if b.lawDepth == 0 {
		f.Site = x
	}
	switch {
	case b.keep > 0:
		f.Keep = keepAsWritten
	case b.lawKeep > 0:
		f.Keep = keepCheck
	}
	e.CF = f
}

// setLocalType records local i's Go type, for a hole that is not a call's
// operand (rwMatch.hole).
func (b *irBuilder) setLocalType(i int, typ string) {
	if b.localTypes == nil {
		b.localTypes = map[int]string{}
	}
	b.localTypes[i] = typ
}

// rwPattern is a law's left side as the IR the builder lowers it to, each
// parameter a hole (K "hole", I its index in the law's parameters, T its
// Go type).
type rwPattern struct {
	law   *check.Law
	left  *irExpr
	holes []*irExpr
}

// rwMatch is a pattern matched at a site: each hole's operand, the holes
// in the order the site evaluates their operands, and the site's call.
type rwMatch struct {
	law   *check.Law
	holes []*irExpr
	vals  []*irExpr
	order []int
	site  *syntax.CallExpr
	// keep is set when a call of the site is inside AsWritten.
	keep bool
	// check is set when a call of the site is a checked side's.
	check bool
	// bind is a generic law's type parameters at the site, ops each
	// parameter's operand in the site's source (ir_fusion.go).
	bind rwBinding
	ops  map[*check.Var]syntax.Expr
}

// rwMark is a body's locals before a site's temporaries, and the number
// of declarations each open loop's pre-header holds (ir_memo.go).
type rwMark struct {
	locals, next int
	memos        []int
}

func (b *irBuilder) mark() rwMark {
	memos := make([]int, len(b.memos))
	for i, m := range b.memos {
		memos[i] = len(m.decls)
	}
	return rwMark{len(b.fn.Locals), b.next, memos}
}

// unmark drops the locals declared since m, and the pre-header
// declarations of the calls memoized since: a site that was not applied
// declares none, and no later local reuses the index of one it declared.
func (b *irBuilder) unmark(m rwMark) {
	b.fn.Locals, b.next = b.fn.Locals[:m.locals], m.next
	for i, n := range m.memos[:min(len(m.memos), len(b.memos))] {
		b.memos[i].decls = b.memos[i].decls[:n]
	}
}

// patterns are the scope's laws as patterns, by the head of their left
// side (rwHead), each head's in declaration order.
func (b *irBuilder) patterns() map[string][]*rwPattern {
	if b.rwPatterns != nil {
		return b.rwPatterns
	}
	b.rwPatterns = map[string][]*rwPattern{}
	for _, law := range b.l.scopeLaws() {
		if law.TopRewrite().OverFuncs {
			continue // decided at the call, from its source (ir_fusion.go fuseAt)
		}
		if p := b.pattern(law); p != nil {
			h := rwHead(p.left)
			b.rwPatterns[h] = append(b.rwPatterns[h], p)
		}
	}
	return b.rwPatterns
}

// pattern is law's left side lowered with each parameter a hole, or nil
// when the body cannot lower it as one value (the law matches nothing
// here). The pattern is never emitted: the locals it declared are
// dropped, and its nodes take no position, so the law's file joins the IR
// file's table only where a site places a side.
func (b *irBuilder) pattern(law *check.Law) (p *rwPattern) {
	m, at := b.mark(), b.at
	b.at = &irPos{}
	defer func() { b.unmark(m); b.at = at }()
	defer func() {
		if r := recover(); r != nil {
			if _, isNo := r.(irUnsupported); !isNo {
				panic(r)
			}
			p = nil
		}
	}()
	p = &rwPattern{law: law}
	vals := map[*check.Var]*irExpr{}
	for i, v := range law.Params {
		h := &irExpr{K: "hole", I: i, T: b.goType(v.Type, law.TopRewrite().Lhs)}
		p.holes = append(p.holes, h)
		vals[v] = h
	}
	pre, left := b.lawSide(law, law.TopRewrite().Lhs, vals, true)
	if len(pre) > 0 || left == nil || left.CF == nil {
		return nil
	}
	p.left = left
	return p
}

// rwHead is a call node's head: its kind and the Go name it calls.
func rwHead(e *irExpr) string {
	return e.K + "\x00" + e.Name + "\x00" + e.T + "\x00" + e.V
}

// match matches the pattern node p against the site's node e; typed
// reports an operand whose Go type its parent fixes (a call's operand).
func (b *irBuilder) match(m *rwMatch, p, e *irExpr, typed bool) bool {
	if p == nil || e == nil {
		return p == e
	}
	if p.K == "hole" {
		return b.hole(m, p, e, typed)
	}
	if e.CF != nil {
		m.keep = m.keep || e.CF.Keep == keepAsWritten
		m.check = m.check || e.CF.Keep == keepCheck
	}
	if p.K != e.K || p.Name != e.Name || p.T != e.T || p.V != e.V || p.Op != e.Op || p.I != e.I ||
		p.K == "local" || p.Body != nil || e.Body != nil ||
		len(p.Args) != len(e.Args) || len(p.Elems) != len(e.Elems) || !slices.Equal(p.Keys, e.Keys) {
		return false
	}
	call := p.K == "call" || p.K == "pcall" || p.K == "mcall"
	if !b.match(m, p.X, e.X, p.K == "mcall") || !b.match(m, p.Y, e.Y, false) {
		return false
	}
	for i := range p.Args {
		if !b.match(m, p.Args[i], e.Args[i], call) {
			return false
		}
	}
	for i := range p.Elems {
		if !b.match(m, p.Elems[i], e.Elems[i], false) {
			return false
		}
	}
	return true
}

// hole binds the hole p to the operand e: an operand of the hole's Go
// type (a call's operand has its parameter's; elsewhere only a constant or
// a local of that type is known to), and at a second occurrence the same
// local or constant.
func (b *irBuilder) hole(m *rwMatch, p, e *irExpr, typed bool) bool {
	if prev := m.vals[p.I]; prev != nil {
		return prev.K == e.K && (e.K == "local" && prev.I == e.I || e.K == "const" && prev.T == e.T && prev.V == e.V && prev.Op == e.Op)
	}
	known := typed || e.K == "const" && e.T == p.T || e.K == "local" && b.localTypes[e.I] == p.T
	if !known {
		return false
	}
	m.vals[p.I] = e
	m.order = append(m.order, p.I)
	return true
}

// matchAt is the first law whose left side matches the call node e, in
// the order its package declares them.
func (b *irBuilder) matchAt(e *irExpr, site *syntax.CallExpr) (rwMatch, bool) {
	if e.CF == nil || e.CF.Stmt {
		return rwMatch{}, false
	}
	for _, p := range b.patterns()[rwHead(e)] {
		m := rwMatch{law: p.law, holes: p.holes, vals: make([]*irExpr, len(p.holes)), site: e.CF.Site}
		if m.site == nil {
			m.site = site
		}
		if b.match(&m, p.left, e, false) && !m.check {
			return m, true
		}
	}
	return rwMatch{}, false
}

// rwAt is where in a statement the walk is.
type rwAt struct {
	// slot collects the statements that go before the statement; nil
	// where the statement may evaluate the site more than once or never.
	slot *[]*irStmt
	// cond is set under the right operand of && or ||.
	cond bool
	// total reports that nothing the statement evaluated before here can
	// fail.
	total bool
	// site is the program's call a right side being walked replaced.
	site *syntax.CallExpr
}

// placed reports that statements may go before the site.
func (at *rwAt) placed() bool { return at.slot != nil && !at.cond && at.total }

// rwSlotted are the statements that evaluate their expressions once,
// before anything else they do.
var rwSlotted = sync.OnceValue(func() map[string]bool {
	return map[string]bool{
		"def": true, "def2": true, "tdef": true, "var": true, "set": true, "store": true, "ret": true, "expr": true,
		"discard": true, "panic": true, "if": true, "switch": true, "tswitch": true, "match": true, "range": true,
	}
})

// rwFuncs reports a lowering with laws over functions in its scope.
func (b *irBuilder) rwFuncs() bool {
	return b.l.canon.funcRw != nil && len(b.l.scopeLaws()) > 0
}

// rewritePass rewrites the body's windows (ir_window.go), then its sites
// (see the file comment).
func (b *irBuilder) rewritePass(body []*irStmt) []*irStmt {
	if b.st.census != nil || b.law != nil {
		return body
	}
	if b.rwWindowsOn() {
		body = b.windows(body)
	}
	if !b.rwFuncs() {
		return body
	}
	return b.rwList(body)
}

func (b *irBuilder) rwList(list []*irStmt) []*irStmt {
	var out []*irStmt
	for _, s := range list {
		out = append(out, b.rwStmt(s)...)
	}
	return out
}

// rwStmt is s after the statements its sites placed before it.
func (b *irBuilder) rwStmt(s *irStmt) []*irStmt {
	var slot []*irStmt
	at := &rwAt{total: true}
	if rwSlotted()[s.K] {
		at.slot = &slot
	}
	s.X = b.rwExpr(s.X, at)
	s.Y = b.rwExpr(s.Y, at)
	for _, c := range s.Cases {
		lazy := &rwAt{cond: true}
		for i, v := range c.Vals {
			c.Vals[i] = b.rwExpr(v, lazy)
		}
	}
	s.Then, s.Else, s.Body = b.rwList(s.Then), b.rwList(s.Else), b.rwList(s.Body)
	for _, c := range s.Cases {
		c.Body = b.rwList(c.Body)
	}
	// The slot's statements hold the operands the sites bound: their own
	// sites are rewritten there.
	return append(b.rwList(slot), s)
}

// rwExpr is e with its sites rewritten, outermost first.
func (b *irBuilder) rwExpr(e *irExpr, at *rwAt) *irExpr {
	if e == nil {
		return nil
	}
	if e.K == "func" {
		// A function literal's body runs when it is called: its own
		// statements, its own slots.
		e.Body = b.rwList(e.Body)
		return e
	}
	site := at.site
	for e.CF != nil {
		m, ok := b.matchAt(e, site)
		if !ok {
			break
		}
		pre, out, applied := b.rewriteAt(m, at)
		if !applied {
			break
		}
		if len(pre) > 0 {
			*at.slot = append(*at.slot, pre...) // rewriteAt placed them only where at.placed()
		}
		if b.l.canon.funcRw.test || b.l.canon.funcRw.every > 0 {
			// A shadow or a canary: its sides are the law's, as written.
			at.total = at.total && rwTotal(out, true)
			return out
		}
		e, site = out, m.site
	}
	saved := at.site
	at.site = site
	e.X = b.rwExpr(e.X, at)
	if e.K == "bin" && (e.Op == "&&" || e.Op == "||") {
		cond := at.cond
		at.cond = true
		e.Y = b.rwExpr(e.Y, at)
		at.cond = cond
	} else {
		e.Y = b.rwExpr(e.Y, at)
	}
	for i, a := range e.Args {
		e.Args[i] = b.rwExpr(a, at)
	}
	for i, a := range e.Elems {
		e.Elems[i] = b.rwExpr(a, at)
	}
	at.site = saved
	at.total = at.total && rwTotal(e, false)
	return e
}

// rwTotal reports e's own step (and, deep, its operands') as one that
// cannot fail: no call, index, assertion or dereference, and no division
// or shift.
func rwTotal(e *irExpr, deep bool) bool {
	if e == nil {
		return true
	}
	switch e.K {
	case "const", "local", "global", "nil", "zero", "field", "addr", "un", "toiface", "lit", "len", "conv", "func", "append", "variant":
	case "bin":
		switch e.Op {
		case "/", "%", "<<", ">>":
			return false
		}
	default:
		return false
	}
	if !deep {
		return true
	}
	if !rwTotal(e.X, true) || !rwTotal(e.Y, true) {
		return false
	}
	for _, a := range slices.Concat(e.Args, e.Elems) {
		if !rwTotal(a, true) {
			return false
		}
	}
	return true
}
