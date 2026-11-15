package eval

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// TestMapOrderNeverDecides is the property of maporder.go: on seeded
// Maps, a main and a test whose outcome is the order a for over the Map
// visits it in (the first key it reaches) get no verdict when the Map has
// two keys or more, the skip that says the order decides; with fewer
// keys, the one order there is decides it. The order-free fors of the
// evalgen "maps" scenario hold the loop's own rules (TestEvalAgreesWithLowering).
//
// Mutants only it kills (testdata/mutants.json): oracle-o14-one-order,
// oracle-o14-test-order-kept.
func TestMapOrderNeverDecides(t *testing.T) {
	for seed := uint64(1); seed <= 30; seed++ {
		r := rand.New(rand.NewPCG(seed, 14))
		var lits []string
		keys := map[int]bool{}
		for range r.IntN(5) {
			k := r.IntN(10)
			if !keys[k] {
				keys[k] = true
				lits = append(lits, fmt.Sprintf("%d: %d", k, r.IntN(10)))
			}
		}
		src := fmt.Sprintf(`func first(m Map[int, int]) int {
	let mut out = -1
	for k, _ in m {
		if out < 0 {
			out = k
		}
	}
	out
}

func main() needs Console {
	Console.Println("\(first(Map[int, int]{%[1]s}))")
}

test First {
	let f = first(Map[int, int]{%[1]s})
	assert f == -1 || f >= 0
	assert f < 0 || f %% 2 == 0
}
`, strings.Join(lits, ", "))
		pkg := checked(t, src)
		decides := len(keys) >= 2
		out, err := RunMain(pkg, DefaultBudget(), Process{})
		if err != nil {
			t.Fatal(err)
		}
		if got := out.Skipped == orderNote; got != decides {
			t.Fatalf("seed %d: main over %d keys: skipped %q, stdout %q; the order decides: %v\n%s", seed, len(keys), out.Skipped, out.Stdout, decides, src)
		}
		rep, err := Run(pkg, DefaultBudget())
		if err != nil {
			t.Fatal(err)
		}
		// The test asserts the first key is even: both orders reach
		// the same verdict only when the extreme keys share a parity.
		lo, hi := 10, -1
		for k := range keys {
			lo, hi = min(lo, k), max(hi, k)
		}
		split := len(keys) >= 2 && lo%2 != hi%2
		res := rep.Results[0]
		if got := res.Status == Skipped && res.Reason == orderNote; got != split {
			t.Fatalf("seed %d: test over keys %v: %s %q; the order decides: %v\n%s", seed, keys, res.Status, res.Reason, split, src)
		}
	}
}
