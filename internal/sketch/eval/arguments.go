package eval

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketch/testplan"
)

// argument is the type-directed replay literal, not describe's diagnostic text.
// Rendering is metered too: report construction cannot bypass the work budget.
func (in *interp) argument(fr *frame, t check.Type, v Value) (out string) {
	mustCharge(in, fr, 1)
	defer func() { mustCharge(in, fr, int64(len(out))) }()
	switch t := t.(type) {
	case *check.Basic:
		floatText := func(f float64, bits int) string {
			s := strconv.FormatFloat(f, 'g', -1, bits)
			if !strings.ContainsAny(s, ".eEnN") {
				s += ".0"
			}
			return s
		}
		switch v := v.(type) {
		case string:
			return strconv.Quote(v)
		case float32:
			return floatText(float64(v), 32)
		case float64:
			return floatText(v, 64)
		case complex64:
			return "complex(" + floatText(float64(real(v)), 32) + ", " + floatText(float64(imag(v)), 32) + ")"
		case complex128:
			return "complex(" + floatText(real(v), 64) + ", " + floatText(imag(v), 64) + ")"
		case int64:
			if t.Kind == check.Duration {
				if v == -1<<63 {
					return "-9223372036854775807ns - 1ns"
				}
				for _, u := range []struct {
					s string
					n int64
				}{{"h", 3600e9}, {"m", 60e9}, {"s", 1e9}, {"ms", 1e6}, {"us", 1e3}} {
					if v != 0 && v%u.n == 0 {
						return strconv.FormatInt(v/u.n, 10) + u.s
					}
				}
				return strconv.FormatInt(v, 10) + "ns"
			}
		}
		return fmt.Sprint(v)
	case *check.Named:
		switch under := t.Underlying().(type) {
		case *check.Struct:
			if ctor := testplan.Constructor(t); ctor != nil && len(ctor.Sig.Params) == 0 {
				return ctor.Name + "()"
			}
			r := v.(*record)
			if len(r.fields) == 0 {
				return in.argumentType(t)
			}
			parts := make([]string, len(r.fields))
			for i, e := range r.fields {
				parts[i] = under.Fields[i].Name + ": " + in.argument(fr, under.Fields[i].Type, e)
			}
			return in.argumentType(t) + "{" + strings.Join(parts, ", ") + "}"
		case *check.Union:
			r := v.(*record)
			return in.argument(fr, r.t, v)
		default:
			return in.argumentType(t) + "(" + in.argument(fr, under, v) + ")"
		}
	case *check.Tuple:
		xs := v.(tuple).elems
		parts := make([]string, len(xs))
		for i, e := range xs {
			parts[i] = in.argument(fr, t.Types[i], e)
		}
		return "(" + strings.Join(parts, ", ") + ")"
	case *check.Slice:
		xs := v.(sliceV).elems()
		parts := make([]string, len(xs))
		for i, e := range xs {
			parts[i] = in.argument(fr, t.Elem, e)
		}
		return in.argumentType(t) + "{" + strings.Join(parts, ", ") + "}"
	case *check.Map:
		m := v.(mapV).b.m
		mustCharge(in, fr, int64(len(m)))
		keys := make([]Value, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		b, basic := t.Key.(*check.Basic)
		sort.Slice(keys, func(i, j int) bool {
			mustCharge(in, fr, 1)
			if basic && b.Kind != check.Bool && b.Kind != check.Complex64 && b.Kind != check.Complex128 {
				return truth(mustValue(in.binop(fr, fr.decl, syntax.LSS, keys[i], keys[j])))
			}
			return in.argument(fr, t.Key, keys[i]) < in.argument(fr, t.Key, keys[j])
		})
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = in.argument(fr, t.Key, k) + ": " + in.argument(fr, t.Elem, m[k])
		}
		return in.argumentType(t) + "{" + strings.Join(parts, ", ") + "}"
	case *check.Union:
		r := v.(*record)
		return in.argument(fr, r.t, v)
	}
	return describe(v)
}

func (in *interp) argumentType(t check.Type) string {
	switch t := t.(type) {
	case *check.Named:
		n := t.Obj.Name
		if p := t.Obj.Pkg; p != nil && p != in.root && p.Path != "prelude" {
			n = p.Name + "." + n
		}
		if len(t.TArgs) > 0 {
			var as []string
			for _, a := range t.TArgs {
				as = append(as, in.argumentType(a))
			}
			n += "[" + strings.Join(as, ", ") + "]"
		}
		return n
	case *check.Slice:
		return "[]" + in.argumentType(t.Elem)
	case *check.Map:
		return "Map[" + in.argumentType(t.Key) + ", " + in.argumentType(t.Elem) + "]"
	}
	return t.String()
}
