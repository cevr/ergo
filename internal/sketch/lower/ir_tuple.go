package lower

// IR builder: tuples and calls of import go (FLIP F4b). See ir.go.
//
// A tuple value is the emitter's Go struct, struct{F0 T0; F1 T1} (expr.go,
// TupleExpr; tupleField): the IR spells it so, builds it as a keyed
// literal ("lit"), and reads an element as a field. A let that destructures
// a tuple binds each name to its element.
//
// A call of a Go function of import go (gocall.go) is a call of its Go
// target by its import path ("pcall"), as goCall writes it, when every
// parameter and result is a Go basic type: no copy, no adapter, no error
// and no poll (a host call is not an interruption point; runtime.md). A Go
// function with several results is bound by one "tdef", Go's own
// `a, b := f(…)`: a let that destructures it binds its names there, and
// any other use reads the results into the tuple's struct.
//
// Prior art: Gleam returns a tuple from an Erlang function as Erlang's
// tuple, and ReScript compiles a tuple to a JavaScript array; OCaml and
// MLton unbox a tuple that does not escape into registers, as Go's SSA
// does with a small struct; Go's own multi-value call is what goCall
// writes.

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

func isTuple(t check.Type) bool {
	tu, ok := t.(*check.Tuple)
	return ok && len(tu.Types) >= 2
}

// tupleType is the IR spelling of the tuple t: struct{F0 T0; F1 T1}.
func (b *irBuilder) tupleType(t *check.Tuple, site syntax.Node) string {
	parts := make([]string, len(t.Types))
	for i, e := range t.Types {
		parts[i] = tupleField(i) + " " + b.goType(e, site)
	}
	return "struct{" + strings.Join(parts, "; ") + "}"
}

// structSpelling writes to out the IR spelling of the unnamed struct type
// that starts at s[i:] in the Go type spelling s, and returns its length
// in s. The emitter spells a tuple struct{ F0 T0; F1 T1 } (types.go); the
// IR spells it as tupleType does, struct{F0 T0; F1 T1}, each field's type
// spelled as every type is. So a tuple stands wherever a type is spelled,
// at any depth: a type argument (Option[(int, int)]), an element, a
// pointer's target, a field, a tuple's own element (PORT-TUPLE-ARG).
func (b *irBuilder) structSpelling(s string, i int, out *strings.Builder) int {
	open := i + len("struct{")
	depth, end := 1, -1
	for j := open; j < len(s) && end < 0; j++ {
		switch s[j] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth--; depth == 0 {
				end = j
			}
		}
	}
	if end < 0 {
		irNo("Go type spelling %s", s)
	}
	var fields []string
	if body := strings.TrimSpace(s[open:end]); body != "" {
		for _, part := range irSplitTop(body, ';') { // ir_gotext_expr.go
			name, ft, ok := strings.Cut(strings.TrimSpace(part), " ")
			if !ok || name == "" || strings.ContainsAny(name, ".[]*") || strings.TrimSpace(ft) == "" {
				irNo("Go type spelling %s", s)
			}
			fields = append(fields, name+" "+b.spelling(strings.TrimSpace(ft)))
		}
	}
	out.WriteString("struct{" + strings.Join(fields, "; ") + "}")
	return end + 1 - i
}

// tupleLit is the tuple value of type t whose elements are vals.
func (b *irBuilder) tupleLit(t *check.Tuple, vals []*irExpr, site syntax.Node, p irPos) *irExpr {
	lit := &irExpr{K: "lit", Pos: p, T: b.tupleType(t, site)}
	for i, v := range vals {
		lit.Keys = append(lit.Keys, tupleField(i))
		lit.Elems = append(lit.Elems, v)
	}
	return lit
}

// tupleValue is the tuple expression x, (a, b), after its operands'
// statements.
func (b *irBuilder) tupleValue(x *syntax.TupleExpr) ([]*irStmt, *irExpr) {
	t, ok := b.l.info.Types[x].(*check.Tuple)
	if !ok || len(t.Types) != len(x.Elts) || len(t.Types) < 2 {
		irNo("type *check.Tuple")
	}
	pre, vals := b.operands(x.Elts) // ir_fail.go
	return pre, b.tupleLit(t, vals, x, b.pos(x, x.Pos()))
}

// hostBasic reports a Go basic type that a Go call passes or returns with
// no adapter: not a Duration (time.Duration in Go), complex or uintptr.
func hostBasic(t check.Type) bool {
	bt, ok := t.(*check.Basic)
	if !ok {
		return false
	}
	switch bt.Kind {
	case check.Complex64, check.Complex128, check.Uintptr, check.Invalid, check.Duration:
		return false
	}
	return true
}

