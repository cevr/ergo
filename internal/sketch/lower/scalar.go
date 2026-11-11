package lower

import (
	"slices"
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Scalar replacement of union locals (OPT6b). A union that is a Go
// interface boxes each value put into it, and Go keeps a box on the stack
// only when nothing outlives the iteration that made it, and when no
// other value of the same Go variable goes to the heap: Go's escape
// analysis does not tell a variable's values (or a struct's fields)
// apart. Such a local lives instead as Go variables of its own, one for
// the variant's index and one slot per variant:
//
//		var at uint8 // the tag
//		var at_1 Idle
//		var at_2 Busy
//
//	  - a write of the union value e takes it apart by a type switch into
//	    the tag and its variant's slot (scatter): the box e came in, if any,
//	    outlives nothing;
//	  - a match on the local switches on its tag, and each case reads its
//	    variant's slot, as a match on a flat union does (tagSwitchOf);
//	  - any other read puts the slot of its tag into a union value of its
//	    own (gather), a box that lives as long as that read's value.
//
// Two kinds of local take the form:
//
//   - a loop-carried local: a let mut of one name with a value, written
//     by a plain assignment inside a loop that its let is outside of;
//   - a join local: a let of one name whose value is a call (under try or
//     not) and that a match takes as its subject. When the call is
//     inlined (inline.go), each return of the inlined body writes the
//     local's tag and slot itself: a returned variant literal goes
//     straight into its slot (case of the known constructor), any other
//     value is scattered. No union value is made, so the variant that a
//     match arm sends to the heap (an Emit's output) takes no other
//     variant's box with it. A call that is not inlined is scattered.
//
// Both need a type that is a union of the Go interface form (a flat union
// is a struct already) whose variants have distinct Go types, and every
// other use of the local reads its value: no closure names it, no address
// or borrow of it is taken, it is the receiver of no call and the target
// of no op-assignment. A function with Go type parameters keeps the union
// value: its variants' Go types could coincide at an instance, and the
// type switch would have two equal cases (a copy for a known record,
// record.go, has Go types).
//
// The reference is scalarLowering "off", set by a test or by a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.scalarLowering=off.
// TestScalarLocalsAgree runs generated programs under both.
//
// Prior art: LLVM's SROA and GraalVM's partial escape analysis replace an
// aggregate that does not escape with its fields; GHC's worker/wrapper,
// case-of-known-constructor and join points carry a known sum as a tag and
// fields and send each constructor at a return straight to its
// alternative; OCaml's flambda unboxes a block a loop carries. Gleam,
// Elixir and ReScript keep a variant a heap value (a tagged tuple, a
// JavaScript object) and rely on the host's allocator or escape analysis.
var scalarLowering string

// scalarLocal is a local in the scalar form: the Go names of its tag and
// slots, the union's Go type and its variants' Go types, by tag.
type scalarLocal struct {
	name     string
	slots    []string
	union    string
	variants []string
	inline   bool // a join local whose call was inlined
}

// scalarVars are the locals of f's body that take the scalar form, and
// the parameters an inlined call of f may bind to a scalar local of its
// caller (aliasable).
func (l *lowerer) scalarVars(f *check.Func) map[*check.Var]bool {
	if scalarLowering == "off" || f.Decl == nil || f.Decl.Body == nil || f.Pkg == nil || f.Pkg.Info == nil {
		return nil
	}
	body := f.Decl.Body
	if out, ok := l.scalarMemo[body]; ok {
		return out
	}
	if l.scalarMemo == nil {
		l.scalarMemo = map[*syntax.BlockStmt]map[*check.Var]bool{}
	}
	out := scalarCandidates(body, f.Pkg.Info, paramVars(f))
	l.scalarMemo[body] = out
	return out
}

// joinCall is the call a join local's value is: x under parentheses and
// at most one try; nil for any other value.
func joinCall(x syntax.Expr) *syntax.CallExpr {
	x = unparen(x)
	if t, ok := x.(*syntax.TryExpr); ok {
		x = unparen(t.X)
	}
	c, _ := x.(*syntax.CallExpr)
	return c
}

// scalarCandidates is the rule above, but for the local's type.
func scalarCandidates(body *syntax.BlockStmt, info *check.Info, params []*check.Var) map[*check.Var]bool {
	decls := map[*check.Var]syntax.Node{}
	join := map[*check.Var]bool{}
	param := map[*check.Var]bool{}
	for _, p := range params {
		if p != nil {
			decls[p], param[p] = body, true
		}
	}
	var loops []syntax.Node
	syntax.Inspect(body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false // a literal's locals are its own frame's
		case *syntax.ForStmt, *syntax.RangeStmt:
			loops = append(loops, n)
		case *syntax.VarDecl:
			for _, spec := range n.Specs {
				if spec.Pattern != nil || len(spec.Names) != 1 || len(spec.Values) != 1 {
					continue
				}
				v, ok := info.Defs[spec.Names[0]].(*check.Var)
				if !ok {
					continue
				}
				switch {
				case n.Mut.IsValid():
					decls[v] = n
				case joinCall(spec.Values[0]) != nil:
					decls[v] = n
					join[v] = true
				}
			}
		}
		return true
	})
	if len(decls) == 0 {
		return nil
	}
	within := func(n, outer syntax.Node) bool { return outer.Pos() <= n.Pos() && n.End() <= outer.End() }
	carried := map[*check.Var]bool{}
	matched := map[*check.Var]bool{}
	bad := map[*check.Var]bool{}
	use := func(x syntax.Expr) *check.Var {
		id, ok := unparen(x).(*syntax.Ident)
		if !ok {
			return nil
		}
		v, _ := info.Uses[id].(*check.Var)
		if v == nil || decls[v] == nil {
			return nil
		}
		return v
	}
	// A read that is a match's subject, a call's operand, an assignment's
	// target or in a return reads the tag and slots, or gathers once on
	// the way out; any other read gathers a new box each time it runs.
	cheap := map[*syntax.Ident]bool{}
	mark := func(x syntax.Expr) {
		if id, ok := unparen(x).(*syntax.Ident); ok {
			cheap[id] = true
		}
	}
	syntax.Inspect(body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.TypeSwitchStmt:
			if len(n.Subjects) == 1 {
				mark(n.Subjects[0].X)
			}
		case *syntax.CallExpr:
			for _, a := range n.Args {
				mark(a)
			}
		case *syntax.AssignStmt:
			for _, x := range n.Lhs {
				mark(x)
			}
		case *syntax.VarDecl:
			for _, spec := range n.Specs {
				for _, id := range spec.Names {
					cheap[id] = true // the name it declares
				}
			}
		case *syntax.ReturnStmt:
			syntax.Inspect(n, func(m syntax.Node) bool {
				if id, ok := m.(*syntax.Ident); ok {
					cheap[id] = true
				}
				return true
			})
		}
		return true
	})
	syntax.Inspect(body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Ident:
			v := use(n)
			if v == nil || cheap[n] {
				return true
			}
			if join[v] || param[v] {
				bad[v] = true
			}
			for _, loop := range loops {
				if within(n, loop) && !within(decls[v], loop) {
					bad[v] = true
				}
			}
		case *syntax.FuncLit:
			syntax.Inspect(n, func(m syntax.Node) bool {
				if id, ok := m.(*syntax.Ident); ok {
					if v := use(id); v != nil {
						bad[v] = true
					}
				}
				return true
			})
			return false
		case *syntax.TypeSwitchStmt:
			if len(n.Subjects) == 1 {
				if v := use(n.Subjects[0].X); v != nil {
					matched[v] = true
				}
			}
		case *syntax.AssignStmt:
			if len(n.Lhs) != 1 {
				return true
			}
			v := use(n.Lhs[0])
			if v == nil {
				return true
			}
			if n.Tok != syntax.ASSIGN {
				bad[v] = true
				return true
			}
			for _, loop := range loops {
				if within(n, loop) && !within(decls[v], loop) {
					carried[v] = true
				}
			}
		case *syntax.UnaryExpr:
			if v := use(n.X); v != nil && n.Op == syntax.AND {
				bad[v] = true
			}
		case *syntax.CallExpr:
			if c := info.Calls[n]; c != nil && c.Recv != nil {
				if v := use(c.Recv); v != nil {
					bad[v] = true
				}
			}
			if c := info.Calls[n]; c == nil || c.Kind == check.CallWith {
				for _, a := range n.Args {
					if v := use(a); v != nil {
						bad[v] = true
					}
				}
			}
		}
		if x, ok := n.(syntax.Expr); ok && info.ContextValues[x] != nil {
			if v := use(x); v != nil {
				bad[v] = true
			}
		}
		return true
	})
	out := map[*check.Var]bool{}
	for v := range decls {
		if bad[v] {
			continue
		}
		if param[v] || join[v] && matched[v] || !join[v] && carried[v] {
			out[v] = true
		}
	}
	return out
}

