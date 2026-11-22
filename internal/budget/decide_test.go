package budget

import (
	"fmt"
	"math/rand/v2"
	"slices"
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
// State.Next, never waits while it fits in both), NoIdleBehindBlockedHead
// (while the next waiter does not fit and has been passed fewer than
// Backfill times, no other waiter fits), HeadWithinBound (no waiter is
// passed more than Backfill times), and the takes themselves: each step
// admits exactly what refHandOn, the model's hand-on written apart,
// admits from the same state (InOrder, and a pass takes the oldest waiter
// that fits). Backfill is 0 to 2. A failing seed prints its trace.
//
// Mutants (testdata/mutants.json): budget-admit-by-count,
// budget-admit-by-cores, budget-newest-first, budget-release-hands-nothing,
// budget-no-backfill, budget-backfill-unbounded, budget-pass-ignores-memory,
// budget-pass-not-oldest.
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
	r := Rules{Cores: 4, Memory: 6, Share: 1 + rng.IntN(2), Backfill: rng.IntN(3)}
	var s State
	var trace strings.Builder
	next := 0
	for step := 0; step < 80; step++ {
		var admitted []string
		ref := State{Holders: slices.Clone(s.Holders), Waiters: slices.Clone(s.Waiters), NextTicket: s.NextTicket}
		if rng.IntN(10) < 6 {
			e := Entry{ID: fmt.Sprint("r", next), Width: 1 + rng.IntN(3), Memory: 1 + rng.Int64N(4), Crit: rng.IntN(3) == 0}
			next++
			fmt.Fprintf(&trace, "join %s width %d memory %d crit %v\n", e.ID, e.Width, e.Memory, e.Crit)
			admitted = s.Join(r, e)
			e.Ticket = ref.NextTicket
			ref.NextTicket++
			ref.Waiters = append(ref.Waiters, e)
		} else {
			all := append(append([]Entry(nil), s.Holders...), s.Waiters...)
			if len(all) == 0 {
				continue
			}
			id := all[rng.IntN(len(all))].ID
			fmt.Fprintf(&trace, "end %s\n", id)
			admitted = s.Remove(r, id)
			ref.Holders = slices.DeleteFunc(ref.Holders, func(e Entry) bool { return e.ID == id })
			ref.Waiters = slices.DeleteFunc(ref.Waiters, func(e Entry) bool { return e.ID == id })
		}
		if want := refHandOn(&ref, r); !slices.Equal(admitted, want) {
			return trace.String(), fmt.Sprintf("the step admitted %v; the model admits %v", admitted, want)
		}
		if w, ok := eligibleNext(s, r); ok && (s.Used()+w.Width > r.Cores || s.MemoryUsed()+w.Memory > r.Memory) {
			if w.Passed > r.Backfill {
				return trace.String(), fmt.Sprintf("HeadWithinBound: %s passed %d times, bound %d", w.ID, w.Passed, r.Backfill)
			}
			for _, o := range s.Waiters {
				if w.Passed < r.Backfill && o.ID != w.ID && s.Used()+o.Width <= r.Cores && s.MemoryUsed()+o.Memory <= r.Memory {
					return trace.String(), fmt.Sprintf("NoIdleBehindBlockedHead: %s waits and fits behind %s (passed %d of %d)", o.ID, w.ID, w.Passed, r.Backfill)
				}
			}
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
	}
	return "", ""
}

// refHandOn is the model's hand-on (cores.ergo handOn), written apart from
// State.Admit over the reference eligibleNext: serve the next waiter while
// it fits; when it does not, the waiter with the lowest ticket that fits
// takes past it, at most r.Backfill times for one next waiter. It answers
// the IDs it admitted, in order.
func refHandOn(s *State, r Rules) []string {
	fit := func(e Entry) bool {
		return s.Used()+e.Width <= r.Cores && s.MemoryUsed()+e.Memory <= r.Memory
	}
	var moved []string
	for {
		h, ok := eligibleNext(*s, r)
		if !ok {
			return moved
		}
		take := h.ID
		if !fit(h) {
			if h.Passed >= r.Backfill {
				return moved
			}
			take = ""
			var ticket int64
			for _, w := range s.Waiters {
				if w.ID != h.ID && fit(w) && (take == "" || w.Ticket < ticket) {
					take, ticket = w.ID, w.Ticket
				}
			}
			if take == "" {
				return moved
			}
			for i := range s.Waiters {
				if s.Waiters[i].ID == h.ID {
					s.Waiters[i].Passed++
				}
			}
		}
		for i, w := range s.Waiters {
			if w.ID == take {
				w.Passed = 0
				s.Waiters = slices.Delete(s.Waiters, i, i+1)
				s.Holders = append(s.Holders, w)
				break
			}
		}
		moved = append(moved, take)
	}
}

// TestReservationIsTheLargestPeak: a command's reservation is what it
// asks for (capped at it), else its largest peak and a quarter (capped at
// that or the default, the larger), else the default (not capped: a
// command never measured is never killed by its own cap), and never over
// the budget (mem.ergo); a run killed at its own cap leaves twice it, and
// an uncapped run leaves its real peak. Mutants: budget-killed-keeps-cap,
// budget-cap-unknown.
func TestReservationIsTheLargestPeak(t *testing.T) {
	r := Rules{Cores: 4, Memory: 100}
	for _, c := range []struct {
		asked, peak, dflt, want, capAt int64
	}{
		{0, 0, 8, 8, 0}, {0, 40, 8, 50, 50}, {0, 4, 8, 5, 8}, {50, 30, 8, 50, 50}, {4, 0, 8, 4, 4}, {0, 300, 8, 100, 100}, {0, 4, 200, 5, 100},
	} {
		if got, capAt := Reservation(r, c.asked, c.peak, c.dflt); got != c.want || capAt != c.capAt {
			t.Errorf("Reservation(asked %d, peak %d, default %d) = %d, cap %d; want %d, cap %d", c.asked, c.peak, c.dflt, got, capAt, c.want, c.capAt)
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
	s := State{Holders: []Entry{{ID: "a", Width: 1, Memory: 8}}, Waiters: []Entry{{ID: "b", Width: 1, Memory: 50, Cap: 50, Key: "k"}}, NextTicket: 1}
	s.Remove(r, "a")
	if len(s.Holders) != 1 || s.Holders[0].Memory != 8 || s.Holders[0].Cap != 0 {
		t.Fatalf("b admitted after its key was deleted: %+v; want charged 8, not capped", s.Holders)
	}
}
