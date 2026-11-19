package v1

import (
	"iter"
	"math/bits"
	"math/rand/v2"
	"slices"
)

// IntMap is the representation of an Ergo Map[K, V] whose key is an
// integer type (MAP-INT, MI-1; the lowering that chooses it is MI-2). It is
// an open-addressing table: linear probing from a home slot, deletion by
// backward shift (no tombstones), and a power-of-two number of slots kept
// at most half full. The home of a key is a seeded mix, a 128-bit multiply
// folded, so the iteration order differs between runs as Go's map's does
// (grammar.md: a Map's iteration order is unspecified), and keys chosen in
// advance cannot be aimed at one run of slots.
//
// A nil *IntMap reads as the empty map, as a nil Go map does; Put on it is
// Go's own nil-map defect. The probe that measured it against Go's map is
// /home/exedev/.cache/baton/wf10/go-backend/IR2-FLIP/MAP-INT-PROBE.
type IntMap[K Integer, V any] struct {
	slots []intSlot[K, V]
	n     int
	shift uint
}

// intMapSeed is the process's hash seed, drawn once as Go's runtime draws
// its map seed. Like Go's, it is visible only as the order in which a Map
// is iterated, which Ergo leaves unspecified (grammar.md 216), so no body
// that uses an IntMap reaches host state through it. A draw per map would
// change nothing a program may rely on.
var intMapSeed = rand.Uint64()

// Integer is the key types IntMap takes: Go's integer types and the types
// defined over them.
type Integer interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

type intSlot[K Integer, V any] struct {
	key  K
	val  V
	used bool
}

// intMapMin is the fewest slots a table has.
const intMapMin = 8

// NewIntMap is an empty map with room for hint entries before it grows.
//
//ergo:wait none
func NewIntMap[K Integer, V any](hint int) *IntMap[K, V] {
	m := &IntMap[K, V]{}
	m.size(hint)
	return m
}

// size gives m an empty table with room for n entries.
func (m *IntMap[K, V]) size(n int) {
	c := intMapMin
	for c < 2*n {
		c <<= 1
	}
	m.slots = make([]intSlot[K, V], c)
	m.shift = uint(64 - bits.TrailingZeros(uint(c)))
	m.n = 0
}

// home is the slot where the probe for k starts.
func (m *IntMap[K, V]) home(k K) int {
	hi, lo := bits.Mul64(uint64(k)^intMapSeed, 0x9E3779B97F4A7C15)
	return int((hi ^ lo) >> m.shift)
}

// Len is the number of entries in m.
//
//ergo:wait none
func (m *IntMap[K, V]) Len() int {
	if m == nil {
		return 0
	}
	return m.n
}

// Get is the value of k in m and whether m holds k.
//
//ergo:wait none
func (m *IntMap[K, V]) Get(k K) (v V, ok bool) {
	if m == nil {
		return v, false
	}
	// Small enough for gc to inline at the call: the probe stops at the
	// first empty slot, which a table at most half full always has.
	mask := len(m.slots) - 1
	for i := m.home(k); m.slots[i].used; i = (i + 1) & mask {
		if m.slots[i].key == k {
			return m.slots[i].val, true
		}
	}
	return
}

// Put sets k to v in m.
//
//ergo:wait none
func (m *IntMap[K, V]) Put(k K, v V) {
	if m == nil {
		var none map[K]V
		none[k] = v // Go's own defect: assignment to entry in nil map
	}
	mask := len(m.slots) - 1
	for i := m.home(k); ; i = (i + 1) & mask {
		s := &m.slots[i]
		if !s.used {
			if 2*(m.n+1) > len(m.slots) {
				m.grow()
				m.Put(k, v)
				return
			}
			*s = intSlot[K, V]{key: k, val: v, used: true}
			m.n++
			return
		}
		if s.key == k {
			s.val = v
			return
		}
	}
}

// Delete removes k from m, if m holds it.
//
//ergo:wait none
func (m *IntMap[K, V]) Delete(k K) {
	if m == nil || m.n == 0 {
		return
	}
	mask := len(m.slots) - 1
	hole := m.home(k)
	for {
		s := &m.slots[hole]
		if !s.used {
			return
		}
		if s.key == k {
			break
		}
		hole = (hole + 1) & mask
	}
	// Backward shift: an entry later in the run moves into the hole when
	// its home is not cyclically in (hole, j], so every entry stays
	// reachable from its home with no empty slot between.
	for j := (hole + 1) & mask; m.slots[j].used; j = (j + 1) & mask {
		if h := m.home(m.slots[j].key); (j-h)&mask >= (j-hole)&mask {
			m.slots[hole] = m.slots[j]
			hole = j
		}
	}
	m.slots[hole] = intSlot[K, V]{}
	m.n--
}

// Clear removes every entry of m and keeps its slots.
//
//ergo:wait none
func (m *IntMap[K, V]) Clear() {
	if m == nil {
		return
	}
	clear(m.slots)
	m.n = 0
}

// Clone is a map with m's entries that shares no storage with m: the
// value copy of an Ergo Map (types.md, Bindings).
//
//ergo:wait none
func (m *IntMap[K, V]) Clone() *IntMap[K, V] {
	if m == nil {
		return nil
	}
	c := *m
	c.slots = slices.Clone(m.slots)
	return &c
}

// All is m's entries, each once, in m's slot order. m is not written while
// the sequence runs (the lowering iterates only a Map no one writes in the
// loop).
//
//ergo:wait none
func (m *IntMap[K, V]) All() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		if m == nil {
			return
		}
		for i := range m.slots {
			s := &m.slots[i]
			if s.used && !yield(s.key, s.val) {
				return
			}
		}
	}
}

// IntMapEqual reports whether a and b hold the same keys with values that
// eq finds equal: the Map arm of the lowering's per-type equality.
//
//ergo:wait none
func IntMapEqual[K Integer, V any](a, b *IntMap[K, V], eq func(V, V) bool) bool {
	if a.Len() != b.Len() {
		return false
	}
	for k, v := range a.All() {
		w, ok := b.Get(k)
		if !ok || !eq(v, w) {
			return false
		}
	}
	return true
}

// grow doubles m's slots and puts its entries back.
func (m *IntMap[K, V]) grow() {
	old := m.slots
	m.size(len(old))
	for i := range old {
		if old[i].used {
			m.Put(old[i].key, old[i].val)
		}
	}
}
