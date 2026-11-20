package lower

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestLedgerDropsSuperseded is DropSuperseded's property (LEDGER-SUPERSEDE)
// over generated key changes: a law's Go code compiles to one of a few
// closures in each build (the four supported platforms, stock and with
// GOEXPERIMENT=simd), before and after an edit. Every build had written its
// key's entry into one ledger, by the rule of ForeignKeySource.Keys (a
// build names its platform when another platform of its experiment set
// compiles another closure, and a build with experiments always names
// them: LEDGER-EXP-KEY); then one build reruns and writes its fresh key.
// After DropSuperseded:
//   - no entry is lost that any build, of either experiment set, looks up
//     now, and no entry of another experiment set goes at all;
//   - every entry of the run's experiment set that no build looks up now
//     is gone.
//
// A failing seed prints both closure maps, the ledger and the replay
// command. Mutant (lower mutants.json): lsd-other-platform-dropped.
func TestLedgerDropsSuperseded(t *testing.T) {
	seeds := make([]uint64, 400)
	for i := range seeds {
		seeds[i] = uint64(i + 1)
	}
	if s := os.Getenv("ERGO_SUPERSEDE_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	}
	platforms := []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"}
	type build struct {
		plat string
		simd bool
	}
	var builds []build
	for _, p := range platforms {
		builds = append(builds, build{p, false}, build{p, true})
	}
	keyOf := func(c map[build]int, b build) string {
		var tags []string
		for _, o := range builds {
			if o.simd == b.simd && o.plat != b.plat && c[o] != c[b] {
				tags = append(tags, b.plat)
				break
			}
		}
		if b.simd {
			tags = append(tags, "goexperiment.simd")
		}
		slices.Sort(tags)
		return strings.Join(tags, ",")
	}
	entry := func(b string) LedgerEntry {
		return LedgerEntry{Law: "m.L", Provider: "m.P", Instance: "(1)", Build: b, Verdict: "proved"}
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x5ced))
		before, after := map[build]int{}, map[build]int{}
		n := 1 + r.IntN(3)
		for _, b := range builds {
			before[b], after[b] = r.IntN(n), r.IntN(n)
		}
		ledger := map[string]LedgerEntry{}
		for _, b := range builds {
			e := entry(keyOf(before, b))
			ledger[e.Key()] = e
		}
		old := map[string]bool{}
		for k := range ledger {
			old[k] = true
		}
		h := builds[r.IntN(len(builds))]
		here := []string{h.plat}
		if h.simd {
			here = append(here, "goexperiment.simd")
		}
		fresh := entry(keyOf(after, h))
		ledger[fresh.Key()] = fresh
		DropSuperseded(ledger, []LedgerEntry{fresh}, here)
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d (replay: ERGO_SUPERSEDE_SEED=%d go test -run TestLedgerDropsSuperseded ./internal/sketch/lower): run %v; %s\nbefore %v\nafter %v\nledger %v", seed, seed, h, fmt.Sprintf(format, args...), before, after, slices.Sorted(func(yield func(string) bool) {
				for k := range ledger {
					if !yield(k) {
						return
					}
				}
			}))
		}
		if _, ok := ledger[fresh.Key()]; !ok {
			fail("the fresh entry %q is gone", fresh.Key())
		}
		// The run's set reads its keys of the edited code; the other set,
		// which did not rerun, reads both its old keys and, once it
		// reruns, its new ones.
		live := map[string]bool{}
		for _, b := range builds {
			live[entry(keyOf(after, b)).Key()] = true
			if b.simd != h.simd {
				live[entry(keyOf(before, b)).Key()] = true
			}
		}
		for k := range old {
			_, kept := ledger[k]
			part := ""
			if i := strings.Index(k, " under "); i >= 0 {
				part = k[i+len(" under "):]
			}
			_, exps := splitBuild(part)
			mine := slices.Equal(exps, map[bool][]string{false: {}, true: {"goexperiment.simd"}}[h.simd])
			switch {
			case !kept && live[k]:
				fail("%q is looked up by a build, and was dropped", k)
			case !kept && !mine:
				fail("%q is another experiment set's, and was dropped", k)
			case kept && mine && !live[k]:
				fail("%q is the run's experiment set's, looked up by no build, and was kept", k)
			}
		}
	}
}
