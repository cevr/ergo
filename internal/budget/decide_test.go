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

// TestReservationIsTheLargestPeak: a command's reservation is what it
// asks for (capped), else its largest peak and a quarter (capped), else
// the default (not capped: a command never measured is never killed by
// its own cap), and never over the budget (mem.ergo); a run killed at its
// own cap leaves twice it, and an uncapped run leaves its real peak.
// Mutants: budget-killed-keeps-cap, budget-cap-unknown.
func TestReservationIsTheLargestPeak(t *testing.T) {
	r := Rules{Cores: 4, Memory: 100}
	for _, c := range []struct {
		asked, peak, dflt, want int64
		capped                  bool
	}{
		{0, 0, 8, 8, false}, {0, 40, 8, 50, true}, {0, 4, 8, 5, true}, {50, 30, 8, 50, true}, {0, 300, 8, 100, true},
	} {
		if got, capped := Reservation(r, c.asked, c.peak, c.dflt); got != c.want || capped != c.capped {
			t.Errorf("Reservation(asked %d, peak %d, default %d) = %d, capped %v; want %d, capped %v", c.asked, c.peak, c.dflt, got, capped, c.want, c.capped)
		}
	}
	if got := KeptPeak(8, 1, true, 8); got != 16 {
		t.Errorf("a run killed at its own cap of 8 (its shell exiting 1) leaves %d, want 16", got)
	}
	if got := KeptPeak(0, 137, true, 20); got != 20 {
		t.Errorf("an uncapped run killed at a peak of 20 leaves %d, want 20", got)
	}
	// Mutant budget-failed-run-keeps-peak: a run that failed short of its
	// work's peak must not cap the next.
	if got := KeptPeak(0, 1, false, 3); got != 0 {
		t.Errorf("a run that failed, unkilled, at a peak of 3 leaves %d, want none", got)
	}
}

// TestPeakIsReadAtAdmission: the hand-on charges a waiter from its
// command's peak as recorded at its admission: a waiter charged 50 and
// capped at its join (a peak of 40) whose key was deleted while it waited
// (the 2026-11-20 clean-up of poisoned peaks) is admitted at the default
// charge, uncapped. Mutant: budget-charge-at-join.
func TestPeakIsReadAtAdmission(t *testing.T) {
	r := Rules{Cores: 1, Memory: 100, Peak: func(string) int64 { return 0 }, Default: 8}
	s := State{Holders: []Entry{{ID: "a", Width: 1, Memory: 8}}, Waiters: []Entry{{ID: "b", Width: 1, Memory: 50, Capped: true, Key: "k"}}, NextTicket: 1}
	s.Remove(r, "a")
	if len(s.Holders) != 1 || s.Holders[0].Memory != 8 || s.Holders[0].Capped {
		t.Fatalf("b admitted after its key was deleted: %+v; want charged 8, not capped", s.Holders)
	}
}
