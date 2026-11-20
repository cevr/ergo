// Package budget is the host-wide budget of cores and memory that every
// Ergo task and every `ergo budget -- <cmd>` takes from before it starts
// work (MONOREPO §6.5, units WS4a and WS4c). Its design is the model
// models/gatebudget (cores.ergo, mem.ergo, nest.ergo), checked by `ergo
// test ./models/gatebudget`; each operation here is one of the model's
// transitions, and its doc comment names the invariant it keeps.
//
// A take names its width in cores (the GOMAXPROCS it runs with), its
// class, crit or lane, and is charged a reservation in memory. The host's
// owner sets the budget (cores, memory) and the lane share in one file
// (Config); nothing here reads the machine's size, since the host is
// shared.
//
// This file is the decision: the state of the budget and the hand-on rule,
// with no file system. store_unix.go keeps the state in a folder of the
// host, one flock per entry, so a crash frees its share by the kernel.
package budget

import "slices"

// Entry is a runner in the budget: a waiter or a holder.
type Entry struct {
	ID     string `json:"id"`
	Pid    int    `json:"pid"`
	Width  int    `json:"width"`
	Memory int64  `json:"memory,omitempty"`
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

// Rules are what a decision reads besides the state: the budget in cores
// and in memory (bytes; 0: memory is not budgeted), and the lane share.
type Rules struct {
	Cores  int
	Memory int64
	Share  int
}

// Used is the cores held.
func (s *State) Used() int {
	n := 0
	for _, h := range s.Holders {
		n += h.Width
	}
	return n
}

// MemoryUsed is the memory reserved by the holders.
func (s *State) MemoryUsed() int64 {
	var n int64
	for _, h := range s.Holders {
		n += h.Memory
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
// (cores.ergo, mem.ergo mNext): the oldest lane waiter while the lane
// class holds fewer than Share cores, else the oldest crit waiter, else
// the oldest lane waiter. It is -1 when no one waits.
func (s *State) Next(r Rules) int {
	lane, crit := oldest(s.Waiters, false), oldest(s.Waiters, true)
	if lane != -1 && s.laneUsed() < r.Share {
		return lane
	}
	if crit != -1 {
		return crit
	}
	return lane
}

// Fits reports admission by cores and memory together (WithinBudget,
// WithinMemory; the model's mutants ByCount and ByCores admit by count and
// by cores alone).
func (s *State) Fits(r Rules, e Entry) bool {
	return s.Used()+e.Width <= r.Cores && (r.Memory == 0 || s.MemoryUsed()+e.Memory <= r.Memory)
}

// Join is the model's Join: e waits with the next ticket, and the hand-on
// runs (InOrder: a newcomer takes at once only when it is the next waiter
// and fits).
func (s *State) Join(r Rules, e Entry) []string {
	e.Ticket = s.NextTicket
	s.NextTicket++
	s.Waiters = append(s.Waiters, e)
	return s.Admit(r)
}

// Remove is the model's Quit, a kill at the reservation and a crash's end:
// id's entry goes, and the hand-on runs (StartsWhenFree, KillHandsOn: an
// end hands the cores and memory on in the same step).
func (s *State) Remove(r Rules, id string) []string {
	s.Holders = slices.DeleteFunc(s.Holders, func(e Entry) bool { return e.ID == id })
	s.Waiters = slices.DeleteFunc(s.Waiters, func(e Entry) bool { return e.ID == id })
	return s.Admit(r)
}

// Admit is the hand-on: it moves the next waiter to the holders while it
// fits, and returns the IDs it moved. A next waiter that does not fit
// stops it (head of line), so a narrow or small waiter never passes a
// wide or large older one (InOrder; the model's mutant Backfill passes
// it). It runs after every change.
func (s *State) Admit(r Rules) []string {
	var moved []string
	for {
		i := s.Next(r)
		if i == -1 || !s.Fits(r, s.Waiters[i]) {
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

// Reservation is the memory a take is charged (mem.ergo): what it asks
// for, else the largest peak its command has reached (the model's mutant
// LastPeak keeps the last), never less than the default, and never more
// than the whole budget, which no take could then fit.
func Reservation(r Rules, asked, peak, dflt int64) int64 {
	res := asked
	if res <= 0 {
		res = max(peak, dflt)
	}
	if r.Memory > 0 && res > r.Memory {
		res = r.Memory
	}
	return res
}
