package lower

import (
	"sort"
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// Hand-over in place (OWN1; plan IR-G §3.1). The value rule (values.go)
// writes a slice in place only through a let mut or a borrow root, or on a
// dead unshared local (V3); elsewhere Append appends to a clipped copy and
// Concat makes a new array, so a loop that grows a value it holds in a
// field or rebinds through slices.Concat is quadratic. Three shortcuts
// take the write in place, each registered in shortcuts.go:
//
//   - own-consume (static): the first operand of a runtime/v1 body whose
//     owned form has a checking form (v1.Body.Check: SlicesConcat's
//     SlicesConcatOwned) is consumed, so the owned form takes it over: on
//     a dead unshared local (consumes, values.go), and on the rebinding
//     p = slices.Concat(p, q) of one place rooted at a let mut or a borrow
//     that names the root nowhere else (rootConsumes), as V3 takes
//     p = p.Append(x). The other operands' elements enter the root, so
//     they are copied unless fresh, as Append's element is (V1).
//   - own-field (static): Append on a field path r.f… of a local that is
//     dead after the call and unshared (fresh): V3's dead source, through
//     record fields (deadField). The journal's Ref.Update literal
//     l.Signals.Append(e), whose l the cell hands over (V6), is one.
//   - own-lineage (checked at run time): Append on any other field path
//     not rooted at a let mut or a borrow (an actor state's i.Log) lowers
//     to v1.LineageAppend with a token of its own (runtime/v1, Lineage):
//     in place only when the receiver is exactly the slice this site made
//     last, else to a copy. Its proof is the token's: every array such a
//     site writes past a length is one it made, and the analysis never
//     counts its result as fresh (fresh), so no root, owned form or Go
//     call takes that array uncopied.
//
// A test build takes none of them: own-consume calls the checking form
// (SlicesConcatPoison: the copy, then the consumed array cleared),
// own-field calls v1.AppendPoison (likewise), and own-lineage calls
// v1.LineageAppendCheck (the copy, and where the run build would append
// in place, the spare room cleared first). A read the proof missed reads
// zero values there, so the program's own tests see it.
//
// The reference is the copying lowering (valueLowering "copy"), which
// takes none; TestHandoverAgrees holds the rule, the reference and a test
// build to a value model. ergo build -report copies prints each site's
// form and why (CopySite).
//
// Prior art: Swift appends in place when the array's reference count says
// it is unique; Erlang's BEAM appends to a binary in place when it is the
// last one made from its buffer (a writable binary), which is the run-time
// check own-lineage makes; Koka and Lean reuse a value whose count is one
// (functional but in place); Go's design 74299 frees and appends by an
// alias analysis it keeps conservative. Ergo has no count: the static rows
// read the value rule's liveness, and the run-time row compares one token.

// CopySite is one Append, Concat or other owned-form site of a lowering and
// the form it takes (ergo build -report copies).
type CopySite struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	// Form is "in place", "one extender" (own-lineage) or "copy".
	Form string `json:"form"`
	Why  string `json:"why"`
}

// CopyReport receives the CopySites of a lowering (Unit.Copies), sorted by
// position, one per source site.
type CopyReport struct {
	Sites []CopySite
}

// appenderConsumes marks, once V3 has decided, each in-place call of an
// appender (gobody.go, goBodyAppends: slices.Concat's SlicesConcat, whose
// result is its first operand with elements added) whose runtime/v1 body
// has an owned form with a checking form: V3 proved the source writable
// (p = slices.Concat(p, q) on a let mut or a borrow root, or a dead
// unshared local), but the plain body still makes a new array, so the
// call takes the owned form, which appends to the source (own-consume).
func (w *valueWalk) appenderConsumes() {
	if !valueOwned() {
		return
	}
	for _, call := range w.calls {
		c := w.info.Calls[call]
		if c == nil || call.Trailing != nil || !w.vf.inPlace[call] || !appenderExtern(c.Func) || !w.l.checkedOwnedForm(c.Func) {
			continue
		}
		w.vf.consumes[call] = true
	}
}