// hostResults are the Go results of the import go function f.
func hostResults(f *check.Func) []check.Type {
	if len(f.Sig.Results) == 1 {
		if t, ok := f.Sig.Results[0].(*check.Tuple); ok {
			return t.Types
		}
	}
	return f.Sig.Results
}

// hostCall is the call x of f, a Go function of import go or the target
// of a user extern: its Go target's "pcall", or, for a target with
// several results, a "tdef" of them into fresh locals and their tuple.
// The domain is goCall's boundary with nothing to adapt. A call with an
// empty needs row binds the reviewed pure tier (check/extern.go, G9: it
// reads only its arguments), so it is a value in its expression; one
// that needs go.Foreign is an effect, which runs before its expression
// (SP4).
func (b *irBuilder) hostCall(x *syntax.CallExpr, c *check.Call, f *check.Func, p irPos) ([]*irStmt, *irExpr, bool) {
	b.l.intMapCall(x, c.Recv, f, true)             // intmap.go: the Go boundary sees Go's map
	if pre, v, ok := b.hostBound(x, c, f, p); ok { // ir_golib.go: the task's context, an Option result
		return pre, v, false
	}
	pre, call := b.hostTarget(x, c, f, p)
	results := hostResults(f)
	effect := len(rowElems(f.Sig.Uses)) > 0 // the foreign tier
	if f.Sig.Fallible {
		// Go's results are the value and the error, which the fail
		// forms bind and convert to go.Error (ir_fail.go bind; several
		// values into a tuple, hostBind).
		if !failsHere(c) {
			irNo("fallible call with an empty row at its site")
		}
		return pre, call, effect
	}
	if len(results) < 2 {
		return pre, call, effect
	}
	t, ok := b.l.info.Types[x].(*check.Tuple)
	if !ok || len(t.Types) != len(results) {
		irNo("call of a Go package function")
	}
	tpre, tuple := b.tdef(call, t, x, p)
	return append(pre, tpre...), tuple, false
}

// tdef binds call, a Go call whose results are t's elements, to fresh
// locals in one "tdef", and is their tuple: the call runs in the tdef, so
// the tuple only reads the locals (letTuple binds the names there instead).
func (b *irBuilder) tdef(call *irExpr, t *check.Tuple, x syntax.Node, p irPos) ([]*irStmt, *irExpr) {
	def := &irStmt{K: "tdef", Pos: p, X: call}
	vals := make([]*irExpr, len(t.Types))
	for i, r := range t.Types {
		l := b.temp("r", b.goType(r, x), p)
		def.Ls = append(def.Ls, l)
		vals[i] = &irExpr{K: "local", Pos: p, I: l}
	}
	return []*irStmt{def}, b.tupleLit(t, vals, x, p)
}

// tupleErrBind binds call, a Go call whose results are t's elements and
// then an error, to fresh locals and the error local e in one "tdef", and
// defines a local of their tuple, which it returns: bind's form of a std
// body returning (A, B, error) (task's TaskExit; EXTERN-MULTI-RESULT).
func (b *irBuilder) tupleErrBind(call *irExpr, t *check.Tuple, e int, x syntax.Node, p irPos) ([]*irStmt, int) {
	pre, lit := b.tdef(call, t, x, p)
	pre[0].Ls = append(pre[0].Ls, e)
	v := b.temp("v", b.tupleType(t, x), p)
	return append(pre, &irStmt{K: "def", Pos: p, Local: v, X: lit}), v
}

// readsTdef reports whether e is the tuple of def, a tdef: its elements
// read def's locals in order, so a let can bind its names in def itself.
// A tuple literal whose operand only ran a tdef, (bits.Add64(…), 3), is
// not.
func readsTdef(e *irExpr, def *irStmt) bool {
	if e == nil || e.K != "lit" || def.K != "tdef" || len(e.Elems) != len(def.Ls) {
		return false
	}
	for i, x := range e.Elems {
		if x.K != "local" || x.I != def.Ls[i] {
			return false
		}
	}
	return true
}

