package stdlib_test

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestOrderLawsRefuteBrokenOrders holds cmp.Order's member laws to their
// meaning (CONSTRAINTS C3): each runs at a user's provider, in the
// check-time evaluator, and refutes the provider that breaks it alone.
// Cycle is reflexive and antisymmetric but not transitive (a rock, paper,
// scissors order on n mod 3); Twisted puts a value after itself; Lawful,
// built with cmp.Then, is an order and holds all three. A failing seed
// prints the source and the replay command.
// Mutant it alone catches (testdata/mutants.json): c3-transitive-true
// (Order.Transitive weakened to true), c3-reflexive-true.
func TestOrderLawsRefuteBrokenOrders(t *testing.T) {
	const src = `import cmp

type Cycle {} implements cmp.Order[int]

func Compare(_ Cycle, a int, b int) int {
	let x = ((a % 3) + 3) % 3
	let y = ((b % 3) + 3) % 3
	if x == y {
		return 0
	}
	if (x + 1) % 3 == y {
		return -1
	}
	1
}

type Twisted {} implements cmp.Order[int]

func Compare(_ Twisted, a int, b int) int {
	if a == b {
		return 1
	}
	cmp.Compare(a, b)
}

type Lawful {} implements cmp.Order[int]

func Compare(_ Lawful, a int, b int) int { cmp.Then(cmp.Compare(a % 2, b % 2), cmp.Compare(a, b)) }

func main() {}
`
	want := map[string]eval.Status{
		"law cmp.Order.Reflexive for Cycle":      eval.Passed,
		"law cmp.Order.Antisymmetric for Cycle":  eval.Passed,
		"law cmp.Order.Transitive for Cycle":     eval.Refuted,
		"law cmp.Order.Reflexive for Twisted":    eval.Refuted,
		"law cmp.Order.Reflexive for Lawful":     eval.Passed,
		"law cmp.Order.Antisymmetric for Lawful": eval.Passed,
		"law cmp.Order.Transitive for Lawful":    eval.Passed,
	}
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("main", []*syntax.File{f}, nil)
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	for seed := uint64(1); seed <= 4; seed++ {
		b := eval.DefaultBudget()
		b.Seed = seed
		r, err := eval.Run(pkg, b)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]eval.Status{}
		for _, res := range r.Results {
			got[res.Name] = res.Status
		}
		for name, st := range want {
			if got[name] != st {
				t.Errorf("seed %d: %s is %q, want %q\nreplay: ergo check --seed %d\n%s", seed, name, got[name], st, seed, src)
			}
		}
	}
}
