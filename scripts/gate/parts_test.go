package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// partsRunning is the number of selections that run the top-level test name:
// a -run selection that matches it, a -skip selection that does not, or a
// part with no selection.
func partsRunning(t *testing.T, selections [][]string, name string) int {
	t.Helper()
	matched := 0
	for _, selection := range selections {
		if selection == nil {
			matched++
			continue
		}
		if (selection[0] == "-run") == regexp.MustCompile(selection[1]).MatchString(name) {
			matched++
		}
	}
	return matched
}

// The parts run every test of the package exactly once, a test the record
// lacks included, and a lane's new tests (names the record lacks) move no
// recorded test to another part: over the checker's and the lowering's
// test names, generated records (a subset of the names with generated
// counted work, stale names no package has, or no rows) and every part
// count up to 16. A failing seed prints itself; replay with PARTS_SEED.
func TestPartsRunEveryTestExactlyOnce(t *testing.T) {
	t.Parallel()
	root := checkoutRoot(t)
	seed := uint64(time.Now().UnixNano())
	if s, ok := lookupSeed(); ok {
		seed = s
	}
	random := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	kinds := []string{"lowerer", "lower", "build", "run", "typed-source"}
	for _, pkg := range []string{"internal/sketch/check", "internal/sketch/lower"} {
		defined, err := testNames(filepath.Join(root, pkg))
		if err != nil {
			t.Fatal(err)
		}
		if len(defined) < 100 {
			t.Fatalf("%s lists %d tests; the property would be vacuous", pkg, len(defined))
		}
		for trial := range 40 {
			rows := map[string]map[string]int{}
			if random.IntN(5) > 0 {
				keep := random.Float64()
				for _, name := range defined {
					if random.Float64() < keep {
						work := map[string]int{}
						for _, kind := range kinds {
							if random.IntN(3) == 0 {
								work[kind] = random.IntN(200)
							}
						}
						rows[name] = work
					}
				}
				for i := range random.IntN(4) {
					rows[fmt.Sprintf("TestStale%d", i)] = map[string]int{}
				}
			}
			n := 1 + random.IntN(16)
			at := fmt.Sprintf("seed %d (PARTS_SEED=%d), %s trial %d (%d rows, %d parts)", seed, seed, pkg, trial, len(rows), n)
			selections := partsOf(rows, defined, n)
			if len(selections) == 0 || len(selections) > n {
				t.Fatalf("%s: %d selections", at, len(selections))
			}
			for _, name := range append(slices.Clone(defined), "TestUnlisted") {
				if k := partsRunning(t, selections, name); k != 1 {
					t.Fatalf("%s: %s runs in %d parts: %q", at, name, k, selections)
				}
			}
			if len(rows) == 0 {
				continue
			}
			grown := append(slices.Clone(defined), "TestNewInTheLane", "TestNewInTheLaneToo")
			if again := partsOf(rows, grown, n); !slices.EqualFunc(again, selections, slices.Equal) {
				t.Fatalf("%s: a lane's new tests changed the deal:\n%q\n%q", at, selections, again)
			}
		}
	}
}

// lookupSeed reads PARTS_SEED, the replay of a failing seed.
func lookupSeed() (uint64, bool) {
	var s uint64
	if v := strings.TrimSpace(os.Getenv("PARTS_SEED")); v != "" {
		if _, err := fmt.Sscan(v, &s); err == nil {
			return s, true
		}
	}
	return 0, false
}

// The probe log's lines count for their top-level test by kind; a line
// with no test is counted apart.
func TestProbeWorkCountsByTest(t *testing.T) {
	t.Parallel()
	log := "lowerer - TestA\nbuild 1f TestA/sub/case\nlowerer - TestA\nrun 2e TestB\ntypecheck 4c TestB 7\nlower 3d\n\n"
	work, unnamed, err := probeWork(strings.NewReader(log))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]int{"TestA": {"lowerer": 2, "build": 1}, "TestB": {"run": 1, "typecheck": 7}}
	if fmt.Sprint(work) != fmt.Sprint(want) || unnamed != 1 {
		t.Fatalf("work %v, unnamed %d; want %v, 1", work, unnamed, want)
	}
}
