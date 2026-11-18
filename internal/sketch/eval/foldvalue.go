package eval

import (
	"go/constant"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// ConstKind is the form of a Const.
type ConstKind int

const (
	ConstBasic  ConstKind = iota + 1 // Basic
	ConstRecord                      // Named, with its fields in Elems
	ConstSlice                       // Elems
	ConstTuple                       // Elems
)

// Const is a value a build-time fold computed that the lowering can write
// as a constant: a basic value, or a record, a slice or a tuple of
// constants (CF1). A record's Named is its declared type, a union's
// variant for a union value. Nothing else is a Const: a function value,
// a pointer, a map, a cell or a capability.
type Const struct {
	Kind  ConstKind
	Basic constant.Value
	Named *check.Named
	Elems []Const
}

// FoldValue computes the expression x of the declaration decl of pkg at
// build time (CF1), as Fold does: no initializer runs, x reads no local,
// and the budget bounds the work. It returns the value as a Const, or a
// reason it could not: the budget ran out (over is set), x reads what
// the evaluator does not run, or the value is not a Const.
func FoldValue(pkg *check.Package, decl syntax.Decl, x syntax.Expr, b Budget) (c Const, why string, over bool) {
	if b.Steps <= 0 || b.Depth <= 0 {
		return Const{}, "no evaluation budget", false
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
			c, why, over = Const{}, ie.Error(), false
		}
	}()
	fr := in.newFrame(pkg, decl)
	got, ab := in.expr(fr, x)
	if ab != nil {
		if ab.msg != "" {
			return Const{}, ab.msg, ab.kind == ctlExhausted
		}
		return Const{}, "the expression does not end in a value", false
	}
	out, ok := toConst(got)
	if !ok {
		return Const{}, "the value holds a part a constant cannot: a function, a pointer, a map or a cell", false
	}
	return out, "", false
}

// Rowless reports a function whose row is empty, once each inferred row
// variable is solved: it needs no capability and fails with nothing, so a
// call of it with constant arguments is a constant (CF1).
func Rowless(fn *check.Func) bool { return rowless(fn) }

// toConst is v as a Const, or false when a part of it is none.
func toConst(v Value) (Const, bool) {
	if c := toConstant(v); c != nil {
		return Const{Kind: ConstBasic, Basic: c}, true
	}
	switch v := v.(type) {
	case *record:
		if v.t == nil || (v.key != "" && len(v.fields) > 0) {
			// A generic record's fields are typed by its type arguments,
			// which the Const does not carry: not folded (CF1 r1).
			return Const{}, false
		}
		out := Const{Kind: ConstRecord, Named: v.t}
		for _, f := range v.fields {
			c, ok := toConst(f)
			if !ok {
				return Const{}, false
			}
			out.Elems = append(out.Elems, c)
		}
		return out, true
	case sliceV:
		if v.array {
			return Const{}, false
		}
		out := Const{Kind: ConstSlice, Elems: []Const{}}
		for _, e := range v.elems() {
			c, ok := toConst(e)
			if !ok {
				return Const{}, false
			}
			out.Elems = append(out.Elems, c)
		}
		return out, true
	case tuple:
		out := Const{Kind: ConstTuple}
		for _, e := range v.elems {
			c, ok := toConst(e)
			if !ok {
				return Const{}, false
			}
			out.Elems = append(out.Elems, c)
		}
		return out, true
	}
	return Const{}, false
}
