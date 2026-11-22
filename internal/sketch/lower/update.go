package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Record updates (V7 for `..`; OWN1 UPDATE, fk-domains' check). A record
// literal with a base, T{..p, f: v}, holds every field of p's value but
// the ones it sets. A rebinding p = T{..p, …} hands its base over
// (parts.go, selfBases; REUSE RU1), and a path under a field the update
// names is apart from the base by type (baseCovers). This file adds the
// unshared premise, under the value rule's reference (valueLowering=copy,
// TestValueRuleAgrees holds the owned rule to it):
//
//   - apart (baseDisjoint): the base holds no part of a field the literal
//     sets, so a read of p.f, with f set, shares nothing with what the base
//     gives the literal when no two fields of p's value share storage. An
//     unshared value (a let mut, a handed or owned parameter) has no such
//     sharing: each part entered it fresh (freshen: a second use of one
//     value is copied), as V3's in-place write on p.f already assumes.
//     Another value may hold one slice in two fields (Two{A: xs, B: xs}
//     of a let), so there f's type must share no Map or slice with a field
//     the base keeps (mayShare, as disjointRead). So in
//     Two{..t, A: t.A.Append(v)}, A and B both []int, the base and the
//     Append are apart when t is unshared, and the literal is fresh when t
//     is (freshFrom: growT's result shares no storage but t's, so
//     t = growT(t, i) hands t over).
//
// Prior art: Rust's struct update syntax `T { f: v, ..p }` moves p's
// remaining fields (p is consumed unless they are Copy); OCaml's
// `{ p with f = v }` and Elm's `{ p | f = v }` share the unchanged fields
// with p, which immutability makes safe; Koka and Lean reuse p's cell in
// place when its reference count is one (Perceus, functional but in
// place), which the value rule decides statically.

// baseDisjoint reports whether base, a use of a variable as the base of a
// record literal, and read, a use of the same variable that heads a field
// path p.f…, are apart: the literal sets f, and the variable's value is
// unshared or f's type can share no Map or slice with a field the literal
// takes from the base (above).
func (w *valueWalk) baseDisjoint(base, read *syntax.Ident, unshared bool) bool {
	if !valueOwned() || valueMutant == "update-base-blind" || base == read {
		return false
	}
	parent, child := w.unparenParent(base)
	lit, ok := parent.(*syntax.CompositeLit)
	if !ok || lit.Base != child {
		return false
	}
	sel, ok := w.parentOf(read).(*syntax.SelectorExpr)
	if !ok || syntax.Node(sel.X) != syntax.Node(read) || w.dotCall(read) != nil {
		return false
	}
	n, ok := w.typeOf(base).(*check.Named)
	if !ok || n.Obj == nil || n.Obj.Pkg == nil || n.Obj.Pkg.Host() {
		return false
	}
	st, ok := n.Underlying().(*check.Struct)
	if !ok {
		return false
	}
	set := map[string]bool{}
	for _, el := range lit.Elts {
		kv, ok := el.(*syntax.KeyValueExpr)
		if !ok {
			return false
		}
		key, ok := kv.Key.(*syntax.Ident)
		if !ok {
			return false
		}
		set[key.Name] = true
	}
	if !set[sel.Sel.Name] {
		return false
	}
	if unshared {
		return true
	}
	ft := w.typeOf(sel)
	for _, f := range st.Fields {
		if !set[f.Name] && w.l.mayShare(ft, f.Type) {
			return false
		}
	}
	return true
}
