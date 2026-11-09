package check

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"math/rand"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Differential testing against go/types (round-4 request A, widened in
// round 5). Where Go's semantics apply, Go's checker is the oracle: a
// generator writes each random program twice (a twin), as Go text and as
// decided Ergo text of the same meaning with no rows (spec D-A6): go/types
// reads the Go text, the checker the Ergo text, and their expressions match
// by tokens (align_test.go). The programs hold:
//
//   - generic struct and interface types, instantiated inside type
//     declarations;
//   - generic functions of one or two type parameters with value
//     parameters and results;
//   - methods whose receiver names the type's parameter or renames it,
//     with parametric signatures, which instantiate the receiver's own and
//     other receivers' types (signature growth and cross-receiver cycles,
//     round-4 M1); the Ergo text writes each as a function whose first
//     parameter is the receiver (196m);
//   - instantiations as values, call targets, in parentheses, explicit
//     with every argument, partial, and inferred.
//
// The forms the decided language removed are not generated: fixed arrays,
// channels, anonymous struct and interface types, bodiless methods and
// method values (#196; the merge round).
//
// Both checkers must agree on accept or reject; a rejection must be an
// instantiation cycle in both (any other go/types error is a generator
// bug and fails the test). On accepted programs, every expression go/types
// records as a value (identifiers, index expressions with one or several
// indices, parentheses, selectors, calls, composite literals) must carry
// the same type in Ergo's Info.Types, generic signatures with their type
// parameters and constraints; a type expression Ergo records must match
// too (round-4 M3). go/types reads the lowered Go text, where each literal
// is converted to the one type Ergo gave it (196u rule 8;
// difflower_test.go), so it checks the types the lowering emits rather
// than Go's untyped constants. Prior art: Csmith-style differential
// testing; Go cross-checks types2 against go/types the same way.

// twin is one generated program written twice: Go text, which go/types
// reads, and decided Ergo text of the same meaning, which the checker
// reads. The two keep every function body and package variable token for
// token, apart from the spellings align_test.go drops (spec D-A6: the
// generators write both; no oracle depends on a translator).
type twin struct{ goSrc, ergoSrc string }

// newTwin pairs a program's two texts; the Ergo text is formatted.
func newTwin(goSrc, ergoSrc string) twin {
	return twin{goSrc: goSrc, ergoSrc: decided(ergoSrc)}
}

// twinBuilder writes a twin: text both syntaxes spell alike, or a pair.
type twinBuilder struct{ g, e strings.Builder }

func (w *twinBuilder) both(format string, args ...any) {
	fmt.Fprintf(&w.g, format, args...)
	fmt.Fprintf(&w.e, format, args...)
}

func (w *twinBuilder) each(goText, ergoText string) {
	w.g.WriteString(goText)
	w.e.WriteString(ergoText)
}

func (w *twinBuilder) reset() { w.g.Reset(); w.e.Reset() }

func (w *twinBuilder) twin() twin { return newTwin(w.g.String(), w.e.String()) }

// tt is a type written in both syntaxes: Go's map[K]V is Ergo's Map[K, V].
type tt struct{ g, e string }

func ttOf(s string) tt { return tt{s, s} }

func (t tt) wrap(goPre, goPost, ergoPre, ergoPost string) tt {
	return tt{goPre + t.g + goPost, ergoPre + t.e + ergoPost}
}

func joinTT(ts []tt, sep string) tt {
	var g, e []string
	for _, t := range ts {
		g, e = append(g, t.g), append(e, t.e)
	}
	return tt{strings.Join(g, sep), strings.Join(e, sep)}
}

// diffFunc is a generated generic function: its parameter count and its
// value parameter types, written over P and Q.
type diffFunc struct {
	ntp    int
	params []tt
	result bool
}

// diffGen writes one program from rng.
type diffGen struct {
	rng    *rand.Rand
	b      twinBuilder
	types  int // generic struct types Gt0…
	ifaces int // generic interface types Gi0…
	funcs  []diffFunc
	vars   int
}

// typeExpr writes a type of depth at most d over the type parameters ps.
func (g *diffGen) typeExpr(ps []string, d int) tt {
	leaves := []string{"int", "string"}
	leaves = append(leaves, ps...)
	leaves = append(leaves, ps...)
	if d == 0 || g.rng.Intn(3) == 0 {
		return ttOf(leaves[g.rng.Intn(len(leaves))])
	}
	inner := g.typeExpr(ps, d-1)
	switch g.rng.Intn(6) {
	case 0:
		return inner.wrap("[]", "", "[]", "")
	case 1:
		return inner.wrap("map[string]", "", "Map[string, ", "]")
	case 2:
		return inner.wrap("func(", ")", "func(", ")")
	case 3:
		n := fmt.Sprintf("Gi%d[", g.rng.Intn(g.ifaces))
		return inner.wrap(n, "]", n, "]")
	default:
		n := fmt.Sprintf("Gt%d[", g.rng.Intn(g.types))
		return inner.wrap(n, "]", n, "]")
	}
}

// zeroType writes a type that has a zero value in Ergo (no function or
// interface type, no bare type parameter in a non-generic context), over
// the type parameters ps.
func (g *diffGen) zeroType(ps []string, d int) tt {
	leaves := []string{"int", "string"}
	leaves = append(leaves, ps...)
	if d == 0 || g.rng.Intn(3) == 0 {
		return ttOf(leaves[g.rng.Intn(len(leaves))])
	}
	inner := g.zeroType(ps, d-1)
	switch g.rng.Intn(4) {
	case 0:
		return inner.wrap("[]", "", "[]", "")
	case 1:
		return inner.wrap("map[string]", "", "Map[string, ", "]")
	default:
		n := fmt.Sprintf("Gt%d[", g.rng.Intn(g.types))
		return inner.wrap(n, "]", n, "]")
	}
}

// substParams replaces the type parameter names in a type written by the
// generator with their arguments, token by token.
func substParams(t string, m map[string]string) string {
	var out strings.Builder
	word := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }
	rs := []rune(t)
	for i := 0; i < len(rs); {
		if !word(rs[i]) {
			out.WriteRune(rs[i])
			i++
			continue
		}
		j := i
		for j < len(rs) && word(rs[j]) {
			j++
		}
		w := string(rs[i:j])
		if a, ok := m[w]; ok {
			w = a
		}
		out.WriteString(w)
		i = j
	}
	return out.String()
}

