package check

import "ergo.local/prototype/internal/sketch/syntax"

// Undiagnosed values and typing's record of callback frames (round 9
// review M1; round 10 ruling). An undiagnosed value is one typing treats
// as invalid, with no diagnostic and no cascade, although no diagnostic of
// its own explains it:
//
//   - in the silent pass whose record class G reads, a type that typing
//     would build from a row that holds an unsolved variable of a member
//     of the open component is provisional: the instance a bare generic
//     catch arm takes from the caught row (catchType) may still arrive
//     (rowWaits); a row from outside the component is final;
//   - in the reporting pass, a reference to a member class G refuses keeps
//     its written signature, and its error row holds invalid so that
//     nothing cascades from it (groupRef, Row.unknown); its diagnostic is
//     the member's S108, which reportGroup decides last.
//
// A local bound to an undiagnosed value is undiagnosed (Var.undiagnosed),
// and each read of an undiagnosed value counts (undiagSeq). A callback
// frame passed to a call that does not check it (an invalid callee, an
// invalid argument, an arity error) is recorded after the call
// (skipFrames): invalid-diagnosed when the call read no undiagnosed value,
// refused-by-class-G in the reporting pass when it did or when it names a
// member class G refuses outside the frame (C46), and provisional
// otherwise. So typing's record of a frame has four states (frameState).
//
// Invalid-diagnosed is the positive verdict: typing skipped the frame for
// a reason it reports and that holds whatever the rows (a refused import,
// S104, S109, the arm's own error). Every other skipped frame, whatever
// the route, keeps the class G uses inside it. In the silent pass this
// makes the pin conservative; in the reporting pass, reportGroup drops a
// use only when its frame is invalid-diagnosed, so a refusal never
// cascades and a class G refusal is never dropped (rows.md, Capability
// rows 6). A staged or unknown result is never evidence that a frame is
// not checked.
//
// Prior art: Hindley-Milner checkers keep an unsolved type a flexible
// variable and poison a type only on a reported error (Elm, Roc); go/types
// records a type once it is final; Gleam checks a group's uses on its
// solved types.

// A frameState is typing's record of a callback frame in one body check.
type frameState uint8

const (
	// frameProvisional: typing did not check the frame, with no positive
	// reason: the frame keeps its uses.
	frameProvisional frameState = iota
	// frameChecked: typing checked the frame.
	frameChecked
	// frameInvalid: typing skipped the frame for a diagnosed reason that
	// does not depend on a row still being inferred (invalid-diagnosed).
	frameInvalid
	// frameRefused: the reporting pass skipped the frame because its call
	// reads a member class G refuses (refused-by-class-G): the frame keeps
	// its uses.
	frameRefused
)

func (s frameState) String() string {
	switch s {
	case frameChecked:
		return "checked"
	case frameInvalid:
		return "invalid-diagnosed"
	case frameRefused:
		return "refused-by-class-G"
	}
	return "provisional"
}

// rowWaits reports whether a type built from a row now waits on a row
// still being inferred, and counts the undiagnosed read when it does: in
// the silent pass, when the row read an unsolved variable of a member of
// the open component (open: readRow counted it while the row's expression
// was checked). A row from outside the component is final, so a type it
// lacks is an error at once (round 11 review M1). A rule that builds a
// type from a row asks it before it reports the row as lacking the type.
func (c *checker) rowWaits(open bool) bool {
	if c.rowsOpen == 0 || !open {
		return false
	}
	c.undiagSeq++
	return true
}

// readRow counts the reads of a callee's error row r at a call (result,
// goExpr): an open read when r holds an unsolved variable of a member of
// the component being checked (rowWaits; round 11 review M1).
func (c *checker) readRow(r *Row) {
	if r == nil || !r.vars {
		return
	}
	for _, e := range r.Elems() {
		v, ok := e.(*RowVar)
		if !ok {
			continue
		}
		if root := v.root(); root.Fn != nil && !root.final && c.group[root.Fn] {
			c.openRowSeq++
		}
	}
}

// readUndiagnosed counts a read of an undiagnosed value.
func (c *checker) readUndiagnosed() { c.undiagSeq++ }

// readLocal counts a read of v when v is undiagnosed.
func (c *checker) readLocal(v *Var) {
	if v.undiagnosed {
		c.undiagSeq++
	}
}

// bindLocal marks v undiagnosed when its type is invalid and the current
// statement has read an undiagnosed value: the value it is bound to may
// come from that read. It returns v.
func (c *checker) bindLocal(v *Var) *Var {
	if isInvalid(v.Type) && c.undiagSeq != c.stmtMark {
		v.undiagnosed = true
	}
	return v
}

// skipFrames records, after the call x is checked, each callback frame of
// x (a function-literal argument or the trailing block) that typing did
// not check: invalid-diagnosed when the call read no undiagnosed value;
// else refused-by-class-G in the reporting pass, and provisional (no
// record) in a silent one.
func (c *checker) skipFrames(x *syntax.CallExpr) {
	for _, a := range x.Args {
		if fl, ok := a.(*syntax.FuncLit); ok {
			c.skipCallFrame(x, fl)
		}
	}
	if x.Trailing != nil {
		c.skipCallFrame(x, x.Trailing)
	}
}

