package eval

import (
	"go/constant"
	"math"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Fold computes the expression x of the declaration decl of pkg at compile
// time, with each variable of binds bound to its constant: the computed
// argument of a rewrite law's right side at a site (types.md, Laws 14).
// It runs no initializer, so x reads constants, its bound variables and
// pure functions only; a package let, an effect or a form the evaluator
// does not run is a reason, never a guess. The budget bounds the work.
// It returns the value as a constant of x's basic type, or "" and the
// reason it could not.
func Fold(pkg *check.Package, decl syntax.Decl, x syntax.Expr, binds map[*check.Var]constant.Value, b Budget) (v constant.Value, why string) {
	if b.Steps <= 0 || b.Depth <= 0 {
		return nil, "no evaluation budget"
	}
	if b.Bytes == 0 {
		b.Bytes = defaultBytes
	}
	in := &interp{budget: b, root: pkg, files: map[syntax.Decl]*syntax.File{}, initials: map[*check.TypeName]Value{}, globals: map[*check.Var]*slot{}}
	defer func() {
		if r := recover(); r != nil {
			ie, ok := r.(internalError)
			if !ok {
				panic(r)
			}
			v, why = nil, ie.Error()
		}
	}()
	fr := in.newFrame(pkg, decl)
	for p, c := range binds {
		val, ok := fromConstant(p.Type, c)
		if !ok {
			return nil, "the constant of " + p.Name + " is not a basic value"
		}
		fr.bind(p, val)
	}
	got, ab := in.expr(fr, x)
	if ab != nil {
		if ab.msg != "" {
			return nil, ab.msg
		}
		return nil, "the expression does not end in a value"
	}
	if c := toConstant(got); c != nil {
		return c, ""
	}
	return nil, "the value is not a basic constant"
}

// toConstant is the constant of a basic value (fromConstant's inverse),
// nil for any other value.
func toConstant(v Value) constant.Value {
	switch v := v.(type) {
	case bool:
		return constant.MakeBool(v)
	case string:
		return constant.MakeString(v)
	case int8:
		return constant.MakeInt64(int64(v))
	case int16:
		return constant.MakeInt64(int64(v))
	case int32:
		return constant.MakeInt64(int64(v))
	case int64:
		return constant.MakeInt64(v)
	case uint8:
		return constant.MakeUint64(uint64(v))
	case uint16:
		return constant.MakeUint64(uint64(v))
	case uint32:
		return constant.MakeUint64(uint64(v))
	case uint64:
		return constant.MakeUint64(v)
	case float32:
		if math.IsInf(float64(v), 0) || math.IsNaN(float64(v)) {
			return nil
		}
		return constant.MakeFloat64(float64(v))
	case float64:
		if math.IsInf(v, 0) || math.IsNaN(v) {
			return nil
		}
		return constant.MakeFloat64(v)
	}
	return nil
}