// substTT is substParams in both syntaxes.
func substTT(t tt, m map[string]tt) tt {
	gm, em := map[string]string{}, map[string]string{}
	for k, v := range m {
		gm[k], em[k] = v.g, v.e
	}
	return tt{substParams(t.g, gm), substParams(t.e, em)}
}

var tpNames = []string{"P", "Q"}

// targs writes type arguments for k parameters in the context ps.
func (g *diffGen) targs(ps []string, k int, zero bool) []tt {
	out := make([]tt, k)
	for i := range out {
		if zero {
			out[i] = g.zeroType(ps, 2)
		} else {
			out[i] = g.typeExpr(ps, 2)
		}
	}
	return out
}

// stmts writes k statements that instantiate generics in the context of
// the type parameters ps (none in Main).
func (g *diffGen) stmts(ps []string, k int) {
	for range k {
		choices := 4
		if len(ps) == 0 {
			choices = 9
		}
		switch g.rng.Intn(choices) {
		case 0:
			i := g.rng.Intn(len(g.funcs))
			a := joinTT(g.targs(ps, g.funcs[i].ntp, false), ", ")
			g.b.each(fmt.Sprintf("\t_ = Fn%d[%s]\n", i, a.g), fmt.Sprintf("\t_ = Fn%d[%s]\n", i, a.e))
		case 1:
			i := g.rng.Intn(len(g.funcs))
			a := joinTT(g.targs(ps, g.funcs[i].ntp, false), ", ")
			g.b.each(fmt.Sprintf("\t_ = (Fn%d[%s])\n", i, a.g), fmt.Sprintf("\t_ = (Fn%d[%s])\n", i, a.e))
		case 2:
			j := g.rng.Intn(g.types)
			a := g.typeExpr(ps, 2)
			g.b.each(fmt.Sprintf("\t_ = Gt%d[%s]{}\n", j, a.g), fmt.Sprintf("\t_ = Gt%d[%s]{}\n", j, a.e))
		case 3:
			j := g.rng.Intn(g.types)
			a := g.typeExpr(ps, 2)
			g.b.each(fmt.Sprintf("\tGt%d[%s]{}.Mt%d()\n", j, a.g, j), fmt.Sprintf("\tGt%d[%s]{}.Mt%d()\n", j, a.e, j))
		default:
			g.call()
		}
	}
}

// call writes a call of a generated function in Main, with arguments
// declared as variables of zero-valued types: explicit, in parentheses
// once or twice, partial, or inferred.
func (g *diffGen) call() {
	i := g.rng.Intn(len(g.funcs))
	f := g.funcs[i]
	ta := g.targs(nil, f.ntp, true)
	m := map[string]tt{}
	for k := range f.ntp {
		m[tpNames[k]] = ta[k]
	}
	var args []string
	for _, p := range f.params {
		v := fmt.Sprintf("a%d", g.vars)
		g.vars++
		a := substTT(p, m)
		g.b.each(fmt.Sprintf("\tvar %s %s\n", v, a.g), fmt.Sprintf("\tlet %s %s\n", v, a.e))
		args = append(args, v)
	}
	// occurs reports whether parameter k can be inferred from the
	// arguments.
	occurs := func(k int) bool {
		for _, p := range f.params {
			if substParams(p.g, map[string]string{tpNames[k]: "\x00"}) != p.g {
				return true
			}
		}
		return false
	}
	all := joinTT(ta, ", ")
	callee := tt{fmt.Sprintf("Fn%d[%s]", i, all.g), fmt.Sprintf("Fn%d[%s]", i, all.e)}
	switch g.rng.Intn(5) {
	case 0:
		callee = callee.wrap("(", ")", "(", ")")
	case 1:
		callee = callee.wrap("((", "))", "((", "))")
	case 2:
		if f.ntp == 2 && occurs(1) {
			callee = tt{fmt.Sprintf("Fn%d[%s]", i, ta[0].g), fmt.Sprintf("Fn%d[%s]", i, ta[0].e)}
		}
	case 3:
		every := true
		for k := range f.ntp {
			every = every && occurs(k)
		}
		if every {
			callee = ttOf(fmt.Sprintf("Fn%d", i))
		}
	}
	lhs := "\t"
	if f.result {
		lhs = "\t_ = "
	}
	g.b.each(fmt.Sprintf("%s%s(%s)\n", lhs, callee.g, strings.Join(args, ", ")), fmt.Sprintf("%s%s(%s)\n", lhs, callee.e, strings.Join(args, ", ")))
}

