package lower

import (
	"encoding/json"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// floatStrictSource holds each form a float product takes in an IR body:
// an operand of a sum, a product bound and summed in a later statement, a
// local's x *= v (plain, and with a value that runs statements), and a
// field's p.x *= v; and one product of integers, which no rounding
// touches.
const floatStrictSource = `type P { x float64 }

func mad(a float64, b float64, c float64) float64 {
	a * b + c
}

func later(a float64, b float64, c float64) float64 {
	let t = a * b
	t + c
}

func scaled(a float64, b float64, c float64) float64 {
	let mut x = a
	x *= b
	x *= if c > 0.0 { c } else { 2.0 }
	x + c
}

func field(a float64, b float64) float64 {
	let mut p = P{x: a}
	p.x *= b
	p.x + a
}

func branchy(a float64, c float64) float64 {
	let t = a * if c > 0.0 { c } else { 2.0 }
	t + c
}

func ints(a int, b int, c int) int {
	a * b + c
}

func main() needs Console {
	Console.Println("\(mad(1.1, 2.2, 3.3)) \(later(1.1, 2.2, 3.3)) \(scaled(1.1, 2.2, 3.3)) \(field(1.1, 2.2)) \(branchy(1.1, 3.3)) \(ints(2, 3, 4))")
}
`

// TestIRFloatProductsRound is FLOAT-STRICT's rule on the IR: every float
// product an IR body computes is under an explicit conversion to its
// type, which Go's spec says rounds and prevents fusion into a fused
// multiply-add (arm64 fuses one otherwise, across statements too), and no
// float op-assignment keeps *=; an integer product takes no conversion.
// It reads the IR in process; TestIRFloatsUnfusedOnArm64 (irspike) builds
// it for arm64 and finds no fused instruction.
//
// Mutants: float-strict-binary-fused,
// float-strict-assign-fused, float-strict-assign-value-fused,
// float-strict-store-fused, float-strict-ints-converted.
func TestIRFloatProductsRound(t *testing.T) {
	file, err := syntax.Parse(floatStrictSource)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("%v", diags)
	}
	files, rep, err := LowerIR(Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: floatStrictSource}}})
	if err != nil {
		t.Fatal(err)
	}
	var ir irFile
	for _, f := range files {
		if f.Path == IRFileName {
			if err := json.Unmarshal(f.Data, &ir); err != nil {
				t.Fatal(err)
			}
		}
	}
	want := map[string]int{"mad": 1, "later": 1, "scaled": 2, "field": 1, "branchy": 1, "ints": 0}
	got := map[string]bool{}
	for _, fn := range ir.Funcs {
		n, ok := want[fn.Name]
		if !ok {
			continue
		}
		got[fn.Name] = true
		rounded, bare := 0, 0
		var walkE func(e *irExpr, conv bool)
		var walkS func(ss []*irStmt)
		walkE = func(e *irExpr, conv bool) {
			if e == nil {
				return
			}
			if e.K == "bin" && e.Op == "*" {
				if conv {
					rounded++
				} else {
					bare++
				}
			}
			walkE(e.X, e.K == "conv")
			walkE(e.Y, false)
			for _, a := range e.Args {
				walkE(a, false)
			}
			walkS(e.Body)
		}
		walkS = func(ss []*irStmt) {
			for _, s := range ss {
				if s.Op == "*" {
					bare++ // x *= v: a product no conversion rounds
				}
				walkE(s.X, false)
				walkE(s.Y, false)
				walkS(s.Then)
				walkS(s.Else)
				walkS(s.Body)
			}
		}
		walkS(fn.Body)
		switch {
		case fn.Name == "ints" && (rounded != 0 || bare != 1):
			t.Errorf("ints: %d converted and %d bare integer products, want 0 and 1", rounded, bare)
		case fn.Name != "ints" && (rounded != n || bare != 0):
			t.Errorf("%s: %d rounded and %d bare float products, want %d and 0", fn.Name, rounded, bare, n)
		}
	}
	for name := range want {
		if !got[name] {
			t.Errorf("%s has no IR body (IR %v, fallback %v)", name, rep.IR, rep.Fallback)
		}
	}
}
