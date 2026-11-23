package lower

// IR builder: an inlined failure jumps to the catch arm that takes it
// (FAIL-INLINE). Once the inliner copies a fallible call into its site
// (a def2), each failure of the copy sets the site's error to a variant
// built in place and jumps past the copy, and the site's catch then
// tests the error against nil and switches on its type: an interface
// value built and taken apart for every failure, where hand-written Go
// branches straight to the code that handles it. std strconv.ParseBool
// in a loop that catches Malformed paid for both (FAIL-ELIDE's receipt:
// 35.75 instr/iter against a floor of 27.27).
//
// failJumps rewrites, in a body the inliner changed, a statement list
//
//	B                          the copy, a block; then its end label, if any
//	D...                       defs of a zero value or a constant
//	if e != nil { tswitch e { case T: A … } } else { S }
//
// when every statement in B that sets e is `set e <toiface T{…}>` (or a
// zero T), each followed by the jump past B or ending B; each such T is
// the one type of a case of the switch, and each case is reached by one
// failure at most; e is named nowhere else; and no arm holds a break or a
// continue (it moves into B, which may sit in a switch of the copy). Then
//
//	D...                       hoisted before B (no failure can see them unset)
//	B'                         each failure replaced by its arm, then a jump to K
//	{ S }                      the success branch, reached from B's end
//	K:
//
// An arm's binding of the failure (`case T as x`) becomes x = the
// variant itself. The switch's default, the unreachable defect, goes with
// the check: no failure is left to reach it.
//
// The reference is irFailJumpLowering "off"
// (-ldflags=-X=ergo.local/prototype/internal/sketch/lower.irFailJumpLowering=off),
// which also leaves a fallible leaf to Go's inliner (ir_forms_inline.go).
//
// Prior art: OCaml's flambda and GHC turn a raise into a jump to its
// handler once the raising function is inlined (OCaml's static
// exceptions, GHC's join points); Gleam's and ReScript's results are
// values matched where they are caught, which a JavaScript engine's
// inliner may fold.

