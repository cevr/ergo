package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Structural equality (eq-structural, types.md Operators 1). == and != take
// every equatable type: Go's comparable types, which keep Go's ==, and the
// types Go cannot compare, a slice of an equatable element, a Map with
// equatable values, and a record, tuple or union that holds one. For each
// of these the lowering emits one Go function per Go type, which compares
// element by element:
//
//   - a slice: the lengths, then each element in order; an empty slice and
//     a nil one are equal (Ergo has no nil);
//   - a Map: the lengths, then each key of a, which b must hold with an equal
//     value;
//   - a record or tuple: each field in order, && short-circuit;
//   - a union: the variant of a, which b must have, then that variant's
//     equality.
//
// An element, field or variant that Go compares keeps Go's ==, so the cost
// is a hand-written Go Equal's: one call per level that Go cannot compare
// inline. A recursive type's function calls itself. A type that mentions a
// type parameter of the enclosing declaration gets a generic function over
// those parameters, whose constraints the declaration states (a type
// parameter that == reaches is comparable, so its elements compare with
// Go's ==). The functions are named in the naming pass (reserveEquals) and
// emitted once, at first use, as default functions are.
//
// Prior art: Rust's #[derive(PartialEq)] and MoonBit's derive(Eq) emit one
// function per type, field by field; Gleam on JavaScript emits isEqual, a
// structural walk at run time, and ReScript emits caml_equal; Go itself
// compares only comparable types and leaves slices to slices.Equal.

// equalOp lowers a == b or a != b of a type Go cannot compare: a call of
// its generated equality function.
func (fc *funcCtx) equalOp(x *syntax.BinaryExpr, parts []string) string {
	t := fc.equalType(x)
	call := fc.equalCall(t, parts[0], parts[1], x)
	if x.Op == syntax.NEQ {
		return "(!" + call + ")"
	}
	return call
}

// equalType is the type both operands of x are compared as: the operand
// whose type is a union when the other is one of its variants, else the
// left one.
func (fc *funcCtx) equalType(x *syntax.BinaryExpr) check.Type {
	l, r := fc.typeOf(x.X), fc.typeOf(x.Y)
	if fc.isSum(r) && !fc.isSum(l) {
		return r
	}
	return l
}

// needsEqual reports a comparison that Go's == cannot lower: an equatable
// type that is not comparable.
func needsEqual(t check.Type) bool {
	return !check.Comparable(t) && check.Equatable(t)
}

// equalCall is the Go text that compares a and b of type t: Go's == for a
// comparable type, else a call of t's equality function.
func (tc *typeCtx) equalCall(t check.Type, a, b string, site syntax.Node) string {
	if check.Comparable(t) {
		return "(" + a + " == " + b + ")"
	}
	name, free := tc.l.equalFunc(t, site)
	if len(free) > 0 {
		args := make([]string, len(free))
		for i, tp := range free {
			args[i] = tc.goType(tp)
		}
		name += "[" + strings.Join(args, ", ") + "]"
	}
	return name + "(" + a + ", " + b + ")"
}

// equalKey is the package-scope key of t's equality function: t's
// spelling (typeKey), one function per Go type.
func (l *lowerer) equalKey(t check.Type) pkgKey {
	return pkgKey{"equal", l.typeKey(t)}
}

// equalBase is the preferred Go name of t's equality function.
func equalBase(t check.Type) string {
	switch u := t.(type) {
	case *check.Named:
		return "ergoEqual" + u.Obj.Name
	case *check.Slice:
		return "ergoEqualSlice"
	case *check.Array:
		return "ergoEqualArray"
	case *check.Map:
		return "ergoEqualMap"
	case *check.Tuple:
		return "ergoEqualTuple"
	}
	return "ergoEqual"
}

