package check

import (
	"slices"

	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// A mapped text (types.md, Borrows 8; wf10 MAPPED). fs.Text is the text of
// an fs.Mapped, the bytes of a file mapped into memory, which the Mapped's
// Close unmaps. Its Go form is the string that points into the mapping,
// so a text read after Close would read unmapped memory. A text is a
// borrow of its Mapped, by the rules of a borrow, with no lifetime written:
//
//  1. Placement. fs.Text is written only as a parameter's type (std fs
//     declares Mapped.Text's result): never a result, a field, an element,
//     a type argument or a let's written type (textPlacement).
//  2. Positions. A value of fs.Text stands only as the value of a let of
//     one name that is not mut (its local then holds the text), an
//     argument of a parameter of type fs.Text, the operand of an index, a
//     slice or len, an operand of a comparison, or the range of a for;
//     never captured by a frame that may outlive the call or run on
//     another task (escaping.go, borrowUse). No conversion makes a string
//     of a text or a text of a string; fs.Clone copies one. Every other
//     place is S108 (checkMappedText). The rule is an allow-list over the
//     parent, as Borrows 6 is, so a position the checker gains later is
//     refused until it is listed here.
//  3. Lifetime. Every use of a text is a use of its Mapped (ownership.go,
//     textUse): `let t = m.Text()` and `let u = t[a:b]` make t and u texts
//     of m's root owner, so a use of either after m is closed or handed
//     off, on any path, is S108 naming the hand-off, and a call that is
//     passed a text of m and takes m is S108 (R17's lend and hand-off in
//     one call).
//
// So no text outlives its Mapped's Close, and the lowering's string is
// never read after the unmap (runtime/v1 Mapped's safety invariant).
// TestMappedTextModel holds the checker to a model of the lifetime.
//
// Prior art: Rust's memmap2 gives `&[u8]` borrowed from the Mmap, so the
// borrow checker refuses a read after drop; Swift's ~Escapable spans
// borrow their owner; Go's x/exp/mmap copies through ReadAt instead, and
// Java's MappedByteBuffer is never unmapped until collected. Gleam, Elixir
// and ReScript read files whole.

const (
	textPlaceText    = "fs.Text is a borrow of its Mapped and stands only as a parameter's type, never as a result, a field, an element, a type argument or a let's type (types.md, Borrows 8); take the text as a parameter, or copy it with fs.Clone"
	textValueText    = "%s is a mapped text (fs.Text), a borrow of its Mapped: it stands only as a let's value, an argument of an fs.Text parameter, the operand of an index, a slice, len or a comparison, or the range of a for (types.md, Borrows 8); copy it with fs.Clone to keep it"
	textMutText      = "%s is a mapped text (fs.Text), a borrow of its Mapped, so a let mut cannot hold it: one let holds one text (types.md, Borrows 8); bind it with let"
	textConvText     = "fs.Text converts to and from nothing: a text is a borrow of its Mapped (m.Text()), and fs.Clone copies one into a string (types.md, Borrows 8)"
	textCaptureText  = "%s is a mapped text (fs.Text), a borrow of its Mapped, and this function literal or block may outlive the call or run on another task; an escaping frame never captures a text: copy it with fs.Clone before it (types.md, Borrows 8)"
	textAfterText    = "%s is a text of %s, which was handed off at %s (%s): a text is a borrow of its Mapped, so a use of %s is a use of %s (types.md, Borrows 8); use %s before %s is handed off, or copy it with fs.Clone"
	textMaybeText    = "%s is a text of %s, which may have been handed off at %s (%s) on one path: a text is a borrow of its Mapped, so a use of %s is a use of %s (types.md, Borrows 8); hand %s off on every path or on none"
	textMutOwnerText = "%s is a let mut or a borrow, and an assignment to it would release the Mapped its text borrows (types.md, Borrows 8); read the text of a Mapped that a let or a parameter holds"
	textTempText     = "the Mapped of this text is a temporary, released at the end of its statement, and the let would keep its text (types.md, Borrows 8); bind the Mapped with let, then its text"
	textTakenText    = "%s is a text of %s, whose field was taken at %s: a text is a borrow of its Mapped's owner (types.md, Borrows 8); use %s before the field is taken"
)

// IsMappedText reports whether t is std fs's Text, a mapped text.
func IsMappedText(t Type) bool {
	n, ok := t.(*Named)
	return ok && n.Obj != nil && n.Obj.Name == "Text" && n.Obj.Pkg != nil && n.Obj.Pkg.Path == "fs" && n.Obj.Pkg.Std()
}

// isMapped reports whether t is std fs's Mapped.
func isMapped(t Type) bool {
	n, ok := t.(*Named)
	return ok && n.Obj != nil && n.Obj.Name == "Mapped" && n.Obj.Pkg != nil && n.Obj.Pkg.Path == "fs" && n.Obj.Pkg.Std()
}

// textPlacement refuses fs.Text written as the type x outside a
// parameter's type (rule 1): ctx.textTop is the parameter's type
// expression, the one place it may stand. Std fs declares it.
func (c *checker) textPlacement(x syntax.Expr, ctx *typeCtx) bool {
	if c.pkg.Std() && c.pkg.Path == "fs" {
		return true
	}
	if ctx != nil && ctx.textTop != nil && unparen(ctx.textTop) == unparen(x) {
		return true
	}
	c.errorf(x, CodeType, textPlaceText)
	return false
}

// checkMappedText applies rule 2 at n, the node being visited, before its
// children: a conversion to or from fs.Text, and a value of fs.Text in a
// place that is not listed.
func (w *escWalk) checkMappedText(n syntax.Node) {
	x, ok := n.(syntax.Expr)
	if !ok || w.collecting {
		return
	}
	if call, ok := x.(*syntax.CallExpr); ok {
		if k := w.info.Calls[call]; k != nil && k.Kind == CallConversion && len(call.Args) == 1 {
			if IsMappedText(k.Type) || IsMappedText(w.info.Types[call.Args[0]]) {
				w.c.errorf(call, CodeType, textConvText)
				return
			}
		}
	}
	if !IsMappedText(w.info.Types[x]) {
		return
	}
	w.textOwner(x)
	if _, ok := x.(*syntax.BasicLit); ok {
		return // a constant borrows nothing
	}
	if _, ok := x.(*syntax.ParenExpr); ok {
		return // the expression inside is judged
	}
	if w.textPlace(x) {
		return
	}
	text := exprText(x)
	if text == "this expression" {
		text = "this text"
	}
	w.c.errorf(x, CodeType, textValueText, text)
}

// textOwner refuses m.Text() where m's root is a let mut or a borrow: an
// assignment there would release the Mapped and keep the text, so a text
// is read only from a Mapped whose owner no assignment replaces (rule 3).
func (w *escWalk) textOwner(x syntax.Expr) {
	call, ok := x.(*syntax.CallExpr)
	if !ok {
		return
	}
	k := w.info.Calls[call]
	if k == nil || k.Kind != CallOp || k.Op == nil || k.Op.Name != "Text" || k.Recv == nil || !isMapped(w.info.Types[k.Recv]) {
		return
	}
	id := textRootIdent(k.Recv)
	if id == nil {
		return
	}
	if v, ok := w.info.Uses[id].(*Var); ok && (v.mut || isPointer(v.Type)) {
		w.c.errorf(id, CodeType, textMutOwnerText, id.Name)
	}
}

// textRootIdent is the identifier at the root of the place x, through
// fields and indexes, or nil.
func textRootIdent(x syntax.Expr) *syntax.Ident {
	for {
		switch r := unparen(x).(type) {
		case *syntax.SelectorExpr:
			x = r.X
		case *syntax.IndexExpr:
			x = r.X
		case *syntax.Ident:
			return r
		default:
			return nil
		}
	}
}

// textEscapingFrom is the outermost frame between the frame of v's
// declaration and the use that may outlive the call it is passed to, or
// nil. A frame passed to a callback that its callee ends every run of
// before it returns (v1.EndsRunsBeforeReturn: tasks.Transform's f) does
// not: it runs on other tasks, but only during the call, and a text is
// read only, so its runs read it at once with no race.
func (w *escWalk) textEscapingFrom(v *Var) syntax.Node {
	from, ok := w.varFrame[v]
	if !ok {
		return nil
	}
	for k := from + 1; k < len(w.frames); k++ {
		if w.frames[k] && !w.endsBeforeReturn(w.frameNodes[k]) {
			return w.frameNodes[k]
		}
	}
	return nil
}

// endsBeforeReturn reports whether the frame n is the argument of a std
// call for a callback parameter that the call's reviewed body ends every
// run of before it returns.
func (w *escWalk) endsBeforeReturn(n syntax.Node) bool {
	j := slices.Index(w.stack, n)
	if j < 1 {
		return false
	}
	child := n
	for j--; j >= 0; j-- {
		if _, ok := w.stack[j].(*syntax.ParenExpr); ok {
			child = w.stack[j]
			continue
		}
		break
	}
	if j < 0 {
		return false
	}
	call, ok := w.stack[j].(*syntax.CallExpr)
	if !ok {
		return false
	}
	k := w.info.Calls[call]
	if k == nil || k.Kind != CallFunc || k.Recv != nil || k.Func == nil || k.Func.Pkg == nil || !k.Func.Pkg.Std() || k.Func.Decl == nil || k.Func.Decl.Extern == nil {
		return false
	}
	path, name := k.Func.Decl.Extern.Split()
	if path != runtimeModule+"/v1" {
		return false
	}
	p, ok := v1.EndsRunsBeforeReturn(name)
	if !ok {
		return false
	}
	if p == len(call.Args) {
		return call.Trailing == child
	}
	return p < len(call.Args) && call.Args[p] == child
}

// textPlace reports whether x, a value of fs.Text, stands in a listed
// place (rule 2).
func (w *escWalk) textPlace(x syntax.Expr) bool {
	parent, child := w.parentOf(x)
	switch p := parent.(type) {
	case *syntax.VarSpec:
		if len(p.Names) != 1 || p.Pattern != nil || len(p.Values) != 1 || p.Values[0] != child {
			return false
		}
		if p.Names[0].Name == "_" {
			return true
		}
		v, ok := w.info.Defs[p.Names[0]].(*Var)
		if !ok || v.pkg {
			return false
		}
		if v.mut {
			w.c.errorf(p.Names[0], CodeType, textMutText, p.Names[0].Name)
		}
		return true
	case *syntax.CallExpr:
		if p.Fun == child {
			return false
		}
		k := w.info.Calls[p]
		if k == nil {
			return true // an unchecked call has its own diagnostic
		}
		if k.Kind == CallLen {
			return true
		}
		for i, a := range p.Args {
			if a == child {
				return IsMappedText(w.paramType(p, i))
			}
		}
	case *syntax.IndexExpr:
		return p.X == child
	case *syntax.SliceExpr:
		return p.X == child
	case *syntax.BinaryExpr:
		switch p.Op {
		case syntax.EQL, syntax.NEQ, syntax.LSS, syntax.LEQ, syntax.GTR, syntax.GEQ:
			return true
		}
	case *syntax.RangeStmt:
		return p.X == child
	}
	return false
}

// paramType is the type of the parameter the i-th argument of call binds,
// or nil: the trailing block is the argument after the written ones, and
// a dot call's receiver is the first parameter (paramNonEscaping).
func (w *escWalk) paramType(call *syntax.CallExpr, i int) Type {
	k := w.info.Calls[call]
	if k == nil {
		return nil
	}
	var sig *Signature
	switch k.Kind {
	case CallFunc:
		if k.Func != nil {
			sig = k.Func.Sig
		}
		if k.Recv != nil {
			i++
		}
	case CallOp, CallProviderOp, CallLawOp:
		if k.Op != nil {
			sig = k.Op.Sig
		}
	case CallFuncValue:
		sig, _ = under(w.info.Types[call.Fun]).(*Signature)
	}
	if sig == nil || i >= len(sig.Params) {
		return nil
	}
	return sig.Params[i].Type
}

// textRootOf is the owner a text x borrows, as the local that roots it,
// with the identifier in x that names it (the text's local, or the root
// of m in m.Text()), or nil for a text of no local owner (a parameter's,
// a constant): a slice of a text is a text of the same owner, `m.Text()`
// is a text of m's root, and a local bound to a text is a text of that
// text's owner.
func (w *ownWalk) textRootOf(x syntax.Expr) (*Var, *syntax.Ident) {
	switch x := unparen(x).(type) {
	case *syntax.SliceExpr:
		return w.textRootOf(x.X)
	case *syntax.Ident:
		v, _ := w.info.Uses[x].(*Var)
		if r := w.textRoots[v]; r != nil {
			return r, x
		}
	case *syntax.CallExpr:
		k := w.info.Calls[x]
		if k == nil || k.Kind != CallOp || k.Op == nil || k.Op.Name != "Text" || k.Recv == nil || !isMapped(w.info.Types[k.Recv]) {
			return nil, nil
		}
		if id := textRootIdent(k.Recv); id != nil {
			if v, _ := w.info.Uses[id].(*Var); v != nil {
				if _, ok := w.slot[v]; ok {
					return v, id
				}
			}
		}
	}
	return nil, nil
}

// textTemporary is the m.Text() call in the text x whose Mapped m is no
// place (a call's result, a literal): a temporary its statement releases,
// or nil.
func textTemporary(info *Info, x syntax.Expr) syntax.Expr {
	switch x := unparen(x).(type) {
	case *syntax.SliceExpr:
		return textTemporary(info, x.X)
	case *syntax.CallExpr:
		k := info.Calls[x]
		if k != nil && k.Kind == CallOp && k.Op != nil && k.Op.Name == "Text" && k.Recv != nil && isMapped(info.Types[k.Recv]) && textRootIdent(k.Recv) == nil {
			return x
		}
	}
	return nil
}

// letText records `let t = e` for a text e of a local owner: t is a text
// of that owner (rule 3).
func (w *ownWalk) letText(spec *syntax.VarSpec) {
	if len(spec.Names) != 1 || len(spec.Values) != 1 || spec.Pattern != nil {
		return
	}
	v, _ := w.info.Defs[spec.Names[0]].(*Var)
	if v == nil || !IsMappedText(v.Type) {
		return
	}
	root, _ := w.textRootOf(spec.Values[0])
	if root == nil {
		if at := textTemporary(w.info, spec.Values[0]); at != nil {
			w.report(at, textTempText)
		}
		return
	}
	if w.textRoots == nil {
		w.textRoots = map[*Var]*Var{}
	}
	w.textRoots[v] = root
}

// textUse checks a use x of the text v, a text of root: a use of root's
// value (rule 3). It reports whether v is a text of a local owner.
func (w *ownWalk) textUse(x *syntax.Ident, v *Var, h hand) bool {
	root := w.textRoots[v]
	if root == nil {
		return false
	}
	i, ok := w.slot[root]
	if !ok || !w.st.reached {
		return true
	}
	s := w.st.slots[i]
	switch s.st {
	case ownMoved:
		w.report(x, textAfterText, x.Name, root.Name, w.pos(s.at), s.how, x.Name, root.Name, x.Name, root.Name)
		return true
	case ownMaybe:
		w.report(x, textMaybeText, x.Name, root.Name, w.pos(s.at), s.how, x.Name, root.Name, root.Name)
		return true
	}
	if s.live() && s.fields|s.maybe != 0 {
		w.report(x, textTakenText, x.Name, root.Name, w.pos(s.fieldAt), x.Name)
		return true
	}
	if h.lend != nil && s.st == ownOwned {
		h.lend(i, x)
	}
	return true
}

// textCapture records the capture of id, a text of root, by the closure
// key: the closure holds a lend of root (resources R31), so root is not
// taken while the closure may still run, as for a capture of root itself
// (literalCaptures). An escaping frame never captures a text (escaping.go),
// so the closure is one the frame keeps in place.
func (w *ownWalk) textCapture(key syntax.Node, id *syntax.Ident, root *Var, dest lendPoint, seen map[*Var]bool) {
	ri, ok := w.slot[root]
	if !ok || seen[root] {
		return
	}
	seen[root] = true
	w.textUse(id, w.info.Uses[id].(*Var), hand{})
	for w.vars[ri].root != nil {
		next, ok := w.slot[w.vars[ri].root]
		if !ok {
			break
		}
		root, ri = w.vars[ri].root, next
	}
	end := w.point(root, ri)
	if w.releasesBefore(dest, end) {
		w.addLoans(key, []resourceLoan{{root: root, end: end, keeper: append([]int(nil), dest.path...)}})
	}
}

// textLoan is the lend of the owner of a text argument a to the call being
// evaluated: a later operand of the call that hands that owner off is
// R17's lend and hand-off in one call, since the callee would read the
// text after the hand-off. A text named by a local is lent by its use
// (textUse); one made in place (m.Text(), a slice of it) is lent here.
func (w *ownWalk) textLoan(a syntax.Expr, lend func(int, *syntax.Ident)) {
	if lend == nil || !w.st.reached || !IsMappedText(w.info.Types[a]) {
		return
	}
	if _, named := unparen(a).(*syntax.Ident); named {
		return
	}
	root, id := w.textRootOf(a)
	if root == nil {
		return
	}
	i, ok := w.slot[root]
	if !ok || w.st.slots[i].st != ownOwned {
		return
	}
	lend(i, id)
}