func (g *diffGen) program() twin {
	g.b.reset()
	g.vars = 0
	g.types = 1 + g.rng.Intn(3)
	g.ifaces = 1 + g.rng.Intn(2)
	g.funcs = make([]diffFunc, 1+g.rng.Intn(3))
	g.b.each("package p\n\n", "")
	for i := range g.types {
		field := ttOf("[]P")
		if g.rng.Intn(4) == 0 {
			// An instantiation inside a type declaration, behind a slice.
			field = g.typeExpr([]string{"P"}, 2).wrap("[]", "", "[]", "")
		}
		g.b.each(fmt.Sprintf("type Gt%d[P any] struct{ F %s }\n\n", i, field.g), fmt.Sprintf("type Gt%d[P] { F %s }\n\n", i, field.e))
	}
	for i := range g.ifaces {
		m := g.typeExpr([]string{"P"}, 1)
		g.b.each(fmt.Sprintf("type Gi%d[P any] interface{ M(%s) }\n\n", i, m.g), fmt.Sprintf("type Gi%d[P] { M(%s) }\n\n", i, m.e))
	}
	for i := range g.funcs {
		f := diffFunc{ntp: 1 + g.rng.Intn(2)}
		ps := tpNames[:f.ntp]
		for range g.rng.Intn(3) {
			p := g.zeroType(ps, 2)
			if len(f.params) == 0 && strings.HasPrefix(p.g, "Gt") {
				// A first parameter Gt0[P] makes a function a method of
				// Gt0 in the decided language (196m), whose instantiation
				// cycles are the receiver's; Go's function has none. The
				// methods below cover that case, so a function's first
				// parameter is never a generated type itself.
				p = p.wrap("[]", "", "[]", "")
			}
			f.params = append(f.params, p)
		}
		f.result = len(f.params) > 0 && g.rng.Intn(2) == 0
		g.funcs[i] = f
	}
	for i, f := range g.funcs {
		ps := tpNames[:f.ntp]
		var tps []string
		var params []tt
		for _, p := range ps {
			tps = append(tps, p+" any")
		}
		for k, p := range f.params {
			params = append(params, p.wrap(fmt.Sprintf("x%d ", k), "", fmt.Sprintf("x%d ", k), ""))
		}
		res := ttOf("")
		if f.result {
			res = f.params[0].wrap(" ", "", " ", "")
		}
		pl := joinTT(params, ", ")
		g.b.each(fmt.Sprintf("func Fn%d[%s](%s)%s {\n", i, strings.Join(tps, ", "), pl.g, res.g),
			fmt.Sprintf("func Fn%d[%s](%s)%s {\n", i, strings.Join(ps, ", "), pl.e, res.e))
		g.stmts(ps, g.rng.Intn(3))
		if f.result {
			g.b.both("\treturn x0\n")
		}
		g.b.both("}\n\n")
	}
	for i := range g.types {
		// The receiver names the type's parameter P, or renames it Q.
		rp := tpNames[g.rng.Intn(2)]
		ps := []string{rp}
		g.b.each(fmt.Sprintf("func (r Gt%d[%s]) Mt%d() {\n", i, rp, i), fmt.Sprintf("func Mt%d[%s](r Gt%d[%s]) {\n", i, rp, i, rp))
		g.stmts(ps, g.rng.Intn(3))
		g.b.both("}\n\n")
		// A parametric signature: it may grow the receiver's own type or
		// another receiver's, in a parameter, a result or an interface.
		pt := g.typeExpr(ps, 2)
		switch g.rng.Intn(2) {
		case 0:
			// Returning a parameter of function type needs `escaping`
			// in Ergo (196e; check/escaping.go); Go has no marker.
			xt := pt.e
			if strings.HasPrefix(xt, "func(") {
				xt = "escaping " + xt
			}
			g.b.each(fmt.Sprintf("func (r Gt%d[%s]) Ms%d(x %s) %s {\n\treturn x\n}\n\n", i, rp, i, pt.g, pt.g),
				fmt.Sprintf("func Ms%d[%s](r Gt%d[%s], x %s) %s {\n\treturn x\n}\n\n", i, rp, i, rp, xt, pt.e))
		default:
			g.b.each(fmt.Sprintf("func (r Gt%d[%s]) Ms%d(x %s) {\n}\n\n", i, rp, i, pt.g),
				fmt.Sprintf("func Ms%d[%s](r Gt%d[%s], x %s) {\n}\n\n", i, rp, i, rp, pt.e))
		}
	}
	g.b.both("func Main() {\n")
	g.stmts(nil, 1+g.rng.Intn(4))
	g.b.both("}\n")
	return g.b.twin()
}

// goKey and ergoKey write a type in one normal form, so the two checkers'
// types compare as strings: package qualifiers and spacing are dropped, and
// a type parameter is written by its index in its declaration's list, so a
// receiver that renames its type's parameter still compares. A generic
// signature lists its parameters' constraints.
func goKey(t types.Type) string {
	switch t := types.Unalias(t).(type) {
	case *types.Basic:
		// rune and byte are go/types' names for int32 and uint8.
		switch t.Kind() {
		case types.Int32:
			return "int32"
		case types.Uint8:
			return "uint8"
		}
		return t.Name()
	case *types.Named:
		s := t.Obj().Name()
		if args := t.TypeArgs(); args != nil && args.Len() > 0 {
			parts := make([]string, args.Len())
			for i := range parts {
				parts[i] = goKey(args.At(i))
			}
			s += "[" + strings.Join(parts, ",") + "]"
		}
		return s
	case *types.TypeParam:
		return fmt.Sprintf("$%d", t.Index())
	case *types.Slice:
		return "[]" + goKey(t.Elem())
	case *types.Array:
		return fmt.Sprintf("[%d]%s", t.Len(), goKey(t.Elem()))
	case *types.Map:
		return "map[" + goKey(t.Key()) + "]" + goKey(t.Elem())
	case *types.Chan:
		return "chan " + goKey(t.Elem())
	case *types.Pointer:
		return "*" + goKey(t.Elem())
	case *types.Signature:
		var tps, ps, rs []string
		for i := range t.TypeParams().Len() {
			tps = append(tps, goKey(t.TypeParams().At(i))+" "+goKey(t.TypeParams().At(i).Constraint()))
		}
		for i := range t.Params().Len() {
			ps = append(ps, goKey(t.Params().At(i).Type()))
		}
		for i := range t.Results().Len() {
			rs = append(rs, goKey(t.Results().At(i).Type()))
		}
		s := "func(" + strings.Join(ps, ",") + ")(" + strings.Join(rs, ",") + ")"
		if len(tps) > 0 {
			s = "[" + strings.Join(tps, ",") + "]" + s
		}
		return s
	case *types.Struct:
		var fs []string
		for i := range t.NumFields() {
			fs = append(fs, t.Field(i).Name()+" "+goKey(t.Field(i).Type()))
		}
		return "struct{" + strings.Join(fs, ";") + "}"
	case *types.Interface:
		var ms []string
		for i := range t.NumMethods() {
			m := t.Method(i)
			ms = append(ms, m.Name()+goKey(m.Type()))
		}
		sort.Strings(ms)
		return "interface{" + strings.Join(ms, ";") + "}"
	}
	return fmt.Sprintf("?%T", t)
}

// ergoKeys writes Ergo types in goKey's normal form; index maps each type
// parameter to its position in its declaration's list.
type ergoKeys struct{ index map[*TypeParam]int }

func newErgoKeys(pkg *Package) ergoKeys {
	k := ergoKeys{index: map[*TypeParam]int{}}
	add := func(tps []*TypeParam) {
		for i, tp := range tps {
			k.index[tp] = i
		}
	}
	for _, o := range pkg.Scope.names {
		switch o := o.(type) {
		case *Func:
			if o.Sig != nil {
				add(o.Sig.TParams)
			}
		case *TypeName:
			if n, ok := o.Type.(*Named); ok {
				add(n.TParams)
				for _, m := range n.mlist {
					if m.Sig != nil {
						add(m.Sig.TParams)
					}
				}
			}
		}
	}
	return k
}

