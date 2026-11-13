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
//   - own-consume (static): slices.Concat is an appender (gobody.go), so
//     V3 already proves p = slices.Concat(p, q) on a let mut or a borrow
//     root, and Concat on a dead unshared local, free to write its source;
//     but its body made a new array anyway. An in-place call of an
//     appender whose runtime/v1 body has an owned form with a checking
//     form (v1.Body.Check) now calls the owned form, SlicesConcatOwned,
//     which appends to the source (appenderConsumes). A spread operand
//     whose elements hold no Map or slice is no longer copied: only its
//     elements enter the root.
//   - own-field (static): Append on a field path r.f… of a local that is
//     dead after the call and unshared (fresh): V3's dead source, through
//     record fields (deadField). Three facts let more sources be fresh: a
//     read of a disjoint field whose value holds no Map or slice (r.head
//     beside r.items) does not count as another use (disjointRead); a use
//     inside a return statement is the last of its frame for every use
//     outside that statement (endingReturn); and a parameter taken over
//     through its fields (`let mut items = r.items`, r.items.Append(v),
//     reads of r.head) is owned (fieldOwned, V5), so callers pass it
//     unshared and r = push(r, v) hands it over (GP's ring-copy).
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

// fieldNames is the field names of the field path x (r.f.g gives [f g]),
// or nil when x is no field path of record fields.
func (w *valueWalk) fieldNames(x syntax.Expr) []string {
	if w.fieldPath(x) == nil {
		return nil
	}
	var names []string
	for e := unparen(x); ; {
		sel, ok := e.(*syntax.SelectorExpr)
		if !ok {
			break
		}
		names = append([]string{sel.Sel.Name}, names...)
		e = unparen(sel.X)
	}
	return names
}

// disjointRead reports whether the use id is a read of a field path of
// its variable that path does not overlap (the two differ at a field) and
// whose value holds no Map or slice: r.head beside r.items. Such a read
// keeps no storage of path's value and cannot see a write through it.
func (w *valueWalk) disjointRead(id *syntax.Ident, path syntax.Expr) bool {
	want := w.fieldNames(path)
	if len(want) == 0 {
		return false
	}
	cur := syntax.Expr(id)
	for {
		sel, ok := w.parent[cur].(*syntax.SelectorExpr)
		if !ok || syntax.Expr(sel.X) != cur || w.dotCall(cur) != nil {
			break
		}
		cur = sel
	}
	got := w.fieldNames(cur)
	if len(got) == 0 || w.l.holdsValue(w.typeOf(cur)) {
		return false
	}
	for i := 0; i < len(got) && i < len(want); i++ {
		if got[i] != want[i] {
			return true
		}
	}
	return false
}

// fieldOwned reports a parameter vv of f that the body takes over through
// its fields (GP ring-copy; types.md, Bindings 2): every use is a field
// path of vv in the body's own frame and outside any loop, one such path
// holding a Map or slice is taken (the value of a let mut, or the source
// of an Append), and f cannot fail after the first taking. Such a
// parameter is owned (V5), as a consumed one: its callers pass it
// unshared, and the body reads it as an unshared local, so
// `let mut items = r.items` copies nothing and r.items.Append(v) appends
// in place (own-field), each when no other use reads that field again.
func (w *valueWalk) fieldOwned(f *check.Func, vv *vvar) bool {
	if !valueOwned() || vv.ptr || vv.local || len(vv.occs) == 0 {
		return false
	}
	first, binds := -1, false
	for _, o := range vv.occs {
		if o.frame != w.root || len(o.loops) > 0 {
			return false
		}
		sel, ok := w.parent[o.id].(*syntax.SelectorExpr)
		if !ok || syntax.Node(sel.X) != syntax.Node(o.id) || w.dotCall(o.id) != nil || w.fieldPath(sel) == nil {
			return false
		}
		cur := syntax.Expr(sel)
		for {
			up, ok := w.parent[cur].(*syntax.SelectorExpr)
			if !ok || syntax.Expr(up.X) != cur || w.dotCall(cur) != nil {
				break
			}
			cur = up
		}
		if !w.l.holdsValue(w.typeOf(cur)) {
			continue
		}
		taken, bound := w.takenField(cur)
		if !taken {
			continue
		}
		if first < 0 || o.pre < first {
			first = o.pre
		}
		binds = binds || bound
	}
	// Only a let mut taking makes the parameter owned: the body copies
	// there anyway, so the copy moves to a caller that shares (none, for a
	// rebinding r = f(r, …)). An Append alone is the one-extender site's.
	if first < 0 || !binds {
		return false
	}
	return len(rowElems(f.Sig.Errors)) == 0 || !w.failsAfter(f, first)
}

// takenField reports a field path that is the source of an Append
// (taken) or the whole value of a let mut (taken and bound).
func (w *valueWalk) takenField(x syntax.Expr) (taken, bound bool) {
	parent, child := w.unparenParent(x)
	if call := w.dotCall(x); call != nil {
		return w.appendSite(call), false
	}
	if call, ok := parent.(*syntax.CallExpr); ok {
		return w.appendSite(call) && w.recvOf(call) == child, false
	}
	for _, o := range w.owns {
		if o.value == child && o.root.local && o.root.mut {
			return true, true
		}
	}
	return false, false
}

// endingReturn is the return statement of o's own frame that holds the
// use o at any depth (an operand of a call in the returned value), or
// nil: once it runs the frame ends, so a use outside it never runs after
// o.
func (w *valueWalk) endingReturn(o *vocc) *syntax.ReturnStmt {
	for n := w.parent[o.id]; n != nil; n = w.parent[n] {
		if f := w.frameOf[n]; f != nil {
			return nil
		}
		if r, ok := n.(*syntax.ReturnStmt); ok {
			if w.frameOfReturn(r) == o.frame {
				return r
			}
			return nil
		}
	}
	return nil
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
