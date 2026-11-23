package lower

// IR builder: what lies outside the function's own package (IRG4). See
// ir.go.
//
//   - A function of another Ergo package of the program is a function of
//     the one Go package the program lowers to: a "call" by its Go name
//     (ir_call.go).
//   - A type of an imported Go package (time.Duration, the runtime's
//     ergo.Malformed) is spelled by the package's import path, which the
//     decoder resolves among main.go's imports (spelling).
//   - A record type that holds itself is admitted once (admitOnce), at any
//     depth.

import (
	"fmt"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// admitOnce reports whether the record or union named by tn was admitted
// already in this body, and marks it admitted: a type that holds itself
// (through a slice, a union) is admitted once.
func (b *irBuilder) admitOnce(tn *check.TypeName) bool {
	if b.admitted[tn] {
		return true
	}
	if b.admitted == nil {
		b.admitted = map[*check.TypeName]bool{}
	}
	b.admitted[tn] = true
	return false
}

// spelling is the IR spelling of the Go type s, the lowered spelling of a
// type the subset admits: names, []T, *T (FLIP F2), pkg.Name and a
// generic type's instance Name[T1, T2], where pkg is the name main.go
// imports a package by. The IR names that package by its import path (the
// decoder resolves the path among main.go's imports), and the lowering
// keeps the import used (keepImport): an IR body is not Go text, and Go
// refuses an unused import.
func (b *irBuilder) spelling(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		if k := funcSpelling(s[i:]); k > 0 { // ir_machine.go
			out.WriteString(s[i : i+k])
			i += k
			continue
		}
		if strings.HasPrefix(s[i:], "struct{") {
			k := b.structSpelling(s, i, &out)
			i += k
			continue
		}
		j := i
		for j < len(s) && isSpellingName(s[j]) {
			j++
		}
		if j == i {
			switch {
			case strings.HasPrefix(s[i:], "[]"):
				out.WriteString("[]")
				i += 2
			case arrayPrefix(s[i:]) != "": // ir_index.go
				out.WriteString(arrayPrefix(s[i:]))
				i += len(arrayPrefix(s[i:]))
			case s[i] == '*':
				// A runtime type is a pointer (*ergo.Ref[T]).
				out.WriteByte('*')
				i++
			case strings.HasPrefix(s[i:], ", "):
				out.WriteString(", ")
				i += 2
			case s[i] == '[' && i > 0 && isSpellingName(s[i-1]), s[i] == ']':
				out.WriteByte(s[i])
				i++
			default:
				irNo("Go type spelling %s", s)
			}
			continue
		}
		q := b.qualifiedName(s, s[i:j])
		switch {
		case strings.HasPrefix(s[i:j], b.l.rt()+".") && j < len(s) && s[j] == '[':
			// A generic runtime type: its arguments may be a generic
			// body's type parameters (actor.WaitFor's Actor[S, E, D]),
			// which no package-level declaration names; the runtime's
			// Site is not generic (as stdCall keeps it).
			b.keepImport("var _ " + b.l.rt() + ".Site")
		case strings.Contains(s[i:j], "."):
			// The whole Go type keeps the import used: a generic type
			// (ergo.Ref) is no type without its arguments. A path that
			// is its own name (time) needs it too. A runtime instance
			// keeps it by Site: its arguments may name a type parameter
			// of a generic body (F1 × F2: arena's *Of[T].Get), which is
			// no type at main.go's top level.
			if strings.Contains(s, "[") && strings.HasPrefix(s[i:j], b.l.rt()+".") {
				b.keepImport("var _ " + b.l.rt() + ".Site")
			} else {
				// A host instance over a generic body's type parameter
				// (config's *Redacted[T]) is kept at int, as valueSource
				// keeps a generic body's element (ir_loops.go).
				b.keepImport("var _ " + b.typeParamsAt(s, "int"))
			}
		}
		out.WriteString(q)
		i = j
	}
	return out.String()
}

// namesTypeParam reports whether the Go type spelling s names a type
// parameter of the function the builder lowers.
func (b *irBuilder) namesTypeParam(s string) bool {
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && isSpellingName(s[j]) {
			j++
		}
		if j == i {
			i++
			continue
		}
		for tp, name := range b.fc.params {
			if s[i:j] == name && b.rwBound[tp] == nil { // a law's type parameter at a site is a Go type (ir_fusion.go)
				return true
			}
		}
		i = j
	}
	return false
}

// typeParamsAt is the Go type spelling s with each type parameter of the
// function the builder lowers spelled as at: a type main.go's top level
// can name.
func (b *irBuilder) typeParamsAt(s, at string) string {
	out := strings.Builder{}
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && isSpellingName(s[j]) {
			j++
		}
		if j == i {
			out.WriteByte(s[i])
			i++
			continue
		}
		name := s[i:j]
		for tp, p := range b.fc.params {
			if name == p && b.rwBound[tp] == nil {
				name = at
				break
			}
		}
		out.WriteString(name)
		i = j
	}
	return out.String()
}

