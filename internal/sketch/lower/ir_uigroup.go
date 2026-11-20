package lower

// IR builder: component calls through std ui's group (P-ui; uigroup.go).

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// groupCall is the IR of the component call x of c through the group g,
// the emitter's groupCall (uigroup.go):
//
//	group[E, A](env, f, args, func(_ *Env) (struct{}, error) { Card(env, f, a, b); return struct{}{}, nil }, site)
//
// As the emitter's, a name, a field selection or a constant is read twice
// (into args, and in the call when it runs), and any other argument is
// evaluated once, in order, into a local both read (preset, NS2a). A
// record argument the component reads part of is evaluated once and the
// group compares the fields it reads (readFields, NS2b). When a compared
// value is one Go's == cannot compare, the call goes through groupBy with
// the generated equality (same), the ==-compared values (cheap,
// P-ui-flow) and a copy the group keeps (keep), and every argument is
// read from its local (NS2c). The body is a literal that closes over the
// maker's environment and locals, as the emitter's does; inGroup keeps
// the call in it from going through group again.
func (b *irBuilder) groupCall(x *syntax.CallExpr, c *check.Call, g *check.Func, p irPos) ([]*irStmt, *irExpr, bool) {
	if b.env < 0 {
		irNo("component call from a pure function")
	}
	frame, _ := b.l.groupFrame(b.fc.typeOf(x.Args[0]))
	by := b.fc.groupNeedsBy(c.Func, x)
	var pre []*irStmt
	var types []string
	var ctypes []check.Type
	var vals []*irExpr
	preset := map[syntax.Expr]int{}
	for k, a := range x.Args {
		if k == 0 {
			continue
		}
		t := b.goType(b.operandType(a), a)
		if fields, ok := b.fc.readFields(c.Func, k, x); ok {
			// The fields the component reads, of the argument evaluated
			// once into a local the call reads (NS2b).
			apre, v := b.operand(a)
			loc := b.temp("arg", t, p)
			pre = append(append(pre, apre...), &irStmt{K: "def", Pos: p, Local: loc, X: v})
			preset[a] = loc
			n := b.fc.typeOf(a).(*check.Named)
			td := b.l.typeDecl(n.Obj, a)
			for _, f := range fields {
				types = append(types, b.goType(f.Type, a))
				ctypes = append(ctypes, f.Type)
				vals = append(vals, &irExpr{K: "field", Pos: p, Name: td.fieldName[f.Name], X: &irExpr{K: "local", Pos: p, I: loc}})
			}
			continue
		}
		types = append(types, t)
		ctypes = append(ctypes, b.operandType(a))
		if _, constant := b.l.info.Values[a]; constant || !by && plainOperand(a) {
			vals = append(vals, b.expr(a))
			continue
		}
		// Evaluated once, here, in order: the call reads the local.
		apre, v := b.operand(a)
		loc := b.temp("arg", t, p)
		pre = append(append(pre, apre...), &irStmt{K: "def", Pos: p, Local: loc, X: v})
		preset[a] = loc
		vals = append(vals, &irExpr{K: "local", Pos: p, I: loc})
	}
	f := b.expr(x.Args[0])
	var argType string
	var args *irExpr
	switch len(types) {
	case 0:
		argType = "struct{}"
		args = &irExpr{K: "zero", Pos: p, T: argType}
	case 1:
		argType, args = types[0], vals[0]
	default:
		var fields, keys []string
		for i, t := range types {
			keys = append(keys, "F"+strconv.Itoa(i))
			fields = append(fields, keys[i]+" "+t)
		}
		argType = "struct{" + strings.Join(fields, "; ") + "}"
		args = &irExpr{K: "lit", Pos: p, T: argType, Keys: keys, Elems: vals}
	}
	bodySig, ok := g.Sig.Params[2].Type.(*check.Signature)
	if !ok {
		ice(x, "std ui's group takes no body third")
	}
	// The body: the call itself, built where the maker's locals and
	// environment are in scope, reading the preset locals.
	lit := &irExpr{K: "func", Pos: p, T: b.sigType(bodySig, x), End: &p}
	lit.Params = []int{b.temp("env", "*"+b.l.env(), p)}
	old, oldPreset := b.fc.inGroup, b.preset
	b.fc.inGroup, b.preset = x, preset
	callPre, call := b.call(x, true)
	b.fc.inGroup, b.preset = old, oldPreset
	lit.Body = append(lit.Body, callPre...)
	if call != nil {
		lit.Body = append(lit.Body, &irStmt{K: "expr", Pos: p, X: call})
	}
	lit.Body = append(lit.Body, &irStmt{K: "ret", Pos: p, X: &irExpr{K: "zero", Pos: p, T: "struct{}"}})
	operands := []*irExpr{f, args}
	if by {
		g = groupByOf(frame)
		if g == nil {
			ice(x, "std ui declares no groupBy over its frame")
		}
		operands = append(operands, b.sameLit(argType, ctypes, x, p), b.cheapLit(argType, ctypes, p), b.keepLit(argType, ctypes, x, p))
	}
	gd := b.l.useFunc(g, x)
	if gd.method != nil {
		ice(x, "std ui's group lowered as a method")
	}
	site, ok := g.Sig.Params[len(g.Sig.Params)-1].Type.(*check.Named)
	if !ok {
		ice(x, "std ui's group takes no CallSite last")
	}
	all := append(append(operands, lit), b.siteValue(x, site, p))
	if !gd.envFree {
		all = append([]*irExpr{{K: "local", Pos: p, I: b.env}}, all...)
	}
	e := &irExpr{K: "call", Pos: p, Name: gd.name, T: b.goType(frame.TArgs[0], x) + ", " + argType, Args: all}
	if gd.fallible {
		e = b.dropErr(e)
	}
	return pre, e, true
}

