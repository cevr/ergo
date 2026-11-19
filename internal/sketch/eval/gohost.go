package eval

import (
	"net/netip"
	"net/url"
	"reflect"
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Go values in the evaluator (NP4f-URL). A std body or a program reaches
// Go's own net/url and net/netip through import go, in the pure tier: the
// evaluator calls the same Go functions and methods, as goNatives does for
// basic values, and holds what they return as Go holds it. A go.Ref (url.
// Parse's *URL) and a Go struct with no Ergo record form (netip.Addr) are
// a goVal: a field read through one is Go's field, a method call is Go's
// method; go.Zero[T]() is T's zero record and go.New(v) the Go T the
// record v spells. A Go error is the failure go.Error{Text: err.Error()}.
// Only the functions of goFuncs and the methods of the types of goTypes
// run; any other Go call stays a skip.

// goVal is a Go value held as Go holds it.
type goVal struct{ v reflect.Value }

// goFuncs are the Go functions the evaluator calls by reflection, by Go
// path and name.
var goFuncs = map[string]any{
	"net/url.Parse":       url.Parse,
	"net/url.ParseQuery":  url.ParseQuery,
	"net/netip.ParseAddr": netip.ParseAddr,
	"strconv.ParseUint":   strconv.ParseUint,
	"strconv.FormatUint":  strconv.FormatUint,
}

// goTypes are the Go types whose methods the evaluator calls and whose
// values go.New makes, by Go path and name.
var goTypes = map[string]reflect.Type{
	"net/url.URL":      reflect.TypeFor[url.URL](),
	"net/url.Userinfo": reflect.TypeFor[url.Userinfo](),
	"net/netip.Addr":   reflect.TypeFor[netip.Addr](),
}

// goReflectNative is the native of fn when it is a function of goFuncs or
// a method of a type of goTypes; nil otherwise.
func goReflectNative(fn *check.Func) native {
	if fn.Decl.Recv != nil {
		path, typ, name := fn.Decl.Extern.SplitMethod()
		if _, ok := goTypes[path+"."+typ]; !ok {
			return nil
		}
		return func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			recv, ok := args[0].(goVal)
			if !ok {
				return nil, fr.unsupported(at, "the Go method %s on %s", fn.Name, describe(args[0]))
			}
			m := recv.v.MethodByName(name)
			if !m.IsValid() {
				return nil, fr.unsupported(at, "the Go method %s.%s", typ, name)
			}
			return in.goCall(fr, at, fn, m, args[1:])
		}
	}
	path, name := fn.Decl.Extern.Split()
	f, ok := goFuncs[path+"."+name]
	if !ok {
		return nil
	}
	fv := reflect.ValueOf(f)
	return func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return in.goCall(fr, at, fn, fv, args)
	}
}

// goCall calls Go's f on args and gives its results as fn's, or the
// go.Error of its last error result.
func (in *interp) goCall(fr *frame, at syntax.Node, fn *check.Func, f reflect.Value, args []Value) (Value, *abrupt) {
	if ab := in.charge(fr, at, 1+argWork(args)); ab != nil {
		return nil, ab
	}
	ft := f.Type()
	if ft.IsVariadic() || ft.NumIn() != len(args) {
		return nil, fr.unsupported(at, "a Go call of %s with %d arguments", fn.Name, len(args))
	}
	ins := make([]reflect.Value, len(args))
	for i, a := range args {
		v, ok := toGo(a, ft.In(i))
		if !ok {
			return nil, fr.unsupported(at, "an argument %s of the Go call %s", describe(a), fn.Name)
		}
		ins[i] = v
	}
	var outs []reflect.Value
	if _, why := guard(func() Value { outs = f.Call(ins); return nil }); why != "" {
		return nil, fr.defect(at, "%s", why)
	}
	if n := len(outs); n > 0 && ft.Out(n-1) == reflect.TypeFor[error]() {
		if err, _ := outs[n-1].Interface().(error); err != nil {
			for _, e := range fn.Sig.Errors.Elems() {
				if g, ok := e.(*check.Named); ok && check.IsGoError(g) {
					return nil, fr.abrupt(ctlFail, at, &record{t: g, fields: []Value{err.Error()}}, "")
				}
			}
			return nil, fr.unsupported(at, "the error of %s, whose row has no go.Error", fn.Name)
		}
		outs = outs[:n-1]
	}
	results := fn.Sig.Results
	if len(results) != len(outs) {
		return nil, fr.unsupported(at, "the %d results of the Go call %s", len(outs), fn.Name)
	}
	vals := make([]Value, len(outs))
	for i, o := range outs {
		v, ok := fromGo(o, fr.rt(results[i]))
		if !ok {
			return nil, fr.unsupported(at, "a result of the Go call %s", fn.Name)
		}
		if s, isText := v.(string); isText {
			if ab := in.allocate(fr, at, int64(len(s))); ab != nil {
				return nil, ab
			}
		}
		vals[i] = v
	}
	switch len(vals) {
	case 0:
		return unit{}, nil
	case 1:
		return vals[0], nil
	}
	return tuple{elems: vals}, nil
}