func isSpellingName(c byte) bool {
	return c == '_' || c == '.' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// qualifiedName is the IR spelling of one type name of the Go type s: a
// name of main.go as it is, pkg.Name by pkg's import path.
func (b *irBuilder) qualifiedName(s, n string) string {
	q, name, ok := strings.Cut(n, ".")
	if !ok {
		return n
	}
	if strings.Contains(name, ".") || q == "" || name == "" {
		irNo("Go type spelling %s", s)
	}
	path := b.importPath(q)
	if path == "" {
		irNo("Go type spelling %s", s)
	}
	return path + "." + name
}

// admitArgs admits the type arguments of t, an instance of a generic type
// of the program (Option[int], Pair[string, Row]): the type-kinded ones,
// which its Go type takes (typeArgs; a row argument has no Go type). The
// instance's fields and variants are its declaration's with the arguments
// put in, so they are admitted as any record's or union's.
func (b *irBuilder) admitArgs(t *check.Named, depth int) {
	for _, a := range typeArgs(t) {
		b.admit(a, depth+1)
	}
}

// typeArgs is the Go type argument list of the generic call c, comma and
// space between them, as the emitter's typeArgs writes it in brackets: a
// type argument of each type-kinded parameter (a row or an operations
// parameter has no Go type parameter). "" for a call of a function that
// is not generic.
func (b *irBuilder) typeArgs(c *check.Call, site syntax.Node) string {
	return b.sigTypeArgs(c.Func.Sig, c.TypeArgs, site)
}

// sigTypeArgs is typeArgs for the type arguments targs of a function of
// signature sig: a call's, or an instance's named as a value (funcName).
func (b *irBuilder) sigTypeArgs(sig *check.Signature, targs []check.Type, site syntax.Node) string {
	var out []string
	for i, tp := range sig.TParams {
		if !b.l.goParam(tp) {
			continue
		}
		if i >= len(targs) {
			irNo("generic call without its type arguments")
		}
		out = append(out, b.goArg(tp, targs[i], site))
	}
	return strings.Join(out, ", ")
}

// callerSlot is the Slot row parameter that the row argument a holds
// alone (written as a row value or as a row argument), or nil.
func callerSlot(a check.Type) *check.TypeParam {
	r := check.SlotRow(a)
	if ra, ok := a.(*check.RowArg); ok && r == nil {
		if es := rowElems(ra.Row); len(es) == 1 {
			r, _ = es[0].(*check.TypeParam)
		}
	}
	if r == nil || !r.Slot {
		return nil
	}
	return r
}

// goArg is the Go type argument a of the Go type parameter tp, as the
// emitter's goArg writes it (rowslot.go): a type-kinded parameter's Go
// type; a Slot row's (`F error`) single named error type, or error for a
// row of none or several; a row that is one row parameter of the caller,
// that parameter's Go type parameter, or error when it has none. A Slot
// row and a held row keep the source body.
func (b *irBuilder) goArg(tp *check.TypeParam, a check.Type, site syntax.Node) string {
	if tp.Kind == check.KindType {
		return b.goType(a, site)
	}
	if r := callerSlot(a); r != nil && tp.Kind != check.KindUses && irInlineSlotLowering != "off" {
		// The caller's own Slot row passed on is its Go type parameter,
		// as the emitter's slotType names it (rowslot.go, INLINE-SLOT).
		if name, ok := b.fc.params[r]; ok {
			return b.spelling(name)
		}
	}
	if tp.Kind == check.KindUses || check.SlotRow(a) != nil {
		irNo("generic call with a row type argument of the caller")
	}
	ra, ok := a.(*check.RowArg)
	if !ok {
		irNo("generic call with a row type argument of the caller")
	}
	es := rowElems(ra.Row)
	if len(es) == 1 {
		switch e := es[0].(type) {
		case *check.Named:
			return b.goType(e, site)
		case *check.TypeParam:
			// The caller's row parameter: its Go type parameter, or,
			// when it has none (an erased row), error, as the
			// emitter's goArg falls through to it.
			if name, ok := b.fc.params[e]; ok {
				return b.spelling(name)
			}
			return "error"
		}
		irNo("generic call with a row type argument of the caller")
	}
	return "error"
}

// valueSource applies the value rule to the operands of the call x, args
// and their IR irArgs, as the emitter's valueSource does (values.go, V3):
// Append's source, unless the call may write it in place, is clipped
// (slices.Clip), so the append copies rather than write past its length
// into an array another value shares.
func (b *irBuilder) valueSource(x *syntax.CallExpr, c *check.Call, args []syntax.Expr, irArgs []*irExpr) {
	if !valueRule() || len(irArgs) == 0 || b.l.valueFacts().inPlace[x] || valueMutant == "in-place" {
		return
	}
	switch {
	case appendsSource(c.Func):
		// The element type: the call's type argument, or the operand's
		// for a []byte operation on its receiver (bs.AppendString(s)).
		var et check.Type
		switch st, isSlice := b.l.info.Types[args[0]].(*check.Slice); {
		case len(c.TypeArgs) == 1:
			et = c.TypeArgs[0]
		case isSlice:
			et = st.Elem
		default:
			irNo("append form")
		}
		elem := b.goType(et, x)
		if b.goType(b.l.info.Types[args[0]], x) != "[]"+elem {
			// Go assigns a defined slice type to []T; the decoder takes
			// identical types only.
			irNo("append to a defined slice type")
		}
		if keep := b.fc.goType(et); b.namesTypeParam(keep) {
			// A generic body's element type is no package-level type
			// (I-FLIP: examples/counter-gui).
			b.keepImport("var _ = " + b.l.goPkg("slices") + ".Clip[[]int]")
		} else {
			b.keepImport("var _ = " + b.l.goPkg("slices") + ".Clip[[]" + keep + "]")
		}
		src := irArgs[0]
		irArgs[0] = &irExpr{K: "pcall", Pos: src.Pos, T: "slices", Name: "Clip", V: "[]" + elem + ", " + elem, Args: []*irExpr{src}}
	case writesMapSource(c.Func) || takesMapSource(c.Func):
		if len(c.TypeArgs) != 2 {
			irNo("Map write form")
		}
		if _, ok := b.intMapOf(b.l.info.Types[args[0]]); ok { // ir_intmap.go: the table's own copy
			irArgs[0] = tableCall(irArgs[0].Pos, "Clone", irArgs[0])
			return
		}
		k, v := b.goType(c.TypeArgs[0], x), b.goType(c.TypeArgs[1], x)
		m := "map[" + k + "]" + v
		if b.goType(b.l.info.Types[args[0]], x) != m {
			irNo("Map write to a defined Map type")
		}
		if keep := "map[" + b.fc.goType(c.TypeArgs[0]) + "]" + b.fc.goType(c.TypeArgs[1]); b.namesTypeParam(keep) {
			// A generic body's Map type is no package-level type, as
			// Clip's above (I-FLIP: examples/domain-search search/cache) and
			// Append's element (I-FLIP reds #15: pipeline's Book[E]
			// Streams.Put).
			b.keepImport("var _ = " + b.l.goPkg("maps") + ".Clone[map[string]bool]")
		} else {
			b.keepImport("var _ = " + b.l.goPkg("maps") + ".Clone[" + keep + "]")
		}
		src := irArgs[0]
		irArgs[0] = &irExpr{K: "pcall", Pos: src.Pos, T: "maps", Name: "Clone", V: m + ", " + k + ", " + v, Args: []*irExpr{src}}
	case len(irArgs) == 2:
		// Go's copy of elements that hold a slice or Map (values.go,
		// copyElemSource): the source is copied, as the emitter's is.
		if t := copyElemSource(b.l, x, c, b.fc.typeOf); t != nil {
			irArgs[1] = b.copyDeep(t, x, irArgs[1], !entryRule())
		}
	}
}

// builtinAppend reports the slice operation Append (check.CollOpOf),
// which the IR writes as Go's builtin append (FLIP F3, PRIM), not as a
// call of its runtime/v1 body (the Go reference). Go sees the builtin at
// once, and with it a place appended back to itself. The value rule has
// already clipped the source of a call that may not write it in place
// (valueSource), and the builtin takes that clipped source, so an append
// to a place another value shares still copies.
func (b *irBuilder) builtinAppend(f *check.Func) bool {
	return check.CollOpOf(f) == check.CollAppend
}

// admitMap admits a Map, Go's map[K]V: its key and its value. A Map is a
// value (V3): a call that writes one writes a copy unless it may write in
// place (valueSource), and a copy of a local is the value rule's ("value
// copy", which keeps the source body).
func (b *irBuilder) admitMap(t *check.Map, depth int) {
	b.admit(t.Key, depth+1)
	b.admit(t.Elem, depth+1)
}

// goRange reports a for-in domain the emitter lowers to Go's range over
// the value (rangeStmt): a slice, a Map, a string's runes.
func goRange(d check.LoopDomain) bool {
	return d == check.LoopSlice || d == check.LoopMap || d == check.LoopString
}

// mapLit is the elements of the Map literal x, each entry's key then its
// value, in source order: Go's map literal evaluates them in that order,
// as the emitter's literal does.
func (b *irBuilder) mapLit(x *syntax.CompositeLit) []*irExpr {
	var list []syntax.Expr
	for _, e := range x.Elts {
		kv, ok := e.(*syntax.KeyValueExpr)
		if !ok {
			irNo("Map literal element without a key")
		}
		list = append(list, kv.Key, kv.Value)
	}
	return b.inOrder(list) // ir_order.go: as the emitter's elements
}

// admitOps admits a value of a program's operation type: the Go interface
// the lowering declares for it (decls.go, kindOps), whose methods take the
// running task first; an operation type whose declaration binds a
// runtime/v1 Go type (`extern go …/runtime/v1.Ref type Ref[T]`: std's
// sync.Ref, or a trusted module's copy) is that type, a pointer to a
// runtime/v1 struct (runtimeType), whose operations are its methods
// (boundOpCall).
func (b *irBuilder) admitOps(t *check.Named) {
	b.admitArgs(t, 0)
}

// opCall is the call x of the operation c.Op on a value of a program's
// operation type, c.Recv: the Go interface's method, the running task
// first, as the emitter's programOpCall writes it. The emitter calls a
// local whose provider it knows by the provider's function instead
// (cellOpCall, a speed path); the IR calls through the interface, which
// gives the same result.
func (b *irBuilder) opCall(x *syntax.CallExpr, c *check.Call, p irPos) ([]*irStmt, *irExpr, bool) {
	switch {
	case c.Recv == nil || c.Op == nil:
		irNo("operation call form")
	case hostRecv(b.l.info.Types[c.Recv]):
		// An operation of a Go interface (fmt.Stringer's String on a
		// parameter of that type): Go's method on its receiver, as the
		// emitter's goCall makes it (goImportCall, gocall.go goSig), never
		// an Ergo operation with the task first (LIB-ON-IR: TestGoCallsErgo's
		// Probe and Wait).
		f, gc := goInterfaceMethod(b.l.info.Types[c.Recv], c)
		return b.hostCall(x, gc, f, p) // ir_tuple.go
	case b.env < 0:
		irNo("operation call from a pure function")
	case b.inPlaceSite(x):
		return b.ownedSiteCall(x, c, b.fc.refSiteOf(x), p) // ir_ownlit.go
	}
	if tp, ptr := paramSource(b.l.info.Types[c.Recv]); tp != nil {
		return b.witnessOpCall(x, c, tp, ptr, p) // ir_witness.go
	}
	n, ok := b.l.info.Types[c.Recv].(*check.Named)
	if !ok {
		irNo("operation call on a %T", b.l.info.Types[c.Recv])
	}
	if b.l.typeDecl(n.Obj, x).kind != kindOps {
		irNo("operation call on a named type that is no operation type")
	}
	b.admitOps(n)
	if s := b.fc.refSiteOf(x); s != nil {
		return b.ownedRefCall(x, c, s, append([]syntax.Expr{c.Recv}, x.Args...), n, p) // ir_refown.go
	}
	if b.l.runtimeType(n, x) != "" {
		return b.boundOpCall(x, c, n, p)
	}
	pre, vals := b.operands(append([]syntax.Expr{c.Recv}, x.Args...))
	if id, ok := unparen(c.Recv).(*syntax.Ident); ok && vals[0].K == "toiface" {
		if v, ok := b.l.info.Uses[id].(*check.Var); ok && b.fc.provs[v] != nil {
			// The parameter of a copy for a known provider: the
			// adapter's method, a static call (ir_known.go).
			vals[0] = vals[0].X
		}
	}
	env := &irExpr{K: "local", Pos: p, I: b.env}
	args := append([]*irExpr{{K: "field", Pos: p, Name: envTask, X: env}}, vals[1:]...)
	return pre, b.opResult(c, &irExpr{K: "mcall", Pos: p, Name: memberName(c.Op.Name), X: vals[0], Args: args}), true // ir_call.go
}

// goInterfaceMethod is the Go method of the operation call c on a value of
// recv, a Go interface type of import go (or a go.Ref or borrow of one),
// as a Go package function hostCall calls: the binding's method, its
// signature the operation's (no receiver parameter), its target
// PATH.TYPE.METHOD, a value receiver; and c as a call of it.
func goInterfaceMethod(recv check.Type, c *check.Call) (*check.Func, *check.Call) {
	n := hostNamed(recv)
	target := n.Obj.Pkg.Path + "." + n.Obj.Name + "." + c.Op.Name
	decl := &syntax.FuncDecl{
		Extern: &syntax.ExternSpec{Target: &syntax.BasicLit{Value: target}},
		Recv:   &syntax.FieldList{List: []*syntax.Field{{Type: &syntax.Ident{Name: n.Obj.Name}}}},
		Name:   &syntax.Ident{Name: c.Op.Name},
	}
	f := &check.Func{Name: c.Op.Name, Pkg: n.Obj.Pkg, Decl: decl, Sig: c.Op.Sig}
	gc := *c
	gc.Kind, gc.Func = check.CallFunc, f
	return f, &gc
}

// countLoop is the for-in s over a prelude Range[T] of an integer T, as
// the emitter's rangeStmt writes it: the Range evaluated once into a
// temporary r, then
//
//	for v := r.Start; v < r.End; v++ { body }
//
// v the loop's binding (or a temporary), Go's per-iteration variable. A
// continue runs the increment, as the emitter's for clause does.
func (b *irBuilder) countLoop(s *syntax.RangeStmt, lp *check.Loop) []*irStmt {
	n, ok := b.l.info.Types[s.X].(*check.Named)
	if !ok {
		irNo("for-in over a Range of no named type")
	}
	if bound(s.Key) {
		irNo("for-in over a Range with a position")
	}
	if bt := b.scalarBasic(lp.Elem); bt == nil || !isInteger(bt) {
		irNo("for-in over a Range of a non-integer")
	}
	p := b.pos(s, s.Pos())
	td := b.l.typeDecl(n.Obj, s.X)
	var pre []*irStmt
	var x *irExpr
	if bx, ok := unparen(s.X).(*syntax.BinaryExpr); ok && bx.Op.String() == ".." && len(td.fields) == 2 {
		// a..b is Range{Start: a, End: b}, its operands in order (the
		// emitter's rangeValue); the operator elsewhere is G1's.
		var parts []*irExpr
		pre, parts = b.operands([]syntax.Expr{bx.X, bx.Y})
		x = &irExpr{K: "lit", Pos: p, T: b.goType(n, s.X), Keys: []string{td.fieldName["Start"], td.fieldName["End"]}, Elems: parts}
	} else {
		pre, x = b.value(s.X)
	}
	r := b.temp("r", b.goType(n, s.X), p)
	var v int
	if bound(s.Value) {
		id, ok := s.Value.(*syntax.Ident)
		if !ok {
			irNo("for-in pattern")
		}
		bv, ok := b.l.info.Defs[id].(*check.Var)
		if !ok {
			irNo("for-in binding without a variable")
		}
		v = b.local(bv, id)
	} else {
		v = b.temp("i", b.goType(lp.Elem, s), p)
	}
	field := func(name string) *irExpr {
		return &irExpr{K: "field", Pos: p, Name: td.fieldName[name], X: &irExpr{K: "local", Pos: p, I: r}}
	}
	loop := &irStmt{K: "count", Pos: p, Local: v, X: field("Start"), Y: field("End"), End: b.pos(s.Body, s.Body.Rbrace)}
	filled := b.fillLoop(s, lp, field("Start"), v, p) // ir_fill.go
	ctr := b.loopBody(loop, s, s.Body)                // ir_stmt.go
	filled()
	// The Range's statements run first, once (rangeStmt).
	loop.Pos = past(loop.Pos, pre) // ir_match.go
	// The nest's counter follows the Range's definition (ir_poll.go).
	head := append(append(pre, &irStmt{K: "def", Pos: p, Local: r, X: x}), ctr...)
	if st := b.loopIdiomIR(s, lp, loop, field); st != nil {
		// A fill, a move or a collect: the call, guarded, before the loop
		// (ir_loops.go), which stays in the guard's else and counts there.
		return append(head, st...)
	}
	return append(head, loop)
}

// conversion is the conversion x, T(v), as the emitter writes it: a Go
// conversion between scalar types (the decoder's "conv" takes numeric,
// string and bool kinds), or for a float to an integer the runtime's
// checked helper (floatconv.go), which a float32 reaches through float64.
// An integer to a string is a rune conversion in Go, which "conv" does not
// take: it keeps the source body.
func (b *irBuilder) conversion(x *syntax.CallExpr, c *check.Call, p irPos) ([]*irStmt, *irExpr, bool) {
	if len(x.Args) != 1 {
		irNo("conversion form")
	}
	if b.funcConversion(x, c) { // ir_defined.go
		pre, v := b.value(x.Args[0])
		return pre, &irExpr{K: "conv", Pos: p, T: b.goType(c.Type, x), X: v}, false
	}
	from, to := b.scalarBasic(b.l.info.Types[x.Args[0]]), b.scalarBasic(c.Type)
	if from == nil || to == nil {
		irNo("conversion of a non-scalar")
	}
	if to.Kind == check.String && isInteger(from) {
		irNo("conversion of an integer to a string")
	}
	pre, v := b.value(x.Args[0])
	if helper := floatToInteger(from, to); helper != "" {
		if from.Kind == check.Float32 {
			v = &irExpr{K: "conv", Pos: p, T: "float64", X: v}
		}
		b.keepImport("var _ = " + b.l.rt() + "." + helper)
		if b.goType(c.Type, x) != b.fc.goType(to) {
			irNo("conversion of a float to a named integer")
		}
		return pre, &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: helper, Args: []*irExpr{v}}, false
	}
	if cv, ok := b.l.info.Values[x.Args[0]]; ok {
		// A constant converted is a constant of the target type, as Go's
		// checker folds int64(0) (the text view compiles the folded form).
		return pre, b.constant(c.Type, cv, x, p), false
	}
	return pre, &irExpr{K: "conv", Pos: p, T: b.goType(c.Type, x), X: v}, false
}