func (k ergoKeys) key(t Type) string {
	switch t := t.(type) {
	case *Basic:
		return t.Name
	case *Named:
		if t.Obj.Name == "any" && len(t.TArgs) == 0 {
			return "interface{}"
		}
		s := t.Obj.Name
		if len(t.TArgs) > 0 {
			parts := make([]string, len(t.TArgs))
			for i, a := range t.TArgs {
				parts[i] = k.key(a)
			}
			s += "[" + strings.Join(parts, ",") + "]"
		}
		return s
	case *TypeParam:
		if i, ok := k.index[t]; ok {
			return fmt.Sprintf("$%d", i)
		}
		return "?" + t.Name
	case *Slice:
		return "[]" + k.key(t.Elem)
	case *Map:
		return "map[" + k.key(t.Key) + "]" + k.key(t.Elem)
	case *Pointer:
		return "*" + k.key(t.Elem)
	case *Signature:
		var tps, ps, rs []string
		for _, tp := range t.TParams {
			cons := "interface{}"
			if tp.Constraint != nil {
				cons = k.key(tp.Constraint)
			}
			tps = append(tps, k.key(tp)+" "+cons)
		}
		for _, p := range t.Params {
			ps = append(ps, k.key(p.Type))
		}
		for _, r := range t.Results {
			rs = append(rs, k.key(r))
		}
		s := "func(" + strings.Join(ps, ",") + ")(" + strings.Join(rs, ",") + ")"
		if len(tps) > 0 {
			s = "[" + strings.Join(tps, ",") + "]" + s
		}
		return s
	case *Struct:
		var fs []string
		for _, f := range t.Fields {
			fs = append(fs, f.Name+" "+k.key(f.Type))
		}
		return "struct{" + strings.Join(fs, ";") + "}"
	case *Interface:
		if t.Set != SetAll {
			// go/types names the predeclared constraint comparable; the
			// other sets have no go/types name and key by their own.
			return t.Set.String()
		}
		var ms []string
		for _, m := range t.Methods {
			ms = append(ms, m.Name+k.key(m.Sig))
		}
		sort.Strings(ms)
		return "interface{" + strings.Join(ms, ";") + "}"
	}
	return fmt.Sprintf("?%T", t)
}

// span is an expression's byte range, the key that matches the two ASTs.
type span struct{ from, to int }

// diffOne checks a twin with both checkers and returns the disagreements;
// a rejection must be an instantiation cycle in both.
func diffOne(p twin) []string { return diffCheck(p, true) }

// diffCheck checks a twin with both checkers and returns the disagreements.
// With cycleOnly, a program both reject must fail by an instantiation
// cycle in both; otherwise any rejection agrees (the inference programs
// fail by unification).
func diffCheck(p twin, cycleOnly bool) []string {
	d, _ := diffCount(p, cycleOnly)
	return d
}

// isRefusal reports whether d is one of Ergo's documented refusals of a
// program whose lowering Go accepts: the inferred subset's S108 "cannot
// infer T; write the type arguments" (D6 as amended in round 7).
func isRefusal(d string) bool {
	return strings.Contains(d, "S108") && strings.Contains(d, "cannot infer") && strings.Contains(d, "write the type arguments")
}

// refusals196u are 196u's S108 refusals of a literal or a constant, one
// pattern per message format in literals.go, operators.go, call.go and
// expr.go, matched against the whole message (round-2 review m3: exact
// texts, never a phrase that other messages share).
var refusals196u = func() []*regexp.Regexp {
	var out []*regexp.Regexp
	for _, p := range []string{
		// Rule 1: a literal of the wrong kind (literalMismatch).
		`a float literal takes only a float type, not \S+; write (an integer literal|-?[0-9]+)`,
		`a rune literal takes only an integer type, not \S+`,
		`a string literal takes only a string type, not \S+`,
		`an integer literal is not a duration: write a duration literal such as -?[0-9]+s`,
		// Rule 1: a literal takes its context's type, so ^7 and 7 % 2 in a
		// float64 context apply an integer operator to a float (Go folds
		// them as untyped integers first).
		`operator \S+ is not defined on \S+`,
		// Rule 4: two literals take their defaults (mismatchText).
		`mismatched types \S+ and \S+; write \S+ \S+ \S+`,
		`mismatched types \S+ and \S+; both literals take their defaults(, so convert one to the other's type, e\.g\. \S+\(…\))?`,
		// Rule 3: a name does not convert (assign).
		`\w+ is \S+; write \S+\(\w+\) or declare \w+ \S+`,
		// Rule 3: a typed constant must fit (representable, conversion).
		`the constant \S+ overflows \S+( \(types\.md, Basic types 2\))?`,
		`\S+ is not an integer, so it is not representable as \S+`,
		`the constant \S+ is not representable as \S+`,
		`the constant \S+ truncates to an integer`,
		`the constant \S+ overflows int, the type a conversion's literal takes; write it where \S+ is expected: let x \S+ = \S+`,
		// D-A3: a shift's literal in a non-integer context.
		`the left operand of a shift is an integer, and \S+ is expected here; write int\(.+\) (<<|>>) .+, then convert the result`,
		// Rule 6: mixed literal defaults, in a call and in a record literal.
		`cannot infer \w+; write the type arguments, e\.g\. .+: its literal arguments take \S+ and \S+ \(D6; 196u rule 6\)`,
		`cannot infer the type argument \w+ of \w+: its literals take \S+ and \S+; write \w+\[…\] \(D6\)`,
		// Rule 6: a literal binds its parameter to its default before the
		// constraint is checked (G(1, s)), so the constraint failure is the
		// refusal; Go infers the parameter from the constraint instead.
		`\S+ does not satisfy the constraint .+ of \w+: .+ \(types\.md, Generics 2\)`,
	} {
		out = append(out, regexp.MustCompile(`^`+p+`$`))
	}
	return out
}()

// is196uRefusal reports whether the diagnostic d ("L:C: S108 message") is
// one of refusals196u.
func is196uRefusal(d string) bool {
	_, msg, ok := strings.Cut(d, ": S108 ")
	if !ok {
		return false
	}
	for _, re := range refusals196u {
		if re.MatchString(msg) {
			return true
		}
	}
	return false
}

// sameConstant reports whether two constants are the same value; values
// of kinds that do not compare (a bool and a number) differ.
func sameConstant(a, b constant.Value) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	return constant.Compare(a, token.EQL, b)
}

// goCheck runs go/types over f, recording into info when it is not nil.
func goCheck(fset *token.FileSet, f *ast.File, info *types.Info) []string {
	var errs []string
	conf := types.Config{Error: func(e error) { errs = append(errs, e.Error()) }}
	_, _ = conf.Check("p", fset, []*ast.File{f}, info)
	return errs
}

