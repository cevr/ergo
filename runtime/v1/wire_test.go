package v1

import (
	"math/rand"
	"os"
	"slices"
	"strconv"
	"testing"
)

// TestWireKeysInAgreesWithWireKeys runs generated Maps (seed 1, or
// ERGO_WIRE_SEED) through WireKeysIn, with one array reused across them
// as a codec function's loop reuses its own, and compares each result with
// WireKeys, the reference. Sizes run from 0 to two past WireKeyRoom, so
// both the in-place path and the heap path are reached; keys share
// prefixes and hold bytes above 0x7f, where byte order is not rune order.
// A failure prints the seed, the case and both orders.
func TestWireKeysInAgreesWithWireKeys(t *testing.T) {
	seed := int64(1)
	if s := os.Getenv("ERGO_WIRE_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = n
	}
	r := rand.New(rand.NewSource(seed))
	parts := []string{"", "a", "ab", "b", "\xff", "é", "z", "A"}
	var buf [WireKeyRoom]string
	sizes := map[int]bool{}
	for n := 0; n < 400; n++ {
		size := r.Intn(WireKeyRoom + 3)
		m := map[string]int{}
		for len(m) < size {
			k := ""
			for range 1 + r.Intn(3) {
				k += parts[r.Intn(len(parts))]
			}
			m[k] = len(m)
		}
		sizes[size] = true
		got, want := WireKeysIn(&buf, m), WireKeys(m)
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d, case %d: WireKeysIn gives %q, WireKeys %q (replay: ERGO_WIRE_SEED=%d go test ./runtime/v1 -run TestWireKeysInAgreesWithWireKeys)", seed, n, got, want, seed)
		}
	}
	for _, s := range []int{0, 1, WireKeyRoom, WireKeyRoom + 1} {
		if !sizes[s] {
			t.Fatalf("seed %d: no case of size %d", seed, s)
		}
	}
}

// TestWireKeysInAllocatesNothing: a Map of at most WireKeyRoom keys is
// sorted in the caller's array, as a codec function's loop ranges it,
// with no allocation (the count, not a time).
func TestWireKeysInAllocatesNothing(t *testing.T) {
	m := map[string]int{"c": 1, "a": 2, "b": 3, "e": 4, "d": 5, "h": 6, "g": 7, "f": 8}
	n := 0
	allocs := testing.AllocsPerRun(100, func() {
		var buf [WireKeyRoom]string
		for _, k := range WireKeysIn(&buf, m) {
			n += len(k)
		}
	})
	if allocs != 0 {
		t.Fatalf("WireKeysIn of %d keys allocates %v times per call, want 0", len(m), allocs)
	}
}