// passesPointer reports whether the call x of a *T function passes its
// receiver as a pointer value. A dot call on an addressable T takes the
// receiver's address where it is evaluated (the emitter's callArgsAddr,
// a place), which the IR has no form for.
func (b *irBuilder) passesPointer(c *check.Call, x *syntax.CallExpr) bool {
	recv := c.Recv
	if recv == nil {
		if len(x.Args) == 0 {
			return false
		}
		recv = x.Args[0]
	}
	_, ptr := b.fc.boundType(b.l.info.Types[recv]).(*check.Pointer)
	return ptr
}

// derefNamed is the named type of a selector's operand: t itself, or the
// record a *T points to, whose field Go's selector reads through the
// pointer (the emitter's fieldName).
func derefNamed(t check.Type) (*check.Named, bool) {
	if p, ok := t.(*check.Pointer); ok {
		t = p.Elem
	}
	// A go.Ref[T] is Go's *T (gocall.go): its fields are T's. The Ref
	// itself is never a declaration, so the naming pass gives it no name
	// (std pace's stats.HeapAlloc stopped the census on that, NAMING-ICE).
	if e, ok := check.GoRefElem(t); ok {
		t = e
	}
	n, ok := t.(*check.Named)
	return n, ok
}

// own is the type whose Go type fu's helpers spell: the instance, for a
// generic union's copy.
func (fu *flatUnion) own() check.Type {
	if fu.inst != nil {
		return fu.inst
	}
	return fu.tn.Type
}