// diffCount is diffCheck that also reports a refusal. The direction is the
// contract (D6 as amended in round 7; spec D-A6): Ergo accepting implies Go
// accepting the lowered text, where each literal is converted to its one
// Ergo type (196u rule 8; difflower_test.go), with the same types. Ergo
// rejecting what Go accepts as written is a refusal: either 196u types the
// program invalid (its lowered text, each literal of one type, is invalid
// Go too: `1 + 'a'`, `Two(1, 'a')`, `var f float64 = 1 << n`), or every
// diagnostic is the inferred subset's refusal.
func diffCount(p twin, cycleOnly bool) ([]string, bool) {
	src, ergoSrc := p.goSrc, p.ergoSrc
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		return []string{"go cannot parse the generated program: " + err.Error()}, false
	}
	goErrs := goCheck(fset, file, nil)
	pkg, diags := CheckSource(ergoSrc)
	var ds []string
	for _, d := range diags {
		ds = append(ds, d.String())
	}
	// The two texts' expressions match by their kept tokens (align_test.go).
	ergoFile, err := syntax.Parse(ergoSrc)
	if err != nil {
		return []string{"the Ergo text does not parse: " + err.Error()}, false
	}
	goRegs, ergoRegs := goRegions(fset, file, src), ergoRegions(ergoFile, ergoSrc)
	if err := alignCheck(goRegs, ergoRegs); err != nil {
		return []string{fmt.Sprintf("the expressions cannot be matched: %v\n%s", err, ergoSrc)}, false
	}
	keys := newErgoKeys(pkg)
	ergoTypes := map[alignKey][]string{}
	for x, t := range pkg.Info.Types {
		if k, ok := alignKeyOf(ergoRegs, int(x.Pos()), int(x.End())); ok {
			ergoTypes[k] = append(ergoTypes[k], keys.key(t))
		}
	}
	ergoValues := map[alignKey][]constant.Value{}
	for x, v := range pkg.Info.Values {
		if k, ok := alignKeyOf(ergoRegs, int(x.Pos()), int(x.End())); ok {
			ergoValues[k] = append(ergoValues[k], v)
		}
	}
	// The lowered text: the same file, each literal converted in place.
	lfset := token.NewFileSet()
	lowered, _ := parser.ParseFile(lfset, "p.go", src, 0)
	offsets := func(x ast.Node) span {
		return span{lfset.Position(x.Pos()).Offset, lfset.Position(x.End()).Offset}
	}
	made, err := lowerLiterals(lowered, func(x ast.Expr) (string, bool) {
		sp := offsets(x)
		k, ok := alignKeyOf(goRegs, sp.from, sp.to)
		if !ok || len(ergoTypes[k]) == 0 {
			return "", false
		}
		return ergoTypes[k][0], true
	})
	if err != nil {
		return []string{fmt.Sprintf("the lowering: %v\n%s", err, src)}, false
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}}
	lowErrs := goCheck(lfset, lowered, info)
	if len(goErrs) == 0 && len(ds) > 0 {
		// 196u types it invalid: its lowered text is invalid Go too, and
		// each diagnostic is one of 196u's refusals, so a checker bug that
		// types a literal wrong and then refuses is not excused.
		refused := true
		for _, d := range ds {
			refused = refused && (isRefusal(d) || len(lowErrs) > 0 && is196uRefusal(d))
		}
		if !refused {
			return []string{fmt.Sprintf("ergo rejects a program go accepts, not with a 196u refusal: %q (lowered go: %q)", ds, lowErrs)}, false
		}
		return nil, true
	}
	if len(ds) == 0 && len(lowErrs) > 0 {
		return []string{fmt.Sprintf("ergo accepts a program whose lowering go rejects (unsound): go %q", lowErrs)}, false
	}
	if len(ds) > 0 && !cycleOnly {
		return nil, false
	}
	if len(ds) > 0 {
		// Both reject: the generated programs fail only by instantiation
		// cycles, so each checker must name one, and nothing else.
		var out []string
		for _, e := range goErrs {
			// A tab starts a continuation line of the error before
			// ("P instantiated as []P").
			if !strings.Contains(e, "instantiation cycle") && !strings.HasPrefix(e, "\t") && !strings.Contains(e, ": \t") {
				out = append(out, "go rejects the generated program for another reason (generator bug): "+e)
			}
		}
		for _, d := range ds {
			if !strings.Contains(d, "instantiation cycle") {
				out = append(out, "ergo rejects for another reason: "+d)
			}
		}
		return out, false
	}
	var out []string
	text := func(sp span) string { return src[sp.from:sp.to] }
	var compare func(x ast.Expr, withValue bool)
	compare = func(x ast.Expr, withValue bool) {
		tv, ok := info.Types[x]
		if !ok {
			return
		}
		sp := offsets(x)
		gt := goKey(tv.Type)
		if gt == "untyped bool" {
			// Go types a comparison untyped bool until its context fixes
			// it; Ergo types it bool, and the lowered Go is the same.
			gt = "bool"
		}
		k, inRegion := alignKeyOf(goRegs, sp.from, sp.to)
		if !inRegion {
			// A signature's or declaration's type expression: its tokens
			// are the ones the Ergo text moves (a receiver becomes the
			// first parameter), so it is not matched.
			if tv.IsValue() {
				out = append(out, fmt.Sprintf("%s: a value outside every body and variable", text(sp)))
			}
			return
		}
		ets, ok := ergoTypes[k]
		same := false
		for _, et := range ets {
			same = same || et == gt
		}
		switch {
		case !tv.IsValue():
			// A type expression: Ergo need not record it, but a type it
			// records must be the same.
			if ok && tv.IsType() && !same {
				out = append(out, fmt.Sprintf("%s (type expression): ergo %s, go %s", text(sp), strings.Join(ets, " or "), gt))
			}
		case !ok:
			out = append(out, fmt.Sprintf("%s: ergo records no type; go %s", text(sp), gt))
		case !same:
			out = append(out, fmt.Sprintf("%s: ergo %s, go %s", text(sp), strings.Join(ets, " or "), gt))
		}
		// A constant: the lowering emits Info.Values, so it must be the
		// value go/types computes for the lowered text (spec D-A4).
		if withValue && tv.IsValue() && tv.Value != nil {
			evs := ergoValues[k]
			if len(evs) == 0 {
				out = append(out, fmt.Sprintf("%s: ergo records no value; go %s", text(sp), tv.Value.ExactString()))
				return
			}
			equal := false
			for _, ev := range evs {
				equal = equal || sameConstant(ev, tv.Value)
			}
			if !equal {
				var es []string
				for _, ev := range evs {
					es = append(es, ev.ExactString())
				}
				out = append(out, fmt.Sprintf("%s: ergo value %s, go %s", text(sp), strings.Join(es, " or "), tv.Value.ExactString()))
			}
		}
	}
	ast.Inspect(lowered, func(n ast.Node) bool {
		x, ok := n.(ast.Expr)
		if !ok {
			return true
		}
		if call, ok := x.(*ast.CallExpr); ok && made[call] {
			// A converted literal: the conversion and the literal carry its
			// one type. The conversion carries its value, rounded to the
			// type; go/types keeps the literal's own source value (1/10 in
			// float32(0.1)), so only the conversion's value is compared.
			// Inside a signed literal Go keeps the operand untyped, which
			// the lowering never needs, so it is not compared.
			compare(call, true)
			compare(call.Args[0], false)
			return false
		}
		// Every expression node go/types records: identifiers, index and
		// selector expressions, parentheses, calls, composite and basic
		// literals, unary and binary operations, function literals.
		compare(x, true)
		return true
	})
	sort.Strings(out)
	return out, false
}

