package check_test

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// K2 (M7): the expected type flows where types.md says it does and nowhere
// else. In a == b a composite literal of a generic type written without
// type arguments takes them from the other operand (Basic types 6), and the
// operand of a conversion T(x) has no expected type (Operators 3).

// TestComparisonGenericLiteralTakesOtherType is synthesis repro a5, with
// the literal on either side, both operators, and an Option of a basic
// type; a literal whose fields do not fit the other side stays S108.
func TestComparisonGenericLiteralTakesOtherType(t *testing.T) {
	const decls = `type T = A | B
type A {}
type B {}
type Box[V] { Item V }
func f() Option[T] { Some{Value: A} }
func g() Option[int] { None }
func h() Box[T] { Box{Item: B} }
`
	for _, body := range []string{
		`f() == Some{Value: A}`,
		`Some{Value: A} == f()`,
		`f() != Some{Value: B}`,
		`(Some{Value: B}) != f()`,
		`g() == Some{Value: 3}`,
		`Some{Value: 3} == g()`,
		`h() == Box{Item: A}`,
		`Box{Item: A} == h()`,
	} {
		src := decls + "func ok() bool { " + body + " }\n"
		if _, d := check.CheckSource(src); len(d) > 0 {
			t.Errorf("%s: want no diagnostics, got %s", body, describe(d))
		}
	}
	for _, body := range []string{
		`f() == Some{Value: 3}`,
		`Some{Value: "x"} == g()`,
		`h() == Box{Item: 1}`,
	} {
		src := decls + "func bad() bool { " + body + " }\n"
		if _, d := check.CheckSource(src); len(d) != 1 || d[0].Code != check.CodeType {
			t.Errorf("%s: want one S108, got %s", body, describe(d))
		}
	}
}

// TestConversionOperandHasNoExpectedType is synthesis repro b1: the if
// under int64(…) is an int, then converted.
func TestConversionOperandHasNoExpectedType(t *testing.T) {
	for _, src := range []string{
		"func f(n int) int64 { int64(if n < 0 { -n } else { n }) }\n",
		"func f(n int) int8 { int8(if n < 0 { 200 } else { n }) }\n",
		"type Celsius float64\nfunc f(n int, x float64) Celsius { Celsius(match n {\ncase 0:\n\tx\ncase _:\n\t1\n}) }\n",
		// A tuple has no type of its own: it takes the conversion's.
		"type U = A{} | B{}\ntype T0 (U, U)\nfunc f() T0 { T0((A, B)) }\n",
	} {
		if _, d := check.CheckSource(src); len(d) > 0 {
			t.Errorf("want no diagnostics, got %s\n%s", describe(d), src)
		}
	}
}

// convGen writes the expressions of the conversion property: an int or
// float64 expression built from parameters, small literals, if, match,
// catch and arithmetic, in random nests.
type convGen struct {
	r     *rand.Rand
	kinds map[string]int
}

var convTargets = []string{"int8", "int16", "int32", "int64", "int", "uint8", "uint16", "uint32", "uint64", "float32", "float64", "Celsius", "Count"}

func (g *convGen) expr(depth int) string {
	if depth <= 0 {
		return g.leaf()
	}
	switch g.r.Intn(6) {
	case 0:
		g.kinds["if"]++
		return "if c { " + g.expr(depth-1) + " } else { " + g.expr(depth-1) + " }"
	case 1:
		g.kinds["match"]++
		return "match n {\ncase 0:\n\t" + g.expr(depth-1) + "\ncase _:\n\t" + g.expr(depth-1) + "\n}"
	case 2:
		g.kinds["catch"]++
		return "pick(n) catch {\ncase Bad:\n\t" + g.expr(depth-1) + "\n}"
	case 3:
		g.kinds["arith"]++
		op := []string{"+", "-", "*"}[g.r.Intn(3)]
		return "(" + g.expr(depth-1) + " " + op + " " + g.expr(depth-1) + ")"
	case 4:
		g.kinds["neg"]++
		return "-(" + g.expr(depth-1) + ")"
	}
	return g.leaf()
}

func (g *convGen) leaf() string {
	switch g.r.Intn(5) {
	case 0:
		g.kinds["int"]++
		return "n"
	case 1:
		g.kinds["float"]++
		return "x"
	case 2:
		g.kinds["literal"]++
		return fmt.Sprint(g.r.Intn(100))
	case 3:
		g.kinds["literal"]++
		return fmt.Sprintf("%d.5", g.r.Intn(10))
	}
	g.kinds["literal"]++
	return fmt.Sprint(150 + g.r.Intn(200)) // past int8 and uint8
}

const convDecls = `type Bad {}
type Celsius float64
type Count int
func pick(n int) int fails Bad { if n < 0 { return Bad } else { n } }
`

// convPair is the two functions of one property case: T(e), and
// let v = e; T(v).
func convPair(target, e string) (direct, viaLet string) {
	direct = convDecls + "func conv(n int, x float64, c bool) " + target + " {\n\t" + target + "(" + e + ")\n}\n"
	viaLet = convDecls + "func conv(n int, x float64, c bool) " + target + " {\n\tlet v = " + e + "\n\t" + target + "(v)\n}\n"
	return direct, viaLet
}

// TestConversionAgreesWithLet is the conversion property: for generated
// e in {if, match, catch, literal, arithmetic} and T a sized integer,
// float or defined type, T(e) checks iff let v = e; T(v) checks. A
// constant operand is a constant conversion (D-A3), which let does not
// keep, so a literal leaf stands only inside a non-constant form. The
// values agree by TestErgoRunConversionAgreesWithLet in lower.
func TestConversionAgreesWithLet(t *testing.T) {
	g := &convGen{r: rand.New(rand.NewSource(17)), kinds: map[string]int{}}
	accepted, rejected := 0, 0
	for i := 0; i < 400; i++ {
		target := convTargets[g.r.Intn(len(convTargets))]
		e := g.expr(1 + g.r.Intn(3))
		if isConstantText(e) {
			continue
		}
		direct, viaLet := convPair(target, e)
		_, dd := check.CheckSource(direct)
		_, dl := check.CheckSource(viaLet)
		if (len(dd) == 0) != (len(dl) == 0) {
			t.Fatalf("case %d: T(e) and let v = e; T(v) disagree\nT(e): %s\n%s\nlet: %s\n%s", i, describe(dd), direct, describe(dl), viaLet)
		}
		if len(dd) == 0 {
			accepted++
		} else {
			rejected++
		}
	}
	for _, k := range []string{"if", "match", "catch", "arith", "literal", "int", "float"} {
		if g.kinds[k] == 0 {
			t.Errorf("the generator never wrote %s", k)
		}
	}
	if accepted < 50 || rejected < 50 {
		t.Errorf("the cases reach %d accepted and %d rejected conversions; the generator misses a verdict", accepted, rejected)
	}
}

// isConstantText reports an expression with no parameter and no form that
// yields a value at run time: a literal or arithmetic on literals.
func isConstantText(e string) bool {
	return !strings.ContainsAny(e, "nxc")
}
