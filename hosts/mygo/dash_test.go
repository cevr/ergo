package mygo

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var dashSeed = flag.Int("dash.seed", 0, "run TestDashGuiOnTheDesktop on this seed only")

// dashScript is a seed's scene script: runs of frames (long enough for a
// silent source to go stale and come back), presses of each panel's pause
// and policy buttons and of labels on no button, the frame's text, and
// lines that are no command.
func dashScript(r *rand.Rand) string {
	var b strings.Builder
	for i := 4 + r.IntN(16); i > 0; i-- {
		switch k := r.IntN(20); {
		case k < 6:
			fmt.Fprintf(&b, "frames %d\n", pick(r, 0, 1, 2, 7, 30, 61, 90))
		case k < 10:
			fmt.Fprintf(&b, "tap toggle %d\n", r.IntN(14)-1)
		case k < 13:
			fmt.Fprintf(&b, "tap policy %d\n", r.IntN(14)-1)
		case k < 17:
			b.WriteString("show\n")
		default:
			b.WriteString(pick(r, "", "# a note", "frames", "frames -1", "frames x", "tap", "tap nothing", "fly", "show me") + "\n")
		}
	}
	b.WriteString("show\n")
	return b.String()
}

// buildDash builds examples/dash-gui's own binary (its scene runs the view
// on ui.TestHost), its window binary (cmd/dash-gui) and the MyGo-direct
// control (controls/dash-gui).
var buildDash = sync.OnceValues(func() ([3]string, error) {
	ergo, err := builtErgo()
	if err != nil {
		return [3]string{}, err
	}
	dir := filepath.Dir(ergo)
	bins := [3]string{filepath.Join(dir, "dash-testhost"), filepath.Join(dir, "dash-desktop"), filepath.Join(dir, "dash-control")}
	if o, err := bounded(ergo, "build", "../../examples/dash-gui/cmd/dash-gui", "-o", bins[0]).CombinedOutput(); err != nil {
		return bins, fmt.Errorf("ergo build examples/dash-gui: %v\n%s", err, o)
	}
	if o, err := bounded(ergo, "build", "./cmd/dash-gui", "-o", bins[1]).CombinedOutput(); err != nil {
		return bins, fmt.Errorf("ergo build cmd/dash-gui: %v\n%s", err, o)
	}
	if o, err := bounded("go", "build", "-o", bins[2], "./controls/dash-gui").CombinedOutput(); err != nil {
		return bins, fmt.Errorf("go build controls/dash-gui: %v\n%s", err, o)
	}
	return bins, nil
})

// TestDashGuiOnTheDesktop is GP4c's agreement: for seeded sensors, frames
// before the script and scene scripts, the dashboard's window binary
// (cmd/dash-gui -headless -check: the example's view on the desktop
// driver, through a headless MyGo window whose display gives the frames,
// its kept layout held to the rebuilt one) prints what the example's own
// `dash-gui scene` prints (the same view on ui.TestHost, its clock moved a
// frame at a time, which cmd/ergo's TestDashGuiAgreesWithControl holds to
// its reference), and so does the MyGo-direct control (controls/dash-gui
// -headless: the hand-Go model, its window on MyGo's widgets and chart
// package laid out and painted by ui.Tester at each frame). The window
// binary's steps are the display's frames and
// clicks on the panels' buttons. A failing seed prints the first line
// that differs and its replay command.
//
// It builds and runs Go binaries because the window binary is Ergo source
// that calls Go (go.Foreign): no in-process check runs it.
func TestDashGuiOnTheDesktop(t *testing.T) {
	bins, err := buildDash()
	if err != nil {
		t.Fatal(err)
	}
	seeds := []int{}
	if *dashSeed != 0 {
		seeds = append(seeds, *dashSeed)
	} else {
		for s := 1; s <= 24; s++ {
			seeds = append(seeds, s)
		}
	}
	failed, lines := 0, 0
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(uint64(seed), 0xda54))
		args := []string{"-seed", fmt.Sprint(r.IntN(1000)), "-frames", fmt.Sprint(pick(r, 0, 1, 40, 70))}
		script := dashScript(r)
		want, err := play(bins[0], script, append([]string{"scene"}, args...)...)
		if err != nil {
			t.Fatalf("dash-gui scene, seed %d: %v\n%s", seed, err, want)
		}
		lines += strings.Count(want, "\n")
		for _, leg := range []struct {
			name string
			args []string
		}{{"desktop", []string{bins[1], "-headless", "-check"}}, {"control", []string{bins[2], "-headless"}}} {
			got, err := play(leg.args[0], script, append(leg.args[1:], args...)...)
			if err == nil && got == want {
				continue
			}
			failed++
			if failed <= 3 {
				t.Errorf("seed %d on the %s (%s): err %v, %s\nscript:\n%s\nreplay: go test -run TestDashGuiOnTheDesktop -dash.seed=%d ./hosts/mygo", seed, leg.name, strings.Join(args, " "), err, firstDiff(want, got), script, seed)
			}
		}
	}
	if failed > 3 {
		t.Errorf("%d more seeds differ", failed-3)
	}
	t.Logf("%d seeds, %d lines agree on both legs", len(seeds), lines)
}