// diffSeeds is the fixed seed set the unit test runs; FuzzDifferential
// runs any seed.
const diffSeeds = 400

// TestDifferentialGoTypes runs the generator over a fixed seed set. Both
// verdicts must occur, so the run covers accepted and rejected programs.
func TestDifferentialGoTypes(t *testing.T) {
	accepted, rejected := 0, 0
	for seed := int64(0); seed < diffSeeds; seed++ {
		g := &diffGen{rng: rand.New(rand.NewSource(seed))}
		src := g.program()
		if d := diffOne(src); len(d) > 0 {
			t.Fatalf("seed %d:\n%s\n%s", seed, strings.Join(d, "\n"), src.goSrc)
		}
		// Class H (round 16): declaration order changes no verdict, and the
		// reference walk is complete and exact.
		if p := OrderProblems(src.ergoSrc, 1); len(p) > 0 {
			t.Fatalf("seed %d (declaration order): %s", seed, p[0])
		}
		if p := RefWalkProblems(src.ergoSrc); len(p) > 0 {
			t.Fatalf("seed %d (reference walk): %s", seed, strings.Join(p, "; "))
		}
		// Info is complete on every accepted generated program (spec D-A4).
		if gaps := cleanInfoGaps(src.ergoSrc); len(gaps) > 0 {
			t.Fatalf("seed %d (Info):\n\t%s\n%s", seed, strings.Join(gaps, "\n\t"), src.ergoSrc)
		}
		if _, diags := CheckSource(src.ergoSrc); len(diags) == 0 {
			accepted++
		} else {
			rejected++
		}
	}
	if accepted < diffSeeds/10 || rejected < diffSeeds/10 {
		t.Errorf("the seeds cover %d accepted and %d rejected programs; the generator misses a verdict", accepted, rejected)
	}
}

// TestDifferentialInfoNames: the reviewer's round-4 explicit and inferred
// call forms, checked by the same oracle, so the generic name inside an
// explicit instantiation keeps its generic type (round-4 M3).
func TestDifferentialInfoNames(t *testing.T) {
	const (
		goFn   = "func Fn[T any](x T) T { return x }\n"
		ergoFn = "func Fn[T](x T) T { return x }\n"
		goAB   = "func Fn[A, B any](a A, b B) B { return b }\n"
		ergoAB = "func Fn[A, B](a A, b B) B { return b }\n"
	)
	for _, c := range []struct{ goDecl, ergoDecl, body string }{
		{goFn, ergoFn, `func F() int { return Fn[int](1) }`},
		{goFn, ergoFn, `func F() int { return ((Fn[int]))(1) }`},
		{goFn, ergoFn, `func F() int { return Fn(1) }`},
		{goAB, ergoAB, `func F() string { return Fn[int, string](1, "a") }`},
		{goAB, ergoAB, `func F() string { return Fn[int](1, "a") }`},
		{goFn, ergoFn, `func F() func(int) int { return Fn[int] }`},
	} {
		p := newTwin("package p\n\n"+c.goDecl+c.body+"\n", c.ergoDecl+c.body+"\n")
		if d := diffOne(p); len(d) > 0 {
			t.Errorf("%s:\n%s", c.body, strings.Join(d, "\n"))
		}
	}
	// Go reads the inside of parentheses as a value: a generic function
	// there, not instantiated inside, is an error in both checkers.
	for _, body := range []string{
		`func F() int { return (Fn)[int](1) }`,
		`func F() int { return (Fn)(1) }`,
		`func F() string { return (Fn2[int])(1, "a") }`,
		`func F() { _ = (Fn) }`,
	} {
		p := newTwin("package p\n\nfunc Fn[T any](x T) T { return x }\n\nfunc Fn2[A, B any](a A, b B) B { return b }\n\n"+body+"\n",
			"func Fn[T](x T) T { return x }\n\nfunc Fn2[A, B](a A, b B) B { return b }\n\n"+body+"\n")
		_, ds := CheckSource(p.ergoSrc)
		if g := goErrors(p.goSrc); len(g) == 0 || len(ds) == 0 {
			t.Errorf("%s: go %q, ergo %v; both must reject", body, g, ds)
		}
	}
}