import (
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// irFailJumpLowering is "off" for the reference: an inlined failure sets
// the site's error, which the catch switches on.
var irFailJumpLowering string

// IRFailJumpOn reports that inlined failures jump to their arms (the
// property's reach check).
func IRFailJumpOn() bool { return irFailJumpLowering != "off" }

// irFailTypedLowering is "off" for the reference of FAILJUMP-TYPED: only
// a failure built in place (`toiface T{…}`) jumps to its arm. On, so does
// one read from a local or a field (`toiface x`, `toiface e.Error`) whose
// Go type is a struct, known where the failure is built (FailT) or bound
// by the copy's Slot row (FailTP): a struct value in an error has that
// type as its dynamic type, so the catch's type switch would take the
// one arm of that type, which the jump takes at the fail point.
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.irFailTypedLowering=off
var irFailTypedLowering string

// SetFailTypedLoweringForTest sets irFailTypedLowering ("" or "off") and
// returns the previous mode.
func SetFailTypedLoweringForTest(mode string) string {
	prev := irFailTypedLowering
	irFailTypedLowering = mode
	return prev
}

// failType is a failure's Go type when it is a struct (FailT), or the
// Slot row whose Go type it is (FailTP), which a copy binding the row to
// a struct turns into FailT (copier). Anything else (an error, a union's
// interface, a row of two) is neither: its dynamic type is not its static
// one, so it keeps the type switch.
func (b *irBuilder) failType(x syntax.Expr) (string, *check.TypeParam) {
	if irFailTypedLowering == "off" || irFailJumpLowering == "off" {
		return "", nil
	}
	t := b.l.info.Types[x]
	if tp := check.SlotRow(t); tp != nil && tp.Slot {
		return "", tp
	}
	if tp, ok := t.(*check.TypeParam); ok && tp.Slot && tp.Kind == check.KindErrors {
		return "", tp
	}
	if !irStructFailure(t) {
		return "", nil
	}
	return irTypeOrNone(func() string { return b.goType(t, x) }), nil
}

// irStructFailure reports a failure type, or a Slot row's type argument
// of one member, that is a struct: a variant, whose Go value in an error
// has exactly that dynamic type.
func irStructFailure(t check.Type) bool {
	var r *check.Row
	switch x := t.(type) {
	case *check.Union:
		r = x.Row
	case *check.RowArg:
		r = x.Row
	}
	if r != nil {
		es := r.Elems()
		if len(es) != 1 {
			return false
		}
		t = es[0]
	}
	n, ok := t.(*check.Named)
	if !ok {
		return false
	}
	_, ok = n.Underlying().(*check.Struct)
	return ok
}

// irSlotParam is the error row parameter a Slot row's type argument is
// alone (a caller passing its own Slot row on), or nil.
func irSlotParam(t check.Type) *check.TypeParam {
	if tp := check.SlotRow(t); tp != nil {
		return tp
	}
	if ra, ok := t.(*check.RowArg); ok && ra.Row != nil {
		if es := ra.Row.Elems(); len(es) == 1 {
			t = es[0]
		}
	}
	if tp, ok := t.(*check.TypeParam); ok && tp.Kind == check.KindErrors {
		return tp
	}
	return nil
}

// irJumpType is the Go type a set of the site's error jumps by: a
// variant built in place, or a typed failure (FAILJUMP-TYPED); "" keeps
// the switch.
func irJumpType(s *irStmt) (string, bool) {
	v := s.X
	if v == nil || v.K != "toiface" || v.X == nil {
		return "", false
	}
	if v.X.K == "lit" || v.X.K == "zero" {
		return v.X.T, false
	}
	if s.FailT != "" && irFailTypedLowering != "off" {
		return s.FailT, true
	}
	return "", false
}

// failJumps applies the rewrite to body and every list nested in it.
func (in *irInliner) failJumps(body []*irStmt) []*irStmt {
	if irFailJumpLowering == "off" {
		return body
	}
	var list func([]*irStmt) []*irStmt
	list = func(l []*irStmt) []*irStmt {
		for _, s := range l {
			s.Then, s.Else, s.Body = list(s.Then), list(s.Else), list(s.Body)
			for _, c := range s.Cases {
				c.Body = list(c.Body)
			}
		}
		for i := 0; i < len(l); i++ {
			if out, ok := in.failJump(body, l, i); ok {
				l = out
			} else if out, ok := in.failThrough(l, i); ok {
				l = out
			}
		}
		return l
	}
	return list(body)
}

// failJump rewrites l around the check at i, or reports false.
func (in *irInliner) failJump(body, l []*irStmt, i int) ([]*irStmt, bool) {
	chk := l[i]
	x := chk.X
	if chk.K != "if" || x == nil || x.K != "bin" || x.Op != "!=" || x.X == nil || x.X.K != "local" || x.Y == nil || x.Y.K != "nil" {
		return nil, false
	}
	e := x.X.I
	if len(chk.Then) != 1 || chk.Then[0].K != "tswitch" || chk.Then[0].X == nil || chk.Then[0].X.K != "local" || chk.Then[0].X.I != e {
		return nil, false
	}
	sw := chk.Then[0]
	// Back from the check: the hoistable defs, an end label, the copy.
	j := i - 1
	for j >= 0 && l[j].K == "def" && l[j].X != nil && (l[j].X.K == "zero" || l[j].X.K == "const") {
		j--
	}
	defs := j + 1
	endLabel := "" // the copy's end, which its returns and failures jump to
	if j >= 0 && l[j].K == "block" && l[j].Label != "" && len(l[j].Body) == 0 {
		endLabel = l[j].Label
		j--
	}
	if j < 0 || l[j].K != "block" || l[j].Label != "" {
		return nil, false
	}
	b := l[j]
	for _, d := range l[defs:i] {
		if irNames(b.Body, d.Local, true) {
			return nil, false
		}
	}
	// The failures: every set of e in the copy.
	type site struct {
		val *irExpr
		arm *irCase
	}
	sites := map[*irStmt]site{}
	used := map[*irCase]bool{}
	typed := 0
	ok := true
	var scan func([]*irStmt, bool)
	scan = func(sl []*irStmt, top bool) {
		for k, s := range sl {
			if !ok {
				return
			}
			if s.K == "set" && s.Local == e {
				t, byType := irJumpType(s)
				if t == "" {
					ok = false
					return
				}
				next := endLabel != "" && k+1 < len(sl) && sl[k+1].K == "goto" && sl[k+1].Label == endLabel
				if !next && !(top && k == len(sl)-1) {
					ok = false
					return
				}
				// The one arm of that type; a failure two arms could
				// take keeps the switch.
				var arm *irCase
				n := 0
				for _, c := range sw.Cases {
					for _, ct := range c.Types {
						if ct == t {
							n++
						}
					}
					if len(c.Types) == 1 && c.Types[0] == t {
						arm = c
					}
				}
				if arm == nil || n != 1 || used[arm] || !irArmMovable(arm, e) {
					ok = false
					return
				}
				used[arm] = true
				sites[s] = site{s.X.X, arm}
				if byType {
					typed++
				}
				continue
			}
			if irNamesStmt(s, e) {
				ok = false
				return
			}
			scan(s.Then, false)
			scan(s.Else, false)
			scan(s.Body, false)
			for _, c := range s.Cases {
				scan(c.Body, false)
			}
			if s.K == "func" || s.X != nil && s.X.K == "func" {
				ok = false
			}
		}
	}
	scan(b.Body, true)
	if !ok || len(sites) == 0 || irNames(l[i+1:], e, true) || irNamesInLiteral(b.Body, e) {
		return nil, false
	}
	in.jumps++
	skip := "fj" + strconv.Itoa(in.jumps)
	// Each failure becomes its arm and the jump past the success branch.
	var rewrite func([]*irStmt) []*irStmt
	rewrite = func(sl []*irStmt) []*irStmt {
		var out []*irStmt
		for k := 0; k < len(sl); k++ {
			s := sl[k]
			if st, hit := sites[s]; hit {
				out = append(out, irArmBody(st.arm, e, st.val)...)
				out = append(out, &irStmt{K: "goto", Pos: s.Pos, Label: skip})
				if endLabel != "" && k+1 < len(sl) && sl[k+1].K == "goto" && sl[k+1].Label == endLabel {
					k++ // the jump past the copy
				}
				continue
			}
			s.Then, s.Else, s.Body = rewrite(s.Then), rewrite(s.Else), rewrite(s.Body)
			for _, c := range s.Cases {
				c.Body = rewrite(c.Body)
			}
			out = append(out, s)
		}
		return out
	}
	b.Body = rewrite(b.Body)
	in.st.report.FailJumps += len(sites)
	in.st.report.FailTyped += typed
	var out []*irStmt
	out = append(out, l[:j]...)
	out = append(out, l[defs:i]...) // hoisted before the copy
	out = append(out, l[j:defs]...)
	if len(chk.Else) > 0 {
		// In a block of its own: the jumps to K pass no declaration.
		out = append(out, &irStmt{K: "block", Pos: chk.Pos, End: chk.Pos, Body: chk.Else})
	}
	out = append(out, &irStmt{K: "block", Pos: chk.Pos, End: chk.Pos, Label: skip})
	out = append(out, l[i+1:]...)
	return out, true
}

// failThrough rewrites l around a check that passes the copy's failure on
// (a `try`'s), when every failure of the copy is built in place:
//
//	B                          the copy, a block; then its end label, if any
//	if e != nil { P }          P: `fail e`, or `set f e; goto L` (the
//	                           try of an enclosing copy)
//
// becomes B with each `set e <toiface T{…}>` (and the jump past B after
// it) replaced by P with the variant for e: the failure passes on built
// in place, so an enclosing site's check can take it to its arm
// (failJump), as a hand-written `return 0, T{…}` reaches its caller's
// switch. e is named nowhere else. INLINE-FAILARG; off with it.
func (in *irInliner) failThrough(l []*irStmt, i int) ([]*irStmt, bool) {
	if irFailArgLowering == "off" {
		return nil, false
	}
	chk := l[i]
	x := chk.X
	if chk.K != "if" || len(chk.Else) != 0 || x == nil || x.K != "bin" || x.Op != "!=" || x.X == nil || x.X.K != "local" || x.Y == nil || x.Y.K != "nil" {
		return nil, false
	}
	e := x.X.I
	reads := func(v *irExpr) bool { return v != nil && v.K == "local" && v.I == e }
	pass := chk.Then
	if !(len(pass) == 1 && pass[0].K == "fail" && reads(pass[0].X) ||
		len(pass) == 2 && pass[0].K == "set" && pass[0].Local != e && reads(pass[0].X) && pass[1].K == "goto") {
		return nil, false
	}
	j := i - 1
	endLabel := ""
	if j >= 0 && l[j].K == "block" && l[j].Label != "" && len(l[j].Body) == 0 {
		endLabel = l[j].Label
		j--
	}
	if j < 0 || l[j].K != "block" || l[j].Label != "" {
		return nil, false
	}
	b := l[j]
	sites := map[*irStmt]bool{}
	ok := true
	var scan func([]*irStmt, bool)
	scan = func(sl []*irStmt, top bool) {
		for k, s := range sl {
			if !ok {
				return
			}
			if s.K == "set" && s.Local == e {
				t, _ := irJumpType(s)
				next := endLabel != "" && k+1 < len(sl) && sl[k+1].K == "goto" && sl[k+1].Label == endLabel
				if t == "" || !next && !(top && k == len(sl)-1) {
					ok = false
					return
				}
				sites[s] = true
				continue
			}
			if irNamesStmt(s, e) || s.K == "func" || s.X != nil && s.X.K == "func" {
				ok = false
				return
			}
			scan(s.Then, false)
			scan(s.Else, false)
			scan(s.Body, false)
			for _, c := range s.Cases {
				scan(c.Body, false)
			}
		}
	}
	scan(b.Body, true)
	if !ok || len(sites) == 0 || irNames(l[i+1:], e, true) || irNamesInLiteral(b.Body, e) {
		return nil, false
	}
	var rewrite func([]*irStmt) []*irStmt
	rewrite = func(sl []*irStmt) []*irStmt {
		var out []*irStmt
		for k := 0; k < len(sl); k++ {
			s := sl[k]
			if sites[s] {
				p := *pass[0]
				p.X, p.FailT, p.FailTP = s.X, s.FailT, s.FailTP // passed on with its type
				out = append(out, &p)
				if len(pass) == 2 {
					g := *pass[1]
					out = append(out, &g)
				}
				if endLabel != "" && k+1 < len(sl) && sl[k+1].K == "goto" && sl[k+1].Label == endLabel {
					k++ // the jump past the copy
				}
				continue
			}
			s.Then, s.Else, s.Body = rewrite(s.Then), rewrite(s.Else), rewrite(s.Body)
			for _, c := range s.Cases {
				c.Body = rewrite(c.Body)
			}
			out = append(out, s)
		}
		return out
	}
	b.Body = rewrite(b.Body)
	in.st.report.FailThrough += len(sites)
	out := append(append([]*irStmt(nil), l[:i]...), l[i+1:]...)
	return out, true
}

// irArmMovable reports an arm that names the failure e only in its
// binding's def, and holds no break or continue.
func irArmMovable(c *irCase, e int) bool {
	body := c.Body
	if len(body) > 0 && irArmBinds(body[0], e) {
		body = body[1:]
	}
	if irNames(body, e, true) {
		return false
	}
	ok := true
	irWalk(body, false, func(s *irStmt, _ bool) {
		if s.K == "break" || s.K == "continue" {
			ok = false
		}
	}, func(*irExpr, bool) {})
	return ok
}

// irArmBinds reports the def of an arm's binding of e (`case T as x`).
func irArmBinds(s *irStmt, e int) bool {
	return s.K == "def" && s.X != nil && s.X.K == "assert" && s.X.X != nil && s.X.X.K == "local" && s.X.X.I == e
}

// irArmBody is the arm's statements with its binding set to the variant
// v itself.
func irArmBody(c *irCase, e int, v *irExpr) []*irStmt {
	out := append([]*irStmt(nil), c.Body...)
	if len(out) > 0 && irArmBinds(out[0], e) {
		d := *out[0]
		d.X = v
		out[0] = &d
	}
	return out
}

// irNamesStmt reports s itself (not its nested lists) naming x.
func irNamesStmt(s *irStmt, x int) bool {
	flat := *s
	flat.Then, flat.Else, flat.Body, flat.Cases = nil, nil, nil, nil
	return irNames([]*irStmt{&flat}, x, true)
}