// sameLit is groupBy's same for arguments of the Go type argType holding
// values of ctypes (one, or fields F0, F1, …), the emitter's sameLit: each
// compared by Go's == or its generated structural equality (equal.go).
func (b *irBuilder) sameLit(argType string, ctypes []check.Type, x *syntax.CallExpr, p irPos) *irExpr {
	env := "*" + b.l.env()
	lit := &irExpr{K: "func", Pos: p, T: "func(" + env + ", " + argType + ", " + argType + ") (bool, error)", End: &p}
	ga, gb := b.temp("ga", argType, p), b.temp("gb", argType, p)
	lit.Params = []int{b.temp("env", env, p), ga, gb}
	part := func(i int) (*irExpr, *irExpr) {
		l, r := &irExpr{K: "local", Pos: p, I: ga}, &irExpr{K: "local", Pos: p, I: gb}
		if len(ctypes) == 1 {
			return l, r
		}
		n := "F" + strconv.Itoa(i)
		return &irExpr{K: "field", Pos: p, Name: n, X: l}, &irExpr{K: "field", Pos: p, Name: n, X: r}
	}
	var conj *irExpr
	for i, t := range ctypes {
		l, r := part(i)
		var eq *irExpr
		if check.Comparable(t) {
			eq = &irExpr{K: "bin", Pos: p, Op: "==", X: l, Y: r}
		} else {
			name, free := b.l.equalFunc(t, x)
			targs := make([]string, len(free))
			for k, tp := range free {
				targs[k] = b.goType(tp, x)
			}
			eq = &irExpr{K: "call", Pos: p, Name: name, T: strings.Join(targs, ", "), Args: []*irExpr{l, r}}
		}
		if conj == nil {
			conj = eq
		} else {
			conj = &irExpr{K: "bin", Pos: p, Op: "&&", X: conj, Y: eq}
		}
	}
	if conj == nil {
		ice(x, "groupBy's same over no argument")
	}
	lit.Body = []*irStmt{{K: "ret", Pos: p, X: conj}}
	return lit
}

// cheapLit is groupBy's cheap, the emitter's cheapLit: the values among
// the arguments that Go's == compares (groupArg), each by ==, and true
// when there are none.
func (b *irBuilder) cheapLit(argType string, ctypes []check.Type, p irPos) *irExpr {
	env := "*" + b.l.env()
	lit := &irExpr{K: "func", Pos: p, T: "func(" + env + ", " + argType + ", " + argType + ") (bool, error)", End: &p}
	ga, gb := b.temp("ga", argType, p), b.temp("gb", argType, p)
	lit.Params = []int{b.temp("env", env, p), ga, gb}
	var conj *irExpr
	for i, t := range ctypes {
		if !groupArg(t) {
			continue
		}
		l, r := &irExpr{K: "local", Pos: p, I: ga}, &irExpr{K: "local", Pos: p, I: gb}
		if len(ctypes) > 1 {
			n := "F" + strconv.Itoa(i)
			l, r = &irExpr{K: "field", Pos: p, Name: n, X: l}, &irExpr{K: "field", Pos: p, Name: n, X: r}
		}
		eq := &irExpr{K: "bin", Pos: p, Op: "==", X: l, Y: r}
		if conj == nil {
			conj = eq
		} else {
			conj = &irExpr{K: "bin", Pos: p, Op: "&&", X: conj, Y: eq}
		}
	}
	if conj == nil {
		conj = &irExpr{K: "const", Pos: p, T: "bool", V: "true"}
	}
	lit.Body = []*irStmt{{K: "ret", Pos: p, X: conj}}
	return lit
}

// keepLit is groupBy's keep, the emitter's keepLit: a copy of the
// arguments that shares no slice or Map with them (the value rule's copy,
// copyOf).
func (b *irBuilder) keepLit(argType string, ctypes []check.Type, x *syntax.CallExpr, p irPos) *irExpr {
	env := "*" + b.l.env()
	lit := &irExpr{K: "func", Pos: p, T: "func(" + env + ", " + argType + ") (" + argType + ", error)", End: &p}
	ga := b.temp("ga", argType, p)
	lit.Params = []int{b.temp("env", env, p), ga}
	a := &irExpr{K: "local", Pos: p, I: ga}
	var v *irExpr
	if len(ctypes) == 1 {
		v = b.copyOf(ctypes[0], x, a)
	} else {
		var keys []string
		var elems []*irExpr
		for i, t := range ctypes {
			n := "F" + strconv.Itoa(i)
			keys = append(keys, n)
			elems = append(elems, b.copyOf(t, x, &irExpr{K: "field", Pos: p, Name: n, X: a}))
		}
		v = &irExpr{K: "lit", Pos: p, T: argType, Keys: keys, Elems: elems}
	}
	lit.Body = []*irStmt{{K: "ret", Pos: p, X: v}}
	return lit
}