// inferProgram writes a program of value-argument inference through
// interfaces (round-5 M1, M3): concrete types whose method M has one of a
// few signatures, value or pointer receivers, some with a second method N;
// named generic interfaces (two with one method set), named plain and
// wider interfaces; functions whose parameter is a named generic or a
// wider generic interface; and Two[T](a, b T), called with two arguments in
// either order or with T written (the explicit controls). Every value is a
// parameter of Main, so no zero value is needed. The calls mix matching
// and mismatching arguments, so both verdicts occur. The decided language
// has no anonymous interfaces and no channels (#196), so none is written.
// The two texts differ only in their declarations.
func inferProgram(rng *rand.Rand, wrap int) twin {
	var b twinBuilder
	b.each("package p\n\n", "")
	b.each("type I0[T any] interface{ M(T) T }\n\n", "type I0[T] { M(T) T }\n\n")
	b.each("type W0 interface {\n\tM(int) int\n\tN()\n}\n\n", "type W0 {\n\tM(int) int,\n\tN(),\n}\n\n")
	b.each("type J0 interface{ M(int) int }\n\n", "type J0 { M(int) int }\n\n")
	b.each("type K0 interface{ M(string) string }\n\n", "type K0 { M(string) string }\n\n")
	b.each("type A0[T any] interface{ M(T) T }\n\n", "type A0[T] { M(T) T }\n\n")
	b.each("type V0[T any] interface {\n\tM(T) T\n\tN()\n}\n\n", "type V0[T] {\n\tM(T) T,\n\tN(),\n}\n\n")
	results := []string{"int", "string"}
	nS := 1 + rng.Intn(3)
	var values, types []string
	for k := range nS {
		r := results[rng.Intn(2)]
		b.each(fmt.Sprintf("type S%d struct{}\n\n", k), fmt.Sprintf("type S%d {}\n\n", k))
		recv := fmt.Sprintf("S%d", k)
		if rng.Intn(3) == 0 {
			recv = "*" + recv
		}
		b.each(fmt.Sprintf("func (%s) M(v %s) %s { return v }\n\n", recv, r, r), fmt.Sprintf("func M(_ %s, v %s) %s { return v }\n\n", recv, r, r))
		if rng.Intn(2) == 0 {
			b.each(fmt.Sprintf("func (%s) N() {}\n\n", recv), fmt.Sprintf("func N(_ %s) {}\n\n", recv))
		}
		values = append(values, fmt.Sprintf("s%d S%d", k, k), fmt.Sprintf("p%d *S%d", k, k))
		// A borrow *T is only a parameter or receiver type in Ergo
		// (types.md, Borrows 1), so no explicit control writes one.
		types = append(types, fmt.Sprintf("S%d", k))
	}
	values = append(values, "w W0", "j J0", "i I0[int]", "e K0", "n int")
	types = append(types, "W0", "J0", "I0[int]", "K0", "int", "int32")
	// Every function returns a value, so a call can be nested in another
	// (round 9 class D).
	b.each("func FnA[T any](x A0[T]) A0[T] { return x }\n\n", "func FnA[T](x A0[T]) A0[T] { return x }\n\n")
	b.each("func FnN[T any](x I0[T]) I0[T] { return x }\n\n", "func FnN[T](x I0[T]) I0[T] { return x }\n\n")
	b.each("func FnW[T any](x V0[T]) V0[T] { return x }\n\n", "func FnW[T](x V0[T]) V0[T] { return x }\n\n")
	b.each("func Two[T any](a T, b T) T { return a }\n\n", "func Two[T](a T, b T) T { return a }\n\n")
	b.each("func Id[U any](x U) U { return x }\n\n", "func Id[U](x U) U { return x }\n\n")
	b.each("func Pair[A, B any](a A, b B) A { return a }\n\n", "func Pair[A, B](a A, b B) A { return a }\n\n")
	b.each("func Apply[T any](f func(T) T, x T) T { return x }\n\n", "func Apply[T](f func(T) T, x T) T { return x }\n\n")
	b.both("func Fix(f func(int) int) int { return f(1) }\n\n")
	b.each("func Rev[T any](x T, f func(T) T) T { return x }\n\n", "func Rev[T](x T, f func(T) T) T { return x }\n\n")
	// P comes first: the decided list [T, P I0[T]] would give T the
	// constraint too (one group); [T any, P I0[T]] is the other spelling
	// (grammar.md, Declarations 3).
	b.each("func Dep[P I0[T], T any](v T, p P) T { return v }\n\n", "func Dep[P I0[T], T](v T, p P) T { return v }\n\n")
	b.both("func Main(%s) {\n", strings.Join(values, ", "))
	name := func(v string) string { return v[:strings.Index(v, " ")] }
	for range 1 + rng.Intn(2) {
		// An argument is a value, or a literal expression (a parameter
		// only literals bind takes their common default; 196u rule 6).
		arg := func() string {
			if rng.Intn(4) == 0 {
				return inferConstants[rng.Intn(len(inferConstants))]
			}
			return name(values[rng.Intn(len(values))])
		}
		v, w := arg(), arg()
		lit := func() string {
			t := []string{"int", "int8", "int32", "string"}[rng.Intn(4)]
			return fmt.Sprintf("func(v %s) %s { return v }", t, t)
		}
		var call string
		switch rng.Intn(12) {
		case 9:
			// An annotated function literal is a typed argument, unified
			// before the literals default, in either position (round-7 M3).
			call = fmt.Sprintf("Apply(%s, %s)", lit(), v)
		case 10:
			call = fmt.Sprintf("Rev(%s, %s)", v, lit())
		case 11:
			// A parameter a constraint mentions: a literal binds it to its
			// default, where go/types infers through the constraint, so
			// the constraint may fail (a 196u refusal; round-7 M2).
			call = fmt.Sprintf("Dep(%s, %s)", v, w)
		case 7:
			// A result in context: Info records the argument's one type
			// (round-7 M1; 196u rule 7).
			call = fmt.Sprintf("Id(%s)", v)
		case 8:
			call = fmt.Sprintf("Id[%s](%s)", types[rng.Intn(len(types))], v)
		case 5:
			// A generic function as an argument: outside the subset unless
			// instantiated (Apply(Id, n) is refused, Apply(Id[int], n) is valid).
			call = fmt.Sprintf("Apply(%s, %s)", []string{"Id", "Id[int]", "Id[string]"}[rng.Intn(3)], v)
		case 6:
			call = fmt.Sprintf("Fix(%s)", []string{"Id", "Id[int]"}[rng.Intn(2)])
		case 0:
			call = fmt.Sprintf("FnA(%s)", v)
		case 1:
			call = fmt.Sprintf("FnN(%s)", v)
		case 2:
			call = fmt.Sprintf("FnW(%s)", v)
		case 3:
			call = fmt.Sprintf("Two(%s, %s)", v, w)
		default:
			call = fmt.Sprintf("Two[%s](%s, %s)", types[rng.Intn(len(types))], v, w)
		}
		// The nesting (round 9 class D) uses no randomness, so the flat and
		// the nested program of one seed make the same calls.
		switch wrap {
		case wrapId:
			call = "Id(" + call + ")"
		case wrapPair:
			call = "Pair(" + call + ", n)"
		}
		b.both("\t_ = %s\n", call)
	}
	b.both("}\n")
	return b.twin()
}

// The nestings of a generated call: flat, as the argument of Id, or as the
// first argument of a second generic call, Pair.
const (
	wrapNone = iota
	wrapId
	wrapPair
)

// diagCauses is a program's diagnostics without positions, which a nesting
// shifts: the code and message of each, sorted.
func diagCauses(p twin) []string {
	_, diags := CheckSource(p.ergoSrc)
	out := make([]string, len(diags))
	for i, d := range diags {
		out[i] = d.Code + " " + d.Message
	}
	sort.Strings(out)
	return out
}

