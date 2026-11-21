package mygo

import (
	"strconv"
	"strings"
	"testing"
)

// TestDesktopDrawsOnlyWhatChanged is the damage paint's growth guard: on
// the shelf app (every row a node, in a Scroll), adding one row after N
// rows draws the same number of nodes anew (desktop.Drawn) at N = 10 and
// N = 40: the new row, the count above the list, and their ancestors; every
// other node takes its last draw again. It kills the mutants that draw
// every node at every paint (hosts/mygo/testdata/mutants.json, GPd-*);
// hostagree's check holds each damage paint to the full one, list and
// spots, at every paint.
//
// It builds and runs a Go binary because the driver is Ergo source that
// calls Go (go.Foreign): hostagree is the program that runs it.
func TestDesktopDrawsOnlyWhatChanged(t *testing.T) {
	bin, err := buildHostAgree()
	if err != nil {
		t.Fatal(err)
	}
	drawn := map[int]int{}
	for _, n := range []int{10, 40} {
		script := "shelf\n" + strings.Repeat("tap add\n", n) + "drawn\ntap add\ndrawn\n"
		cmd := bounded(bin)
		cmd.Stdin = strings.NewReader(script)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("n=%d: %v\n%s", n, err, out)
		}
		var counts []int
		for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			switch {
			case strings.HasPrefix(line, "ok "), line == "end":
			case strings.HasPrefix(line, "drawn "):
				k, err := strconv.Atoi(strings.TrimPrefix(line, "drawn "))
				if err != nil {
					t.Fatalf("n=%d: %q", n, line)
				}
				counts = append(counts, k)
			default:
				t.Fatalf("n=%d: %s", n, line)
			}
		}
		if len(counts) != 2 {
			t.Fatalf("n=%d: %d drawn lines, want 2:\n%s", n, len(counts), out)
		}
		drawn[n] = counts[1]
	}
	if drawn[10] != drawn[40] {
		t.Errorf("adding a row drew %d nodes after 10 rows and %d after 40: the paint grows with the tree", drawn[10], drawn[40])
	}
	// 40 rows are 160 nodes; the row, the count and their ancestors are few.
	if drawn[40] >= 20 {
		t.Errorf("adding a row drew %d nodes", drawn[40])
	}
	t.Logf("adding a row draws %d nodes at 10 and at 40 rows", drawn[40])
}
