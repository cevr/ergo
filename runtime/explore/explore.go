// Package explore decides a machine law on the machine's graph (types.md,
// Laws 13): a bounded breadth-first search with a visited set of hashed
// state keys. The test build of an Ergo program calls it for each
// reachability law (lower machinelaws.go); the lowering writes the key of
// the machine's state type as a Go function over Key.
//
// The search keeps, for each state it has seen, the 128-bit hash of the
// state's key, the index of its parent and the index of its signal; it
// keeps no whole states in the frontier. A run to a state is rebuilt
// from the parents and replayed from its start by the step. The visited
// data is a 16-byte hash (plus map overhead), an 8-byte node and frontier
// indexes; payloads do not multiply by the frontier width. The search
// stops when it has seen budget states: its result then is not a proof.
// It counts work (states, edges, depth); it never reads a clock.
//
// Prior art: TLC's breadth-first search with 64-bit state fingerprints;
// P's PEx keeps hashes of visited states; Quint's simulator keeps no
// state and samples runs. A hash collision can hide a state, so the hash
// is 128 bits wide (about 10^-25 for one collision in 10^7 states).
package explore

import (
	"encoding/binary"
	"math"
	"math/bits"
	"slices"
)

// Hash is the 128-bit hash of a state's key.
type Hash [16]byte

// Key is the canonical encoding of an Ergo value, written by the lowered
// key function of its type: identical values have equal bytes, and values
// that a step can tell apart unequal bytes (a float by its bits, so 0 and
// -0 are apart though 0 == -0). A record writes its fields in declaration order; a
// union writes its variant's index, then the variant; a slice its length,
// then its elements; a Map its length, then its entries in the order of
// their encodings, so the order of Go's map iteration does not show.
type Key struct{ buf []byte }

// Reset empties k.
func (k *Key) Reset() { k.buf = k.buf[:0] }

// Bytes is k's encoding so far.
func (k *Key) Bytes() []byte { return k.buf }

// Uint writes v.
func (k *Key) Uint(v uint64) { k.buf = binary.AppendUvarint(k.buf, v) }

// Int writes v.
func (k *Key) Int(v int64) { k.buf = binary.AppendVarint(k.buf, v) }

// Bool writes v.
func (k *Key) Bool(v bool) {
	if v {
		k.buf = append(k.buf, 1)
	} else {
		k.buf = append(k.buf, 0)
	}
}

// String writes s with its length.
func (k *Key) String(s string) {
	k.Uint(uint64(len(s)))
	k.buf = append(k.buf, s...)
}

// Float writes v by its bits, as they are: -0 apart from 0, though 0 ==
// -0, since a step can tell them apart ("\(x)" shows -0, 1/x is -Inf), and
// each NaN by its payload, which math.Float64bits shows. A key merges two
// states only when no step can tell them apart (batch-15 X1 M1).
func (k *Key) Float(v float64) {
	k.buf = binary.LittleEndian.AppendUint64(k.buf, math.Float64bits(v))
}

// Complex writes v's two parts.
func (k *Key) Complex(v complex128) {
	k.Float(real(v))
	k.Float(imag(v))
}

// Tag writes the index of a union's variant, or a collection's length.
func (k *Key) Tag(i int) { k.Uint(uint64(i)) }

// Map writes m's length, then each entry, as entry writes it, in the order
// of the entries' encodings.
func Map[K comparable, V any](k *Key, m map[K]V, entry func(*Key, K, V)) {
	k.Tag(len(m))
	if len(m) == 0 {
		return
	}
	var sub Key
	entries := make([][]byte, 0, len(m))
	for a, b := range m {
		sub.buf = nil
		entry(&sub, a, b)
		entries = append(entries, sub.buf)
	}
	slices.SortFunc(entries, func(a, b []byte) int { return compareBytes(a, b) })
	for _, e := range entries {
		k.Uint(uint64(len(e)))
		k.buf = append(k.buf, e...)
	}
}

func compareBytes(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return len(a) - len(b)
}