// scalarOf is the scalar form of v, declared now, when v takes it in the
// body being written; nil when it does not.
func (fc *funcCtx) scalarOf(v *check.Var) *scalarLocal {
	if v == nil || !fc.scalarSet[v] || fc.goGeneric {
		return nil
	}
	// The function has Go types (goGeneric is false), so each type
	// parameter t mentions has a Go type here.
	t := fc.boundType(v.Type)
	n, ok := t.(*check.Named)
	if !ok || fc.l.flatUnionOf(t) != nil {
		return nil
	}
	u, ok := n.Underlying().(*check.Union)
	if !ok || len(u.Variants) == 0 || len(u.Variants) > 255 {
		return nil
	}
	sl := &scalarLocal{union: fc.goType(t)}
	for _, t := range u.Variants {
		g := fc.goType(t)
		if slices.Contains(sl.variants, g) {
			return nil
		}
		sl.variants = append(sl.variants, g)
	}
	return sl
}

// scalarLet writes the let of v, a local in the scalar form sl, with the
// value x: its tag and slots, then the value taken apart. A join local's
// call, when inlined, declares them and writes them itself (joinSite,
// joinReturn); when it is not, the union value is made anyway, and the
// local keeps it as a plain one.
func (fc *funcCtx) scalarLet(v *check.Var, sl *scalarLocal, x syntax.Expr, mut bool) {
	if call := joinCall(x); call != nil && !mut {
		if fc.joinAt == nil {
			fc.joinAt = map[*syntax.CallExpr]*scalarLocal{}
		}
		sl.name = v.Name
		fc.joinAt[call] = sl
		val := fc.assigned(x)
		delete(fc.joinAt, call)
		if !sl.inline {
			name := fc.declare(v)
			fc.line("var " + name + " " + sl.union + " = " + val)
			fc.line("_ = " + name)
			return
		}
	} else {
		val := fc.assigned(x)
		fc.scalarDeclare(sl, v.Name)
		fc.scatter(sl, val)
	}
	if fc.scalars == nil {
		fc.scalars = map[*check.Var]*scalarLocal{}
	}
	fc.scalars[v] = sl
	fc.locals[v] = sl.name
}

