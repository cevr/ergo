package eval

import (
	"ergo.local/prototype/runtime/testgen"
	"math/rand/v2"
	"testing"
)

// TestStreamCaseLaws: a generated schedule of case boundaries preserves the
// draw sequence and memoizes a builder only until the next boundary. These laws
// catch case-reseeds-stream and case-retains-world even when both compiler
// adapters share the same defective stream (native agreement cannot do that).
func TestStreamCaseLaws(t *testing.T) {
	for seed := uint64(0); seed < 100; seed++ {
		if testgen.New(seed, "binding-a").Next() == testgen.New(seed, "binding-b").Next() {
			t.Fatalf("seed %d: the name did not select an independent stream", seed)
		}
		schedule := rand.New(rand.NewPCG(seed, 41))
		a, b := testgen.New(seed, "law for provider"), testgen.New(seed, "law for provider")
		calls, current := 0, 0
		for k := 1; k <= 100; k++ {
			if schedule.IntN(3) == 0 || k == 1 {
				a.Start(k)
				current++
			}
			if a.Next() != b.Next() {
				t.Fatalf("seed %d case %d: a boundary changed the stream", seed, k)
			}
			for j := 0; j < 1+schedule.IntN(4); j++ {
				got := a.Built("world", func() any { calls++; return calls }).(int)
				if got != current || calls != current {
					t.Fatalf("seed %d case %d: shared world %d, builds %d, expected %d", seed, k, got, calls, current)
				}
			}
		}
	}
}
