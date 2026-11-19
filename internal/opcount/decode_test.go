package opcount

import (
	"math/rand/v2"
	"testing"
)

// The counter's value is a count exactly when the counter ran for all the
// time it was enabled; a short running time (the kernel multiplexed the
// hardware counter) refuses it as Multiplexed, never as a scaled number,
// and a counter that never ran refuses it as NoCounter. A seeded property
// over the three words, with the equal and zero edges drawn often; the
// failing seed prints its words. Through the read seam: a real
// multiplexed run cannot be made on demand.
//
// Mutant: count-multiplexed-admitted.
func TestShortCountIsRefused(t *testing.T) {
	for seed := range uint64(2000) {
		r := rand.New(rand.NewPCG(seed, 7))
		pick := func() uint64 {
			switch r.IntN(4) {
			case 0:
				return 0
			case 1:
				return 1_000_000
			default:
				return r.Uint64N(2_000_000)
			}
		}
		value, enabled, running := r.Uint64(), pick(), pick()
		got, refused, _ := decode(value, enabled, running)
		var want Refusal
		switch {
		case enabled == 0:
			want = NoCounter
		case running < enabled:
			want = Multiplexed
		}
		if refused != want || (want == "" && got != value) || (want != "" && got != 0) {
			t.Fatalf("seed %d: decode(%d, %d, %d) = %d, %q; want %q and the value only when whole", seed, value, enabled, running, got, refused, want)
		}
	}
}
