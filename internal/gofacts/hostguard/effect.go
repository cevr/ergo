package hostguard

import (
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"reflect"
	"sort"
	"sync/atomic"
)

// The guard of the memory-only rows (gofacts.Effects; np4f-plan GBX6): the
// same drawn calls on the same drawn state, run once under each of the
// determinism guard's two host states (pure.go), must give the same
// results and leave the same state. A row whose function reads host state
// the two differ in (the working directory, the environment, the time
// zone, GOMAXPROCS) is refuted. That it touches no other memory and never
// waits is the reviewer's, as a pure row's completeness is.

// effectCases is the number of drawn cases per row.
const effectCases = 16

// effectRun is one case of a row's guard: run makes the case's calls on
// state, which it built from the draw, and gives their results.
type effectRun struct {
	state any
	run   func() []any
}

// effectCalls are the guarded calls, one per row of gofacts.Effects; each
// draws its state and its calls' arguments from r.
func effectCalls() map[string]func(r *rand.Rand) effectRun {
	i64 := func(r *rand.Rand) (*atomic.Int64, int64) {
		x := new(atomic.Int64)
		x.Store(r.Int64N(1<<20) - 1<<19)
		return x, r.Int64N(1<<20) - 1<<19
	}
	b := func(r *rand.Rand) (*atomic.Bool, bool) {
		x := new(atomic.Bool)
		x.Store(r.IntN(2) == 1)
		return x, r.IntN(2) == 1
	}
	pcg := func(r *rand.Rand) *rand.PCG { return rand.NewPCG(r.Uint64(), r.Uint64()) }
	return map[string]func(r *rand.Rand) effectRun{
		"sync/atomic.Bool.CompareAndSwap": func(r *rand.Rand) effectRun {
			x, v := b(r)
			return effectRun{x, func() []any { return []any{x.CompareAndSwap(v, !v), x.CompareAndSwap(!v, v)} }}
		},
		"sync/atomic.Bool.Load": func(r *rand.Rand) effectRun {
			x, _ := b(r)
			return effectRun{x, func() []any { return []any{x.Load()} }}
		},
		"sync/atomic.Bool.Store": func(r *rand.Rand) effectRun {
			x, v := b(r)
			return effectRun{x, func() []any { x.Store(v); return nil }}
		},
		"sync/atomic.Bool.Swap": func(r *rand.Rand) effectRun {
			x, v := b(r)
			return effectRun{x, func() []any { return []any{x.Swap(v), x.Swap(!v)} }}
		},
		"sync/atomic.Int64.Add": func(r *rand.Rand) effectRun {
			x, d := i64(r)
			return effectRun{x, func() []any { return []any{x.Add(d), x.Add(-2 * d)} }}
		},
		"sync/atomic.Int64.And": func(r *rand.Rand) effectRun {
			x, m := i64(r)
			return effectRun{x, func() []any { return []any{x.And(m)} }}
		},
		"sync/atomic.Int64.CompareAndSwap": func(r *rand.Rand) effectRun {
			x, v := i64(r)
			return effectRun{x, func() []any { return []any{x.CompareAndSwap(v, v+1), x.CompareAndSwap(x.Load(), v)} }}
		},
		"sync/atomic.Int64.Load": func(r *rand.Rand) effectRun {
			x, _ := i64(r)
			return effectRun{x, func() []any { return []any{x.Load()} }}
		},
		"sync/atomic.Int64.Or": func(r *rand.Rand) effectRun {
			x, m := i64(r)
			return effectRun{x, func() []any { return []any{x.Or(m)} }}
		},
		"sync/atomic.Int64.Store": func(r *rand.Rand) effectRun {
			x, v := i64(r)
			return effectRun{x, func() []any { x.Store(v); return nil }}
		},
		"sync/atomic.Int64.Swap": func(r *rand.Rand) effectRun {
			x, v := i64(r)
			return effectRun{x, func() []any { return []any{x.Swap(v), x.Swap(-v)} }}
		},
		"math/rand/v2.NewPCG": func(r *rand.Rand) effectRun {
			s1, s2 := r.Uint64(), r.Uint64()
			var p *rand.PCG
			return effectRun{&p, func() []any { p = rand.NewPCG(s1, s2); return []any{p.Uint64()} }}
		},
		"math/rand/v2.PCG.Seed": func(r *rand.Rand) effectRun {
			p := pcg(r)
			s1, s2 := r.Uint64(), r.Uint64()
			return effectRun{p, func() []any { p.Seed(s1, s2); return nil }}
		},
		"math/rand/v2.PCG.Uint64": func(r *rand.Rand) effectRun {
			p := pcg(r)
			return effectRun{p, func() []any { return []any{p.Uint64(), p.Uint64(), p.Uint64()} }}
		},
	}
}

// effectOutcome is one case's results and the state after, printed.
type effectOutcome struct{ results, state string }

// runEffects runs each call's cases under the current host state; each
// case's draw is seeded by the target's name and the case, so both host
// states draw the same.
func runEffects(calls map[string]func(r *rand.Rand) effectRun) map[string][]effectOutcome {
	out := map[string][]effectOutcome{}
	for target, draw := range calls {
		for c := range effectCases {
			h := fnv.New64a()
			h.Write([]byte(target))
			e := draw(rand.New(rand.NewPCG(h.Sum64()+Seed, uint64(c))))
			res := e.run()
			// The state is printed through its pointers: the cell's or
			// the generator's words, never an address.
			v := reflect.ValueOf(e.state)
			for v.Kind() == reflect.Pointer && !v.IsNil() {
				v = v.Elem()
			}
			out[target] = append(out[target], effectOutcome{fmt.Sprintf("%v", res), fmt.Sprintf("%#v", v.Interface())})
		}
	}
	return out
}

// EffectDiff is a row whose calls give other results, or leave another
// state, under the two host states.
type EffectDiff struct {
	Target string
	Case   int
	A, B   effectOutcome
}

func (d EffectDiff) Error() string {
	return fmt.Sprintf("%s case %d: results %s and state %s under one host state, results %s and state %s under the other", d.Target, d.Case, d.A.results, d.A.state, d.B.results, d.B.state)
}

// effects runs calls under the guard's two host states and gives the
// targets whose outcomes differ, each with its first differing case,
// sorted; a state the guard cannot enter is one diff.
func effects(calls map[string]func(r *rand.Rand) effectRun) []EffectDiff {
	a, b, done, err := pureStates()
	if err != nil {
		return []EffectDiff{{Target: "?", A: effectOutcome{results: err.Error()}}}
	}
	defer done()
	outs := [2]map[string][]effectOutcome{}
	for i, s := range []ambient{a, b} {
		restore, err := s.enter()
		if err != nil {
			return []EffectDiff{{Target: "?", A: effectOutcome{results: err.Error()}}}
		}
		outs[i] = runEffects(calls)
		restore()
	}
	var diffs []EffectDiff
	for target, got := range outs[0] {
		for c, o := range got {
			if p := outs[1][target][c]; o != p {
				diffs = append(diffs, EffectDiff{Target: target, Case: c, A: o, B: p})
				break
			}
		}
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Target < diffs[j].Target })
	return diffs
}

// Effects runs every memory-only row's guard: the refuted targets, each
// with its diff.
func Effects() map[string]error {
	out := map[string]error{}
	for _, d := range effects(effectCalls()) {
		out[d.Target] = d
	}
	return out
}
