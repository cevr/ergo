package opentui

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var starsSeed = flag.Int("stars.seed", 0, "run TestStarsOnTheTerminal on this seed only")

// The texts a stars script types into the filter or a cell, and its
// columns (as hosts/mygo's TestStarsGuiOnTheDesktop draws them).
var (
	starsTyped  = []string{"o", "r", "a", "Ω", "ω", "ä", "İ", "i", " ", "2", "ve", "LY", "z"}
	starsDrafts = []string{"1.5", "-0.25", "30.01", "-30", "abc", "05h30m", "24h00m", "7", "999", "1000", "12345", " ", "0.0", "-", "1.", ".5", "Nova", "Ωmega", "90.1", "-90.0"}
	starsCols   = []string{"name", "constellation", "magnitude", "distance", "class", "ascension", "declination", "planets"}
)

// starsScript is a seed's script of the example's commands: moves of up to
// 50 rows (each row a key press, so a pass of the view), the page keys and
// the ends, sorts, filters typed and taken back, selections, edits each
// column takes and refuses, and lines that are no command.
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

// TestStarsOnTheTerminal: for seeded catalogues (`stars-gui gen`, 0 to
// 2,000 stars) and scripts, table windows of 1 to 12 rows, the star
// table's terminal binary (cmd/stars-tui -headless: the example's view on
// the OpenTUI driver, through a headless screen) prints what the
// example's own `stars-gui scene` prints (the same view on ui.TestHost).
// The terminal's steps are terminal bytes: a key after a click on the
// list, a click on each button, a click on the field and typed text; the
// list scrolls to the table's window, and its rows are the ones the
// viewport it reported wrote (ui.Rows). A failing seed prints the first
// line that differs and its replay command.
//
// It builds and runs Go binaries because the terminal binary is Ergo
// source that calls Go (go.Foreign): no in-process check runs it.
func TestStarsOnTheTerminal(t *testing.T) {
	own, err := build("stars-gui", "../../examples/stars-gui/cmd/stars-gui")
	if err != nil {
		t.Fatal(err)
	}
	term, err := build("stars-tui", "./cmd/stars-tui")
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
	cat := filepath.Join(t.TempDir(), "cat.tsv")
	failed, lines := 0, 0
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(uint64(seed), 0x57a2c))
		n := []int{0, 1, 5, 30, 200, 2000}[r.IntN(6)]
		text, err := play(own, "", "gen", "-seed", strconv.Itoa(seed), "-n", strconv.Itoa(n))
		if err != nil {
			t.Fatalf("stars-gui gen: %v\n%s", err, text)
		}
		if err := os.WriteFile(cat, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		height := strconv.Itoa(1 + r.IntN(12))
		script := starsScript(r)
		want, err := play(own, script, "scene", "-rows", height, cat)
		if err != nil {
			t.Fatalf("stars-gui scene, seed %d: %v\n%s", seed, err, want)
		}
		lines += strings.Count(want, "\n")
		got, err := play(term, script, "-headless", "-rows", height, cat)
		if err == nil && got == want {
			continue
		}
		failed++
		if failed <= 3 {
			t.Errorf("seed %d (%d stars, %s rows): err %v, %s\nscript:\n%s\nreplay: go test -run TestStarsOnTheTerminal -stars.seed=%d", seed, n, height, err, firstDiff(want, got), script, seed)
		}
	}
	if failed > 3 {
		t.Errorf("%d more seeds differ", failed-3)
	}
	t.Logf("%d seeds, %d lines agree", len(seeds), lines)
}

// TestTuiKeepsItsScene is the growth guard of the scene (counted, not
// timed): adding one row to a list (the shelf app's Add, a pass that
// inserts one row) makes as many library calls (screen.Calls) when the
// list holds 10 rows as when it holds 40. A driver that rebuilt the scene
// on a pass, or walked it through the library, would make more.
//
// It builds and runs a Go binary (cmd/hostagree) because the driver is
// Ergo source that calls Go (go.Foreign): no in-process check runs it.
func TestTuiKeepsItsScene(t *testing.T) {
	bin, err := build("hostagree", "./cmd/hostagree")
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{"shelf"}
	for n := 1; n <= 40; n++ {
		step := "tap add"
		if n == 10 || n == 40 {
			step = "cost tap add"
		}
		lines = append(lines, step)
	}
	text, err := play(bin, strings.Join(lines, "\n")+"\n")
	if err != nil {
		t.Fatalf("hostagree: %v\n%s", err, text)
	}
	var costs []string
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "cost "):
			costs = append(costs, strings.TrimPrefix(line, "cost "))
		case strings.HasPrefix(line, "ok "), line == "end":
		default:
			t.Errorf("hostagree: %s", line)
		}
	}
	if len(costs) != 2 {
		t.Fatalf("want two costs, got %q\n%s", costs, text)
	}
	if costs[0] != costs[1] {
		t.Errorf("adding the 10th row made %s library calls, the 40th %s: a pass costs more as the list grows", costs[0], costs[1])
	}
	t.Logf("adding a row costs %s library calls at 10 rows and at 40", costs[0])
}
