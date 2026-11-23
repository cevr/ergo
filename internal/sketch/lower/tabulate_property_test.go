package lower

import (
	"flag"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower/tabgen"
)

var tabSeed = flag.Uint64("tab.seed", 0, "run TestTabulatedAgree on this seed alone")

// tabDecl matches the flags of a table of a generated function gJ.
var tabFlags = regexp.MustCompile(`(?m)^var tabg([0-9]+)K `)

// TestTabulatedAgree is the seeded property of tabulated decisions
// (tabulate.go): each seed writes functions of one or two parameters over
// enums, bools, bytes and ints, deciding or not, with results of every
// kind, and a main that calls each twice on each argument of its domain
// (a slice result is written by its caller between the calls), and on
// even seeds ends in a defect raised inside a tabulated function. The
// lowering tables only functions the generator's model admits (finite
// parameters, a decision, a result with no reference), at least one per
// seed, knownStats.Tabulated counts them, the reference (tabLowering
// "call") tables none, and the two programs print the same, exit alike
// and report a defect alike. A failure prints the seed, the Ergo source
// and both outputs.
// Replay: go test -run '^TestTabulatedAgree$' ./internal/sketch/lower -args -tab.seed=N
//
// It builds Go because the question is what the tables return at run
// time (the first call's fill, the second's read, the defect); no
// in-process check observes it, and Ergo has no reference interpreter yet
// (test-speed T8).
func TestTabulatedAgree(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *tabSeed != 0 {
		seeds = []uint64{*tabSeed}
	}
	reached := map[string]bool{}
	var programs []*lowered
	sources := map[uint64]string{}
	defects := map[uint64]bool{}
	for _, seed := range seeds {
		src, eligible, defect := tabgen.Program(seed, reached)
		sources[seed], defects[seed] = src, defect
		for _, mode := range []string{"", "call"} {
			var made int
			saved, savedHook := tabLowering, statsHook
			tabLowering = mode
			statsHook = func(s knownStats) { made = s.Tabulated }
			p := mustLower(t, fmt.Sprintf("tabulated%d%s", seed, mode), src)
			tabLowering, statsHook = saved, savedHook
			var tabled []int
			var all int
			for _, f := range p.files {
				all += strings.Count(string(f.Data), ".MemoFlag\n")
				for _, m := range tabFlags.FindAllSubmatch(f.Data, -1) {
					var j int
					fmt.Sscan(string(m[1]), &j)
					tabled = append(tabled, j)
				}
			}
			if made != all {
				t.Errorf("seed %d, lowering %q: knownStats.Tabulated %d, %d tables written", seed, mode, made, all)
			}
			if mode == "call" {
				if all != 0 {
					t.Errorf("seed %d: the reference writes %d tables", seed, all)
				}
			} else {
				if len(tabled) == 0 {
					t.Errorf("seed %d: no generated function is tabulated\n%s", seed, src)
				}
				for _, j := range tabled {
					if !eligible[j] {
						t.Errorf("seed %d: g%d is tabulated, which the model refuses\n%s", seed, j, src)
						continue
					}
					for _, k := range strings.Split(strings.Join(tabParamsOf(src, j), ","), ",") {
						reached["tabulated "+k] = true
					}
				}
			}
			programs = append(programs, p)
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the tabulated programs do not build (seeds %v)", seeds)
	}
	for _, seed := range seeds {
		on, onErr, onCode := runExe(t, exes[fmt.Sprintf("tabulated%d", seed)])
		off, offErr, offCode := runExe(t, exes[fmt.Sprintf("tabulated%dcall", seed)])
		ended := (onCode != 0) == defects[seed] && strings.Contains(on, "g0 ")
		if on != off || !sameReport(onErr, offErr) || onCode != offCode || !ended {
			t.Errorf("seed %d: the tables and the reference differ (or the program did not end as written)\nReplay: go test -run '^TestTabulatedAgree$' ./internal/sketch/lower -args -tab.seed=%d\n%s\n--- tables (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s", seed, seed, sources[seed], onCode, on, onErr, offCode, off, offErr)
		}
	}
	if *tabSeed == 0 {
		var missing []string
		for _, k := range []string{"tabulated E0", "tabulated bool", "tabulated byte", "no decision", "int parameter", "slice result", "call of an earlier function", "call in a record literal and an interpolation", "dropped call", "defect"} {
			if !reached[k] {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			t.Errorf("the fixed seeds do not reach %v", missing)
		}
	}
}

// tabParamsOf is gJ's parameter kinds, read from its declaration in src.
func tabParamsOf(src string, j int) []string {
	head := fmt.Sprintf("func g%d(", j)
	i := strings.Index(src, head)
	if i < 0 {
		return nil
	}
	rest := src[i+len(head):]
	rest = rest[:strings.Index(rest, ")")]
	var out []string
	for _, p := range strings.Split(rest, ", ") {
		if f := strings.Fields(p); len(f) == 2 {
			out = append(out, f[1])
		}
	}
	return out
}
