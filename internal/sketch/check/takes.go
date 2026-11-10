package check

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// Takes inference and copy sites (#197 L8 A3; plan 1.1, 1.3; resources
// R17, R19, R44, Gap 35, Gap 39).
//
// The takes clause of an unexported function with a body that writes none
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
// A generic body is checked with every type parameter counted as a
// resource (mayHoldResource). Where the ownership pass would refuse a
// value whose type holds a resource only through a type parameter (a use
// after a hand-off, a second hand-off, a hand-off from a nested frame, a
// lend and a take in one call), it records a copy site for that type
// parameter instead (Info.Copies). A call of a function that copies its
// type parameter, bound to the caller's own type parameter, is a copy
// site of the caller too, so the sites propagate with the fixpoint. A
// generic declaration with no body (an extern) copies each type parameter
// unless runtime/v1's reviewed table (v1.MovesOnly) lists its body. At a
// call that binds a type parameter with a copy site to a resource, the
// call is S108 (R44: a machine runner copies its state). A type parameter
// the function uses as a map key is a copy site too, and that call is
// S112 (R40).
//
// Prior art: Lean 4's borrow inference (owned and borrowed parameters, a
// fixpoint over the call graph); OxCaml's mode crossing (a type parameter
// carries no mark; the instantiation decides); Swift's consuming
// parameters are written, the owner's choice for exported signatures
// (q55). Gleam, Elixir and ReScript copy freely: their values own nothing.

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
		if i < len(sig.Params) && c.mayHoldResource(sig.Params[i].Type) {
			out.Takes = append(out.Takes, i)
		}
	}
	out.TakesSelf = sig.TakesSelf && recv != nil && c.mayHoldResource(recv)
	return takesKey(&out)
}