// spreads reports an operand of an appender that gives its elements, not
// itself: its type is the call's own slice type (slices.Concat's b), so
// only its elements enter the result.
func (w *valueWalk) spreads(call *syntax.CallExpr, a syntax.Expr) bool {
	t, r := w.typeOf(a), w.typeOf(call)
	return t != nil && r != nil && t.String() == r.String() && elemOf(t) != nil
}

// checkedOwnedForm reports a std function whose runtime/v1 body has an
// owned form with a checking form (v1.Body.Check).
func (l *lowerer) checkedOwnedForm(f *check.Func) bool {
	owned := l.ownedForm(f)
	if owned == "" {
		return false
	}
	b, err := v1.BodyOf(owned)
	return err == nil && b.Check != ""
}

// fieldPath is the variable that heads x when x is a field path r.f.g…
// (one field at least, through parentheses) of record fields; else nil.
func (w *valueWalk) fieldPath(x syntax.Expr) *syntax.Ident {
	sel, ok := unparen(x).(*syntax.SelectorExpr)
	if !ok {
		return nil
	}
	for {
		t := w.typeOf(sel.X)
		n, ok := t.(*check.Named)
		if !ok || n.Obj == nil || n.Obj.Pkg == nil || n.Obj.Pkg.Host() {
			return nil
		}
		if _, ok := n.Underlying().(*check.Struct); !ok {
			return nil
		}
		switch inner := unparen(sel.X).(type) {
		case *syntax.Ident:
			if _, ok := w.info.Uses[inner].(*check.Var); !ok {
				return nil
			}
			return inner
		case *syntax.SelectorExpr:
			sel = inner
		default:
			return nil
		}
	}
}

// appendSite reports a call of the prelude's Append of two operands: a
// source and one element.
func (w *valueWalk) appendSite(call *syntax.CallExpr) bool {
	c := w.info.Calls[call]
	if c == nil || c.Kind != check.CallFunc || !preludeAppend(c.Func) {
		return false
	}
	ops := callOperands(call, c)
	return len(ops) == 2 && !w.spreads(call, ops[1])
}

// lineageSites marks, before any freshness is read, every Append on a
// field path not rooted at a let mut or a borrow that V3's rebinding
// pattern does not take in place: a one-extender site (own-lineage) whose
// result is never fresh. deadField then takes back the sites whose source
// is dead.
func (w *valueWalk) lineageSites() {
	if !valueOwned() {
		return
	}
	for _, call := range w.calls {
		if w.vf.inPlace[call] || !w.appendSite(call) {
			continue
		}
		id := w.fieldPath(w.recvOf(call))
		if id == nil {
			continue
		}
		if v, ok := w.info.Uses[id].(*check.Var); ok && w.varOf(v).root() {
			continue
		}
		w.vf.lineage[call] = true
	}
}

// deadField reports an Append on a field path of a local that is dead
// after the call and unshared (own-field): no binding reads the source's
// array again, so the Append writes it, as V3 writes a dead local.
func (w *valueWalk) deadField(call *syntax.CallExpr) bool {
	if !valueOwned() || !w.appendSite(call) {
		return false
	}
	recv := w.recvOf(call)
	if w.fieldPath(recv) == nil {
		return false
	}
	return w.fresh(recv)
}

// settleLineage turns back to the plain clipped Append each one-extender
// site whose result the analysis copies anyway (a root takes it): the
// clipped Append's new array is fresh when its elements hold no Map or
// slice, so the copy goes too.
func (w *valueWalk) settleLineage() {
	for _, call := range w.calls {
		if w.vf.lineage[call] && w.vf.clone[call] && !w.l.holdsValue(elemOf(w.typeOf(call))) {
			delete(w.vf.lineage, call)
			delete(w.vf.clone, call)
		}
	}
}

// ownedFormAt is the admission of own-consume: the runtime/v1 name a
// consumed call (values.go, consumes) calls, the owned form, or in a test
// build its checking form. An owned form with no checking form is not
// this shortcut's (SlicesSortOwned): the call takes it as before.
func (fc *funcCtx) ownedFormAt(x *syntax.CallExpr, body v1.Body) string {
	owned, err := v1.BodyOf(body.Owned)
	if err != nil {
		ice(x, "%s: %v", body.Owned, err)
	}
	if owned.Check == "" {
		return body.Owned
	}
	if fc.l.takes("own-consume") {
		fc.l.took("own-consume")
		fc.copySite(x, "in place", "the first operand is consumed: a rebinding of a let mut or a borrow root, or a dead unshared local")
		return body.Owned
	}
	fc.copySite(x, "copy", "a test build: the checking form copies and clears the consumed array")
	return owned.Check
}

