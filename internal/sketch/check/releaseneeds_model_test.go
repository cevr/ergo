package check

import (
	"fmt"
	"math/rand/v2"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/sketchtest"
)

// TestReleaseNeedsModel (resources R5; releaseneeds.go) is a seeded
// property of the release-needs rule: a release is charged where its value
// is acquired, as a call of its Close there would be. It generates
// packages (sketchtest.RelNeeds) over resource shapes (a Close that needs
// A, B, both or none, directly or through a record field or a slice
// element) × release points (a scope's end, an early return, a
// temporary's statement, a handoff into a taking callee, a handoff of a
// local) × providers (an inferred row, a written row, an exported
// function's empty row, a with that provides A, B or both), with calls
// between the functions (recursion included). The model's rows must equal
// each function's checked capability row, and its missing needs (line and
// capability) the checker's S102s; nothing else is reported. Every node
// the first check found is charged by the second (relHit), so no release
// escapes the charge. ERGO_SEED=<n> replays one seed; a failure prints the
// Ergo source and both results.
//
// Mutants it kills that no other test does (ledger: releaseneeds.go): no
// second check (a release charges nothing); a part's Close not walked
// (Box, []RA); a temporary not charged; a taken parameter not charged; a
// with's provision ignored by a release charge; a charge made only in the
// reporting pass, so an inferred row misses it.
func TestReleaseNeedsModel(t *testing.T) {
	seeds := make([]uint64, 0, 300)
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_SEED: %v", err)
		}
		seeds = append(seeds, n)
	} else {
		for i := range uint64(300) {
			seeds = append(seeds, i+1)
		}
	}
	needRe := regexp.MustCompile(`requires (\w+), which is not in the capability row`)
	for _, seed := range seeds {
		g := sketchtest.NewRelNeeds(rand.New(rand.NewPCG(seed, 0x5e1ea5e)))
		src := g.Source()
		wantRows, wantMissing := g.Model()
		c, diags := checkFor(src)
		var gotMissing, other []string
		for _, d := range diags {
			m := needRe.FindStringSubmatch(d.Message)
			if d.Code != CodeMissingCap || m == nil {
				other = append(other, d.String())
				continue
			}
			gotMissing = append(gotMissing, fmt.Sprintf("%d %s", d.Line, m[1]))
		}
		slices.Sort(gotMissing)
		gotMissing = slices.Compact(gotMissing)
		var gotRows []string
		if c != nil {
			for i := range g.Funcs {
				gotRows = append(gotRows, relRow(c, g.FuncName(i)))
			}
		}
		var unhit int
		if c != nil {
			for n := range c.relSeeds {
				if !c.relHit[n] {
					unhit++
				}
			}
		}
		if len(other) > 0 || unhit > 0 || !slices.Equal(gotRows, wantRows) || !slices.Equal(gotMissing, wantMissing) {
			var full []string
			for _, d := range diags {
				full = append(full, d.String())
			}
			t.Fatalf("seed %d (replay: ERGO_SEED=%d go test -run TestReleaseNeedsModel ./internal/sketch/check)\nsource:\n%s\nchecker rows: %q\nmodel rows:   %q\nchecker missing: %q\nmodel missing:   %q\nnodes not charged: %d\ndiagnostics:\n%s",
				seed, seed, ofNumbered(src), gotRows, wantRows, gotMissing, wantMissing, unhit, strings.Join(full, "\n"))
		}
	}
}

// relRow is the checked capability row of the package function name, its
// capabilities sorted and joined by " | ".
func relRow(c *checker, name string) string {
	for _, f := range c.funcs {
		if f.Name != name || f.owner != nil {
			continue
		}
		var caps []string
		for _, e := range c.types.resolve(c.funcSig(f).Uses).Elems() {
			caps = append(caps, strings.TrimPrefix(e.String(), "main."))
		}
		slices.Sort(caps)
		return strings.Join(caps, " | ")
	}
	return "?"
}
