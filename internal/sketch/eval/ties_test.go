package eval

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestTiesReachEqualArguments holds the tie rule at check time (runtime.md,
// Tests 5, Ties; CONSTRAINTS C2): a law over two values of one record type
// that is false only when they are equal is refuted at every seed, since a
// later parameter repeats an earlier value in a quarter of the cases; two
// drawn records are equal by chance far too rarely for that.
// Mutant it alone catches (testdata/mutants.json): c2-eval-tie-dropped, the
// evaluator drawing every argument anew (the test build's rule kept).
func TestTiesReachEqualArguments(t *testing.T) {
	const src = `type Pair { X int, S string }

law Distinct(a Pair, b Pair) { a != b }

func main() {}
`
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("main", []*syntax.File{f}, nil)
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	for seed := uint64(1); seed <= 20; seed++ {
		b := DefaultBudget()
		b.Seed = seed
		r, err := Run(pkg, b)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, res := range r.Results {
			if res.Name != "law Distinct" {
				continue
			}
			found = true
			if res.Status != Refuted {
				t.Fatalf("seed %d: law Distinct %+v, want refuted at a tie\nreplay: ergo check --seed %d\n%s", seed, res, seed, src)
			}
		}
		if !found {
			t.Fatalf("seed %d: no result for law Distinct in %+v", seed, r.Results)
		}
	}
}