// stdCall is the call x of the std extern fd: a call of its runtime/v1
// body by the runtime's import path ("pcall"), as the emitter's stdCall
// writes it when the body takes no hidden argument, no function and no
// type argument, and returns its Go form plainly. Every other shape keeps
// the source body.
//
// The builder declares nothing for the call: the lowering's funcDecl would
// declare a std record the function takes first as a Go type of main.go
// (methodReceiver), where the source path, which may inline the call,
// declares none; a std extern's call needs only its target.
func (b *irBuilder) stdCall(x *syntax.CallExpr, c *check.Call, f *check.Func, p irPos) ([]*irStmt, *irExpr, bool) {
	fd := b.l.funcs[f]
	if fd == nil {
		if f.Decl.Extern.Target == nil {
			irNo("call of a std intrinsic")
		}
		fd = &funcDecl{fn: f, library: true, extern: strings.Trim(f.Decl.Extern.Target.Value, "\"`")}
	}
	body, name, ok := b.l.stdBody(fd)
	// An operation of the slice or Map type called on its receiver
	// (bs.Text(); PRIM-4): the receiver is the body's first operand, as
	// the emitter's callText passes it (PORT-CALLS).
	ops := x.Args
	if c.Recv != nil {
		ops = append([]syntax.Expr{c.Recv}, x.Args...)
	}
	switch {
	case !ok:
		irNo("call of a std intrinsic")
	case c.Recv != nil && check.CollOpOf(f) == check.CollNone:
		irNo("dot call of a std function")
	case b.fc.refSiteOf(x) != nil:
		if b.inPlaceSite(x) {
			return b.ownedSiteCall(x, c, b.fc.refSiteOf(x), p) // ir_ownlit.go
		}
		return b.ownedRefCall(x, c, b.fc.refSiteOf(x), ops, nil, p) // ir_refown.go
	case commaOk(body):
		irNo("std call with an adapted result")
	case len(body.Params) != len(ops)+trailing(x):
		irNo("std call with another arity")
	case x.Trailing != nil && !adapted(body.Params[len(ops)]):
		irNo("std call with another arity")
	case len(b.l.ownedParams(fd.fn)) > 0:
		irNo("call that passes owned values")
	case c.Site != nil:
		irNo("call with a call site")
	case len(b.fc.captureOwners(x)) > 0:
		irNo("call that captures owners")
	}
	if _, bound := runtimeBindings()[name]; !bound {
		irNo("std call with no runtime binding")
	}
	tuple, isTuple := b.l.info.Types[x].(*check.Tuple)
	if isTuple {
		// A body with Go's several results, one per element, is bound by
		// a tdef (ir_tuple.go, F4b: bits.Add64); any other is adapted.
		// One with an error after them, (A, B, error), is bound by bind
		// alone, which takes the error too (tupleErrBind).
		switch stdTupleAdmits(body, tuple, b.tupleBind == x) {
		case tupleOutsideBind:
			irNo("fallible call with an empty row at its site")
		case tupleAdapted:
			irNo("std call with an adapted result")
		}
	}
	// A generic body's instance (F1): each Go type parameter takes the
	// call's type argument for the Ergo type parameter of its name, as the
	// emitter's stdTypeArgs writes it; the body's Go types read with them.
	targs, subst := b.stdTypeArgs(body, c, x)
	if isTuple {
		// Each result is the element's Go type, read as a single result
		// is (below): spelled alike, or the runtime's at the instance
		// (TakeAt's (T, []T), actor's shown (S, int), keyState's []byte
		// as []uint8; CONV-RESULT).
		for i, r := range body.Results[:len(tuple.Types)] {
			if !b.sameGo(tuple.Types[i], r) && !b.sameRT(tuple.Types[i], r, subst, body.Package) {
				irNo("std call with a converted result")
			}
		}
	}
	// The decoder takes an argument and a result of the runtime's Go type
	// itself (types2.Identical), where Go text would also assign a
	// defined type to its underlying one: each must be spelled alike.
	convs := map[int]string{}
	for i, q := range body.Params {
		if adapted(q) {
			continue // adaptOperands, ir_adapt.go
		}
		conv, ok := b.operandConv(ops[i], q.Type, subst, body.Package) // ir_operand.go
		if !ok {
			irNo("std call with a converted operand")
		}
		if conv != "" {
			convs[i] = conv
		}
	}
	if failsHere(c) && !goFallible(body) || goFallible(body) && !failsHere(c) && isTuple {
		irNo("fallible call with an empty row at its site")
	}
	if !isTuple && !defersCall(body) && goValue(body) && !b.sameGo(b.l.info.Types[x], body.Results[0]) && !b.sameRT(b.l.info.Types[x], body.Results[0], subst, body.Package) {
		irNo("std call with a converted result")
	}
	hidden := b.hidden(body, x, c, p)
	pre, args := b.operands(ops)
	convOperands(args, convs) // ir_operand.go
	// The value rule (V3), as the emitter's std call (std.go): an
	// appender's operand is clipped and a Map write's copied, unless the
	// call writes it in place (bs = bs.AppendString(s)).
	b.valueSource(x, c, ops, args)
	mark := b.next // the block's locals and its adapter's are numbered from here (spawnWith)
	if x.Trailing != nil {
		args = append(args, b.closureLit(nil, x)) // the block, the last operand
	}
	pre = append(pre, b.adaptOperands(body.Params, x, ops, args, 0, p)...) // ir_adapt.go
	// A generic function stands in Go text only as an instance, whose type
	// arguments may be the body's own type parameters: the import is
	// kept by a variable of the runtime's Site type, which is not generic.
	// A body of a runtime leaf (runtime/v1/rpc, BS8) is called by the
	// leaf's import path, which rtRef imports.
	name = b.consumedForm(x, body, name)    // ir_own.go
	name, isExit := b.exitsForm(body, name) // ir_exits.go: OnExit on a static exit form
	moved, repeated := b.movedCaptures(x)   // ir_taskmove.go
	handles := b.captureHandles(moved)
	if repeated {
		// A Set's repeated body lends what it moves: the member's root owns
		// it, TasksGoOwned's prepare (the emitter's movingRootArg).
		if name != "TasksGo" || len(args) != 2 || x.Trailing == nil {
			irNo("function literal that a task captures")
		}
		name = "TasksGoOwned"
		args = []*irExpr{args[0], b.captureRoot(x.Trailing, moved, p), args[1]}
	}
	pkg := v1.PackageOf(name)
	switch {
	case pkg != "" && pkg != runtimeImport && targs != "":
		// A leaf's generic function is kept as an instance (I-FLIP:
		// examples/pipeline's ergorpc.RPCDie).
		if b.namesTypeParam(targs) {
			irNo("runtime leaf's generic function in a generic body")
		}
		b.keepImport("var _ = " + b.l.rtRef(name) + "[" + targs + "]")
	case pkg != "" && pkg != runtimeImport:
		b.keepImport("var _ = " + b.l.rtRef(name))
	case targs != "":
		b.keepImport("var _ " + b.l.rt() + ".Site")
		_ = b.l.rtRef(name) // the leaf it needs linked (NewHost: hostwalk; LY-FIX)
	default:
		b.keepImport("var _ = " + b.l.rtRef(name))
	}
	if pkg == "" {
		pkg = runtimeImport
	}
	if defersCall(body) {
		if isExit || len(args) != len(ops) {
			irNo("std call with an adapted result")
		}
		seqPre, seq, impure := b.stdSeqCall(x, c, body, subst, pkg, name, targs, ops, args, convs, p) // ir_adapted.go: Go's iter.Seq[T], a Seq
		return append(pre, seqPre...), seq, impure
	}
	call := &irExpr{K: "pcall", Pos: p, T: pkg, Name: name, V: targs, Args: append(hidden, args...)}
	if name == "Go" && pkg == runtimeImport && x.Trailing != nil && targs != "" {
		call = b.spawnWith(call, targs, mark, p) // ir_spawn.go: task.Go's block, its captures in the Task (GP-SPAWN2)
	}
	if isExit {
		b.noteExit(call, x) // ir_exits.go: the defer form, if the frame keeps it
	}
	if len(handles) > 0 {
		// The admission step hands the moved captures out of the sender's
		// list, or releases them on a failed one (ir_taskmove.go).
		if isTuple || isExit || goFallible(body) && !failsHere(c) {
			irNo("function literal that a task captures")
		}
		value := goValue(body)
		typ := ""
		if value {
			typ = b.goType(b.l.info.Types[x], x)
		}
		spre, v := b.settleCaptures(call, handles, goFallible(body), value, typ, p)
		return append(pre, spre...), v, !body.Pure
	}
	if isTuple && goFallible(body) {
		b.tupleBound = call // bind binds its results and its error (tupleErrBind)
		return pre, call, false
	}
	if isTuple {
		tpre, e := b.tdef(call, tuple, x, p) // ir_tuple.go
		return append(pre, tpre...), e, false
	}
	if goFallible(body) && !failsHere(c) {
		call = b.dropErr(call) // ir_call.go: the row is empty here
	}
	return pre, call, !body.Pure
}

