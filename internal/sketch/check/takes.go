package check

import (
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Takes inference and copy sites (resource ownership L8 A3; plan 1.1, 1.3; resources
// R17, R19, R44, Gap 35, Gap 39).
//
// Every function writes its takes clause (q137, EX3): a hand-off of a
// lent parameter is S108 with the whole clause to write and a ClauseFix
// (takesFix), and an unwritten clause takes nothing. Only ergo fix's
// clause writer (Options.Infer) infers, as below, and writes what it
// infers. Under it, the takes clause of an unexported function with a
// body that writes none
// is inferred from its body: a parameter is taken when on some path the
// body hands it off (ownership.go's hand-offs: returned, moved into a
// composite literal that is handed off, an argument a callee takes, the
// value an operation that takes is called on, Close). It is Lean 4's
// fixpoint (Beans 5.2): every parameter starts lent and is promoted at
// most once. It runs per strongly connected component of the package's
// call graph, callees first, as row inference does (rows.md, Inference
// 6), so it is linear in the bodies times the promotions of a component.
// The inferred clause is in Info.Takes; a written clause stays in
// Signature.Takes and is an upper bound (the body may take less: the
// callee then releases the parameter itself).
//
// An exported function, a method, an operation, an extern, main and a
// function type write the clause; a body that takes more than its written
// clause is S108 (ownership.go). The clause is part of a function type
// (Gap 39; intern.go, typeops.go): so a function whose clause is inferred
// and not empty writes it when it is used as a value.
//
// A type parameter's ownership is written, [T own] (types.md, Generics 9;
// EXM2, q172): a generic body is checked with each type parameter marked
// own counted as a resource (mayHoldResource), and an unmarked one as
// plain data. Where the ownership pass would refuse a value whose type
// holds a resource only through an own type parameter (a use after a
// hand-off, a second hand-off, a hand-off from a nested frame, a lend and
// a take in one call, a drop that a generic body cannot release yet), it
// refuses it at that site (ownSite), and an own type parameter used as a
// map key is S112 at the key (R40). An instantiation that binds a
// resource, or the caller's own type parameter, to an unmarked type
// parameter is S108 naming the mark (checkOwnMarks, resource.go): the
// call reads the callee's signature, never its body. An extern's mark is
// its Go body's review: runtime/v1's table (v1.MovesOnly) lists each
// extern that writes it (std.TestOwnExternsAreReviewed).
//
// Prior art: Lean 4's borrow inference (owned and borrowed parameters, a
// fixpoint over the call graph) still infers the takes clause above;
// Swift's ~Copyable generic parameters (T: ~Copyable) and Rust's ?Sized
// are written marks of the same kind; OxCaml's mode crossing lets plain
// data cross every mode. Gleam, Elixir and ReScript copy freely: their
// values own nothing.

// takesKey is a signature's takes clause as a canonical string: the
// indexes ascending, and "s" for a bare takes. Empty for no clause.
func takesKey(sig *Signature) string {
	if len(sig.Takes) == 0 && !sig.TakesSelf {
		return ""
	}
	ix := slices.Clone(sig.Takes)
	slices.Sort(ix)
	var b strings.Builder
	b.WriteString("t")
	if sig.TakesSelf {
		b.WriteString("s")
	}
	for _, i := range ix {
		b.WriteString(".")
		b.WriteString(strconv.Itoa(i))
	}
	return b.String()
}

// takesEffect is the part of sig's clause that means something (mode
// crossing): a taken parameter whose type may hold a resource, and the
// bare takes when recv, the value the operation is called on, may hold
// one. A function type compares with recv nil: a method value's bare
// takes is its receiver's, bound when the value is made. Two signatures
// agree on takes when their effects are equal (Gap 39).
func (c *checker) takesEffect(sig *Signature, recv Type) string {
	out := *sig
	out.Takes = nil
	for _, i := range sig.Takes {
		if i < len(sig.Params) && c.hostResourceParam(sig.Params[i].Type) {
			out.Takes = append(out.Takes, i)
		}
	}
	out.TakesSelf = sig.TakesSelf && recv != nil && c.mayHoldAny(recv)
	return takesKey(&out)
}

// takesText is the clause as written, for a message.
func takesText(sig *Signature) string {
	if len(sig.Takes) == 0 && !sig.TakesSelf {
		return "takes nothing"
	}
	var names []string
	if sig.TakesSelf {
		names = append(names, "the value it is called on")
	}
	for _, i := range sig.Takes {
		if i < len(sig.Params) {
			names = append(names, sig.Params[i].Name)
		}
	}
	return "takes " + strings.Join(names, ", ")
}

// ownAdvice spells what a signature keeps as the mode words that write
// it (q168): own before each kept parameter's type, and before an
// operation that keeps its receiver.
func ownAdvice(sig *Signature) string {
	var parts []string
	for _, i := range sig.Takes {
		if i < len(sig.Params) {
			parts = append(parts, sig.Params[i].Name+" own "+sig.Params[i].Type.String())
		}
	}
	if sig.TakesSelf {
		parts = append(parts, "own before the operation")
	}
	return strings.Join(parts, ", ")
}

// writesTakes reports whether f's signature has a written takes clause.
func writesTakes(f *Func) bool {
	return f.Sig != nil && (len(f.Sig.Takes) > 0 || f.Sig.TakesSelf)
}

// HoldsTaken reports whether a call of f leaves the values it takes on the
// caller's frame until it returns (D-C, Q7): the caller hands them off
// when the call returns, and a defect inside it leaves them to the
// caller's frame, which releases them. It holds for f with a runtime Go
// body, which owns no frame entry, and for f with an Ergo body that is
// generic in each value it takes and only forwards those values to such a
// call, its whole body (B2, PRIM-4): a generic body has no release of a
// value of its type parameter (Gap 42), so the forwarded value would
// otherwise have no owner while the inner call runs. A forwarder runs
// nothing after the inner call, so on every path the value is the inner
// call's or the caller's, never both. It reads declarations, never a
// package or function name.
func HoldsTaken(f *Func) bool { return holdsTakenFunc(f, map[*Func]bool{}) }

func holdsTakenFunc(f *Func, seen map[*Func]bool) bool {
	takes := takesOf(f)
	if len(takes) == 0 {
		return false
	}
	if BodyOf(f).Runtime != "" {
		return true
	}
	if seen[f] || f.Decl == nil || f.Decl.Extern != nil || f.Decl.Body == nil || f.Sig.Fallible || f.Pkg == nil || f.Pkg.Info == nil {
		return false
	}
	seen[f] = true
	if len(f.Decl.Body.List) != 1 {
		return false
	}
	es, ok := f.Decl.Body.List[0].(*syntax.ExprStmt)
	if !ok {
		return false
	}
	x, ok := unparen(es.X).(*syntax.CallExpr)
	if !ok {
		return false
	}
	inner := f.Pkg.Info.Calls[x]
	if inner == nil || inner.Kind != CallFunc || !holdsTakenFunc(inner.Func, seen) {
		return false
	}
	args := x.Args
	if inner.Recv != nil {
		args = append([]syntax.Expr{inner.Recv}, args...)
	}
	innerTakes := takesOf(inner.Func)
	for _, i := range takes {
		if i >= len(f.Sig.Params) {
			return false
		}
		p := f.Sig.Params[i]
		generic := false
		for _, tp := range f.Sig.TParams {
			generic = generic || typeMentions(p.Type, tp)
		}
		if !generic {
			return false // the body owns and releases a value of its own type
		}
		// p's one use is an operand the inner call takes.
		uses := 0
		syntax.Inspect(f.Decl.Body, func(n syntax.Node) bool {
			if id, ok := n.(*syntax.Ident); ok && isParam(f.Pkg.Info, id, p) {
				uses++
			}
			return true
		})
		forwarded := false
		for _, j := range innerTakes {
			if j < len(args) {
				if id, ok := unparen(args[j]).(*syntax.Ident); ok && isParam(f.Pkg.Info, id, p) {
					forwarded = true
				}
			}
		}
		if uses != 1 || !forwarded {
			return false
		}
	}
	return true
}

// isParam reports whether id refers to the parameter p of a forwarder:
// its body is one call, which declares no name, so a variable of p's name
// is p (the body's parameter object is not the signature's Var).
func isParam(info *Info, id *syntax.Ident, p *Var) bool {
	v, ok := info.Uses[id].(*Var)
	return ok && id.Name == p.Name && v.Name == p.Name
}

// takesOf is the clause of f: the written one, or the inferred one.
func takesOf(f *Func) []int {
	if f == nil || f.Sig == nil {
		return nil
	}
	if writesTakes(f) {
		return f.Sig.Takes
	}
	if f.Pkg != nil && f.Pkg.Info != nil {
		return f.Pkg.Info.Takes[f]
	}
	return nil
}

// infersTakes reports whether f's clause is inferred: only under ergo fix's
// clause writer (Options.Infer; q137, EX3), an unexported function of
// this package with a body, no receiver and no written clause, other than
// main. Every check, build and test path infers no clause: a hand-off of
// a lent parameter is S108 with the clause to write (takesFix).
func (c *checker) infersTakes(f *Func) bool {
	return c.opts.Infer && f != nil && f.Pkg == c.pkg && f.Decl != nil && f.Decl.Body != nil && f.Decl.Extern == nil &&
		f.Decl.Recv == nil && !writesTakes(f) && !isExported(f.Name) && f.Name != "main"
}

// typeMentions reports whether a value of type t may hold a value of the
// type parameter tp. A function value holds none: what it captures was
// judged where it was captured.
func typeMentions(t Type, tp *TypeParam) bool {
	if _, ok := under(t).(*Signature); ok {
		return false
	}
	found := false
	eachTypeParam(t, func(p *TypeParam) {
		if p == tp {
			found = true
		}
	})
	return found
}

// eachTypeParam calls f for each type parameter t is built from.
func eachTypeParam(t Type, f func(*TypeParam)) {
	seen := map[Type]bool{}
	var walk func(Type)
	walk = func(t Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		if p, ok := t.(*TypeParam); ok {
			f(p)
			return
		}
		_, _, _, _, kids := shape(t)
		for _, k := range kids {
			walk(k)
		}
	}
	walk(t)
}

// inferTakes infers the takes clauses and the copy sites of the package's
// functions, one component of the call graph at a time, callees first.
func (c *checker) inferTakes() {
	saved := c.cur
	defer func() { c.cur = saved }()
	var nodes []*bodyNode
	by := map[*Func]*bodyNode{}
	for _, f := range c.funcs {
		if f.Decl == nil || f.Decl.Body == nil || f.Decl.Extern != nil || f.Sig == nil {
			continue
		}
		n := &bodyNode{fn: f, index: -1, slot: -1}
		nodes = append(nodes, n)
		by[f] = n
	}
	for _, n := range nodes {
		seen := map[*bodyNode]bool{}
		// An edge per function the body names: a call, or a value use,
		// whose clause the body reads.
		syntax.Inspect(n.fn.Decl.Body, func(x syntax.Node) bool {
			var f *Func
			switch x := x.(type) {
			case *syntax.Ident:
				f, _ = c.pkg.Info.Uses[x].(*Func)
			case *syntax.CallExpr:
				if k := c.pkg.Info.Calls[x]; k != nil && k.Kind == CallFunc {
					f = k.Func
				}
			}
			if m := by[f]; m != nil && !seen[m] {
				seen[m] = true
				n.edges = append(n.edges, m)
			}
			return true
		})
		c.ownSteps += len(n.edges)
	}
	for _, scc := range tarjan(nodes) {
		// A body that reaches no member of its own component reads only
		// final facts: one pass decides it.
		cyclic := len(scc) > 1 || slices.Contains(scc[0].edges, scc[0])
		for {
			changed := false
			for _, n := range scc {
				if c.takesPass(n.fn) {
					changed = true
				}
			}
			if !changed || !cyclic {
				break
			}
		}
	}
}

// takesPass walks f's body once with inference on; it reports whether f's
// clause grew.
func (c *checker) takesPass(f *Func) bool {
	c.cur = f.file
	w := c.newOwnWalk()
	w.infer = true
	w.fn = f
	w.promote = map[int]bool{}
	w.frame(f.Decl.Body, takesOf(f), w.paramVars(f.Decl.Recv, f.Decl.Type.Params), f.Decl.Name.Name, "", c.pkg.Info.Closes[f.Decl] != nil)
	changed := false
	if len(w.promote) > 0 {
		ix := slices.Clone(c.pkg.Info.Takes[f])
		for i := range w.promote {
			if !slices.Contains(ix, i) {
				ix = append(ix, i)
				changed = true
			}
		}
		slices.Sort(ix)
		c.pkg.Info.Takes[f] = ix
	}
	return changed
}

// ownExternReviewed refuses own on a type parameter of the extern f
// whose Go body runtime/v1 has not reviewed as moving its values only
// (v1.MovesOnly): the checker cannot read a Go body, so the mark stands
// on the review (types.md, Generics 9).
func (c *checker) ownExternReviewed(f *Func) {
	if f.Decl == nil || f.Decl.TypeParams == nil || BodyOf(f).MovesOnly {
		return
	}
	for _, field := range f.Decl.TypeParams.List {
		if field.Own != nil {
			c.errorIn(field, field.Own.At, field.Own.At+3, CodeType, "%s's Go body is not reviewed as moving its values only (runtime/v1's MovesOnly table), so its type parameters cannot be marked own; drop own, and a resource cannot bind them (types.md, Generics 9)", f.Name)
		}
	}
}

// ownSite refuses, at x, what the body does with a value of t, which
// holds a resource only through an own type parameter: the mark holds the
// body to a resource's rules (types.md, Generics 9).
func (w *ownWalk) ownSite(t Type, x syntax.Node, what string) {
	var names []string
	eachTypeParam(t, func(tp *TypeParam) {
		if tp.Own && !slices.Contains(names, tp.Name) {
			names = append(names, tp.Name)
		}
	})
	if len(names) == 0 || !w.c.mayHoldResource(t) {
		return // plain data: nothing to refuse
	}
	w.reportCode(x, CodeType, "%s, and %s is marked own, so its value has one owner as a resource's has (types.md, Generics 9)", what, strings.Join(names, ", "))
}

// funcValue checks a function named as a value (not called): its takes
// clause is part of its type, so an inferred clause that takes something
// is written (Gap 39). inst is the value's type at this instance: a taken
// parameter whose type is plain data there takes nothing, and one that is
// a resource or an own type parameter takes it.
func (w *ownWalk) funcValue(x *syntax.Ident, inst Type) {
	f, _ := w.info.Uses[x].(*Func)
	if f == nil || writesTakes(f) || !w.c.infersTakes(f) || len(w.info.Takes[f]) == 0 {
		return
	}
	sig := *f.Sig
	if s, ok := inst.(*Signature); ok && len(s.Params) == len(sig.Params) {
		sig = *s
	}
	sig.Takes = w.info.Takes[f]
	held := false
	for _, i := range sig.Takes {
		if w.c.mayHoldResource(sig.Params[i].Type) {
			held = true
		}
	}
	if !held {
		return
	}
	w.reportCode(x, CodeType, "%s is used as a value, so what it keeps is part of its type, and its body %s (resources R19, Gap 39); write %s in its signature", f.Name, takesText(&sig), ownAdvice(&sig))
}
