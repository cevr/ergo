package explore

import (
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strconv"
	"testing"
)

// state is a node of a generated graph. Its key writes only the set bits:
// the bits of id, held in a Go map, so two states are equal exactly when
// their ids are, and a key that depends on the order of map iteration
// splits one state in two.
type state struct {
	id   int
	bits map[int]bool
}

func mkState(id int) state {
	s := state{id: id, bits: map[int]bool{}}
	for b := 0; b < 7; b++ {
		if id&(1<<b) != 0 {
			s.bits[b] = true
		}
	}
	return s
}

func keyState(k *Key, s state) {
	Map(k, s.bits, func(k *Key, b int, v bool) {
		k.Int(int64(b))
		k.Bool(v)
	})
}

// table is a generated graph: next[id][e] is the state signal e leads to
// from id, -1 when the step ends the run (Done) and id itself for an
// Ignore or a Reject.
type table struct {
	next    [][]int
	starts  []int
	targets []map[int]bool // one set for Search, a sequence for Path
	budget  int
}

func genTable(r *rand.Rand) table {
	n, m := 1+r.Intn(64), 1+r.Intn(4)
	t := table{next: make([][]int, n), budget: 1 + r.Intn(n+8)}
	for i := range t.next {
		t.next[i] = make([]int, m)
		for e := range t.next[i] {
			switch r.Intn(6) {
			case 0:
				t.next[i][e] = -1
			case 1:
				t.next[i][e] = i
			default:
				t.next[i][e] = r.Intn(n)
			}
		}
	}
	for k := 1 + r.Intn(3); k > 0; k-- {
		t.starts = append(t.starts, r.Intn(n))
	}
	for k := 1 + r.Intn(4); k > 0; k-- {
		set := map[int]bool{}
		for c := r.Intn(4); c > 0; c-- {
			set[r.Intn(n)] = true
		}
		if r.Intn(4) == 0 {
			for i := 0; i < n; i++ {
				set[i] = true
			}
		}
		t.targets = append(t.targets, set)
	}
	return t
}

func (t table) graph() Graph[state, int] {
	g := Graph[state, int]{Key: keyState}
	for _, s := range t.starts {
		g.Starts = append(g.Starts, mkState(s))
	}
	for e := range t.next[0] {
		g.Signals = append(g.Signals, e)
	}
	g.Step = func(s state, e int) (state, bool) {
		n := t.next[s.id][e]
		if n < 0 {
			return s, false
		}
		return mkState(n), true
	}
	return g
}

// refResult is what the naive reference reports: the length of the run
// to the first target, or -1; the states seen; the depths; and whether
// the graph was seen whole.
type refResult struct {
	found, states, edges, depth, checked int
	complete                             bool
}

// refSearch is breadth-first search on whole states (ids), in the order
// Search promises: the starts, then each layer's states in order, each
// state's signals in order.
func refSearch(t table) refResult {
	target := t.targets[0]
	r := refResult{found: -1}
	seen := map[int]bool{}
	var frontier []int
	for _, s := range t.starts {
		if seen[s] {
			continue
		}
		if r.states == t.budget {
			r.checked = -1
			return r
		}
		seen[s] = true
		r.states++
		if target[s] {
			r.found = 0
			return r
		}
		frontier = append(frontier, s)
	}
	for depth := 1; len(frontier) > 0; depth++ {
		var next []int
		for _, s := range frontier {
			for _, n := range t.next[s] {
				if n < 0 {
					continue
				}
				r.edges++
				if seen[n] {
					continue
				}
				if r.states == t.budget {
					r.checked = depth - 1
					return r
				}
				seen[n] = true
				r.states++
				r.depth = depth
				if target[n] {
					r.found = depth
					return r
				}
				next = append(next, n)
			}
		}
		frontier = next
	}
	r.checked, r.complete = r.depth, true
	return r
}

// refPath is the set of states each layer of a path search holds, on
// whole states, with no budget: whether some run follows the path.
func refPath(t table) bool {
	layer := map[int]bool{}
	for _, s := range t.starts {
		if t.targets[0][s] {
			layer[s] = true
		}
	}
	for d := 1; d < len(t.targets) && len(layer) > 0; d++ {
		next := map[int]bool{}
		for s := range layer {
			for _, n := range t.next[s] {
				if n >= 0 && t.targets[d][n] {
					next[n] = true
				}
			}
		}
		layer = next
	}
	return len(layer) > 0
}

