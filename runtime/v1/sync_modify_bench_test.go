package v1

import (
	"maps"
	"sync"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// BenchmarkModifyOwned: one owned sync.Modify of a small table (a map and
// two counters) that writes the map in place and returns whether the key
// was new, the shape of a coord turn's Modify (EX5a split, owner 3). The
// ergo side is the Go the lowering emits for
// sync.Modify(r, func(t table) (bool, table) { … }): the adapter of an
// Ergo literal's uniform shape (lowerer.adaptPlainFunc) around
// RefModifyOwned. The control is the same write under a sync.Mutex, by
// hand, with the unlock inline and deferred. Its receipt is user
// instructions per op (perf stat), not time.
func BenchmarkModifyOwned(b *testing.B) {
	env := &benchEnv{task: rt.Root("bench")}
	b.Run("ergo", func(b *testing.B) {
		r := NewRef(benchTable{m: map[int]int{}})
		clone := func(t benchTable) benchTable { t.m = maps.Clone(t.m); return t }
		i := 0
		for b.Loop() {
			i++
			k := i & 63
			benchModifySink = RefModifyOwned(env.task, r, func(p benchTable) struct {
				F0 bool
				F1 benchTable
			} {
				out, _ := benchErgoTouch(env, p, k)
				return out
			}, clone, true, false)
		}
	})
	// direct: RefModifyOwned with a Go function in place of the adapter,
	// which splits the Ref's own frame from the adapter's cost.
	b.Run("direct", func(b *testing.B) {
		r := NewRef(benchTable{m: map[int]int{}})
		clone := func(t benchTable) benchTable { t.m = maps.Clone(t.m); return t }
		i := 0
		for b.Loop() {
			i++
			k := i & 63
			benchModifySink = RefModifyOwned(env.task, r, func(t benchTable) struct {
				F0 bool
				F1 benchTable
			} {
				_, old := t.m[k]
				t.m[k] = k
				t.n++
				return struct {
					F0 bool
					F1 benchTable
				}{!old, t}
			}, clone, true, false)
		}
	})
	b.Run("mutex", func(b *testing.B) {
		var mu sync.Mutex
		t := benchTable{m: map[int]int{}}
		i := 0
		for b.Loop() {
			i++
			k := i & 63
			mu.Lock()
			_, old := t.m[k]
			t.m[k] = i
			t.n++
			mu.Unlock()
			benchModifySink = !old
		}
	})
	b.Run("mutexdefer", func(b *testing.B) {
		var mu sync.Mutex
		t := benchTable{m: map[int]int{}}
		i := 0
		for b.Loop() {
			i++
			benchModifySink = func(k int) bool {
				mu.Lock()
				defer mu.Unlock()
				_, old := t.m[k]
				t.m[k] = i
				t.n++
				return !old
			}(i & 63)
		}
	})
}

type benchTable struct {
	m    map[int]int
	n    int
	hits int
}

var benchModifySink bool

// benchErgoTouch is the uniform shape of the literal the ergo side lowers:
// a call-free body, so it polls no stop.
func benchErgoTouch(env *benchEnv, t benchTable, k int) (struct {
	F0 bool
	F1 benchTable
}, error) {
	_ = env
	_, old := t.m[k]
	t.m[k] = k
	t.n++
	return struct {
		F0 bool
		F1 benchTable
	}{!old, t}, nil
}
