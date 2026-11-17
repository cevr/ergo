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
// type the subset admits: names, []T, pkg.Name and a generic type's
// instance Name[T1, T2], where pkg is the name main.go imports a package
// by. The IR names that package by its import path (the decoder resolves
// the path among main.go's imports), and the lowering keeps the import
// used (keepImport): an IR body is not Go text, and Go refuses an unused
// import.
func (b *irBuilder) spelling(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && isSpellingName(s[j]) {
			j++
		}
		if j == i {
			switch {
			case strings.HasPrefix(s[i:], "[]"):
				out.WriteString("[]")
				i += 2
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
		if strings.Contains(s[i:j], ".") {
			// The whole Go type keeps the import used: a generic type
			// (ergo.Ref) is no type without its arguments. A path that
			// is its own name (time) needs it too.
			b.keepImport("var _ " + s)
		}
		out.WriteString(q)
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
		if a == check.NoValue {
			// A type parameter bound by a block with no value (the
			// emitter's struct{}): the decoder has no struct{} type.
			irNo("type argument with no value")
		}
		b.admit(a, depth+1)
	}
}

// typeArgs is the Go type argument list of the generic call c, comma and
// space between them, as the emitter's typeArgs writes it in brackets: a
// type argument of each type-kinded parameter (a row or an operations
// parameter has no Go type parameter). "" for a call of a function that
// is not generic.
func (b *irBuilder) typeArgs(c *check.Call, site syntax.Node) string {
	var out []string
	for i, tp := range c.Func.Sig.TParams {
		if tp.Kind != check.KindType {
			continue
		}
		if i >= len(c.TypeArgs) {
			irNo("generic call without its type arguments")
		}
		out = append(out, b.goType(c.TypeArgs[i], site))
	}
	return strings.Join(out, ", ")
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
	case preludeAppend(c.Func):
		if len(c.TypeArgs) != 1 {
			irNo("append form")
		}
		elem := b.goType(c.TypeArgs[0], x)
		if b.goType(b.l.info.Types[args[0]], x) != "[]"+elem {
			// Go assigns a defined slice type to []T; the decoder takes
			// identical types only.
			irNo("append to a defined slice type")
		}
		b.keepImport("var _ = " + b.l.goPkg("slices") + ".Clip[[]" + b.fc.goType(c.TypeArgs[0]) + "]")
		src := irArgs[0]
		irArgs[0] = &irExpr{K: "pcall", Pos: src.Pos, T: "slices", Name: "Clip", V: "[]" + elem + ", " + elem, Args: []*irExpr{src}}
	case preludeMapWrite(c.Func):
		if len(c.TypeArgs) != 2 {
			irNo("Map write form")
		}
		k, v := b.goType(c.TypeArgs[0], x), b.goType(c.TypeArgs[1], x)
		m := "map[" + k + "]" + v
		if b.goType(b.l.info.Types[args[0]], x) != m {
			irNo("Map write to a defined Map type")
		}
		b.keepImport("var _ = " + b.l.goPkg("maps") + ".Clone[map[" + b.fc.goType(c.TypeArgs[0]) + "]" + b.fc.goType(c.TypeArgs[1]) + "]")
		src := irArgs[0]
		irArgs[0] = &irExpr{K: "pcall", Pos: src.Pos, T: "maps", Name: "Clone", V: m + ", " + k + ", " + v, Args: []*irExpr{src}}
	}
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
	var out []*irExpr
	for _, e := range x.Elts {
		kv, ok := e.(*syntax.KeyValueExpr)
		if !ok {
			irNo("Map literal element without a key")
		}
		out = append(out, b.expr(kv.Key), b.expr(kv.Value))
	}
	return out
}

// admitOps admits a value of a program's operation type: the Go interface
// the lowering declares for it (decls.go, kindOps), whose methods take the
// running task first; a std operation type (sync.Ref, queue.Queue) is its
// runtime type, a pointer to a runtime/v1 struct (runtimeType), whose
// operations are its methods (stdOpCall).
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
	case b.env < 0:
		irNo("operation call from a pure function")
	case b.fc.refSiteOf(x) != nil:
		irNo("operation call into an owning literal")
	}
	n, ok := b.l.info.Types[c.Recv].(*check.Named)
	if !ok {
		irNo("operation call on a %T", b.l.info.Types[c.Recv])
	}
	if b.l.typeDecl(n.Obj, x).kind != kindOps {
		irNo("operation call on a named type that is no operation type")
	}
	b.admitOps(n)
	if b.l.runtimeType(n, x) != "" {
		return b.stdOpCall(x, c, n, p)
	}
	if len(rowElems(c.Op.Sig.Errors)) > 0 && !failsHere(c) {
		irNo("fallible call with an empty row at its site")
	}
	pre, vals := b.operands(append([]syntax.Expr{c.Recv}, x.Args...))
	env := &irExpr{K: "local", Pos: p, I: b.env}
	args := append([]*irExpr{{K: "field", Pos: p, Name: envTask, X: env}}, vals[1:]...)
	return pre, &irExpr{K: "mcall", Pos: p, Name: memberName(c.Op.Name), X: vals[0], Args: args}, true
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
	if len(pre) > 0 {
		irNo("operation call in a range")
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
	b.loopBody(loop, s, s.Body)
	return []*irStmt{{K: "def", Pos: p, Local: r, X: x}, loop}
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
	_, ptr := b.l.info.Types[recv].(*check.Pointer)
	return ptr
}

