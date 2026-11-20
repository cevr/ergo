package budget

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// TestHandOnKeepsTheModelsInvariants: the decision is the model's
// (models/gatebudget cores.ergo, mem.ergo). A seeded property drives State
// through random joins, releases and crashes (a removal is both), for
// runners of widths 1 to 3 and reservations 1 to 4 in both classes, and
// checks the model's invariants after every step: WithinBudget and
// WithinMemory (the widths and reservations held are at most the budget),
// NextNotIdle (the next waiter, by a reference written apart from
// State.Next, never waits while it fits in both) and InOrder (no take
// passes an older waiter of its own class). A failing seed prints its
// trace.
//
// Mutants (testdata/mutants.json): budget-admit-by-count,
// budget-admit-by-cores, budget-newest-first, budget-release-hands-nothing.
func TestHandOnKeepsTheModelsInvariants(t *testing.T) {
	for seed := uint64(1); seed <= 300; seed++ {
		if trace, why := handOnRun(seed); why != "" {
			t.Fatalf("seed %d: %s\n%s\nreplay: go test ./internal/budget -run TestHandOnKeepsTheModelsInvariants (seed %d)", seed, why, trace, seed)
		}
	}
}

// eligibleNext is the reference for the next waiter, written apart from
// State.Next: the oldest lane waiter while the lane class holds less than
// its share, else the oldest crit, else the oldest lane waiter.
func eligibleNext(s State, r Rules) (Entry, bool) {
	var lane, crit *Entry
	laneUsed := 0
	for _, h := range s.Holders {
		if !h.Crit {
			laneUsed += h.Width
		}
	}
	for i := range s.Waiters {
		w := &s.Waiters[i]
		switch {
		case w.Crit && (crit == nil || w.Ticket < crit.Ticket):
			crit = w
		case !w.Crit && (lane == nil || w.Ticket < lane.Ticket):
			lane = w
		}
	}
	switch {
	case lane != nil && laneUsed < r.Share:
		return *lane, true
	case crit != nil:
		return *crit, true
	case lane != nil:
		return *lane, true
	}
	return Entry{}, false
}

func handOnRun(seed uint64) (string, string) {
	rng := rand.New(rand.NewPCG(seed, 7))
	r := Rules{Cores: 4, Memory: 6, Share: 1 + rng.IntN(2)}
	var s State
	var trace strings.Builder
	next := 0
	for step := 0; step < 80; step++ {
		var admitted []string
		if rng.IntN(10) < 6 {
			e := Entry{ID: fmt.Sprint("r", next), Width: 1 + rng.IntN(3), Memory: 1 + rng.Int64N(4), Crit: rng.IntN(3) == 0}
			next++
			fmt.Fprintf(&trace, "join %s width %d memory %d crit %v\n", e.ID, e.Width, e.Memory, e.Crit)
			admitted = s.Join(r, e)
		} else {
			all := append(append([]Entry(nil), s.Holders...), s.Waiters...)
			if len(all) == 0 {
				continue
			}
			id := all[rng.IntN(len(all))].ID
			fmt.Fprintf(&trace, "end %s\n", id)
			admitted = s.Remove(r, id)
		}
		if s.Used() > r.Cores {
			return trace.String(), fmt.Sprintf("WithinBudget: %d cores held of %d", s.Used(), r.Cores)
		}
		if s.MemoryUsed() > r.Memory {
			return trace.String(), fmt.Sprintf("WithinMemory: %d reserved of %d", s.MemoryUsed(), r.Memory)
		}
		if w, ok := eligibleNext(s, r); ok && s.Used()+w.Width <= r.Cores && s.MemoryUsed()+w.Memory <= r.Memory {
			return trace.String(), fmt.Sprintf("NextNotIdle: %s waits and fits (%d of %d cores, %d of %d memory held)", w.ID, s.Used(), r.Cores, s.MemoryUsed(), r.Memory)
		}
		for _, id := range admitted {
			var x Entry
			for _, h := range s.Holders {
				if h.ID == id {
					x = h
				}
			}
			for _, w := range s.Waiters {
				if w.Crit == x.Crit && w.Ticket < x.Ticket {
					return trace.String(), fmt.Sprintf("InOrder: %s took while older %s waits", x.ID, w.ID)
				}
			}
		}
	}
	return "", ""
}

// TestReservationIsTheLargestPeak: a command's reservation is what it asks
// for, else the largest peak kept for it, never under the default and
// never over the budget (mem.ergo); a run killed at its reservation leaves
// twice it. Mutant: budget-killed-keeps-cap.
func TestReservationIsTheLargestPeak(t *testing.T) {
	r := Rules{Cores: 4, Memory: 100}
	for _, c := range []struct{ asked, peak, dflt, want int64 }{
		{0, 0, 8, 8}, {0, 30, 8, 30}, {0, 3, 8, 8}, {50, 30, 8, 50}, {0, 300, 8, 100},
	} {
		if got := Reservation(r, c.asked, c.peak, c.dflt); got != c.want {
			t.Errorf("Reservation(asked %d, peak %d, default %d) = %d, want %d", c.asked, c.peak, c.dflt, got, c.want)
		}
	}
	if got := KeptPeak(8, 137, 8); got != 16 {
		t.Errorf("a run killed at its reservation of 8 leaves %d, want 16", got)
	}
	if got := KeptPeak(8, 0, 5); got != 5 {
		t.Errorf("a run that ends at a peak of 5 leaves %d, want 5", got)
	}
}
