package lower

// The merge pass (LAWFFI LF3b; plan-r1 §2 `pure`: "equal pure calls
// merge"), a pass over a built body before its forms are erased (IR-FORMS:
// passes run on the forms). See ir.go.
//
// A call of a pure callee carries its facts on the node (irExpr.C, set by
// the builder's call). A later call equal to one whose value a `def` bound
// reads that local instead of calling again, when the def runs first on
// every path to it and nothing writes the values involved:
//
//	v := f(a, 1)          v := f(a, 1)
//	…                 ⇒   …
//	w := f(a, 1) + 2      w := v + 2
//
// Equal: the same callee and the same operands, each a constant or a
// local. The def runs first: the later call is after it in the same list,
// or inside a later statement of it; a def inside a branch or a loop body
// serves only that list, and a labelled block (a goto's target) ends what
// came before it. Nothing writes: each operand local and the def's local
// is bound once and never assigned, its address never taken, in the whole
// function (a loop's own variable, which only its header sets, counts as
// bound once), and is of a basic Go type (a number, a bool, a string), as
// the result is. Pure calls are functions of their operands, so the value
// is the call's; the earlier call ran first, so any defect it raises comes
// first too. No statement moves. A function literal's body is a region of
// its own.
//
// -X lower.pureMerge=off turns it off: the reference, the IR without the
// pass. When LAWFFI LF2d's ledger gates the pure tiers, the facts read
// them through check.PureStd (pureCallee, ir_memo.go).

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"sync"
)

// pureMerge, "off", turns the merge pass off: set with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.pureMerge=off.
var pureMerge string

// SetPureMerge sets the merge pass on or off for the lowerings that follow
// and returns what restores the setting before it: the reference of
// TestPureMergeAgrees (internal/experimental/irspike).
func SetPureMerge(on bool) (restore func()) {
	saved := pureMerge
	pureMerge = "off"
	if on {
		pureMerge = ""
	}
	return func() { pureMerge = saved }
}

// irCall is a call's facts, in memory only: its source site, for the
// forms report.
type irCall struct {
	site *syntax.CallExpr
	// fn is the callee (the fold pass reads a Go function's path and
	// name; ir_fold.go).
	fn *check.Func
}

// The merge pass's reasons (ir_forms_report.go).
var (
	mergeFired     = &FormReason{"call", "merge", true, "reads the value of an equal call before it"}
	mergeWritten   = &FormReason{"call", "merge", false, "an operand or the earlier value is written in the function, or not of a basic type"}
	mergeNotValue  = &FormReason{"call", "merge", false, "the equal call before it is not a whole value"}
	mergeNotFirst  = &FormReason{"call", "merge", false, "the equal call before it does not run first on every path"}
)

// markPure gives the call e of x its facts when its callee is pure: a
// pure std body or reviewed-pure Go function (check.PureStd), or an Ergo
// function the lowering emits pure.
func (b *irBuilder) markPure(x *syntax.CallExpr, e *irExpr) {
	if e == nil || (e.K != "call" && e.K != "pcall") || e.X != nil || x.Trailing != nil {
		return
	}
	c := b.l.info.Calls[x]
	if c == nil || c.Kind != check.CallFunc || c.Func == nil || c.Recv != nil || c.StartsTask || c.Site != nil {
		return
	}
	if b.pureCallee(c.Func) { // ir_memo.go
		e.C = &irCall{site: x, fn: c.Func}
	}
}

// mergeEntry is a def of a pure call's value that later calls may read.
type mergeEntry struct {
	local int
	call  *irExpr
}

// merger is one run of the pass over a function's body.
type merger struct {
	b *irBuilder
	// fixed are the locals bound once and never written, of basic types.
	fixed map[int]bool
	// seen are the pure calls met so far in the walk, with whether a def
	// bound them, for the report's reasons.
	seen []mergeSeen
	n    int
}

type mergeSeen struct {
	call  *irExpr
	value bool
}

// mergeCalls is body with each later equal pure call reading the value of
// an earlier def of it.
func (b *irBuilder) mergeCalls(body []*irStmt) []*irStmt {
	if pureMerge == "off" {
		return body
	}
	m := &merger{b: b, fixed: b.fixedLocals(body)}
	m.list(body, nil)
	b.mergeSites += m.n
	return body
}

// list walks list with the entries avail of the lists around it, adding
// the defs it holds for the statements after them.
func (m *merger) list(list []*irStmt, avail []mergeEntry) {
	avail = avail[:len(avail):len(avail)]
	for _, s := range list {
		if s.Label != "" && s.K == "block" {
			// A goto may reach the block from before an entry.
			avail = nil
		}
		m.stmt(s, avail)
		if s.K == "def" && s.X != nil && s.X.C != nil && m.fixed[s.Local] && m.argsFixed(s.X) {
			avail = append(avail, mergeEntry{s.Local, s.X})
		}
	}
}

// stmt rewrites the calls of s, whose nested lists get avail.
func (m *merger) stmt(s *irStmt, avail []mergeEntry) {
	if s.K == "def" && s.X != nil && s.X.C != nil {
		// The def's own call: it may read an entry before it, and is seen
		// as a value.
		s.X = m.expr(s.X, avail, true)
	} else {
		s.X = m.expr(s.X, avail, false)
	}
	s.Y = m.expr(s.Y, avail, false)
	m.list(s.Then, avail)
	m.list(s.Else, avail)
	m.list(s.Body, avail)
	for _, c := range s.Cases {
		for i, v := range c.Vals {
			c.Vals[i] = m.expr(v, avail, false)
		}
		m.list(c.Body, avail)
	}
}