// The IR std call's verdicts on a body whose Ergo result is a tuple
// (stdTupleAdmits).
const (
	tupleAdmitted    = iota
	tupleOutsideBind // (A, B, error) outside bind, which alone takes its error
	tupleAdapted     // results that are not the tuple's elements, one each
)

// stdTupleAdmits is the IR std call's verdict on body, whose Ergo result
// is tuple: Go's several results, one per element, bound by a tdef, and
// with an error after them only under bind (underBind), whose tdef takes
// the error too (tupleErrBind; EXTERN-MULTI-RESULT, task's TaskExit
// (int, string, error)). Any other shape is adapted, which the IR
// refuses.
func stdTupleAdmits(body v1.Body, tuple *check.Tuple, underBind bool) int {
	n := len(body.Results)
	if goFallible(body) {
		if !underBind {
			return tupleOutsideBind
		}
		n--
	}
	if len(tuple.Types) < 2 || n != len(tuple.Types) {
		return tupleAdapted
	}
	return tupleAdmitted
}

// stdTypeArgs is the IR type argument list of the call x of a generic std
// body ("" for a body that is not generic), and the Go spelling of each of
// the body's type parameters at this call.
func (b *irBuilder) stdTypeArgs(body v1.Body, c *check.Call, x *syntax.CallExpr) (string, map[string]string) {
	if len(body.TypeParams) == 0 {
		return "", nil
	}
	if c.Func == nil {
		irNo("generic or task call")
	}
	var out []string
	subst := map[string]string{}
	for _, gp := range body.TypeParams {
		j := -1
		for k, tp := range c.Func.Sig.TParams {
			if tp.Name == gp.Name {
				j = k
			}
		}
		if j < 0 || j >= len(c.TypeArgs) {
			irNo("generic or task call")
		}
		out = append(out, b.goType(c.TypeArgs[j], x))
		subst[gp.Name] = b.fc.goType(c.TypeArgs[j])
	}
	return strings.Join(out, ", "), subst
}

