package check

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// Constant arithmetic against go/types (spec D-A6, round-1 review M3). The
// lowering emits one conversion per literal (difflower_test.go), so
// float64(1) / float64(2) is 0.5, and the differential compares each
// constant's Info.Values entry with the value go/types computes for the
// lowered text. constGen writes programs whose functions return a random
// constant expression of one basic type: integer and float literals, the
// operators + - * / %, unary - and ^, shifts with a literal count,
// parentheses, float32 rounding, and divisors that fold to zero.

// constTypes are the result types constGen draws from.
var constTypes = []string{"int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "float32", "float64"}

type constGen struct{ rng *rand.Rand }

func (g *constGen) isFloat(t string) bool { return strings.HasPrefix(t, "float") }

func (g *constGen) leaf(t string) string {
	r := g.rng.Intn(10)
	switch {
	case r < 5:
		return fmt.Sprint([]int{0, 1, 2, 3, 7, 10, 100, 127, 128, 255, 256, 1000}[g.rng.Intn(12)])
	case r < 7 || g.isFloat(t) && r < 9:
		// A float literal: exact or not in binary, large or small, and
		// one that float32 rounds (16777217).
		return []string{"0.5", "2.5", "0.1", "0.2", "1.0", "3.0", "1e3", "1e-3", "16777217.0", "1e40"}[g.rng.Intn(10)]
	case r < 9:
		return fmt.Sprint([]int{65535, 2147483647, 2147483648, 4294967295, 16777217}[g.rng.Intn(5)])
	}
	return "9223372036854775807"
}

func (g *constGen) expr(t string, depth int) string {
	if depth == 0 || g.rng.Intn(4) == 0 {
		return g.leaf(t)
	}
	unaryOperand := func() string {
		x := g.expr(t, depth-1)
		if strings.HasPrefix(x, "-") || strings.HasPrefix(x, "^") || strings.ContainsAny(x, " ") {
			return "(" + x + ")" // never --x; a sign applies to the whole operand
		}
		return x
	}
	switch g.rng.Intn(9) {
	case 0:
		return "-" + unaryOperand()
	case 1:
		return "^" + unaryOperand()
	case 2:
		return "(" + g.expr(t, depth-1) + ")"
	case 3:
		return g.expr(t, depth-1) + []string{" << ", " >> "}[g.rng.Intn(2)] + fmt.Sprint(g.rng.Intn(10))
	}
	op := []string{" + ", " - ", " * ", " / ", " % "}[g.rng.Intn(5)]
	return g.expr(t, depth-1) + op + g.expr(t, depth-1)
}

// zero is a constant expression that folds to zero.
func (g *constGen) zero() string {
	return []string{"(3 - 3)", "(2 * 0)", "(1 - 1)", "(0 / 5)"}[g.rng.Intn(4)]
}

func (g *constGen) program() twin {
	var w twinBuilder
	w.each("package p\n\nfunc Id[T any](x T) T { return x }\n\n", "func Id[T](x T) T { return x }\n\n")
	for i := range 6 {
		t := constTypes[g.rng.Intn(len(constTypes))]
		switch g.rng.Intn(4) {
		case 0:
			w.both("func F%d() %s { return Id[%s](%s) }\n\n", i, t, t, g.expr(t, 3))
		case 1:
			// A divisor that folds to zero beside a variable.
			w.both("func F%d(x %s) %s { return x %s %s }\n\n", i, t, t, []string{"/", "%"}[g.rng.Intn(2)], g.zero())
		default:
			w.both("func F%d() %s { return %s }\n\n", i, t, g.expr(t, 3))
		}
	}
	return w.twin()
}

// TestDifferentialConstants runs constGen over a fixed seed set. Both
// verdicts must occur, and accepted programs must hold constants.
func TestDifferentialConstants(t *testing.T) {
	accepted, rejected, values := 0, 0, 0
	for seed := int64(0); seed < diffSeeds; seed++ {
		src := (&constGen{rng: rand.New(rand.NewSource(seed))}).program()
		if d := diffCheck(src, false); len(d) > 0 {
			t.Fatalf("seed %d:\n%s\n%s", seed, strings.Join(d, "\n"), src.ergoSrc)
		}
		if gaps := cleanInfoGaps(src.ergoSrc); len(gaps) > 0 {
			t.Fatalf("seed %d (Info):\n\t%s\n%s", seed, strings.Join(gaps, "\n\t"), src.ergoSrc)
		}
		// Each function alone, so one refusal does not hide the others.
		for _, fn := range strings.Split(src.goSrc, "\n\n")[2:] {
			if strings.TrimSpace(fn) == "" {
				continue
			}
			one := newTwin("package p\n\nfunc Id[T any](x T) T { return x }\n\n"+fn+"\n", "func Id[T](x T) T { return x }\n\n"+fn+"\n")
			if d := diffCheck(one, false); len(d) > 0 {
				t.Fatalf("seed %d:\n%s\n%s", seed, strings.Join(d, "\n"), one.ergoSrc)
			}
			pkg, diags := CheckSource(one.ergoSrc)
			if len(diags) == 0 {
				accepted++
				values += len(pkg.Info.Values)
			} else {
				rejected++
			}
		}
	}
	if accepted < diffSeeds/2 || rejected < diffSeeds/2 || values < accepted {
		t.Errorf("the seeds cover %d accepted functions (%d constants) and %d rejected; the generator misses a verdict", accepted, values, rejected)
	}
}

// TestRefusals196u (round-2 review m3): the differential excuses exactly
// 196u's refusal texts. Every TestLiteralRules refusal is one, except a
// mismatch of two typed operands (Go refuses it too) and a call of a
// constant; the class G message, which also says "; write", is not one.
func TestRefusals196u(t *testing.T) {
	not196u := map[string]bool{
		"4: typed operands never convert":       true,
		"a literal is not called":               true,
		"a parenthesized literal is not called": true,
		"a named constant is not called":        true,
	}
	for _, row := range literalRules {
		_, diags := CheckSource(row.src + "\n")
		if len(diags) != 1 {
			t.Fatalf("%s: want one diagnostic, got %s", row.rule, describeDiags(diags))
		}
		if got := is196uRefusal(diags[0].String()); got == not196u[row.rule] {
			t.Errorf("%s: is196uRefusal(%q) = %v", row.rule, diags[0].String(), got)
		}
	}
	for _, d := range []string{
		"5:6: S108 walk is used as a value at 13:10 inside its own recursive group; write its error row: func walk() fails …",
		"2:9: S108 mismatched types int32 and int64; write int64(a)",
		"3:1: S108 cannot use float64 as int in return",
		"3:1: S104 the constant 300 overflows int8",
	} {
		if is196uRefusal(d) {
			t.Errorf("is196uRefusal(%q) = true", d)
		}
	}
}