// toGo is the Go value of v as Go's type t takes it.
func toGo(v Value, t reflect.Type) (reflect.Value, bool) {
	switch v := v.(type) {
	case goVal:
		if v.v.Type().AssignableTo(t) {
			return v.v, true
		}
		return reflect.Value{}, false
	case *record:
		if t.Kind() != reflect.Struct {
			return reflect.Value{}, false
		}
		out := reflect.New(t).Elem()
		s := structOf(v.t)
		if s == nil {
			return reflect.Value{}, false
		}
		for i, f := range s.Fields {
			fv := out.FieldByName(f.Name)
			if !fv.IsValid() || !fv.CanSet() {
				return reflect.Value{}, false
			}
			g, ok := toGoField(v.fields[i], fv.Type())
			if !ok {
				return reflect.Value{}, false
			}
			fv.Set(g)
		}
		return out, true
	}
	rv := reflect.ValueOf(v)
	switch {
	case !rv.IsValid():
		return reflect.Value{}, false
	case rv.Kind() == reflect.String && t.Kind() == reflect.String,
		rv.Kind() == reflect.Bool && t.Kind() == reflect.Bool:
		return rv.Convert(t), true
	case rv.CanInt() || rv.CanUint() || rv.CanFloat():
		if t.Kind() >= reflect.Int && t.Kind() <= reflect.Float64 {
			return rv.Convert(t), true
		}
	}
	return reflect.Value{}, false
}

// toGoField is a struct field's Go value: an Option of a go.Ref is Go's
// pointer, nil for None. A record where Go holds a pointer is that Option
// (the checker's Go boundary admits no other): its variant with no field
// is nil, and the one with a field is the pointer it holds.
func toGoField(v Value, t reflect.Type) (reflect.Value, bool) {
	if r, ok := v.(*record); ok && t.Kind() == reflect.Pointer {
		switch len(r.fields) {
		case 0:
			return reflect.Zero(t), true
		case 1:
			return toGo(r.fields[0], t)
		}
	}
	return toGo(v, t)
}

// fromGo is Go's v as a value of the Ergo type t: a basic value, an
// Option of a Go pointer (None for nil), or a goVal.
func fromGo(v reflect.Value, t rtype) (Value, bool) {
	if b := t.basic(); b != nil {
		switch v.Kind() {
		case reflect.String:
			return v.String(), true
		case reflect.Bool:
			return v.Bool(), true
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return fromInt64(b.Kind, v.Int())
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			return fromUint64(b.Kind, v.Uint())
		case reflect.Float32, reflect.Float64:
			if b.Kind == check.Float32 {
				return float32(v.Float()), true
			}
			return v.Float(), true
		}
		return nil, false
	}
	if _, ok := optionElem(t); ok && v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return optionOf(t, nil, false), true
		}
		return optionOf(t, goVal{v}, true), true
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return nil, false
		}
		return goVal{v}, true
	case reflect.Struct:
		return goVal{v}, true
	case reflect.Map, reflect.Slice:
		return fromGoText(v)
	}
	return nil, false
}

// fromGoText is a Go Map of strings to string slices (url.Values), or a
// string slice, as the Ergo value: a Map or slice the evaluator holds.
func fromGoText(v reflect.Value) (Value, bool) {
	switch {
	case v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.String:
		out := make([]Value, v.Len())
		for i := range out {
			out[i] = v.Index(i).String()
		}
		return newSlice(out), true
	case v.Kind() == reflect.Map && v.Type().Key().Kind() == reflect.String:
		m := newMap()
		it := v.MapRange()
		for it.Next() {
			e, ok := fromGoText(it.Value())
			if !ok {
				return nil, false
			}
			m.b.m[it.Key().String()] = e
		}
		return m, true
	}
	return nil, false
}

// goField is the field name of the Go value g, as a value of the Ergo
// type t.
func goField(g goVal, name string, t rtype) (Value, bool) {
	v := g.v
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	f := v.FieldByName(name)
	if !f.IsValid() {
		return nil, false
	}
	return fromGo(f, t)
}

// goZero is Go's zero of the Go struct t as its record: each field Go's
// zero, a pointer field (an Option) None.
func goZero(t rtype) (Value, bool) {
	n, ok := t.t.(*check.Named)
	s := structOf(n)
	if !ok || s == nil {
		return nil, false
	}
	rec := &record{t: n, fields: make([]Value, len(s.Fields))}
	for i, f := range s.Fields {
		ft := rtype{t: f.Type}
		if _, isOption := optionElem(ft); isOption {
			rec.fields[i] = optionOf(ft, nil, false)
			continue
		}
		z, ok := zeroOf(ft)
		if !ok {
			return nil, false
		}
		rec.fields[i] = z
	}
	return rec, true
}

// goBuiltin runs go.Zero[T]() and go.New(v) of a type of goTypes.
func (in *interp) goBuiltin(fr *frame, x *syntax.CallExpr, c *check.Call) (Value, *abrupt) {
	t := fr.rtypeOf(x)
	switch {
	case c.Name == "go.Zero" && len(x.Args) == 0:
		if ab := in.charge(fr, x, 1); ab != nil {
			return nil, ab
		}
		z, ok := goZero(t)
		if !ok {
			return nil, fr.unsupported(x, "go.Zero of %s", t.t)
		}
		return z, nil
	case c.Name == "go.New" && len(x.Args) == 1:
		v, ab := in.expr(fr, x.Args[0])
		if ab != nil {
			return nil, ab
		}
		r, ok := v.(*record)
		if !ok {
			return nil, fr.unsupported(x, "go.New of %s", describe(v))
		}
		gt, ok := goTypes[r.t.Obj.Pkg.Path+"."+r.t.Obj.Name]
		if !ok {
			return nil, fr.unsupported(x, "go.New of the Go type %s", r.t)
		}
		g, ok := toGo(r, gt)
		if !ok {
			return nil, fr.unsupported(x, "go.New of %s", describe(v))
		}
		if ab := in.charge(fr, x, 1+int64(len(r.fields))); ab != nil {
			return nil, ab
		}
		p := reflect.New(gt)
		p.Elem().Set(g)
		return goVal{p}, nil
	}
	return nil, fr.unsupported(x, "%s", c.Name)
}
