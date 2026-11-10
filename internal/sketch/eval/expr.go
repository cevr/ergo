package eval

import (
	"strconv"
	"strings"
	"time"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// expr evaluates x. Each evaluated node costs a step before its work.
func (in *interp) expr(fr *frame, x syntax.Expr) (Value, *abrupt) {
	if ab := in.charge(fr, x, 1); ab != nil {
		return nil, ab
	}
	v, ab := in.exprOf(fr, x)
	if ab != nil {
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
		v, ok := fromConstant(fr.typeOf(x), c)
		if !ok {
			return nil, fr.unsupported(x, "a constant of %s yet", fr.typeOf(x))
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
	case *syntax.SelectorExpr:
		return in.selector(fr, x)
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
		return nil, fr.unsupported(x, "a function value yet")
	case *syntax.IndexExpr:
		return nil, fr.unsupported(x, "an index or an instantiation yet")
	case *syntax.SliceExpr:
		return nil, fr.unsupported(x, "a slice expression yet")
	case *syntax.StarExpr:
		return nil, fr.unsupported(x, "a pointer")
	}
	return nil, fr.unsupported(x, "the expression form %T", x)
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
	n, ok := fr.typeOf(x).(*check.Named)
	if !ok || structOf(n) == nil || len(structOf(n).Fields) != 0 {
		ice("a bare value of %s is not a record with no fields", tn.Name)
	}
	return &record{t: n}, true
}

func (in *interp) ident(fr *frame, id *syntax.Ident) (Value, *abrupt) {
	switch o := fr.info.Uses[id].(type) {
	case *check.Var:
		if v, ok := fr.vars[o]; ok {
			return v, nil
		}
		return nil, fr.unsupported(id, "a package let (%s) yet", id.Name)
	case *check.Const:
		return o.Val, nil
	case *check.Func:
		return nil, fr.unsupported(id, "a function value (%s) yet", id.Name)
	case nil:
		ice("the identifier %s has no Uses entry", id.Name)
	}
	return nil, fr.unsupported(id, "the name %s here", id.Name)
}

// selector reads a record's field; a package's let or function and an
// operation value are not run yet.
func (in *interp) selector(fr *frame, x *syntax.SelectorExpr) (Value, *abrupt) {
	if id, ok := unparen(x.X).(*syntax.Ident); ok {
		if _, isPkg := fr.info.Uses[id].(*check.PkgName); isPkg {
			return nil, fr.unsupported(x, "a package member (%s.%s) as a value yet", id.Name, x.Sel.Name)
		}
	}
	t := fr.typeOf(x.X)
	n, ok := t.(*check.Named)
	if !ok {
		return nil, fr.unsupported(x, "a field of %s yet", t)
	}
	if check.ScriptOf(n) != nil || structOf(n) == nil {
		return nil, fr.unsupported(x, "a member of %s yet", t)
	}
	base, ab := in.expr(fr, x.X)
	if ab != nil {
		return nil, ab
	}
	r, ok := base.(*record)
	if !ok {
		ice("a field of %T", base)
	}
	return r.fields[fieldIndex(r.t, x.Sel.Name)], nil
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
	case syntax.DOTDOT:
		return nil, fr.unsupported(x, "a range yet")
	}
	a, ab := in.expr(fr, x.X)
	if ab != nil {
		return nil, ab
	}
	b, ab := in.expr(fr, x.Y)
	if ab != nil {
		return nil, ab
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

// composite builds a record literal, an update `T{..b, F: v}` or a spread
// from a record of another type. A field the literal leaves out takes its
// zero value. Collections wait for EV2.
func (in *interp) composite(fr *frame, x *syntax.CompositeLit) (Value, *abrupt) {
	if fr.info.Works[x] != nil {
		return nil, fr.unsupported(x, "a Work literal")
	}
	t := fr.typeOf(x)
	n, ok := t.(*check.Named)
	if !ok || structOf(n) == nil || check.ScriptOf(n) != nil || check.IsGoError(n) {
		return nil, fr.unsupported(x, "a literal of %s yet", t)
	}
	s := structOf(n)
	out := &record{t: n, fields: make([]Value, len(s.Fields))}
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
		z, ok := zero(f.Type)
		if !ok {
			return nil, fr.unsupported(x, "the default value of the field %s yet", f.Name)
		}
		out.fields[i] = z
	}
	return out, nil
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
			if in.match(fr, p, failed.val) {
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

// callExpr runs a call: a conversion, len, or a declared function with a
// body. Every other callee waits for a later unit or never runs at check
// time.
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
		return in.convert(fr, x, v, fr.typeOf(x.Args[0]), c.Type)
	case check.CallLen:
		v, ab := in.expr(fr, x.Args[0])
		if ab != nil {
			return nil, ab
		}
		s, ok := v.(string)
		if !ok {
			return nil, fr.unsupported(x, "len of a collection yet")
		}
		return int64(len(s)), nil
	case check.CallFunc:
		if c.Site != nil {
			return nil, fr.unsupported(x, "a call that passes its CallSite yet")
		}
		if c.Func.Ptr {
			return nil, fr.unsupported(x, "a call that borrows its receiver yet")
		}
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
		if x.Trailing != nil {
			return nil, fr.unsupported(x, "a trailing block yet")
		}
		return in.call(fr, x, c.Func, args)
	case check.CallWith, check.CallProviderOp:
		return nil, fr.unsupported(x, "a with or a capability, which check-time evaluation never runs")
	case check.CallFuncValue, check.CallOp, check.CallLawOp:
		return nil, fr.unsupported(x, "a call of a function value or an operation yet")
	}
	return nil, fr.unsupported(x, "this call (kind %d)", c.Kind)
}

// convert is T(v) from the type from: a basic conversion as Go's, or a
// record to another record type over the same fields.
func (in *interp) convert(fr *frame, x *syntax.CallExpr, v Value, from, to check.Type) (Value, *abrupt) {
	if b := basicOf(to); b != nil {
		if fb := basicOf(from); fb == nil {
			return nil, fr.unsupported(x, "a conversion from %s yet", from)
		}
		out, ok := convertBasic(v, b.Kind)
		if !ok {
			return nil, fr.unsupported(x, "the conversion of %s to %s, which the Go target leaves to the host or which needs a later unit", from, to)
		}
		return out, nil
	}
	if r, ok := v.(*record); ok {
		if n, isNamed := to.(*check.Named); isNamed && structOf(n) != nil {
			return &record{t: n, fields: r.fields}, nil
		}
	}
	return nil, fr.unsupported(x, "a conversion to %s yet", to)
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
		text, ok := textOf(v, basicOf(fr.typeOf(x.Exprs[i])))
		if !ok {
			return nil, fr.unsupported(x.Exprs[i], "the text of %s yet", fr.typeOf(x.Exprs[i]))
		}
		b.WriteString(text)
	}
	if ab := in.charge(fr, x, int64(b.Len())); ab != nil {
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
