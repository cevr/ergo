package eval

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// TestHashAgreesWithEquality is the property of the evaluator's hash
// (types.md, Maps 4; CONSTRAINTS C5b): over seeded pairs of float64 values
// and of records with a float, an int and a string field, hash(a) ==
// hash(b) exactly when a == b. Equal pairs differ only in the sign of a
// zero; unequal pairs differ in one field, so a hash that skips a field
// or reads -0.0 apart from 0.0 is refuted. A failing seed prints the
// Ergo source.
//
// Mutants only it kills (testdata/mutants.json): c5b-hash-zero-sign,
// c5b-hash-record-by-text.
func TestHashAgreesWithEquality(t *testing.T) {
	for seed := uint64(1); seed <= 8; seed++ {
		r := rand.New(rand.NewPCG(seed, 6))
		var body, want strings.Builder
		zero := func() string {
			if r.IntN(2) == 0 {
				return "neg(0.0)" // a constant -0.0 is 0.0
			}
			return "0.0"
		}
		for i := range 12 {
			x, n, s := zero(), r.IntN(5), fmt.Sprintf("%q", []string{"a", "b", "c"}[r.IntN(3)])
			x2, n2, s2 := zero(), n, s
			equal := true
			switch r.IntN(4) {
			case 1:
				x2, equal = "1.5", false
			case 2:
				n2, equal = n+1, false
			case 3:
				s2, equal = `"z"`, false
			}
			fmt.Fprintf(&body, "\tConsole.Println(\"\\(hash(%s) == hash(%s)) \\(hash(P{x: %s, n: %d, s: %s}) == hash(P{x: %s, n: %d, s: %s}))\")\n",
				x, x2, x, n, s, x2, n2, s2)
			fmt.Fprintf(&want, "%v %v\n", x2 != "1.5", equal)
			_ = i
		}
		src := "type P {\n\tx float64,\n\tn int,\n\ts string,\n}\n\nfunc neg(v float64) float64 { -v }\n\nfunc main() needs Console {\n" + body.String() + "}\n"
		pkg := checked(t, src)
		out, err := RunMain(pkg, DefaultBudget(), Process{})
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, src)
		}
		if out.Skipped != "" || out.Stdout != want.String() {
			t.Fatalf("seed %d: hash disagrees with == (skipped %q)\ngot:\n%s\nwant:\n%s\nsource:\n%s", seed, out.Skipped, out.Stdout, want.String(), src)
		}
	}
}