// sameRT reports whether the Ergo type t lowers to the Go type a
// runtime/v1 body spells goType at a call whose type parameters are
// subst: both read as main.go spells them (rtSpelling). pkg is the import
// path of the body's package (v1.Body.Package): a runtime leaf's body
// (runtime/v1/http's HTTPServe) spells its own types unqualified, which
// main.go reaches through the leaf's import (rtRef; PORT-CALLS).
func (b *irBuilder) sameRT(t check.Type, goType string, subst map[string]string, pkg string) bool {
	if t == nil {
		return false
	}
	q := b.l.rt()
	if pkg != "" && pkg != runtimeImport {
		q = b.l.pkgName(pkgKey{"import", pkg}, leafName(pkg)) // naming.go reserves every leaf
		// A leaf names runtime/v1's own types through its import of it,
		// v1.HostRoot (http's HTTPIntoFile): main.go's is rt (CONV-OPERAND).
		goType = leafRootSpelling(goType, b.l.rt())
	}
	return b.fc.goType(t) == rtSpelling(goType, q, subst)
}

// leafRootQualifier is the name every runtime leaf imports runtime/v1 by
// (`v1 "ergo.local/prototype/runtime/v1"`), which its bodies' Go types
// spell runtime/v1's types with (no leaf imports it by another name).
const leafRootQualifier = "v1"

