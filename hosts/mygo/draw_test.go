package mygo

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

var drawSeed = flag.Int("draw.seed", 0, "run TestDrawGuiOnTheDesktop on this seed only")

// drawPicture is a seed's picture file: absent, malformed, or shapes of
// every kind on every layer near the screen (as cmd/ergo's
// TestDrawGuiAgreesWithControl draws them).
func drawPicture(r *rand.Rand) (string, bool) {
	switch r.IntN(12) {
	case 0:
		return "", false
	case 1:
		return "hello\n", true
	}
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="-10000 -10000 20000 20000">` + "\n")
	id := 0
	for layer := 0; layer < 4; layer++ {
		n := r.IntN(8)
		if n == 0 {
			continue
		}
		fmt.Fprintf(&b, "<g id=\"layer%d\">\n", layer)
		for ; n > 0; n-- {
			id += 1 + r.IntN(3)
			x, y := r.IntN(900)-50, r.IntN(700)-50
			switch r.IntN(4) {
			case 0:
				pts := []string{fmt.Sprintf("%d,%d", x, y)}
				for range r.IntN(6) {
					x, y = x+r.IntN(41)-20, y+r.IntN(41)-20
					pts = append(pts, fmt.Sprintf("%d,%d", x, y))
				}
				fmt.Fprintf(&b, "<polyline data-id=\"%d\" points=\"%s\"/>\n", id, strings.Join(pts, " "))
			case 1:
				fmt.Fprintf(&b, "<rect data-id=\"%d\" x=\"%d\" y=\"%d\" width=\"%d\" height=\"%d\"/>\n", id, x, y, r.IntN(120), r.IntN(80))
			case 2:
				fmt.Fprintf(&b, "<ellipse data-id=\"%d\" cx=\"%d\" cy=\"%d\" rx=\"%d\" ry=\"%d\"/>\n", id, x, y, r.IntN(120), r.IntN(80))
			case 3:
				fmt.Fprintf(&b, "<line data-id=\"%d\" x1=\"%d\" y1=\"%d\" x2=\"%d\" y2=\"%d\"/>\n", id, x, y, r.IntN(900)-50, r.IntN(700)-50)
			}
		}
		b.WriteString("</g>\n")
	}
	b.WriteString("</svg>\n")
	return b.String(), true
}

// drawScript is a seed's script of the example's commands: presses on the
// picture (a person presses where the picture shows: 800 by 600 from its
// corner) and on the shapes near its middle, moves and releases anywhere,
// past the picture's edges too, escapes in a drag, every tool, layers,
// zooms and pans with and without a button, deletes, hits, exports and
// saves, and lines that are no command.
func drawScript(r *rand.Rand) string {
	var b strings.Builder
	in := func() string {
		return fmt.Sprintf("%d %d", min(max([]int{0, 5, 100, 250, 400, 401, 799}[r.IntN(7)]+r.IntN(9)-4, 0), 799), min(max([]int{0, 7, 100, 300, 599}[r.IntN(5)]+r.IntN(9)-4, 0), 599))
	}
	anywhere := func() string {
		return fmt.Sprintf("%d %d", []int{0, 5, 100, 250, 400, 401, 799, 800, 1200, -30}[r.IntN(10)]+r.IntN(9)-4, []int{0, 7, 100, 300, 599, 600, -10}[r.IntN(7)]+r.IntN(9)-4)
	}
	for i := 10 + r.IntN(80); i > 0; i-- {
		switch k := r.IntN(40); {
		case k < 5:
			b.WriteString("show\n")
		case k < 9:
			b.WriteString("tool " + pick(r, "pen", "rect", "ellipse", "line", "select", "select") + "\n")
		case k < 20:
			b.WriteString("down " + in() + "\n")
			for range r.IntN(8) {
				b.WriteString("move " + anywhere() + "\n")
			}
			if r.IntN(6) == 0 {
				b.WriteString("escape\n")
			}
			b.WriteString("up " + anywhere() + "\n")
		case k < 23:
			b.WriteString("hit " + anywhere() + "\n")
		case k < 25:
			b.WriteString(pick(r, "delete", "escape") + "\n")
		case k < 27:
			fmt.Fprintf(&b, "layer %d\n", r.IntN(6)-1)
		case k < 29:
			fmt.Fprintf(&b, "pan %d %d\n", pick(r, 0, 100, -100, 50, 9999), pick(r, 0, 100, -100, -9999))
		case k < 31:
			fmt.Fprintf(&b, "zoom %d\n", pick(r, 25, 50, 100, 200, 400, 133, 24))
		case k < 33:
			b.WriteString(pick(r, "export", "save") + "\n")
		case k < 34:
			b.WriteString("move " + anywhere() + "\n")
		default:
			b.WriteString(pick(r, "", "# note", "fly", "down 1", "down 1 x", "zoom", "tool brush", "show me", "down 10001 0", "hit  1 2", "tool") + "\n")
		}
	}
	b.WriteString("show\nexport\n")
	return b.String()
}

// buildDraw builds examples/draw-gui's own binary (its scene runs the view
// on ui.TestHost), its window binary (cmd/draw-gui) and the MyGo-direct
// control (controls/draw-gui).
var buildDraw = sync.OnceValues(func() ([3]string, error) {
	ergo, err := builtErgo()
	if err != nil {
		return [3]string{}, err
	}
	dir := filepath.Dir(ergo)
	bins := [3]string{filepath.Join(dir, "draw-testhost"), filepath.Join(dir, "draw-desktop"), filepath.Join(dir, "draw-control")}
	if o, err := bounded(ergo, "build", "../../examples/draw-gui/cmd/draw-gui", "-o", bins[0]).CombinedOutput(); err != nil {
		return bins, fmt.Errorf("ergo build examples/draw-gui: %v\n%s", err, o)
	}
	if o, err := bounded(ergo, "build", "./cmd/draw-gui", "-o", bins[1]).CombinedOutput(); err != nil {
		return bins, fmt.Errorf("ergo build cmd/draw-gui: %v\n%s", err, o)
	}
	if o, err := bounded("go", "build", "-o", bins[2], "./controls/draw-gui").CombinedOutput(); err != nil {
		return bins, fmt.Errorf("go build controls/draw-gui: %v\n%s", err, o)
	}
	return bins, nil
})

// drawRun runs bin with args in a new folder that holds pic.svg when
// exists, on script; it is what bin printed and what pic.svg holds after
// (absent: ok false).
func drawRun(t *testing.T, bin, pic string, exists bool, script string, args ...string) (out string, err error, file string, ok bool) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pic.svg")
	if exists {
		if err := os.WriteFile(path, []byte(pic), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := bounded(bin, append(args, "pic.svg")...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(script)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	data, rerr := os.ReadFile(path)
	out = stdout.String()
	if err != nil {
		// A failed run's message is part of what the legs agree on.
		return out + stderr.String(), err, string(data), rerr == nil
	}
	if rest := withoutSpillWarning(stderr.String()); rest != "" {
		t.Errorf("%s %v wrote to standard error on a run that passed:\n%s", filepath.Base(bin), args, rest)
	}
	return out, err, string(data), rerr == nil
}

// spillWarning is the warning MyGo's ui package logs once when a frame's
// paths outgrow its mask atlas (ui/path_spill.go): the MyGo-direct control
// draws on MyGo's widgets and may print it, the Ergo legs never do. It is
// a note about how the control drew, not part of what it prints.
var spillWarning = regexp.MustCompile(`(?m)^\d{4}/\d\d/\d\d \d\d:\d\d:\d\d mygo: drawing paths on the CPU: the mask atlas has no room for their masks, as for many large paths that change every frame; opaque StrokePolyline lines draw without masks on a GPU\n`)

// withoutSpillWarning is stderr without the one spill warning a run may
// log; anything else on a passing run's stderr fails the test.
func withoutSpillWarning(stderr string) string {
	return spillWarning.ReplaceAllString(stderr, "")
}

// TestDrawGuiOnTheDesktop is GP3c's agreement: for seeded pictures and
// scripts, the drawing app's window binary (cmd/draw-gui -headless
// -check: the example's view on the desktop driver, through a headless
// MyGo window, its kept layout held to the rebuilt one) prints what the
// example's own `draw-gui scene` prints (the same view on ui.TestHost,
// which cmd/ergo's TestDrawGuiAgreesWithControl holds to its reference
// model) and leaves the same file, and so does the MyGo-direct control
// (controls/draw-gui -headless: the hand-Go model, its window on MyGo's
// widgets and Painter laid out and painted by ui.Tester after each line).
// The window binary's steps are pixels
// and keys: the pointer pressed on the picture, moved and released
// anywhere in the window or past its edge, the toolbar's buttons
// clicked, Escape pressed. A failing seed prints the first line that
// differs and its replay command.
//
// It builds and runs Go binaries because the window binary is Ergo source
// that calls Go (go.Foreign): no in-process check runs it.
func TestDrawGuiOnTheDesktop(t *testing.T) {
	bins, err := buildDraw()
	if err != nil {
		t.Fatal(err)
	}
	seeds := []int{}
	if *drawSeed != 0 {
		seeds = append(seeds, *drawSeed)
	} else {
		for s := 1; s <= 40; s++ {
			seeds = append(seeds, s)
		}
	}
	failed, lines := 0, 0
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(uint64(seed), 0xd7a3c))
		pic, exists := drawPicture(r)
		script := drawScript(r)
		want, werr, wantFile, wantOK := drawRun(t, bins[0], pic, exists, script, "scene")
		if werr != nil && pic != "hello\n" {
			t.Fatalf("draw-gui scene, seed %d: %v\n%s", seed, werr, want)
		}
		lines += strings.Count(want, "\n")
		for _, leg := range []struct {
			name string
			args []string
		}{{"desktop", []string{bins[1], "-headless", "-check"}}, {"control", []string{bins[2], "-headless"}}} {
			got, err, file, ok := drawRun(t, leg.args[0], pic, exists, script, leg.args[1:]...)
			var why string
			switch {
			case (err != nil) != (werr != nil) || got != want:
				why = fmt.Sprintf("err %v, %s", err, firstDiff(want, got))
			case ok != wantOK || file != wantFile:
				why = fmt.Sprintf("the file is %q (%v), the scene's %q (%v)", file, ok, wantFile, wantOK)
			default:
				continue
			}
			failed++
			if failed <= 3 {
				t.Errorf("seed %d on the %s: %s\nscript:\n%s\nreplay: go test -run TestDrawGuiOnTheDesktop -draw.seed=%d ./hosts/mygo", seed, leg.name, why, script, seed)
			}
		}
	}
	if failed > 3 {
		t.Errorf("%d more seeds differ", failed-3)
	}
	t.Logf("%d seeds, %d lines agree on both legs", len(seeds), lines)
}
