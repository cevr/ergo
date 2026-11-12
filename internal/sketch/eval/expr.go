package eval

import (
	"strconv"
	"strings"
	"time"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// expr evaluates x. Each evaluated node costs a step before its work.
func (in *interp) expr(fr *frame, x syntax.Expr) (Value, *abrupt) {
	if ab := in.charge(fr, x, 1); ab != nil {
		return nil, ab
	}
	v, ab := in.exprOf(fr, x)
	if ab != nil {
		if ab.kind == ctlFail && fr.info.TryCovers[x] != nil {
			// A right operand that a try on its left covers (q105,
			// check.Info.TryCovers): its failure leaves the frame, as
			// under the try itself.
			out := *ab
			out.kind = ctlEscape
			return nil, &out
		}
		return nil, ab
	}
	if conv, ok := fr.info.Implicit[x]; ok && conv.Kind != check.ConvVariant && conv.Kind != check.ConvRow {
		// A variant or a row value goes into its union unchanged: the
		// value keeps its record type. An operations or machine table is
		// a function value.
		return nil, fr.unsupported(x, "a conversion to the operation type %s yet", conv.To)
	}
	for _, conv := range fr.info.ImplicitElems[x] {
		if conv.Kind != 0 && conv.Kind != check.ConvVariant && conv.Kind != check.ConvRow {
			return nil, fr.unsupported(x, "a conversion to the operation type %s yet", conv.To)
		}
	}
	return v, nil
}

func (in *interp) exprOf(fr *frame, x syntax.Expr) (Value, *abrupt) {
	if c, ok := fr.info.Values[x]; ok {
		t := fr.rtypeOf(x)
		v, ok := fromConstant(t.t, c)
		if !ok {
			return nil, fr.unsupported(x, "a constant of %s yet", t.t)
		}
		return v, nil
	}
	if r, ok := in.bareRecord(fr, x); ok {
		return r, nil
	}
	if fr.info.ContextValues[x] != nil {
		return nil, fr.unsupported(x, "a context value, which binds capabilities")
	}
	switch x := x.(type) {
	case *syntax.ParenExpr:
		return in.expr(fr, x.X)
	case *syntax.Ident:
		return in.ident(fr, x)
	case *syntax.BasicLit:
		ice("a literal with no Values entry")
	case *syntax.SelectorExpr, *syntax.IndexExpr:
		v, root, ab := in.peek(fr, x)
		if ab != nil {
			return nil, ab
		}
		if root != nil && !basicValue(v) {
			// A part that holds a store leaves the slot: the slot lends.
			root.lend()
		}
		return v, nil
	case *syntax.SliceExpr:
		return in.sliceExpr(fr, x)
	case *syntax.BinaryExpr:
		return in.binary(fr, x)
	case *syntax.UnaryExpr:
		return in.unary(fr, x)
	case *syntax.CompositeLit:
		return in.composite(fr, x)
	case *syntax.CallExpr:
		return in.callExpr(fr, x)
	case *syntax.TryExpr:
		return in.try(fr, x)
	case *syntax.CatchExpr:
		return in.catch(fr, x, false)
	case *syntax.IfStmt, *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		v, ab := in.yieldStmt(fr, x.(syntax.Stmt), false)
		if ab != nil {
			return nil, ab
		}
		if v == nil {
			v = unit{}
		}
		return v, nil
	case *syntax.InterpolatedString:
		return in.interpolated(fr, x)
	case *syntax.TupleExpr:
		out := tuple{elems: make([]Value, len(x.Elts))}
		for i, e := range x.Elts {
			v, ab := in.expr(fr, e)
			if ab != nil {
				return nil, ab
			}
			out.elems[i] = v
		}
		return out, nil
	case *syntax.FuncLit:
		// Making a function literal runs nothing: the value is the literal
		// and the slots it reads. Only a call where it is written runs it
		// at check time; any other call of a function value is opaque
		// (rows.md, Footprints) and never reaches the evaluator.
		return &closure{lit: x, vars: fr.vars, env: fr.env}, nil
	case *syntax.StarExpr:
		return nil, fr.unsupported(x, "a pointer")
	}
	return nil, fr.unsupported(x, "the expression form %T", x)
}

// closure is a function literal's value: the literal and the slots of the
// frame that made it.
type closure struct {
	lit  *syntax.FuncLit
	vars map[*check.Var]*slot
	env  *tenv
}

// bareRecord is the value of a record with no fields named bare,
// `Pending` or `pkg.Pending`: its Uses entry is the record's TypeName.
func (in *interp) bareRecord(fr *frame, x syntax.Expr) (Value, bool) {
	var tn *check.TypeName
	switch h := x.(type) {
	case *syntax.Ident:
		tn, _ = fr.info.Uses[h].(*check.TypeName)
	case *syntax.SelectorExpr:
		id, ok := unparen(h.X).(*syntax.Ident)
		if !ok {
			return nil, false
		}
		pn, ok := fr.info.Uses[id].(*check.PkgName)
		named, isNamed := fr.info.Types[x].(*check.Named)
		if !ok || !isNamed || named.Obj.Pkg != pn.Pkg || named.Obj.Name != h.Sel.Name {
			return nil, false
		}
		tn = named.Obj
	}
	if tn == nil {
		return nil, false
	}
	t := fr.rtypeOf(x)
	n, ok := t.t.(*check.Named)
	if !ok || structOf(n) == nil || len(structOf(n).Fields) != 0 {
		ice("a bare value of %s is not a record with no fields", tn.Name)
	}
	return &record{t: n, key: argsKey(n, t.env)}, true
}

// ident reads a name. A local's slot lends: its value may now be shared.
func (in *interp) ident(fr *frame, id *syntax.Ident) (Value, *abrupt) {
	switch o := fr.info.Uses[id].(type) {
	case *check.Var:
		if s := in.slotOf(fr, o); s != nil {
			s.lend()
			return s.v, nil
		}
		return nil, fr.unsupported(id, "the package let %s, which no initializer bound", id.Name)
	case *check.Const:
		return o.Val, nil
	case *check.Func:
		return nil, fr.unsupported(id, "a function value (%s) yet", id.Name)
	case nil:
		ice("the identifier %s has no Uses entry", id.Name)
	}
	return nil, fr.unsupported(id, "the name %s here", id.Name)
}

// slotOf is v's slot: a local of the frame, or a package let.
func (in *interp) slotOf(fr *frame, v *check.Var) *slot {
	if s, ok := fr.vars[v]; ok {
		return s
	}
	return in.globals[v]
}

// peek reads x without lending: a local, a package let, a field of a
// record or an element of a slice, array or string, through a path of
// them. root is the local's slot at the path's root, nil when the path
// does not start at one; the caller lends it when the value it reads
// holds a store.
func (in *interp) peek(fr *frame, x syntax.Expr) (Value, *slot, *abrupt) {
	switch x := unparen(x).(type) {
	case *syntax.Ident:
		if v, ok := fr.info.Uses[x].(*check.Var); ok {
			if s := in.slotOf(fr, v); s != nil {
				if ab := in.charge(fr, x, 1); ab != nil {
					return nil, nil, ab
				}
				return s.v, s, nil
			}
		}
	case *syntax.SelectorExpr:
		if id, ok := unparen(x.X).(*syntax.Ident); ok {
			if _, isPkg := fr.info.Uses[id].(*check.PkgName); isPkg {
				// A package's let: its slot, which nothing writes.
				if v, isVar := fr.info.Uses[x.Sel].(*check.Var); isVar {
					if s := in.globals[v]; s != nil {
						return s.v, nil, nil
					}
				}
				return nil, nil, fr.unsupported(x, "a package member (%s.%s) as a value yet", id.Name, x.Sel.Name)
			}
		}
		t := fr.rtypeOf(x.X)
		n, ok := t.t.(*check.Named)
		if !ok || check.ScriptOf(n) != nil || structOf(n) == nil {
			return nil, nil, fr.unsupported(x, "a member of %s yet", t.t)
		}
		base, root, ab := in.peek(fr, x.X)
		if ab != nil {
			return nil, nil, ab
		}
		r, ok := base.(*record)
		if !ok {
			ice("a field of %T", base)
		}
		return r.fields[fieldIndex(r.t, x.Sel.Name)], root, nil
	case *syntax.IndexExpr:
		if fr.info.Instances[x] != nil {
			return nil, nil, fr.unsupported(x, "a generic function's instance as a value yet")
		}
		if len(x.Indices) != 1 {
			ice("an index with %d indices", len(x.Indices))
		}
		base, root, ab := in.peek(fr, x.X)
		if ab != nil {
			return nil, nil, ab
		}
		iv, ab := in.expr(fr, x.Indices[0])
		if ab != nil {
			return nil, nil, ab
		}
		switch b := base.(type) {
		case string:
			i, ok := in.index(fr, x, iv, len(b))
			if !ok {
				return nil, nil, fr.defect(x, "index out of range [%s] with length %d", describe(iv), len(b))
			}
			return b[i], root, nil
		case sliceV:
			i, ok := in.index(fr, x, iv, b.n)
			if !ok {
				return nil, nil, fr.defect(x, "index out of range [%s] with length %d", describe(iv), b.n)
			}
			return b.b.data[b.off+i], root, nil
		case mapV:
			ice("a Map read by index")
		}
		ice("an index of %T", base)
	}
	v, ab := in.expr(fr, x)
	return v, nil, ab
}

// index is the integer iv as an index below n.
func (in *interp) index(fr *frame, at syntax.Node, iv Value, n int) (int, bool) {
	i, negative, ok := shiftCount(iv)
	if !ok {
		ice("an index of %T", iv)
	}
	if negative || i >= uint64(n) {
		return 0, false
	}
	return int(i), true
}

// sliceExpr is xs[lo:hi] or xs[lo:hi:max] of a slice, an array or a
// string. A bound past the length is a defect for an array and a string;
// for a slice, Go reads up to the store's capacity, which the Go target's
// growth policy decides, so the evaluator does not guess: a skip.
func (in *interp) sliceExpr(fr *frame, x *syntax.SliceExpr) (Value, *abrupt) {
	base, root, ab := in.peek(fr, x.X)
	if ab != nil {
		return nil, ab
	}
	if root != nil {
		root.lend()
	}
	length := 0
	switch b := base.(type) {
	case string:
		length = len(b)
	case sliceV:
		length = b.n
	default:
		ice("a slice of %T", base)
	}
	bounds := []int{0, length, length}
	for i, e := range []syntax.Expr{x.Low, x.High, x.Max} {
		if e == nil {
			continue
		}
		v, ab := in.expr(fr, e)
		if ab != nil {
			return nil, ab
		}
		n, negative, ok := shiftCount(v)
		if !ok {
			ice("a slice bound of %T", v)
		}
		if negative {
			return nil, fr.defect(e, "slice bounds out of range [%s]", describe(v))
		}
		if n > uint64(length) {
			if s, isSlice := base.(sliceV); isSlice && !s.array {
				return nil, fr.unsupported(e, "a slice bound past the length, which reads the Go target's capacity")
			}
			return nil, fr.defect(e, "slice bounds out of range [%d] with length %d", n, length)
		}
		bounds[i] = int(n)
		if i == 1 && x.Max == nil {
			bounds[2] = max(bounds[2], bounds[1])
		}
	}
	lo, hi, mx := bounds[0], bounds[1], bounds[2]
	if lo > hi || hi > mx {
		return nil, fr.defect(x, "slice bounds out of range [%d:%d:%d]", lo, hi, mx)
	}
	switch b := base.(type) {
	case string:
		if ab := in.charge(fr, x, int64(hi-lo)); ab != nil {
			return nil, ab
		}
		return b[lo:hi], nil
	case sliceV:
		out := sliceV{b: b.b, off: b.off + lo, n: hi - lo}
		if b.array || x.Max != nil && mx < length {
			// A slice of an array is a new slice of its elements, and a
			// three-index slice ends its capacity at max: an Append on it
			// copies, so it never writes past its end.
			if ab := in.charge(fr, x, int64(hi-lo)+1); ab != nil {
				return nil, ab
			}
			out = newSlice(append([]Value(nil), b.b.data[b.off+lo:b.off+hi]...))
		}
		return out, nil
	}
	return nil, nil
}

// binary evaluates x op y: && and || evaluate y only when x does not
// decide; every other operator evaluates x, then y.
func (in *interp) binary(fr *frame, x *syntax.BinaryExpr) (Value, *abrupt) {
	switch x.Op {
	case syntax.LAND, syntax.LOR:
		a, ab := in.expr(fr, x.X)
		if ab != nil {
			return nil, ab
		}
		if truth(a) == (x.Op == syntax.LOR) {
			return a, nil
		}
		return in.expr(fr, x.Y)
	}
	a, ab := in.expr(fr, x.X)
	if ab != nil {
		return nil, ab
	}
	b, ab := in.expr(fr, x.Y)
	if ab != nil {
		return nil, ab
	}
	if x.Op == syntax.DOTDOT {
		// a..b is the prelude's Range[T]{Start: a, End: b} (Loops and len 2).
		t := fr.rtypeOf(x)
		n, ok := t.t.(*check.Named)
		if !ok || structOf(n) == nil {
			ice("a range of %s", t.t)
		}
		r := &record{t: n, key: argsKey(n, t.env), fields: make([]Value, 2)}
		r.fields[fieldIndex(n, "Start")] = a
		r.fields[fieldIndex(n, "End")] = b
		return r, nil
	}
	return in.binop(fr, x, x.Op, a, b)
}

func (in *interp) unary(fr *frame, x *syntax.UnaryExpr) (Value, *abrupt) {
	if x.Op == syntax.AND {
		return nil, fr.unsupported(x, "a pointer")
	}
	a, ab := in.expr(fr, x.X)
	if ab != nil {
		return nil, ab
	}
	v, ok := unop(x.Op, a)
	if !ok {
		return nil, fr.unsupported(x, "the operator %s on %T yet", x.Op, a)
	}
	return v, nil
}

// composite builds a record literal, an update `T{..b, F: v}`, a spread
// from a record of another type, or a slice, array or Map literal. A field
// the literal leaves out takes its default value.
func (in *interp) composite(fr *frame, x *syntax.CompositeLit) (Value, *abrupt) {
	if fr.info.Works[x] != nil {
		return nil, fr.unsupported(x, "a Work literal")
	}
	t := fr.rtypeOf(x)
	if check.RefinementOf(t.t) != nil {
		return nil, fr.unsupported(x, "a literal of the refined type %s, whose where runs at the literal", t.t)
	}
	switch u := t.under().t.(type) {
	case *check.Slice:
		return in.listLit(fr, x, 0, false)
	case *check.Array:
		if len(x.Elts) == 0 {
			return in.zero(fr, x, t)
		}
		return in.listLit(fr, x, int(u.Len), true)
	case *check.Map:
		m := newMap()
		if ab := in.build(fr, x, int64(len(x.Elts)), 2*int64(len(x.Elts)), 0); ab != nil {
			return nil, ab
		}
		for _, e := range x.Elts {
			kv, ok := e.(*syntax.KeyValueExpr)
			if !ok {
				ice("a Map literal entry with no key")
			}
			k, ab := in.expr(fr, kv.Key)
			if ab != nil {
				return nil, ab
			}
			if !basicValue(k) {
				return nil, fr.unsupported(kv.Key, "a Map key of %s yet", fr.rtypeOf(kv.Key).t)
			}
			v, ab := in.expr(fr, kv.Value)
			if ab != nil {
				return nil, ab
			}
			m.b.m[k] = v
		}
		return m, nil
	}
	n, ok := t.t.(*check.Named)
	if !ok || structOf(n) == nil || check.ScriptOf(n) != nil || check.IsGoError(n) {
		return nil, fr.unsupported(x, "a literal of %s yet", t.t)
	}
	s := structOf(n)
	out := &record{t: n, key: argsKey(n, t.env), fields: make([]Value, len(s.Fields))}
	set := make([]bool, len(s.Fields))
	var base *record
	if x.Base != nil {
		b, ab := in.expr(fr, x.Base)
		if ab != nil {
			return nil, ab
		}
		if base, ok = b.(*record); !ok {
			ice("a record update over %T", b)
		}
	}
	if ab := in.charge(fr, x, int64(len(s.Fields))); ab != nil {
		return nil, ab
	}
	for i, e := range x.Elts {
		idx := i
		val := e
		if kv, ok := e.(*syntax.KeyValueExpr); ok {
			id, ok := kv.Key.(*syntax.Ident)
			if !ok {
				ice("a record literal key that is not a field name")
			}
			idx, val = fieldIndex(n, id.Name), kv.Value
		}
		if idx >= len(s.Fields) {
			ice("a record literal with more values than fields")
		}
		v, ab := in.expr(fr, val)
		if ab != nil {
			return nil, ab
		}
		out.fields[idx], set[idx] = v, true
	}
	if base != nil {
		if copies, spread := fr.info.Spreads[x]; spread {
			for _, c := range copies {
				i := fieldIndex(n, c.Field.Name)
				out.fields[i], set[i] = base.fields[fieldIndex(base.t, c.From.Name)], true
			}
		} else {
			for i := range out.fields {
				if !set[i] {
					out.fields[i], set[i] = base.fields[i], true
				}
			}
		}
	}
	for i, f := range s.Fields {
		if set[i] {
			continue
		}
		z, ab := in.zero(fr, x, rtype{f.Type, t.env})
		if ab != nil {
			return nil, ab
		}
		out.fields[i] = z
	}
	return out, nil
}

// listLit builds a slice literal, or an array literal that lists all its
// elements.
func (in *interp) listLit(fr *frame, x *syntax.CompositeLit, n int, array bool) (Value, *abrupt) {
	if ab := in.build(fr, x, int64(len(x.Elts)), int64(len(x.Elts)), 0); ab != nil {
		return nil, ab
	}
	elems := make([]Value, 0, len(x.Elts))
	for _, e := range x.Elts {
		if _, keyed := e.(*syntax.KeyValueExpr); keyed {
			ice("a keyed slice or array literal")
		}
		v, ab := in.expr(fr, e)
		if ab != nil {
			return nil, ab
		}
		elems = append(elems, v)
	}
	if array && len(elems) != n {
		ice("an array literal of %d elements for %d", len(elems), n)
	}
	s := newSlice(elems)
	s.array = array
	return s, nil
}

// zero is the default of t, its size charged before it is built, so a
// huge [N]T exhausts the budget instead of the host's memory.
func (in *interp) zero(fr *frame, at syntax.Node, t rtype) (Value, *abrupt) {
	size, ok := zeroSize(t, in.budget.Steps+1)
	if !ok {
		return nil, fr.unsupported(at, "the default value of %s yet", t.t)
	}
	if ab := in.build(fr, at, size, size, 0); ab != nil {
		return nil, ab
	}
	z, ok := zeroOf(t)
	if !ok {
		return nil, fr.unsupported(at, "the default value of %s yet", t.t)
	}
	return z, nil
}

// try passes a failure of x out of the frame; on a catch it makes the
// catch partial.
func (in *interp) try(fr *frame, x *syntax.TryExpr) (Value, *abrupt) {
	if c, ok := unparen(x.X).(*syntax.CatchExpr); ok {
		return in.catch(fr, c, true)
	}
	v, ab := in.expr(fr, x.X)
	if ab != nil && ab.kind == ctlFail {
		out := *ab
		out.kind = ctlEscape
		return nil, &out
	}
	return v, ab
}

// catch handles the typed failure of its subject with the first arm that
// takes it (rows.md, Catch). A failure no arm takes leaves the frame under
// try, and is a defect in a total catch (Catch 6). A defect, an exhausted
// budget and a failure already leaving the frame are never caught.
func (in *interp) catch(fr *frame, x *syntax.CatchExpr, partial bool) (Value, *abrupt) {
	m := fr.info.Matches[x]
	if m == nil {
		ice("a catch with no Matches entry")
	}
	v, failed := in.expr(fr, x.X)
	if failed == nil {
		return v, nil
	}
	if failed.kind != ctlFail {
		return nil, failed
	}
	for _, a := range x.Arms {
		for _, te := range a.Types {
			p := fr.info.Patterns[te]
			if p == nil {
				ice("a catch arm with no Patterns entry")
			}
			if ab := in.charge(fr, te, 1); ab != nil {
				return nil, ab
			}
			ok, ab := in.match(fr, te, p, failed.val)
			if ab != nil {
				return nil, ab
			}
			if ok {
				val, ab := in.yield(fr, a.Body, false)
				if ab != nil {
					return nil, ab
				}
				if val == nil {
					val = unit{}
				}
				return val, nil
			}
		}
	}
	if partial {
		out := *failed
		out.kind = ctlEscape
		return nil, &out
	}
	return nil, fr.defect(x, "a failure no arm of the catch takes: %s", describe(failed.val))
}

// callExpr runs a call: a conversion, len, a declared function with a
// body, a prelude collection function, or a function literal called where
// it is written. Every other callee waits for a later unit or never runs
// at check time.
func (in *interp) callExpr(fr *frame, x *syntax.CallExpr) (Value, *abrupt) {
	c := fr.info.Calls[x]
	if c == nil {
		ice("a call with no Calls entry")
	}
	if check.IsUnreachable(c) {
		return nil, fr.defect(x, "unreachable")
	}
	switch c.Kind {
	case check.CallConversion:
		if len(x.Args) != 1 {
			ice("a conversion of %d values", len(x.Args))
		}
		v, ab := in.expr(fr, x.Args[0])
		if ab != nil {
			return nil, ab
		}
		return in.convert(fr, x, v, fr.rtypeOf(x.Args[0]), fr.rt(c.Type))
	case check.CallLen:
		v, ab := in.expr(fr, x.Args[0])
		if ab != nil {
			return nil, ab
		}
		switch v := v.(type) {
		case string:
			return int64(len(v)), nil
		case sliceV:
			return int64(v.n), nil
		case mapV:
			return int64(len(v.b.m)), nil
		}
		ice("len of %T", v)
	case check.CallFunc:
		if c.Site != nil {
			return nil, fr.unsupported(x, "a call that passes its CallSite yet")
		}
		if c.Func.Ptr {
			return nil, fr.unsupported(x, "a call that borrows its receiver yet")
		}
		if x.Trailing != nil {
			return nil, fr.unsupported(x, "a trailing block yet")
		}
		if preludeCollection(c.Func) {
			return in.collectionCall(fr, x, c, nil)
		}
		args, ab := in.args(fr, x, c)
		if ab != nil {
			return nil, ab
		}
		return in.call(fr, x, c.Func, c.TypeArgs, args)
	case check.CallFuncValue:
		lit, ok := unparen(x.Fun).(*syntax.FuncLit)
		if !ok {
			return nil, fr.unsupported(x, "a call of a function value yet")
		}
		if x.Trailing != nil {
			return nil, fr.unsupported(x, "a trailing block yet")
		}
		args, ab := in.args(fr, x, c)
		if ab != nil {
			return nil, ab
		}
		return in.callLiteral(fr, x, lit, args)
	case check.CallWith, check.CallProviderOp:
		return nil, fr.unsupported(x, "a with or a capability, which check-time evaluation never runs")
	case check.CallOp, check.CallLawOp:
		return in.operationCall(fr, x, c)
	}
	return nil, fr.unsupported(x, "this call (kind %d)", c.Kind)
}

// args evaluates a call's receiver and arguments, left to right.
func (in *interp) args(fr *frame, x *syntax.CallExpr, c *check.Call) ([]Value, *abrupt) {
	var args []Value
	if c.Recv != nil {
		v, ab := in.expr(fr, c.Recv)
		if ab != nil {
			return nil, ab
		}
		args = append(args, v)
	}
	for _, a := range x.Args {
		v, ab := in.expr(fr, a)
		if ab != nil {
			return nil, ab
		}
		args = append(args, v)
	}
	return args, nil
}

// callLiteral runs a function literal called where it is written: its
// body reads and writes the slots of the frame around it, in place.
func (in *interp) callLiteral(fr *frame, at *syntax.CallExpr, lit *syntax.FuncLit, args []Value) (Value, *abrupt) {
	callee := &frame{pkg: fr.pkg, info: fr.info, decl: fr.decl, vars: fr.vars, env: fr.env, owner: fr.owner, lawOwn: fr.lawOwn}
	var params []*check.Var
	for _, field := range lit.Type.Params.List {
		if len(field.Names) == 0 {
			params = append(params, nil)
		}
		for _, id := range field.Names {
			v, _ := fr.info.Defs[id].(*check.Var)
			params = append(params, v)
		}
	}
	if len(params) != len(args) {
		ice("a function literal of %d parameters called with %d", len(params), len(args))
	}
	for i, p := range params {
		callee.bind(p, args[i])
	}
	return in.runFrame(fr, at, callee, "literal", lit.Body.List)
}

// preludeCollection reports the prelude's Append, Get, Delete or Put,
// which the evaluator runs as value operations, never by their Go bodies
// (plan section 4: pure prelude collection operations).
func preludeCollection(f *check.Func) bool {
	if f == nil || f.Pkg == nil || f.Pkg.Path != std.PreludePath || f.Decl == nil || f.Decl.Extern == nil {
		return false
	}
	switch f.Name {
	case "Append", "Get", "Delete", "Put":
		return true
	}
	return false
}

// collectionCall runs xs.Append(x), m.Get(k), m.Delete(k) or m.Put(k, v).
// own is the token of the slot an assignment hands the receiver over from
// (x = x.Append(e)); the receiver is then read without a lend.
func (in *interp) collectionCall(fr *frame, x *syntax.CallExpr, c *check.Call, own *slot) (Value, *abrupt) {
	var recv Value
	var ab *abrupt
	if own != nil {
		if ab := in.charge(fr, c.Recv, 1); ab != nil {
			return nil, ab
		}
		recv = own.v
	} else if recv, ab = in.expr(fr, c.Recv); ab != nil {
		return nil, ab
	}
	args := make([]Value, len(x.Args))
	for i, a := range x.Args {
		if args[i], ab = in.expr(fr, a); ab != nil {
			return nil, ab
		}
	}
	var tok *token
	if own != nil {
		tok = own.own()
	}
	switch c.Func.Name {
	case "Append":
		return in.appendTo(fr, x, recv.(sliceV), args[0], tok)
	case "Get":
		if !basicValue(args[0]) {
			return nil, fr.unsupported(x.Args[0], "a Map key of %s yet", fr.rtypeOf(x.Args[0]).t)
		}
		if ab := in.charge(fr, x, 1); ab != nil {
			return nil, ab
		}
		v, found := recv.(mapV).b.m[args[0]]
		return in.option(fr, x, v, found)
	case "Delete":
		if !basicValue(args[0]) {
			return nil, fr.unsupported(x.Args[0], "a Map key of %s yet", fr.rtypeOf(x.Args[0]).t)
		}
		return in.deleteIn(fr, x, recv.(mapV), args[0], tok)
	case "Put":
		if !basicValue(args[0]) {
			return nil, fr.unsupported(x.Args[0], "a Map key of %s yet", fr.rtypeOf(x.Args[0]).t)
		}
		return in.putIn(fr, x, recv.(mapV), args[0], args[1], tok)
	}
	ice("the prelude function %s", c.Func.Name)
	return nil, nil
}

// option is Some{v} or None of the call's Option type.
func (in *interp) option(fr *frame, x *syntax.CallExpr, v Value, found bool) (Value, *abrupt) {
	t := fr.rtypeOf(x)
	u, ok := t.under().t.(*check.Union)
	if !ok {
		ice("Get's result is %s, no Option", t.t)
	}
	want := "None"
	if found {
		want = "Some"
	}
	for _, vt := range u.Variants {
		n, ok := vt.(*check.Named)
		if !ok || n.Obj.Name != want {
			continue
		}
		r := &record{t: n, key: argsKey(n, t.under().env)}
		if found {
			r.fields = []Value{v}
		}
		return r, nil
	}
	ice("the Option %s has no %s", t.t, want)
	return nil, nil
}

// convert is T(v) from the type from: a basic conversion as Go's, between
// a defined type and the type it is defined over, or a record to another
// record type over the same fields.
func (in *interp) convert(fr *frame, x *syntax.CallExpr, v Value, from, to rtype) (Value, *abrupt) {
	// Refined conversions are admitted only for checked constants or inside
	// the derived parser, whose ordinary predicate runs before this cast.
	if b := to.basic(); b != nil {
		if from.basic() == nil {
			return nil, fr.unsupported(x, "a conversion from %s yet", from.t)
		}
		out, ok := convertBasic(v, b.Kind)
		if !ok {
			return nil, fr.unsupported(x, "the conversion of %s to %s, which the Go target leaves to the host or which needs a later unit", from.t, to.t)
		}
		return out, nil
	}
	switch v := v.(type) {
	case *record:
		if n, isNamed := to.t.(*check.Named); isNamed && structOf(n) != nil {
			return &record{t: n, key: argsKey(n, to.env), fields: v.fields}, nil
		}
	case sliceV:
		if _, isArray := to.under().t.(*check.Array); isArray == v.array {
			return v, nil
		}
	case mapV, tuple:
		return v, nil
	}
	return nil, fr.unsupported(x, "a conversion to %s yet", to.t)
}

// interpolated builds "a \(x) b": each value's text as the lowering writes
// it (lower/interp.go).
func (in *interp) interpolated(fr *frame, x *syntax.InterpolatedString) (Value, *abrupt) {
	if len(x.Texts) != len(x.Exprs)+1 {
		ice("an interpolated string with %d segments for %d values", len(x.Texts), len(x.Exprs))
	}
	var b strings.Builder
	for i, seg := range x.Texts {
		raw := seg.Value
		if len(raw) < 2 {
			ice("an interpolated string segment %q", raw)
		}
		raw = raw[1:]
		if i == len(x.Texts)-1 {
			raw = raw[:len(raw)-1]
		} else {
			raw = strings.TrimSuffix(raw, `\(`)
		}
		s, err := strconv.Unquote(`"` + raw + `"`)
		if err != nil {
			ice("an interpolated segment %q: %v", seg.Value, err)
		}
		b.WriteString(s)
		if i == len(x.Exprs) {
			break
		}
		v, ab := in.expr(fr, x.Exprs[i])
		if ab != nil {
			return nil, ab
		}
		text, ok := textOf(v, fr.rtypeOf(x.Exprs[i]).basic())
		if !ok {
			return nil, fr.unsupported(x.Exprs[i], "the text of %s yet", fr.typeOf(x.Exprs[i]))
		}
		b.WriteString(text)
	}
	if ab := in.build(fr, x, int64(b.Len()), 0, int64(b.Len())); ab != nil {
		return nil, ab
	}
	return b.String(), nil
}

// textOf is the text of a basic value in an interpolation.
func textOf(v Value, b *check.Basic) (string, bool) {
	if b == nil {
		return "", false
	}
	if b.Kind == check.Duration {
		return time.Duration(v.(int64)).String(), true
	}
	switch v := v.(type) {
	case string:
		return v, true
	case bool:
		return strconv.FormatBool(v), true
	case int8:
		return strconv.FormatInt(int64(v), 10), true
	case int16:
		return strconv.FormatInt(int64(v), 10), true
	case int32:
		return strconv.FormatInt(int64(v), 10), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case uint8:
		return strconv.FormatUint(uint64(v), 10), true
	case uint16:
		return strconv.FormatUint(uint64(v), 10), true
	case uint32:
		return strconv.FormatUint(uint64(v), 10), true
	case uint64:
		return strconv.FormatUint(v, 10), true
	case float32:
		return strconv.FormatFloat(float64(v), 'g', -1, 32), true
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), true
	}
	return "", false
}
