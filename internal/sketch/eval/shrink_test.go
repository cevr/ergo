package eval

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// TestShrinkAgreesWithRuntime holds the evaluator's shrinker to the native
// runner's (TESTSPEED FT1: a refuted case shows the same counterexample in
// both tiers): for seeded tapes and seeded failure predicates, some not
// monotone, and seeded budgets, shrinkTape and v1.ShrinkTape try the same
// candidates in the same order and return the same tape and count. The
// seeds must reach a shrink that keeps a smaller tape and one that spends
// its budget. A failing seed prints the tape, the predicate, both
// candidate lists' first difference and the replay command.
//
// Mutants only it kills (testdata/mutants.json):
// oracle-ft1-shrink-no-zero-pass, oracle-ft1-shrink-budget-ignored.
func TestShrinkAgreesWithRuntime(t *testing.T) {
	first, last := uint64(1), uint64(400)
	if s := os.Getenv("ERGO_SHRINK_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, last = n, n
	}
	reached := map[string]bool{}
	for seed := first; seed <= last; seed++ {
		r := rand.New(rand.NewPCG(seed, 0xf71))
		tape := make([]uint64, 1+r.IntN(20))
		for i := range tape {
			switch r.IntN(3) {
			case 0:
				tape[i] = r.Uint64()
			case 1:
				tape[i] = uint64(r.IntN(1000))
			}
		}
		text, fails := tapePredicate(r, tape)
		if !fails(tape) {
			continue
		}
		budget := shrinkBudget
		if r.IntN(6) == 0 {
			budget = 1 + r.IntN(30)
		}
		var gotTries, wantTries [][]uint64
		got, gotN := shrinkTape(tape, func(c []uint64) bool {
			gotTries = append(gotTries, slices.Clone(c))
			return fails(c)
		}, budget)
		want, wantN := v1.ShrinkTape(tape, func(c []uint64) bool {
			wantTries = append(wantTries, slices.Clone(c))
			return fails(c)
		}, budget)
		if !slices.Equal(got, want) || gotN != wantN || len(gotTries) != len(wantTries) {
			t.Fatalf("seed %d: predicate %s, tape %v, budget %d: evaluator %v in %d tries, runtime %v in %d\nreplay: ERGO_SHRINK_SEED=%d go test -run '^TestShrinkAgreesWithRuntime$' ./internal/sketch/eval", seed, text, tape, budget, got, gotN, want, wantN, seed)
		}
		for i := range gotTries {
			if !slices.Equal(gotTries[i], wantTries[i]) {
				t.Fatalf("seed %d: predicate %s, tape %v: try %d is %v, the runtime's %v\nreplay: ERGO_SHRINK_SEED=%d go test -run '^TestShrinkAgreesWithRuntime$' ./internal/sketch/eval", seed, text, tape, i, gotTries[i], wantTries[i], seed)
			}
		}
		if len(got) < len(tape) || !slices.Equal(got, tape) {
			reached["shrunk"] = true
		}
		if gotN >= budget {
			reached["budget"] = true
		}
	}
	if first != last && (!reached["shrunk"] || !reached["budget"]) {
		t.Fatalf("the seeds reached %v; want a shrink that keeps a smaller tape and one that spends its budget", reached)
	}
}

// TestShrinkReachesLeast holds a refuted drawn case to its shrunk form
// (TESTSPEED FT1): for seeded bounds K and L, a test refuted when n >= K
// and a law refuted when len(xs) >= L report n = K and L zeros, the least
// failing case, as the native runner's shrink of the same tape does, with
// the drawn arguments in Drawn when they differ.
//
// Mutants only it kills in process (testdata/mutants.json):
// oracle-ft1-no-shrink, oracle-ft1-int-drawn-unrecorded,
// oracle-ft1-drawn-dropped.
func TestShrinkReachesLeast(t *testing.T) {
	for seed := uint64(1); seed <= 12; seed++ {
		r := rand.New(rand.NewPCG(seed, 0x1ea5))
		k, l := 1+r.IntN(1000), 1+r.IntN(5)
		src := fmt.Sprintf("test Least(n int, m int8) {\n\tassert n < %d\n}\n\nlaw Short(xs []int) {\n\tlen(xs) < %d\n}\n", k, l)
		res := evalSeeded(t, src, seed)
		zeros := "[]int{" + strings.TrimSuffix(strings.Repeat("0, ", l), ", ") + "}"
		want := map[string][]Argument{
			"Least":     {{Name: "n", Value: strconv.Itoa(k)}, {Name: "m", Value: "0"}},
			"law Short": {{Name: "xs", Value: zeros}},
		}
		for _, got := range res.Results {
			w, ok := want[got.Name]
			if !ok {
				continue
			}
			delete(want, got.Name)
			if got.Status != Refuted || !slices.Equal(got.Arguments, w) {
				t.Fatalf("seed %d: %s: %s %v; want refuted %v\n%s", seed, got.Name, got.Status, got.Arguments, w, src)
			}
			if got.Drawn == nil {
				t.Fatalf("seed %d: %s: no drawn arguments beside the shrunk %v\n%s", seed, got.Name, got.Arguments, src)
			}
		}
		if len(want) > 0 {
			t.Fatalf("seed %d: no result for %v\n%s", seed, want, src)
		}
	}
}

// evalSeeded checks src as package main and evaluates it under seed.
func evalSeeded(t *testing.T, src string, seed uint64) Report {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("main", []*syntax.File{f}, nil)
	if len(ds) != 0 {
		t.Fatalf("%v\n%s", ds, src)
	}
	b := DefaultBudget()
	b.Seed = seed
	res, err := Run(pkg, b)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// tapePredicate is a seeded failure predicate on tapes: a conjunction of
// one to three clauses on the length, a draw's size, a draw's parity (not
// monotone: lowering by one leaves it) and the sum of the draws.
func tapePredicate(r *rand.Rand, tape []uint64) (string, func([]uint64) bool) {
	at := func(c []uint64, i int) uint64 {
		if i < len(c) {
			return c[i]
		}
		return 0
	}
	var texts []string
	var clauses []func([]uint64) bool
	for range 1 + r.IntN(3) {
		i := r.IntN(len(tape))
		switch r.IntN(4) {
		case 0:
			n := r.IntN(len(tape) + 1)
			texts = append(texts, fmt.Sprintf("len >= %d", n))
			clauses = append(clauses, func(c []uint64) bool { return len(c) >= n })
		case 1:
			b := tape[i] / uint64(1+r.IntN(5))
			texts = append(texts, fmt.Sprintf("c[%d] >= %d", i, b))
			clauses = append(clauses, func(c []uint64) bool { return at(c, i) >= b })
		case 2:
			odd := tape[i] % 2
			texts = append(texts, fmt.Sprintf("c[%d]%%2 == %d", i, odd))
			clauses = append(clauses, func(c []uint64) bool { return at(c, i)%2 == odd })
		default:
			var sum uint64
			for _, x := range tape {
				sum += x % 1000
			}
			b := sum / uint64(1+r.IntN(4))
			texts = append(texts, fmt.Sprintf("sum mod 1000 >= %d", b))
			clauses = append(clauses, func(c []uint64) bool {
				var s uint64
				for _, x := range c {
					s += x % 1000
				}
				return s >= b
			})
		}
	}
	return fmt.Sprint(texts), func(c []uint64) bool {
		for _, f := range clauses {
			if !f(c) {
				return false
			}
		}
		return true
	}
}