// hostTarget is the Go call of f's target with x's operands, after their
// statements (goTarget, ir_golib.go); it refuses a call outside its
// domain.
func (b *irBuilder) hostTarget(x *syntax.CallExpr, c *check.Call, f *check.Func, p irPos) ([]*irStmt, *irExpr) {
	// SP4: a user extern whose Go target needs an adapter is not called
	// plainly; every other call is FORK-GOLIB's goTarget (I-FLIP: SP4 x
	// FORK-GOLIB).
	if ge := check.GoExternOf(f); ge != nil && ge.Adapter != check.AdaptSame {
		irNo("call of a Go package function")
	}
	return b.goTarget(x, c, f, p) // ir_golib.go (FORK-GOLIB)
}

// letTuple lowers `let (a, b) = x` and `let a, b = x` of the tuple x:
// each name, in order, is defined from its element. A call of a Go
// function with several results defines the names in its "tdef" itself
// (`a, b := f(…)`); any other value is bound once and read by field.
// ok is false for a let that is not one of these shapes.
func (b *irBuilder) letTuple(spec *syntax.VarSpec, p irPos) (out []*irStmt, ok bool) {
	if len(spec.Values) != 1 {
		return nil, false
	}
	x := spec.Values[0]
	var names []*syntax.Ident
	var vars []*check.Var // each name's variable; nil for _
	switch {
	case spec.Pattern == nil && len(spec.Names) > 1:
		names = spec.Names
		for _, id := range names {
			v, _ := b.l.info.Defs[id].(*check.Var)
			if v == nil && id.Name != "_" {
				irNo("let without a variable")
			}
			vars = append(vars, v)
		}
	case spec.Pattern != nil:
		tp, isTuple := unparen(spec.Pattern).(*syntax.TupleExpr)
		pat := b.l.info.Patterns[spec.Pattern]
		if !isTuple || pat == nil || pat.Kind != check.PatTuple || len(pat.Elems) != len(tp.Elts) {
			return nil, false
		}
		for i, e := range tp.Elts {
			id, isIdent := unparen(e).(*syntax.Ident)
			k := pat.Elems[i].Kind
			if !isIdent || k != check.PatBind && k != check.PatWildcard || k == check.PatBind && pat.Elems[i].Var == nil {
				return nil, false
			}
			names = append(names, id)
			vars = append(vars, pat.Elems[i].Var) // nil for _
		}
	default:
		return nil, false
	}
	t, isTuple := b.l.info.Types[x].(*check.Tuple)
	if !isTuple || len(t.Types) != len(names) {
		return nil, false
	}
	if _, conv := b.l.info.Implicit[x]; conv {
		irNo("let pattern")
	}
	for _, v := range vars {
		if v != nil && b.fc.scalarOf(v) != nil {
			irNo("scalar-form local")
		}
	}
	var pre []*irStmt
	var e *irExpr
	b.part(func() { pre, e = b.value(x) })
	out = append(out, pre...)
	// An element a typed let converts (`let a, b Store = two()`) is
	// defined from its converted field, as the emitter's elemAt.
	convs, converts := b.l.info.ImplicitElems[x]
	if converts && len(convs) != len(names) {
		irNo("element conversions of another tuple")
	}
	locals := make([]int, len(vars))
	for i := range locals {
		locals[i] = -1
	}
	bind := func(i int, v *check.Var) {
		if v == nil {
			return // Go's _
		}
		b.part(func() { locals[i] = b.local(v, names[i]) })
		if locals[i] < 0 {
			b.reserve(v)
		}
	}
	if n := len(out); n > 0 && readsTdef(e, out[n-1]) {
		if converts {
			irNo("element conversions of a Go call's results")
		}
		// The Go call's own locals are the names: `a, b := f(…)`.
		def := out[n-1]
		for i, v := range vars {
			bind(i, v)
			def.Ls[i] = locals[i]
		}
	} else {
		tmp := -1
		b.part(func() { tmp = b.temp("p", b.goType(t, x), p) })
		out = append(out, &irStmt{K: "def", Pos: p, Local: tmp, X: e})
		for i, v := range vars {
			if v == nil {
				continue
			}
			bind(i, v)
			field := &irExpr{K: "field", Pos: p, Name: tupleField(i), X: &irExpr{K: "local", Pos: p, I: tmp}}
			if converts {
				field = b.convert(convs[i], field, x)
			}
			out = append(out, &irStmt{K: "def", Pos: p, Local: locals[i], X: field})
		}
	}
	// As in letPattern, bind all names, then register their owners in
	// order, then release the statement's temporaries.
	for i, v := range vars {
		if locals[i] >= 0 {
			out = append(out, b.ownLet(v, locals[i], spec, nil)...)
		}
	}
	return append(out, b.releaseAt(spec, p)...), true
}