// leafRootSpelling is a leaf body's Go type spelling s with each
// qualifier leafRootQualifier, a name before a dot that is not after one,
// spelled rt.
func leafRootSpelling(s, rt string) string {
	var w strings.Builder
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], leafRootQualifier+".") && (i == 0 || !isIdentByte(s[i-1]) && s[i-1] != '.') {
			w.WriteString(rt + ".")
			i += len(leafRootQualifier) + 1
			continue
		}
		w.WriteByte(s[i])
		i++
	}
	return w.String()
}

// isIdentByte reports a byte of a Go identifier.
func isIdentByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// rtSpelling is a runtime/v1 body's Go type spelling s as main.go spells
// it at a call: each identifier that names a type parameter in subst is
// its spelling there ([]T at T = int is []int), each other exported name
// is the runtime's, qualified by rt (*Ref[T] is *rt.Ref[int]), and byte
// and rune are Go's uint8 and int32. A name before or after a dot is
// another package's, as it stands.
func rtSpelling(s, rt string, subst map[string]string) string {
	var w strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			j := i
			for j < len(s) && (s[j] == '_' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= '0' && s[j] <= '9') {
				j++
			}
			id := s[i:j]
			r, param := subst[id]
			switch {
			case i > 0 && s[i-1] == '.' || j < len(s) && s[j] == '.':
			case param:
				id = r
			case id == "byte":
				id = "uint8"
			case id == "rune":
				id = "int32"
			case c >= 'A' && c <= 'Z':
				id = rt + "." + id
			}
			w.WriteString(id)
			i = j
			continue
		}
		w.WriteByte(c)
		i++
	}
	return w.String()
}

// hidden is the hidden arguments of the std body called at x, as the
// emitter's hiddenArgs writes them, before the call's own: the caller's
// task and Scheduler (fields of the environment), the reading package (a
// string constant) and the call site (a runtime Site of a string
// constant). A frame scope or a result release keeps the source body.
func (b *irBuilder) hidden(body v1.Body, x *syntax.CallExpr, c *check.Call, p irPos) []*irExpr {
	var out []*irExpr
	for _, h := range body.Hidden {
		if b.env < 0 {
			irNo("std call with hidden arguments in a pure function")
		}
		env := &irExpr{K: "local", Pos: p, I: b.env}
		switch h {
		case v1.CallerTask:
			out = append(out, &irExpr{K: "field", Pos: p, Name: envTask, X: env})
		case v1.CallerScheduler:
			// The body takes the runtime's Sleeper, which the program's
			// Scheduler interface implements: Go text assigns it, the
			// decoder takes the parameter's type only, so the IR asserts
			// it (PORT-CALLS-2: http.Serve's HTTPServe). The checker binds
			// the provider at the call, so the field is never nil.
			sched := &irExpr{K: "field", Pos: p, Name: b.l.envField(schedulerCap(c, x)), X: env}
			out = append(out, &irExpr{K: "assert", Pos: p, T: b.spelling(b.l.rt() + ".Sleeper"), X: sched})
		case v1.ReadingPackage:
			if c.Func == nil || c.Func.Pkg == nil {
				irNo("std call with hidden arguments: a context read without its package")
			}
			out = append(out, &irExpr{K: "const", Pos: p, T: "string", V: contextNamespace(c.Func.Pkg)})
		case v1.CallSite:
			src, line, column := b.l.locate(x, x.Pos())
			site := fmt.Sprintf("%s calls %s at %s:%d:%d", b.fc.owner, b.fc.callee(x, c), src, line, column)
			// rt.Site is a defined string type: Site("…") is a conversion
			// of the constant (spelling keeps the import).
			out = append(out, &irExpr{K: "conv", Pos: p, T: b.spelling(b.l.rt() + ".Site"), X: &irExpr{K: "const", Pos: p, T: "string", V: site}})
		case v1.Frame:
			if b.inSeq {
				irNo("std call with an adapted result") // a Seq outlives the frame (seqLit)
			}
			out = append(out, b.frameArg(p)) // ir_own.go (GP-FRAME)
		default:
			irNo("std call with hidden argument %s", h)
		}
	}
	return out
}