// nestedCheck checks the nested programs of one seed: each agrees with
// go/types (verdicts and Info), and has exactly the flat program's
// diagnostics, so a failed or refused call inside another reports one
// diagnostic per root cause, never a dependent one (round-8 M5; D3).
func nestedCheck(seed int64) []string {
	flat := diagCauses(inferProgram(rand.New(rand.NewSource(seed)), wrapNone))
	var out []string
	for _, wrap := range []int{wrapId, wrapPair} {
		src := inferProgram(rand.New(rand.NewSource(seed)), wrap)
		if d := diffCheck(src, false); len(d) > 0 {
			out = append(out, d...)
		}
		if got := diagCauses(src); strings.Join(got, "\n") != strings.Join(flat, "\n") {
			out = append(out, fmt.Sprintf("nesting %d changes the diagnostics:\nflat   %q\nnested %q\n%s", wrap, flat, got, src.ergoSrc))
		}
	}
	return out
}

// inferConstants are the constant arguments the inference generator writes
// (round-7 M1): literals of each kind, parentheses, unary and binary
// operations, comparisons and a constant-producing builtin call, where two
// literals of different defaults are a 196u refusal (`1 + 'a'`); a few
// typed operations with a literal operand, which takes the other side's
// type; and conversions, whose literal operand takes its default (D-A3).
var inferConstants = []string{
	"1", "'a'", "\"s\"", "true",
	"(1)", "-1", "-'a'", "!true", "(('a'))",
	"1 + 'a'", "2 * 3", "1 < 2", "1 << 3", "\"a\" + \"b\"",
	"len(\"ab\")", "-(1 + 2)",
	"n + 1", "('a' * n)", "n < 1 + 2", "!(n < 1)",
	"int8(1)", "int32('a')", "float64(1)", "string(\"x\")", "bool(true)", "int8((1 + 2))", "int(n)",
}

// TestDifferentialInference runs the inference generator over a fixed seed
// set: the verdicts agree with go/types, and on accepted programs every
// value expression's type does. Both verdicts must occur.
func TestDifferentialInference(t *testing.T) {
	accepted, rejected, refused := 0, 0, 0
	for seed := int64(0); seed < diffSeeds; seed++ {
		src := inferProgram(rand.New(rand.NewSource(seed)), wrapNone)
		d, refusal := diffCount(src, false)
		if len(d) > 0 {
			t.Fatalf("seed %d:\n%s\n%s", seed, strings.Join(d, "\n"), src.goSrc)
		}
		if refusal {
			refused++
		}
		if gaps := cleanInfoGaps(src.ergoSrc); len(gaps) > 0 {
			t.Fatalf("seed %d (Info):\n\t%s\n%s", seed, strings.Join(gaps, "\n\t"), src.ergoSrc)
		}
		if _, diags := CheckSource(src.ergoSrc); len(diags) == 0 {
			accepted++
		} else {
			rejected++
		}
	}
	// The refusals: programs go accepts that Ergo refuses with the
	// subset's diagnostic (D6 as amended in round 7), counted for the
	// round's request.
	t.Logf("%d seeds: %d accepted, %d rejected, of which %d are refusals of go-valid programs", diffSeeds, accepted, rejected, refused)
	if accepted < diffSeeds/10 || rejected < diffSeeds/10 {
		t.Errorf("the seeds cover %d accepted and %d rejected programs; the generator misses a verdict", accepted, rejected)
	}
}

// FuzzDifferential runs both generators on any seed (go test -fuzz).
func FuzzDifferential(f *testing.F) {
	for seed := int64(0); seed < 8; seed++ {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed int64) {
		g := &diffGen{rng: rand.New(rand.NewSource(seed))}
		src := g.program()
		if d := diffOne(src); len(d) > 0 {
			t.Fatalf("seed %d:\n%s\n%s", seed, strings.Join(d, "\n"), src.goSrc)
		}
		if p := OrderProblems(src.ergoSrc, 1); len(p) > 0 {
			t.Fatalf("seed %d (declaration order): %s", seed, p[0])
		}
		if p := RefWalkProblems(src.ergoSrc); len(p) > 0 {
			t.Fatalf("seed %d (reference walk): %s", seed, strings.Join(p, "; "))
		}
		if gaps := cleanInfoGaps(src.ergoSrc); len(gaps) > 0 {
			t.Fatalf("seed %d (Info):\n\t%s", seed, strings.Join(gaps, "\n\t"))
		}
		src = inferProgram(rand.New(rand.NewSource(seed)), wrapNone)
		if d := diffCheck(src, false); len(d) > 0 {
			t.Fatalf("seed %d (inference):\n%s\n%s", seed, strings.Join(d, "\n"), src.goSrc)
		}
		if gaps := cleanInfoGaps(src.ergoSrc); len(gaps) > 0 {
			t.Fatalf("seed %d (inference Info):\n\t%s", seed, strings.Join(gaps, "\n\t"))
		}
		if d := nestedCheck(seed); len(d) > 0 {
			t.Fatalf("seed %d (nested):\n%s", seed, strings.Join(d, "\n"))
		}
		src = (&constGen{rng: rand.New(rand.NewSource(seed))}).program()
		if d := diffCheck(src, false); len(d) > 0 {
			t.Fatalf("seed %d (constants):\n%s\n%s", seed, strings.Join(d, "\n"), src.ergoSrc)
		}
		if gaps := cleanInfoGaps(src.ergoSrc); len(gaps) > 0 {
			t.Fatalf("seed %d (constants Info):\n\t%s", seed, strings.Join(gaps, "\n\t"))
		}
	})
}

// TestDifferentialNested runs the nested programs over the fixed seeds:
// every generated call inside Id(…) and inside Pair(…, n) agrees with
// go/types and keeps the flat program's diagnostics, one per root cause
// (round-8 M5; round 9 class D). It also counts the seeds whose calls fail
// or are refused, so the nesting reaches invalid inner calls.
func TestDifferentialNested(t *testing.T) {
	failing := 0
	for seed := int64(0); seed < diffSeeds; seed++ {
		if d := nestedCheck(seed); len(d) > 0 {
			t.Fatalf("seed %d:\n%s", seed, strings.Join(d, "\n"))
		}
		if len(diagCauses(inferProgram(rand.New(rand.NewSource(seed)), wrapNone))) > 0 {
			failing++
		}
	}
	t.Logf("%d seeds: %d nest a failing or refused call", diffSeeds, failing)
	if failing < diffSeeds/10 {
		t.Errorf("only %d seeds nest a failing call", failing)
	}
}