// scatter writes the union value val into the scalar local sl.
func (fc *funcCtx) scatter(sl *scalarLocal, val string) {
	x := fc.temp("x")
	fc.line("switch " + x + " := " + sl.union + "(" + val + ").(type) {")
	for k, g := range sl.variants {
		fc.line("case " + g + ":")
		fc.line("\t" + fc.scalarWrite(sl, k, x))
	}
	fc.line("default:")
	fc.line("\t_ = " + x)
	fc.line("\tpanic(\"ergo: defect: a value of " + sl.union + " that is none of its variants\")")
	fc.line("}")
}

// scalarWrite is the Go statement that puts val, a value of variant k,
// into the scalar local sl.
func (fc *funcCtx) scalarWrite(sl *scalarLocal, k int, val string) string {
	return sl.name + ", " + sl.slots[k] + " = " + strconv.Itoa(k) + ", " + val
}

// gather is the union value of the scalar local sl, in a Go local of its
// own: the slot of its tag.
func (fc *funcCtx) gather(sl *scalarLocal) string {
	g := fc.temp("g")
	fc.line("var " + g + " " + sl.union)
	fc.line("switch " + sl.name + " {")
	for k := range sl.variants {
		fc.line("case " + strconv.Itoa(k) + ":")
		fc.line("\t" + g + " = " + sl.slots[k])
	}
	fc.line("}")
	return g
}