// Sum is the 128-bit FNV-1a hash of k's encoding.
func (k *Key) Sum() Hash {
	// FNV-1a 128 (offset basis and prime of the FNV specification), on
	// two 64-bit halves: hi:lo.
	hi, lo := uint64(0x6c62272e07bb0142), uint64(0x62b821756295c58d)
	const primeLo = 0x13b // the prime is 2^88 + 0x13b
	for _, c := range k.buf {
		lo ^= uint64(c)
		// (hi:lo) * (2^88 + primeLo) mod 2^128
		h1, l1 := bits.Mul64(lo, primeLo)
		h1 += hi * primeLo
		h1 += lo << 24 // lo * 2^88 lands in the high half, shifted by 24
		hi, lo = h1, l1
	}
	var h Hash
	binary.BigEndian.PutUint64(h[:8], hi)
	binary.BigEndian.PutUint64(h[8:], lo)
	return h
}

// Graph is a machine as the search sees it: its starts, its signals, its
// step as an edge (false when the step ends the run, which is no edge),
// and the key of its state.
type Graph[S, E any] struct {
	Starts  []S
	Signals []E
	Step    func(S, E) (S, bool)
	Key     func(*Key, S)
}

// node is a state the search has seen: from the start Starts[sig] when
// parent is -1, else from the node parent by the signal Signals[sig].
type node struct{ parent, sig int32 }

// Result is what a search did.
type Result struct {
	// Found is the node of the first state the search met that is a
	// target (for Path, of the last layer), or -1.
	Found int
	// States is the number of states seen; Edges the number of steps
	// taken that did not end the run.
	States, Edges int
	// Depth is the longest shortest run of a state seen. Checked is the
	// longest run length up to which every run was checked: Depth when
	// the search is Complete, else one less than the depth of the layer
	// the budget stopped.
	Depth, Checked int
	// Complete is true when the whole graph was seen and no target met.
	Complete bool
	// FrontierStates is the peak number of whole state payloads retained in
	// the pending layers (not the caller's Starts or the state being stepped).
	// It counts retained work, not heap samples or elapsed time.
	FrontierStates int
	nodes          []node
}

// Run is the run to the node i: the index of its start in Starts, and the
// indexes of its signals in Signals, in order.
func (r Result) Run(i int) (start int, signals []int) {
	for r.nodes[i].parent >= 0 {
		signals = append(signals, int(r.nodes[i].sig))
		i = int(r.nodes[i].parent)
	}
	slices.Reverse(signals)
	return int(r.nodes[i].sig), signals
}

// Replay is the state at the end of a run, stepped from its start.
func Replay[S, E any](g Graph[S, E], start int, signals []int) S {
	s := g.Starts[start]
	for _, e := range signals {
		s, _ = g.Step(s, g.Signals[e])
	}
	return s
}

// Search explores g breadth first from its starts, the starts included,
// until it meets a state for which target is true or it has seen budget
// states. The first target it meets has a shortest run.
func Search[S, E any](g Graph[S, E], target func(S) bool, budget int) Result {
	return search(g, target, budget, false)
}

// retain selects the old whole-frontier implementation as a reference in
// the seeded property. Production reconstructs each state from its run.
func search[S, E any](g Graph[S, E], target func(S) bool, budget int, retain bool) Result {
	r := Result{Found: -1}
	seen := map[Hash]struct{}{}
	var k Key
	visit := func(s S) bool {
		k.Reset()
		g.Key(&k, s)
		h := k.Sum()
		if _, ok := seen[h]; ok {
			return false
		}
		seen[h] = struct{}{}
		return true
	}
	var frontier []S
	var ids []int
	for i, s := range g.Starts {
		if !visit(s) {
			continue
		}
		if len(r.nodes) == budget {
			r.Checked = -1
			return r
		}
		r.nodes = append(r.nodes, node{parent: -1, sig: int32(i)})
		r.States++
		if target(s) {
			r.Found = len(r.nodes) - 1
			return r
		}
		if retain {
			frontier = append(frontier, s)
		}
		r.FrontierStates = max(r.FrontierStates, len(frontier))
		ids = append(ids, len(r.nodes)-1)
	}
	for depth := 1; len(ids) > 0; depth++ {
		var next []S
		var nextIDs []int
		for f := range ids {
			s := frontierState(g, r, frontier, f, ids[f])
			for e, sig := range g.Signals {
				t, ok := g.Step(s, sig)
				if !ok {
					continue
				}
				r.Edges++
				if !visit(t) {
					continue
				}
				if len(r.nodes) == budget {
					r.Checked = depth - 1
					return r
				}
				r.nodes = append(r.nodes, node{parent: int32(ids[f]), sig: int32(e)})
				r.States++
				r.Depth = depth
				if target(t) {
					r.Found = len(r.nodes) - 1
					return r
				}
				if retain {
					next = append(next, t)
				}
				r.FrontierStates = max(r.FrontierStates, len(frontier)+len(next))
				nextIDs = append(nextIDs, len(r.nodes)-1)
			}
		}
		frontier, ids = next, nextIDs
	}
	r.Checked = r.Depth
	r.Complete = true
	return r
}

