package lower

// IR builder: a zero value set that no read can see is dropped
// (IR-DEADZERO). Coalesce writes a copy's `var b`, renamed to the site's
// local a, as `set a <zero>` (ir_forms_inline.go coalesce): the
// declaration's zero value. Go keeps such a set when a is a record of five
// words or more (no SSA value; its dead store pass looks at one block) and
// writes it on every call. lru's release zeroed the taken node twice
// before ArenaTake wrote it (the copies of Free and vacate), 2 x 48 bytes
// per eviction, where the text path declares it once.
//
// deadZeros drops `set a <zero>` when, along a straight line from it, a
// set of a that does not read a comes before any statement naming a; and
// when, along a straight line from `var a`, it is the first statement
// naming a (the var's zero value already holds). A straight line is def,
// set, store, expr and discard statements and unlabeled blocks, walked
// into and on out of; any other statement (a branch, a jump, a label, a
// loop) ends it and keeps the zero. A local whose address is taken, or
// that a function literal names, keeps every zero: a read through a
// pointer is not a statement naming it.
//
// The reference is irDeadZeroLowering "off"
// (-ldflags=-X=ergo.local/prototype/internal/sketch/lower.irDeadZeroLowering=off).
//
// Prior art: SSA back ends' dead store elimination (LLVM's DSE, GHC's Cmm
// sinking); Go's deadstore pass does it within one block, and not for an
// aggregate auto it keeps in memory across blocks.

// irDeadZeroLowering is "off" for the reference: every zero set stays.
var irDeadZeroLowering string

// IRDeadZeroOn reports that dead zero value sets are dropped (the
// property's reach check).
func IRDeadZeroOn() bool { return irDeadZeroLowering != "off" }

// deadZeros applies the rule to body and every list nested in it,
// outside function literals.
func (in *irInliner) deadZeros(body []*irStmt) []*irStmt {
	if irDeadZeroLowering == "off" {
		return body
	}
	pinned := map[int]bool{}
	irWalk(body, false, func(*irStmt, bool) {}, func(e *irExpr, lit bool) {
		if e.K == "local" && lit {
			pinned[e.I] = true
		}
		if e.K == "addr" {
			r := e.X
			for r != nil && (r.K == "field" || r.K == "index") {
				r = r.X
			}
			if r != nil && r.K == "local" {
				pinned[r.I] = true
			}
		}
	})
	drop := map[*irStmt]bool{}
	var scan func([]*irStmt)
	scan = func(l []*irStmt) {
		for j, t := range l {
			switch {
			case irZeroSet(t) && !pinned[t.Local]:
				if w, _ := irStraight(l[j+1:], t.Local); w != nil && w.K == "set" && w.Op == "" && w.Local == t.Local {
					drop[t] = true // overwritten before any read
				}
			case t.K == "var" && !pinned[t.Local]:
				if w, _ := irStraight(l[j+1:], t.Local); w != nil && irZeroSet(w) {
					drop[w] = true // the var's own zero value holds
				}
			}
			scan(t.Then)
			scan(t.Else)
			scan(t.Body)
			for _, c := range t.Cases {
				scan(c.Body)
			}
		}
	}
	scan(body)
	var filter func([]*irStmt) []*irStmt
	filter = func(l []*irStmt) []*irStmt {
		var out []*irStmt
		for _, s := range l {
			if drop[s] {
				in.st.report.DeadZeros++
				continue
			}
			s.Then, s.Else, s.Body = filter(s.Then), filter(s.Else), filter(s.Body)
			for _, c := range s.Cases {
				c.Body = filter(c.Body)
			}
			out = append(out, s)
		}
		return out
	}
	return filter(body)
}

// irZeroSet reports `set x <zero>`.
func irZeroSet(s *irStmt) bool {
	return s.K == "set" && s.Op == "" && s.X != nil && s.X.K == "zero"
}

// irStraight walks list on a straight line and returns the first
// statement that names x, or nil; a set of x whose value reads x is
// returned as an "expr" (a read). stop reports that the line ended before
// the list did.
func irStraight(list []*irStmt, x int) (w *irStmt, stop bool) {
	for _, s := range list {
		switch {
		case s.K == "block" && s.Label == "" && s.X == nil:
			if w, stop := irStraight(s.Body, x); w != nil || stop {
				return w, stop
			}
		case s.K == "set" && s.Op == "" && s.Local == x:
			if irNames([]*irStmt{{K: "expr", X: s.X}}, x, true) {
				return &irStmt{K: "expr"}, false
			}
			return s, false
		case s.K == "def" || s.K == "set" || s.K == "store" || s.K == "expr" || s.K == "discard":
			if irNames([]*irStmt{s}, x, true) {
				return s, false
			}
		default:
			return nil, true
		}
	}
	return nil, false
}
