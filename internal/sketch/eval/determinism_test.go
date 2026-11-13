package eval

import (
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TestEvalReportIsDeterministic is the seeded determinism property of the
// check-time evaluator (FU-audit; found by TEST-one r2: `ergo check
// examples/ssg/memory` printed different step counts on repeated runs):
// one program evaluated again and again gives the same report, every count
// included. Go's map iteration order changes from one range to the next,
// so an evaluator that charges work in the order it ranges over a Map
// counts differently each run.
//
// Each seed writes properties over records that hold Maps: a property that
// passes (so every case's arguments are described, and their Map keys
// sorted), and one that compares two Maps that differ in one entry (so
// Map equality stops at the first different entry it meets). Each program
// is run eight times. A failing seed prints the source, both reports and
// the replay command.
//
// Mutants it alone catches (testdata/mutants.json): det-argument-order
// (the argument text sorts the keys in the Map's own order) and
// det-equal-order (Map equality walks the Map's own order).
func TestEvalReportIsDeterministic(t *testing.T) {
	first, last := uint64(1), uint64(16)
	if s := os.Getenv("ERGO_DET_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, last = n, n
	}
	for seed := first; seed <= last; seed++ {
		src := deterministicProgram(seed)
		pkg := checked(t, src)
		b := DefaultBudget()
		b.Seed = seed
		want, err := Run(pkg, b)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, src)
		}
		for run := 2; run <= 8; run++ {
			got, err := Run(pkg, b)
			if err != nil {
				t.Fatalf("seed %d: %v\n%s", seed, err, src)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("seed %d: run %d differs from run 1\n%s\nrun 1: %+v\nrun %d: %+v\nreplay: ERGO_DET_SEED=%d go test -run '^TestEvalReportIsDeterministic$' ./internal/sketch/eval", seed, run, src, want, run, got, seed)
			}
		}
	}
}

// deterministicProgram is the property's program for seed: a record of two
// Maps of a seeded key type, a property over it that passes, and one that
// compares a Map with a copy that differs in one entry.
func deterministicProgram(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, 0xde7))
	key, lit := "string", func(i int) string { return strconv.Quote(fmt.Sprintf("k%d", i)) }
	if r.IntN(2) == 0 {
		key, lit = "int", func(i int) string { return strconv.Itoa(i * 7) }
	}
	var b strings.Builder
	fmt.Fprintf(&b, "type Pair { A Map[%s, int], B Map[%s, string] }\n\n", key, key)
	fmt.Fprintf(&b, "test Sizes(p Pair) { assert len(p.A) >= 0 && len(p.B) >= 0 }\n\n")
	n := 4 + r.IntN(12)
	var entries []string
	for i := range n {
		// Equality charges a string by its length, so each entry costs.
		entries = append(entries, fmt.Sprintf("%s: %q", lit(i), strings.Repeat("v", 1+i)))
	}
	fmt.Fprintf(&b, "test Differ {\n\tlet a = Map[%s, string]{%s}\n\tlet mut c = a\n\tc[%s] = \"changed\"\n\tassert a != c\n}\n", key, strings.Join(entries, ", "), lit(r.IntN(n)))
	return b.String()
}
