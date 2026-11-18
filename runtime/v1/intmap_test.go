package v1

import (
	"flag"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// TestIntMapMatchesGoMap is MAP-INT's acceptance property (MI-1): seeded
// random Put, Delete, Get, Clear and Clone steps on an IntMap and on Go's
// map agree after every step, in Len, in each key read, and in All taken as
// a set (each entry once). A clone is followed as its own model, so writes
// on either side are checked not to reach the other. Small key domains keep
// tables small and runs long, so growth, probing past the table's end and
// every arm of backward-shift deletion are reached. A failing seed names
// itself; replay it with go test ./runtime/v1 -run TestIntMapMatchesGoMap
// -intmap.seed=N.
func TestIntMapMatchesGoMap(t *testing.T) {
	seeds := uint64(100)
	t.Run("int", func(t *testing.T) {
		intMapProperty(t, seeds, func(r *rand.Rand) int {
			if r.IntN(20) == 0 {
				return int(r.Uint64())
			}
			return r.IntN(80) - 30
		})
	})
	t.Run("int8", func(t *testing.T) {
		intMapProperty(t, seeds, func(r *rand.Rand) int8 { return int8(r.IntN(256) - 128) })
	})
	t.Run("uint64", func(t *testing.T) {
		intMapProperty(t, seeds, func(r *rand.Rand) uint64 { return ^uint64(0) - uint64(r.IntN(48)) })
	})
}

func intMapProperty[K Integer](t *testing.T, seeds uint64, key func(*rand.Rand) K) {
	for seed := uint64(1); seed <= seeds; seed++ {
		if *intMapReplay != 0 && seed != *intMapReplay {
			continue
		}
		r := rand.New(rand.NewPCG(seed, 0x1a7))
		m, ref := NewIntMap[K, int](r.IntN(4)), map[K]int{}
		var c *IntMap[K, int]
		var cref map[K]int
		var steps []string
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d, after %s: "+format+"\nreplay: go test ./runtime/v1 -run TestIntMapMatchesGoMap -intmap.seed=%d",
				append(append([]any{seed, strings.Join(tail(steps, 12), "; ")}, args...), seed)...)
		}
		for i := 0; i < 1500; i++ {
			k, v := key(r), i
			switch op := r.IntN(100); {
			case op < 45:
				m.Put(k, v)
				ref[k] = v
				steps = append(steps, sprint("put", k))
			case op < 80:
				m.Delete(k)
				delete(ref, k)
				steps = append(steps, sprint("delete", k))
			case op < 81:
				m.Clear()
				clear(ref)
				steps = append(steps, "clear")
			case op < 83:
				c, cref = m.Clone(), maps.Clone(ref)
				steps = append(steps, "clone")
			case op < 90 && c != nil:
				c.Put(k, -v)
				cref[k] = -v
				steps = append(steps, sprint("clone put", k))
			case op < 95 && c != nil:
				c.Delete(k)
				delete(cref, k)
				steps = append(steps, sprint("clone delete", k))
			default:
				steps = append(steps, sprint("get", k))
			}
			if got, ok := m.Get(k); got != ref[k] || ok != has(ref, k) {
				fail("Get(%d) = %d, %v; Go's map has %d, %v", k, got, ok, ref[k], has(ref, k))
			}
			if err := intMapSame(m, ref); err != "" {
				fail("map: %s", err)
			}
			if c != nil {
				if err := intMapSame(c, cref); err != "" {
					fail("clone: %s", err)
				}
				if eq := IntMapEqual(m, c, func(a, b int) bool { return a == b }); eq != maps.Equal(ref, cref) {
					fail("IntMapEqual = %v; Go's maps.Equal = %v", eq, maps.Equal(ref, cref))
				}
			}
		}
	}
}

// intMapSame is "" when m holds exactly ref's entries, by Len and by All
// seen as a set, else what differs.
func intMapSame[K Integer](m *IntMap[K, int], ref map[K]int) string {
	if m.Len() != len(ref) {
		return sprint("Len", m.Len()) + sprint(" want", len(ref))
	}
	seen := map[K]bool{}
	for k, v := range m.All() {
		if seen[k] {
			return sprint("All yields twice", k)
		}
		seen[k] = true
		if w, ok := ref[k]; !ok || w != v {
			return sprint("All yields", k) + sprint(" =", v)
		}
	}
	if len(seen) != len(ref) {
		return sprint("All yields entries:", len(seen))
	}
	return ""
}

// TestIntMapSeedShowsOnlyAsOrder: the process seed (one per process,
// MI-1 ruling) reaches nothing a program reads except the order of All.
// The same seeded steps run under two seeds give the same Get, Len,
// IntMapEqual and entries; for some seed pair the order of All differs,
// so the order is the seed's and not the keys' alone.
func TestIntMapSeedShowsOnlyAsOrder(t *testing.T) {
	defer func(s uint64) { intMapSeed = s }(intMapSeed)
	run := func(seed, steps uint64) (seen []string, order []int) {
		intMapSeed = seed
		r := rand.New(rand.NewPCG(steps, 7))
		m, c := NewIntMap[int, int](0), NewIntMap[int, int](0)
		for i := 0; i < 400; i++ {
			k := r.IntN(90) - 20
			switch r.IntN(4) {
			case 0, 1:
				m.Put(k, i)
			case 2:
				m.Delete(k)
			default:
				c = m.Clone()
				c.Put(k, -i)
			}
			v, ok := m.Get(k)
			seen = append(seen, fmt.Sprint(v, ok, m.Len(), IntMapEqual(m, c, func(a, b int) bool { return a == b })))
		}
		entries := map[int]int{}
		for k, v := range m.All() {
			entries[k] = v
			order = append(order, k)
		}
		seen = append(seen, fmt.Sprint(entries)) // fmt prints a Go map in key order
		return seen, order
	}
	reordered := false
	for steps := uint64(1); steps <= 20; steps++ {
		a, orderA := run(0x5eed, steps)
		b, orderB := run(0x5eed^steps*0x9E3779B97F4A7C15, steps)
		if !slices.Equal(a, b) {
			t.Fatalf("steps %d: a result other than iteration order depends on the seed", steps)
		}
		reordered = reordered || !slices.Equal(orderA, orderB)
	}
	if !reordered {
		t.Fatal("no two seeds iterate in different orders: the seed does not reach the table")
	}
}

// TestIntMapNil: a nil IntMap reads as the empty map and Put on it is Go's
// own nil-map defect, as for a nil Go map.
func TestIntMapNil(t *testing.T) {
	var m *IntMap[int, string]
	if _, ok := m.Get(1); ok || m.Len() != 0 || m.Clone() != nil {
		t.Fatal("a nil IntMap is not the empty map")
	}
	m.Delete(1)
	m.Clear()
	for range m.All() {
		t.Fatal("a nil IntMap yields an entry")
	}
	defer func() {
		if r, ok := recover().(interface{ RuntimeError() }); !ok || !strings.Contains(sprint("", r), "nil map") {
			t.Fatalf("Put on a nil IntMap recovered %v, want Go's nil-map runtime error", r)
		}
	}()
	m.Put(1, "x")
}

var intMapReplay = flag.Uint64("intmap.seed", 0, "run TestIntMapMatchesGoMap at this seed only")

func sprint(label string, x any) string { return fmt.Sprint(label, " ", x) }

func has[K comparable](m map[K]int, k K) bool { _, ok := m[k]; return ok }

func tail(xs []string, n int) []string { return xs[max(0, len(xs)-n):] }