// equalFunc is the equality function of t (needsEqual holds), and the
// type parameters of the enclosing declaration that t mentions, which the
// function takes as its own. It is emitted once, at its first use; the
// name is taken before the body is written, so a recursive type's
// function calls itself.
func (l *lowerer) equalFunc(t check.Type, site syntax.Node) (string, []*check.TypeParam) {
	key := l.equalKey(t)
	name := l.pkgName(key, equalBase(t))
	free := freeTypeParams(t)
	if l.equals[key] {
		return name, free
	}
	l.equals[key] = true
	scope := newNamer(l.names)
	list, names := l.typeParams(free, scope, site)
	tc := &typeCtx{l: l, params: names, site: site}
	goType := tc.goType(t)
	a, b := scope.alloc("a"), scope.alloc("b")
	var body strings.Builder
	fields := func(names []string, types []check.Type) {
		var conj []string
		for i, n := range names {
			conj = append(conj, tc.equalCall(types[i], a+"."+n, b+"."+n, site))
		}
		if len(conj) == 0 {
			conj = []string{"true"}
		}
		body.WriteString("\treturn " + strings.Join(conj, " && ") + "\n")
	}
	switch u := underEqual(t).(type) {
	case *check.Slice:
		i := scope.alloc("i")
		body.WriteString("\tif len(" + a + ") != len(" + b + ") {\n\t\treturn false\n\t}\n")
		body.WriteString("\tfor " + i + " := range " + a + " {\n")
		body.WriteString("\t\tif !" + tc.equalCall(u.Elem, a+"["+i+"]", b+"["+i+"]", site) + " {\n\t\t\treturn false\n\t\t}\n\t}\n\treturn true\n")
	case *check.Array:
		// The length is the type's: only the elements differ.
		i := scope.alloc("i")
		body.WriteString("\tfor " + i + " := range " + a + " {\n")
		body.WriteString("\t\tif !" + tc.equalCall(u.Elem, a+"["+i+"]", b+"["+i+"]", site) + " {\n\t\t\treturn false\n\t\t}\n\t}\n\treturn true\n")
	case *check.Map:
		k, v, w, ok := scope.alloc("k"), scope.alloc("v"), scope.alloc("w"), scope.alloc("ok")
		body.WriteString("\tif len(" + a + ") != len(" + b + ") {\n\t\treturn false\n\t}\n")
		body.WriteString("\tfor " + k + ", " + v + " := range " + a + " {\n")
		body.WriteString("\t\t" + w + ", " + ok + " := " + b + "[" + k + "]\n")
		body.WriteString("\t\tif !" + ok + " || !" + tc.equalCall(u.Elem, v, w, site) + " {\n\t\t\treturn false\n\t\t}\n\t}\n\treturn true\n")
	case *check.Tuple:
		var ns []string
		for i := range u.Types {
			ns = append(ns, tupleField(i))
		}
		fields(ns, u.Types)
	case *check.Struct:
		n, ok := t.(*check.Named)
		if !ok {
			ice(site, "the record type %s has no name", t)
		}
		td := l.typeDecl(n.Obj, site)
		var ns []string
		var ts []check.Type
		for _, f := range u.Fields {
			goName, ok := td.fieldName[f.Name]
			if !ok {
				ice(site, "%s has no field %s", td.name, f.Name)
			}
			ns = append(ns, goName)
			ts = append(ts, f.Type)
		}
		fields(ns, ts)
	case *check.Union:
		if fu := l.flatUnionOf(t); fu != nil {
			// A flat union (flatunion.go): the tags, then the slot the
			// tag names. A slot the tag does not name is zero, which the
			// equality of its type need not accept (a nil union).
			conj := []string{"(" + a + ".tag == " + b + ".tag)"}
			for k, v := range fu.variants {
				if fu.slot[k] {
					conj = append(conj, "("+a+".tag != "+strconv.Itoa(k)+" || "+tc.equalCall(v, fu.flatNarrow(tc, a, k), fu.flatNarrow(tc, b, k), site)+")")
				}
			}
			body.WriteString("\treturn " + strings.Join(conj, " && ") + "\n")
			break
		}
		av, bv, ok := scope.alloc("av"), scope.alloc("bv"), scope.alloc("ok")
		body.WriteString("\tswitch " + av + " := " + a + ".(type) {\n")
		for _, v := range u.Variants {
			vt := tc.goType(v)
			body.WriteString("\tcase " + vt + ":\n")
			body.WriteString("\t\t" + bv + ", " + ok + " := " + b + ".(" + vt + ")\n")
			body.WriteString("\t\treturn " + ok + " && " + tc.equalCall(v, av, bv, site) + "\n")
		}
		body.WriteString("\t}\n\treturn false\n")
	default:
		ice(site, "the type %s needs an equality function but has no structure to compare", t)
	}
	l.gen.WriteString("func " + name + list + "(" + a + ", " + b + " " + goType + ") bool {\n" + body.String() + "}\n\n")
	return name, free
}

// underEqual is the structure t's equality compares: a defined type's or
// an instance's underlying type, t itself otherwise.
func underEqual(t check.Type) check.Type {
	if n, ok := t.(*check.Named); ok {
		return n.Underlying()
	}
	return t
}

// reserveEquals gives, in the naming pass, the key and preferred name of
// every equality function the lowering can emit: one for each type a
// comparison of the build compares that Go cannot, and for each such type
// it reaches through its elements, fields and variants. Reserving one the
// lowering does not emit only renames a local.
func (l *lowerer) reserveEquals(pkgs []*check.Package, reserve func(key pkgKey, t check.Type)) {
	walked := map[check.Type]bool{}
	var walk func(t check.Type)
	walk = func(t check.Type) {
		if t == nil || walked[t] || !needsEqual(t) {
			return
		}
		walked[t] = true
		reserve(l.equalKey(t), t)
		switch u := underEqual(t).(type) {
		case *check.Slice:
			walk(u.Elem)
		case *check.Array:
			walk(u.Elem)
		case *check.Map:
			walk(u.Elem)
		case *check.Tuple:
			for _, e := range u.Types {
				walk(e)
			}
		case *check.Struct:
			for _, f := range u.Fields {
				walk(f.Type)
			}
		case *check.Union:
			for _, v := range u.Variants {
				walk(v)
			}
		}
	}
	for _, p := range pkgs {
		if p.Info == nil {
			continue
		}
		for x := range p.Info.Types {
			b, ok := x.(*syntax.BinaryExpr)
			if !ok || (b.Op != syntax.EQL && b.Op != syntax.NEQ) || !l.readsFact(p, x) {
				continue
			}
			for _, side := range []syntax.Expr{b.X, b.Y} {
				if t, ok := typedIn(p.Info, side); ok {
					walk(t)
				}
			}
		}
	}
}

// typedIn is x's checked type in info, through parentheses.
func typedIn(info *check.Info, x syntax.Expr) (check.Type, bool) {
	for {
		if t, ok := info.Types[x]; ok {
			return t, true
		}
		p, ok := x.(*syntax.ParenExpr)
		if !ok {
			return nil, false
		}
		x = p.X
	}
}
