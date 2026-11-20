// Package budget is the host-wide budget of cores that every Ergo task and
// every `ergo budget -- <cmd>` takes from before it starts work (MONOREPO
// §6.5, unit WS4a). Its design is the model models/gatebudget, checked by
// `ergo test ./models/gatebudget`; each operation here is one of the
// model's transitions, and its doc comment names the invariant it keeps.
//
// The budget is counted in cores. A take names its width (the GOMAXPROCS
// it runs with) and its class, crit or lane. The host's owner sets the
// budget B and the lane share in one file (Config); nothing here reads the
// machine's core count, since the host is shared.
//
// This file is the decision: the state of the budget and the hand-on rule,
// with no file system. store.go keeps the state in a folder of the host,
// one flock per entry, so a crash frees its cores by the kernel.
package budget

import "slices"

// Entry is a runner in the budget: a waiter or a holder.
type Entry struct {
	ID     string `json:"id"`
	Pid    int    `json:"pid"`
	Width  int    `json:"width"`
	Crit   bool   `json:"crit"`
	Ticket int64  `json:"ticket"`
	Lane   string `json:"lane,omitempty"`
}

// State is the budget's state: who holds cores, who waits and in what
// order (the tickets).
type State struct {
	Holders    []Entry `json:"holders"`
	Waiters    []Entry `json:"waiters"`
	NextTicket int64   `json:"next_ticket"`
}

// Rules are what a decision reads besides the state: the budget, the lane
// share, and whether the host's load is high (the interim load gate).
type Rules struct {
	Cores int
	Share int
	High  bool
}

// Used is the cores held.
func (s *State) Used() int {
	n := 0
	for _, h := range s.Holders {
		n += h.Width
	}
	return n
}

func (s *State) laneUsed() int {
	n := 0
	for _, h := range s.Holders {
		if !h.Crit {
			n += h.Width
		}
	}
	return n
}

func oldest(ws []Entry, crit bool) int {
	best := -1
	for i, w := range ws {
		if w.Crit == crit && (best == -1 || w.Ticket < ws[best].Ticket) {
			best = i
		}
	}
	return best
}

// Next is the waiter the hand-on serves next, by the model's rule
// (cores.ergo nextWaiter): the oldest lane waiter while the lane class
// holds fewer than Share cores, else the oldest crit waiter, else the
// oldest lane waiter. A gated waiter is never next: while the load is high
// the lane class is skipped, so a crit waiter behind a gated lane waiter
// is served at once (NextNotIdle; the model's mutant GatedHead, this
// package's first build, chose the gated lane waiter and stopped). It is
// -1 when no one eligible waits.
func (s *State) Next(r Rules) int {
	lane, crit := oldest(s.Waiters, false), oldest(s.Waiters, true)
	if r.High {
		lane = -1
	}
	if lane != -1 && s.laneUsed() < r.Share {
		return lane
	}
	if crit != -1 {
		return crit
	}
	return lane
}

// Fits reports admission by cores (WithinBudget; the model's mutant
// ByCount admits by count).
func (s *State) Fits(r Rules, e Entry) bool { return s.Used()+e.Width <= r.Cores }

// Gated reports the interim load gate: the lane class takes nothing while
// the load is high; crit is never gated (NextNotIdle's one exception; the
// model's mutant GateAll gates crit too). The memory-admission unit
// (WS4c) deletes it.
func Gated(r Rules, e Entry) bool { return r.High && !e.Crit }

// Join is the model's Join: e waits with the next ticket, and the hand-on
// runs (InOrder: a newcomer takes at once only when it is the next waiter
// and fits).
func (s *State) Join(r Rules, e Entry) []string {
	e.Ticket = s.NextTicket
	s.NextTicket++
	s.Waiters = append(s.Waiters, e)
	return s.Admit(r)
}

// Remove is the model's Quit and a crash's end: id's entry goes, and the
// hand-on runs (StartsWhenFree: a release hands the cores on in the same
// step).
func (s *State) Remove(r Rules, id string) []string {
	s.Holders = slices.DeleteFunc(s.Holders, func(e Entry) bool { return e.ID == id })
	s.Waiters = slices.DeleteFunc(s.Waiters, func(e Entry) bool { return e.ID == id })
	return s.Admit(r)
}

// Admit is the hand-on: it moves the next waiter to the holders while it
// fits and is not gated, and returns the IDs it moved. A next waiter that
// does not fit stops it (head of line), so a narrow waiter never passes a
// wide older one (InOrder; the model's mutant Backfill passes it). It runs
// after every change, the load falling included (the model's mutant
// PollAfterLoad does not).
func (s *State) Admit(r Rules) []string {
	var moved []string
	for {
		i := s.Next(r)
		if i == -1 || !s.Fits(r, s.Waiters[i]) || Gated(r, s.Waiters[i]) {
			return moved
		}
		e := s.Waiters[i]
		s.Waiters = slices.Delete(s.Waiters, i, i+1)
		s.Holders = append(s.Holders, e)
		moved = append(moved, e.ID)
	}
}

// Holds reports whether id holds cores.
func (s *State) Holds(id string) bool {
	return slices.ContainsFunc(s.Holders, func(e Entry) bool { return e.ID == id })
}