// boundOpCall is the call x of the operation c.Op on a value of an
// operation type n whose declaration binds a runtime/v1 Go type: the
// method of that Go type, its hidden arguments first, as the emitter's
// opCall writes it through runtimeCall, when the method needs no adapter.
// It reads the declaration and the body contract (runtimeBody, keyed by
// the type's and the operation's names as check.OpBodyOf is), never std
// membership: a trusted module's copy of std's type gets the same call
// (TestStdOpCallCopyAgreesOnIR, internal/sketch/std).
func (b *irBuilder) boundOpCall(x *syntax.CallExpr, c *check.Call, n *check.Named, p irPos) ([]*irStmt, *irExpr, bool) {
	name := check.RuntimeTypeName(n) + "." + c.Op.Name
	if _, bound := runtimeBindings()[name]; !bound {
		irNo("std call with no runtime binding")
	}
	body, err := runtimeBody(name) // bodycontract.go: the facts of the body the binding names
	if err != nil || len(body.Params) != len(x.Args) {
		irNo("std operation without its runtime method")
	}
	option := commaOk(body) && !goFallible(body) // Go's (T, bool): an Option (ir_adapted.go)
	switch {
	case commaOk(body) && !option:
		irNo("std call with an adapted result")
	case b.l.info.Closes[x] != nil:
		irNo("std Close")
	}
	if option {
		return b.commaOkOpCall(x, c, n, body, p)
	}
	if defersCall(body) {
		return b.seqOpCall(x, c, n, body, p) // Go's iter.Seq[T]: a Seq (ir_adapted.go)
	}
	if _, tuple := b.l.info.Types[x].(*check.Tuple); tuple && len(body.Results) != 1+btoi(goFallible(body)) {
		// A tuple of the method's one result (Queue[(A, B)]'s Pop, Go's
		// (T, error) at T the tuple's struct; ir_tuple.go) is that value;
		// Go's several results, one per element, are adapted.
		irNo("std call with an adapted result")
	}
	convs := map[int]string{} // by index of the receiver-first vals
	for i, q := range body.Params {
		if adapted(q) {
			continue // adaptOperands, ir_adapt.go
		}
		conv, ok := b.operandConv(x.Args[i], b.recvTypeArgs(n, q.Type), nil, body.Package) // ir_operand.go
		if !ok {
			irNo("std call with a converted operand")
		}
		if conv != "" {
			convs[i+1] = conv
		}
	}
	if failsHere(c) && !goFallible(body) {
		irNo("fallible call with an empty row at its site")
	}
	resConv := "" // the Go conversion the result takes (ir_operand.go basicOver)
	if goValue(body) && !b.sameGo(b.l.info.Types[x], b.recvTypeArgs(n, body.Results[0])) && !b.sameRT(b.l.info.Types[x], b.recvTypeArgs(n, body.Results[0]), nil, body.Package) {
		// A runtime type over the method's basic result (Mapped.Text's
		// string for fs.Text) is the result converted, when the call is
		// a single value (CONV-OPERAND).
		if !basicOver(b.l.info.Types[x], body.Results[0]) || goFallible(body) && failsHere(c) {
			irNo("std call with a converted result")
		}
		resConv = b.goType(b.l.info.Types[x], x)
	}
	hidden := b.hidden(body, x, c, p)
	pre, vals := b.operands(append([]syntax.Expr{c.Recv}, x.Args...))
	convOperands(vals, convs)                                                 // ir_operand.go
	pre = append(pre, b.adaptOperands(body.Params, x, x.Args, vals, 1, p)...) // ir_adapt.go
	e := &irExpr{K: "mcall", Pos: p, Name: memberName(c.Op.Name), X: vals[0], Args: append(hidden, vals[1:]...)}
	if goFallible(body) && !failsHere(c) {
		e = b.dropErr(e) // ir_call.go: the row is empty here
	}
	if resConv != "" {
		e = &irExpr{K: "conv", Pos: p, T: resConv, X: e}
	}
	return pre, e, !body.Pure
}

// recvTypeArgs is the Go type s of a runtime method of n's Go type with
// the method's type parameters (the receiver's: Ref[T]'s T) replaced by
// n's Go type arguments, so sameGo compares the instance's types.
func (b *irBuilder) recvTypeArgs(n *check.Named, s string) string {
	args := typeArgs(n)
	origin := n
	if n.Orig != nil {
		origin = n.Orig
	}
	i := 0
	for _, tp := range origin.TParams {
		if tp.Kind != check.KindType {
			continue
		}
		if i < len(args) && s == tp.Name {
			return b.fc.goType(args[i])
		}
		i++
	}
	return s
}

// sameGo reports whether the Ergo type t lowers to the Go type a
// runtime/v1 body spells goType, a runtime type unqualified.
func (b *irBuilder) sameGo(t check.Type, goType string) bool {
	if t == nil {
		return false
	}
	s := b.fc.goType(t)
	return s == goType || s == b.l.rt()+"."+goType
}

// importPath is the import path of the package main.go imports by the
// name q, or "".
func (b *irBuilder) importPath(q string) string {
	for k, n := range b.l.pkgNames {
		if path, ok := k.obj.(string); ok && k.kind == "import" && n == q {
			return path
		}
	}
	return ""
}
