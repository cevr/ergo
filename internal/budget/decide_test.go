package budget

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// TestHandOnKeepsTheModelsInvariants: the decision is the model's
// (models/gatebudget cores.ergo). A seeded property drives State through
// random joins, releases, crashes (a removal is both) and load changes,
// for runners of widths 1 to 3 in both classes, and checks the model's
// invariants after every step: WithinBudget (the widths held are at most
// B), NextNotIdle (the next waiter never waits while it fits, unless it is
// of the lane class and the load is high) and InOrder (no take passes an
// older waiter of its own class). A failing seed prints its trace.
//
// Mutants (testdata/mutants.json): budget-admit-by-count,
// budget-gate-all, budget-newest-first, budget-release-hands-nothing,
// budget-gated-head-blocks.
func TestHandOnKeepsTheModelsInvariants(t *testing.T) {
	for seed := uint64(1); seed <= 300; seed++ {
		if trace, why := handOnRun(seed); why != "" {
			t.Fatalf("seed %d: %s\n%s\nreplay: go test ./internal/budget -run TestHandOnKeepsTheModelsInvariants (seed %d)", seed, why, trace, seed)
		}
	}
}

// eligibleNext is the reference for the next waiter, written apart from
// State.Next (the first build's property asked Next itself, and so missed
// its gated head): the eligible waiters are all of them, or only crit
// while the load is high; of those, the oldest lane waiter while the lane
// class holds less than its share, else the oldest crit, else the oldest
// lane waiter.
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
		case !w.Crit && !r.High && (lane == nil || w.Ticket < lane.Ticket):
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
	r := Rules{Cores: 4, Share: 1 + rng.IntN(2)}
	var s State
	var trace strings.Builder
	next := 0
	for step := 0; step < 80; step++ {
		var admitted []string
		switch k := rng.IntN(10); {
		case k < 5:
			e := Entry{ID: fmt.Sprint("r", next), Width: 1 + rng.IntN(3), Crit: rng.IntN(3) == 0}
			next++
			fmt.Fprintf(&trace, "join %s width %d crit %v\n", e.ID, e.Width, e.Crit)
			admitted = s.Join(r, e)
		case k < 8:
			all := append(append([]Entry(nil), s.Holders...), s.Waiters...)
			if len(all) == 0 {
				continue
			}
			id := all[rng.IntN(len(all))].ID
			fmt.Fprintf(&trace, "end %s\n", id)
			admitted = s.Remove(r, id)
		default:
			r.High = !r.High
			fmt.Fprintf(&trace, "load high %v\n", r.High)
			admitted = s.Admit(r)
		}
		if s.Used() > r.Cores {
			return trace.String(), fmt.Sprintf("WithinBudget: %d cores held of %d", s.Used(), r.Cores)
		}
		if w, ok := eligibleNext(s, r); ok && s.Used()+w.Width <= r.Cores {
			return trace.String(), fmt.Sprintf("NextNotIdle: %s waits and fits (%d of %d held, load high %v)", w.ID, s.Used(), r.Cores, r.High)
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