// ownFieldOK is the admission of own-field: whether the Append on a dead
// field path writes its source (the run build) or takes the checking form.
func (fc *funcCtx) ownFieldOK(x *syntax.CallExpr) bool {
	if fc.l.takes("own-field") {
		fc.l.took("own-field")
		fc.copySite(x, "in place", "the source is a field of a local that is dead after the call and unshared")
		return true
	}
	fc.copySite(x, "copy", "a test build: the checking form copies and clears the source's array")
	return false
}

// lineageOK is the admission of own-lineage: whether the one-extender
// site appends in place when its token matches (the run build) or takes
// the checking form.
func (fc *funcCtx) lineageOK(x *syntax.CallExpr) bool {
	if fc.l.takes("own-lineage") {
		fc.l.took("own-lineage")
		fc.copySite(x, "one extender", "in place when the source is the slice this site made last (checked at run time), else a copy")
		return true
	}
	fc.copySite(x, "copy", "a test build: the checking form copies and clears the spare room the run build would write")
	return false
}

// handoverAppend is the Go text of an Append the hand-over takes (own-field
// in a test build, own-lineage), or ""; vals are its lowered operands.
// Every other Append is recorded in the copy report here.
func (fc *funcCtx) handoverAppend(x *syntax.CallExpr, c *check.Call, vals []string) string {
	vf := fc.l.vals
	if vf == nil || !preludeAppend(c.Func) || len(vals) != 2 || len(callOperands(x, c)) != 2 {
		return ""
	}
	switch {
	case vf.lineage[x]:
		form := "LineageAppend"
		if !fc.lineageOK(x) {
			form = "LineageAppendCheck"
		}
		return fc.l.rt() + "." + form + "(&" + fc.l.lineageToken(x) + ", " + vals[0] + ", " + vals[1] + ")"
	case vf.field[x]:
		if fc.ownFieldOK(x) {
			return ""
		}
		return fc.l.rt() + ".AppendPoison(" + vals[0] + ", " + vals[1] + ")"
	case vf.inPlace[x] && valueRule():
		fc.copySite(x, "in place", "a rebinding p = p.Append(x) of a let mut or a borrow root, or a dead unshared local (V3)")
	default:
		fc.copySite(x, "copy", "the source may be read again and is no field path that one site extends")
	}
	return ""
}

// lineageToken is the Go text of the token of the one-extender site x: an
// element of the package's array of Lineages, one per site.
func (l *lowerer) lineageToken(x *syntax.CallExpr) string {
	if l.lineages == nil {
		l.lineages = map[*syntax.CallExpr]int{}
	}
	i, ok := l.lineages[x]
	if !ok {
		i = len(l.lineages)
		l.lineages[x] = i
	}
	return l.pkgName(lineagesKey, "ergoLineages") + "[" + strconv.Itoa(i) + "]"
}

// lineagesKey names the package's array of Lineages.
var lineagesKey = pkgKey{"lineages", nil}

// lineageDecl is the Go declaration of the package's array of Lineages, or
// "" when no site has one.
func (l *lowerer) lineageDecl() string {
	if len(l.lineages) == 0 {
		return ""
	}
	return "var " + l.pkgName(lineagesKey, "ergoLineages") + " [" + strconv.Itoa(len(l.lineages)) + "]" + l.rt() + ".Lineage\n\n"
}

// copySite records the form of the site x in the copy report.
func (fc *funcCtx) copySite(x *syntax.CallExpr, form, why string) {
	file, line, col := fc.l.locate(x, x.Pos())
	fc.l.stats.Copies = append(fc.l.stats.Copies, CopySite{File: file, Line: line, Column: col, Form: form, Why: why})
}

// copyReport is the copy report of stats: one site per position, sorted.
func copyReport(sites []CopySite) []CopySite {
	seen := map[[3]any]bool{}
	var out []CopySite
	for _, s := range sites {
		k := [3]any{s.File, s.Line, s.Column}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Column < b.Column
	})
	return out
}
