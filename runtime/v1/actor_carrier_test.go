package v1

import (
	"math/rand"
	"testing"
	"testing/synctest"
)

// TestEndedActorLeavesNoGoroutine: an actor's timer children run on one
// goroutine kept for its life (rt.Carrier, FU-actor-timer-slot), and the
// actor's end closes it. Each of the first generated cases of
// TestActorModelAgreesWithTimedFold's seed 1 runs in a bubble, which fails
// when a goroutine is left parked after the case. Mutant
// actor-skips-carrier-close (the end leaves the carrier open) is killed
// only here.
func TestEndedActorLeavesNoGoroutine(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	seen := map[string]bool{}
	for n := range 40 {
		c := genActorCase(r)
		synctest.Test(t, func(t *testing.T) {
			if diff := runActorCase(c, seen); diff != "" {
				t.Fatalf("case %d: %s", n, diff)
			}
		})
	}
	if !seen["a timer fired"] {
		t.Fatal("no case fired a timer: the carrier ran no child")
	}
}
