package mygo

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var flowSeed = flag.Int("flow.seed", 0, "run TestFkFlowControlAgrees on this seed only")

// flowScript is a seed's fk-flow script: frames of drawn lengths and
// repeats (a long one capped at 50 ms), presses and moves on and off the
// canvas, clicks on each button and on none, both sliders pressed in and
// out of their range, and, now and then, a line the host cannot read,
// which ends the script with status 1.
func flowScript(r *rand.Rand) string {
	var b strings.Builder
	fmt.Fprintf(&b, "seed %d\n", r.IntN(1000))
	if r.IntN(2) == 0 {
		b.WriteString("click Flow\n")
	}
	for i := 6 + r.IntN(20); i > 0; i-- {
		switch k := r.IntN(24); {
		case k < 7:
			fmt.Fprintf(&b, "frame %d x%d\n", pick(r, 16, 16, 33, 50, 100, 1000), 1+r.IntN(40))
		case k < 11:
			fmt.Fprintf(&b, "press %.1f %.1f\n", r.Float64()*1000-20, r.Float64()*640-20)
		case k < 13:
			fmt.Fprintf(&b, "move %.1f %.1f\n", r.Float64()*1000-20, r.Float64()*640-20)
		case k < 19:
			fmt.Fprintf(&b, "click %s\n", pick(r, "Balls", "Flow", "Pause", "Play", "Clear", "Reset", "Nothing"))
		case k < 21:
			fmt.Fprintf(&b, "%s %.2f\n", pick(r, "strength", "scale"), r.Float64()*4-0.5)
		case k < 23:
			b.WriteString(pick(r, "", "# a note", "frame 16") + "\n")
		default:
			b.WriteString(pick(r, "frame 0", "frame 16 x0", "press 1", "click", "fly", "seed 4", "scale x") + "\n")
		}
	}
	return b.String()
}

// buildFlow builds examples/fk-flow's own binary (the view on ui.TestHost)
// and the MyGo-direct control (controls/fk-flow).
var buildFlow = sync.OnceValues(func() ([2]string, error) {
	ergo, err := builtErgo()
	if err != nil {
		return [2]string{}, err
	}
	dir := filepath.Dir(ergo)
	bins := [2]string{filepath.Join(dir, "flow-testhost"), filepath.Join(dir, "flow-control")}
	if o, err := bounded(ergo, "build", "../../examples/fk-flow/cmd/fk-flow", "-o", bins[0]).CombinedOutput(); err != nil {
		return bins, fmt.Errorf("ergo build examples/fk-flow: %v\n%s", err, o)
	}
	if o, err := bounded("go", "build", "-o", bins[1], "./controls/fk-flow").CombinedOutput(); err != nil {
		return bins, fmt.Errorf("go build controls/fk-flow: %v\n%s", err, o)
	}
	return bins, nil
})

// exit is a run's exit status, 0 when it ran to its end.
func exit(err error) int {
	var e *exec.ExitError
	if errors.As(err, &e) {
		return e.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}

// TestFkFlowControlAgrees is FK12's agreement (plan §18.3: the one perf
// claim of the Foldkit ports has a control): on seeded scripts the
// MyGo-direct control (controls/fk-flow -headless: the hand-Go model, its
// window on MyGo's widgets, the canvas painted with MyGo's Painter by
// ui.Tester at each frame and act) prints what examples/fk-flow prints
// (the Ergo model and view on ui.TestHost), with the same exit status. The
// control's model is examples/fk-flow/testdata/control's, which
// scripts/opcount measures the program against, so the two copies must be
// the same file. A failing seed prints the first line that differs and its
// replay command.
//
// It builds and runs Go binaries because the control is a MyGo program
// and the Ergo side's output is its binary's: no in-process check runs
// either.
func TestFkFlowControlAgrees(t *testing.T) {
	ours, err := os.ReadFile("controls/fk-flow/model.go")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := os.ReadFile("../../examples/fk-flow/testdata/control/model.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ours, theirs) {
		t.Fatal("controls/fk-flow/model.go is not examples/fk-flow/testdata/control/model.go: copy it again")
	}
	bins, err := buildFlow()
	if err != nil {
		t.Fatal(err)
	}
	seeds := []int{}
	if *flowSeed != 0 {
		seeds = append(seeds, *flowSeed)
	} else {
		for s := 1; s <= 24; s++ {
			seeds = append(seeds, s)
		}
	}
	failed, lines := 0, 0
	for _, seed := range seeds {
		script := flowScript(rand.New(rand.NewPCG(uint64(seed), 0xf10)))
		want, werr := play(bins[0], script)
		got, gerr := play(bins[1], script, "-headless")
		lines += strings.Count(want, "\n")
		if exit(werr) == exit(gerr) && exit(werr) >= 0 && got == want {
			continue
		}
		failed++
		if failed <= 3 {
			t.Errorf("seed %d: status %d and %d, %s\nscript:\n%s\nreplay: go test -run TestFkFlowControlAgrees -flow.seed=%d ./hosts/mygo", seed, exit(werr), exit(gerr), firstDiff(want, got), script, seed)
		}
	}
	if failed > 3 {
		t.Errorf("%d more seeds differ", failed-3)
	}
	t.Logf("%d seeds, %d lines agree", len(seeds), lines)
}
