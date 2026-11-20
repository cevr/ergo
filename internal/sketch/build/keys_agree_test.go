package build

import (
	"context"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	linked "ergo.local/prototype/internal/linked/compiler"
)

// TestActionKeysAgreeWithErgo holds the action keys written in Ergo
// (compiler/cachekey's Fields and BuildID, linked as
// internal/linked/compiler) to this package's keyer and buildID, the
// reference until the S5 swap, on seeded field lists. Names and values
// hold the bytes a length-prefixed framing must not confuse (digits,
// ':', NUL, empty strings, multi-byte runes). A failing case prints its
// seed and the replay command.
//
// Mutants it alone catches: internal/selfhostcheck/testdata/mutants.json,
// killer internal/sketch/build:TestActionKeysAgreeWithErgo.
func TestActionKeysAgreeWithErgo(t *testing.T) {
	seed := uint64(1)
	if s := os.Getenv("ERGO_SELFHOST_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = n
	}
	replay := "replay: ERGO_SELFHOST_SEED=" + strconv.FormatUint(seed, 10) + " go test -count=1 -run '^TestActionKeysAgreeWithErgo$' ./internal/sketch/build"
	rng := rand.New(rand.NewPCG(seed, 0xac7))
	ctx := context.Background()
	pieces := []string{"", "p", "1", "12", ":", "1:", "\x00", "é", "𝄞", "schema", "go1.27 link"}
	text := func() string {
		var b strings.Builder
		for range rng.IntN(4) {
			b.WriteString(pieces[rng.IntN(len(pieces))])
		}
		return b.String()
	}
	for c := 0; c < 300; c++ {
		var k keyer
		var names, values []string
		for range rng.IntN(6) {
			n, v := text(), text()
			names, values = append(names, n), append(values, v)
			k.add(n, v)
		}
		want := k.sum()
		got, err := linked.KeyFields(ctx, names, values)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("seed %d case %d: key of names %q values %q: %s, want %s\n%s", seed, c, names, values, got, want, replay)
		}
		parts := 1 + rng.IntN(4)
		id, err := linked.KeyBuildID(ctx, want, parts)
		if err != nil {
			t.Fatal(err)
		}
		if w := buildID(want, parts); id != w {
			t.Fatalf("seed %d case %d: build ID of %s in %d parts: %s, want %s\n%s", seed, c, want, parts, id, w, replay)
		}
	}
}