// expr is x with each pure call equal to an entry of avail replaced by
// its local; value marks x as the whole value of a def.
func (m *merger) expr(x *irExpr, avail []mergeEntry, value bool) *irExpr {
	if x == nil {
		return nil
	}
	if x.K == "func" {
		// A function literal is a region of its own.
		m.list(x.Body, nil)
		return x
	}
	x.X = m.expr(x.X, avail, false)
	x.Y = m.expr(x.Y, avail, false)
	for i, a := range x.Args {
		x.Args[i] = m.expr(a, avail, false)
	}
	for i, el := range x.Elems {
		x.Elems[i] = m.expr(el, avail, false)
	}
	if x.C == nil {
		return x
	}
	for _, e := range avail {
		if samePureCall(e.call, x) {
			m.b.formSite(x.C.site, mergeFired) // ir_forms_report.go
			m.n++
			return &irExpr{K: "local", Pos: x.Pos, I: e.local}
		}
	}
	if r := m.why(x); r != nil {
		m.b.formSite(x.C.site, r)
	}
	m.seen = append(m.seen, mergeSeen{x, value})
	return x
}

// why is the reason a call equal to one seen before reads no entry, or nil
// when none was seen.
func (m *merger) why(x *irExpr) *FormReason {
	var r *FormReason
	for _, s := range m.seen {
		if !samePureCall(s.call, x) {
			continue
		}
		switch {
		case !m.argsFixed(x):
			return mergeWritten
		case !s.value:
			r = mergeNotValue
		case r == nil:
			r = mergeNotFirst
		}
	}
	return r
}

// argsFixed reports a call whose operand locals are all fixed.
func (m *merger) argsFixed(x *irExpr) bool {
	for _, a := range x.Args {
		if a.K == "local" && !m.fixed[a.I] {
			return false
		}
	}
	return true
}

// samePureCall reports two pure calls of one callee on equal operands, each a
// constant or a local.
func samePureCall(a, c *irExpr) bool {
	if a.K != c.K || a.Name != c.Name || a.T != c.T || len(a.Args) != len(c.Args) || a.X != nil || c.X != nil {
		return false
	}
	for i, x := range a.Args {
		y := c.Args[i]
		switch {
		case x.K == "local" && y.K == "local" && x.I == y.I:
		case x.K == "const" && y.K == "const" && x.T == y.T && x.V == y.V:
		default:
			return false
		}
	}
	return true
}

// fixedLocals are the locals of body bound once and never written, of
// basic Go types: a parameter (D-D1: only a let mut is assigned), a def,
// or a loop's variable, which only its header sets.
func (b *irBuilder) fixedLocals(body []*irStmt) map[int]bool {
	binds, written := map[int]int{}, map[int]bool{}
	var stmts func([]*irStmt)
	var expr func(*irExpr)
	expr = func(x *irExpr) {
		if x == nil {
			return
		}
		if x.K == "addr" {
			root := x.X
			for root != nil && root.K != "local" {
				root = root.X
			}
			if root != nil {
				written[root.I] = true
			}
		}
		expr(x.X)
		expr(x.Y)
		for _, a := range x.Args {
			expr(a)
		}
		for _, e := range x.Elems {
			expr(e)
		}
		for _, p := range x.Params {
			binds[p]++
		}
		stmts(x.Body)
	}
	stmts = func(list []*irStmt) {
		for _, s := range list {
			switch s.K {
			case "def":
				binds[s.Local]++
			case "set", "var":
				written[s.Local] = true
			case "count":
				binds[s.Local]++
			case "range":
				for _, k := range []*int{s.Key, s.Val} {
					if k != nil {
						binds[*k]++
					}
				}
			case "tdef":
				for _, l := range s.Ls {
					written[l] = true
				}
			case "store":
				root := s.X
				for root != nil && root.K != "local" {
					root = root.X
				}
				if root != nil {
					written[root.I] = true
				}
			}
			expr(s.X)
			expr(s.Y)
			stmts(s.Then)
			stmts(s.Else)
			stmts(s.Body)
			for _, c := range s.Cases {
				for _, v := range c.Vals {
					expr(v)
				}
				stmts(c.Body)
			}
		}
	}
	stmts(body)
	params := b.next - len(b.fn.Locals)
	byIndex := map[int]*check.Var{}
	for v, i := range b.locals {
		byIndex[i] = v
	}
	fixed := map[int]bool{}
	for i := 0; i < b.next; i++ {
		if written[i] || binds[i] > 1 {
			continue
		}
		switch {
		case i < params:
			if v := byIndex[i]; v != nil && binds[i] == 0 && irMemoBasic(v.Type) { // ir_memo.go
				fixed[i] = true
			}
		case binds[i] == 1 && irBasicGo()[b.fn.Locals[i-params].Type]:
			fixed[i] = true
		}
	}
	return fixed
}

// irBasicGo are the Go types of the values a merged call may take and
// give.
var irBasicGo = sync.OnceValue(func() map[string]bool {
	return map[string]bool{
		"bool": true, "string": true, "int": true, "int8": true, "int16": true, "int32": true, "int64": true,
		"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true, "uintptr": true,
		"float32": true, "float64": true, "byte": true, "rune": true,
	}
})
