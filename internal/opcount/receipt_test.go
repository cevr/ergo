package opcount_test

import (
	"math"
	"math/rand/v2"
	"testing"

	"ergo.local/prototype/internal/opcount"
)

// The receipt's numbers: the slope of runs whose cost is a fixed part plus
// a cost per unit is that cost per unit, whatever the sizes and the fixed
// part; a series or a comparison with a refusal holds no number, however
// many values the rounds gave. A seeded property; a failing seed prints
// its inputs.
//
// Mutants: slope-not-difference, series-refused-value,
// compare-refused-ratio.
func TestReceiptNumbers(t *testing.T) {
	for seed := range uint64(500) {
		r := rand.New(rand.NewPCG(seed, 13))
		fixed, per := float64(r.IntN(1e8)), float64(1+r.IntN(5000))
		small := 1 + r.IntN(1e6)
		large := small + 1 + r.IntN(1e7)
		got := opcount.Slope(fixed+per*float64(small), fixed+per*float64(large), small, large)
		if math.Abs(got-per) > 1e-6*per {
			t.Fatalf("seed %d: Slope of %v + %v n at %d and %d = %v, want %v", seed, fixed, per, small, large, got, per)
		}
		values := []float64{per, per + 1, per + 2}
		refused := &opcount.Refused{ID: opcount.Multiplexed, Reason: "test"}
		clean := opcount.NewSeries("control", "Instructions", opcount.Counted, true, values, nil)
		s := opcount.NewSeries("ergo", "Instructions", opcount.Counted, true, values, refused)
		if s.Values != nil || s.Estimate != nil || s.Spread != nil || s.Refusal == nil {
			t.Fatalf("seed %d: a refused series holds %+v", seed, s)
		}
		for _, c := range []opcount.Comparison{opcount.Compare(clean, s), opcount.Compare(s, clean)} {
			if c.Ratio != nil || c.Verdict == nil || c.Verdict.Kind != "Refused" {
				t.Fatalf("seed %d: a comparison with a refused side holds %+v", seed, c)
			}
		}
	}
}