// skipCallFrame records the frame f of the call x as skipFrame does, but
// in the reporting pass a frame whose call names a member class G refuses
// outside f is refused-by-class-G, never invalid-diagnosed, whatever
// other diagnosed reason skipped it (C46). The name may be in a part of
// the call that typing did not read, such as a callback frame of the
// callee that an arity error skipped, or in a later component, whose
// reference reads no undiagnosed value (groupRef): the S108 is at the
// member, so the frame keeps its uses (rows.md, Capability rows 6).
func (c *checker) skipCallFrame(x *syntax.CallExpr, f syntax.Node) {
	c.skipFrame(f, c.callMark)
	if c.silent > 0 || c.curGen == 0 || c.frameInv[f] != c.curGen || !c.namesRefused(x, f) {
		return
	}
	delete(c.frameInv, f)
	c.frameRef[f] = c.curGen
}

// namesRefused reports whether the call x names, outside its frame f, a
// function class G refuses: a member of the component being reported
// that the reporting pass refuses, or a member of an earlier component it
// refused (refusedMembers). The names are the reference walk's
// (inferredRefs), which resolves the frames typing skipped too.
func (c *checker) namesRefused(x *syntax.CallExpr, f syntax.Node) bool {
	found := false
	syntax.Inspect(x, func(n syntax.Node) bool {
		if found || n == f {
			return false
		}
		id, ok := n.(*syntax.Ident)
		if !ok {
			return true
		}
		fn := c.inferredRefs[id]
		if fn == nil {
			return true
		}
		if c.group[fn] {
			found = c.refused(fn)
		} else {
			_, found = c.refusedMembers[fn]
		}
		return !found
	})
	return found
}

// skipFrame records the callback frame n, when typing did not check it in
// this body check, as skipFrames does: invalid-diagnosed when nothing
// undiagnosed was read since mark (the undiagSeq where the skipping
// expression began), else refused-by-class-G in the reporting pass and
// provisional in a silent one. Every route that skips a value holding a
// frame comes here: a call's arguments and trailing block, the elements
// of a literal whose type is unknown or refused, a refused element of a
// map, struct or machine literal (round 3 review M4), the index of an invalid base
// (stage D1 round 1 review M6). The let mut locals the frame reassigns or
// borrows count as reassigned, so Q3 adds no false S110 beside the
// diagnostic that skipped it (M3).
func (c *checker) skipFrame(n syntax.Node, mark int) {
	if c.curGen == 0 || c.frameGen[n] == c.curGen {
		return
	}
	c.markReassignedIn(n)
	rec := c.frameInv
	if c.undiagSeq != mark {
		if c.silent > 0 {
			return
		}
		rec = c.frameRef
	}
	rec[n] = c.curGen
}

// markReassignedIn marks, from the syntax alone, each let mut local that
// a frame typing skipped assigns to, borrows with &, or calls a function
// on (which may take *T): the frame's names are not in scope, and D-D2
// means a name there that a visible let mut has is that local. Marking
// too many only drops an S110 in a program that already has an error.
func (c *checker) markReassignedIn(n syntax.Node) {
	mark := func(x syntax.Expr) {
		for {
			switch e := x.(type) {
			case *syntax.ParenExpr:
				x = e.X
			case *syntax.SelectorExpr:
				x = e.X
			case *syntax.IndexExpr:
				x = e.X
			case *syntax.Ident:
				if v, ok := c.lookup(e.Name).(*Var); ok && v.mut {
					v.reassigned = true
				}
				return
			default:
				return
			}
		}
	}
	syntax.Inspect(n, func(n syntax.Node) bool {
		switch s := n.(type) {
		case *syntax.AssignStmt:
			for _, l := range s.Lhs {
				mark(l)
			}
		case *syntax.UnaryExpr:
			if s.Op == syntax.AND {
				mark(s.X)
			}
		case *syntax.CallExpr:
			// Every spelling of a called selector, as typing reads it:
			// x.f(), (x.f)(), ((x.f))(), x.f[T]() (stage D1 round 2
			// review M2).
			if sel := calledSelector(s.Fun); sel != nil {
				mark(sel.X)
			}
		}
		return true
	})
}

// frameStateAt is typing's record of the callback frame n in the body
// check gen.
func (c *checker) frameStateAt(n syntax.Node, gen int) frameState {
	switch {
	case gen == 0:
		return frameProvisional
	case c.frameGen[n] == gen:
		return frameChecked
	case c.frameInv[n] == gen:
		return frameInvalid
	case c.frameRef[n] == gen:
		return frameRefused
	}
	return frameProvisional
}

// chainState is the state of g's frame in the body check gen: a frame
// inside one typing did not enter takes that frame's state. memo holds the
// states decided so far, so each guard node is decided once; trace, in a
// test build, receives the first verdict read for each frame.
func (c *checker) chainState(g *guardNode, gen int, memo map[*guardNode]frameState, trace map[syntax.Node]frameVerdict) frameState {
	var chain []*guardNode
	n := g
	for ; n != nil; n = n.up {
		if _, ok := memo[n]; ok {
			break
		}
		chain = append(chain, n)
	}
	st := frameChecked
	if n != nil {
		st = memo[n]
	}
	for i := len(chain) - 1; i >= 0; i-- {
		f := chain[i]
		if st == frameChecked {
			st = c.frameStateAt(f.frame, gen)
		}
		if trace != nil {
			if _, seen := trace[f.frame]; !seen {
				trace[f.frame] = frameVerdict{state: st, gen: gen}
			}
		}
		memo[f] = st
	}
	return st
}
