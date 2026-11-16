package mygo

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var starsSeed = flag.Int("stars.seed", 0, "run TestStarsGuiOnTheDesktop on this seed only")

// The texts a stars script types into the filter or a cell, and its
// columns (as cmd/ergo's TestStarsGuiAgreesWithControl draws them, less
// the invalid UTF-8: a window's text input carries text, not bytes).
var (
	starsTyped  = []string{"o", "r", "a", "Ω", "ω", "ä", "İ", "i", " ", "2", "ve", "LY", "z"}
	starsDrafts = []string{"1.5", "-0.25", "30.01", "-30", "abc", "05h30m", "24h00m", "7", "999", "1000", "12345", " ", "0.0", "-", "1.", ".5", "Nova", "Ωmega", "90.1", "-90.0"}
	starsCols   = []string{"name", "constellation", "magnitude", "distance", "class", "ascension", "declination", "planets"}
)

// starsScript is a seed's script of the example's commands: moves of up to
// 50 rows (each row is a key press, so a pass of the window), the page
// keys and the ends, sorts, filters typed and taken back, selections,
// edits each column takes and refuses, and lines that are no command.
func starsScript(r *rand.Rand) string {
	var b strings.Builder
	for range 20 + r.IntN(60) {
		switch k := r.IntN(30); {
		case k < 5:
			b.WriteString("show\n")
		case k < 8:
			fmt.Fprintf(&b, "%s %d\n", pick(r, "down", "up"), []int{0, 1, 2, 7, 50}[r.IntN(5)])
		case k < 10:
			b.WriteString(pick(r, "pagedown", "pageup", "home", "end") + "\n")
		case k < 12:
			b.WriteString("sort " + pick(r, starsCols...) + "\n")
		case k < 14:
			b.WriteString("filter\n")
		case k < 18:
			if r.IntN(2) == 0 {
				b.WriteString("type " + pick(r, starsTyped...) + "\n")
			} else {
				b.WriteString("type " + pick(r, starsDrafts...) + "\n")
			}
		case k < 20:
			b.WriteString("backspace\n")
		case k < 22:
			b.WriteString(pick(r, "enter", "escape") + "\n")
		case k < 25:
			b.WriteString(pick(r, "mark", "extend") + "\n")
		case k < 28:
			b.WriteString("edit " + pick(r, starsCols...) + "\n")
		default:
			b.WriteString(pick(r, "", "# a note", "fly", "sort up", "down x", "show me", "edit") + "\n")
		}
	}
	b.WriteString("show\n")
	return b.String()
}

// buildStars builds examples/stars-gui's own binary (its scene runs the
// view on ui.TestHost), its window binary (cmd/stars-gui) and the
// MyGo-direct control (controls/stars-gui).
var buildStars = sync.OnceValues(func() ([3]string, error) {
	ergo, err := builtErgo()
	if err != nil {
		return [3]string{}, err
	}
	dir := filepath.Dir(ergo)
	bins := [3]string{filepath.Join(dir, "stars-testhost"), filepath.Join(dir, "stars-desktop"), filepath.Join(dir, "stars-control")}
	if o, err := bounded(ergo, "build", "../../examples/stars-gui/cmd/stars-gui", "-o", bins[0]).CombinedOutput(); err != nil {
		return bins, fmt.Errorf("ergo build examples/stars-gui: %v\n%s", err, o)
	}
	if o, err := bounded(ergo, "build", "./cmd/stars-gui", "-o", bins[1]).CombinedOutput(); err != nil {
		return bins, fmt.Errorf("ergo build cmd/stars-gui: %v\n%s", err, o)
	}
	if o, err := bounded("go", "build", "-o", bins[2], "./controls/stars-gui").CombinedOutput(); err != nil {
		return bins, fmt.Errorf("go build controls/stars-gui: %v\n%s", err, o)
	}
	return bins, nil
})

// TestStarsGuiOnTheDesktop is GP1c's agreement: for seeded catalogues
// (`stars-gui gen`, 0 to 2,000 stars) and scripts, windows of 1 to 12
// rows, the star table's window binary (cmd/stars-gui -headless: the
// example's view on the desktop driver, through a headless MyGo window)
// prints what the example's own `stars-gui scene` prints (the same view
// on ui.TestHost, which cmd/ergo's TestStarsGuiAgreesWithControl holds to
// its reference model), and so does the MyGo-direct control
// (controls/stars-gui -headless: the hand-Go model, its window on MyGo's
// widgets laid out and painted by ui.Tester after each line). The window binary's steps are pixels: a key after
// a click on the list, a click on each button, a click on the field and
// typed text, the wheel to follow the cursor; its rows are the ones the
// window's viewport wrote (ui.Rows). A failing seed prints the first line
// that differs and its replay command.
//
// It builds and runs Go binaries because the window binary is Ergo source
// that calls Go (go.Foreign): no in-process check runs it.
func TestStarsGuiOnTheDesktop(t *testing.T) {
	bins, err := buildStars()
	if err != nil {
		t.Fatal(err)
	}
	seeds := []int{}
	if *starsSeed != 0 {
		seeds = append(seeds, *starsSeed)
	} else {
		for s := 1; s <= 24; s++ {
			seeds = append(seeds, s)
		}
	}
	dir := t.TempDir()
	cat := filepath.Join(dir, "cat.tsv")
	failed, lines := 0, 0
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(uint64(seed), 0x57a2c))
		n := []int{0, 1, 5, 30, 200, 2000}[r.IntN(6)]
		text, err := play(bins[0], "", "gen", "-seed", fmt.Sprint(seed), "-n", fmt.Sprint(n))
		if err != nil {
			t.Fatalf("stars-gui gen: %v\n%s", err, text)
		}
		if err := os.WriteFile(cat, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		height := fmt.Sprint(1 + r.IntN(12))
		script := starsScript(r)
		want, err := play(bins[0], script, "scene", "-rows", height, cat)
		if err != nil {
			t.Fatalf("stars-gui scene, seed %d: %v\n%s", seed, err, want)
		}
		lines += strings.Count(want, "\n")
		for _, leg := range [][2]string{{"desktop", bins[1]}, {"control", bins[2]}} {
			got, err := play(leg[1], script, "-headless", "-rows", height, cat)
			if err == nil && got == want {
				continue
			}
			failed++
			if failed <= 3 {
				t.Errorf("seed %d on the %s (%d stars, %s rows): err %v, %s\nscript:\n%s\nreplay: go test -run TestStarsGuiOnTheDesktop -stars.seed=%d ./hosts/mygo", seed, leg[0], n, height, err, firstDiff(want, got), script, seed)
			}
		}
	}
	if failed > 3 {
		t.Errorf("%d more seeds differ", failed-3)
	}
	t.Logf("%d seeds, %d lines agree on both legs", len(seeds), lines)
}
