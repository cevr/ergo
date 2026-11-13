package v1

import (
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strconv"
	"testing"
)

// jsonSeed is the seed of the json pool properties: 1, or ERGO_JSON_SEED.
func jsonSeed(t *testing.T) int64 {
	if s := os.Getenv("ERGO_JSON_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	return 1
}

// TestJSONRecycleKeepsUsedBuffers draws runs of texts written into one
// pooled Text and holds jsonRecycle to its rule (stdlib.md json 4): after
// each give the Text is empty, and it keeps its buffer exactly when the
// buffer is at most 4 KiB, or this text used a quarter of it, or this is
// at most the fourth text in a row to use less of the same large buffer.
// The runs go on until each outcome has been seen. A failure prints the
// seed and the run; ERGO_JSON_SEED=<seed> replays it.
func TestJSONRecycleKeepsUsedBuffers(t *testing.T) {
	seed := jsonSeed(t)
	r := rand.New(rand.NewSource(seed))
	seen := map[string]bool{}
	want := []string{"small", "used", "struck", "dropped"}
	for n := 0; n < 200 || slices.ContainsFunc(want, func(w string) bool { return !seen[w] }); n++ {
		if n == 20000 {
			t.Fatalf("seed %d: %d runs reach only %v", seed, n, seen)
		}
		tx := &Text{B: make([]byte, 0, 1024)}
		under := 0 // the model: large texts in a row that used under a quarter
		var log []string
		for step := 0; step < 12; step++ {
			// Write a text: short, or up to 256 KiB.
			size := r.Intn(64)
			if r.Intn(3) == 0 {
				size = r.Intn(256 << 10)
			}
			tx.B = append(tx.B, make([]byte, size)...)
			tx.Comma = true
			c, l := cap(tx.B), len(tx.B)
			var kept bool
			switch {
			case c <= 4<<10:
				kept, under = true, 0
				seen["small"] = true
			case l*4 >= c:
				kept, under = true, 0
				seen["used"] = true
			default:
				under++
				kept = under <= 4
				if kept {
					seen["struck"] = true
				} else {
					seen["dropped"] = true
					under = 0
				}
			}
			log = append(log, fmt.Sprintf("cap %d len %d", c, l))
			jsonRecycle(tx)
			if got := cap(tx.B) == c; got != kept || len(tx.B) != 0 || tx.Comma {
				t.Fatalf("seed %d, run %d: after %v the Text keeps cap %d, len %d, comma %v; want the buffer kept %v, empty, no comma",
					seed, n, log, cap(tx.B), len(tx.B), tx.Comma, kept)
			}
		}
	}
}

// TestJSONKeysStack draws trees of Maps, each written inside a value of
// the one above as the fast path writes them, and holds JSONKeys and
// JSONKeysDone to the order WireKeys gives: each Map's keys are its keys
// sorted, a Map written inside it leaves them as they were, and when the
// outermost is done the stack is empty and keeps no string. A failure
// prints the seed and the tree's depth; ERGO_JSON_SEED replays it.
func TestJSONKeysStack(t *testing.T) {
	seed := jsonSeed(t)
	r := rand.New(rand.NewSource(seed))
	tx := &Text{}
	var write func(depth int) string
	write = func(depth int) string {
		m := map[string]int{}
		for range r.Intn(5) {
			m["k"+strconv.Itoa(r.Intn(40))] = 0
		}
		ks := JSONKeys(tx, m)
		want := WireKeys(m)
		for range ks {
			if depth < 4 && r.Intn(2) == 0 {
				if why := write(depth + 1); why != "" {
					return why
				}
			}
			if !slices.Equal(ks, want) {
				return fmt.Sprintf("depth %d: keys %q after an inner Map; want %q", depth, ks, want)
			}
		}
		JSONKeysDone(tx, ks)
		return ""
	}
	for n := range 300 {
		if why := write(0); why != "" {
			t.Fatalf("seed %d, tree %d: %s", seed, n, why)
		}
		if len(tx.keys) != 0 || slices.ContainsFunc(tx.keys[:cap(tx.keys)], func(s string) bool { return s != "" }) {
			t.Fatalf("seed %d, tree %d: the stack holds %q after the outermost Map", seed, n, tx.keys[:cap(tx.keys)])
		}
	}
}