// joinSite is the join local the inlined call x writes, if any; it marks
// the local's value as written by the call's returns.
func (fc *funcCtx) joinSite(x *syntax.CallExpr) *scalarLocal {
	sl := fc.joinAt[x]
	if sl != nil && !sl.inline {
		sl.inline = true
		fc.l.stats.Joins++
		fc.scalarDeclare(sl, sl.name)
	}
	return sl
}

// scalarDeclare declares the tag and slots of sl, named after name.
func (fc *funcCtx) scalarDeclare(sl *scalarLocal, name string) {
	fc.l.stats.Scalars++
	sl.name = fc.temp(name)
	fc.line("var " + sl.name + " uint8")
	fc.line("_ = " + sl.name)
	for _, g := range sl.variants {
		s := fc.temp(name)
		sl.slots = append(sl.slots, s)
		fc.line("var " + s + " " + g)
		fc.line("_ = " + s)
	}
}

// joinReturn writes the return of x from an inlined body whose site's
// result is the join local sl: a variant literal into its slot, any other
// value scattered, then the jump past the body.
func (fc *funcCtx) joinReturn(sl *scalarLocal, x syntax.Expr) {
	k := slices.Index(sl.variants, fc.goType(fc.typeOf(x)))
	val := fc.moved(fc.assigned(x))
	if k >= 0 {
		fc.line(fc.scalarWrite(sl, k, val))
	} else {
		fc.scatter(sl, val)
	}
	fc.line("goto " + fc.frame.inline.label)
}

// scalarArgs are the operands of an inlined call of f, by index, that
// bind their parameter to the caller's scalar local they name: the body
// reads the local's tag and slots as its own (a match on the parameter
// switches on the tag, case of the known constructor across the call). An
// operand takes it when the parameter is aliasable (scalarCandidates),
// the operand has no conversion, and no later operand writes code: a
// later operand could assign the local before the body reads it.
func (fc *funcCtx) scalarArgs(f *check.Func, list []syntax.Expr, skip map[int]bool, addr bool) map[int]*scalarLocal {
	if fc.scalars == nil {
		return nil
	}
	set := fc.l.scalarVars(f)
	params := paramVars(f)
	var out map[int]*scalarLocal
	later := false
	for i := len(list) - 1; i >= 0; i-- {
		if skip[i] {
			continue
		}
		sl := fc.scalarIdent(list[i])
		_, conv := fc.l.info.Implicit[list[i]]
		if sl != nil && !later && !conv && !(addr && i == 0) && i < len(params) && set[params[i]] {
			if out == nil {
				out = map[int]*scalarLocal{}
			}
			out[i] = sl
			continue
		}
		later = later || fc.emits(list[i])
	}
	return out
}

// scalarIdent is the scalar local x names, if it names one.
func (fc *funcCtx) scalarIdent(x syntax.Expr) *scalarLocal {
	if fc.scalars == nil {
		return nil
	}
	id, ok := unparen(x).(*syntax.Ident)
	if !ok {
		return nil
	}
	v, ok := fc.l.info.Uses[id].(*check.Var)
	if !ok {
		return nil
	}
	return fc.scalars[v]
}

// scalarTags is the tag switch of a match on the scalar local subject
// (its Go name) over the Go types types; nil when subject names none.
func (fc *funcCtx) scalarTags(subject string, types []string) *tagSwitch {
	var sl *scalarLocal
	for _, s := range fc.scalars {
		if s.name == subject {
			sl = s
		}
	}
	if sl == nil {
		return nil
	}
	tags := make([]int, len(types))
	for i, t := range types {
		k := slices.Index(sl.variants, t)
		if k < 0 {
			return nil
		}
		tags[i] = k
	}
	return &tagSwitch{tags: tags, head: "switch " + subject + " {", variant: func(k int) string { return sl.slots[k] }}
}