// takesText is the clause as written, for a message.
func takesText(sig *Signature) string {
	if len(sig.Takes) == 0 && !sig.TakesSelf {
		return "nothing"
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

// writesTakes reports whether f's signature has a written takes clause.
func writesTakes(f *Func) bool {
	return f.Sig != nil && (len(f.Sig.Takes) > 0 || f.Sig.TakesSelf)
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

// infersTakes reports whether f's clause is inferred: an unexported
// function of this package with a body, no receiver and no written clause,
// other than main.
func (c *checker) infersTakes(f *Func) bool {
	return f != nil && f.Pkg == c.pkg && f.Decl != nil && f.Decl.Body != nil && f.Decl.Extern == nil &&
		f.Decl.Recv == nil && !writesTakes(f) && !isExported(f.Name) && f.Name != "main"
}

// copiesOf is f's copy sites: its body's, or for a generic declaration
// with no body every type parameter, unless runtime/v1 lists the body as
// moving its values only (plan 1.3, D-B).
func (c *checker) copiesOf(f *Func) []CopySite {
	if f == nil || f.Sig == nil || len(f.Sig.TParams) == 0 {
		return nil
	}
	if f.Decl != nil && f.Decl.Body != nil && f.Decl.Extern == nil {
		if f.Pkg != nil && f.Pkg.Info != nil {
			return f.Pkg.Info.Copies[f]
		}
		return nil
	}
	if f.Decl != nil && f.Decl.Extern != nil {
		if path, name := f.Decl.Extern.Split(); path == runtimeModule+"/v1" && v1.MovesOnly(name) {
			return nil
		}
	}
	out := make([]CopySite, len(f.Sig.TParams))
	for i := range f.Sig.TParams {
		out[i] = CopySite{TParam: i, What: "its body is Go code that may copy it, and runtime/v1 does not list it as moving its values only"}
	}
	return out
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
	c.mapKeyCopies()
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

// mapKeyCopies records, for a generic function, a type parameter it uses
// as a map key (the A1 site whose key names a type parameter).
func (c *checker) mapKeyCopies() {
	owner := map[*TypeParam]*Func{}
	for _, f := range c.funcs {
		if f.Sig == nil {
			continue
		}
		for _, tp := range f.Sig.TParams {
			owner[tp] = f
		}
	}
	for _, s := range c.resourceSites {
		if s.name != "" || s.t == nil || isInvalid(s.t) {
			continue
		}
		eachTypeParam(s.t, func(tp *TypeParam) {
			f := owner[tp]
			if f == nil {
				return
			}
			i := slices.Index(f.Sig.TParams, tp)
			c.addCopy(f, CopySite{TParam: i, At: s.at, Where: c.sitePos(s.file, s.at), What: "it is a map key", Key: true})
		})
	}
}

// sitePos is n's position as file:line:col.
func (c *checker) sitePos(fi *fileInfo, n syntax.Node) string {
	line, col := fi.syntax.Position(n.Pos())
	if fi.syntax.Name == "" {
		return fmt.Sprintf("%d:%d", line, col)
	}
	return fmt.Sprintf("%s:%d:%d", fi.syntax.Name, line, col)
}

// addCopy records site for f when its type parameter has none yet.
func (c *checker) addCopy(f *Func, site CopySite) bool {
	for _, s := range c.pkg.Info.Copies[f] {
		if s.TParam == site.TParam {
			return false
		}
	}
	c.pkg.Info.Copies[f] = append(c.pkg.Info.Copies[f], site)
	return true
}

// takesPass walks f's body once with inference on; it reports whether f's
// clause or copy sites grew.
func (c *checker) takesPass(f *Func) bool {
	c.cur = f.file
	w := c.newOwnWalk()
	w.infer = true
	w.fn = f
	w.copies = map[*TypeParam]CopySite{}
	w.promote = map[int]bool{}
	w.frame(f.Decl.Body, takesOf(f), w.paramVars(f.Decl.Recv, f.Decl.Type.Params), f.Decl.Name.Name, "", f.Name == "Close" && c.pkg.Info.Closes[f.Decl] != nil)
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
	for i, tp := range f.Sig.TParams {
		if s, ok := w.copies[tp]; ok {
			s.TParam = i
			if c.addCopy(f, s) {
				changed = true
			}
		}
	}
	return changed
}

// instance checks a call of a generic function f whose type arguments are
// targs: a type argument that holds a resource, bound to a type parameter
// f copies, is S108 (S112 for a map key); one built from the caller's own
// type parameter is the caller's copy site.
func (w *ownWalk) instance(x *syntax.CallExpr, f *Func, targs []Type) {
	for _, s := range w.c.copiesOf(f) {
		if s.TParam >= len(targs) || s.TParam >= len(f.Sig.TParams) {
			continue
		}
		t := targs[s.TParam]
		if t == nil || isInvalid(t) {
			continue
		}
		tp := f.Sig.TParams[s.TParam]
		where := ""
		if s.Where != "" {
			where = " at " + s.Where
		}
		switch {
		case w.c.holdsResource(t):
			if s.Key {
				w.reportCode(x, "S112", "%s cannot take a %s: %s uses its %s as a map key%s, and a map key has no owner to release it (resources R40); key the map by a plain value, such as an id", f.Name, t, f.Name, tp.Name, where)
				continue
			}
			w.reportCode(x, CodeType, "%s cannot take a %s: %s copies its %s%s (%s), and %s is a resource, which has one owner (resources R44, plan 1.3); pass a value that is not a resource", f.Name, t, f.Name, tp.Name, where, s.What, t)
		case w.c.mayHoldResource(t):
			what := fmt.Sprintf("%s copies its %s", f.Name, tp.Name)
			if s.Key {
				what = fmt.Sprintf("%s uses its %s as a map key", f.Name, tp.Name)
			}
			w.copySite(t, x, what, s.Key)
		}
	}
}

// copySite records x as the copy site of each of the walked function's
// type parameters that t is built from.
func (w *ownWalk) copySite(t Type, x syntax.Node, what string, key bool) {
	if w.copies == nil || w.fn == nil {
		return
	}
	eachTypeParam(t, func(tp *TypeParam) {
		if !slices.Contains(w.fn.Sig.TParams, tp) {
			return
		}
		if _, ok := w.copies[tp]; !ok {
			w.copies[tp] = CopySite{At: x, Where: w.c.sitePos(w.c.cur, x), What: what, Key: key}
		}
	})
}

// funcValue checks a function named as a value (not called): its takes
// clause is part of its type, so an inferred clause that takes something
// is written (Gap 39). inst is the value's type at this instance: a taken
// parameter whose type is plain data there takes nothing, and one that is
// a resource only through the walked function's type parameter is its
// copy site (the value lends what the body hands off).
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
		if t := sig.Params[i].Type; w.c.holdsResource(t) {
			held = true
		} else if w.c.mayHoldResource(t) {
			w.copySite(t, x, fmt.Sprintf("%s is used as a value and takes %s", f.Name, sig.Params[i].Name), false)
		}
	}
	if !held {
		return
	}
	w.reportCode(x, CodeType, "%s is used as a value, so its takes clause is part of its type, and its body %s (resources R19, Gap 39); write %s in its signature", f.Name, takesText(&sig), takesText(&sig))
}
