package opentui

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

var dashSeed = flag.Int("dash.seed", 0, "run TestDashOnTheTerminal on this seed only")

// dashScript is a seed's scene script, as hosts/mygo's TestDashGuiOnTheDesktop
// writes them: runs of frames (long enough for a silent source to go
// stale and come back), presses of each panel's pause and policy buttons
// and of labels on no button, the frame's text, and lines that are no
// command.
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

// TestDashOnTheTerminal: for seeded sensors, frames before the script and
// scene scripts, the dashboard's terminal binary (cmd/dash-tui -headless:
// the example's view on the terminal driver through a headless screen,
// its frames the driver's Ticks, its taps mouse bytes at the buttons)
// prints what the example's own `dash-gui scene` prints (the same view on
// ui.TestHost, its clock moved a frame at a time). A failing seed prints
// the first line that differs and its replay command.
//
// It builds and runs Go binaries because the terminal binary is Ergo
// source that calls Go (go.Foreign): no in-process check runs it.
func TestDashOnTheTerminal(t *testing.T) {
	own, err := build("dash-gui", "../../examples/dash-gui/cmd/dash-gui")
	if err != nil {
		t.Fatal(err)
	}
	term, err := build("dash-tui", "./cmd/dash-tui")
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
		want, err := play(own, script, append([]string{"scene"}, args...)...)
		if err != nil {
			t.Fatalf("dash-gui scene, seed %d: %v\n%s", seed, err, want)
		}
		lines += strings.Count(want, "\n")
		got, err := play(term, script, append([]string{"-headless"}, args...)...)
		if err == nil && got == want {
			continue
		}
		failed++
		if failed <= 3 {
			t.Errorf("seed %d (%s): err %v, %s\nscript:\n%s\nreplay: go test -run TestDashOnTheTerminal -dash.seed=%d .", seed, strings.Join(args, " "), err, firstDiff(want, got), script, seed)
		}
	}
	if failed > 3 {
		t.Errorf("%d more seeds differ", failed-3)
	}
	t.Logf("%d seeds, %d lines agree", len(seeds), lines)
}