// derefNamed is the named type of a selector's operand: t itself, or the
// record a *T points to, whose field Go's selector reads through the
// pointer (the emitter's fieldName).
func derefNamed(t check.Type) (*check.Named, bool) {
	if p, ok := t.(*check.Pointer); ok {
		t = p.Elem
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
	switch {
	case !ok:
		irNo("call of a std intrinsic")
	case c.Recv != nil:
		irNo("dot call of a std function")
	case b.fc.refSiteOf(x) != nil:
		irNo("std call into an owning literal")
	case len(body.TypeParams) > 0:
		irNo("generic or task call")
	case defersCall(body) || commaOk(body):
		irNo("std call with an adapted result")
	case len(body.Params) != len(x.Args):
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
	if _, tuple := b.l.info.Types[x].(*check.Tuple); tuple {
		irNo("std call with an adapted result")
	}
	// The decoder takes an argument and a result of the runtime's Go type
	// itself (types2.Identical), where Go text would also assign a
	// defined type to its underlying one: each must be spelled alike.
	for i, q := range body.Params {
		if q.Func != nil || q.Funcs != nil {
			irNo("std call that adapts a function")
		}
		if !b.sameGo(b.l.info.Types[x.Args[i]], q.Type) {
			irNo("std call with a converted operand")
		}
	}
	if goFallible(body) != failsHere(c) {
		irNo("fallible call with an empty row at its site")
	}
	if goValue(body) && !b.sameGo(b.l.info.Types[x], body.Results[0]) {
		irNo("std call with a converted result")
	}
	hidden := b.hidden(body, x, c, p)
	pre, args := b.operands(x.Args)
	// A body of a runtime leaf (runtime/v1/rpc, BS8) is called by the
	// leaf's import path, which rtRef imports.
	b.keepImport("var _ = " + b.l.rtRef(name))
	pkg := v1.PackageOf(name)
	if pkg == "" {
		pkg = runtimeImport
	}
	return pre, &irExpr{K: "pcall", Pos: p, T: pkg, Name: name, Args: append(hidden, args...)}, !body.Pure
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
			out = append(out, &irExpr{K: "field", Pos: p, Name: b.l.envField(schedulerCap(c, x)), X: env})
		case v1.ReadingPackage:
			if c.Func == nil || c.Func.Pkg == nil {
				irNo("std call with hidden arguments: a context read without its package")
			}
			out = append(out, &irExpr{K: "const", Pos: p, T: "string", V: contextNamespace(c.Func.Pkg)})
		case v1.CallSite:
			src, line, column := b.l.locate(x, x.Pos())
			site := fmt.Sprintf("%s calls %s at %s:%d:%d", b.fc.owner, b.fc.callee(x, c), src, line, column)
			b.keepImport("var _ = " + b.l.rt() + ".Site")
			out = append(out, &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "Site", Args: []*irExpr{{K: "const", Pos: p, T: "string", V: site}}})
		default:
			irNo("std call with hidden argument %s", h)
		}
	}
	return out
}

// stdOpCall is the call x of the operation c.Op on a value of a std
// operation type n (sync.Ref, queue.Queue): the runtime/v1 method of
// the value's Go type, its hidden arguments first, as the emitter's opCall
// writes it through runtimeCall, when the method needs no adapter.
func (b *irBuilder) stdOpCall(x *syntax.CallExpr, c *check.Call, n *check.Named, p irPos) ([]*irStmt, *irExpr, bool) {
	name := n.Obj.Name + "." + c.Op.Name
	if _, bound := runtimeBindings()[name]; !bound {
		irNo("std call with no runtime binding")
	}
	body, err := v1.BodyOf(name)
	if err != nil || len(body.Params) != len(x.Args) {
		irNo("std operation without its runtime method")
	}
	switch {
	case defersCall(body) || commaOk(body):
		irNo("std call with an adapted result")
	case b.l.info.Closes[x] != nil:
		irNo("std Close")
	}
	if _, tuple := b.l.info.Types[x].(*check.Tuple); tuple {
		irNo("std call with an adapted result")
	}
	for i, q := range body.Params {
		if q.Func != nil || q.Funcs != nil {
			irNo("std call that adapts a function")
		}
		if !b.sameGo(b.l.info.Types[x.Args[i]], b.recvTypeArgs(n, q.Type)) {
			irNo("std call with a converted operand")
		}
	}
	if goFallible(body) != failsHere(c) {
		irNo("fallible call with an empty row at its site")
	}
	if goValue(body) && !b.sameGo(b.l.info.Types[x], b.recvTypeArgs(n, body.Results[0])) {
		irNo("std call with a converted result")
	}
	hidden := b.hidden(body, x, c, p)
	pre, vals := b.operands(append([]syntax.Expr{c.Recv}, x.Args...))
	return pre, &irExpr{K: "mcall", Pos: p, Name: memberName(c.Op.Name), X: vals[0], Args: append(hidden, vals[1:]...)}, !body.Pure
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