// Path looks for a run whose first len(targets) states meet targets in
// order: layer k holds the distinct states that a run meeting targets[0]
// to targets[k] reaches. It stops at budget states over all layers.
// Found is a node of the last layer, or -1; Complete is true when the
// search ended without the budget (so Found is -1 only when no run
// follows the path).
func Path[S, E any](g Graph[S, E], targets []func(S) bool, budget int) Result {
	return path(g, targets, budget, false)
}

func path[S, E any](g Graph[S, E], targets []func(S) bool, budget int, retain bool) Result {
	r := Result{Found: -1}
	var k Key
	layer := map[Hash]struct{}{}
	visit := func(s S) bool {
		k.Reset()
		g.Key(&k, s)
		h := k.Sum()
		if _, ok := layer[h]; ok {
			return false
		}
		layer[h] = struct{}{}
		return true
	}
	var frontier []S
	var ids []int
	for i, s := range g.Starts {
		if targets[0](s) && visit(s) {
			if len(r.nodes) == budget {
				return r
			}
			r.nodes = append(r.nodes, node{parent: -1, sig: int32(i)})
			if retain {
				frontier = append(frontier, s)
			}
			r.FrontierStates = max(r.FrontierStates, len(frontier))
			ids = append(ids, len(r.nodes)-1)
		}
	}
	for d := 1; d < len(targets) && len(ids) > 0; d++ {
		layer = map[Hash]struct{}{}
		var next []S
		var nextIDs []int
		for f := range ids {
			s := frontierState(g, r, frontier, f, ids[f])
			for e, sig := range g.Signals {
				t, ok := g.Step(s, sig)
				if !ok {
					continue
				}
				r.Edges++
				if !targets[d](t) || !visit(t) {
					continue
				}
				if len(r.nodes) == budget {
					r.States = len(r.nodes)
					return r
				}
				r.nodes = append(r.nodes, node{parent: int32(ids[f]), sig: int32(e)})
				if retain {
					next = append(next, t)
				}
				r.FrontierStates = max(r.FrontierStates, len(frontier)+len(next))
				nextIDs = append(nextIDs, len(r.nodes)-1)
			}
		}
		frontier, ids = next, nextIDs
		r.Depth = d
	}
	r.States = len(r.nodes)
	r.Checked = r.Depth
	r.Complete = true
	if len(ids) > 0 {
		r.Found = ids[0]
	}
	return r
}

// frontierState materializes only the state being stepped. A wide layer
// keeps indexes, not copies of payload-rich model worlds (batch-15 r7).
func frontierState[S, E any](g Graph[S, E], r Result, states []S, at, id int) S {
	if states != nil {
		return states[at]
	}
	start, signals := r.Run(id)
	return Replay(g, start, signals)
}

// Moves is the indexes of the signals whose step from s does not end
// the run and goes to a state with another key: the moves a random walk
// picks from, so it does not spend its steps on Ignore and Reject.
func Moves[S, E any](g Graph[S, E], s S) []int {
	var k Key
	g.Key(&k, s)
	here := slices.Clone(k.Bytes())
	var out []int
	for e, sig := range g.Signals {
		t, ok := g.Step(s, sig)
		if !ok {
			continue
		}
		k.Reset()
		g.Key(&k, t)
		if !slices.Equal(here, k.Bytes()) {
			out = append(out, e)
		}
	}
	return out
}