// TestSearchAgreesWithReference is the property of the search: on
// generated graphs (up to 64 states, 4 signals, Done, Ignore and Reject
// edges, random targets and budgets) Search agrees with a breadth-first
// search on whole states: the verdict, the counters, the length of the
// run to a target, and that the run replays to a target; Path finds a run
// exactly when one follows the path, and its run replays along the path;
// Moves lists exactly the signals that move to another state.
// ERGO_EXPLORE_SEED replays one seed; a failure prints the graph and both
// results.
func TestSearchAgreesWithReference(t *testing.T) {
	first, count := int64(1), int64(400)
	if s := os.Getenv("ERGO_EXPLORE_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_EXPLORE_SEED: %v", err)
		}
		first, count = n, 1
	}
	foundSome, completeSome, budgetSome, pathSome := false, false, false, false
	for seed := first; seed < first+count; seed++ {
		tb := genTable(rand.New(rand.NewSource(seed)))
		g := tb.graph()
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d (ERGO_EXPLORE_SEED=%d): %s\ngraph: next %v, starts %v, targets %v, budget %d", seed, seed, fmt.Sprintf(format, args...), tb.next, tb.starts, tb.targets, tb.budget)
		}
		got := Search(g, func(s state) bool { return tb.targets[0][s.id] }, tb.budget)
		if got.FrontierStates != 0 {
			fail("Search retained %d whole payload states in pending layers; want node indexes only", got.FrontierStates)
		}
		want := refSearch(tb)
		stored := search(g, func(s state) bool { return tb.targets[0][s.id] }, tb.budget, true)
		if got.Found != stored.Found || got.States != stored.States || got.Edges != stored.Edges || got.Depth != stored.Depth || got.Checked != stored.Checked || got.Complete != stored.Complete || !slices.Equal(got.nodes, stored.nodes) {
			fail("Search replay %+v differs from whole-frontier reference %+v", got, stored)
		}
		gotLen := -1
		if got.Found >= 0 {
			start, sigs := got.Run(got.Found)
			gotLen = len(sigs)
			if end := Replay(g, start, sigs); !tb.targets[0][end.id] {
				fail("the run %d %v replays to %d, not a target", start, sigs, end.id)
			}
		}
		if gotLen != want.found || got.States != want.states || got.Edges != want.edges || got.Depth != want.depth || got.Checked != want.checked || got.Complete != want.complete {
			fail("Search: run %d, states %d, edges %d, depth %d, checked %d, complete %v; the reference: run %d, states %d, edges %d, depth %d, checked %d, complete %v",
				gotLen, got.States, got.Edges, got.Depth, got.Checked, got.Complete, want.found, want.states, want.edges, want.depth, want.checked, want.complete)
		}
		foundSome = foundSome || want.found > 0
		completeSome = completeSome || want.complete
		budgetSome = budgetSome || (want.found < 0 && !want.complete)

		targets := make([]func(state) bool, len(tb.targets))
		for i, set := range tb.targets {
			targets[i] = func(s state) bool { return set[s.id] }
		}
		p := Path(g, targets, len(tb.next)*len(tb.targets)+1)
		storedPath := path(g, targets, len(tb.next)*len(tb.targets)+1, true)
		if p.Found != storedPath.Found || p.States != storedPath.States || p.Edges != storedPath.Edges || p.Depth != storedPath.Depth || p.Checked != storedPath.Checked || p.Complete != storedPath.Complete || !slices.Equal(p.nodes, storedPath.nodes) {
			fail("Path replay %+v differs from whole-frontier reference %+v", p, storedPath)
		}
		if p.FrontierStates != 0 {
			fail("Path retained %d whole payload states in pending layers; want node indexes only", p.FrontierStates)
		}
		if (p.Found >= 0) != refPath(tb) || !p.Complete {
			fail("Path: found %v, complete %v; the reference: %v", p.Found >= 0, p.Complete, refPath(tb))
		}
		if p.Found >= 0 {
			pathSome = pathSome || len(tb.targets) > 1
			start, sigs := p.Run(p.Found)
			s := g.Starts[start]
			ok := len(sigs) == len(tb.targets)-1 && tb.targets[0][s.id]
			for i, e := range sigs {
				var more bool
				s, more = g.Step(s, e)
				ok = ok && more && tb.targets[i+1][s.id]
			}
			if !ok {
				fail("Path: the run %d %v does not follow the path", start, sigs)
			}
		}

		for id := range tb.next {
			var want []int
			for e, n := range tb.next[id] {
				if n >= 0 && n != id {
					want = append(want, e)
				}
			}
			if got := Moves(g, mkState(id)); !slices.Equal(got, want) {
				fail("Moves from %d: %v, want %v", id, got, want)
			}
		}
	}
	if count > 1 && !(foundSome && completeSome && budgetSome && pathSome) {
		t.Fatalf("the generated graphs reach a target past the start %v, are seen whole %v, stop at the budget %v, follow a path of two states or more %v; want each", foundSome, completeSome, budgetSome, pathSome)
	}
}
