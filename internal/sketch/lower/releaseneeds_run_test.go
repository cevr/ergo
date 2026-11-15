package lower

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os/exec"
	"testing"

	"ergo.local/prototype/internal/sketch/sketchtest"
)

// TestReleaseNeedsRun is the run half of the release-needs property
// (resources R5; check/releaseneeds.go; check's TestReleaseNeedsModel is
// the check half). It takes the generated programs (sketchtest.RelNeeds)
// that check and that release a resource whose Close needs a capability,
// links them under the reference lowerings, and runs each: none may end in
// a defect, which a release that reaches a nil service is (the OWGEN r3
// gap: a Close's capability that no row or with provides). A failing seed
// prints the Ergo source and the program's stderr.
//
// It builds Go because its subject is what a release reaches at run time,
// in the environment of the frame that runs it: go/types cannot run the
// program, and the evaluator runs no release.
//
// Mutants it kills that no other test does (ledger: releaseneeds.go): no
// second check, so a program whose main misses a release's need checks
// and its release reaches a nil service.
func TestReleaseNeedsRun(t *testing.T) {
	needsBuild(t)
	restore := referenceLowerings()
	defer restore()
	type runCase struct {
		seed uint64
		name string
	}
	var cases []runCase
	var programs []*lowered
	srcs := map[string]string{}
	for seed := uint64(1); seed <= 400 && len(cases) < 12; seed++ {
		g := sketchtest.NewRelNeeds(rand.New(rand.NewPCG(seed, 0x5e1ea5e)))
		if !g.Releases() {
			continue
		}
		name := fmt.Sprintf("relneeds_%d", seed)
		o := lowerSource(name, g.Source(), true)
		if o.err != nil || len(o.diags) > 0 || o.panicked != nil || len(o.refused) > 0 {
			continue
		}
		cases = append(cases, runCase{seed, name})
		programs = append(programs, o)
		srcs[name] = g.Source()
	}
	if len(cases) < 12 {
		t.Fatalf("only %d generated programs check and release a resource that needs a capability", len(cases))
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		return
	}
	for _, c := range cases {
		cmd := exec.Command(exes[c.name])
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := runProgram(t, cmd); err != nil {
			t.Errorf("seed %d (the property's seed; check: ERGO_SEED=%d go test -run TestReleaseNeedsModel ./internal/sketch/check): %v\n%s\nstderr:\n%s", c.seed, c.seed, err, numbered(srcs[c.name]), stderr.String())
		}
	}
}
