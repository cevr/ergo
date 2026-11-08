package lower

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// K2 (M7) run tests: synthesis repros a5 and b1 run, and a conversion
// T(e) gives the value let v = e; T(v) gives (types.md, Operators 3). The
// verdict half of the property is check's TestConversionAgreesWithLet.

func TestErgoRunExpectedType(t *testing.T) {
	wantRun(t, `type T = A | B
type A {}
type B {}

func f() Option[T] { Some{Value: A} }

func main() needs Console {
	let n = 3
	let x = int64(if n < 0 { -n } else { n })
	Console.Println("\(f() == Some{Value: A}) \(Some{Value: B} == f()) \(x)")
}
`, "true false 3\n")
}

// runConvGen is check's convGen: int or float64 expressions built from
// parameters, small literals, if, match, catch and arithmetic.
type runConvGen struct{ r *rand.Rand }

func (g *runConvGen) expr(depth int) string {
	if depth <= 0 {
		return g.leaf()
	}
	switch g.r.Intn(6) {
	case 0:
		return "if c { " + g.expr(depth-1) + " } else { " + g.expr(depth-1) + " }"
	case 1:
		return "match n {\ncase 0:\n\t" + g.expr(depth-1) + "\ncase _:\n\t" + g.expr(depth-1) + "\n}"
	case 2:
		return "pick(n) catch {\ncase Bad:\n\t" + g.expr(depth-1) + "\n}"
	case 3:
		op := []string{"+", "-", "*"}[g.r.Intn(3)]
		return "(" + g.expr(depth-1) + " " + op + " " + g.expr(depth-1) + ")"
	case 4:
		return "-(" + g.expr(depth-1) + ")"
	}
	return g.leaf()
}

func (g *runConvGen) leaf() string {
	switch g.r.Intn(5) {
	case 0:
		return "n"
	case 1:
		return "x"
	case 2:
		return fmt.Sprint(g.r.Intn(100))
	case 3:
		return fmt.Sprintf("%d.5", g.r.Intn(10))
	}
	return fmt.Sprint(150 + g.r.Intn(200))
}

// TestErgoRunConversionAgreesWithLet runs 40 generated conversions that
// check, each as T(e) and as let v = e; T(v), over inputs that take every
// if, match and catch branch, and wants the two texts equal.
func TestErgoRunConversionAgreesWithLet(t *testing.T) {
	const decls = `type Bad {}
type Celsius float64
type Count int
func pick(n int) int fails Bad { if n < 0 { return Bad } else { n } }
`
	targets := []string{"int8", "int16", "int32", "int64", "int", "uint8", "uint16", "uint32", "uint64", "float32", "float64", "Celsius", "Count"}
	g := &runConvGen{r: rand.New(rand.NewSource(23))}
	var funcs, calls strings.Builder
	cases := 0
	for i := 0; cases < 40 && i < 2000; i++ {
		target := targets[g.r.Intn(len(targets))]
		e := g.expr(1 + g.r.Intn(3))
		if !strings.ContainsAny(e, "nxc") {
			continue // a constant conversion, which let does not keep (D-A3)
		}
		direct := "func d%[1]d(n int, x float64, c bool) " + target + " {\n\t" + target + "(" + e + ")\n}\n"
		viaLet := "func l%[1]d(n int, x float64, c bool) " + target + " {\n\tlet v = " + e + "\n\t" + target + "(v)\n}\n"
		one := fmt.Sprintf(direct+viaLet, 0)
		if _, d := check.CheckSource(decls + one); len(d) > 0 {
			continue
		}
		fmt.Fprintf(&funcs, direct+viaLet, cases)
		fmt.Fprintf(&calls, "\tshow(\"%[1]d\", \"\\(d%[1]d(n, x, c))\", \"\\(l%[1]d(n, x, c))\")\n", cases)
		cases++
	}
	if cases < 40 {
		t.Fatalf("only %d generated conversions check", cases)
	}
	src := decls + funcs.String() + `
func show(i string, a string, b string) needs Console {
	if a == b {
		Console.Println("same")
	} else {
		Console.Println(i + ": " + a + " != " + b)
	}
}

func each(n int, x float64, c bool) needs Console {
` + calls.String() + `}

func main() needs Console {
	for n in []int{-3, 0, 7, 300} {
		each(n, 2.5, true)
		each(n, -1.25, false)
	}
}
`
	out, err := ergoRun(t, src)
	if err != nil {
		t.Fatalf("ergo run: %v\n%s\n%s", err, out, src)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 8*cases {
		t.Fatalf("want %d lines, got %d:\n%s", 8*cases, len(lines), out)
	}
	for _, line := range lines {
		if line != "same" {
			t.Errorf("T(e) and let v = e; T(v) differ: case %s", line)
		}
	}
	if t.Failed() {
		t.Logf("source:\n%s", src)
	}
}
