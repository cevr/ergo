package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// needsBuild marks a test that starts a go command: the fast tier (-short)
// skips it, and the full tier runs it, as lower's needsBuild does.
func needsBuild(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
}

// TestSidesAnswerAsTheModel: the program, the control and the floor of
// benchmarks/server/registry answer seeded mixed scripts (refusals
// included, on one connection) and every route kind's script (over four
// connections) with the model's status and body, so a receipt's three
// sides serve the same answers. It builds Go because the property is about
// the bytes three server binaries send over a socket, which no in-process
// check of the lowered Go sees. Mutants servercount-control-listing-order
// (the control lists the catalog in another order) and
// servercount-floor-no-body-limit (the floor keeps a pin whose body is too long)
// survive every other test. A failing seed replays with
// SERVERCOUNT_SEED=N go test -run TestSidesAnswerAsTheModel ./scripts/servercount.
func TestSidesAnswerAsTheModel(t *testing.T) {
	needsBuild(t)
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	b, err := newBuilder(root, work, os.Getenv("SERVERCOUNT_ERGO"), 4<<30, t.Output())
	if err != nil {
		t.Fatal(err)
	}
	sides, err := b.sides("benchmarks/server/registry")
	if err != nil {
		t.Fatal(err)
	}
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if s := os.Getenv("SERVERCOUNT_SEED"); s != "" {
		seed, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("SERVERCOUNT_SEED=%q: %v", s, err)
		}
		seeds = []uint64{seed}
	}
	for _, s := range sides {
		check := func(tag string, script Script) {
			got, err := serve(s.bin, work, s.name+"-"+tag, script, 4<<30, 0)
			if err != nil {
				t.Fatalf("%s %s: %v", s.name, tag, err)
			}
			if got.wrong != nil {
				t.Errorf("%s %s: %v", s.name, tag, got.wrong)
			} else if got.checked != script.Len() || got.run.Incomplete != "" {
				t.Errorf("%s %s: %d of %d answers checked, run %q", s.name, tag, got.checked, script.Len(), got.run.Incomplete)
			}
		}
		for _, seed := range seeds {
			check("mixed", MixedScript(seed, 300))
		}
		for _, kind := range Kinds {
			check(kind, KindScript(kind, 40, 4))
		}
	}
}
